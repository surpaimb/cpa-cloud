package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type nativePreflightFixture struct {
	app      *App
	server   *httptest.Server
	cookie   *http.Cookie
	csrf     string
	employee employee
	key      keyView
	serial   int
}

func newNativePreflightFixture(t *testing.T) *nativePreflightFixture {
	t.Helper()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Native preflight employee")
	key := createTestKey(t, server.URL, employee.ID, "native-preflight-key", cookie, csrf)
	return &nativePreflightFixture{app: app, server: server, cookie: cookie, csrf: csrf, employee: employee, key: key}
}

func (f *nativePreflightFixture) addPool(t *testing.T, provider, firstEndpoint, secondEndpoint, firstModel, secondModel string) (string, upstreamView, upstreamView) {
	t.Helper()
	f.serial++
	model := fmt.Sprintf("native-preflight-%d", f.serial)
	first := createModelAdmissionUpstream(t, f.server.URL, f.cookie, f.csrf, model+" first", provider, firstEndpoint, "first-secret")
	second := createModelAdmissionUpstream(t, f.server.URL, f.cookie, f.csrf, model+" second", provider, secondEndpoint, "second-secret")
	createModelAdmissionModel(t, f.server.URL, f.cookie, f.csrf, model, first.ID, firstModel)
	body := marshalTestJSON(t, map[string]any{
		"expected_revision": 0,
		"items": []map[string]any{
			{"upstream_id": first.ID, "upstream_model": firstModel, "priority": 10, "weight": 1, "max_concurrency": 1},
			{"upstream_id": second.ID, "upstream_model": secondModel, "priority": 5, "weight": 1, "max_concurrency": 1},
		},
	})
	response := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/models/"+model+"/accounts", body, f.cookie, f.csrf, f.server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("configure native pool status=%d body=%s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
	return model, first, second
}

func (f *nativePreflightFixture) corruptCredential(t *testing.T, accountID string) {
	t.Helper()
	if _, err := f.app.store.db.Exec(`UPDATE upstreams SET credential_ciphertext=? WHERE id=?`, []byte{1, 2, 3}, accountID); err != nil {
		t.Fatal(err)
	}
}

func (f *nativePreflightFixture) anthropic(t *testing.T, ctx context.Context, path, model string, stream bool) (*http.Response, error) {
	t.Helper()
	payload := map[string]any{"model": model, "max_tokens": 8, "messages": []map[string]any{{"role": "user", "content": "hello"}}}
	if !strings.HasSuffix(path, "/count_tokens") {
		payload["stream"] = stream
	}
	body := marshalTestJSON(t, payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.server.URL+path, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+f.key.Key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Anthropic-Version", "2023-06-01")
	return http.DefaultClient.Do(request)
}

func (f *nativePreflightFixture) gemini(t *testing.T, ctx context.Context, model string, stream bool) (*http.Response, error) {
	t.Helper()
	operation := ":generateContent"
	if stream {
		operation = ":streamGenerateContent?alt=sse"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.server.URL+"/v1beta/models/"+model+operation, strings.NewReader(`{"contents":[{"parts":[{"text":"hello"}]}]}`))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+f.key.Key)
	request.Header.Set("Content-Type", "application/json")
	return http.DefaultClient.Do(request)
}

func assertNativeAttempt(t *testing.T, app *App, requestID, accountID, dispatch string) {
	t.Helper()
	var actualAccount, actualDispatch string
	if err := app.store.db.QueryRow(`SELECT account_id,dispatch FROM accounting_attempts WHERE request_id=?`, requestID).Scan(&actualAccount, &actualDispatch); err != nil {
		t.Fatal(err)
	}
	if actualAccount != accountID || actualDispatch != dispatch {
		t.Fatalf("attempt account/dispatch=%q/%q want=%q/%q", actualAccount, actualDispatch, accountID, dispatch)
	}
}

