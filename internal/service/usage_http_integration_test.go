package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

type usageHTTPRow struct {
	provider, status                     string
	input, output, cacheRead, cacheWrite sql.NullInt64
	cost                                 sql.NullInt64
}

func TestUsageHTTPProtocolJSONAndSSE(t *testing.T) {
	t.Run("openai chat", testUsageHTTPChat)
	t.Run("responses", testUsageHTTPResponses)
	t.Run("anthropic", testUsageHTTPAnthropic)
	t.Run("gemini", testUsageHTTPGemini)
}

func testUsageHTTPChat(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if !payload.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"chat_json","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":2,"cache_write_tokens":1}}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		snapshot := `{"id":"chat_stream","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":2}}}`
		_, _ = io.WriteString(w, "data: "+snapshot+"\n\ndata: "+snapshot+"\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	app, server, key, _ := newUsageHTTPChatFixture(t, upstream.URL)
	for _, body := range []string{
		`{"model":"usage-chat","messages":[]}`,
		`{"model":"usage-chat","stream":true,"messages":[]}`,
	} {
		response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", body, key.Key, context.Background())
		responseBody := readBody(response)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("chat status=%d body=%s", response.StatusCode, responseBody)
		}
	}
	assertUsageHTTPRows(t, app, "openai-compatible", [][4]int64{{7, 2, 2, 1}, {14, 5, 4, 2}})
}

func testUsageHTTPResponses(t *testing.T) {
	app, server, key := newResponsesAPIKeyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if !payload.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"resp_json","object":"response","status":"completed","output":[],"usage":{"input_tokens":90,"output_tokens":40,"total_tokens":130,"input_tokens_details":{"cached_tokens":25,"cache_write_tokens":5}}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"x\"}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_stream\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":50,\"output_tokens\":20,\"total_tokens\":70,\"input_tokens_details\":{\"cached_tokens\":10,\"cache_write_tokens\":5}}}}\n\n")
	}))
	for _, body := range []string{
		`{"model":"company-responses","input":"json"}`,
		`{"model":"company-responses","stream":true,"input":"stream"}`,
	} {
		response := employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", body, key.Key, context.Background())
		responseBody := readBody(response)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("responses status=%d body=%s", response.StatusCode, responseBody)
		}
	}
	assertUsageHTTPRows(t, app, "openai-compatible", [][4]int64{{60, 40, 25, 5}, {35, 20, 10, 5}})
}

func testUsageHTTPAnthropic(t *testing.T) {
	fixture := newAnthropicFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if !payload.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"msg_json","type":"message","role":"assistant","content":[],"usage":{"input_tokens":11,"output_tokens":12,"cache_read_input_tokens":13,"cache_creation_input_tokens":14}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":25,\"output_tokens\":1,\"cache_read_input_tokens\":100,\"cache_creation_input_tokens\":50}}}\n\n"+
			": keep-alive\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":15}}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":15}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	}))
	for _, body := range []string{
		`{"model":"company-claude","max_tokens":8,"messages":[]}`,
		`{"model":"company-claude","max_tokens":8,"stream":true,"messages":[]}`,
	} {
		response := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", body, fixture.key.Key, "bearer")
		responseBody := readBody(response)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("anthropic status=%d body=%s", response.StatusCode, responseBody)
		}
	}
	assertUsageHTTPRows(t, fixture.app, "anthropic", [][4]int64{{11, 12, 13, 14}, {25, 15, 100, 50}})
}

