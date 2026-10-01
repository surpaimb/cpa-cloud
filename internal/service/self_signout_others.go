// Independently authored for docs/employee-self-signout-others-contract.md.
package service

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func (a *App) selfSignOutOthers(w http.ResponseWriter, r *http.Request, session selfSession) {
	peer, ok := a.selfGate(w, r, session.EmployeeID)
	if !ok {
		return
	}
	if r.URL.RawQuery != "" {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	input, ok := readSelfFields(w, r, "current_password")
	if !ok {
		a.selfFailure(peer, session.EmployeeID)
		return
	}
	password := input["current_password"]
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
	if a.selfSignOutOthersBeforeTx != nil {
		a.selfSignOutOthersBeforeTx()
	}

	// The initial admission read lock has been released. Exclusive admission
	// serializes final authorization with logout, password change, and disable.
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
		JOIN employee_self_credentials c ON c.employee_id=s.employee_id
		WHERE s.selector=?`, session.Selector).Scan(&employeeID, &csrf, &expires, &status, &currentHash)
	if errors.Is(err, sql.ErrNoRows) {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	expiry, err := parseTime(expires)
	if err != nil || employeeID != session.EmployeeID || status != "active" || !time.Now().UTC().Before(expiry) || subtle.ConstantTimeCompare([]byte(csrf), []byte(session.CSRF)) != 1 {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	if subtle.ConstantTimeCompare(currentHash, priorHash) != 1 {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM employee_self_sessions WHERE employee_id=? AND selector<>?`, session.EmployeeID, session.Selector); err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	commit := tx.Commit
	if a.selfSignOutOthersCommit != nil {
		commit = func() error { return a.selfSignOutOthersCommit(tx) }
	}
	if err := commit(); err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.clearSelfFailures(peer, session.EmployeeID)
	w.WriteHeader(http.StatusNoContent)
}
