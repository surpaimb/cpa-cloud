package accounting

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// These synthetic cases were independently authored from the repository's
// fixed-profile contract and do not call or contain real provider credentials.
func TestProveBoundProfileAcceptsOnlyFixedTextShape(t *testing.T) {
	marker := "正文-🔒-must-not-return"
	payload := []byte(fmt.Sprintf(`{
		"model":"gpt-4.1-2025-04-14",
		"messages":[
			{"role":"developer","content":"规则"},
			{"role":"system","content":""},
			{"role":"user","content":%q},
			{"role":"assistant","content":"answer"}
		],
		"max_completion_tokens":32768,
		"n":1,
		"modalities":["text"],
		"store":false,
		"stream":false
	}`, marker))
	original := append([]byte(nil), payload...)
	proof, err := ProveBoundProfile(validBoundProfileInput(payload))
	if err != nil {
		t.Fatal(err)
	}
	if proof.ProfileID != boundProfileID || proof.ProfileVersion != 1 || proof.Transform != boundProfileTransform {
		t.Fatalf("proof identity=%+v", proof)
	}
	if proof.UpperUsage != (MutuallyExclusiveInputUpperUsage{InputMax: 1_047_576, OutputMax: 32_768}) {
		t.Fatalf("upper usage=%+v", proof.UpperUsage)
	}
	if !bytes.Equal(payload, original) {
		t.Fatal("parser mutated final payload")
	}
	serialized, err := json.Marshal(proof)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(serialized, []byte(marker)) || bytes.Contains(bytes.ToLower(serialized), []byte("payload")) || bytes.Contains(bytes.ToLower(serialized), []byte("hash")) {
		t.Fatalf("proof retained request content: %s", serialized)
	}
	proofType := reflect.TypeOf(proof)
	if proofType.NumField() != 4 {
		t.Fatalf("proof fields=%d", proofType.NumField())
	}
}

func TestProveBoundProfileEndpointAndRouteIdentity(t *testing.T) {
	payload := validBoundProfilePayload("64")
	for _, endpoint := range []string{
		"https://api.openai.com/v1/chat/completions",
		"https://API.OPENAI.COM:443/v1/chat/completions",
	} {
		t.Run("valid "+endpoint, func(t *testing.T) {
			input := validBoundProfileInput(payload)
			input.Endpoint = endpoint
			if _, err := ProveBoundProfile(input); err != nil {
				t.Fatalf("error=%v", err)
			}
		})
	}

	tests := []struct {
		name   string
		mutate func(*BoundProfileInput)
	}{
		{name: "non service provider identity", mutate: func(input *BoundProfileInput) { input.Provider = ProviderOpenAI }},
		{name: "responses protocol", mutate: func(input *BoundProfileInput) { input.Protocol = ProtocolOpenAIResponses }},
		{name: "model alias", mutate: func(input *BoundProfileInput) { input.ActualModel = "gpt-4.1" }},
		{name: "http", mutate: func(input *BoundProfileInput) { input.Endpoint = "http://api.openai.com/v1/chat/completions" }},
		{name: "userinfo", mutate: func(input *BoundProfileInput) { input.Endpoint = "https://user@api.openai.com/v1/chat/completions" }},
		{name: "host suffix", mutate: func(input *BoundProfileInput) {
			input.Endpoint = "https://api.openai.com.evil.test/v1/chat/completions"
		}},
		{name: "trailing dot", mutate: func(input *BoundProfileInput) { input.Endpoint = "https://api.openai.com./v1/chat/completions" }},
		{name: "wrong port", mutate: func(input *BoundProfileInput) { input.Endpoint = "https://api.openai.com:444/v1/chat/completions" }},
		{name: "empty port", mutate: func(input *BoundProfileInput) { input.Endpoint = "https://api.openai.com:/v1/chat/completions" }},
		{name: "path suffix", mutate: func(input *BoundProfileInput) { input.Endpoint = "https://api.openai.com/v1/chat/completions/extra" }},
		{name: "escaped path", mutate: func(input *BoundProfileInput) { input.Endpoint = "https://api.openai.com/v1/chat/%63ompletions" }},
		{name: "query", mutate: func(input *BoundProfileInput) { input.Endpoint = "https://api.openai.com/v1/chat/completions?tenant=x" }},
		{name: "empty query", mutate: func(input *BoundProfileInput) { input.Endpoint = "https://api.openai.com/v1/chat/completions?" }},
		{name: "fragment", mutate: func(input *BoundProfileInput) { input.Endpoint = "https://api.openai.com/v1/chat/completions#x" }},
		{name: "whitespace", mutate: func(input *BoundProfileInput) { input.Endpoint = " https://api.openai.com/v1/chat/completions" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validBoundProfileInput(payload)
			test.mutate(&input)
			if proof, err := ProveBoundProfile(input); !errors.Is(err, ErrBoundProfileUnsupported) || proof != (BoundProfileProof{}) {
				t.Fatalf("proof=%+v error=%v", proof, err)
			}
		})
	}
}

