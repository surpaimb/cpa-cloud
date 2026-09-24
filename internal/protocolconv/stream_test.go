package protocolconv

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestChatToResponsesStreamTextToolUsageAndTerminal(t *testing.T) {
	stream := new(ChatToResponsesStream)
	chunks := []string{
		`{"id":"chat_1","object":"chat.completion.chunk","created":1700000000,"model":"actual","choices":[{"index":0,"delta":{"role":"assistant","content":"Hi "},"finish_reason":null}]}`,
		`{"id":"chat_1","object":"chat.completion.chunk","created":1700000000,"model":"actual","choices":[{"index":0,"delta":{"content":"there"},"finish_reason":null}]}`,
		`{"id":"chat_1","object":"chat.completion.chunk","created":1700000000,"model":"actual","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":"}}]},"finish_reason":null}]}`,
		`{"id":"chat_1","object":"chat.completion.chunk","created":1700000000,"model":"actual","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]},"finish_reason":"tool_calls"}]}`,
		`{"id":"chat_1","object":"chat.completion.chunk","created":1700000000,"model":"actual","choices":[],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}}`,
	}
	var names []string
	for _, chunk := range chunks {
		events, err := stream.Feed([]byte(chunk))
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			names = append(names, event.Name)
		}
	}
	terminal, err := stream.Finish()
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range terminal {
		names = append(names, event.Name)
	}
	wantNames := map[string]bool{
		"response.created": true, "response.in_progress": true,
		"response.output_item.added": true, "response.output_text.delta": true,
		"response.function_call_arguments.delta": true, "response.function_call_arguments.done": true,
		"response.output_item.done": true, "response.completed": true,
	}
	for name := range wantNames {
		if !contains(names, name) {
			t.Fatalf("missing %s in %#v", name, names)
		}
	}
	last := terminal[len(terminal)-1]
	if !last.Terminal || last.Name != "response.completed" {
		t.Fatalf("unexpected terminal: %#v", last)
	}
	var envelope struct {
		Response struct {
			Status string           `json:"status"`
			Usage  map[string]int64 `json:"usage"`
			Output []map[string]any `json:"output"`
		} `json:"response"`
	}
	if err := json.Unmarshal(last.Data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Response.Status != "completed" || envelope.Response.Usage["total_tokens"] != 11 || len(envelope.Response.Output) != 2 {
		t.Fatalf("unexpected terminal response: %#v", envelope.Response)
	}
}

func TestChatToResponsesStreamRequiresExplicitDone(t *testing.T) {
	stream := new(ChatToResponsesStream)
	_, err := stream.Feed([]byte(`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.EOF(); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected interrupted, got %v", err)
	}
}

func TestResponsesToChatStreamTextToolUsageAndTerminal(t *testing.T) {
	stream := new(ResponsesToChatStream)
	events := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"resp_1","object":"response","created_at":1700000000,"model":"actual","status":"in_progress","output":[]}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg_1","type":"message","status":"in_progress","role":"assistant","phase":"final_answer","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hi"}`,
		`{"type":"response.output_text.done","sequence_number":4,"item_id":"msg_1","output_index":0,"content_index":0,"text":"Hi"}`,
		`{"type":"response.content_part.done","sequence_number":5,"item_id":"msg_1","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hi","annotations":[]}}`,
		`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"msg_1","type":"message","status":"completed","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hi","annotations":[]}]}}`,
		`{"type":"response.output_item.added","sequence_number":7,"output_index":1,"item":{"id":"fc_1","type":"function_call","status":"in_progress","call_id":"call_1","name":"lookup","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":8,"item_id":"fc_1","output_index":1,"delta":"{\"q\":1}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":9,"item_id":"fc_1","output_index":1,"arguments":"{\"q\":1}"}`,
		`{"type":"response.output_item.done","sequence_number":10,"output_index":1,"item":{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{\"q\":1}"}}`,
		`{"type":"response.completed","sequence_number":11,"response":{"id":"resp_1","object":"response","created_at":1700000000,"model":"actual","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"Hi","annotations":[]}]},{"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{\"q\":1}"}],"usage":{"input_tokens":8,"output_tokens":3,"total_tokens":11}}}`,
	}
	var output []SSEEvent
	for _, event := range events {
		converted, err := stream.Feed([]byte(event))
		if err != nil {
			t.Fatal(err)
		}
		output = append(output, converted...)
	}
	if len(output) != 5 || string(output[len(output)-1].Data) != "[DONE]" || !output[len(output)-1].Terminal {
		t.Fatalf("unexpected output events: %#v", output)
	}
	var final map[string]any
	if err := json.Unmarshal(output[len(output)-2].Data, &final); err != nil {
		t.Fatal(err)
	}
	if final["usage"].(map[string]any)["total_tokens"] != float64(11) {
		t.Fatalf("usage missing: %#v", final)
	}
	choice := final["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("unexpected finish: %#v", choice)
	}
	if err := stream.EOF(); err != nil {
		t.Fatalf("terminal EOF should be harmless: %v", err)
	}
}

func TestResponsesToChatStreamRejectsFailureAndEarlyEOF(t *testing.T) {
	stream := new(ResponsesToChatStream)
	if _, err := stream.Feed([]byte(`{"type":"response.failed","sequence_number":0,"response":{"id":"r"}}`)); !errors.Is(err, ErrInvalidUpstream) {
		t.Fatalf("expected failed event rejection, got %v", err)
	}
	if err := stream.EOF(); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected interrupted, got %v", err)
	}
}

