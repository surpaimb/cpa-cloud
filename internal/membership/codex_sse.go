package membership

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type codexSSEState struct {
	responseID   string
	text         strings.Builder
	finalText    strings.Builder
	usage        CodexUsage
	unknown      int
	completed    bool
	sawTextDelta bool
	sawFinalItem bool
}

func (a *CodexDirectAdapter) parseSSE(ctx context.Context, body io.Reader, consume func(CodexStreamEvent) error) (*CodexTextResult, error) {
	limited := &io.LimitedReader{R: body, N: a.maxResponse + 1}
	scanner := bufio.NewScanner(limited)
	initialBufferSize := 64 << 10
	if a.maxLine < initialBufferSize {
		initialBufferSize = a.maxLine
	}
	scanner.Buffer(make([]byte, initialBufferSize), a.maxLine)
	state := &codexSSEState{}
	var data bytes.Buffer

	dispatch := func() error {
		if data.Len() == 0 {
			return nil
		}
		raw := bytes.TrimSuffix(data.Bytes(), []byte("\n"))
		data.Reset()
		if len(raw) == 0 {
			return nil
		}
		return state.consumeEvent(raw, consume)
	}

	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, classifyCodexTransportError(ctx, err)
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			if err := dispatch(); err != nil {
				return nil, err
			}
			if state.completed {
				return state.result(), nil
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			field = line
			value = nil
		}
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		if bytes.Equal(field, []byte("data")) {
			if data.Len()+len(value)+1 > a.maxEvent {
				return nil, newCodexAdapterError(CodexErrorSSEFrameTooLarge)
			}
			_, _ = data.Write(value)
			_ = data.WriteByte('\n')
		}
	}
	if scannerErr := scanner.Err(); scannerErr != nil {
		if errors.Is(scannerErr, context.Canceled) || errors.Is(scannerErr, context.DeadlineExceeded) || ctx.Err() != nil {
			return nil, classifyCodexTransportError(ctx, scannerErr)
		}
		if limited.N <= 0 {
			return nil, newCodexAdapterError(CodexErrorResponseTooLarge)
		}
		return nil, newCodexAdapterError(CodexErrorSSEFrameTooLarge)
	}
	if limited.N <= 0 {
		return nil, newCodexAdapterError(CodexErrorResponseTooLarge)
	}
	if err := dispatch(); err != nil {
		return nil, err
	}
	if state.completed {
		return state.result(), nil
	}
	return nil, newCodexAdapterError(CodexErrorProtocol)
}

func (s *codexSSEState) consumeEvent(data []byte, consume func(CodexStreamEvent) error) error {
	if bytes.Equal(data, []byte("[DONE]")) || validateJSON(data) != nil {
		return newCodexAdapterError(CodexErrorProtocol)
	}
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Type == "" {
		return newCodexAdapterError(CodexErrorProtocol)
	}
	switch envelope.Type {
	case "response.created":
		var event struct {
			Response struct {
				ID string `json:"id"`
			} `json:"response"`
		}
		if json.Unmarshal(data, &event) != nil || event.Response.ID == "" {
			return newCodexAdapterError(CodexErrorProtocol)
		}
		s.responseID = event.Response.ID
		return emitCodexEvent(consume, CodexStreamEvent{kind: CodexEventStarted, responseID: s.responseID})
	case "response.output_text.delta":
		var event struct {
			Delta *string `json:"delta"`
		}
		if json.Unmarshal(data, &event) != nil || event.Delta == nil {
			return newCodexAdapterError(CodexErrorProtocol)
		}
		s.sawTextDelta = true
		_, _ = s.text.WriteString(*event.Delta)
		return emitCodexEvent(consume, CodexStreamEvent{kind: CodexEventTextDelta, text: *event.Delta})
	case "response.output_item.done":
		return s.consumeOutputItem(data)
	case "response.completed":
		if s.completed {
			return newCodexAdapterError(CodexErrorProtocol)
		}
		usage, responseID, err := parseCodexCompleted(data)
		if err != nil {
			return err
		}
		if responseID != "" {
			if s.responseID != "" && s.responseID != responseID {
				return newCodexAdapterError(CodexErrorProtocol)
			}
			s.responseID = responseID
		}
		if s.sawFinalItem && s.sawTextDelta && s.text.String() != s.finalText.String() {
			return newCodexAdapterError(CodexErrorProtocol)
		}
		s.usage = usage
		if usage.known() {
			if err := emitCodexEvent(consume, CodexStreamEvent{kind: CodexEventUsage, usage: usage}); err != nil {
				return err
			}
		}
		s.completed = true
		return emitCodexEvent(consume, CodexStreamEvent{kind: CodexEventCompleted, responseID: s.responseID, unknownCount: s.unknown})
	case "response.incomplete":
		var event struct {
			Response struct {
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
			} `json:"response"`
		}
		if json.Unmarshal(data, &event) != nil {
			return newCodexAdapterError(CodexErrorProtocol)
		}
		code := ""
		if event.Response.IncompleteDetails != nil {
			code = allowlistedCodexCode(event.Response.IncompleteDetails.Reason)
		}
		return &CodexAdapterError{code: CodexErrorUpstream, providerCode: code}
	case "response.failed":
		var event struct {
			Response struct {
				Error *struct {
					Code string `json:"code"`
				} `json:"error"`
			} `json:"response"`
		}
		if json.Unmarshal(data, &event) != nil {
			return newCodexAdapterError(CodexErrorProtocol)
		}
		code := ""
		if event.Response.Error != nil {
			code = allowlistedCodexCode(event.Response.Error.Code)
		}
		return &CodexAdapterError{code: CodexErrorUpstream, providerCode: code}
	default:
		s.unknown++
		return nil
	}
}

