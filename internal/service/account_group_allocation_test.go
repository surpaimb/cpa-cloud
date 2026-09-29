package service

// Independent acceptance coverage for docs/account-group-cost-allocation-contract.md.
// All upstreams and credentials in this file are synthetic and loopback-only.
import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

func TestAccountGroupAllocationMigrationRollbackBackfillPartialAndRestart(t *testing.T) {
	ctx := context.Background()
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	if err := migrateAccountingV2(ctx, s.db); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO account_groups(id,name,revision,created_at) VALUES('grp_existing','Existing',7,'2026-09-29T01:02:03Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE VIEW account_group_allocation_current AS SELECT 'preserve' AS marker`); err != nil {
		t.Fatal(err)
	}
	if err := migrateAccountGroupAllocation(ctx, s.db); err == nil {
		t.Fatal("migration accepted a conflicting object")
	}
	var created int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('account_group_allocation_migration_state','account_group_allocation_versions')`).Scan(&created); err != nil || created != 0 {
		t.Fatalf("failed migration committed partial tables count=%d err=%v", created, err)
	}
	if _, err := s.db.Exec(`DROP VIEW account_group_allocation_current`); err != nil {
		t.Fatal(err)
	}
	if err := migrateAccountGroupAllocation(ctx, s.db); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	var version, createdAt string
	var revision, ppm int64
	if err := s.db.QueryRow(`SELECT v.version,v.group_revision,v.multiplier_ppm,v.created_at
		FROM account_group_allocation_current c JOIN account_group_allocation_versions v
		ON v.version=c.version AND v.group_id=c.group_id AND v.group_revision=c.group_revision
		WHERE c.group_id='grp_existing'`).Scan(&version, &revision, &ppm, &createdAt); err != nil {
		t.Fatal(err)
	}
	if version == "" || revision != 7 || ppm != accounting.AllocationMultiplierScale || createdAt != "2026-09-29T01:02:03Z" {
		t.Fatalf("backfill=%q/%d/%d/%q", version, revision, ppm, createdAt)
	}
	if err := migrateAccountGroupAllocation(ctx, s.db); err != nil {
		t.Fatalf("restart migration: %v", err)
	}
	var afterVersion string
	var versions int
	if err := s.db.QueryRow(`SELECT version FROM account_group_allocation_current WHERE group_id='grp_existing'`).Scan(&afterVersion); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM account_group_allocation_versions WHERE group_id='grp_existing'`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if afterVersion != version || versions != 1 {
		t.Fatalf("restart changed history version=%q count=%d", afterVersion, versions)
	}
	if _, err := s.db.Exec(`UPDATE account_group_allocation_versions SET multiplier_ppm=2 WHERE version=?`, version); err == nil {
		t.Fatal("immutable allocation version was updated")
	}

	partial, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer partial.close()
	if err := migrateAccountingV2(ctx, partial.db); err != nil {
		t.Fatal(err)
	}
	if _, err := partial.db.Exec(`CREATE TABLE account_group_allocation_migration_state(singleton INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateAccountGroupAllocation(ctx, partial.db); err == nil || !strings.Contains(err.Error(), "partial") {
		t.Fatalf("partial schema result=%v", err)
	}
	if err := partial.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('account_group_allocation_versions','account_group_allocation_current')`).Scan(&created); err != nil || created != 0 {
		t.Fatalf("partial schema created more tables count=%d err=%v", created, err)
	}
}