func testUsageHTTPGemini(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			snapshot := `{"candidates":[{"index":0,"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":30,"cachedContentTokenCount":6,"candidatesTokenCount":7,"thoughtsTokenCount":3,"totalTokenCount":40}}`
			_, _ = io.WriteString(w, "data: "+snapshot+"\n\ndata: "+snapshot+"\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":20,"candidatesTokenCount":30,"thoughtsTokenCount":10,"totalTokenCount":140}}`)
	}))
	defer upstream.Close()
	server, app, _, _, _, _, key := setupGeminiTest(t, upstream.URL)
	defer func() { server.Close(); _ = app.Close() }()
	for _, path := range []string{
		"/v1beta/models/company-gemini:generateContent",
		"/v1beta/models/company-gemini:streamGenerateContent?alt=sse",
	} {
		response := employeeRequest(t, http.MethodPost, server.URL+path, `{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`, key.Key, context.Background())
		responseBody := readBody(response)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("gemini status=%d body=%s", response.StatusCode, responseBody)
		}
	}
	assertUsageHTTPRows(t, app, "gemini", [][4]int64{{80, 40, 20, 0}, {24, 10, 6, 0}})
}

func TestUsageHTTPCodexChatAndResponses(t *testing.T) {
	input, cached, output, reasoning, total := int64(30), int64(5), int64(7), int64(2), int64(37)
	runner := &fakeCodexExecutor{completeFn: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		return codexExecutionResult{Text: "ok", Usage: membership.CodexUsage{InputTokens: &input, CachedInputTokens: &cached, OutputTokens: &output, ReasoningOutputTokens: &reasoning, TotalTokens: &total}}, nil
	}}
	fixture := newCodexServiceFixture(t, runner)
	defer fixture.close()
	fixture.app.responses = fakeCodexResponsesExecutor{fn: func(context.Context, *membership.CodexAuthCredential, []byte, func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
		return json.RawMessage(`{"id":"resp_usage","object":"response","status":"completed","output":[],"usage":{"input_tokens":40,"output_tokens":10,"total_tokens":50,"input_tokens_details":{"cached_tokens":8,"cache_write_tokens":2}}}`), nil
	}}
	chat := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", `{"model":"company-codex","messages":[{"role":"user","content":"usage"}]}`, fixture.employeeKey.Key, context.Background())
	chatBody := readBody(chat)
	if chat.StatusCode != http.StatusOK {
		t.Fatalf("Codex chat status=%d body=%s", chat.StatusCode, chatBody)
	}
	responses := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"company-codex","input":"x"}`, fixture.employeeKey.Key, context.Background())
	responsesBody := readBody(responses)
	if responses.StatusCode != http.StatusOK {
		t.Fatalf("Codex Responses status=%d body=%s", responses.StatusCode, responsesBody)
	}
	rows := usageHTTPRows(t, fixture.app)
	if len(rows) != 2 || rows[0].provider != "codex" || rows[1].provider != "codex" {
		t.Fatalf("Codex usage rows=%+v", rows)
	}
	// Chat cannot distinguish cache-write tokens, so ordinary input remains
	// unknown while the provider's cache-read snapshot is retained.
	want := map[string][4]*int64{
		"chat":      {nil, int64Ptr(7), int64Ptr(5), nil},
		"responses": {int64Ptr(30), int64Ptr(10), int64Ptr(8), int64Ptr(2)},
	}
	matched := map[string]bool{}
	for _, row := range rows {
		kind := "chat"
		if row.input.Valid {
			kind = "responses"
		}
		assertUsageHTTPNullable(t, row, want[kind])
		matched[kind] = true
	}
	if !matched["chat"] || !matched["responses"] {
		t.Fatalf("Codex protocol usage was not separated: %+v", rows)
	}
	assertUsageHTTPActiveCleared(t, fixture.app)
}

