// Independently authored acceptance tests for the local administrator audit contract.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type adminAuditTestPage struct {
	From       string                `json:"from"`
	To         string                `json:"to"`
	SnapshotAt string                `json:"snapshot_at"`
	Sources    []string              `json:"sources"`
	Items      []adminAuditEventView `json:"items"`
	NextCursor *string               `json:"next_cursor"`
}

func TestAdminAuditFourSourceOrderingFilteringAndWatermarkPagination(t *testing.T) {
	fixture := newRuntimeAccountPoolFixture(t)
	fixture.enableRuntimeAdminHTTP(t)
	adminID := adminAuditTestAdminID(t, fixture.app.store.db)
	seedAdminAuditFourSources(t, fixture.app.store.db, adminID)

	endpoint := fixture.server.URL + adminAuditPath + "?from=2026-09-29T09%3A59%3A59Z&to=2026-09-29T10%3A00%3A01Z&limit=2"
	unauthorized := requestJSON(t, http.MethodGet, endpoint, "", nil, "", "")
	assertAdminAuditError(t, unauthorized, http.StatusUnauthorized, "authentication_required")
	wrongOrigin := requestJSON(t, http.MethodGet, endpoint, "", fixture.cookie, "", "http://wrong-origin.invalid")
	assertAdminAuditError(t, wrongOrigin, http.StatusForbidden, "origin_rejected")

	firstResponse := requestJSON(t, http.MethodGet, endpoint, "", fixture.cookie, "", fixture.server.URL)
	if firstResponse.StatusCode != http.StatusOK {
		t.Fatalf("first page status=%d body=%s", firstResponse.StatusCode, readBody(firstResponse))
	}
	var first adminAuditTestPage
	decodeResponse(t, firstResponse, &first)
	firstResponse.Body.Close()
	if len(first.Items) != 2 || first.Items[0].Source != "account_pool" || first.Items[1].Source != "account_lifecycle" || first.NextCursor == nil {
		t.Fatalf("first page=%+v", first)
	}
	if first.Items[0].OccurredAt != "2026-09-29T10:00:00.1Z" || first.Items[1].OccurredAt != "2026-09-29T10:00:00.1Z" {
		t.Fatalf("mixed precision was not canonicalized: %+v", first.Items)
	}
	if first.Items[0].Revision != nil || first.Items[1].Revision != nil || first.SnapshotAt == "" {
		t.Fatalf("first metadata=%+v", first)
	}

	if _, err := fixture.app.store.db.Exec(`INSERT INTO account_pool_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES('aud_after_watermark',?,'account_group.update','account_group','newer-after-first-page','succeeded','2026-09-29T10:00:00.050Z')`, adminID); err != nil {
		t.Fatal(err)
	}
	secondResponse := requestJSON(t, http.MethodGet, fixture.server.URL+adminAuditPath+"?cursor="+url.QueryEscape(*first.NextCursor), "", fixture.cookie, "", "")
	if secondResponse.StatusCode != http.StatusOK {
		t.Fatalf("second page status=%d body=%s", secondResponse.StatusCode, readBody(secondResponse))
	}
	var second adminAuditTestPage
	decodeResponse(t, secondResponse, &second)
	secondResponse.Body.Close()
	if len(second.Items) != 2 || second.Items[0].Source != "governance_management" || second.Items[1].Source != "governance_general_budget" || second.NextCursor != nil {
		t.Fatalf("second page=%+v", second)
	}
	if second.Items[0].Revision == nil || *second.Items[0].Revision != 7 || second.Items[1].Revision == nil || *second.Items[1].Revision != 9 {
		t.Fatalf("governance revisions=%+v", second.Items)
	}
	if second.From != first.From || second.To != first.To || second.SnapshotAt != first.SnapshotAt {
		t.Fatalf("cursor did not preserve window: first=%+v second=%+v", first, second)
	}
	for _, item := range append(first.Items, second.Items...) {
		if item.EventID == "aud_after_watermark" {
			t.Fatal("post-watermark row entered cursor chain")
		}
	}

	filter := fixture.server.URL + adminAuditPath + "?from=2026-09-29T09%3A59%3A59Z&to=2026-09-29T10%3A00%3A01Z&sources=governance_general_budget&actor_id=" + url.QueryEscape(adminID) + "&action=budget.update&target_type=budget&target_id=budget-policy&result=succeeded"
	filteredResponse := requestJSON(t, http.MethodGet, filter, "", fixture.cookie, "", "")
	if filteredResponse.StatusCode != http.StatusOK {
		t.Fatalf("filtered status=%d body=%s", filteredResponse.StatusCode, readBody(filteredResponse))
	}
	var filtered adminAuditTestPage
	decodeResponse(t, filteredResponse, &filtered)
	filteredResponse.Body.Close()
	if len(filtered.Items) != 1 || filtered.Items[0].Source != "governance_general_budget" || len(filtered.Sources) != 1 || filtered.Sources[0] != "governance_general_budget" {
		t.Fatalf("filtered page=%+v", filtered)
	}

	tampered := []byte(*first.NextCursor)
	if tampered[len(tampered)-1] == 'A' {
		tampered[len(tampered)-1] = 'B'
	} else {
		tampered[len(tampered)-1] = 'A'
	}
	assertAdminAuditError(t, requestJSON(t, http.MethodGet, fixture.server.URL+adminAuditPath+"?cursor="+url.QueryEscape(string(tampered)), "", fixture.cookie, "", ""), http.StatusBadRequest, "invalid_request")
	assertAdminAuditError(t, requestJSON(t, http.MethodGet, fixture.server.URL+adminAuditPath+"?cursor="+url.QueryEscape(*first.NextCursor)+"&limit=2", "", fixture.cookie, "", ""), http.StatusBadRequest, "invalid_request")
}

