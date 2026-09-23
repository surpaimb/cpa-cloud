package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const accountPoolTestPassword = "a-strong-preview-password"

type accountPoolFixture struct {
	app    *App
	server *httptest.Server
	cookie *http.Cookie
	csrf   string
}

func newAccountPoolFixture(t *testing.T, experimentalCodex bool) *accountPoolFixture {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader(accountPoolTestPassword+"\n")); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", ExperimentalCodexMembership: experimentalCodex})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := app.store.migrateAccountPools(context.Background()); err != nil {
		app.Close()
		t.Fatalf("migrate account pools: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", app.login)
	app.registerAccountPoolHandlers(mux)
	server := httptest.NewServer(requestMiddleware(mux))
	t.Cleanup(func() {
		server.Close()
		if err := app.Close(); err != nil {
			t.Errorf("close app: %v", err)
		}
	})
	cookie, csrf := loginTestAdmin(t, server.URL)
	fixture := &accountPoolFixture{app: app, server: server, cookie: cookie, csrf: csrf}
	return fixture
}

func (f *accountPoolFixture) insertUpstream(t *testing.T, id, provider string) {
	t.Helper()
	credentialState, operationID := any(nil), any(nil)
	if provider == codexMembershipProvider {
		credentialState = codexStateImported
		operationID = "operation-" + id
	}
	_, err := f.app.store.db.Exec(`INSERT INTO upstreams(
		id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id
	) VALUES(?,?,?,?,?,?,?,?,?,?,NULL,?)`, id, "Synthetic "+id, provider, "https://example.invalid/v1", 1, []byte{1, 2, 3}, 1, 1, utcNow(), credentialState, operationID)
	if err != nil {
		t.Fatalf("insert upstream %s: %v", id, err)
	}
}

func (f *accountPoolFixture) insertModel(t *testing.T, id, upstreamID, upstreamModel string) {
	t.Helper()
	if _, err := f.app.store.db.Exec(`INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES(?,?,?,?,?)`, id, upstreamID, upstreamModel, 1, utcNow()); err != nil {
		t.Fatalf("insert model: %v", err)
	}
}

