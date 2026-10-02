package service

// Independently authored for docs/employee-self-monthly-renewal-contract.md.
// Both HTTP writes retain current-password and session checks through commit.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cpacloud.local/server/internal/financial"
	"golang.org/x/crypto/bcrypt"
)

const (
	selfMonthlyRenewalPurpose   = "cpacloud/employee-self-monthly-renewal-quote/v1"
	selfMonthlyRenewalLifetime  = 5 * time.Minute
	selfMonthlyRenewalTokenMax  = 2048
	selfMonthlyRenewalEndLayout = "2006-01-02T15:04:05.000000000Z"
)

type selfMonthlyRenewalToken struct {
	Version        int                            `json:"v"`
	Purpose        string                         `json:"p"`
	EmployeeID     string                         `json:"e"`
	Session        string                         `json:"s"`
	PredecessorID  string                         `json:"predecessor_id"`
	PredecessorEnd string                         `json:"predecessor_period_end_at"`
	Expected       financial.ExpectedPurchasePlan `json:"plan"`
	IssuedAt       string                         `json:"iat"`
	ExpiresAt      string                         `json:"exp"`
}

func (a *App) selfMonthlyRenewalTime() time.Time {
	if a.selfMonthlyRenewalNow != nil {
		return a.selfMonthlyRenewalNow().UTC()
	}
	return time.Now().UTC()
}

func selfMonthlyRenewalShape(r *http.Request) string {
	const prefix = "/self/api/v1/billing/subscriptions/"
	escaped, decoded := r.URL.EscapedPath(), r.URL.Path
	for _, suffix := range []string{"/renewal-quotes", "/renew"} {
		if selfMonthlyRenewalSegment(escaped, prefix, suffix) || selfMonthlyRenewalSegment(decoded, prefix, suffix) {
			return suffix
		}
	}
	return ""
}

// Independently authored sibling-route boundary for
// docs/employee-self-subscription-renewal-links-contract.md.
func selfMonthlyRenewalSegment(path, prefix, suffix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	tail := strings.TrimPrefix(path, prefix)
	// A route name must end at a path-segment boundary. A substring match on
	// "/renew" would otherwise intercept sibling routes such as
	// "/renewal-links" before their own guard or handler can run.
	return strings.HasSuffix(tail, suffix) || strings.Contains(tail, suffix+"/")
}

func malformedSelfMonthlyRenewalPath(r *http.Request, suffix string) bool {
	const prefix = "/self/api/v1/billing/subscriptions/"
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, prefix) || !strings.HasSuffix(escaped, suffix) {
		return true
	}
	idSegment := strings.TrimSuffix(strings.TrimPrefix(escaped, prefix), suffix)
	if strings.ContainsRune(idSegment, '/') {
		return true
	}
	id, err := url.PathUnescape(idSegment)
	return err != nil || !validSelfPurchaseID(id, 256) || strings.ContainsAny(id, "/\\%") ||
		id == "." || id == ".." || url.PathEscape(id) != idSegment
}

func selfMonthlyRenewalPath(r *http.Request, suffix string) (string, bool) {
	id := r.PathValue("id")
	return id, validSelfPurchaseID(id, 256) && !strings.ContainsAny(id, "/\\%") && id != "." && id != ".." &&
		r.URL.EscapedPath() == "/self/api/v1/billing/subscriptions/"+url.PathEscape(id)+suffix
}

func (a *App) selfMonthlyRenewalRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		suffix := selfMonthlyRenewalShape(r)
		if suffix == "" {
			next.ServeHTTP(w, r)
			return
		}
		if malformedSelfMonthlyRenewalPath(r, suffix) {
			a.requireSelfReleased(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
				selfError(w, http.StatusBadRequest, "invalid_request")
			}, r.Method == http.MethodPost)(w, r)
			return
		}
		if r.Method != http.MethodPost {
			a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
				w.Header().Set("Allow", http.MethodPost)
				selfError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			}, false)(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func validSelfMonthlyRenewalExpected(plan financial.ExpectedPurchasePlan) bool {
	return financial.ValidPlanCatalogID(plan.PlanID) && validSelfPlanCatalogCurrency(plan.Currency) &&
		plan.Interval == "monthly" && plan.Revision >= 1 && plan.Revision <= selfPlanPurchaseRevisionMax &&
		plan.PriceMicro > 0 && plan.CreditMicro > 0
}

func validSelfMonthlyRenewalToken(token selfMonthlyRenewalToken, session selfSession) (time.Time, time.Time, bool) {
	end, endErr := time.Parse(time.RFC3339Nano, token.PredecessorEnd)
	issued, issuedErr := time.Parse(time.RFC3339Nano, token.IssuedAt)
	expires, expiresErr := time.Parse(time.RFC3339Nano, token.ExpiresAt)
	if token.Version != 1 || token.Purpose != selfMonthlyRenewalPurpose || token.EmployeeID != session.EmployeeID ||
		token.Session != session.Selector || !validSelfPurchaseID(token.PredecessorID, 256) ||
		strings.ContainsAny(token.PredecessorID, "/\\%") || token.PredecessorID == "." || token.PredecessorID == ".." ||
		!validSelfMonthlyRenewalExpected(token.Expected) || endErr != nil || issuedErr != nil || expiresErr != nil ||
		end.Location() != time.UTC || end.Format(selfMonthlyRenewalEndLayout) != token.PredecessorEnd ||
		issued.UTC().Format(time.RFC3339Nano) != token.IssuedAt || expires.UTC().Format(time.RFC3339Nano) != token.ExpiresAt ||
		!issued.Add(selfMonthlyRenewalLifetime).Equal(expires) {
		return time.Time{}, time.Time{}, false
	}
	return issued, expires, true
}

func (a *App) sealSelfMonthlyRenewalToken(token selfMonthlyRenewalToken) (string, error) {
	if a == nil || a.secrets == nil || a.secrets.aead == nil {
		return "", errSelfPlanPurchaseStore
	}
	plain, err := json.Marshal(token)
	if err != nil {
		return "", errSelfPlanPurchaseStore
	}
	nonce := make([]byte, a.secrets.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", errSelfPlanPurchaseStore
	}
	sealed := a.secrets.aead.Seal(nil, nonce, plain, []byte(selfMonthlyRenewalPurpose))
	encoded := base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(encoded) > selfMonthlyRenewalTokenMax {
		return "", errSelfPlanPurchaseStore
	}
	return encoded, nil
}

// Token authentication deliberately does not apply TTL; committed replay is
// resolved before all new-renewal gates, including token expiration.
func (a *App) openSelfMonthlyRenewalToken(encoded string, session selfSession) (selfMonthlyRenewalToken, time.Time, time.Time, error) {
	invalid := func() (selfMonthlyRenewalToken, time.Time, time.Time, error) {
		return selfMonthlyRenewalToken{}, time.Time{}, time.Time{}, errSelfPlanQuoteInvalid
	}
	if a == nil || a.secrets == nil || a.secrets.aead == nil {
		return selfMonthlyRenewalToken{}, time.Time{}, time.Time{}, errSelfPlanPurchaseStore
	}
	if encoded == "" || len(encoded) > selfMonthlyRenewalTokenMax {
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
	plain, err := a.secrets.aead.Open(nil, data[:nonceSize], data[nonceSize:], []byte(selfMonthlyRenewalPurpose))
	if err != nil {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	var token selfMonthlyRenewalToken
	if err := decoder.Decode(&token); err != nil {
		return invalid()
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid()
	}
	canonical, err := json.Marshal(token)
	issued, expires, valid := validSelfMonthlyRenewalToken(token, session)
	if err != nil || !bytes.Equal(canonical, plain) || !valid {
		return invalid()
	}
	return token, issued, expires, nil
}

func selfMonthlyRenewalFinancialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, financial.ErrNotFound):
		selfError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, financial.ErrConflict), errors.Is(err, financial.ErrInsufficient):
		selfError(w, http.StatusConflict, "renewal_unavailable")
	default:
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
	}
}

