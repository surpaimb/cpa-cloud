package protocolconv

import (
	"bytes"
	"encoding/json"
)

type geminiStreamUsage struct {
	input       int64
	output      int64
	total       int64
	cached      int64
	hasCached   bool
	initialized bool
}

func (u *geminiStreamUsage) apply(next geminiStreamUsage) error {
	if u.initialized && (next.input < u.input || next.output < u.output || next.total < u.total || next.cached < u.cached) {
		return invalidUpstream("usage", "cumulative token counters decreased")
	}
	*u = next
	u.initialized = true
	return nil
}

func (u geminiStreamUsage) responsesUsage() map[string]any {
	if !u.initialized {
		return nil
	}
	result := map[string]any{"input_tokens": u.input, "output_tokens": u.output, "total_tokens": u.total}
	if u.hasCached {
		result["input_tokens_details"] = map[string]any{"cached_tokens": u.cached}
	}
	return result
}

func (u geminiStreamUsage) geminiUsage() map[string]any {
	if !u.initialized {
		return nil
	}
	result := map[string]any{"promptTokenCount": u.input, "candidatesTokenCount": u.output, "totalTokenCount": u.total}
	if u.hasCached {
		result["cachedContentTokenCount"] = u.cached
	}
	return result
}

func parseGeminiStreamUsage(raw json.RawMessage) (geminiStreamUsage, error) {
	root, err := decodeObject(raw, CodeInvalidUpstream, "usageMetadata")
	if err != nil {
		return geminiStreamUsage{}, err
	}
	if err := rejectUnknown(root, map[string]bool{
		"promptTokenCount": true, "candidatesTokenCount": true, "totalTokenCount": true, "cachedContentTokenCount": true,
	}, "usageMetadata"); err != nil {
		return geminiStreamUsage{}, err
	}
	input, err := requireInteger(root["promptTokenCount"], "usageMetadata.promptTokenCount", true)
	if err != nil {
		return geminiStreamUsage{}, err
	}
	output, err := requireInteger(root["candidatesTokenCount"], "usageMetadata.candidatesTokenCount", true)
	if err != nil {
		return geminiStreamUsage{}, err
	}
	total, err := requireInteger(root["totalTokenCount"], "usageMetadata.totalTokenCount", true)
	if err != nil || total != input+output {
		return geminiStreamUsage{}, invalidUpstream("usageMetadata.totalTokenCount", "token totals are inconsistent")
	}
	result := geminiStreamUsage{input: input, output: output, total: total}
	if rawCached, ok := root["cachedContentTokenCount"]; ok {
		result.cached, err = requireInteger(rawCached, "usageMetadata.cachedContentTokenCount", true)
		if err != nil {
			return geminiStreamUsage{}, err
		}
		result.hasCached = true
	}
	return result, nil
}

func parseResponsesStreamUsageForGemini(rawResponse json.RawMessage) (geminiStreamUsage, bool, error) {
	response, err := decodeObject(rawResponse, CodeInvalidUpstream, "response")
	if err != nil {
		return geminiStreamUsage{}, false, err
	}
	rawUsage, ok := response["usage"]
	if !ok || bytes.Equal(bytes.TrimSpace(rawUsage), []byte("null")) {
		return geminiStreamUsage{}, false, nil
	}
	usage, err := parseResponsesCanonicalUsage(rawUsage)
	if err != nil {
		return geminiStreamUsage{}, false, err
	}
	usageObject, _ := decodeObject(rawUsage, CodeInvalidUpstream, "usage")
	if rawDetails, ok := usageObject["input_tokens_details"]; ok {
		details, err := decodeObject(rawDetails, CodeInvalidUpstream, "usage.input_tokens_details")
		if err != nil {
			return geminiStreamUsage{}, false, err
		}
		if _, ok := details["cache_write_tokens"]; ok {
			return geminiStreamUsage{}, false, unsupported("usage.input_tokens_details.cache_write_tokens")
		}
	}
	if usage.Reasoning != 0 {
		return geminiStreamUsage{}, false, unsupported("usage.output_tokens_details.reasoning_tokens")
	}
	result := geminiStreamUsage{input: usage.Input, output: usage.Output, total: usage.Total, cached: usage.Cached}
	if rawDetails, ok := usageObject["input_tokens_details"]; ok {
		details, _ := decodeObject(rawDetails, CodeInvalidUpstream, "usage.input_tokens_details")
		_, result.hasCached = details["cached_tokens"]
	}
	return result, true, nil
}