func TestUsageHTTPAttemptAndFinishPersistenceFailures(t *testing.T) {
	t.Run("begin attempt blocks upstream", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer upstream.Close()
		app, server, key, _ := newUsageHTTPChatFixture(t, upstream.URL)
		mustUsageHTTPExec(t, app, `CREATE TRIGGER usage_fail_begin BEFORE INSERT ON accounting_attempts BEGIN SELECT RAISE(ABORT,'synthetic begin failure'); END`)
		response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"usage-chat","messages":[]}`, key.Key, context.Background())
		body := readBody(response)
		if response.StatusCode != http.StatusServiceUnavailable || calls.Load() != 0 || !strings.Contains(body, "storage_unavailable") {
			t.Fatalf("status=%d calls=%d body=%s", response.StatusCode, calls.Load(), body)
		}
		assertUsageHTTPCounts(t, app, 1, 0)
	})

	t.Run("finish failure hides JSON success and recovers interrupted", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"must_not_escape","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
		}))
		defer upstream.Close()
		app, server, key, dataDir := newUsageHTTPChatFixture(t, upstream.URL)
		mustUsageHTTPExec(t, app, `CREATE TRIGGER usage_fail_finish BEFORE UPDATE OF status ON accounting_attempts WHEN NEW.status<>'pending' BEGIN SELECT RAISE(ABORT,'synthetic finish failure'); END`)
		response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"usage-chat","messages":[]}`, key.Key, context.Background())
		body := readBody(response)
		if response.StatusCode != http.StatusServiceUnavailable || strings.Contains(body, "must_not_escape") || !strings.Contains(body, "storage_unavailable") {
			t.Fatalf("status=%d body=%s", response.StatusCode, body)
		}
		mustUsageHTTPExec(t, app, `DROP TRIGGER usage_fail_finish`)
		server.Close()
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
		restarted := openTestApp(t, dataDir)
		defer restarted.Close()
		var requestStatus, attemptStatus string
		if err := restarted.store.db.QueryRow(`SELECT status FROM accounting_requests LIMIT 1`).Scan(&requestStatus); err != nil {
			t.Fatal(err)
		}
		if err := restarted.store.db.QueryRow(`SELECT status FROM accounting_attempts LIMIT 1`).Scan(&attemptStatus); err != nil {
			t.Fatal(err)
		}
		if requestStatus != "interrupted" || attemptStatus != "interrupted" {
			t.Fatalf("recovered statuses request=%q attempt=%q", requestStatus, attemptStatus)
		}
	})

	t.Run("finish failure replaces SSE terminal", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"partial\",\"choices\":[]}\n\ndata: [DONE]\n\n")
		}))
		defer upstream.Close()
		app, server, key, _ := newUsageHTTPChatFixture(t, upstream.URL)
		mustUsageHTTPExec(t, app, `CREATE TRIGGER usage_fail_stream_finish BEFORE UPDATE OF status ON accounting_attempts WHEN NEW.status<>'pending' BEGIN SELECT RAISE(ABORT,'synthetic stream finish failure'); END`)
		response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"usage-chat","stream":true,"messages":[]}`, key.Key, context.Background())
		body := readBody(response)
		if strings.Contains(body, "[DONE]") || !strings.Contains(body, "storage_unavailable") || !strings.Contains(body, "partial") {
			t.Fatalf("stream status=%d body=%s", response.StatusCode, body)
		}
		assertUsageHTTPActiveCleared(t, app)
	})

	t.Run("legacy request failure rolls back all ledgers", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"must_not_escape_legacy","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`)
		}))
		defer upstream.Close()
		app, server, key, dataDir := newUsageHTTPChatFixture(t, upstream.URL)
		mustUsageHTTPExec(t, app, `CREATE TRIGGER usage_fail_legacy_finish BEFORE UPDATE OF outcome ON model_requests WHEN NEW.outcome<>'running' BEGIN SELECT RAISE(ABORT,'synthetic legacy finish failure'); END`)
		response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"usage-chat","messages":[]}`, key.Key, context.Background())
		body := readBody(response)
		if response.StatusCode != http.StatusServiceUnavailable || strings.Contains(body, "must_not_escape_legacy") || !strings.Contains(body, "storage_unavailable") {
			t.Fatalf("status=%d body=%s", response.StatusCode, body)
		}
		assertUsageHTTPStatuses(t, app, "pending", "pending", "running")
		mustUsageHTTPExec(t, app, `DROP TRIGGER usage_fail_legacy_finish`)
		server.Close()
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
		restarted := openTestApp(t, dataDir)
		defer restarted.Close()
		assertUsageHTTPStatuses(t, restarted, "interrupted", "interrupted", "interrupted")
	})
}

func TestUsageHTTPFailureCancellationAndPreAttemptRejection(t *testing.T) {
	var mode atomic.Int32
	cancelStarted := make(chan struct{}, 1)
	cancelReached := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 0:
			w.WriteHeader(http.StatusBadRequest)
		case 1:
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			cancelStarted <- struct{}{}
			<-r.Context().Done()
			cancelReached <- struct{}{}
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"unexpected","choices":[]}`)
		}
	}))
	defer upstream.Close()
	app, server, key, _ := newUsageHTTPChatFixture(t, upstream.URL)

	failed := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"usage-chat","messages":[]}`, key.Key, context.Background())
	if failed.StatusCode != http.StatusBadGateway {
		t.Fatalf("upstream failure status=%d body=%s", failed.StatusCode, readBody(failed))
	}
	failed.Body.Close()
	rows := usageHTTPRows(t, app)
	if len(rows) != 1 || rows[0].status != "failed" || rows[0].input.Valid || rows[0].cost.Valid {
		t.Fatalf("unknown failed usage=%+v", rows)
	}
	assertUsageHTTPActiveCleared(t, app)

	beforeRequests, beforeAttempts := usageHTTPCounts(t, app)
	if _, err := app.store.db.Exec(`UPDATE upstreams SET credential_ciphertext=X'00' WHERE id=(SELECT upstream_id FROM models WHERE id='usage-chat')`); err != nil {
		t.Fatal(err)
	}
	prepared := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"usage-chat","messages":[]}`, key.Key, context.Background())
	if prepared.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("preparation failure status=%d body=%s", prepared.StatusCode, readBody(prepared))
	}
	prepared.Body.Close()
	afterRequests, afterAttempts := usageHTTPCounts(t, app)
	if afterRequests != beforeRequests+1 || afterAttempts != beforeAttempts {
		t.Fatalf("preparation counts before=%d/%d after=%d/%d", beforeRequests, beforeAttempts, afterRequests, afterAttempts)
	}

	if _, err := app.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id=?`, utcNow(), key.ID); err != nil {
		t.Fatal(err)
	}
	revoked := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"usage-chat","messages":[]}`, key.Key, context.Background())
	if revoked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked status=%d body=%s", revoked.StatusCode, readBody(revoked))
	}
	revoked.Body.Close()
	finalRequests, finalAttempts := usageHTTPCounts(t, app)
	if finalRequests != afterRequests || finalAttempts != afterAttempts {
		t.Fatalf("revoked key added ledger rows: %d/%d -> %d/%d", afterRequests, afterAttempts, finalRequests, finalAttempts)
	}

	mode.Store(1)
	cancelApp, cancelServer, cancelKey, _ := newUsageHTTPChatFixture(t, upstream.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, cancelServer.URL+"/v1/chat/completions", strings.NewReader(`{"model":"usage-chat","messages":[]}`))
	request.Header.Set("Authorization", "Bearer "+cancelKey.Key)
	request.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() { _, err := http.DefaultClient.Do(request); done <- err }()
	select {
	case <-cancelStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled request did not reach upstream")
	}
	cancel()
	select {
	case <-cancelReached:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not reach upstream")
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled client request unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled client request did not return")
	}
	waitUsageHTTPStatus(t, cancelApp, "cancelled")
	assertUsageHTTPActiveCleared(t, cancelApp)

	var mismatchCalls atomic.Int32
	anthropic := newAnthropicFixture(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { mismatchCalls.Add(1) }))
	mismatch := employeeRequest(t, http.MethodPost, anthropic.server.URL+"/v1/chat/completions", `{"model":"company-claude","messages":[]}`, anthropic.key.Key, context.Background())
	if mismatch.StatusCode != http.StatusServiceUnavailable || mismatchCalls.Load() != 0 {
		t.Fatalf("provider mismatch status=%d calls=%d body=%s", mismatch.StatusCode, mismatchCalls.Load(), readBody(mismatch))
	}
	mismatch.Body.Close()
	assertUsageHTTPCounts(t, anthropic.app, 0, 0)
}

