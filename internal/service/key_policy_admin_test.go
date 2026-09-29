// Independently authored KEY-02 administrator adapter tests.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cpacloud.local/server/internal/keypolicy"
)

func TestAppRestartFailsClosedWhenAccessKeyPolicyIsMissing(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	stamp := utcNow()
	if _, err := app.store.db.Exec(`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('employee-missing-policy','Missing policy','active','all',1,?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('key-missing-policy','employee-missing-policy','Missing policy','selector-missing-policy',X'01',1,'operation-missing-policy',?)`, stamp); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if restarted, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test"}); !errors.Is(err, keypolicy.ErrInvalidSchema) {
		if restarted != nil {
			restarted.Close()
		}
		t.Fatalf("restart err=%v, want invalid key policy schema", err)
	}
}

func TestKeyPolicyAdminGetPutCASAndEffectiveView(t *testing.T) {
	fixture := newKeyPolicyAdminFixture(t)

	get := requestJSON(t, http.MethodGet, fixture.server.URL+"/admin/api/v1/keys/key-one/policy", "", fixture.cookie, "", "")
	if get.StatusCode != http.StatusOK {
		t.Fatalf("get policy status=%d body=%s", get.StatusCode, readBody(get))
	}
	var view keyPolicyView
	decodeResponse(t, get, &view)
	if view.Revision != 1 || view.ProtocolMode != keypolicy.ModeAll || len(view.Protocols) != 0 || view.SourceMode != keypolicy.ModeAll || len(view.SourceCIDRs) != 0 || len(view.EffectiveProtocols) != 4 || len(view.EffectiveModels) != 1 || view.EffectiveModels[0] != "public-a" {
		t.Fatalf("default view=%#v", view)
	}

	missingCSRF := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/key-one/policy",
		`{"expected_revision":1,"protocol_mode":"all","protocols":[],"model_mode":"all","models":[]}`, fixture.cookie, "", "")
	assertAdminErrorCode(t, missingCSRF, http.StatusForbidden, "origin_rejected")

	for _, body := range []string{
		`{"expected_revision":1,"protocol_mode":"all","protocols":[],"model_mode":"all"}`,
		`{"expected_revision":1,"protocol_mode":"selected","protocols":["unknown"],"model_mode":"all","models":[]}`,
		`{"expected_revision":1,"protocol_mode":"selected","protocols":["openai-chat","openai-chat"],"model_mode":"all","models":[]}`,
		`{"expected_revision":1,"protocol_mode":"all","protocols":[],"model_mode":"selected","models":["public-b"]}`,
		`{"expected_revision":1,"protocol_mode":"all","protocols":[],"model_mode":"all","models":[],"source_mode":"selected"}`,
		`{"expected_revision":1,"protocol_mode":"all","protocols":[],"model_mode":"all","models":[],"source_cidrs":["192.0.2.0/24"]}`,
		`{"expected_revision":1,"protocol_mode":"all","protocols":[],"model_mode":"all","models":[],"source_mode":"selected","source_cidrs":["invalid"]}`,
	} {
		response := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/key-one/policy", body, fixture.cookie, fixture.csrf, fixture.server.URL)
		assertAdminErrorCode(t, response, http.StatusBadRequest, "invalid_key_policy")
	}

	denyAll := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/key-one/policy",
		`{"expected_revision":1,"protocol_mode":"selected","protocols":[],"model_mode":"selected","models":[]}`, fixture.cookie, fixture.csrf, fixture.server.URL)
	if denyAll.StatusCode != http.StatusOK {
		t.Fatalf("deny-all status=%d body=%s", denyAll.StatusCode, readBody(denyAll))
	}
	decodeResponse(t, denyAll, &view)
	if view.Revision != 2 || len(view.Protocols) != 0 || len(view.Models) != 0 || len(view.EffectiveProtocols) != 0 || len(view.EffectiveModels) != 0 {
		t.Fatalf("deny-all view=%#v", view)
	}

	reset := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/key-one/policy",
		`{"expected_revision":2,"protocol_mode":"all","protocols":[],"model_mode":"all","models":[]}`, fixture.cookie, fixture.csrf, fixture.server.URL)
	if reset.StatusCode != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", reset.StatusCode, readBody(reset))
	}
	decodeResponse(t, reset, &view)
	if view.Revision != 3 || len(view.EffectiveProtocols) != 4 || len(view.EffectiveModels) != 1 {
		t.Fatalf("reset view=%#v", view)
	}

	restrictSource := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/key-one/policy",
		`{"expected_revision":3,"protocol_mode":"all","protocols":[],"model_mode":"all","models":[],"source_mode":"selected","source_cidrs":["2001:0db8::9/48","192.0.2.9"]}`, fixture.cookie, fixture.csrf, fixture.server.URL)
	if restrictSource.StatusCode != http.StatusOK {
		t.Fatalf("source restriction status=%d body=%s", restrictSource.StatusCode, readBody(restrictSource))
	}
	decodeResponse(t, restrictSource, &view)
	if view.Revision != 4 || view.SourceMode != keypolicy.ModeSelected || !slices.Equal(view.SourceCIDRs, []string{"192.0.2.9/32", "2001:db8::/48"}) {
		t.Fatalf("source restriction view=%#v", view)
	}

	preserveSource := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/key-one/policy",
		`{"expected_revision":4,"protocol_mode":"selected","protocols":["openai-responses"],"model_mode":"all","models":[]}`, fixture.cookie, fixture.csrf, fixture.server.URL)
	if preserveSource.StatusCode != http.StatusOK {
		t.Fatalf("preserve source status=%d body=%s", preserveSource.StatusCode, readBody(preserveSource))
	}
	decodeResponse(t, preserveSource, &view)
	if view.Revision != 5 || view.SourceMode != keypolicy.ModeSelected || !slices.Equal(view.SourceCIDRs, []string{"192.0.2.9/32", "2001:db8::/48"}) {
		t.Fatalf("omitted source fields changed source restriction: %#v", view)
	}
	resetSource := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/key-one/policy",
		`{"expected_revision":5,"protocol_mode":"selected","protocols":["openai-responses"],"model_mode":"all","models":[],"source_mode":"all","source_cidrs":[]}`, fixture.cookie, fixture.csrf, fixture.server.URL)
	if resetSource.StatusCode != http.StatusOK {
		t.Fatalf("reset source status=%d body=%s", resetSource.StatusCode, readBody(resetSource))
	}
	decodeResponse(t, resetSource, &view)
	if view.Revision != 6 || view.SourceMode != keypolicy.ModeAll || len(view.SourceCIDRs) != 0 {
		t.Fatalf("source reset view=%#v", view)
	}

	stale := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/key-one/policy",
		`{"expected_revision":1,"protocol_mode":"selected","protocols":[],"model_mode":"selected","models":[]}`, fixture.cookie, fixture.csrf, fixture.server.URL)
	assertAdminErrorCode(t, stale, http.StatusConflict, "revision_conflict")
	notFound := requestJSON(t, http.MethodGet, fixture.server.URL+"/admin/api/v1/keys/not-a-key/policy", "", fixture.cookie, "", "")
	assertAdminErrorCode(t, notFound, http.StatusNotFound, "not_found")

	if _, err := fixture.app.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id='key-one'`, utcNow()); err != nil {
		t.Fatal(err)
	}
	revokedUpdate := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/key-one/policy",
		`{"expected_revision":6,"protocol_mode":"selected","protocols":["openai-responses"],"model_mode":"selected","models":["public-a"]}`, fixture.cookie, fixture.csrf, fixture.server.URL)
	if revokedUpdate.StatusCode != http.StatusOK {
		t.Fatalf("revoked update status=%d body=%s", revokedUpdate.StatusCode, readBody(revokedUpdate))
	}
	decodeResponse(t, revokedUpdate, &view)
	if view.Revision != 7 || len(view.EffectiveProtocols) != 0 || len(view.EffectiveModels) != 0 || view.SourceMode != keypolicy.ModeAll {
		t.Fatalf("revoked key policy restored access: %#v", view)
	}
}

func TestKeyPolicyAdminReplacementUsesCurrentEmployeeAndModelState(t *testing.T) {
	fixture := newKeyPolicyAdminFixture(t)
	body := `{"expected_revision":1,"protocol_mode":"all","protocols":[],"model_mode":"selected","models":["public-a"]}`

	status := fixture.runPutAfterMutation(t, "key-one", body, func() {
		if _, err := fixture.app.store.db.Exec(`DELETE FROM employee_models WHERE employee_id='employee-one'`); err != nil {
			t.Fatal(err)
		}
	})
	if status.Code != http.StatusBadRequest {
		t.Fatalf("employee tightening status=%d body=%s", status.Code, status.Body.String())
	}
	assertRecordedAdminErrorCode(t, status, "invalid_key_policy")

	if _, err := fixture.app.store.db.Exec(`INSERT INTO employee_models(employee_id,model_id) VALUES('employee-one','public-a')`); err != nil {
		t.Fatal(err)
	}
	status = fixture.runPutAfterMutation(t, "key-one", body, func() {
		if _, err := fixture.app.store.db.Exec(`UPDATE models SET enabled=0 WHERE id='public-a'`); err != nil {
			t.Fatal(err)
		}
	})
	if status.Code != http.StatusBadRequest {
		t.Fatalf("disabled model status=%d body=%s", status.Code, status.Body.String())
	}
	assertRecordedAdminErrorCode(t, status, "invalid_key_policy")
	if _, err := fixture.app.store.db.Exec(`UPDATE models SET enabled=1 WHERE id='public-a'`); err != nil {
		t.Fatal(err)
	}
	status = fixture.runPutAfterMutation(t, "key-one", body, func() {
		if _, err := fixture.app.store.db.Exec(`UPDATE upstreams SET enabled=0 WHERE id='upstream-one'`); err != nil {
			t.Fatal(err)
		}
	})
	if status.Code != http.StatusBadRequest {
		t.Fatalf("unavailable route status=%d body=%s", status.Code, status.Body.String())
	}
	assertRecordedAdminErrorCode(t, status, "invalid_key_policy")
	if _, err := fixture.app.store.db.Exec(`UPDATE upstreams SET enabled=1 WHERE id='upstream-one'`); err != nil {
		t.Fatal(err)
	}
	status = fixture.runPutAfterMutation(t, "key-one", body, func() {
		if _, err := fixture.app.store.db.Exec(`UPDATE models SET archived=1 WHERE id='public-a'`); err != nil {
			t.Fatal(err)
		}
	})
	if status.Code != http.StatusBadRequest {
		t.Fatalf("model archival status=%d body=%s", status.Code, status.Body.String())
	}
	assertRecordedAdminErrorCode(t, status, "invalid_key_policy")

	tx, err := fixture.app.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	policy, err := keypolicy.LoadTx(context.Background(), tx, "key-one")
	if err != nil {
		t.Fatal(err)
	}
	if policy.Revision != 1 || policy.ModelMode != keypolicy.ModeAll {
		t.Fatalf("rejected races changed policy: %#v", policy)
	}
}

type keyPolicyAdminFixture struct {
	app    *App
	server *httptest.Server
	cookie *http.Cookie
	csrf   string
}

func newKeyPolicyAdminFixture(t *testing.T) *keyPolicyAdminFixture {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openTestApp(t, dataDir)
	if err := keypolicy.Migrate(context.Background(), app.store.db); err != nil {
		app.Close()
		t.Fatal(err)
	}
	stamp := utcNow()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES(?,?,'active','selected',1,?)`, []any{"employee-one", "Employee one", stamp}},
		{`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES(?,?,'active','all',1,?)`, []any{"employee-two", "Employee two", stamp}},
		{`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,archived,created_at) VALUES(?,?, 'openai-compatible','https://example.invalid/v1',1,X'01',1,1,0,?)`, []any{"upstream-one", "Synthetic", stamp}},
		{`INSERT INTO models(id,upstream_id,upstream_model,wire_protocol,enabled,revision,archived,created_at) VALUES(?,?,?,'openai-chat',1,1,0,?)`, []any{"public-a", "upstream-one", "actual-a", stamp}},
		{`INSERT INTO models(id,upstream_id,upstream_model,wire_protocol,enabled,revision,archived,created_at) VALUES(?,?,?,'openai-chat',1,1,0,?)`, []any{"public-b", "upstream-one", "actual-b", stamp}},
		{`INSERT INTO employee_models(employee_id,model_id) VALUES('employee-one','public-a')`, nil},
		{`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES(?,?,?, ?,X'01',1,?,?)`, []any{"key-one", "employee-one", "Key one", "selector-one", "operation-one", stamp}},
		{`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES(?,?,?, ?,X'02',1,?,?)`, []any{"key-two", "employee-two", "Key two", "selector-two", "operation-two", stamp}},
	}
	for _, statement := range statements {
		if _, err := app.store.db.Exec(statement.query, statement.args...); err != nil {
			app.Close()
			t.Fatal(err)
		}
	}
	tx, err := app.store.db.Begin()
	if err != nil {
		app.Close()
		t.Fatal(err)
	}
	for _, keyID := range []string{"key-one", "key-two"} {
		if err := keypolicy.CreateDefaultTx(context.Background(), tx, keyID, timeNowForKeyPolicyTest()); err != nil {
			tx.Rollback()
			app.Close()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		app.Close()
		t.Fatal(err)
	}

	mainServer := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, mainServer.URL)
	mainServer.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/api/v1/keys/{id}/policy", app.requireAdmin(app.getKeyPolicy, false))
	mux.HandleFunc("PUT /admin/api/v1/keys/{id}/policy", app.requireAdmin(app.putKeyPolicy, true))
	server := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close(); _ = app.Close() })
	return &keyPolicyAdminFixture{app: app, server: server, cookie: cookie, csrf: csrf}
}

