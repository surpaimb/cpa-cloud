package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviewWorkflowPersistenceStreamingAndRevocation(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	var employeeCredentialSeen atomic.Bool
	streamCancelled := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected upstream path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-secret" {
			employeeCredentialSeen.Store(true)
			t.Errorf("unexpected upstream authorization %q", got)
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		var model string
		_ = json.Unmarshal(payload["model"], &model)
		if model != "provider-model" {
			t.Errorf("model mapping = %q", model)
		}
		if _, ok := payload["tools"]; !ok {
			t.Error("tools field was not preserved")
		}
		var stream bool
		_ = json.Unmarshal(payload["stream"], &stream)
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			flusher := w.(http.Flusher)
			_, _ = io.WriteString(w, "data: {\"id\":\"first\"}\n\n")
			flusher.Flush()
			<-r.Context().Done()
			streamCancelled <- struct{}{}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat-1","object":"chat.completion"}`)
	}))
	defer upstream.Close()

	app := openTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)

	// Authenticated mutations require both a session cookie and a same-origin CSRF proof.
	bad := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees", `{"name":"No CSRF"}`, cookie, "", "")
	if bad.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", bad.StatusCode)
	}
	bad.Body.Close()

	upstreamResult := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams",
		`{"name":"local mock","provider_kind":"openai-compatible","endpoint":`+quoteJSON(upstream.URL)+`,"api_key":"upstream-secret"}`,
		cookie, csrf, server.URL)
	if upstreamResult.StatusCode != http.StatusCreated {
		t.Fatalf("create upstream status=%d body=%s", upstreamResult.StatusCode, readBody(upstreamResult))
	}
	var upstreamObject upstreamView
	decodeResponse(t, upstreamResult, &upstreamObject)

	modelResult := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models",
		`{"id":"company-model","upstream_id":`+quoteJSON(upstreamObject.ID)+`,"upstream_model":"provider-model"}`,
		cookie, csrf, server.URL)
	if modelResult.StatusCode != http.StatusCreated {
		t.Fatalf("create model status=%d body=%s", modelResult.StatusCode, readBody(modelResult))
	}
	modelResult.Body.Close()

	employeeResult := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees",
		`{"name":"Ada","department":"Engineering","note":"preview"}`, cookie, csrf, server.URL)
	if employeeResult.StatusCode != http.StatusCreated {
		t.Fatalf("create employee status=%d body=%s", employeeResult.StatusCode, readBody(employeeResult))
	}
	var employeeObject employee
	decodeResponse(t, employeeResult, &employeeObject)

	listResult := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/employees", "", cookie, "", "")
	if listResult.StatusCode != http.StatusOK {
		t.Fatalf("list employees status=%d", listResult.StatusCode)
	}
	listResult.Body.Close()

	firstKey := createTestKey(t, server.URL, employeeObject.ID, "first-op", cookie, csrf)
	secondKey := createTestKey(t, server.URL, employeeObject.ID, "second-op", cookie, csrf)
	if firstKey.Key == "" || secondKey.Key == "" {
		t.Fatal("new key plaintext was not returned")
	}
	duplicate := createTestKey(t, server.URL, employeeObject.ID, "first-op", cookie, csrf)
	if duplicate.ID != firstKey.ID || duplicate.Key != "" {
		t.Fatal("idempotent key creation re-exposed or replaced the secret")
	}

	modelsRequest, _ := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
	modelsRequest.Header.Set("Authorization", "Bearer "+firstKey.Key)
	modelsResponse, err := http.DefaultClient.Do(modelsRequest)
	if err != nil || modelsResponse.StatusCode != http.StatusOK {
		t.Fatalf("list models: status=%v err=%v", statusOf(modelsResponse), err)
	}
	modelsResponse.Body.Close()

	chatBody := `{"model":"company-model","messages":[{"role":"user","content":"hello"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}]}`
	chatResponse := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", chatBody, firstKey.Key, context.Background())
	if chatResponse.StatusCode != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", chatResponse.StatusCode, readBody(chatResponse))
	}
	chatResponse.Body.Close()
	if employeeCredentialSeen.Load() {
		t.Fatal("employee credential reached upstream")
	}

	streamCtx, cancel := context.WithCancel(context.Background())
	streamResponse := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions",
		`{"model":"company-model","stream":true,"messages":[],"tools":[]}`, firstKey.Key, streamCtx)
	buffer := make([]byte, 64)
	n, err := streamResponse.Body.Read(buffer)
	if err != nil || !bytes.Contains(buffer[:n], []byte("data:")) {
		t.Fatalf("first SSE chunk not flushed: n=%d err=%v data=%q", n, err, buffer[:n])
	}
	cancel()
	streamResponse.Body.Close()
	select {
	case <-streamCancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not reach upstream")
	}

	revoke := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/keys/"+firstKey.ID+"/revoke", `{}`, cookie, csrf, server.URL)
	if revoke.StatusCode != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", revoke.StatusCode, readBody(revoke))
	}
	revoke.Body.Close()
	rejected := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", chatBody, firstKey.Key, context.Background())
	if rejected.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key status=%d", rejected.StatusCode)
	}
	rejected.Body.Close()

	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app = openTestApp(t, dataDir)
	defer app.Close()
	server = httptest.NewServer(app.Handler())
	defer server.Close()
	restarted := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", chatBody, secondKey.Key, context.Background())
	if restarted.StatusCode != http.StatusOK {
		t.Fatalf("persisted key/route after restart status=%d body=%s", restarted.StatusCode, readBody(restarted))
	}
	restarted.Body.Close()
}

