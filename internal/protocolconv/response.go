package protocolconv

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
)

// ChatResponseToResponses converts one completed, single-choice Chat
// Completions response. Error envelopes and partial responses are rejected.
func ChatResponseToResponses(raw []byte) ([]byte, error) {
	root, err := decodeObject(raw, CodeInvalidUpstream, "")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(root, map[string]bool{
		"id": true, "object": true, "created": true, "model": true,
		"choices": true, "usage": true, "system_fingerprint": true, "service_tier": true,
	}, ""); err != nil {
		return nil, err
	}
	id, err := requireString(root["id"], "id", true)
	if err != nil || id == "" {
		return nil, invalidUpstream("id", "a non-empty string is required")
	}
	object, err := requireString(root["object"], "object", true)
	if err != nil || object != "chat.completion" {
		return nil, invalidUpstream("object", "expected chat.completion")
	}
	model, err := requireString(root["model"], "model", true)
	if err != nil || model == "" {
		return nil, invalidUpstream("model", "a non-empty string is required")
	}
	created, err := requireInteger(root["created"], "created", true)
	if err != nil {
		return nil, err
	}
	targetID := convertedEnvelopeID("resp", id)
	var choices []json.RawMessage
	if json.Unmarshal(root["choices"], &choices) != nil || len(choices) != 1 {
		return nil, invalidUpstream("choices", "exactly one choice is required")
	}
	choice, err := decodeObject(choices[0], CodeInvalidUpstream, "choices[0]")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(choice, map[string]bool{"index": true, "message": true, "finish_reason": true, "logprobs": true}, "choices[0]"); err != nil {
		return nil, err
	}
	if raw, ok := choice["logprobs"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, unsupported("choices[0].logprobs")
	}
	index, err := requireInteger(choice["index"], "choices[0].index", true)
	if err != nil || index != 0 {
		return nil, invalidUpstream("choices[0].index", "only choice zero is supported")
	}
	finishReason, err := requireString(choice["finish_reason"], "choices[0].finish_reason", true)
	if err != nil || (finishReason != "stop" && finishReason != "tool_calls") {
		return nil, unsupported("choices[0].finish_reason")
	}
	message, err := decodeObject(choice["message"], CodeInvalidUpstream, "choices[0].message")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(message, map[string]bool{"role": true, "content": true, "tool_calls": true, "refusal": true, "audio": true}, "choices[0].message"); err != nil {
		return nil, err
	}
	for _, field := range []string{"refusal", "audio"} {
		if raw, ok := message[field]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, unsupported("choices[0].message." + field)
		}
	}
	role, err := requireString(message["role"], "choices[0].message.role", true)
	if err != nil || role != "assistant" {
		return nil, invalidUpstream("choices[0].message.role", "expected assistant")
	}
	output := make([]any, 0, 2)
	if rawContent, ok := message["content"]; ok && !bytes.Equal(bytes.TrimSpace(rawContent), []byte("null")) {
		content, err := requireString(rawContent, "choices[0].message.content", true)
		if err != nil {
			return nil, err
		}
		output = append(output, map[string]any{
			"id": convertedItemID("msg", targetID, 0), "type": "message", "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": content, "annotations": []any{}}},
		})
	}
	if rawCalls, ok := message["tool_calls"]; ok {
		calls, err := chatToolCallsToResponseItems(rawCalls, "choices[0].message.tool_calls")
		if err != nil {
			return nil, err
		}
		for itemIndex, call := range calls {
			converted := call.(map[string]any)
			converted["id"] = convertedItemID("fc", targetID, itemIndex)
			converted["status"] = "completed"
			output = append(output, converted)
		}
	}
	if finishReason == "stop" {
		if _, ok := message["tool_calls"]; ok {
			return nil, invalidUpstream("choices[0].finish_reason", "tool calls require tool_calls finish reason")
		}
	}
	if len(output) == 0 {
		return nil, invalidUpstream("choices[0].message", "content or tool_calls are required")
	}
	if finishReason == "tool_calls" {
		if _, ok := message["tool_calls"]; !ok {
			return nil, invalidUpstream("choices[0].finish_reason", "tool_calls were not present")
		}
	}
	result := map[string]any{
		"id": targetID, "object": "response", "created_at": created, "model": model,
		"status": "completed", "output": output,
	}
	if rawUsage, ok := root["usage"]; ok && !bytes.Equal(bytes.TrimSpace(rawUsage), []byte("null")) {
		usage, err := chatUsageToResponses(rawUsage, "usage", true)
		if err != nil {
			return nil, err
		}
		result["usage"] = usage
	}
	return marshal(result)
}

