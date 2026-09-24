package protocolconv

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
)

func feedMessagesStream(t *testing.T, stream *MessagesToResponsesStream, events ...string) []SSEEvent {
	t.Helper()
	var converted []SSEEvent
	for _, event := range events {
		result, err := stream.Feed([]byte(event))
		if err != nil {
			t.Fatalf("feed %s: %v", event, err)
		}
		converted = append(converted, result...)
	}
	return converted
}

func messagesStart(id string) string {
	return fmt.Sprintf(`{"type":"message_start","message":{"id":%q,"type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":0,"cache_read_input_tokens":2,"cache_creation_input_tokens":1}}}`, id)
}

func TestMessagesToResponsesStreamTextUsagePingAndCompletedOutcome(t *testing.T) {
	stream := new(MessagesToResponsesStream)
	events := feedMessagesStream(t, stream,
		`{"type":"ping"}`,
		messagesStart("msg_1"),
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}`,
		`{"type":"ping"}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`,
		`{"type":"message_stop"}`,
	)
	if err := stream.EOF(); err != nil {
		t.Fatal(err)
	}
	if len(events) != 9 {
		t.Fatalf("events=%d", len(events))
	}
	terminal := events[len(events)-1]
	if !terminal.Terminal || terminal.TerminalOutcome != StreamTerminalCompleted || terminal.Name != "response.completed" {
		t.Fatalf("terminal=%#v", terminal)
	}
	if bytes.Contains(terminal.Data, []byte("cached_tokens")) || bytes.Contains(terminal.Data, []byte("cache_write_tokens")) {
		t.Fatalf("cache-specific usage leaked into Responses usage: %s", terminal.Data)
	}
	if !bytes.Contains(terminal.Data, []byte(`"input_tokens":4`)) || !bytes.Contains(terminal.Data, []byte(`"output_tokens":3`)) || !bytes.Contains(terminal.Data, []byte(`"total_tokens":7`)) {
		t.Fatalf("unexpected usage: %s", terminal.Data)
	}
	for _, event := range events[:len(events)-1] {
		if event.TerminalOutcome != StreamTerminalNone {
			t.Fatalf("nonterminal event has outcome: %#v", event)
		}
	}
}

func TestMessagesResponsesRoundTripFunctionsAndStableFragments(t *testing.T) {
	source := new(MessagesToResponsesStream)
	responses := feedMessagesStream(t, source,
		messagesStart("msg_tools"),
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"call_0","name":"first","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"a\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"1}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"second","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	)
	if err := source.EOF(); err != nil {
		t.Fatal(err)
	}

	target := new(ResponsesToMessagesStream)
	var messages []SSEEvent
	for _, event := range responses {
		converted, err := target.Feed(event.Data)
		if err != nil {
			t.Fatalf("round trip %s: %v\n%s", event.Name, err, event.Data)
		}
		messages = append(messages, converted...)
	}
	if err := target.EOF(); err != nil {
		t.Fatal(err)
	}
	var names []string
	var wire bytes.Buffer
	for _, event := range messages {
		names = append(names, event.Name)
		wire.Write(event.Data)
	}
	wantNames := []string{
		"message_start", "content_block_start", "content_block_delta", "content_block_delta", "content_block_delta", "content_block_stop",
		"content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop",
	}
	if fmt.Sprint(names) != fmt.Sprint(wantNames) {
		t.Fatalf("names=%v", names)
	}
	if !bytes.Contains(wire.Bytes(), []byte(`"partial_json":""`)) || !bytes.Contains(wire.Bytes(), []byte(`"id":"call_0"`)) || !bytes.Contains(wire.Bytes(), []byte(`"id":"call_1"`)) {
		t.Fatalf("function identity or empty fragment was not preserved: %s", wire.Bytes())
	}
	last := messages[len(messages)-1]
	if !last.Terminal || last.TerminalOutcome != StreamTerminalCompleted {
		t.Fatalf("terminal=%#v", last)
	}
}

func TestResponsesToMessagesStreamOrdersParallelFunctionBlocks(t *testing.T) {
	stream := new(ResponsesToMessagesStream)
	events := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"response_id":"r","output_index":0,"item":{"id":"fc0","type":"function_call","status":"in_progress","call_id":"call0","name":"first","arguments":""}}`,
		`{"type":"response.output_item.added","sequence_number":2,"response_id":"r","output_index":1,"item":{"id":"fc1","type":"function_call","status":"in_progress","call_id":"call1","name":"second","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":3,"response_id":"r","item_id":"fc1","output_index":1,"delta":"{}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":4,"response_id":"r","item_id":"fc1","output_index":1,"arguments":"{}"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":5,"response_id":"r","item_id":"fc0","output_index":0,"delta":"{}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":6,"response_id":"r","item_id":"fc0","output_index":0,"arguments":"{}"}`,
		`{"type":"response.output_item.done","sequence_number":7,"response_id":"r","output_index":1,"item":{"id":"fc1","type":"function_call","status":"completed","call_id":"call1","name":"second","arguments":"{}"}}`,
		`{"type":"response.output_item.done","sequence_number":8,"response_id":"r","output_index":0,"item":{"id":"fc0","type":"function_call","status":"completed","call_id":"call0","name":"first","arguments":"{}"}}`,
		`{"type":"response.completed","sequence_number":9,"response":{"id":"r","object":"response","created_at":1,"model":"m","status":"completed","output":[{"id":"fc0","type":"function_call","status":"completed","call_id":"call0","name":"first","arguments":"{}"},{"id":"fc1","type":"function_call","status":"completed","call_id":"call1","name":"second","arguments":"{}"}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`,
	}
	var blockEvents []string
	for _, raw := range events {
		converted, err := stream.Feed([]byte(raw))
		if err != nil {
			t.Fatalf("feed: %v\n%s", err, raw)
		}
		for _, event := range converted {
			if event.Name == "content_block_start" || event.Name == "content_block_delta" || event.Name == "content_block_stop" {
				var payload map[string]json.RawMessage
				_ = json.Unmarshal(event.Data, &payload)
				blockEvents = append(blockEvents, fmt.Sprintf("%s:%s", event.Name, payload["index"]))
			}
		}
	}
	want := []string{"content_block_start:0", "content_block_delta:0", "content_block_stop:0", "content_block_start:1", "content_block_delta:1", "content_block_stop:1"}
	if fmt.Sprint(blockEvents) != fmt.Sprint(want) {
		t.Fatalf("interleaved Messages blocks: %v", blockEvents)
	}
}

