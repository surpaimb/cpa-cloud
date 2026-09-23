package service

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"cpacloud.local/server/internal/accounting"
)

const (
	recoveryWorkerRetryDelay = time.Second
	recoverySettlementLimit  = 3 * time.Second
	recoveryEventMaxAttempts = int64(3)
)

type accountRecoveryCoordinator struct {
	app            *App
	execute        func(context.Context, accountMaintenanceAcquireRequest) (accountPoolRuntimeCode, *recoveryExecutionReceipt)
	newOperationID func() (string, error)
	afterScan      func()
	writeSetting   func(context.Context, int64, bool) (accountRecoverySettings, error)

	settingsMu sync.Mutex
	mu         sync.Mutex
	started    bool
	closed     bool
	accepting  bool
	workerCtx  context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	epoch      uint64
	changed    chan struct{}
	nextWake   time.Time
}

type accountRecoveryCoordinatorSnapshot struct {
	CLIAllowed      bool       `json:"cli_allowed"`
	Enabled         bool       `json:"enabled"`
	SettingRevision int64      `json:"setting_revision"`
	Running         bool       `json:"running"`
	NextWakeAt      *time.Time `json:"next_wake_at"`
	HistoryCount    int64      `json:"history_count"`
	HistoryFull     bool       `json:"history_full"`
	ServerTime      time.Time  `json:"server_time"`
}

type accountRecoveryAccountSnapshot struct {
	AccountID        string     `json:"account_id"`
	CooldownEventID  string     `json:"cooldown_event_id"`
	OperationID      string     `json:"operation_id"`
	RecoveryRevision int64      `json:"recovery_revision"`
	AccountRevision  int64      `json:"account_revision"`
	PoolRevision     int64      `json:"pool_revision"`
	PublicModel      string     `json:"public_model"`
	UpstreamModel    string     `json:"upstream_model"`
	Protocol         string     `json:"protocol"`
	State            string     `json:"state"`
	NextProbeAt      time.Time  `json:"next_probe_at"`
	Due              bool       `json:"due"`
	LastResultCode   *string    `json:"last_result_code"`
	LastFinishedAt   *time.Time `json:"last_finished_at"`
	AttemptCount     int64      `json:"attempt_count"`
	AutoEligible     bool       `json:"auto_eligible"`
	AttentionCode    *string    `json:"attention_code"`
	CheckedAt        *time.Time `json:"checked_at"`
}

func newAccountRecoveryCoordinator(app *App) *accountRecoveryCoordinator {
	coordinator := &accountRecoveryCoordinator{app: app, changed: make(chan struct{}), newOperationID: newRecoveryOperationID}
	if app != nil {
		coordinator.execute = app.executeRecoveryOperation
	}
	coordinator.writeSetting = coordinator.casSetting
	return coordinator
}

