package service

// Independently authored from account-recovery-coordinator-contract.md.
// This API changes administrator settings only; it cannot trigger an arbitrary
// model request and never accepts credentials, prompts or endpoint overrides.
import (
	"context"
	"errors"
	"net/http"
	"time"
)

func (a *App) registerAccountRecoveryHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/account-recovery", a.requireAdmin(a.getAccountRecovery, false))
	mux.HandleFunc("PUT /admin/api/v1/account-recovery", a.requireAdmin(a.setAccountRecovery, true))
	mux.HandleFunc("GET /admin/api/v1/account-recovery/accounts", a.requireAdmin(a.listAccountRecovery, false))
}

func (a *App) getAccountRecovery(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !a.validRecoveryQuery(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	result, err := a.recovery.Snapshot(ctx)
	if err != nil {
		writeRecoveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) setAccountRecovery(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !a.validRecoveryQuery(w, r) {
		return
	}
	var input struct {
		Enabled          *bool `json:"enabled"`
		ExpectedRevision int64 `json:"expected_revision"`
	}
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	if input.Enabled == nil || input.ExpectedRevision <= 0 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "enabled and a positive expected_revision are required.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := a.recovery.SetEnabled(ctx, input.ExpectedRevision, *input.Enabled); err != nil {
		writeRecoveryError(w, err)
		return
	}
	result, err := a.recovery.Snapshot(ctx)
	if err != nil {
		writeRecoveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (a *App) listAccountRecovery(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !a.validRecoveryQuery(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	items, err := a.recovery.AccountSnapshots(ctx)
	if err != nil {
		writeRecoveryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "server_time": time.Now().UTC()})
}

func (a *App) validRecoveryQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "This endpoint does not accept query parameters.")
		return false
	}
	if a.recovery == nil {
		writeRecoveryError(w, nil)
		return false
	}
	return true
}

func writeRecoveryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errAccountRecoveryNotAllowed):
		writeAdminError(w, http.StatusForbidden, "recovery_not_allowed", "Start the service with --allow-account-recovery before enabling recovery.")
	case errors.Is(err, errAccountRecoverySettingConflict):
		writeAdminError(w, http.StatusConflict, "revision_conflict", "Recovery settings changed; reload before updating.")
	default:
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Recovery state is unavailable. Query the current settings before retrying.")
	}
}
