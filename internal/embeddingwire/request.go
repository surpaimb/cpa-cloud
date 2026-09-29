// Package embeddingwire independently implements the bounded, text-only,
// non-streaming float subset of OpenAI's embeddings wire format used by CPA
// Cloud. It is based on the public OpenAI Create embeddings reference checked
// on 2026-09-29 and deliberately does not claim full protocol compatibility.
// The package performs no I/O, authentication, routing, logging, or accounting.
package embeddingwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"unicode/utf8"
)

type EncodingFormat string

const EncodingFloat EncodingFormat = "float"

// Input preserves whether the client supplied one string or an array. Its
// contents are private so a copied value cannot expose a mutable text slice.
type Input struct {
	single bool
	texts  []string
}

func (input Input) Count() int { return len(input.texts) }

type Request struct {
	Model          string
	Input          Input
	EncodingFormat EncodingFormat
}

var requestFields = map[string]bool{
	"model": true, "input": true, "encoding_format": true,
}

var explicitlyUnsupportedRequestFields = map[string]bool{
	"dimensions": true, "user": true, "stream": true,
}

func DecodeRequest(raw []byte) (Request, error) {
	if err := checkJSON(raw, MaxRequestBytes, ErrInvalidRequest); err != nil {
		return Request{}, err
	}
	root, err := decodeObject(raw, ErrInvalidRequest, "body")
	if err != nil {
		return Request{}, err
	}
	keys := make([]string, 0, len(root))
	for key := range root {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if requestFields[key] {
			continue
		}
		if explicitlyUnsupportedRequestFields[key] {
			return Request{}, invalid(ErrUnsupportedFeature, key, "field is outside the supported subset")
		}
		return Request{}, invalid(ErrUnsupportedFeature, key, "unknown field")
	}

	model, err := requireJSONString(root["model"], ErrInvalidRequest, "model")
	if err != nil {
		return Request{}, err
	}
	input, err := decodeInput(root["input"])
	if err != nil {
		return Request{}, err
	}
	format := EncodingFloat
	if rawFormat, present := root["encoding_format"]; present {
		value, err := requireJSONString(rawFormat, ErrInvalidRequest, "encoding_format")
		if err != nil {
			return Request{}, err
		}
		if value != string(EncodingFloat) {
			return Request{}, invalid(ErrUnsupportedFeature, "encoding_format", "only float is supported")
		}
	}

	request := Request{Model: model, Input: input, EncodingFormat: format}
	if err := ValidateRequest(request); err != nil {
		return Request{}, err
	}
	return request, nil
}

func decodeInput(raw json.RawMessage) (Input, error) {
	if len(raw) == 0 {
		return Input{}, invalid(ErrInvalidRequest, "input", "field is required")
	}
	if text, err := requireJSONString(raw, ErrInvalidRequest, "input"); err == nil {
		return Input{single: true, texts: []string{text}}, nil
	}

	var members []json.RawMessage
	if json.Unmarshal(raw, &members) != nil || members == nil {
		return Input{}, invalid(ErrInvalidRequest, "input", "expected a string or string array")
	}
	if len(members) > maxInputItems {
		return Input{}, limited("input")
	}
	texts := make([]string, len(members))
	for index, member := range members {
		field := fmt.Sprintf("input[%d]", index)
		text, err := requireJSONString(member, ErrInvalidRequest, field)
		if err != nil {
			trimmed := bytes.TrimSpace(member)
			if len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '-' || (trimmed[0] >= '0' && trimmed[0] <= '9')) {
				return Input{}, invalid(ErrUnsupportedFeature, "input", "token arrays are outside the supported subset")
			}
			return Input{}, err
		}
		texts[index] = text
	}
	return Input{single: false, texts: texts}, nil
}

func ValidateRequest(request Request) error {
	if err := validateModel(request.Model, "model", ErrInvalidRequest); err != nil {
		return err
	}
	if request.EncodingFormat != EncodingFloat {
		return invalid(ErrUnsupportedFeature, "encoding_format", "only float is supported")
	}
	if len(request.Input.texts) == 0 {
		return invalid(ErrInvalidRequest, "input", "a non-empty string or string array is required")
	}
	if len(request.Input.texts) > maxInputItems {
		return limited("input")
	}
	if request.Input.single && len(request.Input.texts) != 1 {
		return invalid(ErrInvalidRequest, "input", "invalid single-string value")
	}
	totalTextBytes := 0
	for index, text := range request.Input.texts {
		field := fmt.Sprintf("input[%d]", index)
		if text == "" || !utf8.ValidString(text) {
			return invalid(ErrInvalidRequest, field, "a non-empty UTF-8 string is required")
		}
		textBytes := len([]byte(text))
		if textBytes > maxInputTextBytes {
			return limited(field)
		}
		if totalTextBytes > MaxRequestBytes-textBytes {
			return limited("input")
		}
		totalTextBytes += textBytes
	}
	return nil
}

func MarshalRequest(request Request, upstreamModel string) ([]byte, error) {
	if err := ValidateRequest(request); err != nil {
		return nil, err
	}
	if err := validateModel(upstreamModel, "upstream_model", ErrInvalidRequest); err != nil {
		return nil, err
	}

	var input any
	if request.Input.single {
		input = request.Input.texts[0]
	} else {
		input = append([]string(nil), request.Input.texts...)
	}
	wire := struct {
		Model          string         `json:"model"`
		Input          any            `json:"input"`
		EncodingFormat EncodingFormat `json:"encoding_format"`
	}{Model: upstreamModel, Input: input, EncodingFormat: EncodingFloat}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, invalid(ErrInvalidRequest, "body", "could not encode request")
	}
	if len(encoded) > MaxRequestBytes {
		return nil, limited("body")
	}
	return encoded, nil
}
