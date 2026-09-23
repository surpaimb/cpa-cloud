package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"

	"cpacloud.local/server/internal/scheduling"
)

const accountPoolMaintenanceLeaseTable = "account_pool_maintenance_leases"

const accountPoolMaintenanceLeaseDDL = `CREATE TABLE IF NOT EXISTS account_pool_maintenance_leases (
	lease_id TEXT PRIMARY KEY,
	operation_id TEXT NOT NULL UNIQUE,
	account_id TEXT NOT NULL REFERENCES upstreams(id) ON DELETE CASCADE,
	cooldown_event_id TEXT NOT NULL,
	recovery_revision INTEGER NOT NULL CHECK(recovery_revision >= 1),
	pool_revision INTEGER NOT NULL CHECK(pool_revision >= 1),
	account_revision INTEGER NOT NULL CHECK(account_revision >= 1),
	public_model TEXT NOT NULL REFERENCES models(id),
	upstream_model TEXT NOT NULL,
	provider_kind TEXT NOT NULL,
	protocol TEXT NOT NULL,
	dispatch_phase INTEGER NOT NULL CHECK(dispatch_phase IN (1,2)),
	expires_at TEXT NOT NULL,
	created_at TEXT NOT NULL,
	CHECK(length(operation_id) BETWEEN 1 AND 128),
	CHECK(length(account_id) BETWEEN 1 AND 128),
	CHECK(length(cooldown_event_id) BETWEEN 1 AND 128)
)`

type accountMaintenanceAcquireRequest struct {
	OperationID              string
	AccountID                string
	CooldownEventID          string
	ExpectedPoolRevision     int64
	ExpectedAccountRevision  int64
	ExpectedRecoveryRevision int64
	// CapacityWait bounds only queueing, not the lifetime of an admitted lease.
	CapacityWait time.Duration
}

type accountMaintenanceBegin func(context.Context, *sql.Tx, accountRecoveryState) error
type accountMaintenanceTxCallback func(context.Context, *sql.Tx) error
type accountMaintenanceFinalize func(context.Context, *sql.Tx, bool) error

type accountMaintenanceAcquireResult struct {
	Lease *accountMaintenanceLease
	Route route
	State accountRecoveryState
	Code  accountPoolRuntimeCode
}

type accountMaintenanceLease struct {
	runtime         *accountPoolRuntime
	inner           *scheduling.Lease
	route           route
	state           accountRecoveryState
	ctx             context.Context
	cancel          context.CancelFunc
	done            chan struct{}
	mu              sync.Mutex
	finished        bool
	heartbeatFailed bool
	phase           scheduling.DispatchPhase
}

func migrateAccountPoolMaintenanceTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, accountPoolMaintenanceLeaseDDL); err != nil {
		return err
	}
	if err := verifyCanonicalTable(ctx, tx, accountPoolMaintenanceLeaseTable, accountPoolMaintenanceLeaseDDL); err != nil {
		return err
	}
	_, err := loadRestoredMaintenanceLeasesTx(ctx, tx, time.Time{})
	return err
}

