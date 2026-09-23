package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/membership"
)

type generationProbeRoundTripper func(*http.Request) (*http.Response, error)

func (fn generationProbeRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type generationProbeCodexExecutor struct {
	complete func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError)
}

func (e generationProbeCodexExecutor) Complete(ctx context.Context, credential *membership.CodexAuthCredential, request membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
	return e.complete(ctx, credential, request)
}

func (generationProbeCodexExecutor) Stream(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest, func(codexExecutionEvent) error) *codexRunError {
	panic("generation probe must not call the streaming Chat executor")
}

type generationProbeResponsesExecutor struct {
	responses func(context.Context, *membership.CodexAuthCredential, []byte, func(json.RawMessage) error) (json.RawMessage, *codexRunError)
}

func (e generationProbeResponsesExecutor) Responses(ctx context.Context, credential *membership.CodexAuthCredential, body []byte, consume func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
	return e.responses(ctx, credential, body, consume)
}

func TestGenerationProbeAPIKeyProtocolsUseFixedSafeRequests(t *testing.T) {
	tests := []struct {
		name         string
		provider     string
		protocol     accounting.UsageProtocol
		model        string
		path         string
		authHeader   string
		apiKeyHeader string
		googleHeader string
		response     string
		assertBody   func(*testing.T, map[string]json.RawMessage)
	}{
		{
			name: "OpenAI Chat", provider: "openai-compatible", protocol: accounting.ProtocolOpenAIChatCompletions,
			model: "upstream model 中文", path: "/v1/chat/completions", authHeader: "Bearer synthetic-secret",
			response: `{"id":"chat-1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4,"prompt_tokens_details":{"cached_tokens":1,"cache_write_tokens":0}}}`,
			assertBody: func(t *testing.T, root map[string]json.RawMessage) {
				assertGenerationProbeField(t, root, "model", "upstream model 中文")
				assertGenerationProbeField(t, root, "stream", false)
				assertGenerationProbeField(t, root, "max_tokens", float64(generationProbeMaxTokens))
				assertFixedGenerationMessages(t, root["messages"])
			},
		},
		{
			name: "OpenAI Responses", provider: "openai-compatible", protocol: accounting.ProtocolOpenAIResponses,
			model: "responses model", path: "/v1/responses", authHeader: "Bearer synthetic-secret",
			response: `{"id":"resp-1","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`,
			assertBody: func(t *testing.T, root map[string]json.RawMessage) {
				assertGenerationProbeField(t, root, "model", "responses model")
				assertGenerationProbeField(t, root, "input", generationProbePrompt)
				assertGenerationProbeField(t, root, "stream", false)
				assertGenerationProbeField(t, root, "store", false)
				assertGenerationProbeField(t, root, "max_output_tokens", float64(generationProbeMaxTokens))
			},
		},
		{
			name: "Anthropic Messages", provider: anthropicAPIKeyProvider, protocol: accounting.ProtocolAnthropicMessages,
			model: "claude actual 中文", path: "/v1/messages", apiKeyHeader: "synthetic-secret",
			response: `{"id":"msg-1","type":"message","role":"assistant","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`,
			assertBody: func(t *testing.T, root map[string]json.RawMessage) {
				assertGenerationProbeField(t, root, "model", "claude actual 中文")
				assertGenerationProbeField(t, root, "stream", false)
				assertGenerationProbeField(t, root, "max_tokens", float64(generationProbeMaxTokens))
				assertFixedGenerationMessages(t, root["messages"])
			},
		},
		{
			name: "Gemini generateContent", provider: geminiAPIKeyProvider, protocol: accounting.ProtocolGeminiGenerateContent,
			model: "models/gemini-2.5-flash", path: "/v1beta/models/gemini-2.5-flash:generateContent", googleHeader: "synthetic-secret",
			response: `{"candidates":[{"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":2,"cachedContentTokenCount":0,"candidatesTokenCount":1,"thoughtsTokenCount":0,"totalTokenCount":3}}`,
			assertBody: func(t *testing.T, root map[string]json.RawMessage) {
				if _, present := root["model"]; present {
					t.Fatal("Gemini model must be encoded only in the validated URL")
				}
				var contents []struct {
					Role  string `json:"role"`
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				}
				if json.Unmarshal(root["contents"], &contents) != nil || len(contents) != 1 || contents[0].Role != "user" || len(contents[0].Parts) != 1 || contents[0].Parts[0].Text != generationProbePrompt {
					t.Fatal("Gemini probe did not use the fixed synthetic input")
				}
				var config map[string]float64
				if json.Unmarshal(root["generationConfig"], &config) != nil || config["maxOutputTokens"] != generationProbeMaxTokens {
					t.Fatal("Gemini probe did not use the fixed output limit")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			app := &App{cfg: Config{AllowLoopbackUpstream: true}}
			app.http = &http.Client{Transport: generationProbeRoundTripper(func(request *http.Request) (*http.Response, error) {
				calls.Add(1)
				if request.Method != http.MethodPost || request.URL.Path != test.path || request.URL.RawQuery != "" {
					t.Fatalf("unexpected target %s %s", request.Method, request.URL.String())
				}
				if request.Header.Get("Accept") != "application/json" || request.Header.Get("Content-Type") != "application/json" {
					t.Fatalf("unsafe content negotiation: %#v", request.Header)
				}
				if got := request.Header.Get("Authorization"); got != test.authHeader {
					t.Fatalf("Authorization=%q", got)
				}
				if got := request.Header.Get("X-Api-Key"); got != test.apiKeyHeader {
					t.Fatalf("X-Api-Key=%q", got)
				}
				if got := request.Header.Get("x-goog-api-key"); got != test.googleHeader {
					t.Fatalf("x-goog-api-key=%q", got)
				}
				if test.protocol == accounting.ProtocolAnthropicMessages && request.Header.Get("Anthropic-Version") != "2023-06-01" {
					t.Fatalf("Anthropic-Version=%q", request.Header.Get("Anthropic-Version"))
				}
				body, err := io.ReadAll(request.Body)
				if err != nil || bytes.Contains(body, []byte("synthetic-secret")) {
					t.Fatal("request body read failed or retained a credential")
				}
				var root map[string]json.RawMessage
				if json.Unmarshal(body, &root) != nil {
					t.Fatal("probe body is not JSON")
				}
				test.assertBody(t, root)
				return generationProbeHTTPResponse(http.StatusOK, "application/json", test.response), nil
			})}

			result := app.runGenerationProbe(context.Background(), generationProbeInput{
				Selected: route{ProviderKind: test.provider, Endpoint: "http://127.0.0.1", UpstreamModel: test.model},
				Protocol: test.protocol, APIKey: []byte("synthetic-secret"),
			})
			if result.Status != accounting.StatusSucceeded || result.Code != generationCodeOK || calls.Load() != 1 {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
			if result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 1 {
				t.Fatalf("usage=%+v", result.Usage)
			}
		})
	}
}

func TestGenerationProbeAPIKeyFailureAndTerminalBoundaries(t *testing.T) {
	valid := `{"id":"chat-1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`
	partial := `{"id":"chat-1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"O"},"finish_reason":null}],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		transport   error
		wantStatus  accounting.Status
		wantCode    string
	}{
		{"authentication", 401, "application/json", `{"error":{"message":"secret upstream text"}}`, nil, accounting.StatusFailed, generationCodeAuth},
		{"rate limit", 429, "application/json", `{}`, nil, accounting.StatusFailed, generationCodeRateLimited},
		{"unavailable", 503, "application/json", `{}`, nil, accounting.StatusFailed, generationCodeUnavailable},
		{"configuration", 404, "application/json", `{}`, nil, accounting.StatusFailed, generationCodeConfiguration},
		{"unexpected SSE", 200, "text/event-stream", "data: " + valid + "\n\ndata: [DONE]\n\n", nil, accounting.StatusFailed, generationCodeProtocol},
		{"partial JSON", 200, "application/json", partial, nil, accounting.StatusInterrupted, generationCodeInterrupted},
		{"length terminal", 200, "application/json", `{"id":"chat-1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"O"},"finish_reason":"length"}]}`, nil, accounting.StatusInterrupted, generationCodeInterrupted},
		{"error and choice", 200, "application/json", `{"id":"chat-1","object":"chat.completion","error":{"message":"upstream failure"},"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`, nil, accounting.StatusFailed, generationCodeProtocol},
		{"malformed error and choice", 200, "application/json", `{"id":"chat-1","object":"chat.completion","error":[],"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`, nil, accounting.StatusFailed, generationCodeProtocol},
		{"tool and text", 200, "application/json", `{"id":"chat-1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"OK","tool_calls":[{"type":"function"}]},"finish_reason":"stop"}]}`, nil, accounting.StatusFailed, generationCodeProtocol},
		{"empty completed output", 200, "application/json", `{"id":"chat-1","object":"chat.completion","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`, nil, accounting.StatusFailed, generationCodeProtocol},
		{"transport", 0, "", "", errors.New("sensitive dial error"), accounting.StatusFailed, generationCodeUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			app := &App{cfg: Config{AllowLoopbackUpstream: true}, http: &http.Client{Transport: generationProbeRoundTripper(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				if test.transport != nil {
					return nil, test.transport
				}
				return generationProbeHTTPResponse(test.status, test.contentType, test.body), nil
			})}}
			result := app.runGenerationProbe(context.Background(), generationProbeInput{
				Selected: route{ProviderKind: "openai-compatible", Endpoint: "http://127.0.0.1", UpstreamModel: "probe-model"},
				Protocol: accounting.ProtocolOpenAIChatCompletions, APIKey: []byte("secret"),
			})
			if result.Status != test.wantStatus || result.Code != test.wantCode || calls.Load() != 1 {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
		})
	}
}

func TestGenerationProbeProtocolTerminalsRejectTruncationAndTools(t *testing.T) {
	tests := []struct {
		name       string
		protocol   accounting.UsageProtocol
		body       string
		wantStatus accounting.Status
		wantCode   string
	}{
		{
			name: "Responses incomplete", protocol: accounting.ProtocolOpenAIResponses,
			body:       `{"id":"r","object":"response","status":"incomplete","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"O"}]}]}`,
			wantStatus: accounting.StatusInterrupted, wantCode: generationCodeInterrupted,
		},
		{
			name: "Responses tool beside text", protocol: accounting.ProtocolOpenAIResponses,
			body:       `{"id":"r","object":"response","status":"completed","output":[{"type":"function_call","name":"x","arguments":"{}"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`,
			wantStatus: accounting.StatusFailed, wantCode: generationCodeProtocol,
		},
		{
			name: "Anthropic max tokens", protocol: accounting.ProtocolAnthropicMessages,
			body:       `{"id":"m","type":"message","role":"assistant","stop_reason":"max_tokens","content":[{"type":"text","text":"O"}]}`,
			wantStatus: accounting.StatusInterrupted, wantCode: generationCodeInterrupted,
		},
		{
			name: "Gemini max tokens", protocol: accounting.ProtocolGeminiGenerateContent,
			body:       `{"candidates":[{"finishReason":"MAX_TOKENS","content":{"role":"model","parts":[{"text":"O"}]}}]}`,
			wantStatus: accounting.StatusInterrupted, wantCode: generationCodeInterrupted,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := validateGenerationProbeResponse(test.protocol, []byte(test.body))
			if result.Status != test.wantStatus || result.Code != test.wantCode {
				t.Fatalf("result=%+v", result)
			}
		})
	}
}

