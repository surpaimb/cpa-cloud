package service

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUsageStreamTerminalGeminiWithholdsSuccessOnPersistenceFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"visible delta\"}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"},{\"index\":1,\"content\":{\"parts\":[{\"text\":\"still running\"}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"index\":1,\"finishReason\":\"MAX_TOKENS\"}],\"usageMetadata\":{\"promptTokenCount\":6,\"candidatesTokenCount\":2,\"totalTokenCount\":8}}\n\n")
		_, _ = io.WriteString(w, "data: {\"usageMetadata\":{\"promptTokenCount\":6,\"candidatesTokenCount\":2,\"totalTokenCount\":8}}\n\n")
	}))
	defer upstream.Close()

	server, app, _, _, _, _, key := setupGeminiTest(t, upstream.URL)
	defer server.Close()
	defer app.Close()
	if _, err := app.store.db.Exec(`CREATE TRIGGER reject_terminal_usage
		BEFORE UPDATE OF status ON accounting_attempts
		WHEN NEW.status='succeeded'
		BEGIN SELECT RAISE(ABORT, 'synthetic persistence failure'); END`); err != nil {
		t.Fatal(err)
	}

	response := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:streamGenerateContent?alt=sse",
		`{"contents":[{"parts":[{"text":"hello"}]}]}`, key.Key, context.Background())
	body := readBody(response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if !strings.Contains(body, "visible delta") || !strings.Contains(body, `"status":"UNAVAILABLE"`) {
		t.Fatalf("missing delta or fixed failure: %s", body)
	}
	if strings.Contains(body, "finishReason") || strings.Contains(body, "still running") || strings.Contains(body, "promptTokenCount") || strings.Contains(body, "synthetic persistence failure") {
		t.Fatalf("buffered terminal data or persistence detail escaped: %s", body)
	}
}

func TestUsageStreamTerminalResponsesClassifiesAndAccountsTerminalEvents(t *testing.T) {
	tests := []struct {
		name           string
		stream         string
		wantHTTP       int
		wantOutcome    string
		wantInput      int64
		wantInputKnown bool
	}{
		{
			name:        "explicit failure",
			stream:      "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"secret upstream detail\"}}}\n\n",
			wantHTTP:    http.StatusBadGateway,
			wantOutcome: "failed",
		},
		{
			name: "incomplete with usage",
			stream: "data: {\"type\":\"response.incomplete\",\"response\":{\"id\":\"resp_incomplete\",\"object\":\"response\",\"status\":\"incomplete\",\"output\":[]," +
				"\"usage\":{\"input_tokens\":12,\"output_tokens\":3,\"total_tokens\":15,\"input_tokens_details\":{\"cached_tokens\":2,\"cache_write_tokens\":1},\"output_tokens_details\":{\"reasoning_tokens\":1}}}}\n\n",
			wantHTTP:       http.StatusBadGateway,
			wantOutcome:    "interrupted",
			wantInput:      9,
			wantInputKnown: true,
		},
		{
			name:        "eof after delta",
			stream:      "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n",
			wantHTTP:    http.StatusOK,
			wantOutcome: "interrupted",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app, server, key := newResponsesAPIKeyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, test.stream)
			}))
			response := employeeRequest(t, http.MethodPost, server.URL+"/v1/responses",
				`{"model":"company-responses","stream":true,"input":"hello"}`, key.Key, context.Background())
			body := readBody(response)
			if response.StatusCode != test.wantHTTP || strings.Contains(body, "secret upstream detail") || !strings.Contains(body, "upstream_protocol_error") {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
			requestID := response.Header.Get("X-Request-ID")
			var legacyOutcome, requestStatus, attemptStatus string
			var input sql.NullInt64
			if err := app.store.db.QueryRow(`SELECT outcome FROM model_requests WHERE id=?`, requestID).Scan(&legacyOutcome); err != nil {
				t.Fatal(err)
			}
			if err := app.store.db.QueryRow(`SELECT status FROM accounting_requests WHERE id=?`, requestID).Scan(&requestStatus); err != nil {
				t.Fatal(err)
			}
			if err := app.store.db.QueryRow(`SELECT status,input_tokens FROM accounting_attempts WHERE request_id=?`, requestID).Scan(&attemptStatus, &input); err != nil {
				t.Fatal(err)
			}
			if legacyOutcome != test.wantOutcome || requestStatus != test.wantOutcome || attemptStatus != test.wantOutcome {
				t.Fatalf("outcomes legacy/request/attempt=%q/%q/%q want %q", legacyOutcome, requestStatus, attemptStatus, test.wantOutcome)
			}
			if input.Valid != test.wantInputKnown || input.Valid && input.Int64 != test.wantInput {
				t.Fatalf("input usage=%v want known=%v value=%d", input, test.wantInputKnown, test.wantInput)
			}
		})
	}
}

func TestUsageStreamTerminalResponsesOutcomeDistinguishesCancellationAndEOF(t *testing.T) {
	tests := []struct {
		name       string
		contextErr error
		streamErr  error
		want       string
	}{
		{"cancelled", context.Canceled, errResponsesFailed, "cancelled"},
		{"deadline", context.DeadlineExceeded, io.ErrUnexpectedEOF, "cancelled"},
		{"clean eof", nil, io.EOF, "interrupted"},
		{"truncated eof", nil, io.ErrUnexpectedEOF, "interrupted"},
		{"incomplete", nil, errResponsesIncomplete, "interrupted"},
		{"provider failed", nil, errResponsesFailed, "failed"},
		{"malformed", nil, errors.New("invalid SSE event"), "failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := responsesStreamFailureOutcome(test.contextErr, test.streamErr); got != test.want {
				t.Fatalf("outcome=%q want %q", got, test.want)
			}
		})
	}
}
