package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	anthropicAPIKeyProvider = "anthropic-api-key"
	anthropicMaxResponse    = 16 << 20
	anthropicMaxSSELine     = 1 << 20
	anthropicMaxSSEEvent    = 2 << 20
)

func (a *App) messages(w http.ResponseWriter, r *http.Request) {
	a.handleAnthropicRequest(w, r, false)
}

func (a *App) countMessageTokens(w http.ResponseWriter, r *http.Request) {
	a.handleAnthropicRequest(w, r, true)
}

func (a *App) handleAnthropicRequest(w http.ResponseWriter, r *http.Request, countTokens bool) {
	r.Body = http.MaxBytesReader(w, r.Body, modelMaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request.", requestID(r.Context()))
		return
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request.", requestID(r.Context()))
		return
	}
	var model string
	if raw, ok := payload["model"]; !ok || json.Unmarshal(raw, &model) != nil || !validIdentifier(model, 128) {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "A valid model is required.", requestID(r.Context()))
		return
	}
	stream := false
	if raw, ok := payload["stream"]; ok {
		if countTokens || json.Unmarshal(raw, &stream) != nil {
			writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "The stream field is not valid for this request.", requestID(r.Context()))
			return
		}
	}
	version := r.Header.Get("Anthropic-Version")
	if !validAnthropicVersion(version) {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "A valid anthropic-version header is required.", requestID(r.Context()))
		return
	}
	beta := r.Header.Get("Anthropic-Beta")
	if !validAnthropicBeta(beta) {
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "The anthropic-beta header is invalid.", requestID(r.Context()))
		return
	}

	auth, ok := a.authenticateAnthropicEmployee(w, r)
	if !ok {
		return
	}
	modelRequestID := requestID(r.Context())
	route, lease, failure := a.selectModelRoute(r, auth, model, []string{anthropicAPIKeyProvider}, !countTokens)
	if failure != nil {
		if r.Context().Err() != nil {
			return
		}
		writeAnthropicError(w, failure.status, anthropicAdmissionType(failure.status), failure.message, modelRequestID)
		return
	}
	if lease != nil {
		r = r.WithContext(lease.Context())
	}
	defer a.releaseModelLease(lease, modelRequestID, !countTokens)
	if route.KeyVersion != 1 {
		if !countTokens {
			a.finishRequest(modelRequestID, "failed", 0)
		}
		writeAnthropicError(w, 503, "api_error", "No available route for this model.", modelRequestID)
		return
	}

	credential, err := a.secrets.decryptCredential(route.AccountID, route.Ciphertext)
	if err != nil {
		if !countTokens {
			a.finishRequest(modelRequestID, "failed", 0)
		}
		writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "No available Anthropic route exists for this model.", modelRequestID)
		return
	}
	payload["model"], _ = json.Marshal(route.UpstreamModel)
	outgoing, err := json.Marshal(payload)
	if err != nil {
		if !countTokens {
			a.finishRequest(modelRequestID, "failed", 0)
		}
		writeAnthropicError(w, http.StatusBadRequest, "invalid_request_error", "Invalid request.", modelRequestID)
		return
	}
	endpoint, err := validateEndpoint(r.Context(), route.Endpoint, a.cfg.AllowLoopbackUpstream)
	if err != nil {
		if !countTokens {
			a.finishRequest(modelRequestID, "failed", 0)
		}
		writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "No available Anthropic route exists for this model.", modelRequestID)
		return
	}
	target, err := upstreamAnthropicURL(endpoint, countTokens)
	if err != nil {
		if !countTokens {
			a.finishRequest(modelRequestID, "failed", 0)
		}
		writeAnthropicError(w, http.StatusServiceUnavailable, "api_error", "No available Anthropic route exists for this model.", modelRequestID)
		return
	}
	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(outgoing))
	if err != nil {
		if !countTokens {
			a.finishRequest(modelRequestID, "failed", 0)
		}
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "Upstream is unavailable.", modelRequestID)
		return
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+credential)
	upstreamReq.Header.Set("Anthropic-Version", version)
	if beta != "" {
		upstreamReq.Header.Set("Anthropic-Beta", beta)
	}
	upstreamReq.Header.Set("Content-Type", "application/json")
	if stream {
		upstreamReq.Header.Set("Accept", "text/event-stream")
	} else {
		upstreamReq.Header.Set("Accept", "application/json")
	}
	response, err := a.http.Do(upstreamReq)
	if err != nil {
		if !countTokens {
			outcome := "failed"
			if errors.Is(r.Context().Err(), context.Canceled) {
				outcome = "cancelled"
			}
			a.finishRequest(modelRequestID, outcome, 0)
		}
		if r.Context().Err() == nil {
			writeAnthropicError(w, http.StatusBadGateway, "api_error", "Upstream is unavailable.", modelRequestID)
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if !countTokens {
			a.finishRequest(modelRequestID, "failed", response.StatusCode)
		}
		if retry := safeRetryAfter(response.Header.Get("Retry-After")); retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		writeAnthropicUpstreamError(w, response.StatusCode, modelRequestID)
		return
	}
	if stream {
		a.forwardAnthropicStream(w, r, response, modelRequestID)
		return
	}
	a.forwardAnthropicJSON(w, r, response, modelRequestID, !countTokens)
}

