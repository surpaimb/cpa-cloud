package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type anthropicFixture struct {
	app        *App
	server     *httptest.Server
	upstream   *httptest.Server
	employeeID string
	key        keyView
	cookie     *http.Cookie
	csrf       string
}

func newAnthropicFixture(t *testing.T, handler http.Handler) *anthropicFixture {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(handler)
	app := openTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)

	created := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams",
		`{"name":"Anthropic mock","provider_kind":"anthropic-api-key","endpoint":`+quoteJSON(upstream.URL)+`,"api_key":"anthropic-upstream-secret"}`,
		cookie, csrf, server.URL)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create Anthropic upstream status=%d body=%s", created.StatusCode, readBody(created))
	}
	var upstreamObject upstreamView
	decodeResponse(t, created, &upstreamObject)
	model := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models",
		`{"id":"company-claude","upstream_id":`+quoteJSON(upstreamObject.ID)+`,"upstream_model":"claude-provider-model"}`,
		cookie, csrf, server.URL)
	if model.StatusCode != http.StatusCreated {
		t.Fatalf("create Anthropic model status=%d body=%s", model.StatusCode, readBody(model))
	}
	model.Body.Close()
	employeeResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees", `{"name":"Claude user"}`, cookie, csrf, server.URL)
	if employeeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create employee status=%d body=%s", employeeResponse.StatusCode, readBody(employeeResponse))
	}
	var employee employee
	decodeResponse(t, employeeResponse, &employee)
	key := createTestKey(t, server.URL, employee.ID, "anthropic-key-op", cookie, csrf)

	fixture := &anthropicFixture{app: app, server: server, upstream: upstream, employeeID: employee.ID, key: key, cookie: cookie, csrf: csrf}
	t.Cleanup(func() {
		fixture.server.Close()
		_ = fixture.app.Close()
		fixture.upstream.Close()
	})
	return fixture
}

