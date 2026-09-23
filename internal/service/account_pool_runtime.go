package service

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"sync"
	"time"

	"cpacloud.local/server/internal/scheduling"
)

const (
	accountPoolLeaseTable    = "account_pool_runtime_leases"
	accountPoolCooldownTable = "account_pool_runtime_cooldowns"
	accountPoolTimeLayout    = "2006-01-02T15:04:05.000000000Z"
)

type accountPoolRuntimeCode string

const (
	accountPoolAcquired             accountPoolRuntimeCode = "acquired"
	accountPoolLegacy               accountPoolRuntimeCode = "legacy_no_pool"
	accountPoolInvalid              accountPoolRuntimeCode = "invalid_request"
	accountPoolModelNotAllowed      accountPoolRuntimeCode = "model_not_allowed"
	accountPoolNoCompatible         accountPoolRuntimeCode = "no_compatible_account"
	accountPoolCapacityUnavailable  accountPoolRuntimeCode = "capacity_unavailable"
	accountPoolQueueFull            accountPoolRuntimeCode = "queue_full"
	accountPoolCancelled            accountPoolRuntimeCode = "cancelled"
	accountPoolAuthorizationChanged accountPoolRuntimeCode = "authorization_changed"
	accountPoolConfigurationChanged accountPoolRuntimeCode = "configuration_changed"
	accountPoolAccountChanged       accountPoolRuntimeCode = "account_changed"
	accountPoolStorageUnavailable   accountPoolRuntimeCode = "storage_unavailable"
	accountPoolClosed               accountPoolRuntimeCode = "closed"
	accountPoolReleased             accountPoolRuntimeCode = "released"
	accountPoolAlreadyReleased      accountPoolRuntimeCode = "already_released"
)

type accountPoolAcquireResult struct {
	Lease        *accountPoolLease
	Route        route
	PoolRevision int64
	Legacy       bool
	Code         accountPoolRuntimeCode
}

type accountPoolReleaseResult struct {
	Code           accountPoolRuntimeCode
	RetrySuggested bool
}

type accountPoolRuntime struct {
	app           *App
	scheduler     *scheduling.Scheduler
	clock         scheduling.Clock
	leaseTTL      time.Duration
	cooldowns     map[scheduling.FailureClass]time.Duration
	ctx           context.Context
	cancel        context.CancelFunc
	mu            sync.Mutex
	closed        bool
	wg            sync.WaitGroup
	active        map[*accountPoolLease]struct{}
	changeEpoch   uint64
	changeContext context.Context
	changeCancel  context.CancelFunc
}

type accountPoolLease struct {
	runtime         *accountPoolRuntime
	inner           *scheduling.Lease
	poolRevision    int64
	ctx             context.Context
	cancel          context.CancelFunc
	stopRuntime     func() bool
	done            chan struct{}
	mu              sync.Mutex
	finished        bool
	heartbeatFailed bool
	phase           scheduling.DispatchPhase
}

type accountPoolRuntimeConfig struct {
	Clock      scheduling.Clock
	Random     scheduling.Random
	LeaseTTL   time.Duration
	StickyTTL  time.Duration
	MaxSticky  int
	MaxWaiters int
	Cooldowns  map[scheduling.FailureClass]time.Duration
}

type accountPoolAcquireOptions struct {
	ExcludedAccountIDs   []string
	ExpectedPoolRevision int64
}

type poolCandidate struct {
	configured modelAccountView
	provider   string
	revision   int64
	capacity   int
	cooldown   time.Time
}

type poolSnapshot struct {
	revision   int64
	provider   string
	candidates []scheduling.Candidate
	byID       map[string]poolCandidate
}

