package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/protocolconv"
)

type protocolStreamDoer struct {
	response *http.Response
	err      error
	calls    atomic.Int64
}

func (d *protocolStreamDoer) Do(*http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return d.response, d.err
}

func testProtocolStreamLimits() protocolStreamLimits {
	return protocolStreamLimits{MaxLineBytes: 1 << 16, MaxEventBytes: 1 << 18, MaxStreamBytes: 1 << 20, TerminalDrainTimeout: 100 * time.Millisecond}
}

func testProtocolStreamRuntime(kind protocolconv.PlanKind, client, upstream protocolconv.Protocol) *protocolRuntime {
	return &protocolRuntime{prepared: protocolconv.PreparedRequest{
		Plan: protocolconv.Plan{Kind: kind, ClientProtocol: client, UpstreamProtocol: upstream}, Streaming: true,
	}}
}

func chatSuccessSSE() string {
	return "data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
}

func responsesSuccessSSE() string {
	events := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc","type":"function_call","status":"in_progress","call_id":"call","name":"lookup","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":2,"item_id":"fc","output_index":0,"delta":"{}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":3,"item_id":"fc","output_index":0,"arguments":"{}"}`,
		`{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"fc","type":"function_call","status":"completed","call_id":"call","name":"lookup","arguments":"{}"}}`,
		`{"type":"response.completed","sequence_number":5,"response":{"id":"r","object":"response","created_at":1,"model":"m","status":"completed","output":[{"id":"fc","type":"function_call","status":"completed","call_id":"call","name":"lookup","arguments":"{}"}]}}`,
	}
	var builder strings.Builder
	for _, event := range events {
		var eventType string
		start := strings.Index(event, `"type":"`) + len(`"type":"`)
		end := strings.Index(event[start:], `"`)
		eventType = event[start : start+end]
		builder.WriteString("event: " + eventType + "\n")
		builder.WriteString("data: " + event + "\n\n")
	}
	return builder.String()
}

func streamResponse(status int, contentType, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestProtocolRuntimeStreamDispatchesOnceObservesBeforeWritingAndDrainsTerminal(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToChat, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolOpenAIChat)
	doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream; charset=utf-8", chatSuccessSSE())}
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid/v1/chat/completions", nil)
	var order []string
	var terminalWrites int
	result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), func(protocol protocolconv.Protocol, raw []byte) error {
		if protocol != protocolconv.ProtocolOpenAIChat || len(raw) == 0 {
			t.Fatalf("unexpected raw observation: %q %q", protocol, raw)
		}
		order = append(order, "observe")
		return nil
	}, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
		order = append(order, "write")
		if event.Terminal {
			terminalWrites++
		}
		return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if doer.calls.Load() != 1 || !result.Completed || result.TerminalOutcome != protocolconv.StreamTerminalCompleted || !result.DownstreamCommitted || !result.SemanticCommitted || terminalWrites != 1 {
		t.Fatalf("unexpected result: calls=%d result=%#v terminal=%d", doer.calls.Load(), result, terminalWrites)
	}
	if len(order) == 0 || order[0] != "observe" {
		t.Fatalf("conversion wrote before raw observation: %#v", order)
	}
}

func TestProtocolRuntimeStreamResponsesWireConvertsFunctions(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanChatToResponses, protocolconv.ProtocolOpenAIChat, protocolconv.ProtocolOpenAIResponses)
	doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream", responsesSuccessSSE())}
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid/v1/responses", nil)
	observed := 0
	semantic := 0
	terminal := 0
	result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), func(protocol protocolconv.Protocol, raw []byte) error {
		if protocol != protocolconv.ProtocolOpenAIResponses {
			t.Fatalf("unexpected protocol %q", protocol)
		}
		observed++
		return nil
	}, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
		if event.Semantic {
			semantic++
		}
		if event.Terminal {
			terminal++
		}
		return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
	})
	if err != nil || !result.Completed || result.TerminalOutcome != protocolconv.StreamTerminalCompleted || observed != 6 || semantic != 2 || terminal != 1 {
		t.Fatalf("result=%#v observed=%d semantic=%d terminal=%d err=%v", result, observed, semantic, terminal, err)
	}
}

