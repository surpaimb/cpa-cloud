package service

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestClientConfigurationUsesExactAuthorizedWireAndDeterministicDefault(t *testing.T) {
	f := newAnthropicFixture(t, http.NotFoundHandler())
	for _, row := range []struct{ id, wire, provider string }{
		{"z-response", "openai-responses", "openai-compatible"},
		{"a-response", "openai-responses", "openai-compatible"},
		{"chat-only", "openai-chat", "openai-compatible"},
		{"gemini-native", "gemini-generate-content", "gemini-api-key"},
		{"gemini-wrong-provider", "gemini-generate-content", "openai-compatible"},
	} {
		upstream := "upstream-" + row.id
		if _, err := f.app.store.db.Exec(`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES(?,?,?,'https://example.invalid/v1',1,X'01',1,1,?)`, upstream, upstream, row.provider, utcNow()); err != nil {
			t.Fatal(err)
		}
		if _, err := f.app.store.db.Exec(`INSERT INTO models(id,upstream_id,upstream_model,wire_protocol,enabled,created_at) VALUES(?,?,?,?,1,?)`, row.id, upstream, row.id, row.wire, utcNow()); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := http.NewRequest(http.MethodGet, f.server.URL+"/v1/client-configuration", nil)
	req.Header.Set("Authorization", "Bearer "+f.key.Key)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK || res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q", res.StatusCode, res.Header.Get("Cache-Control"))
	}
	var got clientConfigurationResponse
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 1 || len(got.Protocols) != 4 {
		t.Fatalf("response=%+v", got)
	}
	for _, item := range got.Protocols {
		if item.Protocol == "openai-responses" {
			if item.DefaultModel != "a-response" || len(item.Candidates) != 2 || item.Candidates[1] != "z-response" {
				t.Fatalf("responses=%+v", item)
			}
		}
		if item.Protocol == "gemini-generate-content" {
			if item.DefaultModel != "gemini-native" || len(item.Candidates) != 1 {
				t.Fatalf("gemini=%+v", item)
			}
		}
	}
}

func TestClientConfigurationRejectsQueryAndInvalidKey(t *testing.T) {
	f := newAnthropicFixture(t, http.NotFoundHandler())
	for _, target := range []string{f.server.URL + "/v1/client-configuration?key=leak", f.server.URL + "/v1/client-configuration"} {
		req, _ := http.NewRequest(http.MethodGet, target, nil)
		if req.URL.RawQuery == "" {
			req.Header.Set("Authorization", "Bearer invalid")
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusBadRequest && res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("target=%s status=%d", target, res.StatusCode)
		}
	}
}
