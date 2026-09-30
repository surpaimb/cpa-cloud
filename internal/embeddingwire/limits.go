package embeddingwire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

// The byte limits are local memory-safety limits. They are not token limits
// and must not be described as provider model limits.
const (
	MaxRequestBytes  = 4 << 20
	MaxResponseBytes = 32 << 20

	maxJSONDepth          = 16
	maxModelBytes         = 512
	maxInputItems         = 2048
	maxInputTextBytes     = 1 << 20
	maxInputTokensPerItem = 2048
	maxTotalInputTokens   = 65536
	maxTokenID            = 2147483647
	maxVectorDimension    = 1 << 16
	maxTotalValues        = 1 << 20
)

var (
	ErrInvalidRequest     = errors.New("invalid embedding request")
	ErrUnsupportedFeature = errors.New("unsupported embedding feature")
	ErrInvalidResponse    = errors.New("invalid embedding response")
	ErrLimitExceeded      = errors.New("embedding wire limit exceeded")
)

func invalid(base error, field, message string) error {
	if field == "" {
		return fmt.Errorf("%w: %s", base, message)
	}
	return fmt.Errorf("%w: %s: %s", base, field, message)
}

func limited(field string) error {
	if field == "" {
		return ErrLimitExceeded
	}
	return fmt.Errorf("%w: %s", ErrLimitExceeded, field)
}

// checkJSON validates one bounded JSON value before any typed decoding. The
// token walk supplies duplicate-key and nesting checks that encoding/json's
// struct and map decoders do not provide.
func checkJSON(raw []byte, maximum int, base error) error {
	if len(raw) > maximum {
		return limited("body")
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return invalid(base, "body", "one JSON value is required")
	}
	if !utf8.Valid(raw) {
		return invalid(base, "body", "JSON must be valid UTF-8")
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanJSONValue(decoder, 1, base); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return invalid(base, "body", "exactly one JSON value is required")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder, depth int, base error) error {
	if depth > maxJSONDepth {
		return limited("json_depth")
	}
	token, err := decoder.Token()
	if err != nil {
		return invalid(base, "body", "malformed JSON")
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}

	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return invalid(base, "body", "malformed JSON object")
			}
			key, ok := keyToken.(string)
			if !ok {
				return invalid(base, "body", "malformed JSON object")
			}
			if _, duplicate := seen[key]; duplicate {
				return invalid(base, key, "duplicate field")
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder, depth+1, base); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return invalid(base, "body", "malformed JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder, depth+1, base); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return invalid(base, "body", "malformed JSON array")
		}
	default:
		return invalid(base, "body", "malformed JSON")
	}
	return nil
}

func decodeObject(raw json.RawMessage, base error, field string) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &object) != nil || object == nil {
		return nil, invalid(base, field, "expected an object")
	}
	return object, nil
}

func requireJSONString(raw json.RawMessage, base error, field string) (string, error) {
	if len(raw) == 0 || !validJSONStringSurrogates(raw) {
		return "", invalid(base, field, "expected a valid string")
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", invalid(base, field, "expected a string")
	}
	return value, nil
}

// validJSONStringSurrogates rejects an unpaired escaped UTF-16 surrogate.
// encoding/json otherwise replaces one with U+FFFD, silently changing input.
func validJSONStringSurrogates(raw []byte) bool {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return false
	}
	for index := 1; index < len(raw)-1; index++ {
		if raw[index] != '\\' {
			continue
		}
		index++
		if index >= len(raw)-1 {
			return false
		}
		if raw[index] != 'u' {
			continue
		}
		first, ok := parseHex16(raw, index+1)
		if !ok {
			return false
		}
		index += 4
		if first >= 0xd800 && first <= 0xdbff {
			if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
				return false
			}
			second, ok := parseHex16(raw, index+3)
			if !ok || second < 0xdc00 || second > 0xdfff || utf16.DecodeRune(rune(first), rune(second)) == utf8.RuneError {
				return false
			}
			index += 6
		} else if first >= 0xdc00 && first <= 0xdfff {
			return false
		}
	}
	return true
}

func parseHex16(raw []byte, start int) (uint16, bool) {
	if start < 0 || start+4 > len(raw) {
		return 0, false
	}
	var value uint16
	for _, char := range raw[start : start+4] {
		value <<= 4
		switch {
		case char >= '0' && char <= '9':
			value += uint16(char - '0')
		case char >= 'a' && char <= 'f':
			value += uint16(char-'a') + 10
		case char >= 'A' && char <= 'F':
			value += uint16(char-'A') + 10
		default:
			return 0, false
		}
	}
	return value, true
}

func validateModel(value, field string, base error) error {
	if value == "" || !utf8.ValidString(value) {
		return invalid(base, field, "a non-empty UTF-8 string is required")
	}
	if len([]byte(value)) > maxModelBytes {
		return limited(field)
	}
	return nil
}
