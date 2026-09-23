package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrConflict = errors.New("accounting conflict")
	ErrInvalid  = errors.New("invalid accounting metadata")
	ErrNotFound = errors.New("accounting record not found")
)

type Provider string

const (
	ProviderOpenAI           Provider = "openai"
	ProviderOpenAICompatible Provider = "openai-compatible"
	ProviderAnthropic        Provider = "anthropic"
	ProviderGemini           Provider = "gemini"
	ProviderCodex            Provider = "codex"
)

type Dispatch string

const (
	DispatchPrimary  Dispatch = "primary"
	DispatchRetry    Dispatch = "retry"
	DispatchFailover Dispatch = "failover"
)

type Status string

const (
	StatusPending     Status = "pending"
	StatusSucceeded   Status = "succeeded"
	StatusFailed      Status = "failed"
	StatusCancelled   Status = "cancelled"
	StatusInterrupted Status = "interrupted"
)

type RequestStart struct {
	ID         string
	EmployeeID string
	KeyID      string
	ModelID    string
	Provider   Provider
	StartedAt  time.Time
}

type PriceSnapshot struct {
	Version                   string
	Currency                  string
	InputPerMillionMicro      int64
	OutputPerMillionMicro     int64
	CacheReadPerMillionMicro  int64
	CacheWritePerMillionMicro int64
}

type AttemptStart struct {
	ID        string
	RequestID string
	AccountID string
	Provider  Provider
	Dispatch  Dispatch
	StartedAt time.Time
	Price     *PriceSnapshot
}

type Usage struct {
	InputTokens      *int64
	OutputTokens     *int64
	CacheReadTokens  *int64
	CacheWriteTokens *int64
}

type AttemptFinish struct {
	ID         string
	Status     Status
	FinishedAt time.Time
	Usage      Usage
}

type RequestFinish struct {
	ID         string
	Status     Status
	FinishedAt time.Time
}

type RecoveryResult struct {
	Requests int64
	Attempts int64
}

type RequestSummary struct {
	Total       int64
	Pending     int64
	Succeeded   int64
	Failed      int64
	Cancelled   int64
	Interrupted int64
}

type KnownValueSummary struct {
	KnownTotal      int64
	UnknownAttempts int64
}

type AttemptSummary struct {
	Currency            string
	Total               int64
	Pending             int64
	Succeeded           int64
	Failed              int64
	Cancelled           int64
	Interrupted         int64
	KnownCostMicro      int64
	UnknownCostAttempts int64
	InputTokens         KnownValueSummary
	OutputTokens        KnownValueSummary
	CacheReadTokens     KnownValueSummary
	CacheWriteTokens    KnownValueSummary
}

const UnknownCurrency = "UNKNOWN"

type Ledger struct {
	db *sql.DB
}

func NewLedger(db *sql.DB) *Ledger {
	return &Ledger{db: db}
}

