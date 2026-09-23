package accounting

// Independently implemented from docs/account-recovery-execution-plan.md.
// This ledger stores bounded system-probe metadata only. It never creates or
// joins employee requests, keys, prompts, responses, or credentials.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
)

const (
	systemProbeTable      = "system_probe_attempts"
	MaxSystemProbeHistory = 10_000
)

var ErrSystemProbeHistoryFull = errors.New("system probe history full")

type SystemProbeStatus string

const (
	SystemProbePending     SystemProbeStatus = "pending"
	SystemProbeSucceeded   SystemProbeStatus = "succeeded"
	SystemProbeFailed      SystemProbeStatus = "failed"
	SystemProbeCancelled   SystemProbeStatus = "cancelled"
	SystemProbeInterrupted SystemProbeStatus = "interrupted"
)

type SystemProbeResult string

const (
	SystemProbeGenerationOK         SystemProbeResult = "generation_ok"
	SystemProbeAuthenticationFailed SystemProbeResult = "authentication_failed"
	SystemProbeRateLimited          SystemProbeResult = "rate_limited"
	SystemProbeUpstreamUnavailable  SystemProbeResult = "upstream_unavailable"
	SystemProbeUpstreamTimeout      SystemProbeResult = "upstream_timeout"
	SystemProbeProtocolError        SystemProbeResult = "protocol_error"
	SystemProbeUnsupported          SystemProbeResult = "unsupported"
	SystemProbeCancelledResult      SystemProbeResult = "cancelled"
	SystemProbeConfigurationChanged SystemProbeResult = "configuration_changed"
	SystemProbeInterruptedResult    SystemProbeResult = "interrupted"
)

type SystemProbeStart struct {
	OperationID     string
	RecoveryEventID string
	AccountID       string
	AccountRevision int64
	PoolRevision    int64
	PublicModel     string
	UpstreamModel   string
	Provider        Provider
	Protocol        UsageProtocol
	StartedAt       time.Time
}

type SystemProbeFinish struct {
	OperationID string
	Status      SystemProbeStatus
	Result      SystemProbeResult
	FinishedAt  time.Time
	Usage       Usage
}

type SystemProbeAttempt struct {
	SystemProbeStart
	MayHaveSentAt *time.Time
	FinishedAt    *time.Time
	Status        SystemProbeStatus
	Result        *SystemProbeResult
	Usage         Usage
	Price         *PriceSnapshot
	CostMicro     *int64
}

type SystemProbeLedger struct {
	db     *sql.DB
	prices *PriceCatalog
}

func NewSystemProbeLedger(db *sql.DB) *SystemProbeLedger {
	return &SystemProbeLedger{db: db, prices: NewPriceCatalog(db)}
}

