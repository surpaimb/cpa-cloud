package accounting

// Independently authored from the public OpenAI Chat usage contract and the
// earlier-model cache billing rule. See budget-bound-profile-feasibility.md.
import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func parseGPT41SnapshotUsage(data []byte) (*Usage, error) {
	root, err := uniqueUsageObject(data)
	if err != nil {
		return nil, err
	}
	if isErrorObject(root) {
		return nil, nil
	}
	model, present, err := stringField(root, "model")
	if err != nil || !present || model != "gpt-4.1-2025-04-14" {
		return nil, ErrInvalidUsage
	}
	raw, present := root["usage"]
	if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	usage, err := uniqueUsageObject(raw)
	if err != nil {
		return nil, err
	}
	for _, name := range []string{"prompt_tokens_details", "completion_tokens_details"} {
		raw, present := usage[name]
		if !present || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			continue
		}
		details, err := uniqueUsageObject(raw)
		if err != nil {
			return nil, err
		}
		if name == "prompt_tokens_details" {
			// Only absence takes this versioned billing classification. Explicit
			// null remains unknown; nonzero separate writes contradict the profile.
			write, exists, err := integerField(details, "cache_write_tokens")
			if err != nil || write != nil && *write != 0 {
				return nil, ErrInvalidUsage
			}
			if !exists {
				details["cache_write_tokens"] = json.RawMessage("0")
			}
			usage[name], err = json.Marshal(details)
			if err != nil {
				return nil, ErrInvalidUsage
			}
		} else {
			reasoning, _, err := integerField(details, "reasoning_tokens")
			if err != nil || reasoning != nil && *reasoning != 0 {
				return nil, ErrInvalidUsage
			}
		}
	}
	return normalizeOpenAI(usage, "prompt_tokens", "completion_tokens", "prompt_tokens_details", "completion_tokens_details")
}

// This decoder rejects ambiguous duplicate keys in the billing objects. Values
// outside those objects are forwarded by the executor and are never retained.
func uniqueUsageObject(data []byte) (jsonObject, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalidUsage
	}
	object := jsonObject{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, ErrInvalidUsage
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrInvalidUsage
		}
		if _, exists := object[key]; exists {
			return nil, ErrInvalidUsage
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrInvalidUsage
		}
		object[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrInvalidUsage
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidUsage
	}
	return object, nil
}
