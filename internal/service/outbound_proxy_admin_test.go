package service

// Synthetic admin and final-dispatch acceptance; no external provider calls.
import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

func TestOutboundProxyAdminPermissionsCASAndRedaction(t *testing.T) {
	f := newRuntimeAccountPoolFixture(t)
	f.enableRuntimeAdminHTTP(t)
	base := f.server.URL + "/admin/api/v1"
	body := `{"operation_id":"123e4567-e89b-42d3-a456-426614174000","name":"Synthetic proxy","scheme":"https","host":"proxy.example.org","port":443,"address_scope":"public","enabled":true,"credentials":{"username":"proxy-fixture-user","password":"proxy-fixture-password"}}`
	check := func(method, path, input string, cookie *http.Cookie, csrf, origin string, status int) string {
		t.Helper()
		r := requestJSON(t, method, base+path, input, cookie, csrf, origin)
		result := readBody(r)
		if r.StatusCode != status {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, r.StatusCode, status, result)
		}
		for _, secret := range []string{"proxy-fixture-user", "proxy-fixture-password", "credential_ciphertext", "input_fingerprint"} {
			if strings.Contains(result, secret) {
				t.Fatal("proxy secret exposed in admin response")
			}
		}
		return result
	}
	check("POST", "/outbound-proxies", body, nil, "", f.server.URL, 401)
	check("POST", "/outbound-proxies", body, f.cookie, "", f.server.URL, 403)
	check("POST", "/outbound-proxies", body, f.cookie, f.csrf, "http://wrong-origin.invalid", 403)
	var proxy outboundProxyDTO
	json.Unmarshal([]byte(check("POST", "/outbound-proxies", body, f.cookie, f.csrf, f.server.URL, 200)), &proxy)
	if proxy.ID == "" || proxy.Revision != 1 || !proxy.HasCredentials {
		t.Fatal("missing created proxy metadata")
	}
	var duplicate outboundProxyDTO
	json.Unmarshal([]byte(check("POST", "/outbound-proxies", body, f.cookie, f.csrf, f.server.URL, 200)), &duplicate)
	if duplicate.ID != proxy.ID {
		t.Fatal("replayed create duplicated proxy")
	}
	check("POST", "/outbound-proxies", strings.Replace(body, "Synthetic proxy", "Other proxy", 1), f.cookie, f.csrf, f.server.URL, 409)
	check("POST", "/outbound-proxies", strings.Replace(body, `"enabled":true,`, "", 1), f.cookie, f.csrf, f.server.URL, 400)
	check("POST", "/outbound-proxies", strings.Replace(body, `"password":`, `"unknown":true,"password":`, 1), f.cookie, f.csrf, f.server.URL, 400)
	for _, q := range []string{"?limit=0", "?limit=101", "?limit=1&limit=2", "?unknown=1", "?after_id=%zz"} {
		check("GET", "/outbound-proxies"+q, "", f.cookie, "", f.server.URL, 400)
	}
	check("GET", "/outbound-proxies?limit=1", "", f.cookie, "", f.server.URL, 200)
	check("GET", "/outbound-proxies/"+proxy.ID, "", f.cookie, "", f.server.URL, 200)
	f.insertUpstream(t, "ups_proxy_api", "openai-compatible")
	f.insertUpstream(t, "ups_proxy_codex", codexMembershipProvider)
	f.insertUpstream(t, "ups_proxy_http", "openai-compatible")
	if _, err := f.app.store.db.Exec(`UPDATE upstreams SET endpoint='http://127.0.0.1' WHERE id='ups_proxy_http'`); err != nil {
		t.Fatal(err)
	}
	bind := fmt.Sprintf(`{"expected_upstream_revision":1,"proxy_id":%q,"expected_proxy_revision":1,"bind":true}`, proxy.ID)
	for _, id := range []string{"ups_proxy_codex", "ups_proxy_http"} {
		response := check("PUT", "/upstreams/"+id+"/proxy", bind, f.cookie, f.csrf, f.server.URL, 400)
		if !strings.Contains(response, "unsupported_proxy_binding") {
			t.Fatal("missing unsupported binding reason")
		}
	}
	for _, revision := range []string{"0", "-1", "9007199254740992", "null"} {
		check("PUT", "/upstreams/ups_proxy_api/proxy", strings.Replace(bind, `"expected_upstream_revision":1`, `"expected_upstream_revision":`+revision, 1), f.cookie, f.csrf, f.server.URL, 400)
	}
	var binding upstreamProxyDTO
	json.Unmarshal([]byte(check("PUT", "/upstreams/ups_proxy_api/proxy", bind, f.cookie, f.csrf, f.server.URL, 200)), &binding)
	if binding.UpstreamRevision != 2 || binding.Binding == nil || binding.Binding.ProxyID != proxy.ID {
		t.Fatal("binding/revision not saved")
	}
	check("PUT", "/upstreams/ups_proxy_api/proxy", bind, f.cookie, f.csrf, f.server.URL, 409)
	check("GET", "/upstreams/ups_proxy_api/proxy", "", f.cookie, "", f.server.URL, 200)
	// The shared resource dispatcher must preserve prices and OAuth precedence.
	check("GET", "/upstreams/ups_proxy_api/prices", "", f.cookie, "", f.server.URL, 200)
	check("GET", "/upstreams/ups_proxy_api/unknown", "", f.cookie, "", f.server.URL, 404)
	patch := `{"expected_revision":1,"name":"Disabled proxy","scheme":"https","host":"proxy.example.org","port":443,"address_scope":"public","enabled":false,"credential_mode":"keep"}`
	json.Unmarshal([]byte(check("PATCH", "/outbound-proxies/"+proxy.ID, patch, f.cookie, f.csrf, f.server.URL, 200)), &proxy)
	if proxy.Enabled || proxy.Revision != 2 || proxy.ConnectionRevision != 2 || !proxy.HasCredentials {
		t.Fatal("disable/keep state mismatch")
	}
	json.Unmarshal([]byte(check("GET", "/upstreams/ups_proxy_api/proxy", "", f.cookie, "", f.server.URL, 200)), &binding)
	if binding.UpstreamRevision != 3 || binding.Binding == nil || binding.Binding.Enabled {
		t.Fatal("disabled proxy unexpectedly unbound")
	}
	check("PATCH", "/outbound-proxies/"+proxy.ID, patch, f.cookie, f.csrf, f.server.URL, 409)
	unbind := fmt.Sprintf(`{"expected_upstream_revision":3,"proxy_id":%q,"expected_proxy_revision":2,"bind":false}`, proxy.ID)
	json.Unmarshal([]byte(check("PUT", "/upstreams/ups_proxy_api/proxy", unbind, f.cookie, f.csrf, f.server.URL, 200)), &binding)
	if binding.UpstreamRevision != 4 || binding.Binding != nil {
		t.Fatal("explicit unbind failed")
	}
	check("PUT", "/upstreams/ups_proxy_api/proxy", strings.Replace(unbind, `"expected_upstream_revision":3`, `"expected_upstream_revision":4`, 1), f.cookie, f.csrf, f.server.URL, 409)
}

