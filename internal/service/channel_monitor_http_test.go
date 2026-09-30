package service

// Independently authored acceptance for docs/channel-monitor-contract.md.
import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

type channelMonitorFixture struct {
	app *App
	base string
	cookie *http.Cookie
	csrf string
	channel accountChannelView
	upstream upstreamView
	modelID string
}

func newChannelMonitorFixture(t *testing.T) channelMonitorFixture {
	t.Helper()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	upstream := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "Synthetic monitor", "openai-compatible", "http://127.0.0.1:1", "synthetic-monitor-secret")
	modelID := "monitor-public-model"
	createModelAdmissionModel(t, server.URL, cookie, csrf, modelID, upstream.ID, "monitor-provider-model")
	response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/channels", `{"name":"Monitor channel"}`, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusCreated { t.Fatalf("channel status=%d body=%s", response.StatusCode, readBody(response)) }
	var channel accountChannelView
	decodeResponse(t, response, &channel)
	pool := fmt.Sprintf(`{"expected_revision":0,"items":[{"upstream_id":%q,"upstream_model":"monitor-provider-model","priority":0,"weight":1,"max_concurrency":2,"channel_id":%q}]}`, upstream.ID, channel.ID)
	configured := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/models/"+modelID+"/accounts", pool, cookie, csrf, server.URL)
	if configured.StatusCode != http.StatusOK { t.Fatalf("pool status=%d body=%s", configured.StatusCode, readBody(configured)) }
	configured.Body.Close()
	return channelMonitorFixture{app:app,base:server.URL,cookie:cookie,csrf:csrf,channel:channel,upstream:upstream,modelID:modelID}
}

func (f channelMonitorFixture) createBody(enabled bool) string {
	return fmt.Sprintf(`{"name":"Directory watch","channel_id":%q,"model_id":%q,"upstream_id":%q,"scope":"catalog","interval_seconds":300,"enabled":%t}`, f.channel.ID, f.modelID, f.upstream.ID, enabled)
}

func TestChannelMonitorHTTPBindingAndCAS(t *testing.T) {
	f := newChannelMonitorFixture(t)
	f.app.channelMonitors.now = func() time.Time { return time.Date(2026,9,30,12,0,0,0,time.UTC) }
	wrongOrigin := requestJSON(t, http.MethodPost, f.base+"/admin/api/v1/channel-monitors", f.createBody(true), f.cookie, f.csrf, "http://elsewhere.invalid")
	if wrongOrigin.StatusCode != http.StatusForbidden { t.Fatalf("origin status=%d body=%s", wrongOrigin.StatusCode, readBody(wrongOrigin)) }
	wrongOrigin.Body.Close()
	created := requestJSON(t, http.MethodPost, f.base+"/admin/api/v1/channel-monitors", f.createBody(true), f.cookie, f.csrf, f.base)
	if created.StatusCode != http.StatusCreated { t.Fatalf("create status=%d body=%s", created.StatusCode, readBody(created)) }
	var plan channelMonitorPlan
	decodeResponse(t, created, &plan)
	if plan.BindingState != "valid" || plan.Revision != 1 || plan.NextRunAt == nil || *plan.NextRunAt != formatAccountPoolTime(time.Date(2026,9,30,12,5,0,0,time.UTC)) { t.Fatalf("created=%+v", plan) }
	stale := requestJSON(t, http.MethodPatch, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":2,"name":"Wrong"}`, f.cookie, f.csrf, f.base)
	if stale.StatusCode != http.StatusConflict { t.Fatalf("stale status=%d body=%s", stale.StatusCode, readBody(stale)) }
	stale.Body.Close()
	nameOnly := requestJSON(t, http.MethodPatch, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":1,"name":"Renamed"}`, f.cookie, f.csrf, f.base)
	if nameOnly.StatusCode != http.StatusOK { t.Fatalf("name status=%d body=%s", nameOnly.StatusCode, readBody(nameOnly)) }
	var renamed channelMonitorPlan
	decodeResponse(t, nameOnly, &renamed)
	if renamed.Name != "Renamed" || renamed.Revision != 2 || renamed.BindingState != "valid" { t.Fatalf("renamed=%+v", renamed) }
	pool := fmt.Sprintf(`{"expected_revision":1,"items":[{"upstream_id":%q,"upstream_model":"monitor-provider-model","priority":0,"weight":1,"max_concurrency":2,"channel_id":%q}]}`, f.upstream.ID, f.channel.ID)
	updatedPool := requestJSON(t, http.MethodPut, f.base+"/admin/api/v1/models/"+f.modelID+"/accounts", pool, f.cookie, f.csrf, f.base)
	if updatedPool.StatusCode != http.StatusOK { t.Fatalf("pool update status=%d body=%s", updatedPool.StatusCode, readBody(updatedPool)) }
	updatedPool.Body.Close()
	view := requestJSON(t, http.MethodGet, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, "", f.cookie, "", "")
	if view.StatusCode != http.StatusOK { t.Fatalf("view status=%d body=%s", view.StatusCode, readBody(view)) }
	var stalePlan channelMonitorPlan
	decodeResponse(t, view, &stalePlan)
	if stalePlan.BindingState != "stale" || stalePlan.LatestResult != nil { t.Fatalf("stale binding=%+v", stalePlan) }
	noRebind := requestJSON(t, http.MethodPatch, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":2,"enabled":true}`, f.cookie, f.csrf, f.base)
	if noRebind.StatusCode != http.StatusConflict { t.Fatalf("silent rebind status=%d body=%s", noRebind.StatusCode, readBody(noRebind)) }
	noRebind.Body.Close()
	rebind := requestJSON(t, http.MethodPatch, f.base+"/admin/api/v1/channel-monitors/"+plan.ID, `{"expected_revision":2,"rebind":true}`, f.cookie, f.csrf, f.base)
	if rebind.StatusCode != http.StatusOK { t.Fatalf("rebind status=%d body=%s", rebind.StatusCode, readBody(rebind)) }
	var rebound channelMonitorPlan
	decodeResponse(t, rebind, &rebound)
	if rebound.BindingState != "valid" || rebound.Revision != 3 { t.Fatalf("rebound=%+v", rebound) }
}
