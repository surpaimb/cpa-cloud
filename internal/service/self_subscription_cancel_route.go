package service

// Independently authored for
// docs/employee-self-subscription-cancel-route-boundary-contract.md.

import (
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

const selfCancelRoutePrefix = "/self/api/v1/billing/subscriptions/"

// Inspect only a bounded number of decoding views. These views classify a
// request; they never rewrite the URL passed to the canonical POST handler.
func selfCancelRouteViews(r *http.Request) []string {
	view := r.URL.EscapedPath()
	views := make([]string, 0, 6)
	for i := 0; i < 3; i++ {
		views = append(views, view, path.Clean(view))
		decoded, err := url.PathUnescape(view)
		if err != nil || decoded == view {
			break
		}
		view = decoded
	}
	return views
}

// The first operation in the least-decoded matching view owns the route. In
// particular, an escaped slash inside an ID cannot let a later decoded
// "/cancel" steal a renewal-links or other sibling operation.
func selfCancelSiblingOperation(view string) bool {
	if !strings.HasPrefix(view, selfCancelRoutePrefix) {
		return false
	}
	rest := strings.TrimPrefix(view, selfCancelRoutePrefix)
	firstSlash := strings.IndexByte(rest, '/')
	if firstSlash < 0 {
		return false
	}
	operation := strings.SplitN(rest[firstSlash+1:], "/", 2)[0]
	for i := 0; i < 3; i++ {
		for _, sibling := range []string{"renew", "renewal-quotes", "renewal-links", "one-shot-renewal", "purchase-snapshot"} {
			if operation == sibling || strings.HasPrefix(operation, sibling+"/") {
				return true
			}
		}
		decoded, err := url.PathUnescape(operation)
		if err != nil || decoded == operation {
			break
		}
		operation = decoded
	}
	return false
}

func selfCancelSegmentInPath(view string) bool {
	if !strings.HasPrefix(view, selfCancelRoutePrefix) {
		return false
	}
	segments := strings.Split(strings.TrimPrefix(view, selfCancelRoutePrefix), "/")
	for i := 1; i < len(segments); i++ {
		if segments[i] == "cancel" {
			return true
		}
	}
	return false
}

func selfCancelShapedPath(r *http.Request) bool {
	views := selfCancelRouteViews(r)
	// A dot/duplicate-separator path that cleans into a cancellation route
	// is not a valid sibling route, even if its first raw operation is one.
	// Catch it before ServeMux can redirect to the cleaned cancellation path.
	for _, view := range views {
		cleaned := path.Clean(view)
		if cleaned != view && selfCancelSegmentInPath(cleaned) {
			return true
		}
	}
	for _, view := range views {
		if strings.HasPrefix(view, selfCancelRoutePrefix) {
			if selfCancelSiblingOperation(view) {
				return false
			}
			break
		}
	}
	for _, view := range views {
		if selfCancelSegmentInPath(view) {
			return true
		}
	}
	return false
}

func selfCancelCanonicalPath(r *http.Request) bool {
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, selfCancelRoutePrefix) || !strings.HasSuffix(escaped, "/cancel") {
		return false
	}
	segment := strings.TrimSuffix(strings.TrimPrefix(escaped, selfCancelRoutePrefix), "/cancel")
	if segment == "" || strings.ContainsRune(segment, '/') {
		return false
	}
	id, err := url.PathUnescape(segment)
	return err == nil && utf8.ValidString(id) && validSelfPurchaseID(id, 256) &&
		!strings.ContainsAny(id, "/\\%") && id != "." && id != ".." && url.PathEscape(id) == segment
}

// This guard must wrap ServeMux even while the feature is off: ServeMux may
// otherwise clean a cancel-shaped path or redirect before its method-aware
// pattern and /self/api/ fallback can decide the response.
func (a *App) selfCancelRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !selfCancelShapedPath(r) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.cfg.EmployeeSelfSubscriptionCancelEnabled {
			http.NotFound(w, r)
			return
		}
		if !selfCancelCanonicalPath(r) {
			a.requireSelfReleased(func(w http.ResponseWriter, r *http.Request, session selfSession) {
				if r.Method == http.MethodPost {
					peer, ok := a.selfGate(w, r, session.EmployeeID)
					if !ok {
						return
					}
					a.selfFailure(peer, session.EmployeeID)
				}
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
