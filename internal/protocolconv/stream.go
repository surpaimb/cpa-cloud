package protocolconv

import (
	"bytes"
	"encoding/json"
	"sort"
)

// SSEEvent is one logical event. Data excludes SSE framing. A caller must stop
// after Terminal and must treat any EOF before a terminal event as interrupted.
type SSEEvent struct {
	Name     string
	Data     json.RawMessage
	Terminal bool
}

type chatToolStream struct {
	index       int
	outputIndex int
	itemID      string
	callID      string
	name        string
	arguments   bytes.Buffer
	added       bool
}

// ChatToResponsesStream converts complete Chat Completions data objects into
// Responses events. Call Finish only for an explicit [DONE] marker; call EOF
// for transport EOF without [DONE].
type ChatToResponsesStream struct {
	sequence       int64
	started        bool
	terminal       bool
	responseID     string
	model          string
	created        int64
	nextOutput     int
	textIndex      int
	textItemID     string
	textAdded      bool
	text           bytes.Buffer
	tools          map[int]*chatToolStream
	finishReason   string
	convertedUsage map[string]any
}

type chatOutputRef struct {
	outputIndex int
	text        bool
	tool        *chatToolStream
}

func (s *ChatToResponsesStream) event(name string, payload map[string]any, terminal bool) (SSEEvent, error) {
	payload["type"] = name
	payload["sequence_number"] = s.sequence
	s.sequence++
	data, err := marshal(payload)
	if err != nil {
		return SSEEvent{}, err
	}
	return SSEEvent{Name: name, Data: data, Terminal: terminal}, nil
}

// Feed accepts one complete Chat Completions JSON data object (not [DONE]).
func (s *ChatToResponsesStream) Feed(raw []byte) ([]SSEEvent, error) {
	if s == nil || s.terminal {
		return nil, invalidUpstream("", "stream is already terminal")
	}
	root, err := decodeObject(raw, CodeInvalidUpstream, "")
	if err != nil {
		return nil, err
	}
	if rawError, ok := root["error"]; ok && !bytes.Equal(bytes.TrimSpace(rawError), []byte("null")) {
		return nil, invalidUpstream("error", "upstream returned an error event")
	}
	if err := rejectUnknown(root, map[string]bool{
		"id": true, "object": true, "created": true, "model": true,
		"choices": true, "usage": true,
	}, ""); err != nil {
		return nil, err
	}
	id, err := requireString(root["id"], "id", true)
	if err != nil || id == "" {
		return nil, invalidUpstream("id", "a non-empty string is required")
	}
	object, err := requireString(root["object"], "object", true)
	if err != nil || object != "chat.completion.chunk" {
		return nil, invalidUpstream("object", "expected chat.completion.chunk")
	}
	model, err := requireString(root["model"], "model", true)
	if err != nil || model == "" {
		return nil, invalidUpstream("model", "a non-empty string is required")
	}
	created, err := requireInteger(root["created"], "created", true)
	if err != nil {
		return nil, err
	}
	result := make([]SSEEvent, 0, 4)
	if !s.started {
		s.started, s.responseID, s.model, s.created = true, id, model, created
		s.textIndex = -1
		s.tools = make(map[int]*chatToolStream)
		response := s.responseSnapshot("in_progress", nil)
		createdEvent, _ := s.event("response.created", map[string]any{"response": response}, false)
		progressEvent, _ := s.event("response.in_progress", map[string]any{"response": response}, false)
		result = append(result, createdEvent, progressEvent)
	} else if id != s.responseID || model != s.model || created != s.created {
		return nil, invalidUpstream("", "stream metadata changed")
	}
	if rawUsage, ok := root["usage"]; ok && !bytes.Equal(bytes.TrimSpace(rawUsage), []byte("null")) {
		usage, err := chatUsageToResponses(rawUsage, "usage", true)
		if err != nil {
			return nil, err
		}
		s.convertedUsage = usage
	}
	var choices []json.RawMessage
	if json.Unmarshal(root["choices"], &choices) != nil || len(choices) > 1 {
		return nil, invalidUpstream("choices", "at most one choice is supported")
	}
	if s.finishReason != "" && len(choices) != 0 {
		return nil, invalidUpstream("choices", "semantic chunks arrived after finish reason")
	}
	if len(choices) == 0 {
		return result, nil
	}
	choice, err := decodeObject(choices[0], CodeInvalidUpstream, "choices[0]")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(choice, map[string]bool{"index": true, "delta": true, "finish_reason": true}, "choices[0]"); err != nil {
		return nil, err
	}
	index, err := requireInteger(choice["index"], "choices[0].index", true)
	if err != nil || index != 0 {
		return nil, invalidUpstream("choices[0].index", "only choice zero is supported")
	}
	delta, err := decodeObject(choice["delta"], CodeInvalidUpstream, "choices[0].delta")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(delta, map[string]bool{"role": true, "content": true, "tool_calls": true}, "choices[0].delta"); err != nil {
		return nil, err
	}
	if rawRole, ok := delta["role"]; ok {
		role, err := requireString(rawRole, "choices[0].delta.role", true)
		if err != nil || role != "assistant" {
			return nil, invalidUpstream("choices[0].delta.role", "expected assistant")
		}
	}
	if rawContent, ok := delta["content"]; ok && !bytes.Equal(bytes.TrimSpace(rawContent), []byte("null")) {
		text, err := requireString(rawContent, "choices[0].delta.content", true)
		if err != nil {
			return nil, err
		}
		added, err := s.ensureTextItem()
		if err != nil {
			return nil, err
		}
		result = append(result, added...)
		s.text.WriteString(text)
		event, _ := s.event("response.output_text.delta", map[string]any{
			"response_id": s.responseID, "item_id": s.textItemID, "output_index": s.textIndex,
			"content_index": 0, "delta": text,
		}, false)
		result = append(result, event)
	}
	if rawCalls, ok := delta["tool_calls"]; ok {
		events, err := s.feedToolDeltas(rawCalls)
		if err != nil {
			return nil, err
		}
		result = append(result, events...)
	}
	if rawFinish, ok := choice["finish_reason"]; ok && !bytes.Equal(bytes.TrimSpace(rawFinish), []byte("null")) {
		finish, err := requireString(rawFinish, "choices[0].finish_reason", true)
		if err != nil || (finish != "stop" && finish != "tool_calls") {
			return nil, unsupported("choices[0].finish_reason")
		}
		if s.finishReason != "" && s.finishReason != finish {
			return nil, invalidUpstream("choices[0].finish_reason", "finish reason changed")
		}
		s.finishReason = finish
	}
	return result, nil
}