func TestProtocolRuntimeStreamPropagatesMessagesNonSuccessOutcomes(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToMessages, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolAnthropicMessages)
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid/v1/messages", nil)
	tests := []struct {
		name    string
		body    string
		outcome protocolconv.StreamTerminalOutcome
	}{
		{
			name: "incomplete",
			body: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"m\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[],\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\n" +
				"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
				"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"cut\"}}\n\n" +
				"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
				"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"max_tokens\",\"stop_sequence\":null},\"usage\":{\"output_tokens\":1}}\n\n" +
				"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
			outcome: protocolconv.StreamTerminalIncomplete,
		},
		{
			name:    "failed",
			body:    "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"private-secret\"}}\n\n",
			outcome: protocolconv.StreamTerminalFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream", test.body)}
			var terminal []protocolconv.SSEEvent
			observed := 0
			result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), func(protocol protocolconv.Protocol, raw []byte) error {
				if protocol != protocolconv.ProtocolAnthropicMessages || len(raw) == 0 {
					t.Fatalf("unexpected raw observation: protocol=%q raw=%q", protocol, raw)
				}
				observed++
				return nil
			}, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
				if event.Terminal {
					terminal = append(terminal, event)
				}
				return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
			})
			if err != nil || result.Completed || result.TerminalOutcome != test.outcome || len(terminal) != 1 || terminal[0].TerminalOutcome != test.outcome || observed == 0 {
				t.Fatalf("result=%#v terminal=%#v observed=%d err=%v", result, terminal, observed, err)
			}
			if bytes.Contains(terminal[0].Data, []byte("private-secret")) {
				t.Fatalf("terminal leaked upstream error: %s", terminal[0].Data)
			}
		})
	}
}

func TestProtocolRuntimeStreamRejectsStatusAndContentTypeBeforeCommit(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToChat, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolOpenAIChat)
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid", nil)
	t.Run("status", func(t *testing.T) {
		response := streamResponse(http.StatusTooManyRequests, "application/json", `{}`)
		response.Header.Set("Retry-After", "7")
		doer := &protocolStreamDoer{response: response}
		result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), nil, func(protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
			t.Fatal("write callback must not run")
			return protocolStreamWriteResult{}, nil
		})
		var statusErr *protocolUpstreamStatusError
		if !errors.As(err, &statusErr) || result.Header.Get("Retry-After") != "7" || result.DownstreamCommitted {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
	t.Run("content type", func(t *testing.T) {
		doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "application/json", `{}`)}
		result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), nil, func(protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
			t.Fatal("write callback must not run")
			return protocolStreamWriteResult{}, nil
		})
		if err == nil || result.DownstreamCommitted {
			t.Fatalf("result=%#v err=%v", result, err)
		}
	})
}

func TestProtocolRuntimeStreamObserverFailurePreventsFrameOutput(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToChat, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolOpenAIChat)
	doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream", chatSuccessSSE())}
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid", nil)
	writes := 0
	observerErr := errors.New("usage observer unavailable")
	result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), func(protocolconv.Protocol, []byte) error {
		return observerErr
	}, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
		writes++
		return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
	})
	if !errors.Is(err, observerErr) || writes != 0 || result.DownstreamCommitted {
		t.Fatalf("result=%#v writes=%d err=%v", result, writes, err)
	}
}

func TestProtocolRuntimeStreamPreservesPartialCommitOnWriteErrors(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToChat, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolOpenAIChat)
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid", nil)
	for _, terminalOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "first write", true: "terminal write"}[terminalOnly], func(t *testing.T) {
			doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream", chatSuccessSSE())}
			result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), nil, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
				if !terminalOnly || event.Terminal {
					return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: true}, errors.New("short write")
				}
				return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
			})
			if !errors.Is(err, errProtocolStreamDownstream) || !result.DownstreamCommitted || !result.SemanticCommitted || result.Completed || terminalOnly && result.TerminalOutcome != protocolconv.StreamTerminalCompleted {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

func TestProtocolRuntimeStreamWithholdsTerminalUntilCleanEOF(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToChat, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolOpenAIChat)
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid", nil)
	for _, tail := range []string{
		"data: {\"id\":\"c\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"m\",\"choices\":[]}\n\n",
		"data: [DONE]\n\n",
	} {
		doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream", chatSuccessSSE()+tail)}
		successBatchWrites := 0
		result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), nil, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
			if event.Terminal || strings.HasSuffix(event.Name, ".done") || event.Name == "response.completed" {
				successBatchWrites++
			}
			return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
		})
		if !errors.Is(err, protocolconv.ErrInvalidUpstream) || result.Completed || successBatchWrites != 0 {
			t.Fatalf("tail=%q result=%#v success-batch=%d err=%v", tail, result, successBatchWrites, err)
		}
	}
}