type geminiStreamCall struct {
	id        string
	name      string
	arguments string
}

type geminiStreamAction struct {
	text *string
	call *geminiStreamCall
}

type geminiResponseText struct {
	outputIndex int
	itemID      string
	text        bytes.Buffer
	closed      bool
}

type geminiResponseOutput struct {
	text *geminiResponseText
	call *struct {
		outputIndex int
		itemID      string
		callID      string
		name        string
		arguments   string
	}
}

// GeminiToResponsesStream converts complete Gemini GenerateContentResponse
// data objects into typed Responses SSE events.
type GeminiToResponsesStream struct {
	sequence           int64
	started            bool
	terminal           bool
	sourceID           string
	model              string
	responseID         string
	nextOutput         int
	openText           *geminiResponseText
	outputs            []geminiResponseOutput
	callIDs            map[string]struct{}
	generatedCallIndex int
	pending            []geminiStreamAction
	outputBytes        int
	usage              geminiStreamUsage
}

func (s *GeminiToResponsesStream) event(name string, payload map[string]any, semantic, terminal bool) (SSEEvent, error) {
	payload["type"] = name
	payload["sequence_number"] = s.sequence
	s.sequence++
	data, err := marshal(payload)
	if err != nil {
		return SSEEvent{}, err
	}
	return SSEEvent{Name: name, Data: data, Semantic: semantic, Terminal: terminal}, nil
}

