package membership

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const (
	testAccessTokenMarker  = "access-token-secret-marker"
	testRefreshTokenMarker = "refresh-token-secret-marker"
	testAccountIDMarker    = "account-secret-marker"
	testPromptMarker       = "prompt-secret-marker"
	testResponseMarker     = "response-secret-marker"
)

func TestCodexDirectAdapterCompleteUsesPinnedMinimalProtocol(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	credential := testCodexCredential(t, now.Add(time.Hour), testAccountIDMarker)
	requestChecked := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer func() { requestChecked <- struct{}{} }()
		if request.Method != http.MethodPost || request.URL.Path != "/backend-api/codex/responses" {
			t.Errorf("unexpected request target: %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+credential.AccessTokenSecret() {
			t.Errorf("unexpected authorization header")
		}
		if got := request.Header.Get("ChatGPT-Account-ID"); got != testAccountIDMarker {
			t.Errorf("unexpected account header")
		}
		if request.Header.Get("Accept") != "text/event-stream" || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content negotiation headers")
		}

		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			return
		}
		var wire map[string]interface{}
		if err := json.Unmarshal(body, &wire); err != nil {
			t.Errorf("decode body: %v", err)
			return
		}
		expectedKeys := []string{"access_programs", "include", "input", "instructions", "model", "parallel_tool_calls", "prompt_cache_key", "reasoning", "store", "stream", "tool_choice"}
		if len(wire) != len(expectedKeys) {
			t.Errorf("wire body has unexpected fields: %v", sortedMapKeys(wire))
		}
		for _, key := range expectedKeys {
			if _, ok := wire[key]; !ok {
				t.Errorf("wire body missing %q", key)
			}
		}
		for _, omitted := range []string{"tools", "stream_options", "service_tier", "text", "client_metadata"} {
			if _, ok := wire[omitted]; ok {
				t.Errorf("wire body unexpectedly contains %q", omitted)
			}
		}
		if wire["model"] != "gpt-test" || wire["instructions"] != "" || wire["tool_choice"] != "auto" || wire["stream"] != true || wire["store"] != false || wire["parallel_tool_calls"] != false {
			t.Errorf("wire body does not match pinned scalar fields")
		}
		if wire["reasoning"] != nil || wire["access_programs"] != nil {
			t.Errorf("nullable fields must be encoded as null")
		}
		cacheKey, ok := wire["prompt_cache_key"].(string)
		if !ok || len(cacheKey) != 32 {
			t.Errorf("prompt_cache_key is not a random 128-bit hex identifier")
		}
		input, ok := wire["input"].([]interface{})
		if !ok || len(input) != 2 {
			t.Errorf("unexpected input sequence")
		} else {
			assertCodexWireMessage(t, input[0], "user", "input_text", testPromptMarker)
			assertCodexWireMessage(t, input[1], "assistant", "output_text", "prior answer")
		}

		writer.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		chunks := []string{
			"event: response.created\ndata: {\"type\":\"response.cre",
			"ated\",\"response\":{\"id\":\"resp_test\"}}\n\n",
			": keepalive\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"response-secret-\"}\n\n",
			"data: {\"type\":\"response.output_text.delta\",\"delta\":\"marker\"}\n\n",
			"data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"response-secret-marker\"}]}}\n\n",
			"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"usage\":{\"input_tokens\":3,\"input_tokens_details\":{\"cached_tokens\":1},\"output_tokens\":2,\"output_tokens_details\":{\"reasoning_tokens\":1},\"total_tokens\":5}}}\n\n",
		}
		for _, chunk := range chunks {
			_, _ = io.WriteString(writer, chunk)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}))
	defer server.Close()

	adapter := newCodexDirectAdapter(server.URL+"/backend-api/codex/responses", server.Client().Transport, time.Second)
	adapter.now = func() time.Time { return now }
	adapter.rand = bytes.NewReader(bytes.Repeat([]byte{0xab}, 16))
	result, err := adapter.Complete(context.Background(), credential, CodexTextRequest{
		Model: "gpt-test",
		Messages: []CodexTextMessage{
			{Role: CodexRoleUser, Text: testPromptMarker},
			{Role: CodexRoleAssistant, Text: "prior answer"},
		},
	})
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	<-requestChecked
	if result.Text() != testResponseMarker || result.ResponseID() != "resp_test" {
		t.Fatalf("unexpected result through explicit accessors")
	}
	usage := result.Usage()
	assertInt64Pointer(t, usage.InputTokens, 3)
	assertInt64Pointer(t, usage.CachedInputTokens, 1)
	assertInt64Pointer(t, usage.OutputTokens, 2)
	assertInt64Pointer(t, usage.ReasoningOutputTokens, 1)
	assertInt64Pointer(t, usage.TotalTokens, 5)
	if result.UnknownEventCount() != 0 {
		t.Fatalf("unexpected unknown event count")
	}
}

