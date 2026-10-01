// Independently authored for docs/employee-self-key-issuance-contract.md.
package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"cpacloud.local/server/internal/keypolicy"
)

func readSelfSlotJSON(w http.ResponseWriter, r *http.Request, max int64, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	raw, err := io.ReadAll(r.Body)
	if err != nil || !utf8.Valid(raw) {
		writeAdminError(w, 400, "invalid_request", "Invalid request.")
		return false
	}
	check := json.NewDecoder(bytes.NewReader(raw))
	check.UseNumber()
	first, err := check.Token()
	if err != nil || first != json.Delim('{') || validateSelfSlotJSONObject(check, 0) != nil {
		writeAdminError(w, 400, "invalid_request", "Invalid request.")
		return false
	}
	if _, err := check.Token(); !errors.Is(err, io.EOF) {
		writeAdminError(w, 400, "invalid_request", "Invalid request.")
		return false
	}
	decode := json.NewDecoder(bytes.NewReader(raw))
	decode.DisallowUnknownFields()
	if decode.Decode(target) != nil {
		writeAdminError(w, 400, "invalid_request", "Invalid request.")
		return false
	}
	return true
}

func validateSelfSlotJSONObject(decoder *json.Decoder, depth int) error {
	if depth > 12 {
		return errors.New("JSON nesting limit")
	}
	seen := make(map[string]bool)
	for decoder.More() {
		name, err := decoder.Token()
		key, ok := name.(string)
		if err != nil || !ok || seen[key] {
			return errors.New("duplicate or invalid JSON name")
		}
		seen[key] = true
		if err := validateSelfSlotJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return errors.New("invalid JSON object")
	}
	return nil
}

func validateSelfSlotJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 12 {
		return errors.New("JSON nesting limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); ok {
		switch delim {
		case '{':
			return validateSelfSlotJSONObject(decoder, depth)
		case '[':
			for decoder.More() {
				if err := validateSelfSlotJSONValue(decoder, depth+1); err != nil {
					return err
				}
			}
			end, err := decoder.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("invalid JSON array")
			}
			return nil
		default:
			return errors.New("unexpected JSON delimiter")
		}
	}
	return nil
}

func selfSlotAdminError(w http.ResponseWriter, err error) {
	if errors.Is(err, errSelfSlotNotReady) {
		writeAdminError(w, 409, "slot_not_ready", "Key slot is not ready.")
		return
	}
	writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
}

type reserveSelfKeySlotRequest struct {
	Name        string                `json:"name"`
	OperationID string                `json:"operation_id"`
	ExpiresAt   *string               `json:"expires_at"`
	Policy      keypolicy.Replacement `json:"policy"`
}

