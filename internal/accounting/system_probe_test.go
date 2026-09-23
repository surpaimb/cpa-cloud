package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var systemProbeTestStart = time.Date(2026, time.September, 23, 2, 3, 4, 5, time.UTC)

func TestSystemProbeLifecycleFreezesPriceAndReplaysIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "probe.db")
	db, prices, probes := openSystemProbeTest(t, path)
	defer db.Close()
	ctx := context.Background()
	insertPriceUpstream(t, db, "ups_probe")
	firstPrice := &PriceSnapshot{Currency: "USD", InputPerMillionMicro: 10, OutputPerMillionMicro: 20, CacheReadPerMillionMicro: 30, CacheWritePerMillionMicro: 40}
	firstVersion, err := prices.Save(ctx, PriceSave{AccountID: "ups_probe", ActualModel: "actual model", OperationID: probeUUID(90_001), Price: firstPrice})
	if err != nil {
		t.Fatal(err)
	}
	start := testSystemProbeStart(1)
	started := beginSystemProbe(t, db, probes, start)
	if started.Price == nil || started.Price.Version != firstVersion.Version || started.Status != SystemProbePending {
		t.Fatalf("started=%+v", started)
	}
	dispatchedAt := systemProbeTestStart.Add(time.Second)
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		marked, err := probes.MarkMayHaveSentTx(ctx, tx, start.OperationID, dispatchedAt)
		if err != nil || marked.MayHaveSentAt == nil || !marked.MayHaveSentAt.Equal(dispatchedAt) {
			t.Fatalf("marked=%+v err=%v", marked, err)
		}
	})
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		marked, err := probes.MarkMayHaveSentTx(ctx, tx, start.OperationID, dispatchedAt)
		if err != nil || marked.MayHaveSentAt == nil || !marked.MayHaveSentAt.Equal(dispatchedAt) {
			t.Fatalf("dispatch replay=%+v err=%v", marked, err)
		}
		if _, err := probes.MarkMayHaveSentTx(ctx, tx, start.OperationID, dispatchedAt.Add(time.Nanosecond)); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed dispatch err=%v", err)
		}
	})
	secondPrice := &PriceSnapshot{Currency: "USD", InputPerMillionMicro: 100, OutputPerMillionMicro: 200, CacheReadPerMillionMicro: 300, CacheWritePerMillionMicro: 400}
	if _, err := prices.Save(ctx, PriceSave{AccountID: "ups_probe", ActualModel: "actual model", OperationID: probeUUID(90_002), ExpectedRevision: 1, Price: secondPrice}); err != nil {
		t.Fatal(err)
	}
	replayed := beginSystemProbe(t, db, probes, start)
	if replayed.Price == nil || replayed.Price.Version != firstVersion.Version {
		t.Fatalf("price changed during begin replay: %+v", replayed.Price)
	}
	input, output, cacheRead, cacheWrite := int64(1_000_000), int64(2_000_000), int64(3_000_000), int64(4_000_000)
	finish := SystemProbeFinish{OperationID: start.OperationID, Status: SystemProbeSucceeded, Result: SystemProbeGenerationOK,
		FinishedAt: systemProbeTestStart.Add(2 * time.Second), Usage: Usage{InputTokens: &input, OutputTokens: &output, CacheReadTokens: &cacheRead, CacheWriteTokens: &cacheWrite}}
	var completed SystemProbeAttempt
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		var err error
		completed, err = probes.FinishTx(ctx, tx, finish)
		if err != nil {
			t.Fatal(err)
		}
	})
	if completed.CostMicro == nil || *completed.CostMicro != 300 || completed.Price == nil || completed.Price.Version != firstVersion.Version {
		t.Fatalf("completed=%+v", completed)
	}
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		again, err := probes.FinishTx(ctx, tx, finish)
		if err != nil || again.CostMicro == nil || *again.CostMicro != 300 {
			t.Fatalf("finish replay=%+v err=%v", again, err)
		}
		changed := finish
		changed.Result = SystemProbeProtocolError
		changed.Status = SystemProbeFailed
		if _, err := probes.FinishTx(ctx, tx, changed); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed finish err=%v", err)
		}
	})
	stored, err := probes.Get(ctx, start.OperationID)
	if err != nil || stored.Result == nil || *stored.Result != SystemProbeGenerationOK {
		t.Fatalf("get=%+v err=%v", stored, err)
	}
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		inTx, err := probes.GetTx(ctx, tx, start.OperationID)
		if err != nil || inTx.OperationID != start.OperationID {
			t.Fatalf("get tx=%+v err=%v", inTx, err)
		}
	})
	conflict := start
	conflict.PublicModel = "other-model"
	withSystemProbeTxRollback(t, db, func(tx *sql.Tx) {
		if _, err := probes.BeginTx(ctx, tx, conflict); !errors.Is(err, ErrConflict) {
			t.Fatalf("changed begin err=%v", err)
		}
	})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = openPriceDB(t, path)
	defer db.Close()
	probes = NewSystemProbeLedger(db)
	if err := probes.Migrate(ctx); err != nil {
		t.Fatalf("restart migration: %v", err)
	}
	restarted, err := probes.Get(ctx, start.OperationID)
	if err != nil || restarted.Status != SystemProbeSucceeded || restarted.CostMicro == nil || *restarted.CostMicro != 300 {
		t.Fatalf("restart get=%+v err=%v", restarted, err)
	}
}

