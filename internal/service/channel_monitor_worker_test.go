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
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("monitor create status=%d body=%s", response.StatusCode, readBody(response))
	}
	var plan channelMonitorPlan
	decodeResponse(t, response, &plan)
	return plan
}

func TestChannelMonitorClaimRejectsChangedMappingBeforeNetwork(t *testing.T) {
	f := newChannelMonitorFixture(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	f.app.channelMonitors.now = func() time.Time { return now }
	plan := createChannelMonitorPlanForTest(t, f, true)
	if _, err := f.app.store.db.Exec(`UPDATE channel_monitor_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(now.Add(-5*time.Minute)), plan.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.app.channelMonitors.claimDue(context.Background())
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	if claim.Plan.ChannelID != f.channel.ID || claim.Plan.ModelID != f.modelID || claim.Plan.UpstreamID != f.upstream.ID || claim.Plan.Binding.PoolRevision != 1 {
		t.Fatalf("claim snapshot=%+v", claim)
	}
	if second, err := f.app.channelMonitors.claimDue(context.Background()); err != nil || second != nil {
		t.Fatalf("duplicate claim=%+v err=%v", second, err)
	}
	var next string
	if err := f.app.store.db.QueryRow(`SELECT next_run_at FROM channel_monitor_plans WHERE id=?`, plan.ID).Scan(&next); err != nil || next != channelMonitorNextRun(now, 300) {
		t.Fatalf("next=%q err=%v", next, err)
	}
	pool := fmt.Sprintf(`{"expected_revision":1,"items":[{"upstream_id":%q,"upstream_model":"changed-provider-model","priority":0,"weight":1,"max_concurrency":2,"channel_id":%q}]}`, f.upstream.ID, f.channel.ID)
	response := requestJSON(t, http.MethodPut, f.base+"/admin/api/v1/models/"+f.modelID+"/accounts", pool, f.cookie, f.csrf, f.base)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("route change status=%d body=%s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
	called := 0
	f.app.channelMonitors.execute = func(context.Context, string, string, int64, string) (upstreamTestOperationView, int, string, error) {
		called++
		return upstreamTestOperationView{}, 200, "", nil
	}
	f.app.channelMonitors.executeClaim(context.Background(), *claim)
	if called != 0 {
		t.Fatalf("stale mapping dispatched %d upstream calls", called)
	}
	var result, state string
	if err := f.app.store.db.QueryRow(`SELECT state,result_code FROM channel_monitor_runs WHERE operation_id=?`, claim.OperationID).Scan(&state, &result); err != nil || state != "completed" || result != "configuration_changed" {
		t.Fatalf("run=%s/%s err=%v", state, result, err)
	}
	loaded, err := loadChannelMonitorPlan(context.Background(), f.app.store.db, plan.ID, false)
	if err != nil || loaded.Enabled || loaded.NextRunAt != nil || loaded.Revision != 2 {
		t.Fatalf("stale plan=%+v err=%v", loaded, err)
	}
}

func TestChannelMonitorRestartInterruptsWithoutReplay(t *testing.T) {
	f := newChannelMonitorFixture(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	f.app.channelMonitors.now = func() time.Time { return now }
	plan := createChannelMonitorPlanForTest(t, f, true)
	if _, err := f.app.store.db.Exec(`UPDATE channel_monitor_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(now.Add(-time.Second)), plan.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.app.channelMonitors.claimDue(context.Background())
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	if err := migrateChannelMonitors(context.Background(), f.app.store.db, now); err != nil {
		t.Fatal(err)
	}
	var result string
	if err := f.app.store.db.QueryRow(`SELECT result_code FROM channel_monitor_runs WHERE operation_id=?`, claim.OperationID).Scan(&result); err != nil || result != "interrupted" {
		t.Fatalf("old result=%q err=%v", result, err)
	}
	if immediate, err := f.app.channelMonitors.claimDue(context.Background()); err != nil || immediate != nil {
		t.Fatalf("replayed old operation: %+v err=%v", immediate, err)
	}
	var next string
	if err := f.app.store.db.QueryRow(`SELECT next_run_at FROM channel_monitor_plans WHERE id=?`, plan.ID).Scan(&next); err != nil || next != channelMonitorNextRun(now, 300) {
		t.Fatalf("restart next=%q err=%v", next, err)
	}
}

func TestChannelMonitorCancelledClaimDoesNotDispatch(t *testing.T) {
	f := newChannelMonitorFixture(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	f.app.channelMonitors.now = func() time.Time { return now }
	plan := createChannelMonitorPlanForTest(t, f, true)
	if _, err := f.app.store.db.Exec(`UPDATE channel_monitor_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(now.Add(-time.Second)), plan.ID); err != nil {
		t.Fatal(err)
	}
	claim, err := f.app.channelMonitors.claimDue(context.Background())
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	called := 0
	f.app.channelMonitors.execute = func(context.Context, string, string, int64, string) (upstreamTestOperationView, int, string, error) {
		called++
		return upstreamTestOperationView{}, 200, "", nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f.app.channelMonitors.executeClaim(ctx, *claim)
	if called != 0 {
		t.Fatalf("cancelled claim dispatched %d upstream calls", called)
	}
	var result string
	if err := f.app.store.db.QueryRow(`SELECT result_code FROM channel_monitor_runs WHERE operation_id=?`, claim.OperationID).Scan(&result); err != nil || result != "cancelled" {
		t.Fatalf("cancelled result=%q err=%v", result, err)
	}
}

func TestChannelMonitorClaimsRespectGlobalAndAccountLimits(t *testing.T) {
	f := newChannelMonitorFixture(t)
	accounts := []upstreamView{f.upstream}
	for index := 0; index < 2; index++ {
		accounts = append(accounts, createModelAdmissionUpstream(t, f.base, f.cookie, f.csrf,
			fmt.Sprintf("Monitor account %d", index+2), "openai-compatible", "http://127.0.0.1:1", fmt.Sprintf("synthetic-monitor-key-%d", index+2)))
	}
	pool := fmt.Sprintf(`{"expected_revision":1,"items":[{"upstream_id":%q,"upstream_model":"synthetic-1","priority":0,"weight":1,"max_concurrency":2,"channel_id":%q},{"upstream_id":%q,"upstream_model":"synthetic-2","priority":1,"weight":1,"max_concurrency":2,"channel_id":%q},{"upstream_id":%q,"upstream_model":"synthetic-3","priority":2,"weight":1,"max_concurrency":2,"channel_id":%q}]}`,
		accounts[0].ID, f.channel.ID, accounts[1].ID, f.channel.ID, accounts[2].ID, f.channel.ID)
	response := requestJSON(t, http.MethodPut, f.base+"/admin/api/v1/models/"+f.modelID+"/accounts", pool, f.cookie, f.csrf, f.base)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pool status=%d body=%s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	f.app.channelMonitors.now = func() time.Time { return now }
	planIDs := make([]string, 0, 4)
	for index, accountIndex := range []int{0, 0, 1, 2} {
		body := fmt.Sprintf(`{"name":"Capacity %d","channel_id":%q,"model_id":%q,"upstream_id":%q,"scope":"catalog","interval_seconds":300,"enabled":true}`,
			index, f.channel.ID, f.modelID, accounts[accountIndex].ID)
		response := requestJSON(t, http.MethodPost, f.base+"/admin/api/v1/channel-monitors", body, f.cookie, f.csrf, f.base)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("plan %d status=%d body=%s", index, response.StatusCode, readBody(response))
		}
		var plan channelMonitorPlan
		decodeResponse(t, response, &plan)
		planIDs = append(planIDs, plan.ID)
		if _, err := f.app.store.db.Exec(`UPDATE channel_monitor_plans SET next_run_at=? WHERE id=?`, formatAccountPoolTime(now.Add(time.Duration(index-10)*time.Second)), plan.ID); err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < 2; index++ {
		claim, err := f.app.channelMonitors.claimDue(context.Background())
		if err != nil || claim == nil {
			t.Fatalf("claim %d=%+v err=%v", index, claim, err)
		}
		if index == 0 && claim.Plan.ID != planIDs[0] || index == 1 && claim.Plan.ID != planIDs[2] {
			t.Fatalf("claim %d selected plan %s", index, claim.Plan.ID)
		}
	}
	if third, err := f.app.channelMonitors.claimDue(context.Background()); err != nil || third != nil {
		t.Fatalf("global capacity allowed third claim=%+v err=%v", third, err)
	}
	var running, firstAccount int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM channel_monitor_runs WHERE state='running'`).Scan(&running); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM channel_monitor_runs WHERE state='running' AND upstream_id=?`, accounts[0].ID).Scan(&firstAccount); err != nil {
		t.Fatal(err)
	}
	if running != 2 || firstAccount != 1 {
		t.Fatalf("running=%d firstAccount=%d", running, firstAccount)
	}
}

func TestChannelMonitorClaimStorageFailureRollsBack(t *testing.T) {
	f := newChannelMonitorFixture(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	f.app.channelMonitors.now = func() time.Time { return now }
	plan := createChannelMonitorPlanForTest(t, f, true)
	oldNext := formatAccountPoolTime(now.Add(-time.Second))
	if _, err := f.app.store.db.Exec(`UPDATE channel_monitor_plans SET next_run_at=? WHERE id=?`, oldNext, plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`CREATE TRIGGER channel_monitor_fail_insert BEFORE INSERT ON channel_monitor_runs BEGIN SELECT RAISE(ABORT, 'synthetic storage failure'); END`); err != nil {
		t.Fatal(err)
	}
	claim, err := f.app.channelMonitors.claimDue(context.Background())
	if err == nil || claim != nil {
		t.Fatalf("storage failure claim=%+v err=%v", claim, err)
	}
	var next string
	var count int
	if err := f.app.store.db.QueryRow(`SELECT next_run_at FROM channel_monitor_plans WHERE id=?`, plan.ID).Scan(&next); err != nil || next != oldNext {
		t.Fatalf("next after rollback=%q err=%v", next, err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM channel_monitor_runs WHERE plan_id=?`, plan.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("runs after rollback=%d err=%v", count, err)
	}
}
