package service

// Independently authored for docs/employee-self-one-shot-disarm-contract.md.
// The HTTP layer owns current self authorization and the final SQL commit.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"cpacloud.local/server/internal/financial"
	"golang.org/x/crypto/bcrypt"
)

func selfOneShotPath(r *http.Request, suffix string) (string, bool) {
	id := r.PathValue("id")
	if r.URL.RawQuery != "" || r.URL.ForceQuery || !utf8.ValidString(id) || !validSelfPurchaseID(id, 256) ||
		r.URL.EscapedPath() != "/self/api/v1/billing/subscriptions/"+url.PathEscape(id)+"/one-shot-renewal"+suffix {
		return "", false
	}
	return id, true
}

func selfOneShotView(status financial.OneShotRenewalStatus) map[string]any {
	var dueAt, reason, terminalAt any
	if status.DueAt != nil {
		dueAt = status.DueAt.UTC().Format(time.RFC3339Nano)
	}
	if status.Reason != "" {
		reason = status.Reason
	}
	if status.TerminalAt != nil {
		terminalAt = status.TerminalAt.UTC().Format(time.RFC3339Nano)
	}
	return map[string]any{
		"subscription_id": status.PredecessorID, "state": status.State,
		"revision": status.Revision, "due_at": dueAt, "reason": reason,
		"terminal_at": terminalAt,
	}
}

func (a *App) selfOneShotRenewalGet(w http.ResponseWriter, r *http.Request, session selfSession) {
	id, ok := selfOneShotPath(r, "")
	if !ok || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), selfSubscriptionTimeout)
	defer cancel()
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	if err := a.recheckSelfOneShotReadTx(ctx, tx, r, session); err != nil {
		selfOneShotReadAuthError(w, err)
		return
	}
	status, err := financial.NewCommercial(a.store.db).ReadEmployeeOneShotRenewalTx(ctx, tx, session.EmployeeID, id, time.Now().UTC())
	if err != nil {
		selfOneShotFinancialError(w, err)
		return
	}
	if err := a.recheckSelfOneShotReadTx(ctx, tx, r, session); err != nil {
		selfOneShotReadAuthError(w, err)
		return
	}
	commit := tx.Commit
	if a.selfOneShotReadCommit != nil {
		commit = func() error { return a.selfOneShotReadCommit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, selfOneShotView(status))
}

var errSelfOneShotReadAuth = errors.New("one-shot read authorization invalid")

func (a *App) recheckSelfOneShotReadTx(ctx context.Context, tx *sql.Tx, r *http.Request, session selfSession) error {
	if ctx.Err() != nil || a == nil || a.secrets == nil {
		return errSelfPlanPurchaseStore
	}
	for _, object := range selfSchema {
		var actual string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.kind, object.name).Scan(&actual); err != nil || strings.TrimSpace(actual) != strings.TrimSpace(object.ddl) {
			return errSelfPlanPurchaseStore
		}
	}
	cookies := r.Cookies()
	var cookie *http.Cookie
	for _, candidate := range cookies {
		if candidate.Name == selfCookieName {
			if cookie != nil {
				return errSelfOneShotReadAuth
			}
			cookie = candidate
		}
	}
	if cookie == nil {
		return errSelfOneShotReadAuth
	}
	parts := splitSelfCookie(cookie.Value)
	if len(parts) != 2 || parts[0] != session.Selector {
		return errSelfOneShotReadAuth
	}
	var employeeID, status, expires string
	var verifierDigest, passwordHash []byte
	var verifierType, statusType, expiresType, hashType string
	err := tx.QueryRowContext(ctx, `SELECT s.employee_id,s.verifier_digest,typeof(s.verifier_digest),s.expires_at,typeof(s.expires_at),e.status,typeof(e.status),c.password_hash,typeof(c.password_hash) FROM employee_self_sessions s JOIN employees e ON e.id=s.employee_id JOIN employee_self_credentials c ON c.employee_id=s.employee_id WHERE s.selector=?`, session.Selector).
		Scan(&employeeID, &verifierDigest, &verifierType, &expires, &expiresType, &status, &statusType, &passwordHash, &hashType)
	if errors.Is(err, sql.ErrNoRows) {
		return errSelfOneShotReadAuth
	}
	if err != nil || verifierType != "blob" || expiresType != "text" || statusType != "text" || hashType != "blob" || len(passwordHash) == 0 || len(verifierDigest) != 32 {
		return errSelfPlanPurchaseStore
	}
	expiry, err := parseTime(expires)
	if err != nil {
		return errSelfPlanPurchaseStore
	}
	if employeeID != session.EmployeeID || status != "active" || !time.Now().UTC().Before(expiry) ||
		!a.secrets.equalDigest("employee-self-session/v1"+session.Selector, parts[1], verifierDigest) {
		return errSelfOneShotReadAuth
	}
	return nil
}

