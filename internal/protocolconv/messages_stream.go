package protocolconv

import (
	"bytes"
	"encoding/json"
)

// Anthropic Messages streaming event order and cumulative usage semantics:
// https://platform.claude.com/docs/en/build-with-claude/streaming

type messagesStreamBlock struct {
	index     int
	kind      string
	itemID    string
	callID    string
	name      string
	text      bytes.Buffer
	arguments bytes.Buffer
	closed    bool
}

// MessagesToResponsesStream converts Anthropic Messages SSE objects into
// Responses SSE events. It supports only text and client function calls.
type MessagesToResponsesStream struct {
	sequence       int64
	started        bool
	terminal       bool
	responseID     string
	model          string
	blocks         map[int]*messagesStreamBlock
	callIDs        map[string]struct{}
	nextBlock      int
	openBlock      int
	outputBytes    int
	stopReason     string
	stopSequence   *string
	inputTokens    int64
	outputTokens   int64
	usageFinalized bool
	messageDelta   bool
}

func (s *MessagesToResponsesStream) responseEvent(name string, payload map[string]any, semantic, terminal bool) (SSEEvent, error) {
	payload["type"] = name
	payload["sequence_number"] = s.sequence
	s.sequence++
	data, err := marshal(payload)
	if err != nil {
		return SSEEvent{}, err
	}
	return SSEEvent{Name: name, Data: data, Semantic: semantic, Terminal: terminal}, nil
}

func (s *MessagesToResponsesStream) Feed(raw []byte) ([]SSEEvent, error) {
	if s == nil || s.terminal {
		return nil, invalidUpstream("stream", "stream is already terminal")
	}
	event, err := decodeObject(raw, CodeInvalidUpstream, "")
	if err != nil {
		return nil, err
	}
	kind, err := requireString(event["type"], "type", true)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "ping":
		if err := rejectUnknown(event, map[string]bool{"type": true}, ""); err != nil {
			return nil, err
		}
		return nil, nil
	case "message_start":
		return s.feedMessageStart(event)
	case "content_block_start":
		return s.feedContentBlockStart(event)
	case "content_block_delta":
		return s.feedContentBlockDelta(event)
	case "content_block_stop":
		return s.feedContentBlockStop(event)
	case "message_delta":
		return s.feedMessageDelta(event)
	case "message_stop":
		return s.feedMessageStop(event)
	case "error":
		return s.feedError(event)
	default:
		return nil, unsupported("type")
	}
}

func (s *MessagesToResponsesStream) feedMessageStart(event map[string]json.RawMessage) ([]SSEEvent, error) {
	if s.started {
		return nil, invalidUpstream("type", "duplicate message_start event")
	}
	if err := rejectUnknown(event, map[string]bool{"type": true, "message": true}, ""); err != nil {
		return nil, err
	}
	message, err := decodeObject(event["message"], CodeInvalidUpstream, "message")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(message, map[string]bool{
		"id": true, "type": true, "role": true, "model": true, "content": true,
		"stop_reason": true, "stop_sequence": true, "usage": true,
	}, "message"); err != nil {
		return nil, err
	}
	id, err := nonEmptyString(message["id"], "message.id", true)
	if err != nil {
		return nil, err
	}
	messageType, err := requireString(message["type"], "message.type", true)
	if err != nil || messageType != "message" {
		return nil, invalidUpstream("message.type", "expected message")
	}
	role, err := requireString(message["role"], "message.role", true)
	if err != nil || role != "assistant" {
		return nil, invalidUpstream("message.role", "expected assistant")
	}
	model, err := nonEmptyString(message["model"], "message.model", true)
	if err != nil {
		return nil, err
	}
	var content []json.RawMessage
	if json.Unmarshal(message["content"], &content) != nil || len(content) != 0 {
		return nil, invalidUpstream("message.content", "message_start content must be empty")
	}
	for _, field := range []string{"stop_reason", "stop_sequence"} {
		if value, ok := message[field]; !ok || !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, invalidUpstream("message."+field, "message_start field must be null")
		}
	}
	usage, err := parseMessagesStreamStartUsage(message["usage"])
	if err != nil {
		return nil, err
	}
	s.started = true
	s.responseID = convertedEnvelopeID("resp", id)
	s.model = model
	s.blocks = make(map[int]*messagesStreamBlock)
	s.callIDs = make(map[string]struct{})
	s.openBlock = -1
	s.inputTokens = usage.Input
	s.outputTokens = usage.Output
	response := s.responseSnapshot("in_progress", nil)
	created, _ := s.responseEvent("response.created", map[string]any{"response": response}, false, false)
	inProgress, _ := s.responseEvent("response.in_progress", map[string]any{"response": response}, false, false)
	return []SSEEvent{created, inProgress}, nil
}

