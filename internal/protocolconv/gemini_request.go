package protocolconv

import (
	"bytes"
	"encoding/json"
)

var geminiRequestFields = map[string]bool{
	"contents": true, "systemInstruction": true, "tools": true,
	"toolConfig": true, "generationConfig": true,
}

// GeminiRequestToResponses converts a generateContent body. The model is a
// separate argument because Gemini carries it in the request path.
func GeminiRequestToResponses(model string, raw []byte) ([]byte, error) {
	converted, _, err := geminiRequestToResponses(model, raw)
	return converted, err
}

func geminiRequestToResponses(model string, raw []byte) ([]byte, FeatureSet, error) {
	if model == "" {
		return nil, 0, invalid("model", "a non-empty string is required")
	}
	root, err := decodeObject(raw, CodeInvalidRequest, "")
	if err != nil {
		return nil, 0, err
	}
	if err := rejectUnknown(root, geminiRequestFields, ""); err != nil {
		return nil, 0, err
	}
	out := map[string]any{"model": model, "store": false}
	features := Features(FeatureText, FeatureUsage, FeatureFinishReason)
	if rawSystem, ok := root["systemInstruction"]; ok {
		instruction, err := geminiSystemText(rawSystem)
		if err != nil {
			return nil, 0, err
		}
		out["instructions"] = instruction
		features |= Features(FeatureSystemInstruction)
	}
	items, itemFeatures, err := geminiContentsToResponseItems(root["contents"])
	if err != nil {
		return nil, 0, err
	}
	out["input"] = items
	features |= itemFeatures
	if rawTools, ok := root["tools"]; ok {
		tools, err := geminiToolsToResponses(rawTools)
		if err != nil {
			return nil, 0, err
		}
		out["tools"] = tools
		features |= Features(FeatureFunctionDefinitions, FeatureFunctionCalls)
	}
	if rawConfig, ok := root["toolConfig"]; ok {
		choice, err := geminiToolConfigToResponses(rawConfig)
		if err != nil {
			return nil, 0, err
		}
		out["tool_choice"] = choice
	}
	if rawGeneration, ok := root["generationConfig"]; ok {
		if err := geminiGenerationToResponses(rawGeneration, out); err != nil {
			return nil, 0, err
		}
	}
	converted, err := marshal(out)
	return converted, features, err
}

func geminiSystemText(raw json.RawMessage) (string, error) {
	root, err := decodeObject(raw, CodeInvalidRequest, "systemInstruction")
	if err != nil {
		return "", err
	}
	if err := rejectUnknown(root, map[string]bool{"role": true, "parts": true}, "systemInstruction"); err != nil {
		return "", err
	}
	if _, ok := root["role"]; ok {
		return "", unsupported("systemInstruction.role")
	}
	return geminiTextParts(root["parts"], "systemInstruction.parts")
}

