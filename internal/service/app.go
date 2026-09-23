package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	adminMaxBody       = 1 << 20
	codexImportMaxBody = 8 << 20
	modelMaxBody       = 4 << 20
)

type App struct {
	cfg          Config
	store        *store
	secrets      *secrets
	http         *http.Client
	codex        codexExecutor
	responses    codexResponsesExecutor
	oauthHTTP    *http.Client
	admission    sync.RWMutex
	refresh      *codexRefreshCoordinator
	accountPool  *accountPoolRuntime
	loginMu      sync.Mutex
	logins       map[string]*loginAttempt
	catalogMu    sync.Mutex
	catalogs     map[string]codexCatalogCacheEntry
	codexCatalog codexCatalogLister
}

type loginAttempt struct {
	failures    int
	blockedTill time.Time
	lastSeen    time.Time
}

func Open(ctx context.Context, cfg Config) (*App, error) {
	if cfg.Version == "" {
		cfg.Version = "dev"
	}
	if err := ValidateCodexOAuthConfig(cfg); err != nil {
		return nil, err
	}
	sec, err := loadSecrets(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	s, err := openStore(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if err := ensureInitialized(ctx, s.db); err != nil {
		s.close()
		return nil, err
	}
	client := newUpstreamClient(cfg.AllowLoopbackUpstream)
	app := &App{
		cfg: cfg, store: s, secrets: sec, http: client,
		oauthHTTP: newCodexOAuthHTTPClient(), codex: newProductionCodexExecutor(), responses: newProductionCodexResponsesExecutor(),
		logins: make(map[string]*loginAttempt),
	}
	app.refresh = newCodexRefreshCoordinator(app)
	trimExpiredSessions(ctx, s.db)
	if err := recoverCodexOAuthSessions(ctx, s.db); err != nil {
		s.close()
		return nil, err
	}
	app.accountPool, err = newAccountPoolRuntime(app)
	if err != nil {
		s.close()
		return nil, err
	}
	if err := app.refresh.Start(); err != nil {
		app.accountPool.Close()
		s.close()
		return nil, err
	}
	return app, nil
}

func (a *App) Close() error {
	if a.accountPool != nil {
		a.accountPool.Close()
	}
	if a.refresh != nil {
		a.refresh.Close()
	}
	return a.store.close()
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	a.registerAccountPoolHandlers(mux)
	mux.HandleFunc("GET /healthz", a.health)
	mux.HandleFunc("POST /admin/api/v1/sessions", a.login)
	mux.HandleFunc("DELETE /admin/api/v1/sessions", a.requireAdmin(a.logout, true))
	mux.HandleFunc("GET /admin/api/v1/session", a.requireAdmin(a.sessionInfo, false))
	mux.HandleFunc("GET /admin/api/v1/employees", a.requireAdmin(a.listEmployees, false))
	mux.HandleFunc("POST /admin/api/v1/employees", a.requireAdmin(a.createEmployee, true))
	mux.HandleFunc("PATCH /admin/api/v1/employees/{id}", a.requireAdmin(a.updateEmployee, true))
	mux.HandleFunc("PUT /admin/api/v1/employees/{id}/model-policy", a.requireAdmin(a.updateModelPolicy, true))
	mux.HandleFunc("GET /admin/api/v1/employees/{id}/keys", a.requireAdmin(a.listKeys, false))
	mux.HandleFunc("POST /admin/api/v1/employees/{id}/keys", a.requireAdmin(a.createKey, true))
	mux.HandleFunc("POST /admin/api/v1/keys/{id}/revoke", a.requireAdmin(a.revokeKey, true))
	mux.HandleFunc("GET /admin/api/v1/upstreams", a.requireAdmin(a.listUpstreams, false))
	mux.HandleFunc("POST /admin/api/v1/upstreams", a.requireAdmin(a.createUpstream, true))
	mux.HandleFunc("POST /admin/api/v1/upstreams/batch-import", a.requireAdmin(a.batchImportUpstreams, true))
	mux.HandleFunc("POST /admin/api/v1/upstreams/codex-import", a.requireAdmin(a.importCodexUpstream, true))
	mux.HandleFunc("PATCH /admin/api/v1/upstreams/{id}", a.requireAdmin(a.updateUpstream, true))
	mux.HandleFunc("PUT /admin/api/v1/upstreams/{id}/codex-auth", a.requireAdmin(a.replaceCodexCredential, true))
	mux.HandleFunc("POST /admin/api/v1/upstreams/codex-oauth-sessions", a.requireAdmin(a.createCodexOAuthSession, true))
	mux.HandleFunc("GET /admin/api/v1/upstreams/codex-oauth-sessions/{id}", a.requireAdmin(a.getCodexOAuthSession, false))
	mux.HandleFunc("GET /admin/api/v1/codex/oauth/callback", a.requireAdmin(a.completeCodexOAuth, false))
	mux.HandleFunc("POST /admin/api/v1/upstreams/{id}/codex-refresh", a.requireAdmin(a.refreshCodexCredential, true))
	mux.HandleFunc("POST /admin/api/v1/upstreams/{id}/discover-models", a.requireAdmin(a.discoverUpstreamModels, true))
	mux.HandleFunc("GET /admin/api/v1/models", a.requireAdmin(a.listAdminModels, false))
	mux.HandleFunc("POST /admin/api/v1/models", a.requireAdmin(a.createModel, true))
	mux.HandleFunc("GET /admin/api/v1/system/status", a.requireAdmin(a.systemStatus, false))
	mux.HandleFunc("GET /v1/models", a.listModels)
	mux.HandleFunc("POST /v1/chat/completions", a.chatCompletions)
	mux.HandleFunc("POST /v1/responses", a.responsesAPI)
	mux.HandleFunc("POST /v1/messages", a.messages)
	mux.HandleFunc("POST /v1/messages/count_tokens", a.countMessageTokens)
	mux.HandleFunc("GET /v1beta/models", a.listGeminiModels)
	mux.HandleFunc("POST /v1beta/models/{operation}", a.geminiGenerateContent)
	if strings.TrimSpace(a.cfg.WebDir) != "" {
		mux.HandleFunc("GET /", a.serveWeb)
	}
	return requestMiddleware(mux)
}

func requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID, err := newID("req")
		if err != nil {
			http.Error(w, "service unavailable", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-Request-ID", requestID)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID)))
	})
}

type requestIDKey struct{}

func requestID(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey{}).(string)
	return v
}

