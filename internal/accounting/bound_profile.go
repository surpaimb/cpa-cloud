package accounting

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strings"
	"unicode/utf8"
)

// This profile is an independently implemented, deliberately narrow parser for
// the official protocol subset recorded in the repository research contract.
// It performs no network access and retains no request content.
var (
	ErrBoundProfileUnsupported = errors.New("unsupported budget bound profile")
	ErrBoundProfileInvalid     = errors.New("invalid budget bound profile input")
)

const (
	boundProfileID               = "openai-chat-gpt-4.1-2025-04-14-text-v1"
	boundProfileVersion    int64 = 1
	boundProfileTransform        = "strict-final-json-v1"
	boundProfileModel            = "gpt-4.1-2025-04-14"
	boundProfileInputMax   int64 = 1_047_576
	boundProfileOutputMax  int64 = 32_768
	boundProfilePayloadMax       = 4 << 20
	boundProfileJSONDepth        = 16
)

// BoundProfileInput contains server-derived route identity and the exact final
// JSON bytes intended for dispatch. Payload must not come from a reconstructed
// or partially validated request.
type BoundProfileInput struct {
	Payload     []byte
	Protocol    UsageProtocol
	Provider    Provider
	Endpoint    string
	ActualModel string
}

// BoundProfileProof identifies the exact parser contract and its conservative
// capacity bounds. It intentionally contains no payload, payload hash, token
// estimate, or request field value.
type BoundProfileProof struct {
	ProfileID      string
	ProfileVersion int64
	Transform      string
	UpperUsage     MutuallyExclusiveInputUpperUsage
}

// ProveBoundProfile validates one fixed, text-only, non-streaming OpenAI Chat
// Completions profile. The bound is a conditional engineering capacity bound,
// not an unconditional provider billing guarantee.
func ProveBoundProfile(input BoundProfileInput) (BoundProfileProof, error) {
	if input.Provider != ProviderOpenAICompatible || input.Protocol != ProtocolOpenAIChatCompletions ||
		input.ActualModel != boundProfileModel || !isBoundProfileEndpoint(input.Endpoint) {
		return BoundProfileProof{}, ErrBoundProfileUnsupported
	}
	if len(input.Payload) == 0 || len(input.Payload) > boundProfilePayloadMax || !utf8.Valid(input.Payload) {
		return BoundProfileProof{}, ErrBoundProfileInvalid
	}

	value, err := decodeBoundProfileJSON(input.Payload)
	if err != nil || !validateBoundProfilePayload(value) {
		return BoundProfileProof{}, ErrBoundProfileInvalid
	}
	maximum, ok := integerValue(value.object["max_completion_tokens"])
	if !ok {
		return BoundProfileProof{}, ErrBoundProfileInvalid
	}
	return BoundProfileProof{
		ProfileID:      boundProfileID,
		ProfileVersion: boundProfileVersion,
		Transform:      boundProfileTransform,
		UpperUsage: MutuallyExclusiveInputUpperUsage{
			InputMax:  boundProfileInputMax,
			OutputMax: maximum,
		},
	}, nil
}