func geminiContentsToResponseItems(raw json.RawMessage) ([]any, FeatureSet, error) {
	var contents []json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &contents) != nil || contents == nil {
		return nil, 0, invalid("contents", "an array is required")
	}
	items := make([]any, 0, len(contents))
	features := FeatureSet(0)
	pendingCalls := map[string]string{}
	pendingOrder := make([]canonicalCall, 0)
	callIndex := 0
	for contentIndex, rawContent := range contents {
		field := indexField("contents", contentIndex)
		content, err := decodeObject(rawContent, CodeInvalidRequest, field)
		if err != nil {
			return nil, 0, err
		}
		if err := rejectUnknown(content, map[string]bool{"role": true, "parts": true}, field); err != nil {
			return nil, 0, err
		}
		role, err := requireString(content["role"], joinField(field, "role"), false)
		if err != nil || (role != "user" && role != "model") {
			return nil, 0, unsupported(joinField(field, "role"))
		}
		responseRole := map[string]string{"user": "user", "model": "assistant"}[role]
		var parts []json.RawMessage
		if json.Unmarshal(content["parts"], &parts) != nil || parts == nil {
			return nil, 0, invalid(joinField(field, "parts"), "an array is required")
		}
		for partIndex, rawPart := range parts {
			partField := indexField(joinField(field, "parts"), partIndex)
			part, err := decodeObject(rawPart, CodeInvalidRequest, partField)
			if err != nil {
				return nil, 0, err
			}
			switch {
			case part["text"] != nil:
				if err := rejectUnknown(part, map[string]bool{"text": true}, partField); err != nil {
					return nil, 0, err
				}
				text, err := requireString(part["text"], joinField(partField, "text"), false)
				if err != nil {
					return nil, 0, err
				}
				items = append(items, map[string]any{"type": "message", "role": responseRole, "content": text})
				features |= Features(FeatureText)
			case part["functionCall"] != nil:
				if role != "model" {
					return nil, 0, unsupported(joinField(partField, "functionCall"))
				}
				if err := rejectUnknown(part, map[string]bool{"functionCall": true}, partField); err != nil {
					return nil, 0, err
				}
				call, err := decodeObject(part["functionCall"], CodeInvalidRequest, joinField(partField, "functionCall"))
				if err != nil {
					return nil, 0, err
				}
				if err := rejectUnknown(call, map[string]bool{"id": true, "name": true, "args": true}, joinField(partField, "functionCall")); err != nil {
					return nil, 0, err
				}
				id, present, err := optionalNonEmptyString(call["id"], joinField(partField, "functionCall.id"), false)
				if err != nil {
					return nil, 0, err
				}
				name, err := nonEmptyString(call["name"], joinField(partField, "functionCall.name"), false)
				if err != nil {
					return nil, 0, err
				}
				if !present {
					id = convertedItemID("call", "gemini-request", callIndex)
				}
				args := "{}"
				if len(call["args"]) != 0 {
					args, err = compactJSONObject(call["args"], joinField(partField, "functionCall.args"), false)
					if err != nil {
						return nil, 0, err
					}
				}
				items = append(items, map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": args})
				pendingCalls[id] = name
				pendingOrder = append(pendingOrder, canonicalCall{ID: id, Name: name})
				callIndex++
				features |= Features(FeatureFunctionCalls)
			case part["functionResponse"] != nil:
				if role != "user" {
					return nil, 0, unsupported(joinField(partField, "functionResponse"))
				}
				if err := rejectUnknown(part, map[string]bool{"functionResponse": true}, partField); err != nil {
					return nil, 0, err
				}
				response, err := decodeObject(part["functionResponse"], CodeInvalidRequest, joinField(partField, "functionResponse"))
				if err != nil {
					return nil, 0, err
				}
				if err := rejectUnknown(response, map[string]bool{"id": true, "name": true, "response": true}, joinField(partField, "functionResponse")); err != nil {
					return nil, 0, err
				}
				id, present, err := optionalNonEmptyString(response["id"], joinField(partField, "functionResponse.id"), false)
				if err != nil {
					return nil, 0, err
				}
				name, err := nonEmptyString(response["name"], joinField(partField, "functionResponse.name"), false)
				if err != nil {
					return nil, 0, err
				}
				if !present {
					for _, pending := range pendingOrder {
						if pendingCalls[pending.ID] == name {
							id = pending.ID
							break
						}
					}
				}
				if id == "" || pendingCalls[id] != name {
					return nil, 0, invalid(joinField(partField, "functionResponse.name"), "does not match the preceding functionCall")
				}
				delete(pendingCalls, id)
				result, err := compactJSONObject(response["response"], joinField(partField, "functionResponse.response"), false)
				if err != nil {
					return nil, 0, err
				}
				items = append(items, map[string]any{"type": "function_call_output", "call_id": id, "output": result})
				features |= Features(FeatureFunctionResults)
			default:
				return nil, 0, unsupported(partField)
			}
		}
	}
	return items, features, nil
}

func optionalNonEmptyString(raw json.RawMessage, field string, upstream bool) (string, bool, error) {
	if len(raw) == 0 {
		return "", false, nil
	}
	value, err := nonEmptyString(raw, field, upstream)
	return value, true, err
}

func geminiTextParts(raw json.RawMessage, field string) (string, error) {
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || parts == nil {
		return "", invalid(field, "an array is required")
	}
	var output bytes.Buffer
	for index, rawPart := range parts {
		partField := indexField(field, index)
		part, err := decodeObject(rawPart, CodeInvalidRequest, partField)
		if err != nil {
			return "", err
		}
		if err := rejectUnknown(part, map[string]bool{"text": true}, partField); err != nil {
			return "", err
		}
		text, err := requireString(part["text"], joinField(partField, "text"), false)
		if err != nil {
			return "", err
		}
		output.WriteString(text)
	}
	return output.String(), nil
}

