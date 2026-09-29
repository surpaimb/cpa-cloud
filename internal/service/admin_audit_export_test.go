// Independently authored acceptance tests for docs/admin-audit-export-contract.md.
package service

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

const adminAuditExportTestWindow = "?from=2026-09-29T09%3A59%3A59Z&to=2026-09-29T10%3A00%3A01Z"

func readAdminAuditCSV(t *testing.T, response *http.Response) (string, [][]string) {
	t.Helper()
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("CSV status=%d body=%s", response.StatusCode, readBody(response))
	}
	if got := response.Header.Get("Content-Type"); got != "text/csv; charset=utf-8" {
		t.Fatalf("content type=%q", got)
	}
	if got := response.Header.Get("Content-Disposition"); got != adminAuditExportFilename {
		t.Fatalf("disposition=%q", got)
	}
	if got := response.Header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control=%q", got)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Header.Get("Content-Length") != strconv.Itoa(len(data)) || len(data) > adminAuditExportMaxBytes || !strings.Contains(string(data), "\r\n") {
		t.Fatalf("CSV framing length=%d headers=%v", len(data), response.Header)
	}
	records, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return string(data), records
}

func assertAdminAuditExportError(t *testing.T, response *http.Response, status int, code string) {
	t.Helper()
	if response.Header.Get("Content-Disposition") != "" || strings.HasPrefix(response.Header.Get("Content-Type"), "text/csv") || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("partial CSV headers on error: %v", response.Header)
	}
	assertAdminAuditError(t, response, status, code)
}

func TestAdminAuditExportFiveSourcesOrderingFilteringAndMetadataOnly(t *testing.T) {
	fixture := newRuntimeAccountPoolFixture(t)
	fixture.enableRuntimeAdminHTTP(t)
	adminID := adminAuditTestAdminID(t, fixture.app.store.db)
	seedAdminAuditFourSources(t, fixture.app.store.db, adminID)
	seedFinancialAuditFact(t, fixture.app.store.db, "financial-export", "", "2026-09-29T10:00:00.100Z", "redemption.redeem", "redemption", "redeem-export")
	endpoint := fixture.server.URL + adminAuditExportPath + adminAuditExportTestWindow
	assertAdminAuditExportError(t, requestJSON(t, http.MethodGet, endpoint, "", nil, "", ""), http.StatusUnauthorized, "authentication_required")
	assertAdminAuditExportError(t, requestJSON(t, http.MethodGet, endpoint, "", fixture.cookie, "", "http://wrong.invalid"), http.StatusForbidden, "origin_rejected")

	response := requestJSON(t, http.MethodGet, endpoint, "", fixture.cookie, "", fixture.server.URL)
	body, records := readAdminAuditCSV(t, response)
	if len(records) != 6 || strings.Join(records[0], ",") != strings.Join(adminAuditCSVHeader, ",") {
		t.Fatalf("CSV records=%v", records)
	}
	for index, source := range []string{"account_pool", "account_lifecycle", "financial_commercial", "governance_management", "governance_general_budget"} {
		if records[index+1][0] != source || len(records[index+1]) != len(adminAuditCSVHeader) {
			t.Fatalf("row %d=%v", index, records[index+1])
		}
	}
	if records[1][7] != "" || records[3][2] != "" || records[3][7] != "1" || records[3][8] != "2026-09-29T10:00:00.1Z" {
		t.Fatalf("nullable/revision/time projection=%v", records)
	}
	for _, forbidden := range []string{"payload_digest", "csrf_token", "credential_ciphertext", "amount_micro", "prompt", "response_body"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("CSV contains %q", forbidden)
		}
	}

	filter := endpoint + "&sources=account_pool,financial_commercial&action=redemption.redeem&target_type=redemption&target_id=redeem-export&result=succeeded"
	_, selected := readAdminAuditCSV(t, requestJSON(t, http.MethodGet, filter, "", fixture.cookie, "", ""))
	if len(selected) != 2 || selected[1][0] != "financial_commercial" || selected[1][2] != "" {
		t.Fatalf("filtered CSV=%v", selected)
	}
	_, empty := readAdminAuditCSV(t, requestJSON(t, http.MethodGet, endpoint+"&sources=account_pool&action=not-found", "", fixture.cookie, "", ""))
	if len(empty) != 1 {
		t.Fatalf("empty CSV=%v", empty)
	}
}

