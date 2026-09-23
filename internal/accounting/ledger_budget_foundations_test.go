package accounting

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestLedgerBeginAttemptTxVisibilityRollbackAndCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, ledger := openTestLedger(t, path)
	defer db.Close()
	ctx := context.Background()
	request := testRequest("request-budget-tx")
	if err := ledger.BeginRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	attempt := testAttempt("attempt-budget-tx", request.ID)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttemptTx(ctx, tx, attempt); err != nil {
		t.Fatal(err)
	}
	if got := countAttemptTx(t, tx, attempt.ID); got != 1 {
		t.Fatalf("caller transaction attempt count=%d", got)
	}

	observer, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()
	observer.SetMaxOpenConns(1)
	if _, err := observer.Exec(`PRAGMA busy_timeout=1000`); err != nil {
		t.Fatal(err)
	}
	if got := countAttemptDB(t, observer, attempt.ID); got != 0 {
		t.Fatalf("uncommitted attempt visible outside transaction: count=%d", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := countAttemptDB(t, observer, attempt.ID); got != 1 {
		t.Fatalf("committed attempt count=%d", got)
	}

	if _, err := db.Exec(`CREATE TABLE budget_sibling(attempt_id TEXT PRIMARY KEY,value TEXT NOT NULL CHECK(value='valid'))`); err != nil {
		t.Fatal(err)
	}
	rolledBack := testAttempt("attempt-budget-rollback", request.ID)
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttemptTx(ctx, tx, rolledBack); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO budget_sibling(attempt_id,value) VALUES(?,?)`, rolledBack.ID, "invalid"); err == nil {
		t.Fatal("sibling failure injection unexpectedly succeeded")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := countAttemptDB(t, db, rolledBack.ID); got != 0 {
		t.Fatalf("rolled-back attempt count=%d", got)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := ledger.BeginAttemptTx(ctx, tx, rolledBack); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO budget_sibling(attempt_id,value) VALUES(?,?)`, rolledBack.ID, "valid"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := countAttemptDB(t, db, rolledBack.ID); got != 1 {
		t.Fatalf("retried attempt count=%d", got)
	}
}

func TestLedgerBeginAttemptTxIdempotencyValidationCancellationAndCommitFailure(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "ledger.db"))
	defer db.Close()
	ctx := context.Background()
	request := testRequest("request-budget-validation")
	attempt := testAttempt("attempt-budget-validation", request.ID)
	mustBeginRequestAndAttempt(t, ledger, request, attempt)
	one := int64(1)
	if err := ledger.FinishAttempt(ctx, AttemptFinish{ID: attempt.ID, Status: StatusSucceeded, FinishedAt: testFinish, Usage: Usage{
		InputTokens: &one, OutputTokens: &one, CacheReadTokens: &one, CacheWriteTokens: &one,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequest(ctx, RequestFinish{ID: request.ID, Status: StatusSucceeded, FinishedAt: testFinish.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttemptTx(ctx, tx, attempt); err != nil {
		t.Fatalf("exact replay after terminal parent: %v", err)
	}
	changed := attempt
	changed.AccountID = "different-account"
	if err := ledger.BeginAttemptTx(ctx, tx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	if err := ledger.BeginAttemptTx(ctx, nil, attempt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil transaction error=%v", err)
	}
	if err := ledger.BeginAttemptTx(nil, &sql.Tx{}, attempt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil context error=%v", err)
	}
	var nilLedger *Ledger
	if err := nilLedger.BeginAttemptTx(ctx, &sql.Tx{}, attempt); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil ledger error=%v", err)
	}

	activeRequest := testRequest("request-budget-cancel")
	if err := ledger.BeginRequest(ctx, activeRequest); err != nil {
		t.Fatal(err)
	}
	cancelledAttempt := testAttempt("attempt-budget-cancel", activeRequest.ID)
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := ledger.BeginAttemptTx(cancelled, tx, cancelledAttempt); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled transaction error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := countAttemptDB(t, db, cancelledAttempt.ID); got != 0 {
		t.Fatalf("cancelled attempt count=%d", got)
	}

	commitFailure := testAttempt("attempt-budget-commit-failure", activeRequest.ID)
	commitContext, cancelCommit := context.WithCancel(ctx)
	tx, err = db.BeginTx(commitContext, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttemptTx(commitContext, tx, commitFailure); err != nil {
		t.Fatal(err)
	}
	cancelCommit()
	if err := tx.Commit(); err == nil {
		t.Fatal("cancelled transaction commit unexpectedly succeeded")
	}
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("rollback after failed commit: %v", err)
	}
	if got := countAttemptDB(t, db, commitFailure.ID); got != 0 {
		t.Fatalf("commit-failed attempt count=%d", got)
	}
	if err := ledger.BeginAttempt(ctx, commitFailure); err != nil {
		t.Fatalf("retry after commit failure: %v", err)
	}
}

func TestLedgerBeginAttemptTxPreservesParentProviderTimeAndPriceChecks(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "ledger.db"))
	defer db.Close()
	ctx := context.Background()
	request := testRequest("request-budget-parent-checks")
	if err := ledger.BeginRequest(ctx, request); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		attempt AttemptStart
		want    error
	}{
		{name: "missing parent", attempt: testAttempt("attempt-missing-parent", "missing-parent"), want: ErrNotFound},
		{name: "provider mismatch", attempt: func() AttemptStart {
			value := testAttempt("attempt-provider-mismatch", request.ID)
			value.Provider = ProviderAnthropic
			return value
		}(), want: ErrConflict},
		{name: "before parent", attempt: func() AttemptStart {
			value := testAttempt("attempt-before-parent", request.ID)
			value.StartedAt = request.StartedAt.Add(-time.Nanosecond)
			return value
		}(), want: ErrInvalid},
		{name: "invalid price", attempt: func() AttemptStart {
			value := testAttempt("attempt-invalid-price", request.ID)
			value.Price.Currency = "usd"
			return value
		}(), want: ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := ledger.BeginAttemptTx(ctx, tx, test.attempt); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestCalculateUpperCostMatchesIndependentBigIntegerOracle(t *testing.T) {
	base := PriceSnapshot{Version: "price-upper-v1", Currency: "USD"}
	tests := []struct {
		name  string
		usage UpperUsage
		price PriceSnapshot
	}{
		{name: "explicit zero", usage: UpperUsage{}, price: PriceSnapshot{Version: "price-zero", Currency: "USD", InputPerMillionMicro: MaxPriceRate, OutputPerMillionMicro: MaxPriceRate, CacheReadPerMillionMicro: MaxPriceRate, CacheWritePerMillionMicro: MaxPriceRate}},
		{name: "zero price", usage: UpperUsage{InputTokens: math.MaxInt64, OutputTokens: math.MaxInt64, CacheReadTokens: math.MaxInt64, CacheWriteTokens: math.MaxInt64}, price: PriceSnapshot{Version: "price-free", Currency: "USD"}},
		{name: "ceil one", usage: UpperUsage{InputTokens: 1}, price: withUpperRates(base, 1, 0, 0, 0)},
		{name: "exact million", usage: UpperUsage{InputTokens: 1_000_000}, price: withUpperRates(base, 10, 0, 0, 0)},
		{name: "all buckets", usage: UpperUsage{InputTokens: 1_234_567, OutputTokens: 2_345_678, CacheReadTokens: 3_456_789, CacheWriteTokens: 4_567_891}, price: withUpperRates(base, 11, 22, 33, 44)},
		{name: "max int64 exact", usage: UpperUsage{InputTokens: math.MaxInt64}, price: withUpperRates(base, 1_000_000, 0, 0, 0)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			want, ok := upperCostOracle(test.usage, test.price)
			if !ok {
				t.Fatal("test oracle unexpectedly overflowed")
			}
			got, err := CalculateUpperCost(test.usage, test.price)
			if err != nil || got != want {
				t.Fatalf("cost=%d err=%v want=%d", got, err, want)
			}
		})
	}
}

func TestCalculateUpperCostRejectsInvalidAndOverflow(t *testing.T) {
	base := PriceSnapshot{Version: "price-upper-v1", Currency: "USD", InputPerMillionMicro: 1}
	tests := []struct {
		name  string
		usage UpperUsage
		price PriceSnapshot
	}{
		{name: "negative input", usage: UpperUsage{InputTokens: -1}, price: base},
		{name: "negative output", usage: UpperUsage{OutputTokens: -1}, price: base},
		{name: "unknown is not zero", usage: UpperUsage{}, price: PriceSnapshot{}},
		{name: "bad currency", usage: UpperUsage{}, price: PriceSnapshot{Version: "price", Currency: "usd"}},
		{name: "negative rate", usage: UpperUsage{}, price: PriceSnapshot{Version: "price", Currency: "USD", InputPerMillionMicro: -1}},
		{name: "rate above catalog maximum", usage: UpperUsage{}, price: PriceSnapshot{Version: "price", Currency: "USD", InputPerMillionMicro: MaxPriceRate + 1}},
		{name: "cost above max int64", usage: UpperUsage{InputTokens: math.MaxInt64, OutputTokens: 1}, price: withUpperRates(base, 1_000_000, 1, 0, 0)},
		{name: "large multiplication overflow", usage: UpperUsage{InputTokens: math.MaxInt64, OutputTokens: math.MaxInt64, CacheReadTokens: math.MaxInt64, CacheWriteTokens: math.MaxInt64}, price: withUpperRates(base, MaxPriceRate, MaxPriceRate, MaxPriceRate, MaxPriceRate)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got, err := CalculateUpperCost(test.usage, test.price); !errors.Is(err, ErrInvalid) || got != 0 {
				t.Fatalf("cost=%d error=%v", got, err)
			}
		})
	}
}

func upperCostOracle(usage UpperUsage, price PriceSnapshot) (int64, bool) {
	values := []int64{usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens}
	rates := []int64{price.InputPerMillionMicro, price.OutputPerMillionMicro, price.CacheReadPerMillionMicro, price.CacheWritePerMillionMicro}
	total := new(big.Int)
	for index, value := range values {
		product := new(big.Int).Mul(big.NewInt(value), big.NewInt(rates[index]))
		total.Add(total, product)
	}
	total.Add(total, big.NewInt(999_999))
	total.Quo(total, big.NewInt(1_000_000))
	return total.Int64(), total.IsInt64()
}

func withUpperRates(price PriceSnapshot, input, output, cacheRead, cacheWrite int64) PriceSnapshot {
	price.InputPerMillionMicro = input
	price.OutputPerMillionMicro = output
	price.CacheReadPerMillionMicro = cacheRead
	price.CacheWritePerMillionMicro = cacheWrite
	return price
}

func countAttemptTx(t *testing.T, tx *sql.Tx, id string) int {
	t.Helper()
	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func countAttemptDB(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