func isBoundProfileEndpoint(endpoint string) bool {
	if endpoint == "" || strings.TrimSpace(endpoint) != endpoint || strings.ContainsAny(endpoint, "?#") {
		return false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Opaque != "" || parsed.User != nil ||
		parsed.RawPath != "" || parsed.Path != "/v1/chat/completions" || parsed.RawQuery != "" ||
		parsed.ForceQuery || parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	if !strings.EqualFold(parsed.Hostname(), "api.openai.com") {
		return false
	}
	host := parsed.Host
	if !strings.EqualFold(host, "api.openai.com") && !strings.EqualFold(host, "api.openai.com:443") {
		return false
	}
	port := parsed.Port()
	return port == "" || port == "443"
}

type boundJSONKind uint8

const (
	boundJSONNull boundJSONKind = iota
	boundJSONBool
	boundJSONNumber
	boundJSONString
	boundJSONArray
	boundJSONObject
)

type boundJSONValue struct {
	kind    boundJSONKind
	boolean bool
	number  json.Number
	text    string
	array   []boundJSONValue
	object  map[string]boundJSONValue
}

func decodeBoundProfileJSON(payload []byte) (boundJSONValue, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	value, err := readBoundJSONValue(decoder, 0)
	if err != nil {
		return boundJSONValue{}, ErrBoundProfileInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return boundJSONValue{}, ErrBoundProfileInvalid
	}
	return value, nil
}

func readBoundJSONValue(decoder *json.Decoder, depth int) (boundJSONValue, error) {
	if depth > boundProfileJSONDepth {
		return boundJSONValue{}, ErrBoundProfileInvalid
	}
	token, err := decoder.Token()
	if err != nil {
		return boundJSONValue{}, ErrBoundProfileInvalid
	}
	switch typed := token.(type) {
	case nil:
		return boundJSONValue{kind: boundJSONNull}, nil
	case bool:
		return boundJSONValue{kind: boundJSONBool, boolean: typed}, nil
	case json.Number:
		return boundJSONValue{kind: boundJSONNumber, number: typed}, nil
	case string:
		return boundJSONValue{kind: boundJSONString, text: typed}, nil
	case json.Delim:
		switch typed {
		case '{':
			object := make(map[string]boundJSONValue)
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return boundJSONValue{}, ErrBoundProfileInvalid
				}
				key, ok := keyToken.(string)
				if !ok {
					return boundJSONValue{}, ErrBoundProfileInvalid
				}
				if _, duplicate := object[key]; duplicate {
					return boundJSONValue{}, ErrBoundProfileInvalid
				}
				value, err := readBoundJSONValue(decoder, depth+1)
				if err != nil {
					return boundJSONValue{}, err
				}
				object[key] = value
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim('}') {
				return boundJSONValue{}, ErrBoundProfileInvalid
			}
			return boundJSONValue{kind: boundJSONObject, object: object}, nil
		case '[':
			array := make([]boundJSONValue, 0)
			for decoder.More() {
				value, err := readBoundJSONValue(decoder, depth+1)
				if err != nil {
					return boundJSONValue{}, err
				}
				array = append(array, value)
			}
			closing, err := decoder.Token()
			if err != nil || closing != json.Delim(']') {
				return boundJSONValue{}, ErrBoundProfileInvalid
			}
			return boundJSONValue{kind: boundJSONArray, array: array}, nil
		default:
			return boundJSONValue{}, ErrBoundProfileInvalid
		}
	default:
		return boundJSONValue{}, ErrBoundProfileInvalid
	}
}

func validateBoundProfilePayload(root boundJSONValue) bool {
	if root.kind != boundJSONObject || !hasExactKeys(root.object,
		"model", "messages", "max_completion_tokens", "n", "modalities", "store", "stream") {
		return false
	}
	model, ok := stringValue(root.object["model"])
	if !ok || model != boundProfileModel {
		return false
	}
	maximum, ok := integerValue(root.object["max_completion_tokens"])
	if !ok || maximum < 1 || maximum > boundProfileOutputMax {
		return false
	}
	n, ok := integerValue(root.object["n"])
	if !ok || n != 1 {
		return false
	}
	if store, ok := boolValue(root.object["store"]); !ok || store {
		return false
	}
	if stream, ok := boolValue(root.object["stream"]); !ok || stream {
		return false
	}
	modalities := root.object["modalities"]
	if modalities.kind != boundJSONArray || len(modalities.array) != 1 {
		return false
	}
	modality, ok := stringValue(modalities.array[0])
	if !ok || modality != "text" {
		return false
	}
	messages := root.object["messages"]
	if messages.kind != boundJSONArray || len(messages.array) == 0 {
		return false
	}
	for _, message := range messages.array {
		if message.kind != boundJSONObject || !hasExactKeys(message.object, "role", "content") {
			return false
		}
		role, ok := stringValue(message.object["role"])
		if !ok || !validBoundProfileRole(role) {
			return false
		}
		if _, ok := stringValue(message.object["content"]); !ok {
			return false
		}
	}
	return true
}

func hasExactKeys(object map[string]boundJSONValue, keys ...string) bool {
	if len(object) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			return false
		}
	}
	return true
}

func stringValue(value boundJSONValue) (string, bool) {
	return value.text, value.kind == boundJSONString
}

func boolValue(value boundJSONValue) (bool, bool) {
	return value.boolean, value.kind == boundJSONBool
}

func integerValue(value boundJSONValue) (int64, bool) {
	if value.kind != boundJSONNumber {
		return 0, false
	}
	parsed, err := value.number.Int64()
	return parsed, err == nil
}

func validBoundProfileRole(role string) bool {
	switch role {
	case "developer", "system", "user", "assistant":
		return true
	default:
		return false
	}
}
