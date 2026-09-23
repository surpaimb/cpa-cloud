package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpstreamHealthLocalCredentialHTTPContractAndObservation(t *testing.T) {
	var upstreamCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		upstreamCalls.Add(1)
	}))
	defer upstream.Close()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	item := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Health local", "openai-compatible", upstream.URL, "health-local-private")
	operationID := "10000000-0000-4000-8000-000000000001"
	body := healthRequestBody(operationID, item.Revision, "local_credential")

	response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+item.ID+"/tests", body, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("local test status=%d body=%s", response.StatusCode, readBody(response))
	}
	var completed upstreamTestOperationView
	decodeResponse(t, response, &completed)
	if completed.State != "completed" || completed.ResultCode == nil || *completed.ResultCode != "local_credential_ok" || completed.TestedRevision == nil || *completed.TestedRevision != item.Revision {
		t.Fatalf("unexpected local operation: %+v", completed)
	}
	if upstreamCalls.Load() != 0 {
		t.Fatalf("local credential check made %d network calls", upstreamCalls.Load())
	}

	replay := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+item.ID+"/tests", body, cookie, csrf, server.URL)
	if replay.StatusCode != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replay.StatusCode, readBody(replay))
	}
	var replayed upstreamTestOperationView
	decodeResponse(t, replay, &replayed)
	if replayed.OperationID != completed.OperationID || replayed.FinishedAt == nil || completed.FinishedAt == nil || *replayed.FinishedAt != *completed.FinishedAt {
		t.Fatalf("replay changed operation: before=%+v after=%+v", completed, replayed)
	}

	conflict := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+item.ID+"/tests", healthRequestBody(operationID, item.Revision, "catalog"), cookie, csrf, server.URL)
	if conflict.StatusCode != http.StatusConflict || !strings.Contains(readBody(conflict), `"code":"operation_conflict"`) {
		t.Fatal("same operation with different input was not rejected")
	}
	duplicate := `{"operation_id":"` + operationID + `","operation_id":"` + operationID + `","expected_revision":1,"scope":"catalog"}`
	invalid := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+item.ID+"/tests", duplicate, cookie, csrf, server.URL)
	if invalid.StatusCode != http.StatusBadRequest || !strings.Contains(readBody(invalid), `"code":"invalid_request"`) {
		t.Fatal("duplicate JSON key was not rejected")
	}

	get := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/upstreams/"+item.ID+"/tests/"+operationID, "", cookie, "", server.URL)
	if get.StatusCode != http.StatusOK {
		t.Fatalf("GET operation status=%d body=%s", get.StatusCode, readBody(get))
	}
	get.Body.Close()
	list := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/upstreams", "", cookie, "", server.URL)
	var listed struct {
		Items []upstreamView `json:"items"`
	}
	decodeResponse(t, list, &listed)
	if len(listed.Items) != 1 || listed.Items[0].LatestObservation == nil || listed.Items[0].LatestObservation.OperationID != operationID || listed.Items[0].LatestObservation.ResultCode != "local_credential_ok" {
		t.Fatalf("latest observation=%+v", listed.Items)
	}

	var schema string
	if err := app.store.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, upstreamHealthTable).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(schema), "credential_ciphertext") || strings.Contains(strings.ToLower(schema), "secret_value") || strings.Contains(strings.ToLower(schema), "response_body") || strings.Contains(schema, "health-local-private") {
		t.Fatalf("health schema contains secret-bearing fields: %s", schema)
	}
}

