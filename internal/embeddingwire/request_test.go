package embeddingwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeAndMarshalRequestPreservesInputShape(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantCount  int
		wantSingle bool
	}{
		{"single", `{"model":"public-model","input":"hello"}`, 1, true},
		{"batch", `{"model":"public-model","input":["hello","world"],"encoding_format":"float"}`, 2, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := DecodeRequest([]byte(test.body))
			if err != nil {
				t.Fatal(err)
			}
			if request.Model != "public-model" || request.EncodingFormat != EncodingFloat || request.Input.Count() != test.wantCount {
				t.Fatalf("unexpected request metadata: %#v", request)
			}
			encoded, err := MarshalRequest(request, "upstream-model")
			if err != nil {
				t.Fatal(err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &root); err != nil {
				t.Fatal(err)
			}
			var model, format string
			if err := json.Unmarshal(root["model"], &model); err != nil || model != "upstream-model" {
				t.Fatalf("model was not replaced: %s", encoded)
			}
			if err := json.Unmarshal(root["encoding_format"], &format); err != nil || format != "float" {
				t.Fatalf("float encoding was not normalized: %s", encoded)
			}
			var single string
			isSingle := json.Unmarshal(root["input"], &single) == nil
			if isSingle != test.wantSingle {
				t.Fatalf("input shape changed: %s", encoded)
			}
		})
	}
}

func TestDecodeRequestRejectsInvalidAndUnsupportedValues(t *testing.T) {
	tests := []struct {
		name string
		body []byte
		want error
	}{
		{"empty body", nil, ErrInvalidRequest},
		{"not object", []byte(`[]`), ErrInvalidRequest},
		{"second value", []byte(`{} {}`), ErrInvalidRequest},
		{"duplicate", []byte(`{"model":"m","model":"n","input":"x"}`), ErrInvalidRequest},
		{"unknown", []byte(`{"model":"m","input":"x","extra":true}`), ErrUnsupportedFeature},
		{"dimensions", []byte(`{"model":"m","input":"x","dimensions":3}`), ErrUnsupportedFeature},
		{"user", []byte(`{"model":"m","input":"x","user":"u"}`), ErrUnsupportedFeature},
		{"stream", []byte(`{"model":"m","input":"x","stream":false}`), ErrUnsupportedFeature},
		{"base64", []byte(`{"model":"m","input":"x","encoding_format":"base64"}`), ErrUnsupportedFeature},
		{"unknown encoding", []byte(`{"model":"m","input":"x","encoding_format":"binary"}`), ErrUnsupportedFeature},
		{"encoding type", []byte(`{"model":"m","input":"x","encoding_format":1}`), ErrInvalidRequest},
		{"token array", []byte(`{"model":"m","input":[1,2]}`), ErrUnsupportedFeature},
		{"token batch", []byte(`{"model":"m","input":[[1,2],[3]]}`), ErrUnsupportedFeature},
		{"empty model", []byte(`{"model":"","input":"x"}`), ErrInvalidRequest},
		{"missing model", []byte(`{"input":"x"}`), ErrInvalidRequest},
		{"empty input", []byte(`{"model":"m","input":""}`), ErrInvalidRequest},
		{"empty batch", []byte(`{"model":"m","input":[]}`), ErrInvalidRequest},
		{"empty member", []byte(`{"model":"m","input":["x",""]}`), ErrInvalidRequest},
		{"null input", []byte(`{"model":"m","input":null}`), ErrInvalidRequest},
		{"mixed input", []byte(`{"model":"m","input":["x",true]}`), ErrInvalidRequest},
		{"bad utf8", []byte{'{', '"', 'm', 'o', 'd', 'e', 'l', '"', ':', '"', 'm', '"', ',', '"', 'i', 'n', 'p', 'u', 't', '"', ':', '"', 0xff, '"', '}'}, ErrInvalidRequest},
		{"unpaired surrogate", []byte(`{"model":"m","input":"\ud800"}`), ErrInvalidRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeRequest(test.body)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want error matching %v", err, test.want)
			}
		})
	}
}

func TestRequestResourceLimits(t *testing.T) {
	items := make([]string, maxInputItems+1)
	for index := range items {
		items[index] = "x"
	}
	body, err := json.Marshal(map[string]any{"model": "m", "input": items})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRequest(body); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("too many inputs: %v", err)
	}

	tooLong, err := json.Marshal(map[string]any{"model": "m", "input": strings.Repeat("x", maxInputTextBytes+1)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRequest(tooLong); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized text: %v", err)
	}

	programmatic := Request{
		Model:          "m",
		Input:          Input{texts: []string{strings.Repeat("x", maxInputTextBytes), strings.Repeat("x", maxInputTextBytes), strings.Repeat("x", maxInputTextBytes), strings.Repeat("x", maxInputTextBytes), "x"}},
		EncodingFormat: EncodingFloat,
	}
	if err := ValidateRequest(programmatic); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized aggregate input: %v", err)
	}

	oversizedBody := append([]byte(`{"model":"m","input":"x"}`), bytes.Repeat([]byte{' '}, MaxRequestBytes)...)
	if _, err := DecodeRequest(oversizedBody); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized body: %v", err)
	}
}

func TestRequestDepthLimitAndValidSurrogatePair(t *testing.T) {
	request, err := DecodeRequest([]byte(`{"model":"m","input":"\ud83d\ude00"}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.Input.Count() != 1 {
		t.Fatalf("unexpected input count: %d", request.Input.Count())
	}

	nested := strings.Repeat("[", maxJSONDepth+1) + strings.Repeat("]", maxJSONDepth+1)
	if _, err := DecodeRequest([]byte(`{"model":"m","input":"x","extra":` + nested + `}`)); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("excessive JSON depth: %v", err)
	}
}

func TestValidateAndMarshalRequestDefendProgrammaticValues(t *testing.T) {
	request := Request{
		Model:          "public",
		Input:          Input{single: false, texts: []string{"a", "b"}},
		EncodingFormat: EncodingFloat,
	}
	if err := ValidateRequest(request); err != nil {
		t.Fatal(err)
	}
	if _, err := MarshalRequest(request, ""); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty upstream model: %v", err)
	}
	request.Input.texts[1] = ""
	if err := ValidateRequest(request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("empty programmatic member: %v", err)
	}
	request.Input.texts[1] = "b"
	request.EncodingFormat = "base64"
	if err := ValidateRequest(request); !errors.Is(err, ErrUnsupportedFeature) {
		t.Fatalf("unsupported format: %v", err)
	}
}
