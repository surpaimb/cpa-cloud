package protocolconv

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestChatRequestToResponsesTextToolsAndResults(t *testing.T) {
	raw := []byte(`{
  "model":"company-model","stream":true,"stream_options":{"include_usage":true},
  "messages":[
    {"role":"developer","content":"Be concise."},
    {"role":"user","content":"weather"},
    {"role":"assistant","content":null,"tool_calls":[{"id":"call_weather","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"}}]},
    {"role":"tool","tool_call_id":"call_weather","content":"{\"c\":21}"}
  ],
  "tools":[{"type":"function","function":{"name":"weather","description":"Weather lookup","parameters":{"type":"object"},"strict":true}}],
  "tool_choice":{"type":"function","function":{"name":"weather"}},"parallel_tool_calls":false,"max_completion_tokens":200
}`)
	converted, err := ChatRequestToResponses(raw)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(converted, &root); err != nil {
		t.Fatal(err)
	}
	if root["model"] != "company-model" || root["store"] != false || root["stream"] != true || root["max_output_tokens"] != float64(200) {
		t.Fatalf("unexpected root: %#v", root)
	}
	items := root["input"].([]any)
	if len(items) != 4 || items[2].(map[string]any)["type"] != "function_call" || items[3].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("unexpected input items: %#v", items)
	}
	tool := root["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["name"] != "weather" || tool["strict"] != true {
		t.Fatalf("unexpected tool: %#v", tool)
	}
	choice := root["tool_choice"].(map[string]any)
	if choice["type"] != "function" || choice["name"] != "weather" {
		t.Fatalf("unexpected choice: %#v", choice)
	}
}

func TestResponsesRequestToChatTextToolsAndResults(t *testing.T) {
	raw := []byte(`{
  "model":"company-model","instructions":"Be concise.","stream":true,"store":false,
  "input":[
    {"type":"message","role":"user","content":[{"type":"input_text","text":"weather"}]},
    {"type":"function_call","id":"fc_1","call_id":"call_weather","name":"weather","arguments":"{\"city\":\"Paris\"}","status":"completed"},
    {"type":"function_call_output","call_id":"call_weather","output":"{\"c\":21}"}
  ],
  "tools":[{"type":"function","name":"weather","description":"Weather lookup","parameters":{"type":"object"},"strict":true}],
  "tool_choice":{"type":"function","name":"weather"},"parallel_tool_calls":false,"max_output_tokens":200
}`)
	converted, err := ResponsesRequestToChat(raw)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(converted, &root); err != nil {
		t.Fatal(err)
	}
	if root["model"] != "company-model" || root["stream"] != true || root["max_completion_tokens"] != float64(200) {
		t.Fatalf("unexpected root: %#v", root)
	}
	if root["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("stream usage not requested: %#v", root)
	}
	messages := root["messages"].([]any)
	if len(messages) != 4 || messages[0].(map[string]any)["role"] != "developer" || messages[2].(map[string]any)["role"] != "assistant" || messages[3].(map[string]any)["role"] != "tool" {
		t.Fatalf("unexpected messages: %#v", messages)
	}
	tool := root["tools"].([]any)[0].(map[string]any)
	if tool["type"] != "function" || tool["function"].(map[string]any)["name"] != "weather" {
		t.Fatalf("unexpected tool: %#v", tool)
	}
}

func TestRequestConversionsRejectUnrepresentableFields(t *testing.T) {
	tests := []struct {
		name  string
		call  func([]byte) ([]byte, error)
		body  string
		field string
	}{
		{"chat image", ChatRequestToResponses, `{"model":"m","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid"}}]}]}`, "messages[0].content"},
		{"chat n", ChatRequestToResponses, `{"model":"m","messages":[],"n":2}`, "n"},
		{"responses state", ResponsesRequestToChat, `{"model":"m","input":"hi","previous_response_id":"resp_other"}`, "previous_response_id"},
		{"responses background", ResponsesRequestToChat, `{"model":"m","input":"hi","background":true}`, "background"},
		{"hosted tool", ResponsesRequestToChat, `{"model":"m","input":"hi","tools":[{"type":"web_search"}]}`, "tools[0].type"},
		{"reasoning item", ResponsesRequestToChat, `{"model":"m","input":[{"type":"reasoning","id":"r"}]}`, "input[0].type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.call([]byte(test.body))
			if !errors.Is(err, ErrUnsupportedFeature) || Field(err) != test.field {
				t.Fatalf("got err=%v field=%q", err, Field(err))
			}
		})
	}
}

func TestRequestConversionsRejectInvalidShapes(t *testing.T) {
	for _, body := range []string{
		`{"model":"m","messages":null}`,
		`{"model":"m","messages":[{"role":"tool","content":"ok"}]}`,
		`{"model":"m","messages":[{"role":"assistant"}]}`,
	} {
		if _, err := ChatRequestToResponses([]byte(body)); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("expected invalid request for %s, got %v", body, err)
		}
	}
}

func TestResponsesRequestGroupsParallelFunctionCalls(t *testing.T) {
	converted, err := ResponsesRequestToChat([]byte(`{
  "model":"m","parallel_tool_calls":true,
  "input":[
    {"type":"function_call","call_id":"call_a","name":"a","arguments":"{}"},
    {"type":"function_call","call_id":"call_b","name":"b","arguments":"{}"},
    {"type":"function_call_output","call_id":"call_a","output":"A"},
    {"type":"function_call_output","call_id":"call_b","output":"B"}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal(converted, &root)
	messages := root["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("expected one assistant call round and two results, got %#v", messages)
	}
	assistant := messages[0].(map[string]any)
	if assistant["role"] != "assistant" || len(assistant["tool_calls"].([]any)) != 2 {
		t.Fatalf("parallel calls were split: %#v", assistant)
	}
	for index, callID := range []string{"call_a", "call_b"} {
		if messages[index+1].(map[string]any)["tool_call_id"] != callID {
			t.Fatalf("tool output order changed: %#v", messages)
		}
	}
}

func TestRequestNumericOptionsAreBounded(t *testing.T) {
	tests := []struct {
		call  func([]byte) ([]byte, error)
		body  string
		field string
	}{
		{ChatRequestToResponses, `{"model":"m","messages":[],"max_completion_tokens":1.5}`, "max_completion_tokens"},
		{ChatRequestToResponses, `{"model":"m","messages":[],"max_completion_tokens":0}`, "max_completion_tokens"},
		{ChatRequestToResponses, `{"model":"m","messages":[],"temperature":2.1}`, "temperature"},
		{ResponsesRequestToChat, `{"model":"m","input":"x","top_p":-0.1}`, "top_p"},
		{ResponsesRequestToChat, `{"model":"m","input":"x","stream":1}`, "stream"},
	}
	for _, test := range tests {
		_, err := test.call([]byte(test.body))
		if !errors.Is(err, ErrInvalidRequest) || Field(err) != test.field {
			t.Fatalf("body=%s err=%v field=%q", test.body, err, Field(err))
		}
	}
}