func splitSelfCookie(value string) []string {
	// Keep the two-part selector/verifier shape strict without reflecting it.
	var dot int = -1
	for i := range value {
		if value[i] == '.' {
			if dot >= 0 {
				return nil
			}
			dot = i
		}
	}
	if dot < 0 {
		return nil
	}
	return []string{value[:dot], value[dot+1:]}
}

func selfOneShotReadAuthError(w http.ResponseWriter, err error) {
	if errors.Is(err, errSelfOneShotReadAuth) {
		selfError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
}

func (a *App) selfOneShotRenewalDisarm(w http.ResponseWriter, r *http.Request, session selfSession) {
	peer, ok := a.selfGate(w, r, session.EmployeeID)
	if !ok {
		return
	}
	id, ok := selfOneShotPath(r, "/disarm")
	if !ok {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	request, ok := readSelfSubscriptionCancelInput(w, r)
	if !ok {
		a.selfFailure(peer, session.EmployeeID)
		return
	}
	if !validSelfPassword(request.Password) {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	var priorHash []byte
	var hashType string
	err := a.store.db.QueryRowContext(r.Context(), `SELECT password_hash,typeof(password_hash) FROM employee_self_credentials WHERE employee_id=?`, session.EmployeeID).Scan(&priorHash, &hashType)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	comparison := priorHash
	if len(comparison) == 0 {
		comparison = selfDummyHash
	} else if hashType != "blob" {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if bcrypt.CompareHashAndPassword(comparison, []byte(request.Password)) != nil || len(priorHash) == 0 {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if cost, err := bcrypt.Cost(priorHash); err != nil || cost != 12 {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if a.selfOneShotDisarmBeforeTx != nil {
		a.selfOneShotDisarmBeforeTx()
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), selfSubscriptionTimeout)
	defer cancel()
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	if err := a.recheckSelfPlanPurchaseTx(ctx, tx, r, session, priorHash); err != nil {
		a.selfPlanPurchaseAuthError(w, err)
		return
	}
	input := financial.EmployeeOneShotDisarmInput{
		OperationID: request.OperationID, Actor: financial.Actor{Kind: financial.ActorEmployee, ID: session.EmployeeID},
		PredecessorID: id, ExpectedRevision: request.ExpectedRevision, ObservedAt: time.Now().UTC(),
	}
	commercial := financial.NewCommercial(a.store.db)
	result, replay, err := commercial.ProbeEmployeeOneShotDisarmReplayTx(ctx, tx, input)
	if err != nil {
		selfOneShotFinancialError(w, err)
		return
	}
	if !replay {
		input.ObservedAt = time.Now().UTC()
		result, err = commercial.DisarmEmployeeOneShotRenewalTx(ctx, tx, input)
		if err != nil {
			selfOneShotFinancialError(w, err)
			return
		}
	}
	if a.selfOneShotDisarmBeforeCommit != nil {
		a.selfOneShotDisarmBeforeCommit(tx)
	}
	if err := a.recheckSelfPlanPurchaseTx(ctx, tx, r, session, priorHash); err != nil {
		a.selfPlanPurchaseAuthError(w, err)
		return
	}
	confirmed, err := commercial.ReadEmployeeOneShotRenewalTx(ctx, tx, session.EmployeeID, id, time.Now().UTC())
	if err != nil || confirmed.State != "disarmed" || confirmed.Revision != 2 || confirmed.TerminalAt == nil || result.Status.TerminalAt == nil || !confirmed.TerminalAt.Equal(*result.Status.TerminalAt) {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if ctx.Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	commit := tx.Commit
	if a.selfOneShotDisarmCommit != nil {
		commit = func() error { return a.selfOneShotDisarmCommit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.clearSelfFailures(peer, session.EmployeeID)
	view := selfOneShotView(result.Status)
	view["operation_id"] = request.OperationID
	view["replay"] = replay
	writeJSON(w, http.StatusOK, view)
}

func selfOneShotFinancialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, financial.ErrNotFound):
		selfError(w, http.StatusNotFound, "subscription_not_found")
	case errors.Is(err, financial.ErrConflict):
		selfError(w, http.StatusConflict, "disarm_unavailable")
	default:
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
	}
}