func (s *ChatToResponsesStream) ensureTextItem() ([]SSEEvent, error) {
	if s.textAdded {
		return nil, nil
	}
	s.textAdded = true
	s.textIndex = s.nextOutput
	s.nextOutput++
	s.textItemID = convertedItemID("msg", s.responseID, s.textIndex)
	item := map[string]any{"id": s.textItemID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
	added, _ := s.event("response.output_item.added", map[string]any{"response_id": s.responseID, "output_index": s.textIndex, "item": item}, false)
	part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}}
	partAdded, _ := s.event("response.content_part.added", map[string]any{"response_id": s.responseID, "item_id": s.textItemID, "output_index": s.textIndex, "content_index": 0, "part": part}, false)
	return []SSEEvent{added, partAdded}, nil
}

func (s *ChatToResponsesStream) feedToolDeltas(raw json.RawMessage) ([]SSEEvent, error) {
	var deltas []json.RawMessage
	if json.Unmarshal(raw, &deltas) != nil || len(deltas) == 0 {
		return nil, invalidUpstream("choices[0].delta.tool_calls", "a non-empty array is required")
	}
	result := make([]SSEEvent, 0, len(deltas)*2)
	for position, rawDelta := range deltas {
		field := indexField("choices[0].delta.tool_calls", position)
		delta, err := decodeObject(rawDelta, CodeInvalidUpstream, field)
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(delta, map[string]bool{"index": true, "id": true, "type": true, "function": true}, field); err != nil {
			return nil, err
		}
		toolIndex64, err := requireInteger(delta["index"], joinField(field, "index"), true)
		if err != nil || toolIndex64 > 1024 {
			return nil, invalidUpstream(joinField(field, "index"), "invalid tool index")
		}
		toolIndex := int(toolIndex64)
		tool := s.tools[toolIndex]
		if tool == nil {
			callID, err := requireString(delta["id"], joinField(field, "id"), true)
			if err != nil || callID == "" {
				return nil, invalidUpstream(joinField(field, "id"), "the first delta needs a call id")
			}
			kind, err := requireString(delta["type"], joinField(field, "type"), true)
			if err != nil || kind != "function" {
				return nil, unsupported(joinField(field, "type"))
			}
			function, err := decodeObject(delta["function"], CodeInvalidUpstream, joinField(field, "function"))
			if err != nil {
				return nil, err
			}
			if err := rejectUnknown(function, map[string]bool{"name": true, "arguments": true}, joinField(field, "function")); err != nil {
				return nil, err
			}
			name, err := requireString(function["name"], joinField(field, "function.name"), true)
			if err != nil || name == "" {
				return nil, invalidUpstream(joinField(field, "function.name"), "the first delta needs a function name")
			}
			tool = &chatToolStream{index: toolIndex, outputIndex: s.nextOutput, itemID: convertedItemID("fc", s.responseID, s.nextOutput), callID: callID, name: name, added: true}
			s.nextOutput++
			s.tools[toolIndex] = tool
			item := map[string]any{"id": tool.itemID, "type": "function_call", "status": "in_progress", "call_id": callID, "name": name, "arguments": ""}
			added, _ := s.event("response.output_item.added", map[string]any{"response_id": s.responseID, "output_index": tool.outputIndex, "item": item}, false)
			result = append(result, added)
		} else {
			if rawID, ok := delta["id"]; ok {
				id, err := requireString(rawID, joinField(field, "id"), true)
				if err != nil || id != tool.callID {
					return nil, invalidUpstream(joinField(field, "id"), "tool call id changed")
				}
			}
		}
		if rawFunction, ok := delta["function"]; ok {
			function, err := decodeObject(rawFunction, CodeInvalidUpstream, joinField(field, "function"))
			if err != nil {
				return nil, err
			}
			if err := rejectUnknown(function, map[string]bool{"name": true, "arguments": true}, joinField(field, "function")); err != nil {
				return nil, err
			}
			if rawName, ok := function["name"]; ok {
				name, err := requireString(rawName, joinField(field, "function.name"), true)
				if err != nil || name != tool.name {
					return nil, invalidUpstream(joinField(field, "function.name"), "function name changed")
				}
			}
			if rawArguments, ok := function["arguments"]; ok {
				arguments, err := requireString(rawArguments, joinField(field, "function.arguments"), true)
				if err != nil {
					return nil, err
				}
				tool.arguments.WriteString(arguments)
				event, _ := s.event("response.function_call_arguments.delta", map[string]any{"response_id": s.responseID, "item_id": tool.itemID, "output_index": tool.outputIndex, "delta": arguments}, false)
				result = append(result, event)
			}
		}
	}
	return result, nil
}

