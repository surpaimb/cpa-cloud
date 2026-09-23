package accounting

import (
	"errors"
	"testing"
)

func TestGPT41SnapshotBillingClassification(t *testing.T) {
	data := []byte(`{"model":"gpt-4.1-2025-04-14","usage":{"prompt_tokens":120,"completion_tokens":7,"total_tokens":127,"prompt_tokens_details":{"cached_tokens":20}}}`)
	profile := NewGPT41SnapshotUsageAccumulator()
	if err := profile.Observe(data); err != nil {
		t.Fatal(err)
	}
	u := profile.Usage()
	if u.InputTokens == nil || *u.InputTokens != 100 || u.OutputTokens == nil || *u.OutputTokens != 7 || u.CacheReadTokens == nil || *u.CacheReadTokens != 20 || u.CacheWriteTokens == nil || *u.CacheWriteTokens != 0 {
		t.Fatalf("incorrect normalized usage: %+v", u)
	}
	generic, err := ParseUsage(ProtocolOpenAIChatCompletions, data)
	if err != nil {
		t.Fatal(err)
	}
	if generic.InputTokens != nil || generic.CacheWriteTokens != nil {
		t.Fatal("profile changed the generic parser")
	}
	*u.InputTokens = 999
	if *profile.Usage().InputTokens != 100 {
		t.Fatal("usage copy aliases accumulator")
	}
}

func TestGPT41SnapshotUnknownCountersStayUnknown(t *testing.T) {
	for _, usage := range []string{
		`{"prompt_tokens":120,"completion_tokens":7}`,
		`{"prompt_tokens":120,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":null}}`,
		`{"prompt_tokens":120,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":null}}`,
	} {
		a := NewGPT41SnapshotUsageAccumulator()
		if err := a.Observe([]byte(`{"model":"gpt-4.1-2025-04-14","usage":` + usage + `}`)); err != nil {
			t.Fatal(err)
		}
		if a.Usage().InputTokens != nil {
			t.Fatal("incomplete usage became known")
		}
	}
}

func TestGPT41SnapshotInvalidMetadataPoisons(t *testing.T) {
	for name, data := range map[string]string{
		"missing model":        `{"usage":{"prompt_tokens":1}}`,
		"alias":                `{"model":"gpt-4.1","usage":{"prompt_tokens":1}}`,
		"duplicate model":      `{"model":"gpt-4.1","model":"gpt-4.1-2025-04-14"}`,
		"duplicate count":      `{"model":"gpt-4.1-2025-04-14","usage":{"prompt_tokens":1,"prompt_tokens":2}}`,
		"duplicate detail":     `{"model":"gpt-4.1-2025-04-14","usage":{"prompt_tokens_details":{"cached_tokens":1,"cached_tokens":0}}}`,
		"cache write conflict": `{"model":"gpt-4.1-2025-04-14","usage":{"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":1}}}`,
		"reasoning conflict":   `{"model":"gpt-4.1-2025-04-14","usage":{"completion_tokens_details":{"reasoning_tokens":1}}}`,
		"total mismatch":       `{"model":"gpt-4.1-2025-04-14","usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":2}}`,
		"cache over input":     `{"model":"gpt-4.1-2025-04-14","usage":{"prompt_tokens":1,"prompt_tokens_details":{"cached_tokens":2}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			a := NewGPT41SnapshotUsageAccumulator()
			if err := a.Observe([]byte(data)); !errors.Is(err, ErrInvalidUsage) {
				t.Fatalf("got %v", err)
			}
			if a.Usage().InputTokens != nil || a.Usage().OutputTokens != nil || a.Usage().CacheReadTokens != nil || a.Usage().CacheWriteTokens != nil {
				t.Fatal("invalid metadata retained partial values")
			}
			if err := a.Observe([]byte(`{"model":"gpt-4.1-2025-04-14"}`)); !errors.Is(err, ErrInvalidUsage) {
				t.Fatal("invalid result was repaired")
			}
		})
	}
}