func (l *Ledger) Migrate(ctx context.Context) error {
	if l == nil || l.db == nil {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS accounting_requests (
			id TEXT PRIMARY KEY,
			employee_id TEXT NOT NULL,
			key_id TEXT NOT NULL,
			model_id TEXT NOT NULL,
			provider TEXT NOT NULL CHECK(provider IN ('openai','openai-compatible','anthropic','gemini','codex')),
			started_at TEXT NOT NULL,
			finished_at TEXT,
			status TEXT NOT NULL CHECK(status IN ('pending','succeeded','failed','cancelled','interrupted')),
			CHECK((status='pending' AND finished_at IS NULL) OR (status<>'pending' AND finished_at IS NOT NULL))
		)`,
		`CREATE TABLE IF NOT EXISTS accounting_attempts (
			id TEXT PRIMARY KEY,
			request_id TEXT NOT NULL REFERENCES accounting_requests(id),
			account_id TEXT NOT NULL,
			provider TEXT NOT NULL CHECK(provider IN ('openai','openai-compatible','anthropic','gemini','codex')),
			dispatch TEXT NOT NULL CHECK(dispatch IN ('primary','retry','failover')),
			started_at TEXT NOT NULL,
			finished_at TEXT,
			status TEXT NOT NULL CHECK(status IN ('pending','succeeded','failed','cancelled','interrupted')),
			input_tokens INTEGER CHECK(input_tokens IS NULL OR input_tokens>=0),
			output_tokens INTEGER CHECK(output_tokens IS NULL OR output_tokens>=0),
			cache_read_tokens INTEGER CHECK(cache_read_tokens IS NULL OR cache_read_tokens>=0),
			cache_write_tokens INTEGER CHECK(cache_write_tokens IS NULL OR cache_write_tokens>=0),
			price_version TEXT,
			currency TEXT CHECK(currency IS NULL OR (length(currency)=3 AND currency=upper(currency))),
			input_rate INTEGER CHECK(input_rate IS NULL OR input_rate>=0),
			output_rate INTEGER CHECK(output_rate IS NULL OR output_rate>=0),
			cache_read_rate INTEGER CHECK(cache_read_rate IS NULL OR cache_read_rate>=0),
			cache_write_rate INTEGER CHECK(cache_write_rate IS NULL OR cache_write_rate>=0),
			cost_micro INTEGER CHECK(cost_micro IS NULL OR cost_micro>=0),
			CHECK(
				(price_version IS NULL AND currency IS NULL AND input_rate IS NULL AND output_rate IS NULL AND cache_read_rate IS NULL AND cache_write_rate IS NULL)
				OR
				(price_version IS NOT NULL AND currency IS NOT NULL AND input_rate IS NOT NULL AND output_rate IS NOT NULL AND cache_read_rate IS NOT NULL AND cache_write_rate IS NOT NULL)
			),
			CHECK(
				(status='pending' AND finished_at IS NULL AND input_tokens IS NULL AND output_tokens IS NULL AND cache_read_tokens IS NULL AND cache_write_tokens IS NULL AND cost_micro IS NULL)
				OR
				(status<>'pending' AND finished_at IS NOT NULL)
			),
			CHECK(
				(price_version IS NOT NULL AND input_tokens IS NOT NULL AND output_tokens IS NOT NULL AND cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL AND cost_micro IS NOT NULL)
				OR
				((price_version IS NULL OR input_tokens IS NULL OR output_tokens IS NULL OR cache_read_tokens IS NULL OR cache_write_tokens IS NULL) AND cost_micro IS NULL)
			)
		)`,
		`CREATE INDEX IF NOT EXISTS accounting_requests_employee_idx ON accounting_requests(employee_id, started_at)`,
		`CREATE INDEX IF NOT EXISTS accounting_requests_model_idx ON accounting_requests(model_id, started_at)`,
		`CREATE INDEX IF NOT EXISTS accounting_attempts_request_idx ON accounting_attempts(request_id, started_at)`,
		`CREATE INDEX IF NOT EXISTS accounting_attempts_account_idx ON accounting_attempts(account_id, started_at)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate accounting ledger: %w", err)
		}
	}
	if err := validateLedgerSchema(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (l *Ledger) BeginRequest(ctx context.Context, input RequestStart) error {
	startedAt, err := validateRequestStart(input)
	if err != nil {
		return err
	}
	tx, err := l.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := beginRequestTx(ctx, tx, input, startedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginRequestTx participates in a caller-owned admission transaction. It does
// not commit or roll back, so sibling metadata cannot outlive a failed start.
func (l *Ledger) BeginRequestTx(ctx context.Context, tx *sql.Tx, input RequestStart) error {
	if l == nil || l.db == nil || ctx == nil || tx == nil {
		return ErrInvalid
	}
	startedAt, err := validateRequestStart(input)
	if err != nil {
		return err
	}
	return beginRequestTx(ctx, tx, input, startedAt)
}

func beginRequestTx(ctx context.Context, tx *sql.Tx, input RequestStart, startedAt string) error {
	result, err := tx.ExecContext(ctx, `INSERT INTO accounting_requests(
		id,employee_id,key_id,model_id,provider,started_at,status
	) VALUES(?,?,?,?,?,?,'pending') ON CONFLICT(id) DO NOTHING`,
		input.ID, input.EmployeeID, input.KeyID, input.ModelID, string(input.Provider), startedAt)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		var existing RequestStart
		var storedStarted string
		if err := tx.QueryRowContext(ctx, `SELECT id,employee_id,key_id,model_id,provider,started_at FROM accounting_requests WHERE id=?`, input.ID).
			Scan(&existing.ID, &existing.EmployeeID, &existing.KeyID, &existing.ModelID, &existing.Provider, &storedStarted); err != nil {
			return err
		}
		if existing.EmployeeID != input.EmployeeID || existing.KeyID != input.KeyID || existing.ModelID != input.ModelID || existing.Provider != input.Provider || storedStarted != startedAt {
			return fmt.Errorf("%w: request start differs", ErrConflict)
		}
	}
	return nil
}

func (l *Ledger) BeginAttempt(ctx context.Context, input AttemptStart) error {
	startedAt, err := validateAttemptStart(input)
	if err != nil {
		return err
	}
	tx, err := l.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	matched, err := sameAttemptStart(ctx, tx, input, startedAt)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return err
	}
	if matched {
		return tx.Commit()
	}
	if !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("%w: attempt start differs", ErrConflict)
	}
	parent, err := tx.ExecContext(ctx, `UPDATE accounting_requests SET status=status WHERE id=? AND status='pending'`, input.RequestID)
	if err != nil {
		return err
	}
	rows, err := parent.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		var exists int
		if scanErr := tx.QueryRowContext(ctx, `SELECT 1 FROM accounting_requests WHERE id=?`, input.RequestID).Scan(&exists); errors.Is(scanErr, sql.ErrNoRows) {
			return ErrNotFound
		} else if scanErr != nil {
			return scanErr
		}
		return fmt.Errorf("%w: request is terminal", ErrConflict)
	}
	var parentStarted string
	var parentProvider Provider
	if err := tx.QueryRowContext(ctx, `SELECT started_at,provider FROM accounting_requests WHERE id=?`, input.RequestID).Scan(&parentStarted, &parentProvider); err != nil {
		return err
	}
	if parentProvider != input.Provider {
		return fmt.Errorf("%w: attempt provider differs from request", ErrConflict)
	}
	if err := requireNotBefore(input.StartedAt, parentStarted); err != nil {
		return err
	}
	arguments := []any{input.ID, input.RequestID, input.AccountID, string(input.Provider), string(input.Dispatch), startedAt}
	arguments = append(arguments, priceValues(input.Price)...)
	result, err := tx.ExecContext(ctx, `INSERT INTO accounting_attempts(
		id,request_id,account_id,provider,dispatch,started_at,status,price_version,currency,input_rate,output_rate,cache_read_rate,cache_write_rate
	) VALUES(?,?,?,?,?,?,'pending',?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		arguments...)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 0 {
		matched, err := sameAttemptStart(ctx, tx, input, startedAt)
		if err != nil {
			return err
		}
		if !matched {
			return fmt.Errorf("%w: attempt start differs", ErrConflict)
		}
	}
	return tx.Commit()
}

func (l *Ledger) FinishAttempt(ctx context.Context, input AttemptFinish) error {
	finishedAt, err := validateAttemptFinish(input)
	if err != nil {
		return err
	}
	tx, err := l.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := finishAttemptTx(ctx, tx, input, finishedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishAttemptTx finishes an attempt inside a caller-owned transaction. It
// neither commits nor rolls back tx.
func (l *Ledger) FinishAttemptTx(ctx context.Context, tx *sql.Tx, input AttemptFinish) error {
	if l == nil || l.db == nil || tx == nil {
		return ErrInvalid
	}
	finishedAt, err := validateAttemptFinish(input)
	if err != nil {
		return err
	}
	return finishAttemptTx(ctx, tx, input, finishedAt)
}

func finishAttemptTx(ctx context.Context, tx *sql.Tx, input AttemptFinish, finishedAt string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE accounting_attempts SET status=status WHERE id=?`, input.ID); err != nil {
		return err
	}
	row, err := loadAttemptFinish(ctx, tx, input.ID)
	if err != nil {
		return err
	}
	if err := requireNotBefore(input.FinishedAt, row.startedAt); err != nil {
		return err
	}
	cost, err := calculateCost(input.Usage, row.price)
	if err != nil {
		return err
	}
	if row.status != StatusPending {
		if row.status != input.Status || row.finishedAt.String != finishedAt || !sameUsage(row.usage, input.Usage) || !sameNullableInt(row.cost, cost) {
			return fmt.Errorf("%w: attempt finish differs", ErrConflict)
		}
		return nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE accounting_attempts SET
		status=?,finished_at=?,input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=?,cost_micro=?
		WHERE id=? AND status='pending'`, string(input.Status), finishedAt,
		nullableValue(input.Usage.InputTokens), nullableValue(input.Usage.OutputTokens), nullableValue(input.Usage.CacheReadTokens), nullableValue(input.Usage.CacheWriteTokens), nullableValue(cost), input.ID)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if updated != 1 {
		return fmt.Errorf("%w: attempt was already finished", ErrConflict)
	}
	return nil
}

func (l *Ledger) FinishRequest(ctx context.Context, input RequestFinish) error {
	finishedAt, err := validateRequestFinish(input)
	if err != nil {
		return err
	}
	tx, err := l.begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := finishRequestTx(ctx, tx, input, finishedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// FinishRequestTx finishes a request inside a caller-owned transaction. It
// neither commits nor rolls back tx.
func (l *Ledger) FinishRequestTx(ctx context.Context, tx *sql.Tx, input RequestFinish) error {
	if l == nil || l.db == nil || tx == nil {
		return ErrInvalid
	}
	finishedAt, err := validateRequestFinish(input)
	if err != nil {
		return err
	}
	return finishRequestTx(ctx, tx, input, finishedAt)
}

func finishRequestTx(ctx context.Context, tx *sql.Tx, input RequestFinish, finishedAt string) error {
	result, err := tx.ExecContext(ctx, `UPDATE accounting_requests SET status=status WHERE id=?`, input.ID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return ErrNotFound
	}
	var status Status
	var startedAt string
	var storedFinished sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT status,started_at,finished_at FROM accounting_requests WHERE id=?`, input.ID).Scan(&status, &startedAt, &storedFinished); err != nil {
		return err
	}
	if err := requireNotBefore(input.FinishedAt, startedAt); err != nil {
		return err
	}
	if status != StatusPending {
		if status != input.Status || !storedFinished.Valid || storedFinished.String != finishedAt {
			return fmt.Errorf("%w: request finish differs", ErrConflict)
		}
		return nil
	}
	pendingAttempts, succeededAttempts, err := requestAttemptState(ctx, tx, input.ID, input.FinishedAt)
	if err != nil {
		return err
	}
	if pendingAttempts != 0 || input.Status == StatusSucceeded && succeededAttempts == 0 {
		return fmt.Errorf("%w: request attempts do not support terminal status", ErrConflict)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE accounting_requests SET status=?,finished_at=? WHERE id=? AND status='pending'`, string(input.Status), finishedAt, input.ID); err != nil {
		return err
	}
	return nil
}

func (l *Ledger) RecoverInterrupted(ctx context.Context, at time.Time) (RecoveryResult, error) {
	finishedAt, err := canonicalTime(at)
	if err != nil {
		return RecoveryResult{}, err
	}
	tx, err := l.begin(ctx)
	if err != nil {
		return RecoveryResult{}, err
	}
	defer tx.Rollback()
	if err := validateRecoveryTime(ctx, tx, at); err != nil {
		return RecoveryResult{}, err
	}
	attempts, err := tx.ExecContext(ctx, `UPDATE accounting_attempts SET status='interrupted',finished_at=? WHERE status='pending'`, finishedAt)
	if err != nil {
		return RecoveryResult{}, err
	}
	requests, err := tx.ExecContext(ctx, `UPDATE accounting_requests SET status='interrupted',finished_at=? WHERE status='pending'`, finishedAt)
	if err != nil {
		return RecoveryResult{}, err
	}
	result := RecoveryResult{}
	if result.Attempts, err = attempts.RowsAffected(); err != nil {
		return RecoveryResult{}, err
	}
	if result.Requests, err = requests.RowsAffected(); err != nil {
		return RecoveryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RecoveryResult{}, err
	}
	return result, nil
}

func (l *Ledger) SummarizeRequests(ctx context.Context) (RequestSummary, error) {
	if l == nil || l.db == nil {
		return RequestSummary{}, ErrInvalid
	}
	var summary RequestSummary
	err := l.db.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='succeeded' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='cancelled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='interrupted' THEN 1 ELSE 0 END),0)
		FROM accounting_requests`).Scan(&summary.Total, &summary.Pending, &summary.Succeeded, &summary.Failed, &summary.Cancelled, &summary.Interrupted)
	return summary, err
}