// Finish consumes an explicit upstream [DONE] marker.
func (s *ChatToResponsesStream) Finish() ([]SSEEvent, error) {
	if s == nil || s.terminal {
		return nil, invalidUpstream("", "stream is already terminal")
	}
	if !s.started || s.finishReason == "" {
		return nil, interrupted("Chat stream ended before a finish reason")
	}
	if !s.textAdded && len(s.tools) == 0 {
		return nil, invalidUpstream("choices[0].finish_reason", "stream has no representable output")
	}
	if s.finishReason == "tool_calls" && len(s.tools) == 0 {
		return nil, invalidUpstream("choices[0].finish_reason", "tool_calls were not emitted")
	}
	if s.finishReason == "stop" && len(s.tools) != 0 {
		return nil, invalidUpstream("choices[0].finish_reason", "tool calls require tool_calls finish reason")
	}
	result := make([]SSEEvent, 0, 2+len(s.tools)*2)
	for _, output := range s.orderedOutputs() {
		if output.text {
			text := s.text.String()
			done, _ := s.event("response.output_text.done", map[string]any{"response_id": s.responseID, "item_id": s.textItemID, "output_index": s.textIndex, "content_index": 0, "text": text}, false)
			part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
			partDone, _ := s.event("response.content_part.done", map[string]any{"response_id": s.responseID, "item_id": s.textItemID, "output_index": s.textIndex, "content_index": 0, "part": part}, false)
			item := map[string]any{"id": s.textItemID, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
			itemDone, _ := s.event("response.output_item.done", map[string]any{"response_id": s.responseID, "output_index": s.textIndex, "item": item}, false)
			result = append(result, done, partDone, itemDone)
			continue
		}
		tool := output.tool
		arguments := tool.arguments.String()
		argsDone, _ := s.event("response.function_call_arguments.done", map[string]any{"response_id": s.responseID, "item_id": tool.itemID, "output_index": tool.outputIndex, "arguments": arguments}, false)
		item := map[string]any{"id": tool.itemID, "type": "function_call", "status": "completed", "call_id": tool.callID, "name": tool.name, "arguments": arguments}
		itemDone, _ := s.event("response.output_item.done", map[string]any{"response_id": s.responseID, "output_index": tool.outputIndex, "item": item}, false)
		result = append(result, argsDone, itemDone)
	}
	s.terminal = true
	response := s.responseSnapshot("completed", s.convertedUsage)
	completed, _ := s.event("response.completed", map[string]any{"response": response}, true)
	result = append(result, completed)
	return result, nil
}

// EOF reports transport EOF without an explicit [DONE].
func (s *ChatToResponsesStream) EOF() error {
	if s != nil && s.terminal {
		return nil
	}
	return interrupted("Chat stream ended before [DONE]")
}

func (s *ChatToResponsesStream) responseSnapshot(status string, usage map[string]any) map[string]any {
	output := make([]any, 0, s.nextOutput)
	for _, ref := range s.orderedOutputs() {
		if ref.text {
			output = append(output, map[string]any{
				"id": s.textItemID, "type": "message", "status": status, "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": s.text.String(), "annotations": []any{}}},
			})
			continue
		}
		tool := ref.tool
		output = append(output, map[string]any{"id": tool.itemID, "type": "function_call", "status": status, "call_id": tool.callID, "name": tool.name, "arguments": tool.arguments.String()})
	}
	response := map[string]any{"id": s.responseID, "object": "response", "created_at": s.created, "model": s.model, "status": status, "output": output}
	if usage != nil {
		response["usage"] = usage
	}
	return response
}