func TestChatToResponsesStreamRejectsOutOfOrderToolIndexes(t *testing.T) {
	stream := new(ChatToResponsesStream)
	_, err := stream.Feed([]byte(`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_second","type":"function","function":{"name":"second","arguments":"{}"}}]},"finish_reason":null}]}`))
	if !errors.Is(err, ErrInvalidUpstream) {
		t.Fatalf("expected out-of-order tool index rejection, got %v", err)
	}
}

func TestChatToResponsesStreamRejectsInconsistentFinish(t *testing.T) {
	tests := []string{
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call","type":"function","function":{"name":"f","arguments":"{}"}}]},"finish_reason":"stop"}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
		`{"id":"c","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
	}
	for _, chunk := range tests {
		stream := new(ChatToResponsesStream)
		if _, err := stream.Feed([]byte(chunk)); err != nil {
			t.Fatalf("feed: %v", err)
		}
		if _, err := stream.Finish(); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("expected inconsistent finish rejection for %s, got %v", chunk, err)
		}
	}
}

func TestResponsesToChatStreamRejectsPhantomOrChangedTerminalOutput(t *testing.T) {
	base := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg","output_index":0,"content_index":0,"delta":"sent"}`,
		`{"type":"response.output_text.done","sequence_number":4,"item_id":"msg","output_index":0,"content_index":0,"text":"sent"}`,
		`{"type":"response.content_part.done","sequence_number":5,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"sent","annotations":[]}}`,
		`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"sent","annotations":[]}]}}`,
	}
	terminals := []string{
		`{"type":"response.completed","sequence_number":7,"response":{"id":"r","object":"response","created_at":1,"model":"m","status":"completed","output":[{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"changed","annotations":[]}]}]}}`,
		`{"type":"response.completed","sequence_number":7,"response":{"id":"r","object":"response","created_at":1,"model":"m","status":"completed","output":[{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"sent","annotations":[]}]},{"id":"phantom","type":"function_call","status":"completed","call_id":"c","name":"f","arguments":"{}"}]}}`,
	}
	for _, terminal := range terminals {
		stream := new(ResponsesToChatStream)
		for _, event := range base {
			if _, err := stream.Feed([]byte(event)); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := stream.Feed([]byte(terminal)); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("expected terminal mismatch rejection, got %v", err)
		}
	}
}

