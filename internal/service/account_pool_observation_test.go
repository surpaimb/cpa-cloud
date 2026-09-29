// Independently authored acceptance cases for docs/account-pool-runtime-observation-contract.md.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/keypolicy"
)

func seedObservationPool(t *testing.T, f *accountPoolFixture, modelID, accountID string, capacity int) {
	t.Helper()
	f.insertModel(t, modelID, accountID, "provider-model")
	if _, err := f.app.store.db.Exec(`INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES(?,1,?)`, modelID, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,priority,weight,max_concurrency,position) VALUES(?,?,?,0,1,?,0)`, modelID, accountID, "provider-model", capacity); err != nil {
		t.Fatal(err)
	}
}

func observationResponse(t *testing.T, f *accountPoolFixture, modelID, origin string, cookie *http.Cookie) (int, poolRuntimeObservationView, string, http.Header) {
	t.Helper()
	path := "/admin/api/v1/models/" + modelID + "/pool-runtime"
	if queryAt := strings.IndexByte(modelID, '?'); queryAt >= 0 {
		path = "/admin/api/v1/models/" + modelID[:queryAt] + "/pool-runtime" + modelID[queryAt:]
	}
	response := requestJSON(t, http.MethodGet, f.server.URL+path, "", cookie, "", origin)
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	var view poolRuntimeObservationView
	if response.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &view); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode, view, string(body), response.Header
}

func seedObservationIdentity(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO employees(id,name,department,note,status,model_mode,revision,created_at) VALUES('emp_obs','Synthetic','','','active','all',1,?)`, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('key_obs','emp_obs','Synthetic','selector_obs',X'01',1,'operation_obs',?)`, utcNow()); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := keypolicy.CreateDefaultTx(context.Background(), tx, "key_obs", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func insertObservationRequestLease(t *testing.T, db *sql.DB, id, accountID, modelID string, created, expiry time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_pool_runtime_leases(lease_id,account_id,public_model,employee_id,key_id,pool_revision,account_revision,expires_at,created_at) VALUES(?,?,?,'emp_obs','key_obs',1,1,?,?)`, id, accountID, modelID, formatAccountPoolTime(expiry), formatAccountPoolTime(created)); err != nil {
		t.Fatal(err)
	}
}

