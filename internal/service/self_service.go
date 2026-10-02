// Independently authored for docs/employee-self-service-foundation-contract.md.
package service

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

const (
	selfCookieName         = "cpa_self_session"
	selfLifetime           = 2 * time.Hour
	selfEnrollmentLifetime = 15 * time.Minute
	selfMaxBody            = 4096
)

var selfDummyHash = []byte("$2a$12$wJ5fLlDjsUYyrFKSGUN3LejJNhi2PMsS0KtqQffY2nzceZx9Nbm7a")

type selfProfile struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Department string `json:"department"`
	Status     string `json:"status"`
}

type selfSession struct {
	Selector   string
	EmployeeID string
	CSRF       string
	Profile    selfProfile
}

func (a *App) registerSelfHandlers(mux *http.ServeMux) {
	mux.HandleFunc("POST /self/api/v1/enroll", a.redeemSelfEnrollment)
	mux.HandleFunc("POST /self/api/v1/sessions", a.selfLogin)
	mux.HandleFunc("GET /self/api/v1/session", a.requireSelf(a.selfSessionInfo, false))
	mux.HandleFunc("GET /self/api/v1/profile", a.requireSelf(a.selfProfileInfo, false))
	if a.cfg.EmployeeSelfWalletBalanceEnabled {
		mux.HandleFunc("GET /self/api/v1/billing/balance", a.requireSelf(a.selfWalletBalance, false))
	}
	if a.cfg.EmployeeSelfRedemptionEnabled {
		mux.HandleFunc("POST /self/api/v1/billing/redemptions", a.requireSelfRedemption(a.selfRedeemCode, true))
	}
	if a.cfg.EmployeeSelfWalletActivityEnabled {
		mux.HandleFunc("GET /self/api/v1/billing/entries", a.requireSelf(a.selfWalletActivity, false))
	}
	if a.cfg.EmployeeSelfWalletEntryClassificationEnabled {
		mux.HandleFunc("GET /self/api/v1/billing/entry-classifications", a.requireSelf(a.selfWalletEntryClassifications, false))
	}
	if a.cfg.EmployeeSelfSubscriptionStatusEnabled {
		mux.HandleFunc("GET /self/api/v1/billing/subscriptions", a.requireSelf(a.selfSubscriptionStatus, false))
	}
	if a.cfg.EmployeeSelfSubscriptionPurchaseSnapshotEnabled {
		mux.HandleFunc("GET /self/api/v1/billing/subscriptions/{id}/purchase-snapshot", a.requireSelf(a.selfSubscriptionPurchaseSnapshot, false))
		mux.HandleFunc("GET /self/api/v1/billing/subscriptions/{id}/purchase-snapshot/{tail...}", a.requireSelf(a.selfSubscriptionPurchaseSnapshot, false))
	}
	// Independently authored for docs/employee-self-subscription-renewal-links-contract.md.
	if a.cfg.EmployeeSelfSubscriptionRenewalLinksEnabled {
		mux.HandleFunc("GET /self/api/v1/billing/subscriptions/{id}/renewal-links", a.requireSelfReleased(a.selfSubscriptionRenewalLinks, false))
	}
	// Independently authored for docs/employee-self-monthly-renewal-contract.md.
	if a.cfg.EmployeeSelfSubscriptionRenewalEnabled {
		mux.HandleFunc("POST /self/api/v1/billing/subscriptions/{id}/renewal-quotes", a.requireSelfReleased(a.selfMonthlyRenewalQuote, true))
		mux.HandleFunc("POST /self/api/v1/billing/subscriptions/{id}/renew", a.requireSelfReleased(a.selfMonthlyRenewal, true))
	}
	if a.cfg.EmployeeSelfSubscriptionCancelEnabled {
		mux.HandleFunc("POST /self/api/v1/billing/subscriptions/{id}/cancel", a.requireSelfReleased(a.selfSubscriptionCancel, true))
	}
	// Independently authored for docs/employee-self-one-shot-disarm-contract.md.
	if a.cfg.EmployeeSelfOneShotRenewalDisarmEnabled {
		mux.HandleFunc("GET /self/api/v1/billing/subscriptions/{id}/one-shot-renewal", a.requireSelf(a.selfOneShotRenewalGet, false))
		mux.HandleFunc("POST /self/api/v1/billing/subscriptions/{id}/one-shot-renewal/disarm", a.requireSelfReleased(a.selfOneShotRenewalDisarm, true))
		mux.HandleFunc("GET /self/api/v1/billing/subscriptions/{id}/one-shot-renewal/{tail...}", a.requireSelf(a.selfOneShotRenewalGet, false))
		mux.HandleFunc("POST /self/api/v1/billing/subscriptions/{id}/one-shot-renewal/disarm/{tail...}", a.requireSelfReleased(a.selfOneShotRenewalDisarm, true))
	}
	if a.cfg.EmployeeSelfPlanCatalogEnabled {
		mux.HandleFunc("GET /self/api/v1/billing/plans", a.requireSelf(a.selfPlanCatalog, false))
	}
	if a.cfg.EmployeeSelfPlanPurchaseEnabled {
		mux.HandleFunc("POST /self/api/v1/billing/plan-purchase-quotes", a.requireSelfReleased(a.selfPlanPurchaseQuote, true))
		mux.HandleFunc("POST /self/api/v1/billing/subscriptions", a.requireSelfReleased(a.selfPlanPurchase, true))
	}
	// Independently authored for docs/employee-self-key-inventory-contract.md.
	mux.HandleFunc("GET /self/api/v1/keys", a.requireSelf(a.selfListKeys, false))
	// Independently authored for docs/employee-self-key-issuance-contract.md.
	mux.HandleFunc("GET /self/api/v1/key-slots", a.requireSelf(a.selfListKeySlots, false))
	mux.HandleFunc("POST /self/api/v1/keys/issue", a.requireSelfReleased(a.selfIssueKey, true))
	// Independently authored for docs/employee-self-key-revocation-contract.md.
	mux.HandleFunc("POST /self/api/v1/keys/{id}/revoke", a.requireSelfReleased(a.selfRevokeKey, true))
	// Independently authored for docs/employee-self-request-history-contract.md.
	mux.HandleFunc("GET /self/api/v1/usage/requests", a.requireSelf(a.selfRequestHistory, false))
	// Independently authored for docs/employee-self-key-request-history-contract.md.
	mux.HandleFunc("GET /self/api/v1/keys/{id}/usage/requests", a.requireSelf(a.selfKeyRequestHistory, false))
	// Independently authored for docs/employee-self-token-summary-contract.md.
	mux.HandleFunc("GET /self/api/v1/usage/summary", a.requireSelf(a.selfTokenSummary, false))
	// Independently authored for docs/employee-self-key-token-summary-contract.md.
	mux.HandleFunc("GET /self/api/v1/keys/{id}/usage/summary", a.requireSelf(a.selfKeyTokenSummary, false))
	mux.HandleFunc("POST /self/api/v1/password", a.requireSelf(a.selfChangePassword, true))
	mux.HandleFunc("DELETE /self/api/v1/sessions", a.requireSelf(a.selfLogout, true))
	// Independently authored for docs/employee-self-signout-others-contract.md.
	mux.HandleFunc("POST /self/api/v1/sessions/revoke-others", a.requireSelfReleased(a.selfSignOutOthers, true))
	mux.HandleFunc("/self/api/", http.NotFound)
}