func TestResponsesToChatStreamRejectsWrongIndexesAndSequence(t *testing.T) {
	stream := new(ResponsesToChatStream)
	if _, err := stream.Feed([]byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := stream.Feed([]byte(`{"type":"response.output_item.added","sequence_number":2,"output_index":0,"item":{"id":"msg","type":"message","status":"in_progress","role":"assistant","content":[]}}`)); !errors.Is(err, ErrInvalidUpstream) {
		t.Fatalf("expected sequence rejection, got %v", err)
	}

	stream = new(ResponsesToChatStream)
	valid := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
	}
	for _, event := range valid {
		if _, err := stream.Feed([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := stream.Feed([]byte(`{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg","output_index":1,"content_index":0,"delta":"x"}`)); !errors.Is(err, ErrInvalidUpstream) {
		t.Fatalf("expected output index rejection, got %v", err)
	}
}

func TestResponsesToChatStreamRequiresCompleteOrderedSubevents(t *testing.T) {
	startMessage := func(t *testing.T) *ResponsesToChatStream {
		t.Helper()
		stream := new(ResponsesToChatStream)
		for _, event := range []string{
			`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`,
			`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		} {
			if _, err := stream.Feed([]byte(event)); err != nil {
				t.Fatal(err)
			}
		}
		return stream
	}

	t.Run("delta before part", func(t *testing.T) {
		stream := startMessage(t)
		_, err := stream.Feed([]byte(`{"type":"response.output_text.delta","sequence_number":2,"item_id":"msg","output_index":0,"content_index":0,"delta":"x"}`))
		if !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("duplicate part added", func(t *testing.T) {
		stream := startMessage(t)
		part := `{"type":"response.content_part.added","sequence_number":2,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`
		if _, err := stream.Feed([]byte(part)); err != nil {
			t.Fatal(err)
		}
		part = `{"type":"response.content_part.added","sequence_number":3,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`
		if _, err := stream.Feed([]byte(part)); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("item done before content done", func(t *testing.T) {
		stream := startMessage(t)
		for _, event := range []string{
			`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
			`{"type":"response.output_text.done","sequence_number":3,"item_id":"msg","output_index":0,"content_index":0,"text":""}`,
		} {
			if _, err := stream.Feed([]byte(event)); err != nil {
				t.Fatal(err)
			}
		}
		_, err := stream.Feed([]byte(`{"type":"response.output_item.done","sequence_number":4,"output_index":0,"item":{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"","annotations":[]}]}}`))
		if !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("duplicate text done", func(t *testing.T) {
		stream := startMessage(t)
		for _, event := range []string{
			`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
			`{"type":"response.output_text.done","sequence_number":3,"item_id":"msg","output_index":0,"content_index":0,"text":""}`,
		} {
			if _, err := stream.Feed([]byte(event)); err != nil {
				t.Fatal(err)
			}
		}
		_, err := stream.Feed([]byte(`{"type":"response.output_text.done","sequence_number":4,"item_id":"msg","output_index":0,"content_index":0,"text":""}`))
		if !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("terminal before item done", func(t *testing.T) {
		stream := startMessage(t)
		for _, event := range []string{
			`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
			`{"type":"response.output_text.done","sequence_number":3,"item_id":"msg","output_index":0,"content_index":0,"text":""}`,
			`{"type":"response.content_part.done","sequence_number":4,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		} {
			if _, err := stream.Feed([]byte(event)); err != nil {
				t.Fatal(err)
			}
		}
		_, err := stream.Feed([]byte(`{"type":"response.completed","sequence_number":5,"response":{"id":"r","object":"response","created_at":1,"model":"m","status":"completed","output":[{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"","annotations":[]}]}]}}`))
		if !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("function item done before arguments done", func(t *testing.T) {
		stream := new(ResponsesToChatStream)
		for _, event := range []string{
			`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`,
			`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc","type":"function_call","status":"in_progress","call_id":"call","name":"f","arguments":""}}`,
		} {
			if _, err := stream.Feed([]byte(event)); err != nil {
				t.Fatal(err)
			}
		}
		_, err := stream.Feed([]byte(`{"type":"response.output_item.done","sequence_number":2,"output_index":0,"item":{"id":"fc","type":"function_call","status":"completed","call_id":"call","name":"f","arguments":""}}`))
		if !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