func geminiToolsToResponses(raw json.RawMessage) ([]any, error) {
	var groups []json.RawMessage
	if json.Unmarshal(raw, &groups) != nil || groups == nil {
		return nil, invalid("tools", "an array is required")
	}
	tools := make([]any, 0)
	for groupIndex, rawGroup := range groups {
		groupField := indexField("tools", groupIndex)
		group, err := decodeObject(rawGroup, CodeInvalidRequest, groupField)
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(group, map[string]bool{"functionDeclarations": true}, groupField); err != nil {
			return nil, err
		}
		var declarations []json.RawMessage
		if json.Unmarshal(group["functionDeclarations"], &declarations) != nil || declarations == nil {
			return nil, invalid(joinField(groupField, "functionDeclarations"), "an array is required")
		}
		for declarationIndex, rawDeclaration := range declarations {
			field := indexField(joinField(groupField, "functionDeclarations"), declarationIndex)
			declaration, err := decodeObject(rawDeclaration, CodeInvalidRequest, field)
			if err != nil {
				return nil, err
			}
			if err := rejectUnknown(declaration, map[string]bool{"name": true, "description": true, "parameters": true, "parametersJsonSchema": true}, field); err != nil {
				return nil, err
			}
			if _, old := declaration["parameters"]; old && declaration["parametersJsonSchema"] != nil {
				return nil, unsupported(joinField(field, "parametersJsonSchema"))
			}
			name, err := nonEmptyString(declaration["name"], joinField(field, "name"), false)
			if err != nil {
				return nil, err
			}
			parameters := declaration["parametersJsonSchema"]
			if len(parameters) == 0 {
				parameters = declaration["parameters"]
			}
			schema, err := rawJSONObject(parameters, joinField(field, "parameters"), false)
			if err != nil {
				return nil, err
			}
			tool := map[string]any{"type": "function", "name": name, "parameters": schema}
			if rawDescription, ok := declaration["description"]; ok {
				description, err := requireString(rawDescription, joinField(field, "description"), false)
				if err != nil {
					return nil, err
				}
				tool["description"] = description
			}
			tools = append(tools, tool)
		}
	}
	return tools, nil
}

func geminiToolConfigToResponses(raw json.RawMessage) (any, error) {
	root, err := decodeObject(raw, CodeInvalidRequest, "toolConfig")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(root, map[string]bool{"functionCallingConfig": true}, "toolConfig"); err != nil {
		return nil, err
	}
	config, err := decodeObject(root["functionCallingConfig"], CodeInvalidRequest, "toolConfig.functionCallingConfig")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(config, map[string]bool{"mode": true, "allowedFunctionNames": true}, "toolConfig.functionCallingConfig"); err != nil {
		return nil, err
	}
	mode, err := requireString(config["mode"], "toolConfig.functionCallingConfig.mode", false)
	if err != nil {
		return nil, err
	}
	var allowed []string
	if rawAllowed, ok := config["allowedFunctionNames"]; ok {
		if json.Unmarshal(rawAllowed, &allowed) != nil {
			return nil, invalid("toolConfig.functionCallingConfig.allowedFunctionNames", "an array of strings is required")
		}
	}
	switch mode {
	case "AUTO":
		if len(allowed) != 0 {
			return nil, unsupported("toolConfig.functionCallingConfig.allowedFunctionNames")
		}
		return "auto", nil
	case "NONE":
		if len(allowed) != 0 {
			return nil, unsupported("toolConfig.functionCallingConfig.allowedFunctionNames")
		}
		return "none", nil
	case "ANY":
		if len(allowed) == 0 {
			return "required", nil
		}
		if len(allowed) == 1 && allowed[0] != "" {
			return map[string]any{"type": "function", "name": allowed[0]}, nil
		}
		return nil, unsupported("toolConfig.functionCallingConfig.allowedFunctionNames")
	default:
		return nil, unsupported("toolConfig.functionCallingConfig.mode")
	}
}

