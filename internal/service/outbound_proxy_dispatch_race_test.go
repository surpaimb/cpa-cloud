package service

// Independently authored barriers for the shared final-dispatch boundary.
import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/keypolicy"
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

func TestLegacyModelRevisionRejectsWireABAAtFinalDispatch(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
	a := f.base.app
	a.accountPool.Close()
	a.accountPool = f.rt
	f.base.enableRuntimeAdminHTTP(t)
	f.insertAccount(t, "ups_model_aba", true)
	f.insertModelPool(t, "model-aba", "ups_model_aba", 0)
	var upstreamCalls atomic.Int32
	a.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		upstreamCalls.Add(1)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
	})}

	prepare := func(requestIDValue string) (*http.Request, route) {
		t.Helper()
		ctx := context.WithValue(context.Background(), requestIDKey{}, requestIDValue)
		r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)).WithContext(ctx)
		selected, lease, failed := a.prepareModelRoute(r, f.auth1, "model-aba", []string{"openai-compatible"}, accounting.ProtocolOpenAIChatCompletions, true, func(_ *http.Request, selected route) (route, *modelPreflightError) {
			return selected, nil
		})
		if failed != nil || lease != nil {
			t.Fatalf("prepare failed=%+v lease=%v", failed, lease)
		}
		return r, selected
	}

	oldRequest, oldRoute := prepare("request-model-aba-old")
	if oldRoute.ModelRevision != 1 || oldRoute.WireProtocol != wireProtocolLegacyNative {
		t.Fatalf("old route revision=%d wire=%q", oldRoute.ModelRevision, oldRoute.WireProtocol)
	}
	for _, change := range []struct {
		expected int64
		wire     string
	}{{1, string(wireProtocolResponses)}, {2, string(wireProtocolLegacyNative)}} {
		response := requestJSON(t, http.MethodPatch, f.base.server.URL+"/admin/api/v1/models/model-aba", marshalTestJSON(t, map[string]any{
			"expected_revision": change.expected, "wire_protocol": change.wire,
		}), f.base.cookie, f.base.csrf, f.base.server.URL)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("CAS wire %q status=%d body=%s", change.wire, response.StatusCode, readBody(response))
		}
		response.Body.Close()
	}
	if client, failure := a.dispatchModelRoute(oldRequest, f.auth1, "model-aba", oldRoute, nil, true); client != nil || failure == nil {
		t.Fatalf("stale ABA route dispatched client=%v failure=%+v", client, failure)
	}
	assertDispatchAttemptCount(t, a, requestID(oldRequest.Context()), 0)
	if upstreamCalls.Load() != 0 {
		t.Fatalf("stale ABA route made %d upstream calls", upstreamCalls.Load())
	}
	a.finishRequest(requestID(oldRequest.Context()), "failed", 0)

	freshRequest, freshRoute := prepare("request-model-aba-fresh")
	if freshRoute.ModelRevision != 3 || freshRoute.WireProtocol != wireProtocolLegacyNative {
		t.Fatalf("fresh route revision=%d wire=%q", freshRoute.ModelRevision, freshRoute.WireProtocol)
	}
	client, failure := a.dispatchModelRoute(freshRequest, f.auth1, "model-aba", freshRoute, nil, true)
	if failure != nil || client == nil {
		t.Fatalf("fresh route failed client=%v failure=%+v", client, failure)
	}
	request, err := http.NewRequestWithContext(freshRequest.Context(), http.MethodPost, "https://synthetic.invalid/v1/chat/completions", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	assertDispatchAttemptCount(t, a, requestID(freshRequest.Context()), 1)
	if upstreamCalls.Load() != 1 {
		t.Fatalf("fresh route upstream calls=%d", upstreamCalls.Load())
	}
	a.finishRequest(requestID(freshRequest.Context()), "cancelled", http.StatusOK)
}