func TestSystemProbeDispatchRulesUnknownCostAndEmployeeIsolation(t *testing.T) {
	db, _, probes := openSystemProbeTest(t, filepath.Join(t.TempDir(), "probe.db"))
	defer db.Close()
	ctx := context.Background()
	insertPriceUpstream(t, db, "ups_probe")
	employee := NewLedger(db)
	if err := employee.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	request := RequestStart{ID: "employee-request", EmployeeID: "employee", KeyID: "key", ModelID: "public-model", Provider: ProviderOpenAICompatible, StartedAt: systemProbeTestStart}
	if err := employee.BeginRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	beforeRequests, err := employee.SummarizeRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}

	noDispatch := testSystemProbeStart(2)
	beginSystemProbe(t, db, probes, noDispatch)
	withSystemProbeTxRollback(t, db, func(tx *sql.Tx) {
		_, err := probes.FinishTx(ctx, tx, SystemProbeFinish{OperationID: noDispatch.OperationID, Status: SystemProbeSucceeded, Result: SystemProbeGenerationOK, FinishedAt: systemProbeTestStart.Add(time.Second)})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("success without dispatch err=%v", err)
		}
		one := int64(1)
		_, err = probes.FinishTx(ctx, tx, SystemProbeFinish{OperationID: noDispatch.OperationID, Status: SystemProbeFailed, Result: SystemProbeProtocolError,
			FinishedAt: systemProbeTestStart.Add(time.Second), Usage: Usage{InputTokens: &one}})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("undispatched usage err=%v", err)
		}
	})
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		failed, err := probes.FinishTx(ctx, tx, SystemProbeFinish{OperationID: noDispatch.OperationID, Status: SystemProbeFailed, Result: SystemProbeConfigurationChanged, FinishedAt: systemProbeTestStart.Add(time.Second)})
		if err != nil || failed.CostMicro != nil || !sameUsage(failed.Usage, Usage{}) {
			t.Fatalf("undispatched failure=%+v err=%v", failed, err)
		}
	})

	dispatched := testSystemProbeStart(3)
	beginSystemProbe(t, db, probes, dispatched)
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		if _, err := probes.MarkMayHaveSentTx(ctx, tx, dispatched.OperationID, systemProbeTestStart.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	})
	one := int64(1)
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		finished, err := probes.FinishTx(ctx, tx, SystemProbeFinish{OperationID: dispatched.OperationID, Status: SystemProbeFailed, Result: SystemProbeProtocolError,
			FinishedAt: systemProbeTestStart.Add(2 * time.Second), Usage: Usage{InputTokens: &one}})
		if err != nil || finished.CostMicro != nil || finished.Usage.InputTokens == nil || *finished.Usage.InputTokens != one {
			t.Fatalf("unknown cost finish=%+v err=%v", finished, err)
		}
	})
	summaries, err := probes.Summarize(ctx)
	if err != nil || len(summaries) != 1 || summaries[0].Currency != UnknownCurrency || summaries[0].Total != 2 || summaries[0].Failed != 2 || summaries[0].UnknownCostAttempts != 2 || summaries[0].InputTokens.KnownTotal != 1 || summaries[0].InputTokens.UnknownAttempts != 1 {
		t.Fatalf("summaries=%+v err=%v", summaries, err)
	}
	afterRequests, err := employee.SummarizeRequests(ctx)
	if err != nil || afterRequests != beforeRequests {
		t.Fatalf("employee summary changed before=%+v after=%+v err=%v", beforeRequests, afterRequests, err)
	}
	employeeAttempts, err := employee.SummarizeAttempts(ctx)
	if err != nil || len(employeeAttempts) != 0 {
		t.Fatalf("system probes entered employee attempts: %+v err=%v", employeeAttempts, err)
	}
}

