package protocolconv

import (
	"encoding/json"
	"errors"
	"testing"
)

func streamingPrepared(kind PlanKind, client, upstream Protocol) PreparedRequest {
	return PreparedRequest{Plan: Plan{Kind: kind, ClientProtocol: client, UpstreamProtocol: upstream}, Streaming: true}
}

func TestStreamConverterResponsesToChatSemanticAndStrictTerminal(t *testing.T) {
	converter, err := NewStreamConverter(streamingPrepared(PlanChatToResponses, ProtocolOpenAIChat, ProtocolOpenAIResponses))
	if err != nil {
		t.Fatal(err)
	}
	frames := []SSEFrame{
		{Event: "response.created", Data: []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`)},
		{Event: "response.output_item.added", Data: []byte(`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg","type":"message","status":"in_progress","role":"assistant","content":[]}}`)},
		{Event: "response.content_part.added", Data: []byte(`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`)},
		{Event: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg","output_index":0,"content_index":0,"delta":"ok","obfuscation":"pad"}`)},
		{Event: "response.output_text.done", Data: []byte(`{"type":"response.output_text.done","sequence_number":4,"item_id":"msg","output_index":0,"content_index":0,"text":"ok"}`)},
		{Event: "response.content_part.done", Data: []byte(`{"type":"response.content_part.done","sequence_number":5,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"ok","annotations":[]}}`)},
		{Event: "response.output_item.done", Data: []byte(`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}}`)},
		{Event: "response.completed", Data: []byte(`{"type":"response.completed","sequence_number":7,"response":{"id":"r","object":"response","created_at":1,"model":"m","status":"completed","output":[{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}}`)},
	}
	semantic := 0
	terminal := 0
	for _, frame := range frames {
		events, err := converter.FeedFrame(frame)
		if err != nil {
			t.Fatalf("feed %s: %v", frame.Event, err)
		}
		for _, event := range events {
			if event.Semantic {
				semantic++
			}
			if event.Terminal {
				terminal++
				if event.Semantic {
					t.Fatal("terminal envelope must not be semantic")
				}
			}
		}
	}
	if semantic != 1 || terminal != 1 {
		t.Fatalf("semantic=%d terminal=%d", semantic, terminal)
	}
	if err := converter.EOF(); err != nil {
		t.Fatal(err)
	}
	if _, err := converter.FeedFrame(frames[len(frames)-1]); !errors.Is(err, ErrInvalidUpstream) {
		t.Fatalf("expected post-terminal rejection, got %v", err)
	}
}

func TestStreamConverterChatToResponsesFunctionArgumentsAndObfuscation(t *testing.T) {
	converter, err := NewStreamConverter(streamingPrepared(PlanResponsesToChat, ProtocolOpenAIResponses, ProtocolOpenAIChat))
	if err != nil {
		t.Fatal(err)
	}
	frames := []SSEFrame{
		{Data: []byte(`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","obfuscation":"pad","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]},"finish_reason":null}]}`)},
		{Data: []byte(`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`)},
		{Data: []byte("[DONE]")},
	}
	semantic := 0
	terminal := 0
	for _, frame := range frames {
		events, err := converter.FeedFrame(frame)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			if event.Semantic {
				semantic++
			}
			if event.Terminal {
				terminal++
			}
		}
	}
	if semantic != 3 || terminal != 1 {
		t.Fatalf("semantic=%d terminal=%d", semantic, terminal)
	}
	if err := converter.EOF(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamConverterRejectsMismatchedEventsObfuscationAndArguments(t *testing.T) {
	t.Run("event mismatch", func(t *testing.T) {
		converter, _ := NewStreamConverter(streamingPrepared(PlanChatToResponses, ProtocolOpenAIChat, ProtocolOpenAIResponses))
		_, err := converter.FeedFrame(SSEFrame{Event: "response.completed", Data: []byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`)})
		if !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("obfuscation type", func(t *testing.T) {
		converter, _ := NewStreamConverter(streamingPrepared(PlanResponsesToChat, ProtocolOpenAIResponses, ProtocolOpenAIChat))
		_, err := converter.FeedFrame(SSEFrame{Data: []byte(`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","obfuscation":1,"choices":[]}`)})
		if !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
	for _, arguments := range []string{"", "[]", "{\"q\":"} {
		t.Run("arguments_"+arguments, func(t *testing.T) {
			converter, _ := NewStreamConverter(streamingPrepared(PlanResponsesToChat, ProtocolOpenAIResponses, ProtocolOpenAIChat))
			encodedArguments, _ := json.Marshal(arguments)
			chunk := `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call","type":"function","function":{"name":"f","arguments":` + string(encodedArguments) + `}}]},"finish_reason":"tool_calls"}]}`
			if _, err := converter.FeedFrame(SSEFrame{Data: []byte(chunk)}); err != nil {
				t.Fatal(err)
			}
			if _, err := converter.FeedFrame(SSEFrame{Data: []byte("[DONE]")}); !errors.Is(err, ErrInvalidUpstream) {
				t.Fatalf("expected invalid final arguments, got %v", err)
			}
		})
	}
}

func TestChatToResponsesStreamParallelFunctionsRequireUniqueIDsAndOrderedIndexes(t *testing.T) {
	stream := new(ChatToResponsesStream)
	parallel := `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_0","type":"function","function":{"name":"first","arguments":"{}"}},{"index":1,"id":"call_1","type":"function","function":{"name":"second","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	if _, err := stream.Feed([]byte(parallel)); err != nil {
		t.Fatal(err)
	}
	if events, err := stream.Finish(); err != nil || len(events) == 0 || !events[len(events)-1].Terminal {
		t.Fatalf("parallel functions did not complete: events=%d err=%v", len(events), err)
	}

	duplicate := new(ChatToResponsesStream)
	first := `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"same","type":"function","function":{"name":"first","arguments":"{}"}}]},"finish_reason":null}]}`
	second := `{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"same","type":"function","function":{"name":"second","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	if _, err := duplicate.Feed([]byte(first)); err != nil {
		t.Fatal(err)
	}
	if _, err := duplicate.Feed([]byte(second)); !errors.Is(err, ErrInvalidUpstream) {
		t.Fatalf("expected duplicate call id rejection, got %v", err)
	}
}

func TestPrepareCrossProtocolStreamRequestIsExplicitAndNarrow(t *testing.T) {
	features := Features(FeatureText, FeatureUsage, FeatureFinishReason)
	capability := RouteCapability{
		ClientProtocol: ProtocolOpenAIChat, UpstreamProtocol: ProtocolOpenAIResponses,
		Streaming: true, RequestStreaming: true, Features: features,
	}
	prepared, err := PrepareCrossProtocolStreamRequest(capability, "wire-model", []byte(`{"model":"public","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || !prepared.Streaming || prepared.Plan.Kind != PlanChatToResponses {
		t.Fatalf("unexpected preparation: %#v, %v", prepared, err)
	}
	if _, err := PrepareRequest(capability, "wire-model", []byte(`{"model":"public","stream":true,"messages":[{"role":"user","content":"hi"}]}`)); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("production JSON path must remain closed, got %v", err)
	}
	capability.ClientProtocol = ProtocolAnthropicMessages
	prepared, err = PrepareCrossProtocolStreamRequest(capability, "wire-model", []byte(`{"model":"public","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil || !prepared.Streaming || prepared.Plan.Kind != PlanMessagesToResponses {
		t.Fatalf("unexpected Messages preparation: %#v, %v", prepared, err)
	}
}
