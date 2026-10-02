package service

// Independently authored for docs/employee-self-plan-purchase-contract.md.
// The self session, password, quote and financial receipts are rechecked in
// one caller-owned SQLite transaction before an employee purchase can commit.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpacloud.local/server/internal/financial"
	"golang.org/x/crypto/bcrypt"
)

const (
	selfPlanPurchaseTimeout     = 5 * time.Second
	selfPlanQuoteLifetime       = 5 * time.Minute
	selfPlanQuoteMax            = 2048
	selfPlanQuotePurpose        = "cpacloud/employee-self-plan-purchase-quote/v1"
	selfPlanQuoteVersion        = 1
	selfPlanPurchaseRevisionMax = int64(9007199254740991)
)

var (
	errSelfPlanQuoteInvalid   = errors.New("invalid employee self plan quote")
	errSelfPlanPurchaseAuth   = errors.New("employee self plan purchase authentication required")
	errSelfPlanPurchaseSecret = errors.New("employee self plan purchase credential changed")
	errSelfPlanPurchaseStore  = errors.New("employee self plan purchase storage unavailable")
)

type selfPlanPurchaseQuoteToken struct {
	Version    int                            `json:"v"`
	Purpose    string                         `json:"p"`
	EmployeeID string                         `json:"e"`
	Session    string                         `json:"s"`
	Expected   financial.ExpectedPurchasePlan `json:"plan"`
	IssuedAt   string                         `json:"iat"`
	ExpiresAt  string                         `json:"exp"`
}

func (a *App) selfPlanPurchaseTime() time.Time {
	if a.selfPlanPurchaseNow != nil {
		return a.selfPlanPurchaseNow().UTC()
	}
	return time.Now().UTC()
}

func validSelfPurchaseID(id string, limit int) bool {
	return id != "" && len(id) <= limit && strings.TrimSpace(id) == id && !strings.ContainsRune(id, 0)
}

func validSelfPurchaseExpected(plan financial.ExpectedPurchasePlan) bool {
	return financial.ValidPlanCatalogID(plan.PlanID) && validSelfPlanCatalogCurrency(plan.Currency) &&
		plan.Interval == "one_time" && plan.Revision >= 1 && plan.Revision <= selfPlanPurchaseRevisionMax &&
		plan.PriceMicro > 0 && plan.CreditMicro > 0
}

func selfPurchaseOwner(employeeID string) financial.Owner {
	return financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: employeeID}
}

func selfPurchaseInput(employeeID, operationID string, plan financial.ExpectedPurchasePlan, at time.Time) financial.EmployeePurchaseInput {
	return financial.EmployeePurchaseInput{
		OperationID: operationID,
		Actor:       financial.Actor{Kind: financial.ActorEmployee, ID: employeeID},
		Owner:       selfPurchaseOwner(employeeID),
		Expected:    plan,
		ObservedAt:  at.UTC(),
	}
}

func (a *App) sealSelfPlanQuote(quote selfPlanPurchaseQuoteToken) (string, error) {
	if a == nil || a.secrets == nil || a.secrets.aead == nil {
		return "", errSelfPlanPurchaseStore
	}
	plain, err := json.Marshal(quote)
	if err != nil {
		return "", errSelfPlanPurchaseStore
	}
	nonce := make([]byte, a.secrets.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", errSelfPlanPurchaseStore
	}
	sealed := a.secrets.aead.Seal(nil, nonce, plain, []byte(selfPlanQuotePurpose))
	encoded := base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(encoded) > selfPlanQuoteMax {
		return "", errSelfPlanPurchaseStore
	}
	return encoded, nil
}

