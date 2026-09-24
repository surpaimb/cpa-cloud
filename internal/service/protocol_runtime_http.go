// Independently authored from CPA Cloud's protocol conversion contract and
// the public protocol sources recorded in docs/protocol-sources.md.
package service

import (
	"context"
	"errors"
	"net/http"

	"cpacloud.local/server/internal/protocolconv"
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
