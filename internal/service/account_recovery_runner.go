package service

// Independently implemented from CPA Cloud's recovery execution plan and the
// public OpenAI Chat/Responses, Anthropic Messages, and Gemini generateContent
// protocol documentation. This runner deliberately accepts no caller prompt,
// performs no credential refresh or failover, and retains no response body.
// Sources: platform.openai.com/docs/api-reference/chat/create,
// platform.openai.com/docs/api-reference/responses/create,
// docs.anthropic.com/en/api/messages, and ai.google.dev/api/generate-content.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/membership"
)

const (
	generationProbePrompt       = "Reply with OK."
	generationProbeMaxTokens    = 64
	generationProbeMaxBody      = 16 << 20
	generationCodeOK            = "generation_ok"
	generationCodeAuth          = "authentication_failed"
	generationCodeRateLimited   = "rate_limited"
	generationCodeUnavailable   = "upstream_unavailable"
	generationCodeTimeout       = "upstream_timeout"
	generationCodeProtocol      = "protocol_error"
	generationCodeUnsupported   = "unsupported"
	generationCodeCancelled     = "cancelled"
	generationCodeConfiguration = "configuration_changed"
	generationCodeInterrupted   = "interrupted"
)

type generationProbeInput struct {
	Selected        route
	Protocol        accounting.UsageProtocol
	APIKey          []byte
	CodexCredential *membership.CodexAuthCredential
}

type generationProbeResult struct {
	Status accounting.Status
	Code   string
	Usage  accounting.Usage
}

var errGenerationProbeIncomplete = errors.New("generation probe response incomplete")

func (a *App) runGenerationProbe(ctx context.Context, input generationProbeInput) generationProbeResult {
	if ctx == nil || a == nil || !validPoolModelName(input.Selected.UpstreamModel) {
		return failedGenerationProbe(generationCodeConfiguration)
	}
	if result, stopped := generationProbeContextResult(ctx); stopped {
		return result
	}

	switch input.Selected.ProviderKind {
	case codexMembershipProvider:
		if len(input.APIKey) != 0 || input.CodexCredential == nil {
			return failedGenerationProbe(generationCodeConfiguration)
		}
		switch input.Protocol {
		case accounting.ProtocolOpenAIChatCompletions:
			return a.runCodexChatGenerationProbe(ctx, input)
		case accounting.ProtocolOpenAIResponses:
			return a.runCodexResponsesGenerationProbe(ctx, input)
		default:
			return failedGenerationProbe(generationCodeUnsupported)
		}
	case "openai-compatible":
		if input.CodexCredential != nil || !validGenerationProbeAPIKey(input.APIKey) {
			return failedGenerationProbe(generationCodeConfiguration)
		}
		if input.Protocol != accounting.ProtocolOpenAIChatCompletions && input.Protocol != accounting.ProtocolOpenAIResponses {
			return failedGenerationProbe(generationCodeUnsupported)
		}
	case anthropicAPIKeyProvider:
		if input.CodexCredential != nil || !validGenerationProbeAPIKey(input.APIKey) {
			return failedGenerationProbe(generationCodeConfiguration)
		}
		if input.Protocol != accounting.ProtocolAnthropicMessages {
			return failedGenerationProbe(generationCodeUnsupported)
		}
	case geminiAPIKeyProvider:
		if input.CodexCredential != nil || !validGenerationProbeAPIKey(input.APIKey) || !validGeminiUpstreamModel(input.Selected.UpstreamModel) {
			return failedGenerationProbe(generationCodeConfiguration)
		}
		if input.Protocol != accounting.ProtocolGeminiGenerateContent {
			return failedGenerationProbe(generationCodeUnsupported)
		}
	default:
		return failedGenerationProbe(generationCodeUnsupported)
	}
	return a.runAPIKeyGenerationProbe(ctx, input)
}