func TestGenerationProbeRejectsOversizeAndCancellationWithoutRetry(t *testing.T) {
	t.Run("oversize", func(t *testing.T) {
		var calls atomic.Int32
		// Use a zero-allocation repeating reader so the boundary, not memory size,
		// determines the outcome.
		app := &App{cfg: Config{AllowLoopbackUpstream: true}, http: &http.Client{Transport: generationProbeRoundTripper(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(&generationProbeRepeatingReader{remaining: generationProbeMaxBody + 1})}, nil
		})}}
		result := app.runGenerationProbe(context.Background(), generationProbeInput{Selected: route{ProviderKind: "openai-compatible", Endpoint: "http://127.0.0.1", UpstreamModel: "m"}, Protocol: accounting.ProtocolOpenAIChatCompletions, APIKey: []byte("secret")})
		if result.Code != generationCodeProtocol || calls.Load() != 1 {
			t.Fatalf("result=%+v calls=%d", result, calls.Load())
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var calls atomic.Int32
		app := &App{cfg: Config{AllowLoopbackUpstream: true}, http: &http.Client{Transport: generationProbeRoundTripper(func(request *http.Request) (*http.Response, error) {
			calls.Add(1)
			cancel()
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}}
		result := app.runGenerationProbe(ctx, generationProbeInput{Selected: route{ProviderKind: "openai-compatible", Endpoint: "http://127.0.0.1", UpstreamModel: "m"}, Protocol: accounting.ProtocolOpenAIChatCompletions, APIKey: []byte("secret")})
		if result.Status != accounting.StatusCancelled || result.Code != generationCodeCancelled || calls.Load() != 1 {
			t.Fatalf("result=%+v calls=%d", result, calls.Load())
		}
	})
}

func TestGenerationProbeCodexExecutorsRequireCompleteTextAndNormalizeUsage(t *testing.T) {
	credential := &membership.CodexAuthCredential{}
	t.Run("Chat complete", func(t *testing.T) {
		inputTokens, cachedTokens, outputTokens, reasoningTokens, totalTokens := int64(3), int64(1), int64(2), int64(1), int64(5)
		var calls atomic.Int32
		app := &App{codex: generationProbeCodexExecutor{complete: func(_ context.Context, got *membership.CodexAuthCredential, request membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
			calls.Add(1)
			if got != credential || request.Model != "codex actual" || len(request.Messages) != 1 || request.Messages[0].Role != membership.CodexRoleUser || request.Messages[0].Text != generationProbePrompt {
				t.Fatalf("unexpected Codex probe request: %+v", request)
			}
			return codexExecutionResult{Text: "OK", ResponseID: "resp-1", Usage: membership.CodexUsage{InputTokens: &inputTokens, CachedInputTokens: &cachedTokens, OutputTokens: &outputTokens, ReasoningOutputTokens: &reasoningTokens, TotalTokens: &totalTokens}}, nil
		}}}
		result := app.runGenerationProbe(context.Background(), generationProbeInput{Selected: route{ProviderKind: codexMembershipProvider, UpstreamModel: "codex actual"}, Protocol: accounting.ProtocolOpenAIChatCompletions, CodexCredential: credential})
		if result.Status != accounting.StatusSucceeded || result.Code != generationCodeOK || calls.Load() != 1 || result.Usage.InputTokens != nil || result.Usage.CacheReadTokens == nil || *result.Usage.CacheReadTokens != 1 || result.Usage.CacheWriteTokens != nil || result.Usage.OutputTokens == nil || *result.Usage.OutputTokens != 2 {
			t.Fatalf("result=%+v calls=%d", result, calls.Load())
		}
	})

	t.Run("Chat empty completion", func(t *testing.T) {
		app := &App{codex: generationProbeCodexExecutor{complete: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
			return codexExecutionResult{ResponseID: "resp-1"}, nil
		}}}
		result := app.runGenerationProbe(context.Background(), generationProbeInput{Selected: route{ProviderKind: codexMembershipProvider, UpstreamModel: "m"}, Protocol: accounting.ProtocolOpenAIChatCompletions, CodexCredential: credential})
		if result.Code != generationCodeProtocol || result.Status != accounting.StatusFailed {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("Chat missing ID", func(t *testing.T) {
		app := &App{codex: generationProbeCodexExecutor{complete: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
			return codexExecutionResult{Text: "OK"}, nil
		}}}
		result := app.runGenerationProbe(context.Background(), generationProbeInput{Selected: route{ProviderKind: codexMembershipProvider, UpstreamModel: "m"}, Protocol: accounting.ProtocolOpenAIChatCompletions, CodexCredential: credential})
		if result.Code != generationCodeProtocol {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("Chat oversized completion", func(t *testing.T) {
		app := &App{codex: generationProbeCodexExecutor{complete: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
			return codexExecutionResult{Text: strings.Repeat("x", generationProbeMaxBody+1), ResponseID: "resp-oversized"}, nil
		}}}
		result := app.runGenerationProbe(context.Background(), generationProbeInput{Selected: route{ProviderKind: codexMembershipProvider, UpstreamModel: "m"}, Protocol: accounting.ProtocolOpenAIChatCompletions, CodexCredential: credential})
		if result.Code != generationCodeProtocol {
			t.Fatalf("result=%+v", result)
		}
	})

	t.Run("Responses native completion", func(t *testing.T) {
		var calls atomic.Int32
		app := &App{responses: generationProbeResponsesExecutor{responses: func(_ context.Context, got *membership.CodexAuthCredential, body []byte, consume func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
			calls.Add(1)
			if got != credential || consume != nil {
				t.Fatal("Codex Responses probe must use the executor's completed-result mode")
			}
			var root map[string]json.RawMessage
			if json.Unmarshal(body, &root) != nil {
				t.Fatal("invalid Codex Responses body")
			}
			assertGenerationProbeField(t, root, "model", "codex responses actual")
			assertGenerationProbeField(t, root, "input", generationProbePrompt)
			return json.RawMessage(`{"id":"resp-2","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}]}`), nil
		}}}
		result := app.runGenerationProbe(context.Background(), generationProbeInput{Selected: route{ProviderKind: codexMembershipProvider, UpstreamModel: "codex responses actual"}, Protocol: accounting.ProtocolOpenAIResponses, CodexCredential: credential})
		if result.Status != accounting.StatusSucceeded || result.Code != generationCodeOK || calls.Load() != 1 {
			t.Fatalf("result=%+v calls=%d", result, calls.Load())
		}
	})
}

func TestGenerationProbeCodexErrorClassificationAndInputIsolation(t *testing.T) {
	credential := &membership.CodexAuthCredential{}
	tests := []struct {
		name       string
		code       membership.CodexAdapterErrorCode
		wantStatus accounting.Status
		wantCode   string
	}{
		{"auth", membership.CodexErrorReauthentication, accounting.StatusFailed, generationCodeAuth},
		{"rate", membership.CodexErrorRateLimited, accounting.StatusFailed, generationCodeRateLimited},
		{"timeout", membership.CodexErrorTimeout, accounting.StatusFailed, generationCodeTimeout},
		{"cancel", membership.CodexErrorCancelled, accounting.StatusCancelled, generationCodeCancelled},
		{"protocol", membership.CodexErrorSSEFrameTooLarge, accounting.StatusFailed, generationCodeProtocol},
		{"unsupported", membership.CodexErrorUnsupportedFeature, accounting.StatusFailed, generationCodeUnsupported},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			app := &App{codex: generationProbeCodexExecutor{complete: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
				calls.Add(1)
				return codexExecutionResult{}, &codexRunError{Code: test.code}
			}}}
			result := app.runGenerationProbe(context.Background(), generationProbeInput{Selected: route{ProviderKind: codexMembershipProvider, UpstreamModel: "m"}, Protocol: accounting.ProtocolOpenAIChatCompletions, CodexCredential: credential})
			if result.Status != test.wantStatus || result.Code != test.wantCode || calls.Load() != 1 {
				t.Fatalf("result=%+v calls=%d", result, calls.Load())
			}
		})
	}

	app := &App{codex: generationProbeCodexExecutor{complete: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		t.Fatal("invalid mixed credential input reached executor")
		return codexExecutionResult{}, nil
	}}}
	result := app.runGenerationProbe(context.Background(), generationProbeInput{Selected: route{ProviderKind: codexMembershipProvider, UpstreamModel: "m"}, Protocol: accounting.ProtocolOpenAIChatCompletions, APIKey: []byte("also-a-key"), CodexCredential: credential})
	if result.Code != generationCodeConfiguration {
		t.Fatalf("result=%+v", result)
	}
}

