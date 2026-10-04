// Independently authored for docs/employee-self-wallet-balance-route-boundary-contract.md.
// Path rules use the public Go net/http and net/url contracts; normalization is
// rejection evidence only and never changes the request passed to another owner.
package service

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

const (
	selfWalletBalanceRoutePath      = "/self/api/v1/billing/balance"
	selfWalletBalanceRouteMaxPath   = 8192
	selfWalletBalanceRouteMaxPrefix = 1024
	selfWalletBalanceRouteMaxDecode = 16
)

// balanceTargetPath extracts only complete, bounded path bytes. The authority
// search is bounded separately; a long query does not consume path budget.
func balanceTargetPath(requestURI string) (wirePath, targetWithoutQuery string, origin bool, ok bool) {
	if requestURI == "" {
		return "", "", false, false
	}
	start := 0
	origin = requestURI[0] == '/'
	if !origin {
		end := len(requestURI)
		if end > selfWalletBalanceRouteMaxPrefix+1 {
			end = selfWalletBalanceRouteMaxPrefix + 1
		}
		prefix := requestURI[:end]
		colon := strings.IndexByte(prefix, ':')
		if colon <= 0 || colon+1 >= len(prefix) || prefix[colon+1] != '/' {
			return "", "", false, false
		}
		start = colon + 1
		if start+1 < len(prefix) && prefix[start+1] == '/' {
			// In scheme://authority/path, find path after the authority.
			// In scheme:/path, the first slash already starts the path.
			firstPathSlash := strings.IndexByte(prefix[start+2:], '/')
			if firstPathSlash < 0 {
				return "", "", false, false
			}
			start += 2 + firstPathSlash
		}
		if start > selfWalletBalanceRouteMaxPrefix {
			return "", "", false, false
		}
	}
	remaining := requestURI[start:]
	end := len(remaining)
	if end > selfWalletBalanceRouteMaxPath+1 {
		end = selfWalletBalanceRouteMaxPath + 1
	}
	if query := strings.IndexByte(remaining[:end], '?'); query >= 0 {
		wirePath = remaining[:query]
	} else {
		if len(remaining) > selfWalletBalanceRouteMaxPath {
			return "", "", origin, false
		}
		wirePath = remaining
	}
	if len(wirePath) > selfWalletBalanceRouteMaxPath || wirePath == "" {
		return "", "", origin, false
	}
	return wirePath, requestURI[:start+len(wirePath)], origin, true
}

func balanceSameUserinfo(left, right *url.Userinfo) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.String() == right.String()
}

// Every source must agree on the final cleaned destination. A source that
// resolves to a sibling vetoes an earlier balance-looking segment.
func balanceFinalPath(value string) (string, bool) {
	if value == "" || len(value) > selfWalletBalanceRouteMaxPath {
		return "", false
	}
	for round := 0; round < selfWalletBalanceRouteMaxDecode; round++ {
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return "", false
		}
		if decoded == value {
			break
		}
		value = decoded
		if len(value) > selfWalletBalanceRouteMaxPath {
			return "", false
		}
	}
	// Do not decide ownership from a path whose next decode could reveal a
	// separator, dot segment, or sibling beyond the sixteen-round budget.
	if strings.Contains(value, "%") {
		return "", false
	}
	folded := []byte(value)
	for i, c := range folded {
		switch {
		case c == '\\':
			folded[i] = '/'
		case c >= 'A' && c <= 'Z':
			folded[i] = c + ('a' - 'A')
		}
	}
	return path.Clean(string(folded)), true
}

func selfWalletBalanceRouteShape(r *http.Request) (owned, literal bool) {
	if r == nil || r.URL == nil || r.URL.Opaque != "" {
		return false, false
	}
	wire, target, origin, ok := balanceTargetPath(r.RequestURI)
	if !ok || len(r.URL.Path) > selfWalletBalanceRouteMaxPath ||
		len(r.URL.RawPath) > selfWalletBalanceRouteMaxPath {
		return false, false
	}
	decodedWire, err := url.PathUnescape(wire)
	if err != nil || decodedWire != r.URL.Path {
		return false, false
	}
	if r.URL.RawPath != "" {
		decodedRaw, err := url.PathUnescape(r.URL.RawPath)
		if err != nil || decodedRaw != r.URL.Path {
			return false, false
		}
	}
	escaped := r.URL.EscapedPath()
	derived := (&url.URL{Path: r.URL.Path}).EscapedPath()
	if len(escaped) > selfWalletBalanceRouteMaxPath ||
		len(derived) > selfWalletBalanceRouteMaxPath || escaped != wire {
		return false, false
	}
	if !origin {
		parsed, err := url.ParseRequestURI(target)
		if err != nil || parsed.Opaque != "" || parsed.Scheme != r.URL.Scheme ||
			parsed.Host != r.URL.Host || !balanceSameUserinfo(parsed.User, r.URL.User) ||
			parsed.Path != r.URL.Path || parsed.RawPath != r.URL.RawPath {
			return false, false
		}
	} else if r.URL.Scheme != "" || r.URL.Host != "" || r.URL.User != nil {
		return false, false
	}
	views := [...]string{wire, r.URL.Path, escaped, derived}
	if r.URL.RawPath != "" {
		views = [...]string{wire, r.URL.Path, r.URL.RawPath, derived}
	}
	final := ""
	for _, view := range views {
		resolved, ok := balanceFinalPath(view)
		if !ok {
			return false, false
		}
		if final == "" {
			final = resolved
		} else if final != resolved {
			return false, false
		}
	}
	if final != selfWalletBalanceRoutePath &&
		!strings.HasPrefix(final, selfWalletBalanceRoutePath+"/") {
		return false, false
	}
	literal = origin && wire == selfWalletBalanceRoutePath &&
		r.URL.Path == selfWalletBalanceRoutePath &&
		(r.URL.RawPath == "" || r.URL.RawPath == selfWalletBalanceRoutePath) &&
		escaped == selfWalletBalanceRoutePath
	return true, literal
}

func (a *App) selfWalletBalanceRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		owned, literal := selfWalletBalanceRouteShape(r)
		if !owned {
			next.ServeHTTP(w, r)
			return
		}
		if !a.cfg.EmployeeSelfServiceEnabled || !a.cfg.EmployeeSelfWalletBalanceEnabled {
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