func (a *App) health(w http.ResponseWriter, _ *http.Request) {
	response := map[string]string{"status": "ok"}
	if a.cfg.InstanceID != "" {
		response["instance_id"] = a.cfg.InstanceID
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) systemStatus(w http.ResponseWriter, _ *http.Request, _ adminSession) {
	limitations := []string{
		"development preview; not production hardened",
		"Responses resources, background execution, automatic account failover, and reliable billing-grade usage are not implemented; Messages is available only for Anthropic API-key routes",
		"backup/restore automation, production key custody, and multi-process storage are not implemented",
		"the host administrator can access runtime secrets and must protect the data directory and master key",
		"single process and single SQLite database only",
	}
	if a.cfg.ExperimentalCodexMembership {
		limitations = append(limitations, "Codex membership support is experimental, uses a fixed observed protocol, and has not been verified with a real account")
		if !a.codexOAuthConfigured() {
			limitations = append(limitations, "Codex OAuth requires explicit --codex-oauth-client-id and --codex-oauth-redirect-uri configuration")
		}
	} else {
		limitations = append(limitations, "only API-key upstreams are enabled; Codex membership is disabled")
	}
	limitations = append(limitations, "Gemini native support uses Gemini Developer API keys; Google account and Code Assist OAuth credentials are not accepted")
	writeJSON(w, http.StatusOK, map[string]any{
		"version": a.cfg.Version,
		"ready":   true,
		"storage": "sqlite-wal",
		"features": map[string]bool{
			"codex_membership_import":       a.cfg.ExperimentalCodexMembership,
			"responses_api":                 true,
			"responses_streaming":           true,
			"codex_membership_oauth":        a.cfg.ExperimentalCodexMembership && a.codexOAuthConfigured(),
			"gemini_native_api":             true,
			"anthropic_native_api":          true,
			"codex_model_discovery":         a.cfg.ExperimentalCodexMembership,
			"upstream_batch_import":         true,
			"account_pool_configuration":    true,
			"account_pool_routing":          a.accountPool != nil,
			"codex_membership_auto_refresh": a.refresh != nil && a.refresh.enabled(),
		},
		"limitations": limitations,
	})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, max int64, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid request.")
		return false
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid request.")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeAdminError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeModelError(w http.ResponseWriter, status int, code, message, reqID string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"message": message, "type": code, "code": code, "request_id": reqID}})
}

func (a *App) serveWeb(w http.ResponseWriter, r *http.Request) {
	clean := filepath.Clean(strings.TrimPrefix(r.URL.Path, "/"))
	if clean == "." {
		clean = "index.html"
	}
	path := filepath.Join(a.cfg.WebDir, clean)
	root, err1 := filepath.Abs(a.cfg.WebDir)
	resolved, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil || (resolved != root && !strings.HasPrefix(resolved, root+string(os.PathSeparator))) {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() {
		resolved = filepath.Join(root, "index.html")
		info, err = os.Stat(resolved)
		if err != nil || info.IsDir() {
			http.NotFound(w, r)
			return
		}
	}
	if kind := mime.TypeByExtension(filepath.Ext(resolved)); kind != "" {
		w.Header().Set("Content-Type", kind)
	}
	http.ServeFile(w, r, resolved)
}

func listenIsLoopback(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func ValidateListenConfig(cfg Config) error {
	certSet := strings.TrimSpace(cfg.TLSCert) != ""
	keySet := strings.TrimSpace(cfg.TLSKey) != ""
	if certSet != keySet {
		return errors.New("--tls-cert and --tls-key must be provided together")
	}
	if !listenIsLoopback(cfg.Listen) && !certSet {
		return errors.New("non-loopback listening requires --tls-cert and --tls-key")
	}
	return nil
}

func (a *App) Serve(ctx context.Context) error {
	server := &http.Server{
		Addr:              a.cfg.Listen,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      0,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	errCh := make(chan error, 1)
	go func() {
		var err error
		if a.cfg.TLSCert != "" {
			err = server.ListenAndServeTLS(a.cfg.TLSCert, a.cfg.TLSKey)
		} else {
			err = server.ListenAndServe()
		}
		errCh <- err
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown server: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
