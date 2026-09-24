package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

type fakeCodexResponsesExecutor struct {
	fn func(context.Context, *membership.CodexAuthCredential, []byte, func(json.RawMessage) error) (json.RawMessage, *codexRunError)
}

func (f fakeCodexResponsesExecutor) Responses(ctx context.Context, credential *membership.CodexAuthCredential, body []byte, consume func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
	return f.fn(ctx, credential, body, consume)
}

func newResponsesAPIKeyFixture(t *testing.T, upstream http.Handler) (*App, *httptest.Server, keyView) {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	upstreamServer := httptest.NewServer(upstream)
	t.Cleanup(upstreamServer.Close)
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", AllowLoopbackUpstream: true})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() { server.Close(); _ = app.Close() })
	cookie, csrf := loginTestAdmin(t, server.URL)
	u := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams", marshalTestJSON(t, map[string]any{"name": "responses mock", "provider_kind": "openai-compatible", "endpoint": upstreamServer.URL, "api_key": "upstream-secret"}), cookie, csrf, server.URL)
	if u.StatusCode != http.StatusCreated {
		t.Fatalf("create upstream: %d %s", u.StatusCode, readBody(u))
	}
	var upstreamObject upstreamView
	decodeResponse(t, u, &upstreamObject)
	m := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models", marshalTestJSON(t, map[string]any{"id": "company-responses", "upstream_id": upstreamObject.ID, "upstream_model": "provider-responses"}), cookie, csrf, server.URL)
	if m.StatusCode != http.StatusCreated {
		t.Fatalf("create model: %d %s", m.StatusCode, readBody(m))
	}
	m.Body.Close()
	e := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees", `{"name":"Responses User"}`, cookie, csrf, server.URL)
	if e.StatusCode != http.StatusCreated {
		t.Fatalf("create employee: %d %s", e.StatusCode, readBody(e))
	}
	var employeeObject employee
	decodeResponse(t, e, &employeeObject)
	return app, server, createTestKey(t, server.URL, employeeObject.ID, "responses-key-op", cookie, csrf)
}

func TestResponsesAPIKeyPreservesNativeToolsAndResultRound(t *testing.T) {
	var saw atomic.Bool
	_, server, key := newResponsesAPIKeyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("path=%q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		for _, field := range []string{"tools", "instructions", "input", "tool_choice", "parallel_tool_calls", "store"} {
			if _, ok := body[field]; !ok {
				t.Errorf("missing %s", field)
			}
		}
		if string(body["store"]) != "false" || strings.Contains(string(body["input"]), "cpac_") {
			t.Errorf("unsafe body=%s", body)
		}
		var model string
		_ = json.Unmarshal(body["model"], &model)
		if model != "provider-responses" {
			t.Errorf("model=%q", model)
		}
		saw.Store(true)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"weather","arguments":"{\"city\":\"Paris\"}"}],"usage":{"input_tokens":8,"output_tokens":3}}`)
	}))
	body := `{"model":"company-responses","instructions":"be concise","tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}],"tool_choice":"required","parallel_tool_calls":false,"input":[{"type":"function_call_output","call_id":"call_1","output":"sunny"}]}`
	response := employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", body, key.Key, context.Background())
	got := readBody(response)
	if response.StatusCode != 200 || !strings.Contains(got, `"call_id":"call_1"`) || !saw.Load() {
		t.Fatalf("status=%d body=%s", response.StatusCode, got)
	}
}

