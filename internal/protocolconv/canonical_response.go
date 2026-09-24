package protocolconv

import (
	"bytes"
	"encoding/json"
)

type canonicalOutput struct {
	ID         string
	Model      string
	Status     string
	Incomplete string
	Parts      []canonicalOutputPart
	Usage      *canonicalUsage
}

type canonicalOutputPart struct {
	Text *string
	Call *canonicalCall
}

type canonicalCall struct {
	ID        string
	Name      string
	Arguments string
}

type canonicalUsage struct {
	Input      int64
	Output     int64
	Total      int64
	Cached     int64
	CacheWrite int64
	Reasoning  int64
}

func parseResponsesOutput(raw []byte) (canonicalOutput, error) {
	root, err := decodeObject(raw, CodeInvalidUpstream, "")
	if err != nil {
		return canonicalOutput{}, err
	}
	if err := rejectUnknown(root, map[string]bool{
		"id": true, "object": true, "created_at": true, "completed_at": true, "model": true,
		"status": true, "output": true, "usage": true, "background": true, "store": true,
		"error": true, "incomplete_details": true, "previous_response_id": true, "conversation": true,
	}, ""); err != nil {
		return canonicalOutput{}, err
	}
	id, err := nonEmptyString(root["id"], "id", true)
	if err != nil {
		return canonicalOutput{}, err
	}
	object, err := requireString(root["object"], "object", true)
	if err != nil || object != "response" {
		return canonicalOutput{}, invalidUpstream("object", "expected response")
	}
	model, err := nonEmptyString(root["model"], "model", true)
	if err != nil {
		return canonicalOutput{}, err
	}
	status, err := requireString(root["status"], "status", true)
	if err != nil || (status != "completed" && status != "incomplete") {
		return canonicalOutput{}, unsupported("status")
	}
	for _, field := range []string{"background", "store"} {
		if value, present, err := optionalUpstreamBool(root, field); err != nil {
			return canonicalOutput{}, err
		} else if present && value {
			return canonicalOutput{}, invalidUpstream(field, "stateful response cannot be converted")
		}
	}
	for _, field := range []string{"previous_response_id", "conversation", "error"} {
		if value, ok := root[field]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return canonicalOutput{}, invalidUpstream(field, "field is not representable")
		}
	}
	result := canonicalOutput{ID: id, Model: model, Status: status}
	if status == "incomplete" {
		details, err := decodeObject(root["incomplete_details"], CodeInvalidUpstream, "incomplete_details")
		if err != nil {
			return canonicalOutput{}, err
		}
		if err := rejectUnknown(details, map[string]bool{"reason": true}, "incomplete_details"); err != nil {
			return canonicalOutput{}, err
		}
		reason, err := requireString(details["reason"], "incomplete_details.reason", true)
		if err != nil || reason != "max_output_tokens" {
			return canonicalOutput{}, unsupported("incomplete_details.reason")
		}
		result.Incomplete = reason
	} else if value, ok := root["incomplete_details"]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return canonicalOutput{}, invalidUpstream("incomplete_details", "completed response has incomplete details")
	}
	var output []json.RawMessage
	if json.Unmarshal(root["output"], &output) != nil || output == nil {
		return canonicalOutput{}, invalidUpstream("output", "an array is required")
	}
	for index, rawItem := range output {
		field := indexField("output", index)
		item, err := decodeObject(rawItem, CodeInvalidUpstream, field)
		if err != nil {
			return canonicalOutput{}, err
		}
		kind, err := requireString(item["type"], joinField(field, "type"), true)
		if err != nil {
			return canonicalOutput{}, err
		}
		switch kind {
		case "message":
			if err := rejectUnknown(item, map[string]bool{"id": true, "type": true, "status": true, "role": true, "content": true, "phase": true}, field); err != nil {
				return canonicalOutput{}, err
			}
			role, err := requireString(item["role"], joinField(field, "role"), true)
			if err != nil || role != "assistant" {
				return canonicalOutput{}, invalidUpstream(joinField(field, "role"), "expected assistant")
			}
			text, err := responseContentText(item["content"], joinField(field, "content"), true)
			if err != nil {
				return canonicalOutput{}, err
			}
			result.Parts = append(result.Parts, canonicalOutputPart{Text: &text})
		case "function_call":
			if err := rejectUnknown(item, map[string]bool{"id": true, "type": true, "status": true, "call_id": true, "name": true, "arguments": true}, field); err != nil {
				return canonicalOutput{}, err
			}
			callID, err := nonEmptyString(item["call_id"], joinField(field, "call_id"), true)
			if err != nil {
				return canonicalOutput{}, err
			}
			name, err := nonEmptyString(item["name"], joinField(field, "name"), true)
			if err != nil {
				return canonicalOutput{}, err
			}
			arguments, err := requireString(item["arguments"], joinField(field, "arguments"), true)
			if err != nil {
				return canonicalOutput{}, err
			}
			if _, err := rawJSONObject(json.RawMessage(arguments), joinField(field, "arguments"), true); err != nil {
				return canonicalOutput{}, err
			}
			call := canonicalCall{ID: callID, Name: name, Arguments: arguments}
			result.Parts = append(result.Parts, canonicalOutputPart{Call: &call})
		default:
			return canonicalOutput{}, unsupported(joinField(field, "type"))
		}
	}
	if rawUsage, ok := root["usage"]; ok && !bytes.Equal(bytes.TrimSpace(rawUsage), []byte("null")) {
		usage, err := parseResponsesCanonicalUsage(rawUsage)
		if err != nil {
			return canonicalOutput{}, err
		}
		result.Usage = &usage
	}
	return result, nil
}