func (s *ChatToResponsesStream) orderedOutputs() []chatOutputRef {
	outputs := make([]chatOutputRef, 0, s.nextOutput)
	if s.textAdded {
		outputs = append(outputs, chatOutputRef{outputIndex: s.textIndex, text: true})
	}
	for _, tool := range s.tools {
		outputs = append(outputs, chatOutputRef{outputIndex: tool.outputIndex, tool: tool})
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].outputIndex < outputs[j].outputIndex })
	return outputs
}

type responseStreamItem struct {
	kind        string
	itemID      string
	outputIndex int
	chatIndex   int
	callID      string
	name        string
	text        bytes.Buffer
	arguments   bytes.Buffer
	done        bool
}

// ResponsesToChatStream converts complete Responses event objects into Chat
// Completions chunks and a final [DONE].
type ResponsesToChatStream struct {
	started    bool
	terminal   bool
	responseID string
	model      string
	created    int64
	roleSent   bool
	items      map[string]*responseStreamItem
	outputs    map[int]*responseStreamItem
	nextTool   int
	nextSeq    int64
	seqStarted bool
}

// Feed accepts one complete Responses event data object.
func (s *ResponsesToChatStream) Feed(raw []byte) ([]SSEEvent, error) {
	if s == nil || s.terminal {
		return nil, invalidUpstream("", "stream is already terminal")
	}
	event, err := decodeObject(raw, CodeInvalidUpstream, "")
	if err != nil {
		return nil, err
	}
	sequence, err := requireInteger(event["sequence_number"], "sequence_number", true)
	if err != nil {
		return nil, err
	}
	if !s.seqStarted {
		if sequence != 0 {
			return nil, invalidUpstream("sequence_number", "the first event must have sequence zero")
		}
		s.seqStarted = true
	} else if sequence != s.nextSeq {
		return nil, invalidUpstream("sequence_number", "event sequence is not contiguous")
	}
	s.nextSeq = sequence + 1
	kind, err := requireString(event["type"], "type", true)
	if err != nil {
		return nil, err
	}
	if err := validateResponsesEventFields(kind, event); err != nil {
		return nil, err
	}
	switch kind {
	case "response.created":
		if s.started {
			return nil, invalidUpstream("type", "duplicate response.created event")
		}
		response, err := decodeObject(event["response"], CodeInvalidUpstream, "response")
		if err != nil {
			return nil, err
		}
		if err := s.captureResponseMetadata(response); err != nil {
			return nil, err
		}
		return nil, nil
	case "response.in_progress":
		if err := s.requireStarted(); err != nil {
			return nil, err
		}
		response, err := decodeObject(event["response"], CodeInvalidUpstream, "response")
		if err != nil {
			return nil, err
		}
		if err := s.captureResponseMetadata(response); err != nil {
			return nil, err
		}
		return nil, nil
	case "response.output_text.delta":
		if err := s.requireStarted(); err != nil {
			return nil, err
		}
		delta, err := requireString(event["delta"], "delta", true)
		if err != nil {
			return nil, err
		}
		item, err := s.eventItem(event, "message", true)
		if err != nil {
			return nil, err
		}
		item.text.WriteString(delta)
		return s.chatDelta(map[string]any{"content": delta})
	case "response.output_item.added":
		if err := s.requireStarted(); err != nil {
			return nil, err
		}
		item, err := decodeObject(event["item"], CodeInvalidUpstream, "item")
		if err != nil {
			return nil, err
		}
		itemType, err := requireString(item["type"], "item.type", true)
		if err != nil {
			return nil, err
		}
		if itemType != "message" && itemType != "function_call" {
			return nil, unsupported("item.type")
		}
		if itemType == "function_call" {
			if err := rejectUnknown(item, map[string]bool{"id": true, "type": true, "status": true, "call_id": true, "name": true, "arguments": true}, "item"); err != nil {
				return nil, err
			}
		}
		if err := s.validateEventResponseID(event); err != nil {
			return nil, err
		}
		itemID, err := requireString(item["id"], "item.id", true)
		if err != nil || itemID == "" {
			return nil, invalidUpstream("item.id", "a non-empty string is required")
		}
		status, err := requireString(item["status"], "item.status", true)
		if err != nil || status != "in_progress" {
			return nil, invalidUpstream("item.status", "added output item must be in progress")
		}
		outputIndex, err := requireInteger(event["output_index"], "output_index", true)
		if err != nil || outputIndex > 1024 {
			return nil, invalidUpstream("output_index", "invalid output index")
		}
		if s.items == nil {
			s.items = make(map[string]*responseStreamItem)
			s.outputs = make(map[int]*responseStreamItem)
		}
		if _, exists := s.items[itemID]; exists {
			return nil, invalidUpstream("item.id", "duplicate output item")
		}
		if _, exists := s.outputs[int(outputIndex)]; exists || int(outputIndex) != len(s.outputs) {
			return nil, invalidUpstream("output_index", "output indexes must be unique and contiguous")
		}
		streamItem := &responseStreamItem{kind: itemType, itemID: itemID, outputIndex: int(outputIndex), chatIndex: -1}
		if itemType == "message" {
			if err := validateAddedMessage(item); err != nil {
				return nil, err
			}
			s.items[itemID], s.outputs[int(outputIndex)] = streamItem, streamItem
			return nil, nil
		}
		callID, err := requireString(item["call_id"], "item.call_id", true)
		if err != nil || callID == "" {
			return nil, invalidUpstream("item.call_id", "a non-empty string is required")
		}
		name, err := requireString(item["name"], "item.name", true)
		if err != nil || name == "" {
			return nil, invalidUpstream("item.name", "a non-empty string is required")
		}
		arguments, err := requireString(item["arguments"], "item.arguments", true)
		if err != nil || arguments != "" {
			return nil, invalidUpstream("item.arguments", "added function call must start with empty arguments")
		}
		streamItem.chatIndex, streamItem.callID, streamItem.name = s.nextTool, callID, name
		s.nextTool++
		s.items[itemID], s.outputs[int(outputIndex)] = streamItem, streamItem
		return s.chatDelta(map[string]any{"tool_calls": []any{map[string]any{"index": streamItem.chatIndex, "id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": ""}}}})
	case "response.function_call_arguments.delta":
		if err := s.requireStarted(); err != nil {
			return nil, err
		}
		tool, err := s.eventItem(event, "function_call", false)
		if err != nil {
			return nil, err
		}
		delta, err := requireString(event["delta"], "delta", true)
		if err != nil {
			return nil, err
		}
		tool.arguments.WriteString(delta)
		return s.chatDelta(map[string]any{"tool_calls": []any{map[string]any{"index": tool.chatIndex, "function": map[string]any{"arguments": delta}}}})
	case "response.output_text.done":
		item, err := s.eventItem(event, "message", true)
		if err != nil {
			return nil, err
		}
		text, err := requireString(event["text"], "text", true)
		if err != nil || text != item.text.String() {
			return nil, invalidUpstream("text", "done text does not match streamed deltas")
		}
		return nil, nil
	case "response.function_call_arguments.done":
		item, err := s.eventItem(event, "function_call", false)
		if err != nil {
			return nil, err
		}
		arguments, err := requireString(event["arguments"], "arguments", true)
		if err != nil || arguments != item.arguments.String() {
			return nil, invalidUpstream("arguments", "done arguments do not match streamed deltas")
		}
		return nil, nil
	case "response.content_part.added":
		item, err := s.eventItem(event, "message", true)
		if err != nil {
			return nil, err
		}
		part, err := decodeObject(event["part"], CodeInvalidUpstream, "part")
		if err != nil {
			return nil, err
		}
		text, err := responseContentText(mustRawArray(part), "part", true)
		if err != nil || text != "" || item.text.Len() != 0 {
			return nil, invalidUpstream("part", "added text part must be empty")
		}
		return nil, nil
	case "response.content_part.done":
		item, err := s.eventItem(event, "message", true)
		if err != nil {
			return nil, err
		}
		part, err := decodeObject(event["part"], CodeInvalidUpstream, "part")
		if err != nil {
			return nil, err
		}
		text, err := responseContentText(mustRawArray(part), "part", true)
		if err != nil || text != item.text.String() {
			return nil, invalidUpstream("part", "done text part does not match streamed deltas")
		}
		return nil, nil
	case "response.output_item.done":
		if err := s.validateEventResponseID(event); err != nil {
			return nil, err
		}
		terminalItem, err := decodeObject(event["item"], CodeInvalidUpstream, "item")
		if err != nil {
			return nil, err
		}
		itemID, err := requireString(terminalItem["id"], "item.id", true)
		if err != nil {
			return nil, err
		}
		item := s.items[itemID]
		if item == nil {
			return nil, invalidUpstream("item.id", "event references an unknown output item")
		}
		outputIndex, err := requireInteger(event["output_index"], "output_index", true)
		if err != nil || int(outputIndex) != item.outputIndex {
			return nil, invalidUpstream("output_index", "event output index does not match its item")
		}
		if item.done {
			return nil, invalidUpstream("item", "duplicate output item completion")
		}
		if err := validateStreamItemObject(item, terminalItem); err != nil {
			return nil, err
		}
		item.done = true
		return nil, nil
	case "response.completed":
		responseRaw := event["response"]
		converted, err := ResponsesResponseToChat(responseRaw)
		if err != nil {
			return nil, err
		}
		var final map[string]json.RawMessage
		_ = json.Unmarshal(converted, &final)
		if err := s.captureResponseMetadataFromRaw(responseRaw); err != nil {
			return nil, err
		}
		hasTools, err := s.validateTerminalOutput(responseRaw)
		if err != nil {
			return nil, err
		}
		finish := "stop"
		if hasTools {
			finish = "tool_calls"
		}
		deltaEvents, err := s.chatDeltaWithUsage(map[string]any{}, finish, final["usage"])
		if err != nil {
			return nil, err
		}
		s.terminal = true
		deltaEvents = append(deltaEvents, SSEEvent{Data: json.RawMessage("[DONE]"), Terminal: true})
		return deltaEvents, nil
	case "response.failed", "response.incomplete", "error":
		return nil, invalidUpstream("type", "upstream returned a terminal failure event")
	default:
		return nil, unsupported("type")
	}
}