func TestResponsesAPIKeyStreamingRequiresCompletedAndRedactsFailure(t *testing.T) {
	var mode atomic.Int32
	app, server, key := newResponsesAPIKeyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if mode.Load() == 0 {
			io.WriteString(w, "event: response.output_item.added\r\ndata: {\"type\":\"response.output_item.added\",\r\ndata: \"item\":{\"type\":\"function_call\",\"call_id\":\"call_1\"}}\r\n\r\nevent: response.completed\r\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\r\n\r\n")
		} else if mode.Load() == 1 {
			io.WriteString(w, `data: {"type":"response.failed","response":{"error":{"message":"secret upstream detail"}}}`+"\n\n")
		} else {
			io.WriteString(w, `data: {"type":"response.completed","response":null}`+"\n\n")
		}
	}))
	request := `{"model":"company-responses","stream":true,"input":"hello"}`
	response := employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", request, key.Key, context.Background())
	got := readBody(response)
	if response.StatusCode != 200 || !strings.Contains(got, "response.completed") || !strings.Contains(got, `"call_id":"call_1"`) {
		t.Fatalf("stream status=%d body=%s", response.StatusCode, got)
	}
	mode.Store(1)
	failed := employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", request, key.Key, context.Background())
	failedBody := readBody(failed)
	if strings.Contains(failedBody, "secret upstream detail") || !strings.Contains(failedBody, "upstream_protocol_error") {
		t.Fatalf("failure status=%d body=%s", failed.StatusCode, failedBody)
	}
	mode.Store(2)
	malformed := employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", request, key.Key, context.Background())
	malformedBody := readBody(malformed)
	if malformed.StatusCode != http.StatusBadGateway || !strings.Contains(malformedBody, "upstream_protocol_error") {
		t.Fatalf("malformed status=%d body=%s", malformed.StatusCode, malformedBody)
	}
	var outcome string
	if err := app.store.db.QueryRow(`SELECT outcome FROM model_requests ORDER BY started_at DESC LIMIT 1`).Scan(&outcome); err != nil || outcome == "succeeded" {
		t.Fatalf("outcome=%q err=%v", outcome, err)
	}
}

