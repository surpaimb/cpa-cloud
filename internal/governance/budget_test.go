package governance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	_ "modernc.org/sqlite"
)

type budgetFixture struct {
	db     *sql.DB
	ledger *accounting.Ledger
	budget *Budget
	t0     time.Time
}

type budgetCase struct {
	id          string
	scopeID     string
	hardTPM     *int64
	hardCost    *int64
	currency    string
	price       *accounting.PriceSnapshot
	proof       BudgetProof
	observed    time.Time
	expires     time.Time
	policyRev   int64
	budgetOn    bool
	unknownMode string
}

func TestBudgetReserveLifecycleStableScopeAndIdempotency(t *testing.T) {
	f := newBudgetFixture(t)
	limit, costLimit := int64(42), int64(1000)
	c := f.defaultCase("first")
	c.hardTPM, c.hardCost, c.currency = &limit, &costLimit, "USD"
	reserved := f.reserve(t, c)
	if !reserved.Allowed || !reserved.Enforced || reserved.Code != BudgetDecisionReserved || reserved.Reservation.TokenUpper != 40 || *reserved.Reservation.CostUpper != 40 {
		t.Fatalf("unexpected reservation: %+v", reserved)
	}

	tx := f.begin(t)
	replayed, err := f.budget.ReserveTx(context.Background(), tx, f.reserveInput(c))
	if err != nil || !replayed.Allowed || replayed.Reservation == nil {
		t.Fatalf("exact replay: result=%+v err=%v", replayed, err)
	}
	conflicting := f.reserveInput(c)
	conflicting.ObservedAt = conflicting.ObservedAt.Add(time.Nanosecond)
	if _, err := f.budget.ReserveTx(context.Background(), tx, conflicting); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
	f.rollback(t, tx)

	tx = f.begin(t)
	markedAt := c.observed.Add(time.Second)
	if _, err := f.budget.MarkMayHaveSentTx(context.Background(), tx, BudgetMutation{AttemptID: c.id + ":1", ObservedAt: markedAt}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.budget.MarkMayHaveSentTx(context.Background(), tx, BudgetMutation{AttemptID: c.id + ":1", ObservedAt: markedAt}); err != nil {
		t.Fatalf("mark replay: %v", err)
	}
	if _, err := f.budget.MarkMayHaveSentTx(context.Background(), tx, BudgetMutation{AttemptID: c.id + ":1", ObservedAt: markedAt.Add(time.Nanosecond)}); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed mark error=%v", err)
	}
	f.commit(t, tx)

	newExpiry := c.expires.Add(time.Minute)
	tx = f.begin(t)
	if _, err := tx.Exec(`UPDATE governance_requests SET expires_at=? WHERE id=?`, formatTime(newExpiry), c.id); err != nil {
		t.Fatal(err)
	}
	renewed, err := f.budget.RenewTx(context.Background(), tx, BudgetRenew{AttemptID: c.id + ":1", ExpectedExpiresAt: c.expires, ObservedAt: markedAt.Add(time.Second)})
	if err != nil || !renewed.ExecutionExpiresAt.Equal(newExpiry) {
		t.Fatalf("renewed=%+v err=%v", renewed, err)
	}
	f.commit(t, tx)

	f.finishAndSettle(t, c.id, accounting.StatusSucceeded, [4]*int64{ptr64(1), ptr64(1), ptr64(1), ptr64(1)}, markedAt.Add(2*time.Second))
	got, err := f.budget.Get(context.Background(), c.id+":1")
	if err != nil || got.Lifecycle != BudgetSettled || !got.TokenKnown || *got.ActualTokens != 4 || !got.CostKnown || *got.ActualCostMicro != 4 {
		t.Fatalf("settled=%+v err=%v", got, err)
	}

	second := f.defaultCase("second")
	second.scopeID, second.policyRev = c.scopeID, 2
	second.hardTPM, second.hardCost, second.currency = &limit, &costLimit, "USD"
	second.observed = markedAt.Add(3 * time.Second)
	second.expires = second.observed.Add(time.Minute)
	second.proof.FourBuckets = &accounting.UpperUsage{InputTokens: 39}
	result := f.reserve(t, second)
	if result.Allowed || result.Code != BudgetDecisionTPMExceeded {
		t.Fatalf("stable scope did not include old revision: %+v", result)
	}
}