func (a *App) runAPIKeyGenerationProbe(ctx context.Context, input generationProbeInput) generationProbeResult {
	if a.http == nil {
		return failedGenerationProbe(generationCodeUnsupported)
	}
	request, err := a.newGenerationProbeRequest(ctx, input)
	if err != nil {
		if result, stopped := generationProbeContextResult(ctx); stopped {
			return result
		}
		return failedGenerationProbe(generationCodeConfiguration)
	}
	response, err := a.http.Do(request)
	if err != nil {
		if result, stopped := generationProbeContextResult(ctx); stopped {
			return result
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return failedGenerationProbe(generationCodeTimeout)
		}
		return failedGenerationProbe(generationCodeUnavailable)
	}
	if response == nil || response.Body == nil {
		return failedGenerationProbe(generationCodeProtocol)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return generationProbeHTTPFailure(response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		// API-key probes explicitly request non-streaming JSON. An unexpected SSE
		// response is refused rather than treating a partial stream as success.
		return failedGenerationProbe(generationCodeProtocol)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, generationProbeMaxBody+1))
	if err != nil || len(body) == 0 || len(body) > generationProbeMaxBody {
		return failedGenerationProbe(generationCodeProtocol)
	}
	if result, stopped := generationProbeContextResult(ctx); stopped {
		return result
	}
	return validateGenerationProbeResponse(input.Protocol, body)
}

func (a *App) newGenerationProbeRequest(ctx context.Context, input generationProbeInput) (*http.Request, error) {
	endpoint := input.Selected.Endpoint
	var target string
	var payload any
	var err error

	switch input.Protocol {
	case accounting.ProtocolOpenAIChatCompletions:
		endpoint, err = validateEndpoint(ctx, endpoint, a.cfg.AllowLoopbackUpstream)
		if err == nil {
			target, err = upstreamChatURL(endpoint)
		}
		payload = map[string]any{
			"model": input.Selected.UpstreamModel, "stream": false, "max_tokens": generationProbeMaxTokens,
			"messages": []any{map[string]any{"role": "user", "content": generationProbePrompt}},
		}
	case accounting.ProtocolOpenAIResponses:
		endpoint, err = validateEndpoint(ctx, endpoint, a.cfg.AllowLoopbackUpstream)
		if err == nil {
			target, err = upstreamResponsesURL(endpoint)
		}
		payload = map[string]any{
			"model": input.Selected.UpstreamModel, "input": generationProbePrompt,
			"max_output_tokens": generationProbeMaxTokens, "stream": false, "store": false,
		}
	case accounting.ProtocolAnthropicMessages:
		endpoint, err = validateEndpoint(ctx, endpoint, a.cfg.AllowLoopbackUpstream)
		if err == nil {
			target, err = upstreamAnthropicURL(endpoint, false)
		}
		payload = map[string]any{
			"model": input.Selected.UpstreamModel, "max_tokens": generationProbeMaxTokens, "stream": false,
			"messages": []any{map[string]any{"role": "user", "content": generationProbePrompt}},
		}
	case accounting.ProtocolGeminiGenerateContent:
		endpoint, err = validateGeminiEndpoint(ctx, endpoint, a.cfg.AllowLoopbackUpstream)
		if err == nil {
			target, err = geminiGenerateURL(endpoint, input.Selected.UpstreamModel, false)
		}
		payload = map[string]any{
			"contents": []any{map[string]any{
				"role": "user", "parts": []any{map[string]any{"text": generationProbePrompt}},
			}},
			"generationConfig": map[string]any{"maxOutputTokens": generationProbeMaxTokens},
		}
	default:
		return nil, errors.New("unsupported generation probe protocol")
	}
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("invalid generation probe payload")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("invalid generation probe request")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	key := string(input.APIKey)
	switch input.Protocol {
	case accounting.ProtocolAnthropicMessages:
		request.Header.Set("X-Api-Key", key)
		request.Header.Set("Anthropic-Version", "2023-06-01")
	case accounting.ProtocolGeminiGenerateContent:
		request.Header.Set("x-goog-api-key", key)
	default:
		request.Header.Set("Authorization", "Bearer "+key)
	}
	return request, nil
}