func TestProveBoundProfileRejectsNonProfilePayloads(t *testing.T) {
	valid := string(validBoundProfilePayload("64"))
	const invalidMarker = "private-marker-do-not-echo"
	tests := []struct {
		name    string
		payload string
	}{
		{name: "empty", payload: ""},
		{name: "trailing json", payload: valid + `{}`},
		{name: "top array", payload: `[]`},
		{name: "duplicate top key", payload: strings.Replace(valid, `"model":"gpt-4.1-2025-04-14"`, `"model":"gpt-4.1-2025-04-14","model":"gpt-4.1-2025-04-14"`, 1)},
		{name: "missing model", payload: strings.Replace(valid, `"model":"gpt-4.1-2025-04-14",`, ``, 1)},
		{name: "payload model alias", payload: strings.Replace(valid, boundProfileModel, "gpt-4.1", 1)},
		{name: "client token count", payload: strings.Replace(valid, `"stream":false`, `"stream":false,"prompt_tokens":1`, 1)},
		{name: "private unknown value", payload: strings.Replace(valid, `"stream":false`, `"stream":false,"unknown":`+`"`+invalidMarker+`"`, 1)},
		{name: "unknown top", payload: strings.Replace(valid, `"stream":false`, `"stream":false,"temperature":0`, 1)},
		{name: "legacy max", payload: strings.Replace(valid, `"max_completion_tokens":64`, `"max_tokens":64`, 1)},
		{name: "missing n", payload: strings.Replace(valid, `,"n":1`, ``, 1)},
		{name: "multiple choices", payload: strings.Replace(valid, `"n":1`, `"n":2`, 1)},
		{name: "stream", payload: strings.Replace(valid, `"stream":false`, `"stream":true`, 1)},
		{name: "stored", payload: strings.Replace(valid, `"store":false`, `"store":true`, 1)},
		{name: "missing store", payload: strings.Replace(valid, `,"store":false`, ``, 1)},
		{name: "audio modality", payload: strings.Replace(valid, `["text"]`, `["text","audio"]`, 1)},
		{name: "modality string", payload: strings.Replace(valid, `["text"]`, `"text"`, 1)},
		{name: "tools", payload: strings.Replace(valid, `"stream":false`, `"stream":false,"tools":[]`, 1)},
		{name: "previous response", payload: strings.Replace(valid, `"stream":false`, `"stream":false,"previous_response_id":"resp_private"`, 1)},
		{name: "background", payload: strings.Replace(valid, `"stream":false`, `"stream":false,"background":true`, 1)},
		{name: "empty messages", payload: strings.Replace(valid, `[{"role":"user","content":"你好"}]`, `[]`, 1)},
		{name: "media content", payload: strings.Replace(valid, `"content":"你好"`, `"content":[{"type":"image_url","image_url":{"url":"https://private.invalid"}}]`, 1)},
		{name: "unknown message field", payload: strings.Replace(valid, `"content":"你好"`, `"content":"你好","name":"private"`, 1)},
		{name: "duplicate message field", payload: strings.Replace(valid, `"role":"user"`, `"role":"user","role":"assistant"`, 1)},
		{name: "tool role", payload: strings.Replace(valid, `"role":"user"`, `"role":"tool"`, 1)},
		{name: "null content", payload: strings.Replace(valid, `"content":"你好"`, `"content":null`, 1)},
		{name: "nested unknown", payload: strings.Replace(valid, `"content":"你好"`, `"content":{"text":"你好","unknown":1}`, 1)},
		{name: "float n", payload: strings.Replace(valid, `"n":1`, `"n":1.0`, 1)},
		{name: "exponent n", payload: strings.Replace(valid, `"n":1`, `"n":1e0`, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validBoundProfileInput([]byte(test.payload + " "))
			proof, err := ProveBoundProfile(input)
			if !errors.Is(err, ErrBoundProfileInvalid) || proof != (BoundProfileProof{}) {
				t.Fatalf("proof=%+v error=%v", proof, err)
			}
			if strings.Contains(err.Error(), invalidMarker) || err.Error() != ErrBoundProfileInvalid.Error() {
				t.Fatalf("non-fixed error=%q", err)
			}
		})
	}

	invalidUTF8 := append(validBoundProfilePayload("64"), 0xff)
	if _, err := ProveBoundProfile(validBoundProfileInput(invalidUTF8)); !errors.Is(err, ErrBoundProfileInvalid) {
		t.Fatalf("invalid UTF-8 error=%v", err)
	}
	oversized := bytes.Repeat([]byte{'x'}, boundProfilePayloadMax+1)
	if _, err := ProveBoundProfile(validBoundProfileInput(oversized)); !errors.Is(err, ErrBoundProfileInvalid) {
		t.Fatalf("oversized error=%v", err)
	}
}

