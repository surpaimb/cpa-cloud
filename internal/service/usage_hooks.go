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
	_, governed := r.Context().Value(governedRequestKey{}).(*governedRequest)
	request, err := a.usage.beginRequestTx(r.Context(), tx, usageRequestStart{
		RequestID: requestID(r.Context()), EmployeeID: auth.EmployeeID, KeyID: auth.KeyID,
		PublicModel: model, ProviderKind: selected.ProviderKind, Protocol: protocol, StartedAt: startedAt,
		Governed: governed,
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
	var price *accounting.PriceSnapshot
	if lookup := active.request.coordinator.priceLookup; lookup != nil {
		var err error
		price, err = lookup(ctx, selected.AccountID, selected.UpstreamModel)
		if err != nil {
			return errUsageLedgerUnavailable
		}
	}
	dispatch := accounting.DispatchPrimary
	if active.failover {
		dispatch = accounting.DispatchFailover
	}
	attempt, err := active.request.beginDispatchedAttempt(ctx, selected.AccountID, time.Now().UTC(), price, dispatch)
	if err == nil {
		active.attempt = attempt
	}
	return err
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

func (a *App) observeCodexChatUsage(id string, usage any) {
	data, err := json.Marshal(map[string]any{"usage": usage})
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
		active.outcome, active.status, active.endedAt = outcome, status, time.Now().UTC()
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
	return r.coordinator.persist(ctx, func(writeCtx context.Context) error {
		tx, err := r.coordinator.db.BeginTx(writeCtx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if attempt != nil {
			if err := r.coordinator.ledger.FinishAttemptTx(writeCtx, tx, accounting.AttemptFinish{
				ID: attempt.id, Status: snapshot.status, FinishedAt: snapshot.finishedAt, Usage: attempt.finishSnapshot.usage,
			}); err != nil {
				return err
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
		return tx.Commit()
	})
}
