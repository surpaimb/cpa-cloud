package accounting

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestParseUsageCompleteJSON(t *testing.T) {
	tests := []struct {
		name     string
		protocol UsageProtocol
		payload  string
		want     Usage
	}{
		{
			name:     "chat completions",
			protocol: ProtocolOpenAIChatCompletions,
			payload:  `{"choices":[{"message":{"content":"not retained"}}],"usage":{"prompt_tokens":100,"completion_tokens":30,"total_tokens":130,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":10},"completion_tokens_details":{"reasoning_tokens":12}}}`,
			want:     usageValues(70, 30, 20, 10),
		},
		{
			name:     "responses",
			protocol: ProtocolOpenAIResponses,
			payload:  `{"object":"response","output":[{"content":"not retained"}],"usage":{"input_tokens":90,"output_tokens":40,"total_tokens":130,"input_tokens_details":{"cached_tokens":25,"cache_write_tokens":5},"output_tokens_details":{"reasoning_tokens":30}}}`,
			want:     usageValues(60, 40, 25, 5),
		},
		{
			name:     "anthropic messages",
			protocol: ProtocolAnthropicMessages,
			payload:  `{"type":"message","content":[{"type":"text","text":"not retained"}],"usage":{"input_tokens":11,"output_tokens":12,"cache_read_input_tokens":13,"cache_creation_input_tokens":14}}`,
			want:     usageValues(11, 12, 13, 14),
		},
		{
			name:     "gemini",
			protocol: ProtocolGeminiGenerateContent,
			payload:  `{"candidates":[{"content":{"parts":[{"text":"not retained"}]}}],"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":20,"candidatesTokenCount":30,"thoughtsTokenCount":10,"totalTokenCount":140}}`,
			want:     usageValues(80, 40, 20, 0),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ParseUsage(test.protocol, []byte(test.payload))
			if err != nil {
				t.Fatalf("ParseUsage: %v", err)
			}
			assertUsageEqual(t, got, test.want)
		})
	}
}

func TestOpenAIStreamingSnapshotsOverwriteAndRepeat(t *testing.T) {
	acc := mustAccumulator(t, ProtocolOpenAIChatCompletions)
	observe(t, acc, `{"id":"chat","choices":[{"delta":{"content":"marker-secret"}}]}`)
	first := `{"id":"chat","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`
	observe(t, acc, first)
	observe(t, acc, first)
	assertUsageEqual(t, acc.Usage(), usageValues(14, 5, 4, 2))

	observe(t, acc, `{"id":"chat","choices":[],"usage":{"prompt_tokens":30,"completion_tokens":7,"total_tokens":37,"prompt_tokens_details":{"cached_tokens":5,"cache_write_tokens":3},"completion_tokens_details":{"reasoning_tokens":2}}}`)
	assertUsageEqual(t, acc.Usage(), usageValues(22, 7, 5, 3))
}

func TestResponsesSSEEvents(t *testing.T) {
	acc := mustAccumulator(t, ProtocolOpenAIResponses)
	observe(t, acc, `{"type":"response.output_text.delta","delta":"marker-secret"}`)
	completed := `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":50,"output_tokens":20,"total_tokens":70,"input_tokens_details":{"cached_tokens":10,"cache_write_tokens":5},"output_tokens_details":{"reasoning_tokens":12}}}}`
	observe(t, acc, completed)
	observe(t, acc, completed)
	assertUsageEqual(t, acc.Usage(), usageValues(35, 20, 10, 5))

	observe(t, acc, `{"type":"response.incomplete","response":{"status":"incomplete","usage":{"input_tokens":60,"output_tokens":25,"total_tokens":85,"input_tokens_details":{"cached_tokens":12,"cache_write_tokens":6},"output_tokens_details":{"reasoning_tokens":20}}}}`)
	assertUsageEqual(t, acc.Usage(), usageValues(42, 25, 12, 6))
}

func TestAnthropicStreamingMergesCumulativeSnapshots(t *testing.T) {
	acc := mustAccumulator(t, ProtocolAnthropicMessages)
	start := `{"type":"message_start","message":{"content":[],"usage":{"input_tokens":25,"output_tokens":1,"cache_read_input_tokens":100,"cache_creation_input_tokens":50}}}`
	observe(t, acc, start)
	observe(t, acc, `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"marker-secret"}}`)
	delta := `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":15}}`
	observe(t, acc, delta)
	observe(t, acc, delta)
	observe(t, acc, `{"type":"message_stop"}`)
	assertUsageEqual(t, acc.Usage(), usageValues(25, 15, 100, 50))
}

