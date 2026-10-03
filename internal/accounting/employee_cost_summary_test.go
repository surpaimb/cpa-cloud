// Independently authored tests for docs/employee-self-upstream-estimated-cost-summary-contract.md.
package accounting

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newEmployeeCostLedger(t *testing.T) (*Ledger, time.Time, time.Time) {
	t.Helper()
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "employee-cost.db"))
	t.Cleanup(func() { _ = db.Close() })
	if err := ledger.MigrateV2(context.Background()); err != nil {
		t.Fatal(err)
	}
	to := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	return ledger, to.Add(-24 * time.Hour), to
}

func beginEmployeeCost(t *testing.T, ledger *Ledger, requestID, attemptID, employeeID string, at time.Time, price *PriceSnapshot, protocol UsageProtocol) {
	t.Helper()
	ctx := context.Background()
	provider := ProviderOpenAICompatible
	if protocol == ProtocolAnthropicMessages {
		provider = ProviderAnthropic
	}
	if protocol == ProtocolGeminiGenerateContent {
		provider = ProviderGemini
	}
	if err := ledger.BeginRequest(ctx, RequestStart{ID: requestID, EmployeeID: employeeID, KeyID: "employee-key", ModelID: "model", Provider: provider, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	attempt := AttemptStart{ID: attemptID, RequestID: requestID, AccountID: "account", Provider: provider, Dispatch: DispatchPrimary, StartedAt: at, Price: price}
	if protocol != "" {
		attempt.Protocol, attempt.EffectiveModel, attempt.Evidence = protocol, "actual-model", EvidenceProviderResponse
	}
	if err := ledger.BeginAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
}

func finishEmployeeCost(t *testing.T, ledger *Ledger, requestID, attemptID string, at time.Time, usage Usage, reliable bool) {
	t.Helper()
	ctx := context.Background()
	if reliable {
		if err := ledger.MarkAttemptDispatched(ctx, AttemptDispatch{ID: attemptID, OperationID: "dispatch-" + attemptID, DispatchedAt: at.Add(time.Nanosecond)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ledger.FinishAttempt(ctx, AttemptFinish{ID: attemptID, Status: StatusSucceeded, FinishedAt: at.Add(time.Second), Usage: usage, ReliableUsage: reliable}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequest(ctx, RequestFinish{ID: requestID, Status: StatusSucceeded, FinishedAt: at.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeCostSummaryKnownLegacyUnknownNullPendingAndFrozenCorrection(t *testing.T) {
	ledger, from, to := newEmployeeCostLedger(t)
	price := testPrice
	oneMillion := int64(1_000_000)
	zero := int64(0)
	knownAt := from.Add(time.Minute)
	beginEmployeeCost(t, ledger, "known-request", "known-attempt", "employee", knownAt, &price, ProtocolOpenAIChatCompletions)
	finishEmployeeCost(t, ledger, "known-request", "known-attempt", knownAt, Usage{&oneMillion, &oneMillion, &oneMillion, &oneMillion}, true)
	base, err := loadBaseEvent(context.Background(), ledger.db, "known-attempt")
	if err != nil {
		t.Fatal(err)
	}
	delta := int64(20)
	if err := ledger.AppendCorrection(context.Background(), Correction{ID: "known-correction", AttemptID: "known-attempt", TargetEventID: base.ID, OperationID: "known-correction-op", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: knownAt.Add(3 * time.Second), Currency: price.Currency, OutputTokens: CorrectionValue{Delta: &oneMillion}, EstimatedCostDeltaMicro: &delta}); err != nil {
		t.Fatal(err)
	}
	// An old row can contain cost_micro, but without a V2 proof chain it is unknown.
	legacyAt := from.Add(2 * time.Minute)
	beginEmployeeCost(t, ledger, "legacy-request", "legacy-attempt", "employee", legacyAt, &price, "")
	finishEmployeeCost(t, ledger, "legacy-request", "legacy-attempt", legacyAt, Usage{&oneMillion, &zero, &zero, &zero}, false)
	unpricedAt := from.Add(3 * time.Minute)
	beginEmployeeCost(t, ledger, "unpriced-request", "unpriced-attempt", "employee", unpricedAt, nil, "")
	finishEmployeeCost(t, ledger, "unpriced-request", "unpriced-attempt", unpricedAt, Usage{}, false)
	beginEmployeeCost(t, ledger, "pending-request", "pending-attempt", "employee", from.Add(4*time.Minute), &price, ProtocolOpenAIChatCompletions)
	beginEmployeeCost(t, ledger, "other-request", "other-attempt", "other", from.Add(5*time.Minute), &price, "")
	finishEmployeeCost(t, ledger, "other-request", "other-attempt", from.Add(5*time.Minute), Usage{&oneMillion, &zero, &zero, &zero}, false)
	beginEmployeeCost(t, ledger, "outside-request", "outside-attempt", "employee", to, &price, "")
	finishEmployeeCost(t, ledger, "outside-request", "outside-attempt", to, Usage{}, false)

	summary, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Total != 4 || summary.Pending != 1 || summary.Terminal != 3 || len(summary.Groups) != 2 {
		t.Fatalf("counts=%+v", summary)
	}
	if usd := summary.Groups[0]; usd.Currency == nil || *usd.Currency != "USD" || usd.Attempts != 2 || usd.KnownMicro != 120 || usd.UnknownCount != 1 {
		t.Fatalf("USD group=%+v", usd)
	}
	if unknown := summary.Groups[1]; unknown.Currency != nil || unknown.Attempts != 1 || unknown.KnownMicro != 0 || unknown.UnknownCount != 1 {
		t.Fatalf("unpriced group=%+v", unknown)
	}
	// The current catalog cannot retroactively change the frozen attempt price.
	price.InputPerMillionMicro = math.MaxInt64
	again, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to)
	if err != nil || again.Groups[0].KnownMicro != 120 {
		t.Fatalf("frozen read=%+v err=%v", again, err)
	}
}

func TestEmployeeCostSummaryEmbeddingsInputOnlyAndMalformedProofFailsClosed(t *testing.T) {
	ledger, from, to := newEmployeeCostLedger(t)
	price := testPrice
	oneMillion := int64(1_000_000)
	at := from.Add(time.Second)
	beginEmployeeCost(t, ledger, "embed-request", "embed-attempt", "employee", at, &price, ProtocolOpenAIEmbeddings)
	finishEmployeeCost(t, ledger, "embed-request", "embed-attempt", at, Usage{InputTokens: &oneMillion}, true)
	summary, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to)
	if err != nil || len(summary.Groups) != 1 || summary.Groups[0].KnownMicro != 10 || summary.Groups[0].UnknownCount != 0 {
		t.Fatalf("embedding input-only=%+v err=%v", summary, err)
	}
	// Immutable event rows cannot be updated, but a mismatched durable chain
	// can be created by ending an attempt without a reliable event.
	badAt := from.Add(2 * time.Second)
	beginEmployeeCost(t, ledger, "bad-request", "bad-attempt", "employee", badAt, &price, ProtocolOpenAIChatCompletions)
	if err := ledger.MarkAttemptDispatched(context.Background(), AttemptDispatch{ID: "bad-attempt", OperationID: "bad-dispatch", DispatchedAt: badAt.Add(time.Nanosecond)}); err != nil {
		t.Fatal(err)
	}
	finishEmployeeCost(t, ledger, "bad-request", "bad-attempt", badAt, Usage{}, false)
	if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
		t.Fatal("partial V2 chain accepted")
	}
}

func TestEmployeeCostSummarySchemaFailureAndCancellation(t *testing.T) {
	ledger, from, to := newEmployeeCostLedger(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ledger.ReadEmployeeEstimatedCost(ctx, "employee", from, to); err == nil {
		t.Fatal("cancelled read accepted")
	}
	if _, err := ledger.db.Exec(`DROP INDEX accounting_usage_corrections_attempt_idx`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
		t.Fatal("missing V2 index accepted")
	}
}

func TestEmployeeCostSummaryProtocolFamiliesKnownZeroAndNanosecondWindow(t *testing.T) {
	ledger, from, to := newEmployeeCostLedger(t)
	price := testPrice
	oneMillion, zero := int64(1_000_000), int64(0)
	for index, protocol := range []UsageProtocol{ProtocolOpenAIChatCompletions, ProtocolOpenAIResponses, ProtocolAnthropicMessages, ProtocolGeminiGenerateContent, ProtocolOpenAIEmbeddings} {
		id := "family-" + string(rune('a'+index))
		at := from.Add(time.Duration(index+1) * time.Nanosecond)
		beginEmployeeCost(t, ledger, id+"-request", id+"-attempt", "employee", at, &price, protocol)
		usage := Usage{InputTokens: &oneMillion}
		if protocol != ProtocolOpenAIEmbeddings {
			usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens = &oneMillion, &oneMillion, &oneMillion
		}
		finishEmployeeCost(t, ledger, id+"-request", id+"-attempt", at, usage, true)
	}
	beginEmployeeCost(t, ledger, "zero-request", "zero-attempt", "employee", from.Add(time.Minute), &price, ProtocolOpenAIChatCompletions)
	finishEmployeeCost(t, ledger, "zero-request", "zero-attempt", from.Add(time.Minute), Usage{&zero, &zero, &zero, &zero}, true)
	beginEmployeeCost(t, ledger, "before-request", "before-attempt", "employee", from.Add(-time.Nanosecond), &price, "")
	finishEmployeeCost(t, ledger, "before-request", "before-attempt", from.Add(-time.Nanosecond), Usage{}, false)
	beginEmployeeCost(t, ledger, "after-request", "after-attempt", "employee", to, &price, "")
	finishEmployeeCost(t, ledger, "after-request", "after-attempt", to, Usage{}, false)
	summary, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to)
	if err != nil || summary.Total != 6 || summary.Terminal != 6 || len(summary.Groups) != 1 || summary.Groups[0].KnownMicro != 410 || summary.Groups[0].UnknownCount != 0 {
		t.Fatalf("protocol and boundary summary=%+v err=%v", summary, err)
	}
}

func TestEmployeeCostSummaryMalformedCorrectionAndAttemptType(t *testing.T) {
	ledger, from, to := newEmployeeCostLedger(t)
	price := testPrice
	oneMillion, zero := int64(1_000_000), int64(0)
	at := from.Add(time.Minute)
	beginEmployeeCost(t, ledger, "corrupt-request", "corrupt-attempt", "employee", at, &price, ProtocolOpenAIResponses)
	finishEmployeeCost(t, ledger, "corrupt-request", "corrupt-attempt", at, Usage{&oneMillion, &zero, &zero, &zero}, true)
	base, err := loadBaseEvent(context.Background(), ledger.db, "corrupt-attempt")
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.db.Exec(`INSERT INTO accounting_usage_corrections(id,attempt_id,target_event_id,operation_id,sequence,actor,reason,corrected_at,currency,input_delta,estimated_cost_delta_micro)
		VALUES(?,?,?,?,1,'admin','admin_reconciliation',?,'USD',?,?)`, "bad-correction", "corrupt-attempt", base.ID, "bad-operation", at.Add(3*time.Second).Format(time.RFC3339Nano), oneMillion, int64(1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
		t.Fatal("incorrect correction delta accepted")
	}
	if _, err := ledger.db.Exec(`DELETE FROM accounting_usage_corrections WHERE id='bad-correction'`); err == nil {
		t.Fatal("immutable correction unexpectedly deleted")
	}
	// A wrong SQLite storage class must be rejected, even when a scan could coerce it.
	if _, err := ledger.db.Exec(`UPDATE accounting_attempts SET input_rate='not-integer' WHERE id='corrupt-attempt'`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
		t.Fatal("wrong rate type accepted")
	}
}

func TestEmployeeCostSummaryLimitRejectsRatherThanTruncates(t *testing.T) {
	ledger, from, to := newEmployeeCostLedger(t)
	started := from.Add(time.Minute).Format(time.RFC3339Nano)
	if _, err := ledger.db.Exec(`INSERT INTO accounting_requests(id,employee_id,key_id,model_id,provider,started_at,status) VALUES('mass-request','employee','key','model','openai',?,'pending')`, started); err != nil {
		t.Fatal(err)
	}
	_, err := ledger.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x<=250000)
		INSERT INTO accounting_attempts(id,request_id,account_id,provider,dispatch,started_at,status)
		SELECT 'mass-'||x,'mass-request','account','openai','primary',?,'pending' FROM seq`, started)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err != ErrAccountingV2Limit {
		t.Fatalf("limit err=%v", err)
	}
}

func TestEmployeeCostSummaryIndexedWindowSkipsOldHistory(t *testing.T) {
	ledger, from, to := newEmployeeCostLedger(t)
	old := from.Add(-48 * time.Hour).Format(time.RFC3339Nano)
	if _, err := ledger.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x<10000)
		INSERT INTO accounting_requests(id,employee_id,key_id,model_id,provider,started_at,status)
		SELECT 'old-request-'||x,'employee','key','model','openai',?,'pending' FROM seq`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.db.Exec(`WITH RECURSIVE seq(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM seq WHERE x<10000)
		INSERT INTO accounting_attempts(id,request_id,account_id,provider,dispatch,started_at,status)
		SELECT 'old-attempt-'||x,'old-request-'||x,'account','openai','primary',?,'pending' FROM seq`, old); err != nil {
		t.Fatal(err)
	}
	at := from.Add(time.Nanosecond)
	beginEmployeeCost(t, ledger, "inside-request", "inside-attempt", "employee", at, nil, "")
	finishEmployeeCost(t, ledger, "inside-request", "inside-attempt", at, Usage{}, false)
	rows, err := ledger.db.Query(`EXPLAIN QUERY PLAN SELECT r.id,a.id FROM accounting_requests r INDEXED BY accounting_requests_employee_idx
		JOIN accounting_attempts a ON a.request_id=r.id WHERE r.employee_id=? AND r.started_at>=? AND r.started_at<?
		ORDER BY r.id,a.id LIMIT ?`, "employee", from.Format("2006-01-02T15:04:05"), to.Format("2006-01-02T15:04:05"), MaxAccountingV2ReportAttempts+1)
	if err != nil {
		t.Fatal(err)
	}
	usedIndex := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		usedIndex = usedIndex || strings.Contains(detail, "accounting_requests_employee_idx") && strings.Contains(detail, "SEARCH")
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if !usedIndex {
		t.Fatal("window query did not use the employee/time index")
	}
	summary, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to)
	if err != nil || summary.Total != 1 || summary.Terminal != 1 || summary.Groups[0].UnknownCount != 1 {
		t.Fatalf("indexed window summary=%+v err=%v", summary, err)
	}
	if _, err := ledger.db.Exec(`DROP INDEX accounting_requests_employee_idx`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
		t.Fatal("missing base employee/time index accepted")
	}
	if _, err := ledger.db.Exec(`CREATE INDEX accounting_requests_employee_idx ON accounting_requests(employee_id,status)`); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
		t.Fatal("same-name index with wrong columns accepted")
	}
}

func TestEmployeeCostSummaryRejectsPartialPreDispatchV2AndAcceptsSystemRecovery(t *testing.T) {
	for _, status := range []Status{StatusFailed, StatusCancelled, StatusInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			ledger, from, to := newEmployeeCostLedger(t)
			at := from.Add(time.Minute)
			beginEmployeeCost(t, ledger, "partial-request", "partial-attempt", "employee", at, nil, ProtocolOpenAIChatCompletions)
			if err := ledger.FinishAttempt(context.Background(), AttemptFinish{ID: "partial-attempt", Status: status, FinishedAt: at.Add(time.Second)}); err != nil {
				t.Fatal(err)
			}
			if err := ledger.FinishRequest(context.Background(), RequestFinish{ID: "partial-request", Status: status, FinishedAt: at.Add(2 * time.Second)}); err != nil {
				t.Fatal(err)
			}
			if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
				t.Fatal("context-only partial V2 terminal accepted")
			}
		})
	}
	ledger, from, to := newEmployeeCostLedger(t)
	at := from.Add(time.Minute)
	beginEmployeeCost(t, ledger, "recovered-request", "recovered-attempt", "employee", at, nil, ProtocolOpenAIChatCompletions)
	if err := ledger.MarkAttemptDispatched(context.Background(), AttemptDispatch{ID: "recovered-attempt", OperationID: "recovered-dispatch", DispatchedAt: at.Add(time.Nanosecond)}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishAttempt(context.Background(), AttemptFinish{ID: "recovered-attempt", Status: StatusInterrupted, FinishedAt: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequest(context.Background(), RequestFinish{ID: "recovered-request", Status: StatusInterrupted, FinishedAt: at.Add(2 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	tx, err := ledger.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.RecoverV2Tx(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	summary, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to)
	if err != nil || summary.Total != 1 || summary.Groups[0].UnknownCount != 1 {
		t.Fatalf("recovered unknown=%+v err=%v", summary, err)
	}
}

func TestEmployeeCostSummaryRejectsOrphanAndParentChildContradictions(t *testing.T) {
	t.Run("orphan", func(t *testing.T) {
		ledger, from, to := newEmployeeCostLedger(t)
		conn, err := ledger.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=OFF`); err != nil {
			conn.Close()
			t.Fatal(err)
		}
		_, err = conn.ExecContext(context.Background(), `INSERT INTO accounting_attempts(id,request_id,account_id,provider,dispatch,started_at,status)
			VALUES('orphan-attempt','absent-request','account','openai','primary',?,'pending')`, from.Add(time.Minute).Format(time.RFC3339Nano))
		if _, restoreErr := conn.ExecContext(context.Background(), `PRAGMA foreign_keys=ON`); restoreErr != nil {
			conn.Close()
			t.Fatal(restoreErr)
		}
		if err := conn.Close(); err != nil {
			t.Fatal(err)
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
			t.Fatal("orphan attempt hidden by inner join")
		}
	})
	t.Run("terminal-parent-pending-child", func(t *testing.T) {
		ledger, from, to := newEmployeeCostLedger(t)
		at := from.Add(time.Minute)
		beginEmployeeCost(t, ledger, "parent-request", "pending-child", "employee", at, nil, "")
		if _, err := ledger.db.Exec(`UPDATE accounting_requests SET status='failed',finished_at=? WHERE id='parent-request'`, at.Add(time.Second).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
			t.Fatal("terminal parent with pending child accepted")
		}
	})
	t.Run("child-after-parent", func(t *testing.T) {
		ledger, from, to := newEmployeeCostLedger(t)
		at := from.Add(time.Minute)
		beginEmployeeCost(t, ledger, "parent-request", "finished-child", "employee", at, nil, "")
		finishEmployeeCost(t, ledger, "parent-request", "finished-child", at, Usage{}, false)
		if _, err := ledger.db.Exec(`UPDATE accounting_requests SET finished_at=? WHERE id='parent-request'`, at.Add(500*time.Millisecond).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
			t.Fatal("child finish after parent finish accepted")
		}
		if _, err := ledger.db.Exec(`UPDATE accounting_requests SET finished_at=? WHERE id='parent-request'`, at.Add(time.Second).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err != nil {
			t.Fatalf("equal child/parent finish rejected: %v", err)
		}
	})
}

func TestEmployeeCostSummaryRejectsOverMaximumFrozenRateEvenWithZeroUsage(t *testing.T) {
	ledger, from, to := newEmployeeCostLedger(t)
	price := testPrice
	price.InputPerMillionMicro = MaxPriceRate + 1
	zero := int64(0)
	at := from.Add(time.Minute)
	beginEmployeeCost(t, ledger, "overrate-request", "overrate-attempt", "employee", at, &price, ProtocolOpenAIChatCompletions)
	finishEmployeeCost(t, ledger, "overrate-request", "overrate-attempt", at, Usage{&zero, &zero, &zero, &zero}, true)
	if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
		t.Fatal("over-maximum frozen rate trusted as known zero")
	}
}

func TestEmployeeCostSummaryMultipleCorrectionsReplayAndSequenceGap(t *testing.T) {
	ledger, from, to := newEmployeeCostLedger(t)
	price := testPrice
	oneMillion := int64(1_000_000)
	at := from.Add(time.Minute)
	beginEmployeeCost(t, ledger, "chain-request", "chain-attempt", "employee", at, &price, ProtocolOpenAIResponses)
	finishEmployeeCost(t, ledger, "chain-request", "chain-attempt", at, Usage{&oneMillion, &oneMillion, &oneMillion, &oneMillion}, true)
	base, err := loadBaseEvent(context.Background(), ledger.db, "chain-attempt")
	if err != nil {
		t.Fatal(err)
	}
	outputDelta := int64(20)
	first := Correction{ID: "chain-correction-one", AttemptID: "chain-attempt", TargetEventID: base.ID, OperationID: "chain-operation-one", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: at.Add(3 * time.Second), Currency: price.Currency, OutputTokens: CorrectionValue{Delta: &oneMillion}, EstimatedCostDeltaMicro: &outputDelta}
	if err := ledger.AppendCorrection(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	inputDelta := int64(10)
	second := Correction{ID: "chain-correction-two", AttemptID: "chain-attempt", TargetEventID: base.ID, OperationID: "chain-operation-two", Actor: "admin", Reason: CorrectionAdminReconcile, CorrectedAt: at.Add(4 * time.Second), Currency: price.Currency, InputTokens: CorrectionValue{Delta: &oneMillion}, EstimatedCostDeltaMicro: &inputDelta}
	for range 2 {
		if err := ledger.AppendCorrection(context.Background(), second); err != nil {
			t.Fatal(err)
		}
	}
	summary, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to)
	if err != nil || summary.Groups[0].KnownMicro != 130 || summary.Groups[0].UnknownCount != 0 {
		t.Fatalf("effective corrected cost=%+v err=%v", summary, err)
	}
	if _, err := ledger.db.Exec(`INSERT INTO accounting_usage_corrections(id,attempt_id,target_event_id,operation_id,sequence,actor,reason,corrected_at,currency,input_delta,estimated_cost_delta_micro)
		VALUES(?,?,?,?,4,'admin','admin_reconciliation',?,'USD',1,0)`, "chain-correction-gap", "chain-attempt", base.ID, "chain-operation-gap", at.Add(5*time.Second).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
		t.Fatal("non-contiguous correction chain accepted")
	}
}

func TestEmployeeCostSummaryRejectsParentProviderAndSuccessContradictions(t *testing.T) {
	t.Run("provider", func(t *testing.T) {
		ledger, from, to := newEmployeeCostLedger(t)
		at := from.Add(time.Minute)
		beginEmployeeCost(t, ledger, "provider-request", "provider-attempt", "employee", at, nil, "")
		finishEmployeeCost(t, ledger, "provider-request", "provider-attempt", at, Usage{}, false)
		if _, err := ledger.db.Exec(`UPDATE accounting_attempts SET provider='anthropic' WHERE id='provider-attempt'`); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
			t.Fatal("parent/child provider mismatch accepted")
		}
	})
	t.Run("succeeded-parent-without-success", func(t *testing.T) {
		ledger, from, to := newEmployeeCostLedger(t)
		at := from.Add(time.Minute)
		beginEmployeeCost(t, ledger, "status-request", "status-attempt", "employee", at, nil, "")
		if err := ledger.FinishAttempt(context.Background(), AttemptFinish{ID: "status-attempt", Status: StatusFailed, FinishedAt: at.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.FinishRequest(context.Background(), RequestFinish{ID: "status-request", Status: StatusFailed, FinishedAt: at.Add(2 * time.Second)}); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.db.Exec(`UPDATE accounting_requests SET status='succeeded' WHERE id='status-request'`); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
			t.Fatal("succeeded parent with no succeeded attempt accepted")
		}
	})
	t.Run("succeeded-parent-without-any-attempt", func(t *testing.T) {
		ledger, from, to := newEmployeeCostLedger(t)
		at := from.Add(time.Minute)
		if err := ledger.BeginRequest(context.Background(), RequestStart{ID: "empty-success-request", EmployeeID: "employee", KeyID: "key", ModelID: "model", Provider: ProviderOpenAICompatible, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.db.Exec(`UPDATE accounting_requests SET status='succeeded',finished_at=? WHERE id='empty-success-request'`, at.Add(time.Second).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
			t.Fatal("succeeded parent without attempts accepted")
		}
	})
}

func TestEmployeeCostSummarySystemTerminalRequiresNativeContextOrDispatch(t *testing.T) {
	t.Run("forged-undispatched", func(t *testing.T) {
		ledger, from, to := newEmployeeCostLedger(t)
		at := from.Add(time.Minute)
		beginEmployeeCost(t, ledger, "forged-request", "forged-attempt", "employee", at, nil, ProtocolOpenAIChatCompletions)
		if err := ledger.FinishAttempt(context.Background(), AttemptFinish{ID: "forged-attempt", Status: StatusInterrupted, FinishedAt: at.Add(time.Second)}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.FinishRequest(context.Background(), RequestFinish{ID: "forged-request", Status: StatusInterrupted, FinishedAt: at.Add(2 * time.Second)}); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.db.Exec(`INSERT INTO accounting_usage_events(id,attempt_id,request_id,status,evidence,finished_at)
			VALUES(?,?,?,'interrupted','system_terminal',?)`, usageEventID("forged-attempt"), "forged-attempt", "forged-request", at.Add(time.Second).Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to); err == nil {
			t.Fatal("undispatched system event over provider-response context accepted")
		}
	})
	t.Run("native-undispatched", func(t *testing.T) {
		ledger, from, to := newEmployeeCostLedger(t)
		at := from.Add(time.Minute)
		if err := ledger.BeginRequest(context.Background(), RequestStart{ID: "native-request", EmployeeID: "employee", KeyID: "key", ModelID: "model", Provider: ProviderOpenAICompatible, StartedAt: at}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.BeginAttempt(context.Background(), AttemptStart{ID: "native-attempt", RequestID: "native-request", AccountID: "account", Provider: ProviderOpenAICompatible, Dispatch: DispatchPrimary, StartedAt: at, Protocol: ProtocolOpenAIChatCompletions, EffectiveModel: "actual-model", Evidence: EvidenceSystemTerminal}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.FinishAttempt(context.Background(), AttemptFinish{ID: "native-attempt", Status: StatusInterrupted, FinishedAt: at.Add(time.Second), ReliableUsage: true}); err != nil {
			t.Fatal(err)
		}
		if err := ledger.FinishRequest(context.Background(), RequestFinish{ID: "native-request", Status: StatusInterrupted, FinishedAt: at.Add(2 * time.Second)}); err != nil {
			t.Fatal(err)
		}
		summary, err := ledger.ReadEmployeeEstimatedCost(context.Background(), "employee", from, to)
		if err != nil || summary.Total != 1 || summary.Groups[0].UnknownCount != 1 {
			t.Fatalf("native system terminal=%+v err=%v", summary, err)
		}
	})
}