func selfError(w http.ResponseWriter, status int, code string) {
	message := "Request could not be completed."
	if code == "invalid_credentials" {
		message = "Invalid credentials or enrollment secret."
	}
	writeAdminError(w, status, code, message)
}

// Security-sensitive bodies are deliberately restricted to a small object of
// string fields. Token parsing rejects repeated and unknown field names.
func readSelfFields(w http.ResponseWriter, r *http.Request, expected ...string) (map[string]string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, selfMaxBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil || !utf8.Valid(raw) {
		selfError(w, 400, "invalid_request")
		return nil, false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		selfError(w, 400, "invalid_request")
		return nil, false
	}
	allowed := make(map[string]bool, len(expected))
	for _, key := range expected {
		allowed[key] = true
	}
	fields := make(map[string]string, len(expected))
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowed[key] {
			selfError(w, 400, "invalid_request")
			return nil, false
		}
		if _, exists := fields[key]; exists {
			selfError(w, 400, "invalid_request")
			return nil, false
		}
		var value string
		if err := d.Decode(&value); err != nil {
			selfError(w, 400, "invalid_request")
			return nil, false
		}
		fields[key] = value
	}
	last, err := d.Token()
	if err != nil || last != json.Delim('}') {
		selfError(w, 400, "invalid_request")
		return nil, false
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		selfError(w, 400, "invalid_request")
		return nil, false
	}
	if len(fields) != len(expected) {
		selfError(w, 400, "invalid_request")
		return nil, false
	}
	return fields, true
}

