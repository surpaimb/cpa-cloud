package service

// Independently authored for docs/employee-self-subscription-renewal-links-contract.md
// and docs/employee-self-subscription-renewal-links-route-boundary-contract.md.

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
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, "/") {
		return false
	}
	// A literal ServeMux cleaning redirect has first claim on ownership.
	// Decode after cleaning so escaped dot segments cannot steal a sibling.
	original := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
	if shaped, decided := renewalLinksViewDecision(renewalLinksRouteViews(renewalLinksCleanView(original)), false); decided {
		return shaped
	}
	views := renewalLinksRouteViews(original)
	if shaped, decided := renewalLinksViewDecision(views, false); decided {
		return shaped
	}
	// The remaining cleaned views are rejection-only: they recognize encoded
	// dot or prefix aliases without rewriting a request sent to ServeMux.
	shaped, _ := renewalLinksViewDecision(views, true)
	return shaped
}

func renewalLinksRouteViews(segments []string) [][]string {
	// Keep literal slash boundaries over at most two decoding passes. An
	// encoded slash inside an ID must not manufacture an operation segment.
	views := make([][]string, 0, 3)
	for pass := 0; pass < 3; pass++ {
		views = append(views, segments)
		decoded := make([]string, len(segments))
		changed := false
		for i, segment := range segments {
			value, err := url.PathUnescape(segment)
			if err != nil {
				return views
			}
			decoded[i] = value
			changed = changed || value != segment
		}
		if !changed {
			break
		}
		segments = decoded
	}
	return views
}

func renewalLinksViewDecision(views [][]string, clean bool) (shaped, decided bool) {
	for _, view := range views {
		if clean {
			view = renewalLinksCleanView(view)
		}
		if operation, ok := renewalLinksOperation(view); ok {
			if operation == "renewal-links" {
				return true, true
			}
			if operation != "" && !strings.ContainsRune(operation, '%') {
				return false, true
			}
		}
	}
	return false, false
}

func renewalLinksCleanView(view []string) []string {
	cleaned := make([]string, 0, len(view))
	for _, segment := range view {
		switch segment {
		case "", ".":
			continue
		case "..":
			if len(cleaned) != 0 {
				cleaned = cleaned[:len(cleaned)-1]
			}
		default:
			cleaned = append(cleaned, segment)
		}
	}
	return cleaned
}

func renewalLinksOperation(view []string) (string, bool) {
	if len(view) < 7 || view[0] != "self" || view[1] != "api" || view[2] != "v1" ||
		view[3] != "billing" || view[4] != "subscriptions" {
		return "", false
	}
	for _, operation := range view[6:] {
		if operation != "" && operation != "." && operation != ".." {
			return operation, true
		}
	}
	return "", true
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
		if !a.cfg.EmployeeSelfSubscriptionRenewalLinksEnabled {
			http.NotFound(w, r)
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
