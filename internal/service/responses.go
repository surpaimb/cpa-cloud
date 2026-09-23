package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"cpacloud.local/server/internal/membership"
)

const (
	responsesMaxResponse = 16 << 20
	responsesMaxEvent    = 1 << 20
)

type codexResponsesExecutor interface {
	Responses(context.Context, *membership.CodexAuthCredential, []byte, func(json.RawMessage) error) (json.RawMessage, *codexRunError)
}

type productionCodexResponsesExecutor struct {
	adapter *membership.CodexDirectAdapter
}

func newProductionCodexResponsesExecutor() codexResponsesExecutor {
	return &productionCodexResponsesExecutor{adapter: membership.NewCodexDirectAdapter()}
}

func (e *productionCodexResponsesExecutor) Responses(ctx context.Context, credential *membership.CodexAuthCredential, body []byte, consume func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
	result, err := e.adapter.Responses(ctx, credential, body, consume)
	return result, normalizeCodexRunError(err)
}

func (a *App) responsesAPI(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, modelMaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeModelError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request.", requestID(r.Context()))
		return
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		writeModelError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request.", requestID(r.Context()))
		return
	}
	var model string
	if raw, ok := payload["model"]; !ok || json.Unmarshal(raw, &model) != nil || !validIdentifier(model, 128) {
		writeModelError(w, http.StatusBadRequest, "invalid_request_error", "A valid model is required.", requestID(r.Context()))
		return
	}
	stream := false
	if raw, ok := payload["stream"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &stream) != nil {
			writeModelError(w, http.StatusBadRequest, "invalid_request_error", "The stream field must be boolean.", requestID(r.Context()))
			return
		}
	}
	if rejectsResponsesLifecycle(payload) {
		writeModelError(w, http.StatusBadRequest, "unsupported_feature", "This request uses an unsupported Responses lifecycle feature.", requestID(r.Context()))
		return
	}
	if _, present := payload["store"]; !present {
		payload["store"] = json.RawMessage("false")
	}

	auth, ok := a.authenticateEmployee(w, r)
	if !ok {
		return
	}
	reqID := requestID(r.Context())
	selected, lease, failure := a.selectModelRoute(r, auth, model, []string{"openai-compatible", codexMembershipProvider}, true)
	if failure != nil {
		if r.Context().Err() != nil {
			return
		}
		writeModelError(w, failure.status, failure.code, failure.message, reqID)
		return
	}
	if lease != nil {
		r = r.WithContext(lease.Context())
	}
	defer a.releaseModelLease(lease, reqID, true)

	payload["model"], _ = json.Marshal(selected.UpstreamModel)
	outgoing, err := json.Marshal(payload)
	if err != nil {
		a.finishRequest(reqID, "failed", 0)
		writeModelError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request.", reqID)
		return
	}
	if selected.ProviderKind == codexMembershipProvider {
		a.handleCodexResponses(w, r, outgoing, stream, selected, reqID)
		return
	}
	a.handleAPIKeyResponses(w, r, outgoing, stream, selected, reqID)
}

func rejectsResponsesLifecycle(payload map[string]json.RawMessage) bool {
	if raw, ok := payload["background"]; ok {
		var enabled bool
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &enabled) != nil || enabled {
			return true
		}
	}
	if raw, ok := payload["store"]; ok {
		var enabled bool
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &enabled) != nil || enabled {
			return true
		}
	}
	for _, field := range []string{"previous_response_id", "conversation"} {
		if raw, ok := payload[field]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return true
		}
	}
	return false
}