func TestUsageHTTPRejectsSuccessfulErrorEnvelopes(t *testing.T) {
	const privateDetail = "private-provider-error-detail"
	t.Run("anthropic", func(t *testing.T) {
		fixture := newAnthropicFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"`+privateDetail+`"}}`)
		}))
		response := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", `{"model":"company-claude","max_tokens":8,"messages":[]}`, fixture.key.Key, "bearer")
		body := readBody(response)
		if response.StatusCode != http.StatusBadGateway || strings.Contains(body, privateDetail) {
			t.Fatalf("Anthropic status=%d body=%s", response.StatusCode, body)
		}
		assertUsageHTTPUnknownFailure(t, fixture.app, "anthropic")
	})

	t.Run("gemini", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"error":{"code":400,"message":"`+privateDetail+`","status":"INVALID_ARGUMENT"}}`)
		}))
		defer upstream.Close()
		server, app, _, _, _, _, key := setupGeminiTest(t, upstream.URL)
		defer func() { server.Close(); _ = app.Close() }()
		response := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:generateContent", `{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`, key.Key, context.Background())
		body := readBody(response)
		if response.StatusCode != http.StatusBadGateway || strings.Contains(body, privateDetail) {
			t.Fatalf("Gemini status=%d body=%s", response.StatusCode, body)
		}
		assertUsageHTTPUnknownFailure(t, app, "gemini")
	})
}

func newUsageHTTPChatFixture(t *testing.T, upstreamURL string) (*App, *httptest.Server, keyView, string) {
	t.Helper()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	account := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "usage upstream", "openai-compatible", upstreamURL, "usage-secret")
	createModelAdmissionModel(t, server.URL, cookie, csrf, "usage-chat", account.ID, "provider-usage")
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Usage Employee")
	key := createTestKey(t, server.URL, employee.ID, "usage-key", cookie, csrf)
	return app, server, key, app.cfg.DataDir
}

