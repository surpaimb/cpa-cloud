package protocolconv

import (
	"bytes"
	"encoding/json"
)

var messagesRequestFields = map[string]bool{
	"model": true, "max_tokens": true, "messages": true, "system": true,
	"tools": true, "tool_choice": true, "disable_parallel_tool_use": true,
	"stream": true, "temperature": true, "top_p": true,
}

// MessagesRequestToResponses converts the non-media Anthropic Messages subset
// to a stateless Responses request.
func MessagesRequestToResponses(raw []byte) ([]byte, error) {
	converted, _, err := messagesRequestToResponses(raw)
	return converted, err
}

func messagesRequestToResponses(raw []byte) ([]byte, FeatureSet, error) {
	root, err := decodeObject(raw, CodeInvalidRequest, "")
	if err != nil {
		return nil, 0, err
	}
	if err := rejectUnknown(root, messagesRequestFields, ""); err != nil {
		return nil, 0, err
	}
	model, err := requireString(root["model"], "model", false)
	if err != nil || model == "" {
		return nil, 0, invalid("model", "a non-empty string is required")
	}
	maxTokens, err := positiveInteger(root["max_tokens"], "max_tokens", false)
	if err != nil {
		return nil, 0, err
	}
	out := map[string]any{"model": model, "store": false, "max_output_tokens": maxTokens}
	features := Features(FeatureText, FeatureUsage, FeatureFinishReason)
	if rawSystem, ok := root["system"]; ok {
		system, err := requireString(rawSystem, "system", false)
		if err != nil {
			return nil, 0, unsupported("system")
		}
		out["instructions"] = system
		features |= Features(FeatureSystemInstruction)
	}
	items, itemFeatures, err := messagesInputToResponseItems(root["messages"])
	if err != nil {
		return nil, 0, err
	}
	out["input"] = items
	features |= itemFeatures
	if value, present, err := optionalBool(root, "stream"); err != nil {
		return nil, 0, err
	} else if present {
		out["stream"] = value
	}
	if rawTools, ok := root["tools"]; ok {
		tools, err := messagesToolsToResponses(rawTools)
		if err != nil {
			return nil, 0, err
		}
		out["tools"] = tools
		features |= Features(FeatureFunctionDefinitions, FeatureFunctionCalls)
	}
	if rawChoice, ok := root["tool_choice"]; ok {
		choice, err := messagesToolChoiceToResponses(rawChoice)
		if err != nil {
			return nil, 0, err
		}
		out["tool_choice"] = choice
	}
	if disabled, present, err := optionalBool(root, "disable_parallel_tool_use"); err != nil {
		return nil, 0, err
	} else if present {
		out["parallel_tool_calls"] = !disabled
	}
	if err := copyBoundedNumber(root, out, "temperature", 2); err != nil {
		return nil, 0, err
	}
	if err := copyBoundedNumber(root, out, "top_p", 1); err != nil {
		return nil, 0, err
	}
	converted, err := marshal(out)
	return converted, features, err
}

