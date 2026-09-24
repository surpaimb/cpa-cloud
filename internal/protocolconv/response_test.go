package protocolconv

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestChatResponseToResponsesPreservesTextToolsAndUsage(t *testing.T) {
	raw := []byte(`{
  "id":"chatcmpl_1","object":"chat.completion","created":1700000000,"model":"actual",
  "choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"Checking.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"id\":1}"}}]}}],
  "usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_tokens_details":{"cached_tokens":3},"completion_tokens_details":{"reasoning_tokens":1}}
}`)
	converted, err := ChatResponseToResponses(raw)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal(converted, &root)
	if root["object"] != "response" || root["status"] != "completed" {
		t.Fatalf("unexpected root: %#v", root)
	}
	output := root["output"].([]any)
	if len(output) != 2 || output[0].(map[string]any)["type"] != "message" || output[1].(map[string]any)["call_id"] != "call_1" {
		t.Fatalf("unexpected output: %#v", output)
	}
	usage := root["usage"].(map[string]any)
	if usage["input_tokens"] != float64(10) || usage["output_tokens"] != float64(4) || usage["total_tokens"] != float64(14) {
		t.Fatalf("unexpected usage: %#v", usage)
	}
}

func TestResponsesResponseToChatPreservesTextToolsAndUsage(t *testing.T) {
	raw := []byte(`{
  "id":"resp_1","object":"response","created_at":1700000000,"model":"actual","status":"completed",
  "output":[
    {"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"Checking.","annotations":[]}]},
    {"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{\"id\":1}"}
  ],
  "usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":1}}
}`)
	converted, err := ResponsesResponseToChat(raw)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal(converted, &root)
	choice := root["choices"].([]any)[0].(map[string]any)
	if choice["finish_reason"] != "tool_calls" {
		t.Fatalf("unexpected choice: %#v", choice)
	}
	message := choice["message"].(map[string]any)
	if message["content"] != "Checking." || message["tool_calls"].([]any)[0].(map[string]any)["id"] != "call_1" {
		t.Fatalf("unexpected message: %#v", message)
	}
	usage := root["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(10) || usage["completion_tokens"] != float64(4) {
		t.Fatalf("unexpected usage: %#v", usage)
	}
}

func TestResponseConversionsRejectLossyResults(t *testing.T) {
	tests := []struct {
		call  func([]byte) ([]byte, error)
		body  string
		field string
	}{
		{ChatResponseToResponses, `{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"length","message":{"role":"assistant","content":"x"}}]}`, "choices[0].finish_reason"},
		{ResponsesResponseToChat, `{"id":"r","object":"response","created_at":1,"model":"m","status":"completed","output":[{"type":"reasoning","id":"x"}]}`, "output[0].type"},
		{ResponsesResponseToChat, `{"id":"r","object":"response","created_at":1,"model":"m","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_image","image_url":"x"}]}]}`, "output[0].content[0].type"},
	}
	for _, test := range tests {
		_, err := test.call([]byte(test.body))
		if !errors.Is(err, ErrUnsupportedFeature) || Field(err) != test.field {
			t.Fatalf("got err=%v field=%q", err, Field(err))
		}
	}
}

func TestUsageTotalsMustAgree(t *testing.T) {
	_, err := ChatResponseToResponses([]byte(`{"id":"c","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"x"}}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":99}}`))
	if !errors.Is(err, ErrInvalidUpstream) {
		t.Fatalf("expected invalid upstream usage, got %v", err)
	}
}
