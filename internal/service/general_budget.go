package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/governance"
)

const generalBudgetMaxBody = 64 << 10

const generalBudgetSettingsDDL = `CREATE TABLE IF NOT EXISTS governance_general_budget_settings (
	singleton INTEGER PRIMARY KEY CHECK(singleton=1),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	updated_at TEXT NOT NULL
)`

const generalBudgetPoliciesDDL = `CREATE TABLE IF NOT EXISTS governance_general_budget_policies (
	id TEXT PRIMARY KEY,
	scope_kind TEXT NOT NULL CHECK(scope_kind IN ('employee','key','group')),
	scope_id TEXT NOT NULL,
	protocol TEXT NOT NULL CHECK(protocol IN ('','openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content')),
	model TEXT NOT NULL,
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	token_limit INTEGER CHECK(token_limit IS NULL OR (typeof(token_limit)='integer' AND token_limit BETWEEN 1 AND 9007199254740991)),
	token_window TEXT NOT NULL CHECK(token_window IN ('','rolling_60s')),
	cost_limit_micro INTEGER CHECK(cost_limit_micro IS NULL OR (typeof(cost_limit_micro)='integer' AND cost_limit_micro>0)),
	currency TEXT NOT NULL,
	cost_window TEXT NOT NULL CHECK(cost_window IN ('','rolling_24h')),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(scope_kind,scope_id,protocol,model),
	CHECK(token_limit IS NOT NULL OR cost_limit_micro IS NOT NULL),
	CHECK((token_limit IS NULL AND token_window='') OR (token_limit IS NOT NULL AND token_window='rolling_60s')),
	CHECK((cost_limit_micro IS NULL AND currency='' AND cost_window='') OR (cost_limit_micro IS NOT NULL AND length(currency)=3 AND currency GLOB '[A-Z][A-Z][A-Z]' AND cost_window='rolling_24h')),
	CHECK(updated_at>=created_at)
)`

const generalBudgetOperationsDDL = `CREATE TABLE IF NOT EXISTS governance_general_budget_operations (
	operation_id TEXT PRIMARY KEY,
	actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	action TEXT NOT NULL CHECK(action IN ('budget.create','budget.update')),
	payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
	policy_id TEXT NOT NULL,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL
)`

const generalBudgetAuditDDL = `CREATE TABLE IF NOT EXISTS governance_general_budget_audit (
	operation_id TEXT PRIMARY KEY REFERENCES governance_general_budget_operations(operation_id) ON DELETE RESTRICT,
	actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	action TEXT NOT NULL CHECK(action IN ('budget.create','budget.update')),
	policy_id TEXT NOT NULL,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL
)`

const generalBudgetRequestScopesDDL = `CREATE TABLE IF NOT EXISTS governance_general_budget_request_scopes (
	request_id TEXT NOT NULL REFERENCES governance_requests(id) ON DELETE CASCADE,
	scope_kind TEXT NOT NULL CHECK(scope_kind IN ('employee','key','group')),
	scope_id TEXT NOT NULL,
	protocol TEXT NOT NULL CHECK(protocol IN ('','openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content')),
	model TEXT NOT NULL,
	policy_id TEXT NOT NULL REFERENCES governance_general_budget_policies(id) ON DELETE RESTRICT,
	policy_revision INTEGER NOT NULL CHECK(typeof(policy_revision)='integer' AND policy_revision BETWEEN 1 AND 9007199254740991),
	group_revision INTEGER CHECK((scope_kind='group' AND typeof(group_revision)='integer' AND group_revision BETWEEN 1 AND 9007199254740991) OR (scope_kind IN ('employee','key') AND group_revision IS NULL)),
	settings_revision INTEGER NOT NULL CHECK(typeof(settings_revision)='integer' AND settings_revision BETWEEN 1 AND 9007199254740991),
	selector_revision INTEGER NOT NULL CHECK(typeof(selector_revision)='integer' AND selector_revision BETWEEN 1 AND 9007199254740991),
	hard_tpm INTEGER CHECK(hard_tpm IS NULL OR (typeof(hard_tpm)='integer' AND hard_tpm BETWEEN 1 AND 9007199254740991)),
	hard_cost_micro INTEGER CHECK(hard_cost_micro IS NULL OR (typeof(hard_cost_micro)='integer' AND hard_cost_micro>0)),
	hard_currency TEXT NOT NULL,
	hard_window TEXT NOT NULL,
	unknown_mode TEXT NOT NULL CHECK(unknown_mode='deny_unknown'),
	PRIMARY KEY(request_id,policy_id),
	CHECK(hard_tpm IS NOT NULL OR hard_cost_micro IS NOT NULL),
	CHECK((hard_cost_micro IS NULL AND hard_currency='' AND hard_window='') OR (hard_cost_micro IS NOT NULL AND length(hard_currency)=3 AND hard_currency GLOB '[A-Z][A-Z][A-Z]' AND hard_window='rolling_24h'))
)`