func validateAddedMessage(item map[string]json.RawMessage) error {
	if err := rejectUnknown(item, map[string]bool{"id": true, "type": true, "status": true, "role": true, "content": true}, "item"); err != nil {
		return err
	}
	role, err := requireString(item["role"], "item.role", true)
	if err != nil || role != "assistant" {
		return invalidUpstream("item.role", "expected assistant")
	}
	status, err := requireString(item["status"], "item.status", true)
	if err != nil || status != "in_progress" {
		return invalidUpstream("item.status", "added output item must be in progress")
	}
	var content []json.RawMessage
	if json.Unmarshal(item["content"], &content) != nil || len(content) != 0 {
		return invalidUpstream("item.content", "added message must start with empty content")
	}
	return nil
}

func validateResponsesEventFields(kind string, event map[string]json.RawMessage) error {
	allowed := map[string]bool{"type": true, "sequence_number": true}
	add := func(fields ...string) {
		for _, field := range fields {
			allowed[field] = true
		}
	}
	switch kind {
	case "response.created", "response.in_progress", "response.completed", "response.failed", "response.incomplete":
		add("response")
	case "response.output_item.added", "response.output_item.done":
		add("response_id", "output_index", "item")
	case "response.output_text.delta":
		add("response_id", "item_id", "output_index", "content_index", "delta")
	case "response.output_text.done":
		add("response_id", "item_id", "output_index", "content_index", "text")
	case "response.content_part.added", "response.content_part.done":
		add("response_id", "item_id", "output_index", "content_index", "part")
	case "response.function_call_arguments.delta":
		add("response_id", "item_id", "output_index", "delta")
	case "response.function_call_arguments.done":
		add("response_id", "item_id", "output_index", "arguments")
	case "error":
		add("code", "message", "param", "error")
	default:
		return unsupported("type")
	}
	return rejectUnknown(event, allowed, "")
}

