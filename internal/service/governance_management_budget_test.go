package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/governance"
)

func TestGovernanceBudgetManagementRejectsChangedCheckLiteral(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	if _, err := f.base.db.Exec(`DROP TABLE governance_policies`); err != nil {
		t.Fatal(err)
	}
	badDDL := strings.Replace(governancePoliciesDDL, "'deny_unknown'", "'deny un known'", 1)
	if _, err := f.base.db.Exec(badDDL); err != nil {
		t.Fatal(err)
	}
	if err := f.manager.Migrate(context.Background()); !errors.Is(err, errGovernanceManagementSchema) {
		t.Fatalf("changed literal error=%v", err)
	}
}

func TestGovernanceBudgetManagementLegacyMigrationRollbackAndRetry(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	ctx := context.Background()
	for _, statement := range []string{
		`DROP TABLE governance_management_audit`, `DROP TABLE governance_management_operations`,
		`DROP TABLE governance_group_members`, `DROP TABLE governance_policies`, `DROP TABLE governance_groups`,
	} {
		if _, err := f.base.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{governanceGroupsDDL, governanceGroupMembersDDL, governanceGroupMembersIndexDDL,
		legacyGovernancePoliciesDDL, governanceOperationsDDL, governanceAuditDDL} {
		if _, err := f.base.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	stamp := formatGovernanceTime(governanceManagementTestTime)
	if _, err := f.base.db.Exec(`INSERT INTO governance_policies(id,scope_kind,scope_id,enabled,rpm_limit,revision,created_at,updated_at)
		VALUES('legacy-policy','employee','employee-one',1,9,1,?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	limit := int64(9)
	legacyInput := governancePolicyInput{ScopeKind: governance.ScopeEmployee, ScopeID: "employee-one", Enabled: true,
		Hard: governanceHardLimits{RPM: &limit}}
	digest, err := governancePayloadDigest(governancePolicyDigestInput("", 0, legacyInput))
	if err != nil {
		t.Fatal(err)
	}
	op := governanceOperationID(980)
	if _, err := f.base.db.Exec(`INSERT INTO governance_management_operations(operation_id,actor_id,action,payload_digest,resource_kind,resource_id,revision,created_at)
		VALUES(?,?,'policy.create',?,'policy','legacy-policy',1,?)`, op, "admin-one", digest, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.db.Exec(`INSERT INTO governance_management_audit(operation_id,actor_id,action,resource_kind,resource_id,revision,created_at)
		VALUES(?,?,'policy.create','policy','legacy-policy',1,?)`, op, "admin-one", stamp); err != nil {
		t.Fatal(err)
	}

	tx, _ := f.base.db.BeginTx(ctx, nil)
	if version, err := f.manager.schemaVersionTx(ctx, tx); err != nil || version != 1 {
		t.Fatalf("legacy version=%d err=%v", version, err)
	}
	if err := f.manager.MigrateTx(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx, _ = f.base.db.BeginTx(ctx, nil)
	if version, err := f.manager.schemaVersionTx(ctx, tx); err != nil || version != 1 {
		t.Fatalf("rollback version=%d err=%v", version, err)
	}
	tx.Rollback()
	if err := f.manager.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	policy, err := loadGovernancePolicy(ctx, f.base.db, "legacy-policy")
	if err != nil || policy.Budget.TPM != nil || policy.Budget.CostMicro != nil || policy.Budget.UnknownMode != "shadow" {
		t.Fatalf("migrated policy=%+v err=%v", policy, err)
	}
	var receipts int
	if err := f.base.db.QueryRow(`SELECT COUNT(*) FROM governance_management_operations WHERE operation_id=?`, op).Scan(&receipts); err != nil || receipts != 1 {
		t.Fatalf("receipt count=%d err=%v", receipts, err)
	}
	replayed, err := f.manager.createPolicy(ctx, "admin-one", op, legacyInput)
	if err != nil || replayed.ResourceID != "legacy-policy" || replayed.Revision != 1 {
		t.Fatalf("legacy receipt replay=%+v err=%v", replayed, err)
	}
}

func TestGovernanceBudgetSettingsPolicyAndSnapshot(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	ctx := context.Background()
	budgetEnabled := true
	first, err := f.manager.updateSettingsPatch(ctx, "admin-one", governanceOperationID(981), 1, true, &budgetEnabled)
	if err != nil || first.Revision != 2 {
		t.Fatalf("settings receipt=%+v err=%v", first, err)
	}
	replayed, err := f.manager.updateSettingsPatch(ctx, "admin-one", governanceOperationID(981), 1, true, &budgetEnabled)
	if err != nil || replayed != first {
		t.Fatalf("settings replay=%+v err=%v", replayed, err)
	}
	second, err := f.manager.updateSettings(ctx, "admin-one", governanceOperationID(982), 2, true)
	if err != nil || second.Revision != 3 {
		t.Fatalf("legacy settings receipt=%+v err=%v", second, err)
	}
	settings, err := f.core.Settings(ctx)
	if err != nil || !settings.BudgetEnabled {
		t.Fatalf("legacy update cleared budget settings=%+v err=%v", settings, err)
	}

	tpm, cost := int64(500), int64(900)
	currency, window := "USD", governance.ShadowWindowRolling24h
	created, err := f.manager.createPolicy(ctx, "admin-one", governanceOperationID(983), governancePolicyInput{
		ScopeKind: governance.ScopeEmployee, ScopeID: "employee-one", Enabled: true,
		Budget: &governanceBudgetLimits{TPM: &tpm, CostMicro: &cost, Currency: &currency, Window: &window, UnknownMode: "deny_unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// An older client omits budget while editing existing fields. The hard
	// budget remains unchanged and its old-shaped operation digest stays valid.
	if _, err := f.manager.updatePolicy(ctx, "admin-one", governanceOperationID(984), created.ResourceID, 1,
		governancePolicyInput{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	tx, _ := f.base.db.BeginTx(ctx, nil)
	resolvedSettings, scopes, err := f.manager.ResolveScopesTx(ctx, tx, "employee-one", "key-one")
	tx.Rollback()
	if err != nil || !resolvedSettings.BudgetEnabled || len(scopes) != 1 {
		t.Fatalf("resolved settings=%+v scopes=%+v err=%v", resolvedSettings, scopes, err)
	}
	if scopes[0].HardTPM == nil || *scopes[0].HardTPM != tpm || scopes[0].HardCostMicro == nil || *scopes[0].HardCostMicro != cost ||
		scopes[0].HardCurrency != currency || scopes[0].HardWindow != window || scopes[0].UnknownMode != "deny_unknown" {
		t.Fatalf("budget snapshot=%+v", scopes[0])
	}
}

func TestGovernanceBudgetHTTPStrictOptionalCompatibility(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	if err := createRootKey(f.dir); err != nil {
		t.Fatal(err)
	}
	secrets, err := loadSecrets(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	token, csrf := "budget-admin-token", "budget-csrf-token"
	if _, err := f.base.db.Exec(`INSERT INTO sessions(id,admin_id,token_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)`,
		"budget-session", "admin-one", secrets.digest("admin-session/v1", token), csrf,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), governanceManagementTestTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{}, store: f.base, secrets: secrets}
	mux := http.NewServeMux()
	f.manager.Register(app, mux)
	server := httptest.NewServer(requestMiddleware(mux))
	defer server.Close()
	cookie := &http.Cookie{Name: adminCookieName, Value: token}

	settingsBody := `{"operation_id":"` + governanceOperationID(985) + `","expected_revision":1,"enabled":true,"budget_enabled":true}`
	response := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/governance/settings", settingsBody, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", response.StatusCode, readBody(response))
	}
	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/governance/settings", "", cookie, "", "")
	var settings map[string]any
	decodeResponse(t, response, &settings)
	if settings["budget_enabled"] != true {
		t.Fatalf("settings=%v", settings)
	}

	policyBody := `{"operation_id":"` + governanceOperationID(986) + `","scope_kind":"employee","scope_id":"employee-one","enabled":true,` +
		`"hard":{"rpm":null,"concurrency":null},"budget":{"tpm":700,"cost_micro":"800","currency":"USD","window":"rolling_24h","unknown_mode":"deny_unknown"},` +
		`"shadow":{"tpm":null,"cost_micro":null,"currency":null,"window":null}}`
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/governance/policies", policyBody, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("policy status=%d body=%s", response.StatusCode, readBody(response))
	}
	var receipt governanceOperationReceipt
	decodeResponse(t, response, &receipt)
	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/governance/policies/"+receipt.ResourceID, "", cookie, "", "")
	var policy map[string]any
	decodeResponse(t, response, &policy)
	budget := policy["budget"].(map[string]any)
	if budget["tpm"] != float64(700) || budget["cost_micro"] != "800" || budget["unknown_mode"] != "deny_unknown" {
		t.Fatalf("policy budget=%v", budget)
	}

	bad := strings.Replace(policyBody, `"deny_unknown"`, `"unknown"`, 1)
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/governance/policies", bad, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad mode status=%d body=%s", response.StatusCode, readBody(response))
	}
	response = requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/governance/settings", settingsBody, cookie, "", server.URL)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d body=%s", response.StatusCode, readBody(response))
	}
}
