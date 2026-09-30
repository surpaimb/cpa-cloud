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
		name      string
		body      string
		wantCount int
		wantInput string
	}{
		{"single text", `{"model":"public-model","input":"hello"}`, 1, `"hello"`},
		{"text batch", `{"model":"public-model","input":["hello","world"],"encoding_format":"float"}`, 2, `["hello","world"]`},
		{"single token array", `{"model":"public-model","input":[0,12,2147483647]}`, 1, `[0,12,2147483647]`},
		{"token-array batch", `{"model":"public-model","input":[[12,34],[56]]}`, 2, `[[12,34],[56]]`},
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
			if string(root["input"]) != test.wantInput {
				t.Fatalf("input shape changed: %s", encoded)
			}
			if _, present := root["dimensions"]; present {
				t.Fatalf("omitted dimensions changed legacy wire: %s", encoded)
			}
		})
	}
}

func TestExplicitDimensionsPreserveInputShapeAndSelectedModelRules(t *testing.T) {
	for _, input := range []string{`"hello"`, `["hello","world"]`, `[0,12]`, `[[12,34],[56]]`} {
		request, err := DecodeRequest([]byte(`{"model":"public-alias","input":` + input + `,"dimensions":3}`))
		if err != nil || request.Dimensions == nil || *request.Dimensions != 3 {
			t.Fatalf("input=%s request=%+v err=%v", input, request, err)
		}
		encoded, err := MarshalRequest(request, "text-embedding-3-small")
		if err != nil {
			t.Fatal(err)
		}
		var root map[string]json.RawMessage
		if err := json.Unmarshal(encoded, &root); err != nil {
			t.Fatal(err)
		}
		if string(root["input"]) != input || string(root["dimensions"]) != "3" || string(root["model"]) != `"text-embedding-3-small"` {
			t.Fatalf("explicit dimensions wire changed: %s", encoded)
		}
	}
	for _, test := range []struct {
		model string
		value int
		want  bool
	}{
		{"text-embedding-3-small", 1, true},
		{"text-embedding-3-small", 1536, true},
		{"text-embedding-3-small", 1537, false},
		{"text-embedding-3-large", 1, true},
		{"text-embedding-3-large", 3072, true},
		{"text-embedding-3-large", 3073, false},
		{"text-embedding-3-small-v2", 3, false},
		{"text-embedding-ada-002", 3, false},
	} {
		if got := DimensionsAllowed(&test.value, test.model); got != test.want {
			t.Errorf("DimensionsAllowed(%d, %q)=%v want %v", test.value, test.model, got, test.want)
		}
		if !DimensionsAllowed(nil, test.model) {
			t.Errorf("omission rejected model %q", test.model)
		}
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
		{"zero dimensions", []byte(`{"model":"m","input":"x","dimensions":0}`), ErrInvalidRequest},
		{"negative zero dimensions", []byte(`{"model":"m","input":"x","dimensions":-0}`), ErrInvalidRequest},
		{"negative dimensions", []byte(`{"model":"m","input":"x","dimensions":-1}`), ErrInvalidRequest},
		{"fractional dimensions", []byte(`{"model":"m","input":"x","dimensions":1.0}`), ErrInvalidRequest},
		{"exponent dimensions", []byte(`{"model":"m","input":"x","dimensions":1e0}`), ErrInvalidRequest},
		{"overflow dimensions", []byte(`{"model":"m","input":"x","dimensions":2147483648}`), ErrInvalidRequest},
		{"boolean dimensions", []byte(`{"model":"m","input":"x","dimensions":true}`), ErrInvalidRequest},
		{"string dimensions", []byte(`{"model":"m","input":"x","dimensions":"3"}`), ErrInvalidRequest},
		{"null dimensions", []byte(`{"model":"m","input":"x","dimensions":null}`), ErrInvalidRequest},
		{"duplicate dimensions", []byte(`{"model":"m","input":"x","dimensions":2,"dimensions":3}`), ErrInvalidRequest},
		{"user", []byte(`{"model":"m","input":"x","user":"u"}`), ErrUnsupportedFeature},
		{"stream", []byte(`{"model":"m","input":"x","stream":false}`), ErrUnsupportedFeature},
		{"base64", []byte(`{"model":"m","input":"x","encoding_format":"base64"}`), ErrUnsupportedFeature},
		{"unknown encoding", []byte(`{"model":"m","input":"x","encoding_format":"binary"}`), ErrUnsupportedFeature},
		{"encoding type", []byte(`{"model":"m","input":"x","encoding_format":1}`), ErrInvalidRequest},
		{"negative token", []byte(`{"model":"m","input":[-1]}`), ErrInvalidRequest},
		{"negative zero", []byte(`{"model":"m","input":[-0]}`), ErrInvalidRequest},
		{"fractional token", []byte(`{"model":"m","input":[1.0]}`), ErrInvalidRequest},
		{"exponent token", []byte(`{"model":"m","input":[1e0]}`), ErrInvalidRequest},
		{"overflow token", []byte(`{"model":"m","input":[2147483648]}`), ErrInvalidRequest},
		{"huge token", []byte(`{"model":"m","input":[999999999999999999999999999999]}`), ErrInvalidRequest},
		{"token string", []byte(`{"model":"m","input":[1,"2"]}`), ErrInvalidRequest},
		{"text and tokens", []byte(`{"model":"m","input":["a",1]}`), ErrInvalidRequest},
		{"token and batch", []byte(`{"model":"m","input":[1,[2]]}`), ErrInvalidRequest},
		{"batch and token", []byte(`{"model":"m","input":[[1],2]}`), ErrInvalidRequest},
		{"batch and text", []byte(`{"model":"m","input":[[1],"x"]}`), ErrInvalidRequest},
		{"empty token member", []byte(`{"model":"m","input":[[1],[]]}`), ErrInvalidRequest},
		{"nested token member", []byte(`{"model":"m","input":[[[1]]]}`), ErrInvalidRequest},
		{"boolean token", []byte(`{"model":"m","input":[true]}`), ErrInvalidRequest},
		{"null token", []byte(`{"model":"m","input":[[null]]}`), ErrInvalidRequest},
		{"duplicate with token", []byte(`{"model":"m","input":[1],"input":[2]}`), ErrInvalidRequest},
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

func TestTokenArrayResourceLimitsAndProgrammaticValidation(t *testing.T) {
	sequence := make([]int64, maxInputTokensPerItem+1)
	body, err := json.Marshal(map[string]any{"model": "m", "input": sequence})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRequest(body); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized token sequence: %v", err)
	}

	sequence = sequence[:maxInputTokensPerItem]
	batch := make([][]int64, maxTotalInputTokens/maxInputTokensPerItem+1)
	for index := range batch {
		batch[index] = sequence
	}
	body, err = json.Marshal(map[string]any{"model": "m", "input": batch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRequest(body); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized token batch: %v", err)
	}

	request := Request{Model: "m", Input: Input{tokens: []int64{0, maxTokenID}}, EncodingFormat: EncodingFloat}
	if err := ValidateRequest(request); err != nil {
		t.Fatal(err)
	}
	request.Input.tokens[1] = maxTokenID + 1
	if err := ValidateRequest(request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("programmatic overflow: %v", err)
	}
	request.Input.tokens[1] = 1
	request.Input.texts = []string{"mixed"}
	if err := ValidateRequest(request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("programmatic mixed shape: %v", err)
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
