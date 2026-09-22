package membership

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCodexResponsesFunctionToolRoundTripAndNativeEvents(t *testing.T) {
	var received map[string]json.RawMessage
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/backend-api/codex/responses" || r.Header.Get("Authorization") == "" || r.Header.Get("ChatGPT-Account-ID") != testAccountIDMarker {
			t.Fatalf("unexpected request destination or auth headers")
		}
		data, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(data, &received); err != nil {
			t.Fatalf("request JSON: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"city\\\":\"}\r\n\r\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"call_1\",\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}}\r\n\r\n")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\r\ndata: \"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"status\":\"completed\",\"output\":[{\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"weather\",\"arguments\":\"{\\\"city\\\":\\\"Paris\\\"}\"}]}}\r\n\r\n")
	}))
	defer server.Close()
	adapter, credential := testAdapterForServer(t, server, time.Second)
	body := []byte(`{"model":"gpt-test","instructions":"Use tools","input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"Be exact"}]},{"type":"reasoning","id":"rs_1","encrypted_content":"opaque","summary":[]},{"type":"function_call","call_id":"prior_call","name":"weather","arguments":"{\"city\":\"Rome\"}"},{"type":"function_call_output","call_id":"prior_call","output":"sunny"}],"tools":[{"type":"function","name":"weather","description":"Get weather","parameters":{"type":"object"},"strict":true}],"tool_choice":{"type":"function","name":"weather"},"parallel_tool_calls":true,"reasoning":{"effort":"high","summary":"auto"},"text":{"verbosity":"low"},"include":["reasoning.encrypted_content"],"stream":false}`)
	var events []json.RawMessage
	final, err := adapter.Responses(context.Background(), credential, body, func(event json.RawMessage) error {
		events = append(events, append(json.RawMessage(nil), event...))
		return nil
	})
	if err != nil {
		t.Fatalf("Responses: %v", err)
	}
	if len(events) != 3 || !strings.Contains(string(events[1]), `"call_id":"call_1"`) || !strings.Contains(string(final), `"call_id":"call_1"`) {
		t.Fatalf("events/final did not preserve function call: %s / %s", events, final)
	}
	var stream, store bool
	if json.Unmarshal(received["stream"], &stream) != nil || !stream || json.Unmarshal(received["store"], &store) != nil || store {
		t.Fatalf("adapter did not force stream=true/store=false")
	}
	if !strings.Contains(string(received["input"]), `"encrypted_content":"opaque"`) || !strings.Contains(string(received["input"]), `"output":"sunny"`) {
		t.Fatalf("history lost: %s", received["input"])
	}
}

func TestCodexResponsesStringInputAndDefaults(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(data), `"type":"input_text"`) || !strings.Contains(string(data), `"text":"hello"`) {
			t.Fatalf("string input not normalized: %s", data)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n")
	}))
	defer server.Close()
	adapter, cred := testAdapterForServer(t, server, time.Second)
	if _, err := adapter.Responses(context.Background(), cred, []byte(`{"model":"gpt-test","input":"hello"}`), nil); err != nil {
		t.Fatal(err)
	}
}