func TestProveBoundProfileOutputBounds(t *testing.T) {
	for _, test := range []struct {
		value string
		want  int64
		valid bool
	}{
		{value: "1", want: 1, valid: true},
		{value: "32768", want: 32_768, valid: true},
		{value: "0"},
		{value: "32769"},
		{value: "-1"},
		{value: "1.0"},
		{value: "1e0"},
		{value: `"1"`},
	} {
		t.Run(test.value, func(t *testing.T) {
			proof, err := ProveBoundProfile(validBoundProfileInput(validBoundProfilePayload(test.value)))
			if test.valid {
				if err != nil || proof.UpperUsage.OutputMax != test.want || proof.UpperUsage.InputMax != boundProfileInputMax {
					t.Fatalf("proof=%+v error=%v", proof, err)
				}
				return
			}
			if !errors.Is(err, ErrBoundProfileInvalid) || proof != (BoundProfileProof{}) {
				t.Fatalf("proof=%+v error=%v", proof, err)
			}
		})
	}
}

func TestBoundProfileProofUsesMutuallyExclusiveInputArithmetic(t *testing.T) {
	proof, err := ProveBoundProfile(validBoundProfileInput(validBoundProfilePayload("32768")))
	if err != nil {
		t.Fatal(err)
	}
	if proof.UpperUsage.InputMax+proof.UpperUsage.OutputMax != 1_080_344 {
		t.Fatalf("token upper=%d", proof.UpperUsage.InputMax+proof.UpperUsage.OutputMax)
	}
	for _, test := range []struct {
		name       string
		input      int64
		cacheRead  int64
		cacheWrite int64
	}{
		{name: "ordinary highest", input: 17, cacheRead: 11, cacheWrite: 7},
		{name: "cache read highest", input: 7, cacheRead: 17, cacheWrite: 11},
		{name: "cache write highest", input: 11, cacheRead: 7, cacheWrite: 17},
	} {
		t.Run(test.name, func(t *testing.T) {
			price := PriceSnapshot{
				Version: "fixed-profile-price", Currency: "USD",
				InputPerMillionMicro: test.input, OutputPerMillionMicro: 19,
				CacheReadPerMillionMicro: test.cacheRead, CacheWritePerMillionMicro: test.cacheWrite,
			}
			tokens, cost, err := CalculateMutuallyExclusiveInputUpperBound(proof.UpperUsage, price)
			if err != nil {
				t.Fatal(err)
			}
			numerator := proof.UpperUsage.InputMax*17 + proof.UpperUsage.OutputMax*19
			wantCost := (numerator + 999_999) / 1_000_000
			if tokens != 1_080_344 || cost != wantCost {
				t.Fatalf("tokens=%d cost=%d wantCost=%d", tokens, cost, wantCost)
			}
		})
	}
}

func validBoundProfileInput(payload []byte) BoundProfileInput {
	return BoundProfileInput{
		Payload:     payload,
		Protocol:    ProtocolOpenAIChatCompletions,
		Provider:    ProviderOpenAICompatible,
		Endpoint:    "https://api.openai.com:443/v1/chat/completions",
		ActualModel: boundProfileModel,
	}
}

func validBoundProfilePayload(maximum string) []byte {
	return []byte(`{"model":"gpt-4.1-2025-04-14","messages":[{"role":"user","content":"你好"}],"max_completion_tokens":` + maximum + `,"n":1,"modalities":["text"],"store":false,"stream":false}`)
}
