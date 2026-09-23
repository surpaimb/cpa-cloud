package service

// Synthetic integration cases for CPA Cloud's independent probe ledger.
import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

func TestSystemProbeAccountingAdminIsolationAndRestart(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	f.insertUpstream(t, "probe_account", "openai-compatible")
	ctx := context.Background()
	start := accounting.SystemProbeStart{
		OperationID: "f1e905eb-e1da-4a45-97fe-7b310e024233", RecoveryEventID: "cool_probe_test",
		AccountID: "probe_account", AccountRevision: 1, PoolRevision: 1,
		PublicModel: "employee_model", UpstreamModel: "actual-model", Provider: accounting.ProviderOpenAICompatible,
		// Simulate a wall-clock rollback across restart; the stored timeline
		// must remain valid without making the uncertain request executable.
		Protocol: accounting.ProtocolOpenAIChatCompletions, StartedAt: time.Now().UTC().Add(time.Hour),
	}
	tx, err := f.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.systemProbes.BeginTx(ctx, tx, start); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := f.app.systemProbes.MarkMayHaveSentTx(ctx, tx, start.OperationID, start.StartedAt.Add(time.Second)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Merely restarting accounting is the same metadata-only recovery step
	// performed by Open. There is no executable callback to replay this ID.
	if err := f.app.initializeSystemProbeAccounting(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.app.initializeSystemProbeAccounting(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := f.app.store.db.QueryRow(`SELECT status FROM system_probe_attempts WHERE operation_id=?`, start.OperationID).Scan(&status); err != nil || status != "interrupted" {
		t.Fatalf("restart did not interrupt the uncertain attempt: %q %v", status, err)
	}
	for _, table := range []string{"employees", "access_keys", "model_requests", "accounting_requests", "accounting_attempts"} {
		var n int
		if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("probe polluted %s: %d %v", table, n, err)
		}
	}
	statusCode, body := usageAdminGET(t, f, "/admin/api/v1/system-probes/summary", true)
	if statusCode != http.StatusOK {
		t.Fatalf("summary: %d %s", statusCode, body)
	}
	var summary struct {
		Scope      string                `json:"scope"`
		Currencies []usageAttemptSummary `json:"currencies"`
	}
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Scope != "system_probes" || len(summary.Currencies) != 1 || summary.Currencies[0].Interrupted != "1" || summary.Currencies[0].UnknownCostAttempts != "1" {
		t.Fatalf("unexpected isolated summary: %s", body)
	}
	if summary.Currencies[0].InputTokens.UnknownAttempts != "1" {
		t.Fatalf("unknown usage treated as zero: %s", body)
	}
	for _, forbidden := range []string{start.OperationID, start.AccountID, "credential", "Bearer", "actual-model"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("summary exposed non-aggregate metadata: %s", forbidden)
		}
	}
	if code, _ := usageAdminGET(t, f, "/admin/api/v1/system-probes/summary", false); code != http.StatusUnauthorized {
		t.Fatalf("summary accepted unauthenticated request: %d", code)
	}
	if code, _ := usageAdminGET(t, f, "/admin/api/v1/system-probes/summary?employee_id=x", true); code != http.StatusBadRequest {
		t.Fatalf("summary accepted employee filter: %d", code)
	}
	// A malformed storage backend is represented by one fixed public error.
	saved := f.app.systemProbes
	f.app.systemProbes = nil
	w := httptest.NewRecorder()
	f.app.systemProbeSummary(w, httptest.NewRequest(http.MethodGet, "/admin/api/v1/system-probes/summary", nil), adminSession{})
	f.app.systemProbes = saved
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "storage_unavailable") {
		t.Fatalf("storage failure: %d", w.Code)
	}
}

func TestSystemProbeAccountingCallerTransactionRollback(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.close()
	ctx := context.Background()
	if err := accounting.NewPriceCatalog(s.db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{store: s}
	if err := a.initializeSystemProbeAccounting(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = s.db.Exec(`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES('probe','Synthetic','openai-compatible','https://example.invalid/v1',1,X'00',1,1,?)`, utcNow())
	if err != nil {
		t.Fatal(err)
	}
	start := accounting.SystemProbeStart{OperationID: "dbfa4c6d-0885-4483-b7fd-1a31e2fbd20b", RecoveryEventID: "cool_probe", AccountID: "probe", AccountRevision: 1, PoolRevision: 1, PublicModel: "public", UpstreamModel: "actual", Provider: accounting.ProviderOpenAICompatible, Protocol: accounting.ProtocolOpenAIChatCompletions, StartedAt: time.Now().UTC()}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.systemProbes.BeginTx(ctx, tx, start); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM system_probe_attempts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("Begin escaped caller transaction: %d %v", n, err)
	}
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.systemProbes.BeginTx(ctx, tx, start); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// Force a failure after the dispatch marker write, as a maintenance lease
	// CAS failure would do. Neither half is allowed to escape the rollback.
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.systemProbes.MarkMayHaveSentTx(ctx, tx, start.OperationID, start.StartedAt.Add(time.Second)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO no_such_probe_table VALUES(1)`); err == nil {
		tx.Rollback()
		t.Fatal("fault injection did not fail")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// Dispatch is proved through the public transactional API: the original
	// timestamp may be used once after rollback, without a duplicate attempt.
	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.systemProbes.MarkMayHaveSentTx(ctx, tx, start.OperationID, start.StartedAt.Add(2*time.Second)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var employeeRequests sql.NullInt64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM model_requests`).Scan(&employeeRequests); err != nil || employeeRequests.Int64 != 0 {
		t.Fatalf("employee request inserted: %v", err)
	}
}
