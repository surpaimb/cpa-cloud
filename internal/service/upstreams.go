package service

import (
	"context"
	"crypto/tls"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type upstreamView struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ProviderKind string `json:"provider_kind"`
	Endpoint     string `json:"endpoint"`
	Enabled      bool   `json:"enabled"`
	Revision     int64  `json:"revision"`
}

type createUpstreamRequest struct {
	Name         string `json:"name"`
	ProviderKind string `json:"provider_kind"`
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"api_key"`
}

func (a *App) listUpstreams(w http.ResponseWriter, r *http.Request, _ adminSession) {
	rows, err := a.store.db.QueryContext(r.Context(), `SELECT id,name,provider_kind,endpoint,enabled,revision FROM upstreams ORDER BY created_at,id`)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer rows.Close()
	items := make([]upstreamView, 0)
	for rows.Next() {
		var item upstreamView
		var enabled int
		if err := rows.Scan(&item.ID, &item.Name, &item.ProviderKind, &item.Endpoint, &enabled, &item.Revision); err != nil {
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
		item.Enabled = enabled != 0
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (a *App) createUpstream(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var input createUpstreamRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	if !validText(input.Name, 1, 120) || input.ProviderKind != "openai-compatible" || !validText(input.APIKey, 1, 4096) {
		writeAdminError(w, 400, "invalid_request", "Invalid upstream fields.")
		return
	}
	endpoint, err := validateEndpoint(r.Context(), input.Endpoint, a.cfg.AllowLoopbackUpstream)
	if err != nil {
		writeAdminError(w, 400, "invalid_endpoint", "Upstream endpoint is not allowed.")
		return
	}
	id, err := newID("ups")
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	ciphertext, err := a.secrets.encryptCredential(id, input.APIKey)
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	item := upstreamView{ID: id, Name: input.Name, ProviderKind: input.ProviderKind, Endpoint: endpoint, Enabled: true, Revision: 1}
	_, err = a.store.db.ExecContext(r.Context(), `INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, item.ID, item.Name, item.ProviderKind, item.Endpoint, 1, ciphertext, 1, item.Revision, utcNow())
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	writeJSON(w, 201, item)
}

type updateUpstreamRequest struct {
	ExpectedRevision int64   `json:"expected_revision"`
	Name             *string `json:"name"`
	Enabled          *bool   `json:"enabled"`
	APIKey           *string `json:"api_key"`
}

