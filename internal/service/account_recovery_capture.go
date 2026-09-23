package service

// Employee failure capture is independently implemented from the recovery
// coordinator contract. It persists metadata only; it never reads credentials.
import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/scheduling"
)

type accountRecoverySnapshot struct {
	AccountID       string
	PoolRevision    int64
	AccountRevision int64
	ProviderKind    string
	SourceSnapshot  string
	ClientID        string
	PublicModel     string
	UpstreamModel   string
	Protocol        accounting.UsageProtocol
}

func (l *accountPoolLease) BindRecoverySnapshot(ctx context.Context, publicModel string, protocol accounting.UsageProtocol, selected route) accountPoolRuntimeCode {
	if l == nil || l.runtime == nil || selected.AccountID == "" || selected.AccountID != l.inner.AccountID() || publicModel == "" {
		return accountPoolInvalid
	}
	if _, err := usageProvider(selected.ProviderKind, protocol); err != nil {
		return accountPoolInvalid
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.finished || l.heartbeatFailed || l.phase != scheduling.DispatchNotStarted || l.ctx.Err() != nil || ctx.Err() != nil {
		return accountPoolCancelled
	}
	unlockMutation, err := l.runtime.acquireMaintenanceMutationLock(ctx, selected.ProviderKind, selected.AccountID)
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
	snapshot, code := loadCurrentRecoverySnapshotTx(ctx, tx, selected.AccountID, publicModel, protocol)
	if code != "" {
		return code
	}
	if snapshot.PoolRevision != l.poolRevision || snapshot.AccountRevision != selected.Revision ||
		snapshot.ProviderKind != selected.ProviderKind || snapshot.UpstreamModel != selected.UpstreamModel {
		return accountPoolConfigurationChanged
	}
	var expiresText string
	if err := tx.QueryRowContext(ctx, `SELECT expires_at FROM account_pool_runtime_leases
		WHERE lease_id=? AND account_id=? AND public_model=? AND pool_revision=?`,
		l.inner.ID(), snapshot.AccountID, snapshot.PublicModel, snapshot.PoolRevision).Scan(&expiresText); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return accountPoolConfigurationChanged
		}
		return accountPoolStorageUnavailable
	}
	expiresAt, err := parseTime(expiresText)
	if err != nil {
		return accountPoolStorageUnavailable
	}
	if !expiresAt.After(l.runtime.clock.Now()) {
		return accountPoolCancelled
	}
	result, err := tx.ExecContext(ctx, `UPDATE account_pool_runtime_leases SET account_revision=?
		WHERE lease_id=? AND account_id=? AND public_model=? AND pool_revision=?`,
		snapshot.AccountRevision, l.inner.ID(), snapshot.AccountID, snapshot.PublicModel, snapshot.PoolRevision)
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
	l.recovery = &snapshot
	return accountPoolAcquired
}