const generalBudgetReservationScopesDDL = `CREATE TABLE IF NOT EXISTS governance_general_budget_reservation_scopes (
	attempt_id TEXT NOT NULL REFERENCES governance_budget_reservations(attempt_id) ON DELETE CASCADE,
	scope_kind TEXT NOT NULL CHECK(scope_kind IN ('employee','key','group')),
	scope_id TEXT NOT NULL,
	protocol TEXT NOT NULL CHECK(protocol IN ('','openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content')),
	model TEXT NOT NULL,
	policy_id TEXT NOT NULL,
	policy_revision INTEGER NOT NULL CHECK(typeof(policy_revision)='integer' AND policy_revision BETWEEN 1 AND 9007199254740991),
	group_revision INTEGER CHECK((scope_kind='group' AND typeof(group_revision)='integer' AND group_revision BETWEEN 1 AND 9007199254740991) OR (scope_kind IN ('employee','key') AND group_revision IS NULL)),
	settings_revision INTEGER NOT NULL CHECK(typeof(settings_revision)='integer' AND settings_revision BETWEEN 1 AND 9007199254740991),
	selector_revision INTEGER NOT NULL CHECK(typeof(selector_revision)='integer' AND selector_revision BETWEEN 1 AND 9007199254740991),
	hard_tpm INTEGER CHECK(hard_tpm IS NULL OR (typeof(hard_tpm)='integer' AND hard_tpm BETWEEN 1 AND 9007199254740991)),
	hard_cost_micro INTEGER CHECK(hard_cost_micro IS NULL OR (typeof(hard_cost_micro)='integer' AND hard_cost_micro>0)),
	hard_currency TEXT NOT NULL,
	hard_window TEXT NOT NULL,
	unknown_mode TEXT NOT NULL CHECK(unknown_mode='deny_unknown'),
	PRIMARY KEY(attempt_id,policy_id),
	CHECK(hard_tpm IS NOT NULL OR hard_cost_micro IS NOT NULL),
	CHECK((hard_cost_micro IS NULL AND hard_currency='' AND hard_window='') OR (hard_cost_micro IS NOT NULL AND length(hard_currency)=3 AND hard_currency GLOB '[A-Z][A-Z][A-Z]' AND hard_window='rolling_24h'))
)`

const generalBudgetScopeIndexDDL = `CREATE INDEX IF NOT EXISTS governance_general_budget_scope_idx
	ON governance_general_budget_policies(scope_kind,scope_id,enabled,protocol,model,id)`

const generalBudgetReservationScopeIndexDDL = `CREATE INDEX IF NOT EXISTS governance_general_budget_reservation_scope_idx
	ON governance_general_budget_reservation_scopes(scope_kind,scope_id,protocol,model,attempt_id)`

type generalBudgetPolicy struct {
	ID             string
	ScopeKind      governance.ScopeKind
	ScopeID        string
	Protocol       accounting.UsageProtocol
	Model          string
	Enabled        bool
	TokenLimit     *int64
	CostLimitMicro *int64
	Currency       string
	Revision       int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type generalBudgetOperation struct {
	OperationID string
	PolicyID    string
	Revision    int64
	CreatedAt   time.Time
}

func migrateGeneralBudgets(ctx context.Context, db *sql.DB) error {
	if ctx == nil || db == nil {
		return errGovernanceManagementInvalid
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errGovernanceManagementUnavailable
	}
	defer tx.Rollback()
	for _, dependency := range []string{"admins", "employees", "access_keys", "models", "governance_groups", "governance_policies", "governance_settings", "governance_requests", "governance_budget_reservations"} {
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name=?`, dependency).Scan(&kind); err != nil || kind != "table" {
			return errGovernanceManagementSchema
		}
	}
	for _, statement := range []string{generalBudgetSettingsDDL, generalBudgetPoliciesDDL, generalBudgetOperationsDDL, generalBudgetAuditDDL, generalBudgetRequestScopesDDL, generalBudgetReservationScopesDDL, generalBudgetScopeIndexDDL, generalBudgetReservationScopeIndexDDL} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return errGovernanceManagementUnavailable
		}
	}
	if err := validateGeneralBudgetSchema(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO governance_general_budget_settings(singleton,revision,updated_at) VALUES(1,1,?)`, "1970-01-01T00:00:00.000000000Z"); err != nil {
		return errGovernanceManagementUnavailable
	}
	if err := validateGeneralBudgetStoredData(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errGovernanceManagementUnavailable
	}
	return nil
}

func validateGeneralBudgetSchema(ctx context.Context, tx *sql.Tx) error {
	expected := map[string]string{
		"governance_general_budget_settings":           generalBudgetSettingsDDL,
		"governance_general_budget_policies":           generalBudgetPoliciesDDL,
		"governance_general_budget_operations":         generalBudgetOperationsDDL,
		"governance_general_budget_audit":              generalBudgetAuditDDL,
		"governance_general_budget_request_scopes":     generalBudgetRequestScopesDDL,
		"governance_general_budget_reservation_scopes": generalBudgetReservationScopesDDL,
	}
	for name, ddl := range expected {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil || kind != "table" || normalizeGovernanceDDL(actual) != normalizeGovernanceDDL(storedGovernanceDDL(ddl)) {
			return errGovernanceManagementSchema
		}
	}
	for name, expected := range map[string]struct{ table, ddl string }{
		"governance_general_budget_scope_idx":             {"governance_general_budget_policies", generalBudgetScopeIndexDDL},
		"governance_general_budget_reservation_scope_idx": {"governance_general_budget_reservation_scopes", generalBudgetReservationScopeIndexDDL},
	} {
		var kind, table, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,tbl_name,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &table, &actual); err != nil || kind != "index" || table != expected.table || normalizeGovernanceDDL(actual) != normalizeGovernanceDDL(storedGovernanceDDL(expected.ddl)) {
			return errGovernanceManagementSchema
		}
	}
	var explicitIndexes int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND sql IS NOT NULL AND tbl_name LIKE 'governance_general_budget_%'`).Scan(&explicitIndexes); err != nil || explicitIndexes != 2 {
		return errGovernanceManagementSchema
	}
	var triggers int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name LIKE 'governance_general_budget_%'`).Scan(&triggers); err != nil || triggers != 0 {
		return errGovernanceManagementSchema
	}
	return nil
}