// Opening authenticates structure and session binding, but intentionally does
// not check age. Exact committed replay must precede the new-purchase TTL gate.
func (a *App) openSelfPlanQuote(encoded string, session selfSession) (selfPlanPurchaseQuoteToken, time.Time, time.Time, error) {
	invalid := func() (selfPlanPurchaseQuoteToken, time.Time, time.Time, error) {
		return selfPlanPurchaseQuoteToken{}, time.Time{}, time.Time{}, errSelfPlanQuoteInvalid
	}
	if a == nil || a.secrets == nil || a.secrets.aead == nil {
		return selfPlanPurchaseQuoteToken{}, time.Time{}, time.Time{}, errSelfPlanPurchaseStore
	}
	if encoded == "" || len(encoded) > selfPlanQuoteMax {
		return invalid()
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != encoded {
		return invalid()
	}
	nonceSize := a.secrets.aead.NonceSize()
	if len(data) < nonceSize+a.secrets.aead.Overhead() {
		return invalid()
	}
	plain, err := a.secrets.aead.Open(nil, data[:nonceSize], data[nonceSize:], []byte(selfPlanQuotePurpose))
	if err != nil {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	var quote selfPlanPurchaseQuoteToken
	if err := decoder.Decode(&quote); err != nil {
		return invalid()
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid()
	}
	canonical, err := json.Marshal(quote)
	if err != nil || !bytes.Equal(canonical, plain) || quote.Version != selfPlanQuoteVersion || quote.Purpose != selfPlanQuotePurpose ||
		quote.EmployeeID != session.EmployeeID || quote.Session != session.Selector || !validSelfPurchaseExpected(quote.Expected) {
		return invalid()
	}
	issued, issuedErr := time.Parse(time.RFC3339Nano, quote.IssuedAt)
	expires, expiresErr := time.Parse(time.RFC3339Nano, quote.ExpiresAt)
	if issuedErr != nil || expiresErr != nil || issued.UTC().Format(time.RFC3339Nano) != quote.IssuedAt ||
		expires.UTC().Format(time.RFC3339Nano) != quote.ExpiresAt || !issued.Add(selfPlanQuoteLifetime).Equal(expires) {
		return invalid()
	}
	return quote, issued, expires, nil
}

func (a *App) selfPlanPurchaseQuote(w http.ResponseWriter, r *http.Request, session selfSession) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	fields, ok := readSelfFields(w, r, "plan_id")
	if !ok {
		return
	}
	planID := fields["plan_id"]
	if !financial.ValidPlanCatalogID(planID) {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if a == nil || a.store == nil || a.store.db == nil || a.secrets == nil || a.secrets.aead == nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	// The initial admission read lock was released by requireSelfReleased.
	// Hold the exclusive lock only for the bounded final snapshot and issuance.
	a.admission.Lock()
	defer a.admission.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), selfPlanPurchaseTimeout)
	defer cancel()
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	if err := a.recheckSelfPlanPurchaseTx(ctx, tx, r, session, nil); err != nil {
		a.selfPlanPurchaseAuthError(w, err)
		return
	}
	expected, err := financial.NewCommercial(a.store.db).ReadEmployeePurchaseQuoteTx(ctx, tx, selfPurchaseOwner(session.EmployeeID), planID)
	if err != nil {
		selfPlanPurchaseFinancialError(w, err)
		return
	}
	if err := a.recheckSelfPlanPurchaseTx(ctx, tx, r, session, nil); err != nil {
		a.selfPlanPurchaseAuthError(w, err)
		return
	}
	commit := tx.Commit
	if a.selfPlanQuoteCommit != nil {
		commit = func() error { return a.selfPlanQuoteCommit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	now := a.selfPlanPurchaseTime()
	quote := selfPlanPurchaseQuoteToken{
		Version: selfPlanQuoteVersion, Purpose: selfPlanQuotePurpose,
		EmployeeID: session.EmployeeID, Session: session.Selector, Expected: expected,
		IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(selfPlanQuoteLifetime).Format(time.RFC3339Nano),
	}
	token, err := a.sealSelfPlanQuote(quote)
	if err != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"quote_token": token, "plan_id": expected.PlanID, "revision": expected.Revision,
		"currency": expected.Currency, "interval": expected.Interval,
		"price_micro": strconv.FormatInt(expected.PriceMicro, 10), "credit_micro": strconv.FormatInt(expected.CreditMicro, 10),
		"expires_at": quote.ExpiresAt,
	})
}