func (s *MessagesToResponsesStream) feedContentBlockStart(event map[string]json.RawMessage) ([]SSEEvent, error) {
	if err := s.requireMessagesStarted(); err != nil {
		return nil, err
	}
	if s.stopReason != "" {
		return nil, invalidUpstream("type", "content block started after message_delta stop reason")
	}
	if s.openBlock >= 0 {
		return nil, invalidUpstream("index", "content blocks cannot be interleaved")
	}
	if err := rejectUnknown(event, map[string]bool{"type": true, "index": true, "content_block": true}, ""); err != nil {
		return nil, err
	}
	index64, err := requireInteger(event["index"], "index", true)
	if err != nil || index64 < 0 || index64 >= maxConvertedStreamItems || int(index64) != s.nextBlock {
		return nil, invalidUpstream("index", "content block indexes must be contiguous")
	}
	blockObject, err := decodeObject(event["content_block"], CodeInvalidUpstream, "content_block")
	if err != nil {
		return nil, err
	}
	kind, err := requireString(blockObject["type"], "content_block.type", true)
	if err != nil {
		return nil, err
	}
	index := int(index64)
	block := &messagesStreamBlock{index: index, kind: kind}
	s.nextBlock++
	s.blocks[index] = block
	s.openBlock = index
	switch kind {
	case "text":
		if err := rejectUnknown(blockObject, map[string]bool{"type": true, "text": true}, "content_block"); err != nil {
			return nil, err
		}
		text, err := requireString(blockObject["text"], "content_block.text", true)
		if err != nil || text != "" {
			return nil, invalidUpstream("content_block.text", "text block must start empty")
		}
		block.itemID = convertedItemID("msg", s.responseID, index)
		item := map[string]any{"id": block.itemID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
		added, _ := s.responseEvent("response.output_item.added", map[string]any{"response_id": s.responseID, "output_index": index, "item": item}, false, false)
		part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}}
		partAdded, _ := s.responseEvent("response.content_part.added", map[string]any{"response_id": s.responseID, "item_id": block.itemID, "output_index": index, "content_index": 0, "part": part}, false, false)
		return []SSEEvent{added, partAdded}, nil
	case "tool_use":
		if err := rejectUnknown(blockObject, map[string]bool{"type": true, "id": true, "name": true, "input": true}, "content_block"); err != nil {
			return nil, err
		}
		callID, err := nonEmptyString(blockObject["id"], "content_block.id", true)
		if err != nil {
			return nil, err
		}
		if _, exists := s.callIDs[callID]; exists {
			return nil, invalidUpstream("content_block.id", "tool use ids must be unique")
		}
		name, err := nonEmptyString(blockObject["name"], "content_block.name", true)
		if err != nil {
			return nil, err
		}
		input, err := rawJSONObject(blockObject["input"], "content_block.input", true)
		if err != nil || len(input) != 0 {
			return nil, invalidUpstream("content_block.input", "tool input must start as an empty object")
		}
		block.itemID = convertedItemID("fc", s.responseID, index)
		block.callID = callID
		block.name = name
		s.callIDs[callID] = struct{}{}
		item := map[string]any{"id": block.itemID, "type": "function_call", "status": "in_progress", "call_id": callID, "name": name, "arguments": ""}
		added, _ := s.responseEvent("response.output_item.added", map[string]any{"response_id": s.responseID, "output_index": index, "item": item}, true, false)
		return []SSEEvent{added}, nil
	default:
		return nil, unsupported("content_block.type")
	}
}

