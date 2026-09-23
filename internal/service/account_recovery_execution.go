package service

// Independently authored transaction bridge for recovery probes. No public
// handler or background worker calls this yet: automatic recovery remains off.
import (
	"context"
	"database/sql"
	"errors"
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
	return receipt.app.finalizeRecoveryOperationAt(ctx, receipt.lease, receipt.result, receipt.finished)
}

// executeRecoveryOperation only consumes an already-authorized persisted
// recovery snapshot. It cannot select another account or invent a model route.
// A future coordinator must create that snapshot from a real failure path.
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
	runCtx, cancel := context.WithTimeout(ctx, recoveryExecutionTimeout)
	defer cancel()
	stopShutdown := context.AfterFunc(a.accountPool.ctx, cancel)
	defer stopShutdown()
	var credential *membership.CodexAuthCredential
	// The shared credential getter may take the account mutation lock and
	// refresh. Do this before capacity admission, outside all runtime locks.
	state, selected, _, code := a.accountPool.loadMaintenanceCandidate(runCtx, request)
	if code != "" {
		return code, nil
	}
	if state.NextProbeAt.After(a.accountPool.clock.Now()) {
		return accountPoolCapacityUnavailable, nil
	}
	if selected.ProviderKind == codexMembershipProvider {
		updated, imported, failure := a.acquireCodexCredential(runCtx, selected)
		if imported != nil {
			defer imported.Destroy()
		}
		if failure != nil {
			return accountPoolAccountChanged, nil
		}
		// The current shared getter can also observe someone else's reimport.
		// Never infer that an arbitrary revision advance was our own refresh.
		// Adoption requires separate refresh-transition evidence in the later
		// coordinator; until then a changed revision keeps the account isolated.
		if updated.Revision != state.AccountRevision {
			return accountPoolAccountChanged, nil
		}
		credential = imported
	}
	acquired := a.accountPool.AcquireMaintenance(runCtx, request, func(ctx context.Context, tx *sql.Tx, snapshot accountRecoveryState) error {
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
		code = lease.MarkDispatch(runCtx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := a.systemProbes.MarkMayHaveSentTx(ctx, tx, acquired.State.OperationID, a.accountPool.clock.Now().UTC())
			return err
		})
		if code == accountPoolAcquired {
			result = a.runGenerationProbe(lease.Context(), input)
		} else {
			result.Code = "configuration_changed"
		}
	}
	if errors.Is(runCtx.Err(), context.Canceled) {
		result.Status, result.Code = accounting.StatusCancelled, "cancelled"
	} else if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		result.Status, result.Code = accounting.StatusFailed, "upstream_timeout"
	}
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer finishCancel()
	receipt := &recoveryExecutionReceipt{app: a, lease: lease, result: result, finished: a.accountPool.clock.Now().UTC()}
	return receipt.Finalize(finishCtx), receipt
}

func (a *App) finalizeRecoveryOperationAt(ctx context.Context, lease *accountMaintenanceLease, result generationProbeResult, finished time.Time) accountPoolRuntimeCode {
	snapshot := lease.Snapshot()
	return lease.Finalize(ctx, func(ctx context.Context, tx *sql.Tx, current bool) error {
		status, resultCode := result.Status, result.Code
		if !current {
			status, resultCode = accounting.StatusFailed, "configuration_changed"
		}
		_, err := a.systemProbes.FinishTx(ctx, tx, accounting.SystemProbeFinish{
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
