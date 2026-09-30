package service

// Independently authored acceptance for docs/channel-monitor-contract.md.
import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

type channelMonitorFixture struct {
	app      *App
	base     string
	cookie   *http.Cookie
	csrf     string
	channel  accountChannelView
	upstream upstreamView
	modelID  string
}

func TestChannelMonitorHistoryRetentionAndStrictPagination(t *testing.T) {
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
	stamp := formatAccountPoolTime(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	for index := 0; index < 205; index++ {
		operationID := fmt.Sprintf("00000000-0000-4000-8000-%012d", index+1)
		if err := insertChannelMonitorRun(context.Background(), tx, plan, operationID, stamp, true, "catalog_ok"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := trimChannelMonitorHistory(context.Background(), tx, plan.ID); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM channel_monitor_runs WHERE plan_id=?`, plan.ID).Scan(&count); err != nil || count != 200 {
		t.Fatalf("retained=%d err=%v", count, err)
	}
	for _, query := range []string{"?limit=", "?cursor=", "?limit=101", "?limit=2&limit=3", "?unexpected=1"} {
		response := requestJSON(t, http.MethodGet, f.base+"/admin/api/v1/channel-monitors/"+plan.ID+"/runs"+query, "", f.cookie, "", "")
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("query %q status=%d body=%s", query, response.StatusCode, readBody(response))
		}
		response.Body.Close()
	}
	cursor := ""
	seen := map[string]bool{}
	for page := 0; page < 4; page++ {
		url := f.base + "/admin/api/v1/channel-monitors/" + plan.ID + "/runs?limit=50"
		if cursor != "" {
			url += "&cursor=" + cursor
		}
		response := requestJSON(t, http.MethodGet, url, "", f.cookie, "", "")
		if response.StatusCode != http.StatusOK {
			t.Fatalf("page %d status=%d body=%s", page, response.StatusCode, readBody(response))
		}
		var result struct {
			Items      []channelMonitorRun `json:"items"`
			NextCursor *string             `json:"next_cursor"`
		}
		decodeResponse(t, response, &result)
		if len(result.Items) != 50 {
			t.Fatalf("page %d count=%d", page, len(result.Items))
		}
		for _, item := range result.Items {
			if seen[item.OperationID] {
				t.Fatalf("duplicate operation %s", item.OperationID)
			}
			seen[item.OperationID] = true
		}
		if page < 3 && result.NextCursor == nil || page == 3 && result.NextCursor != nil {
			t.Fatalf("page %d cursor=%v", page, result.NextCursor)
		}
		if result.NextCursor != nil {
			cursor = *result.NextCursor
		}
	}
	if len(seen) != 200 || seen["00000000-0000-4000-8000-000000000001"] || !seen["00000000-0000-4000-8000-000000000205"] {
		t.Fatalf("unexpected retained history: count=%d", len(seen))
	}
}

func TestChannelMonitorAdminBoundaryAndInputValidation(t *testing.T) {
	f := newChannelMonitorFixture(t)
	employeeResponse := requestJSON(t, http.MethodPost, f.base+"/admin/api/v1/employees", `{"name":"Synthetic monitor employee"}`, f.cookie, f.csrf, f.base)
	if employeeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("employee status=%d body=%s", employeeResponse.StatusCode, readBody(employeeResponse))
	}
	var employee struct {
		ID string `json:"id"`
	}
	decodeResponse(t, employeeResponse, &employee)
	key := createTestKey(t, f.base, employee.ID, "00000000-0000-4000-8000-000000000201", f.cookie, f.csrf)
	for _, path := range []string{"/admin/api/v1/channel-monitors", "/admin/api/v1/channel-monitors/missing/runs"} {
		response := employeeRequest(t, http.MethodGet, f.base+path, "", key.Key, context.Background())
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("employee bearer %s status=%d body=%s", path, response.StatusCode, readBody(response))
		}
		response.Body.Close()
	}
	for _, item := range []struct {
		body, csrf, origin string
		status             int
	}{
		{f.createBody(false), "", f.base, http.StatusForbidden},
		{f.createBody(false), f.csrf, "https://elsewhere.invalid", http.StatusForbidden},
		{`{"name":"One","name":"Two","channel_id":"x","model_id":"x","upstream_id":"x","scope":"catalog","interval_seconds":300,"enabled":false}`, f.csrf, f.base, http.StatusBadRequest},
		{`{"name":"Bad","channel_id":"x","model_id":"x","upstream_id":"x","scope":"catalog","interval_seconds":299,"enabled":false}`, f.csrf, f.base, http.StatusBadRequest},
	} {
		response := requestJSON(t, http.MethodPost, f.base+"/admin/api/v1/channel-monitors", item.body, f.cookie, item.csrf, item.origin)
		if response.StatusCode != item.status {
			t.Fatalf("invalid create status=%d expected=%d body=%s", response.StatusCode, item.status, readBody(response))
		}
		response.Body.Close()
	}
}

func newChannelMonitorFixture(t *testing.T) channelMonitorFixture {
	t.Helper()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Synthetic monitor", "openai-compatible", "http://127.0.0.1:1", "synthetic-monitor-secret")
	modelID := "monitor-public-model"
	createModelAdmissionModel(t, server.URL, cookie, csrf, modelID, upstream.ID, "monitor-provider-model")
	response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/channels", `{"name":"Monitor channel"}`, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("channel status=%d body=%s", response.StatusCode, readBody(response))
	}
	var channel accountChannelView
	decodeResponse(t, response, &channel)
	pool := fmt.Sprintf(`{"expected_revision":0,"items":[{"upstream_id":%q,"upstream_model":"monitor-provider-model","priority":0,"weight":1,"max_concurrency":2,"channel_id":%q}]}`, upstream.ID, channel.ID)
	configured := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/models/"+modelID+"/accounts", pool, cookie, csrf, server.URL)
	if configured.StatusCode != http.StatusOK {
		t.Fatalf("pool status=%d body=%s", configured.StatusCode, readBody(configured))
	}
	configured.Body.Close()
	return channelMonitorFixture{app: app, base: server.URL, cookie: cookie, csrf: csrf, channel: channel, upstream: upstream, modelID: modelID}
}

func (f channelMonitorFixture) createBody(enabled bool) string {
	return fmt.Sprintf(`{"name":"Directory watch","channel_id":%q,"model_id":%q,"upstream_id":%q,"scope":"catalog","interval_seconds":300,"enabled":%t}`, f.channel.ID, f.modelID, f.upstream.ID, enabled)
}

func TestChannelMonitorHTTPBindingAndCAS(t *testing.T) {
	f := newChannelMonitorFixture(t)
	f.app.channelMonitors.now = func() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }
	wrongOrigin := requestJSON(t, http.MethodPost, f.base+"/admin/api/v1/channel-monitors", f.createBody(true), f.cookie, f.csrf, "http://elsewhere.invalid")
	if wrongOrigin.StatusCode != http.StatusForbidden {
		t.Fatalf("origin status=%d body=%s", wrongOrigin.StatusCode, readBody(wrongOrigin))
	}
	wrongOrigin.Body.Close()
	created := requestJSON(t, http.MethodPost, f.base+"/admin/api/v1/channel-monitors", f.createBody(true), f.cookie, f.csrf, f.base)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.StatusCode, readBody(created))
	}
	var plan channelMonitorPlan
	decodeResponse(t, created, &plan)
	if plan.BindingState != "valid" || plan.Revision != 1 || plan.NextRunAt == nil || *plan.NextRunAt != formatAccountPoolTime(time.Date(2026, 9, 30, 12, 5, 0, 0, time.UTC)) {
		t.Fatalf("created=%+v", plan)
	}
	stale := requestJSON(t, http.MethodPatch, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":2,"name":"Wrong"}`, f.cookie, f.csrf, f.base)
	if stale.StatusCode != http.StatusConflict {
		t.Fatalf("stale status=%d body=%s", stale.StatusCode, readBody(stale))
	}
	stale.Body.Close()
	nameOnly := requestJSON(t, http.MethodPatch, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":1,"name":"Renamed"}`, f.cookie, f.csrf, f.base)
	if nameOnly.StatusCode != http.StatusOK {
		t.Fatalf("name status=%d body=%s", nameOnly.StatusCode, readBody(nameOnly))
	}
	var renamed channelMonitorPlan
	decodeResponse(t, nameOnly, &renamed)
	if renamed.Name != "Renamed" || renamed.Revision != 2 || renamed.BindingState != "valid" {
		t.Fatalf("renamed=%+v", renamed)
	}
	pool := fmt.Sprintf(`{"expected_revision":1,"items":[{"upstream_id":%q,"upstream_model":"monitor-provider-model","priority":0,"weight":1,"max_concurrency":2,"channel_id":%q}]}`, f.upstream.ID, f.channel.ID)
	updatedPool := requestJSON(t, http.MethodPut, f.base+"/admin/api/v1/models/"+f.modelID+"/accounts", pool, f.cookie, f.csrf, f.base)
	if updatedPool.StatusCode != http.StatusOK {
		t.Fatalf("pool update status=%d body=%s", updatedPool.StatusCode, readBody(updatedPool))
	}
	updatedPool.Body.Close()
	view := requestJSON(t, http.MethodGet, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, "", f.cookie, "", "")
	if view.StatusCode != http.StatusOK {
		t.Fatalf("view status=%d body=%s", view.StatusCode, readBody(view))
	}
	var stalePlan channelMonitorPlan
	decodeResponse(t, view, &stalePlan)
	if stalePlan.BindingState != "stale" || stalePlan.LatestResult != nil {
		t.Fatalf("stale binding=%+v", stalePlan)
	}
	noRebind := requestJSON(t, http.MethodPatch, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":2,"enabled":true}`, f.cookie, f.csrf, f.base)
	if noRebind.StatusCode != http.StatusConflict {
		t.Fatalf("silent rebind status=%d body=%s", noRebind.StatusCode, readBody(noRebind))
	}
	noRebind.Body.Close()
	rebind := requestJSON(t, http.MethodPatch, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":2,"rebind":true}`, f.cookie, f.csrf, f.base)
	if rebind.StatusCode != http.StatusOK {
		t.Fatalf("rebind status=%d body=%s", rebind.StatusCode, readBody(rebind))
	}
	var rebound channelMonitorPlan
	decodeResponse(t, rebind, &rebound)
	if rebound.BindingState != "valid" || rebound.Revision != 3 {
		t.Fatalf("rebound=%+v", rebound)
	}
}