func TestBudgetNoHardSkipsProofAndBoundFailuresAreConservative(t *testing.T) {
	f := newBudgetFixture(t)
	c := f.defaultCase("shadow")
	c.hardTPM, c.hardCost, c.currency = nil, nil, ""
	c.unknownMode = "shadow"
	c.proof = BudgetProof{}
	result := f.reserve(t, c)
	if !result.Allowed || result.Enforced || result.Code != BudgetDecisionNotEnforced {
		t.Fatalf("shadow result=%+v", result)
	}
	if _, err := f.budget.Get(context.Background(), c.id+":1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("shadow created reservation: %v", err)
	}

	limit := int64(MaxRevision)
	overflow := f.defaultCase("overflow")
	overflow.hardTPM = &limit
	overflow.proof.FourBuckets = &accounting.UpperUsage{InputTokens: math.MaxInt64, OutputTokens: 1}
	f.prepare(t, overflow)
	tx := f.begin(t)
	if _, err := f.budget.ReserveTx(context.Background(), tx, f.reserveInput(overflow)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("arithmetic overflow error=%v", err)
	}
	f.rollback(t, tx)

	missing := f.defaultCase("missing-proof")
	missing.hardTPM = ptr64(100)
	missing.proof = BudgetProof{ActualModel: "actual", AccountRevision: 1, BounderID: "bounder", BounderRevision: 1, TransformRevision: "v1"}
	result = f.reserve(t, missing)
	if result.Allowed || result.Code != BudgetDecisionBoundUnavailable {
		t.Fatalf("missing proof result=%+v", result)
	}
}

func TestBudgetReserveUsesCallerTransactionAndCostProofFailuresDoNotPersist(t *testing.T) {
	f := newBudgetFixture(t)
	c := f.defaultCase("atomic")
	c.hardTPM = ptr64(100)
	ctx := context.Background()
	request := accounting.RequestStart{ID: c.id, EmployeeID: "employee-1", KeyID: "key-1", ModelID: "public-model", Provider: accounting.ProviderOpenAICompatible, StartedAt: c.observed}
	if err := f.ledger.BeginRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO governance_requests(id,employee_id,key_id,public_model,protocol,settings_revision,budget_enabled,observed_started_at,effective_started_at,effective_lease_at,expires_at,status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.id, request.EmployeeID, request.KeyID, request.ModelID, string(accounting.ProtocolOpenAIChatCompletions), 1, 1, formatTime(c.observed), formatTime(c.observed), formatTime(c.observed), formatTime(c.expires), string(accounting.StatusPending)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO governance_request_scopes(request_id,scope_kind,scope_id,policy_id,policy_revision,group_revision,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		c.id, string(ScopeEmployee), c.scopeID, "policy-1", 1, nil, *c.hardTPM, nil, "", "", "deny_unknown"); err != nil {
		t.Fatal(err)
	}
	attempt := accounting.AttemptStart{ID: c.id + ":1", RequestID: c.id, AccountID: "account-1", Provider: request.Provider, Dispatch: accounting.DispatchPrimary, StartedAt: c.observed, Price: c.price}
	tx := f.begin(t)
	if err := f.ledger.BeginAttemptTx(ctx, tx, attempt); err != nil {
		t.Fatal(err)
	}
	if result, err := f.budget.ReserveTx(ctx, tx, f.reserveInput(c)); err != nil || !result.Allowed {
		t.Fatalf("joint reserve result=%+v err=%v", result, err)
	}
	f.rollback(t, tx)
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE id=?`, attempt.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback left attempt count=%d err=%v", count, err)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM governance_budget_reservations WHERE attempt_id=?`, attempt.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback left reservation count=%d err=%v", count, err)
	}
	tx = f.begin(t)
	if err := f.ledger.BeginAttemptTx(ctx, tx, attempt); err != nil {
		t.Fatal(err)
	}
	if result, err := f.budget.ReserveTx(ctx, tx, f.reserveInput(c)); err != nil || !result.Allowed {
		t.Fatalf("retry reserve result=%+v err=%v", result, err)
	}
	f.commit(t, tx)

	unpriced := f.defaultCase("unpriced-cost")
	unpriced.hardTPM, unpriced.hardCost, unpriced.currency, unpriced.price = nil, ptr64(100), "USD", nil
	if result := f.reserve(t, unpriced); result.Allowed || result.Code != BudgetDecisionBoundUnavailable {
		t.Fatalf("unpriced cost result=%+v", result)
	}
	mismatch := f.defaultCase("currency-mismatch")
	mismatch.hardTPM, mismatch.hardCost, mismatch.currency = nil, ptr64(100), "EUR"
	if result := f.reserve(t, mismatch); result.Allowed || result.Code != BudgetDecisionBoundUnavailable {
		t.Fatalf("currency mismatch result=%+v", result)
	}
	costOverflow := f.defaultCase("cost-overflow")
	costOverflow.hardTPM, costOverflow.hardCost, costOverflow.currency = nil, ptr64(math.MaxInt64), "USD"
	costOverflow.price = &accounting.PriceSnapshot{Version: "max-price", Currency: "USD", InputPerMillionMicro: accounting.MaxPriceRate}
	costOverflow.proof.FourBuckets = &accounting.UpperUsage{InputTokens: math.MaxInt64}
	f.prepare(t, costOverflow)
	tx = f.begin(t)
	if _, err := f.budget.ReserveTx(ctx, tx, f.reserveInput(costOverflow)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cost overflow error=%v", err)
	}
	f.rollback(t, tx)
}

