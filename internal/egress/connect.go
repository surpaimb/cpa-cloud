package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	connectTimeout        = 10 * time.Second
	connectHeaderLimit    = 32 << 10
	connectLineBuffer     = 4 << 10
	maxConnectionAttempts = 16
)

// ProxyConfig is an immutable, already-decrypted connection snapshot. Callers
// must obtain it under their account/binding revision guard. NewClient performs
// no network I/O; addresses are resolved and checked on each new connection.
type ProxyConfig struct {
	ProxyID                 string
	ConnectionRevision      int64
	Host                    string
	Port                    uint16
	Scope                   AddressScope
	Credentials             BasicCredentials
	AllowLoopbackForTesting bool
}

// BasicCredentials keeps proxy authentication fields out of default
// formatting and JSON serialization. The value has no exported secret fields.
type BasicCredentials struct {
	username string
	password string
}

// NewBasicCredentials validates credentials before they enter a connection
// snapshot. An empty username and password mean that authentication is absent.
func NewBasicCredentials(username, password string) (BasicCredentials, error) {
	if len(username) > 1024 || len(password) > 4096 || !utf8.ValidString(username) || !utf8.ValidString(password) ||
		strings.ContainsRune(username, ':') || containsControl(username) || containsControl(password) || username == "" && password != "" {
		return BasicCredentials{}, ErrInvalidConfig
	}
	return BasicCredentials{username: username, password: password}, nil
}

func (credentials BasicCredentials) String() string   { return "<proxy credentials redacted>" }
func (credentials BasicCredentials) GoString() string { return "egress.BasicCredentials{<redacted>}" }

func (credentials BasicCredentials) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, "<proxy credentials redacted>")
}

// Format prevents every fmt verb from exposing credentials through a config
// value. Host and identifiers remain available for bounded diagnostics.
func (config ProxyConfig) Format(state fmt.State, _ rune) {
	_, _ = fmt.Fprintf(state, "ProxyConfig{ProxyID:%q ConnectionRevision:%d Host:%q Port:%d Scope:%q Credentials:<redacted> AllowLoopbackForTesting:%t}",
		config.ProxyID, config.ConnectionRevision, config.Host, config.Port, config.Scope, config.AllowLoopbackForTesting)
}

// CacheKey separates idle connection pools across connection revisions.
type CacheKey struct {
	ProxyID            string
	ConnectionRevision int64
}

type dialContextFunc func(context.Context, string, string) (net.Conn, error)

type dependencies struct {
	resolver       addressResolver
	dialContext    dialContextFunc
	proxyTLS       *tls.Config
	targetTLS      *tls.Config
	connectTimeout time.Duration
}

func productionDependencies() dependencies {
	dialer := &net.Dialer{Timeout: connectTimeout, KeepAlive: 30 * time.Second}
	return dependencies{
		resolver:       systemResolver{},
		dialContext:    dialer.DialContext,
		proxyTLS:       &tls.Config{MinVersion: tls.VersionTLS12},
		targetTLS:      &tls.Config{MinVersion: tls.VersionTLS12},
		connectTimeout: connectTimeout,
	}
}

// Client is an HTTPS-only HTTP client whose connections always traverse the
// configured HTTPS CONNECT proxy. It never consults environment proxy values.
type Client struct {
	client      *http.Client
	transport   *http.Transport
	dialer      *connectDialer
	targetTLS   *tls.Config
	key         CacheKey
	fingerprint [sha256.Size]byte
}

func (client *Client) Format(state fmt.State, _ rune) {
	if client == nil {
		_, _ = io.WriteString(state, "egress.Client<nil>")
		return
	}
	_, _ = fmt.Fprintf(state, "egress.Client{ProxyID:%q ConnectionRevision:%d}", client.key.ProxyID, client.key.ConnectionRevision)
}

