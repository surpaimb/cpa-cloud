package financial

// Independently authored for docs/subscription-period-expiry-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	expirySQLite "modernc.org/sqlite"
)

func TestMonthlyPeriodEndClampsCalendarDay(t *testing.T) {
	for _, tc := range []struct{ start, end string }{
		{"2025-01-31T12:34:56.123456789Z", "2025-02-28T12:34:56.123456789Z"},
		{"2024-01-31T12:34:56.123456789Z", "2024-02-29T12:34:56.123456789Z"},
		{"2026-03-31T00:00:00Z", "2026-04-30T00:00:00.000000000Z"},
		{"2026-12-31T23:59:59Z", "2027-01-31T23:59:59.000000000Z"},
	} {
		start, err := parseSubscriptionStart(tc.start)
		if err != nil {
			t.Fatal(err)
		}
		end, err := monthlyPeriodEnd(start)
		if err != nil || end.Format(subscriptionEndLayout) != tc.end {
			t.Fatalf("start=%s end=%s err=%v", tc.start, end.Format(subscriptionEndLayout), err)
		}
	}
	if _, err := monthlyPeriodEnd(time.Date(9999, 12, 1, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrSchema) {
		t.Fatalf("overflow err=%v", err)
	}
}

func subscriptionTestFixture(t *testing.T) (*sql.DB, *Ledger, *Commercial, string, string) {
	t.Helper()
	db := openFinancialTestDB(t)
	ctx := context.Background()
	ledger, commercial := NewLedger(db), NewCommercial(db)
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := commercial.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner := Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}
	entries, err := ledger.Post(ctx, Post{OperationID: "expiry-seed", Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "adjustment", ResourceID: "expiry-seed", ObservedAt: financialTestTime, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 1000, ResourceKind: "adjustment", ResourceID: "expiry-seed"}}})
	if err != nil {
		t.Fatal(err)
	}
	plan, _, err := commercial.CreatePlan(ctx, CreatePlan{Meta: testCommercialMeta(t, "expiry-plan", map[string]any{"plan": true}, financialTestTime), Name: "Expiry", Currency: "USD", Interval: "monthly", PriceMicro: 10, CreditMicro: 20, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	return db, ledger, commercial, entries[0].AccountID, plan.ID
}

func TestSubscriptionExpiryBatchExactBoundaryAndNoMoneyMovement(t *testing.T) {
	db, ledger, commercial, accountID, planID := subscriptionTestFixture(t)
	defer db.Close()
	start := time.Date(2026, 1, 31, 8, 0, 0, 123, time.UTC)
	end, _ := monthlyPeriodEnd(start)
	for i := 0; i < 101; i++ {
		_, err := db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`, fmt.Sprintf("expiry-%03d", i), accountID, planID, 1, 10, 20, "USD", "monthly", "active", formatCommercialTime(start), end.Format(subscriptionEndLayout))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,revision) VALUES('one-time',?,?,?,?,?,?,?,'active',?,1)`, accountID, planID, 1, 10, 20, "USD", "one_time", formatCommercialTime(start)); err != nil {
		t.Fatal(err)
	}
	if count, err := commercial.ExpireDueSubscriptions(context.Background(), end.Add(-time.Nanosecond)); err != nil || count != 0 {
		t.Fatalf("before end count=%d err=%v", count, err)
	}
	projected, err := commercial.GetSubscription(context.Background(), "expiry-000")
	if err != nil || projected.Status != "expired" || projected.Revision != 1 {
		t.Fatalf("projection=%+v err=%v", projected, err)
	}
	for pass, want := range []int{100, 1, 0} {
		count, err := commercial.ExpireDueSubscriptions(context.Background(), end)
		if err != nil || count != want {
			t.Fatalf("pass=%d count=%d want=%d err=%v", pass, count, want, err)
		}
	}
	var expired, active, entries int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_subscriptions WHERE status='expired'`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_subscriptions WHERE status='active'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if expired != 101 || active != 1 || entries != 1 {
		t.Fatalf("expired=%d active=%d entries=%d", expired, active, entries)
	}
	if balance, err := ledger.Balance(context.Background(), Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}, "USD"); err != nil || balance.AmountMicro != 1000 {
		t.Fatalf("balance=%+v err=%v", balance, err)
	}
	if err := commercial.Migrate(context.Background()); err != nil {
		t.Fatalf("restart migrate: %v", err)
	}
}

func TestSubscriptionLegacyMigrationRollbackAndFrozenEnd(t *testing.T) {
	db, _, commercial, accountID, planID := subscriptionTestFixture(t)
	defer db.Close()
	ctx := context.Background()
	if _, err := db.Exec(`DROP TABLE financial_subscriptions`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(subscriptionsLegacyDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(subscriptionAccountIndexDDL); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,revision) VALUES(?,?,?,?,?,?,?,?,?,?,1)`
	if _, err := db.Exec(insert, "old-month", accountID, planID, 1, 10, 20, "USD", "monthly", "active", "bad-time"); err != nil {
		t.Fatal(err)
	}
	if err := commercial.Migrate(ctx); !errors.Is(err, ErrSchema) {
		t.Fatalf("bad migration err=%v", err)
	}
	var oldDDL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='financial_subscriptions'`).Scan(&oldDDL); err != nil || normalize(oldDDL) != normalize(storedDDL(subscriptionsLegacyDDL)) {
		t.Fatalf("rollback schema err=%v", err)
	}
	start := "2024-01-31T08:00:00Z"
	if _, err := db.Exec(`UPDATE financial_subscriptions SET started_at=? WHERE id='old-month'`, start); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(insert, "old-once", accountID, planID, 1, 10, 20, "USD", "one_time", "active", start); err != nil {
		t.Fatal(err)
	}
	if err := commercial.Migrate(ctx); err != nil {
		t.Fatalf("valid migration: %v", err)
	}
	month, err := commercial.GetSubscription(ctx, "old-month")
	if err != nil || month.PeriodEndAt == nil || month.PeriodEndAt.Format(subscriptionEndLayout) != "2024-02-29T08:00:00.000000000Z" || month.Status != "expired" {
		t.Fatalf("migrated month=%+v err=%v", month, err)
	}
	once, err := commercial.GetSubscription(ctx, "old-once")
	if err != nil || once.PeriodEndAt != nil || once.Status != "active" {
		t.Fatalf("migrated once=%+v err=%v", once, err)
	}
	if err := commercial.Migrate(ctx); err != nil {
		t.Fatalf("idempotent reopen: %v", err)
	}
}

func TestSubscriptionCancellationAtEndConflicts(t *testing.T) {
	db, _, commercial, accountID, planID := subscriptionTestFixture(t)
	defer db.Close()
	start := time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC)
	end, _ := monthlyPeriodEnd(start)
	if _, err := db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES('boundary',?,?,?,?,?,?,?,?,?,?,1)`, accountID, planID, 1, 10, 20, "USD", "monthly", "active", formatCommercialTime(start), end.Format(subscriptionEndLayout)); err != nil {
		t.Fatal(err)
	}
	commercial.now = func() time.Time { return end } // request was accepted before end, commit crosses it
	input := CancelSubscription{Meta: testCommercialMeta(t, "cancel-at-end", map[string]any{"id": "boundary"}, end.Add(-time.Second)), ID: "boundary", ExpectedRevision: 1}
	if _, _, err := commercial.CancelSubscription(context.Background(), input); !errors.Is(err, ErrConflict) {
		t.Fatalf("cancel exact end err=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_commercial_operations WHERE operation_id='cancel-at-end'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed operation count=%d err=%v", count, err)
	}
}