func loadRestoredMaintenanceLeasesTx(ctx context.Context, tx *sql.Tx, now time.Time) ([]scheduling.LeaseSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT lease_id,account_id,operation_id,cooldown_event_id,recovery_revision,pool_revision,account_revision,public_model,upstream_model,provider_kind,protocol,dispatch_phase,expires_at,created_at FROM account_pool_maintenance_leases ORDER BY lease_id`)
	if err != nil {
		return nil, err
	}
	type stored struct {
		snapshot                                                         scheduling.LeaseSnapshot
		operation, event, publicModel, upstreamModel, provider, protocol string
		recoveryRevision, poolRevision, accountRevision                  int64
		phase                                                            int
		created                                                          time.Time
	}
	items := make([]stored, 0)
	for rows.Next() {
		var item stored
		var expires, created string
		if err := rows.Scan(&item.snapshot.LeaseID, &item.snapshot.AccountID, &item.operation, &item.event, &item.recoveryRevision, &item.poolRevision, &item.accountRevision, &item.publicModel, &item.upstreamModel, &item.provider, &item.protocol, &item.phase, &expires, &created); err != nil {
			rows.Close()
			return nil, err
		}
		if item.snapshot.ExpiresAt, err = parseTime(expires); err != nil {
			rows.Close()
			return nil, err
		}
		if item.created, err = parseTime(created); err != nil {
			rows.Close()
			return nil, err
		}
		if !validIdentifier(item.snapshot.LeaseID, 128) || !validIdentifier(item.snapshot.AccountID, 128) || !validIdentifier(item.operation, 128) || !validIdentifier(item.event, 128) || item.recoveryRevision < 1 || item.poolRevision < 1 || item.accountRevision < 1 || !validIdentifier(item.publicModel, 128) || strings.TrimSpace(item.upstreamModel) == "" || len(item.upstreamModel) > 256 || !validRecoveryProviderProtocol(item.provider, item.protocol) || (item.phase != int(scheduling.DispatchNotStarted) && item.phase != int(scheduling.MayHaveSent)) || item.created.After(item.snapshot.ExpiresAt) {
			rows.Close()
			return nil, errors.New("invalid stored maintenance lease")
		}
		items = append(items, item)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return nil, iterationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	restored := make([]scheduling.LeaseSnapshot, 0, len(items))
	for _, item := range items {
		if !item.snapshot.ExpiresAt.After(now) {
			if _, err := tx.ExecContext(ctx, `DELETE FROM account_pool_maintenance_leases WHERE lease_id=?`, item.snapshot.LeaseID); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE account_pool_maintenance_leases SET expires_at=?,created_at=? WHERE lease_id=?`, formatAccountPoolTime(item.snapshot.ExpiresAt), formatAccountPoolTime(item.created), item.snapshot.LeaseID); err != nil {
			return nil, err
		}
		restored = append(restored, item.snapshot)
	}
	return restored, nil
}

func validMaintenanceRequest(req accountMaintenanceAcquireRequest) bool {
	return validIdentifier(req.OperationID, 128) && validIdentifier(req.AccountID, 128) && validIdentifier(req.CooldownEventID, 128) &&
		req.ExpectedPoolRevision >= 1 && req.ExpectedAccountRevision >= 1 && req.ExpectedRecoveryRevision >= 1
}