func TestNonBudgetDispatchBarrierSerializesRevisionThroughDurableMarker(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
	a := f.base.app
	a.accountPool.Close()
	a.accountPool = f.rt
	f.insertAccount(t, "ups_atomic_dispatch", true)
	f.insertModelPool(t, "atomic-dispatch-model", "ups_atomic_dispatch", 1, modelAccountView{UpstreamID: "ups_atomic_dispatch", UpstreamModel: "actual-model", Priority: 1, Weight: 1, MaxConcurrency: 1})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, requestIDKey{}, "request-atomic-dispatch")
	r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader("{}")).WithContext(ctx)
	selected, lease, failed := a.prepareModelRoute(r, f.auth1, "atomic-dispatch-model", []string{"openai-compatible"}, accounting.ProtocolOpenAIResponses, true, func(_ *http.Request, selected route) (route, *modelPreflightError) { return selected, nil })
	if failed != nil || lease == nil {
		t.Fatalf("prepare failed: %+v", failed)
	}
	defer a.releaseModelLease(lease, requestID(ctx), true)
	reached, release := make(chan struct{}), make(chan struct{})
	a.usage.priceLookup = func(context.Context, string, string) (*accounting.PriceSnapshot, error) {
		close(reached)
		<-release
		return nil, nil
	}
	dispatched := make(chan *modelAdmissionError, 1)
	go func() {
		_, failure := a.dispatchModelRoute(r, f.auth1, "atomic-dispatch-model", selected, lease, true)
		dispatched <- failure
	}()
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatal("dispatch did not reach transactional price freeze")
	}
	mutated := make(chan error, 1)
	go func() {
		a.admission.Lock()
		defer a.admission.Unlock()
		_, err := a.store.db.ExecContext(ctx, `UPDATE upstreams SET revision=revision+1 WHERE id='ups_atomic_dispatch'`)
		mutated <- err
	}()
	select {
	case err := <-mutated:
		close(release)
		t.Fatalf("revision mutation crossed the open dispatch barrier: %v", err)
	case <-time.After(60 * time.Millisecond):
	}
	close(release)
	if failure := <-dispatched; failure != nil {
		t.Fatalf("dispatch failure: %+v", failure)
	}
	if err := <-mutated; err != nil {
		t.Fatal(err)
	}
	var dispatches int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempt_dispatches d JOIN accounting_attempts a ON a.id=d.attempt_id WHERE a.request_id=?`, requestID(ctx)).Scan(&dispatches); err != nil || dispatches != 1 {
		t.Fatalf("durable dispatch markers=%d err=%v", dispatches, err)
	}
	a.finishRequest(requestID(ctx), "cancelled", 0)
}

func TestCrossProtocolStreamDispatchRejectsStaleKeyPolicyRevisionAndABA(t *testing.T) {
	for _, test := range []struct {
		name string
		aba  bool
	}{{name: "stale_revision"}, {name: "aba", aba: true}} {
		t.Run(test.name, func(t *testing.T) {
			f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
			a := f.base.app
			a.accountPool.Close()
			a.accountPool = f.rt
			f.insertAccount(t, "ups_key_policy_stream", true)
			f.insertModelPool(t, "key-policy-stream-model", "ups_key_policy_stream", 1, modelAccountView{
				UpstreamID: "ups_key_policy_stream", UpstreamModel: "actual-model", Priority: 1, Weight: 1, MaxConcurrency: 1,
			})
			if _, err := a.store.db.Exec(`UPDATE models SET wire_protocol='openai-responses' WHERE id='key-policy-stream-model'`); err != nil {
				t.Fatal(err)
			}

			ctx := context.WithValue(context.Background(), requestIDKey{}, "request-key-policy-stream-"+test.name)
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)).WithContext(ctx)
			selected, lease, failed := a.prepareModelRoute(r, f.auth1, "key-policy-stream-model", []string{"openai-compatible"}, accounting.ProtocolOpenAIChatCompletions, true, func(_ *http.Request, selected route) (route, *modelPreflightError) {
				return selected, nil
			})
			if failed != nil || lease == nil {
				t.Fatalf("prepare failed=%+v lease=%v", failed, lease)
			}
			defer a.releaseModelLease(lease, requestID(ctx), true)

			now := time.Now().UTC()
			stored, err := keypolicy.Replace(ctx, a.store.db, f.auth1.KeyID, 1, keypolicy.Replacement{
				ProtocolMode: keypolicy.ModeSelected, Protocols: []keypolicy.ClientProtocol{},
				ModelMode: keypolicy.ModeAll, Models: []string{},
			}, now)
			if err != nil {
				t.Fatal(err)
			}
			if test.aba {
				stored, err = keypolicy.Replace(ctx, a.store.db, f.auth1.KeyID, stored.Revision, keypolicy.Replacement{
					ProtocolMode: keypolicy.ModeAll, Protocols: []keypolicy.ClientProtocol{},
					ModelMode: keypolicy.ModeAll, Models: []string{},
				}, now.Add(time.Nanosecond))
				if err != nil {
					t.Fatal(err)
				}
				if stored.Revision != 3 || !keypolicy.Allows(stored, keypolicy.ProtocolOpenAIChat, "key-policy-stream-model") {
					t.Fatalf("ABA policy=%+v", stored)
				}
			}

			var upstreamCalls atomic.Int32
			a.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				upstreamCalls.Add(1)
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{}`))}, nil
			})}
			client, failure := a.dispatchModelRoute(r, f.auth1, "key-policy-stream-model", selected, lease, true)
			if client != nil || failure == nil {
				t.Fatalf("stale key policy dispatched client=%v failure=%+v", client, failure)
			}
			assertDispatchAttemptCount(t, a, requestID(ctx), 0)
			if upstreamCalls.Load() != 0 {
				t.Fatalf("stale key policy made %d upstream calls", upstreamCalls.Load())
			}
			a.finishRequest(requestID(ctx), "failed", 0)
		})
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