func anthropicEmployeeRequest(t *testing.T, ctx context.Context, method, target, body, key, authKind string) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Anthropic-Version", "2023-06-01")
	request.Header.Set("Anthropic-Beta", "tools-2024-05-16")
	if authKind == "x-api-key" {
		request.Header.Set("X-API-Key", key)
	} else {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func TestAnthropicMessagesPreservesNativePayloadAndCountsTokens(t *testing.T) {
	var mu sync.Mutex
	paths := make([]string, 0, 2)
	fixture := newAnthropicFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer anthropic-upstream-secret" {
			t.Errorf("upstream authorization=%q", got)
		}
		if got := r.Header.Get("X-API-Key"); got != "" {
			t.Errorf("employee x-api-key leaked upstream: %q", got)
		}
		if got := r.Header.Get("Anthropic-Version"); got != "2023-06-01" {
			t.Errorf("anthropic-version=%q", got)
		}
		if got := r.Header.Get("Anthropic-Beta"); got != "tools-2024-05-16" {
			t.Errorf("anthropic-beta=%q", got)
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		var model string
		_ = json.Unmarshal(payload["model"], &model)
		if model != "claude-provider-model" {
			t.Errorf("mapped model=%q", model)
		}
		for _, field := range []string{"system", "messages", "tools", "tool_choice", "thinking"} {
			if _, ok := payload[field]; !ok {
				t.Errorf("native field %q was not preserved", field)
			}
		}
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/messages/count_tokens" {
			_, _ = io.WriteString(w, `{"input_tokens":321}`)
			return
		}
		if r.URL.Path != "/v1/messages" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = io.WriteString(w, `{"id":"msg_mock","type":"message","role":"assistant","model":"claude-provider-model","content":[{"type":"thinking","thinking":"summary","signature":"opaque"},{"type":"tool_use","id":"toolu_mock","name":"weather","input":{"city":"Paris"}}],"stop_reason":"tool_use","usage":{"input_tokens":12,"output_tokens":34}}`)
	}))

	body := `{"model":"company-claude","max_tokens":1024,"system":[{"type":"text","text":"be concise"}],"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_previous","name":"weather","input":{"city":"Paris"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_previous","content":"sunny"}]}],"tools":[{"name":"weather","description":"weather","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"},"thinking":{"type":"adaptive"}}`
	response := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", body, fixture.key.Key, "x-api-key")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("messages status=%d body=%s", response.StatusCode, readBody(response))
	}
	messageBody := readBody(response)
	if !strings.Contains(messageBody, `"type":"tool_use"`) || !strings.Contains(messageBody, `"type":"thinking"`) || !strings.Contains(messageBody, `"input_tokens":12`) {
		t.Fatalf("native response fields missing: %s", messageBody)
	}

	countResponse := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages/count_tokens", body, fixture.key.Key, "bearer")
	if countResponse.StatusCode != http.StatusOK || readBody(countResponse) != `{"input_tokens":321}` {
		t.Fatalf("count_tokens response was not preserved")
	}
	var recorded int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM model_requests`).Scan(&recorded); err != nil {
		t.Fatal(err)
	}
	if recorded != 1 {
		t.Fatalf("generation request records=%d, want 1 (token count is not a generation)", recorded)
	}
	mu.Lock()
	if len(paths) != 2 || paths[0] != "/v1/messages" || paths[1] != "/v1/messages/count_tokens" {
		mu.Unlock()
		t.Fatalf("upstream paths=%v", paths)
	}
	mu.Unlock()

	databaseBytes, err := os.ReadFile(filepath.Join(fixture.app.cfg.DataDir, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(databaseBytes, []byte("anthropic-upstream-secret")) || bytes.Contains(databaseBytes, []byte(fixture.key.Key)) {
		t.Fatal("plaintext credential was found in the SQLite file")
	}

	dataDir := fixture.app.cfg.DataDir
	fixture.server.Close()
	if err := fixture.app.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.app = openTestApp(t, dataDir)
	fixture.server = httptest.NewServer(fixture.app.Handler())
	restarted := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", body, fixture.key.Key, "bearer")
	if restarted.StatusCode != http.StatusOK {
		t.Fatalf("Messages after restart status=%d body=%s", restarted.StatusCode, readBody(restarted))
	}
	restarted.Body.Close()
}

func TestAnthropicMessagesSSETerminationErrorsCancellationAndRevocation(t *testing.T) {
	const streamSecret = "anthropic-stream-secret-sentinel"
	cancelReached := make(chan struct{}, 1)
	fixture := newAnthropicFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&payload)
		var system string
		_ = json.Unmarshal(payload["system"], &system)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		switch system {
		case "cancel":
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
			flusher.Flush()
			<-r.Context().Done()
			cancelReached <- struct{}{}
		case "interrupted":
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")
		case "error":
			_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\""+streamSecret+"\"}}\n\n")
		case "multiline":
			_, _ = io.WriteString(w, "event: message_start\r\ndata: {\"type\":\"message_start\",\r\ndata: \"message\":{\"id\":\"msg_multi\"}}\r\n\r\n")
			_, _ = io.WriteString(w, "event: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n")
		case "split":
			stream := "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_split\",\"name\":\"weather\",\"input\":{}}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			for index := range stream {
				_, _ = io.WriteString(w, stream[index:index+1])
				flusher.Flush()
			}
		case "forged-stop":
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"ping\"}\n\n")
		case "half-frame":
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}")
		case "malformed":
			_, _ = io.WriteString(w, "event: message_stop\ndata: not-json\n\n")
		case "oversized":
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: "+strings.Repeat("x", anthropicMaxSSELine+1)+"\n\n")
		default:
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"thinking\",\"thinking\":\"\",\"signature\":\"\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"signature_delta\",\"signature\":\"opaque\"}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"weather\",\"input\":{}}}\n\n")
			_, _ = io.WriteString(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\n\n")
			_, _ = io.WriteString(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		}
	}))

	request := func(ctx context.Context, marker string) *http.Response {
		return anthropicEmployeeRequest(t, ctx, http.MethodPost, fixture.server.URL+"/v1/messages", `{"model":"company-claude","max_tokens":64,"stream":true,"system":`+quoteJSON(marker)+`,"messages":[{"role":"user","content":"hello"}]}`, fixture.key.Key, "bearer")
	}
	completed := request(context.Background(), "complete")
	completedBody := readBody(completed)
	if !strings.Contains(completedBody, "signature_delta") || !strings.Contains(completedBody, "input_json_delta") || !strings.Contains(completedBody, "event: message_stop") {
		t.Fatalf("SSE capabilities were not preserved: %s", completedBody)
	}
	assertLatestAnthropicOutcome(t, fixture.app, "succeeded")

	interrupted := request(context.Background(), "interrupted")
	_ = readBody(interrupted)
	assertLatestAnthropicOutcome(t, fixture.app, "interrupted")

	errorResponse := request(context.Background(), "error")
	if body := readBody(errorResponse); !strings.Contains(body, "event: error") || !strings.Contains(body, "Upstream request failed") || strings.Contains(body, streamSecret) || strings.Contains(body, "overloaded_error") {
		t.Fatalf("stream error was not safely redacted: %s", body)
	}
	assertLatestAnthropicOutcome(t, fixture.app, "failed")

	multiline := request(context.Background(), "multiline")
	multilineBody := readBody(multiline)
	if !strings.Contains(multilineBody, `"id":"msg_multi"`) || !strings.Contains(multilineBody, "event: message_stop") {
		t.Fatalf("multiline CRLF event was not preserved: %s", multilineBody)
	}
	assertLatestAnthropicOutcome(t, fixture.app, "succeeded")

	split := request(context.Background(), "split")
	splitBody := readBody(split)
	if !strings.Contains(splitBody, "toolu_split") || !strings.Contains(splitBody, "input_json_delta") || !strings.Contains(splitBody, "event: message_stop") {
		t.Fatalf("split tool stream was not preserved: %s", splitBody)
	}
	assertLatestAnthropicOutcome(t, fixture.app, "succeeded")

	for _, marker := range []string{"forged-stop", "half-frame", "malformed", "oversized"} {
		invalid := request(context.Background(), marker)
		_ = readBody(invalid)
		if got := latestAnthropicOutcome(t, fixture.app); got != "failed" {
			t.Fatalf("%s stream outcome=%q want failed", marker, got)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelled := request(ctx, "cancel")
	buffer := make([]byte, 64)
	if _, err := cancelled.Body.Read(buffer); err != nil {
		t.Fatalf("read initial cancellation event: %v", err)
	}
	cancel()
	cancelled.Body.Close()
	select {
	case <-cancelReached:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not reach Anthropic upstream")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if latestAnthropicOutcome(t, fixture.app) == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled request outcome=%q", latestAnthropicOutcome(t, fixture.app))
		}
		time.Sleep(10 * time.Millisecond)
	}

	revoke := requestJSON(t, http.MethodPost, fixture.server.URL+"/admin/api/v1/keys/"+fixture.key.ID+"/revoke", `{}`, fixture.cookie, fixture.csrf, fixture.server.URL)
	if revoke.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", revoke.StatusCode, readBody(revoke))
	}
	revoke.Body.Close()
	rejected := request(context.Background(), "complete")
	if rejected.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked Messages key status=%d body=%s", rejected.StatusCode, readBody(rejected))
	}
	var envelope map[string]any
	decodeResponse(t, rejected, &envelope)
	if envelope["type"] != "error" {
		t.Fatalf("revoked error envelope=%v", envelope)
	}
}

func TestAnthropicMessagesRejectsUnauthorizedRoutesAndRedactsUpstreamFailures(t *testing.T) {
	var calls atomic.Int32
	fixture := newAnthropicFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&payload)
		var system string
		_ = json.Unmarshal(payload["system"], &system)
		switch system {
		case "rate":
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
		case "bad-content":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "not json")
		default:
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":{"message":"secret upstream detail"}}`)
		}
	}))
	body := func(system string) string {
		return `{"model":"company-claude","max_tokens":64,"system":` + quoteJSON(system) + `,"messages":[{"role":"user","content":"hello"}]}`
	}

	authFailure := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", body("auth"), fixture.key.Key, "bearer")
	if authFailure.StatusCode != http.StatusBadGateway {
		t.Fatalf("upstream authentication status=%d body=%s", authFailure.StatusCode, readBody(authFailure))
	}
	if responseBody := readBody(authFailure); strings.Contains(responseBody, "secret upstream detail") || !strings.Contains(responseBody, "Upstream authentication failed") {
		t.Fatalf("upstream authentication error was not redacted: %s", responseBody)
	}

	rateFailure := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", body("rate"), fixture.key.Key, "x-api-key")
	if rateFailure.StatusCode != http.StatusTooManyRequests || rateFailure.Header.Get("Retry-After") != "5" {
		t.Fatalf("rate failure status=%d retry=%q body=%s", rateFailure.StatusCode, rateFailure.Header.Get("Retry-After"), readBody(rateFailure))
	}
	rateFailure.Body.Close()

	badContent := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", body("bad-content"), fixture.key.Key, "bearer")
	if badContent.StatusCode != http.StatusBadGateway || strings.Contains(readBody(badContent), "not json") {
		t.Fatal("invalid upstream content was not rejected with a redacted error")
	}

	beforeLocalRejections := calls.Load()
	missingVersion, _ := http.NewRequest(http.MethodPost, fixture.server.URL+"/v1/messages", strings.NewReader(body("unused")))
	missingVersion.Header.Set("Authorization", "Bearer "+fixture.key.Key)
	missingVersion.Header.Set("Content-Type", "application/json")
	missingVersionResponse, err := http.DefaultClient.Do(missingVersion)
	if err != nil {
		t.Fatal(err)
	}
	if missingVersionResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing version status=%d body=%s", missingVersionResponse.StatusCode, readBody(missingVersionResponse))
	}
	missingVersionResponse.Body.Close()

	bothHeaders, _ := http.NewRequest(http.MethodPost, fixture.server.URL+"/v1/messages", strings.NewReader(body("unused")))
	bothHeaders.Header.Set("Authorization", "Bearer "+fixture.key.Key)
	bothHeaders.Header.Set("X-API-Key", fixture.key.Key)
	bothHeaders.Header.Set("Anthropic-Version", "2023-06-01")
	bothHeaders.Header.Set("Content-Type", "application/json")
	bothHeadersResponse, err := http.DefaultClient.Do(bothHeaders)
	if err != nil {
		t.Fatal(err)
	}
	if bothHeadersResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ambiguous employee credentials status=%d body=%s", bothHeadersResponse.StatusCode, readBody(bothHeadersResponse))
	}
	bothHeadersResponse.Body.Close()

	if _, err := fixture.app.store.db.Exec(`UPDATE employees SET model_mode='selected' WHERE id=?`, fixture.employeeID); err != nil {
		t.Fatal(err)
	}
	notAllowed := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", body("unused"), fixture.key.Key, "bearer")
	if notAllowed.StatusCode != http.StatusForbidden {
		t.Fatalf("model policy status=%d body=%s", notAllowed.StatusCode, readBody(notAllowed))
	}
	notAllowed.Body.Close()
	if calls.Load() != beforeLocalRejections {
		t.Fatalf("locally rejected requests reached upstream: before=%d after=%d", beforeLocalRejections, calls.Load())
	}
}

func latestAnthropicOutcome(t *testing.T, app *App) string {
	t.Helper()
	var outcome string
	if err := app.store.db.QueryRow(`SELECT outcome FROM model_requests WHERE model_id='company-claude' ORDER BY started_at DESC, rowid DESC LIMIT 1`).Scan(&outcome); err != nil {
		t.Fatal(err)
	}
	return outcome
}

func assertLatestAnthropicOutcome(t *testing.T, app *App, want string) {
	t.Helper()
	if got := latestAnthropicOutcome(t, app); got != want {
		t.Fatalf("latest Anthropic outcome=%q want=%q", got, want)
	}
}