func (rt *accountPoolRuntime) AcquireMaintenance(ctx context.Context, req accountMaintenanceAcquireRequest, begin accountMaintenanceBegin) accountMaintenanceAcquireResult {
	if !rt.begin() {
		return accountMaintenanceAcquireResult{Code: accountPoolClosed}
	}
	defer rt.wg.Done()
	if ctx == nil || !validMaintenanceRequest(req) {
		return accountMaintenanceAcquireResult{Code: accountPoolInvalid}
	}
	if ctx.Err() != nil {
		return accountMaintenanceAcquireResult{Code: accountPoolCancelled}
	}
	opCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(rt.ctx, cancel)
	defer func() { stop(); cancel() }()
	var capacityDeadline time.Time
	if req.CapacityWait > 0 {
		capacityDeadline = time.Now().Add(req.CapacityWait)
	}

	for {
		epoch, changed := rt.changeSnapshot()
		snapshot, _, capacity, code := rt.loadMaintenanceCandidate(opCtx, req)
		if code != "" {
			return accountMaintenanceAcquireResult{Code: code}
		}
		waitCtx, cancelWait := context.WithCancel(opCtx)
		if req.CapacityWait > 0 {
			cancelWait()
			waitCtx, cancelWait = context.WithDeadline(opCtx, capacityDeadline)
		}
		stopChange := context.AfterFunc(changed, cancelWait)
		if !rt.changeIsCurrent(epoch) {
			stopChange()
			cancelWait()
			continue
		}
		candidate := scheduling.Candidate{ID: snapshot.AccountID, Provider: snapshot.ProviderKind, Models: []string{snapshot.PublicModel}, Enabled: true, Priority: 0, Weight: 1, Capacity: capacity}
		inner, decision := rt.scheduler.Acquire(waitCtx, scheduling.Request{Provider: snapshot.ProviderKind, Model: snapshot.PublicModel, AllowedAccountIDs: []string{snapshot.AccountID}, Candidates: []scheduling.Candidate{candidate}})
		waitExpired := errors.Is(waitCtx.Err(), context.DeadlineExceeded) && opCtx.Err() == nil
		stopChange()
		cancelWait()
		if !rt.changeIsCurrent(epoch) {
			if inner != nil {
				inner.Release(scheduling.ReleaseResult{})
			}
			if opCtx.Err() != nil {
				return accountMaintenanceAcquireResult{Code: accountPoolCancelled}
			}
			continue
		}
		if inner == nil {
			if waitExpired {
				return accountMaintenanceAcquireResult{Code: accountPoolCapacityUnavailable}
			}
			return accountMaintenanceAcquireResult{Code: mapSchedulingCode(decision.Code)}
		}
		rt.cooldownTransition.Lock()
		if !rt.changeIsCurrent(epoch) {
			rt.cooldownTransition.Unlock()
			inner.Release(scheduling.ReleaseResult{})
			continue
		}
		unlockMutation, lockErr := rt.acquireMaintenanceMutationLock(opCtx, snapshot.ProviderKind, snapshot.AccountID)
		if lockErr != nil {
			rt.cooldownTransition.Unlock()
			inner.Release(scheduling.ReleaseResult{})
			return accountMaintenanceAcquireResult{Code: accountPoolCancelled}
		}
		rt.app.admission.RLock()
		state, route, code := rt.persistMaintenanceLease(opCtx, req, inner, begin)
		rt.app.admission.RUnlock()
		unlockMutation()
		rt.cooldownTransition.Unlock()
		if code != "" {
			// A failed Commit response may still have stored the lease and
			// pending attempt. Reserve this capacity through its TTL until the
			// coordinator can prove settlement; never admit a parallel probe
			// by assuming that every storage error was a rollback.
			if code != accountPoolStorageUnavailable {
				inner.Release(scheduling.ReleaseResult{})
			}
			return accountMaintenanceAcquireResult{Code: code}
		}
		leaseCtx, cancelLease := context.WithCancel(ctx)
		lease := &accountMaintenanceLease{runtime: rt, inner: inner, route: route, state: state, ctx: leaseCtx, cancel: cancelLease, done: make(chan struct{}), phase: scheduling.DispatchNotStarted}
		if !rt.startMaintenanceHeartbeat(lease) {
			cancelLease()
			return accountMaintenanceAcquireResult{Code: accountPoolClosed}
		}
		return accountMaintenanceAcquireResult{Lease: lease, Route: route, State: state, Code: accountPoolAcquired}
	}
}

func (rt *accountPoolRuntime) loadMaintenanceCandidate(ctx context.Context, req accountMaintenanceAcquireRequest) (accountRecoveryState, route, int, accountPoolRuntimeCode) {
	tx, err := rt.app.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return accountRecoveryState{}, route{}, 0, accountPoolStorageUnavailable
	}
	defer tx.Rollback()
	state, selected, capacity, code := rt.revalidateMaintenanceTx(ctx, tx, req, "", scheduling.DispatchUnknown)
	if code != "" {
		return accountRecoveryState{}, route{}, 0, code
	}
	if err := tx.Commit(); err != nil {
		return accountRecoveryState{}, route{}, 0, accountPoolStorageUnavailable
	}
	return state, selected, capacity, ""
}