const systemProbeDDL = `CREATE TABLE IF NOT EXISTS system_probe_attempts (
	operation_id TEXT NOT NULL PRIMARY KEY,
	recovery_event_id TEXT NOT NULL,
	account_id TEXT NOT NULL REFERENCES upstreams(id) ON DELETE RESTRICT,
	account_revision INTEGER NOT NULL CHECK(account_revision BETWEEN 1 AND 9007199254740991),
	pool_revision INTEGER NOT NULL CHECK(pool_revision BETWEEN 1 AND 9007199254740991),
	public_model TEXT NOT NULL,
	upstream_model TEXT NOT NULL,
	provider TEXT NOT NULL CHECK(provider IN ('openai-compatible','anthropic','gemini','codex')),
	protocol TEXT NOT NULL CHECK(protocol IN ('openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content')),
	started_at TEXT NOT NULL,
	may_have_sent_at TEXT,
	finished_at TEXT,
	status TEXT NOT NULL CHECK(status IN ('pending','succeeded','failed','cancelled','interrupted')),
	result_code TEXT CHECK(result_code IS NULL OR result_code IN ('generation_ok','authentication_failed','rate_limited','upstream_unavailable','upstream_timeout','protocol_error','unsupported','cancelled','configuration_changed','interrupted')),
	input_tokens INTEGER CHECK(input_tokens IS NULL OR input_tokens>=0),
	output_tokens INTEGER CHECK(output_tokens IS NULL OR output_tokens>=0),
	cache_read_tokens INTEGER CHECK(cache_read_tokens IS NULL OR cache_read_tokens>=0),
	cache_write_tokens INTEGER CHECK(cache_write_tokens IS NULL OR cache_write_tokens>=0),
	price_version TEXT,
	currency TEXT CHECK(currency IS NULL OR (length(currency)=3 AND currency=upper(currency) AND currency NOT GLOB '*[^A-Z]*')),
	input_rate INTEGER CHECK(input_rate IS NULL OR input_rate BETWEEN 0 AND 9007199254740991),
	output_rate INTEGER CHECK(output_rate IS NULL OR output_rate BETWEEN 0 AND 9007199254740991),
	cache_read_rate INTEGER CHECK(cache_read_rate IS NULL OR cache_read_rate BETWEEN 0 AND 9007199254740991),
	cache_write_rate INTEGER CHECK(cache_write_rate IS NULL OR cache_write_rate BETWEEN 0 AND 9007199254740991),
	cost_micro INTEGER CHECK(cost_micro IS NULL OR cost_micro>=0),
	CHECK(
		(price_version IS NULL AND currency IS NULL AND input_rate IS NULL AND output_rate IS NULL AND cache_read_rate IS NULL AND cache_write_rate IS NULL)
		OR
		(price_version IS NOT NULL AND currency IS NOT NULL AND input_rate IS NOT NULL AND output_rate IS NOT NULL AND cache_read_rate IS NOT NULL AND cache_write_rate IS NOT NULL)
	),
	CHECK(
		(status='pending' AND finished_at IS NULL AND result_code IS NULL AND input_tokens IS NULL AND output_tokens IS NULL AND cache_read_tokens IS NULL AND cache_write_tokens IS NULL AND cost_micro IS NULL)
		OR
		(status<>'pending' AND finished_at IS NOT NULL AND result_code IS NOT NULL)
	),
	CHECK(
		may_have_sent_at IS NOT NULL
		OR (status<>'succeeded' AND input_tokens IS NULL AND output_tokens IS NULL AND cache_read_tokens IS NULL AND cache_write_tokens IS NULL AND cost_micro IS NULL)
	),
	CHECK(
		(status='succeeded' AND result_code='generation_ok')
		OR (status='cancelled' AND result_code='cancelled')
		OR (status='interrupted' AND result_code='interrupted')
		OR (status='failed' AND result_code NOT IN ('generation_ok','cancelled','interrupted'))
		OR status='pending'
	),
	CHECK(
		(price_version IS NOT NULL AND input_tokens IS NOT NULL AND output_tokens IS NOT NULL AND cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL AND cost_micro IS NOT NULL)
		OR
		((price_version IS NULL OR input_tokens IS NULL OR output_tokens IS NULL OR cache_read_tokens IS NULL OR cache_write_tokens IS NULL) AND cost_micro IS NULL)
	)
)`