func TestSubscriptionExpiryPersistenceFailureRollsBackAndRetries(t *testing.T) {
	db, _, commercial, accountID, planID := subscriptionTestFixture(t)
	defer db.Close()
	start := time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC)
	end, _ := monthlyPeriodEnd(start)
	for _, id := range []string{"failure-a", "failure-b"} {
		if _, err := db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES(?,?,?,?,?,?,?,?,?,?,?,1)`, id, accountID, planID, 1, 10, 20, "USD", "monthly", "active", formatCommercialTime(start), end.Format(subscriptionEndLayout)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TRIGGER expiry_test_failure BEFORE UPDATE ON financial_subscriptions WHEN OLD.id='failure-b' BEGIN SELECT RAISE(ABORT,'synthetic persistence failure'); END`); err != nil {
		t.Fatal(err)
	}
	if count, err := commercial.ExpireDueSubscriptions(context.Background(), end); err == nil || count != 0 {
		t.Fatalf("failure count=%d err=%v", count, err)
	}
	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_subscriptions WHERE status='active'`).Scan(&active); err != nil || active != 2 {
		t.Fatalf("rollback active=%d err=%v", active, err)
	}
	var sequence int
	var schema, file string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &schema, &file); err != nil || file == "" {
		t.Fatalf("database path err=%v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sql.Open("sqlite", file+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopened.SetMaxOpenConns(1)
	commercial = NewCommercial(reopened)
	if _, err := reopened.Exec(`DROP TRIGGER expiry_test_failure`); err != nil {
		t.Fatal(err)
	}
	if err := commercial.Migrate(context.Background()); err != nil {
		t.Fatalf("restart migration: %v", err)
	}
	if count, err := commercial.ExpireDueSubscriptions(context.Background(), end); err != nil || count != 2 {
		t.Fatalf("retry count=%d err=%v", count, err)
	}
	if err := commercial.Migrate(context.Background()); err != nil {
		t.Fatalf("reopen: %v", err)
	}
}

func TestSubscriptionCorruptFrozenEndFailsClosed(t *testing.T) {
	db, _, commercial, accountID, planID := subscriptionTestFixture(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES('wrong-end',?,?,?,?,?,?,?,?,?,?,1)`, accountID, planID, 1, 10, 20, "USD", "monthly", "active", "2026-01-31T08:00:00Z", "2026-03-01T08:00:00.000000000Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.GetSubscription(context.Background(), "wrong-end"); !errors.Is(err, ErrSchema) {
		t.Fatalf("read err=%v", err)
	}
	if err := commercial.Migrate(context.Background()); !errors.Is(err, ErrSchema) {
		t.Fatalf("restart validation err=%v", err)
	}
}

