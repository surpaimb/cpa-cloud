package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"cpacloud.local/server/internal/keypolicy"
)

type employee struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Department string   `json:"department"`
	Note       string   `json:"note"`
	Status     string   `json:"status"`
	ModelMode  string   `json:"model_mode"`
	Models     []string `json:"models"`
	Revision   int64    `json:"revision"`
}

func (a *App) listEmployees(w http.ResponseWriter, r *http.Request, _ adminSession) {
	rows, err := a.store.db.QueryContext(r.Context(), `SELECT id,name,department,note,status,model_mode,revision FROM employees ORDER BY created_at,id`)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer rows.Close()
	items := make([]employee, 0)
	for rows.Next() {
		var item employee
		if err := rows.Scan(&item.ID, &item.Name, &item.Department, &item.Note, &item.Status, &item.ModelMode, &item.Revision); err != nil {
			writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	for i := range items {
		items[i].Models, err = a.employeeModels(r.Context(), items[i].ID)
		if err != nil {
			writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type createEmployeeRequest struct {
	Name       string `json:"name"`
	Department string `json:"department"`
	Note       string `json:"note"`
}

func (a *App) createEmployee(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var input createEmployeeRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validText(input.Name, 1, 120) || !validText(input.Department, 0, 120) || !validText(input.Note, 0, 2000) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid employee fields.")
		return
	}
	id, err := newID("emp")
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	item := employee{ID: id, Name: input.Name, Department: input.Department, Note: input.Note, Status: "active", ModelMode: "all", Models: []string{}, Revision: 1}
	_, err = a.store.db.ExecContext(r.Context(), `INSERT INTO employees(id,name,department,note,status,model_mode,revision,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		item.ID, item.Name, item.Department, item.Note, item.Status, item.ModelMode, item.Revision, utcNow())
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

type updateEmployeeRequest struct {
	ExpectedRevision int64   `json:"expected_revision"`
	Name             *string `json:"name"`
	Department       *string `json:"department"`
	Note             *string `json:"note"`
	Status           *string `json:"status"`
}

func (a *App) updateEmployee(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var input updateEmployeeRequest
	if !decodeJSON(w, r, adminMaxBody, &input) || input.ExpectedRevision < 1 {
		return
	}
	if input.Name == nil && input.Department == nil && input.Note == nil && input.Status == nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "No employee changes were provided.")
		return
	}
	id := r.PathValue("id")
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer tx.Rollback()
	item, err := loadEmployee(r.Context(), tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Employee was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if item.Revision != input.ExpectedRevision {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	if input.Name != nil {
		*input.Name = strings.TrimSpace(*input.Name)
		if !validText(*input.Name, 1, 120) {
			writeAdminError(w, 400, "invalid_request", "Invalid employee fields.")
			return
		}
		item.Name = *input.Name
	}
	if input.Department != nil {
		if !validText(*input.Department, 0, 120) {
			writeAdminError(w, 400, "invalid_request", "Invalid employee fields.")
			return
		}
		item.Department = *input.Department
	}
	if input.Note != nil {
		if !validText(*input.Note, 0, 2000) {
			writeAdminError(w, 400, "invalid_request", "Invalid employee fields.")
			return
		}
		item.Note = *input.Note
	}
	if input.Status != nil {
		if *input.Status != "active" && *input.Status != "disabled" {
			writeAdminError(w, 400, "invalid_request", "Invalid employee status.")
			return
		}
		item.Status = *input.Status
	}
	item.Revision++
	res, err := tx.ExecContext(r.Context(), `UPDATE employees SET name=?,department=?,note=?,status=?,revision=? WHERE id=? AND revision=?`, item.Name, item.Department, item.Note, item.Status, item.Revision, id, input.ExpectedRevision)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		writeAdminError(w, 409, "revision_conflict", "The object was changed by another request.")
		return
	}
	if item.Status == "disabled" {
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM employee_self_sessions WHERE employee_id=?`, id); err != nil {
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM employee_self_credentials WHERE employee_id=?`, id); err != nil {
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	a.notifyAccountPoolChanged()
	item.Models, err = a.employeeModels(r.Context(), item.ID)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

type modelPolicyRequest struct {
	ExpectedRevision int64    `json:"expected_revision"`
	Mode             string   `json:"mode"`
	Models           []string `json:"models"`
}

func (a *App) updateModelPolicy(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var input modelPolicyRequest
	if !decodeJSON(w, r, adminMaxBody, &input) || input.ExpectedRevision < 1 {
		return
	}
	if input.Mode != "all" && input.Mode != "selected" {
		writeAdminError(w, 400, "invalid_request", "Invalid model policy.")
		return
	}
	if input.Models == nil {
		input.Models = []string{}
	}
	input.Models = uniqueSorted(input.Models)
	if input.Mode == "all" && len(input.Models) != 0 {
		writeAdminError(w, 400, "invalid_request", "All mode must not include model IDs.")
		return
	}
	for _, modelID := range input.Models {
		if !validIdentifier(modelID, 128) {
			writeAdminError(w, 400, "invalid_request", "Invalid model policy.")
			return
		}
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer tx.Rollback()
	item, err := loadEmployee(r.Context(), tx, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, 404, "not_found", "Employee was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if item.Revision != input.ExpectedRevision {
		writeAdminError(w, 409, "revision_conflict", "The object was changed by another request.")
		return
	}
	for _, modelID := range input.Models {
		var exists int
		if err := tx.QueryRowContext(r.Context(), `SELECT 1 FROM models WHERE id=? AND archived=0`, modelID).Scan(&exists); err != nil {
			writeAdminError(w, 400, "invalid_request", "Model policy refers to an unknown model.")
			return
		}
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM employee_models WHERE employee_id=?`, item.ID); err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	for _, modelID := range input.Models {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO employee_models(employee_id,model_id) VALUES(?,?)`, item.ID, modelID); err != nil {
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
	}
	item.ModelMode, item.Models, item.Revision = input.Mode, input.Models, item.Revision+1
	res, err := tx.ExecContext(r.Context(), `UPDATE employees SET model_mode=?,revision=? WHERE id=? AND revision=?`, item.ModelMode, item.Revision, item.ID, input.ExpectedRevision)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		writeAdminError(w, 409, "revision_conflict", "The object was changed by another request.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	a.notifyAccountPoolChanged()
	writeJSON(w, 200, item)
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadEmployee(ctx context.Context, q queryRower, id string) (employee, error) {
	var item employee
	err := q.QueryRowContext(ctx, `SELECT id,name,department,note,status,model_mode,revision FROM employees WHERE id=?`, id).
		Scan(&item.ID, &item.Name, &item.Department, &item.Note, &item.Status, &item.ModelMode, &item.Revision)
	item.Models = []string{}
	return item, err
}

func (a *App) employeeModels(ctx context.Context, id string) ([]string, error) {
	rows, err := a.store.db.QueryContext(ctx, `SELECT model_id FROM employee_models WHERE employee_id=? ORDER BY model_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := make([]string, 0)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		models = append(models, model)
	}
	return models, rows.Err()
}

type keyView struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Key           string        `json:"key,omitempty"`
	IssuanceState *string       `json:"issuance_state,omitempty"`
	ExpiresAt     *string       `json:"expires_at"`
	RevokedAt     *string       `json:"revoked_at"`
	Policy        keyPolicyView `json:"policy"`
}

func (a *App) listKeys(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var exists int
	if err := a.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM employees WHERE id=?`, r.PathValue("id")).Scan(&exists); err != nil {
		writeAdminError(w, 404, "not_found", "Employee was not found.")
		return
	}
	rows, err := a.store.db.QueryContext(r.Context(), `SELECT k.id,k.name,k.expires_at,k.revoked_at,s.state FROM access_keys k LEFT JOIN employee_self_key_slots s ON s.key_id=k.id WHERE k.employee_id=? ORDER BY k.created_at,k.id`, r.PathValue("id"))
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	items := make([]keyView, 0)
	for rows.Next() {
		var item keyView
		var exp, rev, issuance sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &exp, &rev, &issuance); err != nil {
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
		item.ExpiresAt = nullString(exp)
		item.RevokedAt = nullString(rev)
		item.IssuanceState = nullString(issuance)
		items = append(items, item)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	for index := range items {
		policy, err := a.readKeyPolicyView(r.Context(), items[index].ID)
		if err != nil {
			writeKeyPolicyAdminError(w, err)
			return
		}
		items[index].Policy = policy
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

type createKeyRequest struct {
	Name        string                 `json:"name"`
	OperationID string                 `json:"operation_id"`
	ExpiresAt   *string                `json:"expires_at"`
	Policy      *keypolicy.Replacement `json:"policy"`
}

func (a *App) createKey(w http.ResponseWriter, r *http.Request, session adminSession) {
	var input createKeyRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.OperationID = strings.TrimSpace(input.OperationID)
	if !validText(input.Name, 1, 120) || !validIdentifier(input.OperationID, 128) {
		writeAdminError(w, 400, "invalid_request", "Invalid key fields.")
		return
	}
	replacement := keypolicy.Replacement{
		ProtocolMode: keypolicy.ModeSelected, Protocols: append([]keypolicy.ClientProtocol(nil), keypolicy.LegacyClientProtocols...),
		ModelMode: keypolicy.ModeAll, Models: []string{},
		SourceMode: keypolicy.ModeAll, SourceCIDRs: []string{},
		AccountGroupMode: keypolicy.ModeAll, AccountGroupIDs: []string{},
	}
	if input.Policy != nil {
		replacement = *input.Policy
		if replacement.SourceMode == "" && replacement.SourceCIDRs == nil {
			replacement.SourceMode = keypolicy.ModeAll
			replacement.SourceCIDRs = []string{}
		}
		if replacement.AccountGroupMode == "" && replacement.AccountGroupIDs == nil {
			replacement.AccountGroupMode = keypolicy.ModeAll
			replacement.AccountGroupIDs = []string{}
		}
	}
	normalizedPolicy, err := keypolicy.Normalize(replacement)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_key_policy", "Invalid key policy.")
		return
	}
	var expires any
	if input.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *input.ExpiresAt)
		if err != nil || !t.After(time.Now().UTC()) {
			writeAdminError(w, 400, "invalid_request", "Key expiry must be a future RFC3339 time.")
			return
		}
		normalized := t.UTC().Format(time.RFC3339)
		input.ExpiresAt = &normalized
		expires = normalized
	}
	fingerprint, err := keyCreateFingerprint(input.Name, input.ExpiresAt, normalizedPolicy)
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}

	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer tx.Rollback()
	var prior keyView
	var exp, rev sql.NullString
	var priorFingerprint string
	err = tx.QueryRowContext(r.Context(), `SELECT id,name,expires_at,revoked_at,operation_fingerprint FROM access_keys WHERE employee_id=? AND operation_id=?`, r.PathValue("id"), input.OperationID).Scan(&prior.ID, &prior.Name, &exp, &rev, &priorFingerprint)
	if err == nil {
		prior.ExpiresAt = nullString(exp)
		prior.RevokedAt = nullString(rev)
		if !keyCreateRetryMatches(prior, priorFingerprint, input, fingerprint, normalizedPolicy) {
			writeAdminError(w, http.StatusConflict, "operation_conflict", "The operation ID was already used with different key fields.")
			return
		}
		prior.Policy, err = readKeyPolicyViewTx(r.Context(), tx, a, prior.ID)
		if err != nil {
			writeKeyPolicyAdminError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
		writeJSON(w, 200, prior)
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	owner, err := loadKeyPolicyCreateOwnerTx(r.Context(), tx, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, 404, "not_found", "Employee was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if err := a.validateKeyPolicyModelsTx(r.Context(), tx, owner, normalizedPolicy); err != nil {
		writeKeyPolicyAdminError(w, err)
		return
	}
	id, err := newID("key")
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	selector, err := randomToken(12)
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	secret, err := randomToken(32)
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	full := "cpac_" + selector + "." + secret
	now := time.Now().UTC()
	_, err = tx.ExecContext(r.Context(), `INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,operation_fingerprint,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, r.PathValue("id"), input.Name, selector, a.secrets.digest("employee-key/v1\x00"+selector, secret), 1, input.OperationID, fingerprint, expires, now.Format(time.RFC3339Nano))
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	policy, err := keypolicy.CreateTx(r.Context(), tx, id, normalizedPolicy, now)
	if err != nil {
		writeKeyPolicyAdminError(w, err)
		return
	}
	view, err := readKeyPolicyViewTxWithPolicy(r.Context(), tx, a, id, owner, policy)
	if err != nil {
		writeKeyPolicyAdminError(w, err)
		return
	}
	if err := recordAccountPoolAudit(r.Context(), tx, session.AdminID, "key_policy.create", "access_key", id); err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, keyView{ID: id, Name: input.Name, Key: full, ExpiresAt: input.ExpiresAt, Policy: view})
}

func loadKeyPolicyCreateOwnerTx(ctx context.Context, tx *sql.Tx, employeeID string) (keyPolicyOwner, error) {
	var owner keyPolicyOwner
	err := tx.QueryRowContext(ctx, `SELECT id,model_mode,status FROM employees WHERE id=?`, employeeID).
		Scan(&owner.EmployeeID, &owner.EmployeeMode, &owner.Status)
	return owner, err
}

func keyCreateFingerprint(name string, expiresAt *string, policy keypolicy.Replacement) (string, error) {
	canonical := struct {
		Name      string                `json:"name"`
		ExpiresAt *string               `json:"expires_at"`
		Policy    keypolicy.Replacement `json:"policy"`
	}{Name: name, ExpiresAt: expiresAt, Policy: policy}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func keyCreateRetryMatches(prior keyView, storedFingerprint string, input createKeyRequest, requestedFingerprint string, policy keypolicy.Replacement) bool {
	if storedFingerprint != "" {
		return storedFingerprint == requestedFingerprint
	}
	legacyAll := policy.ProtocolMode == keypolicy.ModeAll && len(policy.Protocols) == 0 && policy.ModelMode == keypolicy.ModeAll && len(policy.Models) == 0 && policy.SourceMode == keypolicy.ModeAll && len(policy.SourceCIDRs) == 0 && policy.AccountGroupMode == keypolicy.ModeAll && len(policy.AccountGroupIDs) == 0
	return legacyAll && prior.Name == input.Name && sameOptionalString(prior.ExpiresAt, input.ExpiresAt)
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

// Independently authored for docs/admin-key-revocation-confirmation-contract.md.
// Private operation-owned test seams; production requests have none.
type adminKeyRevocationHooksKey struct{}
type adminKeyRevocationHooks struct {
	beforeBegin  func(context.Context) error
	afterBegin   func(*sql.Tx) error
	afterRead    func(*sql.Tx) error
	beforeCommit func(*sql.Tx) error
	afterCommit  func(error) error
	afterNotify  func()
}

type adminKeyRevocationSlot struct {
	employeeID, keyID, state, createdAt                                  string
	fingerprint                                                          []byte
	employeeRevision, policyRevision, governanceRevision, budgetRevision sql.NullInt64
	armedAt, issuedAt, cancelledAt                                       sql.NullString
}

type adminKeyRevocationState struct {
	employeeID string
	revoked    sql.NullString
	slot       *adminKeyRevocationSlot
}

func validAdminKeyRevocationTime(value sql.NullString) bool {
	if !value.Valid {
		return false
	}
	parsed, err := parseTime(value.String)
	return err == nil && !parsed.IsZero()
}

func readAdminKeyRevocationTx(ctx context.Context, tx *sql.Tx, keyID string) (adminKeyRevocationState, error) {
	var state adminKeyRevocationState
	var kind string
	err := tx.QueryRowContext(ctx, `SELECT employee_id,revoked_at,typeof(revoked_at) FROM access_keys WHERE id=?`, keyID).Scan(&state.employeeID, &state.revoked, &kind)
	if err != nil {
		return state, err
	} // Only this first lookup may mean a genuinely missing Key.
	if state.employeeID == "" || (kind != "null" && kind != "text") || (state.revoked.Valid && !validAdminKeyRevocationTime(state.revoked)) {
		return state, errors.New("invalid revocation Key state")
	}
	var owner string
	if tx.QueryRowContext(ctx, `SELECT id FROM employees WHERE id=?`, state.employeeID).Scan(&owner) != nil || owner != state.employeeID {
		return state, errors.New("invalid revocation Key owner")
	}
	slot := &adminKeyRevocationSlot{}
	err = tx.QueryRowContext(ctx, `SELECT employee_id,key_id,state,created_at,arm_fingerprint,arm_employee_revision,arm_key_policy_revision,arm_governance_revision,arm_budget_revision,armed_at,issued_at,cancelled_at FROM employee_self_key_slots WHERE key_id=?`, keyID).Scan(
		&slot.employeeID, &slot.keyID, &slot.state, &slot.createdAt, &slot.fingerprint, &slot.employeeRevision, &slot.policyRevision, &slot.governanceRevision, &slot.budgetRevision, &slot.armedAt, &slot.issuedAt, &slot.cancelledAt)
	if errors.Is(err, sql.ErrNoRows) {
		return state, nil
	}
	if err != nil {
		return state, errors.New("read revocation slot failed")
	}
	if slot.employeeID != state.employeeID || slot.keyID != keyID || !validAdminKeyRevocationTime(sql.NullString{String: slot.createdAt, Valid: true}) {
		return state, errors.New("invalid revocation slot owner")
	}
	armed := slot.state == "armed" || slot.state == "issued"
	if armed {
		if len(slot.fingerprint) != 32 || !validAdminKeyRevocationTime(slot.armedAt) ||
			!slot.employeeRevision.Valid || slot.employeeRevision.Int64 < 1 || !slot.policyRevision.Valid || slot.policyRevision.Int64 < 1 ||
			!slot.governanceRevision.Valid || slot.governanceRevision.Int64 < 1 || !slot.budgetRevision.Valid || slot.budgetRevision.Int64 < 1 {
			return state, errors.New("invalid revocation slot activation")
		}
	} else if len(slot.fingerprint) != 0 || slot.employeeRevision.Valid || slot.policyRevision.Valid || slot.governanceRevision.Valid || slot.budgetRevision.Valid {
		return state, errors.New("invalid revocation slot inactive snapshot")
	}
	if (slot.issuedAt.Valid != (slot.state == "issued")) || (slot.issuedAt.Valid && !validAdminKeyRevocationTime(slot.issuedAt)) ||
		(slot.cancelledAt.Valid != (slot.state == "cancelled")) || (slot.cancelledAt.Valid && !validAdminKeyRevocationTime(slot.cancelledAt)) ||
		(slot.armedAt.Valid && !validAdminKeyRevocationTime(slot.armedAt)) {
		return state, errors.New("invalid revocation slot times")
	}
	switch slot.state {
	case "pending", "armed", "issued", "cancelled":
	default:
		return state, errors.New("invalid revocation slot state")
	}
	state.slot = slot
	return state, nil
}

func sameAdminKeyRevocationSlot(left, right *adminKeyRevocationSlot) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.employeeID == right.employeeID && left.keyID == right.keyID && left.state == right.state && left.createdAt == right.createdAt &&
		string(left.fingerprint) == string(right.fingerprint) && left.employeeRevision == right.employeeRevision && left.policyRevision == right.policyRevision &&
		left.governanceRevision == right.governanceRevision && left.budgetRevision == right.budgetRevision && left.armedAt == right.armedAt &&
		left.issuedAt == right.issuedAt && left.cancelledAt == right.cancelledAt
}

func confirmAdminKeyRevocation(before, after adminKeyRevocationState) bool {
	if after.employeeID != before.employeeID || !validAdminKeyRevocationTime(after.revoked) || (before.revoked.Valid && before.revoked != after.revoked) {
		return false
	}
	if before.slot != nil && (before.slot.state == "pending" || before.slot.state == "armed") {
		slot := after.slot
		return slot != nil && slot.employeeID == before.slot.employeeID && slot.keyID == before.slot.keyID && slot.createdAt == before.slot.createdAt &&
			slot.state == "cancelled" && validAdminKeyRevocationTime(slot.cancelledAt) && !slot.armedAt.Valid && !slot.issuedAt.Valid &&
			len(slot.fingerprint) == 0 && !slot.employeeRevision.Valid && !slot.policyRevision.Valid && !slot.governanceRevision.Valid && !slot.budgetRevision.Valid
	}
	return sameAdminKeyRevocationSlot(before.slot, after.slot)
}

func (a *App) revokeKey(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var input struct{}
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	fail := func() {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
	}
	hooks, _ := r.Context().Value(adminKeyRevocationHooksKey{}).(adminKeyRevocationHooks)
	if hooks.beforeBegin != nil {
		if hooks.beforeBegin(r.Context()) != nil {
			fail()
			return
		}
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		fail()
		return
	}
	defer tx.Rollback()
	if hooks.afterBegin != nil {
		if hooks.afterBegin(tx) != nil {
			fail()
			return
		}
	}
	keyID := r.PathValue("id")
	before, err := readAdminKeyRevocationTx(r.Context(), tx, keyID)
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Key was not found.")
		return
	}
	if err != nil {
		fail()
		return
	}
	if hooks.afterRead != nil {
		if hooks.afterRead(tx) != nil {
			fail()
			return
		}
	}
	if before.slot != nil && (before.slot.state == "pending" || before.slot.state == "armed") {
		if cancelSelfKeySlotTx(r.Context(), tx, keyID) != nil {
			fail()
			return
		}
		// This operation additionally removes the unused activation timestamp;
		// the shared helper and other cancellation routes remain frozen.
		result, err := tx.ExecContext(r.Context(), `UPDATE employee_self_key_slots SET armed_at=NULL WHERE key_id=? AND employee_id=?`, keyID, before.employeeID)
		if err != nil {
			fail()
			return
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			fail()
			return
		}
	} else if !before.revoked.Valid {
		result, err := tx.ExecContext(r.Context(), `UPDATE access_keys SET revoked_at=COALESCE(revoked_at,?) WHERE id=? AND employee_id=?`, utcNow(), keyID, before.employeeID)
		if err != nil {
			fail()
			return
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			fail()
			return
		}
	}
	after, err := readAdminKeyRevocationTx(r.Context(), tx, keyID)
	if err != nil || !confirmAdminKeyRevocation(before, after) {
		fail()
		return
	}
	if hooks.beforeCommit != nil {
		if hooks.beforeCommit(tx) != nil {
			fail()
			return
		}
	}
	commitErr := tx.Commit()
	if hooks.afterCommit != nil {
		reportErr := hooks.afterCommit(commitErr)
		if commitErr == nil {
			commitErr = reportErr
		} // Never erase an actual native failure.
	}
	if commitErr != nil {
		fail()
		return
	}
	a.notifyAccountPoolChanged()
	if hooks.afterNotify != nil {
		hooks.afterNotify()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func validText(value string, min, max int) bool {
	n := len([]byte(value))
	return n >= min && n <= max && !strings.ContainsRune(value, '\x00')
}
func validIdentifier(value string, max int) bool {
	if !validText(value, 1, max) {
		return false
	}
	for _, r := range value {
		if !(r == '-' || r == '_' || r == '.' || r == ':' || r == '/' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}
func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
func nullString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return &v.String
}