func TestProtocolRuntimeStreamResponsesTerminalBatchIsWithheldOnInvalidTail(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanChatToResponses, protocolconv.ProtocolOpenAIChat, protocolconv.ProtocolOpenAIResponses)
	tail := "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":6,\"response\":{}}\n\n"
	doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream", responsesSuccessSSE()+tail)}
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid", nil)
	successBatchWrites := 0
	result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), nil, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
		if event.Terminal || bytes.Contains(event.Data, []byte(`"finish_reason":"tool_calls"`)) {
			successBatchWrites++
		}
		return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
	})
	if !errors.Is(err, protocolconv.ErrInvalidUpstream) || result.Completed || successBatchWrites != 0 {
		t.Fatalf("result=%#v success-batch=%d err=%v", result, successBatchWrites, err)
	}
}

func TestProtocolRuntimeStreamTerminalDrainTimeout(t *testing.T) {
	tests := []struct {
		name    string
		runtime *protocolRuntime
		body    string
		isBatch func(protocolconv.SSEEvent) bool
	}{
		{
			name: "Chat wire", runtime: testProtocolStreamRuntime(protocolconv.PlanResponsesToChat, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolOpenAIChat), body: chatSuccessSSE(),
			isBatch: func(event protocolconv.SSEEvent) bool {
				return event.Terminal || strings.HasSuffix(event.Name, ".done") || event.Name == "response.completed"
			},
		},
		{
			name: "Responses wire", runtime: testProtocolStreamRuntime(protocolconv.PlanChatToResponses, protocolconv.ProtocolOpenAIChat, protocolconv.ProtocolOpenAIResponses), body: responsesSuccessSSE(),
			isBatch: func(event protocolconv.SSEEvent) bool {
				return event.Terminal || bytes.Contains(event.Data, []byte(`"finish_reason":"tool_calls"`))
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reader, writer := io.Pipe()
			doer := &protocolStreamDoer{response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}}
			request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid", nil)
			go func() { _, _ = io.WriteString(writer, test.body) }()
			limits := testProtocolStreamLimits()
			limits.TerminalDrainTimeout = 20 * time.Millisecond
			successBatchWrites := 0
			result, err := test.runtime.executeStream(context.Background(), doer, request, limits, nil, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
				if test.isBatch(event) {
					successBatchWrites++
				}
				return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
			})
			_ = writer.Close()
			if !errors.Is(err, protocolconv.ErrInterrupted) || result.Completed || successBatchWrites != 0 {
				t.Fatalf("result=%#v success-batch=%d err=%v", result, successBatchWrites, err)
			}
		})
	}
}

func TestProtocolRuntimeStreamCancellationClosesBlockingUpstream(t *testing.T) {
	reader, writer := io.Pipe()
	doer := &protocolStreamDoer{response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}}
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToChat, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolOpenAIChat)
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid", nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := runtime.executeStream(ctx, doer, request, testProtocolStreamLimits(), nil, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
			return protocolStreamWriteResult{}, nil
		})
		done <- err
	}()
	for doer.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not close the upstream read")
	}
	if _, err := writer.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("upstream pipe was not closed: %v", err)
	}
	_ = writer.Close()
}

type gatedStreamReader struct {
	mu     sync.Mutex
	chunks [][]byte
	reads  int
}

func (r *gatedStreamReader) Read(buffer []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	r.chunks = r.chunks[1:]
	r.reads++
	return copy(buffer, chunk), nil
}

func (r *gatedStreamReader) Close() error { return nil }

func (r *gatedStreamReader) readCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reads
}

func TestProtocolRuntimeStreamWriteBackpressureStopsFurtherReads(t *testing.T) {
	parts := strings.SplitAfter(chatSuccessSSE(), "\n\n")
	reader := &gatedStreamReader{}
	for _, part := range parts {
		if part != "" {
			reader.chunks = append(reader.chunks, []byte(part))
		}
	}
	doer := &protocolStreamDoer{response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}}
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToChat, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolOpenAIChat)
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid", nil)
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), nil, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
			select {
			case <-entered:
			default:
				close(entered)
				<-release
			}
			return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
		})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("write callback was not reached")
	}
	time.Sleep(20 * time.Millisecond)
	if reads := reader.readCount(); reads != 1 {
		t.Fatalf("upstream advanced while downstream was blocked: reads=%d", reads)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
