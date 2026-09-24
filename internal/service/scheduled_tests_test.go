package service

// Independently authored acceptance tests for the scheduled-test contract in
// docs/parity-next-batch-2026-09-24.md. Fixtures are synthetic and loopback-only.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestScheduledTestsHTTPCRUDCSRFAndBoundedRuns(t *testing.T) {
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Scheduled fixture", "openai-compatible", "http://127.0.0.1:1", "scheduled-private")

	missingCSRF := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/scheduled-tests", scheduledPlanBody(upstream.ID, false), cookie, "", "")
	if missingCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", missingCSRF.StatusCode, readBody(missingCSRF))
	}
	missingCSRF.Body.Close()

	createdResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/scheduled-tests", scheduledPlanBody(upstream.ID, false), cookie, csrf, server.URL)
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.StatusCode, readBody(createdResponse))
	}
	var created scheduledTestPlan
	decodeResponse(t, createdResponse, &created)
	if created.Revision != 1 || created.Enabled || created.NextRunAt != nil || created.LatestResult != nil {
		t.Fatalf("created plan=%+v", created)
	}

	invalid := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/scheduled-tests/"+created.ID, `{"expected_revision":1,"interval_seconds":299}`, cookie, csrf, server.URL)
	if invalid.StatusCode != http.StatusBadRequest || !strings.Contains(readBody(invalid), `"code":"invalid_request"`) {
		t.Fatal("invalid interval was accepted")
	}

	updatedResponse := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/scheduled-tests/"+created.ID, `{"expected_revision":1,"name":"Catalog reachability","scope":"catalog","interval_seconds":600,"enabled":true}`, cookie, csrf, server.URL)
	if updatedResponse.StatusCode != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updatedResponse.StatusCode, readBody(updatedResponse))
	}
	var updated scheduledTestPlan
	decodeResponse(t, updatedResponse, &updated)
	if updated.Revision != 2 || !updated.Enabled || updated.Scope != "catalog" || updated.IntervalSeconds != 600 || updated.NextRunAt == nil {
		t.Fatalf("updated plan=%+v", updated)
	}

	conflict := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/scheduled-tests/"+created.ID, `{"expected_revision":1,"enabled":false}`, cookie, csrf, server.URL)
	if conflict.StatusCode != http.StatusConflict || !strings.Contains(readBody(conflict), `"code":"revision_conflict"`) {
		t.Fatal("stale revision was not rejected")
	}

	stamp := formatAccountPoolTime(time.Now().UTC())
	for index := 0; index < 3; index++ {
		operation := fmt.Sprintf("31000000-0000-4000-8000-%012d", index)
		if _, err := app.store.db.Exec(`INSERT INTO scheduled_test_runs(plan_id,plan_revision,upstream_id,operation_id,scope,state,result_code,started_at,finished_at,latency_ms,actor) VALUES(?,?,?,?,?,'completed','catalog_ok',?,?,1,'system')`, created.ID, updated.Revision, upstream.ID, operation, "catalog", stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	runsResponse := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/scheduled-tests/"+created.ID+"/runs?limit=2", "", cookie, "", "")
	var first scheduledTestRunsPage
	decodeResponse(t, runsResponse, &first)
	if len(first.Items) != 2 || first.NextCursor == nil || first.Items[0].OperationID != "31000000-0000-4000-8000-000000000002" {
		t.Fatalf("first runs page=%+v", first)
	}
	secondResponse := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/scheduled-tests/"+created.ID+"/runs?limit=2&cursor="+*first.NextCursor, "", cookie, "", "")
	var second scheduledTestRunsPage
	decodeResponse(t, secondResponse, &second)
	if len(second.Items) != 1 || second.NextCursor != nil || second.Items[0].OperationID != "31000000-0000-4000-8000-000000000000" {
		t.Fatalf("second runs page=%+v", second)
	}

	getResponse := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/scheduled-tests/"+created.ID, "", cookie, "", "")
	var got scheduledTestPlan
	decodeResponse(t, getResponse, &got)
	if got.LatestResult == nil || got.LatestResult.OperationID != "31000000-0000-4000-8000-000000000002" {
		t.Fatalf("latest result=%+v", got.LatestResult)
	}

	deleted := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/scheduled-tests/"+created.ID, `{"expected_revision":2}`, cookie, csrf, server.URL)
	if deleted.StatusCode != http.StatusOK || !strings.Contains(readBody(deleted), `"result":"archived"`) {
		t.Fatal("plan was not archived")
	}
	repeated := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/scheduled-tests/"+created.ID, `{"expected_revision":2}`, cookie, csrf, server.URL)
	if repeated.StatusCode != http.StatusOK || !strings.Contains(readBody(repeated), `"result":"already_archived"`) {
		t.Fatal("repeated archive was not recognizable")
	}
	list := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/scheduled-tests", "", cookie, "", "")
	var listed struct {
		Items []scheduledTestPlan `json:"items"`
	}
	decodeResponse(t, list, &listed)
	if len(listed.Items) != 0 {
		t.Fatalf("archived plan remained in list: %+v", listed.Items)
	}
}

func TestScheduledTestsDefaultOffNeverClaimsOrCallsUpstream(t *testing.T) {
	var calls atomic.Int32
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer upstreamServer.Close()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Default off", "openai-compatible", upstreamServer.URL, "off-private")
	response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/scheduled-tests", scheduledPlanBody(upstream.ID, true), cookie, csrf, server.URL)
	var plan scheduledTestPlan
	decodeResponse(t, response, &plan)
	if _, err := app.store.db.Exec(`UPDATE scheduled_test_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(time.Now().Add(-time.Hour)), plan.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	var runs int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM scheduled_test_runs`).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || runs != 0 {
		t.Fatalf("default-off worker calls=%d runs=%d", calls.Load(), runs)
	}
}

func TestScheduledTestsFakeClockClaimConcurrencyCancellationAndRevisionIsolation(t *testing.T) {
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	first := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "First", "openai-compatible", "http://127.0.0.1:1", "first-private")
	second := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Second", "openai-compatible", "http://127.0.0.1:2", "second-private")
	third := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Third", "openai-compatible", "http://127.0.0.1:3", "third-private")
	plans := []scheduledTestPlan{
		createScheduledPlanForTest(t, server.URL, cookie, csrf, first.ID, true),
		createScheduledPlanForTest(t, server.URL, cookie, csrf, second.ID, true),
		createScheduledPlanForTest(t, server.URL, cookie, csrf, third.ID, true),
	}
	now := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	app.scheduledTests.now = func() time.Time { return now }
	if _, err := app.store.db.Exec(`UPDATE scheduled_test_plans SET next_run_at=?`, formatAccountPoolTime(now.Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	claim1, err := app.scheduledTests.claimDue(context.Background())
	if err != nil || claim1 == nil {
		t.Fatalf("first claim=%+v err=%v", claim1, err)
	}
	claim2, err := app.scheduledTests.claimDue(context.Background())
	if err != nil || claim2 == nil || claim2.UpstreamID == claim1.UpstreamID {
		t.Fatalf("second claim=%+v err=%v", claim2, err)
	}
	claim3, err := app.scheduledTests.claimDue(context.Background())
	if err != nil || claim3 != nil {
		t.Fatalf("global concurrency allowed third claim=%+v err=%v", claim3, err)
	}

	started := make(chan struct{})
	finished := make(chan struct{})
	app.scheduledTests.execute = func(ctx context.Context, upstreamID, operationID string, revision int64, scope string) (upstreamTestOperationView, int, string, error) {
		close(started)
		<-ctx.Done()
		code := "cancelled"
		close(finished)
		return upstreamTestOperationView{ResultCode: &code}, http.StatusOK, "", nil
	}
	app.scheduledTests.start(*claim1)
	<-started
	update := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/scheduled-tests/"+claim1.PlanID, fmt.Sprintf(`{"expected_revision":%d,"enabled":false}`, claim1.PlanRevision), cookie, csrf, server.URL)
	if update.StatusCode != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", update.StatusCode, readBody(update))
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("plan update did not cancel owned run")
	}
	deadline := time.Now().Add(time.Second)
	for {
		var state, result string
		if err := app.store.db.QueryRow(`SELECT state,result_code FROM scheduled_test_runs WHERE operation_id=?`, claim1.OperationID).Scan(&state, &result); err == nil && state == "completed" && result == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled run was not durably finalized")
		}
		time.Sleep(10 * time.Millisecond)
	}
	loaded, err := loadScheduledTestPlan(context.Background(), app.store.db, claim1.PlanID, false)
	if err != nil {
		t.Fatal(err)
	}
	items := []scheduledTestPlan{loaded}
	if err := decorateScheduledTestLatest(context.Background(), app.store.db, items); err != nil {
		t.Fatal(err)
	}
	if items[0].Revision != claim1.PlanRevision+1 || items[0].LatestResult != nil {
		t.Fatalf("old result overwrote new plan view: %+v", items[0])
	}
	_ = plans
}