func (c *accountRecoveryCoordinator) Start(ctx context.Context) error {
	if c == nil || c.app == nil || c.app.store == nil || c.app.systemProbes == nil || c.app.accountPool == nil || ctx == nil {
		return errors.New("invalid account recovery coordinator")
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	c.mu.Lock()
	if c.closed || c.started {
		c.mu.Unlock()
		return errors.New("account recovery coordinator cannot start")
	}
	c.started = true
	c.mu.Unlock()
	setting, err := c.loadSettings(ctx)
	if err != nil {
		c.mu.Lock()
		c.started = false
		c.mu.Unlock()
		return err
	}
	if c.app.cfg.AccountRecoveryEnabled && setting.Enabled {
		c.startWorkerLocked()
	}
	return nil
}

func (c *accountRecoveryCoordinator) Close() {
	if c == nil {
		return
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	c.mu.Lock()
	c.closed = true
	c.accepting = false
	cancel, done := c.cancel, c.done
	c.signalLocked()
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (c *accountRecoveryCoordinator) Notify() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.signalLocked()
	c.mu.Unlock()
}

func (c *accountRecoveryCoordinator) SetEnabled(ctx context.Context, expectedRevision int64, enabled bool) (accountRecoverySettings, error) {
	if c == nil || c.app == nil || ctx == nil || expectedRevision < 1 {
		return accountRecoverySettings{}, errors.New("invalid account recovery setting update")
	}
	if enabled && !c.app.cfg.AccountRecoveryEnabled {
		return accountRecoverySettings{}, errAccountRecoveryNotAllowed
	}
	c.settingsMu.Lock()
	defer c.settingsMu.Unlock()
	current, err := c.loadSettings(ctx)
	if err != nil {
		return accountRecoverySettings{}, err
	}
	if current.Revision != expectedRevision {
		return accountRecoverySettings{}, errAccountRecoverySettingConflict
	}
	if current.Enabled == enabled {
		if enabled {
			c.startWorkerLocked()
		}
		return current, nil
	}
	if !enabled {
		c.mu.Lock()
		c.accepting = false
		cancel, done := c.cancel, c.done
		c.signalLocked()
		c.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		if done != nil {
			<-done
		}
		updated, updateErr := c.writeSetting(ctx, current.Revision, false)
		if updateErr != nil {
			resolved, resolvedErr := c.readSettingAfterWrite()
			if resolvedErr == nil {
				if resolved.Enabled {
					c.startWorkerLocked()
				}
				if resolved.Revision == current.Revision+1 && !resolved.Enabled {
					return resolved, nil
				}
			}
			return accountRecoverySettings{}, updateErr
		}
		return updated, nil
	}
	updated, err := c.writeSetting(ctx, current.Revision, true)
	if err != nil {
		resolved, resolvedErr := c.readSettingAfterWrite()
		if resolvedErr == nil && resolved.Enabled {
			c.startWorkerLocked()
			if resolved.Revision == current.Revision+1 {
				return resolved, nil
			}
		}
		return accountRecoverySettings{}, err
	}
	c.startWorkerLocked()
	return updated, nil
}

func (c *accountRecoveryCoordinator) Snapshot(ctx context.Context) (accountRecoveryCoordinatorSnapshot, error) {
	if c == nil || c.app == nil || ctx == nil {
		return accountRecoveryCoordinatorSnapshot{}, errors.New("invalid account recovery snapshot")
	}
	setting, err := c.loadSettings(ctx)
	if err != nil {
		return accountRecoveryCoordinatorSnapshot{}, err
	}
	count, err := c.app.systemProbes.HistoryCount(ctx)
	if err != nil {
		return accountRecoveryCoordinatorSnapshot{}, err
	}
	now := c.now()
	c.mu.Lock()
	running := c.done != nil
	var wake *time.Time
	if !c.nextWake.IsZero() {
		value := c.nextWake
		wake = &value
	}
	c.mu.Unlock()
	return accountRecoveryCoordinatorSnapshot{CLIAllowed: c.app.cfg.AccountRecoveryEnabled, Enabled: setting.Enabled,
		SettingRevision: setting.Revision, Running: running, NextWakeAt: wake, HistoryCount: count,
		HistoryFull: count >= accounting.MaxSystemProbeHistory, ServerTime: now}, nil
}

func (c *accountRecoveryCoordinator) AccountSnapshots(ctx context.Context) ([]accountRecoveryAccountSnapshot, error) {
	if c == nil || c.app == nil || ctx == nil {
		return nil, errors.New("invalid account recovery account snapshot")
	}
	now := c.now()
	setting, err := c.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := c.app.store.db.QueryContext(ctx, `SELECT r.account_id,r.cooldown_event_id,r.operation_id,r.recovery_revision,r.account_revision,r.pool_revision,
		r.public_model,r.upstream_model,r.protocol,r.state,r.next_probe_at,r.attention_code,r.checked_at,
		a.result_code,a.finished_at,(SELECT COUNT(*) FROM system_probe_attempts ec WHERE ec.recovery_event_id=r.cooldown_event_id),
		(SELECT COUNT(*) FROM system_probe_attempts),
		CASE WHEN u.id IS NOT NULL AND u.enabled=1 AND u.revision=r.account_revision AND u.provider_kind=r.provider_kind
			AND m.enabled=1 AND pc.revision=r.pool_revision AND pr.upstream_model=r.upstream_model AND cd.event_id=r.cooldown_event_id
			AND ((r.source_snapshot='api_key' AND r.provider_kind<>'codex-membership' AND cb.upstream_id IS NULL)
				OR (r.source_snapshot='import' AND r.provider_kind='codex-membership' AND cb.upstream_id IS NULL AND u.credential_state<>'reauth_required')
				OR (r.source_snapshot='authorization_code' AND r.provider_kind='codex-membership' AND cb.client_id=r.client_id AND cb.client_id=? AND cb.source='authorization_code' AND u.credential_state<>'reauth_required'))
			THEN 1 ELSE 0 END,r.provider_kind
		FROM account_recovery_states r
		LEFT JOIN system_probe_attempts a ON a.operation_id=r.operation_id
		LEFT JOIN upstreams u ON u.id=r.account_id
		LEFT JOIN models m ON m.id=r.public_model
		LEFT JOIN model_account_pool_configs pc ON pc.model_id=r.public_model
		LEFT JOIN model_account_pool_routes pr ON pr.model_id=r.public_model AND pr.upstream_id=r.account_id
		LEFT JOIN account_pool_runtime_cooldowns cd ON cd.account_id=r.account_id
		LEFT JOIN codex_oauth_bindings cb ON cb.upstream_id=r.account_id
		ORDER BY r.account_id`, c.app.cfg.CodexOAuthClientID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]accountRecoveryAccountSnapshot, 0)
	for rows.Next() {
		var item accountRecoveryAccountSnapshot
		var next string
		var attention, checked, result, finished sql.NullString
		var total int64
		var snapshotCurrent int
		var providerKind string
		if err := rows.Scan(&item.AccountID, &item.CooldownEventID, &item.OperationID, &item.RecoveryRevision, &item.AccountRevision, &item.PoolRevision,
			&item.PublicModel, &item.UpstreamModel, &item.Protocol, &item.State, &next, &attention, &checked, &result, &finished, &item.AttemptCount, &total, &snapshotCurrent, &providerKind); err != nil {
			return nil, err
		}
		if item.NextProbeAt, err = parseTime(next); err != nil || next != formatAccountPoolTime(item.NextProbeAt) {
			return nil, errors.New("invalid recovery projection timestamp")
		}
		if attention.Valid {
			value := attention.String
			item.AttentionCode = &value
		}
		if checked.Valid {
			value, parseErr := parseTime(checked.String)
			if parseErr != nil || checked.String != formatAccountPoolTime(value) {
				return nil, errors.New("invalid recovery checked timestamp")
			}
			item.CheckedAt = &value
		}
		if result.Valid {
			value := result.String
			item.LastResultCode = &value
		}
		if finished.Valid {
			value, parseErr := time.Parse(time.RFC3339Nano, finished.String)
			if parseErr != nil || finished.String != value.UTC().Format(time.RFC3339Nano) {
				return nil, errors.New("invalid recovery result timestamp")
			}
			value = value.UTC()
			item.LastFinishedAt = &value
		}
		item.Due = !item.NextProbeAt.After(now)
		retryable := item.State == recoveryRequired && item.LastResultCode == nil
		if item.State == recoveryInterrupted && item.LastResultCode != nil {
			switch accounting.SystemProbeResult(*item.LastResultCode) {
			case accounting.SystemProbeRateLimited, accounting.SystemProbeUpstreamUnavailable, accounting.SystemProbeUpstreamTimeout, accounting.SystemProbeCancelledResult, accounting.SystemProbeInterruptedResult:
				retryable = true
			}
		}
		providerAllowed := providerKind != codexMembershipProvider || c.app.cfg.ExperimentalCodexMembership
		item.AutoEligible = c.app.cfg.AccountRecoveryEnabled && setting.Enabled && providerAllowed && snapshotCurrent == 1 && retryable && item.AttentionCode == nil && item.AttemptCount < recoveryEventMaxAttempts && total < accounting.MaxSystemProbeHistory
		items = append(items, item)
	}
	return items, rows.Err()
}

func (c *accountRecoveryCoordinator) startWorkerLocked() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || !c.started || c.done != nil || !c.app.cfg.AccountRecoveryEnabled {
		return
	}
	c.workerCtx, c.cancel = context.WithCancel(context.Background())
	c.done = make(chan struct{})
	c.accepting = true
	ctx, done := c.workerCtx, c.done
	go c.run(ctx, done)
}