func geminiGenerationToResponses(raw json.RawMessage, out map[string]any) error {
	config, err := decodeObject(raw, CodeInvalidRequest, "generationConfig")
	if err != nil {
		return err
	}
	if err := rejectUnknown(config, map[string]bool{"maxOutputTokens": true, "temperature": true, "topP": true, "candidateCount": true}, "generationConfig"); err != nil {
		return err
	}
	if rawMax, ok := config["maxOutputTokens"]; ok {
		value, err := positiveInteger(rawMax, "generationConfig.maxOutputTokens", false)
		if err != nil {
			return err
		}
		out["max_output_tokens"] = value
	}
	if rawCandidates, ok := config["candidateCount"]; ok {
		value, err := positiveInteger(rawCandidates, "generationConfig.candidateCount", false)
		if err != nil {
			return err
		}
		if value != 1 {
			return unsupported("generationConfig.candidateCount")
		}
	}
	for source, target := range map[string]string{"temperature": "temperature", "topP": "top_p"} {
		if rawValue, ok := config[source]; ok {
			temporary := map[string]json.RawMessage{target: rawValue}
			converted := map[string]any{}
			if err := copyBoundedNumber(temporary, converted, target, map[string]float64{"temperature": 2, "top_p": 1}[target]); err != nil {
				return err
			}
			out[target] = converted[target]
		}
	}
	return nil
}

// ResponsesRequestToGemini returns the model path component separately from
// the generateContent JSON body.
func ResponsesRequestToGemini(raw []byte) (string, []byte, error) {
	model, converted, _, err := responsesRequestToGemini(raw)
	return model, converted, err
}