func validateGeneralBudgetStoredData(ctx context.Context, tx *sql.Tx) error {
	var settingsRows int
	var settingsRevision int64
	var settingsUpdated string
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(MAX(revision),0),COALESCE(MAX(updated_at),'') FROM governance_general_budget_settings`).Scan(&settingsRows, &settingsRevision, &settingsUpdated); err != nil || settingsRows != 1 || !validGovernanceRevision(settingsRevision) {
		return errGovernanceManagementSchema
	}
	if _, err := time.Parse(time.RFC3339Nano, settingsUpdated); err != nil {
		return errGovernanceManagementSchema
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,scope_kind,scope_id,protocol,model,enabled,token_limit,cost_limit_micro,currency,revision,created_at,updated_at FROM governance_general_budget_policies ORDER BY id`)
	if err != nil {
		return errGovernanceManagementUnavailable
	}
	items := make([]generalBudgetPolicy, 0)
	for rows.Next() {
		policy, err := scanGeneralBudgetPolicy(rows)
		if err != nil || !validGeneralBudgetPolicy(policy) {
			rows.Close()
			return errGovernanceManagementSchema
		}
		items = append(items, policy)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errGovernanceManagementUnavailable
	}
	if err := rows.Close(); err != nil {
		return errGovernanceManagementUnavailable
	}
	for _, policy := range items {
		if err := validateGeneralBudgetTarget(ctx, tx, policy.ScopeKind, policy.ScopeID); err != nil {
			return err
		}
		if policy.Model != "" && !generalBudgetModelExists(ctx, tx, policy.Model) {
			return errGovernanceManagementSchema
		}
	}
	var overlaps int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_general_budget_policies a JOIN governance_general_budget_policies b ON a.id<b.id AND a.scope_kind=b.scope_kind AND a.scope_id=b.scope_id
		WHERE a.enabled=1 AND b.enabled=1 AND a.cost_limit_micro IS NOT NULL AND b.cost_limit_micro IS NOT NULL AND a.currency<>b.currency
		AND (a.protocol='' OR b.protocol='' OR a.protocol=b.protocol) AND (a.model='' OR b.model='' OR a.model=b.model)`).Scan(&overlaps); err != nil || overlaps != 0 {
		return errGovernanceManagementSchema
	}
	foreignRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return errGovernanceManagementUnavailable
	}
	violated := foreignRows.Next()
	iterationErr, closeErr := foreignRows.Err(), foreignRows.Close()
	if iterationErr != nil || closeErr != nil {
		return errGovernanceManagementUnavailable
	}
	if violated {
		return errGovernanceManagementSchema
	}
	return nil
}

func (a *App) registerGeneralBudgetHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/budgets", a.requireAdmin(a.listGeneralBudgets, false))
	mux.HandleFunc("POST /admin/api/v1/budgets", a.requireAdmin(a.createGeneralBudget, true))
	mux.HandleFunc("GET /admin/api/v1/budgets/operations/{operation_id}", a.requireAdmin(a.getGeneralBudgetOperation, false))
	mux.HandleFunc("GET /admin/api/v1/budgets/{id}", a.requireAdmin(a.getGeneralBudget, false))
	mux.HandleFunc("PUT /admin/api/v1/budgets/{id}", a.requireAdmin(a.updateGeneralBudget, true))
}

func (a *App) getGeneralBudgetOperation(w http.ResponseWriter, r *http.Request, _ adminSession) {
	operationID := r.PathValue("operation_id")
	if r.URL.RawQuery != "" || !validGovernanceOperationID(operationID) {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	item, found, err := loadGeneralBudgetOperation(r.Context(), a.store.db, operationID)
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	if !found {
		writeGovernanceManagementError(w, errGovernanceManagementNotFound)
		return
	}
	writeJSON(w, http.StatusOK, generalBudgetOperationView(item))
}

func (a *App) listGeneralBudgets(w http.ResponseWriter, r *http.Request, _ adminSession) {
	limit, after, ok := parseGovernancePage(r)
	if !ok {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	rows, err := a.store.db.QueryContext(r.Context(), `SELECT id,scope_kind,scope_id,protocol,model,enabled,token_limit,cost_limit_micro,currency,revision,created_at,updated_at FROM governance_general_budget_policies WHERE id>? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		writeGovernanceManagementError(w, errGovernanceManagementUnavailable)
		return
	}
	defer rows.Close()
	items := make([]generalBudgetPolicy, 0, limit+1)
	for rows.Next() {
		item, err := scanGeneralBudgetPolicy(rows)
		if err != nil || !validGeneralBudgetPolicy(item) {
			writeGovernanceManagementError(w, errGovernanceManagementSchema)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeGovernanceManagementError(w, errGovernanceManagementUnavailable)
		return
	}
	next := ""
	if len(items) > limit {
		next = items[limit-1].ID
		items = items[:limit]
	}
	views := make([]map[string]any, 0, len(items))
	for _, item := range items {
		views = append(views, generalBudgetPolicyView(item))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": views, "next_cursor": generalBudgetNullableString(next)})
}

func (a *App) getGeneralBudget(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" || !validGovernanceMetadata(r.PathValue("id"), 256) {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	item, err := loadGeneralBudgetPolicy(r.Context(), a.store.db, r.PathValue("id"))
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, generalBudgetPolicyView(item))
}