func generationProbeHTTPResponse(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func assertGenerationProbeField(t *testing.T, root map[string]json.RawMessage, field string, want any) {
	t.Helper()
	var got any
	if json.Unmarshal(root[field], &got) != nil || !generationProbeJSONEqual(got, want) {
		t.Fatalf("%s=%v want %v", field, got, want)
	}
}

func generationProbeJSONEqual(left, right any) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func assertFixedGenerationMessages(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if json.Unmarshal(raw, &messages) != nil || len(messages) != 1 || messages[0].Role != "user" || messages[0].Content != generationProbePrompt {
		t.Fatal("probe did not use the fixed synthetic message")
	}
}

type generationProbeRepeatingReader struct{ remaining int }

func (r *generationProbeRepeatingReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(buffer)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		buffer[i] = 'x'
	}
	r.remaining -= n
	return n, nil
}

func TestGenerationProbeDeadlineIsTimeoutBeforeDispatch(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	var calls atomic.Int32
	app := &App{http: &http.Client{Transport: generationProbeRoundTripper(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not dispatch")
	})}}
	result := app.runGenerationProbe(ctx, generationProbeInput{Selected: route{ProviderKind: "openai-compatible", Endpoint: "https://example.invalid", UpstreamModel: "m"}, Protocol: accounting.ProtocolOpenAIChatCompletions, APIKey: []byte("secret")})
	if result.Status != accounting.StatusFailed || result.Code != generationCodeTimeout || calls.Load() != 0 {
		t.Fatalf("result=%+v calls=%d", result, calls.Load())
	}
}
