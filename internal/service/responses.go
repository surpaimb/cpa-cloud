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
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/keypolicy"
	"cpacloud.local/server/internal/membership"
	"cpacloud.local/server/internal/protocolconv"
	"cpacloud.local/server/internal/scheduling"
)

const (
	responsesMaxResponse = 16 << 20
	responsesMaxEvent    = 1 << 20
	responsesHeartbeat   = time.Second
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
	lifecycle, lifecycleErr := parseResponsesLifecycle(payload, a.cfg, stream)
	if lifecycleErr != nil {
		code, message := "unsupported_feature", "This request uses a disabled or unsupported Responses lifecycle feature."
		if errors.Is(lifecycleErr, errResponseResourceInvalid) && a.cfg.ResponsesStatefulResources {
			code, message = "invalid_request_error", "Invalid Responses lifecycle request."
		}
		writeModelError(w, http.StatusBadRequest, code, message, requestID(r.Context()))
		return
	}
	if _, present := payload["store"]; !present && !lifecycle.store {
		payload["store"] = json.RawMessage("false")
	}
	r = withUsageStreamEvidence(r, stream)

	auth, ok := a.authenticateEmployee(w, r)
	if !ok {
		return
	}
	auth, sourceFailure := authorizeKeySource(auth, r.RemoteAddr)
	if sourceFailure != nil {
		writeModelError(w, sourceFailure.status, sourceFailure.code, sourceFailure.message, requestID(r.Context()))
		return
	}
	auth, policyFailure := authorizeKeyPolicy(auth, keypolicy.ProtocolOpenAIResponses, model)
	if policyFailure != nil {
		writeModelError(w, policyFailure.status, policyFailure.code, policyFailure.message, requestID(r.Context()))
		return
	}
	var persistence *responsePersistencePlan
	if lifecycle.store || lifecycle.previousID != "" {
		persistence, err = prepareResponsePersistence(r.Context(), a.responseResources, auth, model, requestID(r.Context()), payload, lifecycle, time.Now().UTC())
		if err != nil {
			writeResponseResourceError(w, r, err)
			return
		}
	}
	if lifecycle.background {
		providerKind, providerErr := a.backgroundResponseProvider(r.Context(), model)
		if providerErr != nil {
			writeModelError(w, http.StatusBadRequest, "unsupported_feature", "Background execution requires one direct supported route.", requestID(r.Context()))
			return
		}
		view, createErr := a.responseResources.Create(r.Context(), responseResourceCreateInput{
			OperationID: requestID(r.Context()), EmployeeID: auth.EmployeeID, KeyID: auth.KeyID,
			PublicModel: model, ParentResponseID: lifecycle.previousID, ProviderKind: providerKind,
			SourceAddr: auth.SourceAddr, PolicyRevision: auth.Policy.Revision,
			Background: true, StoreBody: true, Items: persistence.items, CreatedAt: persistence.createdAt,
		})
		if createErr != nil {
			writeResponseResourceError(w, r, createErr)
			return
		}
		if a.backgroundResponses != nil {
			a.backgroundResponses.Wake()
		}
		writeJSON(w, http.StatusAccepted, responseResourcePayload(view))
		return
	}
	if failure := a.validatePoolSession(r, auth, model); failure != nil {
		writeModelError(w, failure.status, failure.code, failure.message, requestID(r.Context()))
		return
	}
	if stream {
		if _, ok := w.(http.Flusher); !ok {
			writeModelError(w, http.StatusInternalServerError, "streaming_unavailable", "Streaming is unavailable.", requestID(r.Context()))
			return
		}
	}
	reqID := requestID(r.Context())
	governed, guard, governanceFailure := a.admitGovernedModel(r, auth, model, accounting.ProtocolOpenAIResponses)
	if governanceFailure != nil {
		if r.Context().Err() == nil {
			writeModelError(w, governanceFailure.status, governanceFailure.code, governanceFailure.message, reqID)
		}
		return
	}
	r = governed
	defer guard.Close()
	var upstreamReq *http.Request
	var codexPrepared *codexResponsesPreflight
	var conversion *protocolRuntime
	selected, lease, failure := a.prepareModelRoute(r, auth, model, []string{"openai-compatible", codexMembershipProvider, anthropicAPIKeyProvider, geminiAPIKeyProvider}, accounting.ProtocolOpenAIResponses, true, func(candidateRequest *http.Request, candidate route) (route, *modelPreflightError) {
		candidatePayload := make(map[string]json.RawMessage, len(payload))
		for key, value := range payload {
			candidatePayload[key] = value
		}
		candidatePayload["model"], _ = json.Marshal(candidate.UpstreamModel)
		outgoing, err := json.Marshal(candidatePayload)
		if err != nil {
			return route{}, requestPreflightFailure(http.StatusBadRequest, "invalid_request_error", "Invalid request.")
		}
		if candidate.ProviderKind == codexMembershipProvider {
			prepared, failed := a.prepareCodexResponses(candidateRequest.Context(), outgoing, candidate)
			if failed != nil {
				return route{}, failed
			}
			codexPrepared = prepared
			return prepared.selected, nil
		}
		if candidate.ProviderKind == geminiAPIKeyProvider && candidate.KeyVersion != 2 || candidate.ProviderKind != geminiAPIKeyProvider && candidate.KeyVersion != 1 {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
		}
		var credential string
		if candidate.ProviderKind == geminiAPIKeyProvider {
			credential, err = a.secrets.decryptGeminiAPIKey(candidate.AccountID, candidate.Ciphertext)
		} else {
			credential, err = a.secrets.decryptCredential(candidate.AccountID, candidate.Ciphertext)
		}
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailureAuth)
		}
		var endpoint string
		if candidate.ProviderKind == geminiAPIKeyProvider {
			endpoint, err = validateGeminiEndpoint(candidateRequest.Context(), candidate.Endpoint, a.cfg.AllowLoopbackUpstream)
		} else {
			endpoint, err = validateEndpoint(candidateRequest.Context(), candidate.Endpoint, a.cfg.AllowLoopbackUpstream)
		}
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
		}
		capability, err := routeCapability(candidate, accounting.ProtocolOpenAIResponses, stream)
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
		}
		preparedRuntime, err := prepareProtocolRuntime(capability, candidate.UpstreamModel, outgoing)
		if err != nil {
			if protocolconv.IsUnsupportedRoute(err) {
				return route{}, requestPreflightFailure(http.StatusBadRequest, "unsupported_feature", "This request cannot be represented by the selected route.")
			}
			return route{}, requestPreflightFailure(http.StatusBadRequest, "invalid_request_error", "Invalid request.")
		}
		if preparedRuntime.plan().Kind != protocolconv.PlanNative && rejectsResponsesLifecycle(candidatePayload) {
			return route{}, requestPreflightFailure(http.StatusBadRequest, "unsupported_feature", "Stateful Responses features require a native Responses route.")
		}
		var target string
		switch preparedRuntime.plan().UpstreamProtocol {
		case protocolconv.ProtocolOpenAIResponses:
			target, err = upstreamResponsesURL(endpoint)
		case protocolconv.ProtocolOpenAIChat:
			target, err = upstreamChatURL(endpoint)
		case protocolconv.ProtocolAnthropicMessages:
			target, err = upstreamAnthropicURL(endpoint, false)
		case protocolconv.ProtocolGeminiGenerate:
			target, err = geminiGenerateURL(endpoint, preparedRuntime.model(), false)
		default:
			err = errors.New("unsupported OpenAI wire protocol")
		}
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
		}
		prepared, err := preparedRuntime.newRequest(candidateRequest.Context(), http.MethodPost, target, http.Header{})
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusBadGateway, "upstream_unavailable", "Upstream is unavailable.", scheduling.FailurePermanent)
		}
		switch preparedRuntime.plan().UpstreamProtocol {
		case protocolconv.ProtocolAnthropicMessages:
			prepared.Header.Set("Authorization", "Bearer "+credential)
			prepared.Header.Set("Anthropic-Version", "2023-06-01")
		case protocolconv.ProtocolGeminiGenerate:
			prepared.Header.Set("x-goog-api-key", credential)
		default:
			prepared.Header.Set("Authorization", "Bearer "+credential)
		}
		prepared.Header.Set("Content-Type", "application/json")
		if stream {
			prepared.Header.Set("Accept", "text/event-stream")
		} else {
			prepared.Header.Set("Accept", "application/json")
		}
		upstreamReq = prepared
		if preparedRuntime.plan().Kind != protocolconv.PlanNative {
			conversion = preparedRuntime
		}
		return candidate, nil
	})
	if failure != nil {
		if codexPrepared != nil {
			codexPrepared.Destroy()
		}
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
	if codexPrepared != nil {
		defer codexPrepared.Destroy()
	}
	client, dispatchFailure := a.dispatchModelRoute(r, auth, model, selected, lease, true, upstreamReq)
	if dispatchFailure != nil {
		if a.finishDispatchFailure(r, reqID, true) {
			writeModelError(w, dispatchFailure.status, dispatchFailure.code, dispatchFailure.message, reqID)
		}
		return
	}
	var persist func([]byte) ([]byte, error)
	if lifecycle.store {
		persist = func(body []byte) ([]byte, error) {
			writeCtx, cancel := durableResponseWriteContext(r.Context())
			defer cancel()
			return persistence.persistCompleted(writeCtx, selected.ProviderKind, body)
		}
	}
	if codexPrepared != nil {
		a.handleCodexResponses(w, r, stream, codexPrepared, reqID, persist)
		return
	}
	if conversion != nil {
		if stream {
			a.handleConvertedModelSSE(w, r, conversion, upstreamReq, client, reqID)
		} else {
			a.handleConvertedModelJSON(w, r, conversion, upstreamReq, client, reqID, responsesMaxResponse, persist)
		}
		return
	}
	a.handleAPIKeyResponses(w, r, upstreamReq, stream, reqID, client, persist)
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

func (a *App) handleAPIKeyResponses(w http.ResponseWriter, r *http.Request, req *http.Request, stream bool, reqID string, client upstreamHTTPDoer, persist func([]byte) ([]byte, error)) {
	response, err := client.Do(req)
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
	a.forwardResponsesJSON(w, r, response, reqID, persist)
}

func (a *App) forwardResponsesJSON(w http.ResponseWriter, r *http.Request, response *http.Response, reqID string, persist func([]byte) ([]byte, error)) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		a.responsesProtocolError(w, reqID, response.StatusCode)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, responsesMaxResponse+1))
	if err != nil || len(body) > responsesMaxResponse || validateCompletedResponse(body) != nil {
		a.responsesProtocolError(w, reqID, response.StatusCode)
		return
	}
	if persist != nil {
		body, err = persist(body)
		if err != nil {
			a.finishRequest(reqID, "failed", response.StatusCode)
			writeModelError(w, http.StatusServiceUnavailable, "storage_unavailable", "Response storage is temporarily unavailable.", reqID)
			return
		}
	}
	a.observeRequestUsage(reqID, body)
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

