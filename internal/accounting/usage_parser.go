package accounting

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
)

// ErrInvalidUsage deliberately contains no provider response detail. Once an
// accumulator returns this error, Usage returns an entirely unknown result.
var ErrInvalidUsage = errors.New("invalid protocol usage metadata")

// UsageProtocol identifies the wire contract whose usage metadata is parsed.
type UsageProtocol string

const (
	ProtocolOpenAIChatCompletions UsageProtocol = "openai-chat-completions"
	ProtocolOpenAIResponses       UsageProtocol = "openai-responses"
	ProtocolAnthropicMessages     UsageProtocol = "anthropic-messages"
	ProtocolGeminiGenerateContent UsageProtocol = "gemini-generate-content"
)

// UsageAccumulator consumes complete JSON response objects or individual SSE
// data JSON objects. It retains only normalized counters, never response JSON.
// It is intended to be owned by one request-forwarding goroutine.
type UsageAccumulator struct {
	protocol UsageProtocol
	usage    Usage
	invalid  bool
}

func NewUsageAccumulator(protocol UsageProtocol) (*UsageAccumulator, error) {
	switch protocol {
	case ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses, ProtocolAnthropicMessages, ProtocolGeminiGenerateContent:
		return &UsageAccumulator{protocol: protocol}, nil
	default:
		return nil, ErrInvalidUsage
	}
}

// ParseUsage parses one complete non-streaming response. A valid response with
// no usage metadata returns an all-nil Usage.
func ParseUsage(protocol UsageProtocol, dataJSON []byte) (Usage, error) {
	acc, err := NewUsageAccumulator(protocol)
	if err != nil {
		return Usage{}, err
	}
	if err := acc.Observe(dataJSON); err != nil {
		return Usage{}, err
	}
	return acc.Usage(), nil
}

// Observe consumes one already accepted protocol JSON object. Snapshot-style
// protocols replace prior metadata; Anthropic message_start/message_delta
// events merge the documented input and cumulative-output snapshots.
func (a *UsageAccumulator) Observe(dataJSON []byte) error {
	if a == nil || a.invalid {
		return ErrInvalidUsage
	}
	root, err := decodeJSONObject(dataJSON)
	if err != nil {
		return a.poison()
	}
	var observed *Usage
	switch a.protocol {
	case ProtocolOpenAIChatCompletions:
		observed, err = parseOpenAIChat(root)
	case ProtocolOpenAIResponses:
		observed, err = parseOpenAIResponses(root)
	case ProtocolAnthropicMessages:
		err = a.observeAnthropic(root)
	case ProtocolGeminiGenerateContent:
		observed, err = parseGemini(root)
	default:
		err = ErrInvalidUsage
	}
	if err != nil {
		return a.poison()
	}
	if observed != nil {
		a.usage = cloneUsage(*observed)
	}
	return nil
}

// Usage returns a defensive copy. If any observed usage metadata was invalid,
// every counter is unknown so a caller cannot accidentally bill partial data.
func (a *UsageAccumulator) Usage() Usage {
	if a == nil || a.invalid {
		return Usage{}
	}
	return cloneUsage(a.usage)
}

func (a *UsageAccumulator) poison() error {
	a.invalid = true
	a.usage = Usage{}
	return ErrInvalidUsage
}

func parseOpenAIChat(root jsonObject) (*Usage, error) {
	if isErrorObject(root) {
		return nil, nil
	}
	usage, present, err := objectField(root, "usage")
	if err != nil || !present {
		return nil, err
	}
	return normalizeOpenAI(usage, "prompt_tokens", "completion_tokens", "prompt_tokens_details", "completion_tokens_details")
}

func parseOpenAIResponses(root jsonObject) (*Usage, error) {
	if isErrorObject(root) {
		return nil, nil
	}
	typ, _, err := stringField(root, "type")
	if err != nil {
		return nil, err
	}
	if typ == "response.failed" || typ == "response.cancelled" {
		return nil, nil
	}
	container := root
	if typ != "" {
		switch typ {
		case "response.completed", "response.incomplete":
			var present bool
			container, present, err = objectField(root, "response")
			if err != nil || !present {
				return nil, err
			}
		default:
			return nil, nil
		}
	}
	usage, present, err := objectField(container, "usage")
	if err != nil || !present {
		return nil, err
	}
	return normalizeOpenAI(usage, "input_tokens", "output_tokens", "input_tokens_details", "output_tokens_details")
}

