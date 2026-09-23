package service

// Independently authored barriers for the shared final-dispatch boundary.
import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/scheduling"
)

func TestFinalCodexDispatchSerializesWithRefresh(t *testing.T) {
	for _, refreshFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("refresh_first_%t", refreshFirst), func(t *testing.T) {
			f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
			a := f.base.app
			a.accountPool.Close()
			a.accountPool = f.rt
			a.cfg.ExperimentalCodexMembership = true
			a.cfg.CodexOAuthClientID, a.cfg.CodexOAuthRedirectURI = testOAuthClientID, testOAuthRedirectURI
			id := insertRefreshLifecycleAccount(t, a, "ups_dispatch_codex", time.Now().Add(time.Hour), true)
			f.insertModelPool(t, "dispatch-codex", id, 1, modelAccountView{UpstreamID: id, UpstreamModel: "actual", Priority: 1, Weight: 1, MaxConcurrency: 1})
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			ctx = context.WithValue(ctx, requestIDKey{}, "request-codex-guard")
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader("{}")).WithContext(ctx)
			selected, lease, failed := a.prepareModelRoute(r, f.auth1, "dispatch-codex", []string{codexMembershipProvider}, accounting.ProtocolOpenAIResponses, true, func(_ *http.Request, selected route) (route, *modelPreflightError) { return selected, nil })
			if failed != nil || lease == nil {
				t.Fatalf("prepare failed: %+v", failed)
			}
			defer a.releaseModelLease(lease, requestID(ctx), true)
			access := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
			idToken := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "synthetic-refreshed"}})
			var calls atomic.Int32
			a.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return oauthHTTPResponse(200, map[string]string{"access_token": access, "id_token": idToken, "refresh_token": "synthetic-rotated"}), nil
			})}
			refresh := func() error { _, err := a.refresh.refresh(ctx, id, nil, true); return err }
			if refreshFirst {
				if err := refresh(); err != nil {
					t.Fatal(err)
				}
				client, failure := a.dispatchModelRoute(r, f.auth1, "dispatch-codex", selected, lease, true)
				if failure == nil || client != nil {
					t.Fatal("old credential dispatched after refresh won")
				}
				assertDispatchAttemptCount(t, a, requestID(ctx), 0)
			} else {
				reached, release := make(chan struct{}), make(chan struct{})
				a.usage.priceLookup = func(context.Context, string, string) (*accounting.PriceSnapshot, error) {
					close(reached)
					select {
					case <-release:
						return nil, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				dispatched := make(chan *modelAdmissionError, 1)
				go func() {
					_, failure := a.dispatchModelRoute(r, f.auth1, "dispatch-codex", selected, lease, true)
					dispatched <- failure
				}()
				select {
				case <-reached:
				case <-ctx.Done():
					t.Fatal("dispatch did not reach accounting barrier")
				}
				// Refresh uses this same account lock. An unprotected dispatch would
				// allow it here, between final revision read and attempt creation.
				lockCtx, stop := context.WithTimeout(ctx, 60*time.Millisecond)
				unlock, err := a.acquireCodexMutationLock(lockCtx, id)
				stop()
				if unlock != nil {
					unlock()
				}
				if !errors.Is(err, context.DeadlineExceeded) {
					close(release)
					t.Fatal("dispatch did not protect final revision with mutation lock")
				}
				refreshed := make(chan error, 1)
				go func() { refreshed <- refresh() }()
				close(release)
				if failure := <-dispatched; failure != nil {
					t.Fatalf("dispatch failure: %+v", failure)
				}
				if err := <-refreshed; err != nil {
					t.Fatal(err)
				}
				lease.mu.Lock()
				phase := lease.phase
				lease.mu.Unlock()
				if phase != scheduling.MayHaveSent {
					t.Fatal("refresh ran before dispatch phase was marked")
				}
				assertDispatchAttemptCount(t, a, requestID(ctx), 1)
			}
			if calls.Load() != 1 {
				t.Fatalf("refresh calls=%d", calls.Load())
			}
			a.finishRequest(requestID(ctx), "cancelled", 0)
		})
	}
}

func assertDispatchAttemptCount(t *testing.T, a *App, id string, want int) {
	t.Helper()
	var got int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE request_id=?`, id).Scan(&got); err != nil || got != want {
		t.Fatalf("attempts=%d want=%d err=%v", got, want, err)
	}
}

func TestFinalDispatchCancellationAcrossHTTPProtocols(t *testing.T) {
	for _, test := range []struct{ name, provider, path, body string }{
		{"chat", "openai-compatible", "/v1/chat/completions", `{"model":"cancel-model","messages":[{"role":"user","content":"synthetic"}]}`},
		{"responses", "openai-compatible", "/v1/responses", `{"model":"cancel-model","input":"synthetic"}`},
		{"messages", anthropicAPIKeyProvider, "/v1/messages", `{"model":"cancel-model","max_tokens":10,"messages":[{"role":"user","content":"synthetic"}]}`},
		{"gemini", geminiAPIKeyProvider, "/v1beta/models/cancel-model:generateContent", `{"contents":[{"role":"user","parts":[{"text":"synthetic"}]}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
			a := f.base.app
			f.base.enableRuntimeAdminHTTP(t)
			a.cfg.AllowLoopbackUpstream = true
			a.http = newUpstreamClient(true)
			var network atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { network.Add(1); w.WriteHeader(500) }))
			defer target.Close()
			upstream := createModelAdmissionUpstream(t, f.base.server.URL, f.base.cookie, f.base.csrf, "synthetic", test.provider, target.URL, "synthetic-key")
			f.insertModelPool(t, "cancel-model", upstream.ID, 0)
			key := createTestKey(t, f.base.server.URL, f.auth1.EmployeeID, "cancel-key", f.base.cookie, f.base.csrf)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reached := false
			a.usage.priceLookup = func(context.Context, string, string) (*accounting.PriceSnapshot, error) {
				reached = true
				cancel()
				return nil, context.Canceled
			}
			r := httptest.NewRequest("POST", test.path, strings.NewReader(test.body)).WithContext(ctx)
			r.Header.Set("Authorization", "Bearer "+key.Key)
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("anthropic-version", "2023-06-01")
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, r)
			if !reached || network.Load() != 0 || w.Body.Len() != 0 {
				t.Fatalf("cancellation reached=%t network=%d body_length=%d", reached, network.Load(), w.Body.Len())
			}
			id := w.Header().Get("X-Request-ID")
			assertDispatchAttemptCount(t, a, id, 0)
			var legacy, ledger string
			if err := a.store.db.QueryRow(`SELECT m.outcome,a.status FROM model_requests m JOIN accounting_requests a ON a.id=m.id WHERE m.id=?`, id).Scan(&legacy, &ledger); err != nil {
				t.Fatal(err)
			}
			if legacy != "cancelled" || ledger != "cancelled" {
				t.Fatalf("legacy=%s ledger=%s", legacy, ledger)
			}
		})
	}
}
