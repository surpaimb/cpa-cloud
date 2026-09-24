package service

// Independent integration of CPA Cloud's metadata-only usage contract.
import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/governance"
)

type activeUsageRequest struct {
	mu       sync.Mutex
	request  *usageLedgerRequest
	failover bool
	attempt  *usageLedgerAttempt
	outcome  string
	status   int
	endedAt  time.Time
	finished bool
}

type usageEvidenceContextKey struct{}

func withUsageStreamEvidence(r *http.Request, stream bool) *http.Request {
	evidence := accounting.EvidenceProviderResponse
	if stream {
		evidence = accounting.EvidenceProviderStream
	}
	return r.WithContext(context.WithValue(r.Context(), usageEvidenceContextKey{}, evidence))
}

func usageEvidenceForRequest(r *http.Request) accounting.UsageEvidence {
	if evidence, ok := r.Context().Value(usageEvidenceContextKey{}).(accounting.UsageEvidence); ok {
		return evidence
	}
	return accounting.EvidenceProviderResponse
}

func (a *App) beginRequestUsage(r *http.Request, auth employeeAuth, model string, selected route) error {
	var protocol accounting.UsageProtocol
	switch {
	case r.URL.Path == "/v1/chat/completions":
		protocol = accounting.ProtocolOpenAIChatCompletions
	case r.URL.Path == "/v1/responses":
		protocol = accounting.ProtocolOpenAIResponses
	case r.URL.Path == "/v1/messages":
		protocol = accounting.ProtocolAnthropicMessages
	case strings.HasPrefix(r.URL.Path, "/v1beta/models/"):
		protocol = accounting.ProtocolGeminiGenerateContent
	default:
		return errUsageLedgerInvalid
	}
	startedAt := time.Now().UTC()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO model_requests(id,employee_id,key_id,model_id,started_at,outcome) VALUES(?,?,?,?,?,'running')`, requestID(r.Context()), auth.EmployeeID, auth.KeyID, model, startedAt.Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if a.usage == nil {
		return tx.Commit() // Isolated legacy fixtures have no usage coordinator.
	}
	guard, governed := r.Context().Value(governedRequestKey{}).(*governedRequest)
	request, err := a.usage.beginRequestTx(r.Context(), tx, usageRequestStart{
		RequestID: requestID(r.Context()), EmployeeID: auth.EmployeeID, KeyID: auth.KeyID,
		PublicModel: model, ProviderKind: selected.ProviderKind, Protocol: protocol, Evidence: usageEvidenceForRequest(r), StartedAt: startedAt,
		Governed: governed, guard: guard,
	})
	if err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	a.usageRequests.Store(requestID(r.Context()), &activeUsageRequest{request: request})
	return nil
}

func (a *App) markRequestUsageFailover(id string) error {
	value, ok := a.usageRequests.Load(id)
	if !ok {
		return nil // count_tokens does not create generation accounting records.
	}
	active := value.(*activeUsageRequest)
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.attempt != nil || active.outcome != "" || active.finished {
		return errUsageLedgerConflict
	}
	active.failover = true
	return nil
}

func (a *App) beginRouteUpstreamUsage(ctx context.Context, id string, selected route) error {
	value, ok := a.usageRequests.Load(id)
	if !ok {
		return nil
	} // count_tokens and isolated forwarder unit fixtures
	active := value.(*activeUsageRequest)
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.attempt != nil || active.outcome != "" {
		return errUsageLedgerConflict
	}
	tx, err := active.request.coordinator.db.BeginTx(ctx, nil)
	if err != nil {
		return errUsageLedgerUnavailable
	}
	defer tx.Rollback()
	var price *accounting.PriceSnapshot
	if lookup := active.request.coordinator.priceLookup; lookup != nil {
		price, err = lookup(ctx, selected.AccountID, selected.UpstreamModel) // package-private fault injection only
	} else if lookupTx := active.request.coordinator.priceLookupTx; lookupTx != nil {
		price, err = lookupTx(ctx, tx, selected.AccountID, selected.UpstreamModel)
	} else {
		err = errUsageLedgerUnavailable
	}
	if err != nil {
		return errUsageLedgerUnavailable
	}
	dispatch := accounting.DispatchPrimary
	if active.failover {
		dispatch = accounting.DispatchFailover
	}
	attempt, err := active.request.beginDispatchedAttemptTx(ctx, tx, selected.AccountID, selected.UpstreamModel, time.Now().UTC(), price, dispatch)
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		active.attempt = attempt
	}
	return err
}

type usageDispatchState struct {
	active  *activeUsageRequest
	request *usageLedgerRequest
}

func (s *usageDispatchState) unlock() { s.request.mu.Unlock(); s.active.mu.Unlock() }

func (a *App) prepareUsageDispatch(id string, record bool) (*usageDispatchState, *modelAdmissionError) {
	if !record {
		return nil, nil
	}
	value, ok := a.usageRequests.Load(id)
	if !ok {
		return nil, nil // Isolated forwarding fixtures do not own usage state.
	}
	active := value.(*activeUsageRequest)
	active.mu.Lock()
	request := active.request
	request.mu.Lock()
	state := &usageDispatchState{active: active, request: request}
	if active.attempt != nil || active.outcome != "" || active.finished || request.attempt != nil || request.finishSnapshot != nil {
		state.unlock()
		return nil, poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	return state, nil
}

func (a *App) commitUsageDispatch(ctx context.Context, tx *sql.Tx, selected route, state *usageDispatchState) *modelAdmissionError {
	request := state.request
	var price *accounting.PriceSnapshot
	var err error
	if lookup := request.coordinator.priceLookup; lookup != nil {
		price, err = lookup(ctx, selected.AccountID, selected.UpstreamModel)
	} else if lookupTx := request.coordinator.priceLookupTx; lookupTx != nil {
		price, err = lookupTx(ctx, tx, selected.AccountID, selected.UpstreamModel)
	} else {
		err = errUsageLedgerUnavailable
	}
	if err != nil {
		return poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	dispatch := accounting.DispatchPrimary
	if state.active.failover {
		dispatch = accounting.DispatchFailover
	}
	at := time.Now().UTC()
	accumulator, err := accounting.NewUsageAccumulator(request.protocol)
	if err != nil {
		return poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	attempt := &usageLedgerAttempt{request: request, id: request.id + ":1", accountID: selected.AccountID, dispatch: dispatch, startedAt: at, usage: accumulator}
	start := accounting.AttemptStart{ID: attempt.id, RequestID: request.id, AccountID: selected.AccountID, Provider: request.provider, Dispatch: dispatch, StartedAt: at, Price: price, Protocol: request.protocol, EffectiveModel: selected.UpstreamModel, Evidence: request.evidence}
	if err := request.coordinator.ledger.BeginAttemptTx(ctx, tx, start); err != nil {
		return poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	if err := request.coordinator.ledger.MarkAttemptDispatchedTx(ctx, tx, accounting.AttemptDispatch{ID: attempt.id, OperationID: attempt.id + ":dispatch", DispatchedAt: at}); err != nil {
		return poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	if hook := dispatchBarrierHookFromContext(ctx); hook != nil {
		if err := hook(ctx, tx, attempt.id, at); err != nil {
			return poolAdmissionFailure(accountPoolStorageUnavailable)
		}
	}
	// Publish the immutable identity before Commit because an error response
	// cannot prove whether SQLite durably accepted the transaction.
	request.attempt, state.active.attempt = attempt, attempt
	commit := request.coordinator.dispatchCommit
	if commit == nil {
		commit = func(tx *sql.Tx) error { return tx.Commit() }
	}
	if err := commit(tx); err != nil {
		_ = tx.Rollback()
		attempt.dispatchUncertain = true
		return poolAdmissionFailure(accountPoolStorageUnavailable)
	}
	return nil
}

type dispatchBarrierContextKey struct{}
type dispatchBarrierHook func(context.Context, *sql.Tx, string, time.Time) error

func withDispatchBarrierHook(r *http.Request, hook dispatchBarrierHook) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), dispatchBarrierContextKey{}, hook))
}

func dispatchBarrierHookFromContext(ctx context.Context) dispatchBarrierHook {
	hook, _ := ctx.Value(dispatchBarrierContextKey{}).(dispatchBarrierHook)
	return hook
}

// Invalid usage poisons only the accumulator, preserving unknown counters.
// This hook never retains the passed response/event or prints parser errors.
func (a *App) observeRequestUsage(id string, data []byte) {
	value, ok := a.usageRequests.Load(id)
	if !ok {
		return
	}
	active := value.(*activeUsageRequest)
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.attempt != nil && !active.finished {
		_ = active.attempt.observe(data)
	}
}

func (a *App) observeSSEUsage(id string, frame []byte) {
	var data bytes.Buffer
	for _, line := range bytes.Split(frame, []byte{'\n'}) {
		line = bytes.TrimSuffix(line, []byte{'\r'})
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		value := bytes.TrimPrefix(line[5:], []byte{' '})
		if data.Len() > 0 {
			data.WriteByte('\n')
		}
		data.Write(value)
	}
	if data.Len() > 0 && !bytes.Equal(bytes.TrimSpace(data.Bytes()), []byte("[DONE]")) {
		a.observeRequestUsage(id, data.Bytes())
	}
}

func (a *App) observeCodexChatUsage(id, responseID string, usage any) {
	payload := map[string]any{"usage": usage}
	if responseID != "" {
		payload["id"] = responseID
	}
	data, err := json.Marshal(payload)
	if err == nil {
		a.observeRequestUsage(id, data)
	}
}

func (a *App) finishRequestUsage(ctx context.Context, id, outcome string, status int) error {
	value, ok := a.usageRequests.Load(id)
	if !ok {
		return nil
	}
	active := value.(*activeUsageRequest)
	active.mu.Lock()
	defer active.mu.Unlock()
	if active.outcome != "" && active.outcome != outcome {
		return errUsageLedgerConflict
	}
	if active.outcome == "" {
		terminalStatus := accounting.Status(outcome)
		if !validUsageTerminalStatus(terminalStatus) || terminalStatus == accounting.StatusSucceeded && active.attempt == nil {
			return errUsageLedgerInvalid
		}
		endedAt := time.Now().UTC()
		if active.request.guard != nil {
			if err := active.request.guard.claimTerminal(terminalStatus); err != nil {
				return err
			}
		}
		active.outcome, active.status, active.endedAt = outcome, status, endedAt
	}
	if active.finished {
		return nil
	}
	err := active.request.finishWithModelRequest(ctx, accounting.Status(outcome), active.endedAt, active.status)
	if err == nil {
		active.finished = true
	}
	return err
}

func (a *App) cleanupRequestUsage(id string) {
	value, ok := a.usageRequests.Load(id)
	if !ok {
		return
	}
	defer a.usageRequests.Delete(id)
	active := value.(*activeUsageRequest)
	active.mu.Lock()
	outcome := active.outcome
	status := active.status
	done := active.finished
	active.mu.Unlock()
	if done {
		return
	}
	if outcome == "" {
		outcome = "interrupted"
		if active.request.guard != nil && active.request.guard.cancellationWon() {
			outcome = "cancelled"
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = a.finishRequestUsage(ctx, id, outcome, status)
}

// One transaction commits the attempt, accounting request, legacy request,
// and any governance release. A rejected write leaves all pending for a bounded retry or
// restart recovery; a successful accounting write alone is never completion.
func (r *usageLedgerRequest) finishWithModelRequest(ctx context.Context, status accounting.Status, finishedAt time.Time, httpStatus int) error {
	if r == nil || r.coordinator == nil || r.coordinator.db == nil || ctx == nil {
		return errUsageLedgerInvalid
	}
	r.mu.Lock()
	attempt := r.attempt
	r.mu.Unlock()
	if attempt != nil {
		attempt.mu.Lock()
		defer attempt.mu.Unlock()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.attempt != attempt || !validUsageTerminalStatus(status) || finishedAt.IsZero() || finishedAt.Before(r.startedAt) || attempt == nil && status == accounting.StatusSucceeded {
		return errUsageLedgerInvalid
	}
	if attempt != nil && attempt.budget != nil && attempt.budget.reserveUncertain {
		persisted, err := r.reconcileBudgetStart(attempt)
		if err != nil {
			return err
		}
		if !persisted {
			if status == accounting.StatusSucceeded {
				return errUsageLedgerInvalid
			}
			r.attempt = nil
			attempt = nil
		}
	}
	if attempt != nil && attempt.budget == nil && attempt.dispatchUncertain {
		persisted, err := r.reconcileDispatchStart(attempt)
		if err != nil {
			return err
		}
		if !persisted {
			if status == accounting.StatusSucceeded {
				return errUsageLedgerInvalid
			}
			r.attempt = nil
			attempt = nil
		}
	}
	if r.finishSnapshot == nil {
		r.finishSnapshot = &usageFinishSnapshot{status: status, finishedAt: finishedAt}
	} else if r.finishSnapshot.status != status {
		return errUsageLedgerConflict
	}
	snapshot := r.finishSnapshot
	if attempt != nil {
		if snapshot.finishedAt.Before(attempt.startedAt) {
			return errUsageLedgerInvalid
		}
		if attempt.finishSnapshot == nil {
			attempt.finishSnapshot = &usageAttemptFinishSnapshot{usageFinishSnapshot: *snapshot, usage: attempt.usage.Usage()}
		} else if attempt.finishSnapshot.status != snapshot.status || !attempt.finishSnapshot.finishedAt.Equal(snapshot.finishedAt) {
			return errUsageLedgerConflict
		}
	}
	if r.guard != nil {
		if err := r.guard.claimTerminal(snapshot.status); err != nil {
			return err
		}
	}
	err := r.coordinator.persist(ctx, func(writeCtx context.Context) error {
		tx, err := r.coordinator.db.BeginTx(writeCtx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if attempt != nil {
			reasoning, responseID := attempt.usage.ReliableMetadata()
			if err := r.coordinator.ledger.FinishAttemptTx(writeCtx, tx, accounting.AttemptFinish{
				ID: attempt.id, Status: snapshot.status, FinishedAt: snapshot.finishedAt, Usage: attempt.finishSnapshot.usage,
				ReasoningTokens: reasoning, ResponseID: responseID, ReliableUsage: true,
			}); err != nil {
				return err
			}
			if attempt.budget != nil {
				if r.coordinator.budget == nil {
					return errUsageLedgerUnavailable
				}
				if attempt.budget.markUncertain {
					if _, err := r.coordinator.budget.InterruptTx(writeCtx, tx, governance.BudgetMutation{AttemptID: attempt.id, ObservedAt: snapshot.finishedAt}); err != nil {
						return err
					}
				} else {
					if _, err := r.coordinator.budget.SettleTx(writeCtx, tx, governance.BudgetSettle{AttemptID: attempt.id, ObservedAt: snapshot.finishedAt, Mode: attempt.budget.mode}); err != nil {
						return err
					}
				}
			}
		}
		if err := r.coordinator.ledger.FinishRequestTx(writeCtx, tx, accounting.RequestFinish{ID: r.id, Status: snapshot.status, FinishedAt: snapshot.finishedAt}); err != nil {
			return err
		}
		var upstream any
		if httpStatus != 0 {
			upstream = httpStatus
		}
		result, err := tx.ExecContext(writeCtx, `UPDATE model_requests SET outcome=?,finished_at=?,upstream_status=? WHERE id=? AND outcome='running'`, string(snapshot.status), snapshot.finishedAt.Format(time.RFC3339Nano), upstream, r.id)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed != 1 {
			var storedOutcome string
			var storedStatus sql.NullInt64
			var storedTime string
			if err := tx.QueryRowContext(writeCtx, `SELECT outcome,finished_at,upstream_status FROM model_requests WHERE id=?`, r.id).Scan(&storedOutcome, &storedTime, &storedStatus); err != nil {
				return err
			}
			if storedOutcome != string(snapshot.status) || storedTime != snapshot.finishedAt.Format(time.RFC3339Nano) || storedStatus.Valid != (httpStatus != 0) || storedStatus.Valid && storedStatus.Int64 != int64(httpStatus) {
				return accounting.ErrConflict
			}
		}
		if r.governance != nil {
			if err := r.governance.FinishTx(writeCtx, tx, governance.Finish{RequestID: r.id, Status: snapshot.status, FinishedAt: snapshot.finishedAt}); err != nil {
				return err
			}
		}
		if r.terminalTxHook != nil {
			if err := r.terminalTxHook(writeCtx, tx, snapshot.status, snapshot.finishedAt); err != nil {
				return err
			}
		}
		if attempt != nil && attempt.budget != nil && r.coordinator.budgetCommit != nil {
			return r.coordinator.budgetCommit(tx)
		}
		return tx.Commit()
	})
	if err != nil {
		return err
	}
	if r.guard != nil {
		return r.guard.terminalCommitted(snapshot.status)
	}
	return nil
}

func (r *usageLedgerRequest) reconcileDispatchStart(attempt *usageLedgerAttempt) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), usageLedgerShutdownTimeout)
	defer cancel()
	var attempts, dispatches int
	err := r.coordinator.db.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM accounting_attempts WHERE id=?),(SELECT COUNT(*) FROM accounting_attempt_dispatches WHERE attempt_id=?)`, attempt.id, attempt.id).Scan(&attempts, &dispatches)
	if err != nil || attempts != dispatches || attempts < 0 || attempts > 1 {
		return false, errUsageLedgerUnavailable
	}
	if attempts == 1 {
		attempt.dispatchUncertain = false
		return true, nil
	}
	return false, nil
}
