package protocolconv

import (
	"encoding/json"
	"errors"
	"testing"
)

func allConversionFeatures() FeatureSet {
	return Features(FeatureText, FeatureFunctionDefinitions, FeatureFunctionCalls, FeatureFunctionResults, FeatureSystemInstruction, FeatureDeveloperMessage, FeatureUsage, FeatureFinishReason)
}

func TestCanonicalPlanMatrixAndStreamingBoundary(t *testing.T) {
	tests := []struct {
		client, upstream Protocol
		kind             PlanKind
	}{
		{ProtocolOpenAIChat, ProtocolOpenAIResponses, PlanChatToResponses},
		{ProtocolOpenAIResponses, ProtocolOpenAIChat, PlanResponsesToChat},
		{ProtocolAnthropicMessages, ProtocolOpenAIResponses, PlanMessagesToResponses},
		{ProtocolOpenAIResponses, ProtocolAnthropicMessages, PlanResponsesToMessages},
		{ProtocolGeminiGenerate, ProtocolOpenAIResponses, PlanGeminiToResponses},
		{ProtocolOpenAIResponses, ProtocolGeminiGenerate, PlanResponsesToGemini},
	}
	for _, test := range tests {
		plan, err := SelectPlan(RouteCapability{ClientProtocol: test.client, UpstreamProtocol: test.upstream, Features: allConversionFeatures()})
		if err != nil || plan.Kind != test.kind || plan.ClientProtocol != test.client || plan.UpstreamProtocol != test.upstream {
			t.Fatalf("%s -> %s: plan=%#v err=%v", test.client, test.upstream, plan, err)
		}
		_, err = SelectPlan(RouteCapability{ClientProtocol: test.client, UpstreamProtocol: test.upstream, RequestStreaming: true, Streaming: true, Features: allConversionFeatures()})
		if !errors.Is(err, ErrUnsupportedRoute) {
			t.Fatalf("%s -> %s streaming should be rejected: %v", test.client, test.upstream, err)
		}
	}
	if plan, err := SelectPlan(RouteCapability{ClientProtocol: ProtocolGeminiGenerate, UpstreamProtocol: ProtocolGeminiGenerate, RequestStreaming: true, Streaming: true}); err != nil || plan.Kind != PlanNative {
		t.Fatalf("native streaming should remain available: plan=%#v err=%v", plan, err)
	}
	if _, err := SelectPlan(RouteCapability{ClientProtocol: ProtocolAnthropicMessages, UpstreamProtocol: ProtocolGeminiGenerate}); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("direct Messages -> Gemini must not exist: %v", err)
	}
}

func TestMessagesAndResponsesRequestGolden(t *testing.T) {
	messages := []byte(`{
  "model":"company-model","max_tokens":200,"system":"Be concise.","disable_parallel_tool_use":false,
  "messages":[
    {"role":"user","content":"weather"},
    {"role":"assistant","content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"call_weather","name":"weather","input":{"city":"Paris"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_weather","content":"{\"c\":21}"}]}
  ],
  "tools":[{"name":"weather","description":"Weather lookup","input_schema":{"type":"object"}}],
  "tool_choice":{"type":"tool","name":"weather"}
}`)
	converted, err := MessagesRequestToResponses(messages)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal(converted, &root)
	if root["instructions"] != "Be concise." || root["max_output_tokens"] != float64(200) || root["parallel_tool_calls"] != true {
		t.Fatalf("unexpected Responses request: %#v", root)
	}
	items := root["input"].([]any)
	if len(items) != 4 || items[2].(map[string]any)["call_id"] != "call_weather" || items[3].(map[string]any)["output"] != `{"c":21}` {
		t.Fatalf("tool round changed: %#v", items)
	}

	back, err := ResponsesRequestToMessages(converted)
	if err != nil {
		t.Fatal(err)
	}
	var backRoot map[string]any
	_ = json.Unmarshal(back, &backRoot)
	if backRoot["system"] != "Be concise." || backRoot["max_tokens"] != float64(200) {
		t.Fatalf("unexpected Messages request: %#v", backRoot)
	}
	if len(backRoot["tools"].([]any)) != 1 {
		t.Fatalf("tool definition lost: %#v", backRoot)
	}
}

