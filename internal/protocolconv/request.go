package protocolconv

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
)

var chatRequestFields = map[string]bool{
	"model": true, "messages": true, "stream": true, "stream_options": true,
	"tools": true, "tool_choice": true, "parallel_tool_calls": true,
	"max_completion_tokens": true, "temperature": true, "top_p": true,
}

var responsesRequestFields = map[string]bool{
	"model": true, "input": true, "instructions": true, "stream": true,
	"tools": true, "tool_choice": true, "parallel_tool_calls": true,
	"max_output_tokens": true, "temperature": true, "top_p": true, "store": true,
	"background": true, "previous_response_id": true, "conversation": true,
}

// ChatRequestToResponses converts the explicitly supported Chat Completions
// subset. Unknown fields are rejected because forwarding without semantics would
// make a cross-protocol route appear more capable than it is.
func ChatRequestToResponses(raw []byte) ([]byte, error) {
	root, err := decodeObject(raw, CodeInvalidRequest, "")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(root, chatRequestFields, ""); err != nil {
		return nil, err
	}
	model, err := requireString(root["model"], "model", false)
	if err != nil || model == "" {
		return nil, invalid("model", "a non-empty string is required")
	}
	messages, err := chatMessagesToResponseItems(root["messages"])
	if err != nil {
		return nil, err
	}
	out := map[string]any{"model": model, "input": messages, "store": false}
	if err := copyRequestOptions(root, out, "max_completion_tokens", "max_output_tokens"); err != nil {
		return nil, err
	}
	if rawOptions, ok := root["stream_options"]; ok {
		options, err := decodeObject(rawOptions, CodeInvalidRequest, "stream_options")
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(options, map[string]bool{"include_usage": true}, "stream_options"); err != nil {
			return nil, err
		}
		if value, present, err := optionalBool(options, "include_usage"); err != nil {
			return nil, err
		} else if present && !value {
			return nil, unsupported("stream_options.include_usage")
		}
		// Responses terminal events include the usage snapshot without a separate option.
	}
	if rawTools, ok := root["tools"]; ok {
		tools, err := chatToolsToResponses(rawTools)
		if err != nil {
			return nil, err
		}
		out["tools"] = tools
	}
	if rawChoice, ok := root["tool_choice"]; ok {
		choice, err := chatToolChoiceToResponses(rawChoice)
		if err != nil {
			return nil, err
		}
		out["tool_choice"] = choice
	}
	return marshal(out)
}

// ResponsesRequestToChat converts the explicitly supported stateless Responses
// subset to Chat Completions.
func ResponsesRequestToChat(raw []byte) ([]byte, error) {
	root, err := decodeObject(raw, CodeInvalidRequest, "")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(root, responsesRequestFields, ""); err != nil {
		return nil, err
	}
	model, err := requireString(root["model"], "model", false)
	if err != nil || model == "" {
		return nil, invalid("model", "a non-empty string is required")
	}
	if err := rejectResponsesLifecycle(root); err != nil {
		return nil, err
	}
	messages := make([]any, 0)
	if rawInstructions, ok := root["instructions"]; ok {
		instructions, err := requireString(rawInstructions, "instructions", false)
		if err != nil {
			return nil, err
		}
		messages = append(messages, map[string]any{"role": "developer", "content": instructions})
	}
	input, err := responseInputToChatMessages(root["input"])
	if err != nil {
		return nil, err
	}
	messages = append(messages, input...)
	out := map[string]any{"model": model, "messages": messages}
	if err := copyRequestOptions(root, out, "max_output_tokens", "max_completion_tokens"); err != nil {
		return nil, err
	}
	if stream, present, err := optionalBool(root, "stream"); err != nil {
		return nil, err
	} else if present && stream {
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if rawTools, ok := root["tools"]; ok {
		tools, err := responsesToolsToChat(rawTools)
		if err != nil {
			return nil, err
		}
		out["tools"] = tools
	}
	if rawChoice, ok := root["tool_choice"]; ok {
		choice, err := responsesToolChoiceToChat(rawChoice)
		if err != nil {
			return nil, err
		}
		out["tool_choice"] = choice
	}
	return marshal(out)
}

func rejectResponsesLifecycle(root map[string]json.RawMessage) error {
	for _, field := range []string{"store", "background"} {
		value, present, err := optionalBool(root, field)
		if err != nil {
			return err
		}
		if present && value {
			return unsupported(field)
		}
	}
	for _, field := range []string{"previous_response_id", "conversation"} {
		if raw, ok := root[field]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return unsupported(field)
		}
	}
	return nil
}