func TestSystemProbeCallerTransactionRollbackRetryAndInterrupt(t *testing.T) {
	db, _, probes := openSystemProbeTest(t, filepath.Join(t.TempDir(), "probe.db"))
	defer db.Close()
	ctx := context.Background()
	insertPriceUpstream(t, db, "ups_probe")
	start := testSystemProbeStart(4)
	withSystemProbeTxRollback(t, db, func(tx *sql.Tx) {
		if _, err := probes.BeginTx(ctx, tx, start); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := probes.Get(ctx, start.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rolled back begin err=%v", err)
	}
	beginSystemProbe(t, db, probes, start)
	withSystemProbeTxRollback(t, db, func(tx *sql.Tx) {
		if _, err := probes.FinishTx(ctx, tx, SystemProbeFinish{OperationID: start.OperationID, Status: SystemProbeFailed, Result: SystemProbeUnsupported, FinishedAt: systemProbeTestStart.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
	})
	stored, err := probes.Get(ctx, start.OperationID)
	if err != nil || stored.Status != SystemProbePending {
		t.Fatalf("finish rollback stored=%+v err=%v", stored, err)
	}

	second := testSystemProbeStart(5)
	beginSystemProbe(t, db, probes, second)
	withSystemProbeTxRollback(t, db, func(tx *sql.Tx) {
		count, err := probes.InterruptPendingTx(ctx, tx, systemProbeTestStart.Add(2*time.Second))
		if err != nil || count != 2 {
			t.Fatalf("interrupt rollback count=%d err=%v", count, err)
		}
	})
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		count, err := probes.InterruptPendingTx(ctx, tx, systemProbeTestStart.Add(2*time.Second))
		if err != nil || count != 2 {
			t.Fatalf("interrupt count=%d err=%v", count, err)
		}
	})
	for _, operationID := range []string{start.OperationID, second.OperationID} {
		interrupted, err := probes.Get(ctx, operationID)
		if err != nil || interrupted.Status != SystemProbeInterrupted || interrupted.Result == nil || *interrupted.Result != SystemProbeInterruptedResult || interrupted.CostMicro != nil {
			t.Fatalf("interrupted=%+v err=%v", interrupted, err)
		}
	}
}

func TestSystemProbeInterruptPendingClampsClockRollbackPerAttempt(t *testing.T) {
	db, _, probes := openSystemProbeTest(t, filepath.Join(t.TempDir(), "probe.db"))
	defer db.Close()
	ctx := context.Background()
	insertPriceUpstream(t, db, "ups_probe")
	startedOnly := testSystemProbeStart(6)
	dispatched := testSystemProbeStart(7)
	alreadyTerminal := testSystemProbeStart(8)
	for _, start := range []SystemProbeStart{startedOnly, dispatched, alreadyTerminal} {
		beginSystemProbe(t, db, probes, start)
	}
	sentAt := systemProbeTestStart.Add(5 * time.Second)
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		if _, err := probes.MarkMayHaveSentTx(ctx, tx, dispatched.OperationID, sentAt); err != nil {
			t.Fatal(err)
		}
		if _, err := probes.FinishTx(ctx, tx, SystemProbeFinish{OperationID: alreadyTerminal.OperationID, Status: SystemProbeFailed,
			Result: SystemProbeConfigurationChanged, FinishedAt: systemProbeTestStart.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
	})
	rollbackNow := systemProbeTestStart.Add(-time.Hour)
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		count, err := probes.InterruptPendingTx(ctx, tx, rollbackNow)
		if err != nil || count != 2 {
			t.Fatalf("clock rollback interrupt count=%d err=%v", count, err)
		}
	})
	for _, expected := range []struct {
		operationID string
		finished    time.Time
		status      SystemProbeStatus
	}{
		{startedOnly.OperationID, systemProbeTestStart, SystemProbeInterrupted},
		{dispatched.OperationID, sentAt, SystemProbeInterrupted},
		{alreadyTerminal.OperationID, systemProbeTestStart.Add(time.Second), SystemProbeFailed},
	} {
		attempt, err := probes.Get(ctx, expected.operationID)
		if err != nil || attempt.Status != expected.status || attempt.FinishedAt == nil || !attempt.FinishedAt.Equal(expected.finished) || !sameUsage(attempt.Usage, Usage{}) || attempt.CostMicro != nil {
			t.Fatalf("operation=%s attempt=%+v err=%v", expected.operationID, attempt, err)
		}
	}
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		count, err := probes.InterruptPendingTx(ctx, tx, rollbackNow.Add(-time.Hour))
		if err != nil || count != 0 {
			t.Fatalf("terminal replay count=%d err=%v", count, err)
		}
	})
	if err := probes.Migrate(ctx); err != nil {
		t.Fatalf("clamped terminal metadata failed restart validation: %v", err)
	}
}