func (a *App) selfPlanPurchase(w http.ResponseWriter, r *http.Request, session selfSession) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	peer, ok := a.selfGate(w, r, session.EmployeeID)
	if !ok {
		return
	}
	fields, ok := readSelfFields(w, r, "operation_id", "quote_token", "current_password")
	if !ok {
		a.selfFailure(peer, session.EmployeeID)
		return
	}
	operationID, password := fields["operation_id"], fields["current_password"]
	if !validSelfPurchaseID(operationID, 128) {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	quote, issued, expires, err := a.openSelfPlanQuote(fields["quote_token"], session)
	if errors.Is(err, errSelfPlanPurchaseStore) {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if err != nil {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !validSelfPassword(password) {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	var priorHash []byte
	var hashType string
	err = a.store.db.QueryRowContext(r.Context(), `SELECT password_hash,typeof(password_hash) FROM employee_self_credentials WHERE employee_id=?`, session.EmployeeID).Scan(&priorHash, &hashType)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	comparison := priorHash
	if len(comparison) == 0 {
		comparison = selfDummyHash
	}
	passwordOK := bcrypt.CompareHashAndPassword(comparison, []byte(password)) == nil
	if err == nil && hashType != "blob" {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if !passwordOK || len(priorHash) == 0 {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if a.selfPlanPurchaseBeforeTx != nil {
		a.selfPlanPurchaseBeforeTx()
	}
	// No bcrypt work holds this exclusive admission lock. All mutable auth and
	// financial facts are rechecked after the lock is acquired.
	a.admission.Lock()
	defer a.admission.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), selfPlanPurchaseTimeout)
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
	now := a.selfPlanPurchaseTime()
	input := selfPurchaseInput(session.EmployeeID, operationID, quote.Expected, now)
	commercial := financial.NewCommercial(a.store.db)
	result, replay, err := commercial.ProbeEmployeePurchaseReplayTx(ctx, tx, input)
	if err != nil {
		selfPlanPurchaseFinancialError(w, err)
		return
	}
	if !replay {
		now = a.selfPlanPurchaseTime()
		if now.Before(issued) || !now.Before(expires) {
			selfError(w, http.StatusConflict, "purchase_unavailable")
			return
		}
		input.ObservedAt = now
		result, err = commercial.PurchaseEmployeeSubscriptionTx(ctx, tx, input)
		if err != nil {
			selfPlanPurchaseFinancialError(w, err)
			return
		}
	}
	if a.selfPlanPurchaseBeforeCommit != nil {
		a.selfPlanPurchaseBeforeCommit(tx)
	}
	if err := a.recheckSelfPlanPurchaseTx(ctx, tx, r, session, priorHash); err != nil {
		a.selfPlanPurchaseAuthError(w, err)
		return
	}
	if ctx.Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	commit := tx.Commit
	if a.selfPlanPurchaseCommit != nil {
		commit = func() error { return a.selfPlanPurchaseCommit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.clearSelfFailures(peer, session.EmployeeID)
	status := http.StatusCreated
	if replay {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"operation_id": operationID, "subscription_id": result.Subscription.ID, "replay": replay,
		"plan_id": result.Subscription.PlanID, "revision": result.Subscription.PlanRevision,
		"currency": result.Subscription.Currency, "interval": result.Subscription.Interval,
		"price_micro":  strconv.FormatInt(result.Subscription.PriceMicro, 10),
		"credit_micro": strconv.FormatInt(result.Subscription.CreditMicro, 10),
	})
}

func (a *App) recheckSelfPlanPurchaseTx(ctx context.Context, tx *sql.Tx, r *http.Request, session selfSession, priorHash []byte) error {
	if ctx.Err() != nil || tx == nil || a == nil || a.secrets == nil {
		return errSelfPlanPurchaseStore
	}
	for _, object := range selfSchema {
		var actual string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.kind, object.name).Scan(&actual); err != nil || strings.TrimSpace(actual) != strings.TrimSpace(object.ddl) {
			return errSelfPlanPurchaseStore
		}
	}
	var cookie *http.Cookie
	for _, candidate := range r.Cookies() {
		if candidate.Name == selfCookieName {
			if cookie != nil {
				return errSelfPlanPurchaseAuth
			}
			cookie = candidate
		}
	}
	if cookie == nil {
		return errSelfPlanPurchaseAuth
	}
	parts := strings.Split(cookie.Value, ".")
	providedCSRF := r.Header.Values("X-CSRF-Token")
	if len(parts) != 2 || parts[0] != session.Selector || len(parts[1]) != 43 || len(providedCSRF) != 1 {
		return errSelfPlanPurchaseAuth
	}
	var employeeID, csrf, expires, status string
	var verifierDigest, currentHash []byte
	var verifierType, csrfType, expiresType, statusType, hashType string
	err := tx.QueryRowContext(ctx, `SELECT s.employee_id,s.verifier_digest,typeof(s.verifier_digest),s.csrf_token,typeof(s.csrf_token),
		s.expires_at,typeof(s.expires_at),e.status,typeof(e.status),c.password_hash,typeof(c.password_hash)
		FROM employee_self_sessions s JOIN employees e ON e.id=s.employee_id
		JOIN employee_self_credentials c ON c.employee_id=s.employee_id WHERE s.selector=?`, session.Selector).
		Scan(&employeeID, &verifierDigest, &verifierType, &csrf, &csrfType, &expires, &expiresType, &status, &statusType, &currentHash, &hashType)
	if errors.Is(err, sql.ErrNoRows) {
		return errSelfPlanPurchaseAuth
	}
	if err != nil || verifierType != "blob" || len(verifierDigest) != sha256.Size || csrfType != "text" ||
		expiresType != "text" || statusType != "text" || hashType != "blob" || len(currentHash) == 0 {
		return errSelfPlanPurchaseStore
	}
	expiry, err := parseTime(expires)
	if err != nil {
		return errSelfPlanPurchaseStore
	}
	if employeeID != session.EmployeeID || status != "active" || !a.selfPlanPurchaseTime().Before(expiry) ||
		subtle.ConstantTimeCompare([]byte(csrf), []byte(session.CSRF)) != 1 ||
		subtle.ConstantTimeCompare([]byte(csrf), []byte(providedCSRF[0])) != 1 ||
		!a.secrets.equalDigest("employee-self-session/v1"+session.Selector, parts[1], verifierDigest) {
		return errSelfPlanPurchaseAuth
	}
	if cost, err := bcrypt.Cost(currentHash); err != nil || cost != 12 {
		return errSelfPlanPurchaseStore
	}
	if priorHash != nil && subtle.ConstantTimeCompare(priorHash, currentHash) != 1 {
		return errSelfPlanPurchaseSecret
	}
	return nil
}

func (a *App) selfPlanPurchaseAuthError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSelfPlanPurchaseAuth):
		selfError(w, http.StatusUnauthorized, "authentication_required")
	case errors.Is(err, errSelfPlanPurchaseSecret):
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
	default:
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
	}
}

func selfPlanPurchaseFinancialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, financial.ErrConflict), errors.Is(err, financial.ErrInsufficient), errors.Is(err, financial.ErrNotFound):
		selfError(w, http.StatusConflict, "purchase_unavailable")
	default:
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
	}
}