// NewHTTPSConnectClient builds a client without opening a connection. The
// production constructor intentionally has no custom CA or TLS-disable option.
func NewHTTPSConnectClient(config ProxyConfig) (*Client, error) {
	return newHTTPSConnectClient(config, productionDependencies())
}

func newHTTPSConnectClient(config ProxyConfig, deps dependencies) (*Client, error) {
	if err := validateProxyConfig(config); err != nil {
		return nil, err
	}
	deps = normalizeDependencies(deps)
	dialer := &connectDialer{config: config, deps: deps, authorization: proxyAuthorization(config)}
	targetTLS := deps.targetTLS.Clone()
	if targetTLS.MinVersion < tls.VersionTLS12 {
		targetTLS.MinVersion = tls.VersionTLS12
	}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   8,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   connectTimeout,
		ResponseHeaderTimeout: 2 * time.Minute,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       targetTLS,
	}
	client := &Client{
		transport:   transport,
		dialer:      dialer,
		targetTLS:   targetTLS.Clone(),
		key:         CacheKey{ProxyID: config.ProxyID, ConnectionRevision: config.ConnectionRevision},
		fingerprint: configFingerprint(config),
	}
	client.client = &http.Client{
		Transport:     httpsOnlyRoundTripper{next: transport},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client, nil
}

func normalizeDependencies(deps dependencies) dependencies {
	defaults := productionDependencies()
	if deps.resolver == nil {
		deps.resolver = defaults.resolver
	}
	if deps.dialContext == nil {
		deps.dialContext = defaults.dialContext
	}
	if deps.proxyTLS == nil {
		deps.proxyTLS = defaults.proxyTLS
	}
	if deps.targetTLS == nil {
		deps.targetTLS = defaults.targetTLS
	}
	if deps.connectTimeout <= 0 {
		deps.connectTimeout = defaults.connectTimeout
	}
	return deps
}

func validateProxyConfig(config ProxyConfig) error {
	if config.ProxyID == "" || config.ProxyID != strings.TrimSpace(config.ProxyID) || len(config.ProxyID) > 128 ||
		strings.ContainsAny(config.ProxyID, "\x00\r\n") || config.ConnectionRevision < 1 || config.Port == 0 ||
		(config.Scope != ScopePublic && config.Scope != ScopePrivate) || validateHost(config.Host) != nil {
		return ErrInvalidConfig
	}
	if _, err := NewBasicCredentials(config.Credentials.username, config.Credentials.password); err != nil {
		return ErrInvalidConfig
	}
	return nil
}

func proxyAuthorization(config ProxyConfig) string {
	if config.Credentials.username == "" {
		return ""
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(config.Credentials.username+":"+config.Credentials.password))
}

// Do sends one request through the bound proxy. Redirect responses are
// returned to the caller without following them.
func (client *Client) Do(request *http.Request) (*http.Response, error) {
	if client == nil || client.client == nil || request == nil {
		return nil, ErrInvalidConfig
	}
	response, err := client.client.Do(request)
	if err == nil {
		return response, nil
	}
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
	return nil, sanitizeRequestError(request.Context(), err)
}

// CloseIdleConnections closes reusable tunnels without interrupting requests
// which already own a connection.
func (client *Client) CloseIdleConnections() {
	if client != nil && client.transport != nil {
		client.transport.CloseIdleConnections()
	}
}

func (client *Client) CacheKey() CacheKey {
	if client == nil {
		return CacheKey{}
	}
	return client.key
}

// ProbeTLS establishes CONNECT and completes target TLS without sending an
// HTTP request or any upstream credential. It is intended for the bounded
// administrator handshake operation defined by the service contract.
func (client *Client) ProbeTLS(ctx context.Context, targetHost, targetPort string) error {
	if client == nil || client.dialer == nil || client.targetTLS == nil || ctx == nil || validateHost(targetHost) != nil || !validPort(targetPort) {
		return ErrInvalidConfig
	}
	connection, err := client.dialer.DialContext(ctx, "tcp", net.JoinHostPort(targetHost, targetPort))
	if err != nil {
		return err
	}
	defer connection.Close()
	stopCancellation := context.AfterFunc(ctx, func() { _ = connection.Close() })
	defer stopCancellation()
	deadline := time.Now().Add(connectTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return ErrProxyUnavailable
	}
	targetTLS := client.targetTLS.Clone()
	targetTLS.ServerName = targetHost
	secureTarget := tls.Client(connection, targetTLS)
	if err := secureTarget.HandshakeContext(ctx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrProxyUnavailable
	}
	return nil
}