func (a *App) reserveSelfKeySlot(w http.ResponseWriter, r *http.Request, admin adminSession) {
	if r.URL.RawQuery != "" {
		writeAdminError(w, 400, "invalid_request", "Invalid request.")
		return
	}
	var input reserveSelfKeySlotRequest
	if !readSelfSlotJSON(w, r, adminMaxBody, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validText(input.Name, 1, 120) || !validIdentifier(input.OperationID, 110) {
		writeAdminError(w, 400, "invalid_request", "Invalid request.")
		return
	}
	policy, err := keypolicy.Normalize(input.Policy)
	if err != nil || policy.ProtocolMode != keypolicy.ModeSelected || len(policy.Protocols) == 0 || policy.ModelMode != keypolicy.ModeSelected || len(policy.Models) == 0 {
		writeAdminError(w, 400, "invalid_key_policy", "Invalid key policy.")
		return
	}
	var expires any
	if input.ExpiresAt != nil {
		when, err := time.Parse(time.RFC3339, *input.ExpiresAt)
		if err != nil || !when.After(time.Now().UTC()) {
			writeAdminError(w, 400, "invalid_request", "Invalid request.")
			return
		}
		normalized := when.UTC().Format(time.RFC3339Nano)
		input.ExpiresAt = &normalized
		expires = normalized
	}
	fingerprint, err := keyCreateFingerprint(input.Name, input.ExpiresAt, policy)
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	employeeID := r.PathValue("id")
	if !validIdentifier(employeeID, 128) {
		writeAdminError(w, 404, "not_found", "Employee was not found.")
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	defer tx.Rollback()
	var priorID, priorName, priorOperation, priorFingerprint, priorState string
	var priorExpires sql.NullString
	err = tx.QueryRowContext(r.Context(), `SELECT k.id,k.name,k.operation_id,k.operation_fingerprint,k.expires_at,s.state FROM employee_self_key_slots s JOIN access_keys k ON k.id=s.key_id WHERE s.employee_id=?`, employeeID).Scan(&priorID, &priorName, &priorOperation, &priorFingerprint, &priorExpires, &priorState)
	if err == nil {
		if priorOperation != "selfslot:"+input.OperationID || priorFingerprint != fingerprint {
			writeAdminError(w, 409, "slot_already_reserved", "A Key slot already exists.")
			return
		}
		view, err := readKeyPolicyViewTx(r.Context(), tx, a, priorID)
		if err != nil {
			selfSlotAdminError(w, err)
			return
		}
		if err := tx.Commit(); err != nil {
			selfSlotAdminError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"id": priorID, "name": priorName, "issuance_state": priorState, "expires_at": nullString(priorExpires), "policy": view})
		return
	}
	if !errors.Is(err, sql.ErrNoRows) {
		selfSlotAdminError(w, err)
		return
	}
	var status string
	var passwordHash []byte
	err = tx.QueryRowContext(r.Context(), `SELECT e.status,c.password_hash FROM employees e LEFT JOIN employee_self_credentials c ON c.employee_id=e.id WHERE e.id=?`, employeeID).Scan(&status, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, 404, "not_found", "Employee was not found.")
		return
	}
	if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	if status != "active" || len(passwordHash) == 0 {
		writeAdminError(w, 409, "slot_not_ready", "Key slot is not ready.")
		return
	}
	owner, err := loadKeyPolicyCreateOwnerTx(r.Context(), tx, employeeID)
	if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	if err := a.validateKeyPolicyModelsTx(r.Context(), tx, owner, policy); err != nil {
		writeKeyPolicyAdminError(w, err)
		return
	}
	keyID, err := newID("key")
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	selector, err := randomToken(12)
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	placeholder, err := randomToken(32)
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	selector = "slot_" + selector
	now := time.Now().UTC()
	_, err = tx.ExecContext(r.Context(), `INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,operation_fingerprint,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, keyID, employeeID, input.Name, selector, a.secrets.digest("employee-key/v1\x00"+selector, placeholder), 1, "selfslot:"+input.OperationID, fingerprint, expires, now.Format(time.RFC3339Nano))
	if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	created, err := keypolicy.CreateTx(r.Context(), tx, keyID, policy, now)
	if err != nil {
		writeKeyPolicyAdminError(w, err)
		return
	}
	view, err := readKeyPolicyViewTxWithPolicy(r.Context(), tx, a, keyID, owner, created)
	if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO employee_self_key_slots(employee_id,key_id,state,created_at) VALUES(?,?,'pending',?)`, employeeID, keyID, selfTime(now))
	if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	if err := recordAccountPoolAudit(r.Context(), tx, admin.AdminID, "key_policy.create", "access_key", keyID); err != nil {
		selfSlotAdminError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		selfSlotAdminError(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": keyID, "name": input.Name, "issuance_state": "pending", "expires_at": input.ExpiresAt, "policy": view})
}

type armSelfKeySlotRequest struct {
	ExpectedEmployeeRevision  int64 `json:"expected_employee_revision"`
	ExpectedKeyPolicyRevision int64 `json:"expected_key_policy_revision"`
}

func (a *App) armSelfKeySlot(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" {
		writeAdminError(w, 400, "invalid_request", "Invalid request.")
		return
	}
	var input armSelfKeySlotRequest
	if !readSelfSlotJSON(w, r, adminMaxBody, &input) {
		return
	}
	if input.ExpectedEmployeeRevision < 1 || input.ExpectedKeyPolicyRevision < 1 {
		writeAdminError(w, 400, "invalid_request", "Invalid request.")
		return
	}
	keyID := r.PathValue("id")
	if !validIdentifier(keyID, 128) {
		writeAdminError(w, 404, "not_found", "Key slot was not found.")
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	defer tx.Rollback()
	var employeeID, state string
	if err := tx.QueryRowContext(r.Context(), `SELECT employee_id,state FROM employee_self_key_slots WHERE key_id=?`, keyID).Scan(&employeeID, &state); errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, 404, "not_found", "Key slot was not found.")
		return
	} else if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	if state != "pending" && state != "armed" {
		writeAdminError(w, 409, "slot_unavailable", "Key slot is unavailable.")
		return
	}
	snapshot, err := captureSelfSlotSnapshot(r.Context(), tx, employeeID, keyID)
	if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	if snapshot.EmployeeRevision != input.ExpectedEmployeeRevision || snapshot.KeyPolicyRevision != input.ExpectedKeyPolicyRevision {
		writeAdminError(w, 409, "revision_conflict", "The object was changed by another request.")
		return
	}
	res, err := tx.ExecContext(r.Context(), `UPDATE employee_self_key_slots SET state='armed',arm_fingerprint=?,arm_employee_revision=?,arm_key_policy_revision=?,arm_governance_revision=?,arm_budget_revision=?,armed_at=? WHERE key_id=? AND state IN ('pending','armed')`, snapshot.Digest[:], snapshot.EmployeeRevision, snapshot.KeyPolicyRevision, snapshot.GovernanceRevision, snapshot.BudgetRevision, selfTime(time.Now()), keyID)
	if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	changed, err := res.RowsAffected()
	if err != nil || changed != 1 {
		selfSlotAdminError(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		selfSlotAdminError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"id": keyID, "issuance_state": "armed", "employee_revision": snapshot.EmployeeRevision, "key_policy_revision": snapshot.KeyPolicyRevision, "governance_revision": snapshot.GovernanceRevision, "budget_revision": snapshot.BudgetRevision})
}

