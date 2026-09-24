// Independently authored from CPA Cloud's explicit wire-routing contract; it
// does not infer behavior from any archived or third-party proxy implementation.
package service

import (
	"errors"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/protocolconv"
)

type routeWireProtocol string

const (
	wireProtocolLegacyNative routeWireProtocol = "legacy-native"
	wireProtocolOpenAIChat   routeWireProtocol = "openai-chat"
	wireProtocolResponses    routeWireProtocol = "openai-responses"
	wireProtocolMessages     routeWireProtocol = "anthropic-messages"
	wireProtocolGemini       routeWireProtocol = "gemini-generate-content"
)

func validRouteWireProtocol(value string) bool {
	switch routeWireProtocol(value) {
	case wireProtocolLegacyNative, wireProtocolOpenAIChat, wireProtocolResponses, wireProtocolMessages, wireProtocolGemini:
		return true
	default:
		return false
	}
}

func providerSupportsWire(provider, value string) bool {
	if value == string(wireProtocolLegacyNative) {
		return true
	}
	switch provider {
	case "openai-compatible":
		return value == string(wireProtocolOpenAIChat) || value == string(wireProtocolResponses)
	case codexMembershipProvider:
		return value == string(wireProtocolResponses)
	case anthropicAPIKeyProvider:
		return value == string(wireProtocolMessages)
	case geminiAPIKeyProvider:
		return value == string(wireProtocolGemini)
	default:
		return false
	}
}

func protocolconvProtocol(value routeWireProtocol) (protocolconv.Protocol, error) {
	switch value {
	case wireProtocolOpenAIChat:
		return protocolconv.ProtocolOpenAIChat, nil
	case wireProtocolResponses:
		return protocolconv.ProtocolOpenAIResponses, nil
	case wireProtocolMessages:
		return protocolconv.ProtocolAnthropicMessages, nil
	case wireProtocolGemini:
		return protocolconv.ProtocolGeminiGenerate, nil
	default:
		return "", errors.New("route wire protocol is not explicit")
	}
}

func accountingProtocol(value protocolconv.Protocol) (accounting.UsageProtocol, error) {
	switch value {
	case protocolconv.ProtocolOpenAIChat:
		return accounting.ProtocolOpenAIChatCompletions, nil
	case protocolconv.ProtocolOpenAIResponses:
		return accounting.ProtocolOpenAIResponses, nil
	case protocolconv.ProtocolAnthropicMessages:
		return accounting.ProtocolAnthropicMessages, nil
	case protocolconv.ProtocolGeminiGenerate:
		return accounting.ProtocolGeminiGenerateContent, nil
	default:
		return "", errors.New("unknown protocol")
	}
}

func routeUpstreamProtocol(selected route, client accounting.UsageProtocol) (protocolconv.Protocol, error) {
	if selected.WireProtocol != wireProtocolLegacyNative {
		return protocolconvProtocol(selected.WireProtocol)
	}
	switch selected.ProviderKind {
	case "openai-compatible":
		switch client {
		case accounting.ProtocolOpenAIChatCompletions:
			return protocolconv.ProtocolOpenAIChat, nil
		case accounting.ProtocolOpenAIResponses:
			return protocolconv.ProtocolOpenAIResponses, nil
		}
	case codexMembershipProvider:
		if client == accounting.ProtocolOpenAIChatCompletions || client == accounting.ProtocolOpenAIResponses {
			return protocolconv.ProtocolOpenAIResponses, nil
		}
	case anthropicAPIKeyProvider:
		if client == accounting.ProtocolAnthropicMessages {
			return protocolconv.ProtocolAnthropicMessages, nil
		}
	case geminiAPIKeyProvider:
		if client == accounting.ProtocolGeminiGenerateContent {
			return protocolconv.ProtocolGeminiGenerate, nil
		}
	}
	return "", errors.New("legacy route has no native protocol for this entry")
}

func routeCapability(selected route, client accounting.UsageProtocol, streaming bool) (protocolconv.RouteCapability, error) {
	var clientProtocol protocolconv.Protocol
	switch client {
	case accounting.ProtocolOpenAIChatCompletions:
		clientProtocol = protocolconv.ProtocolOpenAIChat
	case accounting.ProtocolOpenAIResponses:
		clientProtocol = protocolconv.ProtocolOpenAIResponses
	case accounting.ProtocolAnthropicMessages:
		clientProtocol = protocolconv.ProtocolAnthropicMessages
	case accounting.ProtocolGeminiGenerateContent:
		clientProtocol = protocolconv.ProtocolGeminiGenerate
	default:
		return protocolconv.RouteCapability{}, errors.New("unknown client protocol")
	}
	upstream, err := routeUpstreamProtocol(selected, client)
	if err != nil {
		return protocolconv.RouteCapability{}, err
	}
	return protocolconv.RouteCapability{
		ClientProtocol: clientProtocol, UpstreamProtocol: upstream,
		Streaming: true, RequestStreaming: streaming,
		Features: protocolconv.Features(
			protocolconv.FeatureText, protocolconv.FeatureFunctionDefinitions,
			protocolconv.FeatureFunctionCalls, protocolconv.FeatureFunctionResults,
			protocolconv.FeatureSystemInstruction, protocolconv.FeatureDeveloperMessage,
			protocolconv.FeatureUsage, protocolconv.FeatureFinishReason,
		),
	}, nil
}