func usageHTTPRows(t *testing.T, app *App) []usageHTTPRow {
	t.Helper()
	rows, err := app.store.db.Query(`SELECT provider,status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cost_micro FROM accounting_attempts ORDER BY started_at,id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	result := []usageHTTPRow{}
	for rows.Next() {
		var row usageHTTPRow
		if err := rows.Scan(&row.provider, &row.status, &row.input, &row.output, &row.cacheRead, &row.cacheWrite, &row.cost); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertUsageHTTPRows(t *testing.T, app *App, provider string, expected [][4]int64) {
	t.Helper()
	rows := usageHTTPRows(t, app)
	requests, attempts := usageHTTPCounts(t, app)
	if requests != len(expected) || attempts != len(expected) || len(rows) != len(expected) {
		t.Fatalf("ledger counts requests=%d attempts=%d rows=%d want=%d", requests, attempts, len(rows), len(expected))
	}
	remaining := append([][4]int64(nil), expected...)
	for _, row := range rows {
		if row.provider != provider || row.status != "succeeded" || row.cost.Valid || !row.input.Valid || !row.output.Valid || !row.cacheRead.Valid || !row.cacheWrite.Valid {
			t.Fatalf("unexpected usage row=%+v", row)
		}
		actual := [4]int64{row.input.Int64, row.output.Int64, row.cacheRead.Int64, row.cacheWrite.Int64}
		found := -1
		for index, candidate := range remaining {
			if actual == candidate {
				found = index
				break
			}
		}
		if found < 0 {
			t.Fatalf("unexpected normalized usage=%v expected=%v", actual, expected)
		}
		remaining = append(remaining[:found], remaining[found+1:]...)
	}
	assertUsageHTTPActiveCleared(t, app)
}

func assertUsageHTTPNullable(t *testing.T, row usageHTTPRow, expected [4]*int64) {
	t.Helper()
	actual := []sql.NullInt64{row.input, row.output, row.cacheRead, row.cacheWrite}
	for index := range actual {
		if expected[index] == nil {
			if actual[index].Valid {
				t.Fatalf("usage field %d=%v want NULL", index, actual[index])
			}
		} else if !actual[index].Valid || actual[index].Int64 != *expected[index] {
			t.Fatalf("usage field %d=%v want=%d", index, actual[index], *expected[index])
		}
	}
	if row.status != "succeeded" || row.cost.Valid {
		t.Fatalf("usage status/cost=%q/%v", row.status, row.cost)
	}
}

func usageHTTPCounts(t *testing.T, app *App) (int, int) {
	t.Helper()
	var requests, attempts int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_requests`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	return requests, attempts
}

func assertUsageHTTPCounts(t *testing.T, app *App, wantRequests, wantAttempts int) {
	t.Helper()
	requests, attempts := usageHTTPCounts(t, app)
	if requests != wantRequests || attempts != wantAttempts {
		t.Fatalf("ledger counts=%d/%d want=%d/%d", requests, attempts, wantRequests, wantAttempts)
	}
	assertUsageHTTPActiveCleared(t, app)
}

func assertUsageHTTPUnknownFailure(t *testing.T, app *App, provider string) {
	t.Helper()
	rows := usageHTTPRows(t, app)
	if len(rows) != 1 || rows[0].provider != provider || rows[0].status != "failed" || rows[0].input.Valid || rows[0].output.Valid || rows[0].cacheRead.Valid || rows[0].cacheWrite.Valid || rows[0].cost.Valid {
		t.Fatalf("unknown failure row=%+v", rows)
	}
	assertUsageHTTPCounts(t, app, 1, 1)
}

func assertUsageHTTPStatuses(t *testing.T, app *App, requestStatus, attemptStatus, legacyStatus string) {
	t.Helper()
	var actualRequest, actualAttempt, actualLegacy string
	if err := app.store.db.QueryRow(`SELECT status FROM accounting_requests LIMIT 1`).Scan(&actualRequest); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT status FROM accounting_attempts LIMIT 1`).Scan(&actualAttempt); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT outcome FROM model_requests LIMIT 1`).Scan(&actualLegacy); err != nil {
		t.Fatal(err)
	}
	if actualRequest != requestStatus || actualAttempt != attemptStatus || actualLegacy != legacyStatus {
		t.Fatalf("ledger statuses request=%q attempt=%q legacy=%q want=%q/%q/%q", actualRequest, actualAttempt, actualLegacy, requestStatus, attemptStatus, legacyStatus)
	}
	assertUsageHTTPActiveCleared(t, app)
}

func assertUsageHTTPActiveCleared(t *testing.T, app *App) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		active := 0
		app.usageRequests.Range(func(any, any) bool {
			active++
			return true
		})
		if active == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("active usage request entries=%d", active)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func mustUsageHTTPExec(t *testing.T, app *App, statement string) {
	t.Helper()
	if _, err := app.store.db.Exec(statement); err != nil {
		t.Fatal(err)
	}
}

func waitUsageHTTPStatus(t *testing.T, app *App, status string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var count int
		err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE status=?`, status).Scan(&count)
		if err == nil && count > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("attempt status %q not observed: %v", status, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func int64Ptr(value int64) *int64 { return &value }
