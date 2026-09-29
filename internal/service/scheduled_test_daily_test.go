package service

// Independent Go acceptance for docs/scheduled-tests-daily-timezone-contract.md.
import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func scheduledDailyBody(upstreamID string, enabled bool) string {
	return fmt.Sprintf(`{"name":"Daily credential","upstream_id":%q,"scope":"local_credential","schedule_mode":"daily_local","time_zone":"America/New_York","local_time":"01:30","enabled":%t}`, upstreamID, enabled)
}

func TestScheduledDailyHTTPCompatibilityAndCAS(t *testing.T) {
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Daily account", "openai-compatible", "http://127.0.0.1:1", "synthetic-daily-private")
	now := scheduledInstant(t, "2026-10-31T12:00:00Z")
	app.scheduledTests.now = func() time.Time { return now }

	old := createScheduledPlanForTest(t, server.URL, cookie, csrf, upstream.ID, true)
	if old.ScheduleMode != "interval" || old.TimeZone != nil || old.LocalTime != nil || old.IntervalSeconds != 300 || old.NextRunAt == nil || *old.NextRunAt != formatAccountPoolTime(now.Add(300*time.Second)) {
		t.Fatalf("old interval request changed meaning: %+v", old)
	}
	oldPatch := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/scheduled-tests/"+old.ID, `{"expected_revision":1,"interval_seconds":600}`, cookie, csrf, server.URL)
	var updatedOld scheduledTestPlan
	decodeResponse(t, oldPatch, &updatedOld)
	if updatedOld.ScheduleMode != "interval" || updatedOld.IntervalSeconds != 600 || updatedOld.TimeZone != nil {
		t.Fatalf("old interval patch changed mode: %+v", updatedOld)
	}

	wrongOrigin := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/scheduled-tests", scheduledDailyBody(upstream.ID, true), cookie, csrf, "http://elsewhere.invalid")
	if wrongOrigin.StatusCode != http.StatusForbidden {
		t.Fatalf("wrong origin status=%d body=%s", wrongOrigin.StatusCode, readBody(wrongOrigin))
	}
	wrongOrigin.Body.Close()
	missingCSRF := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/scheduled-tests", scheduledDailyBody(upstream.ID, true), cookie, "", "")
	if missingCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", missingCSRF.StatusCode, readBody(missingCSRF))
	}
	missingCSRF.Body.Close()
	for _, body := range []string{
		fmt.Sprintf(`{"name":"Bad","upstream_id":%q,"scope":"catalog","schedule_mode":"daily_local","time_zone":"Local","local_time":"01:30","enabled":true}`, upstream.ID),
		fmt.Sprintf(`{"name":"Bad","upstream_id":%q,"scope":"catalog","schedule_mode":"daily_local","time_zone":"America/New_York","local_time":"2:30","enabled":true}`, upstream.ID),
		fmt.Sprintf(`{"name":"Bad","upstream_id":%q,"scope":"catalog","schedule_mode":"daily_local","time_zone":"America/New_York","local_time":"01:30","interval_seconds":86400,"enabled":true}`, upstream.ID),
	} {
		response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/scheduled-tests", body, cookie, csrf, server.URL)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(readBody(response), `"code":"invalid_request"`) {
			t.Fatal("invalid daily input accepted")
		}
	}

	createdResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/scheduled-tests", scheduledDailyBody(upstream.ID, true), cookie, csrf, server.URL)
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("daily create status=%d body=%s", createdResponse.StatusCode, readBody(createdResponse))
	}
	var daily scheduledTestPlan
	decodeResponse(t, createdResponse, &daily)
	if daily.ScheduleMode != "daily_local" || daily.IntervalSeconds != 86400 || daily.TimeZone == nil || *daily.TimeZone != "America/New_York" || daily.LocalTime == nil || *daily.LocalTime != "01:30" || daily.NextRunAt == nil || *daily.NextRunAt != formatAccountPoolTime(scheduledInstant(t, "2026-11-01T05:30:00Z")) {
		t.Fatalf("daily plan=%+v", daily)
	}
	badOldEdit := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/scheduled-tests/"+daily.ID, `{"expected_revision":1,"interval_seconds":600}`, cookie, csrf, server.URL)
	if badOldEdit.StatusCode != http.StatusBadRequest {
		t.Fatalf("old interval edit converted daily plan: %d %s", badOldEdit.StatusCode, readBody(badOldEdit))
	}
	badOldEdit.Body.Close()
	nameOnly := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/scheduled-tests/"+daily.ID, `{"expected_revision":1,"name":"Renamed daily"}`, cookie, csrf, server.URL)
	var renamed scheduledTestPlan
	decodeResponse(t, nameOnly, &renamed)
	if renamed.ScheduleMode != "daily_local" || renamed.Revision != 2 || renamed.NextRunAt == nil || *renamed.NextRunAt != *daily.NextRunAt {
		t.Fatalf("daily name patch=%+v", renamed)
	}
	stale := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/scheduled-tests/"+daily.ID, `{"expected_revision":1,"enabled":false}`, cookie, csrf, server.URL)
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale revision status=%d body=%s", stale.StatusCode, readBody(stale))
	}
	stale.Body.Close()
	toInterval := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/scheduled-tests/"+daily.ID, `{"expected_revision":2,"schedule_mode":"interval","interval_seconds":600}`, cookie, csrf, server.URL)
	var switched scheduledTestPlan
	decodeResponse(t, toInterval, &switched)
	if switched.ScheduleMode != "interval" || switched.TimeZone != nil || switched.LocalTime != nil || switched.IntervalSeconds != 600 || switched.Revision != 3 {
		t.Fatalf("explicit interval switch=%+v", switched)
	}
	backDaily := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/scheduled-tests/"+daily.ID, `{"expected_revision":3,"schedule_mode":"daily_local","time_zone":"Asia/Shanghai","local_time":"00:30"}`, cookie, csrf, server.URL)
	var switchedBack scheduledTestPlan
	decodeResponse(t, backDaily, &switchedBack)
	if switchedBack.ScheduleMode != "daily_local" || switchedBack.TimeZone == nil || *switchedBack.TimeZone != "Asia/Shanghai" || switchedBack.IntervalSeconds != 86400 || switchedBack.Revision != 4 {
		t.Fatalf("explicit daily switch=%+v", switchedBack)
	}
}