// ResponsesResponseToChat converts one completed Responses object to a
// single-choice Chat Completions response.
func ResponsesResponseToChat(raw []byte) ([]byte, error) {
	root, err := decodeObject(raw, CodeInvalidUpstream, "")
	if err != nil {
		return nil, err
	}
	if err := validateStandardResponseEnvelope(root); err != nil {
		return nil, err
	}
	id, err := requireString(root["id"], "id", true)
	if err != nil || id == "" {
		return nil, invalidUpstream("id", "a non-empty string is required")
	}
	object, err := requireString(root["object"], "object", true)
	if err != nil || object != "response" {
		return nil, invalidUpstream("object", "expected response")
	}
	status, err := requireString(root["status"], "status", true)
	if err != nil || status != "completed" {
		return nil, invalidUpstream("status", "only completed responses are supported")
	}
	model, err := requireString(root["model"], "model", true)
	if err != nil || model == "" {
		return nil, invalidUpstream("model", "a non-empty string is required")
	}
	created, err := requireInteger(root["created_at"], "created_at", true)
	if err != nil {
		return nil, err
	}
	var output []json.RawMessage
	if json.Unmarshal(root["output"], &output) != nil || len(output) == 0 {
		return nil, invalidUpstream("output", "a non-empty array is required")
	}
	var content bytes.Buffer
	toolCalls := make([]any, 0)
	messageSeen := false
	for index, rawItem := range output {
		field := indexField("output", index)
		item, err := decodeObject(rawItem, CodeInvalidUpstream, field)
		if err != nil {
			return nil, err
		}
		kind, err := requireString(item["type"], joinField(field, "type"), true)
		if err != nil {
			return nil, err
		}
		switch kind {
		case "message":
			if messageSeen {
				return nil, unsupported(joinField(field, "type"))
			}
			messageSeen = true
			if err := rejectUnknown(item, map[string]bool{"id": true, "type": true, "status": true, "role": true, "content": true}, field); err != nil {
				return nil, err
			}
			if err := validateCompletedOutputIdentity(item, field); err != nil {
				return nil, err
			}
			role, err := requireString(item["role"], joinField(field, "role"), true)
			if err != nil || role != "assistant" {
				return nil, invalidUpstream(joinField(field, "role"), "expected assistant")
			}
			text, err := responseContentText(item["content"], joinField(field, "content"), true)
			if err != nil {
				return nil, err
			}
			content.WriteString(text)
		case "function_call":
			if err := rejectUnknown(item, map[string]bool{"id": true, "type": true, "status": true, "call_id": true, "name": true, "arguments": true}, field); err != nil {
				return nil, err
			}
			if err := validateCompletedOutputIdentity(item, field); err != nil {
				return nil, err
			}
			callID, err := requireString(item["call_id"], joinField(field, "call_id"), true)
			if err != nil || callID == "" {
				return nil, invalidUpstream(joinField(field, "call_id"), "a non-empty string is required")
			}
			name, err := requireString(item["name"], joinField(field, "name"), true)
			if err != nil || name == "" {
				return nil, invalidUpstream(joinField(field, "name"), "a non-empty string is required")
			}
			arguments, err := requireString(item["arguments"], joinField(field, "arguments"), true)
			if err != nil {
				return nil, err
			}
			toolCalls = append(toolCalls, map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}})
		default:
			return nil, unsupported(joinField(field, "type"))
		}
	}
	message := map[string]any{"role": "assistant", "content": content.String()}
	finishReason := "stop"
	if len(toolCalls) > 0 {
		message["tool_calls"] = toolCalls
		finishReason = "tool_calls"
		if content.Len() == 0 {
			message["content"] = nil
		}
	}
	result := map[string]any{
		"id": convertedEnvelopeID("chatcmpl", id), "object": "chat.completion", "created": created, "model": model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}},
	}
	if rawUsage, ok := root["usage"]; ok && !bytes.Equal(bytes.TrimSpace(rawUsage), []byte("null")) {
		usage, err := responsesUsageToChat(rawUsage, "usage", true)
		if err != nil {
			return nil, err
		}
		result["usage"] = usage
	}
	return marshal(result)
}

