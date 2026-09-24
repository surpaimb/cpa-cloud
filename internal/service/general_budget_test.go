package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/governance"
)

func TestGeneralBudgetSelectorResolutionAndExistingEngine(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	ctx := context.Background()
	budget := migrateGeneralBudgetPrerequisites(t, f)
	if err := migrateGeneralBudgets(ctx, f.base.db); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := f.manager.updateSettingsPatch(ctx, "admin-one", governanceOperationID(1001), 1, true, &enabled); err != nil {
		t.Fatal(err)
	}
	tokenLimit := int64(40)
	created, err := writeGeneralBudgetPolicy(ctx, f.base.db, "admin-one", governanceOperationID(1002), "budget.create", "", 0, generalBudgetPolicy{
		ScopeKind: governance.ScopeEmployee, ScopeID: "employee-one", Protocol: accounting.ProtocolOpenAIResponses, Enabled: true, TokenLimit: &tokenLimit,
	}, governanceManagementTestTime)
	if err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || created.PolicyID == "" {
		t.Fatalf("receipt=%+v", created)
	}

	tx, err := f.base.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	unmatchedSettings, unmatched, err := f.manager.ResolveScopesForRequestTx(ctx, tx, "employee-one", "key-one", "public-model", accounting.ProtocolOpenAIChatCompletions)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if len(unmatched) != 0 {
		tx.Rollback()
		t.Fatalf("selector leaked into chat request: %+v", unmatched)
	}
	chatStarted := governanceManagementTestTime.Add(time.Second)
	if _, decision, err := f.core.AdmitTx(ctx, tx, governance.AdmissionStart{RequestID: "selector-unmatched", Subject: governance.Subject{EmployeeID: "employee-one", KeyID: "key-one", PublicModel: "public-model", Protocol: accounting.ProtocolOpenAIChatCompletions}, SettingsRevision: unmatchedSettings.Revision, SnapshotComplete: true, Scopes: unmatched, StartedAt: chatStarted, ObservedAt: chatStarted}); err != nil || !decision.Allowed {
		tx.Rollback()
		t.Fatalf("unmatched admission decision=%+v err=%v", decision, err)
	}
	if matched, err := f.manager.SnapshotGeneralBudgetScopesTx(ctx, tx, "selector-unmatched", "employee-one", "key-one", "public-model", accounting.ProtocolOpenAIChatCompletions, unmatchedSettings.Revision); err != nil || matched {
		tx.Rollback()
		t.Fatalf("unmatched snapshot matched=%v err=%v", matched, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	wildcardLimit := int64(100)
	wildcardReceipt, err := writeGeneralBudgetPolicy(ctx, f.base.db, "admin-one", governanceOperationID(1003), "budget.create", "", 0, generalBudgetPolicy{
		ScopeKind: governance.ScopeEmployee, ScopeID: "employee-one", Enabled: true, TokenLimit: &wildcardLimit,
	}, governanceManagementTestTime.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	// Persist 70 Chat tokens into the wildcard window. The Responses-only
	// policy must not see this contribution.
	tx, err = f.base.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	chatSettings, chatScopes, err := f.manager.ResolveScopesForRequestTx(ctx, tx, "employee-one", "key-one", "public-model", accounting.ProtocolOpenAIChatCompletions)
	chatAt := governanceManagementTestTime.Add(2 * time.Second)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, decision, err := f.core.AdmitTx(ctx, tx, governance.AdmissionStart{RequestID: "wildcard-chat-request", Subject: governance.Subject{EmployeeID: "employee-one", KeyID: "key-one", PublicModel: "public-model", Protocol: accounting.ProtocolOpenAIChatCompletions}, SettingsRevision: chatSettings.Revision, SnapshotComplete: true, BudgetSnapshot: true, Scopes: chatScopes, StartedAt: chatAt, ObservedAt: chatAt}); err != nil || !decision.Allowed {
		tx.Rollback()
		t.Fatalf("chat admission decision=%+v err=%v", decision, err)
	}
	if matched, err := f.manager.SnapshotGeneralBudgetScopesTx(ctx, tx, "wildcard-chat-request", "employee-one", "key-one", "public-model", accounting.ProtocolOpenAIChatCompletions, chatSettings.Revision); err != nil || !matched {
		tx.Rollback()
		t.Fatalf("chat snapshot matched=%v err=%v", matched, err)
	}
	ledger := accounting.NewLedger(f.base.db)
	chatRequest := accounting.RequestStart{ID: "wildcard-chat-request", EmployeeID: "employee-one", KeyID: "key-one", ModelID: "public-model", Provider: accounting.ProviderOpenAICompatible, StartedAt: chatAt}
	if err := ledger.BeginRequestTx(ctx, tx, chatRequest); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	chatAttempt := accounting.AttemptStart{ID: "wildcard-chat-attempt", RequestID: chatRequest.ID, AccountID: "account-one", Provider: chatRequest.Provider, Dispatch: accounting.DispatchPrimary, StartedAt: chatAt}
	if err := ledger.BeginAttemptTx(ctx, tx, chatAttempt); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	chatProof := governance.BudgetProof{Type: governance.BudgetProofFourBuckets, ActualModel: "actual-model", AccountRevision: 1, PoolRevision: 1, TransformRevision: "transform-v1", BounderID: "test-bounder", BounderRevision: 1, FourBuckets: &accounting.UpperUsage{InputTokens: 50, OutputTokens: 20}}
	if reserved, err := budget.ReserveTx(ctx, tx, governance.BudgetReserve{RequestID: chatRequest.ID, AttemptID: chatAttempt.ID, Proof: chatProof, ObservedAt: chatAt}); err != nil || !reserved.Allowed {
		tx.Rollback()
		t.Fatalf("chat reserve=%+v err=%v", reserved, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = f.base.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings, scopes, err := f.manager.ResolveScopesForRequestTx(ctx, tx, "employee-one", "key-one", "public-model", accounting.ProtocolOpenAIResponses)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if !settings.Enabled || !settings.BudgetEnabled || len(scopes) != 0 {
		tx.Rollback()
		t.Fatalf("settings=%+v scopes=%+v", settings, scopes)
	}
	started := governanceManagementTestTime.Add(3 * time.Second)
	lease, decision, err := f.core.AdmitTx(ctx, tx, governance.AdmissionStart{RequestID: "general-budget-request", Subject: governance.Subject{EmployeeID: "employee-one", KeyID: "key-one", PublicModel: "public-model", Protocol: accounting.ProtocolOpenAIResponses}, SettingsRevision: settings.Revision, SnapshotComplete: true, BudgetSnapshot: true, Scopes: scopes, StartedAt: started, ObservedAt: started})
	if err != nil || !decision.Allowed || lease == nil {
		tx.Rollback()
		t.Fatalf("admit lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	matched, err := f.manager.SnapshotGeneralBudgetScopesTx(ctx, tx, "general-budget-request", "employee-one", "key-one", "public-model", accounting.ProtocolOpenAIResponses, settings.Revision)
	if err != nil || !matched {
		tx.Rollback()
		t.Fatalf("matched snapshot=%v err=%v", matched, err)
	}
	var snapshotCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM governance_general_budget_request_scopes WHERE request_id=?`, "general-budget-request").Scan(&snapshotCount); err != nil || snapshotCount != 2 {
		tx.Rollback()
		t.Fatalf("snapshot count=%d err=%v", snapshotCount, err)
	}
	request := accounting.RequestStart{ID: "general-budget-request", EmployeeID: "employee-one", KeyID: "key-one", ModelID: "public-model", Provider: accounting.ProviderOpenAICompatible, StartedAt: started}
	if err := ledger.BeginRequestTx(ctx, tx, request); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	attempt := accounting.AttemptStart{ID: "general-budget-attempt", RequestID: request.ID, AccountID: "account-one", Provider: request.Provider, Dispatch: accounting.DispatchPrimary, StartedAt: started}
	if err := ledger.BeginAttemptTx(ctx, tx, attempt); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	missingProof := governance.BudgetProof{ActualModel: "actual-model", AccountRevision: 1, PoolRevision: 1, TransformRevision: "transform-v1", BounderID: "test-bounder", BounderRevision: 1}
	if rejected, err := budget.ReserveTx(ctx, tx, governance.BudgetReserve{RequestID: request.ID, AttemptID: attempt.ID, Proof: missingProof, ObservedAt: started}); err != nil || rejected.Allowed || !rejected.Enforced || rejected.Code != governance.BudgetDecisionBoundUnavailable {
		tx.Rollback()
		t.Fatalf("strict selector missing-proof reserve=%+v err=%v", rejected, err)
	}
	proof := governance.BudgetProof{Type: governance.BudgetProofFourBuckets, ActualModel: "actual-model", AccountRevision: 1, PoolRevision: 1, TransformRevision: "transform-v1", BounderID: "test-bounder", BounderRevision: 1, FourBuckets: &accounting.UpperUsage{InputTokens: 20, OutputTokens: 10}}
	reserved, err := budget.ReserveTx(ctx, tx, governance.BudgetReserve{RequestID: request.ID, AttemptID: attempt.ID, Proof: proof, ObservedAt: started})
	if err != nil || !reserved.Allowed || !reserved.Enforced || reserved.Code != governance.BudgetDecisionReserved {
		tx.Rollback()
		t.Fatalf("reserve=%+v err=%v", reserved, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := writeGeneralBudgetPolicy(ctx, f.base.db, "admin-one", governanceOperationID(1004), "budget.update", wildcardReceipt.PolicyID, 1, generalBudgetPolicy{
		Enabled: false, TokenLimit: &wildcardLimit,
	}, governanceManagementTestTime.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	// A retry after an uncertain commit must keep using the immutable snapshot,
	// even if the matching policy was disabled after the first commit.
	tx, err = f.base.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if matched, err := f.manager.MatchGeneralBudgetScopesTx(ctx, tx, "wildcard-chat-request", "employee-one", "key-one", "public-model", accounting.ProtocolOpenAIChatCompletions); err != nil || !matched {
		tx.Rollback()
		t.Fatalf("persisted replay match=%v err=%v", matched, err)
	}
	if matched, err := f.manager.SnapshotGeneralBudgetScopesTx(ctx, tx, "wildcard-chat-request", "employee-one", "key-one", "public-model", accounting.ProtocolOpenAIChatCompletions, chatSettings.Revision); err != nil || !matched {
		tx.Rollback()
		t.Fatalf("persisted replay snapshot=%v err=%v", matched, err)
	}
	if err := tx.QueryRow(`SELECT COUNT(*) FROM governance_general_budget_request_scopes WHERE request_id=?`, "wildcard-chat-request").Scan(&snapshotCount); err != nil || snapshotCount != 1 {
		tx.Rollback()
		t.Fatalf("persisted replay snapshot count=%d err=%v", snapshotCount, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestGeneralBudgetStrictMigrationAndCurrencyOverlap(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	ctx := context.Background()
	migrateGeneralBudgetPrerequisites(t, f)
	if _, err := f.base.db.Exec(`CREATE TABLE governance_general_budget_settings(singleton INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateGeneralBudgets(ctx, f.base.db); !errors.Is(err, errGovernanceManagementSchema) {
		t.Fatalf("lookalike migration err=%v", err)
	}
	for _, name := range []string{"governance_general_budget_policies", "governance_general_budget_operations", "governance_general_budget_audit", "governance_general_budget_request_scopes", "governance_general_budget_reservation_scopes"} {
		var count int
		if err := f.base.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial object %s count=%d err=%v", name, count, err)
		}
	}
	if _, err := f.base.db.Exec(`DROP TABLE governance_general_budget_settings`); err != nil {
		t.Fatal(err)
	}
	if err := migrateGeneralBudgets(ctx, f.base.db); err != nil {
		t.Fatal(err)
	}
	one, two := int64(100), int64(200)
	if _, err := writeGeneralBudgetPolicy(ctx, f.base.db, "admin-one", governanceOperationID(1010), "budget.create", "", 0, generalBudgetPolicy{ScopeKind: governance.ScopeEmployee, ScopeID: "employee-one", Enabled: true, CostLimitMicro: &one, Currency: "USD"}, governanceManagementTestTime); err != nil {
		t.Fatal(err)
	}
	if _, err := writeGeneralBudgetPolicy(ctx, f.base.db, "admin-one", governanceOperationID(1011), "budget.create", "", 0, generalBudgetPolicy{ScopeKind: governance.ScopeEmployee, ScopeID: "employee-one", Protocol: accounting.ProtocolOpenAIResponses, Enabled: true, CostLimitMicro: &two, Currency: "EUR"}, governanceManagementTestTime.Add(time.Second)); !errors.Is(err, errGovernanceManagementResourceConflict) {
		t.Fatalf("overlapping currency err=%v", err)
	}
}

func TestGeneralBudgetHTTPIdempotentCRUD(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	migrateGeneralBudgetPrerequisites(t, f)
	if err := migrateGeneralBudgets(context.Background(), f.base.db); err != nil {
		t.Fatal(err)
	}
	if err := createRootKey(f.dir); err != nil {
		t.Fatal(err)
	}
	secrets, err := loadSecrets(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	token, csrf := "general-budget-token", "general-budget-csrf"
	if _, err := f.base.db.Exec(`INSERT INTO sessions(id,admin_id,token_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)`, "general-budget-session", "admin-one", secrets.digest("admin-session/v1", token), csrf, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), governanceManagementTestTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{}, store: f.base, secrets: secrets}
	mux := http.NewServeMux()
	app.registerGeneralBudgetHandlers(mux)
	server := httptest.NewServer(requestMiddleware(mux))
	defer server.Close()
	cookie := &http.Cookie{Name: adminCookieName, Value: token}
	body := `{"operation_id":"` + governanceOperationID(1020) + `","scope_kind":"employee","scope_id":"employee-one","protocol":"openai-responses","model":null,"enabled":true,"token_limit":50,"cost_limit_micro":"9007199254740993","currency":"USD"}`
	response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/budgets", body, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d body=%s", response.StatusCode, readBody(response))
	}
	var receipt map[string]any
	decodeResponse(t, response, &receipt)
	policyID := receipt["resource_id"].(string)
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/budgets", body, cookie, csrf, server.URL)
	var replay map[string]any
	decodeResponse(t, response, &replay)
	if replay["resource_id"] != policyID || replay["revision"] != float64(1) {
		t.Fatalf("replay=%v", replay)
	}
	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/budgets/"+policyID, "", cookie, "", "")
	var policy map[string]any
	decodeResponse(t, response, &policy)
	if policy["enforcement"] != "strict" || policy["unknown_mode"] != "deny_unknown" || policy["cost"].(map[string]any)["limit_micro"] != "9007199254740993" {
		t.Fatalf("policy=%v", policy)
	}
	update := `{"operation_id":"` + governanceOperationID(1021) + `","expected_revision":1,"enabled":false,"token_limit":25,"cost_limit_micro":null,"currency":null}`
	response = requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/budgets/"+policyID, update, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d body=%s", response.StatusCode, readBody(response))
	}
	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/budgets?limit=10", "", cookie, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.StatusCode, readBody(response))
	}
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/budgets", body, cookie, "", server.URL)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d body=%s", response.StatusCode, readBody(response))
	}
}

func migrateGeneralBudgetPrerequisites(t *testing.T, f *governanceManagementFixture) *governance.Budget {
	t.Helper()
	ctx := context.Background()
	if err := accounting.NewLedger(f.base.db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	budget := governance.NewBudget(f.base.db)
	tx, err := f.base.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.MigrateTx(ctx, tx); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return budget
}
