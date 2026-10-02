package service

// Independently authored for docs/employee-self-redemption-contract.md.
// The self credential and immutable financial chain share one commit boundary.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"path"
	"strconv"
	"time"

	"cpacloud.local/server/internal/financial"
	"golang.org/x/crypto/bcrypt"
)

const selfRedemptionPath = "/self/api/v1/billing/redemptions"

func (a *App) selfRedemptionRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ServeMux can canonicalize encoded or doubled path separators before
		// a route is selected; a code-bearing POST must never be redirected.
		if r.URL.Path == selfRedemptionPath || path.Clean(r.URL.Path) == selfRedemptionPath {
			if r.URL.Path != selfRedemptionPath || r.URL.EscapedPath() != selfRedemptionPath {
				http.NotFound(w, r)
				return
			}
			if !a.cfg.EmployeeSelfRedemptionEnabled {
				http.NotFound(w, r)
				return
			}
			if r.Method != http.MethodPost {
				a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
					w.Header().Set("Allow", http.MethodPost)
					selfError(w, http.StatusMethodNotAllowed, "method_not_allowed")
				}, false)(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) redemptionTime() time.Time {
	if a.selfRedemptionNow != nil {
		return a.selfRedemptionNow().UTC()
	}
	return time.Now().UTC()
}

func selfRedemptionFinancialError(w http.ResponseWriter, err error) {
	if errors.Is(err, financial.ErrConflict) || errors.Is(err, financial.ErrNotFound) || errors.Is(err, financial.ErrInsufficient) {
		selfError(w, http.StatusConflict, "redemption_unavailable")
		return
	}
	selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
}

func (a *App) selfRedeemCode(w http.ResponseWriter, r *http.Request, session selfSession) {
	if r.URL.Path != selfRedemptionPath || r.URL.EscapedPath() != selfRedemptionPath || r.URL.RawQuery != "" ||
		r.URL.ForceQuery || r.ContentLength < 0 || r.ContentLength > selfMaxBody || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	peer, ok := a.selfGate(w, r, session.EmployeeID)
	if !ok {
		return
	}
	fields, ok := readSelfFields(w, r, "operation_id", "code", "current_password")
	if !ok {
		a.selfFailure(peer, session.EmployeeID)
		return
	}
	operationID, code, password := fields["operation_id"], fields["code"], fields["current_password"]
	if !validSelfPurchaseID(operationID, 128) || len(code) > 256 {
		a.selfFailure(peer, session.EmployeeID)
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
	err := a.store.db.QueryRowContext(r.Context(), `SELECT password_hash,typeof(password_hash) FROM employee_self_credentials WHERE employee_id=?`, session.EmployeeID).Scan(&priorHash, &hashType)
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
	if a.secrets == nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	digest := a.secrets.digest(billingWebhookPurpose, code)
	if len(digest) != sha256.Size {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	var codeDigest [sha256.Size]byte
	copy(codeDigest[:], digest)
	if a.selfRedemptionBeforeTx != nil {
		a.selfRedemptionBeforeTx()
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
	input := financial.EmployeeRedemptionInput{OperationID: operationID,
		Actor:      financial.Actor{Kind: financial.ActorEmployee, ID: session.EmployeeID},
		Owner:      financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: session.EmployeeID},
		CodeDigest: codeDigest, ObservedAt: a.redemptionTime()}
	result, err := financial.NewCommercial(a.store.db).RedeemEmployeeCodeTx(ctx, tx, input)
	if err != nil {
		if errors.Is(err, financial.ErrConflict) {
			a.selfFailure(peer, session.EmployeeID)
		}
		selfRedemptionFinancialError(w, err)
		return
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
	if a.selfRedemptionCommit != nil {
		commit = func() error { return a.selfRedemptionCommit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.clearSelfFailures(peer, session.EmployeeID)
	status := http.StatusCreated
	if result.Replay {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"operation_id": operationID, "replay": result.Replay,
		"currency": result.Currency, "amount_micro": strconv.FormatInt(result.AmountMicro, 10),
		"credited_at": result.CreditedAt.Format(time.RFC3339Nano)})
}