func messagesInputToResponseItems(raw json.RawMessage) ([]any, FeatureSet, error) {
	var messages []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &messages) != nil || messages == nil {
		return nil, 0, invalid("messages", "an array is required")
	}
	items := make([]any, 0, len(messages))
	features := FeatureSet(0)
	pendingCalls := map[string]struct{}{}
	for messageIndex, rawMessage := range messages {
		field := indexField("messages", messageIndex)
		message, err := decodeObject(rawMessage, CodeInvalidRequest, field)
		if err != nil {
			return nil, 0, err
		}
		if err := rejectUnknown(message, map[string]bool{"role": true, "content": true}, field); err != nil {
			return nil, 0, err
		}
		role, err := requireString(message["role"], joinField(field, "role"), false)
		if err != nil || (role != "user" && role != "assistant") {
			return nil, 0, unsupported(joinField(field, "role"))
		}
		var text string
		if json.Unmarshal(message["content"], &text) == nil {
			items = append(items, map[string]any{"type": "message", "role": role, "content": text})
			features |= Features(FeatureText)
			continue
		}
		var blocks []json.RawMessage
		if json.Unmarshal(message["content"], &blocks) != nil || blocks == nil {
			return nil, 0, invalid(joinField(field, "content"), "a string or content array is required")
		}
		for blockIndex, rawBlock := range blocks {
			blockField := indexField(joinField(field, "content"), blockIndex)
			block, err := decodeObject(rawBlock, CodeInvalidRequest, blockField)
			if err != nil {
				return nil, 0, err
			}
			kind, err := requireString(block["type"], joinField(blockField, "type"), false)
			if err != nil {
				return nil, 0, err
			}
			switch kind {
			case "text":
				if err := rejectUnknown(block, map[string]bool{"type": true, "text": true}, blockField); err != nil {
					return nil, 0, err
				}
				value, err := requireString(block["text"], joinField(blockField, "text"), false)
				if err != nil {
					return nil, 0, err
				}
				items = append(items, map[string]any{"type": "message", "role": role, "content": value})
				features |= Features(FeatureText)
			case "tool_use":
				if role != "assistant" {
					return nil, 0, unsupported(joinField(blockField, "type"))
				}
				if err := rejectUnknown(block, map[string]bool{"type": true, "id": true, "name": true, "input": true}, blockField); err != nil {
					return nil, 0, err
				}
				id, err := nonEmptyString(block["id"], joinField(blockField, "id"), false)
				if err != nil {
					return nil, 0, err
				}
				name, err := nonEmptyString(block["name"], joinField(blockField, "name"), false)
				if err != nil {
					return nil, 0, err
				}
				input, err := compactJSONObject(block["input"], joinField(blockField, "input"), false)
				if err != nil {
					return nil, 0, err
				}
				items = append(items, map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": input})
				pendingCalls[id] = struct{}{}
				features |= Features(FeatureFunctionCalls)
			case "tool_result":
				if role != "user" {
					return nil, 0, unsupported(joinField(blockField, "type"))
				}
				if err := rejectUnknown(block, map[string]bool{"type": true, "tool_use_id": true, "content": true, "is_error": true}, blockField); err != nil {
					return nil, 0, err
				}
				id, err := nonEmptyString(block["tool_use_id"], joinField(blockField, "tool_use_id"), false)
				if err != nil {
					return nil, 0, err
				}
				if _, ok := pendingCalls[id]; !ok {
					return nil, 0, invalid(joinField(blockField, "tool_use_id"), "does not match a preceding tool_use")
				}
				delete(pendingCalls, id)
				if isError, present, err := optionalBool(block, "is_error"); err != nil {
					return nil, 0, err
				} else if present && isError {
					return nil, 0, unsupported(joinField(blockField, "is_error"))
				}
				content, err := requireString(block["content"], joinField(blockField, "content"), false)
				if err != nil {
					return nil, 0, unsupported(joinField(blockField, "content"))
				}
				items = append(items, map[string]any{"type": "function_call_output", "call_id": id, "output": content})
				features |= Features(FeatureFunctionResults)
			default:
				return nil, 0, unsupported(joinField(blockField, "type"))
			}
		}
	}
	return items, features, nil
}

func messagesToolsToResponses(raw json.RawMessage) ([]any, error) {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || tools == nil {
		return nil, invalid("tools", "an array is required")
	}
	result := make([]any, 0, len(tools))
	for index, rawTool := range tools {
		field := indexField("tools", index)
		tool, err := decodeObject(rawTool, CodeInvalidRequest, field)
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(tool, map[string]bool{"name": true, "description": true, "input_schema": true}, field); err != nil {
			return nil, err
		}
		name, err := nonEmptyString(tool["name"], joinField(field, "name"), false)
		if err != nil {
			return nil, err
		}
		schema, err := rawJSONObject(tool["input_schema"], joinField(field, "input_schema"), false)
		if err != nil {
			return nil, err
		}
		converted := map[string]any{"type": "function", "name": name, "parameters": schema}
		if rawDescription, ok := tool["description"]; ok {
			description, err := requireString(rawDescription, joinField(field, "description"), false)
			if err != nil {
				return nil, err
			}
			converted["description"] = description
		}
		result = append(result, converted)
	}
	return result, nil
}

func messagesToolChoiceToResponses(raw json.RawMessage) (any, error) {
	choice, err := decodeObject(raw, CodeInvalidRequest, "tool_choice")
	if err != nil {
		return nil, err
	}
	kind, err := requireString(choice["type"], "tool_choice.type", false)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "auto", "any", "none":
		if err := rejectUnknown(choice, map[string]bool{"type": true}, "tool_choice"); err != nil {
			return nil, err
		}
		if kind == "any" {
			return "required", nil
		}
		return kind, nil
	case "tool":
		if err := rejectUnknown(choice, map[string]bool{"type": true, "name": true}, "tool_choice"); err != nil {
			return nil, err
		}
		name, err := nonEmptyString(choice["name"], "tool_choice.name", false)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "function", "name": name}, nil
	default:
		return nil, unsupported("tool_choice.type")
	}
}

// ResponsesRequestToMessages converts the supported stateless Responses
// subset to Anthropic Messages. System/developer input items are rejected;
// only the unambiguous top-level instructions field maps to Messages.system.
func ResponsesRequestToMessages(raw []byte) ([]byte, error) {
	converted, _, err := responsesRequestToMessages(raw)
	return converted, err
}

