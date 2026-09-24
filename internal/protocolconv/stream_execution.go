package protocolconv

import (
	"bytes"
	"encoding/json"
)

// StreamConverter converts complete upstream SSE frames. Raw usage observation
// belongs to the HTTP bridge and must happen before FeedFrame is called.
type StreamConverter interface {
	FeedFrame(SSEFrame) ([]SSEEvent, error)
	EOF() error
}

type crossProtocolStreamConverter struct {
	prepared PreparedRequest
	chat     *ChatToResponsesStream
	response *ResponsesToChatStream
	terminal bool
}

func NewStreamConverter(prepared PreparedRequest) (StreamConverter, error) {
	if !prepared.Streaming {
		return nil, &UnsupportedRouteError{
			ClientProtocol: prepared.Plan.ClientProtocol, UpstreamProtocol: prepared.Plan.UpstreamProtocol,
			Reason: "prepared request is not a cross-protocol stream",
		}
	}
	converter := &crossProtocolStreamConverter{prepared: prepared}
	switch prepared.Plan.Kind {
	case PlanChatToResponses:
		converter.response = new(ResponsesToChatStream)
	case PlanResponsesToChat:
		converter.chat = new(ChatToResponsesStream)
	default:
		return nil, &UnsupportedRouteError{
			ClientProtocol: prepared.Plan.ClientProtocol, UpstreamProtocol: prepared.Plan.UpstreamProtocol,
			Reason: "stream conversion is not implemented for this route",
		}
	}
	return converter, nil
}

func (c *crossProtocolStreamConverter) FeedFrame(frame SSEFrame) ([]SSEEvent, error) {
	if c == nil || c.terminal {
		return nil, invalidUpstream("stream", "stream is already terminal")
	}
	var (
		events []SSEEvent
		err    error
	)
	switch c.prepared.Plan.Kind {
	case PlanChatToResponses:
		if !json.Valid(frame.Data) {
			return nil, invalidUpstream("data", "Responses SSE data must be JSON")
		}
		var envelope struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(frame.Data, &envelope) != nil || envelope.Type == "" {
			return nil, invalidUpstream("type", "Responses SSE event type is required")
		}
		if frame.Event != "" && frame.Event != envelope.Type {
			return nil, invalidUpstream("event", "Responses SSE event name does not match its type")
		}
		events, err = c.response.Feed(frame.Data)
	case PlanResponsesToChat:
		if frame.Event != "" && frame.Event != "message" {
			return nil, invalidUpstream("event", "unexpected Chat Completions SSE event name")
		}
		if bytes.Equal(bytes.TrimSpace(frame.Data), []byte("[DONE]")) {
			events, err = c.chat.Finish()
		} else {
			if !json.Valid(frame.Data) {
				return nil, invalidUpstream("data", "Chat Completions SSE data must be JSON or [DONE]")
			}
			events, err = c.chat.Feed(frame.Data)
		}
	default:
		return nil, invalidUpstream("stream", "unsupported stream plan")
	}
	if err != nil {
		return nil, err
	}
	for _, event := range events {
		if event.Terminal {
			if c.terminal {
				return nil, invalidUpstream("stream", "duplicate terminal event")
			}
			c.terminal = true
		}
	}
	return events, nil
}

func (c *crossProtocolStreamConverter) EOF() error {
	if c == nil {
		return interrupted("stream converter is unavailable")
	}
	switch c.prepared.Plan.Kind {
	case PlanChatToResponses:
		return c.response.EOF()
	case PlanResponsesToChat:
		return c.chat.EOF()
	default:
		return interrupted("stream conversion did not complete")
	}
}
