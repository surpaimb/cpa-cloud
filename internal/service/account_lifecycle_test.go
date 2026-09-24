package service

// Independently authored acceptance tests for CPA Cloud's lifecycle contract.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAccountLifecycleCASReferencesTombstonesAndCredentialDestruction(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openTestApp(t, dataDir)
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)

	upstreamResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams",
		`{"name":"lifecycle","provider_kind":"openai-compatible","endpoint":"http://127.0.0.1:19431/v1","api_key":"synthetic-secret"}`,
		cookie, csrf, server.URL)
	if upstreamResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create upstream status=%d body=%s", upstreamResponse.StatusCode, readBody(upstreamResponse))
	}
	var upstream upstreamView
	decodeResponse(t, upstreamResponse, &upstream)

	modelResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models",
		`{"id":"lifecycle-model","upstream_id":`+quoteJSON(upstream.ID)+`,"upstream_model":"provider-v1"}`,
		cookie, csrf, server.URL)
	if modelResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create model status=%d body=%s", modelResponse.StatusCode, readBody(modelResponse))
	}
	var model modelView
	decodeResponse(t, modelResponse, &model)
	if model.Revision != 1 || model.Archived {
		t.Fatalf("new model=%+v", model)
	}

	invalid := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/models/lifecycle-model", `{"expected_revision":0,"enabled":false}`, cookie, csrf, server.URL)
	assertAdminErrorCode(t, invalid, http.StatusBadRequest, "invalid_revision")
	stale := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/models/lifecycle-model", `{"expected_revision":2,"enabled":false}`, cookie, csrf, server.URL)
	assertAdminErrorCode(t, stale, http.StatusConflict, "revision_conflict")

	inUse := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/upstreams/"+upstream.ID, `{"expected_revision":1}`, cookie, csrf, server.URL)
	assertAdminErrorCode(t, inUse, http.StatusConflict, "upstream_in_use")

	archiveModel := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/models/lifecycle-model", `{"expected_revision":1}`, cookie, csrf, server.URL)
	if archiveModel.StatusCode != http.StatusOK {
		t.Fatalf("archive model status=%d body=%s", archiveModel.StatusCode, readBody(archiveModel))
	}
	decodeResponse(t, archiveModel, &model)
	if !model.Archived || model.Enabled || model.Revision != 2 || model.ArchiveResult != "archived" {
		t.Fatalf("archived model=%+v", model)
	}
	repeatModel := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/models/lifecycle-model", `{"expected_revision":1}`, cookie, csrf, server.URL)
	if repeatModel.StatusCode != http.StatusOK {
		t.Fatalf("repeat archive model status=%d body=%s", repeatModel.StatusCode, readBody(repeatModel))
	}
	decodeResponse(t, repeatModel, &model)
	if model.ArchiveResult != "already_archived" || model.Revision != 2 {
		t.Fatalf("repeat archived model=%+v", model)
	}

	defaultModels := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/models", "", cookie, "", "")
	var defaultList struct {
		Items []modelView `json:"items"`
	}
	decodeResponse(t, defaultModels, &defaultList)
	if len(defaultList.Items) != 0 {
		t.Fatalf("default models exposed tombstone: %+v", defaultList.Items)
	}
	archivedModels := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/models?include_archived=true", "", cookie, "", "")
	var archivedList struct {
		Items []modelView `json:"items"`
	}
	decodeResponse(t, archivedModels, &archivedList)
	if len(archivedList.Items) != 1 || !archivedList.Items[0].Archived {
		t.Fatalf("archived model list=%+v", archivedList.Items)
	}
	recreate := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models",
		`{"id":"lifecycle-model","upstream_id":`+quoteJSON(upstream.ID)+`,"upstream_model":"provider-v2"}`,
		cookie, csrf, server.URL)
	assertAdminErrorCode(t, recreate, http.StatusConflict, "already_exists")

	archiveUpstream := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/upstreams/"+upstream.ID, `{"expected_revision":1}`, cookie, csrf, server.URL)
	if archiveUpstream.StatusCode != http.StatusOK {
		t.Fatalf("archive upstream status=%d body=%s", archiveUpstream.StatusCode, readBody(archiveUpstream))
	}
	decodeResponse(t, archiveUpstream, &upstream)
	if !upstream.Archived || upstream.Enabled || upstream.Revision != 2 || upstream.ArchiveResult != "archived" {
		t.Fatalf("archived upstream=%+v", upstream)
	}
	var cipher []byte
	var keyVersion int
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext,key_version FROM upstreams WHERE id=?`, upstream.ID).Scan(&cipher, &keyVersion); err != nil {
		t.Fatal(err)
	}
	if len(cipher) != 0 || keyVersion != 0 {
		t.Fatalf("credential retained: len=%d key_version=%d", len(cipher), keyVersion)
	}
	repeatUpstream := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/upstreams/"+upstream.ID, `{"expected_revision":1}`, cookie, csrf, server.URL)
	if repeatUpstream.StatusCode != http.StatusOK {
		t.Fatalf("repeat archive upstream status=%d body=%s", repeatUpstream.StatusCode, readBody(repeatUpstream))
	}
	decodeResponse(t, repeatUpstream, &upstream)
	if upstream.ArchiveResult != "already_archived" || upstream.Revision != 2 {
		t.Fatalf("repeat archived upstream=%+v", upstream)
	}
	var audits int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM account_lifecycle_audit WHERE target_id IN (?,?)`, upstream.ID, model.ID).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 4 {
		t.Fatalf("successful lifecycle audits=%d", audits)
	}
}