func (l *Ledger) SummarizeAttempts(ctx context.Context) ([]AttemptSummary, error) {
	if l == nil || l.db == nil {
		return nil, ErrInvalid
	}
	rows, err := l.db.QueryContext(ctx, `SELECT COALESCE(currency,'`+UnknownCurrency+`'),COUNT(*),
		COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='succeeded' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='cancelled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='interrupted' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(cost_micro),0),
		COALESCE(SUM(CASE WHEN status<>'pending' AND cost_micro IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(input_tokens),0),COALESCE(SUM(CASE WHEN status<>'pending' AND input_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(output_tokens),0),COALESCE(SUM(CASE WHEN status<>'pending' AND output_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(CASE WHEN status<>'pending' AND cache_read_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(cache_write_tokens),0),COALESCE(SUM(CASE WHEN status<>'pending' AND cache_write_tokens IS NULL THEN 1 ELSE 0 END),0)
		FROM accounting_attempts GROUP BY COALESCE(currency,'`+UnknownCurrency+`') ORDER BY COALESCE(currency,'`+UnknownCurrency+`')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := make([]AttemptSummary, 0)
	for rows.Next() {
		var summary AttemptSummary
		if err := rows.Scan(&summary.Currency, &summary.Total, &summary.Pending, &summary.Succeeded, &summary.Failed, &summary.Cancelled, &summary.Interrupted,
			&summary.KnownCostMicro, &summary.UnknownCostAttempts,
			&summary.InputTokens.KnownTotal, &summary.InputTokens.UnknownAttempts,
			&summary.OutputTokens.KnownTotal, &summary.OutputTokens.UnknownAttempts,
			&summary.CacheReadTokens.KnownTotal, &summary.CacheReadTokens.UnknownAttempts,
			&summary.CacheWriteTokens.KnownTotal, &summary.CacheWriteTokens.UnknownAttempts); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func (l *Ledger) begin(ctx context.Context) (*sql.Tx, error) {
	if l == nil || l.db == nil {
		return nil, ErrInvalid
	}
	return l.db.BeginTx(ctx, nil)
}

type ledgerColumn struct {
	name    string
	kind    string
	notNull bool
	primary bool
}

func validateLedgerSchema(ctx context.Context, tx *sql.Tx) error {
	requestColumns := []ledgerColumn{
		{name: "id", kind: "TEXT", primary: true},
		{name: "employee_id", kind: "TEXT", notNull: true},
		{name: "key_id", kind: "TEXT", notNull: true},
		{name: "model_id", kind: "TEXT", notNull: true},
		{name: "provider", kind: "TEXT", notNull: true},
		{name: "started_at", kind: "TEXT", notNull: true},
		{name: "finished_at", kind: "TEXT"},
		{name: "status", kind: "TEXT", notNull: true},
	}
	requestChecks := []string{
		"check(providerin('openai','openai-compatible','anthropic','gemini','codex'))",
		"check(statusin('pending','succeeded','failed','cancelled','interrupted'))",
		"check((status='pending'andfinished_atisnull)or(status<>'pending'andfinished_atisnotnull))",
	}
	if err := validateLedgerTable(ctx, tx, "accounting_requests", requestColumns, requestChecks); err != nil {
		return err
	}
	attemptColumns := []ledgerColumn{
		{name: "id", kind: "TEXT", primary: true},
		{name: "request_id", kind: "TEXT", notNull: true},
		{name: "account_id", kind: "TEXT", notNull: true},
		{name: "provider", kind: "TEXT", notNull: true},
		{name: "dispatch", kind: "TEXT", notNull: true},
		{name: "started_at", kind: "TEXT", notNull: true},
		{name: "finished_at", kind: "TEXT"},
		{name: "status", kind: "TEXT", notNull: true},
		{name: "input_tokens", kind: "INTEGER"},
		{name: "output_tokens", kind: "INTEGER"},
		{name: "cache_read_tokens", kind: "INTEGER"},
		{name: "cache_write_tokens", kind: "INTEGER"},
		{name: "price_version", kind: "TEXT"},
		{name: "currency", kind: "TEXT"},
		{name: "input_rate", kind: "INTEGER"},
		{name: "output_rate", kind: "INTEGER"},
		{name: "cache_read_rate", kind: "INTEGER"},
		{name: "cache_write_rate", kind: "INTEGER"},
		{name: "cost_micro", kind: "INTEGER"},
	}
	attemptChecks := []string{
		"check(providerin('openai','openai-compatible','anthropic','gemini','codex'))",
		"check(dispatchin('primary','retry','failover'))",
		"check(statusin('pending','succeeded','failed','cancelled','interrupted'))",
		"check(input_tokensisnullorinput_tokens>=0)",
		"check(output_tokensisnulloroutput_tokens>=0)",
		"check(cache_read_tokensisnullorcache_read_tokens>=0)",
		"check(cache_write_tokensisnullorcache_write_tokens>=0)",
		"check(currencyisnullor(length(currency)=3andcurrency=upper(currency)))",
		"check(input_rateisnullorinput_rate>=0)",
		"check(output_rateisnulloroutput_rate>=0)",
		"check(cache_read_rateisnullorcache_read_rate>=0)",
		"check(cache_write_rateisnullorcache_write_rate>=0)",
		"check(cost_microisnullorcost_micro>=0)",
		"check((price_versionisnullandcurrencyisnullandinput_rateisnullandoutput_rateisnullandcache_read_rateisnullandcache_write_rateisnull)or(price_versionisnotnullandcurrencyisnotnullandinput_rateisnotnullandoutput_rateisnotnullandcache_read_rateisnotnullandcache_write_rateisnotnull))",
		"check((status='pending'andfinished_atisnullandinput_tokensisnullandoutput_tokensisnullandcache_read_tokensisnullandcache_write_tokensisnullandcost_microisnull)or(status<>'pending'andfinished_atisnotnull))",
		"check((price_versionisnotnullandinput_tokensisnotnullandoutput_tokensisnotnullandcache_read_tokensisnotnullandcache_write_tokensisnotnullandcost_microisnotnull)or((price_versionisnullorinput_tokensisnulloroutput_tokensisnullorcache_read_tokensisnullorcache_write_tokensisnull)andcost_microisnull))",
	}
	if err := validateLedgerTable(ctx, tx, "accounting_attempts", attemptColumns, attemptChecks); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(accounting_attempts)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	foreignKeys := 0
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			return err
		}
		if table != "accounting_requests" || from != "request_id" || to != "id" {
			return errors.New("invalid accounting attempt foreign key")
		}
		foreignKeys++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if foreignKeys != 1 {
		return errors.New("invalid accounting attempt foreign key")
	}
	return nil
}

func validateLedgerTable(ctx context.Context, tx *sql.Tx, table string, expected []ledgerColumn, checks []string) error {
	var objectType, definition string
	if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, table).Scan(&objectType, &definition); err != nil {
		return err
	}
	if objectType != "table" {
		return errors.New("accounting schema object is not a table")
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	columns := make([]ledgerColumn, 0, len(expected))
	for rows.Next() {
		var position, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&position, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		columns = append(columns, ledgerColumn{name: name, kind: strings.ToUpper(kind), notNull: notNull == 1, primary: primary == 1})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(columns) != len(expected) {
		return errors.New("accounting table has unexpected columns")
	}
	for index := range expected {
		if columns[index] != expected[index] {
			return errors.New("accounting table column differs")
		}
	}
	normalized := strings.Map(func(character rune) rune {
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' {
			return -1
		}
		return character
	}, strings.ToLower(definition))
	for _, check := range checks {
		if !strings.Contains(normalized, check) {
			return errors.New("accounting table constraint differs")
		}
	}
	return nil
}

func requestAttemptState(ctx context.Context, tx *sql.Tx, requestID string, requestFinished time.Time) (int64, int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT status,finished_at FROM accounting_attempts WHERE request_id=?`, requestID)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	var pending, succeeded int64
	for rows.Next() {
		var status Status
		var finished sql.NullString
		if err := rows.Scan(&status, &finished); err != nil {
			return 0, 0, err
		}
		if status == StatusPending {
			pending++
			continue
		}
		if status == StatusSucceeded {
			succeeded++
		}
		if !finished.Valid {
			return 0, 0, errors.New("terminal accounting attempt lacks finish time")
		}
		attemptFinished, err := time.Parse(time.RFC3339Nano, finished.String)
		if err != nil {
			return 0, 0, errors.New("invalid stored accounting timestamp")
		}
		if attemptFinished.After(requestFinished.UTC()) {
			return 0, 0, ErrInvalid
		}
	}
	return pending, succeeded, rows.Err()
}