func loadCurrentRecoverySnapshotTx(ctx context.Context, tx *sql.Tx, accountID, publicModel string, protocol accounting.UsageProtocol) (accountRecoverySnapshot, accountPoolRuntimeCode) {
	var snapshot accountRecoverySnapshot
	var enabled int
	var source, client sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT u.id,c.revision,u.revision,u.provider_kind,u.enabled,r.upstream_model,b.source,b.client_id
		FROM model_account_pool_routes r
		JOIN model_account_pool_configs c ON c.model_id=r.model_id
		JOIN models m ON m.id=r.model_id
		JOIN upstreams u ON u.id=r.upstream_id
		LEFT JOIN codex_oauth_bindings b ON b.upstream_id=u.id
		WHERE r.model_id=? AND r.upstream_id=? AND m.enabled=1`, publicModel, accountID).Scan(
		&snapshot.AccountID, &snapshot.PoolRevision, &snapshot.AccountRevision, &snapshot.ProviderKind,
		&enabled, &snapshot.UpstreamModel, &source, &client)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, accountPoolConfigurationChanged
	}
	if err != nil {
		return snapshot, accountPoolStorageUnavailable
	}
	if enabled != 1 {
		return snapshot, accountPoolAccountChanged
	}
	if _, err := usageProvider(snapshot.ProviderKind, protocol); err != nil {
		return snapshot, accountPoolConfigurationChanged
	}
	snapshot.PublicModel = publicModel
	snapshot.Protocol = protocol
	switch snapshot.ProviderKind {
	case codexMembershipProvider:
		if source.Valid {
			if source.String != "authorization_code" || !client.Valid || client.String == "" {
				return snapshot, accountPoolConfigurationChanged
			}
			snapshot.SourceSnapshot, snapshot.ClientID = source.String, client.String
		} else {
			if client.Valid {
				return snapshot, accountPoolConfigurationChanged
			}
			snapshot.SourceSnapshot = "import"
		}
	default:
		if source.Valid || client.Valid {
			return snapshot, accountPoolConfigurationChanged
		}
		snapshot.SourceSnapshot = "api_key"
	}
	return snapshot, ""
}

func sameRecoverySnapshot(left, right accountRecoverySnapshot) bool {
	return left == right
}

func stateRecoverySnapshot(state accountRecoveryState) accountRecoverySnapshot {
	return accountRecoverySnapshot{
		AccountID: state.AccountID, PoolRevision: state.PoolRevision, AccountRevision: state.AccountRevision,
		ProviderKind: state.ProviderKind, SourceSnapshot: state.SourceSnapshot, ClientID: state.ClientID,
		PublicModel: state.PublicModel, UpstreamModel: state.UpstreamModel, Protocol: accounting.UsageProtocol(state.Protocol),
	}
}

func snapshotRecoveryState(snapshot accountRecoverySnapshot, eventID, operationID string, revision int64, next, created, updated time.Time) accountRecoveryState {
	return accountRecoveryState{
		AccountID: snapshot.AccountID, CooldownEventID: eventID, OperationID: operationID,
		RecoveryRevision: revision, PoolRevision: snapshot.PoolRevision, AccountRevision: snapshot.AccountRevision,
		ProviderKind: snapshot.ProviderKind, SourceSnapshot: snapshot.SourceSnapshot, ClientID: snapshot.ClientID,
		PublicModel: snapshot.PublicModel, UpstreamModel: snapshot.UpstreamModel, Protocol: string(snapshot.Protocol),
		State: recoveryRequired, NextProbeAt: next, CreatedAt: created, UpdatedAt: updated,
	}
}

type capturedAccountFailure struct {
	Cooldown scheduling.CooldownSnapshot
	OldEvent string
}

func (l *accountPoolLease) captureAccountFailureTx(ctx context.Context, tx *sql.Tx, failure scheduling.FailureClass, eventID string, now, proposedUntil time.Time) (capturedAccountFailure, error) {
	var out capturedAccountFailure
	if l.recovery == nil {
		var isolated int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_recovery_states WHERE account_id=?`, l.inner.AccountID()).Scan(&isolated); err != nil {
			return out, err
		}
		if isolated != 0 {
			return out, errors.New("isolated account lease has no recovery snapshot")
		}
		return l.persistCooldownOnlyTx(ctx, tx, failure, eventID, now, proposedUntil)
	}
	current, code := loadCurrentRecoverySnapshotTx(ctx, tx, l.recovery.AccountID, l.recovery.PublicModel, l.recovery.Protocol)
	if code == accountPoolStorageUnavailable {
		return out, errors.New("validate recovery snapshot storage")
	}
	if code != "" || !sameRecoverySnapshot(current, *l.recovery) {
		// A failure from a credential or mapping which is no longer current must
		// not cool or isolate the replacement account revision.
		return out, nil
	}

	var oldEvent, oldFailure, oldUntilText string
	oldCooldown := true
	if err := tx.QueryRowContext(ctx, `SELECT event_id,failure_class,cooldown_until FROM account_pool_runtime_cooldowns WHERE account_id=?`, l.recovery.AccountID).Scan(&oldEvent, &oldFailure, &oldUntilText); errors.Is(err, sql.ErrNoRows) {
		oldCooldown = false
	} else if err != nil {
		return out, err
	}
	var oldUntil time.Time
	if oldCooldown {
		var err error
		oldUntil, err = parseTime(oldUntilText)
		if err != nil || !validCooldownFailure(scheduling.FailureClass(oldFailure)) {
			return out, errors.New("invalid stored cooldown")
		}
	}
	existing, stateExists := accountRecoveryState{}, true
	if oldCooldown {
		var err error
		existing, err = loadAccountRecoveryStateTx(ctx, tx, l.recovery.AccountID)
		if errors.Is(err, sql.ErrNoRows) {
			stateExists = false
		} else if err != nil {
			return out, err
		}
	} else {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_recovery_states WHERE account_id=?`, l.recovery.AccountID).Scan(&count); err != nil {
			return out, err
		}
		if count != 0 {
			return out, errors.New("recovery state is missing its cooldown")
		}
		stateExists = false
	}
	if stateExists && existing.CooldownEventID != oldEvent {
		return out, errors.New("recovery state cooldown conflict")
	}

	winningFailure, winningUntil := failure, proposedUntil
	newReasonWins := !oldCooldown || proposedUntil.After(oldUntil)
	if !newReasonWins {
		winningFailure, winningUntil = scheduling.FailureClass(oldFailure), oldUntil
	}
	operationID, err := newRecoveryOperationID()
	if err != nil {
		return out, err
	}
	var nextState *accountRecoveryState
	if stateExists {
		if existing.RecoveryRevision == math.MaxInt64 {
			return out, errAccountRecoveryConflict
		}
		snapshot := stateRecoverySnapshot(existing)
		if newReasonWins {
			snapshot = *l.recovery
		}
		stateUpdated := now
		if stateUpdated.Before(existing.UpdatedAt) {
			stateUpdated = existing.UpdatedAt
		}
		item := snapshotRecoveryState(snapshot, eventID, operationID, existing.RecoveryRevision+1, winningUntil, existing.CreatedAt, stateUpdated)
		nextState = &item
	} else if newReasonWins && l.runtime.app.cfg.AccountRecoveryEnabled {
		settings, err := loadAccountRecoverySettingsTx(ctx, tx)
		if err != nil {
			return out, err
		}
		if settings.Enabled {
			item := snapshotRecoveryState(*l.recovery, eventID, operationID, 1, winningUntil, now, now)
			nextState = &item
		}
	}

	_, err = tx.ExecContext(ctx, `INSERT INTO account_pool_runtime_cooldowns(account_id,event_id,failure_class,cooldown_until,updated_at)
		VALUES(?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET event_id=excluded.event_id,failure_class=excluded.failure_class,
		cooldown_until=excluded.cooldown_until,updated_at=excluded.updated_at`, l.recovery.AccountID, eventID,
		string(winningFailure), formatAccountPoolTime(winningUntil), formatAccountPoolTime(now))
	if err != nil {
		return out, err
	}
	if nextState != nil {
		if err := writeCapturedRecoveryStateTx(ctx, tx, *nextState, existing, stateExists); err != nil {
			return out, err
		}
	}
	out.Cooldown = scheduling.CooldownSnapshot{AccountID: l.recovery.AccountID, EventID: eventID, Failure: winningFailure, Until: winningUntil}
	out.OldEvent = oldEvent
	return out, nil
}

func (l *accountPoolLease) persistCooldownOnlyTx(ctx context.Context, tx *sql.Tx, failure scheduling.FailureClass, eventID string, now, proposedUntil time.Time) (capturedAccountFailure, error) {
	var oldEvent string
	if err := tx.QueryRowContext(ctx, `SELECT event_id FROM account_pool_runtime_cooldowns WHERE account_id=?`, l.inner.AccountID()).Scan(&oldEvent); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return capturedAccountFailure{}, err
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO account_pool_runtime_cooldowns(account_id,event_id,failure_class,cooldown_until,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(account_id) DO UPDATE SET
		event_id=excluded.event_id,
		failure_class=CASE WHEN excluded.cooldown_until>account_pool_runtime_cooldowns.cooldown_until THEN excluded.failure_class ELSE account_pool_runtime_cooldowns.failure_class END,
		cooldown_until=CASE WHEN excluded.cooldown_until>account_pool_runtime_cooldowns.cooldown_until THEN excluded.cooldown_until ELSE account_pool_runtime_cooldowns.cooldown_until END,
		updated_at=excluded.updated_at`, l.inner.AccountID(), eventID, string(failure), formatAccountPoolTime(proposedUntil), formatAccountPoolTime(now))
	if err != nil {
		return capturedAccountFailure{}, err
	}
	var stored capturedAccountFailure
	var storedFailure, storedUntil string
	stored.Cooldown.AccountID = l.inner.AccountID()
	if err := tx.QueryRowContext(ctx, `SELECT event_id,failure_class,cooldown_until FROM account_pool_runtime_cooldowns WHERE account_id=?`, l.inner.AccountID()).Scan(&stored.Cooldown.EventID, &storedFailure, &storedUntil); err != nil {
		return capturedAccountFailure{}, err
	}
	stored.Cooldown.Failure = scheduling.FailureClass(storedFailure)
	stored.Cooldown.Until, err = parseTime(storedUntil)
	if err != nil || !validCooldownFailure(stored.Cooldown.Failure) {
		return capturedAccountFailure{}, errors.New("invalid stored cooldown")
	}
	stored.OldEvent = oldEvent
	return stored, nil
}