func validateStandardResponseEnvelope(root map[string]json.RawMessage) error {
	allowed := map[string]bool{
		"id": true, "object": true, "created_at": true, "completed_at": true, "model": true,
		"status": true, "output": true, "usage": true, "background": true,
		"error": true, "incomplete_details": true, "instructions": true, "metadata": true,
		"max_output_tokens": true, "max_tool_calls": true, "parallel_tool_calls": true,
		"previous_response_id": true, "prompt_cache_key": true, "prompt_cache_retention": true,
		"reasoning": true, "safety_identifier": true, "service_tier": true, "store": true,
		"temperature": true, "text": true, "tool_choice": true, "tools": true,
		"top_logprobs": true, "top_p": true, "truncation": true, "user": true,
		"context_management": true,
	}
	if err := rejectUnknown(root, allowed, ""); err != nil {
		return err
	}
	for _, field := range []string{"error", "incomplete_details"} {
		if raw, ok := root[field]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return invalidUpstream(field, "completed response must not contain terminal error details")
		}
	}
	return nil
}

func validateCompletedOutputIdentity(item map[string]json.RawMessage, field string) error {
	id, err := requireString(item["id"], joinField(field, "id"), true)
	if err != nil || id == "" {
		return invalidUpstream(joinField(field, "id"), "a non-empty string is required")
	}
	status, err := requireString(item["status"], joinField(field, "status"), true)
	if err != nil || status != "completed" {
		return invalidUpstream(joinField(field, "status"), "completed response contains an unfinished output item")
	}
	return nil
}

func requireInteger(raw json.RawMessage, field string, upstream bool) (int64, error) {
	var value int64
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value < 0 {
		if upstream {
			return 0, invalidUpstream(field, "expected a non-negative integer")
		}
		return 0, invalid(field, "expected a non-negative integer")
	}
	return value, nil
}

func convertedItemID(prefix, responseID string, index int) string {
	sum := sha256.Sum256([]byte(responseID + "\x00" + prefix + "\x00" + strconv.Itoa(index)))
	return prefix + "_cpa_" + hex.EncodeToString(sum[:12])
}

func convertedEnvelopeID(prefix, sourceID string) string {
	sum := sha256.Sum256([]byte("cpa-protocol-conversion\x00" + prefix + "\x00" + sourceID))
	return prefix + "_cpa_" + hex.EncodeToString(sum[:12])
}