func validateRecoveryTime(ctx context.Context, tx *sql.Tx, at time.Time) error {
	rows, err := tx.QueryContext(ctx, `SELECT started_at FROM accounting_requests WHERE status='pending'
		UNION ALL SELECT started_at FROM accounting_attempts WHERE status='pending'
		UNION ALL SELECT a.finished_at FROM accounting_attempts a
		JOIN accounting_requests r ON r.id=a.request_id
		WHERE r.status='pending' AND a.status<>'pending'`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var started string
		if err := rows.Scan(&started); err != nil {
			return err
		}
		parsed, err := time.Parse(time.RFC3339Nano, started)
		if err != nil {
			return errors.New("invalid stored accounting timestamp")
		}
		if at.UTC().Before(parsed) {
			return ErrInvalid
		}
	}
	return rows.Err()
}

func validateRequestStart(input RequestStart) (string, error) {
	if !validID(input.ID) || !validID(input.EmployeeID) || !validID(input.KeyID) || !validModelID(input.ModelID) || !validProvider(input.Provider) {
		return "", ErrInvalid
	}
	return canonicalTime(input.StartedAt)
}

func validateAttemptStart(input AttemptStart) (string, error) {
	if !validID(input.ID) || !validID(input.RequestID) || !validID(input.AccountID) || !validProvider(input.Provider) || !validDispatch(input.Dispatch) || !validPrice(input.Price) {
		return "", ErrInvalid
	}
	return canonicalTime(input.StartedAt)
}

