// Independently authored from CPA Cloud's protocol conversion contract and
// the public protocol sources recorded in docs/protocol-sources.md.
package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"cpacloud.local/server/internal/protocolconv"
)

const (
	protocolStreamTerminalDrainTimeout = 2 * time.Second
	protocolStreamWriteTimeout         = 30 * time.Second
)

// handleConvertedModelJSON observes the immutable raw upstream body before
// conversion, then completes the one parent request and one dispatched attempt.
func (a *App) handleConvertedModelJSON(w http.ResponseWriter, r *http.Request, runtime *protocolRuntime, req *http.Request, client upstreamHTTPDoer, requestID string, maxBody int64, persist func([]byte) ([]byte, error)) {
	result, err := runtime.execute(client, req, maxBody, func(_ protocolconv.Protocol, raw []byte) error {
		a.observeRequestUsage(requestID, raw)
		return nil
	})
	if err != nil {
		outcome, status, code := "failed", http.StatusBadGateway, "upstream_protocol_error"
		var upstreamStatus *protocolUpstreamStatusError
		if errors.As(err, &upstreamStatus) {
			result.StatusCode = upstreamStatus.StatusCode
			code = "upstream_error"
			if upstreamStatus.StatusCode == http.StatusTooManyRequests {
				status, code = http.StatusTooManyRequests, "upstream_rate_limited"
				if retryAfter := result.Header.Get("Retry-After"); retryAfter != "" {
					w.Header().Set("Retry-After", retryAfter)
				}
			}
		} else if r.Context().Err() != nil || errors.Is(err, context.Canceled) {
			outcome = "cancelled"
		} else if errors.Is(err, context.DeadlineExceeded) {
			status, code = http.StatusGatewayTimeout, "upstream_timeout"
		}
		a.finishRequest(requestID, outcome, result.StatusCode)
		if r.Context().Err() == nil {
			writeConvertedProtocolError(w, runtime, status, code, "Upstream request failed.", requestID)
		}
		return
	}
	body := result.ClientBody
	if persist != nil {
		body, err = persist(body)
		if err != nil {
			a.finishRequest(requestID, "failed", result.StatusCode)
			writeConvertedProtocolError(w, runtime, http.StatusServiceUnavailable, "storage_unavailable", "Response storage is temporarily unavailable.", requestID)
			return
		}
	}
	if err := a.finishRequestChecked(requestID, "succeeded", result.StatusCode); err != nil {
		writeConvertedProtocolError(w, runtime, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.", requestID)
		return
	}
	contentType := "application/json"
	if runtime.plan().ClientProtocol == protocolconv.ProtocolGeminiGenerate {
		contentType = "application/json; charset=utf-8"
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// handleConvertedModelSSE converts the two explicitly supported OpenAI stream
// pairs while preserving the single dispatch and accounting parent created by
// the caller. Raw upstream event JSON is observed before conversion.
func (a *App) handleConvertedModelSSE(w http.ResponseWriter, r *http.Request, runtime *protocolRuntime, req *http.Request, client upstreamHTTPDoer, requestID string) {
	controller := http.NewResponseController(w)
	if !supportsProtocolStreamFlush(w) || controller.SetWriteDeadline(time.Now().Add(protocolStreamWriteTimeout)) != nil || controller.SetWriteDeadline(time.Time{}) != nil {
		a.finishRequest(requestID, "failed", 0)
		writeConvertedProtocolError(w, runtime, http.StatusInternalServerError, "streaming_unavailable", "Streaming is unavailable.", requestID)
		return
	}
	defer func() { _ = controller.SetWriteDeadline(time.Time{}) }()
	limits := protocolStreamLimits{
		MaxLineBytes: chatMaxLine, MaxEventBytes: chatMaxEvent,
		MaxStreamBytes: chatMaxStream, TerminalDrainTimeout: protocolStreamTerminalDrainTimeout,
	}
	result, err := runtime.executeStream(r.Context(), client, req, limits, func(_ protocolconv.Protocol, raw []byte) error {
		a.observeRequestUsage(requestID, raw)
		return nil
	}, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
		return writeConvertedProtocolStreamEvent(w, controller, event, protocolStreamWriteTimeout)
	})
	if err == nil && result.Completed {
		_ = a.finishRequestChecked(requestID, "succeeded", result.StatusCode)
		return
	}

	outcome, status, code := "failed", http.StatusBadGateway, "upstream_protocol_error"
	var upstreamStatus *protocolUpstreamStatusError
	switch {
	case errors.As(err, &upstreamStatus):
		result.StatusCode = upstreamStatus.StatusCode
		code = "upstream_error"
		if upstreamStatus.StatusCode == http.StatusTooManyRequests {
			status, code = http.StatusTooManyRequests, "upstream_rate_limited"
			if retryAfter := safeRetryAfter(result.Header.Get("Retry-After")); retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
		}
	case r.Context().Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, errProtocolStreamDownstream):
		outcome = "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		outcome, status, code = "cancelled", http.StatusGatewayTimeout, "upstream_timeout"
	case errors.Is(err, protocolconv.ErrInterrupted):
		outcome = "interrupted"
	}
	a.finishRequest(requestID, outcome, result.StatusCode)
	if r.Context().Err() == nil && !result.DownstreamCommitted && !errors.Is(err, errProtocolStreamDownstream) {
		writeConvertedProtocolError(w, runtime, status, code, "Upstream request failed.", requestID)
	}
}

func supportsProtocolStreamFlush(w http.ResponseWriter) bool {
	for w != nil {
		switch w.(type) {
		case interface{ FlushError() error }, http.Flusher:
			return true
		case interface{ Unwrap() http.ResponseWriter }:
			w = w.(interface{ Unwrap() http.ResponseWriter }).Unwrap()
		default:
			return false
		}
	}
	return false
}

func writeConvertedProtocolStreamEvent(w http.ResponseWriter, controller *http.ResponseController, event protocolconv.SSEEvent, timeout time.Duration) (protocolStreamWriteResult, error) {
	if w == nil || controller == nil || timeout <= 0 {
		return protocolStreamWriteResult{}, errors.New("protocol stream downstream is not configured")
	}
	encoded, err := protocolconv.EncodeSSE(event)
	if err != nil {
		return protocolStreamWriteResult{}, err
	}
	if err := controller.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return protocolStreamWriteResult{}, err
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	committed := protocolStreamWriteResult{DownstreamCommitted: true, SemanticCommitted: event.Semantic}
	written, err := w.Write(encoded)
	if err != nil {
		return committed, err
	}
	if written != len(encoded) {
		return committed, io.ErrShortWrite
	}
	if err := controller.Flush(); err != nil {
		return committed, err
	}
	if err := controller.SetWriteDeadline(time.Time{}); err != nil {
		return committed, err
	}
	return committed, nil
}

func writeConvertedProtocolError(w http.ResponseWriter, runtime *protocolRuntime, status int, code, message, requestID string) {
	if runtime != nil {
		switch runtime.plan().ClientProtocol {
		case protocolconv.ProtocolAnthropicMessages:
			writeAnthropicError(w, status, anthropicAdmissionType(status), message, requestID)
			return
		case protocolconv.ProtocolGeminiGenerate:
			writeGeminiError(w, status, geminiAdmissionStatus(status), message)
			return
		}
	}
	writeModelError(w, status, code, message, requestID)
}