func TestCodexDirectAdapterStreamEmitsNeutralEvents(t *testing.T) {
	adapter, credential, closeServer := testCodexSSEAdapter(t, strings.Join([]string{
		`data: {"type":"response.created","response":{"id":"resp_stream"}}`,
		"",
		`data: {"type":"response.output_text.delta","delta":"hello"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp_stream","usage":{"input_tokens":1}}}`,
		"",
	}, "\n"))
	defer closeServer()

	var events []CodexStreamEvent
	err := adapter.Stream(context.Background(), credential, testCodexTextRequest(), func(event CodexStreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream returned error: %v", err)
	}
	wantKinds := []CodexEventKind{CodexEventStarted, CodexEventTextDelta, CodexEventUsage, CodexEventCompleted}
	if len(events) != len(wantKinds) {
		t.Fatalf("event count = %d, want %d", len(events), len(wantKinds))
	}
	for index, want := range wantKinds {
		if events[index].Kind() != want {
			t.Fatalf("event %d kind = %q, want %q", index, events[index].Kind(), want)
		}
	}
	if events[1].Text() != "hello" || events[0].ResponseID() != "resp_stream" {
		t.Fatalf("explicit stream accessors lost data")
	}
}

func TestCodexDirectAdapterCompletedUsageFieldsRemainUnknown(t *testing.T) {
	adapter, credential, closeServer := testCodexSSEAdapter(t, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp\"}}\n\n")
	defer closeServer()
	result, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	usage := result.Usage()
	if usage.InputTokens != nil || usage.CachedInputTokens != nil || usage.OutputTokens != nil || usage.ReasoningOutputTokens != nil || usage.TotalTokens != nil {
		t.Fatalf("missing usage fields were converted to values: %+v", usage)
	}
}

func TestCodexDirectAdapterRequiresCompletedEvent(t *testing.T) {
	adapter, credential, closeServer := testCodexSSEAdapter(t, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")
	defer closeServer()
	result, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
	if result != nil {
		t.Fatalf("partial result must not be returned as success")
	}
	assertCodexAdapterError(t, err, CodexErrorProtocol)
}

func TestCodexDirectAdapterUnknownAndEmptyEventsDoNotComplete(t *testing.T) {
	stream := strings.Join([]string{
		"data:",
		"",
		`data: {"type":"future.event","secret":"ignored"}`,
		"",
		`data: {"type":"response.completed","response":{"id":"resp"}}`,
		"",
	}, "\n")
	adapter, credential, closeServer := testCodexSSEAdapter(t, stream)
	defer closeServer()
	result, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
	if err != nil {
		t.Fatalf("Complete returned error: %v", err)
	}
	if result.UnknownEventCount() != 1 {
		t.Fatalf("unknown event count = %d, want 1", result.UnknownEventCount())
	}
}

func TestCodexDirectAdapterTerminalFailureEventsAreRedacted(t *testing.T) {
	tests := []struct {
		name         string
		stream       string
		providerCode string
	}{
		{"failed", `data: {"type":"response.failed","response":{"error":{"code":"server_error","message":"response-secret-marker"}}}` + "\n\n", "server_error"},
		{"incomplete", `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n", "max_output_tokens"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter, credential, closeServer := testCodexSSEAdapter(t, test.stream)
			defer closeServer()
			_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
			assertCodexAdapterError(t, err, CodexErrorUpstream)
			var adapterError *CodexAdapterError
			if !errors.As(err, &adapterError) || adapterError.ProviderCode() != test.providerCode {
				t.Fatalf("provider code not retained safely")
			}
			if strings.Contains(fmt.Sprintf("%v", err), testResponseMarker) {
				t.Fatalf("upstream message leaked through error")
			}
		})
	}
}

func TestCodexDirectAdapterRejectsUnsupportedOutputItem(t *testing.T) {
	adapter, credential, closeServer := testCodexSSEAdapter(t, `data: {"type":"response.output_item.done","item":{"type":"function_call","name":"danger"}}`+"\n\n")
	defer closeServer()
	_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
	assertCodexAdapterError(t, err, CodexErrorUnsupportedFeature)
}

func TestCodexDirectAdapterFinalItemFallbackAndConsistency(t *testing.T) {
	t.Run("final item fallback", func(t *testing.T) {
		stream := `data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"response-secret-marker"}]}}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp"}}` + "\n\n"
		adapter, credential, closeServer := testCodexSSEAdapter(t, stream)
		defer closeServer()
		result, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
		if err != nil || result.Text() != testResponseMarker {
			t.Fatalf("final item fallback failed: result=%v error=%v", result, err)
		}
	})

	t.Run("delta and final mismatch", func(t *testing.T) {
		stream := `data: {"type":"response.output_text.delta","delta":"first"}` + "\n\n" +
			`data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"different"}]}}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"resp"}}` + "\n\n"
		adapter, credential, closeServer := testCodexSSEAdapter(t, stream)
		defer closeServer()
		_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
		assertCodexAdapterError(t, err, CodexErrorProtocol)
	})

	t.Run("response ID mismatch", func(t *testing.T) {
		stream := `data: {"type":"response.created","response":{"id":"one"}}` + "\n\n" +
			`data: {"type":"response.completed","response":{"id":"two"}}` + "\n\n"
		adapter, credential, closeServer := testCodexSSEAdapter(t, stream)
		defer closeServer()
		_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
		assertCodexAdapterError(t, err, CodexErrorProtocol)
	})
}