// Feed accepts one complete Gemini GenerateContentResponse JSON object.
func (s *GeminiToResponsesStream) Feed(raw []byte) ([]SSEEvent, error) {
	if s == nil || s.terminal {
		return nil, invalidUpstream("stream", "stream is already terminal")
	}
	root, err := decodeObject(raw, CodeInvalidUpstream, "")
	if err != nil {
		return nil, err
	}
	if err := rejectUnknown(root, map[string]bool{
		"candidates": true, "promptFeedback": true, "usageMetadata": true, "modelVersion": true, "responseId": true,
	}, ""); err != nil {
		return nil, err
	}
	if rawFeedback, ok := root["promptFeedback"]; ok {
		if bytes.Equal(bytes.TrimSpace(rawFeedback), []byte("null")) {
			return nil, invalidUpstream("promptFeedback", "null is not allowed")
		}
		feedback, err := decodeObject(rawFeedback, CodeInvalidUpstream, "promptFeedback")
		if err != nil {
			return nil, err
		}
		if err := rejectUnknown(feedback, map[string]bool{"blockReason": true, "safetyRatings": true}, "promptFeedback"); err != nil {
			return nil, err
		}
		reason, err := requireString(feedback["blockReason"], "promptFeedback.blockReason", true)
		if err != nil || reason == "" || reason == "BLOCK_REASON_UNSPECIFIED" {
			return nil, unsupported("promptFeedback")
		}
		var candidates []json.RawMessage
		if rawCandidates, ok := root["candidates"]; ok && json.Unmarshal(rawCandidates, &candidates) != nil {
			return nil, invalidUpstream("candidates", "an array is required")
		}
		if len(candidates) != 0 {
			return nil, invalidUpstream("promptFeedback", "a blocked prompt cannot include candidates")
		}
		return nil, unsupported("promptFeedback.blockReason")
	}
	if err := s.captureGeminiIdentity(root); err != nil {
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
	if err := rejectUnknown(candidate, map[string]bool{"content": true, "finishReason": true, "index": true, "safetyRatings": true}, "candidates[0]"); err != nil {
		return nil, err
	}
	if _, ok := candidate["safetyRatings"]; ok {
		return nil, unsupported("candidates[0].safetyRatings")
	}
	if rawIndex, ok := candidate["index"]; ok {
		index, err := requireInteger(rawIndex, "candidates[0].index", true)
		if err != nil || index != 0 {
			return nil, invalidUpstream("candidates[0].index", "expected zero")
		}
	}
	finish := ""
	if rawFinish, ok := candidate["finishReason"]; ok {
		finish, err = requireString(rawFinish, "candidates[0].finishReason", true)
		if err != nil || finish == "" || finish == "FINISH_REASON_UNSPECIFIED" {
			return nil, invalidUpstream("candidates[0].finishReason", "a terminal finish reason is required")
		}
		if finish != "STOP" && finish != "MAX_TOKENS" {
			return nil, unsupported("candidates[0].finishReason")
		}
	}
	actions, err := s.geminiActions(candidate["content"])
	if err != nil {
		return nil, err
	}
	if rawUsage, ok := root["usageMetadata"]; ok {
		if bytes.Equal(bytes.TrimSpace(rawUsage), []byte("null")) {
			return nil, invalidUpstream("usageMetadata", "null is not allowed")
		}
		usage, err := parseGeminiStreamUsage(rawUsage)
		if err != nil {
			return nil, err
		}
		if err := s.usage.apply(usage); err != nil {
			return nil, err
		}
	}
	if len(s.pending)+len(actions) > maxConvertedStreamItems {
		return nil, invalidUpstream("stream", "too many buffered Gemini stream actions")
	}
	if !s.started {
		s.pending = append(s.pending, actions...)
		if s.sourceID != "" && s.model != "" {
			started, err := s.startResponses()
			if err != nil {
				return nil, err
			}
			result := append([]SSEEvent(nil), started...)
			for _, action := range s.pending {
				converted, err := s.applyGeminiAction(action)
				if err != nil {
					return nil, err
				}
				result = append(result, converted...)
			}
			s.pending = nil
			if finish != "" {
				terminal, err := s.finishResponses(finish)
				return append(result, terminal...), err
			}
			return result, nil
		}
		if finish != "" {
			return nil, invalidUpstream("responseId", "terminal stream identity is incomplete")
		}
		return nil, nil
	}
	result := make([]SSEEvent, 0, len(actions)+4)
	for _, action := range actions {
		converted, err := s.applyGeminiAction(action)
		if err != nil {
			return nil, err
		}
		result = append(result, converted...)
	}
	if finish != "" {
		terminal, err := s.finishResponses(finish)
		return append(result, terminal...), err
	}
	return result, nil
}

func (s *GeminiToResponsesStream) captureGeminiIdentity(root map[string]json.RawMessage) error {
	fields := []struct {
		name   string
		stored *string
	}{
		{name: "responseId", stored: &s.sourceID},
		{name: "modelVersion", stored: &s.model},
	}
	for _, field := range fields {
		raw, ok := root[field.name]
		if !ok {
			continue
		}
		value, err := requireString(raw, field.name, true)
		if err != nil || value == "" {
			return invalidUpstream(field.name, "a non-empty string is required when present")
		}
		if *field.stored != "" && *field.stored != value {
			return invalidUpstream(field.name, "stream identity changed")
		}
		*field.stored = value
	}
	return nil
}

func (s *GeminiToResponsesStream) geminiActions(rawContent json.RawMessage) ([]geminiStreamAction, error) {
	if len(rawContent) == 0 {
		return nil, nil
	}
	if bytes.Equal(bytes.TrimSpace(rawContent), []byte("null")) {
		return nil, invalidUpstream("candidates[0].content", "null is not allowed")
	}
	content, err := decodeObject(rawContent, CodeInvalidUpstream, "candidates[0].content")
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
	if rawParts, ok := content["parts"]; ok {
		if bytes.Equal(bytes.TrimSpace(rawParts), []byte("null")) || json.Unmarshal(rawParts, &parts) != nil || parts == nil {
			return nil, invalidUpstream("candidates[0].content.parts", "an array is required")
		}
	}
	result := make([]geminiStreamAction, 0, len(parts))
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
			value, err := requireString(part["text"], joinField(field, "text"), true)
			if err != nil {
				return nil, err
			}
			if err := s.reserveGeminiOutput(len(value)); err != nil {
				return nil, err
			}
			text := value
			result = append(result, geminiStreamAction{text: &text})
		case part["functionCall"] != nil:
			if err := rejectUnknown(part, map[string]bool{"functionCall": true}, field); err != nil {
				return nil, err
			}
			callField := joinField(field, "functionCall")
			call, err := decodeObject(part["functionCall"], CodeInvalidUpstream, callField)
			if err != nil {
				return nil, err
			}
			if err := rejectUnknown(call, map[string]bool{"id": true, "name": true, "args": true}, callField); err != nil {
				return nil, err
			}
			id, present, err := optionalNonEmptyString(call["id"], joinField(callField, "id"), true)
			if err != nil {
				return nil, err
			}
			name, err := nonEmptyString(call["name"], joinField(callField, "name"), true)
			if err != nil {
				return nil, err
			}
			arguments, err := compactJSONObject(call["args"], joinField(callField, "args"), true)
			if err != nil {
				return nil, err
			}
			if err := s.reserveGeminiOutput(len(arguments)); err != nil {
				return nil, err
			}
			if s.callIDs == nil {
				s.callIDs = make(map[string]struct{})
			}
			if present {
				if err := reserveUniqueGeminiCallID(s.callIDs, id, joinField(callField, "id"), true); err != nil {
					return nil, err
				}
			}
			result = append(result, geminiStreamAction{call: &geminiStreamCall{id: id, name: name, arguments: arguments}})
		default:
			return nil, unsupported(field)
		}
	}
	return result, nil
}

