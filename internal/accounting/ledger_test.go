package accounting

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var (
	testStart  = time.Date(2026, time.September, 23, 10, 30, 0, 123, time.FixedZone("CST", 8*60*60))
	testFinish = testStart.Add(2 * time.Second)
	testPrice  = PriceSnapshot{
		Version:                   "price-v1",
		Currency:                  "USD",
		InputPerMillionMicro:      10,
		OutputPerMillionMicro:     20,
		CacheReadPerMillionMicro:  30,
		CacheWritePerMillionMicro: 40,
	}
)

func TestLedgerLifecycleIdempotencyConflictAndSeparateSummaries(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "ledger.db"))
	defer db.Close()
	ctx := context.Background()
	request := testRequest("request-1")
	if err := ledger.BeginRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginRequest(ctx, request); err != nil {
		t.Fatalf("idempotent request begin: %v", err)
	}
	conflictingRequest := request
	conflictingRequest.ModelID = "different-model"
	assertErrorIs(t, ledger.BeginRequest(ctx, conflictingRequest), ErrConflict)

	missingParent := testAttempt("attempt-missing", "request-missing")
	assertErrorIs(t, ledger.BeginAttempt(ctx, missingParent), ErrNotFound)
	attempt := testAttempt("attempt-1", request.ID)
	if err := ledger.BeginAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttempt(ctx, attempt); err != nil {
		t.Fatalf("idempotent attempt begin: %v", err)
	}
	changedPrice := attempt
	changedPriceValue := *attempt.Price
	changedPriceValue.Version = "price-v2"
	changedPrice.Price = &changedPriceValue
	assertErrorIs(t, ledger.BeginAttempt(ctx, changedPrice), ErrConflict)
	assertErrorIs(t, ledger.FinishRequest(ctx, RequestFinish{ID: request.ID, Status: StatusSucceeded, FinishedAt: testFinish}), ErrConflict)

	input, output, cacheRead, cacheWrite := int64(1_000_000), int64(2_000_000), int64(3_000_000), int64(4_000_000)
	finish := AttemptFinish{ID: attempt.ID, Status: StatusSucceeded, FinishedAt: testFinish, Usage: Usage{
		InputTokens: &input, OutputTokens: &output, CacheReadTokens: &cacheRead, CacheWriteTokens: &cacheWrite,
	}}
	if err := ledger.FinishAttempt(ctx, finish); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishAttempt(ctx, finish); err != nil {
		t.Fatalf("idempotent attempt finish: %v", err)
	}
	conflictingFinish := finish
	conflictingFinish.Status = StatusFailed
	assertErrorIs(t, ledger.FinishAttempt(ctx, conflictingFinish), ErrConflict)
	assertErrorIs(t, ledger.FinishRequest(ctx, RequestFinish{ID: request.ID, Status: StatusSucceeded, FinishedAt: testFinish.Add(-time.Nanosecond)}), ErrInvalid)
	requestFinish := RequestFinish{ID: request.ID, Status: StatusSucceeded, FinishedAt: testFinish.Add(time.Second)}
	if err := ledger.FinishRequest(ctx, requestFinish); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequest(ctx, requestFinish); err != nil {
		t.Fatalf("idempotent request finish: %v", err)
	}
	changedRequestFinish := requestFinish
	changedRequestFinish.Status = StatusFailed
	assertErrorIs(t, ledger.FinishRequest(ctx, changedRequestFinish), ErrConflict)
	assertErrorIs(t, ledger.BeginAttempt(ctx, testAttempt("attempt-after-terminal", request.ID)), ErrConflict)
	if err := ledger.BeginAttempt(ctx, attempt); err != nil {
		t.Fatalf("exact begin replay after request terminal: %v", err)
	}

	var storedStarted, priceVersion, currency string
	var storedCost int64
	if err := db.QueryRow(`SELECT started_at,price_version,currency,cost_micro FROM accounting_attempts WHERE id=?`, attempt.ID).
		Scan(&storedStarted, &priceVersion, &currency, &storedCost); err != nil {
		t.Fatal(err)
	}
	if storedStarted != testStart.UTC().Format(time.RFC3339Nano) || priceVersion != testPrice.Version || currency != "USD" || storedCost != 300 {
		t.Fatalf("stored attempt started=%q price=%q currency=%q cost=%d", storedStarted, priceVersion, currency, storedCost)
	}
	requestSummary, err := ledger.SummarizeRequests(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if requestSummary.Total != 1 || requestSummary.Succeeded != 1 || requestSummary.Pending != 0 {
		t.Fatalf("request summary=%+v", requestSummary)
	}
	attemptSummary, err := ledger.SummarizeAttempts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(attemptSummary) != 1 {
		t.Fatalf("attempt summaries=%+v", attemptSummary)
	}
	summary := attemptSummary[0]
	if summary.Total != 1 || summary.Succeeded != 1 || summary.KnownCostMicro != 300 || summary.UnknownCostAttempts != 0 ||
		summary.InputTokens.KnownTotal != input || summary.OutputTokens.KnownTotal != output || summary.CacheReadTokens.KnownTotal != cacheRead || summary.CacheWriteTokens.KnownTotal != cacheWrite {
		t.Fatalf("attempt summary=%+v", summary)
	}
}