func (c *accountRecoveryCoordinator) run(ctx context.Context, done chan struct{}) {
	defer func() {
		c.mu.Lock()
		if c.done == done {
			c.done, c.cancel, c.workerCtx = nil, nil, nil
			c.nextWake = time.Time{}
		}
		c.mu.Unlock()
		close(done)
	}()
	var cursorTime time.Time
	var cursorAccount string
	for {
		if ctx.Err() != nil || !c.isAccepting() {
			return
		}
		coordinatorEpoch, changed := c.changeSnapshot()
		runtimeEpoch, runtimeChanged := c.app.accountPool.changeSnapshot()
		state, next, err := c.nextState(ctx, cursorTime, cursorAccount)
		if c.afterScan != nil {
			c.afterScan()
		}
		if !c.changeIsCurrent(coordinatorEpoch) || !c.app.accountPool.changeIsCurrent(runtimeEpoch) {
			continue
		}
		if err != nil {
			if !c.wait(ctx, c.now().Add(recoveryWorkerRetryDelay), coordinatorEpoch, changed, runtimeEpoch, runtimeChanged) {
				return
			}
			continue
		}
		if state == nil {
			cursorTime, cursorAccount = time.Time{}, ""
			if !c.wait(ctx, next, coordinatorEpoch, changed, runtimeEpoch, runtimeChanged) {
				return
			}
			continue
		}
		cursorTime, cursorAccount = state.NextProbeAt, state.AccountID
		c.process(ctx, *state)
		// Every failure path is bounded even when SQLite is unavailable and a
		// durable deferral cannot be written. State changes wake this delay.
		coordinatorEpoch, changed = c.changeSnapshot()
		runtimeEpoch, runtimeChanged = c.app.accountPool.changeSnapshot()
		if !c.wait(ctx, c.now().Add(recoveryWorkerRetryDelay), coordinatorEpoch, changed, runtimeEpoch, runtimeChanged) {
			return
		}
	}
}