func (s *GeminiToResponsesStream) reserveGeminiOutput(count int) error {
	if count < 0 || s.outputBytes > maxConvertedStreamBytes-count {
		return invalidUpstream("stream", "converted stream output exceeded the configured byte limit")
	}
	s.outputBytes += count
	return nil
}

func (s *GeminiToResponsesStream) startResponses() ([]SSEEvent, error) {
	s.started = true
	s.responseID = convertedEnvelopeID("resp", s.sourceID)
	snapshot := s.responsesSnapshot("in_progress")
	created, err := s.event("response.created", map[string]any{"response": snapshot}, false, false)
	if err != nil {
		return nil, err
	}
	inProgress, err := s.event("response.in_progress", map[string]any{"response": snapshot}, false, false)
	if err != nil {
		return nil, err
	}
	return []SSEEvent{created, inProgress}, nil
}

func (s *GeminiToResponsesStream) applyGeminiAction(action geminiStreamAction) ([]SSEEvent, error) {
	if action.text != nil {
		result := make([]SSEEvent, 0, 3)
		if s.openText == nil {
			if s.nextOutput >= maxConvertedStreamItems {
				return nil, invalidUpstream("stream", "too many stream output items")
			}
			text := &geminiResponseText{outputIndex: s.nextOutput, itemID: convertedItemID("msg", s.responseID, s.nextOutput)}
			s.nextOutput++
			s.openText = text
			s.outputs = append(s.outputs, geminiResponseOutput{text: text})
			item := map[string]any{"id": text.itemID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}
			added, _ := s.event("response.output_item.added", map[string]any{"response_id": s.responseID, "output_index": text.outputIndex, "item": item}, false, false)
			part := map[string]any{"type": "output_text", "text": "", "annotations": []any{}}
			partAdded, _ := s.event("response.content_part.added", map[string]any{"response_id": s.responseID, "item_id": text.itemID, "output_index": text.outputIndex, "content_index": 0, "part": part}, false, false)
			result = append(result, added, partAdded)
		}
		s.openText.text.WriteString(*action.text)
		delta, _ := s.event("response.output_text.delta", map[string]any{
			"response_id": s.responseID, "item_id": s.openText.itemID, "output_index": s.openText.outputIndex, "content_index": 0, "delta": *action.text,
		}, *action.text != "", false)
		return append(result, delta), nil
	}
	if action.call == nil {
		return nil, invalidUpstream("stream", "empty Gemini stream action")
	}
	result, err := s.closeGeminiText()
	if err != nil {
		return nil, err
	}
	if s.nextOutput >= maxConvertedStreamItems {
		return nil, invalidUpstream("stream", "too many stream output items")
	}
	callID := action.call.id
	if callID == "" {
		callID = nextUniqueGeminiCallID(s.sourceID, &s.generatedCallIndex, s.callIDs)
	}
	outputIndex := s.nextOutput
	s.nextOutput++
	itemID := convertedItemID("fc", s.responseID, outputIndex)
	callOutput := &struct {
		outputIndex int
		itemID      string
		callID      string
		name        string
		arguments   string
	}{outputIndex: outputIndex, itemID: itemID, callID: callID, name: action.call.name, arguments: action.call.arguments}
	s.outputs = append(s.outputs, geminiResponseOutput{call: callOutput})
	item := map[string]any{"id": itemID, "type": "function_call", "status": "in_progress", "call_id": callID, "name": action.call.name, "arguments": ""}
	added, _ := s.event("response.output_item.added", map[string]any{"response_id": s.responseID, "output_index": outputIndex, "item": item}, true, false)
	delta, _ := s.event("response.function_call_arguments.delta", map[string]any{"response_id": s.responseID, "item_id": itemID, "output_index": outputIndex, "delta": action.call.arguments}, action.call.arguments != "", false)
	done, _ := s.event("response.function_call_arguments.done", map[string]any{"response_id": s.responseID, "item_id": itemID, "output_index": outputIndex, "arguments": action.call.arguments}, false, false)
	item = map[string]any{"id": itemID, "type": "function_call", "status": "completed", "call_id": callID, "name": action.call.name, "arguments": action.call.arguments}
	itemDone, _ := s.event("response.output_item.done", map[string]any{"response_id": s.responseID, "output_index": outputIndex, "item": item}, false, false)
	return append(result, added, delta, done, itemDone), nil
}

