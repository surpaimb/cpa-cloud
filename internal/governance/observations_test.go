package governance

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

type observationFixture struct {
	t        *testing.T
	path     string
	db       *sql.DB
	core     *Coordinator
	ledger   *accounting.Ledger
	revision int64
}

func TestQueryObservationsStableScopeAcrossRevisionsAndStates(t *testing.T) {
	fixture := newObservationFixture(t)
	defer fixture.db.Close()
	windowEnd := governanceStart.Add(2 * time.Hour)

	revisionOne := shadowObservationScope("employee-policy", 1, 10, 100, "USD")
	revisionTwo := shadowObservationScope("employee-policy", 2, 100, 40, "USD")

	fixture.admit("outside-cost", windowEnd.Add(-24*time.Hour), revisionOne)
	fixture.finishAttempt("outside-cost", accounting.StatusSucceeded, knownObservationUsage(1, 0), "USD", 500)
	fixture.admit("tpm-boundary", windowEnd.Add(-time.Minute), revisionOne)
	fixture.finishAttempt("tpm-boundary", accounting.StatusSucceeded, knownObservationUsage(1, 0), "USD", 5)
	fixture.admit("known-usd", windowEnd.Add(-30*time.Second), revisionOne)
	fixture.finishAttempt("known-usd", accounting.StatusSucceeded, knownObservationUsage(1, 11), "USD", 50)

	fixture.setEnabled(true, windowEnd.Add(-25*time.Second))
	fixture.admit("known-eur", windowEnd.Add(-20*time.Second), revisionTwo)
	fixture.finishAttempt("known-eur", accounting.StatusFailed, knownObservationUsage(1, 3), "EUR", 30)
	fixture.admit("unknown", windowEnd.Add(-10*time.Second), revisionTwo)
	fixture.finishAttempt("unknown", accounting.StatusCancelled, accounting.Usage{}, "USD", 0)
	fixture.admit("pending-attempt", windowEnd.Add(-8*time.Second), revisionTwo)
	fixture.beginPendingAttempt("pending-attempt", "USD")
	fixture.admit("zero-attempt", windowEnd.Add(-5*time.Second), revisionTwo)
	fixture.finishGovernanceOnly("zero-attempt", accounting.StatusSucceeded)
	fixture.admit("pending-no-attempt", windowEnd.Add(-time.Second), revisionTwo)

	page := fixture.query(ObservationQuery{Limit: 10, ObservedAt: windowEnd})
	if page.WindowEnd != formatTime(windowEnd) || page.TPMFrom != formatTime(windowEnd.Add(-time.Minute)) || page.CostFrom != formatTime(windowEnd.Add(-24*time.Hour)) {
		t.Fatalf("window=%+v", page)
	}
	if len(page.Items) != 2 || page.NextCursor != "" {
		t.Fatalf("items=%d cursor=%q", len(page.Items), page.NextCursor)
	}
	first, second := page.Items[0], page.Items[1]
	if first.Snapshot.PolicyRevision != "1" || first.Snapshot.SettingsRevision != "2" || second.Snapshot.PolicyRevision != "2" || second.Snapshot.SettingsRevision != "3" {
		t.Fatalf("snapshots=%+v %+v", first.Snapshot, second.Snapshot)
	}
	assertSameObservationTotals(t, first.ScopeTotals, second.ScopeTotals)
	assertTPMTotals(t, first.ScopeTotals.TPM, "16", "2", "1", "1", "2", "1", "1")
	assertCostTotals(t, first.ScopeTotals.Cost, "3", "1", "1", "2", "1", "1", map[string][2]string{
		"EUR": {"30", "1"}, "USD": {"55", "2"},
	})
	if first.Interpretation.TPMState == nil || *first.Interpretation.TPMState != ObservationExceeded ||
		first.Interpretation.CostState == nil || *first.Interpretation.CostState != ObservationUnknown ||
		first.Interpretation.IncomparableCurrencyAttempts == nil || *first.Interpretation.IncomparableCurrencyAttempts != "1" {
		t.Fatalf("first interpretation=%+v", first.Interpretation)
	}
	if second.Interpretation.TPMState == nil || *second.Interpretation.TPMState != ObservationUnknown ||
		second.Interpretation.CostState == nil || *second.Interpretation.CostState != ObservationExceeded ||
		second.Interpretation.IncomparableCurrencyAttempts == nil || *second.Interpretation.IncomparableCurrencyAttempts != "1" {
		t.Fatalf("second interpretation=%+v", second.Interpretation)
	}

	filtered := fixture.query(ObservationQuery{Limit: 10, PolicyID: "employee-policy", ObservedAt: windowEnd})
	if len(filtered.Items) != 2 {
		t.Fatalf("policy filter items=%d", len(filtered.Items))
	}
	assertSameObservationTotals(t, first.ScopeTotals, filtered.Items[0].ScopeTotals)
}

