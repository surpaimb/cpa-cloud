package service

// Integration authored from CPA Cloud's account-pool service contract.
import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"cpacloud.local/server/internal/scheduling"
)

type modelAdmissionError struct {
	status        int
	code, message string
}

// Called only after a committed authorization or routing change. Waiters reload
// their snapshot; already-admitted streams retain the established semantics.
func (a *App) notifyAccountPoolChanged() {
	if a.accountPool != nil {
		a.accountPool.NotifyChanged()
	}
}

func anthropicAdmissionType(status int) string {
	switch status {
	case 400:
		return "invalid_request_error"
	case 401:
		return "authentication_error"
	case 403:
		return "permission_error"
	case 429:
		return "rate_limit_error"
	default:
		return "api_error"
	}
}

func geminiAdmissionStatus(status int) string {
	switch status {
	case 400:
		return "INVALID_ARGUMENT"
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "PERMISSION_DENIED"
	case 409:
		return "ABORTED"
	case 429:
		return "RESOURCE_EXHAUSTED"
	default:
		return "UNAVAILABLE"
	}
}

func (a *App) selectModelRoute(r *http.Request, auth employeeAuth, model string, providers []string, record bool) (route, *accountPoolLease, *modelAdmissionError) {
	var selected route
	var lease *accountPoolLease
	legacy := true
	if a.accountPool != nil {
		sticky, err := a.poolSessionDigest(r, auth, model)
		if err != nil {
			return route{}, nil, err
		}
		result := a.accountPool.Acquire(r.Context(), model, auth, providers, sticky)
		legacy = result.Legacy
		if !legacy {
			if result.Code != accountPoolAcquired {
				return route{}, nil, poolAdmissionFailure(result.Code)
			}
			selected, lease = result.Route, result.Lease
		}
	}
	if legacy {
		a.admission.RLock()
		var failure *modelAdmissionError
		selected, failure = a.legacyEmployeeRoute(r.Context(), auth, model, providers)
		a.admission.RUnlock()
		if failure != nil {
			return route{}, nil, failure
		}
	}
	if record {
		if err := a.beginRequestUsage(r, auth, model, selected); err != nil {
			if lease != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				lease.Release(ctx, scheduling.ReleaseResult{Failure: scheduling.FailureNone, Phase: scheduling.DispatchNotStarted})
			}
			return route{}, nil, poolAdmissionFailure(accountPoolStorageUnavailable)
		}
	}
	return selected, lease, nil
}