func (s *GeminiToResponsesStream) closeGeminiText() ([]SSEEvent, error) {
	if s.openText == nil {
		return nil, nil
	}
	text := s.openText
	value := text.text.String()
	done, _ := s.event("response.output_text.done", map[string]any{"response_id": s.responseID, "item_id": text.itemID, "output_index": text.outputIndex, "content_index": 0, "text": value}, false, false)
	part := map[string]any{"type": "output_text", "text": value, "annotations": []any{}}
	partDone, _ := s.event("response.content_part.done", map[string]any{"response_id": s.responseID, "item_id": text.itemID, "output_index": text.outputIndex, "content_index": 0, "part": part}, false, false)
	item := map[string]any{"id": text.itemID, "type": "message", "status": "completed", "role": "assistant", "content": []any{part}}
	itemDone, _ := s.event("response.output_item.done", map[string]any{"response_id": s.responseID, "output_index": text.outputIndex, "item": item}, false, false)
	text.closed = true
	s.openText = nil
	return []SSEEvent{done, partDone, itemDone}, nil
}

func (s *GeminiToResponsesStream) finishResponses(finish string) ([]SSEEvent, error) {
	result, err := s.closeGeminiText()
	if err != nil {
		return nil, err
	}
	status := "completed"
	name := "response.completed"
	outcome := StreamTerminalCompleted
	if finish == "MAX_TOKENS" {
		status = "incomplete"
		name = "response.incomplete"
		outcome = StreamTerminalIncomplete
	}
	response := s.responsesSnapshot(status)
	if status == "incomplete" {
		response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	}
	terminal, err := s.event(name, map[string]any{"response": response}, false, true)
	if err != nil {
		return nil, err
	}
	terminal.TerminalOutcome = outcome
	s.terminal = true
	return append(result, terminal), nil
}

func (s *GeminiToResponsesStream) responsesSnapshot(status string) map[string]any {
	output := make([]any, 0, len(s.outputs))
	for _, value := range s.outputs {
		if value.text != nil {
			part := map[string]any{"type": "output_text", "text": value.text.text.String(), "annotations": []any{}}
			itemStatus := "completed"
			if !value.text.closed {
				itemStatus = "in_progress"
			}
			output = append(output, map[string]any{"id": value.text.itemID, "type": "message", "status": itemStatus, "role": "assistant", "content": []any{part}})
			continue
		}
		call := value.call
		output = append(output, map[string]any{"id": call.itemID, "type": "function_call", "status": "completed", "call_id": call.callID, "name": call.name, "arguments": call.arguments})
	}
	response := map[string]any{"id": s.responseID, "object": "response", "created_at": int64(0), "model": s.model, "status": status, "output": output}
	if usage := s.usage.responsesUsage(); usage != nil {
		response["usage"] = usage
	}
	return response
}