func mustRawArray(object map[string]json.RawMessage) json.RawMessage {
	raw, _ := json.Marshal([]any{object})
	return raw
}

func (s *ResponsesToChatStream) eventItem(event map[string]json.RawMessage, kind string, contentIndex bool) (*responseStreamItem, error) {
	if err := s.validateEventResponseID(event); err != nil {
		return nil, err
	}
	itemID, err := requireString(event["item_id"], "item_id", true)
	if err != nil {
		return nil, err
	}
	item := s.items[itemID]
	if item == nil || (kind != "" && item.kind != kind) {
		return nil, invalidUpstream("item_id", "event references an unknown output item")
	}
	outputIndex, err := requireInteger(event["output_index"], "output_index", true)
	if err != nil || int(outputIndex) != item.outputIndex {
		return nil, invalidUpstream("output_index", "event output index does not match its item")
	}
	if contentIndex {
		index, err := requireInteger(event["content_index"], "content_index", true)
		if err != nil || index != 0 {
			return nil, invalidUpstream("content_index", "only content index zero is supported")
		}
	}
	if item.done {
		return nil, invalidUpstream("item_id", "event arrived after output item completion")
	}
	return item, nil
}

func (s *ResponsesToChatStream) validateEventResponseID(event map[string]json.RawMessage) error {
	raw, ok := event["response_id"]
	if !ok {
		return nil
	}
	id, err := requireString(raw, "response_id", true)
	if err != nil || id != s.responseID {
		return invalidUpstream("response_id", "event response id changed")
	}
	return nil
}