func normalizeOpenAI(usage jsonObject, inputKey, outputKey, inputDetailsKey, outputDetailsKey string) (*Usage, error) {
	totalInput, _, err := integerField(usage, inputKey)
	if err != nil {
		return nil, err
	}
	output, _, err := integerField(usage, outputKey)
	if err != nil {
		return nil, err
	}
	total, _, err := integerField(usage, "total_tokens")
	if err != nil {
		return nil, err
	}
	inputDetails, detailsPresent, err := objectField(usage, inputDetailsKey)
	if err != nil {
		return nil, err
	}
	var cacheRead, cacheWrite *int64
	if detailsPresent {
		cacheRead, _, err = integerField(inputDetails, "cached_tokens")
		if err != nil {
			return nil, err
		}
		cacheWrite, _, err = integerField(inputDetails, "cache_write_tokens")
		if err != nil {
			return nil, err
		}
	}
	outputDetails, outputDetailsPresent, err := objectField(usage, outputDetailsKey)
	if err != nil {
		return nil, err
	}
	if outputDetailsPresent {
		reasoning, _, err := integerField(outputDetails, "reasoning_tokens")
		if err != nil {
			return nil, err
		}
		// OpenAI documents reasoning_tokens as a subset of output tokens.
		if reasoning != nil && output != nil && *reasoning > *output {
			return nil, ErrInvalidUsage
		}
	}
	if total != nil && totalInput != nil && output != nil {
		sum, ok := checkedAdd(*totalInput, *output)
		if !ok || sum != *total {
			return nil, ErrInvalidUsage
		}
	}
	if totalInput != nil && ((cacheRead != nil && *cacheRead > *totalInput) || (cacheWrite != nil && *cacheWrite > *totalInput)) {
		return nil, ErrInvalidUsage
	}
	var cachedTotal *int64
	if cacheRead != nil && cacheWrite != nil {
		cached, ok := checkedAdd(*cacheRead, *cacheWrite)
		if !ok {
			return nil, ErrInvalidUsage
		}
		cachedTotal = int64Pointer(cached)
	}
	var ordinary *int64
	if totalInput != nil && cachedTotal != nil {
		if *cachedTotal > *totalInput {
			return nil, ErrInvalidUsage
		}
		ordinary = int64Pointer(*totalInput - *cachedTotal)
	}
	return &Usage{
		InputTokens:      ordinary,
		OutputTokens:     cloneInt64(output),
		CacheReadTokens:  cloneInt64(cacheRead),
		CacheWriteTokens: cloneInt64(cacheWrite),
	}, nil
}

func (a *UsageAccumulator) observeAnthropic(root jsonObject) error {
	if isErrorObject(root) {
		return nil
	}
	typ, _, err := stringField(root, "type")
	if err != nil {
		return err
	}
	switch typ {
	case "message":
		usage, present, err := objectField(root, "usage")
		if err != nil || !present {
			return err
		}
		normalized, err := normalizeAnthropic(usage)
		if err != nil {
			return err
		}
		a.usage = normalized
	case "message_start":
		message, present, err := objectField(root, "message")
		if err != nil || !present {
			return err
		}
		usage, present, err := objectField(message, "usage")
		if err != nil || !present {
			return err
		}
		normalized, err := normalizeAnthropic(usage)
		if err != nil {
			return err
		}
		a.usage = normalized
	case "message_delta":
		usage, present, err := objectField(root, "usage")
		if err != nil || !present {
			return err
		}
		return mergeAnthropicUsage(&a.usage, usage)
	default:
		// Ping, content block, stop, and future events carry no accepted
		// top-level billing snapshot in the Messages streaming contract.
		return nil
	}
	return nil
}

func normalizeAnthropic(usage jsonObject) (Usage, error) {
	var result Usage
	var err error
	if result.InputTokens, _, err = integerField(usage, "input_tokens"); err != nil {
		return Usage{}, err
	}
	if result.OutputTokens, _, err = integerField(usage, "output_tokens"); err != nil {
		return Usage{}, err
	}
	if result.CacheReadTokens, _, err = integerField(usage, "cache_read_input_tokens"); err != nil {
		return Usage{}, err
	}
	if result.CacheWriteTokens, _, err = integerField(usage, "cache_creation_input_tokens"); err != nil {
		return Usage{}, err
	}
	return cloneUsage(result), nil
}

