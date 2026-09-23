package service

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
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

	"cpacloud.local/server/internal/egress"
)

// This subprocess verifies the service worker through the production TLS and
// CONNECT implementation. The synthetic root is process-local; product trust
// settings and the operating-system trust store are never changed.
func TestOutboundProxyTestProductionDoubleTLSWithoutTargetHTTP(t *testing.T) {
	if os.Getenv("CPA_TEST_PROXY_HANDSHAKE_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestOutboundProxyTestProductionDoubleTLSWithoutTargetHTTP$", "-test.count=1", "-test.timeout=50s")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GODEBUG=") && !strings.HasPrefix(entry, "CPA_TEST_PROXY_HANDSHAKE_CHILD=") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "GODEBUG=x509usefallbackroots=1", "CPA_TEST_PROXY_HANDSHAKE_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated handshake worker failed: %v\n%s", err, output)
		}
		return
	}

	root, signer, roots := newOutboundProxyTestCA(t)
	x509.SetFallbackRoots(roots)
	certificate := newOutboundProxyTestCertificate(t, root, signer, true)
	var targetHTTP atomic.Int32
	target := httptest.NewUnstartedServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetHTTP.Add(1)
	}))
	target.Config.ErrorLog = log.New(io.Discard, "", 0)
	target.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	target.StartTLS()
	t.Cleanup(target.Close)
	targetURL, _ := url.Parse(target.URL)

	connectHeaders := make(chan http.Header, 1)
	var connectionsMu sync.Mutex
	var connections []net.Conn
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != targetURL.Host {
			http.Error(w, "unexpected target", http.StatusBadRequest)
			return
		}
		connectHeaders <- r.Header.Clone()
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
		connectionsMu.Lock()
		connections = append(connections, connection, upstream)
		connectionsMu.Unlock()
		if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			connection.Close()
			upstream.Close()
			return
		}
		if err := buffer.Flush(); err != nil {
			connection.Close()
			upstream.Close()
			return
		}
		go func() { _, _ = io.Copy(upstream, buffer); _ = upstream.Close(); _ = connection.Close() }()
		_, _ = io.Copy(connection, upstream)
		_ = connection.Close()
		_ = upstream.Close()
	}))
	proxy.Config.ErrorLog = log.New(io.Discard, "", 0)
	proxy.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}
	proxy.StartTLS()
	t.Cleanup(func() {
		connectionsMu.Lock()
		for _, connection := range connections {
			_ = connection.Close()
		}
		connectionsMu.Unlock()
		proxy.Close()
	})
	proxyURL, _ := url.Parse(proxy.URL)
	proxyPort, _ := strconv.Atoi(proxyURL.Port())

	base := newOutboundProxyTestStore(t)
	base.store.allowLoopbackForTesting = true
	credential := outboundProxyCredential{username: "handshake-user", password: "handshake-proxy-secret"}
	proxyView, err := base.store.Create(context.Background(), outboundProxyCreateInput{OperationID: "24000000-0000-4000-8000-000000000001", Name: "Synthetic TLS proxy", Scheme: "https", Host: proxyURL.Hostname(), Port: proxyPort, AddressScope: "private", Enabled: true, Credential: &credential})
	if err != nil {
		t.Fatal(err)
	}
	insertProxyTestUpstream(t, base.db, "ups_tls_handshake", "openai-compatible", target.URL+"/v1", 1)
	binding, err := base.store.SetBinding(context.Background(), upstreamProxyBindingInput{UpstreamID: "ups_tls_handshake", ExpectedUpstreamRevision: 1, ProxyID: proxyView.ID, ExpectedProxyRevision: 1, Bind: true})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := egress.NewClientCache(4)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{AllowLoopbackUpstream: true}, store: &store{db: base.db}, secrets: base.secrets, outboundProxies: base.store, proxyClients: cache}
	coordinator, err := newOutboundProxyTestCoordinator(app)
	if err != nil {
		cache.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { coordinator.Close(); cache.Close() })
	input := outboundProxyTestInput{OperationID: "24000000-0000-4000-8000-000000000002", ExpectedProxyRevision: 1, ExpectedConnectionRevision: 1, UpstreamID: "ups_tls_handshake", ExpectedUpstreamRevision: binding.UpstreamRevision}
	if _, _, err := coordinator.create(context.Background(), proxyView.ID, input); err != nil {
		t.Fatal(err)
	}
	finished := waitOutboundProxyTest(t, coordinator, input.OperationID, outboundProxyTestCompleted)
	if finished.ResultCode == nil || *finished.ResultCode != outboundProxyTestHandshakeOK {
		t.Fatalf("operation=%+v", finished)
	}
	select {
	case headers := <-connectHeaders:
		want := "Basic " + base64.StdEncoding.EncodeToString([]byte("handshake-user:handshake-proxy-secret"))
		if headers.Get("Proxy-Authorization") != want || headers.Get("Authorization") != "" {
			t.Fatal("CONNECT credential isolation failed")
		}
	default:
		t.Fatal("proxy was bypassed")
	}
	if targetHTTP.Load() != 0 {
		t.Fatalf("handshake sent %d target HTTP requests", targetHTTP.Load())
	}
}