func TestLedgerUnknownUsageNegativeValuesAndOverflow(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "ledger.db"))
	defer db.Close()
	ctx := context.Background()

	request := testRequest("request-unknown")
	mustBeginRequestAndAttempt(t, ledger, request, testAttempt("attempt-unknown", request.ID))
	zero := int64(0)
	unknown := AttemptFinish{ID: "attempt-unknown", Status: StatusFailed, FinishedAt: testFinish, Usage: Usage{InputTokens: &zero}}
	if err := ledger.FinishAttempt(ctx, unknown); err != nil {
		t.Fatal(err)
	}
	assertErrorIs(t, ledger.FinishRequest(ctx, RequestFinish{ID: request.ID, Status: StatusSucceeded, FinishedAt: testFinish.Add(time.Second)}), ErrConflict)
	if err := ledger.FinishRequest(ctx, RequestFinish{ID: request.ID, Status: StatusFailed, FinishedAt: testFinish.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	var storedInput sql.NullInt64
	var storedOutput, storedCost sql.NullInt64
	if err := db.QueryRow(`SELECT input_tokens,output_tokens,cost_micro FROM accounting_attempts WHERE id='attempt-unknown'`).Scan(&storedInput, &storedOutput, &storedCost); err != nil {
		t.Fatal(err)
	}
	if !storedInput.Valid || storedInput.Int64 != 0 || storedOutput.Valid || storedCost.Valid {
		t.Fatalf("unknown usage stored input=%+v output=%+v cost=%+v", storedInput, storedOutput, storedCost)
	}
	unpricedRequest := testRequest("request-unpriced")
	unpricedAttempt := testAttempt("attempt-unpriced", unpricedRequest.ID)
	unpricedAttempt.Price = nil
	mustBeginRequestAndAttempt(t, ledger, unpricedRequest, unpricedAttempt)
	if err := ledger.BeginAttempt(ctx, unpricedAttempt); err != nil {
		t.Fatalf("idempotent unpriced begin: %v", err)
	}
	pricedReplay := unpricedAttempt
	price := testPrice
	pricedReplay.Price = &price
	assertErrorIs(t, ledger.BeginAttempt(ctx, pricedReplay), ErrConflict)
	million := int64(1_000_000)
	if err := ledger.FinishAttempt(ctx, AttemptFinish{ID: unpricedAttempt.ID, Status: StatusSucceeded, FinishedAt: testFinish, Usage: Usage{
		InputTokens: &million, OutputTokens: &million, CacheReadTokens: &million, CacheWriteTokens: &million,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequest(ctx, RequestFinish{ID: unpricedRequest.ID, Status: StatusSucceeded, FinishedAt: testFinish.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	var priceColumns, unpricedCost int
	if err := db.QueryRow(`SELECT
		(price_version IS NOT NULL)+(currency IS NOT NULL)+(input_rate IS NOT NULL)+(output_rate IS NOT NULL)+(cache_read_rate IS NOT NULL)+(cache_write_rate IS NOT NULL),
		(cost_micro IS NOT NULL) FROM accounting_attempts WHERE id=?`, unpricedAttempt.ID).Scan(&priceColumns, &unpricedCost); err != nil {
		t.Fatal(err)
	}
	if priceColumns != 0 || unpricedCost != 0 {
		t.Fatalf("unpriced attempt price columns=%d cost present=%d", priceColumns, unpricedCost)
	}
	summaries, err := ledger.SummarizeAttempts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 2 {
		t.Fatalf("unknown summary=%+v", summaries)
	}
	byCurrency := make(map[string]AttemptSummary, len(summaries))
	for _, summary := range summaries {
		byCurrency[summary.Currency] = summary
	}
	usd := byCurrency["USD"]
	if usd.KnownCostMicro != 0 || usd.UnknownCostAttempts != 1 || usd.InputTokens.UnknownAttempts != 0 || usd.OutputTokens.UnknownAttempts != 1 {
		t.Fatalf("USD unknown summary=%+v", usd)
	}
	unpriced := byCurrency[UnknownCurrency]
	if unpriced.Total != 1 || unpriced.KnownCostMicro != 0 || unpriced.UnknownCostAttempts != 1 || unpriced.InputTokens.KnownTotal != million || unpriced.InputTokens.UnknownAttempts != 0 {
		t.Fatalf("unpriced summary=%+v", unpriced)
	}

	negativeRequest := testRequest("request-negative")
	negativeAttempt := testAttempt("attempt-negative", negativeRequest.ID)
	mustBeginRequestAndAttempt(t, ledger, negativeRequest, negativeAttempt)
	negative := int64(-1)
	assertErrorIs(t, ledger.FinishAttempt(ctx, AttemptFinish{ID: negativeAttempt.ID, Status: StatusFailed, FinishedAt: testFinish, Usage: Usage{InputTokens: &negative}}), ErrInvalid)
	assertAttemptStatus(t, db, negativeAttempt.ID, StatusPending)

	overflowRequest := testRequest("request-overflow")
	overflowAttempt := testAttempt("attempt-overflow", overflowRequest.ID)
	overflowAttempt.Price = &PriceSnapshot{Version: "max-price", Currency: "USD", InputPerMillionMicro: math.MaxInt64, OutputPerMillionMicro: math.MaxInt64, CacheReadPerMillionMicro: math.MaxInt64, CacheWritePerMillionMicro: math.MaxInt64}
	mustBeginRequestAndAttempt(t, ledger, overflowRequest, overflowAttempt)
	maximum := int64(math.MaxInt64)
	assertErrorIs(t, ledger.FinishAttempt(ctx, AttemptFinish{ID: overflowAttempt.ID, Status: StatusSucceeded, FinishedAt: testFinish, Usage: Usage{
		InputTokens: &maximum, OutputTokens: &maximum, CacheReadTokens: &maximum, CacheWriteTokens: &maximum,
	}}), ErrInvalid)
	assertAttemptStatus(t, db, overflowAttempt.ID, StatusPending)
}

func TestLedgerConcurrentIdempotencyAndConflictingFinish(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "ledger.db"))
	defer db.Close()
	ctx := context.Background()
	request := testRequest("request-concurrent-begin")
	runConcurrent(t, 16, func() error { return ledger.BeginRequest(ctx, request) }, func(err error) bool { return err == nil })
	var requestCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_requests WHERE id=?`, request.ID).Scan(&requestCount); err != nil || requestCount != 1 {
		t.Fatalf("request count=%d err=%v", requestCount, err)
	}

	attempt := testAttempt("attempt-concurrent-finish", request.ID)
	if err := ledger.BeginAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	one := int64(1)
	finish := AttemptFinish{ID: attempt.ID, Status: StatusSucceeded, FinishedAt: testFinish, Usage: Usage{InputTokens: &one, OutputTokens: &one, CacheReadTokens: &one, CacheWriteTokens: &one}}
	runConcurrent(t, 16, func() error { return ledger.FinishAttempt(ctx, finish) }, func(err error) bool { return err == nil })
	var cost int64
	if err := db.QueryRow(`SELECT cost_micro FROM accounting_attempts WHERE id=?`, attempt.ID).Scan(&cost); err != nil || cost != 1 {
		t.Fatalf("cost=%d err=%v", cost, err)
	}

	request2 := testRequest("request-conflicting-finish")
	attempt2 := testAttempt("attempt-conflicting-finish", request2.ID)
	mustBeginRequestAndAttempt(t, ledger, request2, attempt2)
	start := make(chan struct{})
	errorsCh := make(chan error, 2)
	var wait sync.WaitGroup
	for _, status := range []Status{StatusSucceeded, StatusFailed} {
		status := status
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsCh <- ledger.FinishAttempt(ctx, AttemptFinish{ID: attempt2.ID, Status: status, FinishedAt: testFinish, Usage: Usage{}})
		}()
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	var succeeded, conflicted int
	for err := range errorsCh {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConflict):
			conflicted++
		default:
			t.Fatalf("unexpected concurrent finish error: %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("concurrent finish succeeded=%d conflicted=%d", succeeded, conflicted)
	}

	parentRequest := testRequest("request-parent-race")
	if err := ledger.BeginRequest(ctx, parentRequest); err != nil {
		t.Fatal(err)
	}
	parentAttempt := testAttempt("attempt-parent-race", parentRequest.ID)
	start = make(chan struct{})
	beginResult := make(chan error, 1)
	requestFinishResult := make(chan error, 1)
	go func() {
		<-start
		beginResult <- ledger.BeginAttempt(ctx, parentAttempt)
	}()
	go func() {
		<-start
		requestFinishResult <- ledger.FinishRequest(ctx, RequestFinish{ID: parentRequest.ID, Status: StatusFailed, FinishedAt: testFinish.Add(time.Second)})
	}()
	close(start)
	beginErr, requestFinishErr := <-beginResult, <-requestFinishResult
	if beginErr == nil {
		assertErrorIs(t, requestFinishErr, ErrConflict)
		assertAttemptStatus(t, db, parentAttempt.ID, StatusPending)
	} else {
		assertErrorIs(t, beginErr, ErrConflict)
		if requestFinishErr != nil {
			t.Fatalf("parent finish error=%v", requestFinishErr)
		}
		var attemptCount int
		if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE id=?`, parentAttempt.ID).Scan(&attemptCount); err != nil || attemptCount != 0 {
			t.Fatalf("attempt count=%d err=%v", attemptCount, err)
		}
	}
}

func TestLedgerRestartRecoveryAndFinishCollision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, ledger := openTestLedger(t, path)
	request := testRequest("request-restart")
	attempt := testAttempt("attempt-restart", request.ID)
	mustBeginRequestAndAttempt(t, ledger, request, attempt)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, ledger = openTestLedger(t, path)
	defer db.Close()
	if _, err := ledger.RecoverInterrupted(context.Background(), testStart.Add(-time.Nanosecond)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("early recovery error=%v", err)
	}
	assertAttemptStatus(t, db, attempt.ID, StatusPending)
	var requestStatus Status
	if err := db.QueryRow(`SELECT status FROM accounting_requests WHERE id=?`, request.ID).Scan(&requestStatus); err != nil || requestStatus != StatusPending {
		t.Fatalf("request status after rejected recovery=%q err=%v", requestStatus, err)
	}
	recovered, err := ledger.RecoverInterrupted(context.Background(), testFinish)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Requests != 1 || recovered.Attempts != 1 {
		t.Fatalf("recovered=%+v", recovered)
	}
	second, err := ledger.RecoverInterrupted(context.Background(), testFinish.Add(time.Second))
	if err != nil || second != (RecoveryResult{}) {
		t.Fatalf("second recovery=%+v err=%v", second, err)
	}
	assertAttemptStatus(t, db, attempt.ID, StatusInterrupted)
	var usageCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE id=? AND input_tokens IS NULL AND output_tokens IS NULL AND cache_read_tokens IS NULL AND cache_write_tokens IS NULL AND cost_micro IS NULL`, attempt.ID).Scan(&usageCount); err != nil || usageCount != 1 {
		t.Fatalf("recovered unknown usage count=%d err=%v", usageCount, err)
	}
	assertErrorIs(t, ledger.FinishAttempt(context.Background(), AttemptFinish{ID: attempt.ID, Status: StatusSucceeded, FinishedAt: testFinish.Add(2 * time.Second), Usage: Usage{}}), ErrConflict)
	childTimeRequest := testRequest("request-recovery-child-time")
	childTimeAttempt := testAttempt("attempt-recovery-child-time", childTimeRequest.ID)
	mustBeginRequestAndAttempt(t, ledger, childTimeRequest, childTimeAttempt)
	one := int64(1)
	childFinished := testFinish.Add(10 * time.Second)
	if err := ledger.FinishAttempt(context.Background(), AttemptFinish{ID: childTimeAttempt.ID, Status: StatusSucceeded, FinishedAt: childFinished, Usage: Usage{
		InputTokens: &one, OutputTokens: &one, CacheReadTokens: &one, CacheWriteTokens: &one,
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.RecoverInterrupted(context.Background(), childFinished.Add(-time.Nanosecond)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("recovery before terminal child error=%v", err)
	}
	if err := ledger.FinishRequest(context.Background(), RequestFinish{ID: childTimeRequest.ID, Status: StatusSucceeded, FinishedAt: childFinished}); err != nil {
		t.Fatal(err)
	}

	collisionRequest := testRequest("request-recovery-collision")
	collisionAttempt := testAttempt("attempt-recovery-collision", collisionRequest.ID)
	mustBeginRequestAndAttempt(t, ledger, collisionRequest, collisionAttempt)
	finish := AttemptFinish{ID: collisionAttempt.ID, Status: StatusSucceeded, FinishedAt: testFinish.Add(3 * time.Second), Usage: Usage{InputTokens: &one, OutputTokens: &one, CacheReadTokens: &one, CacheWriteTokens: &one}}
	start := make(chan struct{})
	finishResult := make(chan error, 1)
	recoveryResult := make(chan error, 1)
	go func() {
		<-start
		finishResult <- ledger.FinishAttempt(context.Background(), finish)
	}()
	go func() {
		<-start
		_, err := ledger.RecoverInterrupted(context.Background(), testFinish.Add(4*time.Second))
		recoveryResult <- err
	}()
	close(start)
	finishErr, recoveryErr := <-finishResult, <-recoveryResult
	if recoveryErr != nil || finishErr != nil && !errors.Is(finishErr, ErrConflict) {
		t.Fatalf("finish error=%v recovery error=%v", finishErr, recoveryErr)
	}
	var status Status
	var storedCost sql.NullInt64
	if err := db.QueryRow(`SELECT status,cost_micro FROM accounting_attempts WHERE id=?`, collisionAttempt.ID).Scan(&status, &storedCost); err != nil {
		t.Fatal(err)
	}
	if status == StatusSucceeded {
		if finishErr != nil || !storedCost.Valid || storedCost.Int64 != 1 {
			t.Fatalf("succeeded collision status=%q cost=%+v finishErr=%v", status, storedCost, finishErr)
		}
	} else if status == StatusInterrupted {
		if !errors.Is(finishErr, ErrConflict) || storedCost.Valid {
			t.Fatalf("interrupted collision status=%q cost=%+v finishErr=%v", status, storedCost, finishErr)
		}
	} else {
		t.Fatalf("collision left status=%q", status)
	}
}

func TestLedgerMigrationRollbackRetryAndMetadataOnlySchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE accounting_attempts (blocking INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ledger := NewLedger(db)
	if err := ledger.Migrate(context.Background()); err == nil {
		t.Fatal("migration unexpectedly succeeded with conflicting table")
	}
	var requestsTable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='accounting_requests'`).Scan(&requestsTable); err != nil || requestsTable != 0 {
		t.Fatalf("failed migration left request table count=%d err=%v", requestsTable, err)
	}
	if _, err := db.Exec(`DROP TABLE accounting_attempts`); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}

	const bodyMarker = "prompt body secret with spaces and newlines"
	invalid := testRequest("request-invalid-body")
	invalid.ModelID = bodyMarker
	assertErrorIs(t, ledger.BeginRequest(context.Background(), invalid), ErrInvalid)
	for _, table := range []string{"accounting_requests", "accounting_attempts"} {
		rows, err := db.Query(`PRAGMA table_info(` + table + `)`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var columnID, notNull, primaryKey int
			var name, kind string
			var defaultValue any
			if err := rows.Scan(&columnID, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			for _, forbidden := range []string{"body", "prompt", "response", "error", "header", "log"} {
				if bytes.Contains([]byte(name), []byte(forbidden)) {
					rows.Close()
					t.Fatalf("unexpected content column %q", name)
				}
			}
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte(bodyMarker)) {
		t.Fatal("rejected arbitrary body text was persisted")
	}
}

func TestLedgerMigrationRejectsCounterfeitSchemas(t *testing.T) {
	t.Run("view", func(t *testing.T) {
		db := openRawTestDB(t, filepath.Join(t.TempDir(), "view.db"))
		defer db.Close()
		if _, err := db.Exec(`CREATE VIEW accounting_requests AS SELECT 'request' AS id`); err != nil {
			t.Fatal(err)
		}
		if err := NewLedger(db).Migrate(context.Background()); err == nil {
			t.Fatal("migration accepted a view")
		}
		assertSchemaObjectCount(t, db, "accounting_attempts", 0)
	})

	for _, test := range []struct {
		name  string
		extra string
	}{
		{name: "missing checks"},
		{name: "extra sensitive column", extra: ",prompt_body TEXT"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := openRawTestDB(t, filepath.Join(t.TempDir(), "counterfeit.db"))
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE accounting_requests (
				id TEXT PRIMARY KEY,
				employee_id TEXT NOT NULL,
				key_id TEXT NOT NULL,
				model_id TEXT NOT NULL,
				provider TEXT NOT NULL,
				started_at TEXT NOT NULL,
				finished_at TEXT,
				status TEXT NOT NULL` + test.extra + `
			)`); err != nil {
				t.Fatal(err)
			}
			ledger := NewLedger(db)
			if err := ledger.Migrate(context.Background()); err == nil {
				t.Fatal("migration accepted counterfeit request table")
			}
			assertSchemaObjectCount(t, db, "accounting_attempts", 0)
			var indexes int
			if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name LIKE 'accounting_%_idx'`).Scan(&indexes); err != nil || indexes != 0 {
				t.Fatalf("failed validation left indexes=%d err=%v", indexes, err)
			}
			if _, err := db.Exec(`DROP TABLE accounting_requests`); err != nil {
				t.Fatal(err)
			}
			if err := ledger.Migrate(context.Background()); err != nil {
				t.Fatalf("retry after removing counterfeit table: %v", err)
			}
		})
	}

	t.Run("missing foreign key", func(t *testing.T) {
		db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "foreign-key.db"))
		defer db.Close()
		var definition string
		if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='accounting_attempts'`).Scan(&definition); err != nil {
			t.Fatal(err)
		}
		withoutForeignKey := strings.Replace(definition, "request_id TEXT NOT NULL REFERENCES accounting_requests(id)", "request_id TEXT NOT NULL", 1)
		if withoutForeignKey == definition {
			t.Fatal("test could not remove accounting foreign key")
		}
		for _, statement := range []string{
			`PRAGMA foreign_keys=OFF`,
			`ALTER TABLE accounting_attempts RENAME TO accounting_attempts_old`,
			withoutForeignKey,
			`DROP TABLE accounting_attempts_old`,
			`PRAGMA foreign_keys=ON`,
		} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatalf("rebuild without foreign key: %v", err)
			}
		}
		if err := ledger.Migrate(context.Background()); err == nil {
			t.Fatal("migration accepted missing attempt foreign key")
		}
		var indexes int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND tbl_name='accounting_attempts' AND name LIKE 'accounting_%_idx'`).Scan(&indexes); err != nil || indexes != 0 {
			t.Fatalf("failed foreign-key validation left indexes=%d err=%v", indexes, err)
		}
		if _, err := db.Exec(`DROP TABLE accounting_attempts`); err != nil {
			t.Fatal(err)
		}
		if err := ledger.Migrate(context.Background()); err != nil {
			t.Fatalf("retry after restoring foreign key: %v", err)
		}
	})
}

