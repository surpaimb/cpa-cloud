package service

// Independently authored for docs/subscription-one-shot-renewal-contract.md.

import (
	"context"
	"sync"
	"time"

	"cpacloud.local/server/internal/financial"
)

type subscriptionOneShotWorker struct {
	cancel   context.CancelFunc
	finished sync.WaitGroup
}

func newSubscriptionOneShotWorker(app *App) *subscriptionOneShotWorker {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &subscriptionOneShotWorker{cancel: cancel}
	worker.finished.Add(1)
	go func() {
		defer worker.finished.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		commercial := financial.NewCommercial(app.store.db)
		for {
			passCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			_, _ = commercial.ProcessDueOneShotRenewals(passCtx, time.Now().UTC())
			stop()
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	return worker
}

func (w *subscriptionOneShotWorker) Close() {
	if w != nil {
		w.cancel()
		w.finished.Wait()
	}
}
