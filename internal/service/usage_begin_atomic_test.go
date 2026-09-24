package service

// Governance integration requires both pre-existing request ledgers to start
// atomically. Inject failures without touching a real account or running model.
import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

func TestUsageRequestStartAtomicRollbackAndRetry(t *testing.T) {
	for _, table := range []string{"model_requests", "accounting_requests"} {
		t.Run(table, func(t *testing.T) {
			f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
			a := f.base.app
			a.accountPool.Close()
			a.accountPool = f.rt
			f.insertAccount(t, "atomic-account", true)
			f.insertModelPool(t, "atomic-model", "atomic-account", 1, modelAccountView{UpstreamID: "atomic-account", UpstreamModel: "actual", Priority: 1, Weight: 1, MaxConcurrency: 1})
			r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
			r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, "atomic-request"))
			if _, err := a.store.db.Exec(`CREATE TRIGGER reject_atomic_begin BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
				t.Fatal(err)
			}
			_, lease, failed := a.selectModelRoute(r, f.auth1, "atomic-model", []string{"openai-compatible"}, accounting.ProtocolOpenAIChatCompletions, true)
			if lease != nil || failed == nil {
				t.Fatal("failed request start acquired a route")
			}
			for _, check := range []string{"model_requests", "accounting_requests", "accounting_attempts", "account_pool_runtime_leases", "account_pool_runtime_cooldowns"} {
				var count int
				if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM ` + check).Scan(&count); err != nil || count != 0 {
					t.Fatalf("rollback %s count=%d err=%v", check, count, err)
				}
			}
			if _, found := a.usageRequests.Load("atomic-request"); found {
				t.Fatal("uncommitted request published in memory")
			}
			if _, err := a.store.db.Exec(`DROP TRIGGER reject_atomic_begin`); err != nil {
				t.Fatal(err)
			}
			_, lease, failed = a.selectModelRoute(r, f.auth1, "atomic-model", []string{"openai-compatible"}, accounting.ProtocolOpenAIChatCompletions, true)
			if failed != nil || lease == nil {
				t.Fatalf("retry failed: %+v", failed)
			}
			defer a.releaseModelLease(lease, "atomic-request", true)
			var legacy, ledger string
			if err := a.store.db.QueryRow(`SELECT m.started_at,a.started_at FROM model_requests m JOIN accounting_requests a ON a.id=m.id WHERE m.id='atomic-request'`).Scan(&legacy, &ledger); err != nil {
				t.Fatal(err)
			}
			left, err1 := time.Parse(time.RFC3339Nano, legacy)
			right, err2 := time.Parse(time.RFC3339Nano, ledger)
			if err1 != nil || err2 != nil || !left.Equal(right) {
				t.Fatal(fmt.Sprintf("different admission times, parse errors=%v/%v", err1, err2))
			}
			if err := a.finishRequestChecked("atomic-request", "cancelled", 0); err != nil {
				t.Fatal(err)
			}
		})
	}
}
