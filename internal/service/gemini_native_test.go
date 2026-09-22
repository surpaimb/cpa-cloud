package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGeminiNativeAPIKeyWorkflowStreamingDiscoveryAndRestart(t *testing.T) {
	var requestCount atomic.Int32
	var currentKey atomic.Value
	currentKey.Store("gemini-upstream-secret")
	streamCancelled := make(chan struct{}, 1)
	var receivedMu sync.Mutex
	var received map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-goog-api-key"); got != currentKey.Load().(string) {
			t.Errorf("Gemini API key=%q", got)
		}
		for _, forbidden := range []string{"Authorization", "Cookie", "Origin", "X-CSRF-Token"} {
			if got := r.Header.Get(forbidden); got != "" {
				t.Errorf("employee/admin header %s reached upstream: %q", forbidden, got)
			}
		}
		if r.Method == http.MethodGet {
			if r.URL.Path != "/v1beta/models" || r.URL.Query().Get("pageSize") != "1000" {
				t.Errorf("discovery target=%s", r.URL.String())
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"models":[{"name":"models/z-model","supportedGenerationMethods":["generateContent"]},{"name":"models/embed-only","supportedGenerationMethods":["embedContent"]},{"name":"models/a-model","supportedGenerationMethods":["generateContent","countTokens"]},{"name":"models/z-model","supportedGenerationMethods":["generateContent"]}]}`)
			return
		}
		requestCount.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Gemini body: %v", err)
		}
		receivedMu.Lock()
		received = payload
		receivedMu.Unlock()
		if strings.Contains(r.URL.Path, ":streamGenerateContent") {
			if r.URL.Path != "/v1beta/models/provider-model:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
				t.Errorf("stream target=%s", r.URL.String())
			}
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"first\"}]}}]}\n\n")
			flusher.Flush()
			if bytes.Contains(mustMarshal(payload), []byte("cancel-stream")) {
				<-r.Context().Done()
				streamCancelled <- struct{}{}
				return
			}
			_, _ = io.WriteString(w, "data: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1,\"totalTokenCount\":3}}\n\n")
			flusher.Flush()
			return
		}
		if r.URL.Path != "/v1beta/models/provider-model:generateContent" || r.URL.RawQuery != "" {
			t.Errorf("generate target=%s", r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"weather","args":{"city":"Paris"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":9,"candidatesTokenCount":3,"totalTokenCount":12}}`)
	}))
	defer upstream.Close()

	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)

	upstreamResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams",
		`{"name":"Gemini","provider_kind":"gemini-api-key","endpoint":`+quoteJSON(upstream.URL)+`,"api_key":"gemini-upstream-secret"}`,
		cookie, csrf, server.URL)
	if upstreamResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create Gemini upstream status=%d body=%s", upstreamResponse.StatusCode, readBody(upstreamResponse))
	}
	var upstreamObject upstreamView
	decodeResponse(t, upstreamResponse, &upstreamObject)
	if upstreamObject.ProviderKind != geminiAPIKeyProvider || upstreamObject.Endpoint != upstream.URL {
		t.Fatalf("Gemini upstream=%+v", upstreamObject)
	}

	var ciphertext []byte
	var keyVersion int
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext,key_version FROM upstreams WHERE id=?`, upstreamObject.ID).Scan(&ciphertext, &keyVersion); err != nil {
		t.Fatal(err)
	}
	if keyVersion != 2 {
		t.Fatalf("Gemini key version=%d", keyVersion)
	}
	if _, err := app.secrets.decryptCredential(upstreamObject.ID, ciphertext); err == nil {
		t.Fatal("Gemini credential decrypted with OpenAI-compatible AEAD purpose")
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		stored, err := os.ReadFile(filepath.Join(dataDir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(stored, []byte("gemini-upstream-secret")) {
			t.Fatalf("Gemini API key was stored in plaintext in %s", entry.Name())
		}
	}

	discovery := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstreamObject.ID+"/discover-models", "", cookie, csrf, server.URL)
	if discovery.StatusCode != http.StatusOK {
		t.Fatalf("Gemini discovery status=%d body=%s", discovery.StatusCode, readBody(discovery))
	}
	var discovered struct {
		Items []discoveredModel `json:"items"`
	}
	decodeResponse(t, discovery, &discovered)
	if want := []discoveredModel{{ID: "a-model"}, {ID: "z-model"}}; !reflect.DeepEqual(discovered.Items, want) {
		t.Fatalf("discovered=%v want=%v", discovered.Items, want)
	}

	modelResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models",
		`{"id":"company-gemini","upstream_id":`+quoteJSON(upstreamObject.ID)+`,"upstream_model":"provider-model"}`,
		cookie, csrf, server.URL)
	if modelResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create model status=%d body=%s", modelResponse.StatusCode, readBody(modelResponse))
	}
	modelResponse.Body.Close()
	employeeResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees", `{"name":"Gemini user"}`, cookie, csrf, server.URL)
	if employeeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create employee status=%d body=%s", employeeResponse.StatusCode, readBody(employeeResponse))
	}
	var employeeObject employee
	decodeResponse(t, employeeResponse, &employeeObject)
	key := createTestKey(t, server.URL, employeeObject.ID, "gemini-key-op", cookie, csrf)

	models := employeeRequest(t, http.MethodGet, server.URL+"/v1beta/models?pageSize=1", "", key.Key, context.Background())
	if models.StatusCode != http.StatusOK {
		t.Fatalf("native model list status=%d body=%s", models.StatusCode, readBody(models))
	}
	var modelList struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	decodeResponse(t, models, &modelList)
	if len(modelList.Models) != 1 || modelList.Models[0].Name != "models/company-gemini" {
		t.Fatalf("native models=%+v", modelList.Models)
	}

	requestBody := `{"contents":[{"role":"user","parts":[{"text":"weather"}]},{"role":"model","parts":[{"functionCall":{"id":"call-1","name":"weather","args":{"city":"Paris"}}}]},{"role":"user","parts":[{"functionResponse":{"id":"call-1","name":"weather","response":{"temperature":21}}}]}],"systemInstruction":{"parts":[{"text":"Be concise."}]},"tools":[{"functionDeclarations":[{"name":"weather","description":"Get weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}]}],"toolConfig":{"functionCallingConfig":{"mode":"AUTO","allowedFunctionNames":["weather"]}},"generationConfig":{"candidateCount":1,"maxOutputTokens":128,"temperature":0.2,"topP":0.9,"topK":20,"stopSequences":["done"],"seed":7,"presencePenalty":0,"frequencyPenalty":0,"responseMimeType":"application/json","responseSchema":{"type":"object"}},"safetySettings":[]}`
	generated := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:generateContent", requestBody, key.Key, context.Background())
	if generated.StatusCode != http.StatusOK {
		t.Fatalf("generate status=%d body=%s", generated.StatusCode, readBody(generated))
	}
	generatedBody := readBody(generated)
	for _, expected := range []string{`"finishReason":"STOP"`, `"promptTokenCount":9`, `"functionCall"`} {
		if !strings.Contains(generatedBody, expected) {
			t.Fatalf("native response omitted %s: %s", expected, generatedBody)
		}
	}
	receivedMu.Lock()
	forwarded := mustMarshal(received)
	receivedMu.Unlock()
	for _, expected := range []string{`"systemInstruction"`, `"functionDeclarations"`, `"functionCall"`, `"functionResponse"`, `"generationConfig"`} {
		if !bytes.Contains(forwarded, []byte(expected)) {
			t.Fatalf("forwarded request omitted %s: %s", expected, forwarded)
		}
	}

	stream := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:streamGenerateContent?alt=sse",
		`{"contents":[{"parts":[{"text":"stream"}]}]}`, key.Key, context.Background())
	if stream.StatusCode != http.StatusOK || !strings.HasPrefix(stream.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream status=%d type=%q body=%s", stream.StatusCode, stream.Header.Get("Content-Type"), readBody(stream))
	}
	streamBody := readBody(stream)
	if !strings.Contains(streamBody, `"text":"first"`) || !strings.Contains(streamBody, `"totalTokenCount":3`) || strings.Contains(streamBody, "[DONE]") {
		t.Fatalf("native stream=%s", streamBody)
	}

	newKey := "gemini-upstream-secret-2"
	update := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/upstreams/"+upstreamObject.ID,
		`{"expected_revision":1,"api_key":`+quoteJSON(newKey)+`}`, cookie, csrf, server.URL)
	if update.StatusCode != http.StatusOK {
		t.Fatalf("replace Gemini key status=%d body=%s", update.StatusCode, readBody(update))
	}
	update.Body.Close()
	currentKey.Store(newKey)

	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app = openTestApp(t, dataDir)
	defer app.Close()
	server = httptest.NewServer(app.Handler())
	defer server.Close()
	restarted := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:generateContent",
		`{"contents":[{"parts":[{"text":"after restart"}]}]}`, key.Key, context.Background())
	if restarted.StatusCode != http.StatusOK {
		t.Fatalf("restart status=%d body=%s", restarted.StatusCode, readBody(restarted))
	}
	restarted.Body.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancelledResponse := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:streamGenerateContent",
		`{"contents":[{"parts":[{"text":"cancel-stream"}]}]}`, key.Key, ctx)
	cancelledRequestID := cancelledResponse.Header.Get("X-Request-ID")
	buffer := make([]byte, 256)
	if n, err := cancelledResponse.Body.Read(buffer); err != nil || !bytes.Contains(buffer[:n], []byte("data:")) {
		t.Fatalf("cancel stream first event n=%d err=%v", n, err)
	}
	cancel()
	cancelledResponse.Body.Close()
	select {
	case <-streamCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("Gemini client cancellation did not reach upstream")
	}
	if cancelledOutcome := waitGeminiRequestOutcome(t, app, cancelledRequestID); cancelledOutcome != "cancelled" {
		t.Fatalf("cancelled request outcome=%q", cancelledOutcome)
	}
	if requestCount.Load() < 4 {
		t.Fatalf("Gemini request count=%d", requestCount.Load())
	}
}

