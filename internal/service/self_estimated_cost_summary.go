// Independently authored for docs/employee-self-upstream-estimated-cost-summary-contract.md.
package service

import (
	"context"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"cpacloud.local/server/internal/accounting"
)

const selfEstimatedCostPath = "/self/api/v1/usage/estimated-cost-summary"

const (
	selfEstimatedCostShapeBytes  = 8192
	selfEstimatedCostDecodeLimit = 16
)

type selfEstimatedCostGroup struct {
	Currency     *string `json:"price_currency"`
	Attempts     string  `json:"attempts"`
	KnownMicro   string  `json:"known_estimated_cost_micro"`
	UnknownCount string  `json:"unknown_cost_attempts"`
}

type selfEstimatedCostResponse struct {
	From     string `json:"from"`
	To       string `json:"to"`
	Attempts struct {
		Total    string `json:"total"`
		Pending  string `json:"pending"`
		Terminal string `json:"terminal"`
	} `json:"attempts"`
	Costs []selfEstimatedCostGroup `json:"costs"`
}

// Independently authored for docs/employee-self-estimated-cost-route-boundary-contract.md.
// These views are only evidence for rejection. Never route a decoded view.
func selfEstimatedCostRouteShape(r *http.Request) (shaped, literal bool) {
	if r.URL == nil {
		return false, false
	}
	pathView, pathComplete := selfEstimatedCostPrefix(r.URL.Path)
	rawPathView, rawPathComplete := selfEstimatedCostPrefix(r.URL.RawPath)
	// For rejection, check both possible EscapedPath sources separately. Go's
	// EscapedPath chooses between RawPath and escaping Path by validating the
	// entire RawPath; that full validation is outside this guard's budget.
	escapedSource := (&url.URL{Path: pathView}).EscapedPath()
	escapedView, escapedComplete := selfEstimatedCostPrefix(escapedSource)
	escapedComplete = escapedComplete && pathComplete
	rawView, rawComplete := selfEstimatedCostPrefix(r.RequestURI)
	if r.RequestURI == "" { // In-process callers may not have an HTTP request line.
		rawView, rawComplete = escapedView, escapedComplete
	} else if cut := strings.IndexByte(rawView, '?'); cut >= 0 {
		rawView, rawComplete = rawView[:cut], true
	}
	literal = rawComplete && rawView == selfEstimatedCostPath && pathComplete && pathView == selfEstimatedCostPath &&
		(r.URL.RawPath == "" || rawPathComplete && rawPathView == selfEstimatedCostPath)
	if literal {
		return true, true
	}
	for _, view := range [...]struct {
		value    string
		complete bool
	}{{rawView, rawComplete}, {pathView, pathComplete}, {escapedView, escapedComplete}, {rawPathView, rawPathComplete}} {
		if view.value != "" && selfEstimatedCostShapedPrefix(view.value, view.complete) {
			return true, false
		}
	}
	return false, false
}

func selfEstimatedCostPrefix(value string) (string, bool) {
	if len(value) > selfEstimatedCostShapeBytes {
		return value[:selfEstimatedCostShapeBytes], false
	}
	return value, true
}

func selfEstimatedCostShapedPath(view string) bool {
	view, complete := selfEstimatedCostPrefix(view)
	return selfEstimatedCostShapedPrefix(view, complete)
}

func selfEstimatedCostShapedPrefix(view string, complete bool) bool {
	for round := 0; ; round++ {
		if selfEstimatedCostShapedCandidate(view, complete) {
			return true
		}
		if round == selfEstimatedCostDecodeLimit {
			return selfEstimatedCostResidualProof(view, complete)
		}
		decoded, err := url.PathUnescape(view)
		if err != nil || decoded == view {
			return false
		}
		view = decoded
	}
}