func TestCodexDirectAdapterRejectsUnsupportedInputsBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	adapter := newCodexDirectAdapter(codexResponsesURL, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	}), time.Second)
	now := time.Unix(1_800_000_000, 0)
	adapter.now = func() time.Time { return now }
	credential := testCodexCredential(t, now.Add(time.Hour), testAccountIDMarker)

	tests := []CodexTextRequest{
		{Model: "gpt-test", Messages: []CodexTextMessage{{Role: "system", Text: "secret"}}},
		{Model: "gpt-test", Messages: []CodexTextMessage{{Role: "developer", Text: "secret"}}},
		{Model: "gpt-test", Messages: []CodexTextMessage{{Role: CodexRoleUser, Text: "hello"}}, Features: CodexRequestFeatures{Tools: true}},
		{Model: "gpt-test", Messages: []CodexTextMessage{{Role: CodexRoleUser, Text: "hello"}}, Features: CodexRequestFeatures{Images: true}},
	}
	for _, request := range tests {
		_, err := adapter.Complete(context.Background(), credential, request)
		assertCodexAdapterError(t, err, CodexErrorUnsupportedFeature)
	}
	if calls.Load() != 0 {
		t.Fatalf("unsupported input reached network")
	}
}

func TestCodexDirectAdapterRequiresAccountIDAndUsableJWTBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	adapter := newCodexDirectAdapter(codexResponsesURL, roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	}), time.Second)
	now := time.Unix(1_800_000_000, 0)
	adapter.now = func() time.Time { return now }

	withoutAccount := testCodexCredential(t, now.Add(time.Hour), "")
	_, err := adapter.Complete(context.Background(), withoutAccount, testCodexTextRequest())
	assertCodexAdapterError(t, err, CodexErrorAccountIDRequired)

	expiring := testCodexCredential(t, now.Add(4*time.Minute), testAccountIDMarker)
	_, err = adapter.Complete(context.Background(), expiring, testCodexTextRequest())
	assertCodexAdapterError(t, err, CodexErrorReauthentication)

	malformedData := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":"%s","refresh_token":"%s","account_id":"%s"}}`, "not-a-jwt", testRefreshTokenMarker, testAccountIDMarker)
	malformed, parseErr := ParseCodexAuthJSON([]byte(malformedData))
	if parseErr != nil {
		t.Fatalf("parse fixture: %v", parseErr)
	}
	_, err = adapter.Complete(context.Background(), malformed, testCodexTextRequest())
	assertCodexAdapterError(t, err, CodexErrorAccessTokenInvalid)
	if calls.Load() != 0 {
		t.Fatalf("invalid credential reached network")
	}
}