func (a *App) createGeneralBudget(w http.ResponseWriter, r *http.Request, session adminSession) {
	policy, operationID, _, err := decodeGeneralBudgetRequest(w, r, false)
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	a.admission.Lock()
	receipt, err := writeGeneralBudgetPolicy(r.Context(), a.store.db, session.AdminID, operationID, "budget.create", "", 0, policy, time.Now().UTC())
	a.admission.Unlock()
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, generalBudgetOperationView(receipt))
}

func (a *App) updateGeneralBudget(w http.ResponseWriter, r *http.Request, session adminSession) {
	id := r.PathValue("id")
	if !validGovernanceMetadata(id, 256) {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	policy, operationID, expected, err := decodeGeneralBudgetRequest(w, r, true)
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	a.admission.Lock()
	receipt, err := writeGeneralBudgetPolicy(r.Context(), a.store.db, session.AdminID, operationID, "budget.update", id, expected, policy, time.Now().UTC())
	a.admission.Unlock()
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, generalBudgetOperationView(receipt))
}

func decodeGeneralBudgetRequest(w http.ResponseWriter, r *http.Request, update bool) (generalBudgetPolicy, string, int64, error) {
	if r.URL.RawQuery != "" {
		return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
	}
	object, err := decodeUniqueJSONObject(w, r, generalBudgetMaxBody)
	if err != nil {
		return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
	}
	allowed := map[string]bool{"operation_id": true, "expected_revision": update, "scope_kind": !update, "scope_id": !update, "protocol": !update, "model": !update, "enabled": true, "token_limit": true, "cost_limit_micro": true, "currency": true}
	for key := range object {
		if !allowed[key] {
			return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
		}
	}
	operationID, ok := object["operation_id"].(string)
	if !ok || !validGovernanceOperationID(operationID) {
		return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
	}
	var expected int64
	if update {
		value, ok := parseGovernanceJSONRevision(object["expected_revision"])
		if !ok {
			return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
		}
		expected = value
	}
	policy := generalBudgetPolicy{Enabled: true}
	if !update {
		kind, kindOK := object["scope_kind"].(string)
		id, idOK := object["scope_id"].(string)
		if !kindOK || !idOK {
			return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
		}
		policy.ScopeKind, policy.ScopeID = governance.ScopeKind(kind), id
		if raw, present := object["protocol"]; present && raw != nil {
			value, ok := raw.(string)
			if !ok {
				return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
			}
			policy.Protocol = accounting.UsageProtocol(value)
		}
		if raw, present := object["model"]; present && raw != nil {
			value, ok := raw.(string)
			if !ok {
				return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
			}
			policy.Model = value
		}
	}
	if raw, present := object["enabled"]; present {
		value, ok := raw.(bool)
		if !ok {
			return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
		}
		policy.Enabled = value
	} else {
		return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
	}
	if raw, present := object["token_limit"]; present && raw != nil {
		value, ok := parseGovernanceJSONRevision(raw)
		if !ok {
			return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
		}
		policy.TokenLimit = &value
	}
	if raw, present := object["cost_limit_micro"]; present && raw != nil {
		text, ok := raw.(string)
		value, parseErr := strconv.ParseInt(text, 10, 64)
		if !ok || parseErr != nil || value <= 0 || strconv.FormatInt(value, 10) != text {
			return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
		}
		policy.CostLimitMicro = &value
	}
	if raw, present := object["currency"]; present && raw != nil {
		value, ok := raw.(string)
		if !ok {
			return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
		}
		policy.Currency = value
	}
	if policy.TokenLimit == nil && policy.CostLimitMicro == nil || policy.CostLimitMicro == nil && policy.Currency != "" || policy.CostLimitMicro != nil && !validGeneralBudgetCurrency(policy.Currency) {
		return generalBudgetPolicy{}, "", 0, errGovernanceManagementInvalid
	}
	return policy, operationID, expected, nil
}

