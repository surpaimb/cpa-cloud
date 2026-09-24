package protocolconv

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"unicode/utf8"
)

// SSELimits bounds the wire bytes retained or consumed by the decoder.
type SSELimits struct {
	MaxLineBytes   int
	MaxEventBytes  int
	MaxStreamBytes int64
}

// SSEFrame is one complete SSE event after WHATWG line processing. Data lines
// are joined with LF and the final LF is removed.
type SSEFrame struct {
	Event string
	Data  []byte
}

// SSEDecoder parses a one-shot upstream SSE response using WHATWG line and
// multi-data-field rules (https://html.spec.whatwg.org/multipage/server-sent-events.html).
// It intentionally rejects reconnection and extension fields because this
// runtime never reconnects or replays a dispatched model request.
type SSEDecoder struct {
	reader     *bufio.Reader
	limits     SSELimits
	initErr    error
	started    bool
	closed     bool
	totalBytes int64
	eventBytes int
	eventName  string
	data       bytes.Buffer
	sawData    bool
	sawField   bool
}

func NewSSEDecoder(reader io.Reader, limits SSELimits) *SSEDecoder {
	decoder := &SSEDecoder{limits: limits}
	if reader == nil {
		decoder.initErr = invalidUpstream("stream", "SSE reader is required")
		return decoder
	}
	if limits.MaxLineBytes <= 0 || limits.MaxEventBytes <= 0 || limits.MaxStreamBytes <= 0 ||
		limits.MaxEventBytes < limits.MaxLineBytes || limits.MaxStreamBytes < int64(limits.MaxEventBytes) {
		decoder.initErr = invalidUpstream("stream", "invalid SSE limits")
		return decoder
	}
	decoder.reader = bufio.NewReaderSize(reader, 4096)
	return decoder
}

// Next returns one complete data event. Comment-only blocks are skipped.
func (d *SSEDecoder) Next() (SSEFrame, error) {
	if d == nil {
		return SSEFrame{}, invalidUpstream("stream", "SSE decoder is required")
	}
	if d.initErr != nil {
		err := d.initErr
		d.initErr = nil
		d.closed = true
		return SSEFrame{}, err
	}
	if d.closed {
		return SSEFrame{}, io.EOF
	}
	if !d.started {
		d.started = true
		if err := d.consumeBOM(); err != nil {
			d.closed = true
			return SSEFrame{}, err
		}
	}
	for {
		line, wireBytes, err := d.readLine()
		if err != nil {
			d.closed = true
			if err == io.EOF && !d.sawField && !d.sawData && d.eventName == "" {
				return SSEFrame{}, io.EOF
			}
			if err == io.EOF {
				return SSEFrame{}, io.ErrUnexpectedEOF
			}
			return SSEFrame{}, err
		}
		d.eventBytes += wireBytes
		if d.eventBytes > d.limits.MaxEventBytes {
			d.closed = true
			return SSEFrame{}, invalidUpstream("stream", "SSE event exceeded the configured byte limit")
		}
		if !utf8.Valid(line) {
			d.closed = true
			return SSEFrame{}, invalidUpstream("stream", "SSE line is not valid UTF-8")
		}
		if len(line) == 0 {
			frame, ready, err := d.finishEvent()
			if err != nil {
				d.closed = true
				return SSEFrame{}, err
			}
			if ready {
				return frame, nil
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		d.sawField = true
		field, value, found := bytes.Cut(line, []byte{':'})
		if !found {
			value = nil
		}
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		switch string(field) {
		case "event":
			if len(value) != 0 && !validSSEEventName(string(value)) {
				d.closed = true
				return SSEFrame{}, invalidUpstream("event", "invalid SSE event name")
			}
			d.eventName = string(value)
		case "data":
			d.data.Write(value)
			d.data.WriteByte('\n')
			d.sawData = true
		default:
			d.closed = true
			return SSEFrame{}, invalidUpstream("stream", "unknown SSE field")
		}
	}
}

func (d *SSEDecoder) consumeBOM() error {
	peeked, err := d.reader.Peek(3)
	if len(peeked) == 3 && bytes.Equal(peeked, []byte{0xef, 0xbb, 0xbf}) {
		if _, err := d.reader.Discard(3); err != nil {
			return err
		}
		return d.addStreamBytes(3)
	}
	if err != nil && err != io.EOF {
		return err
	}
	return nil
}

func (d *SSEDecoder) readLine() ([]byte, int, error) {
	line := make([]byte, 0, 128)
	wireBytes := 0
	for {
		value, err := d.reader.ReadByte()
		if err != nil {
			if err == io.EOF && len(line) == 0 {
				return nil, wireBytes, io.EOF
			}
			return nil, wireBytes, io.ErrUnexpectedEOF
		}
		wireBytes++
		if err := d.addStreamBytes(1); err != nil {
			return nil, wireBytes, err
		}
		switch value {
		case '\n':
			return line, wireBytes, nil
		case '\r':
			if next, err := d.reader.Peek(1); err == nil && next[0] == '\n' {
				if _, err := d.reader.Discard(1); err != nil {
					return nil, wireBytes, err
				}
				wireBytes++
				if err := d.addStreamBytes(1); err != nil {
					return nil, wireBytes, err
				}
			}
			return line, wireBytes, nil
		default:
			line = append(line, value)
			if len(line) > d.limits.MaxLineBytes {
				return nil, wireBytes, invalidUpstream("stream", "SSE line exceeded the configured byte limit")
			}
		}
	}
}

func (d *SSEDecoder) addStreamBytes(count int64) error {
	d.totalBytes += count
	if d.totalBytes > d.limits.MaxStreamBytes {
		return invalidUpstream("stream", "SSE stream exceeded the configured byte limit")
	}
	return nil
}

func (d *SSEDecoder) finishEvent() (SSEFrame, bool, error) {
	defer func() {
		d.eventBytes = 0
		d.eventName = ""
		d.data.Reset()
		d.sawData = false
		d.sawField = false
	}()
	if !d.sawData {
		if d.sawField {
			return SSEFrame{}, false, invalidUpstream("data", "SSE event has no data field")
		}
		return SSEFrame{}, false, nil
	}
	data := d.data.Bytes()
	if len(data) > 0 {
		data = data[:len(data)-1]
	}
	if len(data) == 0 {
		return SSEFrame{}, false, invalidUpstream("data", "SSE data is empty")
	}
	return SSEFrame{Event: d.eventName, Data: append([]byte(nil), data...)}, true, nil
}

// EncodeSSE serializes one converted event without retaining model output.
func EncodeSSE(event SSEEvent) ([]byte, error) {
	if event.Name != "" && !validSSEEventName(event.Name) {
		return nil, invalid("event", "invalid SSE event name")
	}
	data := bytes.TrimSpace(event.Data)
	var compact bytes.Buffer
	if bytes.Equal(data, []byte("[DONE]")) {
		compact.WriteString("[DONE]")
	} else {
		if len(data) == 0 || !json.Valid(data) {
			return nil, invalid("data", "SSE data must be a JSON value or [DONE]")
		}
		if err := json.Compact(&compact, data); err != nil {
			return nil, err
		}
	}
	var output bytes.Buffer
	if event.Name != "" {
		output.WriteString("event: ")
		output.WriteString(event.Name)
		output.WriteByte('\n')
	}
	output.WriteString("data: ")
	output.Write(compact.Bytes())
	output.WriteString("\n\n")
	return output.Bytes(), nil
}

func validSSEEventName(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
