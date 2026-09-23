package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cpacloud.local/server/internal/governance"
)

const governanceManagementMaxBody = 64 << 10

type governanceShadowView struct {
	TPM       *int64  `json:"tpm"`
	CostMicro *string `json:"cost_micro"`
	Currency  *string `json:"currency"`
	Window    *string `json:"window"`
}

type governancePolicyHTTPView struct {
	ID        string               `json:"id"`
	ScopeKind governance.ScopeKind `json:"scope_kind"`
	ScopeID   string               `json:"scope_id"`
	Enabled   bool                 `json:"enabled"`
	Hard      governanceHardLimits `json:"hard"`
	Shadow    governanceShadowView `json:"shadow"`
	Revision  int64                `json:"revision"`
	CreatedAt string               `json:"created_at"`
	UpdatedAt string               `json:"updated_at"`
}

func (s *governanceManagementStore) Register(a *App, mux *http.ServeMux) {
	if s == nil || a == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /admin/api/v1/governance/settings", a.requireAdmin(s.getGovernanceSettings, false))
	mux.HandleFunc("PUT /admin/api/v1/governance/settings", a.requireAdmin(func(w http.ResponseWriter, r *http.Request, session adminSession) {
		s.putGovernanceSettings(a, w, r, session)
	}, true))
	mux.HandleFunc("GET /admin/api/v1/governance/groups", a.requireAdmin(s.listGovernanceGroups, false))
	mux.HandleFunc("POST /admin/api/v1/governance/groups", a.requireAdmin(func(w http.ResponseWriter, r *http.Request, session adminSession) {
		s.postGovernanceGroup(a, w, r, session)
	}, true))
	mux.HandleFunc("GET /admin/api/v1/governance/groups/{id}", a.requireAdmin(s.getGovernanceGroup, false))
	mux.HandleFunc("PUT /admin/api/v1/governance/groups/{id}", a.requireAdmin(func(w http.ResponseWriter, r *http.Request, session adminSession) {
		s.putGovernanceGroup(a, w, r, session)
	}, true))
	mux.HandleFunc("GET /admin/api/v1/governance/policies", a.requireAdmin(s.listGovernancePolicies, false))
	mux.HandleFunc("POST /admin/api/v1/governance/policies", a.requireAdmin(func(w http.ResponseWriter, r *http.Request, session adminSession) {
		s.postGovernancePolicy(a, w, r, session)
	}, true))
	mux.HandleFunc("GET /admin/api/v1/governance/policies/{id}", a.requireAdmin(s.getGovernancePolicy, false))
	mux.HandleFunc("PUT /admin/api/v1/governance/policies/{id}", a.requireAdmin(func(w http.ResponseWriter, r *http.Request, session adminSession) {
		s.putGovernancePolicy(a, w, r, session)
	}, true))
	mux.HandleFunc("GET /admin/api/v1/governance/operations/{operation_id}", a.requireAdmin(s.getGovernanceOperation, false))
}