func (a *App) selfMonthlyRenewalQuote(w http.ResponseWriter, r *http.Request, session selfSession) {
	id, valid := selfMonthlyRenewalPath(r, "/renewal-quotes")
	if !valid || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if a == nil || a.store == nil || a.store.db == nil || a.secrets == nil || a.secrets.aead == nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
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
	quote, err := financial.NewCommercial(a.store.db).ReadEmployeeMonthlyRenewalQuoteTx(ctx, tx, session.EmployeeID, id, a.selfMonthlyRenewalTime())
	if err != nil {
		selfMonthlyRenewalFinancialError(w, err)
		return
	}
	if err := a.recheckSelfPlanPurchaseTx(ctx, tx, r, session, nil); err != nil {
		a.selfPlanPurchaseAuthError(w, err)
		return
	}
	commit := tx.Commit
	if a.selfMonthlyRenewalQuoteCommit != nil {
		commit = func() error { return a.selfMonthlyRenewalQuoteCommit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	now := a.selfMonthlyRenewalTime()
	token := selfMonthlyRenewalToken{
		Version: 1, Purpose: selfMonthlyRenewalPurpose, EmployeeID: session.EmployeeID, Session: session.Selector,
		PredecessorID: quote.PredecessorID, PredecessorEnd: quote.PredecessorEnd, Expected: quote.Expected,
		IssuedAt: now.Format(time.RFC3339Nano), ExpiresAt: now.Add(selfMonthlyRenewalLifetime).Format(time.RFC3339Nano),
	}
	encoded, err := a.sealSelfMonthlyRenewalToken(token)
	if err != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"quote_token": encoded, "predecessor_id": quote.PredecessorID, "predecessor_period_end_at": quote.PredecessorEnd,
		"plan_id": quote.Expected.PlanID, "revision": quote.Expected.Revision, "currency": quote.Expected.Currency,
		"interval": "monthly", "price_micro": strconv.FormatInt(quote.Expected.PriceMicro, 10),
		"credit_micro": strconv.FormatInt(quote.Expected.CreditMicro, 10), "expires_at": token.ExpiresAt,
	})
}

func (a *App) selfMonthlyRenewal(w http.ResponseWriter, r *http.Request, session selfSession) {
	id, valid := selfMonthlyRenewalPath(r, "/renew")
	if !valid || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength < 0 || r.ContentLength > selfMaxBody || len(r.TransferEncoding) != 0 {
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
	token, issued, expires, err := a.openSelfMonthlyRenewalToken(fields["quote_token"], session)
	if errors.Is(err, errSelfPlanPurchaseStore) {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if err != nil || token.PredecessorID != id {
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
	if a.selfMonthlyRenewalBeforeTx != nil {
		a.selfMonthlyRenewalBeforeTx()
	}
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
	input := financial.EmployeeMonthlyRenewalInput{
		OperationID: operationID, Actor: financial.Actor{Kind: financial.ActorEmployee, ID: session.EmployeeID},
		Owner: selfPurchaseOwner(session.EmployeeID), PredecessorID: id, PredecessorEnd: token.PredecessorEnd,
		Expected: token.Expected, ObservedAt: a.selfMonthlyRenewalTime(),
	}
	commercial := financial.NewCommercial(a.store.db)
	result, replay, err := commercial.ProbeEmployeeMonthlyRenewalReplayTx(ctx, tx, input)
	if err != nil {
		selfMonthlyRenewalFinancialError(w, err)
		return
	}
	if !replay {
		now := a.selfMonthlyRenewalTime()
		if now.Before(issued) || !now.Before(expires) {
			selfError(w, http.StatusConflict, "renewal_unavailable")
			return
		}
		input.ObservedAt = now
		result, err = commercial.RenewEmployeeMonthlySubscriptionTx(ctx, tx, input)
		if err != nil {
			selfMonthlyRenewalFinancialError(w, err)
			return
		}
	}
	if a.selfMonthlyRenewalBeforeCommit != nil {
		a.selfMonthlyRenewalBeforeCommit(tx)
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
	if a.selfMonthlyRenewalCommit != nil {
		commit = func() error { return a.selfMonthlyRenewalCommit(tx) }
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
	end := ""
	if result.Subscription.PeriodEndAt != nil {
		end = result.Subscription.PeriodEndAt.Format(selfMonthlyRenewalEndLayout)
	}
	writeJSON(w, status, map[string]any{
		"operation_id": operationID, "predecessor_id": id, "subscription_id": result.Subscription.ID,
		"replay": replay, "plan_id": result.Subscription.PlanID, "revision": result.Subscription.PlanRevision,
		"currency": result.Subscription.Currency, "interval": result.Subscription.Interval,
		"price_micro":  strconv.FormatInt(result.Subscription.PriceMicro, 10),
		"credit_micro": strconv.FormatInt(result.Subscription.CreditMicro, 10),
		"started_at":   result.Subscription.StartedAt.Format(time.RFC3339Nano), "period_end_at": end,
	})
}
