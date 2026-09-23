package service

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatResponseValidationAndRedaction(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openTestApp(t, dir)
	defer app.Close()
	for _, statement := range []string{
		`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('test-employee','Test','active','all',1,'2026-01-01T00:00:00Z')`,
		`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('test-key','test-employee','Test','test-selector',x'01',1,'test-op','2026-01-01T00:00:00Z')`,
	} {
		if _, err := app.store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	const chunk = "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"id\":\"call_1\",\"function\":{\"arguments\":\"{}\"}}]}}]}\n\n"
	for _, tc := range []struct {
		name, body, outcome, contains string
		stream                        bool
		status                        int
	}{
		{"json tools", `{"choices":[{"message":{"tool_calls":[{"id":"call_1"}]}}]}`, "succeeded", "call_1", false, 200},
		{"json error", `{"error":{"message":"private-provider-token"}}`, "failed", "upstream_error", false, 502},
		{"json malformed", `{"private-provider-token":`, "failed", "upstream_error", false, 502},
		{"json scalar", `"private-provider-token"`, "failed", "upstream_error", false, 502},
		{"stream tools", chunk + "data: [DONE]\n\n", "succeeded", "call_1", true, 200},
		{"first error", "data: {\"error\":{\"message\":\"private-provider-token\"}}\n\n", "failed", "upstream_error", true, 502},
		{"late error", chunk + "event: error\ndata: {\"message\":\"private-provider-token\"}\n\n", "failed", "upstream_error", true, 200},
		{"truncated", chunk + "data: [DONE]", "interrupted", "upstream_error", true, 200},
		{"missing terminal", chunk, "interrupted", "upstream_error", true, 200},
		{"false done in text", "data: {\"choices\":[{\"delta\":{\"content\":\"data: [DONE]\"}}]}\n\n", "interrupted", "upstream_error", true, 200},
		{"oversize first line", "data: " + strings.Repeat("x", chatMaxLine), "failed", "upstream_error", true, 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id, err := newID("request")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.db.Exec(`INSERT INTO model_requests(id,employee_id,key_id,model_id,started_at,outcome) VALUES(?,'test-employee','test-key','test-model',?,'running')`, id, utcNow()); err != nil {
				t.Fatal(err)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			response := &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}
			if tc.stream {
				response.Header.Set("Content-Type", "text/event-stream")
				app.forwardStream(recorder, request, response, id)
			} else {
				response.Header.Set("Content-Type", "application/json")
				app.forwardJSON(recorder, request, response, id)
			}
			if recorder.Code != tc.status || !strings.Contains(recorder.Body.String(), tc.contains) || strings.Contains(recorder.Body.String(), "private-provider-token") {
				t.Fatalf("response status=%d, expected sanitized body containing %q", recorder.Code, tc.contains)
			}
			var outcome string
			if err := app.store.db.QueryRow(`SELECT outcome FROM model_requests WHERE id=?`, id).Scan(&outcome); err != nil {
				t.Fatal(err)
			}
			if outcome != tc.outcome {
				t.Fatalf("outcome=%s want=%s", outcome, tc.outcome)
			}
		})
	}
}

func TestChatSSEMultilineAndEventBoundaries(t *testing.T) {
	reader := bufio.NewReaderSize(strings.NewReader(": keepalive\r\n\r\ndata: {\r\ndata: \"choices\":[]}\r\n\r\ndata: [DONE]\r\n\r\n"), 16)
	frame, done, err := readChatSSEEvent(reader)
	if err != nil || done || !strings.Contains(string(frame), "choices") {
		t.Fatalf("first frame done=%v err=%v", done, err)
	}
	_, done, err = readChatSSEEvent(reader)
	if err != nil || !done {
		t.Fatalf("terminal done=%v err=%v", done, err)
	}
	for _, input := range []string{
		"data: {\"error\":{\"message\":\"private\"}}\n\n",
		strings.Repeat(": bounded comment\n", chatMaxEvent/18+1),
		"data: {}\n", "data: [DONE]\n",
	} {
		if frame, _, err := readChatSSEEvent(bufio.NewReaderSize(strings.NewReader(input), 32)); err == nil || len(frame) != 0 {
			t.Fatal("invalid or incomplete event escaped")
		}
	}
}
