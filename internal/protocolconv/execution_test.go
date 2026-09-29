package protocolconv

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestPrepareCrossProtocolStreamRequestGeminiResponsesDirections(t *testing.T) {
	features := allConversionFeatures()
	t.Run("Gemini client to Responses wire", func(t *testing.T) {
		capability := RouteCapability{
			ClientProtocol: ProtocolGeminiGenerate, UpstreamProtocol: ProtocolOpenAIResponses,
			Streaming: true, RequestStreaming: true, Features: features,
		}
		prepared, err := PrepareCrossProtocolStreamRequest(capability, "wire-model", []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
		if err != nil {
			t.Fatal(err)
		}
		if !prepared.Streaming || prepared.Plan.Kind != PlanGeminiToResponses || prepared.Model != "wire-model" {
			t.Fatalf("unexpected prepared request: %#v", prepared)
		}
		var body map[string]any
		if err := json.Unmarshal(prepared.Body, &body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "wire-model" || body["stream"] != true || body["store"] != false {
			t.Fatalf("unexpected Responses body: %#v", body)
		}
	})
	t.Run("Responses client to Gemini wire", func(t *testing.T) {
		capability := RouteCapability{
			ClientProtocol: ProtocolOpenAIResponses, UpstreamProtocol: ProtocolGeminiGenerate,
			Streaming: true, RequestStreaming: true, Features: features,
		}
		prepared, err := PrepareCrossProtocolStreamRequest(capability, "wire-model", []byte(`{"model":"public","stream":true,"input":"hi"}`))
		if err != nil {
			t.Fatal(err)
		}
		if !prepared.Streaming || prepared.Plan.Kind != PlanResponsesToGemini || prepared.Model != "wire-model" {
			t.Fatalf("unexpected prepared request: %#v", prepared)
		}
		var body map[string]any
		if err := json.Unmarshal(prepared.Body, &body); err != nil {
			t.Fatal(err)
		}
		if _, ok := body["stream"]; ok {
			t.Fatalf("Gemini streaming belongs in the URL, body=%#v", body)
		}
		contents, ok := body["contents"].([]any)
		if !ok || len(contents) != 1 {
			t.Fatalf("unexpected Gemini body: %#v", body)
		}
	})
}

func TestPrepareCrossProtocolStreamRequestGeminiIsStrict(t *testing.T) {
	features := allConversionFeatures()
	responses := RouteCapability{
		ClientProtocol: ProtocolOpenAIResponses, UpstreamProtocol: ProtocolGeminiGenerate,
		Streaming: true, RequestStreaming: true, Features: features,
	}
	if _, err := PrepareCrossProtocolStreamRequest(responses, "wire", []byte(`{"model":"public","stream":false,"input":"hi"}`)); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Responses stream=false should fail: %v", err)
	}
	gemini := RouteCapability{
		ClientProtocol: ProtocolGeminiGenerate, UpstreamProtocol: ProtocolOpenAIResponses,
		Streaming: true, RequestStreaming: true, Features: features,
	}
	if _, err := PrepareCrossProtocolStreamRequest(gemini, "wire", []byte(`{"stream":true,"contents":[{"parts":[{"text":"hi"}]}]}`)); !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("invented Gemini stream body field should fail: %v", err)
	}
	gemini.Features = Features(FeatureText, FeatureUsage, FeatureFinishReason)
	if _, err := PrepareCrossProtocolStreamRequest(gemini, "wire", []byte(`{"tools":[{"functionDeclarations":[{"name":"f","parametersJsonSchema":{"type":"object"}}]}],"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)); !errors.Is(err, ErrUnsupportedRoute) {
		t.Fatalf("missing function features should fail: %v", err)
	}
}
