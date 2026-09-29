// Independently authored KEY-02 trusted-proxy service integration tests.
package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cpacloud.local/server/internal/keypolicy"
)

func TestTrustedProxyConfigurationFailsBeforeStateMutation(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "must-not-exist")
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", TrustedProxyCIDRs: []string{"127.0.0.1/32", "127.0.0.1"}})
	if app != nil || err == nil {
		if app != nil {
			_ = app.Close()
		}
		t.Fatalf("invalid trusted proxy config app=%v err=%v", app, err)
	}
	if _, statErr := os.Stat(dataDir); !os.IsNotExist(statErr) {
		t.Fatalf("invalid trusted proxy config mutated data directory: %v", statErr)
	}
}

func TestTrustedProxySourceResolutionProtectsOpenAIAndGeminiCatalogs(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test",
		TrustedProxyCIDRs: []string{"127.0.0.1/32"},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() { server.Close(); _ = app.Close() })
	cookie, csrf := loginTestAdmin(t, server.URL)
	status := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/system/status", "", cookie, "", "")
	var statusBody struct {
		Features map[string]bool `json:"features"`
	}
	decodeResponse(t, status, &statusBody)
	if !statusBody.Features["trusted_proxy_source"] {
		t.Fatalf("trusted proxy capability was not reported: %+v", statusBody.Features)
	}
	upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "trusted-source", "openai-compatible", "https://8.8.8.8/v1", "synthetic-secret")
	createModelAdmissionModel(t, server.URL, cookie, csrf, "trusted-model", upstream.ID, "provider-model")
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Trusted Source Employee")
	key := createTestKey(t, server.URL, employee.ID, "trusted-source-key", cookie, csrf)

	policy := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/keys/"+key.ID+"/policy",
		`{"expected_revision":1,"protocol_mode":"all","protocols":[],"model_mode":"all","models":[],"source_mode":"selected","source_cidrs":["203.0.113.0/24"]}`,
		cookie, csrf, server.URL)
	if policy.StatusCode != http.StatusOK {
		t.Fatalf("set forwarded source policy status=%d body=%s", policy.StatusCode, readBody(policy))
	}
	policy.Body.Close()

	doCatalog := func(path string, headers http.Header) *http.Response {
		t.Helper()
		request, requestErr := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		request.Header = headers.Clone()
		request.Header.Set("Authorization", "Bearer "+key.Key)
		response, requestErr := http.DefaultClient.Do(request)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		return response
	}
	trustedHeaders := make(http.Header)
	trustedHeaders.Set("X-Forwarded-For", "203.0.113.19")
	for _, path := range []string{"/v1/models", "/v1beta/models"} {
		response := doCatalog(path, trustedHeaders)
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("trusted catalog %s status=%d body=%s", path, response.StatusCode, body)
		}
	}

	for name, headers := range map[string]http.Header{
		"missing":              {},
		"ambiguous":            {"X-Forwarded-For": []string{"203.0.113.19", "203.0.113.20"}},
		"malformed":            {"X-Forwarded-For": []string{"203.0.113.19, not-an-address"}},
		"ignored alternatives": {"Forwarded": []string{"for=203.0.113.19"}, "X-Real-Ip": []string{"203.0.113.19"}},
	} {
		t.Run(name, func(t *testing.T) {
			response := doCatalog("/v1/models", headers)
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != http.StatusForbidden || strings.Contains(string(body), "not-an-address") {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
		})
	}

	policy = requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/keys/"+key.ID+"/policy",
		`{"expected_revision":2,"protocol_mode":"all","protocols":[],"model_mode":"all","models":[],"source_mode":"selected","source_cidrs":["127.0.0.1/32"]}`,
		cookie, csrf, server.URL)
	if policy.StatusCode != http.StatusOK {
		t.Fatalf("set direct source policy status=%d body=%s", policy.StatusCode, readBody(policy))
	}
	policy.Body.Close()
	app.trustedProxies, err = keypolicy.NewTrustedProxySet([]string{"192.0.2.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	spoofed := make(http.Header)
	spoofed.Add("X-Forwarded-For", "not-an-address")
	spoofed.Add("X-Forwarded-For", "203.0.113.19")
	response := doCatalog("/v1/models", spoofed)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("untrusted peer did not ignore spoofed forwarding headers: status=%d", response.StatusCode)
	}
}
