package embeddingwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestDecodeValidateAndMarshalResponseCanonicalizesIndexes(t *testing.T) {
	raw := []byte(`{
  "object":"list",
  "data":[
    {"object":"embedding","embedding":[0.3,0.4],"index":1},
    {"object":"embedding","embedding":[0.1,-0.2],"index":0}
  ],
  "model":"upstream-model",
  "usage":{"prompt_tokens":2,"total_tokens":2}
}`)
	response, err := DecodeResponse(raw)
	if err != nil {
		t.Fatal(err)
	}
	dimension, err := ValidateResponse(response, "upstream-model", 2)
	if err != nil || dimension != 2 {
		t.Fatalf("dimension=%d err=%v", dimension, err)
	}
	encoded, err := MarshalResponse(response, "public-model")
	if err != nil {
		t.Fatal(err)
	}
	if response.Data[0].Index != 1 || response.Data[1].Index != 0 {
		t.Fatalf("marshal mutated caller order: %#v", response.Data)
	}
	var wire struct {
		Object string `json:"object"`
		Model  string `json:"model"`
		Data   []struct {
			Object    string    `json:"object"`
			Embedding []float64 `json:"embedding"`
			Index     int       `json:"index"`
		} `json:"data"`
		Usage struct {
			PromptTokens int64 `json:"prompt_tokens"`
			TotalTokens  int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(encoded, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Object != "list" || wire.Model != "public-model" || len(wire.Data) != 2 || wire.Data[0].Index != 0 || wire.Data[1].Index != 1 {
		t.Fatalf("response is not canonical: %s", encoded)
	}
	if wire.Data[0].Object != "embedding" || wire.Usage.PromptTokens != 2 || wire.Usage.TotalTokens != 2 {
		t.Fatalf("response fields changed: %s", encoded)
	}
}

func TestDecodeResponseRejectsMalformedWire(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"not object", `[]`},
		{"duplicate root", `{"object":"list","object":"list","data":[],"model":"m"}`},
		{"unknown root", `{"object":"list","data":[],"model":"m","extra":1}`},
		{"wrong root object", `{"object":"embedding","data":[],"model":"m"}`},
		{"null data", `{"object":"list","data":null,"model":"m"}`},
		{"unknown item", `{"object":"list","data":[{"object":"embedding","embedding":[1],"index":0,"extra":1}],"model":"m"}`},
		{"duplicate item", `{"object":"list","data":[{"object":"embedding","embedding":[1],"index":0,"index":0}],"model":"m"}`},
		{"wrong item object", `{"object":"list","data":[{"object":"vector","embedding":[1],"index":0}],"model":"m"}`},
		{"base64 vector", `{"object":"list","data":[{"object":"embedding","embedding":"AAAA","index":0}],"model":"m"}`},
		{"fractional index", `{"object":"list","data":[{"object":"embedding","embedding":[1],"index":0.5}],"model":"m"}`},
		{"duplicate usage", `{"object":"list","data":[],"model":"m","usage":{"prompt_tokens":1,"prompt_tokens":1,"total_tokens":1}}`},
		{"unknown usage", `{"object":"list","data":[],"model":"m","usage":{"prompt_tokens":1,"total_tokens":1,"cached_tokens":0}}`},
		{"fractional usage", `{"object":"list","data":[],"model":"m","usage":{"prompt_tokens":1.5,"total_tokens":2}}`},
		{"unpaired model surrogate", `{"object":"list","data":[],"model":"\ud800"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := DecodeResponse([]byte(test.body))
			if !errors.Is(err, ErrInvalidResponse) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestValidateResponseRejectsUnsafeSemantics(t *testing.T) {
	valid := func() Response {
		return Response{
			Model: "upstream",
			Data: []Embedding{
				{Index: 0, Values: []float64{0.1, 0.2}},
				{Index: 1, Values: []float64{0.3, 0.4}},
			},
			Usage: &Usage{PromptTokens: 2, TotalTokens: 2},
		}
	}
	tests := []struct {
		name     string
		mutate   func(*Response)
		expected int
		model    string
		want     error
	}{
		{"model mismatch", func(*Response) {}, 2, "other", ErrInvalidResponse},
		{"missing item", func(r *Response) { r.Data = r.Data[:1] }, 2, "upstream", ErrInvalidResponse},
		{"duplicate index", func(r *Response) { r.Data[1].Index = 0 }, 2, "upstream", ErrInvalidResponse},
		{"out of range index", func(r *Response) { r.Data[1].Index = 2 }, 2, "upstream", ErrInvalidResponse},
		{"empty vector", func(r *Response) { r.Data[0].Values = nil }, 2, "upstream", ErrInvalidResponse},
		{"unequal dimension", func(r *Response) { r.Data[1].Values = []float64{1} }, 2, "upstream", ErrInvalidResponse},
		{"nan", func(r *Response) { r.Data[0].Values[0] = math.NaN() }, 2, "upstream", ErrInvalidResponse},
		{"positive infinity", func(r *Response) { r.Data[0].Values[0] = math.Inf(1) }, 2, "upstream", ErrInvalidResponse},
		{"negative infinity", func(r *Response) { r.Data[0].Values[0] = math.Inf(-1) }, 2, "upstream", ErrInvalidResponse},
		{"missing usage", func(r *Response) { r.Usage = nil }, 2, "upstream", ErrInvalidResponse},
		{"negative prompt usage", func(r *Response) { r.Usage.PromptTokens = -1 }, 2, "upstream", ErrInvalidResponse},
		{"negative total usage", func(r *Response) { r.Usage.TotalTokens = -1 }, 2, "upstream", ErrInvalidResponse},
		{"usage relation", func(r *Response) { r.Usage.PromptTokens = 3 }, 2, "upstream", ErrInvalidResponse},
		{"zero expected", func(*Response) {}, 0, "upstream", ErrInvalidResponse},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := valid()
			test.mutate(&response)
			_, err := ValidateResponse(response, test.model, test.expected)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}

func TestResponseUsageMissingRemainsUnknown(t *testing.T) {
	response, err := DecodeResponse([]byte(`{
  "object":"list",
  "data":[{"object":"embedding","embedding":[1],"index":0}],
  "model":"upstream"
}`))
	if err != nil {
		t.Fatal(err)
	}
	if response.Usage != nil {
		t.Fatalf("missing usage was synthesized: %#v", response.Usage)
	}
	if _, err := ValidateResponse(response, "upstream", 1); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("missing usage validated: %v", err)
	}
}

func TestResponseResourceLimits(t *testing.T) {
	oversizedBody := bytes.Repeat([]byte{' '}, MaxResponseBytes+1)
	if _, err := DecodeResponse(oversizedBody); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized response body: %v", err)
	}

	tooWide := Response{
		Model: "m",
		Data:  []Embedding{{Index: 0, Values: make([]float64, maxVectorDimension+1)}},
		Usage: &Usage{},
	}
	if _, err := ValidateResponse(tooWide, "m", 1); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized dimension: %v", err)
	}

	shared := make([]float64, maxVectorDimension)
	count := maxTotalValues/maxVectorDimension + 1
	tooMany := Response{Model: "m", Data: make([]Embedding, count), Usage: &Usage{}}
	for index := range tooMany.Data {
		tooMany.Data[index] = Embedding{Index: index, Values: shared}
	}
	if _, err := ValidateResponse(tooMany, "m", count); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("oversized total values: %v", err)
	}

	items := strings.Repeat(`{"object":"embedding","embedding":[1],"index":0},`, maxInputItems)
	items += `{"object":"embedding","embedding":[1],"index":0}`
	body := `{"object":"list","data":[` + items + `],"model":"m"}`
	if _, err := DecodeResponse([]byte(body)); !errors.Is(err, ErrLimitExceeded) {
		t.Fatalf("too many response items: %v", err)
	}
}

func TestMarshalResponseRejectsInvalidAndCopiesValues(t *testing.T) {
	values := []float64{0.1, 0.2}
	response := Response{
		Model: "upstream",
		Data:  []Embedding{{Index: 0, Values: values}},
		Usage: &Usage{PromptTokens: 1, TotalTokens: 1},
	}
	encoded, err := MarshalResponse(response, "public")
	if err != nil {
		t.Fatal(err)
	}
	values[0] = 99
	if strings.Contains(string(encoded), "99") {
		t.Fatalf("encoded response retained caller storage: %s", encoded)
	}
	if _, err := MarshalResponse(response, ""); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("empty public model: %v", err)
	}
	response.Usage = nil
	if _, err := MarshalResponse(response, "public"); !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("missing usage marshaled: %v", err)
	}
}