func TestAdminAuditExportRejectsParametersAndFailsClosed(t *testing.T) {
	fixture := newRuntimeAccountPoolFixture(t)
	fixture.enableRuntimeAdminHTTP(t)
	for _, suffix := range []string{
		"?cursor=x", "?limit=1", "?unknown=x", "?sources=account_pool,account_pool", "?sources=unknown", "?sources=account_pool&sources=financial_commercial",
		"?from=2026-09-29T10%3A00%3A00Z", "?from=2026-08-01T00%3A00%3A00Z&to=2026-09-29T00%3A00%3A00Z", "?actor_id=%20admin", "?action=line%0Abreak", "?target_id=%zz", "?action=x&&result=succeeded",
	} {
		assertAdminAuditExportError(t, requestJSON(t, http.MethodGet, fixture.server.URL+adminAuditExportPath+suffix, "", fixture.cookie, "", ""), http.StatusBadRequest, "invalid_request")
	}
	for _, statement := range []string{
		`DROP TABLE financial_commercial_operations`,
		`DROP TRIGGER financial_commercial_operations_no_update`,
		`INSERT INTO financial_commercial_operations(operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at) VALUES('bad-export','settings.update',NULL,zeroblob(32),'settings','',1,'2026-09-29T10:00:00Z')`,
	} {
		t.Run(statement[:strings.IndexByte(statement, ' ')], func(t *testing.T) {
			broken := newRuntimeAccountPoolFixture(t)
			broken.enableRuntimeAdminHTTP(t)
			if _, err := broken.app.store.db.Exec(statement); err != nil {
				t.Fatal(err)
			}
			sources := "financial_commercial"
			if strings.HasPrefix(statement, "DROP") {
				sources = "account_pool"
			}
			endpoint := broken.server.URL + adminAuditExportPath + adminAuditExportTestWindow + "&sources=" + sources
			assertAdminAuditExportError(t, requestJSON(t, http.MethodGet, endpoint, "", broken.cookie, "", ""), http.StatusServiceUnavailable, "storage_unavailable")
		})
	}
}

func TestAdminAuditExportRowsAndBytesAreBounded(t *testing.T) {
	fixture := newRuntimeAccountPoolFixture(t)
	fixture.enableRuntimeAdminHTTP(t)
	adminID := adminAuditTestAdminID(t, fixture.app.store.db)
	tx, err := fixture.app.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO account_pool_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,'account_group.update','account_group',?,'succeeded','2026-09-29T10:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	for index := range adminAuditExportMaxRows {
		id := fmt.Sprintf("aud_export_%04d", index)
		if _, err := statement.Exec(id, adminID, id); err != nil {
			t.Fatal(err)
		}
	}
	statement.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	endpoint := fixture.server.URL + adminAuditExportPath + adminAuditExportTestWindow + "&sources=account_pool"
	_, records := readAdminAuditCSV(t, requestJSON(t, http.MethodGet, endpoint, "", fixture.cookie, "", ""))
	if len(records) != adminAuditExportMaxRows+1 {
		t.Fatalf("rows=%d", len(records)-1)
	}
	if _, err := fixture.app.store.db.Exec(`INSERT INTO account_pool_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES('aud_export_overflow',?,'account_group.update','account_group','overflow','succeeded','2026-09-29T10:00:00Z')`, adminID); err != nil {
		t.Fatal(err)
	}
	assertAdminAuditExportError(t, requestJSON(t, http.MethodGet, endpoint, "", fixture.cookie, "", ""), http.StatusRequestEntityTooLarge, "export_too_large")

	long := strings.Repeat(`"`, 256)
	items := make([]adminAuditEventView, adminAuditExportMaxRows)
	for index := range items {
		items[index] = adminAuditEventView{Source: "account_pool", EventID: long, ActorID: &long, Action: long, TargetType: long, TargetID: long, Result: "succeeded", OccurredAt: "2026-09-29T10:00:00Z"}
	}
	if _, err := encodeAdminAuditCSV(context.Background(), items); !errors.Is(err, errAdminAuditExportTooLarge) {
		t.Fatalf("byte bound err=%v", err)
	}

	large := newRuntimeAccountPoolFixture(t)
	large.enableRuntimeAdminHTTP(t)
	largeAdmin := adminAuditTestAdminID(t, large.app.store.db)
	largeTx, err := large.app.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	largeStatement, err := largeTx.Prepare(`INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,?,?,?,'succeeded','2026-09-29T10:00:00Z')`)
	if err != nil {
		t.Fatal(err)
	}
	for index := range adminAuditExportMaxRows {
		id := strings.Repeat(`"`, 250) + fmt.Sprintf("%06d", index)
		if _, err := largeStatement.Exec(id, largeAdmin, long, long, long); err != nil {
			t.Fatal(err)
		}
	}
	largeStatement.Close()
	if err := largeTx.Commit(); err != nil {
		t.Fatal(err)
	}
	largeURL := large.server.URL + adminAuditExportPath + adminAuditExportTestWindow + "&sources=account_lifecycle"
	assertAdminAuditExportError(t, requestJSON(t, http.MethodGet, largeURL, "", large.cookie, "", ""), http.StatusRequestEntityTooLarge, "export_too_large")
}