func TestBudgetKnownAndUnknownDimensionsUseIndependentWindows(t *testing.T) {
	f := newBudgetFixture(t)
	limit := int64(10)
	known := f.defaultCase("known-token")
	known.hardTPM, known.price = &limit, nil
	known.proof.FourBuckets = &accounting.UpperUsage{InputTokens: 10}
	f.reserve(t, known)
	f.finishAndSettle(t, known.id, accounting.StatusFailed, [4]*int64{ptr64(2), ptr64(0), ptr64(0), ptr64(0)}, known.observed.Add(10*time.Second))

	afterTokenWindow := f.defaultCase("after-token-window")
	afterTokenWindow.scopeID, afterTokenWindow.hardTPM, afterTokenWindow.price = known.scopeID, &limit, nil
	afterTokenWindow.observed = known.observed.Add(71 * time.Second)
	afterTokenWindow.expires = afterTokenWindow.observed.Add(time.Minute)
	afterTokenWindow.proof.FourBuckets = &accounting.UpperUsage{InputTokens: 10}
	if result := f.reserve(t, afterTokenWindow); !result.Allowed {
		t.Fatalf("known token stayed until execution expiry: %+v", result)
	}

	unknown := f.defaultCase("unknown")
	unknown.scopeID, unknown.hardTPM, unknown.price = "scope-unknown", &limit, nil
	unknown.observed = f.t0.Add(5 * time.Minute)
	unknown.expires = unknown.observed.Add(100 * time.Second)
	unknown.proof.FourBuckets = &accounting.UpperUsage{InputTokens: 10}
	f.reserve(t, unknown)
	f.finishAndSettle(t, unknown.id, accounting.StatusCancelled, [4]*int64{nil, nil, nil, nil}, unknown.observed.Add(10*time.Second))

	blocked := f.defaultCase("unknown-blocked")
	blocked.scopeID, blocked.hardTPM, blocked.price = unknown.scopeID, &limit, nil
	blocked.observed = unknown.expires.Add(59 * time.Second)
	blocked.expires = blocked.observed.Add(time.Minute)
	blocked.proof.FourBuckets = &accounting.UpperUsage{InputTokens: 1}
	if result := f.reserve(t, blocked); result.Allowed || result.Code != BudgetDecisionTPMExceeded {
		t.Fatalf("unknown bound released early: %+v", result)
	}

	afterUnknownWindow := f.defaultCase("unknown-expired")
	afterUnknownWindow.scopeID, afterUnknownWindow.hardTPM, afterUnknownWindow.price = unknown.scopeID, &limit, nil
	afterUnknownWindow.observed = unknown.expires.Add(time.Minute)
	afterUnknownWindow.expires = afterUnknownWindow.observed.Add(time.Minute)
	afterUnknownWindow.proof.FourBuckets = &accounting.UpperUsage{InputTokens: 10}
	if result := f.reserve(t, afterUnknownWindow); !result.Allowed {
		t.Fatalf("unknown bound remained at exact expiry: %+v", result)
	}
}