func responsesRequestToMessages(raw []byte) ([]byte, FeatureSet, error) {
	root, err := decodeObject(raw, CodeInvalidRequest, "")
	if err != nil {
		return nil, 0, err
	}
	if err := rejectEnabledStrictTools(root["tools"]); err != nil {
		return nil, 0, err
	}
	if err := rejectAmbiguousResponsesRoles(root["input"]); err != nil {
		return nil, 0, err
	}
	chat, err := ResponsesRequestToChat(raw)
	if err != nil {
		return nil, 0, err
	}
	chatRoot, _ := decodeObject(chat, CodeInvalidRequest, "")
	out := map[string]any{"model": mustDecoded(chatRoot["model"])}
	features := Features(FeatureText, FeatureUsage, FeatureFinishReason)
	if rawMax, ok := chatRoot["max_completion_tokens"]; ok {
		out["max_tokens"] = mustDecoded(rawMax)
	} else {
		return nil, 0, invalid("max_output_tokens", "a positive integer is required for Messages")
	}
	var chatMessages []json.RawMessage
	_ = json.Unmarshal(chatRoot["messages"], &chatMessages)
	messages := make([]any, 0, len(chatMessages))
	lastWasToolResult := false
	for index, rawMessage := range chatMessages {
		message, _ := decodeObject(rawMessage, CodeInvalidRequest, indexField("messages", index))
		role, _ := requireString(message["role"], joinField(indexField("messages", index), "role"), false)
		switch role {
		case "developer":
			if index != 0 {
				return nil, 0, unsupported(joinField(indexField("messages", index), "role"))
			}
			out["system"] = mustDecoded(message["content"])
			features |= Features(FeatureSystemInstruction)
		case "user", "assistant":
			lastWasToolResult = false
			blocks := make([]any, 0)
			if rawContent, ok := message["content"]; ok && !bytes.Equal(bytes.TrimSpace(rawContent), []byte("null")) {
				blocks = append(blocks, map[string]any{"type": "text", "text": mustDecoded(rawContent)})
			}
			if rawCalls, ok := message["tool_calls"]; ok {
				var calls []json.RawMessage
				_ = json.Unmarshal(rawCalls, &calls)
				for callIndex, rawCall := range calls {
					call, _ := decodeObject(rawCall, CodeInvalidRequest, "")
					function, _ := decodeObject(call["function"], CodeInvalidRequest, "")
					var arguments any
					argumentText, _ := requireString(function["arguments"], "", false)
					if err := json.Unmarshal([]byte(argumentText), &arguments); err != nil {
						return nil, 0, invalid(joinField(indexField("messages", index), indexField("tool_calls", callIndex)+".function.arguments"), "expected a JSON object string")
					}
					if _, ok := arguments.(map[string]any); !ok {
						return nil, 0, invalid("input", "function arguments must be a JSON object")
					}
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": mustDecoded(call["id"]), "name": mustDecoded(function["name"]), "input": arguments})
					features |= Features(FeatureFunctionCalls)
				}
			}
			messages = append(messages, map[string]any{"role": role, "content": blocks})
		case "tool":
			block := map[string]any{"type": "tool_result", "tool_use_id": mustDecoded(message["tool_call_id"]), "content": mustDecoded(message["content"])}
			if lastWasToolResult {
				previous := messages[len(messages)-1].(map[string]any)
				previous["content"] = append(previous["content"].([]any), block)
			} else {
				messages = append(messages, map[string]any{"role": "user", "content": []any{block}})
			}
			lastWasToolResult = true
			features |= Features(FeatureFunctionResults)
		default:
			return nil, 0, unsupported(joinField(indexField("messages", index), "role"))
		}
	}
	out["messages"] = messages
	for _, field := range []string{"stream", "temperature", "top_p"} {
		if value, ok := chatRoot[field]; ok {
			out[field] = mustDecoded(value)
		}
	}
	if rawParallel, ok := chatRoot["parallel_tool_calls"]; ok {
		var parallel bool
		_ = json.Unmarshal(rawParallel, &parallel)
		out["disable_parallel_tool_use"] = !parallel
	}
	if rawTools, ok := chatRoot["tools"]; ok {
		var tools []json.RawMessage
		_ = json.Unmarshal(rawTools, &tools)
		converted := make([]any, 0, len(tools))
		for _, rawTool := range tools {
			tool, _ := decodeObject(rawTool, CodeInvalidRequest, "")
			function, _ := decodeObject(tool["function"], CodeInvalidRequest, "")
			entry := map[string]any{"name": mustDecoded(function["name"]), "input_schema": mustDecoded(function["parameters"])}
			if description, ok := function["description"]; ok {
				entry["description"] = mustDecoded(description)
			}
			converted = append(converted, entry)
		}
		out["tools"] = converted
		features |= Features(FeatureFunctionDefinitions, FeatureFunctionCalls)
	}
	if rawChoice, ok := chatRoot["tool_choice"]; ok {
		choice, err := chatToolChoiceToMessages(rawChoice)
		if err != nil {
			return nil, 0, err
		}
		out["tool_choice"] = choice
	}
	converted, err := marshal(out)
	return converted, features, err
}