func (a *App) updateUpstream(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var input updateUpstreamRequest
	if !decodeJSON(w, r, adminMaxBody, &input) || input.ExpectedRevision < 1 {
		return
	}
	if input.Name == nil && input.Enabled == nil && input.APIKey == nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "No upstream changes were provided.")
		return
	}
	id := r.PathValue("id")
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer tx.Rollback()
	var item upstreamView
	var enabled int
	var ciphertext []byte
	err = tx.QueryRowContext(r.Context(), `SELECT id,name,provider_kind,endpoint,enabled,revision,credential_ciphertext FROM upstreams WHERE id=?`, id).Scan(&item.ID, &item.Name, &item.ProviderKind, &item.Endpoint, &enabled, &item.Revision, &ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, 404, "not_found", "Upstream was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	item.Enabled = enabled != 0
	if item.Revision != input.ExpectedRevision {
		writeAdminError(w, 409, "revision_conflict", "The object was changed by another request.")
		return
	}
	if input.Name != nil {
		*input.Name = strings.TrimSpace(*input.Name)
		if !validText(*input.Name, 1, 120) {
			writeAdminError(w, 400, "invalid_request", "Invalid upstream fields.")
			return
		}
		item.Name = *input.Name
	}
	if input.Enabled != nil {
		item.Enabled = *input.Enabled
	}
	if input.APIKey != nil {
		if !validText(*input.APIKey, 1, 4096) {
			writeAdminError(w, 400, "invalid_request", "Invalid upstream fields.")
			return
		}
		ciphertext, err = a.secrets.encryptCredential(id, *input.APIKey)
		if err != nil {
			writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
			return
		}
	}
	item.Revision++
	res, err := tx.ExecContext(r.Context(), `UPDATE upstreams SET name=?,enabled=?,credential_ciphertext=?,revision=? WHERE id=? AND revision=?`, item.Name, boolInt(item.Enabled), ciphertext, item.Revision, id, input.ExpectedRevision)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		writeAdminError(w, 409, "revision_conflict", "The object was changed by another request.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	writeJSON(w, 200, item)
}

type modelView struct {
	ID            string `json:"id"`
	UpstreamID    string `json:"upstream_id"`
	UpstreamModel string `json:"upstream_model"`
	Enabled       bool   `json:"enabled"`
}

func (a *App) listAdminModels(w http.ResponseWriter, r *http.Request, _ adminSession) {
	rows, err := a.store.db.QueryContext(r.Context(), `SELECT id,upstream_id,upstream_model,enabled FROM models ORDER BY id`)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer rows.Close()
	items := make([]modelView, 0)
	for rows.Next() {
		var item modelView
		var enabled int
		if err := rows.Scan(&item.ID, &item.UpstreamID, &item.UpstreamModel, &enabled); err != nil {
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
		item.Enabled = enabled != 0
		items = append(items, item)
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

type createModelRequest struct {
	ID            string `json:"id"`
	UpstreamID    string `json:"upstream_id"`
	UpstreamModel string `json:"upstream_model"`
}

func (a *App) createModel(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var input createModelRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	if !validIdentifier(input.ID, 128) || !validText(input.UpstreamID, 1, 128) || !validText(input.UpstreamModel, 1, 256) {
		writeAdminError(w, 400, "invalid_request", "Invalid model fields.")
		return
	}
	var exists int
	if err := a.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM upstreams WHERE id=?`, input.UpstreamID).Scan(&exists); err != nil {
		writeAdminError(w, 400, "invalid_request", "Upstream was not found.")
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	_, err := a.store.db.ExecContext(r.Context(), `INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES(?,?,?,?,?)`, input.ID, input.UpstreamID, input.UpstreamModel, 1, utcNow())
	if err != nil {
		if isConflict(err) {
			writeAdminError(w, 409, "already_exists", "Model already exists.")
		} else {
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		}
		return
	}
	writeJSON(w, 201, modelView{ID: input.ID, UpstreamID: input.UpstreamID, UpstreamModel: input.UpstreamModel, Enabled: true})
}

func validateEndpoint(ctx context.Context, raw string, allowLoopback bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid endpoint")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", errors.New("https required")
	}
	if u.Path == "" {
		u.Path = "/v1"
	}
	u.Path = strings.TrimRight(u.EscapedPath(), "/")
	if strings.Contains(strings.ToLower(u.Path), "%2f") || strings.Contains(strings.ToLower(u.Path), "%5c") || strings.Contains(u.Path, "..") {
		return "", errors.New("invalid path")
	}
	host := u.Hostname()
	ips, err := resolveHost(ctx, host)
	if err != nil {
		return "", err
	}
	for _, ip := range ips {
		if !permittedIP(ip, allowLoopback) {
			return "", errors.New("address is not permitted")
		}
	}
	if u.Scheme == "http" {
		literal := net.ParseIP(host)
		if !allowLoopback || literal == nil || !literal.IsLoopback() {
			return "", errors.New("plain HTTP is restricted to literal loopback addresses")
		}
	}
	return u.String(), nil
}

func resolveHost(ctx context.Context, host string) ([]net.IP, error) {
	if parsed := net.ParseIP(host); parsed != nil {
		return []net.IP{parsed}, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return net.DefaultResolver.LookupIP(lookupCtx, "ip", host)
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

func permittedIP(ip net.IP, allowLoopback bool) bool {
	if ip.IsLoopback() {
		return allowLoopback
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	addr, ok := netip.AddrFromSlice(ip)
	return !ok || !cgnat.Contains(addr.Unmap())
}

func newUpstreamClient(allowLoopback bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, ForceAttemptHTTP2: true, MaxIdleConns: 32, MaxIdleConnsPerHost: 8, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 2 * time.Minute, ExpectContinueTimeout: 1 * time.Second, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid upstream address")
		}
		ips, err := resolveHost(ctx, host)
		if err != nil {
			return nil, errors.New("upstream address resolution failed")
		}
		for _, ip := range ips {
			if !permittedIP(ip, allowLoopback) {
				return nil, errors.New("upstream address is not permitted")
			}
		}
		var last error
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
	return &http.Client{Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
}

func upstreamChatURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		path += "/chat/completions"
	} else {
		path += "/v1/chat/completions"
	}
	u.Path = path
	u.RawPath = ""
	return u.String(), nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func safeRetryAfter(value string) string {
	if value == "" {
		return ""
	}
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 && seconds <= 3600 {
		return value
	}
	if t, err := http.ParseTime(value); err == nil && time.Until(t) <= time.Hour {
		return value
	}
	return ""
}