func (c *accountRecoveryCoordinator) nextState(ctx context.Context, cursorTime time.Time, cursorAccount string) (*accountRecoveryState, time.Time, error) {
	now := c.now()
	row := c.app.store.db.QueryRowContext(ctx, accountRecoverySelect+` WHERE attention_code IS NULL AND next_probe_at<=? AND (next_probe_at>? OR (next_probe_at=? AND account_id>?)) ORDER BY next_probe_at,account_id LIMIT 1`,
		formatAccountPoolTime(now), formatAccountPoolTime(cursorTime), formatAccountPoolTime(cursorTime), cursorAccount)
	state, err := scanAccountRecoveryState(row)
	if err == nil {
		return &state, time.Time{}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, time.Time{}, err
	}
	// Wrap the fairness cursor before sleeping, otherwise an older due row
	// inserted behind the cursor could remain unseen indefinitely.
	if !cursorTime.IsZero() {
		row = c.app.store.db.QueryRowContext(ctx, accountRecoverySelect+` WHERE attention_code IS NULL AND next_probe_at<=? ORDER BY next_probe_at,account_id LIMIT 1`, formatAccountPoolTime(now))
		state, err = scanAccountRecoveryState(row)
		if err == nil {
			return &state, time.Time{}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, time.Time{}, err
		}
	}
	var nextValue sql.NullString
	if err := c.app.store.db.QueryRowContext(ctx, `SELECT MIN(next_probe_at) FROM account_recovery_states WHERE attention_code IS NULL`).Scan(&nextValue); err != nil {
		return nil, time.Time{}, err
	}
	if !nextValue.Valid {
		return nil, time.Time{}, nil
	}
	next, err := parseTime(nextValue.String)
	if err != nil || nextValue.String != formatAccountPoolTime(next) {
		return nil, time.Time{}, errors.New("invalid recovery wake timestamp")
	}
	return nil, next, nil
}