func TestQueryObservationsPaginationStrictCursorAndHardOnlySnapshot(t *testing.T) {
	fixture := newObservationFixture(t)
	defer fixture.db.Close()
	windowEnd := governanceStart.Add(3 * time.Hour)
	hardOnly := ScopeSnapshot{Kind: ScopeKey, ID: "key-1", PolicyID: "a-hard", PolicyRevision: 1, RPMLimit: int64Pointer(100)}
	shadowB := shadowKeyObservationScope("b-shadow", 1, 10)
	shadowC := shadowKeyObservationScope("c-shadow", 1, 20)
	fixture.admit("hard", windowEnd.Add(-3*time.Second), hardOnly)
	fixture.finishAttempt("hard", accounting.StatusSucceeded, knownObservationUsage(1, 0), "", 0)
	fixture.admit("shadow-b", windowEnd.Add(-2*time.Second), shadowB)
	fixture.finishAttempt("shadow-b", accounting.StatusSucceeded, knownObservationUsage(2, 0), "", 0)
	fixture.admit("shadow-c", windowEnd.Add(-time.Second), shadowC)
	fixture.finishAttempt("shadow-c", accounting.StatusSucceeded, knownObservationUsage(3, 0), "", 0)

	first := fixture.query(ObservationQuery{Limit: 1, ScopeKind: ScopeKey, ObservedAt: windowEnd})
	if len(first.Items) != 1 || first.Items[0].Snapshot.PolicyID != "a-hard" || first.NextCursor == "" {
		t.Fatalf("first=%+v", first)
	}
	if first.Items[0].Interpretation.TPMState != nil || first.Items[0].Interpretation.CostState != nil ||
		first.Items[0].Interpretation.IncomparableCurrencyAttempts != nil {
		t.Fatalf("hard-only interpretation=%+v", first.Items[0].Interpretation)
	}
	assertTPMTotals(t, first.Items[0].ScopeTotals.TPM, "6", "3", "0", "0", "0", "0", "0")

	second := fixture.query(ObservationQuery{Limit: 1, ScopeKind: ScopeKey, Cursor: first.NextCursor, ObservedAt: windowEnd.Add(time.Second)})
	if len(second.Items) != 1 || second.Items[0].Snapshot.PolicyID != "b-shadow" || second.WindowEnd != first.WindowEnd ||
		second.ObservedAt != formatTime(windowEnd.Add(time.Second)) || second.NextCursor == "" {
		t.Fatalf("second=%+v", second)
	}
	third := fixture.query(ObservationQuery{Limit: 1, ScopeKind: ScopeKey, Cursor: second.NextCursor, ObservedAt: windowEnd.Add(2 * time.Second)})
	if len(third.Items) != 1 || third.Items[0].Snapshot.PolicyID != "c-shadow" || third.NextCursor != "" {
		t.Fatalf("third=%+v", third)
	}

	assertObservationQueryError(t, fixture.core, context.Background(), ObservationQuery{
		Limit: 1, ScopeKind: ScopeEmployee, Cursor: first.NextCursor, ObservedAt: windowEnd,
	}, ObservationInvalidCursor)
	assertObservationQueryError(t, fixture.core, context.Background(), ObservationQuery{
		Limit: 1, ScopeID: "key-1", ObservedAt: windowEnd,
	}, ObservationInvalidQuery)
	assertObservationQueryError(t, fixture.core, context.Background(), ObservationQuery{
		Limit: 1, ScopeKind: ScopeKey, Cursor: mutateObservationCursor(t, first.NextCursor, `,"unknown":1`), ObservedAt: windowEnd,
	}, ObservationInvalidCursor)
	assertObservationQueryError(t, fixture.core, context.Background(), ObservationQuery{
		Limit: 1, ScopeKind: ScopeKey, Cursor: mutateObservationCursor(t, first.NextCursor, `,"v":1`), ObservedAt: windowEnd,
	}, ObservationInvalidCursor)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	assertObservationQueryError(t, fixture.core, cancelled, ObservationQuery{Limit: 10, ObservedAt: windowEnd}, ObservationUnavailable)

	longFilter := strings.Repeat(`\`, 200)
	longKey := strings.Repeat(`"`, 256)
	longQuery := ObservationQuery{Limit: 100, ScopeKind: ScopeEmployee, ScopeID: longFilter, PolicyID: longFilter, ObservedAt: windowEnd}
	longCursor, err := encodeObservationCursor(longQuery, windowEnd, observationKey{scopeKind: ScopeEmployee, scopeID: longKey,
		policyID: longKey, policyRevision: MaxRevision, settingsRevision: MaxRevision})
	if err != nil || len(longCursor) > observationCursorLimit {
		t.Fatalf("worst cursor bytes=%d err=%v", len(longCursor), err)
	}
	rawCursor, err := base64.RawURLEncoding.DecodeString(longCursor)
	if err != nil || len(rawCursor)+33 > 1536 {
		t.Fatalf("worst raw cursor bytes=%d err=%v", len(rawCursor), err)
	}
	if _, err := decodeObservationCursor(longCursor, longQuery); err != nil {
		t.Fatalf("worst cursor decode: %v", err)
	}
}

func TestObservationPendingParentWithFinishedAttemptsStaysUnknown(t *testing.T) {
	fixture := newObservationFixture(t)
	defer fixture.db.Close()
	windowEnd := governanceStart.Add(7 * time.Hour)
	requestTime := windowEnd.Add(-time.Second)
	fixture.admit("pending-parent", requestTime, shadowObservationScope("policy", 1, 100, 100, "USD"))
	fixture.beginAccountingRequest("pending-parent", requestTime)
	fixture.finishNamedAttempt("pending-parent:first", "pending-parent", requestTime, accounting.StatusSucceeded,
		knownObservationUsage(2, 0), "USD", 10)
	fixture.finishNamedAttempt("pending-parent:second", "pending-parent", requestTime.Add(time.Nanosecond), accounting.StatusFailed,
		knownObservationUsage(3, 0), "USD", 20)

	pending := fixture.query(ObservationQuery{Limit: 10, ObservedAt: windowEnd})
	if len(pending.Items) != 1 {
		t.Fatalf("pending items=%d", len(pending.Items))
	}
	item := pending.Items[0]
	assertTPMTotals(t, item.ScopeTotals.TPM, "5", "2", "0", "0", "1", "0", "0")
	assertCostTotals(t, item.ScopeTotals.Cost, "2", "0", "0", "1", "0", "0", map[string][2]string{"USD": {"30", "2"}})
	if item.Interpretation.TPMState == nil || *item.Interpretation.TPMState != ObservationUnknown ||
		item.Interpretation.CostState == nil || *item.Interpretation.CostState != ObservationUnknown {
		t.Fatalf("pending interpretation=%+v", item.Interpretation)
	}

	fixture.finishAccountingAndGovernance("pending-parent", accounting.StatusSucceeded, requestTime.Add(2*time.Second))
	finished := fixture.query(ObservationQuery{Limit: 10, ObservedAt: windowEnd})
	if len(finished.Items) != 1 {
		t.Fatalf("finished items=%d", len(finished.Items))
	}
	item = finished.Items[0]
	assertTPMTotals(t, item.ScopeTotals.TPM, "5", "2", "0", "0", "0", "0", "0")
	assertCostTotals(t, item.ScopeTotals.Cost, "2", "0", "0", "0", "0", "0", map[string][2]string{"USD": {"30", "2"}})
	if item.Interpretation.TPMState == nil || *item.Interpretation.TPMState != ObservationBelow ||
		item.Interpretation.CostState == nil || *item.Interpretation.CostState != ObservationBelow {
		t.Fatalf("finished interpretation=%+v", item.Interpretation)
	}
}

func TestObservationRejectsAccountingParentChildMismatch(t *testing.T) {
	failed, pending := accounting.StatusFailed, accounting.StatusPending
	for _, test := range []struct {
		name      string
		parent    accounting.Status
		attempt   *accounting.Status
		requestID string
	}{
		{name: "succeeded-zero-attempt", parent: accounting.StatusSucceeded, requestID: "succeeded-zero"},
		{name: "succeeded-only-failed-attempt", parent: accounting.StatusSucceeded, attempt: &failed, requestID: "succeeded-failed"},
		{name: "terminal-with-pending-attempt", parent: accounting.StatusFailed, attempt: &pending, requestID: "failed-pending"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newObservationFixture(t)
			defer fixture.db.Close()
			at := governanceStart.Add(7*time.Hour + time.Minute)
			fixture.admit(test.requestID, at, shadowObservationScope("policy", 1, 100, 0, ""))
			fixture.beginAccountingRequest(test.requestID, at)
			if test.attempt != nil {
				if *test.attempt == accounting.StatusPending {
					price := observationPrice("", 0, 1)
					if err := fixture.ledger.BeginAttempt(context.Background(), accounting.AttemptStart{
						ID: test.requestID + ":attempt", RequestID: test.requestID, AccountID: "account-1",
						Provider: accounting.ProviderOpenAICompatible, Dispatch: accounting.DispatchPrimary, StartedAt: at, Price: price,
					}); err != nil {
						t.Fatal(err)
					}
				} else {
					fixture.finishNamedAttempt(test.requestID+":attempt", test.requestID, at, *test.attempt, knownObservationUsage(1, 0), "", 0)
				}
			}
			finishedAt := at.Add(2 * time.Second)
			if err := fixture.ledger.FinishRequest(context.Background(), accounting.RequestFinish{
				ID: test.requestID, Status: test.parent, FinishedAt: finishedAt,
			}); !errors.Is(err, accounting.ErrConflict) {
				t.Fatalf("Ledger accepted invalid parent/child state: %v", err)
			}
			if _, err := fixture.db.Exec(`UPDATE accounting_requests SET status=?,finished_at=? WHERE id=?`,
				string(test.parent), finishedAt.Format(time.RFC3339Nano), test.requestID); err != nil {
				t.Fatal(err)
			}
			if err := runFinish(t, fixture.db, fixture.core, Finish{RequestID: test.requestID, Status: test.parent, FinishedAt: finishedAt}); err != nil {
				t.Fatal(err)
			}
			assertObservationQueryError(t, fixture.core, context.Background(), ObservationQuery{
				Limit: 10, ObservedAt: at.Add(3 * time.Second),
			}, ObservationSchema)
		})
	}
}