func TestModelLifecycleRejectsTargetChangeWithNonEmptyPool(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openTestApp(t, dataDir)
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)
	first := createLifecycleUpstream(t, server.URL, cookie, csrf, "first", 19432)
	second := createLifecycleUpstream(t, server.URL, cookie, csrf, "second", 19433)
	created := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models", `{"id":"pooled-model","upstream_id":`+quoteJSON(first.ID)+`,"upstream_model":"provider-v1"}`, cookie, csrf, server.URL)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create model status=%d body=%s", created.StatusCode, readBody(created))
	}
	created.Body.Close()
	if _, err := app.store.db.Exec(`INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES('pooled-model',1,?)`, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,priority,weight,max_concurrency,position) VALUES('pooled-model',?,'provider-v2',0,1,1,0)`, second.ID); err != nil {
		t.Fatal(err)
	}
	poolReference := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/upstreams/"+second.ID, `{"expected_revision":1}`, cookie, csrf, server.URL)
	assertAdminErrorCode(t, poolReference, http.StatusConflict, "upstream_in_use")
	response := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/models/pooled-model", `{"expected_revision":1,"upstream_id":`+quoteJSON(second.ID)+`,"upstream_model":"provider-v2"}`, cookie, csrf, server.URL)
	assertAdminErrorCode(t, response, http.StatusConflict, "model_pool_not_empty")
}

func TestArchivedModelsDoNotDispatchAcrossFourProtocols(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer provider.Close()
	app := openTestApp(t, dataDir)
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)

	createUpstream := func(name, kind string) upstreamView {
		response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams", `{"name":`+quoteJSON(name)+`,"provider_kind":`+quoteJSON(kind)+`,"endpoint":`+quoteJSON(provider.URL)+`,"api_key":"synthetic"}`, cookie, csrf, server.URL)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create %s upstream status=%d body=%s", kind, response.StatusCode, readBody(response))
		}
		var item upstreamView
		decodeResponse(t, response, &item)
		return item
	}
	openAI := createUpstream("OpenAI", "openai-compatible")
	anthropic := createUpstream("Anthropic", anthropicAPIKeyProvider)
	gemini := createUpstream("Gemini", geminiAPIKeyProvider)
	models := []struct {
		id, upstreamID, actual string
	}{
		{"archived-openai", openAI.ID, "provider-openai"},
		{"archived-anthropic", anthropic.ID, "provider-anthropic"},
		{"archived-gemini", gemini.ID, "provider-gemini"},
	}
	for _, entry := range models {
		created := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models", `{"id":`+quoteJSON(entry.id)+`,"upstream_id":`+quoteJSON(entry.upstreamID)+`,"upstream_model":`+quoteJSON(entry.actual)+`}`, cookie, csrf, server.URL)
		if created.StatusCode != http.StatusCreated {
			t.Fatalf("create model %s status=%d body=%s", entry.id, created.StatusCode, readBody(created))
		}
		created.Body.Close()
	}
	employeeResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees", `{"name":"Lifecycle protocols"}`, cookie, csrf, server.URL)
	if employeeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create employee status=%d body=%s", employeeResponse.StatusCode, readBody(employeeResponse))
	}
	var employeeObject employee
	decodeResponse(t, employeeResponse, &employeeObject)
	key := createTestKey(t, server.URL, employeeObject.ID, "lifecycle-protocol-key", cookie, csrf)
	for _, entry := range models {
		archived := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/models/"+entry.id, `{"expected_revision":1}`, cookie, csrf, server.URL)
		if archived.StatusCode != http.StatusOK {
			t.Fatalf("archive model %s status=%d body=%s", entry.id, archived.StatusCode, readBody(archived))
		}
		archived.Body.Close()
	}
	requests := []*http.Response{
		employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"archived-openai","messages":[]}`, key.Key, context.Background()),
		employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", `{"model":"archived-openai","input":"synthetic"}`, key.Key, context.Background()),
		anthropicEmployeeRequest(t, context.Background(), http.MethodPost, server.URL+"/v1/messages", `{"model":"archived-anthropic","max_tokens":8,"messages":[]}`, key.Key, "bearer"),
		employeeRequest(t, http.MethodPost, server.URL+"/v1beta/models/archived-gemini:generateContent", `{"contents":[{"parts":[{"text":"synthetic"}]}]}`, key.Key, context.Background()),
	}
	for index, response := range requests {
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			t.Fatalf("archived protocol request %d unexpectedly succeeded: %s", index, readBody(response))
		}
		response.Body.Close()
	}
	if hits.Load() != 0 {
		t.Fatalf("archived routes dispatched %d upstream requests", hits.Load())
	}
}

