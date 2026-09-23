package service

// Employee governance is independent of upstream account capacity. Its lease
// covers routing waits and execution; only caller transactions release it.
import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/governance"
)

type governancePolicyResolver interface {
	ResolveScopesTx(context.Context, *sql.Tx, string, string) (governance.Settings, []governance.ScopeSnapshot, error)
}

type requestGovernance struct {
	app        *App
	core       *governance.Coordinator
	policies   governancePolicyResolver
	ctx        context.Context
	cancel     context.CancelFunc
	mu         sync.Mutex
	closed     bool
	requests   map[*governedRequest]struct{}
	wg         sync.WaitGroup
	renewEvery time.Duration
}

type governedRequest struct {
	runtime       *requestGovernance
	id            string
	ctx           context.Context
	cancel        context.CancelFunc
	renewCtx      context.Context
	stopRenew     context.CancelFunc
	stopClient    func() bool
	stopShutdown  func() bool
	done          chan struct{}
	once          sync.Once
	expires       time.Time
	budgetEnabled bool // Immutable hint; ReserveTx rechecks the persisted snapshot.

	mu             sync.Mutex
	phase          governedRequestPhase
	terminalStatus accounting.Status
}

type governedRequestPhase uint8

const (
	governedRequestActive governedRequestPhase = iota
	governedRequestCancellationWon
	governedRequestTerminalizing
	governedRequestTerminal
)

func newRequestGovernance(a *App, core *governance.Coordinator, policies governancePolicyResolver) (*requestGovernance, error) {
	runtime, err := newRequestGovernanceRuntime(a, core, policies)
	if err != nil {
		return nil, err
	}
	if _, err := core.RecoverInterrupted(runtime.ctx, time.Now().UTC()); err != nil {
		runtime.cancel()
		return nil, err
	}
	return runtime, nil
}

// Production startup has already recovered every request ledger in one caller
// transaction. The wrapper above remains useful for isolated coordinator tests.
func newRequestGovernanceRuntime(a *App, core *governance.Coordinator, policies governancePolicyResolver) (*requestGovernance, error) {
	if a == nil || a.store == nil || core == nil || policies == nil {
		return nil, governance.ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &requestGovernance{
		app: a, core: core, policies: policies, ctx: ctx, cancel: cancel,
		requests: make(map[*governedRequest]struct{}), renewEvery: 20 * time.Second,
	}
	return runtime, nil
}

func (g *requestGovernance) Close() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.closed = true
	g.cancel()
	for request := range g.requests {
		request.cancelActive()
	}
	g.mu.Unlock()
	g.wg.Wait()
}

func governanceAdmissionFailure(err error) *modelAdmissionError {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &modelAdmissionError{499, "request_cancelled", "Request cancelled."}
	}
	return &modelAdmissionError{503, "governance_unavailable", "Request governance is temporarily unavailable."}
}