func (l *SystemProbeLedger) Migrate(ctx context.Context) error {
	if l == nil || l.db == nil || ctx == nil {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		systemProbeDDL,
		`CREATE INDEX IF NOT EXISTS system_probe_attempts_account_started_idx ON system_probe_attempts(account_id,started_at)`,
		`CREATE INDEX IF NOT EXISTS system_probe_attempts_status_started_idx ON system_probe_attempts(status,started_at)`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate system probe ledger: %w", err)
		}
	}
	if err := validateSystemProbeSchema(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (l *SystemProbeLedger) BeginTx(ctx context.Context, tx *sql.Tx, input SystemProbeStart) (SystemProbeAttempt, error) {
	if l == nil || l.db == nil || l.prices == nil || ctx == nil || tx == nil {
		return SystemProbeAttempt{}, ErrInvalid
	}
	startedAt, err := validateSystemProbeStart(input)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	existing, err := l.GetTx(ctx, tx, input.OperationID)
	if err == nil {
		if !sameSystemProbeStart(existing.SystemProbeStart, input, startedAt) {
			return SystemProbeAttempt{}, fmt.Errorf("%w: system probe start differs", ErrConflict)
		}
		return existing, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return SystemProbeAttempt{}, err
	}
	var historyCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM system_probe_attempts`).Scan(&historyCount); err != nil {
		return SystemProbeAttempt{}, err
	}
	if historyCount >= MaxSystemProbeHistory {
		return SystemProbeAttempt{}, ErrSystemProbeHistoryFull
	}
	price, err := l.prices.CurrentTx(ctx, tx, input.AccountID, input.UpstreamModel)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	arguments := []any{
		input.OperationID, input.RecoveryEventID, input.AccountID, input.AccountRevision, input.PoolRevision,
		input.PublicModel, input.UpstreamModel, string(input.Provider), string(input.Protocol), startedAt,
	}
	arguments = append(arguments, priceValues(price)...)
	result, err := tx.ExecContext(ctx, `INSERT INTO system_probe_attempts(
		operation_id,recovery_event_id,account_id,account_revision,pool_revision,public_model,upstream_model,provider,protocol,started_at,
		status,price_version,currency,input_rate,output_rate,cache_read_rate,cache_write_rate
	) SELECT ?,?,?,?,?,?,?,?,?,?,'pending',?,?,?,?,?,? WHERE (SELECT COUNT(*) FROM system_probe_attempts)<?
	ON CONFLICT(operation_id) DO NOTHING`, append(arguments, MaxSystemProbeHistory)...)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	stored, err := l.GetTx(ctx, tx, input.OperationID)
	if err != nil {
		if inserted == 0 && errors.Is(err, ErrNotFound) {
			return SystemProbeAttempt{}, ErrSystemProbeHistoryFull
		}
		return SystemProbeAttempt{}, err
	}
	if inserted == 0 && !sameSystemProbeStart(stored.SystemProbeStart, input, startedAt) {
		return SystemProbeAttempt{}, fmt.Errorf("%w: system probe start differs", ErrConflict)
	}
	return stored, nil
}

func (l *SystemProbeLedger) MarkMayHaveSentTx(ctx context.Context, tx *sql.Tx, operationID string, at time.Time) (SystemProbeAttempt, error) {
	if l == nil || l.db == nil || ctx == nil || tx == nil || !validPriceOperationID(operationID) {
		return SystemProbeAttempt{}, ErrInvalid
	}
	markedAt, err := canonicalTime(at)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	stored, err := l.GetTx(ctx, tx, operationID)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	if stored.Status != SystemProbePending {
		return SystemProbeAttempt{}, fmt.Errorf("%w: terminal system probe cannot be dispatched", ErrConflict)
	}
	if at.Before(stored.StartedAt) {
		return SystemProbeAttempt{}, ErrInvalid
	}
	if stored.MayHaveSentAt != nil {
		if !stored.MayHaveSentAt.Equal(at.UTC()) {
			return SystemProbeAttempt{}, fmt.Errorf("%w: system probe dispatch differs", ErrConflict)
		}
		return stored, nil
	}
	updated, err := tx.ExecContext(ctx, `UPDATE system_probe_attempts SET may_have_sent_at=? WHERE operation_id=? AND status='pending' AND may_have_sent_at IS NULL`, markedAt, operationID)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	if changed != 1 {
		return SystemProbeAttempt{}, fmt.Errorf("%w: system probe dispatch raced", ErrConflict)
	}
	return l.GetTx(ctx, tx, operationID)
}

func (l *SystemProbeLedger) FinishTx(ctx context.Context, tx *sql.Tx, input SystemProbeFinish) (SystemProbeAttempt, error) {
	if l == nil || l.db == nil || ctx == nil || tx == nil || !validSystemProbeFinish(input) {
		return SystemProbeAttempt{}, ErrInvalid
	}
	finishedAt, err := canonicalTime(input.FinishedAt)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	stored, err := l.GetTx(ctx, tx, input.OperationID)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	if input.FinishedAt.Before(stored.StartedAt) || stored.MayHaveSentAt != nil && input.FinishedAt.Before(*stored.MayHaveSentAt) {
		return SystemProbeAttempt{}, ErrInvalid
	}
	if stored.MayHaveSentAt == nil && (input.Status == SystemProbeSucceeded || !sameUsage(input.Usage, Usage{})) {
		return SystemProbeAttempt{}, ErrInvalid
	}
	cost, err := calculateCost(input.Usage, stored.Price)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	if stored.Status != SystemProbePending {
		if stored.Status != input.Status || stored.Result == nil || *stored.Result != input.Result || stored.FinishedAt == nil || !stored.FinishedAt.Equal(input.FinishedAt.UTC()) || !sameUsage(stored.Usage, input.Usage) || !sameNullableIntValue(stored.CostMicro, cost) {
			return SystemProbeAttempt{}, fmt.Errorf("%w: system probe finish differs", ErrConflict)
		}
		return stored, nil
	}
	updated, err := tx.ExecContext(ctx, `UPDATE system_probe_attempts SET
		status=?,result_code=?,finished_at=?,input_tokens=?,output_tokens=?,cache_read_tokens=?,cache_write_tokens=?,cost_micro=?
		WHERE operation_id=? AND status='pending'`, string(input.Status), string(input.Result), finishedAt,
		nullableValue(input.Usage.InputTokens), nullableValue(input.Usage.OutputTokens), nullableValue(input.Usage.CacheReadTokens), nullableValue(input.Usage.CacheWriteTokens), nullableValue(cost), input.OperationID)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	if changed != 1 {
		return SystemProbeAttempt{}, fmt.Errorf("%w: system probe was already finished", ErrConflict)
	}
	return l.GetTx(ctx, tx, input.OperationID)
}

func (l *SystemProbeLedger) InterruptPendingTx(ctx context.Context, tx *sql.Tx, at time.Time) (int64, error) {
	if l == nil || l.db == nil || ctx == nil || tx == nil {
		return 0, ErrInvalid
	}
	if _, err := canonicalTime(at); err != nil {
		return 0, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT operation_id,started_at,may_have_sent_at FROM system_probe_attempts WHERE status='pending' ORDER BY operation_id`)
	if err != nil {
		return 0, err
	}
	type pendingProbe struct {
		operationID string
		started     time.Time
		sent        *time.Time
	}
	pending := make([]pendingProbe, 0)
	for rows.Next() {
		var item pendingProbe
		var started string
		var sent sql.NullString
		if err := rows.Scan(&item.operationID, &started, &sent); err != nil {
			rows.Close()
			return 0, err
		}
		item.started, err = parseSystemProbeTime(started)
		if err != nil {
			rows.Close()
			return 0, ErrInvalid
		}
		if sent.Valid {
			sentTime, err := parseSystemProbeTime(sent.String)
			if err != nil || sentTime.Before(item.started) {
				rows.Close()
				return 0, ErrInvalid
			}
			item.sent = &sentTime
		}
		pending = append(pending, item)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return 0, iterationErr
	}
	if closeErr != nil {
		return 0, closeErr
	}
	var interrupted int64
	for _, item := range pending {
		finished := at.UTC()
		if item.started.After(finished) {
			finished = item.started
		}
		if item.sent != nil && item.sent.After(finished) {
			finished = *item.sent
		}
		result, err := tx.ExecContext(ctx, `UPDATE system_probe_attempts SET status='interrupted',result_code='interrupted',finished_at=? WHERE operation_id=? AND status='pending'`, finished.Format(time.RFC3339Nano), item.operationID)
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if changed != 1 {
			return 0, fmt.Errorf("%w: pending system probe changed during interruption", ErrConflict)
		}
		interrupted++
	}
	return interrupted, nil
}

