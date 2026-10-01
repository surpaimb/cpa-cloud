// Independently authored for docs/employee-self-key-issuance-contract.md.
package service

import (
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type selfArmedKeySlot struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	ExpiresAt *string `json:"expires_at"`
}

func (a *App) selfListKeySlots(w http.ResponseWriter, r *http.Request, session selfSession) {
	if r.URL.RawQuery != "" || r.ContentLength > 0 || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	rows, err := a.store.db.QueryContext(r.Context(), `SELECT k.id,k.name,k.expires_at FROM employee_self_key_slots s JOIN access_keys k ON k.id=s.key_id WHERE s.employee_id=? AND s.state='armed' AND k.revoked_at IS NULL ORDER BY k.id LIMIT 2`, session.EmployeeID)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	items := make([]selfArmedKeySlot, 0, 1)
	for rows.Next() {
		var item selfArmedKeySlot
		var expiry sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &expiry); err != nil {
			rows.Close()
			selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		if !validSelfKeyID(item.ID) || !validText(item.Name, 1, 120) || len(items) != 0 {
			rows.Close()
			selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		if expiry.Valid {
			when, err := parseTime(expiry.String)
			if err != nil {
				rows.Close()
				selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
				return
			}
			if !time.Now().UTC().Before(when) {
				continue
			}
			item.ExpiresAt = &expiry.String
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if err := rows.Close(); err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) selfIssueKey(w http.ResponseWriter, r *http.Request, session selfSession) {
	peer, ok := a.selfGate(w, r, session.EmployeeID)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	input, ok := readSelfFields(w, r, "slot_id", "current_password")
	if !ok {
		a.selfFailure(peer, session.EmployeeID)
		return
	}
	keyID, password := input["slot_id"], input["current_password"]
	if !validSelfKeyID(keyID) {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusNotFound, "not_found")
		return
	}
	if !validSelfPassword(password) {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	var priorHash []byte
	err := a.store.db.QueryRowContext(r.Context(), `SELECT password_hash FROM employee_self_credentials WHERE employee_id=?`, session.EmployeeID).Scan(&priorHash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	comparison := priorHash
	if len(comparison) == 0 {
		comparison = selfDummyHash
	}
	if bcrypt.CompareHashAndPassword(comparison, []byte(password)) != nil || len(priorHash) == 0 {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	selector, err := randomToken(12)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	secret, err := randomToken(32)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	if a.selfKeyIssueBeforeTx != nil {
		a.selfKeyIssueBeforeTx()
	}
	// The read lock held by requireSelfReleased ended before bcrypt. Do not
	// upgrade a read lock; recheck the entire decision in this write transaction.
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	var employeeID, csrf, expires, status string
	var currentHash []byte
	err = tx.QueryRowContext(r.Context(), `SELECT s.employee_id,s.csrf_token,s.expires_at,e.status,c.password_hash
		FROM employee_self_sessions s JOIN employees e ON e.id=s.employee_id
		JOIN employee_self_credentials c ON c.employee_id=s.employee_id WHERE s.selector=?`, session.Selector).
		Scan(&employeeID, &csrf, &expires, &status, &currentHash)
	if errors.Is(err, sql.ErrNoRows) {
		selfError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	validUntil, err := parseTime(expires)
	if err != nil || employeeID != session.EmployeeID || status != "active" || !time.Now().UTC().Before(validUntil) || subtle.ConstantTimeCompare([]byte(csrf), []byte(session.CSRF)) != 1 {
		selfError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	if subtle.ConstantTimeCompare(currentHash, priorHash) != 1 {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	var state, priorSelector string
	var storedFingerprint []byte
	err = tx.QueryRowContext(r.Context(), `SELECT s.state,s.arm_fingerprint,k.selector FROM employee_self_key_slots s JOIN access_keys k ON k.id=s.key_id WHERE s.employee_id=? AND s.key_id=?`, session.EmployeeID, keyID).
		Scan(&state, &storedFingerprint, &priorSelector)
	if errors.Is(err, sql.ErrNoRows) {
		selfError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if state != "armed" || len(storedFingerprint) != sha256.Size {
		selfError(w, http.StatusConflict, "slot_unavailable")
		return
	}
	snapshot, err := captureSelfSlotSnapshot(r.Context(), tx, session.EmployeeID, keyID)
	if errors.Is(err, errSelfSlotNotReady) {
		selfError(w, http.StatusConflict, "slot_changed")
		return
	}
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if subtle.ConstantTimeCompare(snapshot.Digest[:], storedFingerprint) != 1 {
		selfError(w, http.StatusConflict, "slot_changed")
		return
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE access_keys SET selector=?,digest=? WHERE id=? AND employee_id=? AND selector=? AND revoked_at IS NULL`, selector, a.secrets.digest("employee-key/v1\x00"+selector, secret), keyID, session.EmployeeID, priorSelector)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	result, err = tx.ExecContext(r.Context(), `UPDATE employee_self_key_slots SET state='issued',issued_at=? WHERE employee_id=? AND key_id=? AND state='armed' AND arm_fingerprint=?`, selfTime(time.Now()), session.EmployeeID, keyID, storedFingerprint)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	commit := tx.Commit
	if a.selfKeyIssueCommit != nil {
		commit = func() error { return a.selfKeyIssueCommit(tx) }
	}
	if err := commit(); err != nil {
		a.notifyAccountPoolChanged()
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.notifyAccountPoolChanged()
	a.clearSelfFailures(peer, session.EmployeeID)
	writeJSON(w, http.StatusCreated, map[string]any{"id": keyID, "name": snapshot.Name, "expires_at": snapshot.ExpiresAt, "key": "cpac_" + selector + "." + secret})
}