func validateStreamItemObject(streamed *responseStreamItem, object map[string]json.RawMessage) error {
	kind, err := requireString(object["type"], "item.type", true)
	if err != nil || kind != streamed.kind {
		return invalidUpstream("item.type", "output item type changed")
	}
	id, err := requireString(object["id"], "item.id", true)
	if err != nil || id != streamed.itemID {
		return invalidUpstream("item.id", "output item id changed")
	}
	status, err := requireString(object["status"], "item.status", true)
	if err != nil || status != "completed" {
		return invalidUpstream("item.status", "completed output item must be completed")
	}
	if streamed.kind == "message" {
		if err := rejectUnknown(object, map[string]bool{"id": true, "type": true, "status": true, "role": true, "content": true}, "item"); err != nil {
			return err
		}
		role, err := requireString(object["role"], "item.role", true)
		if err != nil || role != "assistant" {
			return invalidUpstream("item.role", "expected assistant")
		}
		text, err := responseContentText(object["content"], "item.content", true)
		if err != nil || text != streamed.text.String() {
			return invalidUpstream("item.content", "completed item does not match streamed text")
		}
		return nil
	}
	if err := rejectUnknown(object, map[string]bool{"id": true, "type": true, "status": true, "call_id": true, "name": true, "arguments": true}, "item"); err != nil {
		return err
	}
	callID, err := requireString(object["call_id"], "item.call_id", true)
	if err != nil || callID != streamed.callID {
		return invalidUpstream("item.call_id", "function call id changed")
	}
	name, err := requireString(object["name"], "item.name", true)
	if err != nil || name != streamed.name {
		return invalidUpstream("item.name", "function name changed")
	}
	arguments, err := requireString(object["arguments"], "item.arguments", true)
	if err != nil || arguments != streamed.arguments.String() {
		return invalidUpstream("item.arguments", "completed item does not match streamed arguments")
	}
	return nil
}