func (l *SystemProbeLedger) Get(ctx context.Context, operationID string) (SystemProbeAttempt, error) {
	if l == nil || l.db == nil || ctx == nil || !validPriceOperationID(operationID) {
		return SystemProbeAttempt{}, ErrInvalid
	}
	return loadSystemProbe(ctx, l.db, operationID)
}

func (l *SystemProbeLedger) GetTx(ctx context.Context, tx *sql.Tx, operationID string) (SystemProbeAttempt, error) {
	if l == nil || l.db == nil || ctx == nil || tx == nil || !validPriceOperationID(operationID) {
		return SystemProbeAttempt{}, ErrInvalid
	}
	return loadSystemProbe(ctx, tx, operationID)
}

func (l *SystemProbeLedger) Summarize(ctx context.Context) ([]AttemptSummary, error) {
	if l == nil || l.db == nil || ctx == nil {
		return nil, ErrInvalid
	}
	rows, err := l.db.QueryContext(ctx, `SELECT COALESCE(currency,'`+UnknownCurrency+`'),COUNT(*),
		COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='succeeded' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='failed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='cancelled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='interrupted' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(cost_micro),0),COALESCE(SUM(CASE WHEN status<>'pending' AND cost_micro IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(input_tokens),0),COALESCE(SUM(CASE WHEN status<>'pending' AND input_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(output_tokens),0),COALESCE(SUM(CASE WHEN status<>'pending' AND output_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(cache_read_tokens),0),COALESCE(SUM(CASE WHEN status<>'pending' AND cache_read_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(cache_write_tokens),0),COALESCE(SUM(CASE WHEN status<>'pending' AND cache_write_tokens IS NULL THEN 1 ELSE 0 END),0)
		FROM system_probe_attempts GROUP BY COALESCE(currency,'`+UnknownCurrency+`') ORDER BY COALESCE(currency,'`+UnknownCurrency+`')`)
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

type systemProbeQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadSystemProbe(ctx context.Context, query systemProbeQuery, operationID string) (SystemProbeAttempt, error) {
	var attempt SystemProbeAttempt
	var provider, protocol string
	var started string
	var sent, finished, result sql.NullString
	var input, output, cacheRead, cacheWrite, cost sql.NullInt64
	var version, currency sql.NullString
	var inputRate, outputRate, cacheReadRate, cacheWriteRate sql.NullInt64
	err := query.QueryRowContext(ctx, `SELECT operation_id,recovery_event_id,account_id,account_revision,pool_revision,public_model,upstream_model,provider,protocol,started_at,
		may_have_sent_at,finished_at,status,result_code,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,
		price_version,currency,input_rate,output_rate,cache_read_rate,cache_write_rate,cost_micro
		FROM system_probe_attempts WHERE operation_id=?`, operationID).Scan(
		&attempt.OperationID, &attempt.RecoveryEventID, &attempt.AccountID, &attempt.AccountRevision, &attempt.PoolRevision,
		&attempt.PublicModel, &attempt.UpstreamModel, &provider, &protocol, &started, &sent, &finished, &attempt.Status, &result,
		&input, &output, &cacheRead, &cacheWrite, &version, &currency, &inputRate, &outputRate, &cacheReadRate, &cacheWriteRate, &cost)
	if errors.Is(err, sql.ErrNoRows) {
		return SystemProbeAttempt{}, ErrNotFound
	}
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	attempt.Provider, attempt.Protocol = Provider(provider), UsageProtocol(protocol)
	attempt.StartedAt, err = parseSystemProbeTime(started)
	if err != nil {
		return SystemProbeAttempt{}, errors.New("invalid stored system probe timestamp")
	}
	if sent.Valid {
		parsed, parseErr := parseSystemProbeTime(sent.String)
		if parseErr != nil {
			return SystemProbeAttempt{}, errors.New("invalid stored system probe timestamp")
		}
		attempt.MayHaveSentAt = &parsed
	}
	if finished.Valid {
		parsed, parseErr := parseSystemProbeTime(finished.String)
		if parseErr != nil {
			return SystemProbeAttempt{}, errors.New("invalid stored system probe timestamp")
		}
		attempt.FinishedAt = &parsed
	}
	if result.Valid {
		value := SystemProbeResult(result.String)
		attempt.Result = &value
	}
	attempt.Usage = Usage{InputTokens: pointerFromNull(input), OutputTokens: pointerFromNull(output), CacheReadTokens: pointerFromNull(cacheRead), CacheWriteTokens: pointerFromNull(cacheWrite)}
	attempt.Price, err = priceFromNulls(version, currency, inputRate, outputRate, cacheReadRate, cacheWriteRate)
	if err != nil {
		return SystemProbeAttempt{}, err
	}
	attempt.CostMicro = pointerFromNull(cost)
	return attempt, nil
}

func validateSystemProbeStart(input SystemProbeStart) (string, error) {
	if !validPriceOperationID(input.OperationID) || !validID(input.RecoveryEventID) || !validID(input.AccountID) ||
		input.AccountRevision < 1 || input.AccountRevision > MaxPriceRate || input.PoolRevision < 1 || input.PoolRevision > MaxPriceRate ||
		!validModelID(input.PublicModel) || !validActualModelSave(input.UpstreamModel) || !validSystemProbeProviderProtocol(input.Provider, input.Protocol) {
		return "", ErrInvalid
	}
	return canonicalTime(input.StartedAt)
}

func validSystemProbeFinish(input SystemProbeFinish) bool {
	if !validPriceOperationID(input.OperationID) || !validUsage(input.Usage) || !validSystemProbeResult(input.Result) {
		return false
	}
	switch input.Status {
	case SystemProbeSucceeded:
		return input.Result == SystemProbeGenerationOK
	case SystemProbeCancelled:
		return input.Result == SystemProbeCancelledResult
	case SystemProbeInterrupted:
		return input.Result == SystemProbeInterruptedResult
	case SystemProbeFailed:
		return input.Result != SystemProbeGenerationOK && input.Result != SystemProbeCancelledResult && input.Result != SystemProbeInterruptedResult
	default:
		return false
	}
}

func validSystemProbeResult(result SystemProbeResult) bool {
	switch result {
	case SystemProbeGenerationOK, SystemProbeAuthenticationFailed, SystemProbeRateLimited, SystemProbeUpstreamUnavailable,
		SystemProbeUpstreamTimeout, SystemProbeProtocolError, SystemProbeUnsupported, SystemProbeCancelledResult,
		SystemProbeConfigurationChanged, SystemProbeInterruptedResult:
		return true
	default:
		return false
	}
}

func validSystemProbeProviderProtocol(provider Provider, protocol UsageProtocol) bool {
	switch provider {
	case ProviderOpenAICompatible, ProviderCodex:
		return protocol == ProtocolOpenAIChatCompletions || protocol == ProtocolOpenAIResponses
	case ProviderAnthropic:
		return protocol == ProtocolAnthropicMessages
	case ProviderGemini:
		return protocol == ProtocolGeminiGenerateContent
	default:
		return false
	}
}

func sameSystemProbeStart(stored, input SystemProbeStart, startedAt string) bool {
	return stored.OperationID == input.OperationID && stored.RecoveryEventID == input.RecoveryEventID && stored.AccountID == input.AccountID &&
		stored.AccountRevision == input.AccountRevision && stored.PoolRevision == input.PoolRevision && stored.PublicModel == input.PublicModel &&
		stored.UpstreamModel == input.UpstreamModel && stored.Provider == input.Provider && stored.Protocol == input.Protocol &&
		stored.StartedAt.Format(time.RFC3339Nano) == startedAt
}

func sameNullableIntValue(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func parseSystemProbeTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, errors.New("invalid stored system probe timestamp")
	}
	return parsed, nil
}