func validateAttemptFinish(input AttemptFinish) (string, error) {
	if !validID(input.ID) || !terminalStatus(input.Status) || !validUsage(input.Usage) {
		return "", ErrInvalid
	}
	return canonicalTime(input.FinishedAt)
}

func validateRequestFinish(input RequestFinish) (string, error) {
	if !validID(input.ID) || !terminalStatus(input.Status) {
		return "", ErrInvalid
	}
	return canonicalTime(input.FinishedAt)
}

func validID(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' {
			continue
		}
		switch character {
		case '-', '_', '.', ':', '/', '@', '+':
			continue
		default:
			return false
		}
	}
	return true
}

func validModelID(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return true
}

func validProvider(provider Provider) bool {
	switch provider {
	case ProviderOpenAI, ProviderOpenAICompatible, ProviderAnthropic, ProviderGemini, ProviderCodex:
		return true
	default:
		return false
	}
}

func validDispatch(dispatch Dispatch) bool {
	return dispatch == DispatchPrimary || dispatch == DispatchRetry || dispatch == DispatchFailover
}

func terminalStatus(status Status) bool {
	return status == StatusSucceeded || status == StatusFailed || status == StatusCancelled || status == StatusInterrupted
}

func validPrice(price *PriceSnapshot) bool {
	if price == nil {
		return true
	}
	if !validID(price.Version) || len(price.Currency) != 3 || price.Currency != strings.ToUpper(price.Currency) {
		return false
	}
	for _, character := range price.Currency {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return price.InputPerMillionMicro >= 0 && price.OutputPerMillionMicro >= 0 && price.CacheReadPerMillionMicro >= 0 && price.CacheWritePerMillionMicro >= 0
}

func validUsage(usage Usage) bool {
	for _, value := range []*int64{usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens} {
		if value != nil && *value < 0 {
			return false
		}
	}
	return true
}

func canonicalTime(value time.Time) (string, error) {
	if value.IsZero() {
		return "", ErrInvalid
	}
	return value.UTC().Format(time.RFC3339Nano), nil
}

func sameAttemptStart(ctx context.Context, tx *sql.Tx, input AttemptStart, startedAt string) (bool, error) {
	var existing AttemptStart
	var storedStarted string
	var version, currency sql.NullString
	var inputRate, outputRate, cacheReadRate, cacheWriteRate sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT id,request_id,account_id,provider,dispatch,started_at,price_version,currency,input_rate,output_rate,cache_read_rate,cache_write_rate
		FROM accounting_attempts WHERE id=?`, input.ID).Scan(
		&existing.ID, &existing.RequestID, &existing.AccountID, &existing.Provider, &existing.Dispatch, &storedStarted,
		&version, &currency, &inputRate, &outputRate, &cacheReadRate, &cacheWriteRate)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	existing.Price, err = priceFromNulls(version, currency, inputRate, outputRate, cacheReadRate, cacheWriteRate)
	if err != nil {
		return false, err
	}
	return existing.RequestID == input.RequestID && existing.AccountID == input.AccountID && existing.Provider == input.Provider &&
		existing.Dispatch == input.Dispatch && storedStarted == startedAt && samePrice(existing.Price, input.Price), nil
}

type storedAttemptFinish struct {
	status     Status
	startedAt  string
	finishedAt sql.NullString
	usage      Usage
	cost       sql.NullInt64
	price      *PriceSnapshot
}

func loadAttemptFinish(ctx context.Context, tx *sql.Tx, id string) (storedAttemptFinish, error) {
	var row storedAttemptFinish
	var input, output, cacheRead, cacheWrite sql.NullInt64
	var version, currency sql.NullString
	var inputRate, outputRate, cacheReadRate, cacheWriteRate sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT status,started_at,finished_at,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cost_micro,
		price_version,currency,input_rate,output_rate,cache_read_rate,cache_write_rate FROM accounting_attempts WHERE id=?`, id).Scan(
		&row.status, &row.startedAt, &row.finishedAt, &input, &output, &cacheRead, &cacheWrite, &row.cost,
		&version, &currency, &inputRate, &outputRate, &cacheReadRate, &cacheWriteRate)
	if errors.Is(err, sql.ErrNoRows) {
		return storedAttemptFinish{}, ErrNotFound
	}
	if err != nil {
		return storedAttemptFinish{}, err
	}
	row.price, err = priceFromNulls(version, currency, inputRate, outputRate, cacheReadRate, cacheWriteRate)
	if err != nil {
		return storedAttemptFinish{}, err
	}
	row.usage = Usage{InputTokens: pointerFromNull(input), OutputTokens: pointerFromNull(output), CacheReadTokens: pointerFromNull(cacheRead), CacheWriteTokens: pointerFromNull(cacheWrite)}
	return row, nil
}