func (a *App) authenticateAnthropicEmployee(w http.ResponseWriter, r *http.Request) (employeeAuth, bool) {
	authorizations := r.Header.Values("Authorization")
	apiKeys := r.Header.Values("X-API-Key")
	var key string
	switch {
	case len(authorizations) == 1 && len(apiKeys) == 0:
		parts := strings.Fields(authorizations[0])
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			key = parts[1]
		}
	case len(authorizations) == 0 && len(apiKeys) == 1:
		key = strings.TrimSpace(apiKeys[0])
	}
	auth, valid := a.lookupEmployeeKey(r.Context(), key)
	if !valid {
		writeAnthropicError(w, http.StatusUnauthorized, "authentication_error", "Invalid API key.", requestID(r.Context()))
		return employeeAuth{}, false
	}
	return auth, true
}

func validAnthropicVersion(value string) bool {
	if len(value) != 10 || value[4] != '-' || value[7] != '-' {
		return false
	}
	for i, char := range value {
		if i == 4 || i == 7 {
			continue
		}
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func validAnthropicBeta(value string) bool {
	if len(value) > 1024 {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char > 0x7e {
			return false
		}
	}
	return true
}

func upstreamAnthropicURL(endpoint string, countTokens bool) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		path += "/messages"
	} else {
		path += "/v1/messages"
	}
	if countTokens {
		path += "/count_tokens"
	}
	u.Path = path
	u.RawPath = ""
	return u.String(), nil
}

func writeAnthropicError(w http.ResponseWriter, status int, kind, message, reqID string) {
	writeJSON(w, status, map[string]any{
		"type":       "error",
		"error":      map[string]string{"type": kind, "message": message},
		"request_id": reqID,
	})
}

func writeAnthropicUpstreamError(w http.ResponseWriter, upstreamStatus int, reqID string) {
	switch upstreamStatus {
	case http.StatusTooManyRequests:
		writeAnthropicError(w, http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit was reached.", reqID)
	case 529:
		writeAnthropicError(w, 529, "overloaded_error", "Upstream is overloaded.", reqID)
	case http.StatusUnauthorized, http.StatusForbidden:
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "Upstream authentication failed.", reqID)
	default:
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "Upstream request failed.", reqID)
	}
}

