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

const (
	geminiAPIKeyProvider = "gemini-api-key"
	geminiAPIEndpoint    = "https://generativelanguage.googleapis.com"
)

type upstreamView struct {
	ID                string                   `json:"id"`
	Name              string                   `json:"name"`
	ProviderKind      string                   `json:"provider_kind"`
	Endpoint          string                   `json:"endpoint"`
	Enabled           bool                     `json:"enabled"`
	Revision          int64                    `json:"revision"`
	CredentialState   *string                  `json:"credential_state"`
	VerifiedAt        *string                  `json:"verified_at"`
	OAuthRefresh      *codexOAuthRefreshView   `json:"oauth_refresh,omitempty"`
	LatestObservation *upstreamObservationView `json:"latest_observation"`
	Cooldown          *upstreamCooldownView    `json:"cooldown"`
}

type codexOAuthRefreshView struct {
	Eligible   bool    `json:"eligible"`
	State      string  `json:"state"`
	ReasonCode *string `json:"reason_code,omitempty"`
}

type createUpstreamRequest struct {
	Name         string `json:"name"`
	ProviderKind string `json:"provider_kind"`
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"api_key"`
}

func (a *App) listUpstreams(w http.ResponseWriter, r *http.Request, _ adminSession) {
	rows, err := a.store.db.QueryContext(r.Context(), `SELECT u.id,u.name,u.provider_kind,u.endpoint,u.enabled,u.revision,u.credential_state,u.verified_at,
		b.client_id,b.source,rs.state,rs.reason_code
		FROM upstreams u
		LEFT JOIN codex_oauth_bindings b ON b.upstream_id=u.id
		LEFT JOIN codex_oauth_refresh_states rs ON rs.upstream_id=u.id
		ORDER BY u.created_at,u.id`)
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer rows.Close()
	items := make([]upstreamView, 0)
	for rows.Next() {
		var item upstreamView
		var enabled int
		var state, verified, clientID, source, refreshState, reason sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &item.ProviderKind, &item.Endpoint, &enabled, &item.Revision, &state, &verified,
			&clientID, &source, &refreshState, &reason); err != nil {
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
		item.Enabled = enabled != 0
		item.CredentialState = nullString(state)
		item.VerifiedAt = nullString(verified)
		item.OAuthRefresh = a.codexOAuthRefreshView(item.ProviderKind, clientID, source, refreshState, reason)
		items = append(items, item)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	now := time.Now().UTC()
	if err := a.decorateUpstreamObservations(r.Context(), items); err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if err := a.decorateUpstreamCooldowns(r.Context(), items, now); err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "server_time": now.Format(time.RFC3339Nano)})
}

func (a *App) codexOAuthRefreshView(provider string, clientID, source, state, reason sql.NullString) *codexOAuthRefreshView {
	if provider != codexMembershipProvider {
		return nil
	}
	view := &codexOAuthRefreshView{State: "unavailable"}
	if !a.cfg.ExperimentalCodexMembership {
		code := "feature_disabled"
		view.ReasonCode = &code
		return view
	}
	if !clientID.Valid || !source.Valid || source.String != "authorization_code" {
		code := "source_unavailable"
		view.ReasonCode = &code
		return view
	}
	if !a.codexOAuthConfigured() || clientID.String != a.cfg.CodexOAuthClientID {
		code := "client_mismatch"
		view.ReasonCode = &code
		return view
	}
	view.Eligible = true
	switch state.String {
	case "ready":
		view.State = "ready"
	case "in_progress":
		view.State = "refreshing"
	case "paused":
		view.State = "paused"
		view.ReasonCode = nullString(reason)
	case "reauth_required":
		view.State = "reauth_required"
		view.ReasonCode = nullString(reason)
	default:
		view.Eligible = false
		code := "lifecycle_unavailable"
		view.ReasonCode = &code
	}
	return view
}

func (a *App) decorateUpstreamOAuthRefresh(ctx context.Context, item *upstreamView) error {
	if item.ProviderKind != codexMembershipProvider {
		return nil
	}
	var clientID, source, state, reason sql.NullString
	err := a.store.db.QueryRowContext(ctx, `SELECT b.client_id,b.source,rs.state,rs.reason_code
		FROM upstreams u LEFT JOIN codex_oauth_bindings b ON b.upstream_id=u.id
		LEFT JOIN codex_oauth_refresh_states rs ON rs.upstream_id=u.id WHERE u.id=?`, item.ID).
		Scan(&clientID, &source, &state, &reason)
	if err != nil {
		return err
	}
	item.OAuthRefresh = a.codexOAuthRefreshView(item.ProviderKind, clientID, source, state, reason)
	return nil
}

func (a *App) createUpstream(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var input createUpstreamRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Endpoint = strings.TrimSpace(input.Endpoint)
	if !validText(input.Name, 1, 120) || (input.ProviderKind != "openai-compatible" && input.ProviderKind != anthropicAPIKeyProvider && input.ProviderKind != geminiAPIKeyProvider) || !validText(input.APIKey, 1, 4096) {
		writeAdminError(w, 400, "invalid_request", "Invalid upstream fields.")
		return
	}
	var endpoint string
	var err error
	if input.ProviderKind == geminiAPIKeyProvider {
		endpoint, err = validateGeminiEndpoint(r.Context(), input.Endpoint, a.cfg.AllowLoopbackUpstream)
	} else {
		endpoint, err = validateEndpoint(r.Context(), input.Endpoint, a.cfg.AllowLoopbackUpstream)
	}
	if err != nil {
		writeAdminError(w, 400, "invalid_endpoint", "Upstream endpoint is not allowed.")
		return
	}
	id, err := newID("ups")
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	keyVersion := 1
	var ciphertext []byte
	if input.ProviderKind == geminiAPIKeyProvider {
		keyVersion = 2
		ciphertext, err = a.secrets.encryptGeminiAPIKey(id, input.APIKey)
	} else {
		ciphertext, err = a.secrets.encryptCredential(id, input.APIKey)
	}
	if err != nil {
		writeAdminError(w, 503, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	item := upstreamView{ID: id, Name: input.Name, ProviderKind: input.ProviderKind, Endpoint: endpoint, Enabled: true, Revision: 1}
	_, err = a.store.db.ExecContext(r.Context(), `INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, item.ID, item.Name, item.ProviderKind, item.Endpoint, 1, ciphertext, keyVersion, item.Revision, utcNow())
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
	unlock, err := a.acquireCodexMutationLock(r.Context(), id)
	if err != nil {
		writeAdminError(w, http.StatusRequestTimeout, "request_cancelled", "The request was cancelled.")
		return
	}
	defer unlock()
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
	var state, verified sql.NullString
	err = tx.QueryRowContext(r.Context(), `SELECT id,name,provider_kind,endpoint,enabled,revision,credential_ciphertext,credential_state,verified_at FROM upstreams WHERE id=?`, id).Scan(&item.ID, &item.Name, &item.ProviderKind, &item.Endpoint, &enabled, &item.Revision, &ciphertext, &state, &verified)
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, 404, "not_found", "Upstream was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	item.Enabled = enabled != 0
	item.CredentialState = nullString(state)
	item.VerifiedAt = nullString(verified)
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
		if item.ProviderKind == codexMembershipProvider {
			writeAdminError(w, http.StatusBadRequest, "unsupported_feature", "Codex membership credentials must be replaced with the Codex auth endpoint.")
			return
		}
		if !validText(*input.APIKey, 1, 4096) {
			writeAdminError(w, 400, "invalid_request", "Invalid upstream fields.")
			return
		}
		if item.ProviderKind == geminiAPIKeyProvider {
			ciphertext, err = a.secrets.encryptGeminiAPIKey(id, *input.APIKey)
		} else {
			ciphertext, err = a.secrets.encryptCredential(id, *input.APIKey)
		}
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
	a.notifyAccountPoolChanged()
	if err := a.decorateUpstreamOAuthRefresh(r.Context(), &item); err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	writeJSON(w, 200, item)
}