func TestUpstreamHealthAnthropicPaginationUsesOfficialHeadersAndRejectsCursorLoop(t *testing.T) {
	for _, test := range []struct {
		name       string
		loop       bool
		wantResult string
		wantCalls  int32
	}{
		{name: "two pages", wantResult: "catalog_ok", wantCalls: 2},
		{name: "repeated cursor", loop: true, wantResult: "invalid_response", wantCalls: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				call := calls.Add(1)
				if r.URL.Path != "/v1/models" || r.Header.Get("X-Api-Key") != "anthropic-health-secret" || r.Header.Get("Authorization") != "" || r.Header.Get("Anthropic-Version") != "2023-06-01" || r.URL.Query().Get("limit") != "1000" {
					t.Errorf("unexpected request path=%s query=%s headers=%v", r.URL.Path, r.URL.RawQuery, r.Header)
				}
				if call == 1 {
					if r.URL.Query().Get("after_id") != "" {
						t.Errorf("first after_id=%q", r.URL.Query().Get("after_id"))
					}
					_, _ = io.WriteString(w, `{"data":[{"id":"claude-a"}],"has_more":true,"last_id":"cursor-a"}`)
					return
				}
				if r.URL.Query().Get("after_id") != "cursor-a" {
					t.Errorf("second after_id=%q", r.URL.Query().Get("after_id"))
				}
				if test.loop {
					_, _ = io.WriteString(w, `{"data":[],"has_more":true,"last_id":"cursor-a"}`)
				} else {
					_, _ = io.WriteString(w, `{"data":[{"id":"claude-b"}],"has_more":false,"last_id":"claude-b"}`)
				}
			}))
			defer upstream.Close()
			_, server, cookie, csrf := newModelAdmissionApp(t, false)
			item := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Claude health", anthropicAPIKeyProvider, upstream.URL, "anthropic-health-secret")
			response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+item.ID+"/tests", healthRequestBody("20000000-0000-4000-8000-000000000001", item.Revision, "catalog"), cookie, csrf, server.URL)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("catalog status=%d body=%s", response.StatusCode, readBody(response))
			}
			var operation upstreamTestOperationView
			decodeResponse(t, response, &operation)
			if operation.ResultCode == nil || *operation.ResultCode != test.wantResult || calls.Load() != test.wantCalls {
				t.Fatalf("operation=%+v calls=%d", operation, calls.Load())
			}
		})
	}
}

func TestUpstreamHealthCancellationAfterDurableAdmissionFinishesWithoutReplay(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer upstream.Close()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	item := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Cancellation", "openai-compatible", upstream.URL, "cancel-private")
	coordinator := app.healthTests
	reached := make(chan struct{})
	release := make(chan struct{})
	coordinator.beforeClaim = func() {
		close(reached)
		<-release
	}
	ctx, cancel := context.WithCancel(context.Background())
	type result struct {
		view   upstreamTestOperationView
		status int
		code   string
		err    error
	}
	resultChannel := make(chan result, 1)
	operationID := "30000000-0000-4000-8000-000000000001"
	go func() {
		view, status, code, err := coordinator.run(ctx, item.ID, operationID, item.Revision, "catalog")
		resultChannel <- result{view: view, status: status, code: code, err: err}
	}()
	select {
	case <-reached:
	case <-time.After(2 * time.Second):
		t.Fatal("operation was not durably admitted")
	}
	cancel()
	close(release)
	got := <-resultChannel
	if got.err != nil || got.status != http.StatusOK || got.code != "" || got.view.ResultCode == nil || *got.view.ResultCode != "cancelled" {
		t.Fatalf("cancelled operation=%+v status=%d code=%q err=%v", got.view, got.status, got.code, got.err)
	}
	if calls.Load() != 0 {
		t.Fatalf("cancelled operation made %d network calls", calls.Load())
	}
	coordinator.beforeClaim = nil
	replayed, status, code, err := coordinator.run(context.Background(), item.ID, operationID, item.Revision, "catalog")
	if err != nil || status != http.StatusOK || code != "" || replayed.ResultCode == nil || *replayed.ResultCode != "cancelled" || calls.Load() != 0 {
		t.Fatalf("replay=%+v status=%d code=%q err=%v calls=%d", replayed, status, code, err, calls.Load())
	}
}

func TestUpstreamHealthFinalizeFailureRecoversInterruptedWithoutNetworkReplay(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer upstream.Close()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	item := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Finalize recovery", "openai-compatible", upstream.URL, "finalize-private")
	app.healthTests.beforeFinalize = func() {
		if _, err := app.store.db.Exec(`CREATE TRIGGER fail_health_finalize BEFORE UPDATE ON upstream_test_operations
			WHEN NEW.state='completed' BEGIN SELECT RAISE(ABORT,'forced finalize failure'); END`); err != nil {
			t.Errorf("create finalize trigger: %v", err)
		}
	}
	operationID := "35000000-0000-4000-8000-000000000001"
	_, _, _, runErr := app.healthTests.run(context.Background(), item.ID, operationID, item.Revision, "catalog")
	if runErr == nil {
		t.Fatal("forced terminal write failure was ignored")
	}
	app.healthTests.beforeFinalize = nil
	if _, err := app.store.db.Exec(`DROP TRIGGER fail_health_finalize`); err != nil {
		t.Fatal(err)
	}
	view, err := app.healthTests.get(context.Background(), operationID)
	if err != nil || view.State != "completed" || view.ResultCode == nil || *view.ResultCode != "interrupted" {
		t.Fatalf("runtime orphan recovery=%+v err=%v", view, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("network calls after recovery=%d", calls.Load())
	}
	replay, status, code, err := app.healthTests.run(context.Background(), item.ID, operationID, item.Revision, "catalog")
	if err != nil || status != http.StatusOK || code != "" || replay.ResultCode == nil || *replay.ResultCode != "interrupted" || calls.Load() != 1 {
		t.Fatalf("replay=%+v status=%d code=%q err=%v calls=%d", replay, status, code, err, calls.Load())
	}
}

func TestUpstreamHealthLocalCredentialRejectsInvalidHTTPHeaderValue(t *testing.T) {
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	item := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Invalid header", "openai-compatible", "http://127.0.0.1:1", "initial-key")
	ciphertext, err := app.secrets.encryptCredential(item.ID, "invalid\r\nheader")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`UPDATE upstreams SET credential_ciphertext=? WHERE id=?`, ciphertext, item.ID); err != nil {
		t.Fatal(err)
	}
	view, status, code, err := app.healthTests.run(context.Background(), item.ID, "36000000-0000-4000-8000-000000000001", item.Revision, "local_credential")
	if err != nil || status != http.StatusOK || code != "" || view.ResultCode == nil || *view.ResultCode != "authentication_failed" {
		t.Fatalf("invalid header local check=%+v status=%d code=%q err=%v", view, status, code, err)
	}
}

