package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"cpacloud.local/server/internal/protocolconv"
)

type protocolStreamLimits struct {
	MaxLineBytes         int
	MaxEventBytes        int
	MaxStreamBytes       int64
	TerminalDrainTimeout time.Duration
}

type protocolStreamWriteResult struct {
	DownstreamCommitted bool
	SemanticCommitted   bool
}

type protocolStreamResult struct {
	StatusCode          int
	Header              http.Header
	DownstreamCommitted bool
	SemanticCommitted   bool
	Completed           bool
}

var errProtocolStreamDownstream = errors.New("protocol stream downstream write failed")

type protocolStreamRead struct {
	frame protocolconv.SSEFrame
	err   error
}

// executeStream performs exactly one upstream request after the caller's
// durable dispatch barrier. It owns neither admission nor ledger settlement.
func (p *protocolRuntime) executeStream(
	ctx context.Context,
	client upstreamHTTPDoer,
	request *http.Request,
	limits protocolStreamLimits,
	observeRaw func(protocolconv.Protocol, []byte) error,
	writeClient func(protocolconv.SSEEvent) (protocolStreamWriteResult, error),
) (protocolStreamResult, error) {
	var result protocolStreamResult
	if p == nil || client == nil || request == nil || ctx == nil || writeClient == nil {
		return result, errors.New("protocol stream runtime is incomplete")
	}
	if limits.MaxLineBytes <= 0 || limits.MaxEventBytes <= 0 || limits.MaxStreamBytes <= 0 || limits.TerminalDrainTimeout <= 0 {
		return result, errors.New("protocol stream limits are not configured")
	}
	converter, err := protocolconv.NewStreamConverter(p.prepared)
	if err != nil {
		return result, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	upstreamRequest := request.Clone(streamCtx)
	response, err := client.Do(upstreamRequest)
	if err != nil {
		if streamCtx.Err() != nil {
			return result, streamCtx.Err()
		}
		return result, err
	}
	if response == nil || response.Body == nil {
		return result, errors.New("upstream returned an incomplete response")
	}
	defer response.Body.Close()
	result.StatusCode = response.StatusCode
	result.Header = boundedProtocolStreamHeaders(response.Header)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return result, &protocolUpstreamStatusError{StatusCode: response.StatusCode}
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		return result, errors.New("upstream returned a non-SSE response")
	}

	decoder := protocolconv.NewSSEDecoder(response.Body, protocolconv.SSELimits{
		MaxLineBytes: limits.MaxLineBytes, MaxEventBytes: limits.MaxEventBytes, MaxStreamBytes: limits.MaxStreamBytes,
	})
	var terminalBatch []protocolconv.SSEEvent
	var drainDeadline time.Time
	for {
		frame, readErr := nextProtocolStreamFrame(streamCtx, decoder, response.Body, drainDeadline)
		if readErr != nil {
			if streamCtx.Err() != nil {
				return result, streamCtx.Err()
			}
			if errors.Is(readErr, errProtocolStreamDrainTimeout) {
				cancel()
				_ = response.Body.Close()
				return result, fmt.Errorf("%w: upstream did not close after terminal event", protocolconv.ErrInterrupted)
			}
			if errors.Is(readErr, io.EOF) {
				if err := converter.EOF(); err != nil {
					return result, err
				}
				for _, event := range terminalBatch {
					if err := writeProtocolStreamEvent(&result, writeClient, event); err != nil {
						cancel()
						_ = response.Body.Close()
						return result, err
					}
				}
				if len(terminalBatch) == 0 {
					return result, fmt.Errorf("%w: upstream stream ended without a terminal event", protocolconv.ErrInterrupted)
				}
				result.Completed = true
				return result, nil
			}
			if errors.Is(readErr, io.ErrUnexpectedEOF) {
				return result, fmt.Errorf("%w: upstream SSE ended mid-event", protocolconv.ErrInterrupted)
			}
			if errors.Is(readErr, protocolconv.ErrInvalidUpstream) {
				return result, readErr
			}
			return result, fmt.Errorf("%w: upstream SSE read failed: %v", protocolconv.ErrInterrupted, readErr)
		}

		trimmed := bytes.TrimSpace(frame.Data)
		isDone := p.plan().UpstreamProtocol == protocolconv.ProtocolOpenAIChat && bytes.Equal(trimmed, []byte("[DONE]"))
		if !isDone {
			if !json.Valid(trimmed) {
				return result, fmt.Errorf("%w: invalid upstream SSE JSON", protocolconv.ErrInvalidUpstream)
			}
			if observeRaw != nil {
				if err := observeRaw(p.plan().UpstreamProtocol, append([]byte(nil), frame.Data...)); err != nil {
					cancel()
					_ = response.Body.Close()
					return result, err
				}
			}
		}
		events, err := converter.FeedFrame(frame)
		if err != nil {
			return result, err
		}
		terminalIndex := -1
		for index, event := range events {
			if event.Terminal {
				if terminalIndex >= 0 || index != len(events)-1 {
					return result, fmt.Errorf("%w: invalid converted terminal batch", protocolconv.ErrInvalidUpstream)
				}
				terminalIndex = index
			}
		}
		if terminalIndex >= 0 {
			if !drainDeadline.IsZero() {
				return result, fmt.Errorf("%w: duplicate converted terminal batch", protocolconv.ErrInvalidUpstream)
			}
			terminalBatch = append(terminalBatch, events...)
			drainDeadline = time.Now().Add(limits.TerminalDrainTimeout)
			continue
		}
		if !drainDeadline.IsZero() && len(events) != 0 {
			return result, fmt.Errorf("%w: converted event followed a terminal event", protocolconv.ErrInvalidUpstream)
		}
		for _, event := range events {
			if err := writeProtocolStreamEvent(&result, writeClient, event); err != nil {
				cancel()
				_ = response.Body.Close()
				return result, err
			}
		}
	}
}

