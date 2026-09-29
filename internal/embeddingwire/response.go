package embeddingwire

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

type Embedding struct {
	Index  int
	Values []float64
}

type Usage struct {
	PromptTokens int64
	TotalTokens  int64
}

type Response struct {
	Model string
	Data  []Embedding
	Usage *Usage
}

var responseFields = map[string]bool{
	"object": true, "data": true, "model": true, "usage": true,
}

var embeddingFields = map[string]bool{
	"object": true, "embedding": true, "index": true,
}

var usageFields = map[string]bool{
	"prompt_tokens": true, "total_tokens": true,
}

func DecodeResponse(raw []byte) (Response, error) {
	if err := checkJSON(raw, MaxResponseBytes, ErrInvalidResponse); err != nil {
		return Response{}, err
	}
	root, err := decodeObject(raw, ErrInvalidResponse, "body")
	if err != nil {
		return Response{}, err
	}
	if err := rejectResponseUnknown(root, responseFields, ""); err != nil {
		return Response{}, err
	}
	object, err := requireJSONString(root["object"], ErrInvalidResponse, "object")
	if err != nil || object != "list" {
		return Response{}, invalid(ErrInvalidResponse, "object", "expected list")
	}
	model, err := requireJSONString(root["model"], ErrInvalidResponse, "model")
	if err != nil {
		return Response{}, err
	}

	var rawItems []json.RawMessage
	if len(root["data"]) == 0 || json.Unmarshal(root["data"], &rawItems) != nil || rawItems == nil {
		return Response{}, invalid(ErrInvalidResponse, "data", "expected an array")
	}
	if len(rawItems) > maxInputItems {
		return Response{}, limited("data")
	}
	data := make([]Embedding, len(rawItems))
	totalValues := 0
	for index, rawItem := range rawItems {
		field := fmt.Sprintf("data[%d]", index)
		item, err := decodeObject(rawItem, ErrInvalidResponse, field)
		if err != nil {
			return Response{}, err
		}
		if err := rejectResponseUnknown(item, embeddingFields, field); err != nil {
			return Response{}, err
		}
		kind, err := requireJSONString(item["object"], ErrInvalidResponse, field+".object")
		if err != nil || kind != "embedding" {
			return Response{}, invalid(ErrInvalidResponse, field+".object", "expected embedding")
		}
		var wireIndex int
		if len(item["index"]) == 0 || json.Unmarshal(item["index"], &wireIndex) != nil {
			return Response{}, invalid(ErrInvalidResponse, field+".index", "expected an integer")
		}
		var values []float64
		if len(item["embedding"]) == 0 || json.Unmarshal(item["embedding"], &values) != nil || values == nil {
			return Response{}, invalid(ErrInvalidResponse, field+".embedding", "expected a float array")
		}
		if len(values) > maxVectorDimension {
			return Response{}, limited(field + ".embedding")
		}
		if totalValues > maxTotalValues-len(values) {
			return Response{}, limited("data.embedding")
		}
		totalValues += len(values)
		data[index] = Embedding{Index: wireIndex, Values: append([]float64(nil), values...)}
	}

	var usage *Usage
	if rawUsage, present := root["usage"]; present {
		parsed, err := decodeUsage(rawUsage)
		if err != nil {
			return Response{}, err
		}
		usage = &parsed
	}
	return Response{Model: model, Data: data, Usage: usage}, nil
}

func decodeUsage(raw json.RawMessage) (Usage, error) {
	object, err := decodeObject(raw, ErrInvalidResponse, "usage")
	if err != nil {
		return Usage{}, err
	}
	if err := rejectResponseUnknown(object, usageFields, "usage"); err != nil {
		return Usage{}, err
	}
	var usage Usage
	if len(object["prompt_tokens"]) == 0 || json.Unmarshal(object["prompt_tokens"], &usage.PromptTokens) != nil {
		return Usage{}, invalid(ErrInvalidResponse, "usage.prompt_tokens", "expected an integer")
	}
	if len(object["total_tokens"]) == 0 || json.Unmarshal(object["total_tokens"], &usage.TotalTokens) != nil {
		return Usage{}, invalid(ErrInvalidResponse, "usage.total_tokens", "expected an integer")
	}
	return usage, nil
}

func rejectResponseUnknown(object map[string]json.RawMessage, allowed map[string]bool, prefix string) error {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !allowed[key] {
			if prefix != "" {
				key = prefix + "." + key
			}
			return invalid(ErrInvalidResponse, key, "unknown field")
		}
	}
	return nil
}