func copyRequestOptions(from map[string]json.RawMessage, to map[string]any, maxSource, maxTarget string) error {
	for _, field := range []string{"stream", "parallel_tool_calls"} {
		if value, present, err := optionalBool(from, field); err != nil {
			return err
		} else if present {
			to[field] = value
		}
	}
	if raw, ok := from[maxSource]; ok {
		var value int64
		if json.Unmarshal(raw, &value) != nil || value <= 0 {
			return invalid(maxSource, "expected a positive integer")
		}
		to[maxTarget] = value
	}
	for field, maximum := range map[string]float64{"temperature": 2, "top_p": 1} {
		if raw, ok := from[field]; ok {
			var number json.Number
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			if decoder.Decode(&number) != nil {
				return invalid(field, "expected a finite number")
			}
			value, err := strconv.ParseFloat(number.String(), 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > maximum {
				return invalid(field, "number is outside the target protocol range")
			}
			to[field] = number
		}
	}
	return nil
}

func chatMessagesToResponseItems(raw json.RawMessage) ([]any, error) {
	var messages []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &messages) != nil || messages == nil {
		return nil, invalid("messages", "an array is required")
	}
	items := make([]any, 0, len(messages))
	for index, rawMessage := range messages {
		field := indexField("messages", index)
		message, err := decodeObject(rawMessage, CodeInvalidRequest, field)
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(message, map[string]bool{"role": true, "content": true, "tool_calls": true, "tool_call_id": true}, field); err != nil {
			return nil, err
		}
		role, err := requireString(message["role"], joinField(field, "role"), false)
		if err != nil {
			return nil, err
		}
		switch role {
		case "system", "developer", "user":
			content, err := requireString(message["content"], joinField(field, "content"), false)
			if err != nil {
				if rawContent := bytes.TrimSpace(message["content"]); len(rawContent) > 0 && (rawContent[0] == '[' || rawContent[0] == '{') {
					return nil, unsupported(joinField(field, "content"))
				}
				return nil, err
			}
			if _, exists := message["tool_calls"]; exists {
				return nil, unsupported(joinField(field, "tool_calls"))
			}
			items = append(items, map[string]any{"type": "message", "role": role, "content": content})
		case "assistant":
			represented := false
			if rawContent, exists := message["content"]; exists && !bytes.Equal(bytes.TrimSpace(rawContent), []byte("null")) {
				content, err := requireString(rawContent, joinField(field, "content"), false)
				if err != nil {
					if trimmed := bytes.TrimSpace(rawContent); len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '{') {
						return nil, unsupported(joinField(field, "content"))
					}
					return nil, err
				}
				items = append(items, map[string]any{"type": "message", "role": "assistant", "content": content})
				represented = true
			}
			if rawCalls, exists := message["tool_calls"]; exists {
				calls, err := chatToolCallsToResponseItems(rawCalls, joinField(field, "tool_calls"))
				if err != nil {
					return nil, err
				}
				items = append(items, calls...)
				represented = true
			}
			if !represented {
				return nil, invalid(field, "assistant message needs content or tool_calls")
			}
		case "tool":
			callID, err := requireString(message["tool_call_id"], joinField(field, "tool_call_id"), false)
			if err != nil || callID == "" {
				return nil, invalid(joinField(field, "tool_call_id"), "a non-empty string is required")
			}
			content, err := requireString(message["content"], joinField(field, "content"), false)
			if err != nil {
				return nil, err
			}
			items = append(items, map[string]any{"type": "function_call_output", "call_id": callID, "output": content})
		default:
			return nil, unsupported(joinField(field, "role"))
		}
	}
	return items, nil
}

