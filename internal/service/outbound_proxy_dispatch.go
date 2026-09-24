package service

// Independently authored from CPA Cloud's explicit egress and admission contracts.
import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"cpacloud.local/server/internal/egress"
	"cpacloud.local/server/internal/scheduling"
)

type upstreamHTTPDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// The account revision protects both direct and bound routes. Clients are
// immutable; an edit never silently replaces the exit of an existing request.
type routeEgress struct {
	proxyID            string
	connectionRevision int64
	client             upstreamHTTPDoer
}

func (a *App) routeEgressTx(ctx context.Context, tx *sql.Tx, selected route) (*routeEgress, error) {
	if a.outboundProxies == nil || a.proxyClients == nil {
		return nil, errOutboundProxyUnavailable
	}
	var enabled int
	var revision int64
	var provider, endpoint string
	err := tx.QueryRowContext(ctx, `SELECT enabled,revision,provider_kind,endpoint FROM upstreams WHERE id=? AND archived=0`, selected.AccountID).Scan(&enabled, &revision, &provider, &endpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errUpstreamProxyBindingConflict
	}
	if err != nil {
		return nil, err
	}
	if enabled != 1 || revision != selected.Revision || provider != selected.ProviderKind || endpoint != selected.Endpoint {
		return nil, errUpstreamProxyBindingConflict
	}
	snapshot, err := a.outboundProxies.LoadBindingTx(ctx, tx, selected.AccountID, selected.Revision)
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return &routeEgress{client: a.http}, nil
	}
	config := egress.ProxyConfig{ProxyID: snapshot.Proxy.ID, ConnectionRevision: snapshot.Proxy.ConnectionRevision, Host: snapshot.Proxy.Host, Port: uint16(snapshot.Proxy.Port), Scope: egress.AddressScope(snapshot.Proxy.AddressScope), AllowLoopbackForTesting: a.cfg.AllowLoopbackUpstream}
	if snapshot.Credential != nil {
		config.Credentials, err = egress.NewBasicCredentials(snapshot.Credential.username, snapshot.Credential.password)
		if err != nil {
			return nil, errOutboundProxyUnavailable
		}
	}
	client, err := a.proxyClients.Client(config)
	if err != nil {
		return nil, errOutboundProxyUnavailable
	}
	return &routeEgress{proxyID: config.ProxyID, connectionRevision: config.ConnectionRevision, client: client}, nil
}

func (a *App) prepareRouteEgress(ctx context.Context, selected route) (*routeEgress, error) {
	a.admission.RLock()
	defer a.admission.RUnlock()
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return a.routeEgressTx(ctx, tx, selected)
}

func sameRouteEgress(first, second *routeEgress) bool {
	return first != nil && second != nil && first.proxyID == second.proxyID && first.connectionRevision == second.connectionRevision
}

func egressAdmissionFailure(err error) *modelAdmissionError {
	if errors.Is(err, errUpstreamProxyBindingConflict) || errors.Is(err, errOutboundProxyConflict) || errors.Is(err, errOutboundProxyNotFound) {
		return poolAdmissionFailure(accountPoolAccountChanged)
	}
	if errors.Is(err, errOutboundProxyUnavailable) {
		return &modelAdmissionError{503, "proxy_unavailable", "The configured outbound proxy is unavailable."}
	}
	return poolAdmissionFailure(accountPoolStorageUnavailable)
}

