package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"cpacloud.local/server/internal/membership"
	"cpacloud.local/server/internal/scheduling"
)

type codexRequestMappingError struct {
	unsupported bool
}

func (e *codexRequestMappingError) Error() string {
	return "Codex Chat Completions request cannot be mapped."
}

func mapCodexChatRequest(payload map[string]json.RawMessage, upstreamModel string) (membership.CodexTextRequest, error) {
	for field := range payload {
		switch field {
		case "model", "messages", "stream":
		default:
			return membership.CodexTextRequest{}, &codexRequestMappingError{unsupported: true}
		}
	}
	if rawStream, ok := payload["stream"]; ok {
		trimmed := bytes.TrimSpace(rawStream)
		if !bytes.Equal(trimmed, []byte("true")) && !bytes.Equal(trimmed, []byte("false")) {
			return membership.CodexTextRequest{}, &codexRequestMappingError{}
		}
	}
	rawMessages, ok := payload["messages"]
	if !ok {
		return membership.CodexTextRequest{}, &codexRequestMappingError{}
	}
	var incoming []map[string]json.RawMessage
	if json.Unmarshal(rawMessages, &incoming) != nil || len(incoming) == 0 || len(incoming) > 4096 {
		return membership.CodexTextRequest{}, &codexRequestMappingError{}
	}
	messages := make([]membership.CodexTextMessage, 0, len(incoming))
	totalTextBytes := 0
	for _, source := range incoming {
		if source == nil {
			return membership.CodexTextRequest{}, &codexRequestMappingError{}
		}
		for field := range source {
			if field != "role" && field != "content" {
				return membership.CodexTextRequest{}, &codexRequestMappingError{unsupported: true}
			}
		}
		var role, content string
		rawRole, rolePresent := source["role"]
		rawContent, contentPresent := source["content"]
		if !rolePresent || !contentPresent || json.Unmarshal(rawRole, &role) != nil {
			return membership.CodexTextRequest{}, &codexRequestMappingError{}
		}
		if json.Unmarshal(rawContent, &content) != nil {
			trimmed := bytes.TrimSpace(rawContent)
			unsupported := len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '{')
			return membership.CodexTextRequest{}, &codexRequestMappingError{unsupported: unsupported}
		}
		var mappedRole membership.CodexMessageRole
		switch role {
		case "user":
			mappedRole = membership.CodexRoleUser
		case "assistant":
			mappedRole = membership.CodexRoleAssistant
		case "system", "developer", "tool":
			return membership.CodexTextRequest{}, &codexRequestMappingError{unsupported: true}
		default:
			return membership.CodexTextRequest{}, &codexRequestMappingError{unsupported: true}
		}
		if content == "" {
			return membership.CodexTextRequest{}, &codexRequestMappingError{}
		}
		totalTextBytes += len(content)
		if totalTextBytes > 1<<20 {
			return membership.CodexTextRequest{}, &codexRequestMappingError{}
		}
		messages = append(messages, membership.CodexTextMessage{Role: mappedRole, Text: content})
	}
	return membership.CodexTextRequest{Model: upstreamModel, Messages: messages}, nil
}

type codexChatPreflight struct {
	selected   route
	credential *membership.CodexAuthCredential
	mapped     membership.CodexTextRequest
}

func (p *codexChatPreflight) Destroy() {
	if p != nil && p.credential != nil {
		p.credential.Destroy()
		p.credential = nil
	}
}

func (a *App) prepareCodexChatCompletion(ctx context.Context, payload map[string]json.RawMessage, selected route) (*codexChatPreflight, *modelPreflightError) {
	if !a.cfg.ExperimentalCodexMembership {
		return nil, requestPreflightFailure(http.StatusForbidden, "feature_disabled", "Codex membership routing is disabled.")
	}
	if selected.KeyVersion != 2 || !selected.CredentialState.Valid {
		return nil, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailureAuth)
	}
	if selected.CredentialState.String == codexStateReauth {
		return nil, accountPreflightFailure(http.StatusBadGateway, "upstream_reauthentication_required", "The upstream credential must be re-imported.", scheduling.FailureAuth)
	}
	if selected.CredentialState.String != codexStateImported && selected.CredentialState.String != codexStateVerified {
		return nil, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
	}
	mapped, err := mapCodexChatRequest(payload, selected.UpstreamModel)
	if err != nil {
		var mappingError *codexRequestMappingError
		if errors.As(err, &mappingError) && mappingError.unsupported {
			return nil, requestPreflightFailure(http.StatusBadRequest, "unsupported_feature", "This request uses a feature that is not supported for Codex membership routing.")
		}
		return nil, requestPreflightFailure(http.StatusBadRequest, "invalid_request_error", "Invalid request.")
	}
	selected, credential, runErr := a.acquireCodexCredential(ctx, selected)
	if runErr != nil {
		status, code, message := codexPublicError(runErr)
		var failure *modelPreflightError
		if runErr.PreflightAccountSpecific {
			failure = accountPreflightFailure(status, code, message, runErr.PreflightClass)
		} else {
			failure = requestPreflightFailure(status, code, message)
		}
		failure.UpstreamStatus = runErr.UpstreamStatus
		return nil, failure
	}
	return &codexChatPreflight{selected: selected, credential: credential, mapped: mapped}, nil
}