func TestAdminAuditRejectsInvalidQueriesAndFailsClosedForEverySource(t *testing.T) {
	tests := []string{
		"?unknown=value", "?limit=0", "?limit=01", "?limit=101", "?sources=account_pool,account_pool", "?sources=unknown",
		"?from=2026-09-29T00%3A00%3A00Z", "?from=2026-08-01T00%3A00%3A00Z&to=2026-09-29T00%3A00%3A00Z",
		"?from=2026-09-29T10%3A00%3A00Z&to=2026-09-29T09%3A00%3A00Z", "?actor_id=%20admin", "?action=line%0Abreak", "?result=",
		"?sources=account_pool&sources=account_lifecycle", "?limit=2&&action=x", "?actor_id=" + url.QueryEscape(strings.Repeat("x", 257)), "?actor_id=%zz",
	}
	fixture := newRuntimeAccountPoolFixture(t)
	fixture.enableRuntimeAdminHTTP(t)
	for _, suffix := range tests {
		response := requestJSON(t, http.MethodGet, fixture.server.URL+adminAuditPath+suffix, "", fixture.cookie, "", "")
		assertAdminAuditError(t, response, http.StatusBadRequest, "invalid_request")
	}
	injection := requestJSON(t, http.MethodGet, fixture.server.URL+adminAuditPath+"?action="+url.QueryEscape("x' OR 1=1 --"), "", fixture.cookie, "", "")
	if injection.StatusCode != http.StatusOK {
		t.Fatalf("exact SQL-like filter status=%d body=%s", injection.StatusCode, readBody(injection))
	}
	var empty adminAuditTestPage
	decodeResponse(t, injection, &empty)
	injection.Body.Close()
	if len(empty.Items) != 0 {
		t.Fatalf("SQL-like filter was not exact: %+v", empty.Items)
	}

	for _, table := range []string{"account_pool_audit", "account_lifecycle_audit", "governance_management_audit", "governance_general_budget_audit"} {
		t.Run(table, func(t *testing.T) {
			broken := newRuntimeAccountPoolFixture(t)
			broken.enableRuntimeAdminHTTP(t)
			if _, err := broken.app.store.db.Exec(`DROP TABLE ` + table); err != nil {
				t.Fatal(err)
			}
			response := requestJSON(t, http.MethodGet, broken.server.URL+adminAuditPath+"?sources=account_pool", "", broken.cookie, "", "")
			assertAdminAuditError(t, response, http.StatusServiceUnavailable, "storage_unavailable")
		})
	}
	viewFixture := newRuntimeAccountPoolFixture(t)
	viewFixture.enableRuntimeAdminHTTP(t)
	if _, err := viewFixture.app.store.db.Exec(`DROP TABLE account_lifecycle_audit`); err != nil {
		t.Fatal(err)
	}
	if _, err := viewFixture.app.store.db.Exec(`CREATE VIEW account_lifecycle_audit AS SELECT 'id' id,'actor' actor_id,'action' action,'target' target_type,'id' target_id,'succeeded' result,'2026-09-29T00:00:00Z' occurred_at`); err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, http.MethodGet, viewFixture.server.URL+adminAuditPath, "", viewFixture.cookie, "", "")
	assertAdminAuditError(t, response, http.StatusServiceUnavailable, "storage_unavailable")

	invalidResult := newRuntimeAccountPoolFixture(t)
	invalidResult.enableRuntimeAdminHTTP(t)
	adminID := adminAuditTestAdminID(t, invalidResult.app.store.db)
	if _, err := invalidResult.app.store.db.Exec(`INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES('aud_invalid_result',?,'model.update','model','model-invalid','failed',?)`, adminID, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	response = requestJSON(t, http.MethodGet, invalidResult.server.URL+adminAuditPath, "", invalidResult.cookie, "", "")
	assertAdminAuditError(t, response, http.StatusServiceUnavailable, "storage_unavailable")
}