func TestSystemProbeHistoryLimitPreservesExistingOperations(t *testing.T) {
	db, _, probes := openSystemProbeTest(t, filepath.Join(t.TempDir(), "probe.db"))
	defer db.Close()
	ctx := context.Background()
	insertPriceUpstream(t, db, "ups_probe")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO system_probe_attempts(operation_id,recovery_event_id,account_id,account_revision,pool_revision,public_model,upstream_model,provider,protocol,started_at,status) VALUES(?,?,?,?,?,?,?,?,?,?,'pending')`)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < MaxSystemProbeHistory; index++ {
		if _, err := statement.ExecContext(ctx, probeUUID(index), "recovery-event", "ups_probe", 1, 1, "public-model", "actual model", string(ProviderOpenAICompatible), string(ProtocolOpenAIResponses), systemProbeTestStart.Format(time.RFC3339Nano)); err != nil {
			t.Fatalf("seed index=%d: %v", index, err)
		}
	}
	if err := statement.Close(); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := probes.Migrate(ctx); err != nil {
		t.Fatalf("existing full history prevented restart migration: %v", err)
	}
	existing := testSystemProbeStart(0)
	existing.RecoveryEventID = "recovery-event"
	replayed := beginSystemProbe(t, db, probes, existing)
	if replayed.OperationID != existing.OperationID {
		t.Fatalf("existing replay=%+v", replayed)
	}
	newStart := testSystemProbeStart(MaxSystemProbeHistory)
	withSystemProbeTxRollback(t, db, func(tx *sql.Tx) {
		if _, err := probes.BeginTx(ctx, tx, newStart); !errors.Is(err, ErrSystemProbeHistoryFull) {
			t.Fatalf("history full err=%v", err)
		}
	})
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		finished, err := probes.FinishTx(ctx, tx, SystemProbeFinish{OperationID: existing.OperationID, Status: SystemProbeFailed, Result: SystemProbeConfigurationChanged, FinishedAt: systemProbeTestStart.Add(time.Second)})
		if err != nil || finished.Status != SystemProbeFailed {
			t.Fatalf("finish old at limit=%+v err=%v", finished, err)
		}
	})
}

func TestSystemProbeMigrationStrictRollbackAndStoredValidation(t *testing.T) {
	t.Run("incompatible table rolls back and can be repaired", func(t *testing.T) {
		db := openPriceDB(t, filepath.Join(t.TempDir(), "bad.db"))
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE upstreams(id TEXT PRIMARY KEY); CREATE TABLE system_probe_attempts(marker TEXT PRIMARY KEY); INSERT INTO system_probe_attempts(marker) VALUES('keep')`); err != nil {
			t.Fatal(err)
		}
		ledger := NewSystemProbeLedger(db)
		if err := ledger.Migrate(context.Background()); err == nil {
			t.Fatal("incompatible table was accepted")
		}
		var marker string
		if err := db.QueryRow(`SELECT marker FROM system_probe_attempts`).Scan(&marker); err != nil || marker != "keep" {
			t.Fatalf("failed migration changed old table marker=%q err=%v", marker, err)
		}
		var leakedIndexes int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'system_probe_attempts_%_idx'`).Scan(&leakedIndexes); err != nil || leakedIndexes != 0 {
			t.Fatalf("failed migration leaked indexes=%d err=%v", leakedIndexes, err)
		}
		if _, err := db.Exec(`DROP TABLE system_probe_attempts`); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Migrate(context.Background()); err != nil {
			t.Fatalf("retry migration: %v", err)
		}
	})

	t.Run("extra schema and invalid stored metadata fail closed", func(t *testing.T) {
		db, _, ledger := openSystemProbeTest(t, filepath.Join(t.TempDir(), "strict.db"))
		defer db.Close()
		insertPriceUpstream(t, db, "ups_probe")
		if _, err := db.Exec(`CREATE INDEX system_probe_attempts_unexpected_idx ON system_probe_attempts(result_code)`); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Migrate(context.Background()); err == nil {
			t.Fatal("unexpected index was accepted")
		}
		if _, err := db.Exec(`DROP INDEX system_probe_attempts_unexpected_idx`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
			t.Fatal(err)
		}
		_, err := db.Exec(`INSERT INTO system_probe_attempts(operation_id,recovery_event_id,account_id,account_revision,pool_revision,public_model,upstream_model,provider,protocol,started_at,status) VALUES(?,?,?,?,?,?,?,?,?,?,'pending')`,
			probeUUID(88_001), "event", "ups_probe", 1, 1, "public-model", "actual model", string(ProviderAnthropic), string(ProtocolOpenAIResponses), systemProbeTestStart.Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`PRAGMA ignore_check_constraints=OFF`); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Migrate(context.Background()); err == nil {
			t.Fatal("invalid stored provider/protocol was accepted")
		}
		if _, err := db.Exec(`DELETE FROM system_probe_attempts`); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Migrate(context.Background()); err != nil {
			t.Fatalf("migration after repair: %v", err)
		}
	})

	t.Run("extra secret column is rejected", func(t *testing.T) {
		db, _, ledger := openSystemProbeTest(t, filepath.Join(t.TempDir(), "secret.db"))
		defer db.Close()
		if _, err := db.Exec(`ALTER TABLE system_probe_attempts ADD COLUMN credential_secret TEXT`); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Migrate(context.Background()); err == nil {
			t.Fatal("extra secret column was accepted")
		}
	})

	t.Run("check literal case is exact", func(t *testing.T) {
		db := openPriceDB(t, filepath.Join(t.TempDir(), "case.db"))
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE upstreams(id TEXT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		definition := strings.Replace(systemProbeDDL, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1)
		definition = strings.ReplaceAll(definition, "'generation_ok'", "'GENERATION_OK'")
		if _, err := db.Exec(definition); err != nil {
			t.Fatal(err)
		}
		if err := NewSystemProbeLedger(db).Migrate(context.Background()); err == nil {
			t.Fatal("case-changed result CHECK was accepted")
		}
	})

	t.Run("check literal whitespace is exact and rollback is retryable", func(t *testing.T) {
		db := openPriceDB(t, filepath.Join(t.TempDir(), "literal-space.db"))
		defer db.Close()
		if _, err := db.Exec(`CREATE TABLE upstreams(id TEXT PRIMARY KEY)`); err != nil {
			t.Fatal(err)
		}
		definition := strings.Replace(systemProbeDDL, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1)
		definition = strings.ReplaceAll(definition, "'succeeded'", "'suc ceeded'")
		if _, err := db.Exec(definition); err != nil {
			t.Fatal(err)
		}
		ledger := NewSystemProbeLedger(db)
		if err := ledger.Migrate(context.Background()); err == nil {
			t.Fatal("whitespace-changed status CHECK was accepted")
		}
		var storedDefinition string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, systemProbeTable).Scan(&storedDefinition); err != nil || !strings.Contains(storedDefinition, "'suc ceeded'") {
			t.Fatalf("failed migration changed malformed old table definition=%q err=%v", storedDefinition, err)
		}
		var leakedIndexes int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name IN ('system_probe_attempts_account_started_idx','system_probe_attempts_status_started_idx')`).Scan(&leakedIndexes); err != nil || leakedIndexes != 0 {
			t.Fatalf("failed migration leaked indexes=%d err=%v", leakedIndexes, err)
		}
		if _, err := db.Exec(`DROP TABLE system_probe_attempts`); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Migrate(context.Background()); err != nil {
			t.Fatalf("migration after repairing literal: %v", err)
		}
	})
}