func writeCapturedRecoveryStateTx(ctx context.Context, tx *sql.Tx, item, previous accountRecoveryState, exists bool) error {
	if tx == nil || !validRecoveryState(item) || item.State != recoveryRequired || item.AttentionCode != "" || !item.CheckedAt.IsZero() {
		return errors.New("invalid captured recovery state")
	}
	if !exists {
		result, err := tx.ExecContext(ctx, `INSERT INTO account_recovery_states(
			account_id,cooldown_event_id,operation_id,recovery_revision,pool_revision,account_revision,provider_kind,source_snapshot,client_id,
			public_model,upstream_model,protocol,state,attention_code,checked_at,next_probe_at,created_at,updated_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,NULL,?,?,?) ON CONFLICT(account_id) DO NOTHING`,
			item.AccountID, item.CooldownEventID, item.OperationID, item.RecoveryRevision, item.PoolRevision, item.AccountRevision,
			item.ProviderKind, item.SourceSnapshot, nullableRecoveryClient(item), item.PublicModel, item.UpstreamModel, item.Protocol,
			item.State, formatAccountPoolTime(item.NextProbeAt), formatAccountPoolTime(item.CreatedAt), formatAccountPoolTime(item.UpdatedAt))
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errAccountRecoveryConflict
		}
		return nil
	}
	if item.RecoveryRevision != previous.RecoveryRevision+1 || !item.CreatedAt.Equal(previous.CreatedAt) {
		return errAccountRecoveryConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE account_recovery_states SET
		cooldown_event_id=?,operation_id=?,recovery_revision=?,pool_revision=?,account_revision=?,provider_kind=?,source_snapshot=?,client_id=?,
		public_model=?,upstream_model=?,protocol=?,state=?,attention_code=NULL,checked_at=NULL,next_probe_at=?,updated_at=?
		WHERE account_id=? AND cooldown_event_id=? AND operation_id=? AND recovery_revision=?`,
		item.CooldownEventID, item.OperationID, item.RecoveryRevision, item.PoolRevision, item.AccountRevision, item.ProviderKind,
		item.SourceSnapshot, nullableRecoveryClient(item), item.PublicModel, item.UpstreamModel, item.Protocol, item.State,
		formatAccountPoolTime(item.NextProbeAt), formatAccountPoolTime(item.UpdatedAt), item.AccountID,
		previous.CooldownEventID, previous.OperationID, previous.RecoveryRevision)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return errAccountRecoveryConflict
	}
	return nil
}
