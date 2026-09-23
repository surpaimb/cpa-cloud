package service

// Independently authored from docs/account-recovery-execution-plan.md. System
// probes have no employee identity and never enter the employee usage ledger.
import (
	"context"
	"errors"
	"net/http"
	"time"

	"cpacloud.local/server/internal/accounting"
)

var errSystemProbeStorage = errors.New("system probe storage unavailable")

func (a *App) initializeSystemProbeAccounting(ctx context.Context) error {
	ledger := accounting.NewSystemProbeLedger(a.store.db)
	if err := ledger.Migrate(ctx); err != nil {
		return errSystemProbeStorage
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errSystemProbeStorage
	}
	defer tx.Rollback()
	// Recovery is metadata-only. An uncertain prior attempt must never be
	// resumed by reusing its operation ID, including when probes are disabled.
	now := time.Now().UTC()
	if _, err := ledger.InterruptPendingTx(ctx, tx, now); err != nil {
		return errSystemProbeStorage
	}
	if _, err := tx.ExecContext(ctx, `UPDATE account_recovery_states SET state='interrupted',
		next_probe_at=CASE WHEN next_probe_at>? THEN next_probe_at ELSE ? END,
		updated_at=CASE WHEN updated_at>? THEN updated_at ELSE ? END WHERE state='in_progress'`,
		formatAccountPoolTime(now.Add(recoveryRetryDelay)), formatAccountPoolTime(now.Add(recoveryRetryDelay)),
		formatAccountPoolTime(now), formatAccountPoolTime(now)); err != nil {
		return errSystemProbeStorage
	}
	if err := tx.Commit(); err != nil {
		return errSystemProbeStorage
	}
	a.systemProbes = ledger
	return nil
}

func (a *App) registerSystemProbeHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/system-probes/summary", a.requireAdmin(a.systemProbeSummary, false))
}

func (a *App) systemProbeSummary(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "This summary does not accept query parameters.")
		return
	}
	if a.systemProbes == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageQueryTimeout)
	defer cancel()
	items, err := a.systemProbes.Summarize(ctx)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	result := make([]usageAttemptSummary, 0, len(items))
	for _, item := range items {
		result = append(result, usageAttemptSummary{
			Currency: item.Currency, Total: decimal(item.Total), Pending: decimal(item.Pending),
			Succeeded: decimal(item.Succeeded), Failed: decimal(item.Failed),
			Cancelled: decimal(item.Cancelled), Interrupted: decimal(item.Interrupted),
			KnownCostMicro: decimal(item.KnownCostMicro), UnknownCostAttempts: decimal(item.UnknownCostAttempts),
			InputTokens: systemProbeTokenSummary(item.InputTokens), OutputTokens: systemProbeTokenSummary(item.OutputTokens),
			CacheReadTokens: systemProbeTokenSummary(item.CacheReadTokens), CacheWriteTokens: systemProbeTokenSummary(item.CacheWriteTokens),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"scope": "system_probes", "currencies": result})
}

func systemProbeTokenSummary(value accounting.KnownValueSummary) usageTokenCounts {
	return usageTokenCounts{KnownTotal: decimal(value.KnownTotal), UnknownAttempts: decimal(value.UnknownAttempts)}
}
