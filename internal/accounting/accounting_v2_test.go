package accounting

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAccountingV2DurableDispatchTerminalEventCorrectionAndReport(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "accounting-v2.db"))
	defer db.Close()
	if err := ledger.MigrateV2(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	started := testStart.UTC()
	request := RequestStart{ID: "request-v2", EmployeeID: "employee-v2", KeyID: "key-v2", ModelID: "public-model", Provider: ProviderOpenAICompatible, StartedAt: started}
	price := testPrice
	attempt := AttemptStart{ID: "attempt-v2", RequestID: request.ID, AccountID: "account-v2", Provider: request.Provider, Dispatch: DispatchPrimary, StartedAt: started, Price: &price, Protocol: ProtocolOpenAIChatCompletions, EffectiveModel: "actual-model", Evidence: EvidenceProviderResponse}
	if err := ledger.BeginRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	oneMillion, twoMillion, threeMillion, fourMillion := int64(1_000_000), int64(2_000_000), int64(3_000_000), int64(4_000_000)
	reasoning := int64(500_000)
	responseID := "provider-response-late"
	finish := AttemptFinish{ID: attempt.ID, Status: StatusSucceeded, FinishedAt: started.Add(2 * time.Second), Usage: Usage{InputTokens: &oneMillion, OutputTokens: &twoMillion, CacheReadTokens: &threeMillion, CacheWriteTokens: &fourMillion}, ReasoningTokens: &reasoning, ResponseID: &responseID, ReliableUsage: true}
	if err := ledger.FinishAttempt(ctx, finish); !errors.Is(err, ErrConflict) {
		t.Fatalf("reliable success without durable dispatch err=%v", err)
	}
	assertAttemptStatus(t, db, attempt.ID, StatusPending)
	dispatch := AttemptDispatch{ID: attempt.ID, OperationID: "dispatch-v2", DispatchedAt: started.Add(time.Second)}
	if err := ledger.MarkAttemptDispatched(ctx, dispatch); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkAttemptDispatched(ctx, dispatch); err != nil {
		t.Fatalf("dispatch replay: %v", err)
	}
	changedDispatch := dispatch
	changedDispatch.DispatchedAt = dispatch.DispatchedAt.Add(time.Nanosecond)
	if err := ledger.MarkAttemptDispatched(ctx, changedDispatch); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed dispatch err=%v", err)
	}
	earlyFinish := finish
	earlyFinish.FinishedAt = started.Add(500 * time.Millisecond)
	if err := ledger.FinishAttempt(ctx, earlyFinish); !errors.Is(err, ErrInvalid) {
		t.Fatalf("finish before durable dispatch err=%v", err)
	}
	assertAttemptStatus(t, db, attempt.ID, StatusPending)
	if err := ledger.FinishAttempt(ctx, finish); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequest(ctx, RequestFinish{ID: request.ID, Status: StatusSucceeded, FinishedAt: started.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}

	base, err := loadBaseEvent(ctx, db, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	if base.Evidence != EvidenceProviderResponse || base.ResponseID == nil || *base.ResponseID != responseID || base.EstimatedCostMicro == nil || *base.EstimatedCostMicro != 300 {
		t.Fatalf("base event=%+v", base)
	}

	delta := int64(1_000_000)
	costDelta := int64(20)
	correction := Correction{ID: "correction-v2", AttemptID: attempt.ID, TargetEventID: base.ID, OperationID: "correction-operation-v2", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: started.Add(4 * time.Second), Currency: "USD", OutputTokens: CorrectionValue{Delta: &delta}, EstimatedCostDeltaMicro: &costDelta}
	if err := ledger.AppendCorrection(ctx, correction); err != nil {
		t.Fatal(err)
	}
	if err := ledger.AppendCorrection(ctx, correction); err != nil {
		t.Fatalf("correction replay: %v", err)
	}

	filters := AccountingV2Filters{From: started.Add(-time.Second), To: started.Add(time.Hour)}
	queryCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := ledger.AccountingV2Export(queryCtx, filters, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].CorrectionCount != 1 || rows[0].OutputTokens == nil || *rows[0].OutputTokens != 3_000_000 || rows[0].EstimatedCostMicro == nil || *rows[0].EstimatedCostMicro != 320 || rows[0].ResponseID == nil || *rows[0].ResponseID != responseID || rows[0].Dispatch != DispatchPrimary || !rows[0].StartedAt.Equal(started) || rows[0].DispatchedAt == nil || !rows[0].DispatchedAt.Equal(dispatch.DispatchedAt) {
		t.Fatalf("export rows=%+v", rows)
	}
	for index, at := range []time.Time{correction.CorrectedAt, correction.CorrectedAt.Add(-time.Nanosecond)} {
		reverse := correction
		reverse.ID = "reverse-" + strconv.Itoa(index)
		reverse.OperationID = "reverse-operation-" + strconv.Itoa(index)
		reverse.CorrectedAt = at
		if err := ledger.AppendCorrection(ctx, reverse); !errors.Is(err, ErrConflict) {
			t.Fatalf("reverse correction at %s err=%v", at, err)
		}
	}
	report, err := ledger.AccountingV2Report(queryCtx, filters, AccountingV2Day)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 1 || report[0].Currency != "USD" || report[0].Requests != 1 || report[0].Attempts != 1 || report[0].Corrections != 1 || report[0].KnownEstimatedCostMicro != 320 || report[0].ReasoningTokens.KnownTotal != reasoning {
		t.Fatalf("report=%+v", report)
	}
	if _, err := db.Exec(`UPDATE accounting_usage_events SET status='failed' WHERE attempt_id=?`, attempt.ID); err == nil {
		t.Fatal("immutable usage event accepted update")
	}
}

func TestAccountingV2TransactionalDispatchRollbackLeavesNoFacts(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "dispatch-rollback.db"))
	defer db.Close()
	if err := ledger.MigrateV2(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := testStart.UTC()
	request := RequestStart{ID: "request-tx", EmployeeID: "employee-tx", KeyID: "key-tx", ModelID: "model-tx", Provider: ProviderAnthropic, StartedAt: started}
	if err := ledger.BeginRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	attempt := AttemptStart{ID: "attempt-tx", RequestID: request.ID, AccountID: "account-tx", Provider: request.Provider, Dispatch: DispatchPrimary, StartedAt: started, Protocol: ProtocolAnthropicMessages, EffectiveModel: "claude-model", Evidence: EvidenceBackgroundResult}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttemptTx(context.Background(), tx, attempt); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := ledger.MarkAttemptDispatchedTx(context.Background(), tx, AttemptDispatch{ID: attempt.ID, OperationID: "dispatch-tx", DispatchedAt: started.Add(time.Second)}); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ table, column string }{{"accounting_attempts", "id"}, {"accounting_attempt_contexts", "attempt_id"}, {"accounting_attempt_dispatches", "attempt_id"}} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + entry.table + ` WHERE ` + entry.column + `='attempt-tx'`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("table %s count=%d err=%v", entry.table, count, err)
		}
	}
}