func (c *accountRecoveryCoordinator) process(ctx context.Context, state accountRecoveryState) {
	switch state.State {
	case recoveryRequired:
		c.processRequired(ctx, state)
	case recoveryInProgress:
		c.reconcileTerminal(ctx, state)
	case recoveryInterrupted:
		c.rotateInterrupted(ctx, state)
	}
}

func (c *accountRecoveryCoordinator) processRequired(ctx context.Context, state accountRecoveryState) {
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	current, err := loadAccountRecoveryStateTx(ctx, tx, state.AccountID)
	if err != nil || !sameRecoveryIdentity(current, state) || current.State != recoveryRequired {
		return
	}
	counts, err := c.app.systemProbes.CountsTx(ctx, tx, state.CooldownEventID)
	if err != nil {
		return
	}
	if counts.Total >= accounting.MaxSystemProbeHistory {
		_ = c.blockTx(ctx, tx, state, recoveryAttentionHistoryFull)
		_ = tx.Commit()
		return
	}
	if counts.Event >= recoveryEventMaxAttempts {
		_ = c.blockTx(ctx, tx, state, recoveryAttentionRetryLimit)
		_ = tx.Commit()
		return
	}
	_, attemptErr := c.app.systemProbes.GetTx(ctx, tx, state.OperationID)
	if attemptErr == nil {
		_ = tx.Commit()
		c.reconcileTerminal(ctx, state)
		return
	}
	if !errors.Is(attemptErr, accounting.ErrNotFound) {
		return
	}
	if err := tx.Commit(); err != nil {
		return
	}
	request := accountMaintenanceAcquireRequest{OperationID: state.OperationID, AccountID: state.AccountID, CooldownEventID: state.CooldownEventID,
		ExpectedPoolRevision: state.PoolRevision, ExpectedAccountRevision: state.AccountRevision, ExpectedRecoveryRevision: state.RecoveryRevision,
		CapacityWait: 100 * time.Millisecond}
	code, receipt := c.execute(ctx, request)
	if receipt != nil && code == accountPoolStorageUnavailable {
		settleCtx, cancel := context.WithTimeout(context.Background(), recoverySettlementLimit)
		code = receipt.Finalize(settleCtx)
		cancel()
	}
	if code == accountPoolStorageUnavailable && receipt != nil {
		c.blockCurrent(receipt.lease.Snapshot(), recoveryAttentionSettlementPending)
		return
	}
	switch code {
	case accountPoolAcquired, accountPoolReleased, accountPoolAlreadyReleased:
		c.Notify()
	case accountPoolCapacityUnavailable, accountPoolQueueFull:
		c.deferSame(state, c.now().Add(recoveryWorkerRetryDelay), "")
	case accountPoolAccountChanged, accountPoolConfigurationChanged, accountPoolAuthorizationChanged:
		c.block(state, recoveryAttentionConfigurationChanged)
	case accountPoolCancelled, accountPoolClosed:
		if ctx.Err() == nil {
			c.deferSame(state, c.now().Add(recoveryWorkerRetryDelay), "")
		}
	default:
		c.block(state, recoveryAttentionStorageUnavailable)
	}
}