func TestNativePreflightAccountSwitching(t *testing.T) {
	f := newNativePreflightFixture(t)

	t.Run("bad local credential rebuilds candidate request", func(t *testing.T) {
		tests := []struct {
			name      string
			provider  string
			first     string
			second    string
			countOnly bool
			do        func(*testing.T, context.Context, string) (*http.Response, error)
			handler   func(*testing.T, http.ResponseWriter, *http.Request, string)
		}{
			{
				name: "anthropic messages", provider: anthropicAPIKeyProvider, first: "claude-first", second: "claude-second",
				do: func(t *testing.T, ctx context.Context, model string) (*http.Response, error) {
					return f.anthropic(t, ctx, "/v1/messages", model, false)
				},
				handler: func(t *testing.T, w http.ResponseWriter, r *http.Request, model string) {
					if r.URL.Path != "/v1/messages" || r.Header.Get("Authorization") != "Bearer second-secret" {
						t.Errorf("Anthropic target/header=%q/%q", r.URL.Path, r.Header.Get("Authorization"))
					}
					var payload map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || string(payload["model"]) != fmt.Sprintf("%q", model) {
						t.Errorf("Anthropic mapped body=%s err=%v", payload["model"], err)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"msg","type":"message","role":"assistant","content":[],"model":"claude-second","stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
				},
			},
			{
				name: "anthropic count tokens", provider: anthropicAPIKeyProvider, first: "claude-count-first", second: "claude-count-second", countOnly: true,
				do: func(t *testing.T, ctx context.Context, model string) (*http.Response, error) {
					return f.anthropic(t, ctx, "/v1/messages/count_tokens", model, false)
				},
				handler: func(t *testing.T, w http.ResponseWriter, r *http.Request, model string) {
					if r.URL.Path != "/v1/messages/count_tokens" || r.Header.Get("Authorization") != "Bearer second-secret" {
						t.Errorf("count_tokens target/header=%q/%q", r.URL.Path, r.Header.Get("Authorization"))
					}
					var payload map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || string(payload["model"]) != fmt.Sprintf("%q", model) {
						t.Errorf("count_tokens mapped body=%s err=%v", payload["model"], err)
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"input_tokens":17}`)
				},
			},
			{
				name: "gemini", provider: geminiAPIKeyProvider, first: "gemini-first", second: "gemini-second",
				do: func(t *testing.T, ctx context.Context, model string) (*http.Response, error) {
					return f.gemini(t, ctx, model, false)
				},
				handler: func(t *testing.T, w http.ResponseWriter, r *http.Request, model string) {
					if r.URL.Path != "/v1beta/models/"+model+":generateContent" || r.Header.Get("x-goog-api-key") != "second-secret" {
						t.Errorf("Gemini target/header=%q/%q", r.URL.Path, r.Header.Get("x-goog-api-key"))
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
				},
			},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				var firstCalls, secondCalls atomic.Int32
				firstServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { firstCalls.Add(1) }))
				defer firstServer.Close()
				secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					secondCalls.Add(1)
					test.handler(t, w, r, test.second)
				}))
				defer secondServer.Close()
				model, first, second := f.addPool(t, test.provider, firstServer.URL, secondServer.URL, test.first, test.second)
				f.corruptCredential(t, first.ID)
				response, err := test.do(t, context.Background(), model)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
				}
				if firstCalls.Load() != 0 || secondCalls.Load() != 1 {
					t.Fatalf("calls first/second=%d/%d", firstCalls.Load(), secondCalls.Load())
				}
				requestID := response.Header.Get("X-Request-ID")
				if test.countOnly {
					var requests, attempts int
					if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM model_requests WHERE id=?`, requestID).Scan(&requests); err != nil {
						t.Fatal(err)
					}
					if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE request_id=?`, requestID).Scan(&attempts); err != nil {
						t.Fatal(err)
					}
					if requests != 0 || attempts != 0 {
						t.Fatalf("count_tokens recorded generation rows=%d/%d", requests, attempts)
					}
				} else {
					assertNativeAttempt(t, f.app, requestID, second.ID, "failover")
				}
			})
		}
	})

	t.Run("only one local switch", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer upstream.Close()
		model, first, second := f.addPool(t, anthropicAPIKeyProvider, upstream.URL, upstream.URL, "claude-first-bad", "claude-second-bad")
		f.corruptCredential(t, first.ID)
		f.corruptCredential(t, second.ID)
		response, err := f.anthropic(t, context.Background(), "/v1/messages", model, false)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable || calls.Load() != 0 {
			t.Fatalf("status=%d calls=%d body=%s", response.StatusCode, calls.Load(), readBody(response))
		}
		var attempts int
		if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE request_id=?`, response.Header.Get("X-Request-ID")).Scan(&attempts); err != nil || attempts != 0 {
			t.Fatalf("preflight attempts=%d err=%v", attempts, err)
		}
	})

	t.Run("invalid model mapping does not switch accounts", func(t *testing.T) {
		var firstCalls, secondCalls atomic.Int32
		first := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { firstCalls.Add(1) }))
		defer first.Close()
		second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { secondCalls.Add(1) }))
		defer second.Close()
		model, firstAccount, _ := f.addPool(t, geminiAPIKeyProvider, first.URL, second.URL, "gemini-initial", "gemini-unused")
		if _, err := f.app.store.db.Exec(`UPDATE model_account_pool_routes SET upstream_model='invalid model mapping' WHERE model_id=? AND upstream_id=?`, model, firstAccount.ID); err != nil {
			t.Fatal(err)
		}
		f.app.notifyAccountPoolChanged()
		response, err := f.gemini(t, context.Background(), model, false)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusServiceUnavailable || firstCalls.Load() != 0 || secondCalls.Load() != 0 {
			t.Fatalf("status=%d calls=%d/%d body=%s", response.StatusCode, firstCalls.Load(), secondCalls.Load(), readBody(response))
		}
	})

	t.Run("dispatch boundary prevents replay", func(t *testing.T) {
		t.Run("transport error", func(t *testing.T) {
			dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			deadURL := dead.URL
			dead.Close()
			var secondCalls atomic.Int32
			second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { secondCalls.Add(1) }))
			defer second.Close()
			model, _, _ := f.addPool(t, anthropicAPIKeyProvider, deadURL, second.URL, "claude-dead", "claude-unused")
			response, err := f.anthropic(t, context.Background(), "/v1/messages", model, false)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusBadGateway || secondCalls.Load() != 0 {
				t.Fatalf("status=%d second calls=%d body=%s", response.StatusCode, secondCalls.Load(), readBody(response))
			}
		})

		t.Run("HTTP 429", func(t *testing.T) {
			var firstCalls, secondCalls atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				firstCalls.Add(1)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { secondCalls.Add(1) }))
			defer second.Close()
			model, _, _ := f.addPool(t, geminiAPIKeyProvider, first.URL, second.URL, "gemini-rate", "gemini-unused")
			response, err := f.gemini(t, context.Background(), model, false)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusTooManyRequests || firstCalls.Load() != 1 || secondCalls.Load() != 0 {
				t.Fatalf("status=%d calls=%d/%d body=%s", response.StatusCode, firstCalls.Load(), secondCalls.Load(), readBody(response))
			}
		})

		t.Run("SSE error", func(t *testing.T) {
			var firstCalls, secondCalls atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				firstCalls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"private\"}}\n\n")
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { secondCalls.Add(1) }))
			defer second.Close()
			model, _, _ := f.addPool(t, anthropicAPIKeyProvider, first.URL, second.URL, "claude-stream", "claude-unused")
			response, err := f.anthropic(t, context.Background(), "/v1/messages", model, true)
			if err != nil {
				t.Fatal(err)
			}
			body := readBody(response)
			response.Body.Close()
			if response.StatusCode != http.StatusOK || firstCalls.Load() != 1 || secondCalls.Load() != 0 || strings.Contains(body, "private") {
				t.Fatalf("status=%d calls=%d/%d body=%s", response.StatusCode, firstCalls.Load(), secondCalls.Load(), body)
			}
		})

		t.Run("cancel", func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			var secondCalls atomic.Int32
			first := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				entered <- struct{}{}
				select {
				case <-r.Context().Done():
				case <-release:
				}
			}))
			defer first.Close()
			second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { secondCalls.Add(1) }))
			defer second.Close()
			model, _, _ := f.addPool(t, geminiAPIKeyProvider, first.URL, second.URL, "gemini-cancel", "gemini-unused")
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() {
				response, err := f.gemini(t, ctx, model, false)
				if response != nil {
					response.Body.Close()
				}
				done <- err
			}()
			select {
			case <-entered:
				cancel()
				close(release)
			case <-time.After(3 * time.Second):
				cancel()
				close(release)
				t.Fatal("first transport was not entered")
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("cancelled request did not return")
			}
			if secondCalls.Load() != 0 {
				t.Fatalf("second calls=%d", secondCalls.Load())
			}
		})
	})

	t.Run("employee permission is not expanded", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer upstream.Close()
		model, _, _ := f.addPool(t, geminiAPIKeyProvider, upstream.URL, upstream.URL, "gemini-denied-first", "gemini-denied-second")
		if _, err := f.app.store.db.Exec(`UPDATE employees SET model_mode='selected',revision=revision+1 WHERE id=?`, f.employee.ID); err != nil {
			t.Fatal(err)
		}
		f.app.notifyAccountPoolChanged()
		response, err := f.gemini(t, context.Background(), model, false)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusForbidden || calls.Load() != 0 {
			t.Fatalf("status=%d calls=%d body=%s", response.StatusCode, calls.Load(), readBody(response))
		}
	})
}