func responsesRequestToGemini(raw []byte) (string, []byte, FeatureSet, error) {
	root, err := decodeObject(raw, CodeInvalidRequest, "")
	if err != nil {
		return "", nil, 0, err
	}
	if err := rejectAmbiguousResponsesRoles(root["input"]); err != nil {
		return "", nil, 0, err
	}
	if err := rejectEnabledStrictTools(root["tools"]); err != nil {
		return "", nil, 0, err
	}
	chat, err := ResponsesRequestToChat(raw)
	if err != nil {
		return "", nil, 0, err
	}
	chatRoot, _ := decodeObject(chat, CodeInvalidRequest, "")
	model, _ := requireString(chatRoot["model"], "model", false)
	out := map[string]any{}
	features := Features(FeatureText, FeatureUsage, FeatureFinishReason)
	var chatMessages []json.RawMessage
	_ = json.Unmarshal(chatRoot["messages"], &chatMessages)
	contents := make([]any, 0, len(chatMessages))
	callNames := map[string]string{}
	lastWasFunctionResponse := false
	for index, rawMessage := range chatMessages {
		message, _ := decodeObject(rawMessage, CodeInvalidRequest, "")
		role, _ := requireString(message["role"], "", false)
		if role == "developer" {
			lastWasFunctionResponse = false
			if index != 0 {
				return "", nil, 0, unsupported(joinField(indexField("messages", index), "role"))
			}
			out["systemInstruction"] = map[string]any{"parts": []any{map[string]any{"text": mustDecoded(message["content"])}}}
			features |= Features(FeatureSystemInstruction)
			continue
		}
		parts := make([]any, 0)
		geminiRole := map[string]string{"user": "user", "assistant": "model", "tool": "user"}[role]
		if geminiRole == "" {
			return "", nil, 0, unsupported(joinField(indexField("messages", index), "role"))
		}
		if role == "tool" {
			callID, _ := requireString(message["tool_call_id"], "", false)
			name := callNames[callID]
			if name == "" {
				return "", nil, 0, unsupported(joinField(indexField("messages", index), "tool_call_id"))
			}
			var response map[string]any
			text, _ := requireString(message["content"], "", false)
			if json.Unmarshal([]byte(text), &response) != nil || response == nil {
				return "", nil, 0, unsupported(joinField(indexField("messages", index), "content"))
			}
			parts = append(parts, map[string]any{"functionResponse": map[string]any{"id": callID, "name": name, "response": response}})
			features |= Features(FeatureFunctionResults)
		} else {
			if rawContent, ok := message["content"]; ok && !bytes.Equal(bytes.TrimSpace(rawContent), []byte("null")) {
				parts = append(parts, map[string]any{"text": mustDecoded(rawContent)})
			}
			if rawCalls, ok := message["tool_calls"]; ok {
				var calls []json.RawMessage
				_ = json.Unmarshal(rawCalls, &calls)
				for _, rawCall := range calls {
					call, _ := decodeObject(rawCall, CodeInvalidRequest, "")
					function, _ := decodeObject(call["function"], CodeInvalidRequest, "")
					id, _ := requireString(call["id"], "", false)
					name, _ := requireString(function["name"], "", false)
					arguments, _ := requireString(function["arguments"], "", false)
					var args map[string]any
					if json.Unmarshal([]byte(arguments), &args) != nil || args == nil {
						return "", nil, 0, invalid("input", "function arguments must be a JSON object")
					}
					parts = append(parts, map[string]any{"functionCall": map[string]any{"id": id, "name": name, "args": args}})
					callNames[id] = name
					features |= Features(FeatureFunctionCalls)
				}
			}
		}
		if role == "tool" && lastWasFunctionResponse {
			previous := contents[len(contents)-1].(map[string]any)
			previous["parts"] = append(previous["parts"].([]any), parts...)
		} else {
			contents = append(contents, map[string]any{"role": geminiRole, "parts": parts})
		}
		lastWasFunctionResponse = role == "tool"
	}
	out["contents"] = contents
	if rawTools, ok := chatRoot["tools"]; ok {
		var tools []json.RawMessage
		_ = json.Unmarshal(rawTools, &tools)
		declarations := make([]any, 0, len(tools))
		for _, rawTool := range tools {
			tool, _ := decodeObject(rawTool, CodeInvalidRequest, "")
			function, _ := decodeObject(tool["function"], CodeInvalidRequest, "")
			declaration := map[string]any{"name": mustDecoded(function["name"]), "parametersJsonSchema": mustDecoded(function["parameters"])}
			if description, ok := function["description"]; ok {
				declaration["description"] = mustDecoded(description)
			}
			declarations = append(declarations, declaration)
		}
		out["tools"] = []any{map[string]any{"functionDeclarations": declarations}}
		features |= Features(FeatureFunctionDefinitions, FeatureFunctionCalls)
	}
	if rawChoice, ok := chatRoot["tool_choice"]; ok {
		config, err := responsesChoiceToGemini(rawChoice)
		if err != nil {
			return "", nil, 0, err
		}
		out["toolConfig"] = map[string]any{"functionCallingConfig": config}
	}
	generation := map[string]any{}
	for source, target := range map[string]string{"max_completion_tokens": "maxOutputTokens", "temperature": "temperature", "top_p": "topP"} {
		if value, ok := chatRoot[source]; ok {
			generation[target] = mustDecoded(value)
		}
	}
	if len(generation) > 0 {
		out["generationConfig"] = generation
	}
	converted, err := marshal(out)
	return model, converted, features, err
}

func responsesChoiceToGemini(raw json.RawMessage) (map[string]any, error) {
	var choice string
	if json.Unmarshal(raw, &choice) == nil {
		switch choice {
		case "auto":
			return map[string]any{"mode": "AUTO"}, nil
		case "none":
			return map[string]any{"mode": "NONE"}, nil
		case "required":
			return map[string]any{"mode": "ANY"}, nil
		default:
			return nil, unsupported("tool_choice")
		}
	}
	object, err := decodeObject(raw, CodeInvalidRequest, "tool_choice")
	if err != nil {
		return nil, err
	}
	name, err := nonEmptyString(object["function"], "tool_choice.function", false)
	if err == nil {
		return map[string]any{"mode": "ANY", "allowedFunctionNames": []string{name}}, nil
	}
	function, err := decodeObject(object["function"], CodeInvalidRequest, "tool_choice.function")
	if err != nil {
		return nil, err
	}
	name, err = nonEmptyString(function["name"], "tool_choice.function.name", false)
	if err != nil {
		return nil, err
	}
	return map[string]any{"mode": "ANY", "allowedFunctionNames": []string{name}}, nil
}