func TestAdminAuditCancelledQueryAndCursorValidation(t *testing.T) {
	fixture := newRuntimeAccountPoolFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	query := adminAuditQuery{From: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), Sources: allAdminAuditSourceTokens(), Limit: 10}
	if _, err := fixture.app.queryAdminAudit(ctx, query, nil); err == nil {
		t.Fatal("cancelled audit query succeeded")
	}
	invalid := adminAuditCursor{Version: adminAuditCursorVersion, From: query.From.Format(time.RFC3339Nano), To: query.To.Format(time.RFC3339Nano), SnapshotAt: query.To.Format(time.RFC3339Nano), Sources: allAdminAuditSourceTokens(), Limit: 10, Watermarks: []int64{0, 0, 0, -1}, LastTime: adminAuditTimeKey(query.From), LastSource: "account_pool", LastID: "aud"}
	encoded, err := fixture.app.encodeAdminAuditCursor(invalid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.decodeAdminAuditCursor(encoded); err == nil {
		t.Fatal("negative signed cursor watermark was accepted")
	}
	base := adminAuditCursor{Version: adminAuditCursorVersion, From: query.From.Format(time.RFC3339Nano), To: query.To.Format(time.RFC3339Nano), SnapshotAt: query.To.Format(time.RFC3339Nano), Sources: allAdminAuditSourceTokens(), Limit: 10, Watermarks: []int64{0, 0, 0, 0}, LastTime: adminAuditTimeKey(query.From), LastSource: "account_pool", LastID: "aud"}
	mutations := map[string]func(*adminAuditCursor){
		"old version":    func(cursor *adminAuditCursor) { cursor.Version = 0 },
		"unknown source": func(cursor *adminAuditCursor) { cursor.Sources[0] = "unknown" },
		"noncanonical sources": func(cursor *adminAuditCursor) {
			cursor.Sources[0], cursor.Sources[1] = cursor.Sources[1], cursor.Sources[0]
		},
		"short watermarks":    func(cursor *adminAuditCursor) { cursor.Watermarks = cursor.Watermarks[:3] },
		"unknown last source": func(cursor *adminAuditCursor) { cursor.LastSource = "unknown" },
		"malformed last time": func(cursor *adminAuditCursor) { cursor.LastTime = "2026-09-29T00:00:00Z" },
		"noncanonical snapshot": func(cursor *adminAuditCursor) {
			cursor.SnapshotAt = "2026-09-30T08:00:00+08:00"
		},
		"invalid last id": func(cursor *adminAuditCursor) { cursor.LastID = " bad" },
		"zero limit":      func(cursor *adminAuditCursor) { cursor.Limit = 0 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			candidate := base
			candidate.Sources = append([]string(nil), base.Sources...)
			candidate.Watermarks = append([]int64(nil), base.Watermarks...)
			mutate(&candidate)
			value, err := fixture.app.encodeAdminAuditCursor(candidate)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fixture.app.decodeAdminAuditCursor(value); err == nil {
				t.Fatalf("accepted signed invalid cursor: %+v", candidate)
			}
		})
	}
	fixture.enableRuntimeAdminHTTP(t)
	assertAdminAuditError(t, requestJSON(t, http.MethodGet, fixture.server.URL+adminAuditPath+"?cursor="+strings.Repeat("a", adminAuditCursorLimit+1), "", fixture.cookie, "", ""), http.StatusBadRequest, "invalid_request")
	valid, err := fixture.app.encodeAdminAuditCursor(base)
	if err != nil {
		t.Fatal(err)
	}
	assertAdminAuditError(t, requestJSON(t, http.MethodGet, fixture.server.URL+adminAuditPath+"?cursor="+url.QueryEscape(valid[:len(valid)-1]), "", fixture.cookie, "", ""), http.StatusBadRequest, "invalid_request")
}