func insertObservationMaintenanceLease(t *testing.T, db *sql.DB, id, accountID, modelID string, created, expiry time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_pool_maintenance_leases(lease_id,operation_id,account_id,cooldown_event_id,recovery_revision,pool_revision,account_revision,public_model,upstream_model,provider_kind,protocol,dispatch_phase,expires_at,created_at) VALUES(?, ?,?,'cool_obs',1,1,1,?,'provider-model','openai-compatible','openai-chat',1,?,?)`, id, "op_"+id, accountID, modelID, formatAccountPoolTime(expiry), formatAccountPoolTime(created)); err != nil {
		t.Fatal(err)
	}
}

func insertObservationRecovery(t *testing.T, db *sql.DB, accountID, modelID string, now time.Time) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO account_pool_runtime_cooldowns(account_id,event_id,failure_class,cooldown_until,updated_at) VALUES(?,'cool_obs','transient',?,?)`, accountID, formatAccountPoolTime(now.Add(time.Minute)), formatAccountPoolTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account_recovery_states(account_id,cooldown_event_id,operation_id,recovery_revision,pool_revision,account_revision,provider_kind,source_snapshot,client_id,public_model,upstream_model,protocol,state,next_probe_at,created_at,updated_at) VALUES(?,'cool_obs','recovery_obs',1,1,1,'openai-compatible','api_key',NULL,?,'provider-model','openai-chat','required',?,?,?)`, accountID, modelID, formatAccountPoolTime(now), formatAccountPoolTime(now), formatAccountPoolTime(now)); err != nil {
		t.Fatal(err)
	}
}

func TestPoolRuntimeObservationGlobalCapacityReservationsAndBoundaries(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	f.insertUpstream(t, "ups_obs_a", "openai-compatible")
	f.insertUpstream(t, "ups_obs_b", "openai-compatible")
	seedObservationPool(t, f, "model_obs_a", "ups_obs_a", 4)
	seedObservationPool(t, f, "model_obs_b", "ups_obs_a", 2)
	seedObservationIdentity(t, f.app.store.db)
	now := time.Now().UTC()
	insertObservationRequestLease(t, f.app.store.db, "lease_obs_active", "ups_obs_a", "model_obs_a", now.Add(-time.Second), now.Add(time.Minute))
	insertObservationRequestLease(t, f.app.store.db, "lease_obs_expired", "ups_obs_a", "model_obs_b", now.Add(-time.Minute), now.Add(-time.Second))
	insertObservationMaintenanceLease(t, f.app.store.db, "maintenance_obs", "ups_obs_a", "model_obs_b", now.Add(-time.Second), now.Add(time.Minute))
	insertObservationRecovery(t, f.app.store.db, "ups_obs_a", "model_obs_b", now)
	status, view, body, header := observationResponse(t, f, "model_obs_a", f.server.URL, f.cookie)
	if status != http.StatusOK || view.ModelID != "model_obs_a" || view.ModelRevision != 1 || view.PoolRevision != 1 || view.PoolStatus != "explicit_pool" || len(view.Items) != 1 || header.Get("Cache-Control") != "no-store" {
		t.Fatalf("unexpected observation: status=%d view=%+v body=%s", status, view, body)
	}
	item := view.Items[0]
	if item.UpstreamID != "ups_obs_a" || item.ConfiguredMaxConcurrency != 4 || item.GlobalMaxConcurrency == nil || *item.GlobalMaxConcurrency != 2 || item.RequestReservations != 1 || item.MaintenanceReservations != 1 || item.RemainingLocalSlots == nil || *item.RemainingLocalSlots != 0 || !strings.Contains(strings.Join(item.BlockReasons, ","), "capacity_reserved") || !strings.Contains(strings.Join(item.BlockReasons, ","), "recovery_isolated") || item.CooldownUntil == nil {
		t.Fatalf("wrong shared reservation snapshot: %+v", item)
	}
	if _, err := time.Parse(time.RFC3339Nano, view.AsOf); err != nil || strings.Contains(body, "emp_obs") || strings.Contains(body, "key_obs") || strings.Contains(body, "provider-model") || strings.Contains(body, "example.invalid") || strings.Contains(body, "lease_obs") {
		t.Fatalf("invalid timestamp or identity leak: %s", body)
	}
	if _, err := f.app.store.db.Exec(`UPDATE models SET enabled=0 WHERE id='model_obs_b'`); err != nil {
		t.Fatal(err)
	}
	status, view, _, _ = observationResponse(t, f, "model_obs_a", "", f.cookie)
	if status != http.StatusOK || view.Items[0].GlobalMaxConcurrency == nil || *view.Items[0].GlobalMaxConcurrency != 4 || view.Items[0].RemainingLocalSlots == nil || *view.Items[0].RemainingLocalSlots != 2 {
		t.Fatalf("disabled peer model still lowered global capacity: status=%d view=%+v", status, view)
	}
	status, view, _, _ = observationResponse(t, f, "model_obs_b", "", f.cookie)
	if status != http.StatusOK || view.PoolStatus != "model_disabled" || view.Items[0].GlobalMaxConcurrency != nil || view.Items[0].RemainingLocalSlots != nil || len(view.Items[0].BlockReasons) == 0 || view.Items[0].BlockReasons[0] != "model_disabled" {
		t.Fatalf("disabled model must not project usable slots: status=%d view=%+v", status, view)
	}
}

func TestPoolRuntimeObservationLegacyAuthOriginAndNoPool(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	f.insertUpstream(t, "ups_obs_legacy", "openai-compatible")
	f.insertModel(t, "model_obs_legacy", "ups_obs_legacy", "provider-model")
	status, view, _, _ := observationResponse(t, f, "model_obs_legacy", "", f.cookie)
	if status != http.StatusOK || view.PoolStatus != "legacy_no_pool" || view.PoolRevision != 0 || len(view.Items) != 0 {
		t.Fatalf("legacy model guessed a pool: %+v", view)
	}
	for _, item := range []struct {
		name, modelID, origin string
		cookie                *http.Cookie
		status                int
	}{
		{"anonymous", "model_obs_legacy", "", nil, 401},
		{"wrong_origin", "model_obs_legacy", "https://wrong.invalid", f.cookie, 403},
		{"unknown_model", "missing", "", f.cookie, 404},
		{"query", "model_obs_legacy?include=all", "", f.cookie, 400},
	} {
		t.Run(item.name, func(t *testing.T) {
			got, _, body, _ := observationResponse(t, f, item.modelID, item.origin, item.cookie)
			if got != item.status || strings.Contains(body, "credential") || strings.Contains(body, "selector_obs") {
				t.Fatalf("status=%d body=%s", got, body)
			}
		})
	}
}

func TestPoolRuntimeObservationLocalBlockReasons(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	f.insertUpstream(t, "ups_obs_disabled", "openai-compatible")
	f.insertUpstream(t, "ups_obs_membership", codexMembershipProvider)
	seedObservationPool(t, f, "model_obs_disabled", "ups_obs_disabled", 1)
	seedObservationPool(t, f, "model_obs_membership", "ups_obs_membership", 1)
	if _, err := f.app.store.db.Exec(`UPDATE upstreams SET enabled=0 WHERE id='ups_obs_disabled'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE upstreams SET credential_state='reauth_required' WHERE id='ups_obs_membership'`); err != nil {
		t.Fatal(err)
	}
	status, view, _, _ := observationResponse(t, f, "model_obs_disabled", "", f.cookie)
	if status != 200 || strings.Join(view.Items[0].BlockReasons, ",") != "upstream_disabled" || view.Items[0].RemainingLocalSlots == nil || *view.Items[0].RemainingLocalSlots != 1 {
		t.Fatalf("disabled account snapshot: %+v", view)
	}
	status, view, _, _ = observationResponse(t, f, "model_obs_membership", "", f.cookie)
	if status != 200 || strings.Join(view.Items[0].BlockReasons, ",") != "membership_disabled,reauth_required" {
		t.Fatalf("membership credential snapshot: %+v", view)
	}
}

func TestPoolRuntimeObservationRestartRetainsFutureReservation(t *testing.T) {
	directory := t.TempDir()
	if err := Initialize(context.Background(), directory, strings.NewReader(accountPoolTestPassword+"\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: directory, Listen: "127.0.0.1:0", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	f := &accountPoolFixture{app: app}
	f.insertUpstream(t, "ups_obs_restart", "openai-compatible")
	seedObservationPool(t, f, "model_obs_restart", "ups_obs_restart", 1)
	seedObservationIdentity(t, app.store.db)
	now := time.Now().UTC()
	insertObservationRequestLease(t, app.store.db, "lease_obs_restart", "ups_obs_restart", "model_obs_restart", now.Add(-time.Second), now.Add(time.Minute))
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Config{DataDir: directory, Listen: "127.0.0.1:0", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	view, err := reopened.queryPoolRuntimeObservation(context.Background(), "model_obs_restart")
	if err != nil || len(view.Items) != 1 || view.Items[0].RequestReservations != 1 || view.Items[0].RemainingLocalSlots == nil || *view.Items[0].RemainingLocalSlots != 0 {
		t.Fatalf("restart lost conservative reservation: view=%+v err=%v", view, err)
	}
}

func TestPoolRuntimeObservationSnapshotExcludesLaterWriteAndCancellation(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	f.insertUpstream(t, "ups_obs_snapshot", "openai-compatible")
	seedObservationPool(t, f, "model_obs_snapshot", "ups_obs_snapshot", 1)
	seedObservationIdentity(t, f.app.store.db)
	ctx := context.Background()
	tx, err := f.app.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	model, err := readPoolRuntimeModelTx(ctx, tx, "model_obs_snapshot")
	if err != nil {
		t.Fatal(err)
	}
	asOf := time.Now().UTC()
	var sequence int
	var databaseName, databasePath string
	if err := tx.QueryRowContext(ctx, `PRAGMA database_list`).Scan(&sequence, &databaseName, &databasePath); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	insertObservationRequestLease(t, writer, "lease_obs_later", "ups_obs_snapshot", "model_obs_snapshot", asOf, asOf.Add(time.Minute))
	view, err := f.app.readPoolRuntimeObservationTx(ctx, tx, model, asOf)
	if err != nil {
		t.Fatal(err)
	}
	if view.Items[0].RequestReservations != 0 {
		t.Fatalf("post-snapshot lease leaked into read: %+v", view.Items[0])
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	fresh, err := f.app.queryPoolRuntimeObservation(context.Background(), "model_obs_snapshot")
	if err != nil || fresh.Items[0].RequestReservations != 1 || fresh.Items[0].RemainingLocalSlots == nil || *fresh.Items[0].RemainingLocalSlots != 0 {
		t.Fatalf("fresh read missed lease: view=%+v err=%v", fresh, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.app.queryPoolRuntimeObservation(cancelled, "model_obs_snapshot"); err == nil {
		t.Fatal("cancelled observation unexpectedly succeeded")
	}
}

func TestPoolRuntimeObservationCorruptionFailsClosed(t *testing.T) {
	for _, change := range []struct {
		name   string
		mutate func(*testing.T, *accountPoolFixture)
	}{
		{"missing_maintenance_table", func(t *testing.T, f *accountPoolFixture) {
			if _, err := f.app.store.db.Exec(`DROP TABLE account_pool_maintenance_leases`); err != nil {
				t.Fatal(err)
			}
		}},
		{"bad_lease_time", func(t *testing.T, f *accountPoolFixture) {
			seedObservationIdentity(t, f.app.store.db)
			now := time.Now().UTC()
			insertObservationRequestLease(t, f.app.store.db, "lease_obs_bad", "ups_obs_bad", "model_obs_bad", now, now.Add(time.Minute))
			if _, err := f.app.store.db.Exec(`UPDATE account_pool_runtime_leases SET expires_at='not-a-time' WHERE lease_id='lease_obs_bad'`); err != nil {
				t.Fatal(err)
			}
		}},
		{"bad_route_position", func(t *testing.T, f *accountPoolFixture) {
			if _, err := f.app.store.db.Exec(`UPDATE model_account_pool_routes SET position=1 WHERE model_id='model_obs_bad'`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(change.name, func(t *testing.T) {
			f := newAccountPoolFixture(t, false)
			f.insertUpstream(t, "ups_obs_bad", "openai-compatible")
			seedObservationPool(t, f, "model_obs_bad", "ups_obs_bad", 1)
			change.mutate(t, f)
			status, view, body, _ := observationResponse(t, f, "model_obs_bad", "", f.cookie)
			if status != 503 || view.ModelID != "" || !strings.Contains(body, "storage_unavailable") || strings.Contains(body, "ups_obs_bad") || strings.Contains(body, "not-a-time") {
				t.Fatalf("did not fail closed: status=%d view=%+v body=%s", status, view, body)
			}
		})
	}
}