func TestMessagesResponsesIncompleteAndFailuresAreNonSuccessTerminals(t *testing.T) {
	t.Run("Messages max tokens", func(t *testing.T) {
		stream := new(MessagesToResponsesStream)
		events := feedMessagesStream(t, stream,
			messagesStart("msg_limit"),
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		)
		terminal := events[len(events)-1]
		if terminal.Name != "response.incomplete" || terminal.TerminalOutcome != StreamTerminalIncomplete || !bytes.Contains(terminal.Data, []byte(`"reason":"max_output_tokens"`)) {
			t.Fatalf("terminal=%#v", terminal)
		}
	})

	t.Run("Messages error redaction", func(t *testing.T) {
		stream := new(MessagesToResponsesStream)
		events := feedMessagesStream(t, stream, `{"type":"error","error":{"type":"overloaded_error","message":"private-secret"}}`)
		if len(events) != 1 || events[0].TerminalOutcome != StreamTerminalFailed || bytes.Contains(events[0].Data, []byte("private-secret")) {
			t.Fatalf("events=%#v", events)
		}
	})

	t.Run("Responses error redaction", func(t *testing.T) {
		stream := new(ResponsesToMessagesStream)
		events, err := stream.Feed([]byte(`{"type":"error","sequence_number":0,"code":"server_error","message":"private-secret","param":null}`))
		if err != nil || len(events) != 1 || events[0].TerminalOutcome != StreamTerminalFailed || bytes.Contains(events[0].Data, []byte("private-secret")) {
			t.Fatalf("events=%#v err=%v", events, err)
		}
	})

	t.Run("Responses failed redaction", func(t *testing.T) {
		stream := new(ResponsesToMessagesStream)
		if _, err := stream.Feed([]byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`)); err != nil {
			t.Fatal(err)
		}
		events, err := stream.Feed([]byte(`{"type":"response.failed","sequence_number":1,"response":{"id":"r","object":"response","created_at":1,"model":"m","status":"failed","error":{"code":"server_error","message":"private-secret"}}}`))
		if err != nil || len(events) != 1 || events[0].TerminalOutcome != StreamTerminalFailed || bytes.Contains(events[0].Data, []byte("private-secret")) {
			t.Fatalf("events=%#v err=%v", events, err)
		}
	})

	t.Run("Responses incomplete", func(t *testing.T) {
		source := new(MessagesToResponsesStream)
		responses := feedMessagesStream(t, source,
			messagesStart("msg_incomplete"),
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"cut"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":3}}`,
			`{"type":"message_stop"}`,
		)
		target := new(ResponsesToMessagesStream)
		var messages []SSEEvent
		for _, event := range responses {
			converted, err := target.Feed(event.Data)
			if err != nil {
				t.Fatal(err)
			}
			messages = append(messages, converted...)
		}
		if !bytes.Contains(messages[len(messages)-2].Data, []byte(`"stop_reason":"max_tokens"`)) || messages[len(messages)-1].TerminalOutcome != StreamTerminalIncomplete {
			t.Fatalf("messages=%#v", messages[len(messages)-2:])
		}
	})
}

