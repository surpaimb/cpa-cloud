// Independently authored acceptance tests for the explicit Gemini/Responses
// converted streaming routes.
package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestExplicitWireGeminiToResponsesStreamUsesConvertedRuntime(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/responses" || r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Errorf("target=%s authorization=%q", r.URL.String(), r.Header.Get("Authorization"))
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode converted request: %v", err)
		}
		var stream bool
		if json.Unmarshal(request["stream"], &stream) != nil || !stream {
			t.Errorf("converted Responses request did not enable streaming: %s", request["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r_gemini_client\",\"created_at\":1,\"model\":\"actual-model\"}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"sequence_number\":1,\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"status\":\"in_progress\",\"role\":\"assistant\",\"content\":[]}}\n\n")
		_, _ = io.WriteString(w, "event: response.content_part.added\ndata: {\"type\":\"response.content_part.added\",\"sequence_number\":2,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"\",\"annotations\":[]}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"sequence_number\":3,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"delta\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "event: response.output_text.done\ndata: {\"type\":\"response.output_text.done\",\"sequence_number\":4,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"text\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "event: response.content_part.done\ndata: {\"type\":\"response.content_part.done\",\"sequence_number\":5,\"item_id\":\"msg\",\"output_index\":0,\"content_index\":0,\"part\":{\"type\":\"output_text\",\"text\":\"hello\",\"annotations\":[]}}\n\n")
		_, _ = io.WriteString(w, "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":6,\"output_index\":0,\"item\":{\"id\":\"msg\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\",\"annotations\":[]}]}}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":7,\"response\":{\"id\":\"r_gemini_client\",\"object\":\"response\",\"created_at\":1,\"model\":\"actual-model\",\"status\":\"completed\",\"output\":[{\"id\":\"msg\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\",\"annotations\":[]}]}],\"usage\":{\"input_tokens\":4,\"output_tokens\":2,\"total_tokens\":6}}}\n\n")
	}))

	response := geminiEmployeeKeyRequest(t, http.MethodPost, fixture.server.URL+"/v1beta/models/wire-model:streamGenerateContent?alt=sse", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, fixture.key.Key, context.Background())
	body := readBody(response)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"text":"hello"`) || !strings.Contains(body, `"finishReason":"STOP"`) || strings.Count(body, `"text":"hello"`) != 1 {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if calls.Load() != 1 {
		t.Fatalf("Gemini-to-Responses stream dispatched %d upstream requests", calls.Load())
	}
	assertSingleWireAttempt(t, fixture.app, "openai-responses")
	assertSingleWireUsage(t, fixture.app, int64Ptr(4), int64Ptr(2))
}

func TestExplicitWireResponsesToGeminiStreamBuffersLateIdentityWithoutReplay(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitProviderWireFixture(t, geminiAPIKeyProvider, string(wireProtocolGemini), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/models/actual-model:streamGenerateContent") || r.URL.Query().Get("alt") != "sse" || r.Header.Get("x-goog-api-key") != "upstream-secret" {
			t.Errorf("target=%s key=%q", r.URL.String(), r.Header.Get("x-goog-api-key"))
		}
		var request map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode converted request: %v", err)
		}
		if _, present := request["stream"]; present {
			t.Errorf("converted Gemini request invented a stream field: %#v", request)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"first\"}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"responseId\":\"g_late\",\"modelVersion\":\"actual-model\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"second\"}]}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"responseId\":\"g_late\",\"modelVersion\":\"actual-model\",\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":2,\"totalTokenCount\":5}}\n\n")
	}))

	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"wire-model","stream":true,"input":"hi"}`, fixture.key.Key, context.Background())
	body := readBody(response)
	response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(body, "response.created") || !strings.Contains(body, "response.completed") || strings.Count(body, `"delta":"first"`) != 1 || strings.Count(body, `"delta":"second"`) != 1 {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if strings.Index(body, `"delta":"first"`) > strings.Index(body, `"delta":"second"`) {
		t.Fatalf("late identity changed semantic order: %s", body)
	}
	if calls.Load() != 1 {
		t.Fatalf("Responses-to-Gemini stream dispatched %d upstream requests", calls.Load())
	}
	assertSingleWireAttempt(t, fixture.app, "gemini-generate-content")
	assertSingleWireUsage(t, fixture.app, int64Ptr(3), int64Ptr(2))
}