func validateSystemProbeSchema(ctx context.Context, tx *sql.Tx) error {
	columns := []ledgerColumn{
		{name: "operation_id", kind: "TEXT", notNull: true, primary: true},
		{name: "recovery_event_id", kind: "TEXT", notNull: true},
		{name: "account_id", kind: "TEXT", notNull: true},
		{name: "account_revision", kind: "INTEGER", notNull: true},
		{name: "pool_revision", kind: "INTEGER", notNull: true},
		{name: "public_model", kind: "TEXT", notNull: true},
		{name: "upstream_model", kind: "TEXT", notNull: true},
		{name: "provider", kind: "TEXT", notNull: true},
		{name: "protocol", kind: "TEXT", notNull: true},
		{name: "started_at", kind: "TEXT", notNull: true},
		{name: "may_have_sent_at", kind: "TEXT"},
		{name: "finished_at", kind: "TEXT"},
		{name: "status", kind: "TEXT", notNull: true},
		{name: "result_code", kind: "TEXT"},
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
	checks := []string{
		"check(account_revisionbetween1and9007199254740991)",
		"check(pool_revisionbetween1and9007199254740991)",
		"check(providerin('openai-compatible','anthropic','gemini','codex'))",
		"check(protocolin('openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content'))",
		"check(statusin('pending','succeeded','failed','cancelled','interrupted'))",
		"check(result_codeisnullorresult_codein('generation_ok','authentication_failed','rate_limited','upstream_unavailable','upstream_timeout','protocol_error','unsupported','cancelled','configuration_changed','interrupted'))",
		"check(input_tokensisnullorinput_tokens>=0)", "check(output_tokensisnulloroutput_tokens>=0)",
		"check(cache_read_tokensisnullorcache_read_tokens>=0)", "check(cache_write_tokensisnullorcache_write_tokens>=0)",
		"check(currencyisnullor(length(currency)=3andcurrency=upper(currency)andcurrencynotglob'*[^a-z]*'))",
		"check(input_rateisnullorinput_ratebetween0and9007199254740991)",
		"check(output_rateisnulloroutput_ratebetween0and9007199254740991)",
		"check(cache_read_rateisnullorcache_read_ratebetween0and9007199254740991)",
		"check(cache_write_rateisnullorcache_write_ratebetween0and9007199254740991)",
		"check(cost_microisnullorcost_micro>=0)",
		"check((price_versionisnullandcurrencyisnullandinput_rateisnullandoutput_rateisnullandcache_read_rateisnullandcache_write_rateisnull)or(price_versionisnotnullandcurrencyisnotnullandinput_rateisnotnullandoutput_rateisnotnullandcache_read_rateisnotnullandcache_write_rateisnotnull))",
		"check((status='pending'andfinished_atisnullandresult_codeisnullandinput_tokensisnullandoutput_tokensisnullandcache_read_tokensisnullandcache_write_tokensisnullandcost_microisnull)or(status<>'pending'andfinished_atisnotnullandresult_codeisnotnull))",
		"check(may_have_sent_atisnotnullor(status<>'succeeded'andinput_tokensisnullandoutput_tokensisnullandcache_read_tokensisnullandcache_write_tokensisnullandcost_microisnull))",
		"check((status='succeeded'andresult_code='generation_ok')or(status='cancelled'andresult_code='cancelled')or(status='interrupted'andresult_code='interrupted')or(status='failed'andresult_codenotin('generation_ok','cancelled','interrupted'))orstatus='pending')",
		"check((price_versionisnotnullandinput_tokensisnotnullandoutput_tokensisnotnullandcache_read_tokensisnotnullandcache_write_tokensisnotnullandcost_microisnotnull)or((price_versionisnullorinput_tokensisnulloroutput_tokensisnullorcache_read_tokensisnullorcache_write_tokensisnull)andcost_microisnull))",
	}
	if err := validateLedgerTable(ctx, tx, systemProbeTable, columns, checks); err != nil {
		return errors.New("invalid system probe schema: " + err.Error())
	}
	var objectType, definition string
	if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, systemProbeTable).Scan(&objectType, &definition); err != nil {
		return err
	}
	expectedDefinition := strings.Replace(systemProbeDDL, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1)
	if objectType != "table" || normalizeSystemProbeSQL(definition) != normalizeSystemProbeSQL(expectedDefinition) {
		return errors.New("invalid system probe table definition")
	}
	if err := validateSystemProbeForeignKey(ctx, tx); err != nil {
		return err
	}
	wantedIndexes := map[string]string{
		"system_probe_attempts_account_started_idx": "CREATE INDEX system_probe_attempts_account_started_idx ON system_probe_attempts(account_id,started_at)",
		"system_probe_attempts_status_started_idx":  "CREATE INDEX system_probe_attempts_status_started_idx ON system_probe_attempts(status,started_at)",
	}
	for name, definition := range wantedIndexes {
		var kind, sqlDefinition string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &sqlDefinition); err != nil {
			return errors.New("missing system probe index")
		}
		if kind != "index" || normalizeSystemProbeSQL(sqlDefinition) != normalizeSystemProbeSQL(definition) {
			return errors.New("invalid system probe index")
		}
	}
	indexRows, err := tx.QueryContext(ctx, `PRAGMA index_list(system_probe_attempts)`)
	if err != nil {
		return err
	}
	seenIndexes := make(map[string]bool, len(wantedIndexes))
	primaryIndexes := 0
	for indexRows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := indexRows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			indexRows.Close()
			return err
		}
		if origin == "pk" {
			if unique != 1 || partial != 0 {
				indexRows.Close()
				return errors.New("invalid system probe primary index")
			}
			primaryIndexes++
			continue
		}
		if _, ok := wantedIndexes[name]; !ok || origin != "c" || unique != 0 || partial != 0 {
			indexRows.Close()
			return errors.New("unexpected system probe index")
		}
		seenIndexes[name] = true
	}
	iterationErr, closeErr := indexRows.Err(), indexRows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if primaryIndexes != 1 || len(seenIndexes) != len(wantedIndexes) {
		return errors.New("missing system probe index")
	}
	return validateStoredSystemProbes(ctx, tx)
}

