package protocolconv

import (
	"bytes"
	"encoding/json"
)

// MessagesResponseToResponses converts one non-streaming Anthropic message.
func MessagesResponseToResponses(raw []byte) ([]byte, error) {
	root, err := decodeObject(raw, CodeInvalidUpstream, "")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(root, map[string]bool{
		"id": true, "type": true, "role": true, "model": true, "content": true,
		"stop_reason": true, "stop_sequence": true, "usage": true,
	}, ""); err != nil {
		return nil, err
	}
	id, err := nonEmptyString(root["id"], "id", true)
	if err != nil {
		return nil, err
	}
	kind, err := requireString(root["type"], "type", true)
	if err != nil || kind != "message" {
		return nil, invalidUpstream("type", "expected message")
	}
	role, err := requireString(root["role"], "role", true)
	if err != nil || role != "assistant" {
		return nil, invalidUpstream("role", "expected assistant")
	}
	model, err := nonEmptyString(root["model"], "model", true)
	if err != nil {
		return nil, err
	}
	var blocks []json.RawMessage
	if json.Unmarshal(root["content"], &blocks) != nil || blocks == nil {
		return nil, invalidUpstream("content", "an array is required")
	}
	output := make([]any, 0, len(blocks))
	callCount := 0
	messageCount := 0
	for index, rawBlock := range blocks {
		field := indexField("content", index)
		block, err := decodeObject(rawBlock, CodeInvalidUpstream, field)
		if err != nil {
			return nil, err
		}
		kind, err := requireString(block["type"], joinField(field, "type"), true)
		if err != nil {
			return nil, err
		}
		switch kind {
		case "text":
			if err := rejectUnknown(block, map[string]bool{"type": true, "text": true}, field); err != nil {
				return nil, err
			}
			text, err := requireString(block["text"], joinField(field, "text"), true)
			if err != nil {
				return nil, err
			}
			output = append(output, map[string]any{
				"id": convertedItemID("msg", id, messageCount), "type": "message", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
			})
			messageCount++
		case "tool_use":
			if err := rejectUnknown(block, map[string]bool{"type": true, "id": true, "name": true, "input": true}, field); err != nil {
				return nil, err
			}
			callID, err := nonEmptyString(block["id"], joinField(field, "id"), true)
			if err != nil {
				return nil, err
			}
			name, err := nonEmptyString(block["name"], joinField(field, "name"), true)
			if err != nil {
				return nil, err
			}
			arguments, err := compactJSONObject(block["input"], joinField(field, "input"), true)
			if err != nil {
				return nil, err
			}
			output = append(output, map[string]any{"id": convertedItemID("fc", id, callCount), "type": "function_call", "status": "completed", "call_id": callID, "name": name, "arguments": arguments})
			callCount++
		default:
			return nil, unsupported(joinField(field, "type"))
		}
	}
	stopReason, err := requireString(root["stop_reason"], "stop_reason", true)
	if err != nil {
		return nil, err
	}
	status := "completed"
	result := map[string]any{"id": convertedEnvelopeID("resp", id), "object": "response", "created_at": int64(0), "model": model, "output": output}
	switch stopReason {
	case "end_turn", "stop_sequence":
		if stopReason == "stop_sequence" {
			if sequence, ok := root["stop_sequence"]; !ok || bytes.Equal(bytes.TrimSpace(sequence), []byte("null")) {
				return nil, invalidUpstream("stop_sequence", "stop sequence is required")
			}
		}
	case "tool_use":
		if callCount == 0 {
			return nil, invalidUpstream("stop_reason", "tool_use requires a tool_use block")
		}
	case "max_tokens":
		status = "incomplete"
		result["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	default:
		return nil, unsupported("stop_reason")
	}
	result["status"] = status
	if rawUsage, ok := root["usage"]; ok && !bytes.Equal(bytes.TrimSpace(rawUsage), []byte("null")) {
		usage, err := messagesUsageToResponses(rawUsage)
		if err != nil {
			return nil, err
		}
		result["usage"] = usage
	}
	return marshal(result)
}

func messagesUsageToResponses(raw json.RawMessage) (map[string]any, error) {
	root, err := decodeObject(raw, CodeInvalidUpstream, "usage")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(root, map[string]bool{
		"input_tokens": true, "output_tokens": true,
		"cache_creation_input_tokens": true, "cache_read_input_tokens": true,
	}, "usage"); err != nil {
		return nil, err
	}
	input, err := requireInteger(root["input_tokens"], "usage.input_tokens", true)
	if err != nil {
		return nil, err
	}
	output, err := requireInteger(root["output_tokens"], "usage.output_tokens", true)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"input_tokens": input, "output_tokens": output, "total_tokens": input + output}
	details := map[string]any{}
	if rawValue, ok := root["cache_read_input_tokens"]; ok {
		value, err := requireInteger(rawValue, "usage.cache_read_input_tokens", true)
		if err != nil {
			return nil, err
		}
		details["cached_tokens"] = value
	}
	if rawValue, ok := root["cache_creation_input_tokens"]; ok {
		value, err := requireInteger(rawValue, "usage.cache_creation_input_tokens", true)
		if err != nil {
			return nil, err
		}
		details["cache_write_tokens"] = value
	}
	if len(details) > 0 {
		result["input_tokens_details"] = details
	}
	return result, nil
}

// ResponsesResponseToMessages converts a completed or max-token-incomplete
// Responses object to one Anthropic Messages response.
func ResponsesResponseToMessages(raw []byte) ([]byte, error) {
	canonical, err := parseResponsesOutput(raw)
	if err != nil {
		return nil, err
	}
	content := make([]any, 0, len(canonical.Parts))
	callCount := 0
	for _, part := range canonical.Parts {
		if part.Text != nil {
			content = append(content, map[string]any{"type": "text", "text": *part.Text})
			continue
		}
		call := part.Call
		if call == nil {
			continue
		}
		var input map[string]any
		decoder := json.NewDecoder(bytes.NewBufferString(call.Arguments))
		decoder.UseNumber()
		if decoder.Decode(&input) != nil || input == nil {
			return nil, invalidUpstream("output.arguments", "expected a JSON object string")
		}
		content = append(content, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": input})
		callCount++
	}
	stopReason := "end_turn"
	if canonical.Status == "incomplete" {
		stopReason = "max_tokens"
	} else if callCount > 0 {
		stopReason = "tool_use"
	}
	result := map[string]any{
		"id": convertedEnvelopeID("msg", canonical.ID), "type": "message", "role": "assistant", "model": canonical.Model,
		"content": content, "stop_reason": stopReason, "stop_sequence": nil,
	}
	if canonical.Usage != nil {
		usage := map[string]any{"input_tokens": canonical.Usage.Input, "output_tokens": canonical.Usage.Output}
		if canonical.Usage.Cached != 0 {
			usage["cache_read_input_tokens"] = canonical.Usage.Cached
		}
		if canonical.Usage.CacheWrite != 0 {
			usage["cache_creation_input_tokens"] = canonical.Usage.CacheWrite
		}
		if canonical.Usage.Reasoning != 0 {
			return nil, unsupported("usage.output_tokens_details.reasoning_tokens")
		}
		result["usage"] = usage
	}
	return marshal(result)
}