// EOF reports transport EOF without a valid Gemini finish reason.
func (s *GeminiToResponsesStream) EOF() error {
	if s != nil && s.terminal {
		return nil
	}
	return interrupted("Gemini stream ended before a terminal finish reason")
}

type responsesGeminiPending struct {
	events []SSEEvent
	done   bool
}

// ResponsesToGeminiStream converts complete typed Responses events into
// Gemini GenerateContentResponse data-only SSE events.
type ResponsesToGeminiStream struct {
	validator  ResponsesToChatStream
	terminal   bool
	responseID string
	pending    map[int]*responsesGeminiPending
	itemStatus map[int]string
	nextOutput int
	heldBytes  int
	usage      geminiStreamUsage
}

// Feed accepts one complete Responses event JSON object.
func (s *ResponsesToGeminiStream) Feed(raw []byte) ([]SSEEvent, error) {
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
	if kind == "response.completed" || kind == "response.incomplete" {
		return s.feedResponsesTerminal(kind, event)
	}
	if kind == "response.failed" || kind == "error" {
		return nil, invalidUpstream("type", "upstream returned a terminal failure event")
	}
	if kind == "response.created" || kind == "response.in_progress" {
		if err := validateResponsesSnapshotForGemini(event["response"]); err != nil {
			return nil, err
		}
	}
	feedRaw := raw
	doneStatus := ""
	if kind == "response.output_item.done" {
		feedRaw, doneStatus, err = normalizeResponsesDoneForGemini(event)
		if err != nil {
			return nil, err
		}
	}
	if _, err := s.validator.Feed(feedRaw); err != nil {
		return nil, err
	}
	if kind == "response.created" || kind == "response.in_progress" {
		if s.responseID == "" {
			s.responseID = convertedEnvelopeID("gemini", s.validator.sourceID)
		}
		if err := s.captureResponsesUsage(event["response"]); err != nil {
			return nil, err
		}
		return nil, nil
	}
	s.ensureResponsesPending()
	switch kind {
	case "response.output_item.added":
		index, _ := requireInteger(event["output_index"], "output_index", true)
		s.pending[int(index)] = &responsesGeminiPending{}
		return nil, nil
	case "response.output_text.delta":
		index, _ := requireInteger(event["output_index"], "output_index", true)
		delta, _ := requireString(event["delta"], "delta", true)
		converted, err := s.geminiEvent([]any{map[string]any{"text": delta}}, "", delta != "", false, StreamTerminalNone)
		if err != nil {
			return nil, err
		}
		if err := s.holdResponsesEvent(int(index), converted); err != nil {
			return nil, err
		}
		return s.flushResponsesPending(), nil
	case "response.output_item.done":
		index, _ := requireInteger(event["output_index"], "output_index", true)
		if s.itemStatus == nil {
			s.itemStatus = make(map[int]string)
		}
		s.itemStatus[int(index)] = doneStatus
		item := s.validator.outputs[int(index)]
		pending := s.pending[int(index)]
		if item == nil || pending == nil {
			return nil, invalidUpstream("output_index", "output item is missing")
		}
		if item.kind == "function_call" {
			var arguments map[string]any
			decoder := json.NewDecoder(bytes.NewBufferString(item.arguments.String()))
			decoder.UseNumber()
			if decoder.Decode(&arguments) != nil || arguments == nil {
				return nil, invalidUpstream("item.arguments", "expected a JSON object string")
			}
			part := map[string]any{"functionCall": map[string]any{"id": item.callID, "name": item.name, "args": arguments}}
			converted, err := s.geminiEvent([]any{part}, "", true, false, StreamTerminalNone)
			if err != nil {
				return nil, err
			}
			if err := s.holdResponsesEvent(int(index), converted); err != nil {
				return nil, err
			}
		}
		pending.done = true
		return s.flushResponsesPending(), nil
	default:
		return nil, nil
	}
}

