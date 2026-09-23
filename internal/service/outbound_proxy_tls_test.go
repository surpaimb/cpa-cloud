package service

// This isolated test process exercises the production egress constructor with
// verified synthetic roots. It changes neither the operating-system trust store
// nor the product's TLS configuration surface.
import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"cpacloud.local/server/internal/egress"
)

func TestOutboundProxyProductionTLSChain(t *testing.T) {
	if os.Getenv("CPA_TEST_EGRESS_TLS_CHILD") != "1" {
		executable, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, executable, "-test.run=^TestOutboundProxyProductionTLSChain$", "-test.count=1", "-test.timeout=50s")
		for _, entry := range os.Environ() {
			if !strings.HasPrefix(entry, "GODEBUG=") && !strings.HasPrefix(entry, "CPA_TEST_EGRESS_TLS_CHILD=") {
				command.Env = append(command.Env, entry)
			}
		}
		command.Env = append(command.Env, "GODEBUG=x509usefallbackroots=1", "CPA_TEST_EGRESS_TLS_CHILD=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("isolated TLS verification failed: %v\n%s", err, output)
		}
		return
	}
	root, signer, roots := newOutboundProxyTestCA(t)
	x509.SetFallbackRoots(roots)
	trusted := newOutboundProxyTestCertificate(t, root, signer, true)
	wrongHostname := newOutboundProxyTestCertificate(t, root, signer, false)

	for _, test := range []struct {
		name          string
		proxy, target tls.Certificate
		succeeds      bool
	}{
		{"verified double TLS", trusted, trusted, true},
		{"wrong proxy hostname", wrongHostname, trusted, false},
		{"wrong target hostname", trusted, wrongHostname, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			targetHeaders := make(chan http.Header, 1)
			target := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				targetHeaders <- r.Header.Clone()
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"ok":true}`)
			}))
			target.Config.ErrorLog = log.New(io.Discard, "", 0)
			target.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{test.target}}
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
				go func() { _, _ = io.Copy(upstream, buffer); upstream.Close(); connection.Close() }()
				_, _ = io.Copy(connection, upstream)
				connection.Close()
				upstream.Close()
			}))
			proxy.Config.ErrorLog = log.New(io.Discard, "", 0)
			proxy.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{test.proxy}}
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
			port, _ := strconv.Atoi(proxyURL.Port())
			credential, err := egress.NewBasicCredentials("fixture-user", "fixture-proxy-password")
			if err != nil {
				t.Fatal(err)
			}
			client, err := egress.NewHTTPSConnectClient(egress.ProxyConfig{ProxyID: "proxy_integration", ConnectionRevision: 1, Host: proxyURL.Hostname(), Port: uint16(port), Scope: egress.ScopePrivate, Credentials: credential, AllowLoopbackForTesting: true})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.CloseIdleConnections)
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, target.URL+"/v1/test", strings.NewReader("{}"))
			request.Header.Set("Authorization", "Bearer fixture-upstream-key")
			response, err := client.Do(request)
			if !test.succeeds {
				if err == nil {
					response.Body.Close()
					t.Fatal("invalid certificate was accepted")
				}
				select {
				case <-targetHeaders:
					t.Fatal("HTTP credentials reached an unverified target")
				default:
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || response.StatusCode != 200 || string(body) != `{"ok":true}` {
				t.Fatal("verified tunnel response failed")
			}
			select {
			case headers := <-connectHeaders:
				if headers.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("fixture-user:fixture-proxy-password")) || headers.Get("Authorization") != "" {
					t.Fatal("proxy handshake credential isolation failed")
				}
			default:
				t.Fatal("proxy was bypassed")
			}
			select {
			case headers := <-targetHeaders:
				if headers.Get("Authorization") != "Bearer fixture-upstream-key" || headers.Get("Proxy-Authorization") != "" {
					t.Fatal("target credential isolation failed")
				}
			default:
				t.Fatal("target did not receive the verified request")
			}
		})
	}
}

func newOutboundProxyTestCA(t *testing.T) (*x509.Certificate, *ecdsa.PrivateKey, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "CPA synthetic test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	encoded, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(certificate)
	return certificate, key, roots
}

func newOutboundProxyTestCertificate(t *testing.T, root *x509.Certificate, signer *ecdsa.PrivateKey, validHost bool) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"wrong-host.invalid"}}
	if validHost {
		template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	}
	encoded, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{encoded}, PrivateKey: key}
}
