package service

// Independently authored from CPA Cloud's account-pool failover contract.
// Preparation never owns a response writer and cannot replay model execution.
import (
	"context"
	"net/http"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/scheduling"
)

type modelPreflightError struct {
	Failure         *modelAdmissionError
	AccountSpecific bool
	Class           scheduling.FailureClass
	UpstreamStatus  int
}

func accountPreflightFailure(status int, code, message string, class scheduling.FailureClass) *modelPreflightError {
	return &modelPreflightError{Failure: &modelAdmissionError{status, code, message}, AccountSpecific: true, Class: class}
}

func requestPreflightFailure(status int, code, message string) *modelPreflightError {
	return &modelPreflightError{Failure: &modelAdmissionError{status, code, message}}
}

// Each callback must prepare a fresh candidate using only candidateRequest's
// context. It must not write a response, start an accounting attempt, or invoke
// a model executor. Failed preparation owns disposal of any partial secrets.
// The returned lease is owned by the caller only on success.
func (a *App) prepareModelRoute(r *http.Request, auth employeeAuth, model string, providers []string, protocol accounting.UsageProtocol, record bool, prepare func(*http.Request, route) (route, *modelPreflightError)) (route, *accountPoolLease, *modelAdmissionError) {
	selected, lease, failure := a.selectModelRoute(r, auth, model, providers, record)
	if failure != nil {
		return route{}, nil, failure
	}
	var initialRevision int64
	if lease != nil {
		initialRevision = lease.PoolRevision()
	}
	switched := false
	for {
		candidateRequest := r
		if lease != nil {
			candidateRequest = r.WithContext(lease.Context())
		}
		var preparationError *modelPreflightError
		if lease != nil && protocol != "" {
			if code := lease.BindRecoverySnapshot(candidateRequest.Context(), model, protocol, selected); code != accountPoolAcquired {
				preparationError = &modelPreflightError{Failure: poolAdmissionFailure(code)}
			}
		}
		if candidateRequest.Context().Err() == nil && prepare != nil {
			var actual route
			if preparationError == nil {
				actual, preparationError = prepare(candidateRequest, selected)
			}
			if actual.AccountID != "" {
				selected = actual
				if lease != nil && protocol != "" {
					if code := lease.BindRecoverySnapshot(candidateRequest.Context(), model, protocol, actual); code != accountPoolAcquired {
						preparationError = &modelPreflightError{Failure: poolAdmissionFailure(code)}
					}
				}
			}
		}
		if candidateRequest.Context().Err() != nil {
			preparationError = &modelPreflightError{Failure: poolAdmissionFailure(accountPoolCancelled)}
		}
		if preparationError == nil {
			frozen, err := a.prepareRouteEgress(candidateRequest.Context(), selected)
			if err != nil {
				preparationError = proxyPreflightError(err)
			} else {
				selected.egress = frozen
			}
		}
		if preparationError == nil {
			if switched {
				if err := a.markRequestUsageFailover(requestID(r.Context())); err != nil {
					return a.finishModelPreflight(r, lease, record, &modelPreflightError{Failure: poolAdmissionFailure(accountPoolStorageUnavailable)})
				}
			}
			return selected, lease, nil
		}
		if preparationError.Failure == nil {
			preparationError = &modelPreflightError{Failure: poolAdmissionFailure(accountPoolStorageUnavailable)}
		}
		if !preparationError.AccountSpecific || switched || lease == nil || initialRevision <= 0 || a.accountPool == nil || candidateRequest.Context().Err() != nil {
			return a.finishModelPreflight(r, lease, record, preparationError)
		}
		// Release cancellation belongs to this candidate. Retry must always use
		// the original employee request context, never the cancelled lease.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		released, result := lease.Release(ctx, scheduling.ReleaseResult{Failure: preparationError.Class, Phase: scheduling.DispatchNotStarted})
		cancel()
		if !released || result.Code != accountPoolReleased || !result.RetrySuggested {
			if result.Code == accountPoolStorageUnavailable {
				preparationError = &modelPreflightError{Failure: poolAdmissionFailure(accountPoolStorageUnavailable)}
			}
			return a.finishModelPreflight(r, lease, record, preparationError)
		}
		failedAccount := selected.AccountID
		lease = nil
		sticky, stickyErr := a.poolSessionDigest(r, auth, model)
		if stickyErr != nil {
			return a.finishModelPreflight(r, nil, record, &modelPreflightError{Failure: stickyErr})
		}
		resultNext := a.accountPool.AcquireWithOptions(r.Context(), model, auth, providers, sticky, accountPoolAcquireOptions{
			ExpectedPoolRevision: initialRevision, ExcludedAccountIDs: []string{failedAccount},
		})
		if resultNext.Code != accountPoolAcquired || resultNext.Legacy || resultNext.Lease == nil {
			return a.finishModelPreflight(r, resultNext.Lease, record, &modelPreflightError{Failure: poolAdmissionFailure(resultNext.Code)})
		}
		selected, lease = resultNext.Route, resultNext.Lease
		switched = true
	}
}

func (a *App) finishModelPreflight(r *http.Request, lease *accountPoolLease, record bool, failed *modelPreflightError) (route, *accountPoolLease, *modelAdmissionError) {
	if record {
		outcome := "failed"
		if r.Context().Err() != nil {
			outcome = "cancelled"
		}
		if err := a.finishRequestChecked(requestID(r.Context()), outcome, failed.UpstreamStatus); err != nil {
			failed = &modelPreflightError{Failure: poolAdmissionFailure(accountPoolStorageUnavailable)}
		}
	}
	// A malformed employee request or a global persistence failure is not an
	// account outage. Do not infer account cooldown from a zero upstream status.
	if lease != nil {
		class := scheduling.FailureNone
		if failed.AccountSpecific {
			class = failed.Class
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, result := lease.Release(ctx, scheduling.ReleaseResult{Failure: class, Phase: scheduling.DispatchUnknown})
		cancel()
		if result.Code == accountPoolStorageUnavailable {
			failed = &modelPreflightError{Failure: poolAdmissionFailure(accountPoolStorageUnavailable)}
		}
	}
	if record {
		a.cleanupRequestUsage(requestID(r.Context()))
	}
	return route{}, nil, failed.Failure
}
