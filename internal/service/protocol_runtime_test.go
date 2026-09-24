package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/protocolconv"
)

func protocolRuntimeFeatures() protocolconv.FeatureSet {
	return protocolconv.Features(
		protocolconv.FeatureText,
		protocolconv.FeatureFunctionDefinitions,
		protocolconv.FeatureFunctionCalls,
		protocolconv.FeatureFunctionResults,
		protocolconv.FeatureSystemInstruction,
		protocolconv.FeatureDeveloperMessage,
		protocolconv.FeatureUsage,
		protocolconv.FeatureFinishReason,
	)
}

func TestProtocolRuntimeChatToResponsesDispatchesOnceAndObservesRawUpstream(t *testing.T) {
	var dispatches atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatches.Add(1)
		body, _ := io.ReadAll(r.Body)
		var root map[string]any
		if json.Unmarshal(body, &root) != nil || root["store"] != false || root["model"] != "actual-model" {
			t.Errorf("request was not converted to Responses: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp_1","object":"response","created_at":1,"model":"actual-model","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3}}`))
	}))
	defer server.Close()

	runtime, err := prepareProtocolRuntime(protocolconv.RouteCapability{
		ClientProtocol: protocolconv.ProtocolOpenAIChat, UpstreamProtocol: protocolconv.ProtocolOpenAIResponses,
		Features: protocolRuntimeFeatures(),
	}, "actual-model", []byte(`{"model":"public-model","messages":[{"role":"user","content":"hello"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	request, err := runtime.newRequest(context.Background(), http.MethodPost, server.URL, http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	var observedProtocol protocolconv.Protocol
	var observedRaw []byte
	result, err := runtime.execute(server.Client(), request, 1<<20, func(protocol protocolconv.Protocol, raw []byte) error {
		observedProtocol = protocol
		observedRaw = append([]byte(nil), raw...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if dispatches.Load() != 1 {
		t.Fatalf("dispatch count = %d", dispatches.Load())
	}
	if observedProtocol != protocolconv.ProtocolOpenAIResponses || len(observedRaw) == 0 || string(observedRaw) == string(result.ClientBody) {
		t.Fatalf("usage observer did not receive raw upstream evidence: protocol=%q raw=%s client=%s", observedProtocol, observedRaw, result.ClientBody)
	}
	var client map[string]any
	_ = json.Unmarshal(result.ClientBody, &client)
	if client["object"] != "chat.completion" || client["model"] != "actual-model" {
		t.Fatalf("employee response was not converted to Chat: %#v", client)
	}
}

func TestProtocolRuntimeResponsesToChatUsesChatWireAndRawUsage(t *testing.T) {
	var dispatches atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatches.Add(1)
		body, _ := io.ReadAll(r.Body)
		var root map[string]any
		if json.Unmarshal(body, &root) != nil || root["model"] != "gpt-actual" || root["messages"] == nil {
			t.Errorf("request was not converted to Chat: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chat_1","object":"chat.completion","created":1,"model":"gpt-actual","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`))
	}))
	defer server.Close()

	runtime, err := prepareProtocolRuntime(protocolconv.RouteCapability{
		ClientProtocol: protocolconv.ProtocolOpenAIResponses, UpstreamProtocol: protocolconv.ProtocolOpenAIChat,
		Features: protocolRuntimeFeatures(),
	}, "gpt-actual", []byte(`{"model":"public-model","input":"hello","max_output_tokens":64}`))
	if err != nil {
		t.Fatal(err)
	}
	request, err := runtime.newRequest(context.Background(), http.MethodPost, server.URL, http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	var observed protocolconv.Protocol
	result, err := runtime.execute(server.Client(), request, 1<<20, func(protocol protocolconv.Protocol, raw []byte) error {
		observed = protocol
		var upstream map[string]any
		if json.Unmarshal(raw, &upstream) != nil || upstream["object"] != "chat.completion" {
			t.Fatalf("observer did not receive raw Chat JSON: %s", raw)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if dispatches.Load() != 1 || observed != protocolconv.ProtocolOpenAIChat {
		t.Fatalf("dispatches=%d observed=%q", dispatches.Load(), observed)
	}
	var client map[string]any
	_ = json.Unmarshal(result.ClientBody, &client)
	if client["object"] != "response" || client["status"] != "completed" {
		t.Fatalf("employee response was not converted to Responses: %#v", client)
	}
}

func TestProtocolRuntimeRejectsUnsupportedBeforeDispatch(t *testing.T) {
	var dispatches atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dispatches.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := prepareProtocolRuntime(protocolconv.RouteCapability{
		ClientProtocol: protocolconv.ProtocolOpenAIResponses, UpstreamProtocol: protocolconv.ProtocolGeminiGenerate,
		Features: protocolRuntimeFeatures(),
	}, "", []byte(`{"model":"m","input":[{"type":"message","role":"developer","content":"ambiguous"}]}`))
	if !errors.Is(err, protocolconv.ErrUnsupportedFeature) {
		t.Fatalf("expected typed pre-dispatch rejection, got %v", err)
	}
	if dispatches.Load() != 0 {
		t.Fatalf("unsupported request dispatched %d times", dispatches.Load())
	}
}

func TestProtocolRuntimeCancellationPropagatesWithoutReplay(t *testing.T) {
	started := make(chan struct{})
	doer := &cancellingProtocolDoer{started: started}

	runtime, err := prepareProtocolRuntime(protocolconv.RouteCapability{
		ClientProtocol: protocolconv.ProtocolOpenAIResponses, UpstreamProtocol: protocolconv.ProtocolOpenAIChat,
		Features: protocolRuntimeFeatures(),
	}, "", []byte(`{"model":"m","input":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request, err := runtime.newRequest(ctx, http.MethodPost, "https://synthetic.invalid/v1/chat/completions", http.Header{"Content-Type": []string{"application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := runtime.execute(doer, request, 1<<20, nil)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream did not receive the request")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context cancellation, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not reach upstream")
	}
	if doer.dispatches.Load() != 1 {
		t.Fatalf("cancelled request was replayed: dispatches=%d", doer.dispatches.Load())
	}
}

type cancellingProtocolDoer struct {
	started    chan struct{}
	dispatches atomic.Int64
}

func (d *cancellingProtocolDoer) Do(request *http.Request) (*http.Response, error) {
	d.dispatches.Add(1)
	close(d.started)
	<-request.Context().Done()
	return nil, request.Context().Err()
}
