package service

// This integration fixture uses only synthetic credentials and certificates.
// It exercises the production App, route admission, encrypted proxy binding,
// HTTPS CONNECT transport, protocol handlers, catalogs, health, and recovery.
import (
	"context"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

const (
	egHTTPProxyUser     = "egress-fixture-user"
	egHTTPProxyPassword = "egress-fixture-password"
	egHTTPEmployeeKeyOp = "egress-http-employee-key"
)

type egressHTTPTarget struct {
	t               *testing.T
	employeeKey     atomic.Value
	calls           atomic.Int32
	mu              sync.Mutex
	paths           map[string]int
	inflightReached chan struct{}
	inflightRelease chan struct{}
	inflightOnce    sync.Once
}

type egressHTTPProxy struct {
	t             *testing.T
	server        *httptest.Server
	targetHost    string
	connects      atomic.Int32
	employeeKey   atomic.Value
	connectionsMu sync.Mutex
	connections   []net.Conn
}

type egressHTTPFixture struct {
	t           *testing.T
	app         *App
	server      *httptest.Server
	cookie      *http.Cookie
	csrf        string
	target      *httptest.Server
	targetState *egressHTTPTarget
	proxy       *egressHTTPProxy
	badProxy    *egressHTTPProxy
	proxyView   outboundProxyView
	employeeKey string
	accounts    map[string]upstreamView
}

func TestOutboundProxyHTTPIntegration(t *testing.T) {
	if os.Getenv("CPA_TEST_EGRESS_HTTP_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestOutboundProxyHTTPIntegration$", "-test.count=1", "-test.timeout=110s")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GODEBUG=") && !strings.HasPrefix(entry, "CPA_TEST_EGRESS_HTTP_CHILD=") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "GODEBUG=x509usefallbackroots=1", "CPA_TEST_EGRESS_HTTP_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated HTTP egress integration failed: %v\n%s", err, output)
		}
		return
	}

	root, signer, roots := newOutboundProxyTestCA(t)
	x509.SetFallbackRoots(roots)
	fixture := newEgressHTTPFixture(t, root, signer)
	t.Run("all API-key execution and catalog paths use the binding", fixture.testExecutionCatalogHealthAndRecovery)
	t.Run("proxy failures stay closed and dispatched work is not replayed", fixture.testFailureAndInflightSemantics)
}