func selfEstimatedCostShapedCandidate(candidate string, complete bool) bool {
	// Lowercase ASCII only: URL path identity must not depend on Unicode folds.
	var normalized strings.Builder
	normalized.Grow(len(candidate))
	for i := 0; i < len(candidate); i++ {
		c := candidate[i]
		if c == '\\' {
			c = '/'
		} else if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		normalized.WriteByte(c)
	}
	plain := normalized.String()
	parts := make([]string, 0, 10)
	for _, segment := range strings.Split(plain, "/") {
		if segment != "" && segment != "." {
			parts = append(parts, segment)
		}
	}
	lexical := "/" + strings.Join(parts, "/")
	cleaned := path.Clean(plain)
	// Cleaning discards a terminal slash. On a truncated view that slash is
	// still positive boundary evidence; the end of the prefix alone is not.
	if !complete && strings.HasSuffix(plain, "/") && (lexical == selfEstimatedCostPath || cleaned == selfEstimatedCostPath) {
		return true
	}
	for _, variant := range [...]string{plain, lexical, cleaned} {
		if selfEstimatedCostSegmentProof(variant, complete) || selfEstimatedCostEncodedTail(variant) {
			return true
		}
	}
	return false
}

func selfEstimatedCostSegmentProof(candidate string, complete bool) bool {
	if !strings.HasPrefix(candidate, selfEstimatedCostPath) {
		return false
	}
	tail := candidate[len(selfEstimatedCostPath):]
	return (complete && tail == "") || strings.HasPrefix(tail, "/")
}

func selfEstimatedCostEncodedTail(candidate string) bool {
	if !strings.HasPrefix(candidate, selfEstimatedCostPath) {
		return false
	}
	tail := candidate[len(selfEstimatedCostPath):]
	return strings.HasPrefix(tail, "%3f") || strings.HasPrefix(tail, "%3b") || strings.HasPrefix(tail, "%2e")
}

func selfEstimatedCostHex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

// Read one literal ASCII byte or one arbitrarily %25-wrapped percent escape.
// The input and this proof are both bounded; no decoded path is constructed.
func selfEstimatedCostProofByte(input string, offset int) (value byte, next int, encoded, ok bool) {
	if offset >= len(input) {
		return 0, offset, false, false
	}
	if input[offset] != '%' {
		return input[offset], offset + 1, false, true
	}
	i := offset + 1
	for i+3 < len(input) && input[i] == '2' && input[i+1] == '5' {
		i += 2
	}
	if i+1 >= len(input) {
		return 0, offset, false, false
	}
	hi, validHi := selfEstimatedCostHex(input[i])
	lo, validLo := selfEstimatedCostHex(input[i+1])
	if !validHi || !validLo {
		return 0, offset, false, false
	}
	return hi<<4 | lo, i + 2, true, true
}

func selfEstimatedCostResidualProof(candidate string, complete bool) bool {
	// Store only bounded prefix-match states, never a decoded or routable path.
	segments := [...]string{"self", "api", "v1", "usage", "estimated-cost-summary"}
	clean := []int{0}
	lexical := 0
	at := 0
	first := true
	for at < len(candidate) {
		separators := 0
		for at < len(candidate) {
			value, next, _, ok := selfEstimatedCostProofByte(candidate, at)
			if !ok {
				return false
			}
			if value != '/' && value != '\\' {
				break
			}
			at = next
			separators++
		}
		if first && separators == 0 {
			return false
		}
		first = false
		if at == len(candidate) {
			break
		}
		priorClean, priorLexical := clean[len(clean)-1], lexical
		matches := [len(segments)]bool{true, true, true, true, true}
		length, dotsOnly := 0, true
		for at < len(candidate) {
			value, next, encoded, ok := selfEstimatedCostProofByte(candidate, at)
			if !ok {
				return false
			}
			if value == '/' || value == '\\' {
				break
			}
			if value >= 'A' && value <= 'Z' {
				value += 'a' - 'A'
			}
			if length == len(segments[4]) && matches[4] && (priorClean == 4 || priorLexical == 4) &&
				encoded && (value == '?' || value == ';' || value == '.') {
				return true
			}
			for i, segment := range segments {
				if length >= len(segment) || value != segment[length] {
					matches[i] = false
				}
			}
			if value != '.' {
				dotsOnly = false
			}
			length++
			at = next
		}
		for i, segment := range segments {
			matches[i] = matches[i] && length == len(segment)
		}
		if matches[4] && (priorClean == 4 || priorLexical == 4) && (at < len(candidate) || complete) {
			return true
		}
		if dotsOnly && length == 1 {
			continue
		}
		if dotsOnly && length == 2 {
			if len(clean) > 1 {
				clean = clean[:len(clean)-1]
			}
			lexical = -1
			continue
		}
		nextClean := -1
		if priorClean >= 0 && priorClean < len(segments) && matches[priorClean] {
			nextClean = priorClean + 1
		}
		clean = append(clean, nextClean)
		if priorLexical >= 0 && priorLexical < len(segments) && matches[priorLexical] {
			lexical = priorLexical + 1
		} else {
			lexical = -1
		}
	}
	return false
}