func TestGeminiStreamingSnapshotsOverwrite(t *testing.T) {
	acc := mustAccumulator(t, ProtocolGeminiGenerateContent)
	observe(t, acc, `{"candidates":[{"content":{"parts":[{"text":"marker-secret"}]}}]}`)
	first := `{"usageMetadata":{"promptTokenCount":20,"cachedContentTokenCount":5,"candidatesTokenCount":4,"thoughtsTokenCount":1,"totalTokenCount":25}}`
	observe(t, acc, first)
	observe(t, acc, first)
	observe(t, acc, `{"usageMetadata":{"promptTokenCount":30,"cachedContentTokenCount":6,"candidatesTokenCount":7,"thoughtsTokenCount":3,"totalTokenCount":40}}`)
	assertUsageEqual(t, acc.Usage(), usageValues(24, 10, 6, 0))
}

func TestUnknownAndMissingUsageRemainNil(t *testing.T) {
	for _, protocol := range []UsageProtocol{ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses, ProtocolAnthropicMessages, ProtocolGeminiGenerateContent} {
		got, err := ParseUsage(protocol, []byte(`{"content":"marker-secret"}`))
		if err != nil {
			t.Fatalf("%s: %v", protocol, err)
		}
		assertUsageEqual(t, got, Usage{})
	}

	got, err := ParseUsage(ProtocolOpenAIChatCompletions, []byte(`{"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	if err != nil {
		t.Fatal(err)
	}
	assertUsageEqual(t, got, Usage{OutputTokens: testInt64(2)})

	got, err = ParseUsage(ProtocolGeminiGenerateContent, []byte(`{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}`))
	if err != nil {
		t.Fatal(err)
	}
	assertUsageEqual(t, got, Usage{CacheWriteTokens: testInt64(0)})
}

func TestGeminiCanDeriveCombinedOutputFromTotal(t *testing.T) {
	got, err := ParseUsage(ProtocolGeminiGenerateContent, []byte(`{"usageMetadata":{"promptTokenCount":100,"cachedContentTokenCount":20,"candidatesTokenCount":30,"totalTokenCount":140}}`))
	if err != nil {
		t.Fatal(err)
	}
	assertUsageEqual(t, got, usageValues(80, 40, 20, 0))
}

func TestFailureEventsDoNotExposeOrInventUsage(t *testing.T) {
	marker := "synthetic-secret-error-body"
	tests := []struct {
		protocol UsageProtocol
		payload  string
	}{
		{ProtocolOpenAIChatCompletions, `{"error":{"message":"` + marker + `"}}`},
		{ProtocolOpenAIResponses, `{"type":"response.failed","response":{"error":{"message":"` + marker + `"},"usage":{"input_tokens":99}}}`},
		{ProtocolAnthropicMessages, `{"type":"error","error":{"message":"` + marker + `"}}`},
		{ProtocolGeminiGenerateContent, `{"error":{"message":"` + marker + `"}}`},
	}
	for _, test := range tests {
		acc := mustAccumulator(t, test.protocol)
		if err := acc.Observe([]byte(test.payload)); err != nil {
			t.Fatalf("%s: %v", test.protocol, err)
		}
		assertUsageEqual(t, acc.Usage(), Usage{})
		if strings.Contains(fmt.Sprintf("%#v", acc), marker) {
			t.Fatalf("%s accumulator retained error body", test.protocol)
		}
	}
}

func TestInvalidUsagePoisonsAccumulatorWithFixedError(t *testing.T) {
	tests := []struct {
		name     string
		protocol UsageProtocol
		payload  string
	}{
		{"negative", ProtocolAnthropicMessages, `{"type":"message","usage":{"input_tokens":-1}}`},
		{"fraction", ProtocolAnthropicMessages, `{"type":"message","usage":{"output_tokens":1.5}}`},
		{"openai cache exceeds input", ProtocolOpenAIChatCompletions, `{"usage":{"prompt_tokens":5,"completion_tokens":1,"total_tokens":6,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":2}}}`},
		{"openai one known cache subset exceeds input", ProtocolOpenAIChatCompletions, `{"usage":{"prompt_tokens":5,"prompt_tokens_details":{"cached_tokens":6}}}`},
		{"openai cached sum overflow", ProtocolOpenAIChatCompletions, fmt.Sprintf(`{"usage":{"prompt_tokens":%d,"prompt_tokens_details":{"cached_tokens":%d,"cache_write_tokens":1}}}`, math.MaxInt64, math.MaxInt64)},
		{"openai reasoning exceeds output", ProtocolOpenAIResponses, `{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":3}}}`},
		{"openai total mismatch", ProtocolOpenAIResponses, `{"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":4,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`},
		{"gemini cache exceeds prompt", ProtocolGeminiGenerateContent, `{"usageMetadata":{"promptTokenCount":2,"cachedContentTokenCount":3}}`},
		{"gemini generated overflow", ProtocolGeminiGenerateContent, fmt.Sprintf(`{"usageMetadata":{"candidatesTokenCount":%d,"thoughtsTokenCount":1}}`, math.MaxInt64)},
		{"gemini total mismatch", ProtocolGeminiGenerateContent, `{"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":14}}`},
		{"malformed", ProtocolOpenAIChatCompletions, `{`},
		{"trailing", ProtocolOpenAIChatCompletions, `{} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			acc := mustAccumulator(t, test.protocol)
			if err := acc.Observe([]byte(test.payload)); !errors.Is(err, ErrInvalidUsage) || err.Error() != ErrInvalidUsage.Error() {
				t.Fatalf("got error %v", err)
			}
			assertUsageEqual(t, acc.Usage(), Usage{})
			if err := acc.Observe([]byte(`{}`)); !errors.Is(err, ErrInvalidUsage) {
				t.Fatalf("poisoned accumulator returned %v", err)
			}
		})
	}
}

func TestInvalidAnthropicDeltaDiscardsEarlierUsage(t *testing.T) {
	acc := mustAccumulator(t, ProtocolAnthropicMessages)
	observe(t, acc, `{"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":2,"cache_creation_input_tokens":3}}}`)
	if err := acc.Observe([]byte(`{"type":"message_delta","usage":{"output_tokens":-1}}`)); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("got %v", err)
	}
	assertUsageEqual(t, acc.Usage(), Usage{})
}

func TestAccumulatorDoesNotRetainBodyAndReturnsDefensiveCopy(t *testing.T) {
	const marker = "synthetic-prompt-or-response-marker"
	acc := mustAccumulator(t, ProtocolOpenAIChatCompletions)
	observe(t, acc, `{"choices":[{"message":{"content":"`+marker+`"}}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7,"prompt_tokens_details":{"cached_tokens":1,"cache_write_tokens":1}}}`)
	encoded, err := json.Marshal(acc)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "{}" || strings.Contains(fmt.Sprintf("%#v", acc), marker) {
		t.Fatalf("accumulator exposed or retained response body: json=%s struct=%#v", encoded, acc)
	}
	copy := acc.Usage()
	*copy.InputTokens = 999
	if got := *acc.Usage().InputTokens; got != 3 {
		t.Fatalf("caller mutated accumulator: %d", got)
	}
}

func TestUnknownProtocol(t *testing.T) {
	if _, err := NewUsageAccumulator("unknown"); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("got %v", err)
	}
	if _, err := ParseUsage("unknown", []byte(`{}`)); !errors.Is(err, ErrInvalidUsage) {
		t.Fatalf("got %v", err)
	}
}

func mustAccumulator(t *testing.T, protocol UsageProtocol) *UsageAccumulator {
	t.Helper()
	acc, err := NewUsageAccumulator(protocol)
	if err != nil {
		t.Fatal(err)
	}
	return acc
}

func observe(t *testing.T, acc *UsageAccumulator, payload string) {
	t.Helper()
	if err := acc.Observe([]byte(payload)); err != nil {
		t.Fatalf("Observe: %v", err)
	}
}

func usageValues(input, output, cacheRead, cacheWrite int64) Usage {
	return Usage{
		InputTokens:      testInt64(input),
		OutputTokens:     testInt64(output),
		CacheReadTokens:  testInt64(cacheRead),
		CacheWriteTokens: testInt64(cacheWrite),
	}
}

func testInt64(value int64) *int64 {
	return &value
}

func assertUsageEqual(t *testing.T, got, want Usage) {
	t.Helper()
	assertOptionalInt64(t, "input", got.InputTokens, want.InputTokens)
	assertOptionalInt64(t, "output", got.OutputTokens, want.OutputTokens)
	assertOptionalInt64(t, "cache read", got.CacheReadTokens, want.CacheReadTokens)
	assertOptionalInt64(t, "cache write", got.CacheWriteTokens, want.CacheWriteTokens)
}

func assertOptionalInt64(t *testing.T, name string, got, want *int64) {
	t.Helper()
	if got == nil || want == nil {
		if got != nil || want != nil {
			t.Fatalf("%s: got %v, want %v", name, optionalString(got), optionalString(want))
		}
		return
	}
	if *got != *want {
		t.Fatalf("%s: got %d, want %d", name, *got, *want)
	}
}

func optionalString(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