func TestBudgetDetectsPerBucketAndInputGroupOverage(t *testing.T) {
	tests := []struct {
		name  string
		proof BudgetProof
		usage [4]*int64
	}{
		{name: "four-bucket-output", proof: testFourProof(accounting.UpperUsage{InputTokens: 100, OutputTokens: 1, CacheReadTokens: 100, CacheWriteTokens: 100}), usage: [4]*int64{ptr64(1), ptr64(2), ptr64(1), ptr64(1)}},
		{name: "group-output", proof: testGroupProof(accounting.MutuallyExclusiveInputUpperUsage{InputMax: 1_000_000, OutputMax: 7}), usage: [4]*int64{ptr64(1), ptr64(8), ptr64(0), ptr64(0)}},
		{name: "group-input", proof: testGroupProof(accounting.MutuallyExclusiveInputUpperUsage{InputMax: 3, OutputMax: 100}), usage: [4]*int64{ptr64(2), ptr64(1), ptr64(2), ptr64(0)}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newBudgetFixture(t)
			limit := int64(2_000_000)
			c := f.defaultCase(fmt.Sprintf("overage-%d", index))
			c.hardTPM, c.proof = &limit, test.proof
			f.reserve(t, c)
			f.finishAndSettle(t, c.id, accounting.StatusSucceeded, test.usage, c.observed.Add(time.Second))
			got, err := f.budget.Get(context.Background(), c.id+":1")
			if err != nil || !got.TokenOverage {
				t.Fatalf("overage=%+v err=%v", got, err)
			}
			next := f.defaultCase(c.id + "-next")
			next.hardTPM, next.proof = &limit, test.proof
			next.scopeID = c.scopeID + "-next"
			next.observed = c.observed.Add(2 * time.Second)
			next.expires = next.observed.Add(time.Minute)
			if result := f.reserve(t, next); result.Allowed || result.Code != BudgetDecisionProfileQuarantined {
				t.Fatalf("quarantine result=%+v", result)
			}
		})
	}
}