func TestGeminiAndResponsesRequestGolden(t *testing.T) {
	gemini := []byte(`{
	  "systemInstruction":{"parts":[{"text":"Be concise."}]},
  "contents":[
    {"role":"user","parts":[{"text":"weather"}]},
    {"role":"model","parts":[{"functionCall":{"id":"call_weather","name":"weather","args":{"city":"Paris"}}}]},
    {"role":"user","parts":[{"functionResponse":{"id":"call_weather","name":"weather","response":{"c":21}}}]}
  ],
  "tools":[{"functionDeclarations":[{"name":"weather","description":"Weather lookup","parametersJsonSchema":{"type":"object"}}]}],
  "toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["weather"]}},
  "generationConfig":{"maxOutputTokens":200,"temperature":1,"topP":0.9,"candidateCount":1}
}`)
	converted, err := GeminiRequestToResponses("company-model", gemini)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal(converted, &root)
	if root["model"] != "company-model" || root["instructions"] != "Be concise." || root["max_output_tokens"] != float64(200) {
		t.Fatalf("unexpected Responses request: %#v", root)
	}
	items := root["input"].([]any)
	if len(items) != 3 || items[1].(map[string]any)["call_id"] != "call_weather" || items[2].(map[string]any)["output"] != `{"c":21}` {
		t.Fatalf("tool round changed: %#v", items)
	}

	model, back, err := ResponsesRequestToGemini(converted)
	if err != nil {
		t.Fatal(err)
	}
	if model != "company-model" {
		t.Fatalf("model changed: %q", model)
	}
	var backRoot map[string]any
	_ = json.Unmarshal(back, &backRoot)
	contents := backRoot["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("unexpected Gemini contents: %#v", contents)
	}
	response := contents[2].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if response["id"] != "call_weather" || response["name"] != "weather" {
		t.Fatalf("function response identity changed: %#v", response)
	}
}