func TestNetworkSafetyValidation(t *testing.T) {
	if _, err := validateEndpoint(context.Background(), "http://192.0.2.1/v1", true); err == nil {
		t.Fatal("test flag allowed cleartext to a non-loopback address")
	}
	if err := ValidateListenConfig(Config{Listen: "0.0.0.0:8787"}); err == nil {
		t.Fatal("non-loopback listener was accepted without TLS")
	}
	if err := ValidateListenConfig(Config{Listen: "127.0.0.1:8787"}); err != nil {
		t.Fatalf("loopback listener rejected: %v", err)
	}
	var followed atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		followed.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	response, err := newUpstreamClient(true).Get(redirect.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect || followed.Load() != 0 {
		t.Fatalf("redirect handling status=%d followed=%d", response.StatusCode, followed.Load())
	}
}

func TestAdministratorPasswordLengthBoundaries(t *testing.T) {
	for _, test := range []struct {
		name    string
		length  int
		wantErr bool
	}{
		{name: "below minimum", length: 11, wantErr: true},
		{name: "minimum", length: 12},
		{name: "bcrypt maximum", length: 72},
		{name: "above bcrypt maximum", length: 73, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			password := strings.Repeat("s", test.length)
			err := Initialize(context.Background(), t.TempDir(), strings.NewReader(password+"\n"))
			if test.wantErr && err == nil {
				t.Fatalf("Initialize accepted %d-byte password", test.length)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("Initialize rejected %d-byte password: %v", test.length, err)
			}
			if err != nil && strings.Contains(err.Error(), password) {
				t.Fatal("initialization error exposed the supplied password")
			}
		})
	}

	dataDir := t.TempDir()
	validPassword := strings.Repeat("v", adminPasswordMaxBytes)
	if err := Initialize(context.Background(), dataDir, strings.NewReader(validPassword+"\n")); err != nil {
		t.Fatal(err)
	}
	app := openTestApp(t, dataDir)
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	accepted := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/sessions",
		`{"username":"admin","password":`+quoteJSON(validPassword)+`}`, nil, "", server.URL)
	if accepted.StatusCode != http.StatusOK {
		t.Fatalf("login rejected 72-byte password: status=%d body=%s", accepted.StatusCode, readBody(accepted))
	}
	accepted.Body.Close()
	for _, length := range []int{11, 73} {
		password := strings.Repeat("private", length/7) + strings.Repeat("x", length%7)
		response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/sessions",
			`{"username":"admin","password":`+quoteJSON(password)+`}`, nil, "", server.URL)
		body := readBody(response)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("login password length %d status=%d body=%s", length, response.StatusCode, body)
		}
		if strings.Contains(body, password) {
			t.Fatal("login error exposed the supplied password")
		}
	}
}

func openTestApp(t *testing.T, dataDir string) *App {
	t.Helper()
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", AllowLoopbackUpstream: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func loginTestAdmin(t *testing.T, baseURL string) (*http.Cookie, string) {
	t.Helper()
	response := requestJSON(t, http.MethodPost, baseURL+"/admin/api/v1/sessions", `{"username":"admin","password":"a-strong-preview-password"}`, nil, "", baseURL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.StatusCode, readBody(response))
	}
	var value map[string]string
	decodeResponse(t, response, &value)
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies=%d", len(cookies))
	}
	return cookies[0], value["csrf_token"]
}

func createTestKey(t *testing.T, baseURL, employeeID, operationID string, cookie *http.Cookie, csrf string) keyView {
	t.Helper()
	response := requestJSON(t, http.MethodPost, baseURL+"/admin/api/v1/employees/"+employeeID+"/keys",
		`{"name":"CLI","operation_id":`+quoteJSON(operationID)+`}`, cookie, csrf, baseURL)
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		t.Fatalf("create key status=%d body=%s", response.StatusCode, readBody(response))
	}
	var key keyView
	decodeResponse(t, response, &key)
	return key
}

func requestJSON(t *testing.T, method, target, body string, cookie *http.Cookie, csrf, origin string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func employeeRequest(t *testing.T, method, target, body, key string, ctx context.Context) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func decodeResponse(t *testing.T, response *http.Response, dst any) {
	t.Helper()
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(dst); err != nil {
		t.Fatal(err)
	}
}

func readBody(response *http.Response) string {
	b, _ := io.ReadAll(response.Body)
	response.Body.Close()
	return string(b)
}

func quoteJSON(value string) string {
	b, _ := json.Marshal(value)
	return string(b)
}

func statusOf(response *http.Response) any {
	if response == nil {
		return nil
	}
	return response.StatusCode
}