func TestAccountPoolMigrationRollbackRetryAndRestart(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader(accountPoolTestPassword+"\n")); err != nil {
		t.Fatal(err)
	}
	s, err := openStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,operation_id) VALUES('ups_oauth_pool_test','OAuth fixture','codex-membership','https://chatgpt.com',1,X'0102',2,4,'2026-09-23T00:00:00Z','imported_unverified','pool-oauth-operation')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO codex_oauth_bindings(upstream_id,client_id,source,created_at) VALUES('ups_oauth_pool_test','synthetic-client','authorization_code','2026-09-23T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TABLE account_groups(id TEXT PRIMARY KEY,name TEXT,revision INTEGER,created_at TEXT,unexpected TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := s.migrateAccountPools(context.Background()); err == nil {
		t.Fatal("migration accepted incompatible existing table")
	}
	var created int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='account_channels'`).Scan(&created); err != nil || created != 0 {
		t.Fatalf("failed migration left new tables count=%d err=%v", created, err)
	}
	if _, err := s.db.Exec(`DROP TABLE account_groups`); err != nil {
		t.Fatal(err)
	}
	assertAccountPoolOAuthBinding(t, s.db)
	if err := s.migrateAccountPools(context.Background()); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	statements := []string{
		`INSERT INTO account_groups(id,name,revision,created_at) VALUES('grp_restart','Restart group',1,'2026-09-23T00:00:00Z')`,
		`INSERT INTO account_channels(id,name,group_id,revision,created_at) VALUES('chn_restart','Restart channel','grp_restart',1,'2026-09-23T00:00:00Z')`,
		`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES('ups_restart','Restart upstream','openai-compatible','https://example.invalid/v1',1,X'01',1,1,'2026-09-23T00:00:00Z')`,
		`INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES('restart-model','ups_restart','legacy-restart',1,'2026-09-23T00:00:00Z')`,
		`INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES('restart-model',3,'2026-09-23T00:00:00Z')`,
		`INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,priority,weight,max_concurrency,channel_id,position) VALUES('restart-model','ups_restart','provider-restart',7,3,5,'chn_restart',0)`,
	}
	for _, statement := range statements {
		if _, err := s.db.Exec(statement); err != nil {
			t.Fatalf("seed restart state: %v", err)
		}
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	if err := reopened.migrateAccountPools(context.Background()); err != nil {
		t.Fatalf("restart migration: %v", err)
	}
	for _, table := range []string{accountGroupTable, accountChannelTable, modelPoolConfigTable, modelPoolRouteTable, accountPoolAuditTable} {
		var exists int
		if err := reopened.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&exists); err != nil || exists != 1 {
			t.Fatalf("table %s exists=%d err=%v", table, exists, err)
		}
	}
	var foreignKeys int
	if err := reopened.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign keys=%d err=%v", foreignKeys, err)
	}
	assertAccountPoolOAuthBinding(t, reopened.db)
	var revision int64
	var upstreamModel, channelID string
	if err := reopened.db.QueryRow(`SELECT c.revision,r.upstream_model,r.channel_id FROM model_account_pool_configs c JOIN model_account_pool_routes r ON r.model_id=c.model_id WHERE c.model_id='restart-model'`).Scan(&revision, &upstreamModel, &channelID); err != nil || revision != 3 || upstreamModel != "provider-restart" || channelID != "chn_restart" {
		t.Fatalf("restart state revision=%d model=%q channel=%q err=%v", revision, upstreamModel, channelID, err)
	}
}

func TestAccountPoolMigrationRejectsIncompatibleObjects(t *testing.T) {
	tests := []struct {
		name      string
		statement string
	}{
		{
			name:      "view",
			statement: `CREATE VIEW account_groups AS SELECT 'id' AS id,'name' AS name,1 AS revision,'2026-09-23T00:00:00Z' AS created_at`,
		},
		{
			name:      "missing constraints",
			statement: `CREATE TABLE account_groups(id TEXT PRIMARY KEY,name TEXT NOT NULL,revision INTEGER NOT NULL,created_at TEXT NOT NULL)`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dataDir := t.TempDir()
			if err := Initialize(context.Background(), dataDir, strings.NewReader(accountPoolTestPassword+"\n")); err != nil {
				t.Fatal(err)
			}
			s, err := openStore(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.close()
			if _, err := s.db.Exec(test.statement); err != nil {
				t.Fatal(err)
			}
			if err := s.migrateAccountPools(context.Background()); err == nil {
				t.Fatal("migration accepted incompatible object")
			}
			var created int
			if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='account_channels'`).Scan(&created); err != nil || created != 0 {
				t.Fatalf("failed migration left new tables count=%d err=%v", created, err)
			}
		})
	}
}