func (rt *accountPoolRuntime) persistMaintenanceLease(ctx context.Context, req accountMaintenanceAcquireRequest, inner *scheduling.Lease, begin accountMaintenanceBegin) (accountRecoveryState, route, accountPoolRuntimeCode) {
	tx, err := rt.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return accountRecoveryState{}, route{}, accountPoolStorageUnavailable
	}
	defer tx.Rollback()
	state, selected, _, code := rt.revalidateMaintenanceTx(ctx, tx, req, "", scheduling.DispatchUnknown)
	if code != "" {
		return accountRecoveryState{}, route{}, code
	}
	if begin != nil {
		if err := begin(ctx, tx, state); err != nil {
			return accountRecoveryState{}, route{}, accountPoolStorageUnavailable
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_pool_maintenance_leases(lease_id,operation_id,account_id,cooldown_event_id,recovery_revision,pool_revision,account_revision,public_model,upstream_model,provider_kind,protocol,dispatch_phase,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		inner.ID(), state.OperationID, state.AccountID, state.CooldownEventID, state.RecoveryRevision, state.PoolRevision, state.AccountRevision, state.PublicModel, state.UpstreamModel, state.ProviderKind, state.Protocol, int(scheduling.DispatchNotStarted), formatAccountPoolTime(inner.ExpiresAt()), formatAccountPoolTime(rt.clock.Now()))
	if err != nil {
		return accountRecoveryState{}, route{}, accountPoolStorageUnavailable
	}
	if err := tx.Commit(); err != nil {
		return accountRecoveryState{}, route{}, accountPoolStorageUnavailable
	}
	selected.Ciphertext = append([]byte(nil), selected.Ciphertext...)
	return state, selected, ""
}

func (rt *accountPoolRuntime) revalidateMaintenanceTx(ctx context.Context, tx *sql.Tx, req accountMaintenanceAcquireRequest, leaseID string, expectedPhase scheduling.DispatchPhase) (accountRecoveryState, route, int, accountPoolRuntimeCode) {
	state, err := scanAccountRecoveryState(tx.QueryRowContext(ctx, accountRecoverySelect+` WHERE account_id=?`, req.AccountID))
	if errors.Is(err, sql.ErrNoRows) {
		return state, route{}, 0, accountPoolConfigurationChanged
	}
	if err != nil {
		return state, route{}, 0, accountPoolStorageUnavailable
	}
	if state.OperationID != req.OperationID || state.CooldownEventID != req.CooldownEventID || state.RecoveryRevision != req.ExpectedRecoveryRevision || state.PoolRevision != req.ExpectedPoolRevision || state.AccountRevision != req.ExpectedAccountRevision {
		return state, route{}, 0, accountPoolConfigurationChanged
	}
	if state.State != recoveryRequired && !(leaseID != "" && state.State == recoveryInProgress) {
		return state, route{}, 0, accountPoolConfigurationChanged
	}
	var cooldownEvent string
	if err := tx.QueryRowContext(ctx, `SELECT event_id FROM account_pool_runtime_cooldowns WHERE account_id=?`, state.AccountID).Scan(&cooldownEvent); err != nil || cooldownEvent != state.CooldownEventID {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return state, route{}, 0, accountPoolStorageUnavailable
		}
		return state, route{}, 0, accountPoolConfigurationChanged
	}
	var selected route
	var enabled int
	var capacity int
	var poolRevision int64
	err = tx.QueryRowContext(ctx, `SELECT u.id,u.endpoint,r.upstream_model,u.credential_ciphertext,u.provider_kind,u.revision,u.credential_state,u.key_version,u.enabled,c.revision,
		(SELECT MIN(gr.max_concurrency) FROM model_account_pool_routes gr JOIN models gm ON gm.id=gr.model_id WHERE gr.upstream_id=r.upstream_id AND gm.enabled=1)
		FROM model_account_pool_routes r JOIN model_account_pool_configs c ON c.model_id=r.model_id JOIN models m ON m.id=r.model_id JOIN upstreams u ON u.id=r.upstream_id
		WHERE r.model_id=? AND r.upstream_id=? AND m.enabled=1`, state.PublicModel, state.AccountID).Scan(&selected.AccountID, &selected.Endpoint, &selected.UpstreamModel, &selected.Ciphertext, &selected.ProviderKind, &selected.Revision, &selected.CredentialState, &selected.KeyVersion, &enabled, &poolRevision, &capacity)
	if errors.Is(err, sql.ErrNoRows) {
		return state, route{}, 0, accountPoolConfigurationChanged
	}
	if err != nil {
		return state, route{}, 0, accountPoolStorageUnavailable
	}
	if enabled == 0 || capacity < 1 || selected.Revision != state.AccountRevision || poolRevision != state.PoolRevision || selected.ProviderKind != state.ProviderKind || selected.UpstreamModel != state.UpstreamModel || selected.ProviderKind == codexMembershipProvider && (!rt.app.cfg.ExperimentalCodexMembership || selected.CredentialState.String == codexStateReauth) {
		return state, route{}, 0, accountPoolConfigurationChanged
	}
	if code := validateRecoverySourceTx(ctx, tx, state); code != "" {
		return state, route{}, 0, code
	}
	if leaseID != "" {
		var expires string
		var phase int
		if err := tx.QueryRowContext(ctx, `SELECT expires_at,dispatch_phase FROM account_pool_maintenance_leases WHERE lease_id=? AND operation_id=?`, leaseID, state.OperationID).Scan(&expires, &phase); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return state, route{}, 0, accountPoolConfigurationChanged
			}
			return state, route{}, 0, accountPoolStorageUnavailable
		}
		expiry, err := parseTime(expires)
		if err != nil {
			return state, route{}, 0, accountPoolStorageUnavailable
		}
		if !expiry.After(rt.clock.Now()) || phase != int(expectedPhase) {
			return state, route{}, 0, accountPoolConfigurationChanged
		}
	}
	return state, selected, capacity, ""
}