func newEgressHTTPFixture(t *testing.T, root *x509.Certificate, signer *ecdsa.PrivateKey) *egressHTTPFixture {
	t.Helper()
	targetState := &egressHTTPTarget{t: t, paths: make(map[string]int), inflightReached: make(chan struct{}), inflightRelease: make(chan struct{})}
	targetState.employeeKey.Store("")
	target := httptest.NewUnstartedServer(http.HandlerFunc(targetState.handle))
	target.Config.ErrorLog = log.New(io.Discard, "", 0)
	target.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{newOutboundProxyTestCertificate(t, root, signer, true)}}
	target.StartTLS()
	t.Cleanup(target.Close)
	targetURL, _ := url.Parse(target.URL)
	proxy := newEgressHTTPProxy(t, targetURL.Host, newOutboundProxyTestCertificate(t, root, signer, true))
	badProxy := newEgressHTTPProxy(t, targetURL.Host, newOutboundProxyTestCertificate(t, root, signer, false))

	dataDir := runtimeTestDataDir(t)
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", AllowLoopbackUpstream: true, Version: "egress-http-test"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	cookie, csrf := installRuntimeAdminSession(t, app, time.Now().UTC().Add(time.Hour))
	fixture := &egressHTTPFixture{t: t, app: app, server: server, cookie: cookie, csrf: csrf, target: target, targetState: targetState, proxy: proxy, badProxy: badProxy, accounts: make(map[string]upstreamView)}
	t.Cleanup(func() {
		server.Close()
		_ = app.Close()
		proxy.Close()
		badProxy.Close()
	})

	proxyURL, _ := url.Parse(proxy.server.URL)
	port, _ := strconv.Atoi(proxyURL.Port())
	credential := outboundProxyCredential{username: egHTTPProxyUser, password: egHTTPProxyPassword}
	fixture.proxyView, err = app.outboundProxies.Create(context.Background(), outboundProxyCreateInput{
		OperationID: "30000000-0000-4000-8000-000000000001", Name: "Synthetic CONNECT", Scheme: "https",
		Host: proxyURL.Hostname(), Port: port, AddressScope: "private", Enabled: true, Credential: &credential,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []struct{ key, provider, secret string }{
		{"openai", "openai-compatible", "openai-egress-secret"},
		{"anthropic", anthropicAPIKeyProvider, "anthropic-egress-secret"},
		{"gemini", geminiAPIKeyProvider, "gemini-egress-secret"},
	} {
		view := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Egress "+account.key, account.provider, target.URL, account.secret)
		bound, err := app.outboundProxies.SetBinding(context.Background(), upstreamProxyBindingInput{UpstreamID: view.ID, ExpectedUpstreamRevision: view.Revision, ProxyID: fixture.proxyView.ID, ExpectedProxyRevision: fixture.proxyView.Revision, Bind: true})
		if err != nil {
			t.Fatal(err)
		}
		view.Revision = bound.UpstreamRevision
		fixture.accounts[account.key] = view
	}
	createModelAdmissionModel(t, server.URL, cookie, csrf, "egress-chat", fixture.accounts["openai"].ID, "actual-chat")
	createModelAdmissionModel(t, server.URL, cookie, csrf, "egress-responses", fixture.accounts["openai"].ID, "actual-responses")
	createModelAdmissionModel(t, server.URL, cookie, csrf, "egress-claude", fixture.accounts["anthropic"].ID, "actual-claude")
	createModelAdmissionModel(t, server.URL, cookie, csrf, "egress-gemini", fixture.accounts["gemini"].ID, "actual-gemini")
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Egress employee")
	key := createTestKey(t, server.URL, employee.ID, egHTTPEmployeeKeyOp, cookie, csrf)
	fixture.employeeKey = key.Key
	targetState.employeeKey.Store(key.Key)
	proxy.employeeKey.Store(key.Key)
	badProxy.employeeKey.Store(key.Key)
	return fixture
}

func newEgressHTTPProxy(t *testing.T, targetHost string, certificate tls.Certificate) *egressHTTPProxy {
	t.Helper()
	fixture := &egressHTTPProxy{t: t, targetHost: targetHost}
	fixture.employeeKey.Store("")
	server := httptest.NewUnstartedServer(http.HandlerFunc(fixture.handle))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	server.StartTLS()
	fixture.server = server
	return fixture
}

func (p *egressHTTPProxy) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect || r.Host != p.targetHost {
		http.Error(w, "invalid synthetic target", http.StatusBadRequest)
		return
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(egHTTPProxyUser+":"+egHTTPProxyPassword))
	if r.Header.Get("Proxy-Authorization") != wantAuth || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" {
		p.t.Errorf("CONNECT credential isolation failed: proxy=%q authorization=%q cookie=%q", r.Header.Get("Proxy-Authorization"), r.Header.Get("Authorization"), r.Header.Get("Cookie"))
	}
	if employeeKey, _ := p.employeeKey.Load().(string); employeeKey != "" {
		for name, values := range r.Header {
			if strings.Contains(strings.Join(values, ","), employeeKey) {
				p.t.Errorf("CONNECT header %s contains employee key", name)
			}
		}
	}
	p.connects.Add(1)
	upstream, err := net.DialTimeout("tcp", r.Host, 3*time.Second)
	if err != nil {
		http.Error(w, "unavailable", http.StatusBadGateway)
		return
	}
	connection, buffer, err := w.(http.Hijacker).Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	p.connectionsMu.Lock()
	p.connections = append(p.connections, connection, upstream)
	p.connectionsMu.Unlock()
	if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil || buffer.Flush() != nil {
		connection.Close()
		upstream.Close()
		return
	}
	go func() { _, _ = io.Copy(upstream, buffer); _ = upstream.Close(); _ = connection.Close() }()
	_, _ = io.Copy(connection, upstream)
	_ = connection.Close()
	_ = upstream.Close()
}

func (p *egressHTTPProxy) Close() {
	if p == nil || p.server == nil {
		return
	}
	p.connectionsMu.Lock()
	for _, connection := range p.connections {
		_ = connection.Close()
	}
	p.connectionsMu.Unlock()
	p.server.Close()
}

