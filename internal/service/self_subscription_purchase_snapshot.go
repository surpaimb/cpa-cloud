package service

// Independently authored for docs/employee-self-subscription-purchase-snapshot-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"unicode/utf8"

	"cpacloud.local/server/internal/financial"
)

func selfPurchaseSnapshotPath(r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if r.URL.RawQuery != "" || r.URL.ForceQuery || !utf8.ValidString(id) || !validSelfPurchaseID(id, 256) ||
		r.URL.EscapedPath() != "/self/api/v1/billing/subscriptions/"+url.PathEscape(id)+"/purchase-snapshot" {
		return "", false
	}
	return id, true
}

func (a *App) selfSubscriptionPurchaseSnapshot(w http.ResponseWriter, r *http.Request, session selfSession) {
	id, ok := selfPurchaseSnapshotPath(r)
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
	// Reuse the existing current-session validation within this same snapshot.
	if err := a.recheckSelfOneShotReadTx(ctx, tx, r, session); err != nil {
		selfOneShotReadAuthError(w, err)
		return
	}
	snapshot, err := financial.NewCommercial(a.store.db).ReadEmployeeSubscriptionPurchaseSnapshotTx(ctx, tx, session.EmployeeID, id)
	if errors.Is(err, financial.ErrNotFound) {
		selfError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if err := a.recheckSelfOneShotReadTx(ctx, tx, r, session); err != nil {
		selfOneShotReadAuthError(w, err)
		return
	}
	commit := tx.Commit
	if a.selfPurchaseSnapshotCommit != nil {
		commit = func() error { return a.selfPurchaseSnapshotCommit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subscription_id": snapshot.SubscriptionID,
		"plan_id":         snapshot.PlanID,
		"plan_revision":   snapshot.PlanRevision,
		"currency":        snapshot.Currency,
		"interval":        snapshot.Interval,
		"price_micro":     strconv.FormatInt(snapshot.PriceMicro, 10),
		"credit_micro":    strconv.FormatInt(snapshot.CreditMicro, 10),
		"started_at":      snapshot.StartedAt,
		"period_end_at":   snapshot.PeriodEndAt,
	})
}