func TestAccountingV2CorrectionRejectsKnownSetNoopNegativeOverflowAndCurrency(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "corrections.db"))
	defer db.Close()
	if err := ledger.MigrateV2(context.Background()); err != nil {
		t.Fatal(err)
	}
	base, started := seedAccountingV2FinishedAttempt(t, ledger, "correction-cases")
	zero, one, negative, overflow := int64(0), int64(1), int64(-2), int64(math.MaxInt64)
	cases := []Correction{
		{ID: "known-set", AttemptID: "attempt-correction-cases", TargetEventID: base.ID, OperationID: "op-known-set", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: started.Add(4 * time.Second), Currency: "USD", InputTokens: CorrectionValue{Set: &one}},
		{ID: "noop", AttemptID: "attempt-correction-cases", TargetEventID: base.ID, OperationID: "op-noop", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: started.Add(4 * time.Second), Currency: "USD"},
		{ID: "zero-delta", AttemptID: "attempt-correction-cases", TargetEventID: base.ID, OperationID: "op-zero", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: started.Add(4 * time.Second), Currency: "USD", InputTokens: CorrectionValue{Delta: &zero}},
		{ID: "negative", AttemptID: "attempt-correction-cases", TargetEventID: base.ID, OperationID: "op-negative", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: started.Add(4 * time.Second), Currency: "USD", InputTokens: CorrectionValue{Delta: &negative}},
		{ID: "overflow", AttemptID: "attempt-correction-cases", TargetEventID: base.ID, OperationID: "op-overflow", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: started.Add(4 * time.Second), Currency: "USD", InputTokens: CorrectionValue{Delta: &overflow}},
		{ID: "currency", AttemptID: "attempt-correction-cases", TargetEventID: base.ID, OperationID: "op-currency", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: started.Add(4 * time.Second), Currency: "EUR", InputTokens: CorrectionValue{Delta: &one}},
	}
	for _, correction := range cases {
		if err := ledger.AppendCorrection(context.Background(), correction); !errors.Is(err, ErrInvalid) {
			t.Fatalf("correction %s err=%v", correction.ID, err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_usage_corrections`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rejected correction rows=%d err=%v", count, err)
	}
}

func TestAccountingV2CorrectionAndExportLimitsAreBounded(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "correction-limits.db"))
	defer db.Close()
	if err := ledger.MigrateV2(context.Background()); err != nil {
		t.Fatal(err)
	}
	seedAccountingV2FinishedAttempt(t, ledger, "a-clean")
	base, started := seedAccountingV2FinishedAttempt(t, ledger, "correction-limit")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	statement, err := tx.Prepare(`INSERT INTO accounting_usage_corrections(id,attempt_id,target_event_id,operation_id,sequence,actor,reason,corrected_at,currency,input_delta,estimated_cost_delta_micro) VALUES(?,?,?,?,?,?,'admin_reconciliation',?,'USD',0,0)`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	for index := 0; index < MaxAccountingV2Corrections+1; index++ {
		at := started.Add(4*time.Second + time.Duration(index)*time.Nanosecond).Format(time.RFC3339Nano)
		if _, err := statement.Exec("direct-"+strconv.Itoa(index), "attempt-correction-limit", base.ID, "direct-operation-"+strconv.Itoa(index), index+1, "admin", at); err != nil {
			statement.Close()
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	one := int64(1)
	zeroCost := int64(0)
	if err := ledger.AppendCorrection(context.Background(), Correction{ID: "over-per-attempt", AttemptID: "attempt-correction-limit", TargetEventID: base.ID, OperationID: "over-per-attempt-operation", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: started.Add(time.Minute), Currency: "USD", InputTokens: CorrectionValue{Delta: &one}, EstimatedCostDeltaMicro: &zeroCost}); !errors.Is(err, ErrAccountingV2Limit) {
		t.Fatalf("append limit err=%v", err)
	}
	filters := AccountingV2Filters{From: started, To: started.Add(time.Hour)}
	rows, err := ledger.AccountingV2Export(context.Background(), filters, 1)
	if err != nil {
		t.Fatalf("limited export inspected unselected corrections: %v", err)
	}
	if len(rows) != 1 || rows[0].AttemptID != "attempt-a-clean" {
		t.Fatalf("limited export rows=%+v", rows)
	}
	if _, err := ledger.AccountingV2Export(context.Background(), filters, 10); !errors.Is(err, ErrAccountingV2Limit) {
		t.Fatalf("export correction limit err=%v", err)
	}
}

func TestAccountingV2ReportPaginatesBeyondExportLimit(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "report-pagination.db"))
	defer db.Close()
	if err := ledger.MigrateV2(context.Background()); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	requestStatement, err := tx.Prepare(`INSERT INTO accounting_requests(id,employee_id,key_id,model_id,provider,started_at,finished_at,status) VALUES(?,?,?,?,?,?,?,'succeeded')`)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	attemptStatement, err := tx.Prepare(`INSERT INTO accounting_attempts(id,request_id,account_id,provider,dispatch,started_at,finished_at,status) VALUES(?,?,?,'openai-compatible','primary',?,?,'succeeded')`)
	if err != nil {
		requestStatement.Close()
		tx.Rollback()
		t.Fatal(err)
	}
	const attempts = MaxAccountingV2ExportRows + 1
	for index := 0; index < attempts; index++ {
		suffix := strconv.Itoa(index)
		started := testStart.UTC().Add(time.Duration(index) * time.Nanosecond)
		finished := started.Add(time.Second)
		if _, err := requestStatement.Exec("report-request-"+suffix, "report-employee", "report-key", "report-model", "openai-compatible", started.Format(time.RFC3339Nano), finished.Format(time.RFC3339Nano)); err != nil {
			requestStatement.Close()
			attemptStatement.Close()
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err := attemptStatement.Exec("report-attempt-"+suffix, "report-request-"+suffix, "report-account", started.Format(time.RFC3339Nano), finished.Format(time.RFC3339Nano)); err != nil {
			requestStatement.Close()
			attemptStatement.Close()
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := requestStatement.Close(); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := attemptStatement.Close(); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	report, err := ledger.AccountingV2Report(context.Background(), AccountingV2Filters{From: testStart.UTC(), To: testStart.UTC().Add(time.Hour)}, AccountingV2Day)
	if err != nil {
		t.Fatal(err)
	}
	if len(report) != 1 || report[0].Currency != UnknownCurrency || report[0].Requests != attempts || report[0].Attempts != attempts || report[0].MissingEvidenceAttempts != attempts || report[0].UnknownCostAttempts != attempts {
		t.Fatalf("report=%+v", report)
	}
}

func TestAccountingV2RecoveryUsesDurableDispatchAndKeepsUnknown(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "recovery.db"))
	defer db.Close()
	if err := ledger.MigrateV2(context.Background()); err != nil {
		t.Fatal(err)
	}
	started := testStart.UTC()
	for _, suffix := range []string{"sent", "not-sent"} {
		request := RequestStart{ID: "request-" + suffix, EmployeeID: "employee-recovery", KeyID: "key-recovery", ModelID: "public-model", Provider: ProviderGemini, StartedAt: started}
		attempt := AttemptStart{ID: "attempt-" + suffix, RequestID: request.ID, AccountID: "account-recovery", Provider: request.Provider, Dispatch: DispatchPrimary, StartedAt: started, Protocol: ProtocolGeminiGenerateContent, EffectiveModel: "gemini-model", Evidence: EvidenceSystemTerminal}
		if suffix == "sent" {
			taskID := "background-task-known"
			attempt.TaskID = &taskID
		}
		if err := ledger.BeginRequest(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		if err := ledger.BeginAttempt(context.Background(), attempt); err != nil {
			t.Fatal(err)
		}
		if suffix == "sent" {
			if err := ledger.MarkAttemptDispatched(context.Background(), AttemptDispatch{ID: attempt.ID, OperationID: "dispatch-recovery", DispatchedAt: started.Add(time.Second)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.RecoverInterruptedTx(context.Background(), tx, started.Add(10*time.Second)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if count, err := ledger.RecoverV2Tx(context.Background(), tx); err != nil || count != 1 {
		tx.Rollback()
		t.Fatalf("v2 recovery count=%d err=%v", count, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	filters := AccountingV2Filters{From: started, To: started.Add(time.Hour)}
	rows, err := ledger.AccountingV2Export(context.Background(), filters, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%+v", rows)
	}
	byID := map[string]AccountingV2Row{rows[0].AttemptID: rows[0], rows[1].AttemptID: rows[1]}
	if sent := byID["attempt-sent"]; sent.EventID == nil || sent.Evidence == nil || *sent.Evidence != EvidenceSystemTerminal || sent.InputTokens != nil || sent.EstimatedCostMicro != nil || sent.TaskID == nil || *sent.TaskID != "background-task-known" || sent.DispatchedAt == nil {
		t.Fatalf("dispatched recovery=%+v", sent)
	}
	if notSent := byID["attempt-not-sent"]; notSent.EventID != nil {
		t.Fatalf("non-dispatched recovery invented evidence=%+v", notSent)
	}
}

func TestAccountingV2StrictMigrationRollbackAndRetry(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "migration.db"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE accounting_attempt_contexts(attempt_id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MigrateV2(context.Background()); err == nil {
		t.Fatal("lookalike accounting v2 schema accepted")
	}
	for _, table := range []string{"accounting_attempt_dispatches", "accounting_usage_events", "accounting_usage_corrections"} {
		assertSchemaObjectCount(t, db, table, 0)
	}
	if _, err := db.Exec(`DROP TABLE accounting_attempt_contexts`); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MigrateV2(context.Background()); err != nil {
		t.Fatalf("migration retry: %v", err)
	}
	if err := ledger.MigrateV2(context.Background()); err != nil {
		t.Fatalf("migration idempotency: %v", err)
	}
}

func TestAccountingProtocolCostConstraintMigrationPreservesFactsAndForeignKeys(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "protocol-cost-migration.db"))
	defer db.Close()
	ctx := context.Background()
	if err := ledger.MigrateV2(ctx); err != nil {
		t.Fatal(err)
	}
	base, _ := seedAccountingV2FinishedAttempt(t, ledger, "cost-migration")
	downgradeAccountingCostConstraints(t, db)
	for _, statement := range []string{
		`CREATE TABLE synthetic_attempt_cost_child(attempt_id TEXT PRIMARY KEY REFERENCES accounting_attempts(id))`,
		`INSERT INTO synthetic_attempt_cost_child VALUES('attempt-cost-migration')`,
		`CREATE TABLE synthetic_event_cost_child(event_id TEXT PRIMARY KEY REFERENCES accounting_usage_events(id))`,
		`INSERT INTO synthetic_event_cost_child VALUES('` + base.ID + `')`,
		`CREATE TABLE accounting_attempts_cost_migration(marker TEXT)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.Migrate(ctx); err == nil {
		t.Fatal("attempt cost migration accepted a reserved-object collision")
	}
	assertForeignKeysEnabled(t, db)
	assertSchemaContains(t, db, "accounting_attempts", accountingAttemptLegacyCostCheck, true)
	if _, err := db.Exec(`DROP TABLE accounting_attempts_cost_migration`); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatalf("attempt cost migration: %v", err)
	}
	assertSchemaContains(t, db, "accounting_attempts", accountingAttemptCurrentCostCheck, true)
	if _, err := db.Exec(`CREATE TABLE accounting_usage_events_cost_migration(marker TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MigrateV2(ctx); err == nil {
		t.Fatal("usage event cost migration accepted a reserved-object collision")
	}
	assertForeignKeysEnabled(t, db)
	assertSchemaContains(t, db, "accounting_usage_events", accountingUsageEventLegacyCostCheck, true)
	if _, err := db.Exec(`DROP TABLE accounting_usage_events_cost_migration`); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MigrateV2(ctx); err != nil {
		t.Fatalf("usage event cost migration: %v", err)
	}
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatalf("attempt migration restart: %v", err)
	}
	if err := ledger.MigrateV2(ctx); err != nil {
		t.Fatalf("usage event migration restart: %v", err)
	}
	assertForeignKeysEnabled(t, db)
	assertSchemaContains(t, db, "accounting_usage_events", accountingUsageEventCurrentCostCheck, true)
	var preservedAttempt, preservedEvent int
	if err := db.QueryRow(`SELECT COUNT(*) FROM synthetic_attempt_cost_child WHERE attempt_id='attempt-cost-migration'`).Scan(&preservedAttempt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM synthetic_event_cost_child WHERE event_id=?`, base.ID).Scan(&preservedEvent); err != nil {
		t.Fatal(err)
	}
	if preservedAttempt != 1 || preservedEvent != 1 {
		t.Fatalf("preserved child facts=%d/%d", preservedAttempt, preservedEvent)
	}
	violations, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if violations.Next() {
		violations.Close()
		t.Fatal("cost migration left a foreign key violation")
	}
	if err := violations.Close(); err != nil {
		t.Fatal(err)
	}

	started := testStart.UTC().Add(time.Hour)
	request := RequestStart{ID: "request-embedding-cost", EmployeeID: "employee-v2", KeyID: "key-v2", ModelID: "embedding-model", Provider: ProviderOpenAICompatible, StartedAt: started}
	price := PriceSnapshot{Version: "embedding-price", Currency: "EUR", InputPerMillionMicro: 3_000_000, OutputPerMillionMicro: 9_000_000, CacheReadPerMillionMicro: 9_000_000, CacheWritePerMillionMicro: 9_000_000}
	attempt := AttemptStart{ID: "attempt-embedding-cost", RequestID: request.ID, AccountID: "embedding-account", Provider: request.Provider, Dispatch: DispatchPrimary, StartedAt: started, Price: &price, Protocol: ProtocolOpenAIEmbeddings, EffectiveModel: "provider-embedding", Evidence: EvidenceProviderResponse}
	if err := ledger.BeginRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkAttemptDispatched(ctx, AttemptDispatch{ID: attempt.ID, OperationID: "embedding-cost-dispatch", DispatchedAt: started.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	input := int64(2)
	if err := ledger.FinishAttempt(ctx, AttemptFinish{ID: attempt.ID, Status: StatusSucceeded, FinishedAt: started.Add(2 * time.Second), Usage: Usage{InputTokens: &input}, ReliableUsage: true}); err != nil {
		t.Fatal(err)
	}
	var attemptCost, eventCost int64
	var output, cacheRead, cacheWrite sql.NullInt64
	if err := db.QueryRow(`SELECT a.cost_micro,e.estimated_cost_micro,a.output_tokens,a.cache_read_tokens,a.cache_write_tokens FROM accounting_attempts a JOIN accounting_usage_events e ON e.attempt_id=a.id WHERE a.id=?`, attempt.ID).Scan(&attemptCost, &eventCost, &output, &cacheRead, &cacheWrite); err != nil {
		t.Fatal(err)
	}
	if attemptCost != 6 || eventCost != 6 || output.Valid || cacheRead.Valid || cacheWrite.Valid {
		t.Fatalf("embedding migrated cost=%d/%d buckets=%v/%v/%v", attemptCost, eventCost, output, cacheRead, cacheWrite)
	}
	var eventID string
	if err := db.QueryRow(`SELECT id FROM accounting_usage_events WHERE attempt_id=?`, attempt.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	delta, costDelta := int64(1), int64(3)
	if err := ledger.AppendCorrection(ctx, Correction{ID: "embedding-input-correction", AttemptID: attempt.ID, TargetEventID: eventID, OperationID: "embedding-input-correction-operation", Actor: "embedding-admin", Reason: CorrectionAdminReconcile, CorrectedAt: started.Add(3 * time.Second), Currency: "EUR", InputTokens: CorrectionValue{Delta: &delta}, EstimatedCostDeltaMicro: &costDelta}); err != nil {
		t.Fatal(err)
	}
	rows, err := ledger.AccountingV2Export(ctx, AccountingV2Filters{From: started.Add(-time.Minute), To: started.Add(time.Minute)}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].EstimatedCostMicro == nil || *rows[0].EstimatedCostMicro != 9 || rows[0].OutputTokens != nil {
		t.Fatalf("embedding corrected export=%+v", rows)
	}
}

func downgradeAccountingCostConstraints(t *testing.T, db *sql.DB) {
	t.Helper()
	currentAttempt := `cost_micro IS NULL
				OR
				(price_version IS NOT NULL AND input_tokens IS NOT NULL)`
	legacyAttempt := `(price_version IS NOT NULL AND input_tokens IS NOT NULL AND output_tokens IS NOT NULL AND cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL AND cost_micro IS NOT NULL)
				OR
				((price_version IS NULL OR input_tokens IS NULL OR output_tokens IS NULL OR cache_read_tokens IS NULL OR cache_write_tokens IS NULL) AND cost_micro IS NULL)`
	legacyAttemptDDL := strings.Replace(accountingAttemptsColumnsDDL, currentAttempt, legacyAttempt, 1)
	currentEvent := `CHECK(estimated_cost_micro IS NULL OR input_tokens IS NOT NULL)`
	legacyEvent := `CHECK(estimated_cost_micro IS NULL OR (input_tokens IS NOT NULL AND output_tokens IS NOT NULL AND cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL))`
	legacyEventDDL := strings.Replace(accountingUsageEventsColumnsDDL, currentEvent, legacyEvent, 1)
	if legacyAttemptDDL == accountingAttemptsColumnsDDL || legacyEventDDL == accountingUsageEventsColumnsDDL {
		t.Fatal("test did not locate current cost constraints")
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	statements := []string{
		`DROP TRIGGER accounting_usage_events_no_update`,
		`DROP TRIGGER accounting_usage_events_no_delete`,
		`CREATE TABLE accounting_usage_events_legacy ` + legacyEventDDL,
		`INSERT INTO accounting_usage_events_legacy SELECT * FROM accounting_usage_events`,
		`DROP TABLE accounting_usage_events`,
		`ALTER TABLE accounting_usage_events_legacy RENAME TO accounting_usage_events`,
		`CREATE INDEX accounting_usage_events_finished_idx ON accounting_usage_events(finished_at,id)`,
		`CREATE TRIGGER accounting_usage_events_no_update BEFORE UPDATE ON accounting_usage_events BEGIN SELECT RAISE(ABORT,'accounting v2 facts are immutable'); END`,
		`CREATE TRIGGER accounting_usage_events_no_delete BEFORE DELETE ON accounting_usage_events BEGIN SELECT RAISE(ABORT,'accounting v2 facts are immutable'); END`,
		`CREATE TABLE accounting_attempts_legacy ` + legacyAttemptDDL,
		`INSERT INTO accounting_attempts_legacy SELECT * FROM accounting_attempts`,
		`DROP TABLE accounting_attempts`,
		`ALTER TABLE accounting_attempts_legacy RENAME TO accounting_attempts`,
		`CREATE INDEX accounting_attempts_request_idx ON accounting_attempts(request_id, started_at)`,
		`CREATE INDEX accounting_attempts_account_idx ON accounting_attempts(account_id, started_at)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
}

func assertSchemaContains(t *testing.T, db *sql.DB, table, fragment string, want bool) {
	t.Helper()
	var definition string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	if got := strings.Contains(normalizePriceSQL(definition), fragment); got != want {
		t.Fatalf("schema %s fragment present=%v, want %v: %s", table, got, want, definition)
	}
}

func assertForeignKeysEnabled(t *testing.T, db *sql.DB) {
	t.Helper()
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign_keys=%d err=%v", enabled, err)
	}
}

func TestAccountingV2StrictSchemaRejectsFakeIndexTriggerAndMissingCheck(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		mutate func(*testing.T, *sql.DB)
	}{
		{"fake-index", func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`DROP INDEX accounting_usage_events_finished_idx`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE INDEX accounting_usage_events_finished_idx ON accounting_usage_events(id,finished_at)`); err != nil {
				t.Fatal(err)
			}
		}},
		{"fake-trigger", func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`DROP TRIGGER accounting_usage_events_no_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TRIGGER accounting_usage_events_no_update BEFORE INSERT ON accounting_usage_events BEGIN SELECT RAISE(ABORT,'accounting v2 facts are immutable'); END`); err != nil {
				t.Fatal(err)
			}
		}},
		{"disabled-trigger", func(t *testing.T, db *sql.DB) {
			if _, err := db.Exec(`DROP TRIGGER accounting_usage_events_no_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TRIGGER accounting_usage_events_no_update BEFORE UPDATE ON accounting_usage_events WHEN 0 BEGIN SELECT RAISE(ABORT,'accounting v2 facts are immutable'); END`); err != nil {
				t.Fatal(err)
			}
		}},
		{"missing-late-check", func(t *testing.T, db *sql.DB) {
			var ddl string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='accounting_usage_corrections'`).Scan(&ddl); err != nil {
				t.Fatal(err)
			}
			changed := strings.Replace(ddl, `CHECK(reasoning_delta IS NULL OR reasoning_set IS NULL)`, `CHECK(1)`, 1)
			if changed == ddl {
				t.Fatal("did not locate late correction check")
			}
			if _, err := db.Exec(`DROP TRIGGER accounting_usage_corrections_no_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TRIGGER accounting_usage_corrections_no_delete`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TABLE accounting_usage_corrections`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(changed); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "strict.db"))
			defer db.Close()
			if err := ledger.MigrateV2(context.Background()); err != nil {
				t.Fatal(err)
			}
			testCase.mutate(t, db)
			if err := ledger.MigrateV2(context.Background()); err == nil {
				t.Fatal("lookalike schema accepted")
			}
		})
	}
}