func TestAccountPoolAdminAPIValidationCASPersistenceAndAudit(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	f.insertUpstream(t, "ups_openai_a", "openai-compatible")
	f.insertUpstream(t, "ups_openai_b", "openai-compatible")
	f.insertUpstream(t, "ups_anthropic", anthropicAPIKeyProvider)
	f.insertUpstream(t, "ups_codex", codexMembershipProvider)
	f.insertModel(t, "public-model", "ups_openai_a", "legacy-model")
	f.insertModel(t, "codex-public", "ups_openai_a", "legacy-codex-model")
	if _, err := f.app.store.db.Exec(`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('emp_pool','Pool user','active','selected',1,?)`, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO employee_models(employee_id,model_id) VALUES('emp_pool','public-model')`); err != nil {
		t.Fatal(err)
	}

	missingCSRF := requestJSON(t, http.MethodPost, f.server.URL+"/admin/api/v1/account-groups", `{"name":"Primary"}`, f.cookie, "", f.server.URL)
	if missingCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", missingCSRF.StatusCode, readBody(missingCSRF))
	}
	missingCSRF.Body.Close()

	groupResponse := requestJSON(t, http.MethodPost, f.server.URL+"/admin/api/v1/account-groups", `{"name":"Primary"}`, f.cookie, f.csrf, f.server.URL)
	if groupResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create group status=%d body=%s", groupResponse.StatusCode, readBody(groupResponse))
	}
	var group accountGroupView
	decodeResponse(t, groupResponse, &group)
	groupResponse.Body.Close()
	if group.ID == "" || group.Name != "Primary" || group.Revision != 1 {
		t.Fatalf("group=%+v", group)
	}

	staleGroup := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/account-groups/"+group.ID, `{"expected_revision":2,"name":"Stale"}`, f.cookie, f.csrf, f.server.URL)
	if staleGroup.StatusCode != http.StatusConflict {
		t.Fatalf("stale group status=%d body=%s", staleGroup.StatusCode, readBody(staleGroup))
	}
	assertAdminError(t, staleGroup, "revision_conflict", "The object was changed by another request.")
	staleGroup.Body.Close()

	updatedGroup := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/account-groups/"+group.ID, `{"expected_revision":1,"name":"Primary Updated"}`, f.cookie, f.csrf, f.server.URL)
	if updatedGroup.StatusCode != http.StatusOK {
		t.Fatalf("update group status=%d body=%s", updatedGroup.StatusCode, readBody(updatedGroup))
	}
	decodeResponse(t, updatedGroup, &group)
	updatedGroup.Body.Close()
	if group.Revision != 2 || group.Name != "Primary Updated" {
		t.Fatalf("updated group=%+v", group)
	}

	channelBody := fmt.Sprintf(`{"name":"Default","group_id":%q}`, group.ID)
	channelResponse := requestJSON(t, http.MethodPost, f.server.URL+"/admin/api/v1/channels", channelBody, f.cookie, f.csrf, f.server.URL)
	if channelResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create channel status=%d body=%s", channelResponse.StatusCode, readBody(channelResponse))
	}
	var channel accountChannelView
	decodeResponse(t, channelResponse, &channel)
	channelResponse.Body.Close()
	if channel.ID == "" || channel.GroupID == nil || *channel.GroupID != group.ID || channel.Revision != 1 {
		t.Fatalf("channel=%+v", channel)
	}

	groups := requestJSON(t, http.MethodGet, f.server.URL+"/admin/api/v1/account-groups", "", f.cookie, "", "")
	if groups.StatusCode != http.StatusOK {
		t.Fatalf("list groups status=%d body=%s", groups.StatusCode, readBody(groups))
	}
	groups.Body.Close()
	channels := requestJSON(t, http.MethodGet, f.server.URL+"/admin/api/v1/channels", "", f.cookie, "", "")
	if channels.StatusCode != http.StatusOK {
		t.Fatalf("list channels status=%d body=%s", channels.StatusCode, readBody(channels))
	}
	channels.Body.Close()

	legacyResponse := requestJSON(t, http.MethodGet, f.server.URL+"/admin/api/v1/models/public-model/accounts", "", f.cookie, "", "")
	if legacyResponse.StatusCode != http.StatusOK {
		t.Fatalf("legacy accounts status=%d body=%s", legacyResponse.StatusCode, readBody(legacyResponse))
	}
	var legacy modelAccountsView
	decodeResponse(t, legacyResponse, &legacy)
	legacyResponse.Body.Close()
	if legacy.Revision != 0 || len(legacy.Items) != 1 || legacy.Items[0].UpstreamID != "ups_openai_a" || legacy.Items[0].UpstreamModel != "legacy-model" {
		t.Fatalf("legacy view=%+v", legacy)
	}
	tooMany := putModelAccountsRequest{ExpectedRevision: 0, Items: make([]modelAccountView, maxModelAccounts+1)}
	for i := range tooMany.Items {
		tooMany.Items[i] = modelAccountView{UpstreamID: fmt.Sprintf("ups_%d", i), UpstreamModel: "gpt", Weight: 1, MaxConcurrency: 1}
	}
	tooManyJSON, err := json.Marshal(tooMany)
	if err != nil {
		t.Fatal(err)
	}
	invalidPools := []string{
		`{"expected_revision":0,"items":[]}`,
		`{"expected_revision":0,"items":[{"upstream_id":"ups_openai_a","upstream_model":"gpt-a","priority":0,"weight":1,"max_concurrency":1},{"upstream_id":"ups_openai_a","upstream_model":"gpt-b","priority":0,"weight":1,"max_concurrency":1}]}`,
		`{"expected_revision":0,"items":[{"upstream_id":"ups_missing","upstream_model":"gpt","priority":0,"weight":1,"max_concurrency":1}]}`,
		`{"expected_revision":0,"items":[{"upstream_id":"ups_openai_a","upstream_model":" bad ","priority":0,"weight":1,"max_concurrency":1}]}`,
		`{"expected_revision":0,"items":[{"upstream_id":"ups_openai_a","upstream_model":"gpt","priority":1000001,"weight":1,"max_concurrency":1}]}`,
		`{"expected_revision":0,"items":[{"upstream_id":"ups_openai_a","upstream_model":"gpt","priority":0,"weight":10001,"max_concurrency":1}]}`,
		`{"expected_revision":0,"items":[{"upstream_id":"ups_openai_a","upstream_model":"gpt","priority":0,"weight":1,"max_concurrency":1025}]}`,
		string(tooManyJSON),
	}
	for index, body := range invalidPools {
		response := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/models/public-model/accounts", body, f.cookie, f.csrf, f.server.URL)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid pool %d status=%d body=%s", index, response.StatusCode, readBody(response))
		}
		response.Body.Close()
	}
	var prematureConfigs int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM model_account_pool_configs WHERE model_id='public-model'`).Scan(&prematureConfigs); err != nil || prematureConfigs != 0 {
		t.Fatalf("invalid writes left config count=%d err=%v", prematureConfigs, err)
	}

	putBody := fmt.Sprintf(`{"expected_revision":0,"items":[
		{"upstream_id":"ups_openai_a","upstream_model":"gpt-a","priority":10,"weight":2,"max_concurrency":4,"channel_id":%q},
		{"upstream_id":"ups_openai_b","upstream_model":"gpt-b","priority":5,"weight":1,"max_concurrency":2}
	]}`, channel.ID)
	putResponse := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/models/public-model/accounts", putBody, f.cookie, f.csrf, f.server.URL)
	if putResponse.StatusCode != http.StatusOK {
		t.Fatalf("put accounts status=%d body=%s", putResponse.StatusCode, readBody(putResponse))
	}
	var configured modelAccountsView
	decodeResponse(t, putResponse, &configured)
	putResponse.Body.Close()
	if configured.Revision != 1 || len(configured.Items) != 2 {
		t.Fatalf("configured=%+v", configured)
	}

	stalePool := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/models/public-model/accounts", putBody, f.cookie, f.csrf, f.server.URL)
	if stalePool.StatusCode != http.StatusConflict {
		t.Fatalf("stale pool status=%d body=%s", stalePool.StatusCode, readBody(stalePool))
	}
	assertAdminError(t, stalePool, "revision_conflict", "The object was changed by another request.")
	stalePool.Body.Close()

	mixedProvider := `{"expected_revision":1,"items":[
		{"upstream_id":"ups_openai_a","upstream_model":"gpt-a","priority":0,"weight":1,"max_concurrency":1},
		{"upstream_id":"ups_anthropic","upstream_model":"claude-a","priority":0,"weight":1,"max_concurrency":1}
	]}`
	mixedResponse := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/models/public-model/accounts", mixedProvider, f.cookie, f.csrf, f.server.URL)
	if mixedResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("mixed provider status=%d body=%s", mixedResponse.StatusCode, readBody(mixedResponse))
	}
	assertAdminError(t, mixedResponse, "provider_mismatch", "All accounts in a model pool must use the same provider kind.")
	mixedResponse.Body.Close()

	codexBody := `{"expected_revision":0,"items":[{"upstream_id":"ups_codex","upstream_model":"gpt-codex","priority":0,"weight":1,"max_concurrency":1}]}`
	codexResponse := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/models/codex-public/accounts", codexBody, f.cookie, f.csrf, f.server.URL)
	if codexResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("disabled Codex status=%d body=%s", codexResponse.StatusCode, readBody(codexResponse))
	}
	assertAdminError(t, codexResponse, "unsupported_feature", "Codex membership account pools are disabled.")
	codexResponse.Body.Close()

	configuredResponse := requestJSON(t, http.MethodGet, f.server.URL+"/admin/api/v1/models/public-model/accounts", "", f.cookie, "", "")
	if configuredResponse.StatusCode != http.StatusOK {
		t.Fatalf("get configured status=%d body=%s", configuredResponse.StatusCode, readBody(configuredResponse))
	}
	decodeResponse(t, configuredResponse, &configured)
	configuredResponse.Body.Close()
	if configured.Revision != 1 || len(configured.Items) != 2 || configured.Items[0].UpstreamID != "ups_openai_a" || configured.Items[1].UpstreamID != "ups_openai_b" {
		t.Fatalf("pool changed after rejected write: %+v", configured)
	}

	var employeeModels int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM employee_models WHERE employee_id='emp_pool' AND model_id='public-model'`).Scan(&employeeModels); err != nil || employeeModels != 1 {
		t.Fatalf("employee model permission changed count=%d err=%v", employeeModels, err)
	}
	var auditCount int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_audit`).Scan(&auditCount); err != nil || auditCount != 4 {
		t.Fatalf("audit count=%d err=%v", auditCount, err)
	}
	assertAccountPoolAuditMetadataOnly(t, f.app.store.db)
}