func TestResponsesTCPResetCancelsIdleUpstreamStream(t *testing.T) {
	cancelReached := make(chan struct{}, 1)
	app, server, key := newResponsesAPIKeyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		initial := `{"type":"response.created","sequence_number":0,"response":{"id":"resp_cancel","object":"response","status":"in_progress","output":[]}}`
		_, _ = fmt.Fprintf(w, "event: response.created\ndata: %s\n\n", initial)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		cancelReached <- struct{}{}
	}))

	connection, err := net.DialTimeout("tcp", strings.TrimPrefix(server.URL, "http://"), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"company-responses","stream":true,"input":"cancel"}`
	if _, err := fmt.Fprintf(connection, "POST /v1/responses HTTP/1.1\r\nHost: synthetic\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: keep-alive\r\n\r\n%s", key.Key, len(body), body); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	wireReader := bufio.NewReader(connection)
	wireResponse, err := http.ReadResponse(wireReader, &http.Request{Method: http.MethodPost})
	if err != nil {
		connection.Close()
		t.Fatal(err)
	}
	reader := bufio.NewReader(wireResponse.Body)
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			connection.Close()
			t.Fatal(readErr)
		}
		if strings.Contains(line, "response.created") {
			break
		}
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			connection.Close()
			t.Fatal(readErr)
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	if err := connection.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	heartbeat, err := reader.ReadString('\n')
	if err != nil || !strings.HasPrefix(heartbeat, ": keep-alive") {
		connection.Close()
		t.Fatalf("idle Responses stream heartbeat=%q err=%v", heartbeat, err)
	}
	if tcp, ok := connection.(*net.TCPConn); ok {
		if err := tcp.SetLinger(0); err != nil {
			connection.Close()
			t.Fatal(err)
		}
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelReached:
	case <-time.After(5 * time.Second):
		t.Fatal("TCP reset did not cancel the idle upstream Responses request")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var outcome string
		if err := app.store.db.QueryRow(`SELECT outcome FROM model_requests ORDER BY started_at DESC LIMIT 1`).Scan(&outcome); err == nil && outcome == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled Responses request did not reach a durable terminal state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestResponsesRejectsStatefulLifecycleBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	_, server, key := newResponsesAPIKeyFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	for _, fragment := range []string{`"background":true`, `"background":null`, `"store":true`, `"store":null`, `"previous_response_id":"resp_other"`, `"conversation":"conv_other"`} {
		response := employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", `{"model":"company-responses","input":"x",`+fragment+`}`, key.Key, context.Background())
		if response.StatusCode != 400 || !strings.Contains(readBody(response), "unsupported_feature") {
			t.Fatalf("fragment=%s status=%d", fragment, response.StatusCode)
		}
	}
	invalidStream := employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", `{"model":"company-responses","input":"x","stream":null}`, key.Key, context.Background())
	if invalidStream.StatusCode != http.StatusBadRequest || !strings.Contains(readBody(invalidStream), "invalid_request_error") {
		t.Fatalf("stream null status=%d", invalidStream.StatusCode)
	}
	if calls.Load() != 0 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
}

func TestResponsesSSERejectsOversizedUnterminatedLine(t *testing.T) {
	err := readResponsesSSE(context.Background(), strings.NewReader("data: "+strings.Repeat("x", responsesMaxEvent+1)), func(responsesSSEEvent) error { return nil })
	if err == nil {
		t.Fatal("oversized unterminated SSE line was accepted")
	}
}

func TestResponsesSSERejectsEventNameMismatchAndInjection(t *testing.T) {
	for _, stream := range []string{
		"event: response.created\ndata: {\"type\":\"response.completed\"}\n\n",
		"data: {\"type\":\"response.created\\r\\nevent: injected\"}\n\n",
	} {
		if err := readResponsesSSE(context.Background(), strings.NewReader(stream), func(responsesSSEEvent) error { return nil }); err == nil {
			t.Fatalf("unsafe event accepted: %q", stream)
		}
	}
}

func TestCodexResponsesExecutorBuffersCompletionUntilStateWrite(t *testing.T) {
	runner := &fakeCodexExecutor{completeFn: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		return codexExecutionResult{}, nil
	}}
	fixture := newCodexServiceFixture(t, runner)
	defer fixture.close()
	fixture.app.responses = fakeCodexResponsesExecutor{fn: func(_ context.Context, _ *membership.CodexAuthCredential, body []byte, consume func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
		if !strings.Contains(string(body), `"model":"gpt-codex-provider"`) {
			t.Errorf("mapped body=%s", body)
		}
		added := json.RawMessage(`{"type":"response.output_item.added","item":{"type":"function_call","call_id":"call_codex"}}`)
		completed := json.RawMessage(`{"type":"response.completed","response":{"id":"resp_codex","object":"response","status":"completed","output":[]}}`)
		if consume != nil {
			if err := consume(added); err != nil {
				return nil, normalizeCodexRunError(err)
			}
			if err := consume(completed); err != nil {
				return nil, normalizeCodexRunError(err)
			}
		}
		return json.RawMessage(`{"id":"resp_codex","object":"response","status":"completed","output":[]}`), nil
	}}
	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"company-codex","stream":true,"input":"hi"}`, fixture.employeeKey.Key, context.Background())
	body := readBody(response)
	if response.StatusCode != 200 || !strings.Contains(body, "call_codex") || !strings.Contains(body, "response.completed") {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	second := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"company-codex","input":"again"}`, fixture.employeeKey.Key, context.Background())
	secondBody := readBody(second)
	if second.StatusCode != http.StatusOK || !strings.Contains(secondBody, `"id":"resp_codex"`) {
		t.Fatalf("second status=%d body=%s", second.StatusCode, secondBody)
	}
	third := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"company-codex","stream":true,"input":"again"}`, fixture.employeeKey.Key, context.Background())
	thirdBody := readBody(third)
	if third.StatusCode != http.StatusOK || !strings.Contains(thirdBody, "response.completed") {
		t.Fatalf("third status=%d body=%s", third.StatusCode, thirdBody)
	}
}