func TestGeminiNativeAuthorizationValidationAndErrorRedaction(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload map[string]any
		_ = json.NewDecoder(r.Body).Decode(&payload)
		encoded := mustMarshal(payload)
		if strings.Contains(r.URL.Path, ":streamGenerateContent") && bytes.Contains(encoded, []byte("bad-stream")) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: provider-private-invalid-event\n\n")
			return
		}
		if bytes.Contains(encoded, []byte("rate-limit")) {
			w.Header().Set("Retry-After", "15")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "provider-private-error gemini-secret")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[]}`)
	}))
	defer upstream.Close()
	server, app, cookie, csrf, upstreamObject, employeeObject, key := setupGeminiTest(t, upstream.URL)
	defer server.Close()
	defer app.Close()

	unauthorized := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:generateContent",
		`{"contents":[{"parts":[{"text":"hello"}]}]}`, "bad-key", context.Background())
	if unauthorized.StatusCode != http.StatusUnauthorized || !strings.Contains(readBody(unauthorized), `"status":"UNAUTHENTICATED"`) {
		t.Fatal("invalid employee key did not return Gemini authentication error")
	}
	unsupported := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:generateContent",
		`{"contents":[{"parts":[{"text":"hello"}]}],"cachedContent":"cachedContents/private"}`, key.Key, context.Background())
	if unsupported.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsupported field status=%d body=%s", unsupported.StatusCode, readBody(unsupported))
	}
	if body := readBody(unsupported); !strings.Contains(body, `"status":"UNIMPLEMENTED"`) {
		t.Fatalf("unsupported field error=%s", body)
	}
	if calls.Load() != 0 {
		t.Fatal("invalid requests reached Gemini upstream")
	}
	badGeneration := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:generateContent",
		`{"contents":[{"parts":[{"text":"hello"}]}],"generationConfig":{"temperature":"hot"}}`, key.Key, context.Background())
	if badGeneration.StatusCode != http.StatusBadRequest || !strings.Contains(readBody(badGeneration), `"status":"INVALID_ARGUMENT"`) {
		t.Fatal("invalid generation config did not return a client error")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid generation config reached Gemini upstream")
	}
	badStream := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:streamGenerateContent",
		`{"contents":[{"parts":[{"text":"bad-stream"}]}]}`, key.Key, context.Background())
	badStreamBody := readBody(badStream)
	if badStream.StatusCode != http.StatusBadGateway || !strings.Contains(badStreamBody, `"status":"UNAVAILABLE"`) || strings.Contains(badStreamBody, "provider-private") {
		t.Fatalf("malformed stream status=%d body=%s", badStream.StatusCode, badStreamBody)
	}

	rateLimited := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:generateContent",
		`{"contents":[{"parts":[{"text":"rate-limit"}]}]}`, key.Key, context.Background())
	if rateLimited.StatusCode != http.StatusTooManyRequests || rateLimited.Header.Get("Retry-After") != "15" {
		t.Fatalf("rate limit status=%d retry=%q body=%s", rateLimited.StatusCode, rateLimited.Header.Get("Retry-After"), readBody(rateLimited))
	}
	rateBody := readBody(rateLimited)
	if strings.Contains(rateBody, "provider-private-error") || strings.Contains(rateBody, "gemini-secret") || !strings.Contains(rateBody, `"status":"RESOURCE_EXHAUSTED"`) {
		t.Fatalf("rate limit response leaked upstream details: %s", rateBody)
	}

	policy := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/employees/"+employeeObject.ID+"/model-policy",
		`{"expected_revision":1,"mode":"selected","models":[]}`, cookie, csrf, server.URL)
	if policy.StatusCode != http.StatusOK {
		t.Fatalf("restrict policy status=%d body=%s", policy.StatusCode, readBody(policy))
	}
	policy.Body.Close()
	forbidden := employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/company-gemini:generateContent",
		`{"contents":[{"parts":[{"text":"hello"}]}]}`, key.Key, context.Background())
	if forbidden.StatusCode != http.StatusForbidden || !strings.Contains(readBody(forbidden), `"status":"PERMISSION_DENIED"`) {
		t.Fatal("model policy did not deny Gemini route")
	}
	disable := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/employees/"+employeeObject.ID,
		`{"expected_revision":2,"status":"disabled"}`, cookie, csrf, server.URL)
	if disable.StatusCode != http.StatusOK {
		t.Fatalf("disable employee status=%d body=%s", disable.StatusCode, readBody(disable))
	}
	disable.Body.Close()
	disabled := employeeRequest(t, http.MethodGet, server.URL+"/v1beta/models", "", key.Key, context.Background())
	if disabled.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled employee model list status=%d", disabled.StatusCode)
	}
	disabled.Body.Close()
	reenable := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/employees/"+employeeObject.ID,
		`{"expected_revision":3,"status":"active"}`, cookie, csrf, server.URL)
	if reenable.StatusCode != http.StatusOK {
		t.Fatalf("re-enable employee status=%d body=%s", reenable.StatusCode, readBody(reenable))
	}
	reenable.Body.Close()

	revoke := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/keys/"+key.ID+"/revoke", `{}`, cookie, csrf, server.URL)
	if revoke.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", revoke.StatusCode, readBody(revoke))
	}
	revoke.Body.Close()
	revoked := employeeRequest(t, http.MethodGet, server.URL+"/v1beta/models", "", key.Key, context.Background())
	if revoked.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key model list status=%d", revoked.StatusCode)
	}
	revoked.Body.Close()

	var provider string
	if err := app.store.db.QueryRow(`SELECT provider_kind FROM upstreams WHERE id=?`, upstreamObject.ID).Scan(&provider); err != nil || provider != geminiAPIKeyProvider {
		t.Fatalf("stored provider=%q err=%v", provider, err)
	}
}

func TestGeminiEndpointPolicyAndDiscoveryPagination(t *testing.T) {
	if got, err := validateGeminiEndpoint(context.Background(), "", false); err != nil || got != geminiAPIEndpoint {
		t.Fatalf("default Gemini endpoint=%q err=%v", got, err)
	}
	for _, endpoint := range []string{
		"https://example.com",
		"http://generativelanguage.googleapis.com",
		"https://generativelanguage.googleapis.com/v1beta",
		"https://user@example.com",
	} {
		if _, err := validateGeminiEndpoint(context.Background(), endpoint, false); err == nil {
			t.Fatalf("unsafe Gemini endpoint accepted: %s", endpoint)
		}
	}
	if _, err := parseGeminiDiscoveredModels([]byte(`{"models":[],"nextPageToken":"secret-page"}`)); err == nil {
		t.Fatal("paginated discovery was silently truncated")
	}
}

func setupGeminiTest(t *testing.T, upstreamURL string) (*httptest.Server, *App, *http.Cookie, string, upstreamView, employee, keyView) {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)
	upstreamResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams",
		`{"name":"Gemini","provider_kind":"gemini-api-key","endpoint":`+quoteJSON(upstreamURL)+`,"api_key":"gemini-secret"}`,
		cookie, csrf, server.URL)
	if upstreamResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create upstream status=%d body=%s", upstreamResponse.StatusCode, readBody(upstreamResponse))
	}
	var upstream upstreamView
	decodeResponse(t, upstreamResponse, &upstream)
	modelResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models",
		`{"id":"company-gemini","upstream_id":`+quoteJSON(upstream.ID)+`,"upstream_model":"provider-model"}`,
		cookie, csrf, server.URL)
	if modelResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create model status=%d body=%s", modelResponse.StatusCode, readBody(modelResponse))
	}
	modelResponse.Body.Close()
	employeeResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees", `{"name":"Gemini user"}`, cookie, csrf, server.URL)
	if employeeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create employee status=%d body=%s", employeeResponse.StatusCode, readBody(employeeResponse))
	}
	var employee employee
	decodeResponse(t, employeeResponse, &employee)
	key := createTestKey(t, server.URL, employee.ID, "gemini-op", cookie, csrf)
	return server, app, cookie, csrf, upstream, employee, key
}

func mustMarshal(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

func waitGeminiRequestOutcome(t *testing.T, app *App, requestID string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var outcome string
		if err := app.store.db.QueryRow(`SELECT outcome FROM model_requests WHERE id=?`, requestID).Scan(&outcome); err == nil && outcome != "running" {
			return outcome
		}
		if time.Now().After(deadline) {
			return "running"
		}
		time.Sleep(20 * time.Millisecond)
	}
}
