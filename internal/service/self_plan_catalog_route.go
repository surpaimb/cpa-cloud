// Independently authored for docs/employee-self-plan-catalog-route-boundary-contract.md.
// Path behavior follows the public Go net/http and net/url documentation.
package service

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	selfPlanCatalogRoutePath      = "/self/api/v1/billing/plans"
	selfPlanCatalogRouteMaxBytes  = 8192
	selfPlanCatalogRouteMaxDecode = 16
)

type catalogPathEvidence struct {
	text     string
	complete bool
}

func catalogBoundPath(text string) catalogPathEvidence {
	if len(text) > selfPlanCatalogRouteMaxBytes {
		return catalogPathEvidence{text: text[:selfPlanCatalogRouteMaxBytes]}
	}
	return catalogPathEvidence{text: text, complete: true}
}

func catalogRequestTargetPath(requestURI string) catalogPathEvidence {
	if requestURI == "" {
		return catalogPathEvidence{}
	}
	end := len(requestURI)
	if end > selfPlanCatalogRouteMaxBytes+1 {
		end = selfPlanCatalogRouteMaxBytes + 1
	}
	if query := strings.IndexByte(requestURI[:end], '?'); query >= 0 {
		return catalogPathEvidence{text: requestURI[:query], complete: true}
	}
	return catalogBoundPath(requestURI)
}

func catalogEscapedEvidence(decoded catalogPathEvidence) catalogPathEvidence {
	escaped := catalogBoundPath((&url.URL{Path: decoded.text}).EscapedPath())
	escaped.complete = escaped.complete && decoded.complete
	return escaped
}

func catalogFoldPath(text string) string {
	folded := []byte(text)
	for i, c := range folded {
		switch {
		case c == '\\':
			folded[i] = '/'
		case c >= 'A' && c <= 'Z':
			folded[i] = c + ('a' - 'A')
		}
	}
	return string(folded)
}

func catalogDropEmptyAndDotSegments(text string) string {
	segments := strings.Split(text, "/")
	kept := make([]string, 0, len(segments))
	for _, segment := range segments {
		if segment != "" && segment != "." {
			kept = append(kept, segment)
		}
	}
	result := "/" + strings.Join(kept, "/")
	if strings.HasSuffix(text, "/") && !strings.HasSuffix(result, "/") {
		result += "/"
	}
	return result
}

func catalogHasCompleteSegment(text string, complete bool) bool {
	if !strings.HasPrefix(text, selfPlanCatalogRoutePath) {
		return false
	}
	tail := text[len(selfPlanCatalogRoutePath):]
	return (complete && tail == "") || strings.HasPrefix(tail, "/")
}

func catalogRejectEvidence(view catalogPathEvidence) bool {
	if view.text == "" {
		return false
	}
	for decode := 0; decode <= selfPlanCatalogRouteMaxDecode; decode++ {
		folded := catalogFoldPath(view.text)
		collapsed := catalogDropEmptyAndDotSegments(folded)
		if catalogHasCompleteSegment(folded, view.complete) ||
			catalogHasCompleteSegment(collapsed, view.complete) ||
			(view.complete && catalogHasCompleteSegment(path.Clean(folded), true)) {
			return true
		}
		if decode == selfPlanCatalogRouteMaxDecode {
			break
		}
		unescaped, err := url.PathUnescape(view.text)
		if err != nil || unescaped == view.text {
			break
		}
		view.text = unescaped
	}
	return false
}

// Only complete, literal original views may enter the existing catalog handler.
// Normalization is evidence for rejecting an alias, never a route rewrite.
func selfPlanCatalogRouteShape(r *http.Request) (owned, literal bool) {
	if r == nil || r.URL == nil {
		return false, false
	}
	wire := catalogRequestTargetPath(r.RequestURI)
	decoded := catalogBoundPath(r.URL.Path)
	raw := catalogBoundPath(r.URL.RawPath)
	escaped := catalogEscapedEvidence(decoded)
	if wire.complete && decoded.complete && raw.complete && escaped.complete &&
		wire.text == selfPlanCatalogRoutePath && decoded.text == selfPlanCatalogRoutePath &&
		(raw.text == "" || raw.text == selfPlanCatalogRoutePath) &&
		r.URL.EscapedPath() == selfPlanCatalogRoutePath {
		return true, true
	}
	for _, view := range [...]catalogPathEvidence{wire, decoded, raw, escaped} {
		if catalogRejectEvidence(view) {
			return true, false
		}
	}
	return false, false
}

func (a *App) selfPlanCatalogRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		owned, literal := selfPlanCatalogRouteShape(r)
		if !owned {
			next.ServeHTTP(w, r)
			return
		}
		if !a.cfg.EmployeeSelfServiceEnabled || !a.cfg.EmployeeSelfPlanCatalogEnabled {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Del("Location")
			w.Header().Del("Allow")
			http.NotFound(w, r)
			return
		}
		if literal {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Del("Location")
		w.Header().Del("Allow")
		a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
			selfError(w, http.StatusBadRequest, "invalid_request")
		}, false)(w, r)
	})
}
