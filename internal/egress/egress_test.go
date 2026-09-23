package egress

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPSConnectClientEndToEndAndHeaderIsolation(t *testing.T) {
	pki := newTestPKI(t)
	targetRequests := make(chan http.Header, 2)
	target := startTLSTarget(t, pki.serverTLS(t, "target.test"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetRequests <- r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	proxy := startTLSProxy(t, pki.serverTLS(t, "proxy.test"), proxyBehavior{})
	config := loopbackProxyConfig(proxy.port())
	resolver := staticResolver(map[string][]netip.Addr{
		"proxy.test":  {netip.MustParseAddr("127.0.0.1")},
		"target.test": {netip.MustParseAddr("127.0.0.1")},
	})
	client := testClient(t, config, resolver, pki.roots, pki.roots)
	t.Cleanup(client.CloseIdleConnections)
	t.Setenv("HTTPS_PROXY", "https://environment-proxy.invalid:4443")

	request, err := http.NewRequest(http.MethodPost, "https://target.test:"+target.port()+"/v1/test", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer upstream-secret")
	request.Header.Set("X-Upstream-Token", "upstream-token-secret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil || string(body) != `{"ok":true}` {
		t.Fatalf("response body=%q read=%v close=%v", body, err, closeErr)
	}

	record := proxy.oneRecord(t)
	wantAuthority := net.JoinHostPort("127.0.0.1", target.port())
	if record.authority != wantAuthority {
		t.Fatalf("CONNECT authority=%q want=%q", record.authority, wantAuthority)
	}
	if got := record.header.Get("Proxy-Authorization"); got != proxyAuthorization(config) {
		t.Fatalf("proxy authorization mismatch got=%q", got)
	}
	for _, forbidden := range []string{"Authorization", "X-Upstream-Token", "Cookie", "Origin"} {
		if value := record.header.Get(forbidden); value != "" {
			t.Fatalf("CONNECT leaked %s=%q", forbidden, value)
		}
	}
	select {
	case headers := <-targetRequests:
		if headers.Get("Authorization") != "Bearer upstream-secret" || headers.Get("X-Upstream-Token") != "upstream-token-secret" {
			t.Fatalf("target authentication headers missing: %#v", headers)
		}
		if value := headers.Get("Proxy-Authorization"); value != "" {
			t.Fatalf("proxy authorization reached target: %q", value)
		}
	case <-time.After(time.Second):
		t.Fatal("target request was not observed")
	}
}

func TestHTTPSConnectClientRejectsRedirectAndNeverFallsBack(t *testing.T) {
	pki := newTestPKI(t)
	var targetCalls atomic.Int32
	target := startTLSTarget(t, pki.serverTLS(t, "target.test"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		if r.URL.Path == "/first" {
			http.Redirect(w, r, "/second", http.StatusTemporaryRedirect)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	resolver := staticResolver(map[string][]netip.Addr{
		"proxy.test":  {netip.MustParseAddr("127.0.0.1")},
		"target.test": {netip.MustParseAddr("127.0.0.1")},
	})

	t.Run("redirect", func(t *testing.T) {
		proxy := startTLSProxy(t, pki.serverTLS(t, "proxy.test"), proxyBehavior{})
		client := testClient(t, loopbackProxyConfig(proxy.port()), resolver, pki.roots, pki.roots)
		defer client.CloseIdleConnections()
		response, err := client.Do(mustRequest(t, "https://target.test:"+target.port()+"/first"))
		if err != nil {
			t.Fatalf("redirect response: %v", err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusTemporaryRedirect || targetCalls.Load() != 1 {
			t.Fatalf("status=%d target calls=%d", response.StatusCode, targetCalls.Load())
		}
	})

	t.Run("CONNECT rejected", func(t *testing.T) {
		proxySecret := "proxy-error-body-secret"
		proxy := startTLSProxy(t, pki.serverTLS(t, "proxy.test"), proxyBehavior{status: http.StatusProxyAuthRequired, body: proxySecret})
		client := testClient(t, loopbackProxyConfig(proxy.port()), resolver, pki.roots, pki.roots)
		defer client.CloseIdleConnections()
		_, err := client.Do(mustRequest(t, "https://target.test:"+target.port()+"/must-not-arrive"))
		if !errors.Is(err, ErrProxyRejected) {
			t.Fatalf("error=%v want proxy rejected", err)
		}
		if strings.Contains(err.Error(), proxySecret) {
			t.Fatalf("proxy error body leaked: %v", err)
		}
		if targetCalls.Load() != 1 {
			t.Fatalf("target received fallback request, calls=%d", targetCalls.Load())
		}
	})

	client := testClient(t, loopbackProxyConfig("443"), resolver, pki.roots, pki.roots)
	defer client.CloseIdleConnections()
	request, err := http.NewRequest(http.MethodGet, "http://target.test/plaintext", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(request); !errors.Is(err, ErrHTTPSRequired) {
		t.Fatalf("HTTP target error=%v", err)
	}
}

func TestHTTPSConnectClientAddressBoundariesAndRebinding(t *testing.T) {
	config := ProxyConfig{ProxyID: "proxy-address", ConnectionRevision: 1, Host: "proxy.test", Port: 443, Scope: ScopePublic}
	tests := []struct {
		name      string
		resolver  addressResolver
		config    ProxyConfig
		wantError error
	}{
		{
			name: "private target",
			resolver: staticResolver(map[string][]netip.Addr{
				"target.test": {netip.MustParseAddr("10.0.0.2")},
				"proxy.test":  {netip.MustParseAddr("192.0.2.20")},
			}),
			config: config, wantError: ErrAddressNotPermitted,
		},
		{
			name: "mixed target",
			resolver: staticResolver(map[string][]netip.Addr{
				"target.test": {netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("10.0.0.2")},
				"proxy.test":  {netip.MustParseAddr("192.0.2.20")},
			}),
			config: config, wantError: ErrAddressNotPermitted,
		},
		{
			name: "proxy scope mismatch",
			resolver: staticResolver(map[string][]netip.Addr{
				"target.test": {netip.MustParseAddr("192.0.2.10")},
				"proxy.test":  {netip.MustParseAddr("10.0.0.3")},
			}),
			config: config, wantError: ErrAddressNotPermitted,
		},
		{
			name: "mixed proxy scope",
			resolver: staticResolver(map[string][]netip.Addr{
				"target.test": {netip.MustParseAddr("192.0.2.10")},
				"proxy.test":  {netip.MustParseAddr("192.0.2.20"), netip.MustParseAddr("10.0.0.3")},
			}),
			config: config, wantError: ErrAddressNotPermitted,
		},
		{
			name: "link local proxy",
			resolver: staticResolver(map[string][]netip.Addr{
				"target.test": {netip.MustParseAddr("192.0.2.10")},
				"proxy.test":  {netip.MustParseAddr("169.254.169.254")},
			}),
			config: config, wantError: ErrAddressNotPermitted,
		},
		{
			name: "CGNAT proxy",
			resolver: staticResolver(map[string][]netip.Addr{
				"target.test": {netip.MustParseAddr("192.0.2.10")},
				"proxy.test":  {netip.MustParseAddr("100.100.100.200")},
			}),
			config: config, wantError: ErrAddressNotPermitted,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var dials atomic.Int32
			client, err := newHTTPSConnectClient(test.config, dependencies{
				resolver: test.resolver,
				dialContext: func(context.Context, string, string) (net.Conn, error) {
					dials.Add(1)
					return nil, errors.New("unexpected dial")
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer client.CloseIdleConnections()
			_, err = client.Do(mustRequest(t, "https://target.test/path"))
			if !errors.Is(err, test.wantError) || dials.Load() != 0 {
				t.Fatalf("error=%v dials=%d", err, dials.Load())
			}
		})
	}

	pki := newTestPKI(t)
	target := startTLSTarget(t, pki.serverTLS(t, "target.test"), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	proxy := startTLSProxy(t, pki.serverTLS(t, "proxy.test"), proxyBehavior{})
	resolver := &sequenceResolver{answers: map[string][][]netip.Addr{
		"target.test": {{netip.MustParseAddr("127.0.0.1")}, {netip.MustParseAddr("127.0.0.1")}},
		"proxy.test":  {{netip.MustParseAddr("127.0.0.1")}, {netip.MustParseAddr("192.0.2.50")}},
	}}
	client := testClient(t, loopbackProxyConfig(proxy.port()), resolver, pki.roots, pki.roots)
	response, err := client.Do(mustRequest(t, "https://target.test:"+target.port()+"/first"))
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	_ = response.Body.Close()
	client.CloseIdleConnections()
	_, err = client.Do(mustRequest(t, "https://target.test:"+target.port()+"/second"))
	if !errors.Is(err, ErrAddressNotPermitted) {
		t.Fatalf("scope rebinding error=%v", err)
	}
}

func TestHTTPSConnectClientTLSAndCancellation(t *testing.T) {
	trusted := newTestPKI(t)
	untrusted := newTestPKI(t)
	resolver := staticResolver(map[string][]netip.Addr{
		"proxy.test":  {netip.MustParseAddr("127.0.0.1")},
		"target.test": {netip.MustParseAddr("127.0.0.1")},
	})

	t.Run("wrong proxy certificate", func(t *testing.T) {
		proxy := startTLSProxy(t, untrusted.serverTLS(t, "proxy.test"), proxyBehavior{})
		client := testClient(t, loopbackProxyConfig(proxy.port()), resolver, trusted.roots, trusted.roots)
		defer client.CloseIdleConnections()
		_, err := client.Do(mustRequest(t, "https://target.test:443/path"))
		if err == nil || errors.Is(err, ErrAddressNotPermitted) {
			t.Fatalf("wrong proxy certificate error=%v", err)
		}
	})

	t.Run("wrong target certificate", func(t *testing.T) {
		target := startTLSTarget(t, untrusted.serverTLS(t, "target.test"), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		proxy := startTLSProxy(t, trusted.serverTLS(t, "proxy.test"), proxyBehavior{})
		client := testClient(t, loopbackProxyConfig(proxy.port()), resolver, trusted.roots, trusted.roots)
		defer client.CloseIdleConnections()
		_, err := client.Do(mustRequest(t, "https://target.test:"+target.port()+"/path"))
		if err == nil {
			t.Fatal("untrusted target certificate was accepted")
		}
	})

	t.Run("TLS below 1.2", func(t *testing.T) {
		oldTLS := trusted.serverTLS(t, "proxy.test")
		oldTLS.MaxVersion = tls.VersionTLS11
		proxy := startTLSProxy(t, oldTLS, proxyBehavior{})
		client := testClient(t, loopbackProxyConfig(proxy.port()), resolver, trusted.roots, trusted.roots)
		defer client.CloseIdleConnections()
		_, err := client.Do(mustRequest(t, "https://target.test:443/path"))
		if err == nil {
			t.Fatal("legacy proxy TLS was accepted")
		}
	})

	t.Run("cancel stalled CONNECT", func(t *testing.T) {
		proxy := startTLSProxy(t, trusted.serverTLS(t, "proxy.test"), proxyBehavior{stall: true})
		client := testClient(t, loopbackProxyConfig(proxy.port()), resolver, trusted.roots, trusted.roots)
		defer client.CloseIdleConnections()
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		request := mustRequest(t, "https://target.test:443/path").WithContext(ctx)
		started := time.Now()
		_, err := client.Do(request)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 2*time.Second {
			t.Fatalf("cancel error=%v elapsed=%s", err, time.Since(started))
		}
	})
}

func TestProbeTLSUsesTunnelWithoutSendingHTTP(t *testing.T) {
	pki := newTestPKI(t)
	var targetHTTPCalls atomic.Int32
	target := startTLSTarget(t, pki.serverTLS(t, "target.test"), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetHTTPCalls.Add(1)
	}))
	proxy := startTLSProxy(t, pki.serverTLS(t, "proxy.test"), proxyBehavior{})
	resolver := staticResolver(map[string][]netip.Addr{
		"proxy.test":  {netip.MustParseAddr("127.0.0.1")},
		"target.test": {netip.MustParseAddr("127.0.0.1")},
	})
	client := testClient(t, loopbackProxyConfig(proxy.port()), resolver, pki.roots, pki.roots)
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.ProbeTLS(ctx, "target.test", target.port()); err != nil {
		t.Fatalf("ProbeTLS: %v", err)
	}
	record := proxy.oneRecord(t)
	if record.authority != net.JoinHostPort("127.0.0.1", target.port()) || record.header.Get("Proxy-Authorization") == "" {
		t.Fatalf("probe CONNECT record=%+v", record)
	}
	if targetHTTPCalls.Load() != 0 {
		t.Fatalf("probe sent HTTP request, calls=%d", targetHTTPCalls.Load())
	}
}

func TestCloseIdleConnectionsClosesActiveTunnelWhenReturned(t *testing.T) {
	pki := newTestPKI(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	target := startTLSTarget(t, pki.serverTLS(t, "target.test"), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		_, _ = io.WriteString(w, "ok")
	}))
	proxy := startTLSProxy(t, pki.serverTLS(t, "proxy.test"), proxyBehavior{})
	resolver := staticResolver(map[string][]netip.Addr{
		"proxy.test":  {netip.MustParseAddr("127.0.0.1")},
		"target.test": {netip.MustParseAddr("127.0.0.1")},
	})
	client := testClient(t, loopbackProxyConfig(proxy.port()), resolver, pki.roots, pki.roots)
	defer client.CloseIdleConnections()
	firstDone := make(chan error, 1)
	firstRequest := mustRequest(t, "https://target.test:"+target.port()+"/first")
	go func() {
		response, err := client.Do(firstRequest)
		if err == nil {
			_, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if readErr != nil {
				err = readErr
			} else if closeErr != nil {
				err = closeErr
			}
		}
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not start")
	}
	_ = proxy.oneRecord(t)
	client.CloseIdleConnections()
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first request: %v", err)
	}
	response, err := client.Do(mustRequest(t, "https://target.test:"+target.port()+"/second"))
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	_, readErr := io.ReadAll(response.Body)
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("second response read=%v close=%v", readErr, closeErr)
	}
	_ = proxy.oneRecord(t)
	if calls.Load() != 2 {
		t.Fatalf("target calls=%d", calls.Load())
	}
}

func TestConnectResponseBoundsAndBufferedBytes(t *testing.T) {
	t.Run("buffered bytes", func(t *testing.T) {
		clientSide, serverSide := net.Pipe()
		defer clientSide.Close()
		defer serverSide.Close()
		done := make(chan error, 1)
		go func() {
			_, err := io.WriteString(serverSide, "HTTP/1.1 200 Connection Established\r\nX-Test: yes\r\n\r\nXYZ")
			done <- err
		}()
		request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: "192.0.2.1:443"}, Host: "192.0.2.1:443"}
		reader, response, err := readConnectResponse(clientSide, request)
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("response=%v error=%v", response, err)
		}
		connection := &bufferedConn{Conn: clientSide, reader: reader}
		buffer := make([]byte, 3)
		if _, err := io.ReadFull(connection, buffer); err != nil || string(buffer) != "XYZ" {
			t.Fatalf("buffer=%q error=%v", buffer, err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})

	t.Run("fragment ending in CRLF is not an empty line", func(t *testing.T) {
		clientSide, serverSide := net.Pipe()
		defer clientSide.Close()
		done := make(chan struct{})
		go func() {
			defer close(done)
			// The status line leaves 4079 bytes in the 4096-byte reader. This
			// header fills those bytes exactly, so ReadSlice returns its CRLF
			// as a separate fragment. It is still a non-empty logical line.
			value := strings.Repeat("a", 4071)
			_, _ = io.WriteString(serverSide, "HTTP/1.1 200 OK\r\nX-Long: "+value+"\r\nX-Next: yes\r\n\r\nTUNNEL")
			_ = serverSide.Close()
		}()
		request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: "192.0.2.1:443"}, Host: "192.0.2.1:443"}
		reader, response, err := readConnectResponse(clientSide, request)
		if err != nil || response.Header.Get("X-Next") != "yes" {
			t.Fatalf("response=%v error=%v", response, err)
		}
		buffer := make([]byte, len("TUNNEL"))
		if _, err := io.ReadFull(reader, buffer); err != nil || string(buffer) != "TUNNEL" {
			t.Fatalf("buffer=%q error=%v", buffer, err)
		}
		<-done
	})

	t.Run("oversized header", func(t *testing.T) {
		clientSide, serverSide := net.Pipe()
		defer clientSide.Close()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = io.WriteString(serverSide, "HTTP/1.1 200 OK\r\nX-Large: "+strings.Repeat("a", connectHeaderLimit)+"\r\n\r\n")
			_ = serverSide.Close()
		}()
		request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: "192.0.2.1:443"}, Host: "192.0.2.1:443"}
		_, _, err := readConnectResponse(clientSide, request)
		if !errors.Is(err, ErrConnectHeaderLarge) {
			t.Fatalf("error=%v", err)
		}
		_ = clientSide.Close()
		<-done
	})
}

func TestClientCacheSeparatesConnectionRevisions(t *testing.T) {
	cache, err := newClientCache(2, dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	first := ProxyConfig{ProxyID: "proxy-cache", ConnectionRevision: 1, Host: "proxy.test", Port: 443, Scope: ScopePublic, Credentials: mustCredentials(t, "user", "one")}
	firstClient, err := cache.Client(first)
	if err != nil {
		t.Fatal(err)
	}
	again, err := cache.Client(first)
	if err != nil || again != firstClient {
		t.Fatalf("same revision client=%p first=%p error=%v", again, firstClient, err)
	}
	conflict := first
	conflict.Credentials = mustCredentials(t, "user", "changed-without-revision")
	if _, err := cache.Client(conflict); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("conflicting key error=%v", err)
	}
	second := first
	second.ConnectionRevision = 2
	second.Credentials = mustCredentials(t, "user", "two")
	secondClient, err := cache.Client(second)
	if err != nil || secondClient == firstClient || secondClient.CacheKey().ConnectionRevision != 2 {
		t.Fatalf("second revision client=%p first=%p key=%+v error=%v", secondClient, firstClient, secondClient.CacheKey(), err)
	}
	cache.Invalidate(first.ProxyID)
	recreated, err := cache.Client(second)
	if err != nil || recreated == secondClient {
		t.Fatalf("invalidate client=%p previous=%p error=%v", recreated, secondClient, err)
	}
	cache.Close()
	if _, err := cache.Client(first); !errors.Is(err, ErrCacheClosed) {
		t.Fatalf("closed cache error=%v", err)
	}
}

func TestProxyConfigValidationIsBoundedAndRedacted(t *testing.T) {
	valid := ProxyConfig{ProxyID: "proxy-valid", ConnectionRevision: 1, Host: "proxy.example", Port: 443, Scope: ScopePublic}
	if _, err := NewHTTPSConnectClient(valid); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	invalid := []ProxyConfig{
		{},
		{ProxyID: "proxy", ConnectionRevision: 1, Host: "https://proxy.example/path", Port: 443, Scope: ScopePublic},
		{ProxyID: "proxy", ConnectionRevision: 1, Host: "proxy.example", Port: 443, Scope: "other"},
		{ProxyID: "proxy", ConnectionRevision: 1, Host: "proxy.example", Port: 443, Scope: ScopePublic, Credentials: BasicCredentials{username: "bad:name"}},
		{ProxyID: "proxy", ConnectionRevision: 1, Host: "proxy.example", Port: 443, Scope: ScopePublic, Credentials: BasicCredentials{password: "secret-without-user"}},
	}
	for index, item := range invalid {
		if _, err := NewHTTPSConnectClient(item); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid[%d] error=%v", index, err)
		}
	}
	secret := "proxy-password-must-not-appear"
	item := valid
	item.Credentials = mustCredentials(t, "user", secret)
	item.Host = "unresolvable.invalid"
	if err := ValidateProxyAddress(context.Background(), item); err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked secret or unexpectedly nil: %v", err)
	}
	for _, rendered := range []string{
		fmt.Sprintf("%v", item), fmt.Sprintf("%+v", item), fmt.Sprintf("%#v", item),
		fmt.Sprintf("%v", item.Credentials), fmt.Sprintf("%d", item.Credentials), fmt.Sprintf("%+v", struct{ Credentials BasicCredentials }{item.Credentials}),
	} {
		if strings.Contains(rendered, secret) || strings.Contains(rendered, "user") {
			t.Fatalf("formatted config leaked credentials: %q", rendered)
		}
	}
	secretClient, err := NewHTTPSConnectClient(item)
	if err != nil {
		t.Fatal(err)
	}
	defer secretClient.CloseIdleConnections()
	for _, rendered := range []string{fmt.Sprintf("%v", secretClient), fmt.Sprintf("%+v", secretClient), fmt.Sprintf("%#v", secretClient)} {
		if strings.Contains(rendered, secret) || strings.Contains(rendered, "user") {
			t.Fatalf("formatted client leaked credentials: %q", rendered)
		}
	}
	if !proxyAddressAllowed(netip.MustParseAddr("fd00::1"), ScopePrivate, false) {
		t.Fatal("IPv6 ULA was not accepted for private proxy scope")
	}
	if proxyAddressAllowed(netip.MustParseAddr("fe80::1"), ScopePrivate, false) || targetAddressAllowed(netip.MustParseAddr("fd00::1"), false) {
		t.Fatal("link-local proxy or private target was accepted")
	}
	for _, invalid := range [][2]string{{"user\u000bname", "password"}, {"username", "pass\u007fword"}, {string([]byte{0xff}), "password"}, {"username", string([]byte{0xff})}} {
		if _, err := NewBasicCredentials(invalid[0], invalid[1]); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid credentials accepted: %q/%q err=%v", invalid[0], invalid[1], err)
		}
	}
}