func (a *App) cancelSelfKeySlot(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" {
		writeAdminError(w, 400, "invalid_request", "Invalid request.")
		return
	}
	var input struct{}
	if !readSelfSlotJSON(w, r, adminMaxBody, &input) {
		return
	}
	keyID := r.PathValue("id")
	if !validIdentifier(keyID, 128) {
		writeAdminError(w, 404, "not_found", "Key slot was not found.")
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRowContext(r.Context(), `SELECT state FROM employee_self_key_slots WHERE key_id=?`, keyID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, 404, "not_found", "Key slot was not found.")
		return
	} else if err != nil {
		selfSlotAdminError(w, err)
		return
	}
	if state == "issued" {
		writeAdminError(w, 409, "slot_unavailable", "Key slot is unavailable.")
		return
	}
	if state != "cancelled" {
		if err := cancelSelfKeySlotTx(r.Context(), tx, keyID); err != nil {
			selfSlotAdminError(w, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		selfSlotAdminError(w, err)
		return
	}
	a.notifyAccountPoolChanged()
	w.WriteHeader(http.StatusNoContent)
}

func cancelSelfKeySlotTx(ctx context.Context, tx *sql.Tx, keyID string) error {
	stamp := selfTime(time.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE access_keys SET revoked_at=COALESCE(revoked_at,?) WHERE id=?`, stamp, keyID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE employee_self_key_slots SET state='cancelled',arm_fingerprint=NULL,arm_employee_revision=NULL,arm_key_policy_revision=NULL,arm_governance_revision=NULL,arm_budget_revision=NULL,cancelled_at=? WHERE key_id=? AND state IN ('pending','armed')`, stamp, keyID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errSelfSlotNotReady
	}
	return nil
}