func (a *App) selfWriteOrigin(w http.ResponseWriter, r *http.Request) bool {
	if len(r.Header.Values("Origin")) != 1 || len(r.Header.Values("X-Self-Request")) != 1 || !validRequiredOrigin(r, a.cfg.TLSCert != "") || r.Header.Get("X-Self-Request") != "1" {
		selfError(w, 403, "request_rejected")
		return false
	}
	return true
}

func validSelfPassword(password string) bool {
	return utf8.ValidString(password) && len(password) >= 12 && len(password) <= 72
}

func (a *App) issueSelfEnrollment(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if _, ok := readSelfFields(w, r); !ok {
		return
	}
	secret, err := randomToken(32)
	if err != nil {
		selfError(w, 503, "service_unavailable")
		return
	}
	expires := selfTime(time.Now().Add(selfEnrollmentLifetime))
	id := r.PathValue("id")
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	var status string
	var password []byte
	err = tx.QueryRowContext(r.Context(), `SELECT e.status,c.password_hash FROM employees e LEFT JOIN employee_self_credentials c ON c.employee_id=e.id WHERE e.id=?`, id).Scan(&status, &password)
	if errors.Is(err, sql.ErrNoRows) {
		selfError(w, 404, "not_found")
		return
	}
	if err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	if status != "active" || password != nil {
		selfError(w, 409, "enrollment_unavailable")
		return
	}
	result, err := tx.ExecContext(r.Context(), `INSERT INTO employee_self_credentials(employee_id,password_hash,enrollment_digest,enrollment_expires_at,updated_at) VALUES(?,NULL,?,?,?) ON CONFLICT(employee_id) DO UPDATE SET enrollment_digest=excluded.enrollment_digest,enrollment_expires_at=excluded.enrollment_expires_at,updated_at=excluded.updated_at WHERE password_hash IS NULL`, id, a.secrets.digest("employee-self-enrollment/v1", secret), expires, utcNow())
	if err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		selfError(w, 503, "storage_unavailable")
		return
	}
	if err := tx.Commit(); err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	writeJSON(w, 201, map[string]string{"employee_id": id, "enrollment_secret": secret, "expires_at": expires})
}

func (a *App) selfLimited(peer, employeeID string) bool {
	now := time.Now()
	a.selfLoginMu.Lock()
	defer a.selfLoginMu.Unlock()
	for _, key := range []string{"peer:" + peer, "employee:" + employeeID} {
		if entry := a.selfLogins[key]; entry != nil && now.Before(entry.blockedTill) {
			return true
		}
	}
	return len(a.selfLogins) >= 4096
}

func (a *App) selfFailure(peer, employeeID string) {
	now := time.Now()
	a.selfLoginMu.Lock()
	defer a.selfLoginMu.Unlock()
	if len(a.selfLogins) >= 4096 {
		for key, entry := range a.selfLogins {
			if now.Sub(entry.lastSeen) > 15*time.Minute {
				delete(a.selfLogins, key)
			}
		}
	}
	for _, key := range []string{"peer:" + peer, "employee:" + employeeID} {
		entry := a.selfLogins[key]
		if entry == nil || now.Sub(entry.lastSeen) > 15*time.Minute {
			if len(a.selfLogins) >= 4096 {
				continue
			}
			entry = &loginAttempt{}
			a.selfLogins[key] = entry
		}
		entry.failures++
		entry.lastSeen = now
		if entry.failures >= 5 {
			delay := time.Duration(entry.failures-4) * time.Minute
			if delay > 15*time.Minute {
				delay = 15 * time.Minute
			}
			entry.blockedTill = now.Add(delay)
		}
	}
}

func (a *App) clearSelfFailures(peer, employeeID string) {
	a.selfLoginMu.Lock()
	delete(a.selfLogins, "peer:"+peer)
	delete(a.selfLogins, "employee:"+employeeID)
	a.selfLoginMu.Unlock()
}

func (a *App) selfGate(w http.ResponseWriter, r *http.Request, employeeID string) (string, bool) {
	peer := clientAddress(r)
	if employeeID == "" || len(employeeID) > 200 {
		a.selfFailure(peer, employeeID)
		selfError(w, 401, "invalid_credentials")
		return peer, false
	}
	if a.selfLimited(peer, employeeID) {
		w.Header().Set("Retry-After", "60")
		selfError(w, 429, "login_limited")
		return peer, false
	}
	return peer, true
}