func TestArchivedCodexRejectsCredentialLifecycleAndDiscovery(t *testing.T) {
	app := openOAuthTestApp(t, t.TempDir())
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)
	operationID := "ca04390a-d35a-40d2-bf03-a1ca642ece61"
	auth := codexAdminAuthJSON(t, time.Now().Add(2*time.Hour), "acct-archived", "archived-refresh")
	upstream := importTestCodexUpstream(t, server.URL, cookie, csrf, operationID, auth)
	bindOAuthTestUpstream(t, app, upstream.ID, testOAuthClientID)

	archived := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/upstreams/"+upstream.ID, `{"expected_revision":1}`, cookie, csrf, server.URL)
	if archived.StatusCode != http.StatusOK {
		t.Fatalf("archive Codex status=%d body=%s", archived.StatusCode, readBody(archived))
	}
	archived.Body.Close()
	var ciphertext []byte
	var bindings, refreshStates int
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id=?`, upstream.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT (SELECT COUNT(*) FROM codex_oauth_bindings WHERE upstream_id=?),(SELECT COUNT(*) FROM codex_oauth_refresh_states WHERE upstream_id=?)`, upstream.ID, upstream.ID).Scan(&bindings, &refreshStates); err != nil {
		t.Fatal(err)
	}
	if len(ciphertext) != 0 || bindings != 0 || refreshStates != 0 {
		t.Fatalf("archived Codex retained recoverable state: cipher=%d bindings=%d refresh=%d", len(ciphertext), bindings, refreshStates)
	}

	replace := codexAdminRequest(t, http.MethodPut, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-auth", map[string]any{
		"expected_revision": 2, "auth_json": codexAdminAuthJSON(t, time.Now().Add(3*time.Hour), "acct-archived", "replacement"),
	}, cookie, csrf, server.URL)
	assertCodexAdminError(t, replace, http.StatusConflict, "upstream_archived")
	refresh := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-refresh", map[string]any{"expected_revision": 2}, cookie, csrf, server.URL)
	assertCodexAdminError(t, refresh, http.StatusNotFound, "not_found")
	discovery := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/discover-models", "", cookie, csrf, server.URL)
	assertAdminErrorCode(t, discovery, http.StatusNotFound, "not_found")
	reimport := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-import", map[string]any{
		"name": "must not revive", "auth_json": auth, "operation_id": operationID,
	}, cookie, csrf, server.URL)
	assertCodexAdminError(t, reimport, http.StatusConflict, "already_exists")
}