func (c *accountRecoveryCoordinator) reconcileTerminal(ctx context.Context, state accountRecoveryState) {
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	current, err := loadAccountRecoveryStateTx(ctx, tx, state.AccountID)
	if err != nil || !sameRecoveryIdentity(current, state) {
		return
	}
	attempt, err := c.app.systemProbes.GetTx(ctx, tx, state.OperationID)
	if errors.Is(err, accounting.ErrNotFound) {
		if current.State == recoveryRequired || current.State == recoveryInProgress {
			_ = c.blockTx(ctx, tx, current, recoveryAttentionSettlementPending)
			_ = tx.Commit()
		}
		return
	}
	if err != nil {
		return
	}
	if attempt.Status == accounting.SystemProbePending {
		if current.State == recoveryRequired || current.State == recoveryInProgress {
			_ = c.blockTx(ctx, tx, current, recoveryAttentionSettlementPending)
			_ = tx.Commit()
		}
		return
	}
	if current.State == recoveryRequired || current.State == recoveryInProgress {
		attention := attentionForAttempt(attempt)
		now := recoveryStateUpdateTime(current, c.now())
		next := now.Add(recoveryRetryDelay)
		if attempt.FinishedAt != nil && attempt.FinishedAt.Add(recoveryRetryDelay).After(next) {
			next = attempt.FinishedAt.Add(recoveryRetryDelay)
		}
		result, updateErr := tx.ExecContext(ctx, `UPDATE account_recovery_states SET state='interrupted',attention_code=?,checked_at=?,next_probe_at=?,updated_at=? WHERE account_id=? AND operation_id=? AND recovery_revision=? AND state=?`,
			nullableRecoveryString(attention), formatAccountPoolTime(now), formatAccountPoolTime(next), formatAccountPoolTime(now), current.AccountID, current.OperationID, current.RecoveryRevision, current.State)
		if recoveryChangedExactlyOne(result, updateErr) != nil {
			return
		}
	}
	_ = tx.Commit()
	c.Notify()
}

func (c *accountRecoveryCoordinator) rotateInterrupted(ctx context.Context, state accountRecoveryState) {
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	current, err := loadAccountRecoveryStateTx(ctx, tx, state.AccountID)
	if err != nil || !sameRecoveryIdentity(current, state) || current.State != recoveryInterrupted {
		return
	}
	attempt, err := c.app.systemProbes.GetTx(ctx, tx, current.OperationID)
	if errors.Is(err, accounting.ErrNotFound) || err == nil && attempt.Status == accounting.SystemProbePending {
		_ = c.blockTx(ctx, tx, current, recoveryAttentionSettlementPending)
		_ = tx.Commit()
		return
	}
	if err != nil {
		return
	}
	if err := validateAccountRecoveryRelationsTx(ctx, tx, current); err != nil {
		attention := recoveryAttentionStorageUnavailable
		if errors.Is(err, errAccountRecoveryConflict) || errors.Is(err, sql.ErrNoRows) {
			attention = recoveryAttentionConfigurationChanged
		}
		_ = c.blockTx(ctx, tx, current, attention)
		_ = tx.Commit()
		return
	}
	attention := attentionForAttempt(attempt)
	counts, err := c.app.systemProbes.CountsTx(ctx, tx, current.CooldownEventID)
	if err != nil {
		return
	}
	if counts.Total >= accounting.MaxSystemProbeHistory {
		attention = recoveryAttentionHistoryFull
	}
	if counts.Event >= recoveryEventMaxAttempts {
		attention = recoveryAttentionRetryLimit
	}
	if attention != "" {
		_ = c.blockTx(ctx, tx, current, attention)
		_ = tx.Commit()
		return
	}
	if current.RecoveryRevision >= 9007199254740991 {
		_ = c.blockTx(ctx, tx, current, recoveryAttentionRetryLimit)
		_ = tx.Commit()
		return
	}
	operationID, err := c.newOperationID()
	if err != nil {
		_ = c.blockTx(ctx, tx, current, recoveryAttentionStorageUnavailable)
		_ = tx.Commit()
		return
	}
	now := recoveryStateUpdateTime(current, c.now())
	result, err := tx.ExecContext(ctx, `UPDATE account_recovery_states SET operation_id=?,recovery_revision=recovery_revision+1,state='required',attention_code=NULL,checked_at=NULL,next_probe_at=?,updated_at=? WHERE account_id=? AND operation_id=? AND recovery_revision=? AND state='interrupted'`,
		operationID, formatAccountPoolTime(now), formatAccountPoolTime(now), current.AccountID, current.OperationID, current.RecoveryRevision)
	if recoveryChangedExactlyOne(result, err) != nil || tx.Commit() != nil {
		return
	}
	c.Notify()
}