func (s *MessagesToResponsesStream) feedContentBlockDelta(event map[string]json.RawMessage) ([]SSEEvent, error) {
	if err := s.requireMessagesStarted(); err != nil {
		return nil, err
	}
	if err := rejectUnknown(event, map[string]bool{"type": true, "index": true, "delta": true}, ""); err != nil {
		return nil, err
	}
	block, err := s.messagesBlock(event["index"])
	if err != nil {
		return nil, err
	}
	delta, err := decodeObject(event["delta"], CodeInvalidUpstream, "delta")
	if err != nil {
		return nil, err
	}
	kind, err := requireString(delta["type"], "delta.type", true)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "text_delta":
		if block.kind != "text" {
			return nil, invalidUpstream("delta.type", "text delta does not match its content block")
		}
		if err := rejectUnknown(delta, map[string]bool{"type": true, "text": true}, "delta"); err != nil {
			return nil, err
		}
		text, err := requireString(delta["text"], "delta.text", true)
		if err != nil {
			return nil, err
		}
		if err := s.reserveMessagesOutput(len(text)); err != nil {
			return nil, err
		}
		block.text.WriteString(text)
		converted, _ := s.responseEvent("response.output_text.delta", map[string]any{
			"response_id": s.responseID, "item_id": block.itemID, "output_index": block.index, "content_index": 0, "delta": text,
		}, text != "", false)
		return []SSEEvent{converted}, nil
	case "input_json_delta":
		if block.kind != "tool_use" {
			return nil, invalidUpstream("delta.type", "input JSON delta does not match its content block")
		}
		if err := rejectUnknown(delta, map[string]bool{"type": true, "partial_json": true}, "delta"); err != nil {
			return nil, err
		}
		partial, err := requireString(delta["partial_json"], "delta.partial_json", true)
		if err != nil {
			return nil, err
		}
		if err := s.reserveMessagesOutput(len(partial)); err != nil {
			return nil, err
		}
		block.arguments.WriteString(partial)
		converted, _ := s.responseEvent("response.function_call_arguments.delta", map[string]any{
			"response_id": s.responseID, "item_id": block.itemID, "output_index": block.index, "delta": partial,
		}, partial != "", false)
		return []SSEEvent{converted}, nil
	default:
		return nil, unsupported("delta.type")
	}
}