func (a *App) handleCodexChatCompletion(w http.ResponseWriter, r *http.Request, publicModel string, stream bool, prepared *codexChatPreflight, modelRequestID string) {
	if stream {
		a.streamCodexChatCompletion(w, r, publicModel, prepared.mapped, prepared.credential, prepared.selected, modelRequestID)
		return
	}
	result, runErr := a.codex.Complete(r.Context(), prepared.credential, prepared.mapped)
	if runErr != nil {
		a.handleCodexRunFailure(w, r, prepared.selected, modelRequestID, runErr, false)
		return
	}
	if stateErr := a.markCodexVerified(prepared.selected.AccountID, prepared.selected.Revision); stateErr != nil {
		a.finishRequest(modelRequestID, "failed", http.StatusOK)
		writeModelError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.", modelRequestID)
		return
	}
	a.observeCodexChatUsage(modelRequestID, result.ResponseID, codexResponsesUsage(result.Usage))
	if err := a.finishRequestChecked(modelRequestID, "succeeded", http.StatusOK); err != nil {
		writeModelError(w, 503, "storage_unavailable", "Service is temporarily unavailable.", modelRequestID)
		return
	}
	response := map[string]any{
		"id":      localChatCompletionID(modelRequestID),
		"object":  "chat.completion",
		"created": time.Now().UTC().Unix(),
		"model":   publicModel,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": result.Text},
			"finish_reason": "stop",
		}},
	}
	if usage := codexChatUsage(result.Usage); usage != nil {
		response["usage"] = usage
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) streamCodexChatCompletion(w http.ResponseWriter, r *http.Request, publicModel string, mapped membership.CodexTextRequest, credential *membership.CodexAuthCredential, selected route, modelRequestID string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		a.finishRequest(modelRequestID, "failed", 0)
		writeModelError(w, http.StatusInternalServerError, "streaming_unavailable", "Streaming is unavailable.", modelRequestID)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	created := time.Now().UTC().Unix()
	committed := false
	writeFailed := false
	var usage membership.CodexUsage
	ensureStarted := func() error {
		if committed {
			return nil
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-store")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		committed = true
		chunk := codexStreamChunk(modelRequestID, publicModel, created, map[string]any{"role": "assistant", "content": ""}, nil, nil)
		if err := writeSSEJSON(w, chunk); err != nil {
			writeFailed = true
			cancel()
			return err
		}
		flusher.Flush()
		return nil
	}
	runErr := a.codex.Stream(ctx, credential, mapped, func(event codexExecutionEvent) error {
		switch event.Kind {
		case membership.CodexEventStarted:
			return ensureStarted()
		case membership.CodexEventTextDelta:
			if err := ensureStarted(); err != nil {
				return err
			}
			chunk := codexStreamChunk(modelRequestID, publicModel, created, map[string]any{"content": event.Text}, nil, nil)
			if err := writeSSEJSON(w, chunk); err != nil {
				writeFailed = true
				cancel()
				return err
			}
			flusher.Flush()
		case membership.CodexEventUsage, membership.CodexEventCompleted:
			if codexUsageKnown(event.Usage) {
				usage = event.Usage
				a.observeCodexChatUsage(modelRequestID, event.ResponseID, codexResponsesUsage(usage))
			}
		case membership.CodexEventFailed, membership.CodexEventCancelled:
			// Rendering is decided from the returned normalized error so a
			// pre-stream failure can still use a normal JSON HTTP status.
		}
		return nil
	})
	if runErr != nil {
		if writeFailed || r.Context().Err() != nil {
			a.finishRequest(modelRequestID, "cancelled", runErr.UpstreamStatus)
			return
		}
		a.handleCodexRunFailure(w, r, selected, modelRequestID, runErr, committed)
		return
	}
	if stateErr := a.markCodexVerified(selected.AccountID, selected.Revision); stateErr != nil {
		outcome := "failed"
		if committed {
			outcome = "interrupted"
		}
		a.finishRequest(modelRequestID, outcome, http.StatusOK)
		if committed {
			writeCodexStreamError(w, modelRequestID, "storage_unavailable", "Service is temporarily unavailable.")
		} else {
			writeModelError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.", modelRequestID)
		}
		return
	}
	if err := ensureStarted(); err != nil {
		a.finishRequest(modelRequestID, "cancelled", http.StatusOK)
		return
	}
	usageObject := codexChatUsage(usage)
	if err := a.finishRequestChecked(modelRequestID, "succeeded", http.StatusOK); err != nil {
		writeCodexStreamError(w, modelRequestID, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	finalChunk := codexStreamChunk(modelRequestID, publicModel, created, map[string]any{}, stringPointer("stop"), usageObject)
	if err := writeSSEJSON(w, finalChunk); err != nil {
		a.finishRequest(modelRequestID, "cancelled", http.StatusOK)
		return
	}
	if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
		a.finishRequest(modelRequestID, "cancelled", http.StatusOK)
		return
	}
	flusher.Flush()
}

func (a *App) handleCodexRunFailure(w http.ResponseWriter, r *http.Request, selected route, modelRequestID string, runErr *codexRunError, streamCommitted bool) {
	a.handleCodexFailure(w, r, selected, modelRequestID, runErr, streamCommitted, writeCodexStreamError)
}

func (a *App) handleCodexFailure(w http.ResponseWriter, r *http.Request, selected route, modelRequestID string, runErr *codexRunError, streamCommitted bool, writeStreamError func(http.ResponseWriter, string, string, string)) {
	if codexCredentialNeedsReimport(runErr.Code) {
		if stateErr := a.markCodexReauthentication(selected.AccountID, selected.Revision); stateErr != nil {
			outcome := "failed"
			if streamCommitted {
				outcome = "interrupted"
			}
			a.finishRequest(modelRequestID, outcome, runErr.UpstreamStatus)
			if streamCommitted {
				writeStreamError(w, modelRequestID, "storage_unavailable", "Service is temporarily unavailable.")
			} else {
				writeModelError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.", modelRequestID)
			}
			return
		}
	}
	if r.Context().Err() != nil {
		a.finishRequest(modelRequestID, "cancelled", runErr.UpstreamStatus)
		return
	}
	outcome := "failed"
	if streamCommitted {
		outcome = "interrupted"
	}
	a.finishRequest(modelRequestID, outcome, runErr.UpstreamStatus)
	if streamCommitted {
		writeStreamError(w, modelRequestID, codexPublicErrorCode(runErr), "Upstream request failed.")
		return
	}
	a.writeCodexRunError(w, r, modelRequestID, runErr)
}

func (a *App) writeCodexRunError(w http.ResponseWriter, r *http.Request, requestID string, runErr *codexRunError) {
	if r.Context().Err() != nil {
		return
	}
	status, code, message := codexPublicError(runErr)
	if status == http.StatusTooManyRequests && runErr.RetryAfter > 0 && runErr.RetryAfter <= time.Hour {
		seconds := int64((runErr.RetryAfter + time.Second - 1) / time.Second)
		if seconds < 1 {
			seconds = 1
		}
		w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	}
	writeModelError(w, status, code, message, requestID)
}

func codexPublicError(runErr *codexRunError) (int, string, string) {
	switch runErr.Code {
	case membership.CodexErrorInvalidRequest:
		return http.StatusBadRequest, "invalid_request_error", "Invalid request."
	case membership.CodexErrorUnsupportedFeature:
		return http.StatusBadRequest, "unsupported_feature", "This request uses an unsupported feature."
	case membership.CodexErrorReauthentication, membership.CodexErrorAccessTokenInvalid, membership.CodexErrorAccountIDRequired:
		return http.StatusBadGateway, "upstream_reauthentication_required", "The upstream credential must be re-imported."
	case membership.CodexErrorRateLimited:
		return http.StatusTooManyRequests, "upstream_rate_limited", "Upstream rate limit was reached."
	case membership.CodexErrorUsageExhausted:
		return http.StatusTooManyRequests, "upstream_usage_exhausted", "Upstream usage is exhausted."
	case membership.CodexErrorAuthModeUnsupported:
		return http.StatusBadGateway, "upstream_auth_mode_unsupported", "The upstream account requires an unsupported authentication mode."
	case membership.CodexErrorTimeout:
		return http.StatusGatewayTimeout, "upstream_timeout", "Upstream request timed out."
	case membership.CodexErrorProtocol, membership.CodexErrorResponseTooLarge, membership.CodexErrorSSEFrameTooLarge:
		return http.StatusBadGateway, "upstream_protocol_error", "Upstream returned an invalid response."
	default:
		return http.StatusBadGateway, "upstream_error", "Upstream request failed."
	}
}

func codexPublicErrorCode(runErr *codexRunError) string {
	_, code, _ := codexPublicError(runErr)
	return code
}

func codexCredentialNeedsReimport(code membership.CodexAdapterErrorCode) bool {
	return code == membership.CodexErrorReauthentication || code == membership.CodexErrorAccessTokenInvalid || code == membership.CodexErrorAccountIDRequired
}

func writeCodexStreamError(w http.ResponseWriter, requestID, code, message string) {
	payload := map[string]any{"error": map[string]any{
		"message": message, "type": code, "code": code, "request_id": requestID,
	}}
	data, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (a *App) markCodexVerified(id string, revision int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := a.store.db.ExecContext(ctx, `UPDATE upstreams SET credential_state=?,verified_at=? WHERE id=? AND provider_kind=? AND revision=? AND credential_state=?`,
		codexStateVerified, utcNow(), id, codexMembershipProvider, revision, codexStateImported)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		var currentRevision int64
		var currentState string
		if queryErr := a.store.db.QueryRowContext(ctx, `SELECT revision,credential_state FROM upstreams WHERE id=? AND provider_kind=?`, id, codexMembershipProvider).Scan(&currentRevision, &currentState); queryErr != nil {
			return queryErr
		}
		if currentRevision == revision && currentState == codexStateVerified {
			return nil
		}
		return errors.New("Codex credential state changed during request")
	}
	return nil
}

func (a *App) markCodexReauthentication(id string, revision int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := a.store.db.ExecContext(ctx, `UPDATE upstreams SET credential_state=? WHERE id=? AND provider_kind=? AND revision=?`,
		codexStateReauth, id, codexMembershipProvider, revision)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err == nil && changed > 0 {
		a.notifyAccountPoolChanged()
	}
	return err
}

func codexChatUsage(usage membership.CodexUsage) map[string]any {
	result := make(map[string]any)
	if usage.InputTokens != nil {
		result["prompt_tokens"] = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		result["completion_tokens"] = *usage.OutputTokens
	}
	if usage.TotalTokens != nil {
		result["total_tokens"] = *usage.TotalTokens
	}
	if usage.CachedInputTokens != nil {
		result["prompt_tokens_details"] = map[string]any{"cached_tokens": *usage.CachedInputTokens}
	}
	if usage.ReasoningOutputTokens != nil {
		result["completion_tokens_details"] = map[string]any{"reasoning_tokens": *usage.ReasoningOutputTokens}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func codexResponsesUsage(usage membership.CodexUsage) map[string]any {
	result := make(map[string]any)
	if usage.InputTokens != nil {
		result["input_tokens"] = *usage.InputTokens
	}
	if usage.OutputTokens != nil {
		result["output_tokens"] = *usage.OutputTokens
	}
	if usage.TotalTokens != nil {
		result["total_tokens"] = *usage.TotalTokens
	}
	if usage.CachedInputTokens != nil {
		result["input_tokens_details"] = map[string]any{"cached_tokens": *usage.CachedInputTokens}
	}
	if usage.ReasoningOutputTokens != nil {
		result["output_tokens_details"] = map[string]any{"reasoning_tokens": *usage.ReasoningOutputTokens}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func codexUsageKnown(usage membership.CodexUsage) bool {
	return usage.InputTokens != nil || usage.CachedInputTokens != nil || usage.OutputTokens != nil || usage.ReasoningOutputTokens != nil || usage.TotalTokens != nil
}

func codexStreamChunk(requestID, model string, created int64, delta map[string]any, finishReason *string, usage map[string]any) map[string]any {
	chunk := map[string]any{
		"id": localChatCompletionID(requestID), "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finishReason}},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	return chunk
}

func writeSSEJSON(w http.ResponseWriter, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}

func localChatCompletionID(requestID string) string { return "chatcmpl_" + requestID }

func stringPointer(value string) *string { return &value }