// Final dispatch is serialized with authorization, route and proxy edits. No
// network runs under admission. The accounting write precedes MayHaveSent;
// failures here cannot start an upstream attempt or trigger a second account.
func (a *App) dispatchModelRoute(r *http.Request, auth employeeAuth, model string, selected route, lease *accountPoolLease, record bool, wire ...*http.Request) (upstreamHTTPDoer, *modelAdmissionError) {
	if lease != nil {
		lease.mu.Lock()
		defer lease.mu.Unlock()
		if lease.ctx.Err() != nil {
			return nil, poolAdmissionFailure(accountPoolCancelled)
		}
		if lease.finished || lease.phase != scheduling.DispatchNotStarted {
			return nil, poolAdmissionFailure(accountPoolConfigurationChanged)
		}
	}
	// Refresh/re-import serializes with the same account mutation lock rather
	// than admission. Keep it until the immutable attempt and dispatch phase
	// are committed, in the established lease -> mutation -> admission order.
	if selected.ProviderKind == codexMembershipProvider {
		unlock, err := a.acquireCodexMutationLock(r.Context(), selected.AccountID)
		if err != nil {
			return nil, poolAdmissionFailure(accountPoolCancelled)
		}
		defer unlock()
	}
	a.admission.RLock()
	defer a.admission.RUnlock()
	if r.Context().Err() != nil {
		return nil, poolAdmissionFailure(accountPoolCancelled)
	}
	budgetState, budgetFailure := a.prepareBudgetDispatch(r, selected, lease, record, wire)
	if budgetFailure != nil {
		return nil, budgetFailure
	}
	if budgetState != nil {
		defer budgetState.unlock()
	}
	var usageState *usageDispatchState
	if budgetState == nil {
		usageState, budgetFailure = a.prepareUsageDispatch(requestID(r.Context()), record)
		if budgetFailure != nil {
			return nil, budgetFailure
		}
		if usageState != nil {
			defer usageState.unlock()
		}
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		return nil, poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	defer tx.Rollback()
	if a.accountPool == nil {
		return nil, poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	authorized, available, allowed, err := a.accountPool.authorizationCurrent(r.Context(), tx, model, auth)
	if err != nil {
		return nil, poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	if !authorized {
		return nil, poolAdmissionFailure(accountPoolAuthorizationChanged)
	}
	if !available {
		return nil, poolAdmissionFailure(accountPoolConfigurationChanged)
	}
	if !allowed {
		return nil, poolAdmissionFailure(accountPoolModelNotAllowed)
	}
	var matches int
	if lease == nil {
		err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM models m WHERE m.id=? AND m.enabled=1 AND m.revision=? AND m.upstream_id=? AND m.upstream_model=? AND m.wire_protocol=? AND NOT EXISTS(SELECT 1 FROM model_account_pool_configs WHERE model_id=m.id)`, model, selected.ModelRevision, selected.AccountID, selected.UpstreamModel, string(selected.WireProtocol)).Scan(&matches)
	} else {
		err = tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM model_account_pool_configs c JOIN model_account_pool_routes p ON p.model_id=c.model_id JOIN account_pool_runtime_leases l ON l.public_model=c.model_id AND l.account_id=p.upstream_id WHERE c.model_id=? AND c.revision=? AND p.upstream_id=? AND p.upstream_model=? AND p.wire_protocol=? AND l.lease_id=? AND l.employee_id=? AND l.key_id=? AND l.expires_at>?`, model, lease.PoolRevision(), selected.AccountID, selected.UpstreamModel, string(selected.WireProtocol), lease.inner.ID(), auth.EmployeeID, auth.KeyID, formatAccountPoolTime(a.accountPool.clock.Now())).Scan(&matches)
	}
	if err != nil {
		return nil, poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	if matches != 1 {
		return nil, poolAdmissionFailure(accountPoolConfigurationChanged)
	}
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM account_recovery_states WHERE account_id=?`, selected.AccountID).Scan(&matches); err != nil {
		return nil, poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	if matches != 0 {
		return nil, poolAdmissionFailure(accountPoolAccountChanged)
	}
	current, err := a.routeEgressTx(r.Context(), tx, selected)
	if err != nil {
		return nil, egressAdmissionFailure(err)
	}
	if !sameRouteEgress(selected.egress, current) {
		return nil, poolAdmissionFailure(accountPoolAccountChanged)
	}
	// Return the originally frozen client, not a newly read configuration.
	if budgetState != nil {
		if failure := a.commitBudgetDispatch(r.Context(), tx, selected, budgetState); failure != nil {
			return nil, failure
		}
		if lease != nil {
			lease.phase = scheduling.MayHaveSent
		}
		return selected.egress.client, nil
	}
	if usageState != nil {
		if failure := a.commitUsageDispatch(r.Context(), tx, selected, usageState); failure != nil {
			return nil, failure
		}
		if lease != nil {
			lease.phase = scheduling.MayHaveSent
		}
		return selected.egress.client, nil
	}
	if err := tx.Rollback(); err != nil {
		return nil, poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	if lease != nil {
		lease.phase = scheduling.MayHaveSent
	}
	return selected.egress.client, nil
}

// A rejected final dispatch has no upstream attempt. Cancellation must retain
// that fact in both ledgers and must not write an error to a disconnected caller.
func (a *App) finishDispatchFailure(r *http.Request, id string, record bool) bool {
	cancelled := r.Context().Err() != nil
	if record {
		outcome := "failed"
		if cancelled {
			outcome = "cancelled"
		}
		a.finishRequest(id, outcome, 0)
	}
	return !cancelled
}

// Model catalog pages are separate safe GETs, but all pages belong to one
// revision and exit. A change aborts the operation instead of mixing catalogs.
func (a *App) catalogEgressCurrent(ctx context.Context, selected route, frozen *routeEgress) bool {
	current, err := a.prepareRouteEgress(ctx, selected)
	return err == nil && sameRouteEgress(frozen, current)
}

func proxyPreflightError(err error) *modelPreflightError {
	if errors.Is(err, errOutboundProxyUnavailable) {
		return accountPreflightFailure(http.StatusServiceUnavailable, "proxy_unavailable", "The configured outbound proxy is unavailable.", scheduling.FailurePermanent)
	}
	return &modelPreflightError{Failure: egressAdmissionFailure(err)}
}