// Independently authored pure observer controls; no SQL/DB/fixture is used.
type expiryPurePanicError struct{}

func (expiryPurePanicError) Error() string { panic("SYNTHETIC_EXPIRY_PUBLIC_CANARY") }

type expiryPureWrappedError struct{ next error }

func (expiryPureWrappedError) Error() string   { panic("SYNTHETIC_EXPIRY_PUBLIC_CANARY") }
func (e expiryPureWrappedError) Unwrap() error { return e.next }

func TestExpirySQLObservationPure20261021(t *testing.T) {
	t.Run("context-equivalence", func(t *testing.T) {
		type originalKey struct{}
		parent, cancel := context.WithCancelCause(context.WithValue(context.Background(), originalKey{}, "synthetic-public-value"))
		defer cancel(nil)
		pass, stop := context.WithTimeout(parent, 5*time.Second)
		defer stop()
		var seen ExpiryObservation
		calls := 0
		sink := func(o ExpiryObservation) { seen = o; calls++ }
		bound := WithExpiryObservation(pass, sink, 7)
		check := func() {
			a, aOK := bound.Deadline()
			b, bOK := pass.Deadline()
			if bound.Done() != pass.Done() || aOK != bOK || !a.Equal(b) || bound.Err() != pass.Err() || context.Cause(bound) != context.Cause(pass) || bound.Value(originalKey{}) != pass.Value(originalKey{}) {
				t.Fatal("EXPIRY_CONTEXT_EQUIVALENCE")
			}
		}
		check()
		if WithExpiryObservation(pass, nil, 9) != pass || WithExpiryObservation(nil, sink, 1) != nil || calls != 0 {
			t.Fatal("EXPIRY_CONTEXT_NIL_IDENTITY")
		}
		cause := errors.New("synthetic-public-cause")
		cancel(cause)
		check()
		scope := expiryScopeFor(bound)
		started := scope.enter(ExpiryBegin, 0)
		scope.returned(ExpiryBegin, 0, started, context.Canceled, ExpiryCountNA)
		if calls != 2 || seen.Pass != 7 || seen.Context != ExpiryContextCancelled || seen.Error != ExpiryErrorCancelled || !seen.Valid() {
			t.Fatal("EXPIRY_CONTEXT_FINITE_CAPTURE")
		}
	})
	t.Run("error-classes", func(t *testing.T) {
		var typedNil *expirySQLite.Error
		for _, tc := range []struct {
			err  error
			kind ExpiryError
			code ExpirySQLite
		}{
			{nil, ExpiryErrorNone, ExpirySQLiteNone},
			{context.Canceled, ExpiryErrorCancelled, ExpirySQLiteNone},
			{context.DeadlineExceeded, ExpiryErrorDeadline, ExpirySQLiteNone},
			{sql.ErrTxDone, ExpiryErrorTxDone, ExpirySQLiteNone},
			{sql.ErrConnDone, ExpiryErrorConnDone, ExpirySQLiteNone},
			{expiryPureWrappedError{context.Canceled}, ExpiryErrorCancelled, ExpirySQLiteNone},
			{expiryPurePanicError{}, ExpiryErrorOther, ExpirySQLiteNone},
			{typedNil, ExpiryErrorOther, ExpirySQLiteNone},
			{&expirySQLite.Error{}, ExpiryErrorSQLite, ExpirySQLiteUnknown},
			{expiryPureWrappedError{&expirySQLite.Error{}}, ExpiryErrorOther, ExpirySQLiteNone},
		} {
			kind, code := expiryErrorClass(tc.err)
			if kind != tc.kind || code != tc.code {
				t.Fatal("EXPIRY_ERROR_CLASS_CONTROL")
			}
		}
	})
	t.Run("sqlite-whitelist", func(t *testing.T) {
		for i, code := range []int{1, 4, 5, 6, 9, 10, 19, 261, 517, 773, 262, 518, 4106} {
			if expirySQLiteClass(code) != ExpirySQLite(i+2) {
				t.Fatal("EXPIRY_SQLITE_WHITELIST_CONTROL")
			}
		}
		for _, code := range []int{-1, 0, 99999} {
			if expirySQLiteClass(code) != ExpirySQLiteUnknown {
				t.Fatal("EXPIRY_SQLITE_UNKNOWN_CONTROL")
			}
		}
	})
	t.Run("finite-values", func(t *testing.T) {
		base := ExpiryObservation{Pass: 1, Phase: ExpiryQuery, Edge: ExpiryReturn, Error: ExpiryErrorNone, Context: ExpiryContextNone, Elapsed: ExpiryLT1MS}
		if !base.Valid() {
			t.Fatal("EXPIRY_FINITE_VALID_CONTROL")
		}
		for _, mutate := range []func(*ExpiryObservation){
			func(o *ExpiryObservation) { o.Phase = 255 },
			func(o *ExpiryObservation) { o.Edge = 255 },
			func(o *ExpiryObservation) { o.Error = 0 },
			func(o *ExpiryObservation) { o.Context = 0 },
			func(o *ExpiryObservation) { o.Pass = 0 },
			func(o *ExpiryObservation) { o.Item = 1 },
			func(o *ExpiryObservation) { o.Elapsed = ExpiryElapsedInvalid },
			func(o *ExpiryObservation) { o.Error, o.SQLite = ExpiryErrorSQLite, ExpirySQLiteUnknown },
			func(o *ExpiryObservation) { o.Count = ExpiryCountOne },
		} {
			v := base
			mutate(&v)
			if v.Valid() {
				t.Fatal("EXPIRY_FINITE_REJECT_CONTROL")
			}
		}
		for _, tc := range []struct {
			d time.Duration
			b ExpiryElapsed
		}{{-1, ExpiryElapsedInvalid}, {0, ExpiryLT1MS}, {time.Millisecond, Expiry1MSLT100MS}, {100 * time.Millisecond, Expiry100MSLT1S}, {time.Second, Expiry1SLT10S}, {10 * time.Second, Expiry10SLT60S}, {time.Minute, ExpiryGE60S}} {
			if expiryElapsedBucket(tc.d) != tc.b {
				t.Fatal("EXPIRY_ELAPSED_CONTROL")
			}
		}
	})
	t.Run("synthetic-canary", func(t *testing.T) {
		var seen ExpiryObservation
		ctx := WithExpiryObservation(context.Background(), func(o ExpiryObservation) { seen = o }, 1)
		scope := expiryScopeFor(ctx)
		scope.returned(ExpiryQuery, 0, time.Now(), expiryPurePanicError{}, ExpiryCountNA)
		if seen.Error != ExpiryErrorOther || seen.SQLite != ExpirySQLiteNone || !seen.Valid() {
			t.Fatal("EXPIRY_CANARY_CLASS_CONTROL")
		}
		// No Error method, string field, arbitrary value or raw handle can be
		// passed by this record. This is classification, not DB-fault coverage.
		if expiryCount(-1) != ExpiryCountInvalid || expiryCount(0) != ExpiryCountZero || expiryCount(1) != ExpiryCountOne || expiryCount(100) != ExpiryCountMany {
			t.Fatal("EXPIRY_COUNT_BUCKET_CONTROL")
		}
	})
}
