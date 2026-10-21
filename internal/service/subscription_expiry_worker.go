package service

// Independently authored for docs/subscription-period-expiry-contract.md.

import (
	"context"
	"errors"
	"sync"
	"time"

	"cpacloud.local/server/internal/financial"
)

type subscriptionExpiryWorker struct {
	cancel   context.CancelFunc
	finished sync.WaitGroup
	parent   context.Context
	observer financial.ExpiryObserver
}

func newSubscriptionExpiryWorker(app *App) *subscriptionExpiryWorker {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &subscriptionExpiryWorker{cancel: cancel, parent: ctx, observer: app.expiryObserver}
	worker.expiryPoint(financial.ExpiryBind, ctx)
	worker.finished.Add(1)
	go func() {
		defer func() {
			worker.expiryPoint(financial.ExpiryDoneReady, ctx)
			worker.finished.Done()
		}()
		worker.expiryPoint(financial.ExpiryStart, ctx)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		commercial := financial.NewCommercial(app.store.db)
		var pass uint16
		for {
			passCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			if worker.observer != nil && pass < ^uint16(0) {
				pass++
			}
			started := worker.expiryEnter(financial.ExpiryPass, pass, passCtx)
			callCtx := financial.WithExpiryObservation(passCtx, worker.observer, pass)
			count, passErr := commercial.ExpireDueSubscriptions(callCtx, time.Now().UTC())
			kind := financial.ExpiryErrorNone
			if passErr != nil {
				kind = financial.ExpiryErrorOther
			}
			worker.expiryReturn(financial.ExpiryPass, pass, passCtx, started, kind, expiryWorkerCount(count))
			stopStarted := worker.expiryEnter(financial.ExpiryStop, pass, passCtx)
			stop()
			worker.expiryReturn(financial.ExpiryStop, pass, passCtx, stopStarted, financial.ExpiryErrorNone, financial.ExpiryCountNA)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return worker
}

func (w *subscriptionExpiryWorker) Close() {
	if w != nil {
		cancelStarted := w.expiryEnter(financial.ExpiryCancel, 0, w.parent)
		w.cancel()
		w.expiryReturn(financial.ExpiryCancel, 0, w.parent, cancelStarted, financial.ExpiryErrorNone, financial.ExpiryCountNA)
		waitStarted := w.expiryEnter(financial.ExpiryWait, 0, w.parent)
		w.finished.Wait()
		w.expiryReturn(financial.ExpiryWait, 0, w.parent, waitStarted, financial.ExpiryErrorNone, financial.ExpiryCountNA)
	}
}

// Independently authored passive expiry-only producers. The immutable sink has
// no return value; no raw error/context/DB pointer crosses the internal API.
func (w *subscriptionExpiryWorker) expiryPoint(phase financial.ExpiryPhase, ctx context.Context) {
	if w.observer != nil {
		w.observer(financial.ExpiryObservation{Phase: phase, Edge: financial.ExpiryPoint, Error: financial.ExpiryErrorNone, Context: expiryWorkerContext(ctx)})
	}
}

func (w *subscriptionExpiryWorker) expiryEnter(phase financial.ExpiryPhase, pass uint16, ctx context.Context) time.Time {
	if w.observer == nil {
		return time.Time{}
	}
	started := time.Now()
	w.observer(financial.ExpiryObservation{Pass: pass, Phase: phase, Edge: financial.ExpiryEnter, Error: financial.ExpiryErrorNone, Context: expiryWorkerContext(ctx)})
	return started
}

func (w *subscriptionExpiryWorker) expiryReturn(phase financial.ExpiryPhase, pass uint16, ctx context.Context, started time.Time, kind financial.ExpiryError, count financial.ExpiryCount) {
	if w.observer != nil {
		w.observer(financial.ExpiryObservation{Pass: pass, Phase: phase, Edge: financial.ExpiryReturn, Error: kind, Context: expiryWorkerContext(ctx), Elapsed: expiryWorkerElapsed(time.Since(started)), Count: count})
	}
}

func expiryWorkerContext(ctx context.Context) financial.ExpiryContext {
	switch err := ctx.Err(); {
	case err == nil:
		return financial.ExpiryContextNone
	case errors.Is(err, context.Canceled):
		return financial.ExpiryContextCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return financial.ExpiryContextDeadline
	default:
		return financial.ExpiryContextOther
	}
}

func expiryWorkerElapsed(d time.Duration) financial.ExpiryElapsed {
	switch {
	case d < 0:
		return financial.ExpiryElapsedInvalid
	case d < time.Millisecond:
		return financial.ExpiryLT1MS
	case d < 100*time.Millisecond:
		return financial.Expiry1MSLT100MS
	case d < time.Second:
		return financial.Expiry100MSLT1S
	case d < 10*time.Second:
		return financial.Expiry1SLT10S
	case d < time.Minute:
		return financial.Expiry10SLT60S
	default:
		return financial.ExpiryGE60S
	}
}

func expiryWorkerCount(n int) financial.ExpiryCount {
	switch {
	case n < 0:
		return financial.ExpiryCountInvalid
	case n == 0:
		return financial.ExpiryCountZero
	case n == 1:
		return financial.ExpiryCountOne
	default:
		return financial.ExpiryCountMany
	}
}
