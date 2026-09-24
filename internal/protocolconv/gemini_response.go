package protocolconv

import (
	"bytes"
	"encoding/json"
)

// GeminiResponseToResponses converts one non-streaming generateContent result.
func GeminiResponseToResponses(raw []byte) ([]byte, error) {
	root, err := decodeObject(raw, CodeInvalidUpstream, "")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(root, map[string]bool{"candidates": true, "usageMetadata": true, "modelVersion": true, "responseId": true}, ""); err != nil {
		return nil, err
	}
	responseID, err := nonEmptyString(root["responseId"], "responseId", true)
	if err != nil {
		return nil, err
	}
	model, err := nonEmptyString(root["modelVersion"], "modelVersion", true)
	if err != nil {
		return nil, err
	}
	var candidates []json.RawMessage
	if json.Unmarshal(root["candidates"], &candidates) != nil || len(candidates) != 1 {
		return nil, unsupported("candidates")
	}
	candidate, err := decodeObject(candidates[0], CodeInvalidUpstream, "candidates[0]")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(candidate, map[string]bool{"content": true, "finishReason": true, "index": true}, "candidates[0]"); err != nil {
		return nil, err
	}
	if rawIndex, ok := candidate["index"]; ok {
		index, err := requireInteger(rawIndex, "candidates[0].index", true)
		if err != nil || index != 0 {
			return nil, invalidUpstream("candidates[0].index", "expected zero")
		}
	}
	content, err := decodeObject(candidate["content"], CodeInvalidUpstream, "candidates[0].content")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(content, map[string]bool{"role": true, "parts": true}, "candidates[0].content"); err != nil {
		return nil, err
	}
	role, err := requireString(content["role"], "candidates[0].content.role", true)
	if err != nil || role != "model" {
		return nil, invalidUpstream("candidates[0].content.role", "expected model")
	}
	var parts []json.RawMessage
	if json.Unmarshal(content["parts"], &parts) != nil || parts == nil {
		return nil, invalidUpstream("candidates[0].content.parts", "an array is required")
	}
	reservedCallIDs, err := geminiResponseExplicitCallIDs(parts)
	if err != nil {
		return nil, err
	}
	output := make([]any, 0, len(parts))
	messageIndex := 0
	callIndex := 0
	generatedCallIndex := 0
	for index, rawPart := range parts {
		field := indexField("candidates[0].content.parts", index)
		part, err := decodeObject(rawPart, CodeInvalidUpstream, field)
		if err != nil {
			return nil, err
		}
		switch {
		case part["text"] != nil:
			if err := rejectUnknown(part, map[string]bool{"text": true}, field); err != nil {
				return nil, err
			}
			text, err := requireString(part["text"], joinField(field, "text"), true)
			if err != nil {
				return nil, err
			}
			output = append(output, map[string]any{
				"id": convertedItemID("msg", responseID, messageIndex), "type": "message", "status": "completed", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
			})
			messageIndex++
		case part["functionCall"] != nil:
			if err := rejectUnknown(part, map[string]bool{"functionCall": true}, field); err != nil {
				return nil, err
			}
			call, err := decodeObject(part["functionCall"], CodeInvalidUpstream, joinField(field, "functionCall"))
			if err != nil {
				return nil, err
			}
			if err := rejectUnknown(call, map[string]bool{"id": true, "name": true, "args": true}, joinField(field, "functionCall")); err != nil {
				return nil, err
			}
			id, present, err := optionalNonEmptyString(call["id"], joinField(field, "functionCall.id"), true)
			if err != nil {
				return nil, err
			}
			name, err := nonEmptyString(call["name"], joinField(field, "functionCall.name"), true)
			if err != nil {
				return nil, err
			}
			if !present {
				id = nextUniqueGeminiCallID(responseID, &generatedCallIndex, reservedCallIDs)
			}
			arguments := "{}"
			if len(call["args"]) != 0 {
				arguments, err = compactJSONObject(call["args"], joinField(field, "functionCall.args"), true)
				if err != nil {
					return nil, err
				}
			}
			output = append(output, map[string]any{"id": convertedItemID("fc", responseID, callIndex), "type": "function_call", "status": "completed", "call_id": id, "name": name, "arguments": arguments})
			callIndex++
		default:
			return nil, unsupported(field)
		}
	}
	finish, err := requireString(candidate["finishReason"], "candidates[0].finishReason", true)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": convertedEnvelopeID("resp", responseID), "object": "response", "created_at": int64(0), "model": model, "output": output}
	switch finish {
	case "STOP":
		result["status"] = "completed"
	case "MAX_TOKENS":
		result["status"] = "incomplete"
		result["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	default:
		return nil, unsupported("candidates[0].finishReason")
	}
	if rawUsage, ok := root["usageMetadata"]; ok && !bytes.Equal(bytes.TrimSpace(rawUsage), []byte("null")) {
		usage, err := geminiUsageToResponses(rawUsage)
		if err != nil {
			return nil, err
		}
		result["usage"] = usage
	}
	return marshal(result)
}

func geminiResponseExplicitCallIDs(parts []json.RawMessage) (map[string]struct{}, error) {
	reserved := map[string]struct{}{}
	for index, rawPart := range parts {
		partField := indexField("candidates[0].content.parts", index)
		part, err := decodeObject(rawPart, CodeInvalidUpstream, partField)
		if err != nil {
			return nil, err
		}
		rawCall, ok := part["functionCall"]
		if !ok {
			continue
		}
		callField := joinField(partField, "functionCall")
		call, err := decodeObject(rawCall, CodeInvalidUpstream, callField)
		if err != nil {
			return nil, err
		}
		id, present, err := optionalNonEmptyString(call["id"], joinField(callField, "id"), true)
		if err != nil {
			return nil, err
		}
		if present {
			if err := reserveUniqueGeminiCallID(reserved, id, joinField(callField, "id"), true); err != nil {
				return nil, err
			}
		}
	}
	return reserved, nil
}

func geminiUsageToResponses(raw json.RawMessage) (map[string]any, error) {
	root, err := decodeObject(raw, CodeInvalidUpstream, "usageMetadata")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(root, map[string]bool{"promptTokenCount": true, "candidatesTokenCount": true, "totalTokenCount": true, "cachedContentTokenCount": true}, "usageMetadata"); err != nil {
		return nil, err
	}
	input, err := requireInteger(root["promptTokenCount"], "usageMetadata.promptTokenCount", true)
	if err != nil {
		return nil, err
	}
	output, err := requireInteger(root["candidatesTokenCount"], "usageMetadata.candidatesTokenCount", true)
	if err != nil {
		return nil, err
	}
	total, err := requireInteger(root["totalTokenCount"], "usageMetadata.totalTokenCount", true)
	if err != nil || total != input+output {
		return nil, invalidUpstream("usageMetadata.totalTokenCount", "token totals are inconsistent")
	}
	result := map[string]any{"input_tokens": input, "output_tokens": output, "total_tokens": total}
	if rawCached, ok := root["cachedContentTokenCount"]; ok {
		cached, err := requireInteger(rawCached, "usageMetadata.cachedContentTokenCount", true)
		if err != nil {
			return nil, err
		}
		result["input_tokens_details"] = map[string]any{"cached_tokens": cached}
	}
	return result, nil
}

// ResponsesResponseToGemini converts a completed or max-token-incomplete
// Responses object to one generateContent result.
func ResponsesResponseToGemini(raw []byte) ([]byte, error) {
	canonical, err := parseResponsesOutput(raw)
	if err != nil {
		return nil, err
	}
	parts := make([]any, 0, len(canonical.Parts))
	for _, part := range canonical.Parts {
		if part.Text != nil {
			parts = append(parts, map[string]any{"text": *part.Text})
			continue
		}
		call := part.Call
		if call == nil {
			continue
		}
		var args map[string]any
		decoder := json.NewDecoder(bytes.NewBufferString(call.Arguments))
		decoder.UseNumber()
		if decoder.Decode(&args) != nil || args == nil {
			return nil, invalidUpstream("output.arguments", "expected a JSON object string")
		}
		parts = append(parts, map[string]any{"functionCall": map[string]any{"id": call.ID, "name": call.Name, "args": args}})
	}
	finish := "STOP"
	if canonical.Status == "incomplete" {
		finish = "MAX_TOKENS"
	}
	result := map[string]any{
		"responseId": convertedEnvelopeID("gemini", canonical.ID), "modelVersion": canonical.Model,
		"candidates": []any{map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": parts}, "finishReason": finish}},
	}
	if canonical.Usage != nil {
		if canonical.Usage.CacheWrite != 0 || canonical.Usage.Reasoning != 0 {
			return nil, unsupported("usage")
		}
		usage := map[string]any{"promptTokenCount": canonical.Usage.Input, "candidatesTokenCount": canonical.Usage.Output, "totalTokenCount": canonical.Usage.Total}
		if canonical.Usage.Cached != 0 {
			usage["cachedContentTokenCount"] = canonical.Usage.Cached
		}
		result["usageMetadata"] = usage
	}
	return marshal(result)
}