type httpsOnlyRoundTripper struct{ next http.RoundTripper }

func (roundTripper httpsOnlyRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Scheme != "https" || request.URL.Host == "" || request.URL.User != nil {
		return nil, ErrHTTPSRequired
	}
	if request.URL.Fragment != "" || request.URL.RawFragment != "" || request.URL.Opaque != "" || validateHost(request.URL.Hostname()) != nil {
		return nil, ErrInvalidConfig
	}
	if port := request.URL.Port(); port != "" && !validPort(port) {
		return nil, ErrInvalidConfig
	}
	host := request.URL.Hostname()
	wantAuthority := host
	if strings.ContainsRune(host, ':') {
		wantAuthority = "[" + host + "]"
	}
	if port := request.URL.Port(); port != "" {
		wantAuthority = net.JoinHostPort(host, port)
	}
	if request.URL.Host != wantAuthority {
		return nil, ErrInvalidConfig
	}
	if request.Host != "" && request.Host != request.URL.Host {
		return nil, ErrInvalidConfig
	}
	for name := range request.Header {
		if strings.EqualFold(name, "Proxy-Authorization") {
			return nil, ErrForbiddenHeader
		}
	}
	return roundTripper.next.RoundTrip(request)
}

type connectDialer struct {
	config        ProxyConfig
	deps          dependencies
	authorization string
}

func (dialer *connectDialer) DialContext(ctx context.Context, network, targetAddress string) (net.Conn, error) {
	if ctx == nil {
		return nil, ErrInvalidConfig
	}
	targetHost, targetPort, err := net.SplitHostPort(targetAddress)
	if err != nil || validateHost(targetHost) != nil || !validPort(targetPort) {
		return nil, ErrInvalidConfig
	}
	targetIPs, err := resolveTarget(ctx, dialer.deps.resolver, targetHost, dialer.config.AllowLoopbackForTesting)
	if err != nil {
		return nil, err
	}
	proxyIPs, err := resolveProxy(ctx, dialer.deps.resolver, dialer.config)
	if err != nil {
		return nil, err
	}
	attempts := 0
	var lastError error
	for _, targetIP := range targetIPs {
		for _, proxyIP := range proxyIPs {
			attempts++
			if attempts > maxConnectionAttempts {
				return nil, ErrProxyUnavailable
			}
			connection, retry, err := dialer.connect(ctx, network, targetHost, targetPort, targetIP, proxyIP)
			if err == nil {
				return connection, nil
			}
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !retry {
				return nil, err
			}
			lastError = err
		}
	}
	if lastError != nil {
		return nil, lastError
	}
	return nil, ErrProxyUnavailable
}