func (a *App) runCodexChatGenerationProbe(ctx context.Context, input generationProbeInput) generationProbeResult {
	if a.codex == nil {
		return failedGenerationProbe(generationCodeUnsupported)
	}
	result, runErr := a.codex.Complete(ctx, input.CodexCredential, membership.CodexTextRequest{
		Model:    input.Selected.UpstreamModel,
		Messages: []membership.CodexTextMessage{{Role: membership.CodexRoleUser, Text: generationProbePrompt}},
	})
	if runErr != nil {
		return generationProbeCodexFailure(ctx, runErr)
	}
	if stopped, ok := generationProbeContextResult(ctx); ok {
		return stopped
	}
	usage, err := parseCodexGenerationUsage(result.Usage)
	if err != nil {
		return failedGenerationProbe(generationCodeProtocol)
	}
	if strings.TrimSpace(result.ResponseID) == "" || len(result.ResponseID) > 1024 || strings.TrimSpace(result.Text) == "" || len(result.Text) > generationProbeMaxBody {
		return generationProbeResult{Status: accounting.StatusFailed, Code: generationCodeProtocol, Usage: usage}
	}
	return generationProbeResult{Status: accounting.StatusSucceeded, Code: generationCodeOK, Usage: usage}
}

func (a *App) runCodexResponsesGenerationProbe(ctx context.Context, input generationProbeInput) generationProbeResult {
	if a.responses == nil {
		return failedGenerationProbe(generationCodeUnsupported)
	}
	body, err := json.Marshal(map[string]any{
		"model": input.Selected.UpstreamModel, "input": generationProbePrompt,
		"stream": false, "store": false,
	})
	if err != nil {
		return failedGenerationProbe(generationCodeConfiguration)
	}
	response, runErr := a.responses.Responses(ctx, input.CodexCredential, body, nil)
	if runErr != nil {
		return generationProbeCodexFailure(ctx, runErr)
	}
	if stopped, ok := generationProbeContextResult(ctx); ok {
		return stopped
	}
	return validateGenerationProbeResponse(accounting.ProtocolOpenAIResponses, response)
}

func validateGenerationProbeResponse(protocol accounting.UsageProtocol, body []byte) generationProbeResult {
	if len(body) == 0 || len(body) > generationProbeMaxBody {
		return failedGenerationProbe(generationCodeProtocol)
	}
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil || root == nil || presentNonNullJSON(root["error"]) {
		return failedGenerationProbe(generationCodeProtocol)
	}
	usage, err := accounting.ParseUsage(protocol, body)
	if err != nil {
		return failedGenerationProbe(generationCodeProtocol)
	}
	switch protocol {
	case accounting.ProtocolOpenAIChatCompletions:
		err = validateGenerationChatResponse(body)
	case accounting.ProtocolOpenAIResponses:
		err = validateGenerationResponsesResponse(body)
	case accounting.ProtocolAnthropicMessages:
		err = validateGenerationAnthropicResponse(body)
	case accounting.ProtocolGeminiGenerateContent:
		err = validateGenerationGeminiResponse(body)
	default:
		return failedGenerationProbe(generationCodeUnsupported)
	}
	if err != nil {
		status, code := accounting.StatusFailed, generationCodeProtocol
		if errors.Is(err, errGenerationProbeIncomplete) {
			status, code = accounting.StatusInterrupted, generationCodeInterrupted
		}
		return generationProbeResult{Status: status, Code: code, Usage: usage}
	}
	return generationProbeResult{Status: accounting.StatusSucceeded, Code: generationCodeOK, Usage: usage}
}

