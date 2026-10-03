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

func selfEstimatedCostShapedPath(decoded string) bool {
	for candidate := strings.ToLower(decoded); ; {
		candidate = strings.ReplaceAll(candidate, `\`, "/")
		clean := path.Clean(candidate)
		if strings.HasPrefix(clean, selfEstimatedCostPath) {
			return true
		}
		parts := make([]string, 0, 10)
		for _, segment := range strings.Split(candidate, "/") {
			if segment != "" && segment != "." {
				parts = append(parts, segment)
			}
		}
		lexical := "/" + strings.Join(parts, "/")
		if strings.HasPrefix(lexical, selfEstimatedCostPath) {
			return true
		}
		unescaped, err := url.PathUnescape(candidate)
		if err != nil || unescaped == candidate {
			return false
		}
		candidate = unescaped
	}
}

func (a *App) selfEstimatedCostRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !selfEstimatedCostShapedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if !a.cfg.EmployeeSelfServiceEnabled || !a.cfg.EmployeeSelfUpstreamEstimatedCostSummaryEnabled {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != selfEstimatedCostPath || r.URL.EscapedPath() != selfEstimatedCostPath {
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