func newSelfSession() (selector, verifier, csrf, expiry string, err error) {
	selector, err = newID("self")
	if err != nil {
		return
	}
	verifier, err = randomToken(32)
	if err != nil {
		return
	}
	csrf, err = randomToken(32)
	if err != nil {
		return
	}
	expiry = selfTime(time.Now().Add(selfLifetime))
	return
}

func (a *App) insertSelfSession(ctx context.Context, tx *sql.Tx, employeeID, selector, verifier, csrf, expiry string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO employee_self_sessions(selector,employee_id,verifier_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)`, selector, employeeID, a.secrets.digest("employee-self-session/v1"+selector, verifier), csrf, expiry, utcNow())
	return err
}

func (a *App) setSelfCookie(w http.ResponseWriter, selector, verifier string) {
	http.SetCookie(w, &http.Cookie{Name: selfCookieName, Value: selector + "." + verifier, Path: "/self/", MaxAge: int(selfLifetime.Seconds()), HttpOnly: true, Secure: a.cfg.TLSCert != "", SameSite: http.SameSiteStrictMode})
}

func (a *App) redeemSelfEnrollment(w http.ResponseWriter, r *http.Request) {
	if !a.selfWriteOrigin(w, r) {
		return
	}
	input, ok := readSelfFields(w, r, "employee_id", "enrollment_secret", "password")
	if !ok {
		return
	}
	peer, ok := a.selfGate(w, r, input["employee_id"])
	if !ok {
		return
	}
	secret := input["enrollment_secret"]
	if !validSelfPassword(input["password"]) || len(secret) != 43 {
		a.selfFailure(peer, input["employee_id"])
		selfError(w, 401, "invalid_credentials")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input["password"]), 12)
	if err != nil {
		selfError(w, 503, "service_unavailable")
		return
	}
	selector, verifier, csrf, expiry, err := newSelfSession()
	if err != nil {
		selfError(w, 503, "service_unavailable")
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	var profile selfProfile
	var digest []byte
	var expires string
	err = tx.QueryRowContext(r.Context(), `SELECT e.id,e.name,e.department,e.status,c.enrollment_digest,COALESCE(c.enrollment_expires_at,'') FROM employees e JOIN employee_self_credentials c ON c.employee_id=e.id WHERE e.id=?`, input["employee_id"]).Scan(&profile.ID, &profile.Name, &profile.Department, &profile.Status, &digest, &expires)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		selfError(w, 503, "storage_unavailable")
		return
	}
	actual := a.secrets.digest("employee-self-enrollment/v1", secret)
	if len(digest) != len(actual) {
		digest = make([]byte, len(actual))
	}
	enrollmentExpiry, parseErr := parseTime(expires)
	valid := subtle.ConstantTimeCompare(actual, digest) == 1 && profile.Status == "active" && parseErr == nil && time.Now().UTC().Before(enrollmentExpiry)
	if !valid {
		a.selfFailure(peer, input["employee_id"])
		selfError(w, 401, "invalid_credentials")
		return
	}
	res, err := tx.ExecContext(r.Context(), `UPDATE employee_self_credentials SET password_hash=?,enrollment_digest=NULL,enrollment_expires_at=NULL,updated_at=? WHERE employee_id=? AND password_hash IS NULL AND enrollment_digest=?`, hash, utcNow(), profile.ID, digest)
	if err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	changed, err := res.RowsAffected()
	if err != nil || changed != 1 {
		a.selfFailure(peer, input["employee_id"])
		selfError(w, 401, "invalid_credentials")
		return
	}
	if err := a.insertSelfSession(r.Context(), tx, profile.ID, selector, verifier, csrf, expiry); err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	if err := tx.Commit(); err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	a.clearSelfFailures(peer, profile.ID)
	a.setSelfCookie(w, selector, verifier)
	writeJSON(w, 200, a.selfSessionResponse(csrf, profile))
}

func (a *App) selfLogin(w http.ResponseWriter, r *http.Request) {
	if !a.selfWriteOrigin(w, r) {
		return
	}
	input, ok := readSelfFields(w, r, "employee_id", "password")
	if !ok {
		return
	}
	peer, ok := a.selfGate(w, r, input["employee_id"])
	if !ok {
		return
	}
	if !validSelfPassword(input["password"]) {
		a.selfFailure(peer, input["employee_id"])
		selfError(w, 401, "invalid_credentials")
		return
	}
	a.admission.RLock()
	defer a.admission.RUnlock()
	var profile selfProfile
	var hash []byte
	err := a.store.db.QueryRowContext(r.Context(), `SELECT e.id,e.name,e.department,e.status,c.password_hash FROM employees e LEFT JOIN employee_self_credentials c ON c.employee_id=e.id WHERE e.id=?`, input["employee_id"]).Scan(&profile.ID, &profile.Name, &profile.Department, &profile.Status, &hash)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		selfError(w, 503, "storage_unavailable")
		return
	}
	comparison := hash
	if len(comparison) == 0 {
		comparison = selfDummyHash
	}
	passwordOK := bcrypt.CompareHashAndPassword(comparison, []byte(input["password"])) == nil
	if !passwordOK || profile.Status != "active" || hash == nil {
		a.selfFailure(peer, input["employee_id"])
		selfError(w, 401, "invalid_credentials")
		return
	}
	selector, verifier, csrf, expiry, err := newSelfSession()
	if err != nil {
		selfError(w, 503, "service_unavailable")
		return
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	if err := a.insertSelfSession(r.Context(), tx, profile.ID, selector, verifier, csrf, expiry); err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	if err := tx.Commit(); err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	a.clearSelfFailures(peer, profile.ID)
	a.setSelfCookie(w, selector, verifier)
	writeJSON(w, 200, a.selfSessionResponse(csrf, profile))
}

func (a *App) requireSelf(next func(http.ResponseWriter, *http.Request, selfSession), write bool) http.HandlerFunc {
	return a.requireSelfWithLock(next, write, true, "csrf_rejected")
}

// Mutations needing admission.Lock must finish initial session validation before
// releasing the read lock; their final transaction revalidates that session.
func (a *App) requireSelfReleased(next func(http.ResponseWriter, *http.Request, selfSession), write bool) http.HandlerFunc {
	return a.requireSelfWithLock(next, write, false, "csrf_rejected")
}

// The redemption contract uses one indistinguishable Origin/CSRF rejection.
func (a *App) requireSelfRedemption(next func(http.ResponseWriter, *http.Request, selfSession), write bool) http.HandlerFunc {
	return a.requireSelfWithLock(next, write, false, "request_rejected")
}

func (a *App) requireSelfWithLock(next func(http.ResponseWriter, *http.Request, selfSession), write, holdLock bool, csrfError string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if len(r.Header.Values("Origin")) > 1 || !validOptionalOrigin(r, a.cfg.TLSCert != "") {
			selfError(w, 403, "request_rejected")
			return
		}
		cookie, err := r.Cookie(selfCookieName)
		if err != nil {
			selfError(w, 401, "authentication_required")
			return
		}
		parts := strings.Split(cookie.Value, ".")
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "self_") || len(parts[1]) != 43 {
			selfError(w, 401, "authentication_required")
			return
		}
		a.admission.RLock()
		locked := true
		defer func() {
			if locked {
				a.admission.RUnlock()
			}
		}()
		var session selfSession
		var stored []byte
		var expires string
		err = a.store.db.QueryRowContext(r.Context(), `SELECT s.selector,s.employee_id,s.verifier_digest,s.csrf_token,s.expires_at,e.id,e.name,e.department,e.status FROM employee_self_sessions s JOIN employees e ON e.id=s.employee_id WHERE s.selector=?`, parts[0]).Scan(&session.Selector, &session.EmployeeID, &stored, &session.CSRF, &expires, &session.Profile.ID, &session.Profile.Name, &session.Profile.Department, &session.Profile.Status)
		if errors.Is(err, sql.ErrNoRows) {
			selfError(w, 401, "authentication_required")
			return
		}
		if err != nil {
			selfError(w, 503, "storage_unavailable")
			return
		}
		expiry, err := parseTime(expires)
		if err != nil || !time.Now().UTC().Before(expiry) || session.Profile.Status != "active" || !a.secrets.equalDigest("employee-self-session/v1"+session.Selector, parts[1], stored) {
			selfError(w, 401, "authentication_required")
			return
		}
		if write {
			if !a.selfWriteOrigin(w, r) {
				return
			}
			provided := r.Header.Values("X-CSRF-Token")
			if len(provided) != 1 || subtle.ConstantTimeCompare([]byte(provided[0]), []byte(session.CSRF)) != 1 {
				selfError(w, 403, csrfError)
				return
			}
		}
		if !holdLock {
			a.admission.RUnlock()
			locked = false
		}
		next(w, r, session)
	}
}

func (a *App) selfSessionInfo(w http.ResponseWriter, _ *http.Request, session selfSession) {
	writeJSON(w, 200, a.selfSessionResponse(session.CSRF, session.Profile))
}

func (a *App) selfSessionResponse(csrf string, profile selfProfile) map[string]any {
	return map[string]any{"csrf_token": csrf, "profile": profile, "features": map[string]bool{
		"employee_self_wallet_balance":                 a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled,
		"employee_self_redemption":                     a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfRedemptionEnabled,
		"employee_self_wallet_activity":                a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfWalletActivityEnabled,
		"employee_self_wallet_entry_classification":    a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfWalletActivityEnabled && a.cfg.EmployeeSelfWalletEntryClassificationEnabled,
		"employee_self_subscription_status":            a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled,
		"employee_self_subscription_purchase_snapshot": a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfSubscriptionPurchaseSnapshotEnabled,
		"employee_self_subscription_renewal_links":     a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled && a.cfg.EmployeeSelfSubscriptionRenewalLinksEnabled,
		"employee_self_subscription_renewal":           a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfSubscriptionRenewalEnabled,
		"employee_self_subscription_cancel":            a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled && a.cfg.EmployeeSelfSubscriptionCancelEnabled,
		"employee_self_one_shot_renewal_disarm":        a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfSubscriptionStatusEnabled && a.cfg.EmployeeSelfOneShotRenewalDisarmEnabled,
		"employee_self_plan_catalog":                   a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfPlanCatalogEnabled,
		"employee_self_plan_purchase":                  a.cfg.EmployeeSelfServiceEnabled && a.cfg.EmployeeSelfWalletBalanceEnabled && a.cfg.EmployeeSelfPlanCatalogEnabled && a.cfg.EmployeeSelfPlanPurchaseEnabled,
	}}
}

func (a *App) selfProfileInfo(w http.ResponseWriter, _ *http.Request, session selfSession) {
	writeJSON(w, 200, session.Profile)
}

func (a *App) selfLogout(w http.ResponseWriter, r *http.Request, session selfSession) {
	if _, err := a.store.db.ExecContext(r.Context(), `DELETE FROM employee_self_sessions WHERE selector=?`, session.Selector); err != nil {
		selfError(w, 503, "storage_unavailable")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: selfCookieName, Value: "", Path: "/self/", MaxAge: -1, HttpOnly: true, Secure: a.cfg.TLSCert != "", SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// Independently authored for docs/employee-self-password-change-contract.md.
// requireSelf holds admission.RLock through this handler, so administrative
// disable cannot interleave with the final credential/session transaction.
func (a *App) selfChangePassword(w http.ResponseWriter, r *http.Request, session selfSession) {
	peer, ok := a.selfGate(w, r, session.EmployeeID)
	if !ok {
		return
	}
	input, ok := readSelfFields(w, r, "current_password", "new_password")
	if !ok {
		a.selfFailure(peer, session.EmployeeID)
		return
	}
	current, next := input["current_password"], input["new_password"]
	if !validSelfPassword(next) {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !validSelfPassword(current) {
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
	passwordOK := bcrypt.CompareHashAndPassword(comparison, []byte(current)) == nil
	if !passwordOK || len(priorHash) == 0 || current == next {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(next), 12)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "service_unavailable")
		return
	}
	if a.selfPasswordBeforeTx != nil {
		a.selfPasswordBeforeTx()
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(r.Context(), `UPDATE employee_self_credentials SET password_hash=?,updated_at=? WHERE employee_id=? AND password_hash=? AND EXISTS (SELECT 1 FROM employees WHERE id=? AND status='active') AND EXISTS (SELECT 1 FROM employee_self_sessions WHERE selector=? AND employee_id=? AND expires_at>?)`, newHash, utcNow(), session.EmployeeID, priorHash, session.EmployeeID, session.Selector, session.EmployeeID, selfTime(time.Now()))
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if changed != 1 {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM employee_self_sessions WHERE employee_id=?`, session.EmployeeID); err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	commit := tx.Commit
	if a.selfPasswordCommit != nil {
		commit = func() error { return a.selfPasswordCommit(tx) }
	}
	if err := commit(); err != nil {
		// The commit outcome can be unknown. Never claim success or issue a new
		// credential; a fresh login determines which password is current.
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.clearSelfFailures(peer, session.EmployeeID)
	http.SetCookie(w, &http.Cookie{Name: selfCookieName, Value: "", Path: "/self/", MaxAge: -1, HttpOnly: true, Secure: a.cfg.TLSCert != "", SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}