type responsesSSEDelivery struct {
	event responsesSSEEvent
	ack   chan error
}

func readResponsesSSEAsync(ctx context.Context, reader io.Reader) (<-chan responsesSSEDelivery, <-chan error) {
	events := make(chan responsesSSEDelivery)
	done := make(chan error, 1)
	go func() {
		done <- readResponsesSSE(ctx, reader, func(event responsesSSEEvent) error {
			delivery := responsesSSEDelivery{event: event, ack: make(chan error, 1)}
			select {
			case events <- delivery:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case err := <-delivery.ack:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	return events, done
}

var (
	errResponsesCompleted  = errors.New("Responses stream completed")
	errResponsesFailed     = errors.New("Responses stream failed")
	errResponsesIncomplete = errors.New("Responses stream incomplete")
	errResponsesDownstream = errors.New("Responses downstream disconnected")
)

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
	streamContext, cancelStream := context.WithCancel(r.Context())
	defer cancelStream()
	stopBodyClose := context.AfterFunc(streamContext, func() { _ = response.Body.Close() })
	defer stopBodyClose()
	heartbeat := time.NewTicker(responsesHeartbeat)
	defer heartbeat.Stop()
	start := func() {
		if !committed {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache, no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(http.StatusOK)
			committed = true
		}
	}
	consume := func(event responsesSSEEvent) error {
		switch event.kind {
		case "response.completed":
			if validateCompletedEvent(event.data) != nil {
				return errors.New("invalid completed event")
			}
			terminal, completed = event, true
			return errResponsesCompleted
		case "response.failed", "error":
			return errResponsesFailed
		case "response.incomplete":
			a.observeRequestUsage(reqID, event.data)
			return errResponsesIncomplete
		default:
			start()
			if err := writeResponsesSSE(w, event); err != nil {
				return fmt.Errorf("%w: %v", errResponsesDownstream, err)
			}
			flusher.Flush()
			return nil
		}
	}
	events, readDone := readResponsesSSEAsync(streamContext, response.Body)
	var err error
readLoop:
	for {
		select {
		case delivery := <-events:
			consumeErr := consume(delivery.event)
			delivery.ack <- consumeErr
			if consumeErr != nil {
				err = consumeErr
				break readLoop
			}
		case err = <-readDone:
			break readLoop
		case <-heartbeat.C:
			if !committed {
				continue
			}
			if _, writeErr := io.WriteString(w, ": keep-alive\n\n"); writeErr != nil {
				cancelStream()
				err = context.Canceled
				break readLoop
			}
			flusher.Flush()
		case <-streamContext.Done():
			err = streamContext.Err()
			break readLoop
		}
	}
	if (!errors.Is(err, errResponsesCompleted) && err != nil) || !completed {
		outcome := responsesStreamFailureOutcome(streamContext.Err(), err)
		a.finishRequest(reqID, outcome, response.StatusCode)
		if streamContext.Err() == nil {
			if committed {
				writeResponsesStreamError(w, reqID, "upstream_protocol_error", "Upstream request failed.")
			} else {
				writeModelError(w, http.StatusBadGateway, "upstream_protocol_error", "Upstream request failed.", reqID)
			}
		}
		return
	}
	a.observeRequestUsage(reqID, terminal.data)
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

func responsesStreamFailureOutcome(contextErr, streamErr error) string {
	if contextErr != nil || errors.Is(streamErr, errResponsesDownstream) {
		return "cancelled"
	}
	if errors.Is(streamErr, errResponsesIncomplete) || errors.Is(streamErr, io.EOF) || errors.Is(streamErr, io.ErrUnexpectedEOF) {
		return "interrupted"
	}
	return "failed"
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

type codexResponsesPreflight struct {
	selected   route
	credential *membership.CodexAuthCredential
	body       []byte
}

func (p *codexResponsesPreflight) Destroy() {
	if p != nil && p.credential != nil {
		p.credential.Destroy()
		p.credential = nil
	}
}

func (a *App) prepareCodexResponses(ctx context.Context, body []byte, selected route) (*codexResponsesPreflight, *modelPreflightError) {
	if !a.cfg.ExperimentalCodexMembership {
		return nil, requestPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.")
	}
	if selected.KeyVersion != 2 || !selected.CredentialState.Valid || selected.CredentialState.String == codexStateReauth {
		return nil, accountPreflightFailure(http.StatusBadGateway, "upstream_reauthentication_required", "The upstream credential must be re-imported.", scheduling.FailureAuth)
	}
	if selected.CredentialState.String != codexStateImported && selected.CredentialState.String != codexStateVerified {
		return nil, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
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
	return &codexResponsesPreflight{selected: selected, credential: credential, body: body}, nil
}

func (a *App) handleCodexResponses(w http.ResponseWriter, r *http.Request, stream bool, prepared *codexResponsesPreflight, reqID string, persist func([]byte) ([]byte, error)) {
	if !stream {
		result, runErr := a.responses.Responses(r.Context(), prepared.credential, prepared.body, nil)
		if runErr != nil {
			a.handleCodexFailure(w, r, prepared.selected, reqID, runErr, false, writeResponsesStreamError)
			return
		}
		if validateCompletedResponse(result) != nil {
			a.responsesProtocolError(w, reqID, http.StatusOK)
			return
		}
		if persist != nil {
			stored, persistErr := persist(result)
			if persistErr != nil {
				a.finishRequest(reqID, "failed", 200)
				writeModelError(w, 503, "storage_unavailable", "Response storage is temporarily unavailable.", reqID)
				return
			}
			result = stored
		}
		if err := a.markCodexVerified(prepared.selected.AccountID, prepared.selected.Revision); err != nil {
			a.finishRequest(reqID, "failed", 200)
			writeModelError(w, 503, "storage_unavailable", "Service is temporarily unavailable.", reqID)
			return
		}
		a.observeRequestUsage(reqID, result)
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
	_, runErr := a.responses.Responses(r.Context(), prepared.credential, prepared.body, func(event json.RawMessage) error {
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
		a.handleCodexFailure(w, r, prepared.selected, reqID, runErr, committed, writeResponsesStreamError)
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
	if err := a.markCodexVerified(prepared.selected.AccountID, prepared.selected.Revision); err != nil {
		a.finishRequest(reqID, "failed", 200)
		if committed {
			writeResponsesStreamError(w, reqID, "storage_unavailable", "Service is temporarily unavailable.")
		} else {
			writeModelError(w, 503, "storage_unavailable", "Service is temporarily unavailable.", reqID)
		}
		return
	}
	a.observeRequestUsage(reqID, completed)
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