func TestObservationAllowsFailedAccountingParentWithoutAttempt(t *testing.T) {
	fixture := newObservationFixture(t)
	defer fixture.db.Close()
	at := governanceStart.Add(7*time.Hour + 2*time.Minute)
	fixture.admit("failed-zero", at, shadowObservationScope("policy", 1, 100, 0, ""))
	fixture.beginAccountingRequest("failed-zero", at)
	fixture.finishAccountingAndGovernance("failed-zero", accounting.StatusFailed, at.Add(time.Second))

	page := fixture.query(ObservationQuery{Limit: 10, ObservedAt: at.Add(2 * time.Second)})
	if len(page.Items) != 1 {
		t.Fatalf("items=%d", len(page.Items))
	}
	assertTPMTotals(t, page.Items[0].ScopeTotals.TPM, "0", "0", "0", "0", "0", "0", "1")
	if page.Items[0].Interpretation.TPMState == nil || *page.Items[0].Interpretation.TPMState != ObservationBelow {
		t.Fatalf("interpretation=%+v", page.Items[0].Interpretation)
	}
}

func TestObservationMultiScopeLateSettlementAndRecovery(t *testing.T) {
	t.Run("multi-scope", func(t *testing.T) {
		fixture := newObservationFixture(t)
		defer fixture.db.Close()
		windowEnd := governanceStart.Add(8 * time.Hour)
		employee := shadowObservationScope("employee-policy", 1, 100, 0, "")
		groupOne := shadowGroupObservationScope("group-policy", 1, 100)
		groupTwo := shadowGroupObservationScope("group-policy", 2, 100)
		fixture.admit("multi-one", windowEnd.Add(-2*time.Second), employee, groupOne)
		fixture.finishAttempt("multi-one", accounting.StatusSucceeded, knownObservationUsage(2, 0), "", 0)
		fixture.admit("multi-two", windowEnd.Add(-time.Second), groupTwo)
		fixture.finishAttempt("multi-two", accounting.StatusSucceeded, knownObservationUsage(3, 0), "", 0)

		page := fixture.query(ObservationQuery{Limit: 10, ObservedAt: windowEnd})
		if len(page.Items) != 3 {
			t.Fatalf("items=%d", len(page.Items))
		}
		var employeeItem ObservationItem
		groupItems := make([]ObservationItem, 0, 2)
		for _, item := range page.Items {
			if item.Snapshot.ScopeKind == ScopeEmployee {
				employeeItem = item
			} else if item.Snapshot.ScopeKind == ScopeGroup {
				groupItems = append(groupItems, item)
			}
		}
		assertTPMTotals(t, employeeItem.ScopeTotals.TPM, "2", "1", "0", "0", "0", "0", "0")
		if len(groupItems) != 2 || groupItems[0].Snapshot.GroupRevision == nil || *groupItems[0].Snapshot.GroupRevision != "1" ||
			groupItems[1].Snapshot.GroupRevision == nil || *groupItems[1].Snapshot.GroupRevision != "2" {
			t.Fatalf("group items=%+v", groupItems)
		}
		assertSameObservationTotals(t, groupItems[0].ScopeTotals, groupItems[1].ScopeTotals)
		assertTPMTotals(t, groupItems[0].ScopeTotals.TPM, "5", "2", "0", "0", "0", "0", "0")
	})

	t.Run("late-page-settlement", func(t *testing.T) {
		fixture := newObservationFixture(t)
		defer fixture.db.Close()
		windowEnd := governanceStart.Add(9 * time.Hour)
		fixture.admit("late-known", windowEnd.Add(-2*time.Second), shadowObservationScope("a-policy", 1, 100, 0, ""))
		fixture.finishAttempt("late-known", accounting.StatusSucceeded, knownObservationUsage(2, 0), "", 0)
		fixture.admit("late-pending", windowEnd.Add(-time.Second), shadowObservationScope("b-policy", 1, 100, 0, ""))
		fixture.beginPendingAttempt("late-pending", "")

		first := fixture.query(ObservationQuery{Limit: 1, ObservedAt: windowEnd})
		if len(first.Items) != 1 || first.NextCursor == "" {
			t.Fatalf("first=%+v", first)
		}
		assertTPMTotals(t, first.Items[0].ScopeTotals.TPM, "2", "1", "0", "1", "1", "0", "0")

		fixture.finishPendingAttempt("late-pending", accounting.StatusSucceeded, knownObservationUsage(3, 0))
		second := fixture.query(ObservationQuery{Limit: 1, Cursor: first.NextCursor, ObservedAt: windowEnd.Add(time.Second)})
		if second.WindowEnd != first.WindowEnd || second.ObservedAt == first.ObservedAt || len(second.Items) != 1 {
			t.Fatalf("second=%+v", second)
		}
		assertTPMTotals(t, second.Items[0].ScopeTotals.TPM, "5", "2", "0", "0", "0", "0", "0")
	})

	t.Run("recovery-and-idempotent-finish", func(t *testing.T) {
		fixture := newObservationFixture(t)
		windowEnd := governanceStart.Add(10 * time.Hour)
		fixture.admit("recovered", windowEnd.Add(-2*time.Second), shadowObservationScope("a-recovery", 1, 100, 0, ""))
		fixture.beginPendingAttempt("recovered", "")
		fixture.admit("idempotent", windowEnd.Add(-time.Second), shadowObservationScope("b-idempotent", 1, 100, 0, ""))
		fixture.finishAttemptTwice("idempotent", accounting.StatusFailed, knownObservationUsage(4, 0))
		if err := fixture.db.Close(); err != nil {
			t.Fatal(err)
		}
		fixture.db = openGovernanceDB(t, fixture.path, 1)
		defer fixture.db.Close()
		fixture.ledger = accounting.NewLedger(fixture.db)
		if err := fixture.ledger.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		fixture.core = newTestCoordinator(t, fixture.db)
		if err := fixture.core.Migrate(context.Background()); err != nil {
			t.Fatal(err)
		}
		if recovered, err := fixture.ledger.RecoverInterrupted(context.Background(), windowEnd); err != nil || recovered.Requests != 1 || recovered.Attempts != 1 {
			t.Fatalf("accounting recovery=%+v err=%v", recovered, err)
		}
		if recovered, err := fixture.core.RecoverInterrupted(context.Background(), windowEnd); err != nil || recovered.Interrupted != 1 {
			t.Fatalf("governance recovery=%+v err=%v", recovered, err)
		}
		if recovered, err := fixture.ledger.RecoverInterrupted(context.Background(), windowEnd.Add(time.Second)); err != nil || recovered.Requests != 0 || recovered.Attempts != 0 {
			t.Fatalf("second accounting recovery=%+v err=%v", recovered, err)
		}
		if recovered, err := fixture.core.RecoverInterrupted(context.Background(), windowEnd.Add(time.Second)); err != nil || recovered.Interrupted != 0 {
			t.Fatalf("second governance recovery=%+v err=%v", recovered, err)
		}
		page := fixture.query(ObservationQuery{Limit: 10, ObservedAt: windowEnd})
		if len(page.Items) != 2 {
			t.Fatalf("items=%d", len(page.Items))
		}
		for _, item := range page.Items {
			if item.Snapshot.PolicyID == "a-recovery" {
				assertTPMTotals(t, item.ScopeTotals.TPM, "4", "1", "1", "0", "0", "0", "0")
			}
		}
	})
}

