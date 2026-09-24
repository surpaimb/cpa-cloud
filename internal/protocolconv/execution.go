package protocolconv

import (
	"bytes"
	"encoding/json"
)

// PreparedRequest freezes the protocol plan and serialized upstream body that
// must be proved by budget admission before dispatch.
type PreparedRequest struct {
	Plan     Plan
	Model    string
	Body     []byte
	Features FeatureSet
}

// PrepareRequest selects a named conversion, validates all supported request
// fields, applies feature gates, and returns the exact upstream bytes. It does
// no I/O and must run before the durable dispatch barrier.
func PrepareRequest(capability RouteCapability, model string, raw []byte) (PreparedRequest, error) {
	plan, err := SelectPlan(capability)
	if err != nil {
		return PreparedRequest{}, err
	}
	prepared := PreparedRequest{Plan: plan, Model: model}
	if plan.Kind == PlanNative {
		prepared.Body = append([]byte(nil), raw...)
		return prepared, nil
	}
	if bodyRequestsStreaming(raw, capability.ClientProtocol) {
		return PreparedRequest{}, &UnsupportedRouteError{ClientProtocol: capability.ClientProtocol, UpstreamProtocol: capability.UpstreamProtocol, Reason: "cross-protocol streaming is not enabled"}
	}
	conversionInput := raw
	if model != "" && capability.ClientProtocol != ProtocolGeminiGenerate {
		conversionInput, err = replaceRequestModel(raw, model)
		if err != nil {
			return PreparedRequest{}, err
		}
	}
	switch plan.Kind {
	case PlanChatToResponses:
		prepared.Body, err = ChatRequestToResponses(conversionInput)
		prepared.Features = requestFeatures(conversionInput)
	case PlanResponsesToChat:
		prepared.Body, err = ResponsesRequestToChat(conversionInput)
		prepared.Features = requestFeatures(conversionInput)
	case PlanMessagesToResponses:
		prepared.Body, prepared.Features, err = messagesRequestToResponses(conversionInput)
	case PlanResponsesToMessages:
		prepared.Body, prepared.Features, err = responsesRequestToMessages(conversionInput)
	case PlanGeminiToResponses:
		prepared.Body, prepared.Features, err = geminiRequestToResponses(model, raw)
	case PlanResponsesToGemini:
		prepared.Model, prepared.Body, prepared.Features, err = responsesRequestToGemini(conversionInput)
	default:
		err = &UnsupportedRouteError{ClientProtocol: capability.ClientProtocol, UpstreamProtocol: capability.UpstreamProtocol}
	}
	if err != nil {
		return PreparedRequest{}, err
	}
	prepared.Features |= Features(FeatureUsage, FeatureFinishReason)
	if err := requireFeatures(capability, prepared.Features); err != nil {
		return PreparedRequest{}, err
	}
	return prepared, nil
}

func bodyRequestsStreaming(raw []byte, protocol Protocol) bool {
	if protocol == ProtocolGeminiGenerate {
		return false // Gemini streaming is selected by the URL operation.
	}
	root, err := decodeObject(raw, CodeInvalidRequest, "")
	if err != nil {
		return false // The selected converter returns the typed shape error.
	}
	var stream bool
	return json.Unmarshal(root["stream"], &stream) == nil && stream
}

func replaceRequestModel(raw []byte, model string) ([]byte, error) {
	root, err := decodeObject(raw, CodeInvalidRequest, "")
	if err != nil {
		return nil, err
	}
	root["model"], _ = json.Marshal(model)
	return json.Marshal(root)
}

// ConvertResponse serializes validated raw upstream JSON for the employee.
// Accounting must observe raw before calling this function.
func ConvertResponse(prepared PreparedRequest, raw []byte) ([]byte, error) {
	switch prepared.Plan.Kind {
	case PlanNative:
		return append([]byte(nil), raw...), nil
	case PlanChatToResponses:
		return ResponsesResponseToChat(raw)
	case PlanResponsesToChat:
		return ChatResponseToResponses(raw)
	case PlanMessagesToResponses:
		return ResponsesResponseToMessages(raw)
	case PlanResponsesToMessages:
		return MessagesResponseToResponses(raw)
	case PlanGeminiToResponses:
		return ResponsesResponseToGemini(raw)
	case PlanResponsesToGemini:
		return GeminiResponseToResponses(raw)
	default:
		return nil, &UnsupportedRouteError{ClientProtocol: prepared.Plan.ClientProtocol, UpstreamProtocol: prepared.Plan.UpstreamProtocol}
	}
}

func requestFeatures(raw []byte) FeatureSet {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return Features(FeatureText)
	}
	features := Features(FeatureText)
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case []any:
			for _, item := range typed {
				visit(item)
			}
		case map[string]any:
			for key, item := range typed {
				switch key {
				case "tools":
					features |= Features(FeatureFunctionDefinitions, FeatureFunctionCalls)
				case "tool_calls", "function_call":
					features |= Features(FeatureFunctionCalls)
				case "tool_call_id", "function_call_output", "functionResponse":
					features |= Features(FeatureFunctionResults)
				case "instructions", "system", "systemInstruction":
					features |= Features(FeatureSystemInstruction)
				case "role":
					if role, ok := item.(string); ok && role == "developer" {
						features |= Features(FeatureDeveloperMessage)
					}
				}
				visit(item)
			}
		}
	}
	visit(value)
	return features
}