func TestPriceCatalogCurrentTx(t *testing.T) {
	db, prices := openPriceCatalog(t, filepath.Join(t.TempDir(), "prices.db"))
	defer db.Close()
	insertPriceUpstream(t, db, "ups_probe")
	written, err := prices.Save(context.Background(), PriceSave{AccountID: "ups_probe", ActualModel: "actual model", OperationID: probeUUID(99_001), Price: &PriceSnapshot{Currency: "USD", InputPerMillionMicro: 1}})
	if err != nil {
		t.Fatal(err)
	}
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		current, err := prices.CurrentTx(context.Background(), tx, "ups_probe", "actual model")
		if err != nil || current == nil || current.Version != written.Version {
			t.Fatalf("current tx=%+v err=%v", current, err)
		}
		missing, err := prices.CurrentTx(context.Background(), tx, "ups_probe", "missing model")
		if err != nil || missing != nil {
			t.Fatalf("missing current tx=%+v err=%v", missing, err)
		}
	})
	if _, err := prices.CurrentTx(context.Background(), nil, "ups_probe", "actual model"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil tx err=%v", err)
	}
}

func TestNormalizeSystemProbeSQLPreservesQuotedWhitespaceAndEscapes(t *testing.T) {
	input := " CREATE\u2003TABLE \"table  name\" (value TEXT CHECK(value IN ('suc ceeded','it''s valid')), [bracket  name] TEXT, `tick  name` TEXT) "
	want := "CREATETABLE\"table  name\"(valueTEXTCHECK(valueIN('suc ceeded','it''s valid')),[bracket  name]TEXT,`tick  name`TEXT)"
	if got := normalizeSystemProbeSQL(input); got != want {
		t.Fatalf("normalized SQL=%q want=%q", got, want)
	}
}