func (s *MessagesToResponsesStream) feedContentBlockStop(event map[string]json.RawMessage) ([]SSEEvent, error) {
	if err := s.requireMessagesStarted(); err != nil {
		return nil, err
	}
	if err := rejectUnknown(event, map[string]bool{"type": true, "index": true}, ""); err != nil {
		return nil, err
	}
	block, err := s.messagesBlock(event["index"])
	if err != nil {
		return nil, err
	}
	if block.index != s.openBlock {
		return nil, invalidUpstream("index", "content blocks must close in order")
	}
	block.closed = true
	s.openBlock = -1
	if block.kind == "text" {
		text := block.text.String()
		done, _ := s.responseEvent("response.output_text.done", map[string]any{"response_id": s.responseID, "item_id": block.itemID, "output_index": block.index, "content_index": 0, "text": text}, false, false)
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
		partDone, _ := s.responseEvent("response.content_part.done", map[string]any{"response_id": s.responseID, "item_id": block.itemID, "output_index": block.index, "content_index": 0, "part": part}, false, false)
		item := map[string]any{"id": block.itemID, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
		itemDone, _ := s.responseEvent("response.output_item.done", map[string]any{"response_id": s.responseID, "output_index": block.index, "item": item}, false, false)
		return []SSEEvent{done, partDone, itemDone}, nil
	}
	arguments := block.arguments.String()
	if arguments == "" {
		return nil, invalidUpstream("delta.partial_json", "tool arguments must form a complete JSON object")
	}
	if _, err := compactJSONObject(json.RawMessage(arguments), "delta.partial_json", true); err != nil {
		return nil, err
	}
	argumentsDone, _ := s.responseEvent("response.function_call_arguments.done", map[string]any{"response_id": s.responseID, "item_id": block.itemID, "output_index": block.index, "arguments": arguments}, false, false)
	item := map[string]any{"id": block.itemID, "type": "function_call", "status": "completed", "call_id": block.callID, "name": block.name, "arguments": arguments}
	itemDone, _ := s.responseEvent("response.output_item.done", map[string]any{"response_id": s.responseID, "output_index": block.index, "item": item}, false, false)
	return []SSEEvent{argumentsDone, itemDone}, nil
}

func (s *MessagesToResponsesStream) feedMessageDelta(event map[string]json.RawMessage) ([]SSEEvent, error) {
	if err := s.requireMessagesStarted(); err != nil {
		return nil, err
	}
	if s.messageDelta {
		return nil, invalidUpstream("type", "message_delta is duplicated")
	}
	if err := rejectUnknown(event, map[string]bool{"type": true, "delta": true, "usage": true}, ""); err != nil {
		return nil, err
	}
	for _, block := range s.blocks {
		if !block.closed {
			return nil, invalidUpstream("type", "message_delta arrived before content block completion")
		}
	}
	delta, err := decodeObject(event["delta"], CodeInvalidUpstream, "delta")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(delta, map[string]bool{"stop_reason": true, "stop_sequence": true}, "delta"); err != nil {
		return nil, err
	}
	rawReason, ok := delta["stop_reason"]
	if !ok || bytes.Equal(bytes.TrimSpace(rawReason), []byte("null")) {
		return nil, invalidUpstream("delta.stop_reason", "a terminal stop reason is required")
	}
	reason, err := requireString(rawReason, "delta.stop_reason", true)
	if err != nil {
		return nil, err
	}
	switch reason {
	case "end_turn", "stop_sequence", "tool_use", "max_tokens":
	default:
		return nil, unsupported("delta.stop_reason")
	}
	s.stopReason = reason
	if rawSequence, ok := delta["stop_sequence"]; ok && !bytes.Equal(bytes.TrimSpace(rawSequence), []byte("null")) {
		sequence, err := requireString(rawSequence, "delta.stop_sequence", true)
		if err != nil || sequence == "" {
			return nil, invalidUpstream("delta.stop_sequence", "a non-empty string is required")
		}
		s.stopSequence = &sequence
	}
	if s.stopReason == "stop_sequence" && s.stopSequence == nil {
		return nil, invalidUpstream("delta.stop_sequence", "stop sequence is required")
	}
	if s.stopReason != "stop_sequence" && s.stopSequence != nil {
		return nil, invalidUpstream("delta.stop_sequence", "stop sequence does not match stop reason")
	}
	usage, err := decodeObject(event["usage"], CodeInvalidUpstream, "usage")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(usage, map[string]bool{"output_tokens": true}, "usage"); err != nil {
		return nil, err
	}
	output, err := requireInteger(usage["output_tokens"], "usage.output_tokens", true)
	if err != nil || output < 0 || output < s.outputTokens {
		return nil, invalidUpstream("usage.output_tokens", "cumulative output usage decreased")
	}
	s.outputTokens = output
	s.usageFinalized = true
	s.messageDelta = true
	return nil, nil
}

func (s *MessagesToResponsesStream) feedMessageStop(event map[string]json.RawMessage) ([]SSEEvent, error) {
	if err := s.requireMessagesStarted(); err != nil {
		return nil, err
	}
	if err := rejectUnknown(event, map[string]bool{"type": true}, ""); err != nil {
		return nil, err
	}
	if s.stopReason == "" || !s.usageFinalized {
		return nil, interrupted("Messages stream ended before its final message_delta")
	}
	for _, block := range s.blocks {
		if !block.closed {
			return nil, interrupted("Messages stream ended with an open content block")
		}
	}
	if s.stopReason == "tool_use" && len(s.callIDs) == 0 {
		return nil, invalidUpstream("delta.stop_reason", "tool_use requires a tool block")
	}
	s.terminal = true
	status := "completed"
	name := "response.completed"
	if s.stopReason == "max_tokens" {
		status = "incomplete"
		name = "response.incomplete"
	}
	response := s.responseSnapshot(status, s.responsesUsage())
	if status == "incomplete" {
		response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	terminal, _ := s.responseEvent(name, map[string]any{"response": response}, false, true)
	if status == "completed" {
		terminal.TerminalOutcome = StreamTerminalCompleted
	} else {
		terminal.TerminalOutcome = StreamTerminalIncomplete
	}
	return []SSEEvent{terminal}, nil
}

func (s *MessagesToResponsesStream) feedError(event map[string]json.RawMessage) ([]SSEEvent, error) {
	if err := rejectUnknown(event, map[string]bool{"type": true, "error": true}, ""); err != nil {
		return nil, err
	}
	upstream, err := decodeObject(event["error"], CodeInvalidUpstream, "error")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(upstream, map[string]bool{"type": true, "message": true}, "error"); err != nil {
		return nil, err
	}
	if _, err := nonEmptyString(upstream["type"], "error.type", true); err != nil {
		return nil, err
	}
	if _, err := requireString(upstream["message"], "error.message", true); err != nil {
		return nil, err
	}
	s.terminal = true
	failed, err := s.responseEvent("error", map[string]any{
		"code": "upstream_error", "message": "The upstream provider returned an error.", "param": nil,
	}, false, true)
	if err != nil {
		return nil, err
	}
	failed.TerminalOutcome = StreamTerminalFailed
	return []SSEEvent{failed}, nil
}

func (s *MessagesToResponsesStream) requireMessagesStarted() error {
	if s == nil || !s.started {
		return invalidUpstream("type", "message_start must precede stream events")
	}
	return nil
}

func (s *MessagesToResponsesStream) messagesBlock(rawIndex json.RawMessage) (*messagesStreamBlock, error) {
	index, err := requireInteger(rawIndex, "index", true)
	if err != nil || index < 0 || index >= int64(s.nextBlock) {
		return nil, invalidUpstream("index", "event references an unknown content block")
	}
	block := s.blocks[int(index)]
	if block == nil || block.closed {
		return nil, invalidUpstream("index", "content block is missing or already closed")
	}
	return block, nil
}

func (s *MessagesToResponsesStream) reserveMessagesOutput(count int) error {
	if count < 0 || s.outputBytes > maxConvertedStreamBytes-count {
		return invalidUpstream("stream", "converted stream output exceeded the configured byte limit")
	}
	s.outputBytes += count
	return nil
}

func (s *MessagesToResponsesStream) responsesUsage() map[string]any {
	return map[string]any{"input_tokens": s.inputTokens, "output_tokens": s.outputTokens, "total_tokens": s.inputTokens + s.outputTokens}
}

func (s *MessagesToResponsesStream) responseSnapshot(status string, usage map[string]any) map[string]any {
	output := make([]any, 0, len(s.blocks))
	for index := 0; index < s.nextBlock; index++ {
		block := s.blocks[index]
		itemStatus := status
		if itemStatus == "incomplete" && block.closed {
			itemStatus = "completed"
		}
		if block.kind == "text" {
			part := map[string]any{"type": "output_text", "text": block.text.String(), "annotations": []any{}}
			output = append(output, map[string]any{"id": block.itemID, "type": "message", "status": itemStatus, "role": "assistant", "content": []any{part}})
		} else {
			arguments := block.arguments.String()
			output = append(output, map[string]any{"id": block.itemID, "type": "function_call", "status": itemStatus, "call_id": block.callID, "name": block.name, "arguments": arguments})
		}
	}
	response := map[string]any{"id": s.responseID, "object": "response", "created_at": int64(0), "model": s.model, "status": status, "output": output}
	if usage != nil {
		response["usage"] = usage
	}
	return response
}

func (s *MessagesToResponsesStream) EOF() error {
	if s != nil && s.terminal {
		return nil
	}
	return interrupted("Messages stream ended before message_stop")
}

func parseMessagesStreamStartUsage(raw json.RawMessage) (canonicalUsage, error) {
	mapped, err := messagesUsageToResponses(raw)
	if err != nil {
		return canonicalUsage{}, err
	}
	usage := canonicalUsage{Input: mapped["input_tokens"].(int64), Output: mapped["output_tokens"].(int64), Total: mapped["total_tokens"].(int64)}
	if usage.Input < 0 || usage.Output < 0 {
		return canonicalUsage{}, invalidUpstream("message.usage", "token counts must be non-negative")
	}
	if rawDetails, ok := mapped["input_tokens_details"].(map[string]any); ok {
		if value, ok := rawDetails["cached_tokens"].(int64); ok {
			usage.Cached = value
		}
		if value, ok := rawDetails["cache_write_tokens"].(int64); ok {
			usage.CacheWrite = value
		}
		if usage.Cached < 0 || usage.CacheWrite < 0 {
			return canonicalUsage{}, invalidUpstream("message.usage", "cache token counts must be non-negative")
		}
	}
	return usage, nil
}

// ResponsesToMessagesStream converts the supported Responses event subset to
// Anthropic Messages SSE. Responses validation is shared with the proven Chat
// converter; Messages framing is emitted independently.
type ResponsesToMessagesStream struct {
	validator ResponsesToChatStream
	messageID string
	pending   map[int]*messagesPendingBlock
	nextBlock int
	terminal  bool
}

type messagesPendingBlock struct {
	events  []SSEEvent
	started bool
	done    bool
}

func (s *ResponsesToMessagesStream) Feed(raw []byte) ([]SSEEvent, error) {
	if s == nil || s.terminal {
		return nil, invalidUpstream("stream", "stream is already terminal")
	}
	event, err := decodeObject(raw, CodeInvalidUpstream, "")
	if err != nil {
		return nil, err
	}
	kind, err := requireString(event["type"], "type", true)
	if err != nil {
		return nil, err
	}
	if kind == "response.incomplete" {
		return s.feedIncomplete(event)
	}
	if kind == "response.failed" || kind == "error" {
		return s.feedFailed(kind, event)
	}
	if _, err := s.validator.Feed(raw); err != nil {
		return nil, err
	}
	switch kind {
	case "response.created":
		s.messageID = convertedEnvelopeID("msg", s.validator.sourceID)
		s.pending = make(map[int]*messagesPendingBlock)
		usage, err := responsesStartMessagesUsage(event["response"])
		if err != nil {
			return nil, err
		}
		message := map[string]any{
			"id": s.messageID, "type": "message", "role": "assistant", "model": s.validator.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": usage,
		}
		return messagesEvents("message_start", map[string]any{"message": message}, false, false)
	case "response.in_progress", "response.output_text.done", "response.content_part.done", "response.function_call_arguments.done":
		return nil, nil
	case "response.output_item.added":
		item, err := responseStreamEventItem(event, &s.validator)
		if err != nil {
			return nil, err
		}
		pending := &messagesPendingBlock{}
		s.pending[item.outputIndex] = pending
		if item.kind != "function_call" {
			return nil, nil
		}
		block := map[string]any{"type": "tool_use", "id": item.callID, "name": item.name, "input": map[string]any{}}
		start, _ := messagesEvent("content_block_start", map[string]any{"index": item.outputIndex, "content_block": block}, true, false)
		pending.events = append(pending.events, start)
		pending.started = true
		return s.flushPendingBlocks(), nil
	case "response.content_part.added":
		item, err := responseStreamEventItem(event, &s.validator)
		if err != nil {
			return nil, err
		}
		pending := s.pending[item.outputIndex]
		if item.kind != "message" || pending == nil || pending.started {
			return nil, invalidUpstream("output_index", "text content block start is duplicated")
		}
		start, _ := messagesEvent("content_block_start", map[string]any{"index": item.outputIndex, "content_block": map[string]any{"type": "text", "text": ""}}, false, false)
		pending.events = append(pending.events, start)
		pending.started = true
		return s.flushPendingBlocks(), nil
	case "response.output_text.delta":
		item, err := responseStreamEventItem(event, &s.validator)
		if err != nil {
			return nil, err
		}
		delta, _ := requireString(event["delta"], "delta", true)
		converted, _ := messagesEvent("content_block_delta", map[string]any{"index": item.outputIndex, "delta": map[string]any{"type": "text_delta", "text": delta}}, delta != "", false)
		s.pending[item.outputIndex].events = append(s.pending[item.outputIndex].events, converted)
		return s.flushPendingBlocks(), nil
	case "response.function_call_arguments.delta":
		item, err := responseStreamEventItem(event, &s.validator)
		if err != nil {
			return nil, err
		}
		delta, _ := requireString(event["delta"], "delta", true)
		converted, _ := messagesEvent("content_block_delta", map[string]any{"index": item.outputIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": delta}}, delta != "", false)
		s.pending[item.outputIndex].events = append(s.pending[item.outputIndex].events, converted)
		return s.flushPendingBlocks(), nil
	case "response.output_item.done":
		item, err := responseStreamEventItem(event, &s.validator)
		if err != nil {
			return nil, err
		}
		pending := s.pending[item.outputIndex]
		if pending == nil || !pending.started {
			return nil, invalidUpstream("output_index", "content block was not started")
		}
		stop, _ := messagesEvent("content_block_stop", map[string]any{"index": item.outputIndex}, false, false)
		pending.events = append(pending.events, stop)
		pending.done = true
		return s.flushPendingBlocks(), nil
	case "response.completed":
		return s.finishResponse(event, false)
	default:
		return nil, nil
	}
}

func (s *ResponsesToMessagesStream) feedIncomplete(event map[string]json.RawMessage) ([]SSEEvent, error) {
	if err := validateResponsesEventFields("response.incomplete", event); err != nil {
		return nil, err
	}
	if err := s.acceptResponseSequence(event, true); err != nil {
		return nil, err
	}
	if !s.validator.started {
		return nil, invalidUpstream("type", "response.created must precede response.incomplete")
	}
	if err := s.validator.captureResponseMetadataFromRaw(event["response"]); err != nil {
		return nil, err
	}
	if _, err := parseResponsesOutput(event["response"]); err != nil {
		return nil, err
	}
	if _, err := s.validator.validateTerminalOutput(event["response"]); err != nil {
		return nil, err
	}
	s.validator.terminal = true
	return s.finishResponse(event, true)
}

func (s *ResponsesToMessagesStream) feedFailed(kind string, event map[string]json.RawMessage) ([]SSEEvent, error) {
	if err := validateResponsesEventFields(kind, event); err != nil {
		return nil, err
	}
	if err := s.acceptResponseSequence(event, kind == "response.failed"); err != nil {
		return nil, err
	}
	if kind == "response.failed" {
		if err := s.validator.captureResponseMetadataFromRaw(event["response"]); err != nil {
			return nil, err
		}
		response, err := decodeObject(event["response"], CodeInvalidUpstream, "response")
		if err != nil {
			return nil, err
		}
		object, err := requireString(response["object"], "response.object", true)
		if err != nil || object != "response" {
			return nil, invalidUpstream("response.object", "expected response")
		}
		status, err := requireString(response["status"], "response.status", true)
		if err != nil || status != "failed" {
			return nil, invalidUpstream("response.status", "response.failed requires failed status")
		}
		upstream, err := decodeObject(response["error"], CodeInvalidUpstream, "response.error")
		if err != nil {
			return nil, err
		}
		if _, err := requireString(upstream["message"], "response.error.message", true); err != nil {
			return nil, err
		}
	} else {
		if rawMessage, ok := event["message"]; ok {
			if _, err := requireString(rawMessage, "message", true); err != nil {
				return nil, err
			}
		}
		if rawError, ok := event["error"]; ok && !bytes.Equal(bytes.TrimSpace(rawError), []byte("null")) {
			if _, err := decodeObject(rawError, CodeInvalidUpstream, "error"); err != nil {
				return nil, err
			}
		}
	}
	failed, err := messagesEvent("error", map[string]any{
		"error": map[string]any{"type": "api_error", "message": "The upstream provider returned an error."},
	}, false, true)
	if err != nil {
		return nil, err
	}
	failed.TerminalOutcome = StreamTerminalFailed
	s.validator.terminal = true
	s.terminal = true
	return []SSEEvent{failed}, nil
}

func (s *ResponsesToMessagesStream) acceptResponseSequence(event map[string]json.RawMessage, requireStarted bool) error {
	sequence, err := requireInteger(event["sequence_number"], "sequence_number", true)
	if err != nil {
		return err
	}
	if !s.validator.seqStarted {
		if sequence != 0 {
			return invalidUpstream("sequence_number", "the first event must have sequence zero")
		}
		s.validator.seqStarted = true
	} else if sequence != s.validator.nextSeq {
		return invalidUpstream("sequence_number", "event sequence is not contiguous")
	}
	if requireStarted && !s.validator.started {
		return invalidUpstream("type", "response.created must precede terminal response events")
	}
	s.validator.nextSeq = sequence + 1
	return nil
}

func (s *ResponsesToMessagesStream) finishResponse(event map[string]json.RawMessage, incomplete bool) ([]SSEEvent, error) {
	if len(s.pending) != 0 || s.nextBlock != len(s.validator.outputs) {
		return nil, invalidUpstream("response.output", "converted content blocks were not emitted in order")
	}
	canonical, err := parseResponsesOutput(event["response"])
	if err != nil {
		return nil, err
	}
	if incomplete != (canonical.Status == "incomplete") {
		return nil, invalidUpstream("response.status", "terminal event does not match response status")
	}
	stopReason := "end_turn"
	for _, part := range canonical.Parts {
		if part.Call != nil {
			stopReason = "tool_use"
			break
		}
	}
	if incomplete {
		stopReason = "max_tokens"
	}
	outputTokens := int64(0)
	if canonical.Usage != nil {
		if canonical.Usage.Reasoning != 0 {
			return nil, unsupported("response.usage.output_tokens_details.reasoning_tokens")
		}
		outputTokens = canonical.Usage.Output
	}
	messageDelta, _ := messagesEvent("message_delta", map[string]any{
		"delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": outputTokens},
	}, false, false)
	messageStop, _ := messagesEvent("message_stop", map[string]any{}, false, true)
	if incomplete {
		messageStop.TerminalOutcome = StreamTerminalIncomplete
	} else {
		messageStop.TerminalOutcome = StreamTerminalCompleted
	}
	s.terminal = true
	return []SSEEvent{messageDelta, messageStop}, nil
}

func (s *ResponsesToMessagesStream) flushPendingBlocks() []SSEEvent {
	var result []SSEEvent
	for {
		pending := s.pending[s.nextBlock]
		if pending == nil {
			return result
		}
		result = append(result, pending.events...)
		pending.events = nil
		if !pending.done {
			return result
		}
		delete(s.pending, s.nextBlock)
		s.nextBlock++
	}
}

func (s *ResponsesToMessagesStream) EOF() error {
	if s != nil && s.terminal {
		return nil
	}
	return interrupted("Responses stream ended before a terminal response event")
}

func responseStreamEventItem(event map[string]json.RawMessage, validator *ResponsesToChatStream) (*responseStreamItem, error) {
	var rawID json.RawMessage
	if value, ok := event["item_id"]; ok {
		rawID = value
	} else if rawItem, ok := event["item"]; ok {
		item, err := decodeObject(rawItem, CodeInvalidUpstream, "item")
		if err != nil {
			return nil, err
		}
		rawID = item["id"]
	}
	id, err := requireString(rawID, "item_id", true)
	if err != nil {
		return nil, err
	}
	item := validator.items[id]
	if item == nil {
		return nil, invalidUpstream("item_id", "event references an unknown output item")
	}
	return item, nil
}

func responsesStartMessagesUsage(rawResponse json.RawMessage) (map[string]any, error) {
	response, err := decodeObject(rawResponse, CodeInvalidUpstream, "response")
	if err != nil {
		return nil, err
	}
	usage := map[string]any{"input_tokens": int64(0), "output_tokens": int64(0)}
	raw, ok := response["usage"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return usage, nil
	}
	parsed, err := parseResponsesCanonicalUsage(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Reasoning != 0 {
		return nil, unsupported("response.usage.output_tokens_details.reasoning_tokens")
	}
	usage["input_tokens"] = parsed.Input
	usage["output_tokens"] = parsed.Output
	if parsed.Cached != 0 {
		usage["cache_read_input_tokens"] = parsed.Cached
	}
	if parsed.CacheWrite != 0 {
		usage["cache_creation_input_tokens"] = parsed.CacheWrite
	}
	return usage, nil
}

func messagesEvents(name string, payload map[string]any, semantic, terminal bool) ([]SSEEvent, error) {
	event, err := messagesEvent(name, payload, semantic, terminal)
	if err != nil {
		return nil, err
	}
	return []SSEEvent{event}, nil
}

func messagesEvent(name string, payload map[string]any, semantic, terminal bool) (SSEEvent, error) {
	payload["type"] = name
	data, err := marshal(payload)
	if err != nil {
		return SSEEvent{}, err
	}
	return SSEEvent{Name: name, Data: data, Semantic: semantic, Terminal: terminal}, nil
}
