package service

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"cpacloud.local/server/internal/protocolconv"
)

func geminiConvertedSuccessSSE() string {
	return "data: {\"responseId\":\"g\",\"modelVersion\":\"gemini-test\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"ok\"}]}}]}\n\n" +
		"data: {\"candidates\":[{\"content\":{\"role\":\"model\"},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1,\"totalTokenCount\":3}}\n\n"
}

func TestProtocolRuntimeGeminiWireObservesBeforeTypedResponsesOutput(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToGemini, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolGeminiGenerate)
	doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream", geminiConvertedSuccessSSE())}
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid/v1beta/models/m:streamGenerateContent?alt=sse", nil)
	var order []string
	var names []string
	result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), func(protocol protocolconv.Protocol, raw []byte) error {
		if protocol != protocolconv.ProtocolGeminiGenerate || len(raw) == 0 {
			t.Fatalf("unexpected raw observation: %q %q", protocol, raw)
		}
		order = append(order, "observe")
		return nil
	}, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
		order = append(order, "write")
		names = append(names, event.Name)
		return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if doer.calls.Load() != 1 || !result.Completed || result.TerminalOutcome != protocolconv.StreamTerminalCompleted || !result.SemanticCommitted {
		t.Fatalf("calls=%d result=%#v", doer.calls.Load(), result)
	}
	if len(order) == 0 || order[0] != "observe" || names[0] != "response.created" || names[len(names)-1] != "response.completed" {
		t.Fatalf("order=%#v names=%#v", order, names)
	}
}

func TestProtocolRuntimeResponsesWireEmitsDataOnlyGeminiFunction(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanGeminiToResponses, protocolconv.ProtocolGeminiGenerate, protocolconv.ProtocolOpenAIResponses)
	doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream", responsesSuccessSSE())}
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid/v1/responses", nil)
	observed := 0
	semantic := 0
	terminal := 0
	result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), func(protocol protocolconv.Protocol, raw []byte) error {
		if protocol != protocolconv.ProtocolOpenAIResponses || len(raw) == 0 {
			t.Fatalf("unexpected raw observation: %q %q", protocol, raw)
		}
		observed++
		return nil
	}, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
		if event.Name != "" {
			t.Fatalf("Gemini output must be data-only: %#v", event)
		}
		if event.Semantic {
			semantic++
			if !bytes.Contains(event.Data, []byte(`"functionCall"`)) {
				t.Fatalf("unexpected semantic event: %s", event.Data)
			}
		}
		if event.Terminal {
			terminal++
		}
		return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
	})
	if err != nil || doer.calls.Load() != 1 || !result.Completed || observed != 6 || semantic != 1 || terminal != 1 {
		t.Fatalf("calls=%d result=%#v observed=%d semantic=%d terminal=%d err=%v", doer.calls.Load(), result, observed, semantic, terminal, err)
	}
}

func TestProtocolRuntimeGeminiTerminalBatchWithheldOnInvalidTail(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToGemini, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolGeminiGenerate)
	tail := "data: {\"responseId\":\"g\",\"modelVersion\":\"gemini-test\",\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"late\"}]}}]}\n\n"
	doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream", geminiConvertedSuccessSSE()+tail)}
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid", nil)
	terminalWrites := 0
	result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), nil, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
		if event.Terminal || strings.HasSuffix(event.Name, ".done") {
			terminalWrites++
		}
		return protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}, nil
	})
	if !errors.Is(err, protocolconv.ErrInvalidUpstream) || result.Completed || terminalWrites != 0 {
		t.Fatalf("result=%#v terminalWrites=%d err=%v", result, terminalWrites, err)
	}
}

func TestProtocolRuntimeResponsesIncompleteMapsToGeminiIncomplete(t *testing.T) {
	body := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r\",\"created_at\":1,\"model\":\"m\"}}\n\n" +
		"event: response.incomplete\ndata: {\"type\":\"response.incomplete\",\"sequence_number\":1,\"response\":{\"id\":\"r\",\"object\":\"response\",\"created_at\":1,\"model\":\"m\",\"status\":\"incomplete\",\"incomplete_details\":{\"reason\":\"max_output_tokens\"},\"output\":[]}}\n\n"
	runtime := testProtocolStreamRuntime(protocolconv.PlanGeminiToResponses, protocolconv.ProtocolGeminiGenerate, protocolconv.ProtocolOpenAIResponses)
	doer := &protocolStreamDoer{response: streamResponse(http.StatusOK, "text/event-stream", body)}
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid", nil)
	result, err := runtime.executeStream(context.Background(), doer, request, testProtocolStreamLimits(), nil, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
		return protocolStreamWriteResult{DownstreamCommitted: true}, nil
	})
	if err != nil || result.Completed || result.TerminalOutcome != protocolconv.StreamTerminalIncomplete {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
