package service

// Independently authored for docs/employee-self-subscription-purchase-snapshot-contract.md
// and docs/employee-self-subscription-purchase-snapshot-route-boundary-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"cpacloud.local/server/internal/financial"
)

// Classify snapshot-shaped requests before ServeMux can clean or redirect a
// path. The classification views are only used to reject; no view is passed
// to the business handler as a rewritten URL or PathValue.
func (a *App) selfPurchaseSnapshotRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !snapshotShapedRawPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.cfg.EmployeeSelfSubscriptionPurchaseSnapshotEnabled {
			http.NotFound(w, r)
			return
		}
		if malformedPurchaseSnapshotRawPath(r) {
			a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
				selfError(w, http.StatusBadRequest, "invalid_request")
			}, false)(w, r)
			return
		}
		if r.Method != http.MethodGet {
			a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
				w.Header().Set("Allow", http.MethodGet)
				selfError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			}, false)(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

const snapshotRoutePrefix = "/self/api/v1/billing/subscriptions/"

func snapshotRouteViews(r *http.Request) []string {
	view := r.URL.EscapedPath()
	views := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		views = append(views, view)
		decoded, err := url.PathUnescape(view)
		if err != nil || decoded == view {
			break
		}
		view = decoded
	}
	return views
}

func snapshotRouteOperation(view string) (string, bool) {
	if !strings.HasPrefix(view, snapshotRoutePrefix) {
		return "", false
	}
	rest := strings.TrimPrefix(view, snapshotRoutePrefix)
	separator := strings.IndexByte(rest, '/')
	if separator < 0 {
		return "", false
	}
	for _, segment := range strings.Split(rest[separator+1:], "/") {
		if segment != "" && segment != "." && segment != ".." {
			return segment, true
		}
	}
	return "", true
}

func snapshotShapedRawPath(r *http.Request) bool {
	views := snapshotRouteViews(r)
	// A cleaning redirect into this endpoint is ours to reject, even when
	// the original path contains dot segments or repeated prefix slashes.
	for _, view := range views {
		cleaned := path.Clean(view)
		if cleaned != view {
			if operation, ok := snapshotRouteOperation(cleaned); ok {
				if operation == "purchase-snapshot" {
					return true
				}
				if operation != "" && !strings.ContainsRune(operation, '%') {
					return false
				}
			}
		}
	}
	// The first unambiguous operation owns the path. In particular, a later
	// snapshot segment in a sibling or unknown tail does not steal that route.
	for _, view := range views {
		operation, ok := snapshotRouteOperation(view)
		if !ok {
			continue
		}
		if operation == "purchase-snapshot" {
			return true
		}
		if !strings.ContainsRune(operation, '%') {
			return false
		}
	}
	return false
}

func malformedPurchaseSnapshotRawPath(r *http.Request) bool {
	const suffix = "/purchase-snapshot"
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, snapshotRoutePrefix) {
		return true
	}
	rest := strings.TrimPrefix(escaped, snapshotRoutePrefix)
	if !strings.HasSuffix(rest, suffix) {
		return true
	}
	segment := strings.TrimSuffix(rest, suffix)
	if strings.Contains(segment, "/") {
		return true
	}
	id, err := url.PathUnescape(segment)
	return err != nil || !validSelfPurchaseID(id, 256) || strings.ContainsAny(id, "/\\%") || id == "." || id == ".." || url.PathEscape(id) != segment
}

func selfPurchaseSnapshotPath(r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if r.URL.RawQuery != "" || r.URL.ForceQuery || !utf8.ValidString(id) || !validSelfPurchaseID(id, 256) || strings.ContainsAny(id, "/\\%") || id == "." || id == ".." ||
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