func (s *store) migrateAccountPoolRuntime(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS account_pool_runtime_leases (
			lease_id TEXT PRIMARY KEY,
			account_id TEXT NOT NULL REFERENCES upstreams(id),
			public_model TEXT NOT NULL REFERENCES models(id),
			employee_id TEXT NOT NULL REFERENCES employees(id),
			key_id TEXT NOT NULL REFERENCES access_keys(id),
			pool_revision INTEGER NOT NULL,
			account_revision INTEGER NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS account_pool_runtime_cooldowns (
			account_id TEXT PRIMARY KEY REFERENCES upstreams(id) ON DELETE CASCADE,
			failure_class TEXT NOT NULL,
			cooldown_until TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS account_pool_runtime_lease_expiry_idx ON account_pool_runtime_leases(expires_at)`,
		`CREATE INDEX IF NOT EXISTS account_pool_runtime_cooldown_expiry_idx ON account_pool_runtime_cooldowns(cooldown_until)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	if err := verifyAccountPoolTable(ctx, tx, accountPoolLeaseTable,
		[]string{"lease_id", "account_id", "public_model", "employee_id", "key_id", "pool_revision", "account_revision", "expires_at", "created_at"},
		[]string{"references upstreams(id)", "references models(id)", "references employees(id)", "references access_keys(id)"}); err != nil {
		return err
	}
	if err := verifyAccountPoolTable(ctx, tx, accountPoolCooldownTable,
		[]string{"account_id", "failure_class", "cooldown_until", "updated_at"},
		[]string{"references upstreams(id) on delete cascade"}); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	violated := rows.Next()
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if violated {
		return errors.New("account pool runtime migration foreign key check failed")
	}
	if closeErr != nil {
		return closeErr
	}
	return tx.Commit()
}

func newAccountPoolRuntime(app *App) (*accountPoolRuntime, error) {
	return newAccountPoolRuntimeWithConfig(app, accountPoolRuntimeConfig{})
}

func newAccountPoolRuntimeWithConfig(app *App, config accountPoolRuntimeConfig) (*accountPoolRuntime, error) {
	if app == nil || app.store == nil {
		return nil, errors.New("account pool runtime requires an application store")
	}
	if config.Clock == nil {
		config.Clock = runtimeClock{}
	}
	if config.LeaseTTL <= 0 {
		config.LeaseTTL = 2 * time.Minute
	}
	if config.MaxWaiters <= 0 {
		config.MaxWaiters = 256
	}
	if config.Cooldowns == nil {
		config.Cooldowns = map[scheduling.FailureClass]time.Duration{
			scheduling.FailureRateLimit:  time.Minute,
			scheduling.FailureOverloaded: 30 * time.Second,
			scheduling.FailureTransient:  10 * time.Second,
			scheduling.FailureAuth:       5 * time.Minute,
		}
	}
	now := config.Clock.Now()
	tx, err := app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT lease_id,account_id,expires_at,created_at FROM account_pool_runtime_leases ORDER BY lease_id`)
	if err != nil {
		return nil, err
	}
	restored := make([]scheduling.LeaseSnapshot, 0)
	leaseCreated := make(map[string]time.Time)
	for rows.Next() {
		var snapshot scheduling.LeaseSnapshot
		var expires, created string
		if err := rows.Scan(&snapshot.LeaseID, &snapshot.AccountID, &expires, &created); err != nil {
			rows.Close()
			return nil, err
		}
		snapshot.ExpiresAt, err = parseTime(expires)
		if err != nil {
			rows.Close()
			return nil, err
		}
		leaseCreated[snapshot.LeaseID], err = parseTime(created)
		if err != nil {
			rows.Close()
			return nil, err
		}
		restored = append(restored, snapshot)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return nil, iterationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	for _, snapshot := range restored {
		if !snapshot.ExpiresAt.After(now) {
			if _, err := tx.Exec(`DELETE FROM account_pool_runtime_leases WHERE lease_id=?`, snapshot.LeaseID); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := tx.Exec(`UPDATE account_pool_runtime_leases SET expires_at=?,created_at=? WHERE lease_id=?`, formatAccountPoolTime(snapshot.ExpiresAt), formatAccountPoolTime(leaseCreated[snapshot.LeaseID]), snapshot.LeaseID); err != nil {
			return nil, err
		}
	}
	activeRestored := restored[:0]
	for _, snapshot := range restored {
		if snapshot.ExpiresAt.After(now) {
			activeRestored = append(activeRestored, snapshot)
		}
	}
	restored = activeRestored
	type storedCooldown struct {
		accountID string
		until     time.Time
		updated   time.Time
	}
	cooldownRows, err := tx.Query(`SELECT account_id,cooldown_until,updated_at FROM account_pool_runtime_cooldowns ORDER BY account_id`)
	if err != nil {
		return nil, err
	}
	storedCooldowns := make([]storedCooldown, 0)
	for cooldownRows.Next() {
		var item storedCooldown
		var until, updated string
		if err := cooldownRows.Scan(&item.accountID, &until, &updated); err != nil {
			cooldownRows.Close()
			return nil, err
		}
		item.updated, err = parseTime(updated)
		if err != nil {
			cooldownRows.Close()
			return nil, err
		}
		item.until, err = parseTime(until)
		if err != nil {
			cooldownRows.Close()
			return nil, err
		}
		storedCooldowns = append(storedCooldowns, item)
	}
	iterationErr, closeErr = cooldownRows.Err(), cooldownRows.Close()
	if iterationErr != nil {
		return nil, iterationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	for _, item := range storedCooldowns {
		if !item.until.After(now) {
			if _, err := tx.Exec(`DELETE FROM account_pool_runtime_cooldowns WHERE account_id=?`, item.accountID); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := tx.Exec(`UPDATE account_pool_runtime_cooldowns SET cooldown_until=?,updated_at=? WHERE account_id=?`, formatAccountPoolTime(item.until), formatAccountPoolTime(item.updated), item.accountID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	runtimeContext, cancel := context.WithCancel(context.Background())
	changeContext, changeCancel := context.WithCancel(context.Background())
	rt := &accountPoolRuntime{
		app:           app,
		clock:         config.Clock,
		leaseTTL:      config.LeaseTTL,
		cooldowns:     config.Cooldowns,
		ctx:           runtimeContext,
		cancel:        cancel,
		active:        make(map[*accountPoolLease]struct{}),
		changeContext: changeContext,
		changeCancel:  changeCancel,
	}
	rt.scheduler = scheduling.New(scheduling.Config{Clock: config.Clock, Random: config.Random, LeaseTTL: config.LeaseTTL, StickyTTL: config.StickyTTL, MaxSticky: config.MaxSticky, MaxWaiters: config.MaxWaiters, Cooldowns: config.Cooldowns, RestoredLeases: restored})
	return rt, nil
}

type runtimeClock struct{}

func (runtimeClock) Now() time.Time                         { return time.Now() }
func (runtimeClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

func (rt *accountPoolRuntime) Acquire(ctx context.Context, publicModel string, auth employeeAuth, allowedProviders []string, stickyOpaque string) accountPoolAcquireResult {
	return rt.AcquireWithOptions(ctx, publicModel, auth, allowedProviders, stickyOpaque, accountPoolAcquireOptions{})
}

func (rt *accountPoolRuntime) AcquireWithOptions(ctx context.Context, publicModel string, auth employeeAuth, allowedProviders []string, stickyOpaque string, options accountPoolAcquireOptions) accountPoolAcquireResult {
	if !rt.begin() {
		return accountPoolAcquireResult{Code: accountPoolClosed}
	}
	defer rt.wg.Done()
	if ctx == nil || !validIdentifier(publicModel, 128) || auth.EmployeeID == "" || auth.KeyID == "" || len(allowedProviders) == 0 || len(stickyOpaque) > 256 || !validAccountPoolAcquireOptions(options) {
		return accountPoolAcquireResult{Code: accountPoolInvalid}
	}
	options.ExcludedAccountIDs = append([]string(nil), options.ExcludedAccountIDs...)
	if ctx.Err() != nil {
		return accountPoolAcquireResult{Code: accountPoolCancelled}
	}
	opContext, cancelOperation := context.WithCancel(ctx)
	stopOperation := context.AfterFunc(rt.ctx, cancelOperation)
	defer func() {
		stopOperation()
		cancelOperation()
	}()
	sticky := ""
	if stickyOpaque != "" {
		sticky = auth.EmployeeID + "\x00" + publicModel + "\x00" + stickyOpaque
	}
	var waitingPool *poolSnapshot
	for {
		epoch, changed := rt.changeSnapshot()
		rt.app.admission.RLock()
		authorized, modelAvailable, modelAllowed, err := rt.authorizationCurrent(opContext, nil, publicModel, auth)
		var pool poolSnapshot
		var legacy bool
		var code accountPoolRuntimeCode
		if err == nil && authorized && modelAvailable && modelAllowed {
			pool, legacy, code = rt.loadPool(opContext, publicModel, allowedProviders)
		}
		rt.app.admission.RUnlock()

		// A committed mutation may race any of the reads above. Reload from a
		// single newer generation rather than interpreting a mixed snapshot.
		if !rt.changeIsCurrent(epoch) {
			if opContext.Err() != nil {
				return accountPoolAcquireResult{Code: accountPoolCancelled}
			}
			continue
		}
		if err != nil {
			return accountPoolAcquireResult{Code: accountPoolStorageUnavailable}
		}
		if !authorized {
			return accountPoolAcquireResult{Code: accountPoolAuthorizationChanged}
		}
		if !modelAvailable {
			return accountPoolAcquireResult{Code: accountPoolNoCompatible}
		}
		if !modelAllowed {
			return accountPoolAcquireResult{Code: accountPoolModelNotAllowed}
		}
		if code == accountPoolStorageUnavailable {
			return accountPoolAcquireResult{Code: code}
		}
		if options.ExpectedPoolRevision > 0 && (legacy || pool.revision != options.ExpectedPoolRevision) {
			return accountPoolAcquireResult{Code: accountPoolConfigurationChanged}
		}
		if code != "" {
			return accountPoolAcquireResult{Code: code}
		}
		if legacy {
			if waitingPool != nil {
				return accountPoolAcquireResult{Code: accountPoolConfigurationChanged}
			}
			return accountPoolAcquireResult{Legacy: true, Code: accountPoolLegacy}
		}
		if waitingPool != nil && !reflect.DeepEqual(*waitingPool, pool) {
			return accountPoolAcquireResult{Code: accountPoolConfigurationChanged}
		}
		poolForWait := pool
		waitingPool = &poolForWait

		waitContext, cancelWait := context.WithCancel(opContext)
		stopChange := context.AfterFunc(changed, cancelWait)
		// Register cancellation before this second check. A notification either
		// changes the epoch here or cancels waitContext inside scheduler.Acquire.
		if !rt.changeIsCurrent(epoch) {
			stopChange()
			cancelWait()
			continue
		}
		inner, decision := rt.scheduler.Acquire(waitContext, scheduling.Request{Provider: pool.provider, Model: publicModel, AllowedAccountIDs: candidateIDs(pool.candidates), ExcludedAccountIDs: append([]string(nil), options.ExcludedAccountIDs...), StickyKey: sticky, Candidates: pool.candidates})
		stopChange()
		cancelWait()
		if !rt.changeIsCurrent(epoch) {
			if inner != nil {
				inner.Release(scheduling.ReleaseResult{})
			}
			if opContext.Err() != nil {
				return accountPoolAcquireResult{Code: accountPoolCancelled}
			}
			continue
		}
		if inner == nil {
			return accountPoolAcquireResult{Code: mapSchedulingCode(decision.Code)}
		}
		rt.app.admission.RLock()
		selected, code := rt.persistRevalidatedLease(opContext, publicModel, auth, allowedProviders, pool, options, inner)
		rt.app.admission.RUnlock()
		if code != "" {
			inner.Release(scheduling.ReleaseResult{})
			return accountPoolAcquireResult{Code: code}
		}
		leaseContext, cancelLease := context.WithCancel(ctx)
		lease := &accountPoolLease{runtime: rt, inner: inner, poolRevision: pool.revision, ctx: leaseContext, cancel: cancelLease, done: make(chan struct{}), phase: scheduling.DispatchNotStarted}
		lease.stopRuntime = context.AfterFunc(rt.ctx, cancelLease)
		if !rt.startHeartbeat(lease) {
			lease.Release(context.Background(), scheduling.ReleaseResult{})
			return accountPoolAcquireResult{Code: accountPoolClosed}
		}
		return accountPoolAcquireResult{Lease: lease, Route: selected, PoolRevision: pool.revision, Code: accountPoolAcquired}
	}
}

func (rt *accountPoolRuntime) authorizationCurrent(ctx context.Context, tx *sql.Tx, model string, auth employeeAuth) (bool, bool, bool, error) {
	query := func(statement string, args ...any) *sql.Row {
		if tx != nil {
			return tx.QueryRowContext(ctx, statement, args...)
		}
		return rt.app.store.db.QueryRowContext(ctx, statement, args...)
	}
	var status, mode string
	var expires, revoked sql.NullString
	err := query(`SELECT e.status,e.model_mode,k.expires_at,k.revoked_at FROM access_keys k JOIN employees e ON e.id=k.employee_id WHERE k.id=? AND k.employee_id=?`, auth.KeyID, auth.EmployeeID).Scan(&status, &mode, &expires, &revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, false, nil
	}
	if err != nil {
		return false, false, false, err
	}
	authorized := status == "active" && !revoked.Valid
	if authorized && expires.Valid {
		expiry, parseErr := parseTime(expires.String)
		authorized = parseErr == nil && rt.clock.Now().Before(expiry)
	}
	if !authorized {
		return false, false, false, nil
	}
	var modelEnabled int
	err = query(`SELECT enabled FROM models WHERE id=?`, model).Scan(&modelEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		return true, false, false, nil
	}
	if err != nil {
		return false, false, false, err
	}
	if modelEnabled == 0 {
		return true, false, false, nil
	}
	if mode != "selected" {
		return true, true, true, nil
	}
	var allowed int
	err = query(`SELECT 1 FROM employee_models WHERE employee_id=? AND model_id=?`, auth.EmployeeID, model).Scan(&allowed)
	if errors.Is(err, sql.ErrNoRows) {
		return true, true, false, nil
	}
	return true, true, err == nil, err
}

func (rt *accountPoolRuntime) loadPool(ctx context.Context, model string, allowedProviders []string) (poolSnapshot, bool, accountPoolRuntimeCode) {
	var revision int64
	err := rt.app.store.db.QueryRowContext(ctx, `SELECT revision FROM model_account_pool_configs WHERE model_id=?`, model).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return poolSnapshot{}, true, ""
	}
	if err != nil {
		return poolSnapshot{}, false, accountPoolStorageUnavailable
	}
	allowed := make(map[string]bool, len(allowedProviders))
	for _, provider := range allowedProviders {
		allowed[provider] = true
	}
	rows, err := rt.app.store.db.QueryContext(ctx, `SELECT r.upstream_id,r.upstream_model,r.priority,r.weight,r.max_concurrency,u.provider_kind,u.enabled,u.revision,u.credential_state,c.cooldown_until,
		(SELECT MIN(global_route.max_concurrency) FROM model_account_pool_routes global_route JOIN models global_model ON global_model.id=global_route.model_id WHERE global_route.upstream_id=r.upstream_id AND global_model.enabled=1)
		FROM model_account_pool_routes r JOIN upstreams u ON u.id=r.upstream_id
		LEFT JOIN account_pool_runtime_cooldowns c ON c.account_id=u.id
		WHERE r.model_id=? ORDER BY r.position LIMIT ?`, model, maxModelAccounts+1)
	if err != nil {
		return poolSnapshot{}, false, accountPoolStorageUnavailable
	}
	defer rows.Close()
	pool := poolSnapshot{revision: revision, byID: make(map[string]poolCandidate)}
	for rows.Next() {
		var item poolCandidate
		var enabled int
		var state, cooldown sql.NullString
		if err := rows.Scan(&item.configured.UpstreamID, &item.configured.UpstreamModel, &item.configured.Priority, &item.configured.Weight, &item.configured.MaxConcurrency, &item.provider, &enabled, &item.revision, &state, &cooldown, &item.capacity); err != nil {
			return poolSnapshot{}, false, accountPoolStorageUnavailable
		}
		if pool.provider == "" {
			pool.provider = item.provider
		} else if pool.provider != item.provider {
			return poolSnapshot{}, false, accountPoolConfigurationChanged
		}
		if !allowed[item.provider] || enabled == 0 || item.provider == codexMembershipProvider && (!rt.app.cfg.ExperimentalCodexMembership || state.String == codexStateReauth) {
			continue
		}
		if cooldown.Valid {
			item.cooldown, err = parseTime(cooldown.String)
			if err != nil {
				return poolSnapshot{}, false, accountPoolStorageUnavailable
			}
		}
		pool.byID[item.configured.UpstreamID] = item
		pool.candidates = append(pool.candidates, scheduling.Candidate{ID: item.configured.UpstreamID, Provider: item.provider, Models: []string{model}, Enabled: true, Priority: item.configured.Priority, Weight: item.configured.Weight, Capacity: item.capacity, CooldownUntil: item.cooldown})
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return poolSnapshot{}, false, accountPoolStorageUnavailable
	}
	if len(pool.candidates) == 0 {
		return pool, false, accountPoolNoCompatible
	}
	return pool, false, ""
}

func (rt *accountPoolRuntime) persistRevalidatedLease(ctx context.Context, model string, auth employeeAuth, allowedProviders []string, pool poolSnapshot, options accountPoolAcquireOptions, inner *scheduling.Lease) (route, accountPoolRuntimeCode) {
	tx, err := rt.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return route{}, accountPoolStorageUnavailable
	}
	defer tx.Rollback()
	authorized, modelAvailable, modelAllowed, err := rt.authorizationCurrent(ctx, tx, model, auth)
	if err != nil {
		return route{}, accountPoolStorageUnavailable
	}
	if !authorized {
		return route{}, accountPoolAuthorizationChanged
	}
	if !modelAvailable {
		return route{}, accountPoolNoCompatible
	}
	if !modelAllowed {
		return route{}, accountPoolModelNotAllowed
	}
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM model_account_pool_configs WHERE model_id=?`, model).Scan(&revision); err != nil || revision != pool.revision {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return route{}, accountPoolStorageUnavailable
		}
		return route{}, accountPoolConfigurationChanged
	}
	expected, ok := pool.byID[inner.AccountID()]
	if !ok {
		return route{}, accountPoolConfigurationChanged
	}
	if accountPoolAccountExcluded(inner.AccountID(), options.ExcludedAccountIDs) {
		return route{}, accountPoolConfigurationChanged
	}
	var selected route
	var enabled int
	var effectiveCapacity int
	err = tx.QueryRowContext(ctx, `SELECT u.id,u.endpoint,r.upstream_model,u.credential_ciphertext,u.provider_kind,u.revision,u.credential_state,u.key_version,u.enabled,
		(SELECT MIN(global_route.max_concurrency) FROM model_account_pool_routes global_route JOIN models global_model ON global_model.id=global_route.model_id WHERE global_route.upstream_id=r.upstream_id AND global_model.enabled=1)
		FROM model_account_pool_routes r JOIN upstreams u ON u.id=r.upstream_id
		WHERE r.model_id=? AND r.upstream_id=?`, model, inner.AccountID()).Scan(&selected.AccountID, &selected.Endpoint, &selected.UpstreamModel, &selected.Ciphertext, &selected.ProviderKind, &selected.Revision, &selected.CredentialState, &selected.KeyVersion, &enabled, &effectiveCapacity)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return route{}, accountPoolAccountChanged
		}
		return route{}, accountPoolStorageUnavailable
	}
	if effectiveCapacity != expected.capacity {
		return route{}, accountPoolConfigurationChanged
	}
	if enabled == 0 || selected.Revision != expected.revision || selected.ProviderKind != expected.provider || selected.UpstreamModel != expected.configured.UpstreamModel || !providerAllowed(selected.ProviderKind, allowedProviders) || selected.ProviderKind == codexMembershipProvider && (!rt.app.cfg.ExperimentalCodexMembership || selected.CredentialState.String == codexStateReauth) {
		return route{}, accountPoolAccountChanged
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_pool_runtime_leases(lease_id,account_id,public_model,employee_id,key_id,pool_revision,account_revision,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, inner.ID(), selected.AccountID, model, auth.EmployeeID, auth.KeyID, pool.revision, selected.Revision, formatAccountPoolTime(inner.ExpiresAt()), formatAccountPoolTime(rt.clock.Now()))
	if err != nil {
		return route{}, accountPoolStorageUnavailable
	}
	if err := tx.Commit(); err != nil {
		return route{}, accountPoolStorageUnavailable
	}
	selected.Ciphertext = append([]byte(nil), selected.Ciphertext...)
	return selected, ""
}