func TestUpstreamHealthCapacityPerAccountAndStaleFinalization(t *testing.T) {
	started := make(chan string, upstreamHealthMaxRunning)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- r.Header.Get("Authorization")
		<-release
		_, _ = io.WriteString(w, `{"data":[]}`)
	}))
	defer upstream.Close()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	items := make([]upstreamView, 0, upstreamHealthMaxRunning+1)
	for index := 0; index < upstreamHealthMaxRunning+1; index++ {
		items = append(items, createModelAdmissionUpstream(t, server.URL, cookie, csrf, "capacity", "openai-compatible", upstream.URL, "capacity-secret"))
	}
	type outcome struct {
		view upstreamTestOperationView
		err  error
	}
	outcomes := make(chan outcome, upstreamHealthMaxRunning)
	for index := 0; index < upstreamHealthMaxRunning; index++ {
		index := index
		go func() {
			view, _, _, err := app.healthTests.run(context.Background(), items[index].ID, healthOperationID(index), items[index].Revision, "catalog")
			outcomes <- outcome{view: view, err: err}
		}()
	}
	for index := 0; index < upstreamHealthMaxRunning; index++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("catalog tests did not fill global capacity")
		}
	}
	active, status, code, err := app.healthTests.run(context.Background(), items[0].ID, healthOperationID(0), items[0].Revision, "catalog")
	if err != nil || status != http.StatusAccepted || code != "" || active.State != "in_progress" {
		t.Fatalf("active idempotent replay=%+v status=%d code=%q err=%v", active, status, code, err)
	}
	_, status, code, err = app.healthTests.run(context.Background(), items[0].ID, "40000000-0000-4000-8000-000000000099", items[0].Revision, "catalog")
	if err != nil || status != http.StatusConflict || code != "test_in_progress" {
		t.Fatalf("same account admission status=%d code=%q err=%v", status, code, err)
	}
	_, status, code, err = app.healthTests.run(context.Background(), items[4].ID, "40000000-0000-4000-8000-000000000100", items[4].Revision, "catalog")
	if err != nil || status != http.StatusTooManyRequests || code != "test_capacity_exceeded" {
		t.Fatalf("global admission status=%d code=%q err=%v", status, code, err)
	}

	patchDone := make(chan *http.Response, 1)
	go func() {
		patchDone <- requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/upstreams/"+items[0].ID, marshalTestJSON(t, map[string]any{"expected_revision": items[0].Revision, "api_key": "replacement-secret"}), cookie, csrf, server.URL)
	}()
	select {
	case response := <-patchDone:
		if response.StatusCode != http.StatusOK {
			t.Fatalf("concurrent PATCH status=%d body=%s", response.StatusCode, readBody(response))
		}
		response.Body.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("credential replacement was blocked by catalog network I/O")
	}
	close(release)
	var stale int
	for index := 0; index < upstreamHealthMaxRunning; index++ {
		outcome := <-outcomes
		if outcome.err != nil || outcome.view.ResultCode == nil {
			t.Fatalf("outcome=%+v", outcome)
		}
		if *outcome.view.ResultCode == "stale" {
			stale++
		}
	}
	if stale != 1 {
		t.Fatalf("stale results=%d want=1", stale)
	}
	var rejectedRows int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM upstream_test_operations WHERE operation_id IN (?,?)`, "40000000-0000-4000-8000-000000000099", "40000000-0000-4000-8000-000000000100").Scan(&rejectedRows); err != nil || rejectedRows != 0 {
		t.Fatalf("rejected rows=%d err=%v", rejectedRows, err)
	}
}

func TestUpstreamHealthMigrationStrictRetryAndInterruptedRecovery(t *testing.T) {
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	item := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "migration", "openai-compatible", "http://127.0.0.1:1", "migration-private")
	app.healthTests.Close()

	tests := []struct {
		name string
		ddl  func() string
	}{
		{name: "wrong type", ddl: func() string { return strings.Replace(healthCreateDDL(), "latency_ms INTEGER", "latency_ms TEXT", 1) }},
		{name: "wrong foreign key action", ddl: func() string { return strings.Replace(healthCreateDDL(), "ON DELETE RESTRICT", "ON DELETE CASCADE", 1) }},
		{name: "extra secret column", ddl: func() string {
			return strings.Replace(healthCreateDDL(), "\tCHECK((state=", "\tsecret_value TEXT,\n\tCHECK((state=", 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := app.store.db.Exec(`DROP TABLE IF EXISTS upstream_test_operations`); err != nil {
				t.Fatal(err)
			}
			if _, err := app.store.db.Exec(test.ddl()); err != nil {
				t.Fatal(err)
			}
			if err := app.healthTests.migrate(context.Background()); err == nil {
				t.Fatal("incompatible health schema was accepted")
			}
			var upstreamCount int
			if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM upstreams WHERE id=?`, item.ID).Scan(&upstreamCount); err != nil || upstreamCount != 1 {
				t.Fatalf("existing upstream changed after failed migration: count=%d err=%v", upstreamCount, err)
			}
		})
	}
	if _, err := app.store.db.Exec(`DROP TABLE upstream_test_operations`); err != nil {
		t.Fatal(err)
	}
	if err := app.healthTests.migrate(context.Background()); err != nil {
		t.Fatalf("migration was not retryable after repair: %v", err)
	}
	if _, err := app.store.db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
		t.Fatal(err)
	}
	_, insertErr := app.store.db.Exec(`INSERT INTO upstream_test_operations
		(operation_id,upstream_id,provider_kind,source_snapshot,requested_revision,scope,state,created_at)
		VALUES(?,?,?,'api_key',1,'catalog','pending',?)`, "50000000-0000-4000-8000-000000000001", item.ID, "invalid-provider", utcNow())
	_, disableErr := app.store.db.Exec(`PRAGMA ignore_check_constraints=OFF`)
	if insertErr != nil || disableErr != nil {
		t.Fatalf("insert invalid row=%v restore constraints=%v", insertErr, disableErr)
	}
	if err := app.healthTests.migrate(context.Background()); err == nil {
		t.Fatal("migration accepted a structurally valid table containing an invalid row")
	}
	if _, err := app.store.db.Exec(`DELETE FROM upstream_test_operations`); err != nil {
		t.Fatal(err)
	}
	for index, state := range []string{"pending", "in_progress"} {
		started := any(nil)
		if state == "in_progress" {
			started = utcNow()
		}
		if _, err := app.store.db.Exec(`INSERT INTO upstream_test_operations
			(operation_id,upstream_id,provider_kind,source_snapshot,requested_revision,scope,state,created_at,started_at)
			VALUES(?,?,?,'api_key',1,'catalog',?,?,?)`, healthOperationID(index+10), item.ID, item.ProviderKind, state, utcNow(), started); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.healthTests.migrate(context.Background()); err != nil {
		t.Fatalf("startup recovery failed: %v", err)
	}
	rows, err := app.store.db.Query(`SELECT state,result_code FROM upstream_test_operations ORDER BY operation_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var state, result string
		if err := rows.Scan(&state, &result); err != nil {
			t.Fatal(err)
		}
		if state != "completed" || result != "interrupted" {
			t.Fatalf("recovered state=%q result=%q", state, result)
		}
		count++
	}
	if err := rows.Err(); err != nil || count != 2 {
		t.Fatalf("recovered rows=%d err=%v", count, err)
	}
}

func healthRequestBody(operationID string, revision int64, scope string) string {
	encoded, _ := json.Marshal(map[string]any{"operation_id": operationID, "expected_revision": revision, "scope": scope})
	return string(encoded)
}

func healthOperationID(index int) string {
	return fmt.Sprintf("40000000-0000-4000-8000-%012d", index)
}

func healthCreateDDL() string {
	return strings.Replace(upstreamHealthDDL, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1)
}