func parseResponsesCanonicalUsage(raw json.RawMessage) (canonicalUsage, error) {
	root, err := decodeObject(raw, CodeInvalidUpstream, "usage")
	if err != nil {
		return canonicalUsage{}, err
	}
	if err := rejectUnknown(root, map[string]bool{"input_tokens": true, "output_tokens": true, "total_tokens": true, "input_tokens_details": true, "output_tokens_details": true}, "usage"); err != nil {
		return canonicalUsage{}, err
	}
	input, err := requireInteger(root["input_tokens"], "usage.input_tokens", true)
	if err != nil {
		return canonicalUsage{}, err
	}
	output, err := requireInteger(root["output_tokens"], "usage.output_tokens", true)
	if err != nil {
		return canonicalUsage{}, err
	}
	total, err := requireInteger(root["total_tokens"], "usage.total_tokens", true)
	if err != nil || total != input+output {
		return canonicalUsage{}, invalidUpstream("usage.total_tokens", "token totals are inconsistent")
	}
	usage := canonicalUsage{Input: input, Output: output, Total: total}
	if rawDetails, ok := root["input_tokens_details"]; ok {
		details, err := decodeObject(rawDetails, CodeInvalidUpstream, "usage.input_tokens_details")
		if err != nil {
			return canonicalUsage{}, err
		}
		if err := rejectUnknown(details, map[string]bool{"cached_tokens": true, "cache_write_tokens": true}, "usage.input_tokens_details"); err != nil {
			return canonicalUsage{}, err
		}
		if rawValue, ok := details["cached_tokens"]; ok {
			usage.Cached, err = requireInteger(rawValue, "usage.input_tokens_details.cached_tokens", true)
			if err != nil {
				return canonicalUsage{}, err
			}
		}
		if rawValue, ok := details["cache_write_tokens"]; ok {
			usage.CacheWrite, err = requireInteger(rawValue, "usage.input_tokens_details.cache_write_tokens", true)
			if err != nil {
				return canonicalUsage{}, err
			}
		}
	}
	if rawDetails, ok := root["output_tokens_details"]; ok {
		details, err := decodeObject(rawDetails, CodeInvalidUpstream, "usage.output_tokens_details")
		if err != nil {
			return canonicalUsage{}, err
		}
		if err := rejectUnknown(details, map[string]bool{"reasoning_tokens": true}, "usage.output_tokens_details"); err != nil {
			return canonicalUsage{}, err
		}
		if rawValue, ok := details["reasoning_tokens"]; ok {
			usage.Reasoning, err = requireInteger(rawValue, "usage.output_tokens_details.reasoning_tokens", true)
			if err != nil {
				return canonicalUsage{}, err
			}
		}
	}
	return usage, nil
}

func optionalUpstreamBool(object map[string]json.RawMessage, field string) (bool, bool, error) {
	raw, ok := object[field]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, ok, nil
	}
	var value bool
	if json.Unmarshal(raw, &value) != nil {
		return false, true, invalidUpstream(field, "expected a boolean")
	}
	return value, true, nil
}
