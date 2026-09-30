// Package embeddingwire independently implements the bounded text and token-array,
// non-streaming float subset of OpenAI's embeddings wire format used by CPA
// Cloud. It is based on the public OpenAI Create embeddings reference checked
// on 2026-09-30 and deliberately does not claim full protocol compatibility.
// The package performs no I/O, authentication, routing, logging, or accounting.
package embeddingwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"unicode/utf8"
)

type EncodingFormat string

const EncodingFloat EncodingFormat = "float"

// Input preserves the four supported JSON shapes. Its slices are private so
// callers cannot mutate decoded text or token IDs through a copied value.
type Input struct {
	single     bool
	texts      []string
	tokens     []int64
	tokenBatch [][]int64
}

func (input Input) Count() int {
	if input.tokens != nil {
		return 1
	}
	if input.tokenBatch != nil {
		return len(input.tokenBatch)
	}
	return len(input.texts)
}

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
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '"' {
		text, err := requireJSONString(raw, ErrInvalidRequest, "input")
		if err != nil {
			return Input{}, err
		}
		return Input{single: true, texts: []string{text}}, nil
	}

	var members []json.RawMessage
	if json.Unmarshal(raw, &members) != nil || members == nil {
		return Input{}, invalid(ErrInvalidRequest, "input", "expected a supported input shape")
	}
	if len(members) == 0 {
		return Input{}, invalid(ErrInvalidRequest, "input", "array must not be empty")
	}
	if len(members) > maxInputItems {
		return Input{}, limited("input")
	}
	first := bytes.TrimSpace(members[0])
	switch first[0] {
	case '"':
		texts := make([]string, len(members))
		for index, member := range members {
			field := fmt.Sprintf("input[%d]", index)
			text, err := requireJSONString(member, ErrInvalidRequest, field)
			if err != nil {
				return Input{}, err
			}
			texts[index] = text
		}
		return Input{texts: texts}, nil
	case '[':
		batch := make([][]int64, len(members))
		total := 0
		for index, member := range members {
			sequence, err := decodeTokenSequence(member, fmt.Sprintf("input[%d]", index))
			if err != nil {
				return Input{}, err
			}
			if total > maxTotalInputTokens-len(sequence) {
				return Input{}, limited("input")
			}
			total += len(sequence)
			batch[index] = sequence
		}
		return Input{tokenBatch: batch}, nil
	default:
		if !isJSONIntegerStart(first[0]) {
			return Input{}, invalid(ErrInvalidRequest, "input", "expected a supported input shape")
		}
		sequence, err := decodeTokenSequence(raw, "input")
		if err != nil {
			return Input{}, err
		}
		return Input{tokens: sequence}, nil
	}
}

func isJSONIntegerStart(char byte) bool { return char == '-' || char >= '0' && char <= '9' }

func decodeTokenSequence(raw json.RawMessage, field string) ([]int64, error) {
	var members []json.RawMessage
	if json.Unmarshal(raw, &members) != nil || members == nil || len(members) == 0 {
		return nil, invalid(ErrInvalidRequest, field, "expected a non-empty token array")
	}
	if len(members) > maxInputTokensPerItem {
		return nil, limited(field)
	}
	tokens := make([]int64, len(members))
	for index, member := range members {
		rawID := bytes.TrimSpace(member)
		if len(rawID) == 0 || rawID[0] < '0' || rawID[0] > '9' {
			return nil, invalid(ErrInvalidRequest, field, "invalid token ID")
		}
		for _, char := range rawID {
			if char < '0' || char > '9' {
				return nil, invalid(ErrInvalidRequest, field, "invalid token ID")
			}
		}
		value, err := strconv.ParseInt(string(rawID), 10, 32)
		if err != nil {
			return nil, invalid(ErrInvalidRequest, field, "invalid token ID")
		}
		tokens[index] = value
	}
	return tokens, nil
}

func ValidateRequest(request Request) error {
	if err := validateModel(request.Model, "model", ErrInvalidRequest); err != nil {
		return err
	}
	if request.EncodingFormat != EncodingFloat {
		return invalid(ErrUnsupportedFeature, "encoding_format", "only float is supported")
	}
	if request.Input.tokens != nil || request.Input.tokenBatch != nil {
		if request.Input.single || request.Input.texts != nil || request.Input.tokens != nil && request.Input.tokenBatch != nil {
			return invalid(ErrInvalidRequest, "input", "mixed input shapes")
		}
		if request.Input.tokens != nil {
			return validateTokenSequence(request.Input.tokens, "input")
		}
		if len(request.Input.tokenBatch) == 0 {
			return invalid(ErrInvalidRequest, "input", "array must not be empty")
		}
		if len(request.Input.tokenBatch) > maxInputItems {
			return limited("input")
		}
		total := 0
		for index, sequence := range request.Input.tokenBatch {
			field := fmt.Sprintf("input[%d]", index)
			if err := validateTokenSequence(sequence, field); err != nil {
				return err
			}
			if total > maxTotalInputTokens-len(sequence) {
				return limited("input")
			}
			total += len(sequence)
		}
		return nil
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

func validateTokenSequence(tokens []int64, field string) error {
	if len(tokens) == 0 {
		return invalid(ErrInvalidRequest, field, "token array must not be empty")
	}
	if len(tokens) > maxInputTokensPerItem {
		return limited(field)
	}
	for _, token := range tokens {
		if token < 0 || token > maxTokenID {
			return invalid(ErrInvalidRequest, field, "invalid token ID")
		}
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
	switch {
	case request.Input.tokens != nil:
		input = append([]int64(nil), request.Input.tokens...)
	case request.Input.tokenBatch != nil:
		batch := make([][]int64, len(request.Input.tokenBatch))
		for index, sequence := range request.Input.tokenBatch {
			batch[index] = append([]int64(nil), sequence...)
		}
		input = batch
	case request.Input.single:
		input = request.Input.texts[0]
	default:
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