func (s *codexSSEState) consumeOutputItem(data []byte) error {
	var event struct {
		Item struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string  `json:"type"`
				Text *string `json:"text"`
			} `json:"content"`
		} `json:"item"`
	}
	if json.Unmarshal(data, &event) != nil || event.Item.Type == "" {
		return newCodexAdapterError(CodexErrorProtocol)
	}
	if event.Item.Type != "message" || event.Item.Role != "assistant" {
		return newCodexAdapterError(CodexErrorUnsupportedFeature)
	}
	s.sawFinalItem = true
	for _, content := range event.Item.Content {
		if content.Type != "output_text" || content.Text == nil {
			return newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		_, _ = s.finalText.WriteString(*content.Text)
	}
	return nil
}

func parseCodexCompleted(data []byte) (CodexUsage, string, error) {
	var event struct {
		Response *struct {
			ID    string `json:"id"`
			Usage *struct {
				InputTokens       *int64 `json:"input_tokens"`
				OutputTokens      *int64 `json:"output_tokens"`
				TotalTokens       *int64 `json:"total_tokens"`
				InputTokenDetails *struct {
					CachedTokens *int64 `json:"cached_tokens"`
				} `json:"input_tokens_details"`
				OutputTokenDetails *struct {
					ReasoningTokens *int64 `json:"reasoning_tokens"`
				} `json:"output_tokens_details"`
			} `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(data, &event) != nil || event.Response == nil {
		return CodexUsage{}, "", newCodexAdapterError(CodexErrorProtocol)
	}
	usage := CodexUsage{}
	if event.Response.Usage != nil {
		usage.InputTokens = event.Response.Usage.InputTokens
		usage.OutputTokens = event.Response.Usage.OutputTokens
		usage.TotalTokens = event.Response.Usage.TotalTokens
		if event.Response.Usage.InputTokenDetails != nil {
			usage.CachedInputTokens = event.Response.Usage.InputTokenDetails.CachedTokens
		}
		if event.Response.Usage.OutputTokenDetails != nil {
			usage.ReasoningOutputTokens = event.Response.Usage.OutputTokenDetails.ReasoningTokens
		}
		if hasNegativeCodexUsage(usage) {
			return CodexUsage{}, "", newCodexAdapterError(CodexErrorProtocol)
		}
	}
	return usage, event.Response.ID, nil
}

func hasNegativeCodexUsage(usage CodexUsage) bool {
	for _, value := range []*int64{usage.InputTokens, usage.CachedInputTokens, usage.OutputTokens, usage.ReasoningOutputTokens, usage.TotalTokens} {
		if value != nil && *value < 0 {
			return true
		}
	}
	return false
}

func emitCodexEvent(consume func(CodexStreamEvent) error, event CodexStreamEvent) error {
	if consume == nil {
		return nil
	}
	if err := consume(event); err != nil {
		return newCodexAdapterError(CodexErrorEventConsumerStopped)
	}
	return nil
}

func (s *codexSSEState) result() *CodexTextResult {
	text := s.text.String()
	if !s.sawTextDelta {
		text = s.finalText.String()
	}
	return &CodexTextResult{text: text, responseID: s.responseID, usage: s.usage, unknownCount: s.unknown}
}