func TestBudgetInterruptRecoveryAndMissingRelationshipFailClosed(t *testing.T) {
	f := newBudgetFixture(t)
	limit := int64(100)
	reserved := f.defaultCase("reserved")
	reserved.hardTPM = &limit
	f.reserve(t, reserved)
	marked := f.defaultCase("marked")
	marked.scopeID, marked.hardTPM = "marked-scope", &limit
	marked.observed, marked.expires = f.t0.Add(time.Second), f.t0.Add(2*time.Minute)
	f.reserve(t, marked)
	tx := f.begin(t)
	if _, err := f.budget.MarkMayHaveSentTx(context.Background(), tx, BudgetMutation{AttemptID: marked.id + ":1", ObservedAt: marked.observed.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	f.commit(t, tx)

	tx = f.begin(t)
	recovered, err := f.budget.RecoverTx(context.Background(), tx, f.t0.Add(3*time.Minute))
	if err != nil || recovered.Interrupted != 2 || recovered.Released != 0 || !recovered.EffectiveAt.Equal(f.t0.Add(3*time.Minute)) {
		t.Fatalf("recovery=%+v err=%v", recovered, err)
	}
	f.commit(t, tx)
	for _, id := range []string{reserved.id + ":1", marked.id + ":1"} {
		got, err := f.budget.Get(context.Background(), id)
		if err != nil || got.Lifecycle != BudgetInterrupted || got.TokenKnown || got.CostKnown {
			t.Fatalf("recovered %s=%+v err=%v", id, got, err)
		}
	}

	missing := f.defaultCase("missing")
	missing.scopeID, missing.hardTPM = "missing-scope", &limit
	missing.observed, missing.expires = f.t0.Add(4*time.Minute), f.t0.Add(5*time.Minute)
	f.prepare(t, missing)
	tx = f.begin(t)
	if _, err := f.budget.RecoverTx(context.Background(), tx, f.t0.Add(6*time.Minute)); !errors.Is(err, ErrSchema) {
		t.Fatalf("missing reservation recovery error=%v", err)
	}
	f.rollback(t, tx)
}

func TestBudgetExplicitInterruptReleaseAndCancellation(t *testing.T) {
	f := newBudgetFixture(t)
	limit := int64(10)
	interrupted := f.defaultCase("explicit-interrupt")
	interrupted.hardTPM, interrupted.price = &limit, nil
	interrupted.proof.FourBuckets = &accounting.UpperUsage{InputTokens: 10}
	f.reserve(t, interrupted)
	tx := f.begin(t)
	at := interrupted.observed.Add(time.Second)
	got, err := f.budget.InterruptTx(context.Background(), tx, BudgetMutation{AttemptID: interrupted.id + ":1", ObservedAt: at})
	if err != nil || got.Lifecycle != BudgetInterrupted || got.TokenKnown || got.CostKnown {
		t.Fatalf("interrupt=%+v err=%v", got, err)
	}
	if _, err := f.budget.InterruptTx(context.Background(), tx, BudgetMutation{AttemptID: interrupted.id + ":1", ObservedAt: at}); err != nil {
		t.Fatalf("interrupt replay: %v", err)
	}
	if _, err := f.budget.ReserveTx(context.Background(), tx, f.reserveInput(interrupted)); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal reserve replay regained send eligibility: %v", err)
	}
	f.commit(t, tx)

	released := f.defaultCase("released")
	released.scopeID, released.hardTPM, released.price = "released-scope", &limit, nil
	released.proof.FourBuckets = &accounting.UpperUsage{InputTokens: 10}
	released.observed, released.expires = f.t0.Add(3*time.Minute), f.t0.Add(4*time.Minute)
	f.reserve(t, released)
	tx = f.begin(t)
	if err := f.ledger.FinishAttemptTx(context.Background(), tx, accounting.AttemptFinish{ID: released.id + ":1", Status: accounting.StatusFailed, FinishedAt: released.observed.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	got, err = f.budget.SettleTx(context.Background(), tx, BudgetSettle{AttemptID: released.id + ":1", ObservedAt: released.observed.Add(time.Second), Mode: BudgetSettleReleaseNotStarted})
	if err != nil || got.Lifecycle != BudgetReleasedNotStarted {
		t.Fatalf("release=%+v err=%v", got, err)
	}
	f.commit(t, tx)
	next := f.defaultCase("after-release")
	next.scopeID, next.hardTPM, next.price = released.scopeID, &limit, nil
	next.proof.FourBuckets = &accounting.UpperUsage{InputTokens: 10}
	next.observed, next.expires = released.observed.Add(2*time.Second), released.observed.Add(2*time.Minute)
	if result := f.reserve(t, next); !result.Allowed {
		t.Fatalf("released reservation consumed budget: %+v", result)
	}

	cancelled := f.defaultCase("cancelled-reserve")
	cancelled.scopeID, cancelled.hardTPM = "cancelled-scope", &limit
	cancelled.observed, cancelled.expires = f.t0.Add(6*time.Minute), f.t0.Add(7*time.Minute)
	f.prepare(t, cancelled)
	tx = f.begin(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.budget.ReserveTx(ctx, tx, f.reserveInput(cancelled)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("cancelled reserve error=%v", err)
	}
	f.rollback(t, tx)
	if _, err := f.budget.ReserveTx(context.Background(), nil, f.reserveInput(cancelled)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil tx error=%v", err)
	}
}

func TestBudgetMigrationRollbackStrictSchemaAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "budget-migrate.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	ledger := accounting.NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	createBudgetExternalSchema(t, db)
	if _, err := db.Exec(`CREATE VIEW governance_budget_clock AS SELECT 1 singleton,'2026-01-01T00:00:00.000000000Z' last_effective_at`); err != nil {
		t.Fatal(err)
	}
	b := NewBudget(db)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.MigrateTx(context.Background(), tx); !errors.Is(err, ErrUnavailable) && !errors.Is(err, ErrSchema) {
		t.Fatalf("malformed migration error=%v", err)
	}
	_ = tx.Rollback()
	var leaked int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='governance_budget_reservations'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatalf("migration leaked table count=%d err=%v", leaked, err)
	}
	if _, err := db.Exec(`DROP VIEW governance_budget_clock`); err != nil {
		t.Fatal(err)
	}
	migrateBudget(t, b, db)
	if _, err := db.Exec(`CREATE INDEX budget_unexpected_idx ON governance_budget_reservations(request_id)`); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.BeginTx(context.Background(), nil)
	if err := b.MigrateTx(context.Background(), tx); !errors.Is(err, ErrSchema) {
		t.Fatalf("unexpected index accepted: %v", err)
	}
	_ = tx.Rollback()
	if _, err := db.Exec(`DROP INDEX budget_unexpected_idx`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE governance_budget_clock SET last_effective_at='bad'`); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.BeginTx(context.Background(), nil)
	if err := b.MigrateTx(context.Background(), tx); !errors.Is(err, ErrSchema) {
		t.Fatalf("bad stored clock accepted: %v", err)
	}
	_ = tx.Rollback()
	if _, err := db.Exec(`UPDATE governance_budget_clock SET last_effective_at=?`, initialSettingsTime); err != nil {
		t.Fatal(err)
	}
	migrateBudget(t, b, db)
}

func newBudgetFixture(t *testing.T) *budgetFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "budget.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	ledger := accounting.NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	createBudgetExternalSchema(t, db)
	budget := NewBudget(db)
	migrateBudget(t, budget, db)
	return &budgetFixture{db: db, ledger: ledger, budget: budget, t0: time.Date(2026, 9, 24, 1, 2, 3, 4, time.UTC)}
}

func createBudgetExternalSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	statements := []string{
		`CREATE TABLE governance_settings(singleton INTEGER PRIMARY KEY,enabled INTEGER NOT NULL,budget_enabled INTEGER NOT NULL,revision INTEGER NOT NULL)`,
		`INSERT INTO governance_settings VALUES(1,1,1,1)`,
		`CREATE TABLE governance_requests(id TEXT PRIMARY KEY,employee_id TEXT NOT NULL,key_id TEXT NOT NULL,public_model TEXT NOT NULL,protocol TEXT NOT NULL,settings_revision INTEGER NOT NULL,budget_enabled INTEGER NOT NULL,observed_started_at TEXT NOT NULL,effective_started_at TEXT NOT NULL,effective_lease_at TEXT NOT NULL,expires_at TEXT NOT NULL,status TEXT NOT NULL)`,
		`CREATE TABLE governance_request_scopes(request_id TEXT NOT NULL REFERENCES governance_requests(id),scope_kind TEXT NOT NULL,scope_id TEXT NOT NULL,policy_id TEXT NOT NULL,policy_revision INTEGER NOT NULL,group_revision INTEGER,hard_tpm INTEGER,hard_cost_micro INTEGER,hard_currency TEXT NOT NULL,hard_window TEXT NOT NULL,unknown_mode TEXT NOT NULL,PRIMARY KEY(request_id,scope_kind,scope_id))`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}

func migrateBudget(t *testing.T, budget *Budget, db *sql.DB) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.MigrateTx(context.Background(), tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func (f *budgetFixture) defaultCase(id string) budgetCase {
	return budgetCase{id: id, scopeID: "scope-1", hardTPM: ptr64(1000), price: testPrice(), proof: testFourProof(accounting.UpperUsage{InputTokens: 10, OutputTokens: 10, CacheReadTokens: 10, CacheWriteTokens: 10}), observed: f.t0, expires: f.t0.Add(time.Minute), policyRev: 1, budgetOn: true, unknownMode: "deny_unknown"}
}

func (f *budgetFixture) prepare(t *testing.T, c budgetCase) {
	t.Helper()
	ctx := context.Background()
	request := accounting.RequestStart{ID: c.id, EmployeeID: "employee-1", KeyID: "key-1", ModelID: "public-model", Provider: accounting.ProviderOpenAICompatible, StartedAt: c.observed}
	if err := f.ledger.BeginRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	tx := f.begin(t)
	if err := f.ledger.BeginAttemptTx(ctx, tx, accounting.AttemptStart{ID: c.id + ":1", RequestID: c.id, AccountID: "account-1", Provider: request.Provider, Dispatch: accounting.DispatchPrimary, StartedAt: c.observed, Price: c.price}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO governance_requests(id,employee_id,key_id,public_model,protocol,settings_revision,budget_enabled,observed_started_at,effective_started_at,effective_lease_at,expires_at,status) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.id, request.EmployeeID, request.KeyID, request.ModelID, string(accounting.ProtocolOpenAIChatCompletions), 1, boolInteger(c.budgetOn), formatTime(c.observed), formatTime(c.observed), formatTime(c.observed), formatTime(c.expires), string(accounting.StatusPending)); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	currency, window := c.currency, ""
	if c.hardCost != nil {
		window = ShadowWindowRolling24h
	}
	if _, err := tx.Exec(`INSERT INTO governance_request_scopes(request_id,scope_kind,scope_id,policy_id,policy_revision,group_revision,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		c.id, string(ScopeEmployee), c.scopeID, "policy-1", c.policyRev, nil, nullableBudgetInt(c.hardTPM), nullableBudgetInt(c.hardCost), currency, window, c.unknownMode); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	f.commit(t, tx)
}

func (f *budgetFixture) reserve(t *testing.T, c budgetCase) BudgetReserveResult {
	t.Helper()
	f.prepare(t, c)
	tx := f.begin(t)
	result, err := f.budget.ReserveTx(context.Background(), tx, f.reserveInput(c))
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	f.commit(t, tx)
	return result
}

func (f *budgetFixture) reserveInput(c budgetCase) BudgetReserve {
	return BudgetReserve{RequestID: c.id, AttemptID: c.id + ":1", Proof: c.proof, ObservedAt: c.observed}
}

func (f *budgetFixture) finishAndSettle(t *testing.T, requestID string, status accounting.Status, usage [4]*int64, at time.Time) {
	t.Helper()
	tx := f.begin(t)
	if err := f.ledger.FinishAttemptTx(context.Background(), tx, accounting.AttemptFinish{ID: requestID + ":1", Status: status, FinishedAt: at,
		Usage: accounting.Usage{InputTokens: usage[0], OutputTokens: usage[1], CacheReadTokens: usage[2], CacheWriteTokens: usage[3]}}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := f.budget.SettleTx(context.Background(), tx, BudgetSettle{AttemptID: requestID + ":1", ObservedAt: at, Mode: BudgetSettleFromAttempt}); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	f.commit(t, tx)
}

func (f *budgetFixture) begin(t *testing.T) *sql.Tx {
	t.Helper()
	tx, err := f.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return tx
}
func (f *budgetFixture) commit(t *testing.T, tx *sql.Tx) {
	t.Helper()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
func (f *budgetFixture) rollback(t *testing.T, tx *sql.Tx) {
	t.Helper()
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func testPrice() *accounting.PriceSnapshot {
	return &accounting.PriceSnapshot{Version: "price-v1", Currency: "USD", InputPerMillionMicro: 1_000_000, OutputPerMillionMicro: 1_000_000, CacheReadPerMillionMicro: 1_000_000, CacheWritePerMillionMicro: 1_000_000}
}
func testFourProof(usage accounting.UpperUsage) BudgetProof {
	return BudgetProof{Type: BudgetProofFourBuckets, ActualModel: "actual-model", AccountRevision: 1, PoolRevision: 1, TransformRevision: "transform-v1", BounderID: "bounder", BounderRevision: 1, FourBuckets: &usage}
}
func testGroupProof(usage accounting.MutuallyExclusiveInputUpperUsage) BudgetProof {
	return BudgetProof{Type: BudgetProofMutuallyExclusiveInput, ActualModel: "actual-model", AccountRevision: 1, PoolRevision: 1, TransformRevision: "transform-v1", BounderID: "bounder", BounderRevision: 1, MutuallyExclusiveInput: &usage}
}
func ptr64(value int64) *int64 { return &value }