func (s *governanceManagementStore) getGovernanceSettings(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	settings, err := s.core.Settings(ctx)
	if err != nil {
		writeGovernanceManagementError(w, errGovernanceManagementUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, governanceSettingsResponse(settings))
}

func (s *governanceManagementStore) putGovernanceSettings(a *App, w http.ResponseWriter, r *http.Request, session adminSession) {
	if r.URL.RawQuery != "" {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	object, err := decodeUniqueJSONObject(w, r, governanceManagementMaxBody)
	if err != nil || !exactJSONKeys(object, "operation_id", "expected_revision", "enabled") {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	operationID, operationOK := object["operation_id"].(string)
	expected, revisionOK := parseGovernanceJSONRevision(object["expected_revision"])
	enabled, enabledOK := object["enabled"].(bool)
	if !operationOK || !revisionOK || !enabledOK {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	a.admission.Lock()
	receipt, err := s.updateSettings(r.Context(), session.AdminID, operationID, expected, enabled)
	a.admission.Unlock()
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (s *governanceManagementStore) listGovernanceGroups(w http.ResponseWriter, r *http.Request, _ adminSession) {
	limit, after, ok := parseGovernancePage(r)
	if !ok {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	items, next, err := s.listGroups(r.Context(), limit, after)
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *governanceManagementStore) postGovernanceGroup(a *App, w http.ResponseWriter, r *http.Request, session adminSession) {
	if r.URL.RawQuery != "" {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	object, err := decodeUniqueJSONObject(w, r, governanceManagementMaxBody)
	if err != nil || !exactJSONKeys(object, "operation_id", "name", "employee_ids") {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	operationID, operationOK := object["operation_id"].(string)
	name, nameOK := object["name"].(string)
	employees, employeesOK := parseGovernanceStringArray(object["employee_ids"])
	if !operationOK || !nameOK || !employeesOK {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	a.admission.Lock()
	receipt, err := s.createGroup(r.Context(), session.AdminID, operationID, name, employees)
	a.admission.Unlock()
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (s *governanceManagementStore) getGovernanceGroup(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" || !validGovernanceMetadata(r.PathValue("id"), 256) {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	item, err := s.group(r.Context(), r.PathValue("id"))
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *governanceManagementStore) putGovernanceGroup(a *App, w http.ResponseWriter, r *http.Request, session adminSession) {
	if r.URL.RawQuery != "" || !validGovernanceMetadata(r.PathValue("id"), 256) {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	object, err := decodeUniqueJSONObject(w, r, governanceManagementMaxBody)
	if err != nil || !exactJSONKeys(object, "operation_id", "expected_revision", "name", "employee_ids") {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	operationID, operationOK := object["operation_id"].(string)
	expected, revisionOK := parseGovernanceJSONRevision(object["expected_revision"])
	name, nameOK := object["name"].(string)
	employees, employeesOK := parseGovernanceStringArray(object["employee_ids"])
	if !operationOK || !revisionOK || !nameOK || !employeesOK {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	a.admission.Lock()
	receipt, err := s.updateGroup(r.Context(), session.AdminID, operationID, r.PathValue("id"), expected, name, employees)
	a.admission.Unlock()
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (s *governanceManagementStore) listGovernancePolicies(w http.ResponseWriter, r *http.Request, _ adminSession) {
	limit, after, ok := parseGovernancePage(r)
	if !ok {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	items, next, err := s.listPolicies(r.Context(), limit, after)
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (s *governanceManagementStore) postGovernancePolicy(a *App, w http.ResponseWriter, r *http.Request, session adminSession) {
	if r.URL.RawQuery != "" {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	object, err := decodeUniqueJSONObject(w, r, governanceManagementMaxBody)
	if err != nil || !exactJSONKeys(object, "operation_id", "scope_kind", "scope_id", "enabled", "hard", "shadow") {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	operationID, operationOK := object["operation_id"].(string)
	input, inputOK := parseGovernancePolicyJSON(object, true)
	if !operationOK || !inputOK {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	a.admission.Lock()
	receipt, err := s.createPolicy(r.Context(), session.AdminID, operationID, input)
	a.admission.Unlock()
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (s *governanceManagementStore) getGovernancePolicy(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" || !validGovernanceMetadata(r.PathValue("id"), 256) {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	item, err := loadGovernancePolicy(r.Context(), s.db, r.PathValue("id"))
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, governancePolicyResponse(item))
}

func (s *governanceManagementStore) putGovernancePolicy(a *App, w http.ResponseWriter, r *http.Request, session adminSession) {
	if r.URL.RawQuery != "" || !validGovernanceMetadata(r.PathValue("id"), 256) {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	object, err := decodeUniqueJSONObject(w, r, governanceManagementMaxBody)
	if err != nil || !exactJSONKeys(object, "operation_id", "expected_revision", "enabled", "hard", "shadow") {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	operationID, operationOK := object["operation_id"].(string)
	expected, revisionOK := parseGovernanceJSONRevision(object["expected_revision"])
	input, inputOK := parseGovernancePolicyJSON(object, false)
	if !operationOK || !revisionOK || !inputOK {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	a.admission.Lock()
	receipt, err := s.updatePolicy(r.Context(), session.AdminID, operationID, r.PathValue("id"), expected, input)
	a.admission.Unlock()
	if err != nil {
		writeGovernanceManagementError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (s *governanceManagementStore) getGovernanceOperation(w http.ResponseWriter, r *http.Request, _ adminSession) {
	operationID := r.PathValue("operation_id")
	if r.URL.RawQuery != "" || !validGovernanceOperationID(operationID) {
		writeGovernanceManagementError(w, errGovernanceManagementInvalid)
		return
	}
	var receipt governanceOperationReceipt
	err := s.db.QueryRowContext(r.Context(), `SELECT operation_id,resource_kind,resource_id,revision,created_at
		FROM governance_management_operations WHERE operation_id=?`, operationID).Scan(&receipt.OperationID, &receipt.ResourceKind,
		&receipt.ResourceID, &receipt.Revision, &receipt.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		writeGovernanceManagementError(w, errGovernanceManagementNotFound)
		return
	}
	if err != nil {
		writeGovernanceManagementError(w, errGovernanceManagementUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, receipt)
}

func (s *governanceManagementStore) listGroups(ctx context.Context, limit int, after string) ([]governanceGroupView, *string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, errGovernanceManagementUnavailable
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM governance_groups WHERE id>? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, nil, errGovernanceManagementUnavailable
	}
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, errGovernanceManagementUnavailable
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, errGovernanceManagementUnavailable
	}
	if err := rows.Close(); err != nil {
		return nil, nil, errGovernanceManagementUnavailable
	}
	var next *string
	if len(ids) > limit {
		value := ids[limit-1]
		next = &value
		ids = ids[:limit]
	}
	items := make([]governanceGroupView, 0, len(ids))
	for _, id := range ids {
		item, err := loadGovernanceGroup(ctx, tx, id)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	return items, next, nil
}

func (s *governanceManagementStore) listPolicies(ctx context.Context, limit int, after string) ([]governancePolicyHTTPView, *string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM governance_policies WHERE id>? ORDER BY id LIMIT ?`, after, limit+1)
	if err != nil {
		return nil, nil, errGovernanceManagementUnavailable
	}
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, nil, errGovernanceManagementUnavailable
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, nil, errGovernanceManagementUnavailable
	}
	if err := rows.Close(); err != nil {
		return nil, nil, errGovernanceManagementUnavailable
	}
	var next *string
	if len(ids) > limit {
		value := ids[limit-1]
		next = &value
		ids = ids[:limit]
	}
	items := make([]governancePolicyHTTPView, 0, len(ids))
	for _, id := range ids {
		item, err := loadGovernancePolicy(ctx, s.db, id)
		if err != nil {
			return nil, nil, err
		}
		items = append(items, governancePolicyResponse(item))
	}
	return items, next, nil
}

func governanceSettingsResponse(item governance.Settings) map[string]any {
	return map[string]any{"enabled": item.Enabled, "revision": item.Revision, "updated_at": item.UpdatedAt.UTC().Format(time.RFC3339Nano)}
}

func governancePolicyResponse(item governancePolicyView) governancePolicyHTTPView {
	shadow := governanceShadowView{TPM: item.Shadow.TPM, Currency: item.Shadow.Currency, Window: item.Shadow.Window}
	if item.Shadow.CostMicro != nil {
		value := strconv.FormatInt(*item.Shadow.CostMicro, 10)
		shadow.CostMicro = &value
	}
	return governancePolicyHTTPView{item.ID, item.ScopeKind, item.ScopeID, item.Enabled, item.Hard, shadow, item.Revision, item.CreatedAt, item.UpdatedAt}
}

func parseGovernancePolicyJSON(object map[string]any, create bool) (governancePolicyInput, bool) {
	var input governancePolicyInput
	enabled, ok := object["enabled"].(bool)
	if !ok {
		return input, false
	}
	input.Enabled = enabled
	if create {
		kind, kindOK := object["scope_kind"].(string)
		scopeID, scopeOK := object["scope_id"].(string)
		if !kindOK || !scopeOK {
			return input, false
		}
		input.ScopeKind, input.ScopeID = governance.ScopeKind(kind), scopeID
	}
	hard, ok := object["hard"].(map[string]any)
	if !ok || !exactJSONKeys(hard, "rpm", "concurrency") {
		return input, false
	}
	if input.Hard.RPM, ok = parseGovernanceNullableSafeInt(hard["rpm"]); !ok {
		return input, false
	}
	if input.Hard.Concurrency, ok = parseGovernanceNullableSafeInt(hard["concurrency"]); !ok {
		return input, false
	}
	shadow, ok := object["shadow"].(map[string]any)
	if !ok || !exactJSONKeys(shadow, "tpm", "cost_micro", "currency", "window") {
		return input, false
	}
	if input.Shadow.TPM, ok = parseGovernanceNullableSafeInt(shadow["tpm"]); !ok {
		return input, false
	}
	if input.Shadow.CostMicro, ok = parseGovernanceNullableCost(shadow["cost_micro"]); !ok {
		return input, false
	}
	if input.Shadow.Currency, ok = parseGovernanceNullableString(shadow["currency"]); !ok {
		return input, false
	}
	if input.Shadow.Window, ok = parseGovernanceNullableString(shadow["window"]); !ok {
		return input, false
	}
	if create {
		return input, validGovernancePolicyInput(input)
	}
	return input, validGovernancePolicyLimits(input)
}

func parseGovernanceNullableSafeInt(value any) (*int64, bool) {
	if value == nil {
		return nil, true
	}
	parsed, ok := parseGovernanceJSONRevision(value)
	if !ok {
		return nil, false
	}
	return &parsed, true
}

func parseGovernanceNullableCost(value any) (*int64, bool) {
	if value == nil {
		return nil, true
	}
	text, ok := value.(string)
	if !ok || text == "" || text == "0" || len(text) > 19 || len(text) > 1 && text[0] == '0' {
		return nil, false
	}
	for _, character := range text {
		if character < '0' || character > '9' {
			return nil, false
		}
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil || parsed <= 0 {
		return nil, false
	}
	return &parsed, true
}

func parseGovernanceNullableString(value any) (*string, bool) {
	if value == nil {
		return nil, true
	}
	text, ok := value.(string)
	if !ok {
		return nil, false
	}
	return &text, true
}

func parseGovernanceJSONRevision(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	text := number.String()
	if text == "" || text == "0" || len(text) > 1 && text[0] == '0' {
		return 0, false
	}
	for _, character := range text {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(text, 10, 64)
	return parsed, err == nil && validGovernanceRevision(parsed)
}

func parseGovernanceStringArray(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil, false
		}
		result = append(result, text)
	}
	return result, true
}

func parseGovernancePage(r *http.Request) (int, string, bool) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, "", false
	}
	for key, values := range query {
		if key != "limit" && key != "after_id" || len(values) != 1 {
			return 0, "", false
		}
	}
	limit := governanceManagementDefaultPage
	if values, ok := query["limit"]; ok {
		if values[0] == "" || len(values[0]) > 1 && values[0][0] == '0' {
			return 0, "", false
		}
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > governanceManagementMaxPage {
			return 0, "", false
		}
		limit = parsed
	}
	after := ""
	if values, ok := query["after_id"]; ok {
		after = values[0]
		if !validGovernanceMetadata(after, 256) {
			return 0, "", false
		}
	}
	return limit, after, true
}

func writeGovernanceManagementError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errGovernanceManagementInvalid):
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid governance request.")
	case errors.Is(err, errGovernanceManagementNotFound):
		writeAdminError(w, http.StatusNotFound, "not_found", "Governance resource was not found.")
	case errors.Is(err, errGovernanceManagementOperationConflict):
		writeAdminError(w, http.StatusConflict, "operation_conflict", "The operation ID was already used with different input.")
	case errors.Is(err, errGovernanceManagementRevisionConflict):
		writeAdminError(w, http.StatusConflict, "revision_conflict", "Governance configuration changed; reload before updating.")
	case errors.Is(err, errGovernanceManagementResourceConflict):
		writeAdminError(w, http.StatusConflict, "resource_conflict", "Governance configuration conflicts with an existing resource or limit.")
	default:
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Governance configuration is temporarily unavailable.")
	}
}