func rejectEnabledStrictTools(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil {
		return nil // The canonical converter returns the precise shape error.
	}
	for index, rawTool := range tools {
		tool, err := decodeObject(rawTool, CodeInvalidRequest, indexField("tools", index))
		if err != nil {
			return err
		}
		if rawStrict, ok := tool["strict"]; ok {
			var strict bool
			if json.Unmarshal(rawStrict, &strict) != nil {
				return invalid(joinField(indexField("tools", index), "strict"), "expected a boolean")
			}
			if strict {
				return unsupported(joinField(indexField("tools", index), "strict"))
			}
		}
	}
	return nil
}

func rejectAmbiguousResponsesRoles(raw json.RawMessage) error {
	var items []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &items) != nil {
		return nil
	}
	for index, rawItem := range items {
		item, err := decodeObject(rawItem, CodeInvalidRequest, indexField("input", index))
		if err != nil {
			return err
		}
		var kind, role string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["role"], &role)
		if kind == "message" && (role == "system" || role == "developer") {
			return unsupported(joinField(indexField("input", index), "role"))
		}
	}
	return nil
}

func chatToolChoiceToMessages(raw json.RawMessage) (any, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		switch text {
		case "auto", "none":
			return map[string]any{"type": text}, nil
		case "required":
			return map[string]any{"type": "any"}, nil
		default:
			return nil, unsupported("tool_choice")
		}
	}
	choice, err := decodeObject(raw, CodeInvalidRequest, "tool_choice")
	if err != nil {
		return nil, err
	}
	name, err := nonEmptyString(choice["function"], "tool_choice.function", false)
	if err == nil {
		return map[string]any{"type": "tool", "name": name}, nil
	}
	function, err := decodeObject(choice["function"], CodeInvalidRequest, "tool_choice.function")
	if err != nil {
		return nil, err
	}
	name, err = nonEmptyString(function["name"], "tool_choice.function.name", false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"type": "tool", "name": name}, nil
}

func positiveInteger(raw json.RawMessage, field string, upstream bool) (int64, error) {
	value, err := requireInteger(raw, field, upstream)
	if err != nil {
		return 0, err
	}
	if value <= 0 {
		if upstream {
			return 0, invalidUpstream(field, "expected a positive integer")
		}
		return 0, invalid(field, "expected a positive integer")
	}
	return value, nil
}

func nonEmptyString(raw json.RawMessage, field string, upstream bool) (string, error) {
	value, err := requireString(raw, field, upstream)
	if err != nil || value == "" {
		if upstream {
			return "", invalidUpstream(field, "a non-empty string is required")
		}
		return "", invalid(field, "a non-empty string is required")
	}
	return value, nil
}

func rawJSONObject(raw json.RawMessage, field string, upstream bool) (map[string]any, error) {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if len(raw) == 0 || decoder.Decode(&value) != nil || value == nil {
		if upstream {
			return nil, invalidUpstream(field, "expected a JSON object")
		}
		return nil, invalid(field, "expected a JSON object")
	}
	return value, nil
}

func compactJSONObject(raw json.RawMessage, field string, upstream bool) (string, error) {
	if _, err := rawJSONObject(raw, field, upstream); err != nil {
		return "", err
	}
	var buffer bytes.Buffer
	if err := json.Compact(&buffer, raw); err != nil {
		if upstream {
			return "", invalidUpstream(field, "expected a JSON object")
		}
		return "", invalid(field, "expected a JSON object")
	}
	return buffer.String(), nil
}

func mustDecoded(raw json.RawMessage) any {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	_ = decoder.Decode(&value)
	return value
}

func copyBoundedNumber(from map[string]json.RawMessage, to map[string]any, field string, maximum float64) error {
	if _, ok := from[field]; !ok {
		return nil
	}
	temporary := map[string]json.RawMessage{field: from[field]}
	converted := map[string]any{}
	if err := copyRequestOptions(temporary, converted, "__unused", "__unused"); err != nil {
		return err
	}
	value, ok := converted[field]
	if !ok {
		return invalid(field, "expected a finite number")
	}
	_ = maximum
	to[field] = value
	return nil
}
