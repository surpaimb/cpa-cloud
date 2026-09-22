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
	doneItems    map[string]codexSeenOutputItem
	usage        CodexUsage
	unknown      int
	completed    bool
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
		_, _ = s.text.WriteString(*event.Delta)
		return emitCodexEvent(consume, CodexStreamEvent{kind: CodexEventTextDelta, text: *event.Delta})
	case "response.output_item.added":
		return consumeCodexOutputItemAdded(data)
	case "response.output_item.done":
		return s.consumeOutputItem(data)
	case "response.reasoning_summary_text.delta",
		"response.reasoning_summary_text.done",
		"response.reasoning_summary_part.added",
		"response.reasoning_summary_part.done",
		"response.reasoning_text.delta",
		"response.content_part.added",
		"response.content_part.done",
		"response.in_progress",
		"response.metadata",
		"codex.response.metadata",
		"response.output_text.done",
		"responsesapi.websocket_timing":
		// The pinned official client treats these as non-text metadata or
		// framing. This text-only adapter deliberately does not expose
		// chain-of-thought.
		return nil
	case "response.custom_tool_call_input.delta",
		"response.custom_tool_call_input.done",
		"response.function_call_arguments.delta",
		"response.function_call_arguments.done":
		return newCodexAdapterError(CodexErrorUnsupportedFeature)
	case "response.completed":
		if s.completed {
			return newCodexAdapterError(CodexErrorProtocol)
		}
		usage, responseID, output, err := parseCodexCompleted(data)
		if err != nil {
			return err
		}
		completedText, completedHasMessage, err := parseCodexCompletedOutput(output)
		if err != nil {
			return err
		}
		if completedHasMessage {
			if s.sawFinalItem && s.finalText.String() != completedText {
				return newCodexAdapterError(CodexErrorProtocol)
			}
			if !s.sawFinalItem {
				s.sawFinalItem = true
				_, _ = s.finalText.WriteString(completedText)
			}
		}
		if responseID != "" {
			if s.responseID != "" && s.responseID != responseID {
				return newCodexAdapterError(CodexErrorProtocol)
			}
			s.responseID = responseID
		}
		if s.sawFinalItem {
			visibleText := s.text.String()
			finalText := s.finalText.String()
			if !strings.HasPrefix(finalText, visibleText) {
				return newCodexAdapterError(CodexErrorProtocol)
			}
			missingText := strings.TrimPrefix(finalText, visibleText)
			if missingText != "" {
				_, _ = s.text.WriteString(missingText)
				if err := emitCodexEvent(consume, CodexStreamEvent{kind: CodexEventTextDelta, text: missingText}); err != nil {
					return err
				}
			}
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

type codexOutputItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Role    string `json:"role"`
	Summary []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	} `json:"content"`
	EncryptedContent json.RawMessage `json:"encrypted_content"`
}

type codexSeenOutputItem struct {
	kind string
	text string
}

func (s *codexSSEState) consumeOutputItem(data []byte) error {
	var event struct {
		Item codexOutputItem `json:"item"`
	}
	if json.Unmarshal(data, &event) != nil || event.Item.Type == "" {
		return newCodexAdapterError(CodexErrorProtocol)
	}
	text, isMessage, err := parseCodexOutputItem(event.Item)
	if err != nil {
		return err
	}
	duplicate, err := s.recordDoneItem(event.Item, text)
	if err != nil || duplicate {
		return err
	}
	if !isMessage {
		return nil
	}
	s.sawFinalItem = true
	_, _ = s.finalText.WriteString(text)
	return nil
}

func (s *codexSSEState) recordDoneItem(item codexOutputItem, text string) (bool, error) {
	if item.ID == "" {
		return false, nil
	}
	if seen, ok := s.doneItems[item.ID]; ok {
		if seen.kind != item.Type || seen.text != text {
			return false, newCodexAdapterError(CodexErrorProtocol)
		}
		return true, nil
	}
	if s.doneItems == nil {
		s.doneItems = make(map[string]codexSeenOutputItem)
	}
	s.doneItems[item.ID] = codexSeenOutputItem{kind: item.Type, text: text}
	return false, nil
}

func consumeCodexOutputItemAdded(data []byte) error {
	var event struct {
		Item codexOutputItem `json:"item"`
	}
	if json.Unmarshal(data, &event) != nil || event.Item.Type == "" {
		return newCodexAdapterError(CodexErrorProtocol)
	}
	_, _, err := parseCodexOutputItem(event.Item)
	return err
}

func parseCodexOutputItem(item codexOutputItem) (string, bool, error) {
	switch item.Type {
	case "reasoning":
		if item.Summary == nil {
			return "", false, newCodexAdapterError(CodexErrorProtocol)
		}
		for _, summary := range item.Summary {
			if summary.Type != "summary_text" || summary.Text == nil {
				return "", false, newCodexAdapterError(CodexErrorProtocol)
			}
		}
		for _, content := range item.Content {
			if (content.Type != "reasoning_text" && content.Type != "text") || content.Text == nil {
				return "", false, newCodexAdapterError(CodexErrorProtocol)
			}
		}
		if len(item.EncryptedContent) > 0 && !isJSONNull(item.EncryptedContent) {
			var encryptedContent string
			if json.Unmarshal(item.EncryptedContent, &encryptedContent) != nil {
				return "", false, newCodexAdapterError(CodexErrorProtocol)
			}
		}
		// Reasoning summary, raw content, and encrypted content are normal
		// protocol metadata but outside this text-response surface. Do not
		// retain or forward them.
		return "", false, nil
	case "message":
		if item.Role != "assistant" {
			return "", false, newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		if item.Content == nil {
			return "", false, newCodexAdapterError(CodexErrorProtocol)
		}
		var text strings.Builder
		for _, content := range item.Content {
			if content.Type != "output_text" || content.Text == nil {
				return "", false, newCodexAdapterError(CodexErrorUnsupportedFeature)
			}
			_, _ = text.WriteString(*content.Text)
		}
		return text.String(), true, nil
	default:
		return "", false, newCodexAdapterError(CodexErrorUnsupportedFeature)
	}
}

func parseCodexCompletedOutput(output []json.RawMessage) (string, bool, error) {
	var text strings.Builder
	hasMessage := false
	for _, rawItem := range output {
		var item codexOutputItem
		if validateJSON(rawItem) != nil || json.Unmarshal(rawItem, &item) != nil || item.Type == "" {
			return "", false, newCodexAdapterError(CodexErrorProtocol)
		}
		itemText, isMessage, err := parseCodexOutputItem(item)
		if err != nil {
			return "", false, err
		}
		if isMessage {
			hasMessage = true
			_, _ = text.WriteString(itemText)
		}
	}
	return text.String(), hasMessage, nil
}

func parseCodexCompleted(data []byte) (CodexUsage, string, []json.RawMessage, error) {
	var event struct {
		Response *struct {
			ID     string            `json:"id"`
			Output []json.RawMessage `json:"output"`
			Usage  *struct {
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
		return CodexUsage{}, "", nil, newCodexAdapterError(CodexErrorProtocol)
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
			return CodexUsage{}, "", nil, newCodexAdapterError(CodexErrorProtocol)
		}
	}
	return usage, event.Response.ID, event.Response.Output, nil
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
	return &CodexTextResult{text: s.text.String(), responseID: s.responseID, usage: s.usage, unknownCount: s.unknown}
}
