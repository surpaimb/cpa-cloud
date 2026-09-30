package service

// Independently authored acceptance for docs/channel-monitor-contract.md.
import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func createChannelMonitorPlanForTest(t *testing.T, f channelMonitorFixture, enabled bool) channelMonitorPlan {
	t.Helper()
	response := requestJSON(t, http.MethodPost, f.base+"/admin/api/v1/channel-monitors", f.createBody(enabled), f.cookie, f.csrf, f.base)
	if response.StatusCode != http.StatusCreated { t.Fatalf("monitor create status=%d body=%s", response.StatusCode, readBody(response)) }
	var plan channelMonitorPlan
	decodeResponse(t, response, &plan)
	return plan
}

func TestChannelMonitorClaimRejectsChangedMappingBeforeNetwork(t *testing.T) {
	f := newChannelMonitorFixture(t)
	now := time.Date(2026,9,30,12,0,0,0,time.UTC)
	f.app.channelMonitors.now = func() time.Time { return now }
	plan := createChannelMonitorPlanForTest(t, f, true)
	if _, err := f.app.store.db.Exec(`UPDATE channel_monitor_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(now.Add(-5*time.Minute)), plan.ID); err != nil { t.Fatal(err) }
	claim, err := f.app.channelMonitors.claimDue(context.Background())
	if err != nil || claim == nil { t.Fatalf("claim=%+v err=%v", claim, err) }
	if claim.Plan.ChannelID != f.channel.ID || claim.Plan.ModelID != f.modelID || claim.Plan.UpstreamID != f.upstream.ID || claim.Plan.Binding.PoolRevision != 1 { t.Fatalf("claim snapshot=%+v", claim) }
	if second, err := f.app.channelMonitors.claimDue(context.Background()); err != nil || second != nil { t.Fatalf("duplicate claim=%+v err=%v", second, err) }
	var next string
	if err := f.app.store.db.QueryRow(`SELECT next_run_at FROM channel_monitor_plans WHERE id=?`, plan.ID).Scan(&next); err != nil || next != channelMonitorNextRun(now, 300) { t.Fatalf("next=%q err=%v", next, err) }
	pool := fmt.Sprintf(`{"expected_revision":1,"items":[{"upstream_id":%q,"upstream_model":"changed-provider-model","priority":0,"weight":1,"max_concurrency":2,"channel_id":%q}]}`, f.upstream.ID, f.channel.ID)
	response := requestJSON(t, http.MethodPut, f.base+"/admin/api/v1/models/"+f.modelID+"/accounts", pool, f.cookie, f.csrf, f.base)
	if response.StatusCode != http.StatusOK { t.Fatalf("route change status=%d body=%s", response.StatusCode, readBody(response)) }
	response.Body.Close()
	called := 0
	f.app.channelMonitors.execute = func(context.Context, string, string, int64, string) (upstreamTestOperationView, int, string, error) { called++; return upstreamTestOperationView{}, 200, "", nil }
	f.app.channelMonitors.executeClaim(context.Background(), *claim)
	if called != 0 { t.Fatalf("stale mapping dispatched %d upstream calls", called) }
	var result, state string
	if err := f.app.store.db.QueryRow(`SELECT state,result_code FROM channel_monitor_runs WHERE operation_id=?`, claim.OperationID).Scan(&state,&result); err != nil || state != "completed" || result != "configuration_changed" { t.Fatalf("run=%s/%s err=%v", state,result,err) }
	loaded, err := loadChannelMonitorPlan(context.Background(), f.app.store.db, plan.ID, false)
	if err != nil || loaded.Enabled || loaded.NextRunAt != nil || loaded.Revision != 2 { t.Fatalf("stale plan=%+v err=%v", loaded,err) }
}

func TestChannelMonitorRestartInterruptsWithoutReplay(t *testing.T) {
	f := newChannelMonitorFixture(t)
	now := time.Date(2026,9,30,12,0,0,0,time.UTC)
	f.app.channelMonitors.now = func() time.Time { return now }
	plan := createChannelMonitorPlanForTest(t, f, true)
	if _, err := f.app.store.db.Exec(`UPDATE channel_monitor_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(now.Add(-time.Second)), plan.ID); err != nil { t.Fatal(err) }
	claim, err := f.app.channelMonitors.claimDue(context.Background())
	if err != nil || claim == nil { t.Fatalf("claim=%+v err=%v", claim,err) }
	if err := migrateChannelMonitors(context.Background(), f.app.store.db, now); err != nil { t.Fatal(err) }
	var result string
	if err := f.app.store.db.QueryRow(`SELECT result_code FROM channel_monitor_runs WHERE operation_id=?`, claim.OperationID).Scan(&result); err != nil || result != "interrupted" { t.Fatalf("old result=%q err=%v", result,err) }
	if immediate, err := f.app.channelMonitors.claimDue(context.Background()); err != nil || immediate != nil { t.Fatalf("replayed old operation: %+v err=%v", immediate,err) }
	var next string
	if err := f.app.store.db.QueryRow(`SELECT next_run_at FROM channel_monitor_plans WHERE id=?`, plan.ID).Scan(&next); err != nil || next != channelMonitorNextRun(now, 300) { t.Fatalf("restart next=%q err=%v", next,err) }
}