func TestAdminAuditSameTimestampUsesSourceThenEventIDKeyset(t *testing.T) {
	fixture := newRuntimeAccountPoolFixture(t)
	fixture.enableRuntimeAdminHTTP(t)
	adminID := adminAuditTestAdminID(t, fixture.app.store.db)
	for _, row := range []struct{ id, stamp string }{{"aud_a", "2026-09-29T10:00:00.100Z"}, {"aud_b", "2026-09-29T10:00:00.1Z"}} {
		if _, err := fixture.app.store.db.Exec(`INSERT INTO account_pool_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,'account_group.update','account_group',?,'succeeded',?)`, row.id, adminID, row.id, row.stamp); err != nil {
			t.Fatal(err)
		}
	}
	endpoint := fixture.server.URL + adminAuditPath + "?from=2026-09-29T09%3A59%3A59Z&to=2026-09-29T10%3A00%3A01Z&sources=account_pool&limit=1"
	firstResponse := requestJSON(t, http.MethodGet, endpoint, "", fixture.cookie, "", "")
	var first adminAuditTestPage
	decodeResponse(t, firstResponse, &first)
	firstResponse.Body.Close()
	if len(first.Items) != 1 || first.Items[0].EventID != "aud_b" || first.NextCursor == nil {
		t.Fatalf("first page=%+v", first)
	}
	secondResponse := requestJSON(t, http.MethodGet, fixture.server.URL+adminAuditPath+"?cursor="+url.QueryEscape(*first.NextCursor), "", fixture.cookie, "", "")
	var second adminAuditTestPage
	decodeResponse(t, secondResponse, &second)
	secondResponse.Body.Close()
	if len(second.Items) != 1 || second.Items[0].EventID != "aud_a" || second.NextCursor != nil {
		t.Fatalf("second page=%+v", second)
	}
}

func seedAdminAuditFourSources(t *testing.T, db *sql.DB, adminID string) {
	t.Helper()
	digest := make([]byte, 32)
	for index := range digest {
		digest[index] = byte(index + 1)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO account_pool_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES('aud_pool',?,'account_group.update','account_group','pool-target','succeeded','2026-09-29T10:00:00.1Z')`, []any{adminID}},
		{`INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES('aud_lifecycle',?,'model.update','model','lifecycle-target','succeeded','2026-09-29T10:00:00.100Z')`, []any{adminID}},
		{`INSERT INTO governance_management_operations(operation_id,actor_id,action,payload_digest,resource_kind,resource_id,revision,created_at) VALUES('11111111-1111-4111-8111-111111111111',?,'group.update',?,'group','governance-target',7,'2026-09-29T10:00:00.01Z')`, []any{adminID, digest}},
		{`INSERT INTO governance_management_audit(operation_id,actor_id,action,resource_kind,resource_id,revision,created_at) VALUES('11111111-1111-4111-8111-111111111111',?,'group.update','group','governance-target',7,'2026-09-29T10:00:00.01Z')`, []any{adminID}},
		{`INSERT INTO governance_general_budget_operations(operation_id,actor_id,action,payload_digest,policy_id,revision,created_at) VALUES('22222222-2222-4222-8222-222222222222',?,'budget.update',?,'budget-policy',9,'2026-09-29T10:00:00Z')`, []any{adminID, digest}},
		{`INSERT INTO governance_general_budget_audit(operation_id,actor_id,action,policy_id,revision,created_at) VALUES('22222222-2222-4222-8222-222222222222',?,'budget.update','budget-policy',9,'2026-09-29T10:00:00Z')`, []any{adminID}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatalf("seed audit fact: %v", err)
		}
	}
}

func adminAuditTestAdminID(t *testing.T, db *sql.DB) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM admins WHERE username='admin'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertAdminAuditError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	body := readBody(response)
	response.Body.Close()
	if response.StatusCode != status {
		t.Fatalf("status=%d want=%d body=%s", response.StatusCode, status, body)
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil || payload.Error.Code != code {
		t.Fatalf("error body=%s code=%q err=%v", body, payload.Error.Code, err)
	}
}

func TestAdminAuditResponseContainsMetadataOnly(t *testing.T) {
	fixture := newRuntimeAccountPoolFixture(t)
	fixture.enableRuntimeAdminHTTP(t)
	adminID := adminAuditTestAdminID(t, fixture.app.store.db)
	seedAdminAuditFourSources(t, fixture.app.store.db, adminID)
	response := requestJSON(t, http.MethodGet, fixture.server.URL+adminAuditPath+"?from=2026-09-29T09%3A59%3A59Z&to=2026-09-29T10%3A00%3A01Z", "", fixture.cookie, "", "")
	body := readBody(response)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	for _, forbidden := range []string{"payload_digest", "csrf_token", "token_digest", "credential_ciphertext", "Authorization", "prompt", "response_body", "01020304"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("response contains forbidden value %q: %s", forbidden, body)
		}
	}
}