func (a *App) handleAPIKeyResponses(w http.ResponseWriter, r *http.Request, body []byte, stream bool, selected route, reqID string) {
	if selected.ProviderKind != "openai-compatible" || selected.KeyVersion != 1 {
		a.finishRequest(reqID, "failed", 0)
		writeModelError(w, http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", reqID)
		return
	}
	credential, err := a.secrets.decryptCredential(selected.AccountID, selected.Ciphertext)
	if err != nil {
		a.finishRequest(reqID, "failed", 0)
		writeModelError(w, http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", reqID)
		return
	}
	endpoint, err := validateEndpoint(r.Context(), selected.Endpoint, a.cfg.AllowLoopbackUpstream)
	if err != nil {
		a.finishRequest(reqID, "failed", 0)
		writeModelError(w, 503, "no_available_route", "No available route for this model.", reqID)
		return
	}
	target, err := upstreamResponsesURL(endpoint)
	if err != nil {
		a.finishRequest(reqID, "failed", 0)
		writeModelError(w, 503, "no_available_route", "No available route for this model.", reqID)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		a.finishRequest(reqID, "failed", 0)
		writeModelError(w, 502, "upstream_unavailable", "Upstream is unavailable.", reqID)
		return
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	response, err := a.http.Do(req)
	if err != nil {
		outcome := "failed"
		status := http.StatusBadGateway
		code := "upstream_unavailable"
		if r.Context().Err() != nil {
			outcome = "cancelled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
			code = "upstream_timeout"
		}
		a.finishRequest(reqID, outcome, 0)
		if r.Context().Err() == nil {
			writeModelError(w, status, code, map[bool]string{true: "Upstream request timed out.", false: "Upstream is unavailable."}[status == http.StatusGatewayTimeout], reqID)
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		a.finishRequest(reqID, "failed", response.StatusCode)
		if retry := safeRetryAfter(response.Header.Get("Retry-After")); retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		status, code := http.StatusBadGateway, "upstream_error"
		if response.StatusCode == http.StatusTooManyRequests {
			status, code = http.StatusTooManyRequests, "upstream_rate_limited"
		}
		writeModelError(w, status, code, "Upstream request failed.", reqID)
		return
	}
	if stream {
		a.forwardResponsesStream(w, r, response, reqID)
		return
	}
	a.forwardResponsesJSON(w, response, reqID)
}

func (a *App) forwardResponsesJSON(w http.ResponseWriter, response *http.Response, reqID string) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		a.responsesProtocolError(w, reqID, response.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, responsesMaxResponse+1))
	if err != nil || len(body) > responsesMaxResponse || validateCompletedResponse(body) != nil {
		a.responsesProtocolError(w, reqID, response.StatusCode)
		return
	}
	if err := a.finishRequestChecked(reqID, "succeeded", response.StatusCode); err != nil {
		writeModelError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.", reqID)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func validateCompletedResponse(raw []byte) error {
	var envelope struct {
		ID     string          `json:"id"`
		Object string          `json:"object"`
		Status string          `json:"status"`
		Output json.RawMessage `json:"output"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.ID == "" || envelope.Object != "response" || envelope.Status != "completed" {
		return errors.New("invalid response")
	}
	var output []json.RawMessage
	if len(envelope.Output) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Output), []byte("null")) || json.Unmarshal(envelope.Output, &output) != nil {
		return errors.New("invalid response output")
	}
	return nil
}

func validateCompletedEvent(raw []byte) error {
	var event struct {
		Type     string          `json:"type"`
		Response json.RawMessage `json:"response"`
	}
	if json.Unmarshal(raw, &event) != nil || event.Type != "response.completed" || validateCompletedResponse(event.Response) != nil {
		return errors.New("invalid completed event")
	}
	return nil
}

func (a *App) responsesProtocolError(w http.ResponseWriter, reqID string, upstreamStatus int) {
	a.finishRequest(reqID, "failed", upstreamStatus)
	writeModelError(w, http.StatusBadGateway, "upstream_protocol_error", "Upstream returned an invalid response.", reqID)
}

type responsesSSEEvent struct {
	name string
	data json.RawMessage
	kind string
}

var errResponsesCompleted = errors.New("Responses stream completed")

func readResponsesSSE(ctx context.Context, reader io.Reader, consume func(responsesSSEEvent) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), responsesMaxEvent+1)
	var name string
	var data []byte
	total := 0
	flush := func() error {
		if len(data) == 0 {
			name = ""
			return nil
		}
		joined := bytes.TrimSuffix(data, []byte("\n"))
		var header struct {
			Type string `json:"type"`
		}
		if len(joined) > responsesMaxEvent || json.Unmarshal(joined, &header) != nil || !validResponsesEventName(header.Type) || (name != "" && name != header.Type) {
			return errors.New("invalid SSE event")
		}
		event := responsesSSEEvent{name: name, data: append(json.RawMessage(nil), joined...), kind: header.Type}
		name, data = "", data[:0]
		return consume(event)
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Text()
		total += len(line) + 1
		if total > responsesMaxResponse {
			return errors.New("SSE response too large")
		}
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			if flushErr := flush(); flushErr != nil {
				return flushErr
			}
		} else if strings.HasPrefix(line, "event:") {
			name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		} else if strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data:")
			if strings.HasPrefix(value, " ") {
				value = value[1:]
			}
			data = append(data, value...)
			data = append(data, '\n')
			if len(data) > responsesMaxEvent {
				return errors.New("SSE event too large")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}

func validResponsesEventName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func (a *App) forwardResponsesStream(w http.ResponseWriter, r *http.Request, response *http.Response, reqID string) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		a.responsesProtocolError(w, reqID, response.StatusCode)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		a.finishRequest(reqID, "failed", response.StatusCode)
		writeModelError(w, 500, "streaming_unavailable", "Streaming is unavailable.", reqID)
		return
	}
	committed := false
	completed := false
	var terminal responsesSSEEvent
	start := func() {
		if !committed {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache, no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			committed = true
		}
	}
	err := readResponsesSSE(r.Context(), response.Body, func(event responsesSSEEvent) error {
		switch event.kind {
		case "response.completed":
			if validateCompletedEvent(event.data) != nil {
				return errors.New("invalid completed event")
			}
			terminal, completed = event, true
			return errResponsesCompleted
		case "response.failed", "response.incomplete", "error":
			return errors.New("terminal upstream failure")
		default:
			start()
			if err := writeResponsesSSE(w, event); err != nil {
				return err
			}
			flusher.Flush()
			return nil
		}
	})
	if (!errors.Is(err, errResponsesCompleted) && err != nil) || !completed {
		outcome := "interrupted"
		if r.Context().Err() != nil {
			outcome = "cancelled"
		}
		a.finishRequest(reqID, outcome, response.StatusCode)
		if r.Context().Err() == nil {
			if committed {
				writeResponsesStreamError(w, reqID, "upstream_protocol_error", "Upstream request failed.")
			} else {
				writeModelError(w, http.StatusBadGateway, "upstream_protocol_error", "Upstream request failed.", reqID)
			}
		}
		return
	}
	if err := a.finishRequestChecked(reqID, "succeeded", response.StatusCode); err != nil {
		if committed {
			writeResponsesStreamError(w, reqID, "storage_unavailable", "Service is temporarily unavailable.")
		} else {
			writeModelError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.", reqID)
		}
		return
	}
	start()
	if err := writeResponsesSSE(w, terminal); err != nil {
		return
	}
	flusher.Flush()
}

func writeResponsesStreamError(w http.ResponseWriter, requestID, code, message string) {
	payload := map[string]any{"type": "error", "code": code, "message": message, "error": map[string]any{
		"message": message, "type": code, "code": code, "request_id": requestID,
	}}
	_ = writeResponsesSSE(w, responsesSSEEvent{kind: "error", data: mustMarshalJSON(payload)})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func mustMarshalJSON(value any) json.RawMessage { data, _ := json.Marshal(value); return data }

func writeResponsesSSE(w io.Writer, event responsesSSEEvent) error {
	var compact bytes.Buffer
	if err := json.Compact(&compact, event.data); err != nil {
		return err
	}
	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.kind, compact.Bytes())
	return err
}

func (a *App) handleCodexResponses(w http.ResponseWriter, r *http.Request, body []byte, stream bool, selected route, reqID string) {
	if !a.cfg.ExperimentalCodexMembership {
		a.finishRequest(reqID, "failed", 0)
		writeModelError(w, 503, "no_available_route", "No available route for this model.", reqID)
		return
	}
	if selected.KeyVersion != 2 || !selected.CredentialState.Valid || selected.CredentialState.String == codexStateReauth {
		a.finishRequest(reqID, "failed", 0)
		writeModelError(w, 502, "upstream_reauthentication_required", "The upstream credential must be re-imported.", reqID)
		return
	}
	if selected.CredentialState.String != codexStateImported && selected.CredentialState.String != codexStateVerified {
		a.finishRequest(reqID, "failed", 0)
		writeModelError(w, 503, "no_available_route", "No available route for this model.", reqID)
		return
	}
	selected, credential, runErr := a.acquireCodexCredential(r.Context(), selected)
	if runErr != nil {
		a.handleCodexFailure(w, r, selected, reqID, runErr, false, writeResponsesStreamError)
		return
	}
	defer credential.Destroy()
	if !stream {
		result, runErr := a.responses.Responses(r.Context(), credential, body, nil)
		if runErr != nil {
			a.handleCodexFailure(w, r, selected, reqID, runErr, false, writeResponsesStreamError)
			return
		}
		if validateCompletedResponse(result) != nil {
			a.responsesProtocolError(w, reqID, http.StatusOK)
			return
		}
		if err := a.markCodexVerified(selected.AccountID, selected.Revision); err != nil {
			a.finishRequest(reqID, "failed", 200)
			writeModelError(w, 503, "storage_unavailable", "Service is temporarily unavailable.", reqID)
			return
		}
		if err := a.finishRequestChecked(reqID, "succeeded", 200); err != nil {
			writeModelError(w, 503, "storage_unavailable", "Service is temporarily unavailable.", reqID)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		_, _ = w.Write(result)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		a.finishRequest(reqID, "failed", 0)
		writeModelError(w, 500, "streaming_unavailable", "Streaming is unavailable.", reqID)
		return
	}
	committed := false
	var completed json.RawMessage
	start := func() {
		if !committed {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache, no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(200)
			committed = true
		}
	}
	_, runErr = a.responses.Responses(r.Context(), credential, body, func(event json.RawMessage) error {
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(event, &head) != nil || !validResponsesEventName(head.Type) {
			return errors.New("invalid Codex event")
		}
		if head.Type == "response.completed" {
			if validateCompletedEvent(event) != nil {
				return errors.New("invalid completed event")
			}
			completed = append(completed[:0], event...)
			return nil
		}
		if head.Type == "response.failed" || head.Type == "response.incomplete" || head.Type == "error" {
			return errors.New("terminal Codex event")
		}
		start()
		if err := writeResponsesSSE(w, responsesSSEEvent{kind: head.Type, data: event}); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	})
	if runErr != nil {
		a.handleCodexFailure(w, r, selected, reqID, runErr, committed, writeResponsesStreamError)
		return
	}
	if len(completed) == 0 {
		a.finishRequest(reqID, "interrupted", 200)
		if committed {
			writeResponsesStreamError(w, reqID, "upstream_protocol_error", "Upstream returned an invalid response.")
		} else {
			writeModelError(w, 502, "upstream_protocol_error", "Upstream returned an invalid response.", reqID)
		}
		return
	}
	if err := a.markCodexVerified(selected.AccountID, selected.Revision); err != nil {
		a.finishRequest(reqID, "failed", 200)
		if committed {
			writeResponsesStreamError(w, reqID, "storage_unavailable", "Service is temporarily unavailable.")
		} else {
			writeModelError(w, 503, "storage_unavailable", "Service is temporarily unavailable.", reqID)
		}
		return
	}
	if err := a.finishRequestChecked(reqID, "succeeded", 200); err != nil {
		if committed {
			writeResponsesStreamError(w, reqID, "storage_unavailable", "Service is temporarily unavailable.")
		} else {
			writeModelError(w, 503, "storage_unavailable", "Service is temporarily unavailable.", reqID)
		}
		return
	}
	start()
	if err := writeResponsesSSE(w, responsesSSEEvent{kind: "response.completed", data: completed}); err != nil {
		return
	}
	flusher.Flush()
}

func upstreamResponsesURL(endpoint string) (string, error) {
	// Kept next to the handler for now; the URL policy itself is shared with Chat.
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		path += "/responses"
	} else {
		path += "/v1/responses"
	}
	u.Path, u.RawPath = path, ""
	return u.String(), nil
}