func validateGenerationChatResponse(body []byte) error {
	var envelope struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Choices []struct {
			FinishReason *string `json:"finish_reason"`
			Message      struct {
				Role         string          `json:"role"`
				Content      string          `json:"content"`
				ToolCalls    json.RawMessage `json:"tool_calls"`
				FunctionCall json.RawMessage `json:"function_call"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.ID == "" || envelope.Object != "chat.completion" || len(envelope.Choices) == 0 {
		return errors.New("invalid chat completion")
	}
	for _, choice := range envelope.Choices {
		if choice.FinishReason == nil || *choice.FinishReason == "" {
			return errGenerationProbeIncomplete
		}
		if *choice.FinishReason == "length" {
			return errGenerationProbeIncomplete
		}
		if *choice.FinishReason != "stop" || choice.Message.Role != "assistant" || strings.TrimSpace(choice.Message.Content) == "" ||
			nonNullJSON(choice.Message.ToolCalls) || nonNullJSON(choice.Message.FunctionCall) {
			return errors.New("invalid chat completion terminal")
		}
	}
	return nil
}

func validateGenerationResponsesResponse(body []byte) error {
	var envelope struct {
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return errors.New("invalid responses payload")
	}
	if envelope.Status == "incomplete" || envelope.Status == "in_progress" || envelope.Status == "queued" {
		return errGenerationProbeIncomplete
	}
	if validateCompletedResponse(body) != nil || len(envelope.Output) == 0 {
		return errors.New("invalid completed response")
	}
	foundText := false
	for _, output := range envelope.Output {
		if output.Type == "reasoning" {
			continue
		}
		if output.Type != "message" || output.Role != "assistant" {
			return errors.New("completed response contains unsupported output")
		}
		for _, content := range output.Content {
			if content.Type != "output_text" {
				return errors.New("completed response contains non-text content")
			}
			if strings.TrimSpace(content.Text) != "" {
				foundText = true
			}
		}
	}
	if foundText {
		return nil
	}
	return errors.New("completed response has no output text")
}

func nonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) && !bytes.Equal(trimmed, []byte("[]"))
}

func presentNonNullJSON(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null"))
}

func validateGenerationAnthropicResponse(body []byte) error {
	var envelope struct {
		ID         string  `json:"id"`
		Type       string  `json:"type"`
		Role       string  `json:"role"`
		StopReason *string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.ID == "" || envelope.Type != "message" || envelope.Role != "assistant" {
		return errors.New("invalid Anthropic message")
	}
	if envelope.StopReason == nil || *envelope.StopReason == "" {
		return errGenerationProbeIncomplete
	}
	switch *envelope.StopReason {
	case "max_tokens":
		return errGenerationProbeIncomplete
	case "end_turn", "stop_sequence":
	default:
		return errors.New("invalid Anthropic terminal")
	}
	for _, content := range envelope.Content {
		if content.Type == "text" && strings.TrimSpace(content.Text) != "" {
			return nil
		}
	}
	return errors.New("Anthropic message has no text")
}

func validateGenerationGeminiResponse(body []byte) error {
	var envelope struct {
		Candidates []struct {
			FinishReason string `json:"finishReason"`
			Content      struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Candidates) == 0 {
		return errors.New("invalid Gemini response")
	}
	foundText := false
	for _, candidate := range envelope.Candidates {
		if candidate.FinishReason == "" || candidate.FinishReason == "FINISH_REASON_UNSPECIFIED" {
			return errGenerationProbeIncomplete
		}
		if candidate.FinishReason == "MAX_TOKENS" {
			return errGenerationProbeIncomplete
		}
		if candidate.FinishReason != "STOP" {
			return errors.New("invalid Gemini terminal")
		}
		if candidate.Content.Role != "model" {
			return errors.New("invalid Gemini role")
		}
		for _, part := range candidate.Content.Parts {
			foundText = foundText || strings.TrimSpace(part.Text) != ""
		}
	}
	if !foundText {
		return errors.New("Gemini response has no text")
	}
	return nil
}

func parseCodexGenerationUsage(usage membership.CodexUsage) (accounting.Usage, error) {
	metadata := make(map[string]any)
	if usage.InputTokens != nil {
		metadata["prompt_tokens"] = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		metadata["completion_tokens"] = *usage.OutputTokens
	}
	if usage.TotalTokens != nil {
		metadata["total_tokens"] = *usage.TotalTokens
	}
	inputDetails := make(map[string]any)
	if usage.CachedInputTokens != nil {
		inputDetails["cached_tokens"] = *usage.CachedInputTokens
	}
	// CodexUsage has no cache-write field. Keep it unknown; the shared parser
	// consequently also keeps ordinary input unknown instead of inventing zero.
	if len(inputDetails) != 0 {
		metadata["prompt_tokens_details"] = inputDetails
	}
	if usage.ReasoningOutputTokens != nil {
		metadata["completion_tokens_details"] = map[string]any{"reasoning_tokens": *usage.ReasoningOutputTokens}
	}
	body, err := json.Marshal(map[string]any{"usage": metadata})
	if err != nil {
		return accounting.Usage{}, accounting.ErrInvalidUsage
	}
	return accounting.ParseUsage(accounting.ProtocolOpenAIChatCompletions, body)
}

func generationProbeHTTPFailure(status int) generationProbeResult {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return failedGenerationProbe(generationCodeAuth)
	case http.StatusTooManyRequests:
		return failedGenerationProbe(generationCodeRateLimited)
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return failedGenerationProbe(generationCodeTimeout)
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return failedGenerationProbe(generationCodeConfiguration)
	default:
		if status >= 500 {
			return failedGenerationProbe(generationCodeUnavailable)
		}
		return failedGenerationProbe(generationCodeProtocol)
	}
}

func generationProbeCodexFailure(ctx context.Context, runErr *codexRunError) generationProbeResult {
	if result, stopped := generationProbeContextResult(ctx); stopped {
		return result
	}
	if runErr == nil {
		return failedGenerationProbe(generationCodeProtocol)
	}
	switch runErr.Code {
	case membership.CodexErrorAccessTokenInvalid, membership.CodexErrorReauthentication,
		membership.CodexErrorAccountIDRequired, membership.CodexErrorAuthModeUnsupported:
		return failedGenerationProbe(generationCodeAuth)
	case membership.CodexErrorRateLimited, membership.CodexErrorUsageExhausted:
		return failedGenerationProbe(generationCodeRateLimited)
	case membership.CodexErrorTimeout:
		return failedGenerationProbe(generationCodeTimeout)
	case membership.CodexErrorCancelled:
		return generationProbeResult{Status: accounting.StatusCancelled, Code: generationCodeCancelled}
	case membership.CodexErrorUnsupportedFeature:
		return failedGenerationProbe(generationCodeUnsupported)
	case membership.CodexErrorInvalidRequest:
		return failedGenerationProbe(generationCodeConfiguration)
	case membership.CodexErrorProtocol, membership.CodexErrorResponseTooLarge,
		membership.CodexErrorSSEFrameTooLarge, membership.CodexErrorRedirectRejected,
		membership.CodexErrorEventConsumerStopped:
		return failedGenerationProbe(generationCodeProtocol)
	default:
		return failedGenerationProbe(generationCodeUnavailable)
	}
}

func generationProbeContextResult(ctx context.Context) (generationProbeResult, bool) {
	if ctx == nil || ctx.Err() == nil {
		return generationProbeResult{}, false
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return failedGenerationProbe(generationCodeTimeout), true
	}
	return generationProbeResult{Status: accounting.StatusCancelled, Code: generationCodeCancelled}, true
}

func failedGenerationProbe(code string) generationProbeResult {
	return generationProbeResult{Status: accounting.StatusFailed, Code: code}
}

func validGenerationProbeAPIKey(key []byte) bool {
	if len(key) == 0 || len(key) > 16<<10 {
		return false
	}
	for _, value := range key {
		if value < 0x21 || value > 0x7e {
			return false
		}
	}
	return true
}