func validateGeminiEndpoint(ctx context.Context, raw string, allowLoopback bool) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = geminiAPIEndpoint
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid Gemini endpoint")
	}
	if u.Scheme == "https" && strings.EqualFold(u.Host, "generativelanguage.googleapis.com") && (u.Path == "" || u.Path == "/") {
		return geminiAPIEndpoint, nil
	}
	if !allowLoopback {
		return "", errors.New("Gemini endpoint must use the official service")
	}
	endpoint, err := validateEndpoint(ctx, raw, true)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || net.ParseIP(parsed.Hostname()) == nil || !net.ParseIP(parsed.Hostname()).IsLoopback() {
		return "", errors.New("test Gemini endpoint must be a literal loopback address")
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	if parsed.Path == "/v1" {
		parsed.Path = ""
	}
	return strings.TrimRight(parsed.String(), "/"), nil
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
	var providerKind string
	if err := a.store.db.QueryRowContext(r.Context(), `SELECT provider_kind FROM upstreams WHERE id=?`, input.UpstreamID).Scan(&providerKind); err != nil {
		writeAdminError(w, 400, "invalid_request", "Upstream was not found.")
		return
	}
	if providerKind == geminiAPIKeyProvider && (strings.Contains(input.ID, "/") || !validGeminiUpstreamModel(input.UpstreamModel)) {
		writeAdminError(w, 400, "invalid_request", "Invalid Gemini model fields.")
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