func (s *ResponsesToChatStream) validateTerminalOutput(raw json.RawMessage) (bool, error) {
	response, err := decodeObject(raw, CodeInvalidUpstream, "response")
	if err != nil {
		return false, err
	}
	var output []json.RawMessage
	if json.Unmarshal(response["output"], &output) != nil || len(output) == 0 {
		return false, invalidUpstream("response.output", "a non-empty output array is required")
	}
	if len(output) != len(s.outputs) {
		return false, invalidUpstream("response.output", "terminal output count does not match streamed items")
	}
	hasTools := false
	for index, rawItem := range output {
		streamed := s.outputs[index]
		if streamed == nil || !streamed.done {
			return false, invalidUpstream(indexField("response.output", index), "output item was not completed in the stream")
		}
		item, err := decodeObject(rawItem, CodeInvalidUpstream, indexField("response.output", index))
		if err != nil {
			return false, err
		}
		if err := validateStreamItemObject(streamed, item); err != nil {
			return false, err
		}
		hasTools = hasTools || streamed.kind == "function_call"
	}
	return hasTools, nil
}

func (s *ResponsesToChatStream) captureResponseMetadataFromRaw(raw json.RawMessage) error {
	response, err := decodeObject(raw, CodeInvalidUpstream, "response")
	if err != nil {
		return err
	}
	return s.captureResponseMetadata(response)
}

func (s *ResponsesToChatStream) captureResponseMetadata(response map[string]json.RawMessage) error {
	id, err := requireString(response["id"], "response.id", true)
	if err != nil || id == "" {
		return invalidUpstream("response.id", "a non-empty string is required")
	}
	model, err := requireString(response["model"], "response.model", true)
	if err != nil || model == "" {
		return invalidUpstream("response.model", "a non-empty string is required")
	}
	created, err := requireInteger(response["created_at"], "response.created_at", true)
	if err != nil {
		return err
	}
	if !s.started {
		s.started, s.responseID, s.model, s.created = true, id, model, created
		return nil
	}
	if id != s.responseID || model != s.model || created != s.created {
		return invalidUpstream("response", "stream metadata changed")
	}
	return nil
}

func (s *ResponsesToChatStream) requireStarted() error {
	if s == nil || !s.started {
		return invalidUpstream("type", "response.created must precede output events")
	}
	return nil
}

func (s *ResponsesToChatStream) chatDelta(delta map[string]any) ([]SSEEvent, error) {
	return s.chatDeltaWithUsage(delta, "", nil)
}

func (s *ResponsesToChatStream) chatDeltaWithUsage(delta map[string]any, finish string, usage json.RawMessage) ([]SSEEvent, error) {
	if !s.roleSent {
		delta["role"] = "assistant"
		s.roleSent = true
	}
	choice := map[string]any{"index": 0, "delta": delta, "finish_reason": nil}
	if finish != "" {
		choice["finish_reason"] = finish
	}
	payload := map[string]any{"id": s.responseID, "object": "chat.completion.chunk", "created": s.created, "model": s.model, "choices": []any{choice}}
	if len(usage) > 0 {
		var decoded any
		if json.Unmarshal(usage, &decoded) != nil {
			return nil, invalidUpstream("response.usage", "invalid usage")
		}
		payload["usage"] = decoded
	}
	data, err := marshal(payload)
	if err != nil {
		return nil, err
	}
	return []SSEEvent{{Data: data}}, nil
}

// EOF reports transport EOF without a terminal response.completed event.
func (s *ResponsesToChatStream) EOF() error {
	if s != nil && s.terminal {
		return nil
	}
	return interrupted("Responses stream ended before response.completed")
}