func TestClientRejectsAmbiguousRequestAuthorityAndProxyHeader(t *testing.T) {
	client, err := NewHTTPSConnectClient(ProxyConfig{ProxyID: "proxy-request", ConnectionRevision: 1, Host: "proxy.example", Port: 443, Scope: ScopePublic})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	tests := []struct {
		name string
		req  *http.Request
		want error
	}{
		{name: "userinfo", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "target.example", User: url.UserPassword("name", "secret")}, Header: make(http.Header)}, want: ErrHTTPSRequired},
		{name: "fragment", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "target.example", Fragment: "hidden"}, Header: make(http.Header)}, want: ErrInvalidConfig},
		{name: "invalid host", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "bad host"}, Header: make(http.Header)}, want: ErrInvalidConfig},
		{name: "noncanonical port", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "target.example:0443"}, Header: make(http.Header)}, want: ErrInvalidConfig},
		{name: "trailing empty port", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "target.example:"}, Header: make(http.Header)}, want: ErrInvalidConfig},
		{name: "host override", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "target.example"}, Host: "other.example", Header: make(http.Header)}, want: ErrInvalidConfig},
		{name: "proxy auth header", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "target.example"}, Header: http.Header{"Proxy-Authorization": {"Basic should-not-forward"}}}, want: ErrForbiddenHeader},
		{name: "empty lowercase proxy auth header", req: &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "target.example"}, Header: http.Header{"proxy-authorization": {""}}}, want: ErrForbiddenHeader},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.Do(test.req); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestClientErrorsNeverContainTargetSecrets(t *testing.T) {
	secret := "target-query-secret"
	config := ProxyConfig{ProxyID: "proxy-errors", ConnectionRevision: 1, Host: "proxy.test", Port: 443, Scope: ScopePublic}
	client, err := newHTTPSConnectClient(config, dependencies{
		resolver: staticResolver(map[string][]netip.Addr{
			"target.test": {netip.MustParseAddr("192.0.2.10")},
			"proxy.test":  {netip.MustParseAddr("192.0.2.20")},
		}),
		dialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("dial failure with internal detail")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	request := mustRequest(t, "https://target.test/path?token="+secret)
	_, err = client.Do(request)
	if !errors.Is(err, ErrProxyUnavailable) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "target.test") || strings.Contains(err.Error(), "internal detail") {
		t.Fatalf("unsanitized error: %v", err)
	}
	userinfo := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "https", Host: "target.test", User: url.UserPassword("user", secret)}, Header: make(http.Header)}
	_, err = client.Do(userinfo)
	if !errors.Is(err, ErrHTTPSRequired) || strings.Contains(err.Error(), secret) {
		t.Fatalf("userinfo error leaked: %v", err)
	}
}