func (g *requestGovernance) Admit(r *http.Request, auth employeeAuth, model string, protocol accounting.UsageProtocol) (*http.Request, *governedRequest, *modelAdmissionError) {
	admissionCtx, cancelAdmission := context.WithCancel(r.Context())
	stopAdmission := context.AfterFunc(g.ctx, cancelAdmission)
	cleanupAdmission := func() { stopAdmission(); cancelAdmission() }
	if g.ctx.Err() != nil {
		cleanupAdmission()
		return r, nil, governanceAdmissionFailure(context.Canceled)
	}
	started := time.Now().UTC()
	budgetEnabled := false
	g.app.admission.RLock()
	lease, failed := func() (*governance.Lease, *modelAdmissionError) {
		tx, err := g.app.store.db.BeginTx(admissionCtx, nil)
		if err != nil {
			return nil, governanceAdmissionFailure(err)
		}
		defer tx.Rollback()
		if g.app.accountPool == nil {
			return nil, governanceAdmissionFailure(governance.ErrUnavailable)
		}
		authorized, available, allowed, err := g.app.accountPool.authorizationCurrent(admissionCtx, tx, model, auth)
		if err != nil {
			return nil, governanceAdmissionFailure(err)
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
		settings, scopes, err := g.policies.ResolveScopesTx(admissionCtx, tx, auth.EmployeeID, auth.KeyID)
		if err != nil {
			return nil, governanceAdmissionFailure(err)
		}
		if settings.Enabled && settings.BudgetEnabled {
			for _, scope := range scopes {
				if scope.UnknownMode == "deny_unknown" && (scope.HardTPM != nil || scope.HardCostMicro != nil) {
					budgetEnabled = true
				}
			}
		}
		lease, decision, err := g.core.AdmitTx(admissionCtx, tx, governance.AdmissionStart{
			RequestID: requestID(r.Context()), Subject: governance.Subject{EmployeeID: auth.EmployeeID, KeyID: auth.KeyID, PublicModel: model, Protocol: protocol},
			SettingsRevision: settings.Revision, SnapshotComplete: true, Scopes: scopes, StartedAt: started, ObservedAt: started,
		})
		if err != nil {
			return nil, governanceAdmissionFailure(err)
		}
		if !decision.Allowed {
			switch decision.Code {
			case governance.DecisionRPMExceeded, governance.DecisionConcurrencyExceeded:
				return nil, &modelAdmissionError{429, "request_limit_exceeded", "Request limit reached; try again later."}
			default:
				return nil, &modelAdmissionError{409, "governance_changed", "Request governance changed; submit a new request."}
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, governanceAdmissionFailure(err)
		}
		return lease, nil
	}()
	g.app.admission.RUnlock()
	if failed != nil {
		cleanupAdmission()
		return r, nil, failed
	}
	if lease == nil {
		cleanupAdmission()
		return r, nil, nil
	}
	cleanupAdmission()
	ctx, cancel := context.WithCancel(r.Context())
	renewCtx, stopRenew := context.WithCancel(ctx)
	guard := &governedRequest{
		runtime: g, id: requestID(r.Context()), ctx: ctx, cancel: cancel, renewCtx: renewCtx, stopRenew: stopRenew,
		done: make(chan struct{}), expires: lease.ExpiresAt, phase: governedRequestActive,
		budgetEnabled: budgetEnabled,
	}
	guard.stopClient = context.AfterFunc(r.Context(), guard.cancelActive)
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		guard.stopClient()
		guard.cancelActive()
		close(guard.done)
		guard.Close()
		return r, nil, governanceAdmissionFailure(context.Canceled)
	}
	guard.stopShutdown = context.AfterFunc(g.ctx, guard.cancelActive)
	g.requests[guard] = struct{}{}
	g.wg.Add(1)
	g.mu.Unlock()
	go guard.renew()
	return r.WithContext(context.WithValue(ctx, governedRequestKey{}, guard)), guard, nil
}

type governedRequestKey struct{}

func (r *governedRequest) renew() {
	defer r.runtime.wg.Done()
	defer close(r.done)
	ticker := time.NewTicker(r.runtime.renewEvery)
	defer ticker.Stop()
	for {
		select {
		case <-r.renewCtx.Done():
			return
		case <-ticker.C:
			if !r.renewOnce() {
				return
			}
		}
	}
}

func (r *governedRequest) renewOnce() bool {
	ctx, cancel := context.WithTimeout(r.renewCtx, 3*time.Second)
	err := func() error {
		tx, err := r.runtime.app.store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		observed := time.Now().UTC()
		lease, err := r.runtime.core.RenewTx(ctx, tx, governance.Renew{RequestID: r.id, ExpectedExpiresAt: r.expires, ObservedAt: observed})
		if err != nil {
			return err
		}
		if r.budgetEnabled {
			budget := r.runtime.app.budget
			if budget == nil {
				return governance.ErrUnavailable
			}
			_, err := budget.GetTx(ctx, tx, r.id+":1")
			if err == nil {
				if _, err := budget.RenewTx(ctx, tx, governance.BudgetRenew{AttemptID: r.id + ":1", ExpectedExpiresAt: r.expires, ObservedAt: observed}); err != nil {
					return err
				}
			} else if errors.Is(err, governance.ErrNotFound) {
				var attempts int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounting_attempts WHERE request_id=?`, r.id).Scan(&attempts); err != nil || attempts != 0 {
					return governance.ErrUnavailable
				}
			} else {
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		r.expires = lease.ExpiresAt
		return nil
	}()
	cancel()
	if err == nil {
		return true
	}
	// A terminal accounting transaction may have released this lease between
	// ticks. That must not cancel an already successful reply.
	readCtx, readCancel := context.WithTimeout(context.Background(), time.Second)
	var status string
	readErr := r.runtime.app.store.db.QueryRowContext(readCtx, `SELECT status FROM governance_requests WHERE id=?`, r.id).Scan(&status)
	readCancel()
	if readErr == nil && (status == string(accounting.StatusSucceeded) || status == string(accounting.StatusFailed) || status == string(accounting.StatusCancelled)) {
		return false
	}
	r.cancelActive()
	return false
}

// cancelActive serializes cancellation with terminal persistence. Cancellation
// may stop execution only while no terminal outcome has claimed the request.
func (r *governedRequest) cancelActive() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.phase == governedRequestActive {
		r.phase = governedRequestCancellationWon
		r.cancel()
		r.stopRenew()
	}
	r.mu.Unlock()
}

// claimTerminal is the request's terminal serialization point. It stops and
// joins renewal outside the phase lock so a renewal failure can never deadlock
// while publishing its result. A failed storage write leaves the same status
// claimed, allowing only an idempotent retry of the first settlement snapshot.
func (r *governedRequest) claimTerminal(status accounting.Status) error {
	if r == nil || !validUsageTerminalStatus(status) {
		return errUsageLedgerInvalid
	}
	r.runtime.mu.Lock()
	r.mu.Lock()
	if r.runtime.closed && r.phase == governedRequestActive {
		r.phase = governedRequestCancellationWon
		r.cancel()
		r.stopRenew()
	}
	switch r.phase {
	case governedRequestActive:
		if r.ctx.Err() != nil && status != accounting.StatusCancelled {
			r.phase = governedRequestCancellationWon
			r.cancel()
			r.stopRenew()
			r.mu.Unlock()
			r.runtime.mu.Unlock()
			return errUsageLedgerConflict
		}
		r.phase = governedRequestTerminalizing
		r.terminalStatus = status
	case governedRequestCancellationWon:
		if status != accounting.StatusCancelled {
			r.mu.Unlock()
			r.runtime.mu.Unlock()
			return errUsageLedgerConflict
		}
		r.phase = governedRequestTerminalizing
		r.terminalStatus = status
	case governedRequestTerminalizing, governedRequestTerminal:
		if r.terminalStatus != status {
			r.mu.Unlock()
			r.runtime.mu.Unlock()
			return errUsageLedgerConflict
		}
	}
	r.stopRenew()
	r.mu.Unlock()
	r.runtime.mu.Unlock()
	<-r.done
	return nil
}

func (r *governedRequest) terminalCommitted(status accounting.Status) error {
	if r == nil {
		return errUsageLedgerInvalid
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if (r.phase != governedRequestTerminalizing && r.phase != governedRequestTerminal) || r.terminalStatus != status {
		return errUsageLedgerConflict
	}
	r.phase = governedRequestTerminal
	return nil
}

func (r *governedRequest) cancellationWon() bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.phase == governedRequestCancellationWon ||
		(r.terminalStatus == accounting.StatusCancelled && (r.phase == governedRequestTerminalizing || r.phase == governedRequestTerminal))
}

func (r *governedRequest) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		defer r.cancel()
		defer func() {
			r.runtime.mu.Lock()
			delete(r.runtime.requests, r)
			r.runtime.mu.Unlock()
		}()
		if r.stopClient != nil {
			r.stopClient()
		}
		if r.stopShutdown != nil {
			r.stopShutdown()
		}
		r.mu.Lock()
		status := r.terminalStatus
		if r.phase == governedRequestActive || r.phase == governedRequestCancellationWon {
			status = accounting.StatusFailed
			if r.phase == governedRequestCancellationWon || r.ctx.Err() != nil {
				status = accounting.StatusCancelled
			}
			r.phase = governedRequestTerminalizing
			r.terminalStatus = status
		}
		r.stopRenew()
		r.mu.Unlock()
		<-r.done
		// If accounting exists, its atomic terminal transaction owns release.
		// Failed terminal persistence must keep the governance lease until TTL.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		finish := governance.Finish{RequestID: r.id, Status: status, FinishedAt: time.Now().UTC()}
		for attempt := 0; attempt < 3; attempt++ {
			err := func() error {
				tx, err := r.runtime.app.store.db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				var count int
				if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounting_requests WHERE id=?`, r.id).Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					return nil
				}
				if err := r.runtime.core.FinishTx(ctx, tx, finish); err != nil {
					return err
				}
				return tx.Commit()
			}()
			if err == nil {
				_ = r.terminalCommitted(status)
				return
			}
			if ctx.Err() != nil {
				return
			}
			if attempt < 2 {
				select {
				case <-time.After(time.Duration(attempt+1) * 10 * time.Millisecond):
				case <-ctx.Done():
					return
				}
			}
		}
	})
}

func (a *App) admitGovernedModel(r *http.Request, auth employeeAuth, model string, protocol accounting.UsageProtocol) (*http.Request, *governedRequest, *modelAdmissionError) {
	if a.governance == nil {
		return r, nil, nil
	}
	return a.governance.Admit(r, auth, model, protocol)
}