func TestQueryObservationsRejectsMismatchAndCheckedOverflow(t *testing.T) {
	t.Run("relation", func(t *testing.T) {
		fixture := newObservationFixture(t)
		defer fixture.db.Close()
		at := governanceStart.Add(4 * time.Hour)
		fixture.admit("mismatch", at, shadowObservationScope("policy", 1, 10, 0, ""))
		fixture.finishAttempt("mismatch", accounting.StatusSucceeded, knownObservationUsage(1, 0), "", 0)
		if _, err := fixture.db.Exec(`UPDATE accounting_requests SET employee_id='different' WHERE id='mismatch'`); err != nil {
			t.Fatal(err)
		}
		assertObservationQueryError(t, fixture.core, context.Background(), ObservationQuery{Limit: 10, ObservedAt: at}, ObservationSchema)
	})

	t.Run("token", func(t *testing.T) {
		fixture := newObservationFixture(t)
		defer fixture.db.Close()
		at := governanceStart.Add(5 * time.Hour)
		fixture.admit("token-overflow", at, shadowObservationScope("policy", 1, 1, 0, ""))
		one := int64(1)
		maximum := int64(math.MaxInt64)
		zero := int64(0)
		fixture.finishAttempt("token-overflow", accounting.StatusSucceeded, accounting.Usage{
			InputTokens: &maximum, OutputTokens: &one, CacheReadTokens: &zero, CacheWriteTokens: &zero,
		}, "", 0)
		assertObservationQueryError(t, fixture.core, context.Background(), ObservationQuery{Limit: 10, ObservedAt: at}, ObservationOverflow)
	})

	t.Run("cost", func(t *testing.T) {
		fixture := newObservationFixture(t)
		defer fixture.db.Close()
		at := governanceStart.Add(6 * time.Hour)
		fixture.admit("cost-overflow", at, shadowObservationScope("policy", 1, 0, 1, "USD"))
		fixture.beginAccountingRequest("cost-overflow", at)
		million := int64(1_000_000)
		zero := int64(0)
		fixture.finishNamedAttempt("cost-overflow:max", "cost-overflow", at, accounting.StatusSucceeded,
			accounting.Usage{InputTokens: &million, OutputTokens: &zero, CacheReadTokens: &zero, CacheWriteTokens: &zero}, "USD", math.MaxInt64)
		one := int64(1)
		fixture.finishNamedAttempt("cost-overflow:one", "cost-overflow", at.Add(time.Nanosecond), accounting.StatusSucceeded,
			accounting.Usage{InputTokens: &one, OutputTokens: &zero, CacheReadTokens: &zero, CacheWriteTokens: &zero}, "USD", 1)
		fixture.finishAccountingAndGovernance("cost-overflow", accounting.StatusSucceeded, at.Add(2*time.Second))
		assertObservationQueryError(t, fixture.core, context.Background(), ObservationQuery{Limit: 10, ObservedAt: at}, ObservationOverflow)
	})

	if _, err := checkedAdd(math.MaxInt64, 1); !isObservationKind(err, ObservationOverflow) {
		t.Fatalf("checked add error=%v", err)
	}
}