func calculateCost(usage Usage, price *PriceSnapshot) (*int64, error) {
	if price == nil {
		return nil, nil
	}
	values := []*int64{usage.InputTokens, usage.OutputTokens, usage.CacheReadTokens, usage.CacheWriteTokens}
	for _, value := range values {
		if value == nil {
			return nil, nil
		}
	}
	rates := []int64{price.InputPerMillionMicro, price.OutputPerMillionMicro, price.CacheReadPerMillionMicro, price.CacheWritePerMillionMicro}
	var high, low uint64
	for index, value := range values {
		productHigh, productLow := bits.Mul64(uint64(*value), uint64(rates[index]))
		var carry uint64
		low, carry = bits.Add64(low, productLow, 0)
		var overflow uint64
		high, overflow = bits.Add64(high, productHigh, carry)
		if overflow != 0 {
			return nil, ErrInvalid
		}
	}
	if high == 0 && low == 0 {
		zero := int64(0)
		return &zero, nil
	}
	var carry uint64
	low, carry = bits.Add64(low, 999_999, 0)
	high, carry = bits.Add64(high, 0, carry)
	if carry != 0 || high >= 1_000_000 {
		return nil, ErrInvalid
	}
	quotient, _ := bits.Div64(high, low, 1_000_000)
	if quotient > math.MaxInt64 {
		return nil, ErrInvalid
	}
	cost := int64(quotient)
	return &cost, nil
}