func chatUsageToResponses(raw json.RawMessage, field string, upstream bool) (map[string]any, error) {
	usage, err := decodeObject(raw, map[bool]ErrorCode{true: CodeInvalidUpstream, false: CodeInvalidRequest}[upstream], field)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(usage, map[string]bool{
		"prompt_tokens": true, "completion_tokens": true, "total_tokens": true,
		"prompt_tokens_details": true, "completion_tokens_details": true,
	}, field); err != nil {
		return nil, err
	}
	input, err := requireInteger(usage["prompt_tokens"], joinField(field, "prompt_tokens"), upstream)
	if err != nil {
		return nil, err
	}
	output, err := requireInteger(usage["completion_tokens"], joinField(field, "completion_tokens"), upstream)
	if err != nil {
		return nil, err
	}
	total, err := requireInteger(usage["total_tokens"], joinField(field, "total_tokens"), upstream)
	if err != nil || total != input+output {
		return nil, invalidUpstream(joinField(field, "total_tokens"), "token totals are inconsistent")
	}
	result := map[string]any{"input_tokens": input, "output_tokens": output, "total_tokens": total}
	if details, ok := usage["prompt_tokens_details"]; ok {
		converted, err := convertUsageDetails(details, joinField(field, "prompt_tokens_details"), upstream, map[string]string{"cached_tokens": "cached_tokens", "cache_write_tokens": "cache_write_tokens", "audio_tokens": ""})
		if err != nil {
			return nil, err
		}
		result["input_tokens_details"] = converted
	}
	if details, ok := usage["completion_tokens_details"]; ok {
		converted, err := convertUsageDetails(details, joinField(field, "completion_tokens_details"), upstream, map[string]string{"reasoning_tokens": "reasoning_tokens", "accepted_prediction_tokens": "", "audio_tokens": "", "rejected_prediction_tokens": ""})
		if err != nil {
			return nil, err
		}
		result["output_tokens_details"] = converted
	}
	return result, nil
}

func responsesUsageToChat(raw json.RawMessage, field string, upstream bool) (map[string]any, error) {
	usage, err := decodeObject(raw, map[bool]ErrorCode{true: CodeInvalidUpstream, false: CodeInvalidRequest}[upstream], field)
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(usage, map[string]bool{
		"input_tokens": true, "output_tokens": true, "total_tokens": true,
		"input_tokens_details": true, "output_tokens_details": true,
	}, field); err != nil {
		return nil, err
	}
	input, err := requireInteger(usage["input_tokens"], joinField(field, "input_tokens"), upstream)
	if err != nil {
		return nil, err
	}
	output, err := requireInteger(usage["output_tokens"], joinField(field, "output_tokens"), upstream)
	if err != nil {
		return nil, err
	}
	total, err := requireInteger(usage["total_tokens"], joinField(field, "total_tokens"), upstream)
	if err != nil || total != input+output {
		return nil, invalidUpstream(joinField(field, "total_tokens"), "token totals are inconsistent")
	}
	result := map[string]any{"prompt_tokens": input, "completion_tokens": output, "total_tokens": total}
	if details, ok := usage["input_tokens_details"]; ok {
		converted, err := convertUsageDetails(details, joinField(field, "input_tokens_details"), upstream, map[string]string{"cached_tokens": "cached_tokens", "cache_write_tokens": "cache_write_tokens"})
		if err != nil {
			return nil, err
		}
		result["prompt_tokens_details"] = converted
	}
	if details, ok := usage["output_tokens_details"]; ok {
		converted, err := convertUsageDetails(details, joinField(field, "output_tokens_details"), upstream, map[string]string{"reasoning_tokens": "reasoning_tokens"})
		if err != nil {
			return nil, err
		}
		result["completion_tokens_details"] = converted
	}
	return result, nil
}

func convertUsageDetails(raw json.RawMessage, field string, upstream bool, fields map[string]string) (map[string]any, error) {
	details, err := decodeObject(raw, map[bool]ErrorCode{true: CodeInvalidUpstream, false: CodeInvalidRequest}[upstream], field)
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]bool, len(fields))
	for source := range fields {
		allowed[source] = true
	}
	if err := rejectUnknown(details, allowed, field); err != nil {
		return nil, err
	}
	result := map[string]any{}
	for source, target := range fields {
		if value, ok := details[source]; ok {
			count, err := requireInteger(value, joinField(field, source), upstream)
			if err != nil {
				return nil, err
			}
			if target == "" {
				if count != 0 {
					return nil, unsupported(joinField(field, source))
				}
				continue
			}
			result[target] = count
		}
	}
	return result, nil
}