func (fixture *keyPolicyAdminFixture) runPutAfterMutation(t *testing.T, keyID, body string, mutate func()) *httptest.ResponseRecorder {
	t.Helper()
	fixture.app.admission.Lock()
	readDone := make(chan struct{})
	requestDone := make(chan *httptest.ResponseRecorder, 1)
	request := httptest.NewRequest(http.MethodPut, "/admin/api/v1/keys/"+keyID+"/policy", nil)
	request.SetPathValue("id", keyID)
	request.Body = newSignalingBody(body, readDone)
	go func() {
		recorder := httptest.NewRecorder()
		fixture.app.putKeyPolicy(recorder, request, adminSession{})
		requestDone <- recorder
	}()
	<-readDone
	mutate()
	fixture.app.admission.Unlock()
	return <-requestDone
}

type signalingBody struct {
	reader *strings.Reader
	done   chan struct{}
	once   sync.Once
}

func newSignalingBody(value string, done chan struct{}) io.ReadCloser {
	return &signalingBody{reader: strings.NewReader(value), done: done}
}

func (body *signalingBody) Read(buffer []byte) (int, error) {
	n, err := body.reader.Read(buffer)
	if err == io.EOF {
		body.once.Do(func() { close(body.done) })
	}
	return n, err
}

func (body *signalingBody) Close() error { return nil }

func assertRecordedAdminErrorCode(t *testing.T, recorder *httptest.ResponseRecorder, code string) {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != code {
		t.Fatalf("error code=%q body=%s", envelope.Error.Code, recorder.Body.String())
	}
}

func timeNowForKeyPolicyTest() time.Time {
	return time.Date(2026, 9, 25, 2, 3, 4, 0, time.UTC)
}