type staticResolver map[string][]netip.Addr

func (resolver staticResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	addresses := resolver[host]
	if len(addresses) == 0 {
		return nil, errors.New("not found")
	}
	return append([]netip.Addr(nil), addresses...), nil
}

type sequenceResolver struct {
	mu      sync.Mutex
	answers map[string][][]netip.Addr
}

func (resolver *sequenceResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	answers := resolver.answers[host]
	if len(answers) == 0 {
		return nil, errors.New("not found")
	}
	answer := answers[0]
	if len(answers) > 1 {
		resolver.answers[host] = answers[1:]
	}
	return append([]netip.Addr(nil), answer...), nil
}

type testPKI struct {
	t        *testing.T
	ca       *x509.Certificate
	key      *ecdsa.PrivateKey
	roots    *x509.CertPool
	serialID int64
}

func newTestPKI(t *testing.T) *testPKI {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "egress test CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return &testPKI{t: t, ca: certificate, key: key, roots: roots, serialID: 1}
}

func (pki *testPKI) serverTLS(t *testing.T, host string) *tls.Config {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pki.serialID++
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(pki.serialID), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, pki.ca, &key.PublicKey, pki.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
}

type testTarget struct {
	listener net.Listener
	server   *http.Server
}

func startTLSTarget(t *testing.T, config *tls.Config, handler http.Handler) *testTarget {
	t.Helper()
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	listener := tls.NewListener(base, config)
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	target := &testTarget{listener: listener, server: server}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	})
	return target
}