func attentionForAttempt(attempt accounting.SystemProbeAttempt) string {
	if attempt.Result == nil {
		return recoveryAttentionSettlementPending
	}
	switch *attempt.Result {
	case accounting.SystemProbeRateLimited, accounting.SystemProbeUpstreamUnavailable, accounting.SystemProbeUpstreamTimeout,
		accounting.SystemProbeCancelledResult, accounting.SystemProbeInterruptedResult:
		return ""
	case accounting.SystemProbeAuthenticationFailed:
		return recoveryAttentionAuthentication
	case accounting.SystemProbeProtocolError:
		return recoveryAttentionProtocol
	case accounting.SystemProbeUnsupported:
		return recoveryAttentionUnsupported
	case accounting.SystemProbeConfigurationChanged:
		return recoveryAttentionConfigurationChanged
	default:
		return recoveryAttentionSettlementPending
	}
}

func (c *accountRecoveryCoordinator) deferSame(state accountRecoveryState, next time.Time, attention string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	now := recoveryStateUpdateTime(state, c.now())
	if attention == "" {
		if next.Before(now) {
			next = now.Add(recoveryWorkerRetryDelay)
		}
		result, err := tx.ExecContext(ctx, `UPDATE account_recovery_states SET attention_code=NULL,checked_at=?,next_probe_at=?,updated_at=? WHERE account_id=? AND operation_id=? AND recovery_revision=? AND state=?`, formatAccountPoolTime(now), formatAccountPoolTime(next), formatAccountPoolTime(now), state.AccountID, state.OperationID, state.RecoveryRevision, state.State)
		if recoveryChangedExactlyOne(result, err) != nil {
			return
		}
	} else if c.blockTx(ctx, tx, state, attention) != nil {
		return
	}
	if tx.Commit() == nil {
		c.Notify()
	}
}

func (c *accountRecoveryCoordinator) block(state accountRecoveryState, attention string) {
	c.deferSame(state, state.NextProbeAt, attention)
}

func (c *accountRecoveryCoordinator) blockCurrent(expected accountRecoveryState, attention string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	state, err := loadAccountRecoveryStateTx(ctx, tx, expected.AccountID)
	if err != nil {
		return
	}
	if !sameRecoveryIdentity(state, expected) {
		return
	}
	if c.blockTx(ctx, tx, state, attention) == nil && tx.Commit() == nil {
		c.Notify()
	}
}

func (c *accountRecoveryCoordinator) blockTx(ctx context.Context, tx *sql.Tx, state accountRecoveryState, attention string) error {
	if !validRecoveryAttention(attention) || attention == "" {
		return errors.New("invalid recovery attention")
	}
	now := recoveryStateUpdateTime(state, c.now())
	result, err := tx.ExecContext(ctx, `UPDATE account_recovery_states SET attention_code=?,checked_at=?,updated_at=? WHERE account_id=? AND operation_id=? AND recovery_revision=? AND state=?`, attention, formatAccountPoolTime(now), formatAccountPoolTime(now), state.AccountID, state.OperationID, state.RecoveryRevision, state.State)
	return recoveryChangedExactlyOne(result, err)
}

func recoveryStateUpdateTime(state accountRecoveryState, now time.Time) time.Time {
	now = now.UTC()
	if state.CreatedAt.After(now) {
		now = state.CreatedAt
	}
	if state.UpdatedAt.After(now) {
		now = state.UpdatedAt
	}
	return now
}