func (rt *accountPoolRuntime) acquireMaintenanceMutationLock(ctx context.Context, provider, accountID string) (func(), error) {
	if provider != codexMembershipProvider {
		return func() {}, nil
	}
	return rt.app.acquireCodexMutationLock(ctx, accountID)
}

func validateRecoverySourceTx(ctx context.Context, tx *sql.Tx, state accountRecoveryState) accountPoolRuntimeCode {
	if state.ProviderKind != codexMembershipProvider {
		if state.SourceSnapshot != "api_key" || state.ClientID != "" {
			return accountPoolConfigurationChanged
		}
		return ""
	}
	if state.SourceSnapshot == "import" && state.ClientID == "" {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_oauth_bindings WHERE upstream_id=?`, state.AccountID).Scan(&count); err != nil {
			return accountPoolStorageUnavailable
		}
		if count != 0 {
			return accountPoolConfigurationChanged
		}
		return ""
	}
	if state.SourceSnapshot != "authorization_code" || state.ClientID == "" {
		return accountPoolConfigurationChanged
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM codex_oauth_bindings WHERE upstream_id=? AND client_id=? AND source='authorization_code'`, state.AccountID, state.ClientID).Scan(&count); err != nil {
		return accountPoolStorageUnavailable
	}
	if count != 1 {
		return accountPoolConfigurationChanged
	}
	return ""
}

