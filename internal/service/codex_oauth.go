package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"cpacloud.local/server/internal/membership"
)

const (
	codexOAuthAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	codexOAuthTokenURL     = "https://auth.openai.com/oauth/token"
	codexOAuthCallbackPath = "/admin/api/v1/codex/oauth/callback"
	codexOAuthScopes       = "openid profile email offline_access"
	codexOAuthSessionTTL   = 10 * time.Minute
	codexOAuthMaxBody      = 1 << 20
	codexOAuthMaxAttempts  = 3
	codexOAuthHTTPTimeout  = 20 * time.Second
)

type codexOAuthSessionRequest struct {
	Name        string `json:"name"`
	OperationID string `json:"operation_id"`
}

type codexOAuthSessionResponse struct {
	AuthorizationURL string `json:"authorization_url"`
	ExpiresAt        string `json:"expires_at"`
}

type codexOAuthSessionSecret struct {
	State    string `json:"state"`
	Verifier string `json:"verifier"`
}

type codexRefreshRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

type codexOAuthTokenRequest struct {
	GrantType    string `json:"grant_type"`
	ClientID     string `json:"client_id"`
	Code         string `json:"code,omitempty"`
	RedirectURI  string `json:"redirect_uri,omitempty"`
	CodeVerifier string `json:"code_verifier,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

type codexOAuthTokenResponse struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
}

type codexOAuthWireError struct {
	Status    int
	Code      string
	Retryable bool
	After     time.Duration
}

func newCodexOAuthHTTPClient() *http.Client {
	client := newUpstreamClient(false)
	client.Timeout = codexOAuthHTTPTimeout
	return client
}

func (e *codexOAuthWireError) Error() string { return "Codex OAuth token request failed." }

func ValidateCodexOAuthConfig(cfg Config) error {
	clientID := strings.TrimSpace(cfg.CodexOAuthClientID)
	redirect := strings.TrimSpace(cfg.CodexOAuthRedirectURI)
	if clientID == "" && redirect == "" {
		return nil
	}
	if clientID != "" && (!validText(clientID, 1, 512) || clientID != cfg.CodexOAuthClientID || containsControl(clientID)) {
		return errors.New("--codex-oauth-client-id must be 1 to 512 characters without surrounding whitespace")
	}
	if redirect == "" {
		return nil
	}
	u, err := url.Parse(redirect)
	if err != nil || !u.IsAbs() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != codexOAuthCallbackPath || u.Host == "" {
		return errors.New("--codex-oauth-redirect-uri must be an absolute callback URI without query or fragment")
	}
	if u.Scheme == "http" {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return errors.New("--codex-oauth-redirect-uri requires HTTPS except for literal loopback development addresses")
		}
	} else if u.Scheme != "https" {
		return errors.New("--codex-oauth-redirect-uri requires HTTPS except for literal loopback development addresses")
	}
	return nil
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func (a *App) codexOAuthConfigured() bool {
	return strings.TrimSpace(a.cfg.CodexOAuthClientID) != "" && strings.TrimSpace(a.cfg.CodexOAuthRedirectURI) != "" && ValidateCodexOAuthConfig(a.cfg) == nil
}

func (a *App) requireCodexOAuth(w http.ResponseWriter) bool {
	if !a.cfg.ExperimentalCodexMembership {
		writeAdminError(w, http.StatusForbidden, "feature_disabled", "Codex membership support is disabled.")
		return false
	}
	if !a.codexOAuthConfigured() {
		writeAdminError(w, http.StatusConflict, "codex_oauth_not_configured", "Codex OAuth requires --codex-oauth-client-id and --codex-oauth-redirect-uri.")
		return false
	}
	return true
}

func (a *App) createCodexOAuthSession(w http.ResponseWriter, r *http.Request, admin adminSession) {
	if !a.requireCodexOAuth(w) {
		return
	}
	var input codexOAuthSessionRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.OperationID = strings.TrimSpace(input.OperationID)
	if !validText(input.Name, 1, 120) || !validUUIDOperation(input.OperationID) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid Codex OAuth session fields.")
		return
	}
	if existing, err := a.loadCodexOAuthSessionByOperation(r.Context(), input.OperationID, admin.SessionID); err == nil {
		writeJSON(w, http.StatusOK, existing)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	state, err := randomToken(32)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	verifier, err := randomToken(48)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	id, err := newID("oauth")
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	secretJSON, err := json.Marshal(codexOAuthSessionSecret{State: state, Verifier: verifier})
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	ciphertext, err := a.secrets.encryptCodexOAuthSession(id, secretJSON)
	clear(secretJSON)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	expires := time.Now().UTC().Add(codexOAuthSessionTTL)
	_, err = a.store.db.ExecContext(r.Context(), `INSERT INTO codex_oauth_sessions(
		id,operation_id,admin_id,admin_session_id,state_digest,secret_ciphertext,name,expires_at,used_at,created_at
	) VALUES(?,?,?,?,?,?,?,?,NULL,?)`, id, input.OperationID, admin.AdminID, admin.SessionID,
		a.secrets.digest("codex-oauth-state/v1", state), ciphertext, input.Name, expires.Format(time.RFC3339Nano), utcNow())
	if err != nil {
		if isConflict(err) {
			if existing, lookupErr := a.loadCodexOAuthSessionByOperation(r.Context(), input.OperationID, admin.SessionID); lookupErr == nil {
				writeJSON(w, http.StatusOK, existing)
				return
			}
			writeAdminError(w, http.StatusConflict, "already_exists", "A conflicting authorization session already exists.")
			return
		}
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	writeJSON(w, http.StatusCreated, codexOAuthSessionResponse{AuthorizationURL: a.codexAuthorizationURL(state, verifier), ExpiresAt: expires.Format(time.RFC3339Nano)})
}

func (a *App) loadCodexOAuthSessionByOperation(ctx context.Context, operationID, adminSessionID string) (codexOAuthSessionResponse, error) {
	var id, expires, used string
	var encrypted []byte
	err := a.store.db.QueryRowContext(ctx, `SELECT id,secret_ciphertext,expires_at,COALESCE(used_at,'') FROM codex_oauth_sessions WHERE operation_id=? AND admin_session_id=?`, operationID, adminSessionID).
		Scan(&id, &encrypted, &expires, &used)
	if err != nil {
		return codexOAuthSessionResponse{}, err
	}
	expiry, err := parseTime(expires)
	if err != nil || used != "" || !time.Now().UTC().Before(expiry) {
		return codexOAuthSessionResponse{}, sql.ErrNoRows
	}
	plaintext, err := a.secrets.decryptCodexOAuthSession(id, encrypted)
	if err != nil {
		return codexOAuthSessionResponse{}, err
	}
	defer clear(plaintext)
	var secret codexOAuthSessionSecret
	if err := json.Unmarshal(plaintext, &secret); err != nil || secret.State == "" || secret.Verifier == "" {
		return codexOAuthSessionResponse{}, errors.New("OAuth session is invalid")
	}
	return codexOAuthSessionResponse{AuthorizationURL: a.codexAuthorizationURL(secret.State, secret.Verifier), ExpiresAt: expires}, nil
}

func (a *App) codexAuthorizationURL(state, verifier string) string {
	u, _ := url.Parse(codexOAuthAuthorizeURL)
	challenge := sha256.Sum256([]byte(verifier))
	query := u.Query()
	query.Set("response_type", "code")
	query.Set("client_id", a.cfg.CodexOAuthClientID)
	query.Set("redirect_uri", a.cfg.CodexOAuthRedirectURI)
	query.Set("scope", codexOAuthScopes)
	query.Set("state", state)
	query.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	query.Set("code_challenge_method", "S256")
	u.RawQuery = query.Encode()
	return u.String()
}

func (a *App) completeCodexOAuth(w http.ResponseWriter, r *http.Request, admin adminSession) {
	if !a.requireCodexOAuth(w) {
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	providerError := r.URL.Query().Get("error")
	if state == "" || (code == "") == (providerError == "") || len(state) > 512 || len(code) > 8192 || len(providerError) > 128 {
		a.writeCodexOAuthPage(w, http.StatusBadRequest, false)
		return
	}
	var id, operationID, name, expires string
	var encrypted []byte
	err := a.store.db.QueryRowContext(r.Context(), `SELECT id,operation_id,name,expires_at,secret_ciphertext
		FROM codex_oauth_sessions WHERE state_digest=? AND admin_id=? AND admin_session_id=? AND used_at IS NULL`,
		a.secrets.digest("codex-oauth-state/v1", state), admin.AdminID, admin.SessionID).
		Scan(&id, &operationID, &name, &expires, &encrypted)
	if err != nil {
		a.writeCodexOAuthPage(w, http.StatusBadRequest, false)
		return
	}
	expiry, err := parseTime(expires)
	if err != nil || !time.Now().UTC().Before(expiry) {
		_, _ = a.store.db.ExecContext(r.Context(), `UPDATE codex_oauth_sessions SET used_at=? WHERE id=? AND used_at IS NULL`, utcNow(), id)
		a.writeCodexOAuthPage(w, http.StatusBadRequest, false)
		return
	}
	result, err := a.store.db.ExecContext(r.Context(), `UPDATE codex_oauth_sessions SET used_at=? WHERE id=? AND used_at IS NULL`, utcNow(), id)
	if err != nil {
		a.writeCodexOAuthPage(w, http.StatusServiceUnavailable, false)
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		a.writeCodexOAuthPage(w, http.StatusBadRequest, false)
		return
	}
	plaintext, err := a.secrets.decryptCodexOAuthSession(id, encrypted)
	if err != nil {
		a.writeCodexOAuthPage(w, http.StatusServiceUnavailable, false)
		return
	}
	defer clear(plaintext)
	var secret codexOAuthSessionSecret
	if json.Unmarshal(plaintext, &secret) != nil || subtle.ConstantTimeCompare([]byte(secret.State), []byte(state)) != 1 || secret.Verifier == "" {
		a.writeCodexOAuthPage(w, http.StatusBadRequest, false)
		return
	}
	if providerError != "" {
		if !validOAuthAuthorizationError(providerError) {
			a.writeCodexOAuthPage(w, http.StatusBadRequest, false)
			return
		}
		a.writeCodexOAuthPage(w, http.StatusBadRequest, false)
		return
	}
	tokens, wireErr := a.requestCodexOAuthTokens(r.Context(), codexOAuthTokenRequest{
		GrantType: "authorization_code", ClientID: a.cfg.CodexOAuthClientID, Code: code,
		RedirectURI: a.cfg.CodexOAuthRedirectURI, CodeVerifier: secret.Verifier,
	})
	if wireErr != nil {
		a.writeCodexOAuthPage(w, http.StatusBadGateway, false)
		return
	}
	rawAuth, err := codexAuthJSONFromTokens(tokens)
	if err != nil {
		a.writeCodexOAuthPage(w, http.StatusBadGateway, false)
		return
	}
	defer clear(rawAuth)
	credential, err := parseSchedulableCodexAuth(rawAuth)
	if err != nil {
		a.writeCodexOAuthPage(w, http.StatusBadGateway, false)
		return
	}
	credential.Destroy()
	upstreamID, err := newID("ups")
	if err != nil {
		a.writeCodexOAuthPage(w, http.StatusServiceUnavailable, false)
		return
	}
	ciphertext, err := a.secrets.encryptCodexAuth(upstreamID, rawAuth)
	if err != nil {
		a.writeCodexOAuthPage(w, http.StatusServiceUnavailable, false)
		return
	}
	_, err = a.store.db.ExecContext(r.Context(), `INSERT INTO upstreams(
		id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id
	) VALUES(?,?,?,?,?,?,?,?,?,?,NULL,?)`, upstreamID, name, codexMembershipProvider, codexMembershipEndpoint, 1, ciphertext, 2, 1, utcNow(), codexStateImported, operationID)
	if err != nil {
		a.writeCodexOAuthPage(w, http.StatusServiceUnavailable, false)
		return
	}
	a.writeCodexOAuthPage(w, http.StatusOK, true)
}

func (a *App) writeCodexOAuthPage(w http.ResponseWriter, status int, success bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; base-uri 'none'")
	w.WriteHeader(status)
	if success {
		_, _ = io.WriteString(w, "<!doctype html><title>Authorization complete</title><p>Codex authorization was saved. You may close this window.</p>")
		return
	}
	_, _ = io.WriteString(w, "<!doctype html><title>Authorization failed</title><p>Codex authorization could not be completed. Return to CPA Cloud and start again.</p>")
}

func (a *App) refreshCodexCredential(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !a.requireCodexOAuth(w) {
		return
	}
	var input codexRefreshRequest
	if !decodeJSON(w, r, adminMaxBody, &input) || input.ExpectedRevision < 1 {
		return
	}
	id := r.PathValue("id")
	lock := a.codexRefreshLock(id)
	lock.Lock()
	defer lock.Unlock()

	var provider, state string
	var revision int64
	var ciphertext []byte
	err := a.store.db.QueryRowContext(r.Context(), `SELECT provider_kind,revision,credential_ciphertext,COALESCE(credential_state,'') FROM upstreams WHERE id=?`, id).
		Scan(&provider, &revision, &ciphertext, &state)
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Upstream was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if provider != codexMembershipProvider {
		writeAdminError(w, http.StatusBadRequest, "invalid_upstream_type", "This upstream does not use Codex membership credentials.")
		return
	}
	if revision != input.ExpectedRevision {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	plaintext, err := a.secrets.decryptCodexAuth(id, ciphertext)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "credential_unavailable", "The upstream credential is unavailable.")
		return
	}
	defer clear(plaintext)
	credential, err := membership.ParseCodexAuthJSON(plaintext)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "credential_unavailable", "The upstream credential is unavailable.")
		return
	}
	refreshToken := credential.RefreshTokenSecret()
	credential.Destroy()
	tokens, wireErr := a.requestCodexOAuthTokens(r.Context(), codexOAuthTokenRequest{GrantType: "refresh_token", ClientID: a.cfg.CodexOAuthClientID, RefreshToken: refreshToken})
	if wireErr != nil {
		if wireErr.Status == http.StatusUnauthorized || wireErr.Code == "invalid_grant" {
			result, updateErr := a.store.db.ExecContext(r.Context(), `UPDATE upstreams SET credential_state=?,verified_at=NULL WHERE id=? AND provider_kind=? AND revision=?`, codexStateReauth, id, codexMembershipProvider, revision)
			if updateErr != nil {
				writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
				return
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
				return
			}
			writeAdminError(w, http.StatusConflict, "codex_reauthorization_required", "Codex authorization must be completed again.")
			return
		}
		writeAdminError(w, http.StatusBadGateway, "codex_refresh_failed", "Codex credential refresh failed.")
		return
	}
	rawAuth, err := codexAuthJSONFromTokens(tokens)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, "codex_refresh_failed", "Codex credential refresh failed.")
		return
	}
	defer clear(rawAuth)
	validated, err := parseSchedulableCodexAuth(rawAuth)
	if err != nil {
		writeAdminError(w, http.StatusBadGateway, "codex_refresh_failed", "Codex credential refresh failed.")
		return
	}
	validated.Destroy()
	rotated, err := a.secrets.encryptCodexAuth(id, rawAuth)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	result, err := a.store.db.ExecContext(r.Context(), `UPDATE upstreams SET credential_ciphertext=?,key_version=2,revision=revision+1,credential_state=?,verified_at=NULL WHERE id=? AND provider_kind=? AND revision=?`,
		rotated, codexStateImported, id, codexMembershipProvider, revision)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	item, err := loadUpstreamView(r.Context(), a.store.db, id)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (a *App) codexRefreshLock(id string) *sync.Mutex {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()
	lock := a.refreshes[id]
	if lock == nil {
		lock = &sync.Mutex{}
		a.refreshes[id] = lock
	}
	return lock
}

func (a *App) requestCodexOAuthTokens(ctx context.Context, payload codexOAuthTokenRequest) (codexOAuthTokenResponse, *codexOAuthWireError) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return codexOAuthTokenResponse{}, &codexOAuthWireError{}
	}
	for attempt := 0; attempt < codexOAuthMaxAttempts; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, codexOAuthTokenURL, bytes.NewReader(encoded))
		if err != nil {
			return codexOAuthTokenResponse{}, &codexOAuthWireError{}
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		response, requestErr := a.oauthHTTP.Do(req)
		if requestErr != nil {
			wireErr := &codexOAuthWireError{Retryable: ctx.Err() == nil}
			if wireErr.Retryable && attempt+1 < codexOAuthMaxAttempts && sleepContext(ctx, time.Duration(1<<attempt)*250*time.Millisecond) == nil {
				continue
			}
			return codexOAuthTokenResponse{}, wireErr
		}
		body, readErr := io.ReadAll(io.LimitReader(response.Body, codexOAuthMaxBody+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil || len(body) > codexOAuthMaxBody {
			return codexOAuthTokenResponse{}, &codexOAuthWireError{Status: response.StatusCode}
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			var tokens codexOAuthTokenResponse
			if json.Unmarshal(body, &tokens) != nil || strings.TrimSpace(tokens.AccessToken) == "" || strings.TrimSpace(tokens.IDToken) == "" || strings.TrimSpace(tokens.RefreshToken) == "" {
				return codexOAuthTokenResponse{}, &codexOAuthWireError{Status: response.StatusCode}
			}
			return tokens, nil
		}
		wireErr := &codexOAuthWireError{Status: response.StatusCode, Retryable: response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500}
		var envelope struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(body, &envelope) == nil && validOAuthErrorCode(envelope.Error) {
			wireErr.Code = envelope.Error
		}
		if response.StatusCode == http.StatusTooManyRequests {
			wireErr.After = retryDelay(response.Header.Get("Retry-After"), attempt)
		} else {
			wireErr.After = time.Duration(1<<attempt) * 250 * time.Millisecond
		}
		if wireErr.Retryable && attempt+1 < codexOAuthMaxAttempts && sleepContext(ctx, wireErr.After) == nil {
			continue
		}
		return codexOAuthTokenResponse{}, wireErr
	}
	return codexOAuthTokenResponse{}, &codexOAuthWireError{}
}

func retryDelay(value string, attempt int) time.Duration {
	if value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			delay := time.Duration(seconds) * time.Second
			if delay > 2*time.Second {
				return 2 * time.Second
			}
			return delay
		}
		if target, err := http.ParseTime(value); err == nil {
			delay := time.Until(target)
			if delay < 0 {
				return 0
			}
			if delay > 2*time.Second {
				return 2 * time.Second
			}
			return delay
		}
	}
	return time.Duration(1<<attempt) * 250 * time.Millisecond
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func validOAuthErrorCode(code string) bool {
	switch code {
	case "invalid_grant", "invalid_client", "invalid_request", "temporarily_unavailable", "server_error":
		return true
	default:
		return false
	}
}

func validOAuthAuthorizationError(code string) bool {
	switch code {
	case "access_denied", "invalid_request", "unauthorized_client", "unsupported_response_type", "invalid_scope", "server_error", "temporarily_unavailable":
		return true
	default:
		return false
	}
}

func codexAuthJSONFromTokens(tokens codexOAuthTokenResponse) ([]byte, error) {
	accountID, fedramp, err := codexAccountIDFromIDToken(tokens.IDToken)
	if err != nil || fedramp || !validText(accountID, 1, 512) {
		return nil, errors.New("Codex identity token is unsupported")
	}
	return json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"access_token": tokens.AccessToken, "id_token": tokens.IDToken,
			"refresh_token": tokens.RefreshToken, "account_id": accountID,
		},
		"last_refresh": utcNow(),
	})
}

func codexAccountIDFromIDToken(token string) (string, bool, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return "", false, errors.New("invalid identity token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", false, errors.New("invalid identity token")
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(payload, &claims) != nil {
		return "", false, errors.New("invalid identity token")
	}
	var auth struct {
		AccountID string `json:"chatgpt_account_id"`
		FedRAMP   bool   `json:"chatgpt_account_is_fedramp"`
	}
	if json.Unmarshal(claims["https://api.openai.com/auth"], &auth) != nil || strings.TrimSpace(auth.AccountID) == "" {
		return "", false, errors.New("identity token has no account")
	}
	return auth.AccountID, auth.FedRAMP, nil
}

func trimCodexOAuthSessions(ctx context.Context, db *sql.DB) {
	cleanup, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _ = db.ExecContext(cleanup, `DELETE FROM codex_oauth_sessions WHERE used_at IS NOT NULL OR expires_at <= ?`, utcNow())
}