func writeGeneralBudgetPolicy(ctx context.Context, db *sql.DB, actor, operationID, action, id string, expected int64, input generalBudgetPolicy, observed time.Time) (generalBudgetOperation, error) {
	if ctx == nil || db == nil || !validGovernanceActor(actor) || !validGovernanceOperationID(operationID) || action != "budget.create" && action != "budget.update" || !validUTCTimeService(observed) {
		return generalBudgetOperation{}, errGovernanceManagementInvalid
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return generalBudgetOperation{}, errGovernanceManagementUnavailable
	}
	defer tx.Rollback()
	storedRevision := int64(0)
	if action == "budget.update" {
		stored, err := loadGeneralBudgetPolicy(ctx, tx, id)
		if err != nil {
			return generalBudgetOperation{}, err
		}
		input.ID, input.ScopeKind, input.ScopeID, input.Protocol, input.Model, input.CreatedAt = stored.ID, stored.ScopeKind, stored.ScopeID, stored.Protocol, stored.Model, stored.CreatedAt
		storedRevision = stored.Revision
	}
	digest := generalBudgetDigest(action, id, actor, input, expected)
	if prior, found, err := loadGeneralBudgetOperation(ctx, tx, operationID); err != nil {
		return generalBudgetOperation{}, err
	} else if found {
		var storedDigest []byte
		if err := tx.QueryRowContext(ctx, `SELECT payload_digest FROM governance_general_budget_operations WHERE operation_id=?`, operationID).Scan(&storedDigest); err != nil {
			return generalBudgetOperation{}, errGovernanceManagementUnavailable
		}
		if hex.EncodeToString(storedDigest) == hex.EncodeToString(digest[:]) {
			return prior, nil
		}
		return generalBudgetOperation{}, errGovernanceManagementOperationConflict
	}
	if action == "budget.create" {
		if err := validateGeneralBudgetTarget(ctx, tx, input.ScopeKind, input.ScopeID); err != nil || input.Model != "" && !generalBudgetModelExists(ctx, tx, input.Model) {
			if err != nil {
				return generalBudgetOperation{}, err
			}
			return generalBudgetOperation{}, errGovernanceManagementResourceConflict
		}
		if !validGeneralBudgetPolicy(input) {
			return generalBudgetOperation{}, errGovernanceManagementInvalid
		}
		generated, err := newID("gb")
		if err != nil {
			return generalBudgetOperation{}, errGovernanceManagementUnavailable
		}
		input.ID, input.Revision = generated, 1
		input.CreatedAt = observed
	} else {
		if !validGovernanceRevision(expected) {
			return generalBudgetOperation{}, errGovernanceManagementInvalid
		}
		if storedRevision != expected {
			return generalBudgetOperation{}, errGovernanceManagementRevisionConflict
		}
		input.Revision = expected + 1
		if input.Revision > governance.MaxRevision {
			return generalBudgetOperation{}, errGovernanceManagementRevisionConflict
		}
	}
	input.UpdatedAt = observed
	if err := rejectGeneralBudgetCurrencyOverlap(ctx, tx, input); err != nil {
		return generalBudgetOperation{}, err
	}
	if action == "budget.create" {
		_, err = tx.ExecContext(ctx, `INSERT INTO governance_general_budget_policies(id,scope_kind,scope_id,protocol,model,enabled,token_limit,token_window,cost_limit_micro,currency,cost_window,revision,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.ID, input.ScopeKind, input.ScopeID, input.Protocol, input.Model, boolGovernance(input.Enabled), nullableGovernanceInt(input.TokenLimit), generalBudgetTokenWindow(input), nullableGovernanceInt(input.CostLimitMicro), input.Currency, generalBudgetCostWindow(input), input.Revision, formatGovernanceTime(input.CreatedAt), formatGovernanceTime(input.UpdatedAt))
	} else {
		result, updateErr := tx.ExecContext(ctx, `UPDATE governance_general_budget_policies SET enabled=?,token_limit=?,token_window=?,cost_limit_micro=?,currency=?,cost_window=?,revision=?,updated_at=? WHERE id=? AND revision=?`, boolGovernance(input.Enabled), nullableGovernanceInt(input.TokenLimit), generalBudgetTokenWindow(input), nullableGovernanceInt(input.CostLimitMicro), input.Currency, generalBudgetCostWindow(input), input.Revision, formatGovernanceTime(input.UpdatedAt), input.ID, expected)
		if updateErr == nil {
			var changed int64
			changed, updateErr = result.RowsAffected()
			if updateErr == nil && changed != 1 {
				updateErr = errGovernanceManagementRevisionConflict
			}
		}
		err = updateErr
	}
	if err != nil {
		if errors.Is(err, errGovernanceManagementRevisionConflict) {
			return generalBudgetOperation{}, err
		}
		return generalBudgetOperation{}, errGovernanceManagementResourceConflict
	}
	var settingsRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM governance_general_budget_settings WHERE singleton=1`).Scan(&settingsRevision); err != nil || settingsRevision >= governance.MaxRevision {
		return generalBudgetOperation{}, errGovernanceManagementRevisionConflict
	}
	if _, err := tx.ExecContext(ctx, `UPDATE governance_general_budget_settings SET revision=revision+1,updated_at=? WHERE singleton=1 AND revision=?`, formatGovernanceTime(observed), settingsRevision); err != nil {
		return generalBudgetOperation{}, errGovernanceManagementUnavailable
	}
	receipt := generalBudgetOperation{OperationID: operationID, PolicyID: input.ID, Revision: input.Revision, CreatedAt: observed}
	if _, err := tx.ExecContext(ctx, `INSERT INTO governance_general_budget_operations(operation_id,actor_id,action,payload_digest,policy_id,revision,created_at) VALUES(?,?,?,?,?,?,?)`, operationID, actor, action, digest[:], input.ID, input.Revision, formatGovernanceTime(observed)); err != nil {
		return generalBudgetOperation{}, errGovernanceManagementUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO governance_general_budget_audit(operation_id,actor_id,action,policy_id,revision,created_at) VALUES(?,?,?,?,?,?)`, operationID, actor, action, input.ID, input.Revision, formatGovernanceTime(observed)); err != nil {
		return generalBudgetOperation{}, errGovernanceManagementUnavailable
	}
	if err := tx.Commit(); err != nil {
		return generalBudgetOperation{}, errGovernanceManagementUnavailable
	}
	return receipt, nil
}

func (s *governanceManagementStore) ResolveScopesForRequestTx(ctx context.Context, tx *sql.Tx, employeeID, keyID, model string, protocol accounting.UsageProtocol) (governance.Settings, []governance.ScopeSnapshot, error) {
	if !validGovernanceMetadata(model, 256) || !validGeneralBudgetProtocol(protocol) {
		return governance.Settings{}, nil, errGovernanceManagementInvalid
	}
	return s.ResolveScopesTx(ctx, tx, employeeID, keyID)
}

func (s *governanceManagementStore) MatchGeneralBudgetScopesTx(ctx context.Context, tx *sql.Tx, requestID, employeeID, keyID, model string, protocol accounting.UsageProtocol) (bool, error) {
	if s == nil || ctx == nil || tx == nil || !validGovernanceMetadata(requestID, 256) || !validGovernanceMetadata(employeeID, 256) || !validGovernanceMetadata(keyID, 256) || !validGovernanceMetadata(model, 256) || !validGeneralBudgetProtocol(protocol) {
		return false, errGovernanceManagementInvalid
	}
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='governance_general_budget_policies'`).Scan(&present); err != nil {
		return false, errGovernanceManagementUnavailable
	}
	if present == 0 {
		return false, nil
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_general_budget_request_scopes WHERE request_id=?`, requestID).Scan(&count); err != nil {
		return false, errGovernanceManagementUnavailable
	}
	if count != 0 {
		return true, nil
	}
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_general_budget_policies p WHERE p.enabled=1 AND (p.protocol='' OR p.protocol=?) AND (p.model='' OR p.model=?) AND (
		(p.scope_kind='employee' AND p.scope_id=?) OR (p.scope_kind='key' AND p.scope_id=?) OR
		(p.scope_kind='group' AND EXISTS(SELECT 1 FROM governance_group_members m WHERE m.group_id=p.scope_id AND m.employee_id=?)))`, protocol, model, employeeID, keyID, employeeID).Scan(&count)
	if err != nil {
		return false, errGovernanceManagementUnavailable
	}
	return count != 0, nil
}

// SnapshotGeneralBudgetScopesTx freezes every matching selector policy after
// the parent governance request has been inserted and before the caller commits
// admission. Each selector remains a separate rolling window; revisions never
// reset a stable (kind,id,protocol,model) scope.
func (s *governanceManagementStore) SnapshotGeneralBudgetScopesTx(ctx context.Context, tx *sql.Tx, requestID, employeeID, keyID, model string, protocol accounting.UsageProtocol, settingsRevision int64) (bool, error) {
	if s == nil || ctx == nil || tx == nil || !validGovernanceMetadata(requestID, 256) || !validGovernanceMetadata(employeeID, 256) || !validGovernanceMetadata(keyID, 256) || !validGovernanceMetadata(model, 256) || !validGeneralBudgetProtocol(protocol) || !validGovernanceRevision(settingsRevision) {
		return false, errGovernanceManagementInvalid
	}
	var present int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='governance_general_budget_policies'`).Scan(&present); err != nil {
		return false, errGovernanceManagementUnavailable
	}
	if present == 0 {
		return false, nil
	}
	var existing int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_general_budget_request_scopes WHERE request_id=?`, requestID).Scan(&existing); err != nil {
		return false, errGovernanceManagementUnavailable
	}
	if existing != 0 {
		return true, nil
	}
	var selectorRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM governance_general_budget_settings WHERE singleton=1`).Scan(&selectorRevision); err != nil || !validGovernanceRevision(selectorRevision) {
		return false, errGovernanceManagementSchema
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.id,p.scope_kind,p.scope_id,p.protocol,p.model,p.enabled,p.token_limit,p.cost_limit_micro,p.currency,p.revision,p.created_at,p.updated_at,
		CASE WHEN p.scope_kind='group' THEN g.revision ELSE NULL END
		FROM governance_general_budget_policies p
		LEFT JOIN governance_groups g ON p.scope_kind='group' AND g.id=p.scope_id
		WHERE p.enabled=1 AND (p.protocol='' OR p.protocol=?) AND (p.model='' OR p.model=?) AND (
			(p.scope_kind='employee' AND p.scope_id=?) OR (p.scope_kind='key' AND p.scope_id=?) OR
			(p.scope_kind='group' AND EXISTS(SELECT 1 FROM governance_group_members m WHERE m.group_id=p.scope_id AND m.employee_id=?)))
		ORDER BY p.scope_kind,p.scope_id,p.id`, protocol, model, employeeID, keyID, employeeID)
	if err != nil {
		return false, errGovernanceManagementUnavailable
	}
	type matched struct {
		policy generalBudgetPolicy
		group  sql.NullInt64
	}
	matches := make([]matched, 0)
	for rows.Next() {
		var item matched
		if err := scanGeneralBudgetPolicyWithGroup(rows, &item.policy, &item.group); err != nil {
			rows.Close()
			return false, errGovernanceManagementSchema
		}
		matches = append(matches, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, errGovernanceManagementUnavailable
	}
	if err := rows.Close(); err != nil {
		return false, errGovernanceManagementUnavailable
	}
	for _, match := range matches {
		if match.policy.ScopeKind == governance.ScopeGroup && !match.group.Valid {
			return false, errGovernanceManagementSchema
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO governance_general_budget_request_scopes(request_id,scope_kind,scope_id,protocol,model,policy_id,policy_revision,group_revision,settings_revision,selector_revision,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'deny_unknown')`, requestID, match.policy.ScopeKind, match.policy.ScopeID, match.policy.Protocol, match.policy.Model, match.policy.ID, match.policy.Revision, nullableGeneralBudgetGroup(match.group), settingsRevision, selectorRevision,
			nullableGovernanceInt(match.policy.TokenLimit), nullableGovernanceInt(match.policy.CostLimitMicro), match.policy.Currency, generalBudgetCostWindow(match.policy))
		if err != nil {
			return false, errGovernanceManagementUnavailable
		}
	}
	return len(matches) != 0, nil
}

func nullableGeneralBudgetGroup(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func rejectGeneralBudgetCurrencyOverlap(ctx context.Context, tx *sql.Tx, input generalBudgetPolicy) error {
	if !input.Enabled || input.CostLimitMicro == nil {
		return nil
	}
	var legacyCurrency sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT hard_currency FROM governance_policies WHERE scope_kind=? AND scope_id=? AND enabled=1 AND hard_cost_micro IS NOT NULL`, input.ScopeKind, input.ScopeID).Scan(&legacyCurrency); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errGovernanceManagementUnavailable
	}
	if legacyCurrency.Valid && legacyCurrency.String != input.Currency {
		return errGovernanceManagementResourceConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT protocol,model,currency FROM governance_general_budget_policies WHERE scope_kind=? AND scope_id=? AND enabled=1 AND cost_limit_micro IS NOT NULL AND id<>?`, input.ScopeKind, input.ScopeID, input.ID)
	if err != nil {
		return errGovernanceManagementUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var protocol, model, currency string
		if err := rows.Scan(&protocol, &model, &currency); err != nil {
			return errGovernanceManagementUnavailable
		}
		if selectorsOverlap(string(input.Protocol), protocol) && selectorsOverlap(input.Model, model) && currency != input.Currency {
			return errGovernanceManagementResourceConflict
		}
	}
	return rows.Err()
}

func selectorsOverlap(left, right string) bool { return left == "" || right == "" || left == right }

func generalBudgetDigest(action, id, actor string, input generalBudgetPolicy, expected int64) [32]byte {
	parts := []string{"general-budget/v1", action, id, actor, strconv.FormatInt(expected, 10), string(input.ScopeKind), input.ScopeID, string(input.Protocol), input.Model, strconv.FormatBool(input.Enabled), pointerDecimal(input.TokenLimit), pointerDecimal(input.CostLimitMicro), input.Currency}
	return sha256.Sum256([]byte(strings.Join(parts, "\x00")))
}

func pointerDecimal(value *int64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatInt(*value, 10)
}

func loadGeneralBudgetOperation(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, operationID string) (generalBudgetOperation, bool, error) {
	var item generalBudgetOperation
	var created string
	err := query.QueryRowContext(ctx, `SELECT operation_id,policy_id,revision,created_at FROM governance_general_budget_operations WHERE operation_id=?`, operationID).Scan(&item.OperationID, &item.PolicyID, &item.Revision, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, errGovernanceManagementUnavailable
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return item, false, errGovernanceManagementSchema
	}
	return item, true, nil
}

func loadGeneralBudgetPolicy(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (generalBudgetPolicy, error) {
	row := query.QueryRowContext(ctx, `SELECT id,scope_kind,scope_id,protocol,model,enabled,token_limit,cost_limit_micro,currency,revision,created_at,updated_at FROM governance_general_budget_policies WHERE id=?`, id)
	item, err := scanGeneralBudgetPolicy(row)
	if errors.Is(err, sql.ErrNoRows) {
		return item, errGovernanceManagementNotFound
	}
	if err != nil {
		return item, errGovernanceManagementUnavailable
	}
	if !validGeneralBudgetPolicy(item) {
		return item, errGovernanceManagementSchema
	}
	return item, nil
}

type generalBudgetScanner interface{ Scan(...any) error }

func scanGeneralBudgetPolicy(scanner generalBudgetScanner) (generalBudgetPolicy, error) {
	var item generalBudgetPolicy
	var enabled int
	var token, cost sql.NullInt64
	var created, updated string
	err := scanner.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &item.Protocol, &item.Model, &enabled, &token, &cost, &item.Currency, &item.Revision, &created, &updated)
	if err != nil {
		return item, err
	}
	item.Enabled = enabled == 1
	setGovernanceOptionalInt(&item.TokenLimit, token)
	setGovernanceOptionalInt(&item.CostLimitMicro, cost)
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err == nil {
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	}
	return item, err
}

func scanGeneralBudgetPolicyWithGroup(scanner generalBudgetScanner, item *generalBudgetPolicy, group *sql.NullInt64) error {
	var enabled int
	var token, cost sql.NullInt64
	var created, updated string
	err := scanner.Scan(&item.ID, &item.ScopeKind, &item.ScopeID, &item.Protocol, &item.Model, &enabled, &token, &cost, &item.Currency, &item.Revision, &created, &updated, group)
	if err != nil {
		return err
	}
	item.Enabled = enabled == 1
	setGovernanceOptionalInt(&item.TokenLimit, token)
	setGovernanceOptionalInt(&item.CostLimitMicro, cost)
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err == nil {
		item.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated)
	}
	return err
}

func validGeneralBudgetPolicy(item generalBudgetPolicy) bool {
	return (item.ID == "" || validGovernanceMetadata(item.ID, 256)) && (item.ScopeKind == governance.ScopeEmployee || item.ScopeKind == governance.ScopeKey || item.ScopeKind == governance.ScopeGroup) && validGovernanceMetadata(item.ScopeID, 256) &&
		(item.Protocol == "" || validGeneralBudgetProtocol(item.Protocol)) && (item.Model == "" || validGovernanceMetadata(item.Model, 256)) && validGovernanceSafeLimit(item.TokenLimit) && validGovernanceCostLimit(item.CostLimitMicro) &&
		(item.TokenLimit != nil || item.CostLimitMicro != nil) && (item.CostLimitMicro == nil && item.Currency == "" || item.CostLimitMicro != nil && validGeneralBudgetCurrency(item.Currency)) &&
		(item.Revision == 0 || validGovernanceRevision(item.Revision)) && (item.CreatedAt.IsZero() || validUTCTimeService(item.CreatedAt)) && (item.UpdatedAt.IsZero() || validUTCTimeService(item.UpdatedAt))
}

func validGeneralBudgetProtocol(protocol accounting.UsageProtocol) bool {
	return protocol == accounting.ProtocolOpenAIChatCompletions || protocol == accounting.ProtocolOpenAIResponses || protocol == accounting.ProtocolAnthropicMessages || protocol == accounting.ProtocolGeminiGenerateContent
}

func validGeneralBudgetCurrency(currency string) bool {
	return len(currency) == 3 && currency[0] >= 'A' && currency[0] <= 'Z' && currency[1] >= 'A' && currency[1] <= 'Z' && currency[2] >= 'A' && currency[2] <= 'Z'
}

func validateGeneralBudgetTarget(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, kind governance.ScopeKind, id string) error {
	var count int
	var statement string
	switch kind {
	case governance.ScopeEmployee:
		statement = `SELECT COUNT(*) FROM employees WHERE id=?`
	case governance.ScopeKey:
		statement = `SELECT COUNT(*) FROM access_keys WHERE id=?`
	case governance.ScopeGroup:
		statement = `SELECT COUNT(*) FROM governance_groups WHERE id=?`
	default:
		return errGovernanceManagementInvalid
	}
	if err := query.QueryRowContext(ctx, statement, id).Scan(&count); err != nil {
		return errGovernanceManagementUnavailable
	}
	if count != 1 {
		return errGovernanceManagementResourceConflict
	}
	return nil
}

func generalBudgetModelExists(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) bool {
	var count int
	return query.QueryRowContext(ctx, `SELECT COUNT(*) FROM models WHERE id=?`, id).Scan(&count) == nil && count == 1
}

func generalBudgetPolicyView(item generalBudgetPolicy) map[string]any {
	var protocol, model any
	if item.Protocol != "" {
		protocol = item.Protocol
	}
	if item.Model != "" {
		model = item.Model
	}
	var cost, currency any
	if item.CostLimitMicro != nil {
		cost, currency = strconv.FormatInt(*item.CostLimitMicro, 10), item.Currency
	}
	return map[string]any{"id": item.ID, "scope": map[string]any{"kind": item.ScopeKind, "id": item.ScopeID, "protocol": protocol, "model": model}, "enabled": item.Enabled, "enforcement": "strict", "unknown_mode": "deny_unknown", "token": map[string]any{"limit": item.TokenLimit, "window": generalBudgetNullableString(generalBudgetTokenWindow(item))}, "cost": map[string]any{"limit_micro": cost, "currency": currency, "window": generalBudgetNullableString(generalBudgetCostWindow(item))}, "revision": item.Revision, "created_at": item.CreatedAt.Format(time.RFC3339Nano), "updated_at": item.UpdatedAt.Format(time.RFC3339Nano)}
}

func generalBudgetOperationView(item generalBudgetOperation) map[string]any {
	return map[string]any{"operation_id": item.OperationID, "resource_kind": "budget", "resource_id": item.PolicyID, "revision": item.Revision, "created_at": item.CreatedAt.Format(time.RFC3339Nano)}
}

func generalBudgetTokenWindow(item generalBudgetPolicy) string {
	if item.TokenLimit != nil {
		return "rolling_60s"
	}
	return ""
}

func generalBudgetCostWindow(item generalBudgetPolicy) string {
	if item.CostLimitMicro != nil {
		return governance.ShadowWindowRolling24h
	}
	return ""
}

func generalBudgetNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func validUTCTimeService(value time.Time) bool {
	return !value.IsZero() && value.Location() == time.UTC && value.Equal(value.UTC())
}
