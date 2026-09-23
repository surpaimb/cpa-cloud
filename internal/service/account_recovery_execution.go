package service

// Independently authored transaction bridge for bounded recovery probes.
import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/membership"
)

const recoveryExecutionTimeout = 10 * time.Second
const recoveryRetryDelay = 5 * time.Minute

// A failed metadata commit retains the immutable settlement separately from
// the network operation. Retrying this receipt can never call the provider.
type recoveryExecutionReceipt struct {
	app      *App
	lease    *accountMaintenanceLease
	result   generationProbeResult
	finished time.Time
}

func (receipt *recoveryExecutionReceipt) Finalize(ctx context.Context) accountPoolRuntimeCode {
	if receipt == nil {
		return accountPoolInvalid
	}
	code := receipt.app.finalizeRecoveryOperationAt(ctx, receipt.lease, receipt.result, receipt.finished)
	if code == accountPoolStorageUnavailable {
		return receipt.lease.ResolveSettlement(ctx, receipt.proveCommitted)
	}
	return code
}

// executeRecoveryOperation only consumes an already-authorized persisted
// recovery snapshot. It cannot select another account or invent a model route.
// A coordinator creates that snapshot from an attributable real failure.
func (a *App) executeRecoveryOperation(ctx context.Context, request accountMaintenanceAcquireRequest) (accountPoolRuntimeCode, *recoveryExecutionReceipt) {
	if a == nil || a.accountPool == nil || a.systemProbes == nil || ctx == nil {
		return accountPoolInvalid, nil
	}
	// Runtime shutdown owns the entire operation, including metadata settlement,
	// rather than only the lease heartbeat. Store.Close must wait for this work.
	if !a.accountPool.begin() {
		return accountPoolClosed, nil
	}
	defer a.accountPool.wg.Done()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopShutdown := context.AfterFunc(a.accountPool.ctx, cancel)
	defer stopShutdown()
	var credential *membership.CodexAuthCredential
	// The shared credential getter may take the account mutation lock and
	// refresh. Do this before capacity admission, outside all runtime locks.
	prepareCtx, prepareCancel := context.WithTimeout(runCtx, codexOAuthHTTPTimeout+10*time.Second)
	defer prepareCancel()
	permissionTx, err := a.store.db.BeginTx(prepareCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return accountPoolStorageUnavailable, nil
	}
	permissionErr := a.recoveryDispatchAllowedTx(prepareCtx, permissionTx)
	_ = permissionTx.Rollback()
	if permissionErr != nil {
		return accountPoolClosed, nil
	}
	state, selected, _, code := a.accountPool.loadMaintenanceCandidate(prepareCtx, request)
	if code != "" {
		return code, nil
	}
	if state.NextProbeAt.After(a.accountPool.clock.Now()) {
		return accountPoolCapacityUnavailable, nil
	}
	if selected.ProviderKind == codexMembershipProvider {
		_, imported, adopted, failure := a.acquireCodexRecoveryCredential(prepareCtx, selected, state)
		if imported != nil {
			defer imported.Destroy()
		}
		if failure != nil {
			return accountPoolAccountChanged, nil
		}
		state = adopted
		request.ExpectedAccountRevision = state.AccountRevision
		request.ExpectedRecoveryRevision = state.RecoveryRevision
		credential = imported
	}
	prepareCancel()
	request.CapacityWait = 100 * time.Millisecond
	acquired := a.accountPool.AcquireMaintenance(runCtx, request, func(ctx context.Context, tx *sql.Tx, snapshot accountRecoveryState) error {
		if err := a.recoveryDispatchAllowedTx(ctx, tx); err != nil {
			return err
		}
		counts, err := a.systemProbes.CountsTx(ctx, tx, snapshot.CooldownEventID)
		if err != nil {
			return err
		}
		if counts.Event >= 3 || counts.Total >= accounting.MaxSystemProbeHistory {
			return accounting.ErrSystemProbeHistoryFull
		}
		// Even a pending record may represent a lost response from a prior
		// execution. Operation IDs are never reused to dispatch another probe.
		if _, err := a.systemProbes.GetTx(ctx, tx, snapshot.OperationID); !errors.Is(err, accounting.ErrNotFound) {
			if err != nil {
				return err
			}
			return accounting.ErrConflict
		}
		provider, err := usageProvider(snapshot.ProviderKind, accounting.UsageProtocol(snapshot.Protocol))
		if err != nil {
			return err
		}
		now := a.accountPool.clock.Now().UTC()
		_, err = a.systemProbes.BeginTx(ctx, tx, accounting.SystemProbeStart{
			OperationID: snapshot.OperationID, RecoveryEventID: snapshot.CooldownEventID,
			AccountID: snapshot.AccountID, AccountRevision: snapshot.AccountRevision, PoolRevision: snapshot.PoolRevision,
			PublicModel: snapshot.PublicModel, UpstreamModel: snapshot.UpstreamModel,
			Provider: provider, Protocol: accounting.UsageProtocol(snapshot.Protocol), StartedAt: now,
		})
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE account_recovery_states SET state='in_progress',updated_at=?
			WHERE account_id=? AND operation_id=? AND recovery_revision=? AND state='required'`,
			formatAccountPoolTime(now), snapshot.AccountID, snapshot.OperationID, snapshot.RecoveryRevision)
		return recoveryChangedExactlyOne(result, err)
	})
	if acquired.Code != accountPoolAcquired {
		return acquired.Code, nil
	}
	lease := acquired.Lease
	executeCtx, executeCancel := context.WithTimeout(lease.Context(), recoveryExecutionTimeout)
	defer executeCancel()
	result := generationProbeResult{Status: accounting.StatusFailed, Code: "authentication_failed"}
	input := generationProbeInput{Selected: acquired.Route, Protocol: accounting.UsageProtocol(acquired.State.Protocol), CodexCredential: credential}
	var key string
	var secretErr error
	switch acquired.Route.ProviderKind {
	case "openai-compatible", anthropicAPIKeyProvider:
		if acquired.Route.KeyVersion != 1 {
			secretErr = errSystemProbeStorage
		} else {
			key, secretErr = a.secrets.decryptCredential(acquired.Route.AccountID, acquired.Route.Ciphertext)
		}
	case geminiAPIKeyProvider:
		if acquired.Route.KeyVersion != 2 {
			secretErr = errSystemProbeStorage
		} else {
			key, secretErr = a.secrets.decryptGeminiAPIKey(acquired.Route.AccountID, acquired.Route.Ciphertext)
		}
	case codexMembershipProvider:
		if credential == nil {
			secretErr = errSystemProbeStorage
		}
	default:
		secretErr = errSystemProbeStorage
		result.Code = "unsupported"
	}
	input.APIKey = []byte(key)
	key = ""
	defer clear(input.APIKey)
	if secretErr == nil {
		code = lease.MarkDispatch(executeCtx, func(ctx context.Context, tx *sql.Tx) error {
			if err := a.recoveryDispatchAllowedTx(ctx, tx); err != nil {
				return err
			}
			_, err := a.systemProbes.MarkMayHaveSentTx(ctx, tx, acquired.State.OperationID, a.accountPool.clock.Now().UTC())
			return err
		})
		if code == accountPoolAcquired {
			result = a.runGenerationProbe(executeCtx, input)
		} else {
			result.Code = "configuration_changed"
		}
	}
	if errors.Is(executeCtx.Err(), context.Canceled) {
		result.Status, result.Code = accounting.StatusCancelled, "cancelled"
	} else if errors.Is(executeCtx.Err(), context.DeadlineExceeded) {
		result.Status, result.Code = accounting.StatusFailed, "upstream_timeout"
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer finishCancel()
	receipt := &recoveryExecutionReceipt{app: a, lease: lease, result: result, finished: a.accountPool.clock.Now().UTC()}
	return receipt.Finalize(finishCtx), receipt
}

func (a *App) recoveryDispatchAllowedTx(ctx context.Context, tx *sql.Tx) error {
	if !a.cfg.AccountRecoveryEnabled || ctx.Err() != nil {
		return accounting.ErrConflict
	}
	setting, err := loadAccountRecoverySettingsTx(ctx, tx)
	if err != nil {
		return err
	}
	if !setting.Enabled {
		return accounting.ErrConflict
	}
	return nil
}

// The absence of a lease alone is not evidence of settlement. The complete
// immutable attempt and its atomic isolation outcome must also match.
func (receipt *recoveryExecutionReceipt) proveCommitted(ctx context.Context, tx *sql.Tx) error {
	snapshot := receipt.lease.Snapshot()
	attempt, err := receipt.app.systemProbes.GetTx(ctx, tx, snapshot.OperationID)
	if err != nil {
		return err
	}
	provider, err := usageProvider(snapshot.ProviderKind, accounting.UsageProtocol(snapshot.Protocol))
	finished := recoveryFinishTime(attempt, snapshot, receipt.finished)
	if err != nil || attempt.OperationID != snapshot.OperationID || attempt.RecoveryEventID != snapshot.CooldownEventID ||
		attempt.AccountID != snapshot.AccountID || attempt.AccountRevision != snapshot.AccountRevision || attempt.PoolRevision != snapshot.PoolRevision ||
		attempt.PublicModel != snapshot.PublicModel || attempt.UpstreamModel != snapshot.UpstreamModel || attempt.Protocol != accounting.UsageProtocol(snapshot.Protocol) || attempt.Provider != provider ||
		attempt.FinishedAt == nil || !attempt.FinishedAt.Equal(finished) || attempt.Result == nil || !reflect.DeepEqual(attempt.Usage, receipt.result.Usage) {
		return accounting.ErrConflict
	}
	exact := attempt.Status == accounting.SystemProbeStatus(receipt.result.Status) && *attempt.Result == accounting.SystemProbeResult(receipt.result.Code)
	stale := attempt.Status == accounting.SystemProbeFailed && *attempt.Result == accounting.SystemProbeConfigurationChanged
	if !exact && !stale {
		return accounting.ErrConflict
	}
	state, stateErr := loadAccountRecoveryStateTx(ctx, tx, snapshot.AccountID)
	var event string
	cooldownErr := tx.QueryRowContext(ctx, `SELECT event_id FROM account_pool_runtime_cooldowns WHERE account_id=?`, snapshot.AccountID).Scan(&event)
	if stateErr != nil && !errors.Is(stateErr, sql.ErrNoRows) || cooldownErr != nil && !errors.Is(cooldownErr, sql.ErrNoRows) {
		return accounting.ErrConflict
	}
	if errors.Is(stateErr, sql.ErrNoRows) {
		if event == snapshot.CooldownEventID {
			return accounting.ErrConflict
		}
		return nil
	}
	if err := validatePersistedRecoveryAnchorTx(ctx, tx, state); err != nil {
		return err
	}
	if state.OperationID != snapshot.OperationID {
		// A separately established isolation must remain untouched.
		return nil
	}
	if attempt.Status == accounting.SystemProbeSucceeded || state.CooldownEventID != snapshot.CooldownEventID || state.RecoveryRevision != snapshot.RecoveryRevision || state.State != recoveryInterrupted || state.NextProbeAt.Before(finished.Add(recoveryRetryDelay)) {
		return accounting.ErrConflict
	}
	return nil
}

func (a *App) finalizeRecoveryOperationAt(ctx context.Context, lease *accountMaintenanceLease, result generationProbeResult, finished time.Time) accountPoolRuntimeCode {
	snapshot := lease.Snapshot()
	return lease.Finalize(ctx, func(ctx context.Context, tx *sql.Tx, current bool) error {
		attempt, err := a.systemProbes.GetTx(ctx, tx, snapshot.OperationID)
		if err != nil {
			return err
		}
		finished := recoveryFinishTime(attempt, snapshot, finished)
		status, resultCode := result.Status, result.Code
		if !current {
			status, resultCode = accounting.StatusFailed, "configuration_changed"
		}
		_, err = a.systemProbes.FinishTx(ctx, tx, accounting.SystemProbeFinish{
			OperationID: snapshot.OperationID, Status: accounting.SystemProbeStatus(status),
			Result: accounting.SystemProbeResult(resultCode), FinishedAt: finished, Usage: result.Usage,
		})
		if err != nil {
			return err
		}
		if !current {
			// A stale attempt may close its own orphaned in-progress marker,
			// but never a newer event/operation. Isolation remains in place.
			_, err := tx.ExecContext(ctx, `UPDATE account_recovery_states SET state='interrupted',next_probe_at=?,updated_at=?
				WHERE account_id=? AND cooldown_event_id=? AND operation_id=? AND recovery_revision=? AND state='in_progress'`,
				formatAccountPoolTime(finished.Add(recoveryRetryDelay)), formatAccountPoolTime(finished),
				snapshot.AccountID, snapshot.CooldownEventID, snapshot.OperationID, snapshot.RecoveryRevision)
			return err
		}
		if status == accounting.StatusSucceeded && resultCode == "generation_ok" {
			changed, err := tx.ExecContext(ctx, `DELETE FROM account_pool_runtime_cooldowns WHERE account_id=? AND event_id=?`, snapshot.AccountID, snapshot.CooldownEventID)
			if err := recoveryChangedExactlyOne(changed, err); err != nil {
				return err
			}
			changed, err = tx.ExecContext(ctx, `DELETE FROM account_recovery_states WHERE account_id=? AND operation_id=? AND recovery_revision=?`, snapshot.AccountID, snapshot.OperationID, snapshot.RecoveryRevision)
			return recoveryChangedExactlyOne(changed, err)
		}
		// The terminal attempt remains immutable. The future coordinator must
		// allocate a fresh operation after this durable delay, never replay it.
		changed, err := tx.ExecContext(ctx, `UPDATE account_recovery_states SET state='interrupted',next_probe_at=?,updated_at=? WHERE account_id=? AND operation_id=? AND recovery_revision=?`,
			formatAccountPoolTime(finished.Add(recoveryRetryDelay)), formatAccountPoolTime(finished), snapshot.AccountID, snapshot.OperationID, snapshot.RecoveryRevision)
		return recoveryChangedExactlyOne(changed, err)
	})
}

// A wall-clock rollback must not create an impossible ledger interval. The
// lower bounds are immutable for this operation, so receipt retries compute
// exactly the same completion time without changing or repeating the request.
func recoveryFinishTime(attempt accounting.SystemProbeAttempt, snapshot accountRecoveryState, observed time.Time) time.Time {
	finished := observed.UTC()
	for _, lower := range []time.Time{attempt.StartedAt, snapshot.UpdatedAt} {
		if lower.After(finished) {
			finished = lower
		}
	}
	if attempt.MayHaveSentAt != nil && attempt.MayHaveSentAt.After(finished) {
		finished = *attempt.MayHaveSentAt
	}
	return finished
}

func recoveryChangedExactlyOne(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return accounting.ErrConflict
	}
	return nil
}
