package protocolconv

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestGeminiToResponsesStreamTextFunctionUsageAndTerminal(t *testing.T) {
	stream := new(GeminiToResponsesStream)
	frames := []string{
		`{"responseId":"g1","modelVersion":"gemini-test","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Hi "}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"there"}]}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2,"totalTokenCount":4}}`,
		`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call_weather","name":"weather","args":{"city":"Paris"}}}]}}]}`,
		`{"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5,"cachedContentTokenCount":1}}`,
	}
	var output []SSEEvent
	for _, frame := range frames {
		events, err := stream.Feed([]byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		output = append(output, events...)
	}
	if err := stream.EOF(); err != nil {
		t.Fatal(err)
	}
	wantDeltas := []string{"Hi ", "there"}
	var deltas []string
	var argumentDeltas []string
	for index, event := range output {
		var root map[string]any
		if err := json.Unmarshal(event.Data, &root); err != nil {
			t.Fatalf("event %d: %v", index, err)
		}
		if root["sequence_number"] != float64(index) {
			t.Fatalf("event %d sequence=%v", index, root["sequence_number"])
		}
		switch event.Name {
		case "response.output_text.delta":
			deltas = append(deltas, root["delta"].(string))
		case "response.function_call_arguments.delta":
			argumentDeltas = append(argumentDeltas, root["delta"].(string))
		}
	}
	if fmt.Sprint(deltas) != fmt.Sprint(wantDeltas) {
		t.Fatalf("text replayed or reordered: %#v", deltas)
	}
	if len(argumentDeltas) != 1 || argumentDeltas[0] != `{"city":"Paris"}` {
		t.Fatalf("function arguments not emitted once: %#v", argumentDeltas)
	}
	terminal := output[len(output)-1]
	if terminal.Name != "response.completed" || !terminal.Terminal || terminal.TerminalOutcome != StreamTerminalCompleted {
		t.Fatalf("unexpected terminal: %#v", terminal)
	}
	var envelope struct {
		Response struct {
			Status string `json:"status"`
			Output []struct {
				Type      string `json:"type"`
				CallID    string `json:"call_id"`
				Arguments string `json:"arguments"`
				Content   []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"output"`
			Usage struct {
				Input   int64 `json:"input_tokens"`
				Output  int64 `json:"output_tokens"`
				Total   int64 `json:"total_tokens"`
				Details struct {
					Cached int64 `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if err := json.Unmarshal(terminal.Data, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Response.Status != "completed" || len(envelope.Response.Output) != 2 ||
		envelope.Response.Output[0].Content[0].Text != "Hi there" ||
		envelope.Response.Output[1].CallID != "call_weather" || envelope.Response.Output[1].Arguments != `{"city":"Paris"}` ||
		envelope.Response.Usage.Input != 2 || envelope.Response.Usage.Output != 3 || envelope.Response.Usage.Total != 5 || envelope.Response.Usage.Details.Cached != 1 {
		t.Fatalf("unexpected terminal response: %#v", envelope.Response)
	}
}

func TestGeminiToResponsesStreamBuffersLateIdentityWithoutReplay(t *testing.T) {
	stream := new(GeminiToResponsesStream)
	frames := []string{
		`{"candidates":[{"content":{"role":"model","parts":[{"text":"a"}]}}]}`,
		`{"responseId":"late","candidates":[{"content":{"role":"model","parts":[{"text":"b"}]}}]}`,
		`{"modelVersion":"gemini-late","candidates":[{"content":{"role":"model","parts":[{"text":"c"}]}}]}`,
	}
	for index, frame := range frames {
		events, err := stream.Feed([]byte(frame))
		if err != nil {
			t.Fatal(err)
		}
		if index < 2 && len(events) != 0 {
			t.Fatalf("identity-incomplete frame %d emitted %#v", index, events)
		}
		if index == 2 {
			if len(events) < 6 || events[0].Name != "response.created" {
				t.Fatalf("late identity did not start with response.created: %#v", events)
			}
			var deltas []string
			for _, event := range events {
				if event.Name == "response.output_text.delta" {
					var root map[string]any
					_ = json.Unmarshal(event.Data, &root)
					deltas = append(deltas, root["delta"].(string))
				}
			}
			if strings.Join(deltas, "") != "abc" || len(deltas) != 3 {
				t.Fatalf("late actions replayed or reordered: %#v", deltas)
			}
		}
	}
	terminal, err := stream.Feed([]byte(`{"candidates":[{"content":{"role":"model"},"finishReason":"STOP"}]}`))
	if err != nil || len(terminal) == 0 || !terminal[len(terminal)-1].Terminal {
		t.Fatalf("terminal=%#v err=%v", terminal, err)
	}
}

func TestGeminiToResponsesStreamIdentityAndIncompleteRules(t *testing.T) {
	t.Run("terminal identity missing", func(t *testing.T) {
		stream := new(GeminiToResponsesStream)
		events, err := stream.Feed([]byte(`{"responseId":"only-id","candidates":[{"content":{"role":"model","parts":[{"text":"held"}]},"finishReason":"STOP"}]}`))
		if !errors.Is(err, ErrInvalidUpstream) || len(events) != 0 {
			t.Fatalf("events=%#v err=%v", events, err)
		}
	})
	t.Run("identity changes", func(t *testing.T) {
		stream := new(GeminiToResponsesStream)
		if _, err := stream.Feed([]byte(`{"responseId":"one","modelVersion":"m","candidates":[{"content":{"role":"model","parts":[]}}]}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Feed([]byte(`{"responseId":"two","candidates":[{"content":{"role":"model","parts":[]}}]}`)); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("explicit null identity", func(t *testing.T) {
		stream := new(GeminiToResponsesStream)
		if _, err := stream.Feed([]byte(`{"responseId":null,"candidates":[{"content":{"role":"model","parts":[]}}]}`)); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("max tokens", func(t *testing.T) {
		stream := new(GeminiToResponsesStream)
		events, err := stream.Feed([]byte(`{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model","parts":[{"text":"cut"}]},"finishReason":"MAX_TOKENS"}]}`))
		if err != nil {
			t.Fatal(err)
		}
		terminal := events[len(events)-1]
		if terminal.Name != "response.incomplete" || terminal.TerminalOutcome != StreamTerminalIncomplete {
			t.Fatalf("unexpected terminal: %#v", terminal)
		}
		if !strings.Contains(string(terminal.Data), `"reason":"max_output_tokens"`) {
			t.Fatalf("missing incomplete reason: %s", terminal.Data)
		}
	})
}

func TestGeminiToResponsesStreamGeneratesAndReservesCallIDs(t *testing.T) {
	stream := new(GeminiToResponsesStream)
	events, err := stream.Feed([]byte(`{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"first","args":{}}},{"functionCall":{"id":"explicit","name":"second","args":{}}}]},"finishReason":"STOP"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	var callIDs []string
	for _, event := range events {
		if event.Name != "response.output_item.added" {
			continue
		}
		var root struct {
			Item struct {
				Type   string `json:"type"`
				CallID string `json:"call_id"`
			} `json:"item"`
		}
		_ = json.Unmarshal(event.Data, &root)
		if root.Item.Type == "function_call" {
			callIDs = append(callIDs, root.Item.CallID)
		}
	}
	if len(callIDs) != 2 || !strings.HasPrefix(callIDs[0], "call_cpa_") || callIDs[1] != "explicit" || callIDs[0] == callIDs[1] {
		t.Fatalf("unexpected call IDs: %#v", callIDs)
	}

	duplicate := new(GeminiToResponsesStream)
	_, err = duplicate.Feed([]byte(`{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"same","name":"first","args":{}}},{"functionCall":{"id":"same","name":"second","args":{}}}]}}]}`))
	if !errors.Is(err, ErrInvalidUpstream) {
		t.Fatalf("duplicate call IDs should fail: %v", err)
	}
}

func TestGeminiToResponsesStreamRejectsUnsafeOrLossyFrames(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{"prompt block", `{"promptFeedback":{"blockReason":"SAFETY","safetyRatings":[]}}`},
		{"block and candidate", `{"promptFeedback":{"blockReason":"SAFETY"},"candidates":[{"index":0}]}`},
		{"multiple candidates", `{"responseId":"g","modelVersion":"m","candidates":[{"index":0},{"index":1}]}`},
		{"wrong candidate", `{"responseId":"g","modelVersion":"m","candidates":[{"index":1,"content":{"role":"model","parts":[]}}]}`},
		{"safety finish", `{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model"},"finishReason":"SAFETY"}]}`},
		{"safety ratings", `{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model"},"safetyRatings":[]}]}`},
		{"citation", `{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model"},"citationMetadata":{}}]}`},
		{"media", `{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model","parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}}]}`},
		{"thought", `{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model","parts":[{"text":"hidden","thought":true}]}}]}`},
		{"model status", `{"responseId":"g","modelVersion":"m","modelStatus":{},"candidates":[{"content":{"role":"model"}}]}`},
		{"usage details", `{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model"}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":0,"totalTokenCount":1,"thoughtsTokenCount":1}}`},
		{"bad args", `{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"f","args":[]}}]}}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stream := new(GeminiToResponsesStream)
			if _, err := stream.Feed([]byte(test.raw)); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestGeminiToResponsesStreamUsageIsCumulativeAndBounded(t *testing.T) {
	t.Run("decreasing", func(t *testing.T) {
		stream := new(GeminiToResponsesStream)
		if _, err := stream.Feed([]byte(`{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model"}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":2,"totalTokenCount":4}}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := stream.Feed([]byte(`{"candidates":[{"content":{"role":"model"}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("inconsistent", func(t *testing.T) {
		stream := new(GeminiToResponsesStream)
		if _, err := stream.Feed([]byte(`{"responseId":"g","modelVersion":"m","candidates":[{"content":{"role":"model"}}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":9}}`)); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("pending action count", func(t *testing.T) {
		parts := make([]map[string]any, maxConvertedStreamItems+1)
		for index := range parts {
			parts[index] = map[string]any{"text": ""}
		}
		raw, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"role": "model", "parts": parts}}}})
		stream := new(GeminiToResponsesStream)
		if _, err := stream.Feed(raw); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestResponsesToGeminiStreamTextFunctionUsageAndTerminal(t *testing.T) {
	stream := new(ResponsesToGeminiStream)
	events := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"r","object":"response","created_at":1,"model":"wire","status":"in_progress","output":[],"usage":{"input_tokens":3,"output_tokens":0,"total_tokens":3}}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg","output_index":0,"content_index":0,"delta":"Hi"}`,
		`{"type":"response.output_text.done","sequence_number":4,"item_id":"msg","output_index":0,"content_index":0,"text":"Hi"}`,
		`{"type":"response.content_part.done","sequence_number":5,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"Hi","annotations":[]}}`,
		`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi","annotations":[]}]}}`,
		`{"type":"response.output_item.added","sequence_number":7,"output_index":1,"item":{"id":"fc","type":"function_call","status":"in_progress","call_id":"call","name":"lookup","arguments":""}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":8,"item_id":"fc","output_index":1,"delta":"{\"q\":"}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":9,"item_id":"fc","output_index":1,"delta":"1}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":10,"item_id":"fc","output_index":1,"arguments":"{\"q\":1}"}`,
		`{"type":"response.output_item.done","sequence_number":11,"output_index":1,"item":{"id":"fc","type":"function_call","status":"completed","call_id":"call","name":"lookup","arguments":"{\"q\":1}"}}`,
		`{"type":"response.completed","sequence_number":12,"response":{"id":"r","object":"response","created_at":1,"model":"wire","status":"completed","output":[{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Hi","annotations":[]}]},{"id":"fc","type":"function_call","status":"completed","call_id":"call","name":"lookup","arguments":"{\"q\":1}"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5,"input_tokens_details":{"cached_tokens":1}}}}`,
	}
	var converted []SSEEvent
	for index, raw := range events {
		output, err := stream.Feed([]byte(raw))
		if err != nil {
			t.Fatalf("event %d: %v", index, err)
		}
		if index == 8 || index == 9 || index == 10 {
			if len(output) != 0 {
				t.Fatalf("function arguments leaked before item completion: %#v", output)
			}
		}
		converted = append(converted, output...)
	}
	if err := stream.EOF(); err != nil {
		t.Fatal(err)
	}
	if len(converted) != 3 {
		t.Fatalf("unexpected converted event count: %d %#v", len(converted), converted)
	}
	for _, event := range converted {
		if event.Name != "" {
			t.Fatalf("Gemini event must be data-only: %#v", event)
		}
	}
	var textChunk, callChunk, terminal map[string]any
	_ = json.Unmarshal(converted[0].Data, &textChunk)
	_ = json.Unmarshal(converted[1].Data, &callChunk)
	_ = json.Unmarshal(converted[2].Data, &terminal)
	textPart := textChunk["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	callPart := callChunk["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionCall"].(map[string]any)
	terminalCandidate := terminal["candidates"].([]any)[0].(map[string]any)
	if textPart["text"] != "Hi" || callPart["id"] != "call" || callPart["name"] != "lookup" ||
		callPart["args"].(map[string]any)["q"] != float64(1) || terminalCandidate["finishReason"] != "STOP" ||
		len(terminalCandidate["content"].(map[string]any)["parts"].([]any)) != 0 {
		t.Fatalf("unexpected chunks: text=%#v call=%#v terminal=%#v", textChunk, callChunk, terminal)
	}
	usage := terminal["usageMetadata"].(map[string]any)
	if usage["promptTokenCount"] != float64(3) || usage["candidatesTokenCount"] != float64(2) || usage["totalTokenCount"] != float64(5) || usage["cachedContentTokenCount"] != float64(1) {
		t.Fatalf("unexpected usage: %#v", usage)
	}
	if !converted[0].Semantic || !converted[1].Semantic || !converted[2].Terminal || converted[2].TerminalOutcome != StreamTerminalCompleted {
		t.Fatalf("unexpected event flags: %#v", converted)
	}
}

func TestResponsesToGeminiStreamIncompleteAndEmptyOutput(t *testing.T) {
	stream := new(ResponsesToGeminiStream)
	if events, err := stream.Feed([]byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`)); err != nil || len(events) != 0 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	events, err := stream.Feed([]byte(`{"type":"response.incomplete","sequence_number":1,"response":{"id":"r","object":"response","created_at":1,"model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}}`))
	if err != nil || len(events) != 1 || events[0].TerminalOutcome != StreamTerminalIncomplete {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if !strings.Contains(string(events[0].Data), `"finishReason":"MAX_TOKENS"`) || !strings.Contains(string(events[0].Data), `"parts":[]`) {
		t.Fatalf("unexpected terminal: %s", events[0].Data)
	}
}

func TestResponsesToGeminiStreamAcceptsIncompleteTextItem(t *testing.T) {
	stream := new(ResponsesToGeminiStream)
	events := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"msg","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":2,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg","output_index":0,"content_index":0,"delta":"cut"}`,
		`{"type":"response.output_text.done","sequence_number":4,"item_id":"msg","output_index":0,"content_index":0,"text":"cut"}`,
		`{"type":"response.content_part.done","sequence_number":5,"item_id":"msg","output_index":0,"content_index":0,"part":{"type":"output_text","text":"cut","annotations":[]}}`,
		`{"type":"response.output_item.done","sequence_number":6,"output_index":0,"item":{"id":"msg","type":"message","status":"incomplete","role":"assistant","content":[{"type":"output_text","text":"cut","annotations":[]}]}}`,
		`{"type":"response.incomplete","sequence_number":7,"response":{"id":"r","object":"response","created_at":1,"model":"m","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"id":"msg","type":"message","status":"incomplete","role":"assistant","content":[{"type":"output_text","text":"cut","annotations":[]}]}]}}`,
	}
	var output []SSEEvent
	for index, raw := range events {
		converted, err := stream.Feed([]byte(raw))
		if err != nil {
			t.Fatalf("event %d: %v", index, err)
		}
		output = append(output, converted...)
	}
	if len(output) != 2 || !output[0].Semantic || output[1].TerminalOutcome != StreamTerminalIncomplete {
		t.Fatalf("unexpected output: %#v", output)
	}
}

func TestResponsesToGeminiStreamPreservesOutputOrder(t *testing.T) {
	stream := new(ResponsesToGeminiStream)
	events := []string{
		`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`,
		`{"type":"response.output_item.added","sequence_number":1,"output_index":0,"item":{"id":"fc","type":"function_call","status":"in_progress","call_id":"call","name":"first","arguments":""}}`,
		`{"type":"response.output_item.added","sequence_number":2,"output_index":1,"item":{"id":"msg","type":"message","status":"in_progress","role":"assistant","content":[]}}`,
		`{"type":"response.content_part.added","sequence_number":3,"item_id":"msg","output_index":1,"content_index":0,"part":{"type":"output_text","text":"","annotations":[]}}`,
		`{"type":"response.output_text.delta","sequence_number":4,"item_id":"msg","output_index":1,"content_index":0,"delta":"second"}`,
		`{"type":"response.output_text.done","sequence_number":5,"item_id":"msg","output_index":1,"content_index":0,"text":"second"}`,
		`{"type":"response.content_part.done","sequence_number":6,"item_id":"msg","output_index":1,"content_index":0,"part":{"type":"output_text","text":"second","annotations":[]}}`,
		`{"type":"response.output_item.done","sequence_number":7,"output_index":1,"item":{"id":"msg","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"second","annotations":[]}]}}`,
		`{"type":"response.function_call_arguments.delta","sequence_number":8,"item_id":"fc","output_index":0,"delta":"{}"}`,
		`{"type":"response.function_call_arguments.done","sequence_number":9,"item_id":"fc","output_index":0,"arguments":"{}"}`,
		`{"type":"response.output_item.done","sequence_number":10,"output_index":0,"item":{"id":"fc","type":"function_call","status":"completed","call_id":"call","name":"first","arguments":"{}"}}`,
	}
	var output []SSEEvent
	for index, raw := range events {
		converted, err := stream.Feed([]byte(raw))
		if err != nil {
			t.Fatalf("event %d: %v", index, err)
		}
		if index < len(events)-1 && len(converted) != 0 {
			t.Fatalf("out-of-order output escaped at %d: %#v", index, converted)
		}
		output = append(output, converted...)
	}
	if len(output) != 2 || !strings.Contains(string(output[0].Data), `"functionCall"`) || !strings.Contains(string(output[1].Data), `"text":"second"`) {
		t.Fatalf("output order changed: %#v", output)
	}
}

func TestResponsesToGeminiStreamRejectsFailuresUsageAndMalformedTerminal(t *testing.T) {
	t.Run("upstream error", func(t *testing.T) {
		stream := new(ResponsesToGeminiStream)
		if _, err := stream.Feed([]byte(`{"type":"error","sequence_number":0,"message":"private"}`)); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("stateful snapshot", func(t *testing.T) {
		stream := new(ResponsesToGeminiStream)
		_, err := stream.Feed([]byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m","background":true,"output":[]}}`))
		if !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("hosted snapshot item", func(t *testing.T) {
		stream := new(ResponsesToGeminiStream)
		_, err := stream.Feed([]byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m","output":[{"id":"ws","type":"web_search_call"}]}}`))
		if !errors.Is(err, ErrUnsupportedFeature) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("unsupported incomplete reason", func(t *testing.T) {
		stream := new(ResponsesToGeminiStream)
		_, _ = stream.Feed([]byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m"}}`))
		_, err := stream.Feed([]byte(`{"type":"response.incomplete","sequence_number":1,"response":{"id":"r","object":"response","created_at":1,"model":"m","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[]}}`))
		if !errors.Is(err, ErrUnsupportedFeature) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("decreasing usage", func(t *testing.T) {
		stream := new(ResponsesToGeminiStream)
		_, err := stream.Feed([]byte(`{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m","usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`))
		if err != nil {
			t.Fatal(err)
		}
		_, err = stream.Feed([]byte(`{"type":"response.in_progress","sequence_number":1,"response":{"id":"r","created_at":1,"model":"m","usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}}`))
		if !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("got %v", err)
		}
	})
	for _, detail := range []string{
		`"input_tokens_details":{"cache_write_tokens":0}`,
		`"output_tokens_details":{"reasoning_tokens":1}`,
	} {
		t.Run(detail, func(t *testing.T) {
			stream := new(ResponsesToGeminiStream)
			raw := `{"type":"response.created","sequence_number":0,"response":{"id":"r","created_at":1,"model":"m","usage":{"input_tokens":1,"output_tokens":0,"total_tokens":1,` + detail + `}}}`
			if _, err := stream.Feed([]byte(raw)); !errors.Is(err, ErrUnsupportedFeature) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestGeminiResponsesStreamsRequireTerminalBeforeEOF(t *testing.T) {
	if err := new(GeminiToResponsesStream).EOF(); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("Gemini EOF: %v", err)
	}
	if err := new(ResponsesToGeminiStream).EOF(); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("Responses EOF: %v", err)
	}
}
