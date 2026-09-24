package protocolconv

import (
	"errors"
	"fmt"
)

// Protocol names the JSON wire contract used on one side of a model request.
// It is deliberately independent of provider/account names.
type Protocol string

const (
	ProtocolOpenAIChat        Protocol = "openai-chat"
	ProtocolOpenAIResponses   Protocol = "openai-responses"
	ProtocolAnthropicMessages Protocol = "anthropic-messages"
	ProtocolGeminiGenerate    Protocol = "gemini-generate-content"
)

// Feature is an explicitly declared semantic capability of a route. A route
// must opt in to every feature required by a cross-protocol request.
type Feature uint64

const (
	FeatureText Feature = 1 << iota
	FeatureFunctionDefinitions
	FeatureFunctionCalls
	FeatureFunctionResults
	FeatureSystemInstruction
	FeatureDeveloperMessage
	FeatureUsage
	FeatureFinishReason
)

type FeatureSet uint64

func Features(values ...Feature) FeatureSet {
	var set FeatureSet
	for _, value := range values {
		set |= FeatureSet(value)
	}
	return set
}

func (s FeatureSet) HasAll(required FeatureSet) bool { return s&required == required }

// RouteCapability is supplied by routing after employee authentication and
// account selection. Conversion code never guesses a wire protocol from a
// provider name.
type RouteCapability struct {
	ClientProtocol   Protocol
	UpstreamProtocol Protocol
	// Streaming declares native route support. RequestStreaming describes the
	// employee request and is always rejected for conversion plans in this slice.
	Streaming        bool
	RequestStreaming bool
	Features         FeatureSet
}

type PlanKind string

const (
	PlanNative              PlanKind = "native"
	PlanChatToResponses     PlanKind = "chat-to-responses"
	PlanResponsesToChat     PlanKind = "responses-to-chat"
	PlanMessagesToResponses PlanKind = "messages-to-responses"
	PlanResponsesToMessages PlanKind = "responses-to-messages"
	PlanGeminiToResponses   PlanKind = "gemini-to-responses"
	PlanResponsesToGemini   PlanKind = "responses-to-gemini"
)

type Plan struct {
	Kind             PlanKind
	ClientProtocol   Protocol
	UpstreamProtocol Protocol
}

// UnsupportedRouteError is safe to surface without including request values.
type UnsupportedRouteError struct {
	ClientProtocol   Protocol
	UpstreamProtocol Protocol
	Missing          FeatureSet
	Reason           string
}

func (e *UnsupportedRouteError) Error() string {
	if e == nil {
		return "unsupported protocol route"
	}
	if e.Reason != "" {
		return e.Reason
	}
	return fmt.Sprintf("unsupported protocol route %s -> %s", e.ClientProtocol, e.UpstreamProtocol)
}

var ErrUnsupportedRoute = &UnsupportedRouteError{}

func (e *UnsupportedRouteError) Is(target error) bool {
	_, ok := target.(*UnsupportedRouteError)
	return ok
}

func SelectPlan(capability RouteCapability) (Plan, error) {
	plan := Plan{ClientProtocol: capability.ClientProtocol, UpstreamProtocol: capability.UpstreamProtocol}
	if !knownProtocol(capability.ClientProtocol) || !knownProtocol(capability.UpstreamProtocol) {
		return Plan{}, &UnsupportedRouteError{ClientProtocol: capability.ClientProtocol, UpstreamProtocol: capability.UpstreamProtocol, Reason: "unknown protocol capability"}
	}
	if capability.ClientProtocol == capability.UpstreamProtocol {
		if capability.RequestStreaming && !capability.Streaming {
			return Plan{}, &UnsupportedRouteError{ClientProtocol: capability.ClientProtocol, UpstreamProtocol: capability.UpstreamProtocol, Reason: "selected native route does not support streaming"}
		}
		plan.Kind = PlanNative
		return plan, nil
	}
	if capability.RequestStreaming {
		return Plan{}, &UnsupportedRouteError{ClientProtocol: capability.ClientProtocol, UpstreamProtocol: capability.UpstreamProtocol, Reason: "cross-protocol streaming is not enabled"}
	}
	switch {
	case capability.ClientProtocol == ProtocolOpenAIChat && capability.UpstreamProtocol == ProtocolOpenAIResponses:
		plan.Kind = PlanChatToResponses
	case capability.ClientProtocol == ProtocolOpenAIResponses && capability.UpstreamProtocol == ProtocolOpenAIChat:
		plan.Kind = PlanResponsesToChat
	case capability.ClientProtocol == ProtocolAnthropicMessages && capability.UpstreamProtocol == ProtocolOpenAIResponses:
		plan.Kind = PlanMessagesToResponses
	case capability.ClientProtocol == ProtocolOpenAIResponses && capability.UpstreamProtocol == ProtocolAnthropicMessages:
		plan.Kind = PlanResponsesToMessages
	case capability.ClientProtocol == ProtocolGeminiGenerate && capability.UpstreamProtocol == ProtocolOpenAIResponses:
		plan.Kind = PlanGeminiToResponses
	case capability.ClientProtocol == ProtocolOpenAIResponses && capability.UpstreamProtocol == ProtocolGeminiGenerate:
		plan.Kind = PlanResponsesToGemini
	default:
		return Plan{}, &UnsupportedRouteError{ClientProtocol: capability.ClientProtocol, UpstreamProtocol: capability.UpstreamProtocol}
	}
	return plan, nil
}

func requireFeatures(capability RouteCapability, required FeatureSet) error {
	if capability.Features.HasAll(required) {
		return nil
	}
	return &UnsupportedRouteError{
		ClientProtocol:   capability.ClientProtocol,
		UpstreamProtocol: capability.UpstreamProtocol,
		Missing:          required &^ capability.Features,
		Reason:           "selected route lacks required protocol semantics",
	}
}

func knownProtocol(protocol Protocol) bool {
	switch protocol {
	case ProtocolOpenAIChat, ProtocolOpenAIResponses, ProtocolAnthropicMessages, ProtocolGeminiGenerate:
		return true
	default:
		return false
	}
}

func IsUnsupportedRoute(err error) bool { return errors.Is(err, ErrUnsupportedRoute) }
