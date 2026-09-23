package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

const recoverySyntheticResponse = `{"id":"synthetic","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":13,"completion_tokens":2,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`

func TestRecoveryExecutionAtomicSettlementAndStaleIsolation(t *testing.T) {
	f := newRuntimeFixture(t, nil, time.Minute, 10)
	a := f.base.app
	if err := a.accountPool.Close(); err != nil {
		t.Fatal(err)
	}
	a.accountPool = f.rt
	a.cfg.AllowLoopbackUpstream = true
	a.http = newUpstreamClient(true)
	a.cfg.AccountRecoveryEnabled = true
	if _, err := a.store.db.Exec(`UPDATE account_recovery_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for caseIndex, kind := range []string{"success", "stale", "rollback", "dispatch_failure", "cancel", "deadline", "close"} {
		t.Run(kind, func(t *testing.T) {
			runCtx, cancel := context.WithCancel(ctx)
			defer cancel()
			runtimeStopped := make(chan struct{})
			transportMayReturn := make(chan struct{})
			if kind == "close" {
				original := a.http.Transport
				a.http.Transport = generationProbeRoundTripper(func(r *http.Request) (*http.Response, error) {
					response, err := original.RoundTrip(r)
					<-transportMayReturn
					return response, err
				})
				defer func() { a.http.Transport = original }()
			}
			accountID := "recovery_" + kind
			modelID := "model_" + kind
			f.insertAccount(t, accountID, true)
			f.insertModelPool(t, modelID, accountID, 1, modelAccountView{UpstreamID: accountID, UpstreamModel: "actual", Priority: 0, Weight: 1, MaxConcurrency: 1})
			var calls atomic.Int32
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer synthetic-probe-key" {
					t.Error("unexpected probe target or credential")
				}
				var payload map[string]any
				if json.NewDecoder(r.Body).Decode(&payload) != nil || payload["model"] != "actual" {
					t.Error("probe did not use actual mapping")
				}
				if kind == "cancel" || kind == "deadline" || kind == "close" {
					if kind == "cancel" {
						cancel()
					} else if kind == "close" {
						go func() {
							_ = a.accountPool.Close()
							close(runtimeStopped)
						}()
					}
					<-r.Context().Done()
					if kind == "close" {
						// Keep the runner inside RoundTrip after cancellation. Close
						// must still wait, rather than only join the lease heartbeat.
						select {
						case <-runtimeStopped:
							t.Error("runtime closed before probe execution and settlement")
						case <-time.After(100 * time.Millisecond):
						}
						close(transportMayReturn)
					}
					return
				}
				if kind == "stale" {
					if _, err := a.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id=?`, accountID); err != nil {
						t.Error(err)
					}
				}
				if kind == "rollback" {
					if _, err := a.store.db.Exec(`CREATE TRIGGER recovery_finish_fault BEFORE DELETE ON account_recovery_states BEGIN SELECT RAISE(ABORT,'synthetic fault'); END`); err != nil {
						t.Error(err)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(recoverySyntheticResponse))
			}))
			defer mock.Close()
			cipher, err := a.secrets.encryptCredential(accountID, "synthetic-probe-key")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.store.db.Exec(`UPDATE upstreams SET endpoint=?,credential_ciphertext=? WHERE id=?`, mock.URL+"/v1", cipher, accountID); err != nil {
				t.Fatal(err)
			}
			operationIDs := []string{"10f905eb-e1da-4a45-97fe-7b310e024233", "11f905eb-e1da-4a45-97fe-7b310e024233", "12f905eb-e1da-4a45-97fe-7b310e024233", "13f905eb-e1da-4a45-97fe-7b310e024233", "14f905eb-e1da-4a45-97fe-7b310e024233", "15f905eb-e1da-4a45-97fe-7b310e024233", "16f905eb-e1da-4a45-97fe-7b310e024233"}
			state := accountRecoveryState{AccountID: accountID, CooldownEventID: "event_" + kind, OperationID: operationIDs[caseIndex], RecoveryRevision: 1, PoolRevision: 1, AccountRevision: 1, ProviderKind: "openai-compatible", SourceSnapshot: "api_key", PublicModel: modelID, UpstreamModel: "actual", Protocol: string(accounting.ProtocolOpenAIChatCompletions), State: recoveryRequired, NextProbeAt: f.clock.Now(), CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now()}
			seedRecoveryExecutionState(t, a, state)
			request := accountMaintenanceAcquireRequest{OperationID: state.OperationID, AccountID: accountID, CooldownEventID: state.CooldownEventID, ExpectedPoolRevision: 1, ExpectedAccountRevision: 1, ExpectedRecoveryRevision: 1}
			if kind == "dispatch_failure" {
				if _, err := a.store.db.Exec(`CREATE TRIGGER recovery_dispatch_fault BEFORE UPDATE OF dispatch_phase ON account_pool_maintenance_leases BEGIN SELECT RAISE(ABORT,'synthetic fault'); END`); err != nil {
					t.Fatal(err)
				}
				defer a.store.db.Exec(`DROP TRIGGER IF EXISTS recovery_dispatch_fault`)
			}
			code, receipt := a.executeRecoveryOperation(runCtx, request)
			if kind == "close" {
				select {
				case <-runtimeStopped:
				case <-time.After(3 * time.Second):
					t.Fatal("runtime close did not join recovery execution")
				}
			}
			if kind == "rollback" {
				if code != accountPoolStorageUnavailable {
					t.Fatalf("finalize storage fault returned %s", code)
				}
				assertRecoveryExecutionCounts(t, a, accountID, 1, 1)
				attempt, err := a.systemProbes.Get(ctx, state.OperationID)
				if err != nil || attempt.Status != accounting.SystemProbePending {
					t.Fatalf("partial ledger commit: %+v %v", attempt, err)
				}
				if _, err := a.store.db.Exec(`DROP TRIGGER recovery_finish_fault`); err != nil {
					t.Fatal(err)
				}
				if receipt == nil {
					t.Fatal("failed commit discarded maintenance ownership")
				}
				if got := receipt.Finalize(ctx); got != accountPoolReleased {
					t.Fatalf("metadata-only retry: %s", got)
				}
			} else if code != accountPoolReleased {
				t.Fatalf("execution: %s", code)
			}
			attempt, err := a.systemProbes.Get(ctx, state.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if kind == "success" || kind == "rollback" {
				assertRecoveryExecutionCounts(t, a, accountID, 0, 0)
				if attempt.Status != accounting.SystemProbeSucceeded || attempt.Result == nil || *attempt.Result != accounting.SystemProbeGenerationOK || attempt.Usage.OutputTokens == nil || *attempt.Usage.OutputTokens != 2 {
					t.Fatalf("success metadata: %+v", attempt)
				}
			} else {
				assertRecoveryExecutionCounts(t, a, accountID, 1, 0)
				wantStatus, wantResult := accounting.SystemProbeFailed, accounting.SystemProbeConfigurationChanged
				if kind == "cancel" || kind == "close" {
					wantStatus, wantResult = accounting.SystemProbeCancelled, accounting.SystemProbeCancelledResult
				} else if kind == "deadline" {
					wantResult = accounting.SystemProbeUpstreamTimeout
				}
				if attempt.Status != wantStatus || attempt.Result == nil || *attempt.Result != wantResult {
					t.Fatalf("stale or dispatch failure recorded success: %+v", attempt)
				}
				var state string
				if err := a.store.db.QueryRow(`SELECT state FROM account_recovery_states WHERE account_id=?`, accountID).Scan(&state); err != nil || state != recoveryInterrupted {
					t.Fatalf("failed probe did not retain interrupted isolation: %s %v", state, err)
				}
			}
			wantCalls := int32(1)
			if kind == "dispatch_failure" {
				wantCalls = 0
				if attempt.MayHaveSentAt != nil {
					t.Fatal("dispatch rollback leaked MayHaveSent")
				}
			}
			if calls.Load() != wantCalls {
				t.Fatalf("calls=%d want %d", calls.Load(), wantCalls)
			}
			if code, _ := a.executeRecoveryOperation(ctx, request); code == accountPoolReleased || calls.Load() != wantCalls {
				t.Fatalf("completed operation was replayed: %s calls=%d", code, calls.Load())
			}
			var requests int
			if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM model_requests`).Scan(&requests); err != nil || requests != 0 {
				t.Fatalf("probe made employee request: %d %v", requests, err)
			}
		})
	}
}

func seedRecoveryExecutionState(t *testing.T, a *App, state accountRecoveryState) {
	t.Helper()
	tx, err := a.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO account_pool_runtime_cooldowns(account_id,event_id,failure_class,cooldown_until,updated_at) VALUES(?,?,'transient',?,?)`, state.AccountID, state.CooldownEventID, formatAccountPoolTime(state.NextProbeAt.Add(-time.Second)), formatAccountPoolTime(state.NextProbeAt.Add(-2*time.Second)))
	if err != nil {
		t.Fatal(err)
	}
	if err := putAccountRecoveryStateTx(context.Background(), tx, state); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertRecoveryExecutionCounts(t *testing.T, a *App, account string, wantState, wantLease int) {
	t.Helper()
	for table, want := range map[string]int{"account_recovery_states": wantState, "account_pool_runtime_cooldowns": wantState, "account_pool_maintenance_leases": wantLease} {
		var n int
		if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE account_id=?`, account).Scan(&n); err != nil || n != want {
			t.Fatalf("%s rows=%d want=%d err=%v", table, n, want, err)
		}
	}
}

func TestRecoveryIsolationAlsoBlocksLegacyRoute(t *testing.T) {
	f := newRuntimeFixture(t, nil, time.Minute, 10)
	f.insertAccount(t, "legacy_isolated", true)
	f.insertModelPool(t, "pooled_source", "legacy_isolated", 1, modelAccountView{UpstreamID: "legacy_isolated", UpstreamModel: "actual", Weight: 1, MaxConcurrency: 1})
	f.insertModelPool(t, "legacy_alias", "legacy_isolated", 0)
	state := accountRecoveryState{AccountID: "legacy_isolated", CooldownEventID: "legacy_event", OperationID: "legacy_op", RecoveryRevision: 1, PoolRevision: 1, AccountRevision: 1, ProviderKind: "openai-compatible", SourceSnapshot: "api_key", PublicModel: "pooled_source", UpstreamModel: "actual", Protocol: string(accounting.ProtocolOpenAIChatCompletions), State: recoveryRequired, NextProbeAt: f.clock.Now(), CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now()}
	seedRecoveryExecutionState(t, f.base.app, state)
	if _, failure := f.base.app.legacyEmployeeRoute(context.Background(), f.auth1, "legacy_alias", []string{"openai-compatible"}); failure == nil || failure.code != "no_available_route" {
		t.Fatalf("legacy route bypassed recovery: %+v", failure)
	}
	// A clock rollback must not turn an otherwise valid isolation snapshot
	// into an invalid updated-before-created record during startup recovery.
	future := time.Now().UTC().Add(time.Hour)
	if _, err := f.base.app.store.db.Exec(`UPDATE account_recovery_states SET state='in_progress',created_at=?,updated_at=? WHERE account_id=?`, formatAccountPoolTime(future), formatAccountPoolTime(future), state.AccountID); err != nil {
		t.Fatal(err)
	}
	if err := f.base.app.initializeSystemProbeAccounting(context.Background()); err != nil {
		t.Fatal(err)
	}
	stored, err := scanAccountRecoveryState(f.base.app.store.db.QueryRow(accountRecoverySelect+` WHERE account_id=?`, state.AccountID))
	if err != nil || stored.State != recoveryInterrupted || !stored.UpdatedAt.Equal(future) {
		t.Fatalf("clock rollback corrupted recovery state: %+v %v", stored, err)
	}
	// The account remains manually enabled; clearing the exact event restores
	// route eligibility without changing employee keys or permission rows.
	if result, _, _ := f.rt.clearCooldown(context.Background(), state.AccountID, 1, state.CooldownEventID); result != cooldownCleared {
		t.Fatalf("clear: %s", result)
	}
	if _, failure := f.base.app.legacyEmployeeRoute(context.Background(), f.auth1, "legacy_alias", []string{"openai-compatible"}); failure != nil {
		t.Fatalf("clear did not restore legacy route: %+v", failure)
	}
}