func (a *App) forwardAnthropicJSON(w http.ResponseWriter, r *http.Request, response *http.Response, reqID string, record bool) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		if record {
			a.finishRequest(reqID, "failed", response.StatusCode)
		}
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "Upstream returned an invalid response.", reqID)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, anthropicMaxResponse+1))
	if err != nil || len(body) > anthropicMaxResponse || !json.Valid(body) {
		if record {
			a.finishRequest(reqID, "failed", response.StatusCode)
		}
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "Upstream returned an invalid response.", reqID)
		return
	}
	if r.Context().Err() != nil {
		if record {
			a.finishRequest(reqID, "cancelled", response.StatusCode)
		}
		return
	}
	if record {
		a.finishRequest(reqID, "succeeded", response.StatusCode)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (a *App) forwardAnthropicStream(w http.ResponseWriter, r *http.Request, response *http.Response, reqID string) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		a.finishRequest(reqID, "failed", response.StatusCode)
		writeAnthropicError(w, http.StatusBadGateway, "api_error", "Upstream returned an invalid response.", reqID)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		a.finishRequest(reqID, "failed", response.StatusCode)
		writeAnthropicError(w, http.StatusInternalServerError, "api_error", "Streaming is unavailable.", reqID)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	limited := &io.LimitedReader{R: response.Body, N: anthropicMaxResponse + 1}
	reader := bufio.NewReaderSize(limited, 64<<10)
	outcome := "interrupted"
	var frame anthropicSSEFrame
	for {
		line, err := readBoundedSSELine(reader, anthropicMaxSSELine)
		if limited.N <= 0 {
			outcome = "failed"
			break
		}
		if len(line) > 0 {
			if frame.size+len(line) > anthropicMaxSSEEvent {
				outcome = "failed"
				break
			}
			frame.size += len(line)
			frame.raw.Write(line)
			trimmed := strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r")
			if trimmed == "" {
				kind, frameErr := frame.validate()
				if frameErr != nil {
					outcome = "failed"
					break
				}
				var outgoing []byte
				if kind == "error" {
					outgoing = anthropicRedactedSSEError(reqID)
				} else {
					outgoing = bytes.Clone(frame.raw.Bytes())
				}
				if len(outgoing) > 0 {
					if _, writeErr := w.Write(outgoing); writeErr != nil {
						outcome = "cancelled"
						break
					}
					flusher.Flush()
				}
				frame.reset()
				if kind == "error" {
					outcome = "failed"
					break
				}
				if kind == "message_stop" {
					outcome = "succeeded"
					break
				}
				continue
			}
			if parseErr := frame.addLine(trimmed); parseErr != nil {
				outcome = "failed"
				break
			}
		}
		if err != nil {
			if r.Context().Err() != nil {
				outcome = "cancelled"
			} else if limited.N <= 0 || !errors.Is(err, io.EOF) {
				outcome = "failed"
			} else if frame.size != 0 {
				// A final event without its blank-line delimiter is incomplete and
				// is never forwarded or treated as a terminal event.
				outcome = "failed"
			}
			break
		}
	}
	a.finishRequest(reqID, outcome, response.StatusCode)
}

type anthropicSSEFrame struct {
	raw       bytes.Buffer
	data      bytes.Buffer
	eventName string
	size      int
}

func (f *anthropicSSEFrame) addLine(line string) error {
	if line == "" || strings.HasPrefix(line, ":") {
		return nil
	}
	field, value, found := strings.Cut(line, ":")
	if !found {
		field, value = line, ""
	} else if strings.HasPrefix(value, " ") {
		value = value[1:]
	}
	switch field {
	case "event":
		if f.eventName != "" || value == "" {
			return errors.New("invalid SSE event name")
		}
		f.eventName = value
	case "data":
		if f.data.Len()+len(value)+1 > anthropicMaxSSEEvent {
			return errors.New("SSE data exceeds limit")
		}
		f.data.WriteString(value)
		f.data.WriteByte('\n')
	case "id", "retry":
		// Preserve standard SSE metadata without interpreting it.
	default:
		return errors.New("invalid SSE field")
	}
	return nil
}

func (f *anthropicSSEFrame) validate() (string, error) {
	if f.size == 0 {
		return "", nil
	}
	if f.eventName == "" && f.data.Len() == 0 {
		return "", nil
	}
	if f.eventName == "" || f.data.Len() == 0 {
		return "", errors.New("incomplete SSE event")
	}
	raw := bytes.TrimSuffix(f.data.Bytes(), []byte("\n"))
	var envelope struct {
		Type string `json:"type"`
	}
	if !json.Valid(raw) || json.Unmarshal(raw, &envelope) != nil || envelope.Type == "" || envelope.Type != f.eventName {
		return "", errors.New("invalid SSE data event")
	}
	return envelope.Type, nil
}

func (f *anthropicSSEFrame) reset() {
	f.raw.Reset()
	f.data.Reset()
	f.eventName = ""
	f.size = 0
}

func anthropicRedactedSSEError(reqID string) []byte {
	payload, _ := json.Marshal(map[string]any{
		"type":       "error",
		"error":      map[string]string{"type": "api_error", "message": "Upstream request failed."},
		"request_id": reqID,
	})
	return []byte("event: error\ndata: " + string(payload) + "\n\n")
}

func readBoundedSSELine(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(line)+len(fragment) > limit {
			return nil, errors.New("SSE line exceeds limit")
		}
		line = append(line, fragment...)
		if !errors.Is(err, bufio.ErrBufferFull) {
			return line, err
		}
	}
}