func normalizeSystemProbeSQL(value string) string {
	characters := []rune(value)
	var normalized strings.Builder
	normalized.Grow(len(value))
	var closingQuote rune
	for index := 0; index < len(characters); index++ {
		character := characters[index]
		if closingQuote != 0 {
			normalized.WriteRune(character)
			if character == closingQuote {
				if index+1 < len(characters) && characters[index+1] == closingQuote {
					normalized.WriteRune(characters[index+1])
					index++
				} else {
					closingQuote = 0
				}
			}
			continue
		}
		switch character {
		case '\'', '"', '`':
			closingQuote = character
			normalized.WriteRune(character)
		case '[':
			closingQuote = ']'
			normalized.WriteRune(character)
		default:
			if !unicode.IsSpace(character) {
				normalized.WriteRune(character)
			}
		}
	}
	return normalized.String()
}

func validateSystemProbeForeignKey(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(system_probe_attempts)`)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return err
		}
		if table != "upstreams" || from != "account_id" || to != "id" || onUpdate != "NO ACTION" || onDelete != "RESTRICT" {
			rows.Close()
			return errors.New("invalid system probe foreign key")
		}
		count++
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if count != 1 {
		return errors.New("invalid system probe foreign key")
	}
	violations, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(system_probe_attempts)`)
	if err != nil {
		return err
	}
	violated := violations.Next()
	iterationErr, closeErr = violations.Err(), violations.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if violated {
		return errors.New("system probe foreign key violation")
	}
	return nil
}