func TestCodexRefreshAndArchiveUseOneAccountMutationOrder(t *testing.T) {
	app := openOAuthTestApp(t, t.TempDir())
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)
	upstream := importTestCodexUpstream(t, server.URL, cookie, csrf, "73fe7a7c-f581-4f1b-9408-e2605cfc2a31",
		codexAdminAuthJSON(t, time.Now().Add(time.Hour), "acct-archive-race", "archive-race-old"))
	bindOAuthTestUpstream(t, app, upstream.ID, testOAuthClientID)
	started, release := make(chan struct{}), make(chan struct{})
	access := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
	idToken := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-archive-race"}})
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": access, "id_token": idToken, "refresh_token": "archive-race-rotated"}), nil
	})}
	refreshDone := make(chan *http.Response, 1)
	go func() {
		refreshDone <- codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-refresh", map[string]any{"expected_revision": 1}, cookie, csrf, server.URL)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh did not reach the synthetic OAuth transport")
	}
	archiveDone := make(chan *http.Response, 1)
	go func() {
		archiveDone <- requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/upstreams/"+upstream.ID, `{"expected_revision":1}`, cookie, csrf, server.URL)
	}()
	close(release)
	refresh := <-refreshDone
	if refresh.StatusCode != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refresh.StatusCode, readBody(refresh))
	}
	refresh.Body.Close()
	archive := <-archiveDone
	assertAdminErrorCode(t, archive, http.StatusConflict, "revision_conflict")
	var archived int
	var revision int64
	if err := app.store.db.QueryRow(`SELECT archived,revision FROM upstreams WHERE id=?`, upstream.ID).Scan(&archived, &revision); err != nil {
		t.Fatal(err)
	}
	if archived != 0 || revision != 2 {
		t.Fatalf("refresh/archive race state archived=%d revision=%d", archived, revision)
	}
	assertRefreshTokenStored(t, app, upstream.ID, "archive-race-rotated")
}

func TestAccountLifecycleMigrationRollsBackAndRetries(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "migration.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE upstreams(id TEXT PRIMARY KEY)`,
		`CREATE TABLE models(id TEXT PRIMARY KEY)`,
		`CREATE TABLE account_lifecycle_audit(id TEXT PRIMARY KEY)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	store := &store{db: db}
	if err := store.migrateAccountLifecycle(context.Background()); err == nil {
		t.Fatal("migration unexpectedly accepted incompatible audit table")
	}
	columns, err := tableColumns(context.Background(), db, "models")
	if err != nil {
		t.Fatal(err)
	}
	if columns["revision"] || columns["archived"] || columns["archived_at"] {
		t.Fatalf("failed migration left partial columns: %+v", columns)
	}
	if _, err := db.Exec(`DROP TABLE account_lifecycle_audit`); err != nil {
		t.Fatal(err)
	}
	if err := store.migrateAccountLifecycle(context.Background()); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	if err := store.migrateAccountLifecycle(context.Background()); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
}

func TestAccountLifecycleMigrationRejectsLookalikeColumns(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "lookalike.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, statement := range []string{
		`CREATE TABLE upstreams(id TEXT PRIMARY KEY, archived TEXT NOT NULL DEFAULT '0', archived_at TEXT)`,
		`CREATE TABLE models(id TEXT PRIMARY KEY, revision INTEGER NOT NULL DEFAULT 1, archived INTEGER NOT NULL DEFAULT 0, archived_at TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := (&store{db: db}).migrateAccountLifecycle(context.Background()); err == nil {
		t.Fatal("migration accepted lookalike columns without required types and checks")
	}
	var auditTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='account_lifecycle_audit'`).Scan(&auditTables); err != nil {
		t.Fatal(err)
	}
	if auditTables != 0 {
		t.Fatal("failed validation did not roll back audit table creation")
	}
}

func createLifecycleUpstream(t *testing.T, base string, cookie *http.Cookie, csrf, name string, port int) upstreamView {
	t.Helper()
	response := requestJSON(t, http.MethodPost, base+"/admin/api/v1/upstreams",
		`{"name":`+quoteJSON(name)+`,"provider_kind":"openai-compatible","endpoint":"http://127.0.0.1:`+strconv.Itoa(port)+`/v1","api_key":"synthetic"}`,
		cookie, csrf, base)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create upstream status=%d body=%s", response.StatusCode, readBody(response))
	}
	var item upstreamView
	decodeResponse(t, response, &item)
	return item
}

func assertAdminErrorCode(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	if response.StatusCode != status {
		t.Fatalf("status=%d want=%d body=%s", response.StatusCode, status, readBody(response))
	}
	defer response.Body.Close()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != code {
		t.Fatalf("error code=%q want=%q", body.Error.Code, code)
	}
}
