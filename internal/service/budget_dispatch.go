package service

// Independent integration of immutable accounting and budget receipts. All
// upstream IO remains outside these functions and outside SQLite transactions.
import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/governance"
)

type budgetAttemptState struct {
	start            accounting.AttemptStart
	reserve          governance.BudgetReserve
	reserveUncertain bool
	markUncertain    bool
	mode             governance.BudgetSettleMode
}

type budgetDispatchState struct {
	active  *activeUsageRequest
	request *usageLedgerRequest
	proof   governance.BudgetProof
	proved  bool
}

func (s *budgetDispatchState) unlock() { s.request.mu.Unlock(); s.active.mu.Unlock() }

// Take usage locks before opening the dispatch transaction. Terminal writers
// use usage -> SQLite too; reversing that order would deadlock a one-connection DB.
func (a *App) prepareBudgetDispatch(r *http.Request, selected route, lease *accountPoolLease, record bool, wire []*http.Request) (*budgetDispatchState, *modelAdmissionError) {
	guard, _ := r.Context().Value(governedRequestKey{}).(*governedRequest)
	if !record || guard == nil || !guard.budgetEnabled {
		return nil, nil
	}
	value, ok := a.usageRequests.Load(requestID(r.Context()))
	if !ok || a.budget == nil {
		return nil, budgetStorageFailure()
	}
	active := value.(*activeUsageRequest)
	active.mu.Lock()
	req := active.request
	req.mu.Lock()
	state := &budgetDispatchState{active: active, request: req}
	if active.attempt != nil || active.outcome != "" || active.finished || req.attempt != nil || req.finishSnapshot != nil {
		state.unlock()
		return nil, budgetStorageFailure()
	}
	if len(wire) == 1 {
		proof, err := proveModelBudgetWire(wire[0], req.protocol, req.provider, selected.UpstreamModel)
		if err == nil {
			poolRevision := int64(0)
			if lease != nil {
				poolRevision = lease.PoolRevision()
			}
			state.proof = governance.BudgetProof{Type: governance.BudgetProofMutuallyExclusiveInput, ActualModel: selected.UpstreamModel, AccountRevision: selected.Revision, PoolRevision: poolRevision, TransformRevision: proof.Transform, BounderID: proof.ProfileID, BounderRevision: proof.ProfileVersion, MutuallyExclusiveInput: &proof.UpperUsage}
			state.proved = true
		}
	}
	return state, nil
}

func budgetStorageFailure() *modelAdmissionError {
	return &modelAdmissionError{503, "budget_unavailable", "Request budget storage is temporarily unavailable."}
}

func budgetDecisionFailure(code governance.BudgetDecisionCode) *modelAdmissionError {
	switch code {
	case governance.BudgetDecisionTPMExceeded, governance.BudgetDecisionCostExceeded:
		return &modelAdmissionError{429, "budget_exceeded", "Request budget limit reached."}
	case governance.BudgetDecisionBoundUnavailable:
		return &modelAdmissionError{503, "budget_bound_unavailable", "This request has no supported budget bound or matching price."}
	case governance.BudgetDecisionProfileQuarantined:
		return &modelAdmissionError{503, "budget_profile_quarantined", "The budget profile is unavailable after an observed bound violation."}
	default:
		return budgetStorageFailure()
	}
}

func (a *App) commitBudgetTx(stage string, tx *sql.Tx) error {
	// The override is package-private fault injection, never user configuration.
	if a.budgetCommit != nil {
		return a.budgetCommit(stage, tx)
	}
	return tx.Commit()
}