func (a *App) legacyEmployeeRoute(ctx context.Context, auth employeeAuth, model string, providers []string) (route, *modelAdmissionError) {
	var status, mode string
	var expires, revoked sql.NullString
	err := a.store.db.QueryRowContext(ctx, `SELECT e.status,e.model_mode,k.expires_at,k.revoked_at FROM access_keys k JOIN employees e ON e.id=k.employee_id WHERE k.id=? AND k.employee_id=?`, auth.KeyID, auth.EmployeeID).Scan(&status, &mode, &expires, &revoked)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return route{}, poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	if err != nil || status != "active" || revoked.Valid {
		return route{}, poolAdmissionFailure(accountPoolAuthorizationChanged)
	}
	if expires.Valid {
		expiry, err := parseTime(expires.String)
		if err != nil || !time.Now().Before(expiry) {
			return route{}, poolAdmissionFailure(accountPoolAuthorizationChanged)
		}
	}
	if mode == "selected" {
		var allowed int
		err := a.store.db.QueryRowContext(ctx, `SELECT 1 FROM employee_models WHERE employee_id=? AND model_id=?`, auth.EmployeeID, model).Scan(&allowed)
		if errors.Is(err, sql.ErrNoRows) {
			return route{}, poolAdmissionFailure(accountPoolModelNotAllowed)
		}
		if err != nil {
			return route{}, poolAdmissionFailure(accountPoolStorageUnavailable)
		}
	}
	var selected route
	err = a.store.db.QueryRowContext(ctx, `SELECT u.id,u.endpoint,m.upstream_model,u.credential_ciphertext,u.provider_kind,u.revision,u.credential_state,u.key_version FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.id=? AND m.enabled=1 AND m.archived=0 AND u.enabled=1 AND u.archived=0
		AND NOT EXISTS(SELECT 1 FROM account_recovery_states recovery WHERE recovery.account_id=u.id)`, model).Scan(&selected.AccountID, &selected.Endpoint, &selected.UpstreamModel, &selected.Ciphertext, &selected.ProviderKind, &selected.Revision, &selected.CredentialState, &selected.KeyVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return route{}, poolAdmissionFailure(accountPoolNoCompatible)
	}
	if err != nil {
		return route{}, poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	if !providerAllowed(selected.ProviderKind, providers) {
		return route{}, poolAdmissionFailure(accountPoolNoCompatible)
	}
	return selected, nil
}

func (a *App) poolSessionDigest(r *http.Request, auth employeeAuth, model string) (string, *modelAdmissionError) {
	values := r.Header.Values("X-CPA-Session")
	if len(values) == 0 {
		return "", nil
	}
	if len(values) != 1 || len(values[0]) < 1 || len(values[0]) > 256 || strings.TrimSpace(values[0]) != values[0] {
		return "", poolAdmissionFailure(accountPoolInvalid)
	}
	for _, char := range values[0] {
		if unicode.IsControl(char) {
			return "", poolAdmissionFailure(accountPoolInvalid)
		}
	}
	purpose := "pool-session/v1\x00" + auth.EmployeeID + "\x00" + auth.KeyID + "\x00" + model + "\x00" + r.URL.Path
	return hex.EncodeToString(a.secrets.digest(purpose, values[0])), nil
}

// validatePoolSession performs the metadata-only part of sticky routing before
// governance admission. It must remain free of database and network effects;
// selectModelRoute recomputes the same digest after admission for routing.
func (a *App) validatePoolSession(r *http.Request, auth employeeAuth, model string) *modelAdmissionError {
	_, failure := a.poolSessionDigest(r, auth, model)
	return failure
}

func poolAdmissionFailure(code accountPoolRuntimeCode) *modelAdmissionError {
	switch code {
	case accountPoolInvalid:
		return &modelAdmissionError{400, "invalid_request_error", "Invalid account-pool session metadata."}
	case accountPoolAuthorizationChanged:
		return &modelAdmissionError{401, "invalid_api_key", "Invalid API key."}
	case accountPoolModelNotAllowed:
		return &modelAdmissionError{403, "model_not_allowed", "Model is not allowed for this key."}
	case accountPoolCapacityUnavailable, accountPoolQueueFull:
		return &modelAdmissionError{429, "account_capacity_unavailable", "Account capacity is temporarily unavailable."}
	case accountPoolConfigurationChanged, accountPoolAccountChanged:
		return &modelAdmissionError{409, "route_changed", "The route changed before execution; submit a new request."}
	case accountPoolStorageUnavailable:
		return &modelAdmissionError{503, "storage_unavailable", "Service is temporarily unavailable."}
	default:
		return &modelAdmissionError{503, "no_available_route", "No available route for this model."}
	}
}

// Execution failures only cool accounts for later independent requests. The
// separate preparation helper owns the bounded, never-dispatched failover path.
func (a *App) releaseModelLease(lease *accountPoolLease, reqID string, record bool) {
	defer a.cleanupRequestUsage(reqID)
	if lease == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := scheduling.ReleaseResult{Failure: scheduling.FailureNone, StreamCommitted: true, ExecutionUncertain: true}
	dispatched := true
	if value, ok := a.usageRequests.Load(reqID); ok {
		active := value.(*activeUsageRequest)
		active.mu.Lock()
		dispatched = active.attempt != nil
		active.mu.Unlock()
	}
	if record && dispatched {
		var outcome string
		var status sql.NullInt64
		err := a.store.db.QueryRowContext(ctx, `SELECT outcome,upstream_status FROM model_requests WHERE id=?`, reqID).Scan(&outcome, &status)
		if err != nil {
			// A storage read failure is not evidence of an account failure.
			result.Failure = scheduling.FailureNone
		} else if outcome == "failed" || outcome == "interrupted" || outcome == "running" {
			switch status.Int64 {
			case 401, 403:
				result.Failure = scheduling.FailureAuth
			case 429:
				result.Failure = scheduling.FailureRateLimit
			case 503, 529:
				result.Failure = scheduling.FailureOverloaded
			default:
				if status.Int64 == 0 || status.Int64 >= 500 || status.Int64 == 200 {
					result.Failure = scheduling.FailureTransient
				} else {
					result.Failure = scheduling.FailurePermanent
				}
			}
		}
	}
	lease.Release(ctx, result)
}

// The model directory uses the same enabled-account selection domain as the
// request path. Explicit pools replace the legacy primary, including when that
// primary is disabled. Busy/cooling accounts remain listed, since availability
// is temporary and neither state changes employee authorization.
func (a *App) availableModelRouteSQL(geminiOnly bool) string {
	eligible := func(alias string) string {
		condition := alias + ".enabled=1 AND " + alias + ".archived=0"
		if geminiOnly {
			return condition + " AND " + alias + ".provider_kind='gemini-api-key'"
		}
		if !a.cfg.ExperimentalCodexMembership {
			return condition + " AND " + alias + ".provider_kind<>'codex-membership'"
		}
		return condition + " AND (" + alias + ".provider_kind<>'codex-membership' OR " + alias + ".credential_state<>'reauth_required')"
	}
	if a.accountPool == nil {
		return "(" + eligible("u") + ")"
	}
	return "((NOT EXISTS(SELECT 1 FROM model_account_pool_configs pc WHERE pc.model_id=m.id) AND " + eligible("u") + ") OR EXISTS(SELECT 1 FROM model_account_pool_routes pr JOIN upstreams pu ON pu.id=pr.upstream_id WHERE pr.model_id=m.id AND " + eligible("pu") + "))"
}
