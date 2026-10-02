package service

// Independently authored for docs/employee-self-subscription-cancel-contract.md.
// The HTTP layer owns the auth recheck, transaction, final expiry guard, and commit.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"cpacloud.local/server/internal/financial"
	"golang.org/x/crypto/bcrypt"
)

type selfSubscriptionCancelInput struct {
	OperationID      string
	ExpectedRevision int64
	Password         string
}

func readSelfSubscriptionCancelInput(w http.ResponseWriter, r *http.Request) (selfSubscriptionCancelInput, bool) {
	invalid := func() (selfSubscriptionCancelInput, bool) {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return selfSubscriptionCancelInput{}, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, selfMaxBody)
	raw, err := io.ReadAll(r.Body)
	if err != nil || !utf8.Valid(raw) {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	first, err := decoder.Token()
	if err != nil || first != json.Delim('{') {
		return invalid()
	}
	fields := make(map[string]json.RawMessage, 3)
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || (key != "operation_id" && key != "expected_revision" && key != "current_password") {
			return invalid()
		}
		if _, exists := fields[key]; exists {
			return invalid()
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return invalid()
		}
		fields[key] = value
	}
	last, err := decoder.Token()
	if err != nil || last != json.Delim('}') || len(fields) != 3 {
		return invalid()
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return invalid()
	}
	var input selfSubscriptionCancelInput
	if len(bytes.TrimSpace(fields["operation_id"])) == 0 || bytes.TrimSpace(fields["operation_id"])[0] != '"' ||
		len(bytes.TrimSpace(fields["current_password"])) == 0 || bytes.TrimSpace(fields["current_password"])[0] != '"' ||
		json.Unmarshal(fields["operation_id"], &input.OperationID) != nil || json.Unmarshal(fields["current_password"], &input.Password) != nil ||
		!utf8.ValidString(input.OperationID) || !validSelfPurchaseID(input.OperationID, 128) {
		return invalid()
	}
	revision, err := strconv.ParseInt(string(fields["expected_revision"]), 10, 64)
	if err != nil || revision < 1 || revision > selfPlanPurchaseRevisionMax || strconv.FormatInt(revision, 10) != string(fields["expected_revision"]) {
		return invalid()
	}
	input.ExpectedRevision = revision
	return input, true
}

func (a *App) selfSubscriptionCancelTime() time.Time {
	if a.selfSubscriptionCancelNow != nil {
		return a.selfSubscriptionCancelNow().UTC()
	}
	return time.Now().UTC()
}

func (a *App) selfSubscriptionCancel(w http.ResponseWriter, r *http.Request, session selfSession) {
	peer, ok := a.selfGate(w, r, session.EmployeeID)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if r.URL.RawQuery != "" || r.URL.ForceQuery || !utf8.ValidString(id) || !validSelfPurchaseID(id, 256) ||
		r.URL.EscapedPath() != "/self/api/v1/billing/subscriptions/"+url.PathEscape(id)+"/cancel" {
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
	if a == nil || a.store == nil || a.store.db == nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
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
	passwordOK := bcrypt.CompareHashAndPassword(comparison, []byte(request.Password)) == nil
	if !passwordOK || len(priorHash) == 0 {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if cost, err := bcrypt.Cost(priorHash); err != nil || cost != 12 {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if a.selfSubscriptionCancelBeforeTx != nil {
		a.selfSubscriptionCancelBeforeTx()
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
	input := financial.EmployeeSubscriptionCancelInput{
		OperationID: request.OperationID, Actor: financial.Actor{Kind: financial.ActorEmployee, ID: session.EmployeeID},
		SubscriptionID: id, ExpectedRevision: request.ExpectedRevision, ObservedAt: a.selfSubscriptionCancelTime(),
	}
	commercial := financial.NewCommercial(a.store.db)
	result, replay, err := commercial.ProbeEmployeeSubscriptionCancelReplayTx(ctx, tx, input)
	if err != nil {
		selfSubscriptionCancelFinancialError(w, err)
		return
	}
	if !replay {
		input.ObservedAt = a.selfSubscriptionCancelTime()
		result, err = commercial.CancelEmployeeSubscriptionTx(ctx, tx, input)
		if err != nil {
			selfSubscriptionCancelFinancialError(w, err)
			return
		}
	}
	if a.selfSubscriptionCancelBeforeCommit != nil {
		a.selfSubscriptionCancelBeforeCommit(tx)
	}
	if err := a.recheckSelfPlanPurchaseTx(ctx, tx, r, session, priorHash); err != nil {
		a.selfPlanPurchaseAuthError(w, err)
		return
	}
	if !replay && result.Subscription.PeriodEndAt != nil && !a.selfSubscriptionCancelTime().Before(*result.Subscription.PeriodEndAt) {
		selfError(w, http.StatusConflict, "cancel_unavailable")
		return
	}
	if ctx.Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	commit := tx.Commit
	if a.selfSubscriptionCancelCommit != nil {
		commit = func() error { return a.selfSubscriptionCancelCommit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.clearSelfFailures(peer, session.EmployeeID)
	writeJSON(w, http.StatusOK, map[string]any{
		"operation_id": request.OperationID, "subscription_id": result.Subscription.ID,
		"replay": replay, "status": "cancelled", "revision": result.Subscription.Revision,
		"cancelled_at": result.Receipt.CreatedAt.Format(time.RFC3339Nano),
	})
}

func selfSubscriptionCancelFinancialError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, financial.ErrNotFound):
		selfError(w, http.StatusNotFound, "subscription_not_found")
	case errors.Is(err, financial.ErrConflict):
		selfError(w, http.StatusConflict, "cancel_unavailable")
	default:
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
	}
}
