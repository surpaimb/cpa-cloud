package protocolconv

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

type oneByteReader struct{ reader *bytes.Reader }

func (r *oneByteReader) Read(buffer []byte) (int, error) {
	if len(buffer) > 1 {
		buffer = buffer[:1]
	}
	return r.reader.Read(buffer)
}

func testSSELimits() SSELimits {
	return SSELimits{MaxLineBytes: 1024, MaxEventBytes: 4096, MaxStreamBytes: 1 << 20}
}

func TestSSEDecoderArbitrarySplitsLineEndingsCommentsAndMultilineData(t *testing.T) {
	wire := []byte("\xef\xbb\xbf: ignored\r\nevent: response.output_text.delta\rdata: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"x\"}\n\ndata: [DONE]\r\r")
	decoder := NewSSEDecoder(&oneByteReader{reader: bytes.NewReader(wire)}, testSSELimits())
	first, err := decoder.Next()
	if err != nil {
		t.Fatal(err)
	}
	if first.Event != "response.output_text.delta" || string(first.Data) != "{\"type\":\"response.output_text.delta\",\n\"delta\":\"x\"}" {
		t.Fatalf("unexpected first frame: %#v", first)
	}
	second, err := decoder.Next()
	if err != nil || second.Event != "" || string(second.Data) != "[DONE]" {
		t.Fatalf("unexpected second frame: %#v, %v", second, err)
	}
	if _, err := decoder.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("expected physical EOF, got %v", err)
	}
}

func TestSSEDecoderRejectsUnsupportedFieldsAndEmptyData(t *testing.T) {
	for _, wire := range []string{
		"id: replay\ndata: {}\n\n",
		"retry: 100\ndata: {}\n\n",
		"extension: x\ndata: {}\n\n",
		"event: response.created\n\n",
		"data:\n\n",
	} {
		decoder := NewSSEDecoder(bytes.NewBufferString(wire), testSSELimits())
		if _, err := decoder.Next(); !errors.Is(err, ErrInvalidUpstream) {
			t.Fatalf("expected typed rejection for %q, got %v", wire, err)
		}
	}
}

func TestSSEDecoderEnforcesLineEventAndStreamLimits(t *testing.T) {
	tests := []struct {
		name   string
		wire   string
		limits SSELimits
	}{
		{name: "line", wire: "data: 12345\n\n", limits: SSELimits{MaxLineBytes: 8, MaxEventBytes: 32, MaxStreamBytes: 64}},
		{name: "event", wire: ":123456789\ndata: {}\n\n", limits: SSELimits{MaxLineBytes: 16, MaxEventBytes: 16, MaxStreamBytes: 64}},
		{name: "stream", wire: ":x\n\n:y\n\n:z\n\n", limits: SSELimits{MaxLineBytes: 8, MaxEventBytes: 10, MaxStreamBytes: 10}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoder := NewSSEDecoder(bytes.NewBufferString(test.wire), test.limits)
			if _, err := decoder.Next(); !errors.Is(err, ErrInvalidUpstream) {
				t.Fatalf("expected limit rejection, got %v", err)
			}
		})
	}
}

func TestSSEDecoderDoesNotDispatchUnterminatedEvent(t *testing.T) {
	decoder := NewSSEDecoder(bytes.NewBufferString("data: {\"partial\":true}"), testSSELimits())
	if _, err := decoder.Next(); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}
}

func TestEncodeSSE(t *testing.T) {
	encoded, err := EncodeSSE(SSEEvent{Name: "response.completed", Data: []byte(" { \"type\" : \"response.completed\" } "), Terminal: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n" {
		t.Fatalf("unexpected encoding: %q", encoded)
	}
	done, err := EncodeSSE(SSEEvent{Data: []byte("[DONE]"), Terminal: true})
	if err != nil || string(done) != "data: [DONE]\n\n" {
		t.Fatalf("unexpected done encoding: %q, %v", done, err)
	}
}