func TestOutboundProxyFinalDispatchRevisionAndAuthorization(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		for _, change := range []string{"none", "bind", "proxy_name", "proxy_connection", "proxy_disabled", "account", "key", "policy", "model", "route"} {
			t.Run(fmt.Sprintf("pooled_%t/%s", pooled, change), func(t *testing.T) {
				f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
				a := f.base.app
				a.accountPool.Close()
				a.accountPool = f.rt
				f.insertAccount(t, "ups_dispatch", true)
				revision := int64(0)
				if pooled {
					revision = 1
				}
				f.insertModelPool(t, "dispatch-model", "ups_dispatch", revision, modelAccountView{UpstreamID: "ups_dispatch", UpstreamModel: "actual-model", Priority: 1, Weight: 1, MaxConcurrency: 2})
				proxy, err := a.outboundProxies.Create(context.Background(), outboundProxyCreateInput{OperationID: "123e4567-e89b-42d3-a456-426614174001", Name: "proxy", Scheme: "https", Host: "proxy.example.org", Port: 443, AddressScope: "public", Enabled: true})
				if err != nil {
					t.Fatal(err)
				}
				initiallyBound := strings.HasPrefix(change, "proxy_")
				if initiallyBound {
					if _, err := a.outboundProxies.SetBinding(context.Background(), upstreamProxyBindingInput{UpstreamID: "ups_dispatch", ExpectedUpstreamRevision: 1, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: true}); err != nil {
						t.Fatal(err)
					}
				}
				r := httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader("{}"))
				r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, "request-egress-dispatch"))
				selected, lease, failed := a.prepareModelRoute(r, f.auth1, "dispatch-model", []string{"openai-compatible"}, accounting.ProtocolOpenAIChatCompletions, true, func(_ *http.Request, selected route) (route, *modelPreflightError) { return selected, nil })
				if failed != nil {
					t.Fatalf("preparation failed: %+v", failed)
				}
				defer a.releaseModelLease(lease, requestID(r.Context()), true)
				a.admission.Lock()
				switch change {
				case "bind":
					_, err = a.outboundProxies.SetBinding(context.Background(), upstreamProxyBindingInput{UpstreamID: "ups_dispatch", ExpectedUpstreamRevision: selected.Revision, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: true})
				case "proxy_name", "proxy_connection", "proxy_disabled":
					port, enabled := 443, true
					if change == "proxy_connection" {
						port = 8443
					}
					if change == "proxy_disabled" {
						enabled = false
					}
					_, err = a.outboundProxies.Update(context.Background(), outboundProxyUpdateInput{ID: proxy.ID, ExpectedRevision: 1, Name: "renamed", Scheme: "https", Host: "proxy.example.org", Port: port, AddressScope: "public", Enabled: enabled, CredentialMode: "keep"})
				case "account":
					_, err = a.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id='ups_dispatch'`)
				case "key":
					_, err = a.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id=?`, utcNow(), f.auth1.KeyID)
				case "policy":
					_, err = a.store.db.Exec(`DELETE FROM employee_models WHERE employee_id=?`, f.auth1.EmployeeID)
				case "model":
					_, err = a.store.db.Exec(`UPDATE models SET enabled=0 WHERE id='dispatch-model'`)
				case "route":
					if pooled {
						_, err = a.store.db.Exec(`UPDATE model_account_pool_configs SET revision=revision+1 WHERE model_id='dispatch-model'`)
					} else {
						_, err = a.store.db.Exec(`UPDATE models SET upstream_model='changed' WHERE id='dispatch-model'`)
					}
				}
				a.admission.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				client, failure := a.dispatchModelRoute(r, f.auth1, "dispatch-model", selected, lease, true)
				allowed := change == "none" || change == "proxy_name"
				if (failure == nil) != allowed || (client != nil) != allowed {
					t.Fatalf("dispatch allowed=%t failure=%+v", allowed, failure)
				}
				var attempts int
				if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil {
					t.Fatal(err)
				}
				want := 0
				if allowed {
					want = 1
				}
				if attempts != want {
					t.Fatalf("attempts=%d want=%d", attempts, want)
				}
				a.finishRequest(requestID(r.Context()), "cancelled", 0)
			})
		}
	}
}