var errProtocolStreamDrainTimeout = errors.New("protocol stream terminal drain timed out")

func nextProtocolStreamFrame(ctx context.Context, decoder *protocolconv.SSEDecoder, body io.Closer, deadline time.Time) (protocolconv.SSEFrame, error) {
	var remaining time.Duration
	if !deadline.IsZero() {
		remaining = time.Until(deadline)
		if remaining <= 0 {
			_ = body.Close()
			return protocolconv.SSEFrame{}, errProtocolStreamDrainTimeout
		}
	}
	read := make(chan protocolStreamRead, 1)
	go func() {
		frame, err := decoder.Next()
		read <- protocolStreamRead{frame: frame, err: err}
	}()
	if deadline.IsZero() {
		select {
		case value := <-read:
			return value.frame, value.err
		case <-ctx.Done():
			_ = body.Close()
			return protocolconv.SSEFrame{}, ctx.Err()
		}
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case value := <-read:
		return value.frame, value.err
	case <-ctx.Done():
		_ = body.Close()
		return protocolconv.SSEFrame{}, ctx.Err()
	case <-timer.C:
		_ = body.Close()
		return protocolconv.SSEFrame{}, errProtocolStreamDrainTimeout
	}
}

func writeProtocolStreamEvent(result *protocolStreamResult, writeClient func(protocolconv.SSEEvent) (protocolStreamWriteResult, error), event protocolconv.SSEEvent) error {
	written, err := writeClient(event)
	result.DownstreamCommitted = result.DownstreamCommitted || written.DownstreamCommitted
	result.SemanticCommitted = result.SemanticCommitted || written.SemanticCommitted
	if err != nil {
		return fmt.Errorf("%w: %v", errProtocolStreamDownstream, err)
	}
	return nil
}

func boundedProtocolStreamHeaders(source http.Header) http.Header {
	const (
		maxHeaderValueBytes = 1024
		maxHeaderValues     = 8
		maxHeaderBytes      = 4096
	)
	result := make(http.Header)
	total := 0
	for _, name := range []string{"Content-Type", "Retry-After"} {
		values := source.Values(name)
		for index, value := range values {
			if index >= maxHeaderValues || len(value) > maxHeaderValueBytes || total+len(value) > maxHeaderBytes {
				continue
			}
			if len(value) <= maxHeaderValueBytes {
				result.Add(name, value)
				total += len(value)
			}
		}
	}
	return result
}
