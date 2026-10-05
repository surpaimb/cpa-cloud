// Independently authored for
// docs/employee-self-subscription-status-route-boundary-contract.md.
// The normalization below classifies rejection shapes only. It never rewrites
// a request or transfers an ID subroute or sibling to the collection reader.
package service

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	selfSubscriptionCollectionPath      = "/self/api/v1/billing/subscriptions"
	selfSubscriptionCollectionMaxPath   = 8192
	selfSubscriptionCollectionMaxPrefix = 1024
	selfSubscriptionCollectionMaxDecode = 16
)

// Separate the request-target's path without charging query bytes to the
// path budget. A literal '?' at path byte 8193 terminates an 8192-byte path.
func subscriptionCollectionTargetPath(requestURI string) (wirePath, target string, origin, ok bool) {
	if requestURI == "" {
		return "", "", false, false
	}
	origin = requestURI[0] == '/'
	start := 0
	if !origin {
		probe := requestURI
		if len(probe) > selfSubscriptionCollectionMaxPrefix+1 {
			probe = probe[:selfSubscriptionCollectionMaxPrefix+1]
		}
		colon := strings.IndexByte(probe, ':')
		if colon < 1 || colon+1 >= len(probe) || probe[colon+1] != '/' {
			return "", "", false, false
		}
		start = colon + 1
		if start+1 < len(probe) && probe[start+1] == '/' {
			// Locate the first slash after the authority. The slash belongs
			// to the path, including when the URL authority is empty.
			slash := strings.IndexByte(probe[start+2:], '/')
			if slash < 0 {
				return "", "", false, false
			}
			start += 2 + slash
		}
		if start > selfSubscriptionCollectionMaxPrefix {
			return "", "", false, false
		}
	}
	remaining := requestURI[start:]
	look := remaining
	if len(look) > selfSubscriptionCollectionMaxPath+1 {
		look = look[:selfSubscriptionCollectionMaxPath+1]
	}
	if question := strings.IndexByte(look, '?'); question >= 0 {
		wirePath = remaining[:question]
	} else {
		if len(remaining) > selfSubscriptionCollectionMaxPath {
			return "", "", origin, false
		}
		wirePath = remaining
	}
	if wirePath == "" || len(wirePath) > selfSubscriptionCollectionMaxPath {
		return "", "", origin, false
	}
	return wirePath, requestURI[:start+len(wirePath)], origin, true
}

func subscriptionCollectionUserEqual(left, right *url.Userinfo) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.String() == right.String()
}

// A complete collection segment must survive bounded decoding and cleaning.
// A nonempty ID below the collection, or a sibling visited under /billing,
// vetoes ownership even when later dot segments clean back to the collection.
func subscriptionCollectionDestination(view string) (string, bool) {
	if view == "" || len(view) > selfSubscriptionCollectionMaxPath {
		return "", false
	}
	value := view
	for i := 0; i < selfSubscriptionCollectionMaxDecode; i++ {
		decoded, err := url.PathUnescape(value)
		if err != nil || len(decoded) > selfSubscriptionCollectionMaxPath {
			return "", false
		}
		if decoded == value {
			break
		}
		value = decoded
	}
	if strings.Contains(value, "%") {
		return "", false
	}
	folded := strings.ReplaceAll(value, "\\", "/")
	parts := strings.Split(folded, "/")
	stack := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			continue
		}
		if len(stack) == 4 &&
			stack[0] == "self" && stack[1] == "api" &&
			stack[2] == "v1" && stack[3] == "billing" &&
			part != "subscriptions" {
			return "", false
		}
		if len(stack) == 5 &&
			stack[0] == "self" && stack[1] == "api" &&
			stack[2] == "v1" && stack[3] == "billing" &&
			stack[4] == "subscriptions" {
			return "", false
		}
		stack = append(stack, part)
	}
	return path.Clean(folded), true
}

func selfSubscriptionCollectionShape(r *http.Request) (owned, literal bool) {
	if r == nil || r.URL == nil || r.URL.Opaque != "" {
		return false, false
	}
	wire, target, origin, ok := subscriptionCollectionTargetPath(r.RequestURI)
	if !ok || len(r.URL.Path) > selfSubscriptionCollectionMaxPath ||
		len(r.URL.RawPath) > selfSubscriptionCollectionMaxPath {
		return false, false
	}
	decoded, err := url.PathUnescape(wire)
	if err != nil || decoded != r.URL.Path {
		return false, false
	}
	if r.URL.RawPath != "" {
		rawDecoded, err := url.PathUnescape(r.URL.RawPath)
		if err != nil || rawDecoded != r.URL.Path {
			return false, false
		}
	}
	escaped := r.URL.EscapedPath()
	derived := (&url.URL{Path: r.URL.Path}).EscapedPath()
	if len(escaped) > selfSubscriptionCollectionMaxPath ||
		len(derived) > selfSubscriptionCollectionMaxPath ||
		escaped != wire {
		return false, false
	}
	if origin {
		if r.URL.Scheme != "" || r.URL.Host != "" || r.URL.User != nil {
			return false, false
		}
	} else {
		parsed, err := url.ParseRequestURI(target)
		if err != nil || parsed.Opaque != "" ||
			parsed.Scheme != r.URL.Scheme || parsed.Host != r.URL.Host ||
			!subscriptionCollectionUserEqual(parsed.User, r.URL.User) ||
			parsed.Path != r.URL.Path || parsed.RawPath != r.URL.RawPath {
			return false, false
		}
	}
	views := []string{wire, r.URL.Path, escaped, derived}
	if r.URL.RawPath != "" {
		views = append(views, r.URL.RawPath)
	}
	for _, view := range views {
		destination, ok := subscriptionCollectionDestination(view)
		if !ok || destination != selfSubscriptionCollectionPath {
			return false, false
		}
	}
	literal = origin && wire == selfSubscriptionCollectionPath &&
		r.URL.Path == selfSubscriptionCollectionPath &&
		(r.URL.RawPath == "" || r.URL.RawPath == selfSubscriptionCollectionPath) &&
		escaped == selfSubscriptionCollectionPath
	return true, literal
}

func (a *App) selfSubscriptionCollectionRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		owned, literal := selfSubscriptionCollectionShape(r)
		if !owned {
			next.ServeHTTP(w, r)
			return
		}
		if !a.cfg.EmployeeSelfServiceEnabled || !a.cfg.EmployeeSelfSubscriptionStatusEnabled {
			w.Header().Del("Location")
			w.Header().Del("Allow")
			http.NotFound(w, r)
			return
		}
		if literal {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Del("Location")
		w.Header().Del("Allow")
		a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
			selfError(w, http.StatusBadRequest, "invalid_request")
		}, false)(w, r)
	})
}
