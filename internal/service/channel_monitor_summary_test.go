package service

// Independently authored acceptance for docs/channel-monitor-summary-contract.md.
import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestChannelMonitorSummaryEmptyAndAdminBoundary(t *testing.T) {
	f := newChannelMonitorFixture(t)
	plan := createChannelMonitorPlanForTest(t, f, false)
	path := f.base + "/admin/api/v1/channel-monitors/" + plan.ID + "/summary"
	response := requestJSON(t, http.MethodGet, path, "", f.cookie, "", "")
	if response.StatusCode != http.StatusOK || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("empty summary status=%d cache=%q body=%s", response.StatusCode, response.Header.Get("Cache-Control"), readBody(response))
	}
	var summary channelMonitorSummary
	decodeResponse(t, response, &summary)
	if summary.PlanID != plan.ID || summary.ThroughSequence != 0 || summary.RetainedCompleted != 0 || summary.Running != 0 || summary.RetainedWindowFull || summary.EarliestFinishedAt != nil || summary.LatestFinishedAt != nil {
		t.Fatalf("empty summary=%+v", summary)
	}
	if _, err := parseTime(summary.AsOf); err != nil || !strings.HasSuffix(summary.AsOf, "Z") {
		t.Fatalf("as_of=%q err=%v", summary.AsOf, err)
	}
	for _, scope := range []string{"local_credential", "catalog"} {
		if len(summary.Counts[scope]) != len(channelMonitorSummaryCodes) {
			t.Fatalf("scope %q counts=%v", scope, summary.Counts[scope])
		}
		for _, code := range channelMonitorSummaryCodes {
			if summary.Counts[scope][code] != 0 {
				t.Fatalf("scope %q code %q nonzero", scope, code)
			}
		}
	}
	for _, suffix := range []string{"?limit=1", "?x=1"} {
		bad := requestJSON(t, http.MethodGet, path+suffix, "", f.cookie, "", "")
		if bad.StatusCode != http.StatusBadRequest || bad.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("query %q status=%d body=%s", suffix, bad.StatusCode, readBody(bad))
		}
		bad.Body.Close()
	}
	missing := requestJSON(t, http.MethodGet, f.base+"/admin/api/v1/channel-monitors/mon_missing/summary", "", f.cookie, "", "")
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing status=%d body=%s", missing.StatusCode, readBody(missing))
	}
	missing.Body.Close()
	wrongOrigin := requestJSON(t, http.MethodGet, path, "", f.cookie, "", "http://elsewhere.invalid")
	if wrongOrigin.StatusCode != http.StatusForbidden {
		t.Fatalf("origin status=%d body=%s", wrongOrigin.StatusCode, readBody(wrongOrigin))
	}
	wrongOrigin.Body.Close()
	unauthorized := requestJSON(t, http.MethodGet, path, "", nil, "", "")
	if unauthorized.StatusCode != http.StatusUnauthorized || unauthorized.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.StatusCode, readBody(unauthorized))
	}
	unauthorized.Body.Close()
	employeeResponse := requestJSON(t, http.MethodPost, f.base+"/admin/api/v1/employees", `{"name":"Summary employee"}`, f.cookie, f.csrf, f.base)
	if employeeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("employee status=%d body=%s", employeeResponse.StatusCode, readBody(employeeResponse))
	}
	var employee struct {
		ID string `json:"id"`
	}
	decodeResponse(t, employeeResponse, &employee)
	key := createTestKey(t, f.base, employee.ID, "00000000-0000-4000-8000-000000000201", f.cookie, f.csrf)
	bearer := employeeRequest(t, http.MethodGet, path, "", key.Key, context.Background())
	if bearer.StatusCode != http.StatusUnauthorized || bearer.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("employee bearer status=%d body=%s", bearer.StatusCode, readBody(bearer))
	}
	bearer.Body.Close()
}