func (l *accountMaintenanceLease) Context() context.Context {
	if l == nil {
		return context.Background()
	}
	return l.ctx
}
func (l *accountMaintenanceLease) Route() route {
	if l == nil {
		return route{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.route
	out.Ciphertext = append([]byte(nil), out.Ciphertext...)
	return out
}
func (l *accountMaintenanceLease) Snapshot() accountRecoveryState {
	if l == nil {
		return accountRecoveryState{}
	}
	return l.state
}

func (l *accountMaintenanceLease) MarkDispatch(ctx context.Context, callback accountMaintenanceTxCallback) accountPoolRuntimeCode {
	if l == nil || callback == nil {
		return accountPoolInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.finished || l.heartbeatFailed || l.phase != scheduling.DispatchNotStarted || l.ctx.Err() != nil {
		return accountPoolConfigurationChanged
	}
	l.runtime.cooldownTransition.Lock()
	defer l.runtime.cooldownTransition.Unlock()
	unlockMutation, err := l.runtime.acquireMaintenanceMutationLock(ctx, l.state.ProviderKind, l.state.AccountID)
	if err != nil {
		return accountPoolCancelled
	}
	defer unlockMutation()
	l.runtime.app.admission.RLock()
	defer l.runtime.app.admission.RUnlock()
	tx, err := l.runtime.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return accountPoolStorageUnavailable
	}
	defer tx.Rollback()
	req := accountMaintenanceAcquireRequest{OperationID: l.state.OperationID, AccountID: l.state.AccountID, CooldownEventID: l.state.CooldownEventID, ExpectedPoolRevision: l.state.PoolRevision, ExpectedAccountRevision: l.state.AccountRevision, ExpectedRecoveryRevision: l.state.RecoveryRevision}
	if _, _, _, code := l.runtime.revalidateMaintenanceTx(ctx, tx, req, l.inner.ID(), scheduling.DispatchNotStarted); code != "" {
		return code
	}
	if err := callback(ctx, tx); err != nil {
		return accountPoolStorageUnavailable
	}
	result, err := tx.ExecContext(ctx, `UPDATE account_pool_maintenance_leases SET dispatch_phase=? WHERE lease_id=? AND dispatch_phase=?`, int(scheduling.MayHaveSent), l.inner.ID(), int(scheduling.DispatchNotStarted))
	if err != nil {
		return accountPoolStorageUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return accountPoolConfigurationChanged
	}
	if err := tx.Commit(); err != nil {
		return accountPoolStorageUnavailable
	}
	l.phase = scheduling.MayHaveSent
	return accountPoolAcquired
}

func (l *accountMaintenanceLease) Heartbeat(ctx context.Context) accountPoolRuntimeCode {
	if l == nil {
		return accountPoolInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.finished {
		return accountPoolAlreadyReleased
	}
	if l.heartbeatFailed {
		return accountPoolStorageUnavailable
	}
	expires, ok := l.inner.Renew()
	if !ok {
		l.heartbeatFailed = true
		l.cancel()
		return accountPoolCancelled
	}
	result, err := l.runtime.app.store.db.ExecContext(ctx, `UPDATE account_pool_maintenance_leases SET expires_at=? WHERE lease_id=?`, formatAccountPoolTime(expires), l.inner.ID())
	if err != nil {
		l.heartbeatFailed = true
		l.cancel()
		return accountPoolStorageUnavailable
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		l.heartbeatFailed = true
		l.cancel()
		return accountPoolStorageUnavailable
	}
	return accountPoolAcquired
}

func (l *accountMaintenanceLease) Finalize(ctx context.Context, callback accountMaintenanceFinalize) accountPoolRuntimeCode {
	if l == nil || callback == nil {
		return accountPoolInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.finished {
		return accountPoolAlreadyReleased
	}
	l.runtime.cooldownTransition.Lock()
	defer l.runtime.cooldownTransition.Unlock()
	unlockMutation, err := l.runtime.acquireMaintenanceMutationLock(ctx, l.state.ProviderKind, l.state.AccountID)
	if err != nil {
		return accountPoolCancelled
	}
	defer unlockMutation()
	l.runtime.app.admission.RLock()
	defer l.runtime.app.admission.RUnlock()
	tx, err := l.runtime.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return accountPoolStorageUnavailable
	}
	defer tx.Rollback()
	var storedOperation, storedEvent string
	err = tx.QueryRowContext(ctx, `SELECT operation_id,cooldown_event_id FROM account_pool_maintenance_leases WHERE lease_id=?`, l.inner.ID()).Scan(&storedOperation, &storedEvent)
	if err != nil {
		return accountPoolStorageUnavailable
	}
	currentMatch := false
	req := accountMaintenanceAcquireRequest{OperationID: l.state.OperationID, AccountID: l.state.AccountID, CooldownEventID: l.state.CooldownEventID, ExpectedPoolRevision: l.state.PoolRevision, ExpectedAccountRevision: l.state.AccountRevision, ExpectedRecoveryRevision: l.state.RecoveryRevision}
	_, _, _, matchCode := l.runtime.revalidateMaintenanceTx(ctx, tx, req, l.inner.ID(), l.phase)
	if matchCode == accountPoolStorageUnavailable {
		return accountPoolStorageUnavailable
	}
	if matchCode == "" && !l.heartbeatFailed {
		currentMatch = true
	}
	if err := callback(ctx, tx, currentMatch); err != nil {
		return accountPoolStorageUnavailable
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM account_pool_maintenance_leases WHERE lease_id=? AND operation_id=? AND cooldown_event_id=?`, l.inner.ID(), storedOperation, storedEvent)
	if err != nil {
		return accountPoolStorageUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return accountPoolStorageUnavailable
	}
	var remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_pool_runtime_cooldowns WHERE account_id=? AND event_id=?`, l.state.AccountID, l.state.CooldownEventID).Scan(&remaining); err != nil {
		return accountPoolStorageUnavailable
	}
	if err := tx.Commit(); err != nil {
		return accountPoolStorageUnavailable
	}
	return l.completeReleaseLocked(remaining)
}

// ResolveSettlement never writes metadata or performs network I/O. It only
// releases memory after the caller proves that its immutable receipt already
// committed atomically, including the terminal ledger and isolation outcome.
func (l *accountMaintenanceLease) ResolveSettlement(ctx context.Context, proof accountMaintenanceTxCallback) accountPoolRuntimeCode {
	if l == nil || proof == nil {
		return accountPoolInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.finished {
		return accountPoolAlreadyReleased
	}
	l.runtime.cooldownTransition.Lock()
	defer l.runtime.cooldownTransition.Unlock()
	tx, err := l.runtime.app.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return accountPoolStorageUnavailable
	}
	defer tx.Rollback()
	var count, remaining int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_pool_maintenance_leases WHERE lease_id=? OR operation_id=?`, l.inner.ID(), l.state.OperationID).Scan(&count); err != nil || count != 0 {
		return accountPoolStorageUnavailable
	}
	if err := proof(ctx, tx); err != nil {
		return accountPoolStorageUnavailable
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_pool_runtime_cooldowns WHERE account_id=? AND event_id=?`, l.state.AccountID, l.state.CooldownEventID).Scan(&remaining); err != nil {
		return accountPoolStorageUnavailable
	}
	if err := tx.Commit(); err != nil {
		return accountPoolStorageUnavailable
	}
	return l.completeReleaseLocked(remaining)
}

// Both callers hold lease.mu and cooldownTransition until memory publication.
func (l *accountMaintenanceLease) completeReleaseLocked(remaining int) accountPoolRuntimeCode {
	l.finished = true
	close(l.done)
	l.cancel()
	l.runtime.NotifyChanged()
	if remaining == 0 {
		l.runtime.scheduler.ClearCooldown(l.state.AccountID, l.state.CooldownEventID)
	}
	l.inner.Release(scheduling.ReleaseResult{Phase: scheduling.DispatchUnknown})
	return accountPoolReleased
}

func (rt *accountPoolRuntime) startMaintenanceHeartbeat(lease *accountMaintenanceLease) bool {
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return false
	}
	rt.maintenanceActive[lease] = struct{}{}
	rt.wg.Add(1)
	rt.mu.Unlock()
	go func() {
		defer rt.wg.Done()
		defer func() { rt.mu.Lock(); delete(rt.maintenanceActive, lease); rt.mu.Unlock() }()
		interval := rt.leaseTTL / 3
		if interval <= 0 {
			interval = time.Nanosecond
		}
		for {
			select {
			case <-rt.ctx.Done():
				return
			case <-lease.ctx.Done():
				return
			case <-lease.done:
				return
			case <-rt.clock.After(interval):
				if lease.Heartbeat(rt.ctx) != accountPoolAcquired {
					return
				}
			}
		}
	}()
	return true
}

// cancelMaintenanceLeases terminates execution contexts for the exact
// isolation event an administrator cleared. Recovery snapshots are immutable,
// so filtering needs only rt.mu and deliberately never takes lease.mu. In
// particular, callers may hold cooldownTransition without reversing the
// Finalize lock order (lease.mu -> cooldownTransition).
func (rt *accountPoolRuntime) cancelMaintenanceLeases(accountID, eventID string) {
	if rt == nil || accountID == "" || eventID == "" {
		return
	}
	rt.mu.Lock()
	for lease := range rt.maintenanceActive {
		if lease.state.AccountID == accountID && lease.state.CooldownEventID == eventID {
			lease.cancel()
		}
	}
	rt.mu.Unlock()
}
