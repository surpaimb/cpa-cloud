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
	wg         sync.WaitGroup
	renewEvery time.Duration
}

type governedRequest struct {
	runtime      *requestGovernance
	id           string
	ctx          context.Context
	cancel       context.CancelFunc
	stopShutdown func() bool
	done         chan struct{}
	once         sync.Once
	expires      time.Time
}

func newRequestGovernance(a *App, core *governance.Coordinator, policies governancePolicyResolver) (*requestGovernance, error) {
	if a == nil || a.store == nil || core == nil || policies == nil {
		return nil, governance.ErrInvalid
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime := &requestGovernance{app: a, core: core, policies: policies, ctx: ctx, cancel: cancel, renewEvery: 20 * time.Second}
	if _, err := core.RecoverInterrupted(ctx, time.Now().UTC()); err != nil {
		cancel()
		return nil, err
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
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(g.ctx, cancel)
	cleanup := func() { stop(); cancel() }
	if g.ctx.Err() != nil {
		cleanup()
		return r, nil, governanceAdmissionFailure(context.Canceled)
	}
	started := time.Now().UTC()
	g.app.admission.RLock()
	lease, failed := func() (*governance.Lease, *modelAdmissionError) {
		tx, err := g.app.store.db.BeginTx(ctx, nil)
		if err != nil {
			return nil, governanceAdmissionFailure(err)
		}
		defer tx.Rollback()
		if g.app.accountPool == nil {
			return nil, governanceAdmissionFailure(governance.ErrUnavailable)
		}
		authorized, available, allowed, err := g.app.accountPool.authorizationCurrent(ctx, tx, model, auth)
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
		settings, scopes, err := g.policies.ResolveScopesTx(ctx, tx, auth.EmployeeID, auth.KeyID)
		if err != nil {
			return nil, governanceAdmissionFailure(err)
		}
		lease, decision, err := g.core.AdmitTx(ctx, tx, governance.AdmissionStart{
			RequestID: requestID(ctx), Subject: governance.Subject{EmployeeID: auth.EmployeeID, KeyID: auth.KeyID, PublicModel: model, Protocol: protocol},
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
		cleanup()
		return r, nil, failed
	}
	if lease == nil {
		cleanup()
		return r, nil, nil
	}
	guard := &governedRequest{runtime: g, id: requestID(ctx), ctx: ctx, cancel: cancel, stopShutdown: stop, done: make(chan struct{}), expires: lease.ExpiresAt}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		cleanup()
		close(guard.done)
		guard.Close()
		return r, nil, governanceAdmissionFailure(context.Canceled)
	}
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
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(r.ctx, 3*time.Second)
			err := func() error {
				tx, err := r.runtime.app.store.db.BeginTx(ctx, nil)
				if err != nil {
					return err
				}
				defer tx.Rollback()
				lease, err := r.runtime.core.RenewTx(ctx, tx, governance.Renew{RequestID: r.id, ExpectedExpiresAt: r.expires, ObservedAt: time.Now().UTC()})
				if err != nil {
					return err
				}
				if err := tx.Commit(); err != nil {
					return err
				}
				r.expires = lease.ExpiresAt
				return nil
			}()
			cancel()
			if err != nil {
				// A terminal accounting transaction may have released this lease
				// between ticks. That must not cancel an already successful reply.
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				var status string
				readErr := r.runtime.app.store.db.QueryRowContext(ctx, `SELECT status FROM governance_requests WHERE id=?`, r.id).Scan(&status)
				cancel()
				if readErr == nil && (status == string(accounting.StatusSucceeded) || status == string(accounting.StatusFailed) || status == string(accounting.StatusCancelled)) {
					return
				}
				r.cancel()
				return
			}
		}
	}
}

func (r *governedRequest) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		cancelled := r.ctx.Err() != nil
		r.stopShutdown()
		r.cancel()
		<-r.done
		// If accounting exists, its atomic terminal transaction owns release.
		// Failed terminal persistence must keep the governance lease until TTL.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		status := accounting.StatusFailed
		if cancelled {
			status = accounting.StatusCancelled
		}
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
			if err == nil || ctx.Err() != nil {
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
