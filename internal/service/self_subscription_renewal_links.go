package service

// Independently authored for docs/employee-self-subscription-renewal-links-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"cpacloud.local/server/internal/financial"
)

func selfRenewalLinksShapedPath(r *http.Request) bool {
	const prefix = "/self/api/v1/billing/subscriptions/"
	const suffix = "/renewal-links"
	escaped, decoded := r.URL.EscapedPath(), r.URL.Path
	return strings.HasPrefix(escaped, prefix) && strings.Contains(strings.TrimPrefix(escaped, prefix), suffix) ||
		strings.HasPrefix(decoded, prefix) && strings.Contains(strings.TrimPrefix(decoded, prefix), suffix)
}

func malformedSelfRenewalLinksPath(r *http.Request) bool {
	const prefix = "/self/api/v1/billing/subscriptions/"
	const suffix = "/renewal-links"
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, prefix) || !strings.HasSuffix(escaped, suffix) {
		return true
	}
	segment := strings.TrimSuffix(strings.TrimPrefix(escaped, prefix), suffix)
	if strings.ContainsRune(segment, '/') {
		return true
	}
	id, err := url.PathUnescape(segment)
	return err != nil || !validSelfPurchaseID(id, 256) || strings.ContainsAny(id, "/\\%") ||
		id == "." || id == ".." || url.PathEscape(id) != segment
}

func selfRenewalLinksPath(r *http.Request) (string, bool) {
	id := r.PathValue("id")
	return id, validSelfPurchaseID(id, 256) && !strings.ContainsAny(id, "/\\%") && id != "." && id != ".." &&
		r.URL.EscapedPath() == "/self/api/v1/billing/subscriptions/"+url.PathEscape(id)+"/renewal-links"
}

func (a *App) selfRenewalLinksRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !selfRenewalLinksShapedPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		if malformedSelfRenewalLinksPath(r) {
			a.requireSelfReleased(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
				selfError(w, http.StatusBadRequest, "invalid_request")
			}, false)(w, r)
			return
		}
		if r.Method != http.MethodGet {
			a.requireSelfReleased(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
				w.Header().Set("Allow", http.MethodGet)
				selfError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			}, false)(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) selfSubscriptionRenewalLinks(w http.ResponseWriter, r *http.Request, session selfSession) {
	id, valid := selfRenewalLinksPath(r)
	if !valid || r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if a == nil || a.store == nil || a.store.db == nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	// requireSelfReleased performed the initial authentication. This lock
	// serializes the final read/response decision with self logout and disable.
	a.admission.Lock()
	defer a.admission.Unlock()
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
	links, err := financial.NewCommercial(a.store.db).ReadEmployeeSubscriptionRenewalLinksTx(ctx, tx, session.EmployeeID, id)
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
	if a.selfRenewalLinksCommit != nil {
		commit = func() error { return a.selfRenewalLinksCommit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subscription_id": id, "predecessor_id": links.PredecessorID, "successor_id": links.SuccessorID,
	})
}