func TestScheduledDailyClaimCatchUpClockRollbackAndRestart(t *testing.T) {
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Daily claim", "openai-compatible", "http://127.0.0.1:1", "synthetic-daily-claim")
	now := scheduledInstant(t, "2026-11-05T12:00:00Z")
	app.scheduledTests.now = func() time.Time { return now }
	response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/scheduled-tests", scheduledDailyBody(upstream.ID, true), cookie, csrf, server.URL)
	var plan scheduledTestPlan
	decodeResponse(t, response, &plan)
	if _, err := app.store.db.Exec(`UPDATE scheduled_test_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(scheduledInstant(t, "2026-10-31T05:30:00Z")), plan.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := app.scheduledTests.claimDue(context.Background())
	if err != nil || claim == nil {
		t.Fatalf("catch-up claim=%+v err=%v", claim, err)
	}
	var next string
	if err := app.store.db.QueryRow(`SELECT next_run_at FROM scheduled_test_plans WHERE id=?`, plan.ID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != formatAccountPoolTime(scheduledInstant(t, "2026-11-06T06:30:00Z")) {
		t.Fatalf("catch-up replayed missed days: %s", next)
	}
	if second, err := app.scheduledTests.claimDue(context.Background()); err != nil || second != nil {
		t.Fatalf("second catch-up claim=%+v err=%v", second, err)
	}
	if err := migrateScheduledTests(context.Background(), app.store.db, now); err != nil {
		t.Fatal(err)
	}
	var oldState, oldResult string
	if err := app.store.db.QueryRow(`SELECT state,result_code FROM scheduled_test_runs WHERE operation_id=?`, claim.OperationID).Scan(&oldState, &oldResult); err != nil {
		t.Fatal(err)
	}
	if oldState != "completed" || oldResult != "interrupted" {
		t.Fatalf("restart state=%s result=%s", oldState, oldResult)
	}
	newClaim, err := app.scheduledTests.claimDue(context.Background())
	if err != nil || newClaim == nil || newClaim.OperationID == claim.OperationID {
		t.Fatalf("restart claim=%+v err=%v", newClaim, err)
	}
	if again, err := app.scheduledTests.claimDue(context.Background()); err != nil || again != nil {
		t.Fatalf("restart replayed more than once: %+v err=%v", again, err)
	}
	if err := app.scheduledTests.finalize(context.Background(), *newClaim, "local_credential_ok", now, 0); err != nil {
		t.Fatal(err)
	}
	app.scheduledTests.now = func() time.Time { return scheduledInstant(t, "2026-11-01T05:15:00Z") }
	if rollbackClaim, err := app.scheduledTests.claimDue(context.Background()); err != nil || rollbackClaim != nil {
		t.Fatalf("clock rollback repeated daily run: %+v err=%v", rollbackClaim, err)
	}
	var count int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM scheduled_test_runs WHERE plan_id=?`, plan.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("restart created %d operations, want 2", count)
	}
}