func chatToolCallsToResponseItems(raw json.RawMessage, field string) ([]any, error) {
	var calls []json.RawMessage
	if json.Unmarshal(raw, &calls) != nil || len(calls) == 0 {
		return nil, invalid(field, "a non-empty array is required")
	}
	items := make([]any, 0, len(calls))
	for index, rawCall := range calls {
		callField := indexField(field, index)
		call, err := decodeObject(rawCall, CodeInvalidRequest, callField)
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(call, map[string]bool{"id": true, "type": true, "function": true}, callField); err != nil {
			return nil, err
		}
		callID, err := requireString(call["id"], joinField(callField, "id"), false)
		if err != nil || callID == "" {
			return nil, invalid(joinField(callField, "id"), "a non-empty string is required")
		}
		kind, err := requireString(call["type"], joinField(callField, "type"), false)
		if err != nil || kind != "function" {
			return nil, unsupported(joinField(callField, "type"))
		}
		function, err := decodeObject(call["function"], CodeInvalidRequest, joinField(callField, "function"))
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(function, map[string]bool{"name": true, "arguments": true}, joinField(callField, "function")); err != nil {
			return nil, err
		}
		name, err := requireString(function["name"], joinField(callField, "function.name"), false)
		if err != nil || name == "" {
			return nil, invalid(joinField(callField, "function.name"), "a non-empty string is required")
		}
		arguments, err := requireString(function["arguments"], joinField(callField, "function.arguments"), false)
		if err != nil {
			return nil, err
		}
		items = append(items, map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": arguments})
	}
	return items, nil
}

func responseInputToChatMessages(raw json.RawMessage) ([]any, error) {
	if len(raw) == 0 {
		return nil, invalid("input", "a string or array is required")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []any{map[string]any{"role": "user", "content": text}}, nil
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil {
		return nil, invalid("input", "a string or array is required")
	}
	messages := make([]any, 0, len(items))
	pendingCalls := make([]any, 0)
	var pendingAssistantContent *string
	flushAssistant := func() {
		if len(pendingCalls) == 0 && pendingAssistantContent == nil {
			return
		}
		message := map[string]any{"role": "assistant", "content": nil}
		if pendingAssistantContent != nil {
			message["content"] = *pendingAssistantContent
		}
		if len(pendingCalls) != 0 {
			message["tool_calls"] = pendingCalls
		}
		messages = append(messages, message)
		pendingCalls = make([]any, 0)
		pendingAssistantContent = nil
	}
	for index, rawItem := range items {
		field := indexField("input", index)
		item, err := decodeObject(rawItem, CodeInvalidRequest, field)
		if err != nil {
			return nil, err
		}
		kind, err := requireString(item["type"], joinField(field, "type"), false)
		if err != nil {
			return nil, err
		}
		switch kind {
		case "message":
			message, err := responseMessageToChat(item, field)
			if err != nil {
				return nil, err
			}
			if message["role"] == "assistant" {
				flushAssistant()
				content := message["content"].(string)
				pendingAssistantContent = &content
			} else {
				flushAssistant()
				messages = append(messages, message)
			}
		case "function_call":
			if err := rejectUnknown(item, map[string]bool{"type": true, "id": true, "call_id": true, "name": true, "arguments": true, "status": true}, field); err != nil {
				return nil, err
			}
			callID, err := requireString(item["call_id"], joinField(field, "call_id"), false)
			if err != nil || callID == "" {
				return nil, invalid(joinField(field, "call_id"), "a non-empty string is required")
			}
			name, err := requireString(item["name"], joinField(field, "name"), false)
			if err != nil || name == "" {
				return nil, invalid(joinField(field, "name"), "a non-empty string is required")
			}
			arguments, err := requireString(item["arguments"], joinField(field, "arguments"), false)
			if err != nil {
				return nil, err
			}
			pendingCalls = append(pendingCalls, map[string]any{"id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}})
		case "function_call_output":
			flushAssistant()
			if err := rejectUnknown(item, map[string]bool{"type": true, "id": true, "call_id": true, "output": true, "status": true}, field); err != nil {
				return nil, err
			}
			callID, err := requireString(item["call_id"], joinField(field, "call_id"), false)
			if err != nil || callID == "" {
				return nil, invalid(joinField(field, "call_id"), "a non-empty string is required")
			}
			output, err := requireString(item["output"], joinField(field, "output"), false)
			if err != nil {
				return nil, err
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": callID, "content": output})
		default:
			return nil, unsupported(joinField(field, "type"))
		}
	}
	flushAssistant()
	return messages, nil
}

func responseMessageToChat(item map[string]json.RawMessage, field string) (map[string]any, error) {
	if err := rejectUnknown(item, map[string]bool{"type": true, "id": true, "role": true, "content": true, "status": true}, field); err != nil {
		return nil, err
	}
	role, err := requireString(item["role"], joinField(field, "role"), false)
	if err != nil || (role != "system" && role != "developer" && role != "user" && role != "assistant") {
		return nil, unsupported(joinField(field, "role"))
	}
	content, err := responseContentText(item["content"], joinField(field, "content"), false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"role": role, "content": content}, nil
}

func responseContentText(raw json.RawMessage, field string, upstream bool) (string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || parts == nil {
		if upstream {
			return "", invalidUpstream(field, "expected text content")
		}
		return "", invalid(field, "expected text content")
	}
	var joined bytes.Buffer
	for index, rawPart := range parts {
		partField := indexField(field, index)
		part, err := decodeObject(rawPart, map[bool]ErrorCode{true: CodeInvalidUpstream, false: CodeInvalidRequest}[upstream], partField)
		if err != nil {
			return "", err
		}
		kind, err := requireString(part["type"], joinField(partField, "type"), upstream)
		if err != nil {
			return "", err
		}
		if kind != "input_text" && kind != "output_text" {
			return "", unsupported(joinField(partField, "type"))
		}
		if err := rejectUnknown(part, map[string]bool{"type": true, "text": true, "annotations": true, "logprobs": true}, partField); err != nil {
			return "", err
		}
		if annotations, ok := part["annotations"]; ok && !emptyJSONArray(annotations) {
			return "", unsupported(joinField(partField, "annotations"))
		}
		if logprobs, ok := part["logprobs"]; ok && !emptyJSONArray(logprobs) && !bytes.Equal(bytes.TrimSpace(logprobs), []byte("null")) {
			return "", unsupported(joinField(partField, "logprobs"))
		}
		value, err := requireString(part["text"], joinField(partField, "text"), upstream)
		if err != nil {
			return "", err
		}
		joined.WriteString(value)
	}
	return joined.String(), nil
}

func emptyJSONArray(raw json.RawMessage) bool {
	var values []json.RawMessage
	return json.Unmarshal(raw, &values) == nil && len(values) == 0
}

func chatToolsToResponses(raw json.RawMessage) ([]any, error) {
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
		if err := rejectUnknown(tool, map[string]bool{"type": true, "function": true}, field); err != nil {
			return nil, err
		}
		kind, err := requireString(tool["type"], joinField(field, "type"), false)
		if err != nil || kind != "function" {
			return nil, unsupported(joinField(field, "type"))
		}
		function, err := decodeObject(tool["function"], CodeInvalidRequest, joinField(field, "function"))
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(function, map[string]bool{"name": true, "description": true, "parameters": true, "strict": true}, joinField(field, "function")); err != nil {
			return nil, err
		}
		if err := validateFunctionDefinition(function, joinField(field, "function")); err != nil {
			return nil, err
		}
		converted := map[string]any{"type": "function"}
		for key, value := range function {
			var decoded any
			if json.Unmarshal(value, &decoded) != nil {
				return nil, invalid(joinField(field, "function."+key), "invalid value")
			}
			converted[key] = decoded
		}
		if name, ok := converted["name"].(string); !ok || name == "" {
			return nil, invalid(joinField(field, "function.name"), "a non-empty string is required")
		}
		result = append(result, converted)
	}
	return result, nil
}

func responsesToolsToChat(raw json.RawMessage) ([]any, error) {
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
		if err := rejectUnknown(tool, map[string]bool{"type": true, "name": true, "description": true, "parameters": true, "strict": true}, field); err != nil {
			return nil, err
		}
		kind, err := requireString(tool["type"], joinField(field, "type"), false)
		if err != nil || kind != "function" {
			return nil, unsupported(joinField(field, "type"))
		}
		if err := validateFunctionDefinition(tool, field); err != nil {
			return nil, err
		}
		function := map[string]any{}
		for key, value := range tool {
			if key == "type" {
				continue
			}
			var decoded any
			if json.Unmarshal(value, &decoded) != nil {
				return nil, invalid(joinField(field, key), "invalid value")
			}
			function[key] = decoded
		}
		if name, ok := function["name"].(string); !ok || name == "" {
			return nil, invalid(joinField(field, "name"), "a non-empty string is required")
		}
		result = append(result, map[string]any{"type": "function", "function": function})
	}
	return result, nil
}

func validateFunctionDefinition(function map[string]json.RawMessage, field string) error {
	name, err := requireString(function["name"], joinField(field, "name"), false)
	if err != nil || name == "" {
		return invalid(joinField(field, "name"), "a non-empty string is required")
	}
	if raw, ok := function["description"]; ok {
		if _, err := requireString(raw, joinField(field, "description"), false); err != nil {
			return err
		}
	}
	if raw, ok := function["parameters"]; ok {
		var schema map[string]json.RawMessage
		if json.Unmarshal(raw, &schema) != nil || schema == nil {
			return invalid(joinField(field, "parameters"), "expected a JSON Schema object")
		}
	}
	if raw, ok := function["strict"]; ok {
		var strict bool
		if json.Unmarshal(raw, &strict) != nil {
			return invalid(joinField(field, "strict"), "expected a boolean")
		}
	}
	return nil
}

func chatToolChoiceToResponses(raw json.RawMessage) (any, error) {
	var simple string
	if json.Unmarshal(raw, &simple) == nil {
		if simple != "auto" && simple != "none" && simple != "required" {
			return nil, unsupported("tool_choice")
		}
		return simple, nil
	}
	choice, err := decodeObject(raw, CodeInvalidRequest, "tool_choice")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(choice, map[string]bool{"type": true, "function": true}, "tool_choice"); err != nil {
		return nil, err
	}
	kind, _ := requireString(choice["type"], "tool_choice.type", false)
	if kind != "function" {
		return nil, unsupported("tool_choice.type")
	}
	function, err := decodeObject(choice["function"], CodeInvalidRequest, "tool_choice.function")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(function, map[string]bool{"name": true}, "tool_choice.function"); err != nil {
		return nil, err
	}
	name, err := requireString(function["name"], "tool_choice.function.name", false)
	if err != nil || name == "" {
		return nil, invalid("tool_choice.function.name", "a non-empty string is required")
	}
	return map[string]any{"type": "function", "name": name}, nil
}

func responsesToolChoiceToChat(raw json.RawMessage) (any, error) {
	var simple string
	if json.Unmarshal(raw, &simple) == nil {
		if simple != "auto" && simple != "none" && simple != "required" {
			return nil, unsupported("tool_choice")
		}
		return simple, nil
	}
	choice, err := decodeObject(raw, CodeInvalidRequest, "tool_choice")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(choice, map[string]bool{"type": true, "name": true}, "tool_choice"); err != nil {
		return nil, err
	}
	kind, _ := requireString(choice["type"], "tool_choice.type", false)
	if kind != "function" {
		return nil, unsupported("tool_choice.type")
	}
	name, err := requireString(choice["name"], "tool_choice.name", false)
	if err != nil || name == "" {
		return nil, invalid("tool_choice.name", "a non-empty string is required")
	}
	return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
}