func TestAccountGroupAllocationAdminStrictCASIdempotencyAndRollback(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	createdResponse := requestJSON(t, http.MethodPost, f.server.URL+"/admin/api/v1/account-groups", `{"name":"Allocation group"}`, f.cookie, f.csrf, f.server.URL)
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createdResponse.StatusCode, readBody(createdResponse))
	}
	var group accountGroupView
	decodeResponse(t, createdResponse, &group)
	createdResponse.Body.Close()
	if group.Revision != 1 || group.Allocation.Version == "" || group.Allocation.MultiplierPPM != "1000000" {
		t.Fatalf("default allocation=%+v", group)
	}
	endpoint := f.server.URL + "/admin/api/v1/account-groups/" + group.ID + "/allocation"
	valid := `{"operation_id":"550e8400-e29b-41d4-a716-446655440000","expected_revision":1,"allocation_multiplier_ppm":"1250000"}`
	unauthorized := requestJSON(t, http.MethodPost, endpoint, valid, nil, "", "")
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.StatusCode, readBody(unauthorized))
	}
	unauthorized.Body.Close()
	missingCSRF := requestJSON(t, http.MethodPost, endpoint, valid, f.cookie, "", f.server.URL)
	if missingCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", missingCSRF.StatusCode, readBody(missingCSRF))
	}
	missingCSRF.Body.Close()
	for index, body := range []string{
		`{"operation_id":"550e8400-e29b-41d4-a716-446655440000","expected_revision":1,"allocation_multiplier_ppm":"01250000"}`,
		`{"operation_id":"550e8400-e29b-41d4-a716-446655440000","expected_revision":1,"allocation_multiplier_ppm":"0"}`,
		`{"operation_id":"550e8400-e29b-41d4-a716-446655440000","expected_revision":1,"allocation_multiplier_ppm":"1000000001"}`,
		`{"operation_id":"550e8400-e29b-41d4-a716-446655440000","expected_revision":1,"allocation_multiplier_ppm":1250000}`,
		`{"operation_id":"550e8400-e29b-41d4-a716-446655440000","expected_revision":9007199254740991,"allocation_multiplier_ppm":"1250000"}`,
		`{"operation_id":"550e8400-e29b-41d4-a716-446655440000","expected_revision":1,"allocation_multiplier_ppm":"1250000","extra":true}`,
		`{"operation_id":"550e8400-e29b-41d4-a716-446655440000","operation_id":"550e8400-e29b-41d4-a716-446655440000","expected_revision":1,"allocation_multiplier_ppm":"1250000"}`,
	} {
		response := requestJSON(t, http.MethodPost, endpoint, body, f.cookie, f.csrf, f.server.URL)
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid body %d status=%d body=%s", index, response.StatusCode, readBody(response))
		}
		response.Body.Close()
	}
	zeroRevision := requestJSON(t, http.MethodPost, endpoint, strings.Replace(valid, `"expected_revision":1`, `"expected_revision":0`, 1), f.cookie, f.csrf, f.server.URL)
	if zeroRevision.StatusCode != http.StatusConflict {
		t.Fatalf("zero revision status=%d body=%s", zeroRevision.StatusCode, readBody(zeroRevision))
	}
	zeroRevision.Body.Close()

	if _, err := f.app.store.db.Exec(`CREATE TRIGGER account_group_allocation_test_failure BEFORE INSERT ON account_group_allocation_versions WHEN NEW.operation_id IS NOT NULL BEGIN SELECT RAISE(ABORT,'synthetic allocation failure'); END`); err != nil {
		t.Fatal(err)
	}
	failed := requestJSON(t, http.MethodPost, endpoint, valid, f.cookie, f.csrf, f.server.URL)
	if failed.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("failed write status=%d body=%s", failed.StatusCode, readBody(failed))
	}
	failed.Body.Close()
	var currentRevision, versions int64
	if err := f.app.store.db.QueryRow(`SELECT revision FROM account_groups WHERE id=?`, group.ID).Scan(&currentRevision); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_group_allocation_versions WHERE group_id=?`, group.ID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if currentRevision != 1 || versions != 1 {
		t.Fatalf("failed write state revision=%d versions=%d", currentRevision, versions)
	}
	if _, err := f.app.store.db.Exec(`DROP TRIGGER account_group_allocation_test_failure`); err != nil {
		t.Fatal(err)
	}
	saved := requestJSON(t, http.MethodPost, endpoint, valid, f.cookie, f.csrf, f.server.URL)
	if saved.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d body=%s", saved.StatusCode, readBody(saved))
	}
	var first accountGroupAllocationVersionView
	decodeResponse(t, saved, &first)
	saved.Body.Close()
	if first.GroupID != group.ID || first.Revision != 2 || first.MultiplierPPM != "1250000" || first.Version == "" {
		t.Fatalf("saved=%+v", first)
	}
	replay := requestJSON(t, http.MethodPost, endpoint, valid, f.cookie, f.csrf, f.server.URL)
	var replayed accountGroupAllocationVersionView
	decodeResponse(t, replay, &replayed)
	replay.Body.Close()
	if replay.StatusCode != http.StatusOK || replayed.Version != first.Version || replayed.Revision != first.Revision {
		t.Fatalf("replay status=%d item=%+v", replay.StatusCode, replayed)
	}
	conflict := requestJSON(t, http.MethodPost, endpoint, strings.Replace(valid, `"1250000"`, `"1500000"`, 1), f.cookie, f.csrf, f.server.URL)
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("operation conflict status=%d body=%s", conflict.StatusCode, readBody(conflict))
	}
	assertAdminError(t, conflict, "operation_conflict", "Allocation operation conflicts with an existing operation.")
	conflict.Body.Close()

	rename := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/account-groups/"+group.ID, `{"expected_revision":2,"name":"Renamed allocation group"}`, f.cookie, f.csrf, f.server.URL)
	if rename.StatusCode != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", rename.StatusCode, readBody(rename))
	}
	decodeResponse(t, rename, &group)
	rename.Body.Close()
	if group.Revision != 3 || group.Allocation.Version != first.Version || group.Allocation.MultiplierPPM != "1250000" {
		t.Fatalf("rename changed allocation=%+v", group)
	}

	allocationBody := `{"operation_id":"d8fe4c89-cf2a-4c42-8578-610b9f53e511","expected_revision":3,"allocation_multiplier_ppm":"2000000"}`
	nameBody := `{"expected_revision":3,"name":"Concurrent name"}`
	statuses := make(chan int, 2)
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		response := requestJSON(t, http.MethodPost, endpoint, allocationBody, f.cookie, f.csrf, f.server.URL)
		statuses <- response.StatusCode
		response.Body.Close()
	}()
	go func() {
		defer workers.Done()
		response := requestJSON(t, http.MethodPut, f.server.URL+"/admin/api/v1/account-groups/"+group.ID, nameBody, f.cookie, f.csrf, f.server.URL)
		statuses <- response.StatusCode
		response.Body.Close()
	}()
	workers.Wait()
	close(statuses)
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("concurrent CAS statuses=%v", counts)
	}
	if err := f.app.store.db.QueryRow(`SELECT revision FROM account_groups WHERE id=?`, group.ID).Scan(&currentRevision); err != nil || currentRevision != 4 {
		t.Fatalf("concurrent revision=%d err=%v", currentRevision, err)
	}
	assertAccountPoolAuditMetadataOnly(t, f.app.store.db)
}

func TestAccountGroupAllocationDispatchFreezeCorrectionLegacyAndFailure(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`)
	}))
	defer upstream.Close()
	app, server, key, _ := newUsageHTTPChatFixture(t, upstream.URL)
	cookie, csrf := loginTestAdmin(t, server.URL)
	var accountID string
	if err := app.store.db.QueryRow(`SELECT upstream_id FROM models WHERE id='usage-chat'`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	catalog := accounting.NewPriceCatalog(app.store.db)
	price := &accounting.PriceSnapshot{Currency: "USD", InputPerMillionMicro: 1_000_000, OutputPerMillionMicro: 2_000_000, CacheReadPerMillionMicro: 0, CacheWritePerMillionMicro: 0}
	priceVersion, err := catalog.Save(context.Background(), accounting.PriceSave{AccountID: accountID, ActualModel: "provider-usage", OperationID: "ee7798fa-a5d2-41a0-93ce-a7e16ea89816", ExpectedRevision: 0, Price: price})
	if err != nil {
		t.Fatal(err)
	}
	request := func() *http.Response {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"usage-chat","messages":[]}`))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+key.Key)
		req.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	legacy := request()
	if legacy.StatusCode != http.StatusOK {
		t.Fatalf("legacy status=%d body=%s", legacy.StatusCode, readBody(legacy))
	}
	legacy.Body.Close()
	assertLatestAllocationAttempt(t, app, nil, nil, accounting.AllocationMultiplierScale, 200, 200, priceVersion.Version)

	groupResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/account-groups", `{"name":"Runtime allocation"}`, cookie, csrf, server.URL)
	var group accountGroupView
	decodeResponse(t, groupResponse, &group)
	groupResponse.Body.Close()
	allocationEndpoint := server.URL + "/admin/api/v1/account-groups/" + group.ID + "/allocation"
	allocationResponse := requestJSON(t, http.MethodPost, allocationEndpoint, `{"operation_id":"737410c3-0f6e-46d3-879a-b2766683be7d","expected_revision":1,"allocation_multiplier_ppm":"1250000"}`, cookie, csrf, server.URL)
	if allocationResponse.StatusCode != http.StatusOK {
		t.Fatalf("allocation status=%d body=%s", allocationResponse.StatusCode, readBody(allocationResponse))
	}
	var firstMultiplier accountGroupAllocationVersionView
	decodeResponse(t, allocationResponse, &firstMultiplier)
	allocationResponse.Body.Close()
	channelResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/channels", fmt.Sprintf(`{"name":"Runtime channel","group_id":%q}`, group.ID), cookie, csrf, server.URL)
	var channel accountChannelView
	decodeResponse(t, channelResponse, &channel)
	channelResponse.Body.Close()
	poolBody := fmt.Sprintf(`{"expected_revision":0,"items":[{"upstream_id":%q,"upstream_model":"provider-usage","wire_protocol":"legacy-native","priority":0,"weight":1,"max_concurrency":1,"channel_id":%q}]}`, accountID, channel.ID)
	poolResponse := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/models/usage-chat/accounts", poolBody, cookie, csrf, server.URL)
	if poolResponse.StatusCode != http.StatusOK {
		t.Fatalf("pool status=%d body=%s", poolResponse.StatusCode, readBody(poolResponse))
	}
	poolResponse.Body.Close()
	pooled := request()
	if pooled.StatusCode != http.StatusOK {
		t.Fatalf("pooled status=%d body=%s", pooled.StatusCode, readBody(pooled))
	}
	pooled.Body.Close()
	pooledAttempt := assertLatestAllocationAttempt(t, app, &group.ID, &firstMultiplier.Version, 1_250_000, 200, 250, priceVersion.Version)
	if _, err := app.store.db.Exec(`UPDATE accounting_attempt_allocation_snapshots SET multiplier_ppm=1250001 WHERE attempt_id=?`, pooledAttempt); err == nil {
		t.Fatal("frozen attempt allocation snapshot was mutable")
	}

	updatedResponse := requestJSON(t, http.MethodPost, allocationEndpoint, `{"operation_id":"bcb736f9-1c9e-4c7e-bfbf-662086b29358","expected_revision":2,"allocation_multiplier_ppm":"2000000"}`, cookie, csrf, server.URL)
	if updatedResponse.StatusCode != http.StatusOK {
		t.Fatalf("second allocation status=%d body=%s", updatedResponse.StatusCode, readBody(updatedResponse))
	}
	var secondMultiplier accountGroupAllocationVersionView
	decodeResponse(t, updatedResponse, &secondMultiplier)
	updatedResponse.Body.Close()
	var eventID, finishedAt string
	if err := app.store.db.QueryRow(`SELECT e.id,a.finished_at FROM accounting_attempts a JOIN accounting_usage_events e ON e.attempt_id=a.id WHERE a.id=?`, pooledAttempt).Scan(&eventID, &finishedAt); err != nil {
		t.Fatal(err)
	}
	finished, err := time.Parse(time.RFC3339Nano, finishedAt)
	if err != nil {
		t.Fatal(err)
	}
	correction := fmt.Sprintf(`{"id":"allocation-correction","attempt_id":%q,"target_event_id":%q,"operation_id":"allocation-correction-operation","reason":"admin_reconciliation","corrected_at":%q,"currency":"USD","output_tokens_delta":"50","estimated_cost_delta_micro":"100"}`, pooledAttempt, eventID, finished.Add(time.Second).Format(time.RFC3339Nano))
	status, body, _ := accountingV2Request(t, server.URL+"/admin/api/v1/billing/usage-corrections", cookie, []byte(correction), csrf, server.URL)
	if status != http.StatusOK {
		t.Fatalf("correction status=%d body=%s", status, body)
	}
	var corrected int64
	if err := app.store.db.QueryRow(`SELECT adjusted_cost_micro FROM accounting_usage_allocation_corrections WHERE attempt_id=?`, pooledAttempt).Scan(&corrected); err != nil || corrected != 375 {
		t.Fatalf("frozen correction adjusted=%d err=%v", corrected, err)
	}
	var frozenPPM int64
	var frozenVersion string
	if err := app.store.db.QueryRow(`SELECT multiplier_ppm,multiplier_version FROM accounting_attempt_allocation_snapshots WHERE attempt_id=?`, pooledAttempt).Scan(&frozenPPM, &frozenVersion); err != nil || frozenPPM != 1_250_000 || frozenVersion != firstMultiplier.Version {
		t.Fatalf("frozen snapshot ppm=%d version=%q err=%v", frozenPPM, frozenVersion, err)
	}

	latest := request()
	if latest.StatusCode != http.StatusOK {
		t.Fatalf("latest status=%d body=%s", latest.StatusCode, readBody(latest))
	}
	latest.Body.Close()
	assertLatestAllocationAttempt(t, app, &group.ID, &secondMultiplier.Version, 2_000_000, 200, 400, priceVersion.Version)
	reports, err := accounting.NewLedger(app.store.db).AccountingV2Report(context.Background(), accounting.AccountingV2Filters{From: time.Now().UTC().Add(-time.Hour), To: time.Now().UTC().Add(time.Hour)}, accounting.AccountingV2Day)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].KnownEstimatedCostMicro != 700 || reports[0].KnownAdjustedAllocationCostMicro != 975 || reports[0].UnknownAdjustedAllocationAttempts != 0 {
		t.Fatalf("allocation report=%+v", reports)
	}
	exportFrom := time.Now().UTC().Add(-time.Hour).Truncate(time.Second).Format(time.RFC3339)
	exportTo := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Format(time.RFC3339)
	status, export, _ := accountingV2Request(t, server.URL+"/admin/api/v1/usage/export?from="+exportFrom+"&to="+exportTo+"&limit=10", cookie, nil, "", "")
	if status != http.StatusOK || !strings.Contains(string(export), "adjusted_allocation_cost_micro") || !strings.Contains(string(export), firstMultiplier.Version) || !strings.Contains(string(export), secondMultiplier.Version) {
		t.Fatalf("allocation export status=%d body=%s", status, export)
	}
	if _, err := catalog.Save(context.Background(), accounting.PriceSave{AccountID: accountID, ActualModel: "provider-usage", OperationID: "19b9d49e-f0e4-477c-a21f-e268c18c9d12", ExpectedRevision: 1, Price: nil}); err != nil {
		t.Fatal(err)
	}
	unknown := request()
	if unknown.StatusCode != http.StatusOK {
		t.Fatalf("unknown-cost status=%d body=%s", unknown.StatusCode, readBody(unknown))
	}
	unknown.Body.Close()
	var unknownBase, unknownAdjusted sql.NullInt64
	if err := app.store.db.QueryRow(`SELECT a.cost_micro,e.adjusted_cost_micro FROM accounting_attempts a
		JOIN accounting_usage_events u ON u.attempt_id=a.id JOIN accounting_usage_allocation_events e ON e.event_id=u.id
		ORDER BY a.started_at DESC,a.id DESC LIMIT 1`).Scan(&unknownBase, &unknownAdjusted); err != nil {
		t.Fatal(err)
	}
	if unknownBase.Valid || unknownAdjusted.Valid {
		t.Fatalf("unknown cost became known base=%v adjusted=%v", unknownBase, unknownAdjusted)
	}
	zeroPrice := &accounting.PriceSnapshot{Currency: "USD"}
	zeroVersion, err := catalog.Save(context.Background(), accounting.PriceSave{AccountID: accountID, ActualModel: "provider-usage", OperationID: "5f582a27-16b0-4f3c-8e39-a88fa3e54b98", ExpectedRevision: 2, Price: zeroPrice})
	if err != nil {
		t.Fatal(err)
	}
	zero := request()
	if zero.StatusCode != http.StatusOK {
		t.Fatalf("known-zero status=%d body=%s", zero.StatusCode, readBody(zero))
	}
	zero.Body.Close()
	assertLatestAllocationAttempt(t, app, &group.ID, &secondMultiplier.Version, 2_000_000, 0, 0, zeroVersion.Version)

	if _, err := app.store.db.Exec(`CREATE TRIGGER reject_allocation_snapshot BEFORE INSERT ON accounting_attempt_allocation_snapshots BEGIN SELECT RAISE(ABORT,'synthetic allocation snapshot failure'); END`); err != nil {
		t.Fatal(err)
	}
	beforeCalls := calls.Load()
	failed := request()
	failedBody := readBody(failed)
	if failed.StatusCode != http.StatusServiceUnavailable || calls.Load() != beforeCalls {
		t.Fatalf("snapshot failure status=%d calls=%d/%d body=%s", failed.StatusCode, beforeCalls, calls.Load(), failedBody)
	}
	var attempts int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil || attempts != 5 {
		t.Fatalf("snapshot failure attempts=%d err=%v", attempts, err)
	}
}

func assertLatestAllocationAttempt(t *testing.T, app *App, groupID, version *string, ppm, expectedBase, adjusted int64, priceVersion string) string {
	t.Helper()
	var attemptID string
	var storedGroup, storedVersion, storedPrice *string
	var storedPPM, baseCost int64
	var storedAdjusted *int64
	err := app.store.db.QueryRow(`SELECT a.id,s.account_group_id,s.multiplier_version,s.multiplier_ppm,a.cost_micro,e.adjusted_cost_micro,a.price_version
		FROM accounting_attempts a JOIN accounting_attempt_allocation_snapshots s ON s.attempt_id=a.id
		JOIN accounting_usage_events u ON u.attempt_id=a.id JOIN accounting_usage_allocation_events e ON e.event_id=u.id
		ORDER BY a.started_at DESC,a.id DESC LIMIT 1`).Scan(&attemptID, &storedGroup, &storedVersion, &storedPPM, &baseCost, &storedAdjusted, &storedPrice)
	if err != nil {
		t.Fatal(err)
	}
	if !sameTestStringPointer(storedGroup, groupID) || !sameTestStringPointer(storedVersion, version) || storedPPM != ppm || storedAdjusted == nil || *storedAdjusted != adjusted || baseCost != expectedBase || storedPrice == nil || *storedPrice != priceVersion {
		t.Fatalf("attempt=%s group=%v version=%v ppm=%d base=%d adjusted=%v price=%v", attemptID, storedGroup, storedVersion, storedPPM, baseCost, storedAdjusted, storedPrice)
	}
	return attemptID
}

func sameTestStringPointer(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