func TestObservationSnapshotConflictAndFutureEffectiveFloor(t *testing.T) {
	fixture := newObservationFixture(t)
	defer fixture.db.Close()
	observed := governanceStart.Add(7 * time.Hour)
	first := shadowObservationScope("policy", 1, 10, 0, "")
	second := shadowObservationScope("policy", 1, 20, 0, "")
	fixture.admit("first", observed.Add(time.Second), first)
	fixture.finishGovernanceOnly("first", accounting.StatusSucceeded)
	fixture.admit("second", observed.Add(2*time.Second), second)
	fixture.finishGovernanceOnly("second", accounting.StatusSucceeded)
	assertObservationQueryError(t, fixture.core, context.Background(), ObservationQuery{Limit: 10, ObservedAt: observed}, ObservationSchema)

	if _, err := fixture.db.Exec(`DELETE FROM governance_request_scopes WHERE request_id='second'; DELETE FROM governance_requests WHERE id='second'`); err != nil {
		t.Fatal(err)
	}
	page := fixture.query(ObservationQuery{Limit: 10, ObservedAt: observed})
	if page.WindowEnd != formatTime(observed.Add(2*time.Second)) {
		t.Fatalf("effective floor window=%s", page.WindowEnd)
	}
}

func TestObservationIndexMigrationStrictRollbackAndRetry(t *testing.T) {
	for _, test := range []struct {
		name      string
		malformed string
		drop      string
	}{
		{name: "column-order", malformed: `CREATE INDEX governance_requests_effective_idx ON governance_requests(id,effective_started_at)`, drop: `DROP INDEX governance_requests_effective_idx`},
		{name: "partial", malformed: `CREATE INDEX governance_requests_effective_idx ON governance_requests(effective_started_at,id) WHERE status='pending'`, drop: `DROP INDEX governance_requests_effective_idx`},
		{name: "same-name-view", malformed: `CREATE VIEW governance_requests_effective_idx AS SELECT id FROM governance_requests`, drop: `DROP VIEW governance_requests_effective_idx`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "observation-index.db")
			db := openGovernanceDB(t, path, 1)
			defer db.Close()
			if _, err := db.Exec(requestsDDL); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(test.malformed); err != nil {
				t.Fatal(err)
			}
			coordinator := newTestCoordinator(t, db)
			if err := coordinator.Migrate(context.Background()); err == nil || !errors.Is(err, ErrSchema) && !errors.Is(err, ErrUnavailable) {
				t.Fatalf("malformed index migration error=%v", err)
			}
			assertObjectCount(t, db, "governance_settings", 0)
			assertObjectCount(t, db, "governance_request_scopes", 0)
			assertObjectCount(t, db, "governance_request_scopes_scope_idx", 0)
			if _, err := db.Exec(test.drop); err != nil {
				t.Fatal(err)
			}
			if err := coordinator.Migrate(context.Background()); err != nil {
				t.Fatalf("retry migration: %v", err)
			}
			var definition string
			if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='governance_requests_effective_idx'`).Scan(&definition); err != nil {
				t.Fatal(err)
			}
			if normalizeDDL(definition) != normalizeDDL(storedDDL(requestsEffectiveIndexDDL)) {
				t.Fatalf("effective index=%s", definition)
			}
		})
	}
	t.Run("old-schema-preserves-history", func(t *testing.T) {
		fixture := newObservationFixture(t)
		defer fixture.db.Close()
		fixture.admit("historical", governanceStart, shadowObservationScope("policy", 1, 100, 0, ""))
		fixture.finishGovernanceOnly("historical", accounting.StatusSucceeded)
		if _, err := fixture.db.Exec(`DROP INDEX governance_requests_effective_idx`); err != nil {
			t.Fatal(err)
		}
		if err := fixture.core.Migrate(context.Background()); err != nil {
			t.Fatalf("old schema upgrade: %v", err)
		}
		assertRequestCount(t, fixture.db, 1)
		assertObjectCount(t, fixture.db, "governance_requests_effective_idx", 1)
	})
}

func newObservationFixture(t *testing.T) *observationFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "observations.db")
	db := openGovernanceDB(t, path, 1)
	ledger := accounting.NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	core := newTestCoordinator(t, db)
	if err := core.Migrate(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	fixture := &observationFixture{t: t, path: path, db: db, core: core, ledger: ledger, revision: 1}
	fixture.setEnabled(true, governanceStart.Add(-48*time.Hour))
	return fixture
}

func (f *observationFixture) setEnabled(enabled bool, at time.Time) {
	f.t.Helper()
	settings := setEnabled(f.t, f.db, f.core, f.revision, enabled, at)
	f.revision = settings.Revision
}

func (f *observationFixture) admit(id string, at time.Time, scopes ...ScopeSnapshot) {
	f.t.Helper()
	input := AdmissionStart{RequestID: id, Subject: Subject{EmployeeID: "employee-1", KeyID: "key-1", PublicModel: "public-model",
		Protocol: accounting.ProtocolOpenAIResponses}, SettingsRevision: f.revision, SnapshotComplete: true, Scopes: scopes, StartedAt: at, ObservedAt: at}
	lease, decision, err := runAdmit(f.t, f.db, f.core, input)
	if err != nil || lease == nil || !decision.Allowed {
		f.t.Fatalf("admit %s lease=%+v decision=%+v err=%v", id, lease, decision, err)
	}
}

func (f *observationFixture) beginAccountingRequest(id string, at time.Time) {
	f.t.Helper()
	if err := f.ledger.BeginRequest(context.Background(), accounting.RequestStart{ID: id, EmployeeID: "employee-1", KeyID: "key-1",
		ModelID: "public-model", Provider: accounting.ProviderOpenAICompatible, StartedAt: at}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *observationFixture) beginPendingAttempt(requestID, currency string) {
	f.t.Helper()
	at := observationRequestTime(f.t, f.db, requestID)
	f.beginAccountingRequest(requestID, at)
	price := observationPrice(currency, 1, 1)
	if err := f.ledger.BeginAttempt(context.Background(), accounting.AttemptStart{ID: requestID + ":attempt", RequestID: requestID,
		AccountID: "account-1", Provider: accounting.ProviderOpenAICompatible, Dispatch: accounting.DispatchPrimary,
		StartedAt: at, Price: price}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *observationFixture) finishPendingAttempt(requestID string, status accounting.Status, usage accounting.Usage) {
	f.t.Helper()
	at := observationRequestTime(f.t, f.db, requestID)
	if err := f.ledger.FinishAttempt(context.Background(), accounting.AttemptFinish{ID: requestID + ":attempt", Status: status,
		FinishedAt: at.Add(time.Second), Usage: usage}); err != nil {
		f.t.Fatal(err)
	}
	f.finishAccountingAndGovernance(requestID, status, at.Add(2*time.Second))
}

func (f *observationFixture) finishAttemptTwice(requestID string, status accounting.Status, usage accounting.Usage) {
	f.t.Helper()
	at := observationRequestTime(f.t, f.db, requestID)
	f.beginAccountingRequest(requestID, at)
	start := accounting.AttemptStart{ID: requestID + ":attempt", RequestID: requestID, AccountID: "account-1",
		Provider: accounting.ProviderOpenAICompatible, Dispatch: accounting.DispatchPrimary, StartedAt: at}
	if err := f.ledger.BeginAttempt(context.Background(), start); err != nil {
		f.t.Fatal(err)
	}
	finish := accounting.AttemptFinish{ID: start.ID, Status: status, FinishedAt: at.Add(time.Second), Usage: usage}
	for iteration := 0; iteration < 2; iteration++ {
		if err := f.ledger.FinishAttempt(context.Background(), finish); err != nil {
			f.t.Fatal(err)
		}
	}
	requestFinish := accounting.RequestFinish{ID: requestID, Status: status, FinishedAt: at.Add(2 * time.Second)}
	governanceFinish := Finish{RequestID: requestID, Status: status, FinishedAt: at.Add(2 * time.Second)}
	for iteration := 0; iteration < 2; iteration++ {
		if err := f.ledger.FinishRequest(context.Background(), requestFinish); err != nil {
			f.t.Fatal(err)
		}
		if err := runFinish(f.t, f.db, f.core, governanceFinish); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *observationFixture) finishAttempt(requestID string, status accounting.Status, usage accounting.Usage, currency string, cost int64) {
	f.t.Helper()
	at := observationRequestTime(f.t, f.db, requestID)
	f.beginAccountingRequest(requestID, at)
	f.finishNamedAttempt(requestID+":attempt", requestID, at, status, usage, currency, cost)
	f.finishAccountingAndGovernance(requestID, status, at.Add(time.Second))
}

func (f *observationFixture) finishNamedAttempt(id, requestID string, at time.Time, status accounting.Status, usage accounting.Usage, currency string, cost int64) {
	f.t.Helper()
	price := observationPrice(currency, cost, observationInputTokens(usage))
	if err := f.ledger.BeginAttempt(context.Background(), accounting.AttemptStart{ID: id, RequestID: requestID, AccountID: "account-1",
		Provider: accounting.ProviderOpenAICompatible, Dispatch: accounting.DispatchPrimary, StartedAt: at, Price: price}); err != nil {
		f.t.Fatal(err)
	}
	if err := f.ledger.FinishAttempt(context.Background(), accounting.AttemptFinish{ID: id, Status: status, FinishedAt: at.Add(time.Second), Usage: usage}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *observationFixture) finishAccountingAndGovernance(requestID string, status accounting.Status, at time.Time) {
	f.t.Helper()
	if err := f.ledger.FinishRequest(context.Background(), accounting.RequestFinish{ID: requestID, Status: status, FinishedAt: at}); err != nil {
		f.t.Fatal(err)
	}
	if err := runFinish(f.t, f.db, f.core, Finish{RequestID: requestID, Status: status, FinishedAt: at}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *observationFixture) finishGovernanceOnly(requestID string, status accounting.Status) {
	f.t.Helper()
	at := observationRequestTime(f.t, f.db, requestID)
	if err := runFinish(f.t, f.db, f.core, Finish{RequestID: requestID, Status: status, FinishedAt: at.Add(time.Second)}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *observationFixture) query(query ObservationQuery) ObservationPage {
	f.t.Helper()
	page, err := f.core.QueryObservations(context.Background(), query)
	if err != nil {
		f.t.Fatal(err)
	}
	return page
}

func shadowObservationScope(policy string, revision, tpm, cost int64, currency string) ScopeSnapshot {
	scope := ScopeSnapshot{Kind: ScopeEmployee, ID: "employee-1", PolicyID: policy, PolicyRevision: revision}
	if tpm > 0 {
		scope.ShadowTPM = int64Pointer(tpm)
	}
	if cost > 0 {
		scope.ShadowCostMicro = int64Pointer(cost)
		scope.ShadowCurrency = currency
		scope.ShadowWindow = ShadowWindowRolling24h
	}
	return scope
}

func shadowKeyObservationScope(policy string, revision, tpm int64) ScopeSnapshot {
	scope := shadowObservationScope(policy, revision, tpm, 0, "")
	scope.Kind, scope.ID = ScopeKey, "key-1"
	return scope
}

func shadowGroupObservationScope(policy string, revision, tpm int64) ScopeSnapshot {
	scope := shadowObservationScope(policy, revision, tpm, 0, "")
	scope.Kind, scope.ID, scope.GroupRevision = ScopeGroup, "group-1", int64Pointer(revision)
	return scope
}

func knownObservationUsage(input, output int64) accounting.Usage {
	zero := int64(0)
	return accounting.Usage{InputTokens: &input, OutputTokens: &output, CacheReadTokens: &zero, CacheWriteTokens: &zero}
}

func observationPrice(currency string, cost, inputTokens int64) *accounting.PriceSnapshot {
	if currency == "" {
		return nil
	}
	rate := int64(0)
	if cost > 0 {
		if inputTokens == 1_000_000 {
			rate = cost
		} else {
			rate = cost * 1_000_000 / inputTokens
		}
	}
	return &accounting.PriceSnapshot{Version: "price-" + currency + "-" + canonicalInt(cost), Currency: currency, InputPerMillionMicro: rate}
}

func observationInputTokens(usage accounting.Usage) int64 {
	if usage.InputTokens == nil || *usage.InputTokens == 0 {
		return 1
	}
	return *usage.InputTokens
}

func observationRequestTime(t *testing.T, db *sql.DB, id string) time.Time {
	t.Helper()
	var stored string
	if err := db.QueryRow(`SELECT effective_started_at FROM governance_requests WHERE id=?`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	parsed, err := parseStoredTime(stored)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func assertObservationQueryError(t *testing.T, core *Coordinator, ctx context.Context, query ObservationQuery, want ObservationErrorKind) {
	t.Helper()
	_, err := core.QueryObservations(ctx, query)
	if !isObservationKind(err, want) {
		t.Fatalf("error=%v want=%s", err, want)
	}
}

func isObservationKind(err error, want ObservationErrorKind) bool {
	var typed *ObservationError
	return errors.As(err, &typed) && typed.Kind == want
}

func mutateObservationCursor(t *testing.T, encoded, suffix string) string {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.HasSuffix(text, "}") {
		t.Fatalf("cursor=%s", text)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(strings.TrimSuffix(text, "}") + suffix + "}"))
}

func assertSameObservationTotals(t *testing.T, left, right ObservationScopeTotals) {
	t.Helper()
	if left.TPM != right.TPM || len(left.Cost.ByCurrency) != len(right.Cost.ByCurrency) ||
		left.Cost.KnownAttempts != right.Cost.KnownAttempts || left.Cost.UnknownCostAttempts != right.Cost.UnknownCostAttempts ||
		left.Cost.PendingAttempts != right.Cost.PendingAttempts || left.Cost.PendingRequests != right.Cost.PendingRequests ||
		left.Cost.PendingRequestsWithoutAttempt != right.Cost.PendingRequestsWithoutAttempt ||
		left.Cost.ZeroAttemptRequests != right.Cost.ZeroAttemptRequests {
		t.Fatalf("totals differ left=%+v right=%+v", left, right)
	}
	for index := range left.Cost.ByCurrency {
		if left.Cost.ByCurrency[index] != right.Cost.ByCurrency[index] {
			t.Fatalf("currency totals differ left=%+v right=%+v", left.Cost.ByCurrency, right.Cost.ByCurrency)
		}
	}
}

func assertTPMTotals(t *testing.T, got ObservationTPMTotals, tokens, known, unknown, pendingAttempts, pendingRequests, pendingNoAttempt, zero string) {
	t.Helper()
	if got.KnownTokens != tokens || got.KnownAttempts != known || got.UnknownTokenAttempts != unknown || got.PendingAttempts != pendingAttempts ||
		got.PendingRequests != pendingRequests ||
		got.PendingRequestsWithoutAttempt != pendingNoAttempt || got.ZeroAttemptRequests != zero {
		t.Fatalf("TPM totals=%+v", got)
	}
}

func assertCostTotals(t *testing.T, got ObservationCostTotals, known, unknown, pendingAttempts, pendingRequests, pendingNoAttempt, zero string, currencies map[string][2]string) {
	t.Helper()
	if got.KnownAttempts != known || got.UnknownCostAttempts != unknown || got.PendingAttempts != pendingAttempts || got.PendingRequests != pendingRequests ||
		got.PendingRequestsWithoutAttempt != pendingNoAttempt || got.ZeroAttemptRequests != zero || len(got.ByCurrency) != len(currencies) {
		t.Fatalf("cost totals=%+v", got)
	}
	for _, item := range got.ByCurrency {
		want, ok := currencies[item.Currency]
		if !ok || item.KnownCostMicro != want[0] || item.Attempts != want[1] {
			t.Fatalf("currency=%+v want=%+v", item, want)
		}
	}
}
