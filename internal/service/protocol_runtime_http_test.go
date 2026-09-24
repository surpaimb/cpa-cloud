package service

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/protocolconv"
)

type protocolStreamHTTPWriter struct {
	header           http.Header
	body             bytes.Buffer
	shortWrite       bool
	flushErr         error
	terminalFlushErr error
	deadline         time.Time
	deadlineErr      error
	flushes          int
}

func (w *protocolStreamHTTPWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *protocolStreamHTTPWriter) WriteHeader(int) {}

func (w *protocolStreamHTTPWriter) Write(value []byte) (int, error) {
	if !w.deadline.IsZero() && time.Until(w.deadline) < 25*time.Millisecond {
		time.Sleep(time.Until(w.deadline) + 5*time.Millisecond)
		return 0, os.ErrDeadlineExceeded
	}
	if bytes.Contains(value, []byte("response.completed")) || bytes.Contains(value, []byte("[DONE]")) {
		w.flushErr = w.terminalFlushErr
	}
	if w.shortWrite {
		count := len(value) / 2
		_, _ = w.body.Write(value[:count])
		return count, nil
	}
	return w.body.Write(value)
}

func (w *protocolStreamHTTPWriter) FlushError() error {
	w.flushes++
	return w.flushErr
}

func (w *protocolStreamHTTPWriter) SetWriteDeadline(deadline time.Time) error {
	if w.deadlineErr != nil {
		return w.deadlineErr
	}
	w.deadline = deadline
	return nil
}

func TestWriteConvertedProtocolStreamEventReportsFlushShortWriteAndDeadline(t *testing.T) {
	event := protocolconv.SSEEvent{Name: "response.output_text.delta", Data: []byte(`{"type":"response.output_text.delta","delta":"ok"}`), Semantic: true}
	for _, test := range []struct {
		name   string
		writer *protocolStreamHTTPWriter
		limit  time.Duration
	}{
		{name: "flush error", writer: &protocolStreamHTTPWriter{flushErr: errors.New("synthetic flush failure")}, limit: time.Second},
		{name: "short write", writer: &protocolStreamHTTPWriter{shortWrite: true}, limit: time.Second},
		{name: "write deadline", writer: &protocolStreamHTTPWriter{}, limit: 10 * time.Millisecond},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := writeConvertedProtocolStreamEvent(test.writer, http.NewResponseController(test.writer), event, test.limit)
			if err == nil || !result.DownstreamCommitted || !result.SemanticCommitted {
				t.Fatalf("result=%#v err=%v", result, err)
			}
		})
	}
}

type protocolStreamTrackingBody struct {
	io.Reader
	closed bool
}

func (b *protocolStreamTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestConvertedProtocolTerminalFlushFailureCannotCompleteAndClosesUpstream(t *testing.T) {
	runtime := testProtocolStreamRuntime(protocolconv.PlanResponsesToChat, protocolconv.ProtocolOpenAIResponses, protocolconv.ProtocolOpenAIChat)
	body := &protocolStreamTrackingBody{Reader: strings.NewReader(chatSuccessSSE())}
	doer := &protocolStreamDoer{response: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}}
	request, _ := http.NewRequest(http.MethodPost, "https://synthetic.invalid/v1/chat/completions", nil)
	writer := &protocolStreamHTTPWriter{terminalFlushErr: errors.New("synthetic terminal flush failure")}
	controller := http.NewResponseController(writer)
	result, err := runtime.executeStream(request.Context(), doer, request, testProtocolStreamLimits(), nil, func(event protocolconv.SSEEvent) (protocolStreamWriteResult, error) {
		return writeConvertedProtocolStreamEvent(writer, controller, event, time.Second)
	})
	if !errors.Is(err, errProtocolStreamDownstream) || result.Completed || !result.DownstreamCommitted || !body.closed {
		t.Fatalf("result=%#v closed=%t err=%v", result, body.closed, err)
	}
}
