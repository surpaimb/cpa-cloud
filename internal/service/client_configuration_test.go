package service

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"cpacloud.local/server/internal/keypolicy"
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

func TestClientConfigurationRequiresOneDispatchableRouteWitness(t *testing.T) {
	fixture := newExplicitWireFixture(t, string(wireProtocolOpenAIChat), http.NotFoundHandler())
	installAccountGroup(t, fixture.app, "grp-unselected", "channel-unselected")
	installAccountGroup(t, fixture.app, "grp-selected", "channel-selected")

	var chatUpstream string
	if err := fixture.app.store.db.QueryRow(`SELECT upstream_id FROM models WHERE id='wire-model'`).Scan(&chatUpstream); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.store.db.Exec(`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES('ups-selected-responses','selected responses','openai-compatible','https://example.invalid/v1',1,X'01',1,1,?)`, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.store.db.Exec(`INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES('wire-model',1,?)`, utcNow()); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []struct {
		upstream, upstreamModel, wire, channel string
		position                               int
	}{
		{chatUpstream, "chat-upstream", string(wireProtocolOpenAIChat), "channel-unselected", 0},
		{"ups-selected-responses", "responses-upstream", string(wireProtocolResponses), "channel-selected", 1},
	} {
		if _, err := fixture.app.store.db.Exec(`INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,wire_protocol,priority,weight,max_concurrency,channel_id,position) VALUES('wire-model',?,?,?,0,1,4,?,?)`, statement.upstream, statement.upstreamModel, statement.wire, statement.channel, statement.position); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := keypolicy.Replace(context.Background(), fixture.app.store.db, fixture.key.ID, 1, keypolicy.Replacement{
		ProtocolMode: keypolicy.ModeAll, Protocols: []keypolicy.ClientProtocol{},
		ModelMode: keypolicy.ModeAll, Models: []string{},
		SourceMode: keypolicy.ModeAll, SourceCIDRs: []string{},
		AccountGroupMode: keypolicy.ModeSelected, AccountGroupIDs: []string{"grp-selected"},
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	fixture.app.notifyAccountPoolChanged()

	fetch := func() clientConfigurationResponse {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, fixture.server.URL+"/v1/client-configuration", nil)
		req.Header.Set("Authorization", "Bearer "+fixture.key.Key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var got clientConfigurationResponse
		if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("status=%d response=%+v", res.StatusCode, got)
		}
		return got
	}

	for _, item := range fetch().Protocols {
		switch item.Protocol {
		case string(keypolicy.ProtocolOpenAIChat):
			if len(item.Candidates) != 0 {
				t.Fatalf("chat candidates=%v; mixed-wire pool is not dispatchable", item.Candidates)
			}
		case string(keypolicy.ProtocolOpenAIResponses):
			if len(item.Candidates) != 0 {
				t.Fatalf("responses candidates=%v; runtime rejects the whole mixed-wire pool", item.Candidates)
			}
		}
	}

	if _, err := fixture.app.store.db.Exec(`UPDATE model_account_pool_routes SET wire_protocol=?,channel_id=? WHERE model_id='wire-model'`, string(wireProtocolResponses), "channel-selected"); err != nil {
		t.Fatal(err)
	}
	fixture.app.notifyAccountPoolChanged()
	for _, item := range fetch().Protocols {
		switch item.Protocol {
		case string(keypolicy.ProtocolOpenAIChat):
			if len(item.Candidates) != 0 {
				t.Fatalf("chat candidates=%v after uniform responses update", item.Candidates)
			}
		case string(keypolicy.ProtocolOpenAIResponses):
			if len(item.Candidates) != 1 || item.Candidates[0] != "wire-model" {
				t.Fatalf("responses candidates=%v for dispatchable uniform pool", item.Candidates)
			}
		}
	}
}
