package service

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"

	"cpacloud.local/server/internal/accounting"
)

func TestBudgetWireProfileUsesFinalRequestWithoutConsumingBody(t *testing.T) {
	payload := []byte(`{"model":"gpt-4.1-2025-04-14","messages":[{"role":"user","content":"synthetic-only"}],"max_completion_tokens":7,"n":1,"modalities":["text"],"store":false,"stream":false}`)
	r, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Content-Type", "application/json")
	proof, err := proveModelBudgetWire(r, accounting.ProtocolOpenAIChatCompletions, accounting.ProviderOpenAICompatible, "gpt-4.1-2025-04-14")
	if err != nil || proof.ProfileVersion != 1 || proof.UpperUsage.OutputMax != 7 {
		t.Fatalf("proof: %+v %v", proof, err)
	}
	after, err := io.ReadAll(r.Body)
	if err != nil || !bytes.Equal(after, payload) {
		t.Fatal("proof consumed or changed outgoing request")
	}
	if _, err := proveModelBudgetWire(r, accounting.ProtocolOpenAIChatCompletions, accounting.ProviderOpenAICompatible, "public-alias"); !errors.Is(err, accounting.ErrBoundProfileUnsupported) {
		t.Fatal("accepted public alias as actual model")
	}
	r.URL.Host = "api.openai.com.example.invalid"
	if _, err := proveModelBudgetWire(r, accounting.ProtocolOpenAIChatCompletions, accounting.ProviderOpenAICompatible, "gpt-4.1-2025-04-14"); !errors.Is(err, accounting.ErrBoundProfileUnsupported) {
		t.Fatal("accepted disguised endpoint")
	}
}

func TestBudgetWireProfileRejectsUnfrozenBodiesAndProtocols(t *testing.T) {
	for _, protocol := range []accounting.UsageProtocol{accounting.ProtocolOpenAIResponses, accounting.ProtocolAnthropicMessages, accounting.ProtocolGeminiGenerateContent} {
		r, _ := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		r.Header.Set("Content-Type", "application/json")
		if _, err := proveModelBudgetWire(r, protocol, accounting.ProviderOpenAICompatible, "gpt-4.1-2025-04-14"); !errors.Is(err, accounting.ErrBoundProfileUnsupported) {
			t.Fatal("accepted unproven protocol")
		}
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.GetBody = nil },
		func(r *http.Request) { r.Method = "GET" },
		func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") },
		func(r *http.Request) { r.ContentLength = modelMaxBody + 1 },
	} {
		r, _ := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		r.Header.Set("Content-Type", "application/json")
		mutate(r)
		if _, err := proveModelBudgetWire(r, accounting.ProtocolOpenAIChatCompletions, accounting.ProviderOpenAICompatible, "gpt-4.1-2025-04-14"); err == nil {
			t.Fatal("accepted unfrozen payload")
		}
	}
}