func TestMessagesToResponsesStreamRejectsInvalidStateAndArguments(t *testing.T) {
	tests := []struct {
		name   string
		events []string
	}{
		{name: "unknown event", events: []string{`{"type":"content_block_mystery"}`}},
		{name: "thinking block", events: []string{messagesStart("m"), `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`}},
		{name: "interleaved blocks", events: []string{messagesStart("m"), `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`, `{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`}},
		{name: "duplicate tool id", events: []string{messagesStart("m"), `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"same","name":"f","input":{}}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`, `{"type":"content_block_stop","index":0}`, `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"same","name":"g","input":{}}}`}},
		{name: "truncated arguments", events: []string{messagesStart("m"), `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c","name":"f","input":{}}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":"}}`, `{"type":"content_block_stop","index":0}`}},
		{name: "array arguments", events: []string{messagesStart("m"), `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c","name":"f","input":{}}}`, `{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"[]"}}`, `{"type":"content_block_stop","index":0}`}},
		{name: "no argument fragments", events: []string{messagesStart("m"), `{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"c","name":"f","input":{}}}`, `{"type":"content_block_stop","index":0}`}},
		{name: "decreasing usage", events: []string{`{"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":2}}}`, `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":1}}`}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := new(MessagesToResponsesStream)
			var err error
			for _, raw := range test.events {
				_, err = stream.Feed([]byte(raw))
				if err != nil {
					break
				}
			}
			if !errors.Is(err, ErrInvalidUpstream) && !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("expected closed failure, got %v", err)
			}
		})
	}

	if err := new(MessagesToResponsesStream).EOF(); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("premature EOF: %v", err)
	}
}

func TestMessagesStreamConverterRequiresNamedMessagesEventsAndTerminalOutcomes(t *testing.T) {
	converter, err := NewStreamConverter(streamingPrepared(PlanResponsesToMessages, ProtocolOpenAIResponses, ProtocolAnthropicMessages))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := converter.FeedFrame(SSEFrame{Data: []byte(`{"type":"ping"}`)}); !errors.Is(err, ErrInvalidUpstream) {
		t.Fatalf("unnamed Messages event accepted: %v", err)
	}

	converter, _ = NewStreamConverter(streamingPrepared(PlanResponsesToMessages, ProtocolOpenAIResponses, ProtocolAnthropicMessages))
	events, err := converter.FeedFrame(SSEFrame{Event: "error", Data: []byte(`{"type":"error","error":{"type":"api_error","message":"secret"}}`)})
	if err != nil || len(events) != 1 || events[0].TerminalOutcome != StreamTerminalFailed {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if err := converter.EOF(); err != nil {
		t.Fatal(err)
	}

	responses, err := NewStreamConverter(streamingPrepared(PlanMessagesToResponses, ProtocolAnthropicMessages, ProtocolOpenAIResponses))
	if err != nil {
		t.Fatal(err)
	}
	failed, err := responses.FeedFrame(SSEFrame{Event: "error", Data: []byte(`{"type":"error","sequence_number":0,"code":"server_error","message":"private-secret","param":null}`)})
	if err != nil || len(failed) != 1 || failed[0].Name != "error" || failed[0].TerminalOutcome != StreamTerminalFailed || bytes.Contains(failed[0].Data, []byte("private-secret")) {
		t.Fatalf("failed=%#v err=%v", failed, err)
	}
	if err := responses.EOF(); err != nil {
		t.Fatal(err)
	}
}