// Match only an inner guard's bounded first URL.Path view. The selected guard
// will recognize this view immediately, so its deeper decoding loop is never
// used as a probe or as part of the cost guard's own work budget.
func selfEstimatedCostInnerFirstView(value, route string) bool {
	clean := path.Clean(value)
	if clean == route || strings.HasPrefix(clean, route+"/") {
		return true
	}
	parts := make([]string, 0, 10)
	for _, segment := range strings.Split(value, "/") {
		if segment != "" && segment != "." {
			parts = append(parts, segment)
		}
	}
	lexical := "/" + strings.Join(parts, "/")
	return lexical == route || strings.HasPrefix(lexical, route+"/")
}

func (a *App) selfEstimatedCostInnerFirstGuard(urlPath string, terminal http.Handler) http.Handler {
	if len(urlPath) > selfEstimatedCostShapeBytes {
		return nil
	}
	// Preserve each sibling's actual first-view normalization and chain order.
	slashView := strings.ReplaceAll(strings.ToLower(urlPath), `\`, "/")
	switch {
	case selfEstimatedCostInnerFirstView(slashView, selfTopupCreditPath):
		return a.selfTopupCreditRouteGuard(terminal)
	case selfEstimatedCostInnerFirstView(slashView, selfAdminAdjustmentPath):
		return a.selfAdminAdjustmentRouteGuard(terminal)
	case selfEstimatedCostInnerFirstView(slashView, selfRedemptionHistoryPath):
		return a.selfRedemptionHistoryRouteGuard(terminal)
	case selfClassificationShapedPath(urlPath):
		return a.selfClassificationRouteGuard(terminal)
	case selfRedemptionShapedPath(urlPath, false):
		return a.selfRedemptionRouteGuard(terminal)
	default:
		return nil
	}
}

func (a *App) selfEstimatedCostRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shaped, literal := selfEstimatedCostRouteShape(r)
		if !shaped {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if !literal {
			// This terminal handler is deliberately not the ordinary inner chain:
			// a sibling decline must never reach ServeMux or the web fallback.
			terminal := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !a.cfg.EmployeeSelfServiceEnabled || !a.cfg.EmployeeSelfUpstreamEstimatedCostSummaryEnabled {
					http.NotFound(w, r)
					return
				}
				a.requireSelfReleased(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
					selfError(w, http.StatusBadRequest, "invalid_request")
				}, false)(w, r)
			})
			if r.URL != nil {
				if sibling := a.selfEstimatedCostInnerFirstGuard(r.URL.Path, terminal); sibling != nil {
					sibling.ServeHTTP(w, r)
					return
				}
			}
			terminal.ServeHTTP(w, r)
			return
		}
		if !a.cfg.EmployeeSelfServiceEnabled || !a.cfg.EmployeeSelfUpstreamEstimatedCostSummaryEnabled {
			http.NotFound(w, r)
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

func (a *App) selfEstimatedCostSummary(w http.ResponseWriter, r *http.Request, session selfSession) {
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	to := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	from := to.Add(-24 * time.Hour)
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	summary, err := accounting.NewLedger(a.store.db).ReadEmployeeEstimatedCost(ctx, session.EmployeeID, from, to)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	result := selfEstimatedCostResponse{From: from.Format(time.RFC3339), To: to.Format(time.RFC3339), Costs: make([]selfEstimatedCostGroup, 0, len(summary.Groups))}
	result.Attempts.Total, result.Attempts.Pending, result.Attempts.Terminal = decimal(summary.Total), decimal(summary.Pending), decimal(summary.Terminal)
	for _, group := range summary.Groups {
		result.Costs = append(result.Costs, selfEstimatedCostGroup{Currency: group.Currency, Attempts: decimal(group.Attempts), KnownMicro: decimal(group.KnownMicro), UnknownCount: decimal(group.UnknownCount)})
	}
	if a.selfEstimatedCostBeforeFinal != nil {
		a.selfEstimatedCostBeforeFinal()
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	current, err := a.selfRedemptionHistorySessionCurrent(ctx, session)
	if err != nil || ctx.Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if !current {
		selfError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	writeJSON(w, http.StatusOK, result)
}
