package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDiscoverUpstreamModelsAuthorizationIsolationAndNoRouteMutation(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Errorf("upstream request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer discovery-secret" {
			t.Errorf("upstream authorization = %q", got)
		}
		for _, forbidden := range []string{"Cookie", "Origin", "X-CSRF-Token"} {
			if got := r.Header.Get(forbidden); got != "" {
				t.Errorf("admin header %s reached upstream: %q", forbidden, got)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"z-model"},{"id":"a-model"},{"id":"z-model"}]}`)
	}))
	defer upstream.Close()

	appServer, app, cookie, csrf := newDiscoveryTestApp(t)
	defer appServer.Close()
	defer app.Close()
	upstreamID := createDiscoveryTestUpstream(t, appServer.URL, upstream.URL, "discovery-secret", cookie, csrf)
	target := appServer.URL + "/admin/api/v1/upstreams/" + upstreamID + "/discover-models"

	unauthenticated := requestJSON(t, http.MethodPost, target, "", nil, csrf, appServer.URL)
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d body=%s", unauthenticated.StatusCode, readBody(unauthenticated))
	}
	unauthenticated.Body.Close()

	missingCSRF := requestJSON(t, http.MethodPost, target, "", cookie, "", appServer.URL)
	if missingCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", missingCSRF.StatusCode, readBody(missingCSRF))
	}
	missingCSRF.Body.Close()

	wrongOrigin := requestJSON(t, http.MethodPost, target, "", cookie, csrf, "http://invalid.example")
	if wrongOrigin.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong origin status=%d body=%s", wrongOrigin.StatusCode, readBody(wrongOrigin))
	}
	wrongOrigin.Body.Close()
	if calls.Load() != 0 {
		t.Fatalf("unauthorized requests reached upstream: calls=%d", calls.Load())
	}

	response := requestJSON(t, http.MethodPost, target, "", cookie, csrf, appServer.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("discovery status=%d body=%s", response.StatusCode, readBody(response))
	}
	var result struct {
		Items []discoveredModel `json:"items"`
	}
	decodeResponse(t, response, &result)
	if want := []discoveredModel{{ID: "a-model"}, {ID: "z-model"}}; !reflect.DeepEqual(result.Items, want) {
		t.Fatalf("discovered items=%v want=%v", result.Items, want)
	}
	if calls.Load() != 1 {
		t.Fatalf("authorized discovery calls=%d", calls.Load())
	}

	models := requestJSON(t, http.MethodGet, appServer.URL+"/admin/api/v1/models", "", cookie, "", "")
	if models.StatusCode != http.StatusOK {
		t.Fatalf("list models status=%d body=%s", models.StatusCode, readBody(models))
	}
	var stored struct {
		Items []modelView `json:"items"`
	}
	decodeResponse(t, models, &stored)
	if len(stored.Items) != 0 {
		t.Fatalf("discovery created model routes: %v", stored.Items)
	}

	disable := requestJSON(t, http.MethodPatch, appServer.URL+"/admin/api/v1/upstreams/"+upstreamID,
		`{"expected_revision":1,"enabled":false}`, cookie, csrf, appServer.URL)
	if disable.StatusCode != http.StatusOK {
		t.Fatalf("disable upstream status=%d body=%s", disable.StatusCode, readBody(disable))
	}
	disable.Body.Close()
	disabled := requestJSON(t, http.MethodPost, target, "", cookie, csrf, appServer.URL)
	if disabled.StatusCode != http.StatusConflict {
		t.Fatalf("disabled discovery status=%d body=%s", disabled.StatusCode, readBody(disabled))
	}
	assertAdminError(t, disabled, "upstream_disabled", "Upstream is disabled.")
	if calls.Load() != 1 {
		t.Fatalf("disabled upstream was contacted: calls=%d", calls.Load())
	}
}

func TestDiscoverUpstreamModelsFailureBoundsAndRedaction(t *testing.T) {
	appServer, app, cookie, csrf := newDiscoveryTestApp(t)
	defer appServer.Close()
	defer app.Close()

	tooMany := strings.Builder{}
	tooMany.WriteString(`{"data":[`)
	for index := 0; index <= modelDiscoveryMaxItems; index++ {
		if index != 0 {
			tooMany.WriteByte(',')
		}
		fmt.Fprintf(&tooMany, `{"id":"model-%04d"}`, index)
	}
	tooMany.WriteString(`]}`)

	tests := []struct {
		name        string
		status      int
		body        string
		apiKey      string
		marker      string
		wantStatus  int
		wantCode    string
		wantMessage string
		retryAfter  string
	}{
		{name: "upstream status", status: http.StatusTeapot, body: `provider-error-private`, apiKey: "key-status-private", marker: "provider-error-private", wantStatus: http.StatusBadGateway, wantCode: "model_discovery_failed", wantMessage: "Unable to discover upstream models."},
		{name: "unauthorized", status: http.StatusUnauthorized, body: `private-auth-error`, apiKey: "key-auth-private", marker: "private-auth-error", wantStatus: http.StatusBadGateway, wantCode: "upstream_authentication_failed", wantMessage: "Upstream authentication failed."},
		{name: "forbidden", status: http.StatusForbidden, body: `private-forbidden-error`, apiKey: "key-forbidden-private", marker: "private-forbidden-error", wantStatus: http.StatusBadGateway, wantCode: "upstream_authentication_failed", wantMessage: "Upstream authentication failed."},
		{name: "not found", status: http.StatusNotFound, body: `private-not-found-error`, apiKey: "key-not-found-private", marker: "private-not-found-error", wantStatus: http.StatusBadGateway, wantCode: "model_discovery_unsupported", wantMessage: "Upstream model discovery is not supported."},
		{name: "method not allowed", status: http.StatusMethodNotAllowed, body: `private-method-error`, apiKey: "key-method-private", marker: "private-method-error", wantStatus: http.StatusBadGateway, wantCode: "model_discovery_unsupported", wantMessage: "Upstream model discovery is not supported."},
		{name: "rate limited", status: http.StatusTooManyRequests, body: `private-rate-error`, apiKey: "key-rate-private", marker: "private-rate-error", wantStatus: http.StatusTooManyRequests, wantCode: "upstream_rate_limited", wantMessage: "Upstream rate limit was reached.", retryAfter: "30"},
		{name: "oversized body", status: http.StatusOK, body: strings.Repeat("x", modelDiscoveryMaxBody+1), apiKey: "key-size-private", marker: strings.Repeat("x", 32), wantStatus: http.StatusBadGateway, wantCode: "invalid_model_response", wantMessage: "Upstream returned an invalid model list."},
		{name: "malformed JSON", status: http.StatusOK, body: `secret-invalid-json`, apiKey: "key-json-private", marker: "secret-invalid-json", wantStatus: http.StatusBadGateway, wantCode: "invalid_model_response", wantMessage: "Upstream returned an invalid model list."},
		{name: "invalid model ID", status: http.StatusOK, body: `{"data":[{"id":17}]}`, apiKey: "key-id-private", marker: `17`, wantStatus: http.StatusBadGateway, wantCode: "invalid_model_response", wantMessage: "Upstream returned an invalid model list."},
		{name: "too many models", status: http.StatusOK, body: tooMany.String(), apiKey: "key-count-private", marker: "model-1000", wantStatus: http.StatusBadGateway, wantCode: "invalid_model_response", wantMessage: "Upstream returned an invalid model list."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Authorization"); got != "Bearer "+test.apiKey {
					t.Errorf("authorization=%q", got)
				}
				if test.retryAfter != "" {
					w.Header().Set("Retry-After", test.retryAfter)
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			}))
			defer upstream.Close()
			upstreamID := createDiscoveryTestUpstream(t, appServer.URL, upstream.URL, test.apiKey, cookie, csrf)
			response := requestJSON(t, http.MethodPost,
				appServer.URL+"/admin/api/v1/upstreams/"+upstreamID+"/discover-models", "", cookie, csrf, appServer.URL)
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
			}
			if test.retryAfter != "" && response.Header.Get("Retry-After") != test.retryAfter {
				t.Fatalf("Retry-After=%q", response.Header.Get("Retry-After"))
			}
			body := assertAdminError(t, response, test.wantCode, test.wantMessage)
			if strings.Contains(body, test.apiKey) || strings.Contains(body, test.marker) {
				t.Fatalf("failure exposed upstream material: %s", body)
			}
		})
	}

	var followed atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		followed.Add(1)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	redirectID := createDiscoveryTestUpstream(t, appServer.URL, redirect.URL, "redirect-key", cookie, csrf)
	redirectResponse := requestJSON(t, http.MethodPost,
		appServer.URL+"/admin/api/v1/upstreams/"+redirectID+"/discover-models", "", cookie, csrf, appServer.URL)
	if redirectResponse.StatusCode != http.StatusBadGateway {
		t.Fatalf("redirect discovery status=%d body=%s", redirectResponse.StatusCode, readBody(redirectResponse))
	}
	assertAdminError(t, redirectResponse, "model_discovery_failed", "Unable to discover upstream models.")
	if followed.Load() != 0 {
		t.Fatal("model discovery followed an upstream redirect")
	}
}

func TestDiscoverUpstreamModelsTenSecondTimeout(t *testing.T) {
	cancelled := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	defer upstream.Close()

	appServer, app, cookie, csrf := newDiscoveryTestApp(t)
	defer appServer.Close()
	defer app.Close()
	upstreamID := createDiscoveryTestUpstream(t, appServer.URL, upstream.URL, "timeout-private-key", cookie, csrf)
	started := time.Now()
	response := requestJSON(t, http.MethodPost,
		appServer.URL+"/admin/api/v1/upstreams/"+upstreamID+"/discover-models", "", cookie, csrf, appServer.URL)
	elapsed := time.Since(started)
	if response.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("timeout status=%d body=%s", response.StatusCode, readBody(response))
	}
	body := assertAdminError(t, response, "model_discovery_timeout", "Upstream model discovery timed out.")
	if strings.Contains(body, "timeout-private-key") {
		t.Fatalf("timeout exposed key: %s", body)
	}
	if elapsed < 9*time.Second || elapsed > 15*time.Second {
		t.Fatalf("discovery timeout elapsed=%s", elapsed)
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout did not cancel upstream request")
	}
}

func TestDiscoverUpstreamModelsClientCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	defer upstream.Close()

	appServer, app, cookie, csrf := newDiscoveryTestApp(t)
	defer appServer.Close()
	defer app.Close()
	upstreamID := createDiscoveryTestUpstream(t, appServer.URL, upstream.URL, "cancel-private-key", cookie, csrf)
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		appServer.URL+"/admin/api/v1/upstreams/"+upstreamID+"/discover-models", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.AddCookie(cookie)
	request.Header.Set("X-CSRF-Token", csrf)
	request.Header.Set("Origin", appServer.URL)
	result := make(chan error, 1)
	go func() {
		response, err := http.DefaultClient.Do(request)
		if response != nil {
			response.Body.Close()
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("discovery did not reach upstream")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled client request unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("client cancellation did not stop admin request")
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("client cancellation did not reach upstream")
	}
}

func TestUpstreamModelsURL(t *testing.T) {
	for _, test := range []struct {
		endpoint string
		want     string
	}{
		{endpoint: "https://api.example.test", want: "https://api.example.test/v1/models"},
		{endpoint: "https://api.example.test/v1", want: "https://api.example.test/v1/models"},
		{endpoint: "https://api.example.test/openai", want: "https://api.example.test/openai/v1/models"},
	} {
		got, err := upstreamModelsURL(test.endpoint)
		if err != nil || got != test.want {
			t.Fatalf("upstreamModelsURL(%q)=%q,%v want=%q", test.endpoint, got, err, test.want)
		}
	}
}

func newDiscoveryTestApp(t *testing.T) (*httptest.Server, *App, *http.Cookie, string) {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)
	return server, app, cookie, csrf
}

func createDiscoveryTestUpstream(
	t *testing.T,
	baseURL string,
	endpoint string,
	apiKey string,
	cookie *http.Cookie,
	csrf string,
) string {
	t.Helper()
	response := requestJSON(t, http.MethodPost, baseURL+"/admin/api/v1/upstreams",
		`{"name":"Discovery","provider_kind":"openai-compatible","endpoint":`+quoteJSON(endpoint)+`,"api_key":`+quoteJSON(apiKey)+`}`,
		cookie, csrf, baseURL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create discovery upstream status=%d body=%s", response.StatusCode, readBody(response))
	}
	var item upstreamView
	decodeResponse(t, response, &item)
	return item.ID
}

func assertAdminError(t *testing.T, response *http.Response, code, message string) string {
	t.Helper()
	bodyBytes, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var value struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(bodyBytes, &value); err != nil {
		t.Fatalf("decode admin error: %v body=%s", err, bodyBytes)
	}
	if value.Error.Code != code || value.Error.Message != message {
		t.Fatalf("admin error=%+v want code=%q message=%q", value.Error, code, message)
	}
	return string(bodyBytes)
}
