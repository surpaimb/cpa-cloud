// Independently authored for docs/employee-self-plan-purchase-route-boundary-contract.md.
package service

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	selfPlanPurchaseQuotePath = "/self/api/v1/billing/plan-purchase-quotes"
	selfPlanPurchasePath      = "/self/api/v1/billing/subscriptions"
	selfPlanPurchasePathBytes = 8192
	selfPlanPurchaseUnescapes = 16
)

type selfPlanPurchasePathView struct {
	value    string
	complete bool
}

func boundedSelfPlanPurchasePath(value string) selfPlanPurchasePathView {
	if len(value) > selfPlanPurchasePathBytes {
		return selfPlanPurchasePathView{value: value[:selfPlanPurchasePathBytes]}
	}
	return selfPlanPurchasePathView{value: value, complete: true}
}

func selfPlanPurchaseEscapedView(decodedPath selfPlanPurchasePathView) selfPlanPurchasePathView {
	escaped := boundedSelfPlanPurchasePath((&url.URL{Path: decodedPath.value}).EscapedPath())
	escaped.complete = escaped.complete && decodedPath.complete
	return escaped
}

func selfPlanPurchaseRequestPath(requestURI string) selfPlanPurchasePathView {
	if requestURI == "" {
		return selfPlanPurchasePathView{}
	}
	// The extra byte proves whether a path of exactly 8192 bytes ends at a
	// literal query separator. Never scan an unbounded query.
	limit := len(requestURI)
	if limit > selfPlanPurchasePathBytes+1 {
		limit = selfPlanPurchasePathBytes + 1
	}
	if question := strings.IndexByte(requestURI[:limit], '?'); question >= 0 {
		return selfPlanPurchasePathView{value: requestURI[:question], complete: true}
	}
	return boundedSelfPlanPurchasePath(requestURI)
}

func selfPlanPurchaseASCIIPath(value string) string {
	var normalized strings.Builder
	normalized.Grow(len(value))
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\\' {
			c = '/'
		} else if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		normalized.WriteByte(c)
	}
	return normalized.String()
}

func selfPlanPurchaseQuoteSegment(value string, complete bool) bool {
	if !strings.HasPrefix(value, selfPlanPurchaseQuotePath) {
		return false
	}
	tail := value[len(selfPlanPurchaseQuotePath):]
	return complete && tail == "" || strings.HasPrefix(tail, "/")
}

func selfPlanPurchaseShapedView(view selfPlanPurchasePathView) bool {
	if view.value == "" {
		return false
	}
	for round := 0; ; round++ {
		plain := selfPlanPurchaseASCIIPath(view.value)
		parts := make([]string, 0, 10)
		for _, segment := range strings.Split(plain, "/") {
			if segment != "" && segment != "." {
				parts = append(parts, segment)
			}
		}
		lexical := "/" + strings.Join(parts, "/")
		if strings.HasSuffix(plain, "/") {
			// Preserve an observed separator even when the following bytes
			// lie beyond the classification budget.
			lexical += "/"
		}
		cleaned := path.Clean(plain)
		if selfPlanPurchaseQuoteSegment(plain, view.complete) ||
			selfPlanPurchaseQuoteSegment(lexical, view.complete) ||
			(view.complete && selfPlanPurchaseQuoteSegment(cleaned, true)) ||
			(view.complete && (plain == selfPlanPurchasePath || lexical == selfPlanPurchasePath || cleaned == selfPlanPurchasePath)) {
			return true
		}
		if round == selfPlanPurchaseUnescapes {
			return false
		}
		decoded, err := url.PathUnescape(view.value)
		if err != nil || decoded == view.value {
			return false
		}
		view.value = decoded
	}
}

// Only rejection uses decoded or cleaned views. A successful purchase still
// reaches the original ServeMux route with the untouched request target.
func selfPlanPurchaseRouteShape(r *http.Request) (shaped, literal bool) {
	if r == nil || r.URL == nil {
		return false, false
	}
	rawRequest := selfPlanPurchaseRequestPath(r.RequestURI)
	decodedPath := boundedSelfPlanPurchasePath(r.URL.Path)
	rawPath := boundedSelfPlanPurchasePath(r.URL.RawPath)
	// EscapedPath validates RawPath over its full length. Derive a separately
	// bounded rejection view from Path instead of calling it on oversized data.
	escaped := selfPlanPurchaseEscapedView(decodedPath)
	if rawRequest.complete && decodedPath.complete && rawPath.complete && escaped.complete {
		for _, target := range [...]string{selfPlanPurchaseQuotePath, selfPlanPurchasePath} {
			if rawRequest.value == target && decodedPath.value == target &&
				(r.URL.RawPath == "" || rawPath.value == target) && r.URL.EscapedPath() == target {
				return true, true
			}
		}
	}
	for _, view := range [...]selfPlanPurchasePathView{rawRequest, decodedPath, escaped, rawPath} {
		if selfPlanPurchaseShapedView(view) {
			return true, false
		}
	}
	return false, false
}

func (a *App) selfPlanPurchaseRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// GET subscriptions belongs to the independently gated read route.
		if r.Method != http.MethodPost {
			next.ServeHTTP(w, r)
			return
		}
		shaped, literal := selfPlanPurchaseRouteShape(r)
		if !shaped {
			next.ServeHTTP(w, r)
			return
		}
		if !a.cfg.EmployeeSelfServiceEnabled || !a.cfg.EmployeeSelfPlanPurchaseEnabled {
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
		a.requireSelfReleased(func(w http.ResponseWriter, r *http.Request, session selfSession) {
			if _, ok := a.selfGate(w, r, session.EmployeeID); !ok {
				return
			}
			selfError(w, http.StatusBadRequest, "invalid_request")
		}, true)(w, r)
	})
}