func TestChannelMonitorSummaryAllCodesAcrossOldBindingAndArchive(t *testing.T) {
	f := newChannelMonitorFixture(t)
	created := createChannelMonitorPlanForTest(t, f, false)
	plan, err := loadChannelMonitorPlan(context.Background(), f.app.store.db, created.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for index, code := range channelMonitorSummaryCodes {
		for scopeIndex, scope := range []string{"local_credential", "catalog"} {
			plan.Scope = scope
			stamp := "2026-09-30T12:00:00Z"
			if index == 0 && scopeIndex == 0 {
				stamp = "2026-09-29T01:00:00Z"
			}
			if err := insertChannelMonitorRun(context.Background(), tx, plan, fmt.Sprintf("00000000-0000-4000-8000-%012d", 400+index*2+scopeIndex), stamp, true, code); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rebound := requestJSON(t, http.MethodPatch, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":1,"rebind":true}`, f.cookie, f.csrf, f.base)
	if rebound.StatusCode != http.StatusOK {
		t.Fatalf("rebind status=%d body=%s", rebound.StatusCode, readBody(rebound))
	}
	rebound.Body.Close()
	plan.Revision = 2
	plan.Scope = "catalog"
	tx, err = f.app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertChannelMonitorRun(context.Background(), tx, plan, "00000000-0000-4000-8000-000000000499", "2026-10-01T02:00:00Z", true, "catalog_ok"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	archived := requestJSON(t, http.MethodDelete, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":2}`, f.cookie, f.csrf, f.base)
	if archived.StatusCode != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", archived.StatusCode, readBody(archived))
	}
	archived.Body.Close()
	response := requestJSON(t, http.MethodGet, f.base+"/admin/api/v1/channel-monitors/"+plan.ID+"/summary", "", f.cookie, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", response.StatusCode, readBody(response))
	}
	var summary channelMonitorSummary
	decodeResponse(t, response, &summary)
	if summary.RetainedCompleted != len(channelMonitorSummaryCodes)*2+1 || summary.Running != 0 || summary.EarliestFinishedAt == nil || *summary.EarliestFinishedAt != "2026-09-29T01:00:00Z" || summary.LatestFinishedAt == nil || *summary.LatestFinishedAt != "2026-10-01T02:00:00Z" {
		t.Fatalf("cross-binding summary=%+v earliest=%q latest=%q", summary, *summary.EarliestFinishedAt, *summary.LatestFinishedAt)
	}
	for _, scope := range []string{"local_credential", "catalog"} {
		for _, code := range channelMonitorSummaryCodes {
			want := 1
			if scope == "catalog" && code == "catalog_ok" {
				want = 2
			}
			if got := summary.Counts[scope][code]; got != want {
				t.Fatalf("scope=%s code=%s got=%d want=%d", scope, code, got, want)
			}
		}
	}
}

func TestChannelMonitorSummaryRejectsUnexpectedSchema(t *testing.T) {
	f := newChannelMonitorFixture(t)
	plan := createChannelMonitorPlanForTest(t, f, false)
	if _, err := f.app.store.db.Exec(`CREATE INDEX channel_monitor_unexpected_idx ON channel_monitor_runs(scope)`); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, http.MethodGet, f.base+"/admin/api/v1/channel-monitors/"+plan.ID+"/summary", "", f.cookie, "", "")
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("schema status=%d body=%s", response.StatusCode, readBody(response))
	}
	body := readBody(response)
	if !strings.Contains(body, "storage_unavailable") || strings.Contains(body, "channel_monitor_unexpected_idx") {
		t.Fatalf("unsafe schema error=%s", body)
	}
}

func TestChannelMonitorSummaryConcurrentWritesStayAtOneWatermark(t *testing.T) {
	f := newChannelMonitorFixture(t)
	created := createChannelMonitorPlanForTest(t, f, false)
	plan, err := loadChannelMonitorPlan(context.Background(), f.app.store.db, created.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	writerDone := make(chan error, 1)
	go func() {
		for index := 0; index < 24; index++ {
			tx, err := f.app.store.db.BeginTx(context.Background(), nil)
			if err != nil {
				writerDone <- err
				return
			}
			operation := fmt.Sprintf("00000000-0000-4000-8000-%012d", 600+index)
			if err := insertChannelMonitorRun(context.Background(), tx, plan, operation, "2026-09-30T12:00:00Z", true, "catalog_ok"); err != nil {
				tx.Rollback()
				writerDone <- err
				return
			}
			if err := tx.Commit(); err != nil {
				writerDone <- err
				return
			}
		}
		writerDone <- nil
	}()
	for index := 0; index < 24; index++ {
		summary, err := loadChannelMonitorSummary(context.Background(), f.app.store.db, plan.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if int64(summary.RetainedCompleted) != summary.ThroughSequence || summary.Counts["catalog"]["catalog_ok"] != summary.RetainedCompleted || summary.Running != 0 {
			t.Fatalf("mixed snapshot at %d: %+v", index, summary)
		}
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	final, err := loadChannelMonitorSummary(context.Background(), f.app.store.db, plan.ID, time.Now())
	if err != nil || final.RetainedCompleted != 24 || final.ThroughSequence != 24 {
		t.Fatalf("final summary=%+v err=%v", final, err)
	}
}

func TestChannelMonitorSummaryRetainedWindowAndRunning(t *testing.T) {
	f := newChannelMonitorFixture(t)
	created := createChannelMonitorPlanForTest(t, f, false)
	plan, err := loadChannelMonitorPlan(context.Background(), f.app.store.db, created.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	stamp := "2026-09-30T12:00:00Z"
	for index := 0; index < 205; index++ {
		code := "catalog_ok"
		plan.Scope = "catalog"
		if index%2 == 0 {
			code = "local_credential_ok"
			plan.Scope = "local_credential"
		}
		if err := insertChannelMonitorRun(context.Background(), tx, plan, fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1), stamp, true, code); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := trimChannelMonitorHistory(context.Background(), tx, plan.ID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	plan.Scope = "catalog"
	if err := insertChannelMonitorRun(context.Background(), tx, plan, "00000000-0000-4000-8000-000000000206", stamp, false, ""); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, http.MethodGet, f.base+"/admin/api/v1/channel-monitors/"+plan.ID+"/summary", "", f.cookie, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", response.StatusCode, readBody(response))
	}
	var summary channelMonitorSummary
	decodeResponse(t, response, &summary)
	if summary.ThroughSequence != 206 || summary.RetainedCompleted != 200 || !summary.RetainedWindowFull || summary.Running != 1 || summary.Counts["local_credential"]["local_credential_ok"] != 100 || summary.Counts["catalog"]["catalog_ok"] != 100 || summary.EarliestFinishedAt == nil || *summary.EarliestFinishedAt != stamp || summary.LatestFinishedAt == nil || *summary.LatestFinishedAt != stamp {
		t.Fatalf("retained summary=%+v", summary)
	}
}

func TestChannelMonitorSummaryArchivedHistoricalAndCorruptRow(t *testing.T) {
	f := newChannelMonitorFixture(t)
	created := createChannelMonitorPlanForTest(t, f, false)
	plan, err := loadChannelMonitorPlan(context.Background(), f.app.store.db, created.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	stamp := "2026-09-30T12:00:00Z"
	tx, err := f.app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertChannelMonitorRun(context.Background(), tx, plan, "00000000-0000-4000-8000-000000000301", stamp, true, "catalog_ok"); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	path := f.base + "/admin/api/v1/channel-monitors/" + plan.ID + "/summary"
	archived := requestJSON(t, http.MethodDelete, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":1}`, f.cookie, f.csrf, f.base)
	if archived.StatusCode != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", archived.StatusCode, readBody(archived))
	}
	archived.Body.Close()
	response := requestJSON(t, http.MethodGet, path, "", f.cookie, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("archived summary status=%d body=%s", response.StatusCode, readBody(response))
	}
	var summary channelMonitorSummary
	decodeResponse(t, response, &summary)
	if summary.RetainedCompleted != 1 || summary.Counts["catalog"]["catalog_ok"] != 1 {
		t.Fatalf("archived summary=%+v", summary)
	}
	if _, err := f.app.store.db.Exec(`UPDATE channel_monitor_runs SET finished_at='not-a-time' WHERE plan_id=?`, plan.ID); err != nil {
		t.Fatal(err)
	}
	broken := requestJSON(t, http.MethodGet, path, "", f.cookie, "", "")
	if broken.StatusCode != http.StatusServiceUnavailable || broken.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("corrupt status=%d body=%s", broken.StatusCode, readBody(broken))
	}
	body := readBody(broken)
	if !strings.Contains(body, "storage_unavailable") || strings.Contains(body, "not-a-time") || strings.Contains(body, "synthetic-monitor-secret") {
		t.Fatalf("unsafe error body=%s", body)
	}
}