func sameRecoveryIdentity(left, right accountRecoveryState) bool {
	return left.AccountID == right.AccountID && left.CooldownEventID == right.CooldownEventID && left.OperationID == right.OperationID && left.RecoveryRevision == right.RecoveryRevision
}

func (c *accountRecoveryCoordinator) wait(ctx context.Context, wake time.Time, coordinatorEpoch uint64, changed <-chan struct{}, runtimeEpoch uint64, runtimeChanged context.Context) bool {
	c.mu.Lock()
	c.nextWake = wake
	current := c.epoch == coordinatorEpoch && c.accepting && !c.closed
	c.mu.Unlock()
	if !current || !c.app.accountPool.changeIsCurrent(runtimeEpoch) {
		return true
	}
	if wake.IsZero() {
		select {
		case <-ctx.Done():
			return false
		case <-changed:
			return true
		case <-runtimeChanged.Done():
			return true
		}
	}
	delay := wake.Sub(c.now())
	if delay < 0 {
		delay = 0
	}
	select {
	case <-ctx.Done():
		return false
	case <-changed:
		return true
	case <-runtimeChanged.Done():
		return true
	case <-c.app.accountPool.clock.After(delay):
		return true
	}
}

func (c *accountRecoveryCoordinator) changeSnapshot() (uint64, <-chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch, c.changed
}

func (c *accountRecoveryCoordinator) changeIsCurrent(epoch uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.epoch == epoch && c.accepting && !c.closed
}

func (c *accountRecoveryCoordinator) signalLocked() {
	close(c.changed)
	c.changed = make(chan struct{})
	c.epoch++
}

func (c *accountRecoveryCoordinator) isAccepting() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accepting && !c.closed
}
func (c *accountRecoveryCoordinator) now() time.Time {
	if c.app != nil && c.app.accountPool != nil && c.app.accountPool.clock != nil {
		return c.app.accountPool.clock.Now().UTC()
	}
	return time.Now().UTC()
}

func (c *accountRecoveryCoordinator) loadSettings(ctx context.Context) (accountRecoverySettings, error) {
	tx, err := c.app.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return accountRecoverySettings{}, err
	}
	defer tx.Rollback()
	item, err := loadAccountRecoverySettingsTx(ctx, tx)
	if err != nil {
		return accountRecoverySettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return accountRecoverySettings{}, err
	}
	return item, nil
}

func (c *accountRecoveryCoordinator) casSetting(ctx context.Context, expected int64, enabled bool) (accountRecoverySettings, error) {
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return accountRecoverySettings{}, err
	}
	defer tx.Rollback()
	current, err := loadAccountRecoverySettingsTx(ctx, tx)
	if err != nil {
		return accountRecoverySettings{}, err
	}
	if current.Revision != expected || expected >= 9007199254740991 {
		return accountRecoverySettings{}, errAccountRecoverySettingConflict
	}
	now := c.now()
	if current.UpdatedAt.After(now) {
		now = current.UpdatedAt
	}
	result, err := tx.ExecContext(ctx, `UPDATE account_recovery_settings SET enabled=?,revision=revision+1,updated_at=? WHERE singleton=1 AND revision=?`, boolInt(enabled), formatAccountPoolTime(now), expected)
	if err != nil {
		return accountRecoverySettings{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return accountRecoverySettings{}, err
	}
	if changed != 1 {
		return accountRecoverySettings{}, errAccountRecoverySettingConflict
	}
	updated, err := loadAccountRecoverySettingsTx(ctx, tx)
	if err != nil {
		return accountRecoverySettings{}, err
	}
	if err := tx.Commit(); err != nil {
		return accountRecoverySettings{}, err
	}
	return updated, nil
}

func (c *accountRecoveryCoordinator) readSettingAfterWrite() (accountRecoverySettings, error) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return c.loadSettings(ctx)
}

func newRecoveryOperationID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32], nil
}
