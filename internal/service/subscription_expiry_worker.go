package service

// Independently authored for docs/subscription-period-expiry-contract.md.

import (
	"context"
	"sync"
	"time"

	"cpacloud.local/server/internal/financial"
)

type subscriptionExpiryWorker struct {
	cancel   context.CancelFunc
	finished sync.WaitGroup
}

func newSubscriptionExpiryWorker(app *App) *subscriptionExpiryWorker {
	ctx, cancel := context.WithCancel(context.Background())
	worker := &subscriptionExpiryWorker{cancel: cancel}
	worker.finished.Add(1)
	go func() {
		defer worker.finished.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		commercial := financial.NewCommercial(app.store.db)
		for {
			passCtx, stop := context.WithTimeout(ctx, 5*time.Second)
			_, _ = commercial.ExpireDueSubscriptions(passCtx, time.Now().UTC())
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

func (w *subscriptionExpiryWorker) Close() {
	if w != nil {
		w.cancel()
		w.finished.Wait()
	}
}