func TestAdminAuditExportCSVFormulaSafetyAndCancellation(t *testing.T) {
	actor := "@LOOKUP()"
	item := adminAuditEventView{Source: "account_pool", EventID: "=1+1", ActorID: &actor, Action: "+cmd", TargetType: "-value", TargetID: `@x,"quoted"`, Result: "succeeded", OccurredAt: "2026-09-29T10:00:00Z"}
	payload, err := encodeAdminAuditCSV(context.Background(), []adminAuditEventView{item})
	if err != nil {
		t.Fatal(err)
	}
	records, err := csv.NewReader(strings.NewReader(string(payload))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for index, want := range map[int]string{1: "'=1+1", 2: "'@LOOKUP()", 3: "'+cmd", 4: "'-value", 5: `'@x,"quoted"`} {
		if records[1][index] != want {
			t.Fatalf("column %d=%q want %q", index, records[1][index], want)
		}
	}
	for _, value := range []string{"=\n1", "x\r1", "\u202e=1", "\ufeff=1", string([]byte{0xff})} {
		if _, err := safeAdminAuditCSVCell(value); err == nil {
			t.Fatalf("accepted unsafe CSV cell %q", value)
		}
	}
	emptyActor := ""
	item.ActorID = &emptyActor
	if _, err := encodeAdminAuditCSV(context.Background(), []adminAuditEventView{item}); err == nil {
		t.Fatal("empty linked actor became an unlinked CSV cell")
	}
	fixture := newRuntimeAccountPoolFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	query := adminAuditQuery{From: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), Sources: allAdminAuditSourceTokens(), Limit: adminAuditExportMaxRows}
	if _, err := fixture.app.queryAdminAuditExport(ctx, query); err == nil {
		t.Fatal("cancelled export succeeded")
	}
}

func TestAdminAuditExportWatermarkExcludesLaterWrite(t *testing.T) {
	fixture := newRuntimeAccountPoolFixture(t)
	adminID := adminAuditTestAdminID(t, fixture.app.store.db)
	seedAdminAuditFourSources(t, fixture.app.store.db, adminID)
	ctx := context.Background()
	tx, err := fixture.app.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateAdminAuditSources(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var watermark int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid),0) FROM account_pool_audit`).Scan(&watermark); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.store.db.Exec(`INSERT INTO account_pool_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES('aud_export_later',?,'account_group.update','account_group','later','succeeded','2026-09-29T10:00:00Z')`, adminID); err != nil {
		t.Fatal(err)
	}
	readTx, err := fixture.app.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	query := adminAuditQuery{From: time.Date(2026, 9, 29, 9, 59, 59, 0, time.UTC), To: time.Date(2026, 9, 29, 10, 0, 1, 0, time.UTC), Sources: []string{"account_pool"}, Limit: adminAuditExportMaxRows}
	items, err := queryAdminAuditSource(ctx, readTx, adminAuditSources[0], watermark, query, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].EventID != "aud_pool" {
		t.Fatalf("post-watermark write entered export snapshot: %+v", items)
	}
}