func TestAccountPoolListReadLimitIsFixedAndBounded(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	tx, err := f.app.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxAccountGroups; i++ {
		if _, err := tx.Exec(`INSERT INTO account_groups(id,name,revision,created_at) VALUES(?,?,1,?)`, fmt.Sprintf("grp_limit_%04d", i), fmt.Sprintf("Group %04d", i), utcNow()); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, http.MethodGet, f.server.URL+"/admin/api/v1/account-groups", "", f.cookie, "", "")
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("read limit status=%d body=%s", response.StatusCode, readBody(response))
	}
	assertAdminError(t, response, "read_limit_exceeded", "Too many account groups to return.")
	response.Body.Close()
}

func TestAccountPoolConcurrentFirstWriteHasSingleCASWinner(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	f.insertUpstream(t, "ups_cas", "openai-compatible")
	f.insertModel(t, "cas-model", "ups_cas", "legacy")
	body := `{"expected_revision":0,"items":[{"upstream_id":"ups_cas","upstream_model":"gpt-cas","priority":0,"weight":1,"max_concurrency":2}]}`

	statuses := make(chan int, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			response := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/models/cas-model/accounts", body, f.cookie, f.csrf, f.server.URL)
			statuses <- response.StatusCode
			response.Body.Close()
		}()
	}
	workers.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("CAS statuses=%v", counts)
	}
	var configs, routes, audits int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM model_account_pool_configs WHERE model_id='cas-model'`).Scan(&configs); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM model_account_pool_routes WHERE model_id='cas-model'`).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_audit WHERE target_id='cas-model'`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if configs != 1 || routes != 1 || audits != 1 {
		t.Fatalf("CAS state configs=%d routes=%d audits=%d", configs, routes, audits)
	}
}

func TestAccountPoolWriteRollbackAndRetry(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	f.insertUpstream(t, "ups_rollback_a", "openai-compatible")
	f.insertUpstream(t, "ups_rollback_b", "openai-compatible")
	f.insertModel(t, "rollback-model", "ups_rollback_a", "legacy")
	if _, err := f.app.store.db.Exec(`CREATE TRIGGER account_pool_test_failure BEFORE INSERT ON model_account_pool_routes WHEN NEW.upstream_id='ups_rollback_b' BEGIN SELECT RAISE(ABORT,'synthetic route failure'); END`); err != nil {
		t.Fatal(err)
	}
	body := `{"expected_revision":0,"items":[{"upstream_id":"ups_rollback_a","upstream_model":"gpt-a","priority":0,"weight":1,"max_concurrency":1},{"upstream_id":"ups_rollback_b","upstream_model":"gpt-b","priority":0,"weight":1,"max_concurrency":1}]}`
	failed := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/models/rollback-model/accounts", body, f.cookie, f.csrf, f.server.URL)
	if failed.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failed write status=%d body=%s", failed.StatusCode, readBody(failed))
	}
	assertAdminError(t, failed, "storage_unavailable", "Service is temporarily unavailable.")
	failed.Body.Close()
	for _, table := range []string{modelPoolConfigTable, modelPoolRouteTable, accountPoolAuditTable} {
		var count int
		if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("failed write left %s rows=%d err=%v", table, count, err)
		}
	}
	if _, err := f.app.store.db.Exec(`DROP TRIGGER account_pool_test_failure`); err != nil {
		t.Fatal(err)
	}
	retried := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/models/rollback-model/accounts", body, f.cookie, f.csrf, f.server.URL)
	if retried.StatusCode != http.StatusOK {
		t.Fatalf("retry write status=%d body=%s", retried.StatusCode, readBody(retried))
	}
	var view modelAccountsView
	decodeResponse(t, retried, &view)
	retried.Body.Close()
	if view.Revision != 1 || len(view.Items) != 2 {
		t.Fatalf("retry view=%+v", view)
	}
}

func assertAccountPoolAuditMetadataOnly(t *testing.T, db *sql.DB) {
	t.Helper()
	rows, err := db.Query(`PRAGMA table_info(account_pool_audit)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	allowed := map[string]bool{"id": true, "actor_id": true, "action": true, "target_type": true, "target_id": true, "result": true, "occurred_at": true}
	seen := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if !allowed[name] {
			t.Fatalf("audit table contains non-metadata column %q", name)
		}
		seen[name] = true
	}
	if len(seen) != len(allowed) {
		t.Fatalf("audit columns=%v", seen)
	}
	var encoded string
	if err := db.QueryRow(`SELECT json_group_array(json_object('action',action,'target_type',target_type,'target_id',target_id,'result',result)) FROM account_pool_audit`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var metadata []map[string]string
	if err := json.Unmarshal([]byte(encoded), &metadata); err != nil || len(metadata) == 0 {
		t.Fatalf("audit metadata invalid len=%d err=%v", len(metadata), err)
	}
}

func assertAccountPoolOAuthBinding(t *testing.T, db *sql.DB) {
	t.Helper()
	var clientID, source string
	var revision int64
	var ciphertext []byte
	if err := db.QueryRow(`SELECT b.client_id,b.source,u.revision,u.credential_ciphertext FROM codex_oauth_bindings b JOIN upstreams u ON u.id=b.upstream_id WHERE b.upstream_id='ups_oauth_pool_test'`).Scan(&clientID, &source, &revision, &ciphertext); err != nil {
		t.Fatalf("read preserved OAuth binding: %v", err)
	}
	if clientID != "synthetic-client" || source != "authorization_code" || revision != 4 || len(ciphertext) != 2 || ciphertext[0] != 1 || ciphertext[1] != 2 {
		t.Fatalf("OAuth binding changed client=%q source=%q revision=%d ciphertext=%x", clientID, source, revision, ciphertext)
	}
}