func validateResponsesSnapshotForGemini(rawResponse json.RawMessage) error {
	response, err := decodeObject(rawResponse, CodeInvalidUpstream, "response")
	if err != nil {
		return err
	}
	for _, field := range []string{"background", "store"} {
		if value, present, err := optionalUpstreamBool(response, field); err != nil {
			return err
		} else if present && value {
			return invalidUpstream("response."+field, "stateful response cannot be converted")
		}
	}
	for _, field := range []string{"previous_response_id", "conversation", "error", "incomplete_details"} {
		if value, ok := response[field]; ok && !bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return invalidUpstream("response."+field, "field is not representable")
		}
	}
	if rawOutput, ok := response["output"]; ok {
		var output []json.RawMessage
		if bytes.Equal(bytes.TrimSpace(rawOutput), []byte("null")) || json.Unmarshal(rawOutput, &output) != nil || output == nil {
			return invalidUpstream("response.output", "an array is required")
		}
		for index, rawItem := range output {
			field := indexField("response.output", index)
			item, err := decodeObject(rawItem, CodeInvalidUpstream, field)
			if err != nil {
				return err
			}
			kind, err := requireString(item["type"], joinField(field, "type"), true)
			if err != nil {
				return err
			}
			if kind != "message" && kind != "function_call" {
				return unsupported(joinField(field, "type"))
			}
		}
	}
	return nil
}

func normalizeResponsesDoneForGemini(event map[string]json.RawMessage) ([]byte, string, error) {
	item, err := decodeObject(event["item"], CodeInvalidUpstream, "item")
	if err != nil {
		return nil, "", err
	}
	status, err := requireString(item["status"], "item.status", true)
	if err != nil || (status != "completed" && status != "incomplete") {
		return nil, "", invalidUpstream("item.status", "done output item must be completed or incomplete")
	}
	if status == "completed" {
		raw, err := marshalRawObject(event)
		return raw, status, err
	}
	item["status"] = json.RawMessage(`"completed"`)
	normalizedItem, err := marshalRawObject(item)
	if err != nil {
		return nil, "", err
	}
	event["item"] = normalizedItem
	raw, err := marshalRawObject(event)
	return raw, status, err
}

func marshalRawObject(value map[string]json.RawMessage) ([]byte, error) {
	return json.Marshal(value)
}

func (s *ResponsesToGeminiStream) ensureResponsesPending() {
	if s.pending == nil {
		s.pending = make(map[int]*responsesGeminiPending)
	}
}

func (s *ResponsesToGeminiStream) holdResponsesEvent(index int, event SSEEvent) error {
	pending := s.pending[index]
	if pending == nil {
		return invalidUpstream("output_index", "event references an unknown output item")
	}
	if len(event.Data) > maxConvertedStreamBytes-s.heldBytes {
		return invalidUpstream("stream", "delayed Gemini stream exceeded the configured byte limit")
	}
	s.heldBytes += len(event.Data)
	pending.events = append(pending.events, event)
	return nil
}

func (s *ResponsesToGeminiStream) flushResponsesPending() []SSEEvent {
	var result []SSEEvent
	for {
		pending := s.pending[s.nextOutput]
		if pending == nil {
			return result
		}
		for _, event := range pending.events {
			s.heldBytes -= len(event.Data)
		}
		result = append(result, pending.events...)
		pending.events = nil
		if !pending.done {
			return result
		}
		delete(s.pending, s.nextOutput)
		s.nextOutput++
	}
}

func (s *ResponsesToGeminiStream) captureResponsesUsage(rawResponse json.RawMessage) error {
	usage, present, err := parseResponsesStreamUsageForGemini(rawResponse)
	if err != nil || !present {
		return err
	}
	return s.usage.apply(usage)
}