func validateStoredSystemProbes(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT operation_id FROM system_probe_attempts ORDER BY operation_id`)
	if err != nil {
		return err
	}
	operationIDs := make([]string, 0)
	for rows.Next() {
		var operationID string
		if err := rows.Scan(&operationID); err != nil {
			rows.Close()
			return err
		}
		operationIDs = append(operationIDs, operationID)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, operationID := range operationIDs {
		attempt, err := loadSystemProbe(ctx, tx, operationID)
		if err != nil {
			return err
		}
		if _, err := validateSystemProbeStart(attempt.SystemProbeStart); err != nil || !validStoredSystemProbe(attempt) {
			return errors.New("invalid stored system probe metadata")
		}
	}
	return nil
}

func validStoredSystemProbe(attempt SystemProbeAttempt) bool {
	if !validSystemProbePrice(attempt.Price) {
		return false
	}
	if attempt.MayHaveSentAt != nil && attempt.MayHaveSentAt.Before(attempt.StartedAt) {
		return false
	}
	if attempt.Status == SystemProbePending {
		return attempt.FinishedAt == nil && attempt.Result == nil && sameUsage(attempt.Usage, Usage{}) && attempt.CostMicro == nil
	}
	if attempt.FinishedAt == nil || attempt.Result == nil || attempt.FinishedAt.Before(attempt.StartedAt) || attempt.MayHaveSentAt != nil && attempt.FinishedAt.Before(*attempt.MayHaveSentAt) {
		return false
	}
	if attempt.MayHaveSentAt == nil && (attempt.Status == SystemProbeSucceeded || !sameUsage(attempt.Usage, Usage{})) {
		return false
	}
	finish := SystemProbeFinish{OperationID: attempt.OperationID, Status: attempt.Status, Result: *attempt.Result, FinishedAt: *attempt.FinishedAt, Usage: attempt.Usage}
	if !validSystemProbeFinish(finish) {
		return false
	}
	cost, err := calculateCost(attempt.Usage, attempt.Price)
	return err == nil && sameNullableIntValue(attempt.CostMicro, cost)
}

func validSystemProbePrice(price *PriceSnapshot) bool {
	if !validPrice(price) {
		return false
	}
	if price == nil {
		return true
	}
	return price.InputPerMillionMicro <= MaxPriceRate && price.OutputPerMillionMicro <= MaxPriceRate &&
		price.CacheReadPerMillionMicro <= MaxPriceRate && price.CacheWritePerMillionMicro <= MaxPriceRate
}