func (dialer *connectDialer) connect(ctx context.Context, network, targetHost, targetPort string, targetIP, proxyIP netip.Addr) (net.Conn, bool, error) {
	proxyAddress := net.JoinHostPort(proxyIP.String(), strconv.Itoa(int(dialer.config.Port)))
	raw, err := dialer.deps.dialContext(ctx, network, proxyAddress)
	if err != nil {
		return nil, true, ErrProxyUnavailable
	}
	closed := true
	defer func() {
		if closed {
			_ = raw.Close()
		}
	}()
	stopCancellation := context.AfterFunc(ctx, func() { _ = raw.Close() })
	defer stopCancellation()
	deadline := time.Now().Add(dialer.deps.connectTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}
	if err := raw.SetDeadline(deadline); err != nil {
		return nil, true, ErrProxyUnavailable
	}
	proxyTLS := dialer.deps.proxyTLS.Clone()
	if proxyTLS.MinVersion < tls.VersionTLS12 {
		proxyTLS.MinVersion = tls.VersionTLS12
	}
	proxyTLS.ServerName = dialer.config.Host
	proxyTLS.NextProtos = []string{"http/1.1"}
	secureProxy := tls.Client(raw, proxyTLS)
	if err := secureProxy.HandshakeContext(ctx); err != nil {
		return nil, true, ErrProxyUnavailable
	}
	authority := net.JoinHostPort(targetIP.String(), targetPort)
	request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: authority}, Host: authority, Header: make(http.Header)}
	if dialer.authorization != "" {
		request.Header.Set("Proxy-Authorization", dialer.authorization)
	}
	if err := request.Write(secureProxy); err != nil {
		return nil, true, ErrProxyUnavailable
	}
	buffered, response, err := readConnectResponse(secureProxy, request)
	if err != nil {
		return nil, true, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, false, ErrProxyRejected
	}
	if err := secureProxy.SetDeadline(time.Time{}); err != nil {
		return nil, true, ErrProxyUnavailable
	}
	stopCancellation()
	if ctx.Err() != nil {
		return nil, false, ctx.Err()
	}
	closed = false
	_ = targetHost // retained for clarity: outer Transport owns target SNI.
	return &bufferedConn{Conn: secureProxy, reader: buffered}, false, nil
}

func readConnectResponse(connection net.Conn, request *http.Request) (*bufio.Reader, *http.Response, error) {
	reader := bufio.NewReaderSize(connection, connectLineBuffer)
	var header bytes.Buffer
	lineLength := 0
	for {
		fragment, err := reader.ReadSlice('\n')
		if header.Len()+len(fragment) > connectHeaderLimit {
			return nil, nil, ErrConnectHeaderLarge
		}
		_, _ = header.Write(fragment)
		lineLength += len(fragment)
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			return nil, nil, ErrConnectResponse
		}
		if lineLength == 2 && bytes.Equal(fragment, []byte("\r\n")) {
			break
		}
		lineLength = 0
	}
	response, err := http.ReadResponse(bufio.NewReader(bytes.NewReader(header.Bytes())), request)
	if err != nil {
		return nil, nil, ErrConnectResponse
	}
	if response.Body != nil {
		_ = response.Body.Close()
	}
	return reader, response, nil
}

type bufferedConn struct {
	net.Conn
	reader io.Reader
}

func (connection *bufferedConn) Read(buffer []byte) (int, error) {
	return connection.reader.Read(buffer)
}

func validPort(port string) bool {
	value, err := strconv.Atoi(port)
	return err == nil && value >= 1 && value <= 65535 && strconv.Itoa(value) == port
}

func configFingerprint(config ProxyConfig) [sha256.Size]byte {
	hash := sha256.New()
	for _, value := range []string{
		config.ProxyID, strconv.FormatInt(config.ConnectionRevision, 10), config.Host,
		strconv.Itoa(int(config.Port)), string(config.Scope), config.Credentials.username,
		config.Credentials.password, strconv.FormatBool(config.AllowLoopbackForTesting),
	} {
		_, _ = io.WriteString(hash, value)
		_, _ = hash.Write([]byte{0})
	}
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hash.Sum(nil))
	return fingerprint
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func sanitizeRequestError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	for _, fixed := range []error{
		ErrInvalidConfig, ErrResolutionFailed, ErrAddressNotPermitted, ErrTooManyAddresses,
		ErrProxyUnavailable, ErrProxyRejected, ErrConnectResponse, ErrConnectHeaderLarge,
		ErrHTTPSRequired, ErrForbiddenHeader,
	} {
		if errors.Is(err, fixed) {
			return fixed
		}
	}
	return ErrRequestFailed
}