func TestScheduledDailyLegacyMigrationPreservesPlanAndHistory(t *testing.T) {
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Legacy migration", "openai-compatible", "http://127.0.0.1:1", "synthetic-legacy")
	var adminID string
	if err := app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`DROP TABLE scheduled_test_runs`, `DROP TABLE scheduled_test_plans`, scheduledTestPlanDDL, scheduledTestRunDDL,
		`CREATE INDEX scheduled_test_plans_due_idx ON scheduled_test_plans(enabled,archived_at,next_run_at,id)`,
		`CREATE INDEX scheduled_test_runs_plan_idx ON scheduled_test_runs(plan_id,sequence DESC)`,
		`CREATE UNIQUE INDEX scheduled_test_runs_account_active_idx ON scheduled_test_runs(upstream_id) WHERE state='running'`} {
		if _, err := app.store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	stamp := formatAccountPoolTime(scheduledInstant(t, "2026-09-30T10:00:00Z"))
	if _, err := app.store.db.Exec(`INSERT INTO scheduled_test_plans(id,name,upstream_id,scope,interval_seconds,enabled,revision,next_run_at,created_by_admin_id,updated_by_admin_id,created_at,updated_at,archived_at) VALUES('sch_legacy','Legacy',?,'catalog',600,1,7,?,?,?,?,?,NULL)`, upstream.ID, stamp, adminID, adminID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO scheduled_test_runs(plan_id,plan_revision,upstream_id,operation_id,scope,state,result_code,started_at,finished_at,latency_ms,actor) VALUES('sch_legacy',7,?,'legacy-operation','catalog','completed','catalog_ok',?,?,1,'system')`, upstream.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := migrateScheduledTests(context.Background(), app.store.db, scheduledInstant(t, "2026-09-30T09:00:00Z")); err != nil {
		t.Fatal(err)
	}
	plan, err := loadScheduledTestPlan(context.Background(), app.store.db, "sch_legacy", false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ScheduleMode != "interval" || plan.IntervalSeconds != 600 || plan.Revision != 7 || plan.NextRunAt == nil || *plan.NextRunAt != stamp || plan.TimeZone != nil || plan.LocalTime != nil {
		t.Fatalf("legacy row changed: %+v", plan)
	}
	var operation string
	if err := app.store.db.QueryRow(`SELECT operation_id FROM scheduled_test_runs WHERE plan_id='sch_legacy'`).Scan(&operation); err != nil || operation != "legacy-operation" {
		t.Fatalf("history changed: %q err=%v", operation, err)
	}
}

func TestScheduledDailyPartialMigrationFailsClosed(t *testing.T) {
	app, _, _, _ := newModelAdmissionApp(t, false)
	for _, statement := range []string{`DROP TABLE scheduled_test_runs`, `DROP TABLE scheduled_test_plans`, scheduledTestPlanDDL, scheduledTestRunDDL,
		`CREATE INDEX scheduled_test_plans_due_idx ON scheduled_test_plans(enabled,archived_at,next_run_at,id)`,
		`CREATE INDEX scheduled_test_runs_plan_idx ON scheduled_test_runs(plan_id,sequence DESC)`,
		`CREATE UNIQUE INDEX scheduled_test_runs_account_active_idx ON scheduled_test_runs(upstream_id) WHERE state='running'`,
		`ALTER TABLE scheduled_test_plans ADD COLUMN ` + scheduledDailyColumnDDL[0]} {
		if _, err := app.store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateScheduledTests(context.Background(), app.store.db, time.Now()); err == nil {
		t.Fatal("partial daily schema accepted")
	}
	var count int
	rows, err := app.store.db.Query(`PRAGMA table_info(scheduled_test_plans)`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var cid, notNull, primary int
		var name, kind string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			t.Fatal(err)
		}
		if name == "time_zone" || name == "local_time" {
			count++
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if count != 0 {
		t.Fatalf("failed migration left %d new columns", count)
	}
}
