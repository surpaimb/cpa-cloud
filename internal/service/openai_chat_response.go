package service

// Independently implemented from CPA Cloud's response-validation contract.
// This preserves compatible payloads while rejecting malformed/error envelopes.
import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

const (
	chatMaxJSON   = 16 << 20
	chatMaxLine   = 256 << 10
	chatMaxEvent  = 1 << 20
	chatMaxStream = 64 << 20
)

func validChatResponseObject(body []byte) bool {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(body, &envelope) != nil || envelope == nil {
		return false
	}
	if raw, ok := envelope["error"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false
	}
	return true
}

func (a *App) forwardJSON(w http.ResponseWriter, r *http.Request, response *http.Response, id string) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		a.finishRequest(id, "failed", response.StatusCode)
		writeModelError(w, 502, "upstream_error", "Upstream returned an invalid response.", id)
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, chatMaxJSON+1))
	if r.Context().Err() != nil {
		a.finishRequest(id, "cancelled", response.StatusCode)
		return
	}
	if err != nil || len(body) > chatMaxJSON || !validChatResponseObject(body) {
		a.finishRequest(id, "failed", response.StatusCode)
		writeModelError(w, 502, "upstream_error", "Upstream returned an invalid response.", id)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	if _, err := w.Write(body); err != nil {
		a.finishRequest(id, "cancelled", response.StatusCode)
		return
	}
	a.finishRequest(id, "succeeded", response.StatusCode)
}

// Read complete SSE events before forwarding: errors never escape in fragments,
// and a literal [DONE] inside generated content is not a completion marker.
func readChatSSEEvent(reader *bufio.Reader) ([]byte, bool, error) {
	var frame, data bytes.Buffer
	seenData, errorEvent := false, false
	for {
		line, err := readBoundedSSELine(reader, chatMaxLine)
		if frame.Len()+len(line) > chatMaxEvent {
			return nil, false, errors.New("chat event limit")
		}
		frame.Write(line)
		if err != nil {
			if errors.Is(err, io.EOF) && frame.Len() != 0 {
				return nil, false, io.ErrUnexpectedEOF
			}
			return nil, false, err
		}
		trimmed := bytes.TrimSuffix(bytes.TrimSuffix(line, []byte{'\n'}), []byte{'\r'})
		if len(trimmed) == 0 {
			if errorEvent {
				return nil, false, errors.New("chat upstream error")
			}
			if !seenData || len(bytes.TrimSpace(data.Bytes())) == 0 {
				frame.Reset()
				data.Reset()
				seenData = false
				continue
			}
			if bytes.Equal(bytes.TrimSpace(data.Bytes()), []byte("[DONE]")) {
				return bytes.Clone(frame.Bytes()), true, nil
			}
			if !validChatResponseObject(data.Bytes()) {
				return nil, false, errors.New("invalid chat event")
			}
			return bytes.Clone(frame.Bytes()), false, nil
		}
		field, value, _ := bytes.Cut(trimmed, []byte{':'})
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		switch string(field) {
		case "data":
			if seenData {
				data.WriteByte('\n')
			}
			data.Write(value)
			seenData = true
		case "event":
			if bytes.Equal(bytes.TrimSpace(value), []byte("error")) {
				errorEvent = true
			}
		}
	}
}

func (a *App) forwardStream(w http.ResponseWriter, r *http.Request, response *http.Response, id string) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		a.finishRequest(id, "failed", response.StatusCode)
		writeModelError(w, 502, "upstream_error", "Upstream returned an invalid response.", id)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		a.finishRequest(id, "failed", response.StatusCode)
		writeModelError(w, 500, "streaming_unavailable", "Streaming is unavailable.", id)
		return
	}
	limited := &io.LimitedReader{R: response.Body, N: chatMaxStream + 1}
	reader := bufio.NewReaderSize(limited, 32<<10)
	committed := false
	for {
		frame, done, err := readChatSSEEvent(reader)
		if r.Context().Err() != nil {
			a.finishRequest(id, "cancelled", response.StatusCode)
			return
		}
		if err != nil || limited.N <= 0 {
			outcome := "failed"
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				outcome = "interrupted"
			}
			a.finishRequest(id, outcome, response.StatusCode)
			if !committed {
				writeModelError(w, 502, "upstream_error", "Upstream returned an invalid stream.", id)
				return
			}
			body, _ := json.Marshal(map[string]any{"error": map[string]string{"type": "upstream_error", "code": "upstream_error", "message": "Upstream stream failed.", "request_id": id}})
			_, _ = w.Write(append(append([]byte("data: "), body...), []byte("\n\n")...))
			flusher.Flush()
			return
		}
		if !committed {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache, no-store")
			w.Header().Set("X-Accel-Buffering", "no")
			w.WriteHeader(response.StatusCode)
			committed = true
		}
		if _, err := w.Write(frame); err != nil {
			a.finishRequest(id, "cancelled", response.StatusCode)
			return
		}
		flusher.Flush()
		if done {
			a.finishRequest(id, "succeeded", response.StatusCode)
			return
		}
	}
}