func mergeAnthropicUsage(current *Usage, usage jsonObject) error {
	fields := []struct {
		name string
		dest **int64
	}{
		{"input_tokens", &current.InputTokens},
		{"output_tokens", &current.OutputTokens},
		{"cache_read_input_tokens", &current.CacheReadTokens},
		{"cache_creation_input_tokens", &current.CacheWriteTokens},
	}
	for _, field := range fields {
		value, present, err := integerField(usage, field.name)
		if err != nil {
			return err
		}
		if present {
			*field.dest = cloneInt64(value)
		}
	}
	return nil
}

func parseGemini(root jsonObject) (*Usage, error) {
	if isErrorObject(root) {
		return nil, nil
	}
	metadata, present, err := objectField(root, "usageMetadata")
	if err != nil || !present {
		return nil, err
	}
	prompt, _, err := integerField(metadata, "promptTokenCount")
	if err != nil {
		return nil, err
	}
	cacheRead, _, err := integerField(metadata, "cachedContentTokenCount")
	if err != nil {
		return nil, err
	}
	candidates, _, err := integerField(metadata, "candidatesTokenCount")
	if err != nil {
		return nil, err
	}
	thoughts, _, err := integerField(metadata, "thoughtsTokenCount")
	if err != nil {
		return nil, err
	}
	total, _, err := integerField(metadata, "totalTokenCount")
	if err != nil {
		return nil, err
	}
	var ordinary *int64
	if prompt != nil && cacheRead != nil {
		if *cacheRead > *prompt {
			return nil, ErrInvalidUsage
		}
		ordinary = int64Pointer(*prompt - *cacheRead)
	}
	var generated *int64
	if candidates != nil && thoughts != nil {
		value, ok := checkedAdd(*candidates, *thoughts)
		if !ok {
			return nil, ErrInvalidUsage
		}
		generated = int64Pointer(value)
	}
	if total != nil && prompt != nil {
		if *total < *prompt {
			return nil, ErrInvalidUsage
		}
		fromTotal := *total - *prompt
		if generated != nil && *generated != fromTotal {
			return nil, ErrInvalidUsage
		}
		generated = int64Pointer(fromTotal)
	}
	if generated != nil && candidates != nil && *generated < *candidates {
		return nil, ErrInvalidUsage
	}
	// Cache creation is a separate Gemini API operation and is not a
	// generateContent usage category.
	zero := int64(0)
	return &Usage{
		InputTokens:      ordinary,
		OutputTokens:     generated,
		CacheReadTokens:  cloneInt64(cacheRead),
		CacheWriteTokens: &zero,
	}, nil
}

type jsonObject map[string]json.RawMessage

func decodeJSONObject(data []byte) (jsonObject, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object jsonObject
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, ErrInvalidUsage
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidUsage
	}
	return object, nil
}

func objectField(object jsonObject, name string) (jsonObject, bool, error) {
	raw, ok := object[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false, nil
	}
	var nested jsonObject
	if err := json.Unmarshal(raw, &nested); err != nil || nested == nil {
		return nil, false, ErrInvalidUsage
	}
	return nested, true, nil
}

func stringField(object jsonObject, name string) (string, bool, error) {
	raw, ok := object[name]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", false, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false, ErrInvalidUsage
	}
	return value, true, nil
}

func integerField(object jsonObject, name string) (*int64, bool, error) {
	raw, ok := object[name]
	if !ok {
		return nil, false, nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true, nil
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil || value < 0 {
		return nil, true, ErrInvalidUsage
	}
	return &value, true, nil
}

func isErrorObject(root jsonObject) bool {
	if typ, _, _ := stringField(root, "type"); typ == "error" {
		return true
	}
	raw, ok := root["error"]
	return ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func checkedAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func cloneUsage(usage Usage) Usage {
	return Usage{
		InputTokens:      cloneInt64(usage.InputTokens),
		OutputTokens:     cloneInt64(usage.OutputTokens),
		CacheReadTokens:  cloneInt64(usage.CacheReadTokens),
		CacheWriteTokens: cloneInt64(usage.CacheWriteTokens),
	}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	return int64Pointer(*value)
}

func int64Pointer(value int64) *int64 {
	return &value
}