func TestScheduledTestsRestartRecoveryHistoryRetentionAndArchiveHook(t *testing.T) {
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Recovery", "openai-compatible", "http://127.0.0.1:1", "recovery-private")
	plan := createScheduledPlanForTest(t, server.URL, cookie, csrf, upstream.ID, true)
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	operation := "32000000-0000-4000-8000-000000000001"
	if _, err := app.store.db.Exec(`INSERT INTO scheduled_test_runs(plan_id,plan_revision,upstream_id,operation_id,scope,state,result_code,started_at,finished_at,latency_ms,actor) VALUES(?,?,?,?,?,'running',NULL,?,NULL,NULL,'system')`, plan.ID, plan.Revision, upstream.ID, operation, plan.Scope, formatAccountPoolTime(now.Add(-time.Minute))); err != nil {
		t.Fatal(err)
	}
	if err := migrateScheduledTests(context.Background(), app.store.db, now); err != nil {
		t.Fatal(err)
	}
	var state, result, next string
	if err := app.store.db.QueryRow(`SELECT state,result_code FROM scheduled_test_runs WHERE operation_id=?`, operation).Scan(&state, &result); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT next_run_at FROM scheduled_test_plans WHERE id=?`, plan.ID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if state != "completed" || result != "interrupted" || next != formatAccountPoolTime(now) {
		t.Fatalf("restart recovery state=%s result=%s next=%s", state, result, next)
	}

	for index := 0; index < 205; index++ {
		claim := scheduledTestClaim{PlanID: plan.ID, PlanRevision: plan.Revision, UpstreamID: upstream.ID, UpstreamRevision: upstream.Revision, Scope: plan.Scope, OperationID: fmt.Sprintf("33000000-0000-4000-8000-%012d", index), StartedAt: now}
		if _, err := app.store.db.Exec(`INSERT INTO scheduled_test_runs(plan_id,plan_revision,upstream_id,operation_id,scope,state,result_code,started_at,finished_at,latency_ms,actor) VALUES(?,?,?,?,?,'running',NULL,?,NULL,NULL,'system')`, claim.PlanID, claim.PlanRevision, claim.UpstreamID, claim.OperationID, claim.Scope, formatAccountPoolTime(now)); err != nil {
			t.Fatal(err)
		}
		if err := app.scheduledTests.finalize(context.Background(), claim, "local_credential_ok", now, 0); err != nil {
			t.Fatal(err)
		}
	}
	var count int64
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM scheduled_test_runs WHERE plan_id=? AND state='completed'`, plan.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != scheduledTestMaxHistory {
		t.Fatalf("retained completed runs=%d", count)
	}

	var adminID string
	if err := app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	tx, err := app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := archiveScheduledTestsForUpstreamTx(context.Background(), tx, upstream.ID, adminID, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != plan.ID {
		t.Fatalf("archive hook ids=%v", ids)
	}
	var enabled int
	var revision int64
	var nextValue sql.NullString
	if err := app.store.db.QueryRow(`SELECT enabled,revision,next_run_at FROM scheduled_test_plans WHERE id=?`, plan.ID).Scan(&enabled, &revision, &nextValue); err != nil {
		t.Fatal(err)
	}
	if enabled != 0 || revision != plan.Revision+1 || nextValue.Valid {
		t.Fatalf("archive hook enabled=%d revision=%d next=%v", enabled, revision, nextValue)
	}
}

func TestScheduledTestsMigrationRejectsMalformedSchemaAndIndexThenRetries(t *testing.T) {
	app, _, _, _ := newModelAdmissionApp(t, false)
	for _, statement := range []string{
		`DROP TABLE scheduled_test_runs`, `DROP TABLE scheduled_test_plans`,
		`CREATE TABLE scheduled_test_plans (
			id TEXT NOT NULL PRIMARY KEY,name INTEGER NOT NULL,upstream_id TEXT NOT NULL,scope TEXT NOT NULL,
			interval_seconds INTEGER NOT NULL,enabled INTEGER NOT NULL,revision INTEGER NOT NULL,next_run_at TEXT,
			created_by_admin_id TEXT NOT NULL,updated_by_admin_id TEXT NOT NULL,created_at TEXT NOT NULL,updated_at TEXT NOT NULL,archived_at TEXT)`,
	} {
		if _, err := app.store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateScheduledTests(context.Background(), app.store.db, time.Now()); err == nil {
		t.Fatal("malformed scheduled plan schema was accepted")
	}
	var runObjects int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name IN ('scheduled_test_runs','scheduled_test_runs_plan_idx','scheduled_test_runs_account_active_idx')`).Scan(&runObjects); err != nil {
		t.Fatal(err)
	}
	if runObjects != 0 {
		t.Fatalf("failed migration did not roll back created run objects: %d", runObjects)
	}
	if _, err := app.store.db.Exec(`DROP TABLE scheduled_test_plans`); err != nil {
		t.Fatal(err)
	}
	if err := migrateScheduledTests(context.Background(), app.store.db, time.Now()); err != nil {
		t.Fatalf("retry after repairing table: %v", err)
	}
	if _, err := app.store.db.Exec(`DROP INDEX scheduled_test_plans_due_idx`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`CREATE INDEX scheduled_test_plans_due_idx ON scheduled_test_plans(id)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateScheduledTests(context.Background(), app.store.db, time.Now()); err == nil {
		t.Fatal("malformed due index was accepted")
	}
	if _, err := app.store.db.Exec(`DROP INDEX scheduled_test_plans_due_idx`); err != nil {
		t.Fatal(err)
	}
	if err := migrateScheduledTests(context.Background(), app.store.db, time.Now()); err != nil {
		t.Fatalf("retry after repairing index: %v", err)
	}
}

func TestScheduledTestsClaimCloseRaceAndEligibilityStorageFailureAreTerminal(t *testing.T) {
	t.Run("close after claim", func(t *testing.T) {
		app, server, cookie, csrf := newModelAdmissionApp(t, false)
		upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Close race", "openai-compatible", "http://127.0.0.1:1", "close-private")
		plan := createScheduledPlanForTest(t, server.URL, cookie, csrf, upstream.ID, true)
		now := time.Now().UTC()
		app.scheduledTests.now = func() time.Time { return now }
		if _, err := app.store.db.Exec(`UPDATE scheduled_test_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(now.Add(-time.Second)), plan.ID); err != nil {
			t.Fatal(err)
		}
		claim, err := app.scheduledTests.claimDue(context.Background())
		if err != nil || claim == nil {
			t.Fatalf("claim=%+v err=%v", claim, err)
		}
		var calls atomic.Int32
		app.scheduledTests.execute = func(context.Context, string, string, int64, string) (upstreamTestOperationView, int, string, error) {
			calls.Add(1)
			return upstreamTestOperationView{}, 0, "", nil
		}
		app.scheduledTests.mu.Lock()
		app.scheduledTests.closed = true
		app.scheduledTests.mu.Unlock()
		app.scheduledTests.start(*claim)
		var state, result string
		if err := app.store.db.QueryRow(`SELECT state,result_code FROM scheduled_test_runs WHERE operation_id=?`, claim.OperationID).Scan(&state, &result); err != nil {
			t.Fatal(err)
		}
		if state != "completed" || result != "interrupted" || calls.Load() != 0 {
			t.Fatalf("close race state=%s result=%s calls=%d", state, result, calls.Load())
		}
	})

	t.Run("eligibility storage failure", func(t *testing.T) {
		app, server, cookie, csrf := newModelAdmissionApp(t, false)
		upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Storage", "openai-compatible", "http://127.0.0.1:1", "storage-private")
		plan := createScheduledPlanForTest(t, server.URL, cookie, csrf, upstream.ID, true)
		now := time.Now().UTC()
		app.scheduledTests.now = func() time.Time { return now }
		if _, err := app.store.db.Exec(`UPDATE scheduled_test_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(now.Add(-time.Second)), plan.ID); err != nil {
			t.Fatal(err)
		}
		claim, err := app.scheduledTests.claimDue(context.Background())
		if err != nil || claim == nil {
			t.Fatalf("claim=%+v err=%v", claim, err)
		}
		app.scheduledTests.checkUpstream = func(context.Context, string, int64) (bool, error) {
			return false, errors.New("synthetic storage failure")
		}
		var calls atomic.Int32
		app.scheduledTests.execute = func(context.Context, string, string, int64, string) (upstreamTestOperationView, int, string, error) {
			calls.Add(1)
			return upstreamTestOperationView{}, 0, "", nil
		}
		app.scheduledTests.executeClaim(context.Background(), *claim)
		var result string
		if err := app.store.db.QueryRow(`SELECT result_code FROM scheduled_test_runs WHERE operation_id=?`, claim.OperationID).Scan(&result); err != nil {
			t.Fatal(err)
		}
		if result != "storage_unavailable" || calls.Load() != 0 {
			t.Fatalf("storage eligibility result=%s calls=%d", result, calls.Load())
		}
	})
}

func TestArchivedUpstreamAtomicallyDisablesAndCancelsScheduledTests(t *testing.T) {
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Archive schedule", "openai-compatible", "http://127.0.0.1:1", "archive-private")
	plan := createScheduledPlanForTest(t, server.URL, cookie, csrf, upstream.ID, true)
	now := time.Now().UTC()
	app.scheduledTests.now = func() time.Time { return now }
	if _, err := app.store.db.Exec(`UPDATE scheduled_test_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(now.Add(-time.Second)), plan.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := app.scheduledTests.claimDue(context.Background())
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	started := make(chan struct{})
	app.scheduledTests.execute = func(ctx context.Context, _ string, _ string, _ int64, _ string) (upstreamTestOperationView, int, string, error) {
		close(started)
		<-ctx.Done()
		code := "cancelled"
		return upstreamTestOperationView{ResultCode: &code}, http.StatusOK, "", nil
	}
	app.scheduledTests.start(*claim)
	<-started

	response := requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/upstreams/"+upstream.ID, fmt.Sprintf(`{"expected_revision":%d}`, upstream.Revision), cookie, csrf, server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("archive upstream status=%d body=%s", response.StatusCode, readBody(response))
	}
	response.Body.Close()

	var upstreamArchived, enabled int
	var revision int64
	var next sql.NullString
	if err := app.store.db.QueryRow(`SELECT archived FROM upstreams WHERE id=?`, upstream.ID).Scan(&upstreamArchived); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT enabled,revision,next_run_at FROM scheduled_test_plans WHERE id=?`, plan.ID).Scan(&enabled, &revision, &next); err != nil {
		t.Fatal(err)
	}
	if upstreamArchived != 1 || enabled != 0 || revision != plan.Revision+1 || next.Valid {
		t.Fatalf("archive transaction upstream=%d enabled=%d revision=%d next=%v", upstreamArchived, enabled, revision, next)
	}

	deadline := time.Now().Add(time.Second)
	for {
		var state, result string
		err := app.store.db.QueryRow(`SELECT state,result_code FROM scheduled_test_runs WHERE operation_id=?`, claim.OperationID).Scan(&state, &result)
		if err == nil && state == "completed" && result == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("archived upstream run did not finalize as cancelled: state=%q result=%q err=%v", state, result, err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	create := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/scheduled-tests", scheduledPlanBody(upstream.ID, false), cookie, csrf, server.URL)
	if create.StatusCode != http.StatusBadRequest || !strings.Contains(readBody(create), `"code":"invalid_upstream"`) {
		t.Fatal("archived upstream accepted for a new scheduled test")
	}
}

func scheduledPlanBody(upstreamID string, enabled bool) string {
	return fmt.Sprintf(`{"name":"Credential schedule","upstream_id":%q,"scope":"local_credential","interval_seconds":300,"enabled":%t}`, upstreamID, enabled)
}

func createScheduledPlanForTest(t *testing.T, baseURL string, cookie *http.Cookie, csrf, upstreamID string, enabled bool) scheduledTestPlan {
	t.Helper()
	response := requestJSON(t, http.MethodPost, baseURL+"/admin/api/v1/scheduled-tests", scheduledPlanBody(upstreamID, enabled), cookie, csrf, baseURL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create scheduled plan status=%d body=%s", response.StatusCode, readBody(response))
	}
	var plan scheduledTestPlan
	decodeResponse(t, response, &plan)
	return plan
}