func sameUsage(left, right Usage) bool {
	return samePointer(left.InputTokens, right.InputTokens) && samePointer(left.OutputTokens, right.OutputTokens) &&
		samePointer(left.CacheReadTokens, right.CacheReadTokens) && samePointer(left.CacheWriteTokens, right.CacheWriteTokens)
}

func samePointer(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func sameNullableInt(stored sql.NullInt64, expected *int64) bool {
	return !stored.Valid && expected == nil || stored.Valid && expected != nil && stored.Int64 == *expected
}

func nullableValue(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func pointerFromNull(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func priceValues(price *PriceSnapshot) []any {
	if price == nil {
		return []any{nil, nil, nil, nil, nil, nil}
	}
	return []any{
		price.Version,
		price.Currency,
		price.InputPerMillionMicro,
		price.OutputPerMillionMicro,
		price.CacheReadPerMillionMicro,
		price.CacheWritePerMillionMicro,
	}
}

func priceFromNulls(version, currency sql.NullString, input, output, cacheRead, cacheWrite sql.NullInt64) (*PriceSnapshot, error) {
	known := version.Valid && currency.Valid && input.Valid && output.Valid && cacheRead.Valid && cacheWrite.Valid
	unknown := !version.Valid && !currency.Valid && !input.Valid && !output.Valid && !cacheRead.Valid && !cacheWrite.Valid
	if unknown {
		return nil, nil
	}
	if !known {
		return nil, errors.New("incomplete accounting price snapshot")
	}
	return &PriceSnapshot{
		Version:                   version.String,
		Currency:                  currency.String,
		InputPerMillionMicro:      input.Int64,
		OutputPerMillionMicro:     output.Int64,
		CacheReadPerMillionMicro:  cacheRead.Int64,
		CacheWritePerMillionMicro: cacheWrite.Int64,
	}, nil
}

func samePrice(left, right *PriceSnapshot) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func requireNotBefore(value time.Time, stored string) error {
	parsed, err := time.Parse(time.RFC3339Nano, stored)
	if err != nil {
		return errors.New("invalid stored accounting timestamp")
	}
	if value.UTC().Before(parsed) {
		return ErrInvalid
	}
	return nil
}