func (l *accountPoolLease) Context() context.Context { return l.ctx }

func (l *accountPoolLease) PoolRevision() int64 {
	if l == nil {
		return 0
	}
	return l.poolRevision
}

func (l *accountPoolLease) MarkDispatch() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.finished && l.phase < scheduling.MayHaveSent {
		l.phase = scheduling.MayHaveSent
	}
}

func (l *accountPoolLease) MarkOutput() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.finished && l.phase < scheduling.OutputCommitted {
		l.phase = scheduling.OutputCommitted
	}
}

func (l *accountPoolLease) Heartbeat(ctx context.Context) accountPoolRuntimeCode {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.finished {
		return accountPoolAlreadyReleased
	}
	if l.heartbeatFailed {
		return accountPoolStorageUnavailable
	}
	expiresAt, ok := l.inner.Renew()
	if !ok {
		l.heartbeatFailed = true
		l.cancel()
		return accountPoolCancelled
	}
	result, err := l.runtime.app.store.db.ExecContext(ctx, `UPDATE account_pool_runtime_leases SET expires_at=? WHERE lease_id=?`, formatAccountPoolTime(expiresAt), l.inner.ID())
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

func (l *accountPoolLease) Release(ctx context.Context, result scheduling.ReleaseResult) (bool, accountPoolReleaseResult) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.finished {
		return false, accountPoolReleaseResult{Code: accountPoolAlreadyReleased}
	}
	result.Phase = conservativeDispatchPhase(result.Phase, l.phase)
	if l.heartbeatFailed || l.ctx.Err() != nil {
		result.Phase = scheduling.DispatchUnknown
	}
	l.finished = true
	close(l.done)
	l.cancel()
	if l.stopRuntime != nil {
		l.stopRuntime()
	}
	tx, err := l.runtime.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false, accountPoolReleaseResult{Code: accountPoolStorageUnavailable}
	}
	defer tx.Rollback()
	var expires string
	if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM account_pool_runtime_leases WHERE lease_id=?`, l.inner.ID()).Scan(&expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			l.inner.Release(scheduling.ReleaseResult{})
			return false, accountPoolReleaseResult{Code: accountPoolAlreadyReleased}
		}
		return false, accountPoolReleaseResult{Code: accountPoolStorageUnavailable}
	}
	expiry, err := parseTime(expires)
	if err != nil {
		return false, accountPoolReleaseResult{Code: accountPoolStorageUnavailable}
	}
	if !expiry.After(l.runtime.clock.Now()) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM account_pool_runtime_leases WHERE lease_id=?`, l.inner.ID()); err != nil || tx.Commit() != nil {
			return false, accountPoolReleaseResult{Code: accountPoolStorageUnavailable}
		}
		l.inner.Release(scheduling.ReleaseResult{})
		return false, accountPoolReleaseResult{Code: accountPoolAlreadyReleased}
	}
	if duration := l.runtime.cooldowns[result.Failure]; duration > 0 {
		until := formatAccountPoolTime(l.runtime.clock.Now().Add(duration))
		_, err = tx.ExecContext(ctx, `INSERT INTO account_pool_runtime_cooldowns(account_id,failure_class,cooldown_until,updated_at) VALUES(?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET failure_class=excluded.failure_class,cooldown_until=excluded.cooldown_until,updated_at=excluded.updated_at WHERE excluded.cooldown_until>account_pool_runtime_cooldowns.cooldown_until`, l.inner.AccountID(), string(result.Failure), until, rtNow(l.runtime))
		if err != nil {
			return false, accountPoolReleaseResult{Code: accountPoolStorageUnavailable}
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_pool_runtime_leases WHERE lease_id=?`, l.inner.ID()); err != nil {
		return false, accountPoolReleaseResult{Code: accountPoolStorageUnavailable}
	}
	if err := tx.Commit(); err != nil {
		return false, accountPoolReleaseResult{Code: accountPoolStorageUnavailable}
	}
	released, decision := l.inner.Release(result)
	if !released {
		return false, accountPoolReleaseResult{Code: accountPoolAlreadyReleased}
	}
	return true, accountPoolReleaseResult{Code: accountPoolReleased, RetrySuggested: decision.RetrySuggested}
}

func (rt *accountPoolRuntime) startHeartbeat(lease *accountPoolLease) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return false
	}
	rt.active[lease] = struct{}{}
	rt.wg.Add(1)
	go func() {
		defer rt.wg.Done()
		defer rt.unregisterLease(lease)
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

func (rt *accountPoolRuntime) begin() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.closed {
		return false
	}
	rt.wg.Add(1)
	return true
}

// NotifyChanged invalidates the generation observed by queued admissions. It
// never cancels leases that have already been returned to an upstream handler.
// Callers invoke it only after committing an authorization or routing change.
func (rt *accountPoolRuntime) NotifyChanged() {
	rt.mu.Lock()
	if rt.closed {
		rt.mu.Unlock()
		return
	}
	previous := rt.changeCancel
	rt.changeEpoch++
	rt.changeContext, rt.changeCancel = context.WithCancel(context.Background())
	rt.mu.Unlock()
	previous()
}

func (rt *accountPoolRuntime) changeSnapshot() (uint64, context.Context) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.changeEpoch, rt.changeContext
}

func (rt *accountPoolRuntime) changeIsCurrent(epoch uint64) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return !rt.closed && rt.changeEpoch == epoch
}

func (rt *accountPoolRuntime) Close() error {
	rt.mu.Lock()
	if !rt.closed {
		rt.closed = true
		rt.cancel()
		rt.changeCancel()
		for lease := range rt.active {
			lease.cancel()
		}
	}
	rt.mu.Unlock()
	rt.wg.Wait()
	return nil
}

func (rt *accountPoolRuntime) unregisterLease(lease *accountPoolLease) {
	rt.mu.Lock()
	delete(rt.active, lease)
	rt.mu.Unlock()
}

func candidateIDs(candidates []scheduling.Candidate) []string {
	ids := make([]string, len(candidates))
	for i := range candidates {
		ids[i] = candidates[i].ID
	}
	return ids
}

func validAccountPoolAcquireOptions(options accountPoolAcquireOptions) bool {
	if options.ExpectedPoolRevision < 0 || len(options.ExcludedAccountIDs) > maxModelAccounts {
		return false
	}
	seen := make(map[string]struct{}, len(options.ExcludedAccountIDs))
	for _, id := range options.ExcludedAccountIDs {
		if !validIdentifier(id, 128) {
			return false
		}
		if _, duplicate := seen[id]; duplicate {
			return false
		}
		seen[id] = struct{}{}
	}
	return true
}

func accountPoolAccountExcluded(accountID string, excluded []string) bool {
	for _, id := range excluded {
		if id == accountID {
			return true
		}
	}
	return false
}

func conservativeDispatchPhase(explicit, observed scheduling.DispatchPhase) scheduling.DispatchPhase {
	if explicit == scheduling.DispatchUnknown || observed == scheduling.DispatchUnknown || explicit > scheduling.OutputCommitted || observed > scheduling.OutputCommitted {
		return scheduling.DispatchUnknown
	}
	if explicit > observed {
		return explicit
	}
	return observed
}

func providerAllowed(provider string, allowed []string) bool {
	for _, candidate := range allowed {
		if candidate == provider {
			return true
		}
	}
	return false
}

func mapSchedulingCode(code scheduling.ReasonCode) accountPoolRuntimeCode {
	switch code {
	case scheduling.ReasonCancelled:
		return accountPoolCancelled
	case scheduling.ReasonQueueFull:
		return accountPoolQueueFull
	case scheduling.ReasonCapacityUnavailable:
		return accountPoolCapacityUnavailable
	case scheduling.ReasonNoAllowedAccount, scheduling.ReasonNoCompatibleAccount:
		return accountPoolNoCompatible
	default:
		return accountPoolInvalid
	}
}

func rtNow(rt *accountPoolRuntime) string {
	return formatAccountPoolTime(rt.clock.Now())
}

func formatAccountPoolTime(value time.Time) string {
	return value.UTC().Format(accountPoolTimeLayout)
}

func (code accountPoolRuntimeCode) Error() string { return string(code) }

var _ error = accountPoolRuntimeCode("")