func openSystemProbeTest(t *testing.T, path string) (*sql.DB, *PriceCatalog, *SystemProbeLedger) {
	t.Helper()
	db := openPriceDB(t, path)
	if _, err := db.Exec(`CREATE TABLE upstreams(id TEXT PRIMARY KEY)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	prices := NewPriceCatalog(db)
	if err := prices.Migrate(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	probes := NewSystemProbeLedger(db)
	if err := probes.Migrate(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, prices, probes
}

func testSystemProbeStart(index int) SystemProbeStart {
	return SystemProbeStart{OperationID: probeUUID(index), RecoveryEventID: "recovery-event", AccountID: "ups_probe", AccountRevision: 1, PoolRevision: 1,
		PublicModel: "public-model", UpstreamModel: "actual model", Provider: ProviderOpenAICompatible, Protocol: ProtocolOpenAIResponses, StartedAt: systemProbeTestStart}
}

func probeUUID(index int) string {
	return fmt.Sprintf("10000000-0000-4000-8000-%012d", index)
}

func beginSystemProbe(t *testing.T, db *sql.DB, ledger *SystemProbeLedger, input SystemProbeStart) SystemProbeAttempt {
	t.Helper()
	var attempt SystemProbeAttempt
	withSystemProbeTx(t, db, func(tx *sql.Tx) {
		var err error
		attempt, err = ledger.BeginTx(context.Background(), tx, input)
		if err != nil {
			t.Fatal(err)
		}
	})
	return attempt
}

func withSystemProbeTx(t *testing.T, db *sql.DB, fn func(*sql.Tx)) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	fn(tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func withSystemProbeTxRollback(t *testing.T, db *sql.DB, fn func(*sql.Tx)) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	fn(tx)
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}