func TestCodexDirectAdapterDoesNotRetry401(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"error":{"code":"invalid_token","message":"access-token-secret-marker prompt-secret-marker"}}`)
	}))
	defer server.Close()
	adapter, credential := testAdapterForServer(t, server, time.Second)
	_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
	assertCodexAdapterError(t, err, CodexErrorReauthentication)
	if calls.Load() != 1 {
		t.Fatalf("401 request count = %d, want 1", calls.Load())
	}
	assertNoSensitiveMarkers(t, fmt.Sprintf("%v %#v", err, err))
}

func TestCodexDirectAdapterClassifiesOnlyAllowlistedHTTPMetadata(t *testing.T) {
	tests := []struct {
		name         string
		status       int
		body         string
		retryAfter   string
		wantCode     CodexAdapterErrorCode
		wantProvider string
		wantRetry    time.Duration
	}{
		{"agent identity", http.StatusForbidden, `{"error":{"code":"agent_identity_required"}}`, "", CodexErrorAuthModeUnsupported, "agent_identity_required", 0},
		{"rate limited", http.StatusTooManyRequests, `{"error":{"code":"rate_limit_exceeded"}}`, "7", CodexErrorRateLimited, "rate_limit_exceeded", 7 * time.Second},
		{"usage exhausted", http.StatusPaymentRequired, `{"error":{"code":"usage_limit_reached"}}`, "", CodexErrorUsageExhausted, "usage_limit_reached", 0},
		{"server error", http.StatusInternalServerError, `{"error":{"code":"server_error"}}`, "", CodexErrorUpstream, "server_error", 0},
		{"unknown provider code", http.StatusForbidden, `{"error":{"code":"prompt-secret-marker"}}`, "", CodexErrorUpstream, "", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				if test.retryAfter != "" {
					writer.Header().Set("Retry-After", test.retryAfter)
				}
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			}))
			defer server.Close()
			adapter, credential := testAdapterForServer(t, server, time.Second)
			_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
			assertCodexAdapterError(t, err, test.wantCode)
			var adapterError *CodexAdapterError
			if !errors.As(err, &adapterError) || adapterError.ProviderCode() != test.wantProvider || adapterError.RetryAfter() != test.wantRetry {
				t.Fatalf("unexpected safe HTTP metadata: %#v", adapterError)
			}
			assertNoSensitiveMarkers(t, fmt.Sprintf("%v %#v", err, err))
		})
	}
}

func TestCodexDirectAdapterRejectsRedirectWithoutVisitingDestination(t *testing.T) {
	var destinationCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationCalls.Add(1)
	}))
	defer destination.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		http.Redirect(writer, &http.Request{}, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	adapter, credential := testAdapterForServer(t, origin, time.Second)
	_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
	assertCodexAdapterError(t, err, CodexErrorRedirectRejected)
	if destinationCalls.Load() != 0 {
		t.Fatalf("redirect destination received credential-bearing request")
	}
}

func TestCodexDirectAdapterCancellationAndTimeout(t *testing.T) {
	t.Run("caller cancellation", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(started)
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}))
		defer func() {
			close(release)
			server.Close()
		}()
		adapter, credential := testAdapterForServer(t, server, time.Second)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := adapter.Complete(ctx, credential, testCodexTextRequest())
			done <- err
		}()
		<-started
		cancel()
		err := <-done
		assertCodexAdapterError(t, err, CodexErrorCancelled)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation does not unwrap to context.Canceled")
		}
	})

	t.Run("adapter timeout", func(t *testing.T) {
		release := make(chan struct{})
		server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}))
		defer func() {
			close(release)
			server.Close()
		}()
		adapter, credential := testAdapterForServer(t, server, 25*time.Millisecond)
		_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
		assertCodexAdapterError(t, err, CodexErrorTimeout)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("timeout does not unwrap to context.DeadlineExceeded")
		}
	})
}

func TestCodexDirectAdapterBoundsSSE(t *testing.T) {
	t.Run("line", func(t *testing.T) {
		adapter, credential, closeServer := testCodexSSEAdapter(t, "data: "+strings.Repeat("x", 256)+"\n\n")
		defer closeServer()
		adapter.maxLine = 64
		_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
		assertCodexAdapterError(t, err, CodexErrorSSEFrameTooLarge)
	})

	t.Run("event", func(t *testing.T) {
		adapter, credential, closeServer := testCodexSSEAdapter(t, "data: 1234567890\ndata: 1234567890\n\n")
		defer closeServer()
		adapter.maxEvent = 16
		_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
		assertCodexAdapterError(t, err, CodexErrorSSEFrameTooLarge)
	})

	t.Run("response", func(t *testing.T) {
		adapter, credential, closeServer := testCodexSSEAdapter(t, strings.Repeat(": padding\n", 64))
		defer closeServer()
		adapter.maxResponse = 96
		_, err := adapter.Complete(context.Background(), credential, testCodexTextRequest())
		assertCodexAdapterError(t, err, CodexErrorResponseTooLarge)
	})
}

func TestCodexDirectAdapterStreamReportsCancellationAndFailure(t *testing.T) {
	adapter, credential, closeServer := testCodexSSEAdapter(t, "data: {\"type\":\"response.failed\",\"response\":{}}\n\n")
	defer closeServer()
	var events []CodexEventKind
	err := adapter.Stream(context.Background(), credential, testCodexTextRequest(), func(event CodexStreamEvent) error {
		events = append(events, event.Kind())
		return nil
	})
	assertCodexAdapterError(t, err, CodexErrorUpstream)
	if len(events) != 1 || events[0] != CodexEventFailed {
		t.Fatalf("failure event not emitted: %v", events)
	}
}

func TestCodexDirectAdapterConsumerCanStopStream(t *testing.T) {
	adapter, credential, closeServer := testCodexSSEAdapter(t, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
	defer closeServer()
	err := adapter.Stream(context.Background(), credential, testCodexTextRequest(), func(CodexStreamEvent) error {
		return errors.New("consumer secret")
	})
	assertCodexAdapterError(t, err, CodexErrorEventConsumerStopped)
	if strings.Contains(err.Error(), "consumer secret") {
		t.Fatalf("consumer error leaked")
	}
}

func TestCodexDirectAdapterSafeFormatting(t *testing.T) {
	event := CodexStreamEvent{kind: CodexEventTextDelta, text: testResponseMarker, responseID: "response-id-secret"}
	result := &CodexTextResult{text: testResponseMarker, responseID: "response-id-secret"}
	adapterError := &CodexAdapterError{code: CodexErrorUpstream, providerCode: "server_error", cause: errors.New(testPromptMarker)}
	request := CodexTextRequest{Model: "gpt-test", Messages: []CodexTextMessage{{Role: CodexRoleUser, Text: testPromptMarker}}}
	message := CodexTextMessage{Role: CodexRoleUser, Text: testPromptMarker}

	var logBuffer bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuffer, nil))
	logger.Info("safe", "event", event, "result", result, "error", adapterError, "request", request, "message", message)
	formatted := fmt.Sprintf("%v %#v %v %#v %v %#v %v %#v %v %#v", event, event, result, result, adapterError, adapterError, request, request, message, message)
	eventJSON, _ := json.Marshal(event)
	resultJSON, _ := json.Marshal(result)
	errorJSON, _ := json.Marshal(adapterError)
	requestJSON, _ := json.Marshal(request)
	messageJSON, _ := json.Marshal(message)
	combined := formatted + logBuffer.String() + string(eventJSON) + string(resultJSON) + string(errorJSON) + string(requestJSON) + string(messageJSON)
	assertNoSensitiveMarkers(t, combined)
	if strings.Contains(combined, "response-id-secret") {
		t.Fatalf("response identifier leaked through default formatting")
	}
}

func TestCodexDirectAdapterProductionDestinationIsFixed(t *testing.T) {
	adapter := NewCodexDirectAdapter()
	if adapter.endpoint != codexResponsesURL {
		t.Fatalf("production endpoint = %q, want pinned URL", adapter.endpoint)
	}
	if CodexDirectProtocolBaseline != "44b857c00e5803adedbc5b2e94c4a33574a157fe" {
		t.Fatalf("unexpected protocol baseline")
	}
}

func testCodexSSEAdapter(t *testing.T, stream string) (*CodexDirectAdapter, *CodexAuthCredential, func()) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, stream)
	}))
	adapter, credential := testAdapterForServer(t, server, time.Second)
	return adapter, credential, server.Close
}

func testAdapterForServer(t *testing.T, server *httptest.Server, timeout time.Duration) (*CodexDirectAdapter, *CodexAuthCredential) {
	t.Helper()
	now := time.Unix(1_800_000_000, 0)
	adapter := newCodexDirectAdapter(server.URL+"/backend-api/codex/responses", server.Client().Transport, timeout)
	adapter.now = func() time.Time { return now }
	credential := testCodexCredential(t, now.Add(time.Hour), testAccountIDMarker)
	return adapter, credential
}

func testCodexCredential(t *testing.T, expiresAt time.Time, accountID string) *CodexAuthCredential {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payloadData, err := json.Marshal(map[string]interface{}{"exp": expiresAt.Unix(), "marker": testAccessTokenMarker})
	if err != nil {
		t.Fatalf("marshal JWT payload: %v", err)
	}
	token := header + "." + base64.RawURLEncoding.EncodeToString(payloadData) + "." + testAccessTokenMarker
	data, err := json.Marshal(map[string]interface{}{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"access_token":  token,
			"refresh_token": testRefreshTokenMarker,
			"id_token":      "id-token-secret-marker",
			"account_id":    accountID,
		},
	})
	if err != nil {
		t.Fatalf("marshal auth fixture: %v", err)
	}
	credential, err := ParseCodexAuthJSON(data)
	if err != nil {
		t.Fatalf("ParseCodexAuthJSON: %v", err)
	}
	return credential
}

func testCodexTextRequest() CodexTextRequest {
	return CodexTextRequest{Model: "gpt-test", Messages: []CodexTextMessage{{Role: CodexRoleUser, Text: testPromptMarker}}}
}

func assertCodexWireMessage(t *testing.T, value interface{}, role, contentType, text string) {
	t.Helper()
	message, ok := value.(map[string]interface{})
	if !ok || message["type"] != "message" || message["role"] != role {
		t.Fatalf("unexpected wire message: %v", value)
	}
	content, ok := message["content"].([]interface{})
	if !ok || len(content) != 1 {
		t.Fatalf("unexpected wire content")
	}
	part, ok := content[0].(map[string]interface{})
	if !ok || part["type"] != contentType || part["text"] != text {
		t.Fatalf("unexpected wire content part: %v", content[0])
	}
}

func assertCodexAdapterError(t *testing.T, err error, want CodexAdapterErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %q", want)
	}
	code, ok := CodexRequestErrorCode(err)
	if !ok || code != want {
		t.Fatalf("error code = %q (ok=%v), want %q; error=%v", code, ok, want, err)
	}
}

func assertInt64Pointer(t *testing.T, value *int64, want int64) {
	t.Helper()
	if value == nil || *value != want {
		t.Fatalf("usage value = %v, want %d", value, want)
	}
}

func assertNoSensitiveMarkers(t *testing.T, value string) {
	t.Helper()
	for _, marker := range []string{testAccessTokenMarker, testRefreshTokenMarker, testAccountIDMarker, testPromptMarker, testResponseMarker, "id-token-secret-marker"} {
		if strings.Contains(value, marker) {
			t.Fatalf("sensitive marker %q leaked", marker)
		}
	}
}

func sortedMapKeys(value map[string]interface{}) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	// Ordering is used only to make a failure readable.
	for left := 0; left < len(keys); left++ {
		for right := left + 1; right < len(keys); right++ {
			if keys[right] < keys[left] {
				keys[left], keys[right] = keys[right], keys[left]
			}
		}
	}
	return keys
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