func TestGeminiOptionalCallIDsAreStableWithinToolRound(t *testing.T) {
	converted, err := GeminiRequestToResponses("m", []byte(`{
  "contents":[
    {"role":"model","parts":[{"functionCall":{"name":"echo","args":{"value":"x"}}}]},
    {"role":"user","parts":[{"functionResponse":{"name":"echo","response":{"output":"ok"}}}]}
  ]
}`))
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal(converted, &root)
	items := root["input"].([]any)
	callID := items[0].(map[string]any)["call_id"]
	if callID == "" || items[1].(map[string]any)["call_id"] != callID {
		t.Fatalf("optional Gemini IDs were not associated: %#v", items)
	}

	response, err := GeminiResponseToResponses([]byte(`{
  "responseId":"g_optional","modelVersion":"m",
  "candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"echo"}}]},"finishReason":"STOP"}]
}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(response, &root)
	output := root["output"].([]any)[0].(map[string]any)
	if output["call_id"] == "" || output["arguments"] != "{}" {
		t.Fatalf("optional upstream Gemini fields were not represented: %#v", output)
	}
}

func TestMessagesResponseConversionsPreserveFinishAndUsage(t *testing.T) {
	upstream := []byte(`{
  "id":"msg_1","type":"message","role":"assistant","model":"actual",
  "content":[{"type":"text","text":"Checking."},{"type":"tool_use","id":"call_1","name":"lookup","input":{"id":1}}],
  "stop_reason":"tool_use","stop_sequence":null,
  "usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":3,"cache_creation_input_tokens":1}
}`)
	responses, err := MessagesResponseToResponses(upstream)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal(responses, &root)
	if root["status"] != "completed" || len(root["output"].([]any)) != 2 || root["usage"].(map[string]any)["total_tokens"] != float64(14) {
		t.Fatalf("unexpected Responses object: %#v", root)
	}
	back, err := ResponsesResponseToMessages(responses)
	if err != nil {
		t.Fatal(err)
	}
	var message map[string]any
	_ = json.Unmarshal(back, &message)
	if message["stop_reason"] != "tool_use" || len(message["content"].([]any)) != 2 {
		t.Fatalf("unexpected Messages response: %#v", message)
	}

	limited := []byte(`{"id":"msg_2","type":"message","role":"assistant","model":"actual","content":[{"type":"text","text":"partial"}],"stop_reason":"max_tokens","stop_sequence":null}`)
	responses, err = MessagesResponseToResponses(limited)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(responses, &root)
	if root["status"] != "incomplete" {
		t.Fatalf("max_tokens finish was lost: %#v", root)
	}
}

func TestGeminiResponseConversionsPreserveFinishAndUsage(t *testing.T) {
	upstream := []byte(`{
  "responseId":"g_1","modelVersion":"actual",
  "candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"Checking."},{"functionCall":{"id":"call_1","name":"lookup","args":{"id":1}}}]},"finishReason":"STOP"}],
  "usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":4,"totalTokenCount":14,"cachedContentTokenCount":3}
}`)
	responses, err := GeminiResponseToResponses(upstream)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	_ = json.Unmarshal(responses, &root)
	if root["status"] != "completed" || len(root["output"].([]any)) != 2 {
		t.Fatalf("unexpected Responses object: %#v", root)
	}
	back, err := ResponsesResponseToGemini(responses)
	if err != nil {
		t.Fatal(err)
	}
	var gemini map[string]any
	_ = json.Unmarshal(back, &gemini)
	candidate := gemini["candidates"].([]any)[0].(map[string]any)
	if candidate["finishReason"] != "STOP" || len(candidate["content"].(map[string]any)["parts"].([]any)) != 2 {
		t.Fatalf("unexpected Gemini response: %#v", gemini)
	}
}

func TestResponsesOutputOrderIsPreserved(t *testing.T) {
	responses := []byte(`{
  "id":"resp_order","object":"response","created_at":1,"model":"actual","status":"completed",
  "output":[
    {"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"before","annotations":[]}]},
    {"id":"fc_1","type":"function_call","status":"completed","call_id":"call_1","name":"lookup","arguments":"{}"},
    {"id":"msg_2","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"after","annotations":[]}]}
  ]
}`)
	messagesRaw, err := ResponsesResponseToMessages(responses)
	if err != nil {
		t.Fatal(err)
	}
	var messages map[string]any
	_ = json.Unmarshal(messagesRaw, &messages)
	blocks := messages["content"].([]any)
	if blocks[0].(map[string]any)["type"] != "text" || blocks[1].(map[string]any)["type"] != "tool_use" || blocks[2].(map[string]any)["text"] != "after" {
		t.Fatalf("Messages output order changed: %#v", blocks)
	}

	geminiRaw, err := ResponsesResponseToGemini(responses)
	if err != nil {
		t.Fatal(err)
	}
	var gemini map[string]any
	_ = json.Unmarshal(geminiRaw, &gemini)
	parts := gemini["candidates"].([]any)[0].(map[string]any)["content"].(map[string]any)["parts"].([]any)
	if parts[0].(map[string]any)["text"] != "before" || parts[1].(map[string]any)["functionCall"] == nil || parts[2].(map[string]any)["text"] != "after" {
		t.Fatalf("Gemini output order changed: %#v", parts)
	}
}

func TestCanonicalConversionsRejectUnrepresentableBeforeDispatch(t *testing.T) {
	tests := []struct {
		name  string
		call  func() error
		field string
	}{
		{"Messages media", func() error {
			_, err := MessagesRequestToResponses([]byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":[{"type":"image","source":{}}]}]}`))
			return err
		}, "messages[0].content[0].type"},
		{"Responses ambiguous role", func() error {
			_, err := ResponsesRequestToMessages([]byte(`{"model":"m","max_output_tokens":1,"input":[{"type":"message","role":"developer","content":"x"}]}`))
			return err
		}, "input[0].role"},
		{"Responses strict tool to Messages", func() error {
			_, err := ResponsesRequestToMessages([]byte(`{"model":"m","max_output_tokens":1,"input":"x","tools":[{"type":"function","name":"f","parameters":{"type":"object"},"strict":true}]}`))
			return err
		}, "tools[0].strict"},
		{"Responses string result to Gemini", func() error {
			_, _, err := ResponsesRequestToGemini([]byte(`{"model":"m","max_output_tokens":1,"input":[{"type":"function_call","call_id":"c","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"c","output":"plain text"}]}`))
			return err
		}, "messages[1].content"},
		{"Gemini safety", func() error {
			_, err := GeminiRequestToResponses("m", []byte(`{"contents":[],"safetySettings":[]}`))
			return err
		}, "safetySettings"},
		{"Gemini multiple candidates", func() error {
			_, err := GeminiResponseToResponses([]byte(`{"responseId":"r","modelVersion":"m","candidates":[{},{}]}`))
			return err
		}, "candidates"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.call()
			if !errors.Is(err, ErrUnsupportedFeature) || Field(err) != test.field {
				t.Fatalf("err=%v field=%q", err, Field(err))
			}
		})
	}
}

func TestPrepareRequestRequiresDeclaredSemantics(t *testing.T) {
	_, err := PrepareRequest(RouteCapability{
		ClientProtocol: ProtocolOpenAIChat, UpstreamProtocol: ProtocolOpenAIResponses,
		Features: Features(FeatureText, FeatureUsage, FeatureFinishReason),
	}, "", []byte(`{"model":"m","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`))
	var unsupported *UnsupportedRouteError
	if !errors.As(err, &unsupported) || unsupported.Missing&FeatureSet(FeatureFunctionCalls) == 0 {
		t.Fatalf("missing function capability should reject: %#v", err)
	}
}

func TestPrepareRequestRejectsBodyStreamingEvenIfCallerFlagIsWrong(t *testing.T) {
	_, err := PrepareRequest(RouteCapability{
		ClientProtocol: ProtocolOpenAIChat, UpstreamProtocol: ProtocolOpenAIResponses,
		Features: allConversionFeatures(),
	}, "", []byte(`{"model":"m","stream":true,"messages":[{"role":"user","content":"x"}]}`))
	if !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("cross-protocol body stream should be rejected: %v", err)
	}
}