// The caller holds lease, mutation, admission and usage locks continuously
// through validation, reservation, mark confirmation and in-memory lease phase.
func (a *App) commitBudgetDispatch(ctx context.Context, tx *sql.Tx, selected route, state *budgetDispatchState) *modelAdmissionError {
	req := state.request
	price, err := accounting.NewPriceCatalog(a.store.db).CurrentTx(ctx, tx, selected.AccountID, selected.UpstreamModel)
	if err != nil {
		return budgetStorageFailure()
	}
	dispatch := accounting.DispatchPrimary
	if state.active.failover {
		dispatch = accounting.DispatchFailover
	}
	at := time.Now().UTC()
	start := accounting.AttemptStart{ID: req.id + ":1", RequestID: req.id, AccountID: selected.AccountID, Provider: req.provider, Dispatch: dispatch, StartedAt: at, Price: price, Protocol: req.protocol, EffectiveModel: selected.UpstreamModel, Evidence: req.evidence}
	reserve := governance.BudgetReserve{RequestID: req.id, AttemptID: start.ID, Proof: state.proof, ObservedAt: at}
	if err := req.coordinator.ledger.BeginAttemptTx(ctx, tx, start); err != nil {
		return budgetStorageFailure()
	}
	result, err := a.budget.ReserveTx(ctx, tx, reserve)
	if err != nil {
		return budgetStorageFailure()
	}
	if !result.Allowed {
		return budgetDecisionFailure(result.Code)
	}
	if !result.Enforced || !state.proved {
		return budgetStorageFailure()
	}
	if _, err := a.budget.MarkMayHaveSentTx(ctx, tx, governance.BudgetMutation{AttemptID: start.ID, ObservedAt: at}); err != nil {
		return budgetStorageFailure()
	}
	if err := req.coordinator.ledger.MarkAttemptDispatchedTx(ctx, tx, accounting.AttemptDispatch{ID: start.ID, OperationID: start.ID + ":dispatch", DispatchedAt: at}); err != nil {
		return budgetStorageFailure()
	}
	attempt := &usageLedgerAttempt{request: req, id: start.ID, accountID: start.AccountID, dispatch: dispatch, startedAt: at, usage: accounting.NewGPT41SnapshotUsageAccumulator(), budget: &budgetAttemptState{start: start, reserve: reserve, mode: governance.BudgetSettleReleaseNotStarted}}
	// Publish the exact original identity before Commit, because a returned
	// error alone cannot tell whether the reservation was durably written.
	req.attempt = attempt
	state.active.attempt = attempt
	if err := a.commitBudgetTx("reserve", tx); err != nil {
		_ = tx.Rollback()
		attempt.budget.reserveUncertain = true
		attempt.budget.markUncertain = true
		return budgetStorageFailure()
	}
	attempt.budget.mode = governance.BudgetSettleFromAttempt
	return nil
}

// Resolve only the original reservation identity. A confirmed absent receipt
// permits finishing without an attempt. Failure to read leaves it pending for
// a bounded cleanup retry or startup recovery; it can never regain send rights.
func (r *usageLedgerRequest) reconcileBudgetStart(attempt *usageLedgerAttempt) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), usageLedgerShutdownTimeout)
	defer cancel()
	tx, err := r.coordinator.db.BeginTx(ctx, nil)
	if err != nil {
		return false, errUsageLedgerUnavailable
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounting_attempts WHERE id=?`, attempt.id).Scan(&count); err != nil {
		return false, errUsageLedgerUnavailable
	}
	if count == 0 {
		if _, err := r.coordinator.budget.GetTx(ctx, tx, attempt.id); !errors.Is(err, governance.ErrNotFound) {
			return false, errUsageLedgerUnavailable
		}
		return false, nil
	}
	if err := r.coordinator.ledger.BeginAttemptTx(ctx, tx, attempt.budget.start); err != nil {
		return false, errUsageLedgerUnavailable
	}
	if existing, err := r.coordinator.budget.GetTx(ctx, tx, attempt.id); err == nil && existing.Lifecycle == governance.BudgetMayHaveSent {
		attempt.budget.reserveUncertain = false
		attempt.budget.markUncertain = true
		return true, nil
	}
	result, err := r.coordinator.budget.ReserveTx(ctx, tx, attempt.budget.reserve)
	if err != nil || !result.Allowed || !result.Enforced {
		return false, errUsageLedgerUnavailable
	}
	attempt.budget.reserveUncertain = false
	return true, nil
}
