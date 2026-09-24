package protocolconv

import (
	"bytes"
	"encoding/json"
)

// PreparedRequest freezes the protocol plan and serialized upstream body that
// must be proved by budget admission before dispatch.
type PreparedRequest struct {
	Plan      Plan
	Model     string
	Body      []byte
	Features  FeatureSet
	Streaming bool
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

// PrepareCrossProtocolStreamRequest prepares the exact upstream request body
// for the independently reviewed Chat/Responses and Messages/Responses
// streaming bridges. The
// production JSON runtime deliberately does not call this function: callers
// must provide the bounded stream executor and durable dispatch barrier before
// opting into this path.
func PrepareCrossProtocolStreamRequest(capability RouteCapability, model string, raw []byte) (PreparedRequest, error) {
	if !capability.RequestStreaming || !capability.Streaming {
		return PreparedRequest{}, &UnsupportedRouteError{
			ClientProtocol: capability.ClientProtocol, UpstreamProtocol: capability.UpstreamProtocol,
			Reason: "cross-protocol streaming requires an explicit streaming route",
		}
	}
	plan := Plan{ClientProtocol: capability.ClientProtocol, UpstreamProtocol: capability.UpstreamProtocol}
	switch {
	case capability.ClientProtocol == ProtocolOpenAIChat && capability.UpstreamProtocol == ProtocolOpenAIResponses:
		plan.Kind = PlanChatToResponses
	case capability.ClientProtocol == ProtocolOpenAIResponses && capability.UpstreamProtocol == ProtocolOpenAIChat:
		plan.Kind = PlanResponsesToChat
	case capability.ClientProtocol == ProtocolAnthropicMessages && capability.UpstreamProtocol == ProtocolOpenAIResponses:
		plan.Kind = PlanMessagesToResponses
	case capability.ClientProtocol == ProtocolOpenAIResponses && capability.UpstreamProtocol == ProtocolAnthropicMessages:
		plan.Kind = PlanResponsesToMessages
	default:
		return PreparedRequest{}, &UnsupportedRouteError{
			ClientProtocol: capability.ClientProtocol, UpstreamProtocol: capability.UpstreamProtocol,
			Reason: "cross-protocol streaming is not enabled for this route",
		}
	}
	if !bodyRequestsStreaming(raw, capability.ClientProtocol) {
		return PreparedRequest{}, invalid("stream", "true is required for cross-protocol streaming")
	}
	conversionInput := raw
	var err error
	if model != "" {
		conversionInput, err = replaceRequestModel(raw, model)
		if err != nil {
			return PreparedRequest{}, err
		}
	}
	prepared := PreparedRequest{Plan: plan, Model: model, Streaming: true}
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
					if role, ok := item.(string); ok {
						switch role {
						case "developer":
							features |= Features(FeatureDeveloperMessage)
						case "system":
							features |= Features(FeatureSystemInstruction)
						}
					}
				}
				visit(item)
			}
		}
	}
	visit(value)
	return features
}
