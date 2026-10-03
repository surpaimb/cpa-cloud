package service

// Independently authored for docs/employee-self-one-shot-route-boundary-contract.md.
// See the Go net/http ServeMux and net/url EscapedPath/PathUnescape documentation:
// https://pkg.go.dev/net/http#ServeMux and https://pkg.go.dev/net/url#URL.EscapedPath.

import (
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"
)

const selfOneShotRoutePrefix = "/self/api/v1/billing/subscriptions/"

type selfOneShotRouteKind uint8

const (
	selfOneShotRouteNone selfOneShotRouteKind = iota
	selfOneShotRouteStatus
	selfOneShotRouteDisarm
)

// The views are for rejection only. Neither the request path nor PathValue is
// changed, and decoding is bounded so an encoded alias never becomes valid.
func selfOneShotRouteViews(r *http.Request) []string {
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

func selfOneShotRouteSibling(view string) bool {
	if !strings.HasPrefix(view, selfOneShotRoutePrefix) {
		return false
	}
	rest := strings.TrimPrefix(view, selfOneShotRoutePrefix)
	slash := strings.IndexByte(rest, '/')
	if slash < 0 {
		return false
	}
	operation := strings.SplitN(rest[slash+1:], "/", 2)[0]
	for i := 0; i < 3; i++ {
		for _, sibling := range []string{"renew", "renewal-quotes", "renewal-links", "purchase-snapshot", "cancel"} {
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

func selfOneShotRouteOperation(view string) selfOneShotRouteKind {
	if !strings.HasPrefix(view, selfOneShotRoutePrefix) {
		return selfOneShotRouteNone
	}
	segments := strings.Split(strings.TrimPrefix(view, selfOneShotRoutePrefix), "/")
	for i, segment := range segments {
		if segment != "one-shot-renewal" {
			continue
		}
		// Without an ID separator, this text can itself be an opaque ID for
		// an unrelated operation. The bare operation and its disarm child
		// remain recognizable as empty-ID one-shot shapes.
		if i == 0 && len(segments) > 1 && segments[1] != "" && segments[1] != "disarm" {
			continue
		}
		for _, tail := range segments[i+1:] {
			if tail == "disarm" {
				return selfOneShotRouteDisarm
			}
		}
		return selfOneShotRouteStatus
	}
	return selfOneShotRouteNone
}

func selfOneShotShapedPath(r *http.Request) selfOneShotRouteKind {
	views := selfOneShotRouteViews(r)
	// A path that ServeMux would clean into this route must not redirect into
	// it, even when its raw spelling includes a sibling operation.
	for _, view := range views {
		clean := path.Clean(view)
		if clean != view {
			if kind := selfOneShotRouteOperation(clean); kind != selfOneShotRouteNone {
				return kind
			}
		}
	}
	// The first operation in the least-decoded view reserves sibling routes.
	for _, view := range views {
		if strings.HasPrefix(view, selfOneShotRoutePrefix) {
			if selfOneShotRouteSibling(view) {
				return selfOneShotRouteNone
			}
			break
		}
	}
	var kind selfOneShotRouteKind
	for _, view := range views {
		candidate := selfOneShotRouteOperation(view)
		if candidate > kind {
			kind = candidate
		}
	}
	return kind
}

func selfOneShotCanonicalRoute(r *http.Request, kind selfOneShotRouteKind) bool {
	suffix := "/one-shot-renewal"
	if kind == selfOneShotRouteDisarm {
		suffix += "/disarm"
	} else if kind != selfOneShotRouteStatus {
		return false
	}
	escaped := r.URL.EscapedPath()
	if !strings.HasPrefix(escaped, selfOneShotRoutePrefix) {
		return false
	}
	rest := strings.TrimPrefix(escaped, selfOneShotRoutePrefix)
	if !strings.HasSuffix(rest, suffix) {
		return false
	}
	segment := strings.TrimSuffix(rest, suffix)
	if segment == "" || strings.ContainsRune(segment, '/') {
		return false
	}
	id, err := url.PathUnescape(segment)
	if err != nil || !utf8.ValidString(id) || !validSelfPurchaseID(id, 256) || id == "." || id == ".." ||
		strings.ContainsAny(id, "/\\") || url.PathEscape(id) != segment {
		return false
	}
	// A second decoding pass must not turn an opaque ID into a path or alias.
	if decoded, err := url.PathUnescape(id); err == nil && decoded != id {
		if strings.ContainsAny(decoded, "/\\") || decoded == "." || decoded == ".." {
			return false
		}
	}
	return true
}

func (a *App) selfOneShotRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kind := selfOneShotShapedPath(r)
		if kind == selfOneShotRouteNone {
			next.ServeHTTP(w, r)
			return
		}
		if !a.cfg.EmployeeSelfOneShotRenewalDisarmEnabled {
			http.NotFound(w, r)
			return
		}
		if !selfOneShotCanonicalRoute(r, kind) {
			write := kind == selfOneShotRouteDisarm && r.Method == http.MethodPost
			if write {
				a.requireSelfReleased(func(w http.ResponseWriter, r *http.Request, session selfSession) {
					peer, ok := a.selfGate(w, r, session.EmployeeID)
					if !ok {
						return
					}
					a.selfFailure(peer, session.EmployeeID)
					selfError(w, http.StatusBadRequest, "invalid_request")
				}, true)(w, r)
				return
			}
			a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
				selfError(w, http.StatusBadRequest, "invalid_request")
			}, false)(w, r)
			return
		}
		allowed := http.MethodGet
		if kind == selfOneShotRouteDisarm {
			allowed = http.MethodPost
		}
		if r.Method != allowed {
			a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
				w.Header().Set("Allow", allowed)
				selfError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			}, false)(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