func (s *ResponsesToGeminiStream) feedResponsesTerminal(kind string, event map[string]json.RawMessage) ([]SSEEvent, error) {
	if err := validateResponsesEventFields(kind, event); err != nil {
		return nil, err
	}
	sequence, err := requireInteger(event["sequence_number"], "sequence_number", true)
	if err != nil {
		return nil, err
	}
	if !s.validator.seqStarted || sequence != s.validator.nextSeq {
		return nil, invalidUpstream("sequence_number", "event sequence is not contiguous")
	}
	if !s.validator.started {
		return nil, invalidUpstream("type", "response.created must precede terminal response events")
	}
	responseRaw := event["response"]
	canonical, err := parseResponsesOutput(responseRaw)
	if err != nil {
		return nil, err
	}
	expectedStatus := "completed"
	finish := "STOP"
	outcome := StreamTerminalCompleted
	if kind == "response.incomplete" {
		expectedStatus = "incomplete"
		finish = "MAX_TOKENS"
		outcome = StreamTerminalIncomplete
	}
	if canonical.Status != expectedStatus {
		return nil, invalidUpstream("response.status", "terminal event does not match response status")
	}
	if err := s.validator.captureResponseMetadataFromRaw(responseRaw); err != nil {
		return nil, err
	}
	if err := validateResponsesTerminalOutputForGemini(responseRaw, &s.validator, s.itemStatus); err != nil {
		return nil, err
	}
	if err := s.captureResponsesUsage(responseRaw); err != nil {
		return nil, err
	}
	if len(s.pending) != 0 || s.nextOutput != len(s.validator.outputs) {
		return nil, invalidUpstream("response.output", "converted output items were not emitted in order")
	}
	s.validator.nextSeq = sequence + 1
	s.validator.terminal = true
	s.terminal = true
	terminal, err := s.geminiEvent(nil, finish, false, true, outcome)
	if err != nil {
		return nil, err
	}
	return []SSEEvent{terminal}, nil
}

func validateResponsesTerminalOutputForGemini(raw json.RawMessage, validator *ResponsesToChatStream, streamedStatuses map[int]string) error {
	response, err := decodeObject(raw, CodeInvalidUpstream, "response")
	if err != nil {
		return err
	}
	var output []json.RawMessage
	if json.Unmarshal(response["output"], &output) != nil || output == nil {
		return invalidUpstream("response.output", "an array is required")
	}
	if len(output) != len(validator.outputs) {
		return invalidUpstream("response.output", "terminal output count does not match streamed items")
	}
	responseStatus, err := requireString(response["status"], "response.status", true)
	if err != nil {
		return err
	}
	for index, rawItem := range output {
		streamed := validator.outputs[index]
		if streamed == nil || !streamed.done {
			return invalidUpstream(indexField("response.output", index), "output item was not completed in the stream")
		}
		item, err := decodeObject(rawItem, CodeInvalidUpstream, indexField("response.output", index))
		if err != nil {
			return err
		}
		itemStatus, err := requireString(item["status"], joinField(indexField("response.output", index), "status"), true)
		if err != nil || itemStatus != streamedStatuses[index] {
			return invalidUpstream(joinField(indexField("response.output", index), "status"), "terminal item status changed")
		}
		if responseStatus == "completed" && itemStatus != "completed" || responseStatus == "incomplete" && itemStatus != "completed" && itemStatus != "incomplete" {
			return invalidUpstream(joinField(indexField("response.output", index), "status"), "item status is inconsistent with the response")
		}
		item["status"] = json.RawMessage(`"completed"`)
		if err := validateStreamItemObject(streamed, item); err != nil {
			return err
		}
	}
	return nil
}

func (s *ResponsesToGeminiStream) geminiEvent(parts []any, finish string, semantic, terminal bool, outcome StreamTerminalOutcome) (SSEEvent, error) {
	if parts == nil {
		parts = []any{}
	}
	candidate := map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": parts}}
	if finish != "" {
		candidate["finishReason"] = finish
	}
	payload := map[string]any{"responseId": s.responseID, "modelVersion": s.validator.model, "candidates": []any{candidate}}
	if terminal {
		if usage := s.usage.geminiUsage(); usage != nil {
			payload["usageMetadata"] = usage
		}
	}
	data, err := marshal(payload)
	if err != nil {
		return SSEEvent{}, err
	}
	return SSEEvent{Data: data, Semantic: semantic, Terminal: terminal, TerminalOutcome: outcome}, nil
}

// EOF reports transport EOF without a terminal Responses event.
func (s *ResponsesToGeminiStream) EOF() error {
	if s != nil && s.terminal {
		return nil
	}
	return interrupted("Responses stream ended before a terminal response event")
}