func TestCodexResponsesNormalizesEasyMessagesAndEmptyToolOutput(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		wire := string(data)
		for _, marker := range []string{`"type":"message"`, `"type":"input_text"`, `"type":"output_text"`, `"annotations":[]`, `"output":""`} {
			if !strings.Contains(wire, marker) {
				t.Fatalf("normalized request missing %s: %s", marker, wire)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"id":"r","object":"response","status":"completed","output":[]}}`+"\n\n")
	}))
	defer server.Close()
	adapter, cred := testAdapterForServer(t, server, time.Second)
	body := []byte(`{"model":"m","input":[{"role":"user","content":"hello"},{"role":"assistant","content":[{"type":"output_text","text":"calling","annotations":[]}]},{"type":"function_call_output","call_id":"c","output":""}]}`)
	if _, err := adapter.Responses(context.Background(), cred, body, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCodexResponsesRejectsUnsupportedAndInvalidShapesBeforeNetwork(t *testing.T) {
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	adapter, cred := testAdapterForServer(t, server, time.Second)
	tests := []struct {
		name, body string
		code       CodexAdapterErrorCode
	}{
		{"unknown", `{"model":"m","input":"x","temperature":1}`, CodexErrorUnsupportedFeature},
		{"store", `{"model":"m","input":"x","store":true}`, CodexErrorUnsupportedFeature},
		{"hosted tool", `{"model":"m","input":"x","tools":[{"type":"web_search"}]}`, CodexErrorUnsupportedFeature},
		{"image", `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"x"}]}]}`, CodexErrorUnsupportedFeature},
		{"custom tool choice", `{"model":"m","input":"x","tool_choice":{"type":"custom","name":"x"}}`, CodexErrorUnsupportedFeature},
		{"previous response", `{"model":"m","input":"x","previous_response_id":"r"}`, CodexErrorUnsupportedFeature},
		{"reasoning without encrypted", `{"model":"m","input":[{"type":"reasoning","summary":[]}]}`, CodexErrorUnsupportedFeature},
		{"bad stream", `{"model":"m","input":"x","stream":"true"}`, CodexErrorInvalidRequest},
		{"null stream", `{"model":"m","input":"x","stream":null}`, CodexErrorInvalidRequest},
		{"null store", `{"model":"m","input":"x","store":null}`, CodexErrorInvalidRequest},
		{"null parallel", `{"model":"m","input":"x","parallel_tool_calls":null}`, CodexErrorInvalidRequest},
		{"null instructions", `{"model":"m","input":"x","instructions":null}`, CodexErrorInvalidRequest},
		{"null reasoning", `{"model":"m","input":"x","reasoning":null}`, CodexErrorInvalidRequest},
		{"null text", `{"model":"m","input":"x","text":null}`, CodexErrorInvalidRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := adapter.Responses(context.Background(), cred, []byte(tt.body), nil)
			assertCodexAdapterError(t, err, tt.code)
		})
	}
	if calls != 0 {
		t.Fatalf("made %d network calls", calls)
	}
}

func TestCodexResponsesTerminalAndFramingFailures(t *testing.T) {
	tests := []struct {
		name, stream string
		code         CodexAdapterErrorCode
	}{
		{"failed", `data: {"type":"response.failed","response":{"error":{"code":"server_error","message":"secret"}}}` + "\n\n", CodexErrorUpstream},
		{"incomplete", `data: {"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}` + "\n\n", CodexErrorUpstream},
		{"error", `data: {"type":"error","message":"secret"}` + "\n\n", CodexErrorUpstream},
		{"eof", `data: {"type":"response.output_text.delta","delta":"partial"}` + "\n\n", CodexErrorProtocol},
		{"done", `data: [DONE]` + "\n\n", CodexErrorProtocol},
		{"null completed response", `data: {"type":"response.completed","response":null}` + "\n\n", CodexErrorProtocol},
		{"missing output", `data: {"type":"response.completed","response":{"id":"r","object":"response","status":"completed"}}` + "\n\n", CodexErrorProtocol},
		{"wrong status", `data: {"type":"response.completed","response":{"id":"r","object":"response","status":"incomplete","output":[]}}` + "\n\n", CodexErrorProtocol},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			adapter, cred, closeFn := testCodexSSEAdapter(t, tt.stream)
			defer closeFn()
			var callbacks int
			_, err := adapter.Responses(context.Background(), cred, []byte(`{"model":"m","input":"x"}`), func(json.RawMessage) error { callbacks++; return nil })
			assertCodexAdapterError(t, err, tt.code)
			if callbacks != 0 && tt.name != "eof" {
				t.Fatalf("terminal failure leaked to callback")
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("raw upstream error leaked")
			}
		})
	}
}

func TestCodexResponsesConsumerCancellationAndBounds(t *testing.T) {
	stream := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\"}}\n\n" + "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"object\":\"response\",\"status\":\"completed\",\"output\":[]}}\n\n"
	adapter, cred, closeFn := testCodexSSEAdapter(t, stream)
	defer closeFn()
	_, err := adapter.Responses(context.Background(), cred, []byte(`{"model":"m","input":"x"}`), func(json.RawMessage) error { return errors.New("stop") })
	assertCodexAdapterError(t, err, CodexErrorEventConsumerStopped)
	adapter2, cred2, closeFn2 := testCodexSSEAdapter(t, "data: "+strings.Repeat("x", 256)+"\n\n")
	defer closeFn2()
	adapter2.maxLine = 64
	_, err = adapter2.Responses(context.Background(), cred2, []byte(`{"model":"m","input":"x"}`), nil)
	assertCodexAdapterError(t, err, CodexErrorSSEFrameTooLarge)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = adapter.Responses(ctx, cred, []byte(`{"model":"m","input":"x"}`), nil)
	assertCodexAdapterError(t, err, CodexErrorCancelled)
}