func TestCodexResponsesFailureAfterDeltaUsesNativeErrorEvent(t *testing.T) {
	for _, code := range []membership.CodexAdapterErrorCode{membership.CodexErrorUpstream, membership.CodexErrorReauthentication} {
		t.Run(string(code), func(t *testing.T) {
			fixture := newCodexServiceFixture(t, &fakeCodexExecutor{})
			defer fixture.close()
			fixture.app.responses = fakeCodexResponsesExecutor{fn: func(_ context.Context, _ *membership.CodexAuthCredential, _ []byte, consume func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
				if err := consume(json.RawMessage(`{"type":"response.output_text.delta","delta":"synthetic"}`)); err != nil {
					return nil, normalizeCodexRunError(err)
				}
				return nil, &codexRunError{Code: code, UpstreamStatus: http.StatusUnauthorized}
			}}
			response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"company-codex","stream":true,"input":"x"}`, fixture.employeeKey.Key, context.Background())
			body := readBody(response)
			if response.StatusCode != http.StatusOK || strings.Contains(body, "response.completed") {
				t.Fatalf("unexpected terminal status=%d body=%s", response.StatusCode, body)
			}
			var nativeError bool
			for _, line := range strings.Split(body, "\n") {
				if !strings.HasPrefix(line, "data: ") {
					continue
				}
				var event struct {
					Type    string `json:"type"`
					Code    string `json:"code"`
					Message string `json:"message"`
				}
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
					t.Fatal(err)
				}
				if event.Type == "error" && event.Code != "" && event.Message != "" {
					nativeError = true
				}
			}
			if !nativeError {
				t.Fatalf("missing native error event: %s", body)
			}
		})
	}
}

func TestCodexResponsesReauthenticationCancellationAndRevisionConflict(t *testing.T) {
	runner := &fakeCodexExecutor{completeFn: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		return codexExecutionResult{}, nil
	}}
	fixture := newCodexServiceFixture(t, runner)
	defer fixture.close()
	fixture.app.responses = fakeCodexResponsesExecutor{fn: func(context.Context, *membership.CodexAuthCredential, []byte, func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
		return nil, &codexRunError{Code: membership.CodexErrorReauthentication, UpstreamStatus: http.StatusUnauthorized}
	}}
	unauthorized := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"company-codex","input":"x"}`, fixture.employeeKey.Key, context.Background())
	if unauthorized.StatusCode != http.StatusBadGateway || !strings.Contains(readBody(unauthorized), "upstream_reauthentication_required") {
		t.Fatalf("401 status=%d", unauthorized.StatusCode)
	}
	var state string
	if err := fixture.app.store.db.QueryRow(`SELECT credential_state FROM upstreams WHERE id=?`, fixture.upstream.ID).Scan(&state); err != nil || state != codexStateReauth {
		t.Fatalf("state=%q err=%v", state, err)
	}
	if _, err := fixture.app.store.db.Exec(`UPDATE upstreams SET credential_state=? WHERE id=?`, codexStateImported, fixture.upstream.ID); err != nil {
		t.Fatal(err)
	}

	cancelSeen := make(chan struct{})
	started := make(chan struct{})
	fixture.app.responses = fakeCodexResponsesExecutor{fn: func(ctx context.Context, _ *membership.CodexAuthCredential, _ []byte, _ func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
		close(started)
		<-ctx.Done()
		close(cancelSeen)
		return nil, &codexRunError{Code: membership.CodexErrorCancelled}
	}}
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, fixture.server.URL+"/v1/responses", strings.NewReader(`{"model":"company-codex","stream":true,"input":"x"}`))
	req.Header.Set("Authorization", "Bearer "+fixture.employeeKey.Key)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		response, _ := http.DefaultClient.Do(req)
		if response != nil {
			response.Body.Close()
		}
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach Responses executor")
	}
	cancel()
	select {
	case <-cancelSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not reach Responses executor")
	}
	<-done

	fixture.app.responses = fakeCodexResponsesExecutor{fn: func(_ context.Context, _ *membership.CodexAuthCredential, _ []byte, consume func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
		if _, err := fixture.app.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id=?`, fixture.upstream.ID); err != nil {
			t.Error(err)
		}
		if err := consume(json.RawMessage(`{"type":"response.output_item.added","item":{"type":"message"}}`)); err != nil {
			return nil, normalizeCodexRunError(err)
		}
		if err := consume(json.RawMessage(`{"type":"response.completed","response":{"id":"resp_stale","object":"response","status":"completed","output":[]}}`)); err != nil {
			return nil, normalizeCodexRunError(err)
		}
		return json.RawMessage(`{"id":"resp_stale","object":"response","status":"completed","output":[]}`), nil
	}}
	stale := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"company-codex","stream":true,"input":"x"}`, fixture.employeeKey.Key, context.Background())
	staleBody := readBody(stale)
	if strings.Contains(staleBody, "response.completed") || !strings.Contains(staleBody, "storage_unavailable") {
		t.Fatalf("stale status=%d body=%s", stale.StatusCode, staleBody)
	}
}
