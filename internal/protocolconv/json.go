package protocolconv

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const maxJSONDepth = 64

func decodeObject(raw []byte, kind ErrorCode, field string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]json.RawMessage
	if err := decoder.Decode(&value); err != nil || value == nil {
		if kind == CodeInvalidUpstream {
			return nil, invalidUpstream(field, "expected a JSON object")
		}
		return nil, invalid(field, "expected a JSON object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if kind == CodeInvalidUpstream {
			return nil, invalidUpstream(field, "expected one JSON value")
		}
		return nil, invalid(field, "expected one JSON value")
	}
	return value, nil
}

func rejectUnknown(object map[string]json.RawMessage, allowed map[string]bool, prefix string) error {
	for key := range object {
		if !allowed[key] {
			return unsupported(joinField(prefix, key))
		}
	}
	return nil
}

func joinField(prefix, field string) string {
	if prefix == "" {
		return field
	}
	if strings.HasPrefix(field, "[") {
		return prefix + field
	}
	return prefix + "." + field
}

func indexField(prefix string, index int) string {
	return fmt.Sprintf("%s[%d]", prefix, index)
}

func requireString(raw json.RawMessage, field string, upstream bool) (string, error) {
	var value string
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		if upstream {
			return "", invalidUpstream(field, "expected a string")
		}
		return "", invalid(field, "expected a string")
	}
	return value, nil
}

func optionalBool(object map[string]json.RawMessage, field string) (bool, bool, error) {
	raw, ok := object[field]
	if !ok {
		return false, false, nil
	}
	var value bool
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil {
		return false, true, invalid(field, "expected a boolean")
	}
	return value, true, nil
}

func marshal(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, invalid("", "could not encode converted request")
	}
	return data, nil
}

func cloneRaw(value json.RawMessage) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