func (s *egressHTTPTarget) handle(w http.ResponseWriter, r *http.Request) {
	s.calls.Add(1)
	s.mu.Lock()
	s.paths[r.URL.Path]++
	s.mu.Unlock()
	employeeKey, _ := s.employeeKey.Load().(string)
	for _, header := range []string{"Proxy-Authorization", "Cookie", "Origin", "X-CSRF-Token"} {
		if r.Header.Get(header) != "" {
			s.t.Errorf("target received forbidden %s header", header)
		}
	}
	if employeeKey != "" {
		for name, values := range r.Header {
			joined := strings.Join(values, ",")
			if strings.Contains(joined, employeeKey) {
				s.t.Errorf("target header %s contains employee key", name)
			}
			if strings.Contains(joined, egHTTPProxyUser) || strings.Contains(joined, egHTTPProxyPassword) {
				s.t.Errorf("target header %s contains proxy credential", name)
			}
		}
	}
	w.Header().Set("Connection", "close")
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-Api-Key") == "anthropic-egress-secret" {
			if r.URL.Query().Get("after_id") == "" {
				_, _ = io.WriteString(w, `{"data":[{"id":"claude-page-one","display_name":"Claude one","type":"model"}],"has_more":true,"last_id":"cursor-one"}`)
			} else {
				_, _ = io.WriteString(w, `{"data":[{"id":"claude-page-two","display_name":"Claude two","type":"model"}],"has_more":false,"last_id":"cursor-two"}`)
			}
			return
		}
		if r.Header.Get("Authorization") != "Bearer openai-egress-secret" {
			s.t.Errorf("OpenAI catalog authorization=%q", r.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(w, `{"data":[{"id":"openai-catalog-model"}]}`)
	case r.Method == http.MethodGet && r.URL.Path == "/v1beta/models":
		if r.Header.Get("x-goog-api-key") != "gemini-egress-secret" {
			s.t.Errorf("Gemini catalog key=%q", r.Header.Get("x-goog-api-key"))
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("pageToken") == "" {
			_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-page-one","supportedGenerationMethods":["generateContent"]}],"nextPageToken":"gemini-next"}`)
		} else {
			_, _ = io.WriteString(w, `{"models":[{"name":"models/gemini-page-two","supportedGenerationMethods":["generateContent"]}]}`)
		}
	case r.URL.Path == "/v1/chat/completions":
		s.assertAuthorization(r, "Bearer openai-egress-secret")
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "hold-inflight") {
			s.inflightOnce.Do(func() { close(s.inflightReached) })
			<-s.inflightRelease
		}
		var payload struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(body, &payload)
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\ndata: {\"id\":\"chat\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, recoverySyntheticResponse)
	case r.URL.Path == "/v1/responses":
		s.assertAuthorization(r, "Bearer openai-egress-secret")
		var payload map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if string(payload["stream"]) == "true" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: response.output_item.added\ndata: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"message\",\"id\":\"item_1\",\"role\":\"assistant\",\"content\":[]}}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)
	case r.URL.Path == "/v1/messages/count_tokens":
		s.assertAuthorization(r, "Bearer anthropic-egress-secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":9}`)
	case r.URL.Path == "/v1/messages":
		s.assertAuthorization(r, "Bearer anthropic-egress-secret")
		var payload map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if string(payload["stream"]) == "true" {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"actual-claude\",\"stop_reason\":null,\"usage\":{\"input_tokens\":1,\"output_tokens\":0}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"OK"}],"model":"actual-claude","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	case strings.HasPrefix(r.URL.Path, "/v1beta/models/actual-gemini:streamGenerateContent"):
		s.assertGeminiKey(r)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"OK\"}]}}]}\n\ndata: {\"candidates\":[{\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":1,\"candidatesTokenCount\":1,\"totalTokenCount\":2}}\n\n")
	case strings.HasPrefix(r.URL.Path, "/v1beta/models/actual-gemini:generateContent"):
		s.assertGeminiKey(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
	default:
		s.t.Errorf("unexpected target request %s %s", r.Method, r.URL.String())
		http.Error(w, "unexpected", http.StatusNotFound)
	}
}

func (s *egressHTTPTarget) assertAuthorization(r *http.Request, want string) {
	if r.Header.Get("Authorization") != want || r.Header.Get("Proxy-Authorization") != "" {
		s.t.Errorf("target authorization=%q proxy=%q", r.Header.Get("Authorization"), r.Header.Get("Proxy-Authorization"))
	}
}

func (s *egressHTTPTarget) assertGeminiKey(r *http.Request) {
	if r.Header.Get("x-goog-api-key") != "gemini-egress-secret" || r.Header.Get("Authorization") != "" {
		s.t.Errorf("Gemini target key=%q authorization=%q", r.Header.Get("x-goog-api-key"), r.Header.Get("Authorization"))
	}
}

func (f *egressHTTPFixture) testExecutionCatalogHealthAndRecovery(t *testing.T) {
	requests := []struct {
		name, method, path, body, marker string
		anthropic                        bool
	}{
		{"chat json", http.MethodPost, "/v1/chat/completions", `{"model":"egress-chat","messages":[{"role":"user","content":"hi"}]}`, `"chat.completion"`, false},
		{"chat sse", http.MethodPost, "/v1/chat/completions", `{"model":"egress-chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`, "[DONE]", false},
		{"responses json", http.MethodPost, "/v1/responses", `{"model":"egress-responses","input":"hi"}`, `"status":"completed"`, false},
		{"responses sse", http.MethodPost, "/v1/responses", `{"model":"egress-responses","stream":true,"input":"hi"}`, "response.completed", false},
		{"messages json", http.MethodPost, "/v1/messages", `{"model":"egress-claude","max_tokens":8,"messages":[{"role":"user","content":"hi"}]}`, `"stop_reason":"end_turn"`, true},
		{"messages sse", http.MethodPost, "/v1/messages", `{"model":"egress-claude","max_tokens":8,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, "event: message_stop", true},
		{"count tokens", http.MethodPost, "/v1/messages/count_tokens", `{"model":"egress-claude","messages":[{"role":"user","content":"hi"}]}`, `"input_tokens":9`, true},
		{"gemini json", http.MethodPost, "/v1beta/models/egress-gemini:generateContent", `{"contents":[{"parts":[{"text":"hi"}]}]}`, `"finishReason":"STOP"`, false},
		{"gemini sse", http.MethodPost, "/v1beta/models/egress-gemini:streamGenerateContent?alt=sse", `{"contents":[{"parts":[{"text":"hi"}]}]}`, `"totalTokenCount":2`, false},
	}
	for _, test := range requests {
		t.Run(test.name, func(t *testing.T) {
			f.assertProxyUsed(t, func() {
				request, err := http.NewRequest(test.method, f.server.URL+test.path, strings.NewReader(test.body))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+f.employeeKey)
				request.Header.Set("Content-Type", "application/json")
				if test.anthropic {
					request.Header.Set("Anthropic-Version", "2023-06-01")
				}
				response, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				body := readBody(response)
				if response.StatusCode != http.StatusOK || !strings.Contains(body, test.marker) {
					t.Fatalf("status=%d body=%s", response.StatusCode, body)
				}
			})
		})
	}

	for _, key := range []string{"openai", "anthropic", "gemini"} {
		account := f.accounts[key]
		t.Run(key+" paged catalog", func(t *testing.T) {
			path := "/v1/models"
			wantPages := 1
			if key == "anthropic" {
				wantPages = 2
			}
			if key == "gemini" {
				path, wantPages = "/v1beta/models", 2
			}
			beforePages := f.targetState.pathCount(path)
			f.assertProxyUsed(t, func() {
				response := requestJSON(t, http.MethodPost, f.server.URL+"/admin/api/v1/upstreams/"+account.ID+"/discover-models", "", f.cookie, f.csrf, f.server.URL)
				body := readBody(response)
				if response.StatusCode != http.StatusOK || !strings.Contains(body, `"items"`) {
					t.Fatalf("catalog status=%d body=%s", response.StatusCode, body)
				}
			})
			if pages := f.targetState.pathCount(path) - beforePages; pages != wantPages {
				t.Fatalf("catalog pages=%d want %d", pages, wantPages)
			}
		})
	}
	for index, key := range []string{"openai", "anthropic", "gemini"} {
		account := f.accounts[key]
		operation := fmt.Sprintf("31000000-0000-4000-8000-%012d", index+1)
		t.Run(key+" health catalog", func(t *testing.T) {
			f.assertProxyUsed(t, func() {
				response := requestJSON(t, http.MethodPost, f.server.URL+"/admin/api/v1/upstreams/"+account.ID+"/tests", healthRequestBody(operation, account.Revision, "catalog"), f.cookie, f.csrf, f.server.URL)
				body := readBody(response)
				if response.StatusCode != http.StatusOK || !strings.Contains(body, `"result_code":"catalog_ok"`) {
					t.Fatalf("health status=%d body=%s", response.StatusCode, body)
				}
			})
		})
	}

	putModelAdmissionPool(t, f.server.URL, f.cookie, f.csrf, "egress-chat", f.accounts["openai"].ID, "actual-chat")
	f.app.cfg.AccountRecoveryEnabled = true
	if _, err := f.app.store.db.Exec(`UPDATE account_recovery_settings SET enabled=1,revision=revision+1,updated_at=? WHERE singleton=1`, formatAccountPoolTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	now := f.app.accountPool.clock.Now().UTC()
	state := accountRecoveryState{AccountID: f.accounts["openai"].ID, CooldownEventID: "cool_egress_http", OperationID: "32000000-0000-4000-8000-000000000001", RecoveryRevision: 1, PoolRevision: 1, AccountRevision: f.accounts["openai"].Revision, ProviderKind: "openai-compatible", SourceSnapshot: "api_key", PublicModel: "egress-chat", UpstreamModel: "actual-chat", Protocol: string(accounting.ProtocolOpenAIChatCompletions), State: recoveryRequired, NextProbeAt: now, CreatedAt: now, UpdatedAt: now}
	seedRecoveryExecutionState(t, f.app, state)
	f.assertProxyUsed(t, func() {
		code, receipt := f.app.executeRecoveryOperation(context.Background(), accountMaintenanceAcquireRequest{OperationID: state.OperationID, AccountID: state.AccountID, CooldownEventID: state.CooldownEventID, ExpectedPoolRevision: 1, ExpectedAccountRevision: state.AccountRevision, ExpectedRecoveryRevision: 1})
		if code != accountPoolReleased || receipt == nil {
			t.Fatalf("recovery code=%s receipt=%v", code, receipt != nil)
		}
	})
}

func (s *egressHTTPTarget) pathCount(path string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paths[path]
}

func (f *egressHTTPFixture) testFailureAndInflightSemantics(t *testing.T) {
	chat := func(body string) (int, string) {
		response := employeeRequest(t, http.MethodPost, f.server.URL+"/v1/chat/completions", body, f.employeeKey, context.Background())
		return response.StatusCode, readBody(response)
	}
	current := f.mustProxy(t)
	f.updateProxy(t, current, current.Port, false, outboundProxyCredentialKeep, nil)
	f.assertNoTargetOrProxy(t, func() {
		status, body := chat(`{"model":"egress-chat","messages":[{"role":"user","content":"disabled"}]}`)
		if status != http.StatusServiceUnavailable || !strings.Contains(body, "proxy_unavailable") && !strings.Contains(body, "no_available_route") {
			t.Fatalf("disabled status=%d body=%s", status, body)
		}
	})
	current = f.mustProxy(t)
	f.updateProxy(t, current, current.Port, true, outboundProxyCredentialKeep, nil)
	if _, err := f.app.store.db.Exec(`UPDATE outbound_proxies SET credential_ciphertext=X'0102' WHERE id=?`, f.proxyView.ID); err != nil {
		t.Fatal(err)
	}
	f.app.proxyClients.Invalidate(f.proxyView.ID)
	f.assertNoTargetOrProxy(t, func() {
		status, body := chat(`{"model":"egress-chat","messages":[{"role":"user","content":"bad-secret"}]}`)
		if status != http.StatusServiceUnavailable || !strings.Contains(body, "proxy_unavailable") && !strings.Contains(body, "no_available_route") {
			t.Fatalf("bad ciphertext status=%d body=%s", status, body)
		}
	})
	current = f.mustProxy(t)
	replacement := &outboundProxyCredential{username: egHTTPProxyUser, password: egHTTPProxyPassword}
	f.updateProxy(t, current, current.Port, true, outboundProxyCredentialReplace, replacement)
	badURL, _ := url.Parse(f.badProxy.server.URL)
	badPort, _ := strconv.Atoi(badURL.Port())
	current = f.mustProxy(t)
	f.updateProxy(t, current, badPort, true, outboundProxyCredentialKeep, nil)
	f.assertNoTarget(t, func() {
		status, _ := chat(`{"model":"egress-chat","messages":[{"role":"user","content":"bad-cert"}]}`)
		if status < 500 {
			t.Fatalf("bad certificate status=%d", status)
		}
	})
	goodURL, _ := url.Parse(f.proxy.server.URL)
	goodPort, _ := strconv.Atoi(goodURL.Port())
	current = f.mustProxy(t)
	f.updateProxy(t, current, goodPort, true, outboundProxyCredentialKeep, nil)
	f.clearAccountCooldown(t, f.accounts["openai"].ID)

	f.app.proxyClients.Invalidate(f.proxyView.ID)
	beforeCalls := f.targetState.calls.Load()
	result := make(chan struct {
		status int
		body   string
	}, 1)
	go func() {
		response, err := http.NewRequest(http.MethodPost, f.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"egress-chat","messages":[{"role":"user","content":"hold-inflight"}]}`))
		if err != nil {
			result <- struct {
				status int
				body   string
			}{0, err.Error()}
			return
		}
		response.Header.Set("Authorization", "Bearer "+f.employeeKey)
		response.Header.Set("Content-Type", "application/json")
		completed, err := http.DefaultClient.Do(response)
		if err != nil {
			result <- struct {
				status int
				body   string
			}{0, err.Error()}
			return
		}
		result <- struct {
			status int
			body   string
		}{completed.StatusCode, readBody(completed)}
	}()
	select {
	case <-f.targetState.inflightReached:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not enter dispatched target")
	}
	current = f.mustProxy(t)
	f.updateProxy(t, current, current.Port, false, outboundProxyCredentialKeep, nil)
	close(f.targetState.inflightRelease)
	select {
	case completed := <-result:
		if completed.status != http.StatusOK || !strings.Contains(completed.body, `"chat.completion"`) {
			t.Fatalf("inflight status=%d body=%s", completed.status, completed.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("inflight request did not finish")
	}
	if got := f.targetState.calls.Load() - beforeCalls; got != 1 {
		t.Fatalf("dispatched request target calls=%d want 1", got)
	}
}

func (f *egressHTTPFixture) assertProxyUsed(t *testing.T, action func()) {
	t.Helper()
	f.app.proxyClients.Invalidate(f.proxyView.ID)
	before := f.proxy.connects.Load()
	action()
	if f.proxy.connects.Load() <= before {
		t.Fatal("bound request did not create an HTTPS CONNECT tunnel")
	}
}

func (f *egressHTTPFixture) assertNoTargetOrProxy(t *testing.T, action func()) {
	t.Helper()
	beforeCalls, beforeConnects := f.targetState.calls.Load(), f.proxy.connects.Load()
	action()
	if f.targetState.calls.Load() != beforeCalls || f.proxy.connects.Load() != beforeConnects {
		t.Fatalf("failed-closed request reached target/proxy: calls %d->%d connects %d->%d", beforeCalls, f.targetState.calls.Load(), beforeConnects, f.proxy.connects.Load())
	}
}

func (f *egressHTTPFixture) assertNoTarget(t *testing.T, action func()) {
	t.Helper()
	before := f.targetState.calls.Load()
	action()
	if f.targetState.calls.Load() != before {
		t.Fatal("proxy TLS failure fell back to the target directly")
	}
}

func (f *egressHTTPFixture) mustProxy(t *testing.T) outboundProxyView {
	t.Helper()
	value, err := f.app.outboundProxies.Get(context.Background(), f.proxyView.ID)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (f *egressHTTPFixture) updateProxy(t *testing.T, current outboundProxyView, port int, enabled bool, credentialMode string, credential *outboundProxyCredential) outboundProxyView {
	t.Helper()
	f.app.admission.Lock()
	result, err := f.app.outboundProxies.Update(context.Background(), outboundProxyUpdateInput{ID: current.ID, ExpectedRevision: current.Revision, Name: current.Name, Scheme: current.Scheme, Host: current.Host, Port: port, AddressScope: current.AddressScope, Enabled: enabled, CredentialMode: credentialMode, Credential: credential})
	if err != nil || result.ConnectionChanged {
		f.app.proxyClients.Invalidate(current.ID)
	}
	f.app.admission.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return result.View
}

func (f *egressHTTPFixture) clearAccountCooldown(t *testing.T, accountID string) {
	t.Helper()
	var event string
	var revision int64
	if err := f.app.store.db.QueryRow(`SELECT COALESCE((SELECT event_id FROM account_pool_runtime_cooldowns WHERE account_id=?),(SELECT cooldown_event_id FROM account_recovery_states WHERE account_id=?),''),revision FROM upstreams WHERE id=?`, accountID, accountID, accountID).Scan(&event, &revision); err != nil {
		t.Fatal(err)
	}
	if event == "" {
		return
	}
	if result, _, _ := f.app.accountPool.clearCooldown(context.Background(), accountID, revision, event); result != cooldownCleared && result != cooldownAlreadyClear {
		t.Fatalf("clear cooldown=%s", result)
	}
}