func (target *testTarget) port() string {
	_, port, _ := net.SplitHostPort(target.listener.Addr().String())
	return port
}

type proxyBehavior struct {
	status int
	stall  bool
	body   string
}

type proxyRecord struct {
	authority string
	header    http.Header
}

type testProxy struct {
	listener net.Listener
	behavior proxyBehavior
	records  chan proxyRecord
	mu       sync.Mutex
	active   map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func startTLSProxy(t *testing.T, config *tls.Config, behavior proxyBehavior) *testProxy {
	t.Helper()
	base, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &testProxy{listener: tls.NewListener(base, config), behavior: behavior, records: make(chan proxyRecord, 16), active: make(map[net.Conn]struct{})}
	proxy.wg.Add(1)
	go proxy.serve()
	t.Cleanup(proxy.close)
	return proxy
}

func (proxy *testProxy) serve() {
	defer proxy.wg.Done()
	for {
		connection, err := proxy.listener.Accept()
		if err != nil {
			return
		}
		proxy.mu.Lock()
		proxy.active[connection] = struct{}{}
		proxy.mu.Unlock()
		proxy.wg.Add(1)
		go proxy.handle(connection)
	}
}

func (proxy *testProxy) handle(connection net.Conn) {
	defer proxy.wg.Done()
	defer func() {
		_ = connection.Close()
		proxy.mu.Lock()
		delete(proxy.active, connection)
		proxy.mu.Unlock()
	}()
	_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
	reader := bufio.NewReader(connection)
	request, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	proxy.records <- proxyRecord{authority: request.Host, header: request.Header.Clone()}
	if proxy.behavior.stall {
		_, _ = io.Copy(io.Discard, connection)
		return
	}
	status := proxy.behavior.status
	if status == 0 {
		status = http.StatusOK
	}
	if status < 200 || status >= 300 {
		_, _ = fmt.Fprintf(connection, "HTTP/1.1 %d Rejected\r\nContent-Length: %d\r\n\r\n%s", status, len(proxy.behavior.body), proxy.behavior.body)
		return
	}
	upstream, err := net.DialTimeout("tcp", request.Host, time.Second)
	if err != nil {
		_, _ = io.WriteString(connection, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer upstream.Close()
	_, _ = io.WriteString(connection, "HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = connection.SetDeadline(time.Time{})
	done := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(upstream, reader)
		done <- struct{}{}
	}()
	_, _ = io.Copy(connection, upstream)
	_ = connection.Close()
	<-done
}

func (proxy *testProxy) port() string {
	_, port, _ := net.SplitHostPort(proxy.listener.Addr().String())
	return port
}

func (proxy *testProxy) oneRecord(t *testing.T) proxyRecord {
	t.Helper()
	select {
	case record := <-proxy.records:
		return record
	case <-time.After(time.Second):
		t.Fatal("CONNECT was not observed")
		return proxyRecord{}
	}
}

func (proxy *testProxy) close() {
	_ = proxy.listener.Close()
	proxy.mu.Lock()
	for connection := range proxy.active {
		_ = connection.Close()
	}
	proxy.mu.Unlock()
	proxy.wg.Wait()
}

func loopbackProxyConfig(port string) ProxyConfig {
	value, _ := net.LookupPort("tcp", port)
	return ProxyConfig{
		ProxyID: "proxy-test", ConnectionRevision: 1, Host: "proxy.test", Port: uint16(value), Scope: ScopePrivate,
		Credentials: mustCredentialsValue("proxy-user", "proxy-password"), AllowLoopbackForTesting: true,
	}
}

func mustCredentials(t *testing.T, username, password string) BasicCredentials {
	t.Helper()
	credentials, err := NewBasicCredentials(username, password)
	if err != nil {
		t.Fatal(err)
	}
	return credentials
}

func mustCredentialsValue(username, password string) BasicCredentials {
	credentials, err := NewBasicCredentials(username, password)
	if err != nil {
		panic(err)
	}
	return credentials
}

func testClient(t *testing.T, config ProxyConfig, resolver addressResolver, proxyRoots, targetRoots *x509.CertPool) *Client {
	t.Helper()
	dialer := &net.Dialer{Timeout: time.Second}
	client, err := newHTTPSConnectClient(config, dependencies{
		resolver: resolver, dialContext: dialer.DialContext,
		proxyTLS:       &tls.Config{RootCAs: proxyRoots, MinVersion: tls.VersionTLS12},
		targetTLS:      &tls.Config{RootCAs: targetRoots, MinVersion: tls.VersionTLS12},
		connectTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func mustRequest(t *testing.T, target string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	return request
}