func seedAccountingV2FinishedAttempt(t *testing.T, ledger *Ledger, suffix string) (UsageEvent, time.Time) {
	t.Helper()
	started := testStart.UTC()
	request := RequestStart{ID: "request-" + suffix, EmployeeID: "employee-v2", KeyID: "key-v2", ModelID: "public-model", Provider: ProviderOpenAICompatible, StartedAt: started}
	price := testPrice
	attempt := AttemptStart{ID: "attempt-" + suffix, RequestID: request.ID, AccountID: "account-v2", Provider: request.Provider, Dispatch: DispatchPrimary, StartedAt: started, Price: &price, Protocol: ProtocolOpenAIResponses, EffectiveModel: "actual-model", Evidence: EvidenceProviderResponse}
	if err := ledger.BeginRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkAttemptDispatched(context.Background(), AttemptDispatch{ID: attempt.ID, OperationID: "dispatch-" + suffix, DispatchedAt: started.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	one := int64(1)
	if err := ledger.FinishAttempt(context.Background(), AttemptFinish{ID: attempt.ID, Status: StatusSucceeded, FinishedAt: started.Add(2 * time.Second), Usage: Usage{InputTokens: &one, OutputTokens: &one, CacheReadTokens: &one, CacheWriteTokens: &one}, ReliableUsage: true}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequest(context.Background(), RequestFinish{ID: request.ID, Status: StatusSucceeded, FinishedAt: started.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	base, err := loadBaseEvent(context.Background(), ledger.db, attempt.ID)
	if err != nil {
		t.Fatal(err)
	}
	return base, started
}

var _ EventRecorder = (*Ledger)(nil)
var _ TransactionalEventRecorder = (*Ledger)(nil)