func openTestLedger(t *testing.T, path string) (*sql.DB, *Ledger) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{`PRAGMA foreign_keys=ON`, `PRAGMA busy_timeout=5000`} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	ledger := NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, ledger
}

func openRawTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{`PRAGMA foreign_keys=ON`, `PRAGMA busy_timeout=5000`} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func assertSchemaObjectCount(t *testing.T, db *sql.DB, name string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("schema object %q count=%d want=%d", name, got, want)
	}
}

func testRequest(id string) RequestStart {
	return RequestStart{ID: id, EmployeeID: "employee-1", KeyID: "key-1", ModelID: "model-1", Provider: ProviderGemini, StartedAt: testStart}
}

func testAttempt(id, requestID string) AttemptStart {
	price := testPrice
	return AttemptStart{ID: id, RequestID: requestID, AccountID: "account-1", Provider: ProviderGemini, Dispatch: DispatchPrimary, StartedAt: testStart, Price: &price}
}

func mustBeginRequestAndAttempt(t *testing.T, ledger *Ledger, request RequestStart, attempt AttemptStart) {
	t.Helper()
	if err := ledger.BeginRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
}

func assertAttemptStatus(t *testing.T, db *sql.DB, id string, want Status) {
	t.Helper()
	var got Status
	if err := db.QueryRow(`SELECT status FROM accounting_attempts WHERE id=?`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("attempt status=%q want=%q", got, want)
	}
}

func assertErrorIs(t *testing.T, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Fatalf("error=%v want %v", err, target)
	}
}

func runConcurrent(t *testing.T, count int, operation func() error, accept func(error) bool) {
	t.Helper()
	start := make(chan struct{})
	errorsCh := make(chan error, count)
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			errorsCh <- operation()
		}()
	}
	close(start)
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if !accept(err) {
			t.Fatalf("concurrent error: %v", err)
		}
	}
}