func ValidateResponse(response Response, expectedUpstreamModel string, expectedInputs int) (int, error) {
	if err := validateModel(expectedUpstreamModel, "expected_upstream_model", ErrInvalidResponse); err != nil {
		return 0, err
	}
	if err := validateModel(response.Model, "model", ErrInvalidResponse); err != nil {
		return 0, err
	}
	if response.Model != expectedUpstreamModel {
		return 0, invalid(ErrInvalidResponse, "model", "does not match the selected upstream model")
	}
	if expectedInputs <= 0 {
		return 0, invalid(ErrInvalidResponse, "data", "expected input count must be positive")
	}
	if expectedInputs > maxInputItems {
		return 0, limited("data")
	}
	if len(response.Data) != expectedInputs {
		return 0, invalid(ErrInvalidResponse, "data", "item count does not match the request")
	}

	seen := make([]bool, expectedInputs)
	dimension := 0
	totalValues := 0
	for position, embedding := range response.Data {
		field := fmt.Sprintf("data[%d]", position)
		if embedding.Index < 0 || embedding.Index >= expectedInputs {
			return 0, invalid(ErrInvalidResponse, field+".index", "index is outside the request range")
		}
		if seen[embedding.Index] {
			return 0, invalid(ErrInvalidResponse, field+".index", "duplicate index")
		}
		seen[embedding.Index] = true
		if len(embedding.Values) == 0 {
			return 0, invalid(ErrInvalidResponse, field+".embedding", "vector must not be empty")
		}
		if len(embedding.Values) > maxVectorDimension {
			return 0, limited(field + ".embedding")
		}
		if dimension == 0 {
			dimension = len(embedding.Values)
		} else if len(embedding.Values) != dimension {
			return 0, invalid(ErrInvalidResponse, field+".embedding", "vector dimensions differ")
		}
		if totalValues > maxTotalValues-len(embedding.Values) {
			return 0, limited("data.embedding")
		}
		totalValues += len(embedding.Values)
		for _, value := range embedding.Values {
			if math.IsNaN(value) || math.IsInf(value, 0) {
				return 0, invalid(ErrInvalidResponse, field+".embedding", "vector values must be finite")
			}
		}
	}
	for index, present := range seen {
		if !present {
			return 0, invalid(ErrInvalidResponse, fmt.Sprintf("data[%d]", index), "embedding is missing")
		}
	}
	if response.Usage == nil {
		return 0, invalid(ErrInvalidResponse, "usage", "usage is required")
	}
	if response.Usage.PromptTokens < 0 || response.Usage.TotalTokens < 0 {
		return 0, invalid(ErrInvalidResponse, "usage", "token counts must be non-negative")
	}
	if response.Usage.TotalTokens < response.Usage.PromptTokens {
		return 0, invalid(ErrInvalidResponse, "usage", "total_tokens must be at least prompt_tokens")
	}
	return dimension, nil
}

func MarshalResponse(response Response, publicModel string) ([]byte, error) {
	if _, err := ValidateResponse(response, response.Model, len(response.Data)); err != nil {
		return nil, err
	}
	if err := validateModel(publicModel, "public_model", ErrInvalidResponse); err != nil {
		return nil, err
	}

	data := make([]wireEmbedding, len(response.Data))
	for index, embedding := range response.Data {
		data[index] = wireEmbedding{
			Object: "embedding", Index: embedding.Index,
			Embedding: append([]float64(nil), embedding.Values...),
		}
	}
	sort.Slice(data, func(i, j int) bool { return data[i].Index < data[j].Index })
	usage := wireUsage{PromptTokens: response.Usage.PromptTokens, TotalTokens: response.Usage.TotalTokens}
	wire := wireResponse{Object: "list", Data: data, Model: publicModel, Usage: usage}
	encoded, err := json.Marshal(wire)
	if err != nil {
		return nil, invalid(ErrInvalidResponse, "body", "could not encode response")
	}
	if len(encoded) > MaxResponseBytes {
		return nil, limited("body")
	}
	return encoded, nil
}

type wireEmbedding struct {
	Object    string    `json:"object"`
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type wireUsage struct {
	PromptTokens int64 `json:"prompt_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
}

type wireResponse struct {
	Object string          `json:"object"`
	Data   []wireEmbedding `json:"data"`
	Model  string          `json:"model"`
	Usage  wireUsage       `json:"usage"`
}
