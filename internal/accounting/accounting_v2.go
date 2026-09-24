package accounting

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	MaxAccountingV2ExportRows     = 5_000
	MaxAccountingV2ListRows       = 200
	MaxAccountingV2Corrections    = 200
	MaxAccountingV2ReportAttempts = 250_000
)

const accountingV2FinishedKeySQL = `(substr(a.finished_at,1,19)||'.'||substr((CASE WHEN instr(a.finished_at,'.')>0 THEN substr(a.finished_at,instr(a.finished_at,'.')+1,length(a.finished_at)-instr(a.finished_at,'.')-1) ELSE '' END)||'000000000',1,9)||'Z')`

var ErrAccountingV2Limit = errors.New("accounting v2 result limit exceeded")

type UsageEvidence string

const (
	EvidenceProviderResponse UsageEvidence = "provider_response"
	EvidenceProviderStream   UsageEvidence = "provider_stream"
	EvidenceBackgroundResult UsageEvidence = "background_result"
	EvidenceSystemTerminal   UsageEvidence = "system_terminal"
)

type EventRecorder interface {
	BeginRequest(context.Context, RequestStart) error
	BeginAttempt(context.Context, AttemptStart) error
	MarkAttemptDispatched(context.Context, AttemptDispatch) error
	FinishAttempt(context.Context, AttemptFinish) error
	FinishRequest(context.Context, RequestFinish) error
	AppendCorrection(context.Context, Correction) error
}

// TransactionalEventRecorder is the cross-module bridge for background claim,
// permission/route revalidation, attempt creation, budget reservation and the
// durable dispatch intent. The caller owns the transaction and performs no
// network I/O until its single commit is confirmed.
type TransactionalEventRecorder interface {
	EventRecorder
	BeginRequestTx(context.Context, *sql.Tx, RequestStart) error
	BeginAttemptTx(context.Context, *sql.Tx, AttemptStart) error
	MarkAttemptDispatchedTx(context.Context, *sql.Tx, AttemptDispatch) error
	FinishAttemptTx(context.Context, *sql.Tx, AttemptFinish) error
	FinishRequestTx(context.Context, *sql.Tx, RequestFinish) error
	AppendCorrectionTx(context.Context, *sql.Tx, Correction) error
}

type AttemptDispatch struct {
	ID           string
	OperationID  string
	DispatchedAt time.Time
}

type CorrectionReason string

const (
	CorrectionLateProviderUsage CorrectionReason = "late_provider_usage"
	CorrectionAdminReconcile    CorrectionReason = "admin_reconciliation"
)

// CorrectionValue either applies a signed delta or establishes an exact value
// for a previously unknown dimension. Exactly one field may be set.
type CorrectionValue struct {
	Delta *int64
	Set   *int64
}

type Correction struct {
	ID                      string
	AttemptID               string
	TargetEventID           string
	OperationID             string
	Actor                   string
	Reason                  CorrectionReason
	CorrectedAt             time.Time
	Currency                string
	InputTokens             CorrectionValue
	OutputTokens            CorrectionValue
	CacheReadTokens         CorrectionValue
	CacheWriteTokens        CorrectionValue
	ReasoningTokens         CorrectionValue
	EstimatedCostDeltaMicro *int64
}

type UsageEvent struct {
	ID                 string
	AttemptID          string
	RequestID          string
	Protocol           UsageProtocol
	PublicModel        string
	EffectiveModel     string
	AccountID          string
	Dispatch           Dispatch
	Evidence           UsageEvidence
	Status             Status
	StartedAt          time.Time
	DispatchedAt       *time.Time
	FinishedAt         time.Time
	InputTokens        *int64
	OutputTokens       *int64
	CacheReadTokens    *int64
	CacheWriteTokens   *int64
	ReasoningTokens    *int64
	PriceVersion       *string
	Currency           *string
	EstimatedCostMicro *int64
	SourceEventID      *string
	ResponseID         *string
	TaskID             *string
	ToolRunID          *string
}

type AccountingV2Filters struct {
	From       time.Time
	To         time.Time
	EmployeeID string
	KeyID      string
	ModelID    string
	AccountID  string
	Provider   Provider
	Protocol   UsageProtocol
	Status     Status
	Currency   string
}

type AccountingV2Period string

const (
	AccountingV2Day   AccountingV2Period = "day"
	AccountingV2Month AccountingV2Period = "month"
)

type AccountingV2Row struct {
	AttemptID          string
	RequestID          string
	EmployeeID         string
	KeyID              string
	PublicModel        string
	EffectiveModel     *string
	AccountID          string
	Provider           Provider
	Protocol           *UsageProtocol
	Status             Status
	Dispatch           Dispatch
	StartedAt          time.Time
	DispatchedAt       *time.Time
	FinishedAt         time.Time
	EventID            *string
	Evidence           *UsageEvidence
	ResponseID         *string
	TaskID             *string
	ToolRunID          *string
	CorrectionCount    int64
	InputTokens        *int64
	OutputTokens       *int64
	CacheReadTokens    *int64
	CacheWriteTokens   *int64
	ReasoningTokens    *int64
	PriceVersion       *string
	Currency           *string
	EstimatedCostMicro *int64
}

type AccountingV2Report struct {
	PeriodStart             time.Time
	PeriodEnd               time.Time
	Currency                string
	Requests                int64
	Attempts                int64
	Corrections             int64
	MissingEvidenceAttempts int64
	KnownEstimatedCostMicro int64
	UnknownCostAttempts     int64
	InputTokens             KnownValueSummary
	OutputTokens            KnownValueSummary
	CacheReadTokens         KnownValueSummary
	CacheWriteTokens        KnownValueSummary
	ReasoningTokens         KnownValueSummary
}

func (l *Ledger) MigrateV2(ctx context.Context) error {
	if l == nil || l.db == nil || ctx == nil {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := l.MigrateV2Tx(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// MigrateV2Tx is the accounting component's caller-owned migration hook. The
// integration layer owns ordering and the surrounding commit.
func (l *Ledger) MigrateV2Tx(ctx context.Context, tx *sql.Tx) error {
	if l == nil || l.db == nil || ctx == nil || tx == nil {
		return ErrInvalid
	}
	statements := []string{
		`CREATE TABLE IF NOT EXISTS accounting_attempt_contexts (
			attempt_id TEXT PRIMARY KEY REFERENCES accounting_attempts(id),
			protocol TEXT NOT NULL CHECK(protocol IN ('openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content')),
			effective_model TEXT NOT NULL,
			evidence TEXT NOT NULL CHECK(evidence IN ('provider_response','provider_stream','background_result','system_terminal')),
			response_id TEXT,
			task_id TEXT,
			tool_run_id TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS accounting_attempt_dispatches (
			attempt_id TEXT PRIMARY KEY REFERENCES accounting_attempts(id),
			operation_id TEXT NOT NULL UNIQUE,
			dispatched_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS accounting_usage_events (
			id TEXT PRIMARY KEY,
			attempt_id TEXT NOT NULL UNIQUE REFERENCES accounting_attempts(id),
			request_id TEXT NOT NULL REFERENCES accounting_requests(id),
			status TEXT NOT NULL CHECK(status IN ('succeeded','failed','cancelled','interrupted')),
			evidence TEXT NOT NULL CHECK(evidence IN ('provider_response','provider_stream','background_result','system_terminal')),
			finished_at TEXT NOT NULL,
			source_event_id TEXT,
			response_id TEXT,
			task_id TEXT,
			tool_run_id TEXT,
			input_tokens INTEGER CHECK(input_tokens IS NULL OR input_tokens>=0),
			output_tokens INTEGER CHECK(output_tokens IS NULL OR output_tokens>=0),
			cache_read_tokens INTEGER CHECK(cache_read_tokens IS NULL OR cache_read_tokens>=0),
			cache_write_tokens INTEGER CHECK(cache_write_tokens IS NULL OR cache_write_tokens>=0),
			reasoning_tokens INTEGER CHECK(reasoning_tokens IS NULL OR reasoning_tokens>=0),
			price_version TEXT,
			currency TEXT CHECK(currency IS NULL OR (length(currency)=3 AND currency=upper(currency) AND currency NOT GLOB '*[^A-Z]*')),
			estimated_cost_micro INTEGER CHECK(estimated_cost_micro IS NULL OR estimated_cost_micro>=0),
			CHECK(reasoning_tokens IS NULL OR output_tokens IS NULL OR reasoning_tokens<=output_tokens),
			CHECK((price_version IS NULL AND currency IS NULL AND estimated_cost_micro IS NULL) OR (price_version IS NOT NULL AND currency IS NOT NULL)),
			CHECK(estimated_cost_micro IS NULL OR (input_tokens IS NOT NULL AND output_tokens IS NOT NULL AND cache_read_tokens IS NOT NULL AND cache_write_tokens IS NOT NULL))
		)`,
		`CREATE TABLE IF NOT EXISTS accounting_usage_corrections (
			id TEXT PRIMARY KEY,
			attempt_id TEXT NOT NULL REFERENCES accounting_attempts(id),
			target_event_id TEXT NOT NULL REFERENCES accounting_usage_events(id),
			operation_id TEXT NOT NULL UNIQUE,
			sequence INTEGER NOT NULL CHECK(sequence BETWEEN 1 AND 9007199254740991),
			actor TEXT NOT NULL,
			reason TEXT NOT NULL CHECK(reason IN ('late_provider_usage','admin_reconciliation')),
			corrected_at TEXT NOT NULL,
			currency TEXT,
			input_delta INTEGER,
			input_set INTEGER CHECK(input_set IS NULL OR input_set>=0),
			output_delta INTEGER,
			output_set INTEGER CHECK(output_set IS NULL OR output_set>=0),
			cache_read_delta INTEGER,
			cache_read_set INTEGER CHECK(cache_read_set IS NULL OR cache_read_set>=0),
			cache_write_delta INTEGER,
			cache_write_set INTEGER CHECK(cache_write_set IS NULL OR cache_write_set>=0),
			reasoning_delta INTEGER,
			reasoning_set INTEGER CHECK(reasoning_set IS NULL OR reasoning_set>=0),
			estimated_cost_delta_micro INTEGER,
			UNIQUE(attempt_id,sequence),
			CHECK(input_delta IS NULL OR input_set IS NULL),
			CHECK(output_delta IS NULL OR output_set IS NULL),
			CHECK(cache_read_delta IS NULL OR cache_read_set IS NULL),
			CHECK(cache_write_delta IS NULL OR cache_write_set IS NULL),
			CHECK(reasoning_delta IS NULL OR reasoning_set IS NULL)
		)`,
		`CREATE INDEX IF NOT EXISTS accounting_usage_events_finished_idx ON accounting_usage_events(finished_at,id)`,
		`CREATE INDEX IF NOT EXISTS accounting_usage_corrections_attempt_idx ON accounting_usage_corrections(attempt_id,sequence)`,
	}
	for _, table := range []string{"accounting_attempt_contexts", "accounting_attempt_dispatches", "accounting_usage_events", "accounting_usage_corrections"} {
		statements = append(statements,
			`CREATE TRIGGER IF NOT EXISTS `+table+`_no_update BEFORE UPDATE ON `+table+` BEGIN SELECT RAISE(ABORT,'accounting v2 facts are immutable'); END`,
			`CREATE TRIGGER IF NOT EXISTS `+table+`_no_delete BEFORE DELETE ON `+table+` BEGIN SELECT RAISE(ABORT,'accounting v2 facts are immutable'); END`)
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate accounting v2: %w", err)
		}
	}
	return validateAccountingV2Schema(ctx, tx)
}

func hasAttemptContext(input AttemptStart) bool {
	return input.Protocol != "" || input.EffectiveModel != "" || input.Evidence != "" || input.ResponseID != nil || input.TaskID != nil || input.ToolRunID != nil
}

func validAttemptContext(input AttemptStart) bool {
	if !protocolAllowedForProvider(input.Provider, input.Protocol) || !validEffectiveModel(input.EffectiveModel) {
		return false
	}
	switch input.Evidence {
	case EvidenceProviderResponse, EvidenceProviderStream, EvidenceBackgroundResult, EvidenceSystemTerminal:
	default:
		return false
	}
	for _, value := range []*string{input.ResponseID, input.TaskID, input.ToolRunID} {
		if value != nil && !validID(*value) {
			return false
		}
	}
	return true
}

func recordAttemptContextTx(ctx context.Context, tx *sql.Tx, input AttemptStart) error {
	if !hasAttemptContext(input) {
		return nil
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO accounting_attempt_contexts(attempt_id,protocol,effective_model,evidence,response_id,task_id,tool_run_id)
		VALUES(?,?,?,?,?,?,?) ON CONFLICT(attempt_id) DO NOTHING`, input.ID, string(input.Protocol), input.EffectiveModel, string(input.Evidence), nullableString(input.ResponseID), nullableString(input.TaskID), nullableString(input.ToolRunID))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 1 {
		return err
	}
	var protocol UsageProtocol
	var model string
	var evidence UsageEvidence
	var responseID, taskID, toolRunID sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT protocol,effective_model,evidence,response_id,task_id,tool_run_id FROM accounting_attempt_contexts WHERE attempt_id=?`, input.ID).
		Scan(&protocol, &model, &evidence, &responseID, &taskID, &toolRunID); err != nil {
		return err
	}
	if protocol != input.Protocol || model != input.EffectiveModel || evidence != input.Evidence || !sameNullableString(nullStringPointer(responseID), input.ResponseID) || !sameNullableString(nullStringPointer(taskID), input.TaskID) || !sameNullableString(nullStringPointer(toolRunID), input.ToolRunID) {
		return ErrConflict
	}
	return nil
}

func (l *Ledger) MarkAttemptDispatched(ctx context.Context, input AttemptDispatch) error {
	if l == nil || l.db == nil || ctx == nil || !validID(input.ID) || !validID(input.OperationID) {
		return ErrInvalid
	}
	dispatchedAt, err := canonicalTime(input.DispatchedAt)
	if err != nil {
		return err
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := markAttemptDispatchedTx(ctx, tx, input, dispatchedAt); err != nil {
		return err
	}
	return tx.Commit()
}

func (l *Ledger) MarkAttemptDispatchedTx(ctx context.Context, tx *sql.Tx, input AttemptDispatch) error {
	if l == nil || l.db == nil || ctx == nil || tx == nil || !validID(input.ID) || !validID(input.OperationID) {
		return ErrInvalid
	}
	dispatchedAt, err := canonicalTime(input.DispatchedAt)
	if err != nil {
		return err
	}
	return markAttemptDispatchedTx(ctx, tx, input, dispatchedAt)
}

func markAttemptDispatchedTx(ctx context.Context, tx *sql.Tx, input AttemptDispatch, dispatchedAt string) error {
	var startedAt string
	var status Status
	if err := tx.QueryRowContext(ctx, `SELECT a.started_at,a.status FROM accounting_attempts a JOIN accounting_attempt_contexts c ON c.attempt_id=a.id WHERE a.id=?`, input.ID).Scan(&startedAt, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if status != StatusPending {
		return ErrConflict
	}
	started, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil || input.DispatchedAt.Before(started) {
		return ErrInvalid
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO accounting_attempt_dispatches(attempt_id,operation_id,dispatched_at) VALUES(?,?,?) ON CONFLICT(attempt_id) DO NOTHING`, input.ID, input.OperationID, dispatchedAt)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 1 {
		return err
	}
	var operation, at string
	if err := tx.QueryRowContext(ctx, `SELECT operation_id,dispatched_at FROM accounting_attempt_dispatches WHERE attempt_id=?`, input.ID).Scan(&operation, &at); err != nil {
		return err
	}
	if operation != input.OperationID || at != dispatchedAt {
		return ErrConflict
	}
	return nil
}

func recordUsageBaseTx(ctx context.Context, tx *sql.Tx, finish AttemptFinish, cost *int64) error {
	var requestID string
	var protocol UsageProtocol
	var evidence UsageEvidence
	var version, currency, contextResponseID, contextTaskID, contextToolRunID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT a.request_id,c.protocol,c.evidence,a.price_version,a.currency,c.response_id,c.task_id,c.tool_run_id
		FROM accounting_attempts a JOIN accounting_attempt_contexts c ON c.attempt_id=a.id WHERE a.id=?`, finish.ID).
		Scan(&requestID, &protocol, &evidence, &version, &currency, &contextResponseID, &contextTaskID, &contextToolRunID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrInvalid
	}
	if err != nil {
		return err
	}
	var dispatchedText sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT dispatched_at FROM accounting_attempt_dispatches WHERE attempt_id=?`, finish.ID).Scan(&dispatchedText); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if !dispatchedText.Valid && (finish.Status == StatusSucceeded || evidence != EvidenceSystemTerminal) {
		return ErrConflict
	}
	if dispatchedText.Valid {
		dispatchedAt, err := time.Parse(time.RFC3339Nano, dispatchedText.String)
		if err != nil || finish.FinishedAt.Before(dispatchedAt) {
			return ErrInvalid
		}
	}
	responseID, err := finishIdentifier(finish.ResponseID, contextResponseID)
	if err != nil {
		return err
	}
	taskID, err := finishIdentifier(finish.TaskID, contextTaskID)
	if err != nil {
		return err
	}
	toolRunID, err := finishIdentifier(finish.ToolRunID, contextToolRunID)
	if err != nil {
		return err
	}
	id := usageEventID(finish.ID)
	result, err := tx.ExecContext(ctx, `INSERT INTO accounting_usage_events(id,attempt_id,request_id,status,evidence,finished_at,source_event_id,response_id,task_id,tool_run_id,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,price_version,currency,estimated_cost_micro)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(attempt_id) DO NOTHING`, id, finish.ID, requestID, string(finish.Status), string(evidence), finish.FinishedAt.UTC().Format(time.RFC3339Nano), nullableString(finish.SourceEventID), nullableString(responseID), nullableString(taskID), nullableString(toolRunID), nullableValue(finish.Usage.InputTokens), nullableValue(finish.Usage.OutputTokens), nullableValue(finish.Usage.CacheReadTokens), nullableValue(finish.Usage.CacheWriteTokens), nullableValue(finish.ReasoningTokens), nullableNullString(version), nullableNullString(currency), nullableValue(cost))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 1 {
		return err
	}
	stored, err := loadBaseEvent(ctx, tx, finish.ID)
	if err != nil {
		return err
	}
	if stored.ID != id || stored.Status != finish.Status || !stored.FinishedAt.Equal(finish.FinishedAt) || !sameNullableString(stored.SourceEventID, finish.SourceEventID) || !sameNullableString(stored.ResponseID, responseID) || !sameNullableString(stored.TaskID, taskID) || !sameNullableString(stored.ToolRunID, toolRunID) || !sameUsage(Usage{InputTokens: stored.InputTokens, OutputTokens: stored.OutputTokens, CacheReadTokens: stored.CacheReadTokens, CacheWriteTokens: stored.CacheWriteTokens}, finish.Usage) || !samePointer(stored.ReasoningTokens, finish.ReasoningTokens) || !samePointer(stored.EstimatedCostMicro, cost) {
		return ErrConflict
	}
	_ = protocol
	return nil
}

// RecoverV2Tx appends an unknown system-terminal event only for attempts whose
// durable dispatch permission was committed before the process stopped. It
// never creates a replacement attempt and performs no network I/O.
func (l *Ledger) RecoverV2Tx(ctx context.Context, tx *sql.Tx) (int64, error) {
	if l == nil || l.db == nil || ctx == nil || tx == nil {
		return 0, ErrInvalid
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.request_id,a.status,a.finished_at,a.price_version,a.currency,c.response_id,c.task_id,c.tool_run_id
		FROM accounting_attempts a JOIN accounting_attempt_contexts c ON c.attempt_id=a.id
		JOIN accounting_attempt_dispatches d ON d.attempt_id=a.id
		LEFT JOIN accounting_usage_events e ON e.attempt_id=a.id
		WHERE a.status='interrupted' AND a.finished_at IS NOT NULL AND e.id IS NULL ORDER BY a.id`)
	if err != nil {
		return 0, err
	}
	type recoveryRow struct {
		attemptID, requestID, finishedAt string
		status                           Status
		version, currency                sql.NullString
		responseID, taskID, toolRunID    sql.NullString
	}
	items := make([]recoveryRow, 0)
	for rows.Next() {
		var item recoveryRow
		if err := rows.Scan(&item.attemptID, &item.requestID, &item.status, &item.finishedAt, &item.version, &item.currency, &item.responseID, &item.taskID, &item.toolRunID); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	var inserted int64
	for _, item := range items {
		result, err := tx.ExecContext(ctx, `INSERT INTO accounting_usage_events(id,attempt_id,request_id,status,evidence,finished_at,response_id,task_id,tool_run_id,price_version,currency)
			VALUES(?,?,?,?,'system_terminal',?,?,?,?,?,?) ON CONFLICT(attempt_id) DO NOTHING`, usageEventID(item.attemptID), item.attemptID, item.requestID, string(item.status), item.finishedAt, nullableNullString(item.responseID), nullableNullString(item.taskID), nullableNullString(item.toolRunID), nullableNullString(item.version), nullableNullString(item.currency))
		if err != nil {
			return 0, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		inserted += changed
	}
	return inserted, nil
}

func (l *Ledger) AppendCorrection(ctx context.Context, correction Correction) error {
	if l == nil || l.db == nil || ctx == nil || !validCorrection(correction) {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := l.AppendCorrectionTx(ctx, tx, correction); err != nil {
		return err
	}
	return tx.Commit()
}

func (l *Ledger) AppendCorrectionTx(ctx context.Context, tx *sql.Tx, correction Correction) error {
	if l == nil || l.db == nil || ctx == nil || tx == nil || !validCorrection(correction) {
		return ErrInvalid
	}
	at := correction.CorrectedAt.UTC().Format(time.RFC3339Nano)
	if existing, found, err := loadCorrectionIdentity(ctx, tx, correction.ID, correction.OperationID); err != nil {
		return err
	} else if found {
		if sameCorrection(existing, correction, at) {
			return nil
		}
		return ErrConflict
	}
	base, err := loadBaseEvent(ctx, tx, correction.AttemptID)
	if err != nil {
		return err
	}
	if base.ID != correction.TargetEventID || !correction.CorrectedAt.After(base.FinishedAt) {
		return ErrConflict
	}
	var correctionCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounting_usage_corrections WHERE attempt_id=?`, correction.AttemptID).Scan(&correctionCount); err != nil {
		return err
	}
	if correctionCount >= MaxAccountingV2Corrections {
		return ErrAccountingV2Limit
	}
	if correctionCount != 0 {
		var lastCorrected string
		if err := tx.QueryRowContext(ctx, `SELECT corrected_at FROM accounting_usage_corrections WHERE attempt_id=? ORDER BY sequence DESC LIMIT 1`, correction.AttemptID).Scan(&lastCorrected); err != nil {
			return err
		}
		last, err := time.Parse(time.RFC3339Nano, lastCorrected)
		if err != nil {
			return ErrInvalid
		}
		if !correction.CorrectedAt.After(last) {
			return ErrConflict
		}
	}
	current, price, err := effectiveUsage(ctx, tx, base)
	if err != nil {
		return err
	}
	updated, err := applyCorrection(current, correction)
	if err != nil {
		return err
	}
	oldCost, err := calculateCost(current.usage(), price)
	if err != nil {
		return err
	}
	newCost, err := calculateCost(updated.usage(), price)
	if err != nil {
		return err
	}
	var derivedDelta *int64
	if oldCost != nil && newCost != nil {
		value, ok := checkedSignedSub(*newCost, *oldCost)
		if !ok {
			return ErrInvalid
		}
		derivedDelta = &value
	}
	if !samePointer(derivedDelta, correction.EstimatedCostDeltaMicro) {
		return ErrInvalid
	}
	expectedCurrency := ""
	if price != nil {
		expectedCurrency = price.Currency
	}
	if correction.Currency != expectedCurrency {
		return ErrInvalid
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO accounting_usage_corrections(id,attempt_id,target_event_id,operation_id,sequence,actor,reason,corrected_at,currency,
		input_delta,input_set,output_delta,output_set,cache_read_delta,cache_read_set,cache_write_delta,cache_write_set,reasoning_delta,reasoning_set,estimated_cost_delta_micro)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, correction.ID, correction.AttemptID, correction.TargetEventID, correction.OperationID, correctionCount+1, correction.Actor, string(correction.Reason), at, nullableCurrency(correction.Currency),
		nullableValue(correction.InputTokens.Delta), nullableValue(correction.InputTokens.Set), nullableValue(correction.OutputTokens.Delta), nullableValue(correction.OutputTokens.Set), nullableValue(correction.CacheReadTokens.Delta), nullableValue(correction.CacheReadTokens.Set), nullableValue(correction.CacheWriteTokens.Delta), nullableValue(correction.CacheWriteTokens.Set), nullableValue(correction.ReasoningTokens.Delta), nullableValue(correction.ReasoningTokens.Set), nullableValue(correction.EstimatedCostDeltaMicro))
	return err
}

func (l *Ledger) AccountingV2Export(ctx context.Context, filters AccountingV2Filters, limit int) ([]AccountingV2Row, error) {
	items, _, _, err := l.accountingV2ExportPage(ctx, filters, limit, "", "")
	return items, err
}

func (l *Ledger) accountingV2ExportPage(ctx context.Context, filters AccountingV2Filters, limit int, afterFinishedKey, afterAttemptID string) ([]AccountingV2Row, string, string, error) {
	if l == nil || l.db == nil || ctx == nil || !validV2Filters(filters) || limit < 1 || limit > MaxAccountingV2ExportRows {
		return nil, "", "", ErrInvalid
	}
	where, args := accountingV2Where(filters)
	if afterFinishedKey != "" {
		if len(afterFinishedKey) != len("2006-01-02T15:04:05.000000000Z") || !validID(afterAttemptID) {
			return nil, "", "", ErrInvalid
		}
		where += ` AND (` + accountingV2FinishedKeySQL + `>? OR (` + accountingV2FinishedKeySQL + `=? AND a.id>?))`
		args = append(args, afterFinishedKey, afterFinishedKey, afterAttemptID)
	}
	args = append(args, limit)
	rows, err := l.db.QueryContext(ctx, `SELECT a.id,a.request_id,r.employee_id,r.key_id,r.model_id,a.account_id,a.provider,a.status,a.dispatch,a.started_at,d.dispatched_at,a.finished_at,
		c.protocol,c.effective_model,e.id,e.evidence,e.response_id,e.task_id,e.tool_run_id,e.input_tokens,e.output_tokens,e.cache_read_tokens,e.cache_write_tokens,e.reasoning_tokens,e.price_version,e.currency,e.estimated_cost_micro,
		a.input_rate,a.output_rate,a.cache_read_rate,a.cache_write_rate
		FROM accounting_attempts a JOIN accounting_requests r ON r.id=a.request_id
		LEFT JOIN accounting_attempt_contexts c ON c.attempt_id=a.id LEFT JOIN accounting_attempt_dispatches d ON d.attempt_id=a.id LEFT JOIN accounting_usage_events e ON e.attempt_id=a.id
		WHERE a.status<>'pending' AND `+where+` ORDER BY `+accountingV2FinishedKeySQL+`,a.id LIMIT ?`, args...)
	if err != nil {
		return nil, "", "", err
	}
	type rawAccountingRow struct {
		item  AccountingV2Row
		base  effectiveCounters
		price *PriceSnapshot
	}
	rawItems := make([]rawAccountingRow, 0)
	for rows.Next() {
		var raw rawAccountingRow
		item := &raw.item
		var started, finished string
		var dispatched sql.NullString
		var protocol, effectiveModel, eventID, evidence, responseID, taskID, toolRunID, priceVersion, currency sql.NullString
		var input, output, cacheRead, cacheWrite, reasoning, cost, inputRate, outputRate, cacheReadRate, cacheWriteRate sql.NullInt64
		if err := rows.Scan(&item.AttemptID, &item.RequestID, &item.EmployeeID, &item.KeyID, &item.PublicModel, &item.AccountID, &item.Provider, &item.Status, &item.Dispatch, &started, &dispatched, &finished,
			&protocol, &effectiveModel, &eventID, &evidence, &responseID, &taskID, &toolRunID, &input, &output, &cacheRead, &cacheWrite, &reasoning, &priceVersion, &currency, &cost, &inputRate, &outputRate, &cacheReadRate, &cacheWriteRate); err != nil {
			rows.Close()
			return nil, "", "", err
		}
		item.StartedAt, err = time.Parse(time.RFC3339Nano, started)
		if err != nil {
			rows.Close()
			return nil, "", "", ErrInvalid
		}
		if dispatched.Valid {
			value, parseErr := time.Parse(time.RFC3339Nano, dispatched.String)
			if parseErr != nil {
				rows.Close()
				return nil, "", "", ErrInvalid
			}
			item.DispatchedAt = &value
		}
		item.FinishedAt, err = time.Parse(time.RFC3339Nano, finished)
		if err != nil {
			rows.Close()
			return nil, "", "", ErrInvalid
		}
		item.EffectiveModel = nullStringPointer(effectiveModel)
		item.EventID = nullStringPointer(eventID)
		item.ResponseID, item.TaskID, item.ToolRunID = nullStringPointer(responseID), nullStringPointer(taskID), nullStringPointer(toolRunID)
		item.PriceVersion, item.Currency = nullStringPointer(priceVersion), nullStringPointer(currency)
		if protocol.Valid {
			value := UsageProtocol(protocol.String)
			item.Protocol = &value
		}
		if evidence.Valid {
			value := UsageEvidence(evidence.String)
			item.Evidence = &value
		}
		raw.base = effectiveCounters{pointerFromNull(input), pointerFromNull(output), pointerFromNull(cacheRead), pointerFromNull(cacheWrite), pointerFromNull(reasoning)}
		if priceVersion.Valid {
			if !currency.Valid || !inputRate.Valid || !outputRate.Valid || !cacheReadRate.Valid || !cacheWriteRate.Valid {
				rows.Close()
				return nil, "", "", ErrInvalid
			}
			raw.price = &PriceSnapshot{Version: priceVersion.String, Currency: currency.String, InputPerMillionMicro: inputRate.Int64, OutputPerMillionMicro: outputRate.Int64, CacheReadPerMillionMicro: cacheReadRate.Int64, CacheWritePerMillionMicro: cacheWriteRate.Int64}
		}
		rawItems = append(rawItems, raw)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, "", "", err
	}
	if err := rows.Close(); err != nil {
		return nil, "", "", err
	}

	items := make([]AccountingV2Row, 0, len(rawItems))
	const correctionBatchSize = 50
	for offset := 0; offset < len(rawItems); offset += correctionBatchSize {
		end := offset + correctionBatchSize
		if end > len(rawItems) {
			end = len(rawItems)
		}
		attemptIDs := make([]string, 0, end-offset)
		for _, raw := range rawItems[offset:end] {
			attemptIDs = append(attemptIDs, raw.item.AttemptID)
		}
		corrections, err := l.loadExportCorrections(ctx, attemptIDs)
		if err != nil {
			return nil, "", "", err
		}
		for _, raw := range rawItems[offset:end] {
			if raw.item.EventID != nil {
				current := raw.base
				for _, correction := range corrections[raw.item.AttemptID] {
					current, err = applyCorrection(current, correction)
					if err != nil {
						return nil, "", "", err
					}
				}
				raw.item.CorrectionCount = int64(len(corrections[raw.item.AttemptID]))
				raw.item.InputTokens, raw.item.OutputTokens = current.input, current.output
				raw.item.CacheReadTokens, raw.item.CacheWriteTokens, raw.item.ReasoningTokens = current.cacheRead, current.cacheWrite, current.reasoning
				raw.item.EstimatedCostMicro, err = calculateCost(current.usage(), raw.price)
				if err != nil {
					return nil, "", "", err
				}
			}
			items = append(items, raw.item)
		}
	}
	if len(items) == 0 {
		return items, "", "", nil
	}
	last := items[len(items)-1]
	return items, accountingV2TimeKey(last.FinishedAt), last.AttemptID, nil
}

func (l *Ledger) loadExportCorrections(ctx context.Context, attemptIDs []string) (map[string][]Correction, error) {
	result := make(map[string][]Correction)
	count := 0
	maximum := len(attemptIDs) * MaxAccountingV2Corrections
	const batchSize = 500
	for offset := 0; offset < len(attemptIDs); offset += batchSize {
		end := offset + batchSize
		if end > len(attemptIDs) {
			end = len(attemptIDs)
		}
		arguments := make([]any, 0, end-offset+1)
		placeholders := make([]string, 0, end-offset)
		for _, attemptID := range attemptIDs[offset:end] {
			arguments = append(arguments, attemptID)
			placeholders = append(placeholders, "?")
		}
		arguments = append(arguments, maximum-count+1)
		rows, err := l.db.QueryContext(ctx, `SELECT attempt_id,input_delta,input_set,output_delta,output_set,cache_read_delta,cache_read_set,cache_write_delta,cache_write_set,reasoning_delta,reasoning_set
			FROM accounting_usage_corrections WHERE attempt_id IN (`+strings.Join(placeholders, ",")+`) ORDER BY attempt_id,sequence LIMIT ?`, arguments...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			count++
			if count > maximum {
				rows.Close()
				return nil, ErrAccountingV2Limit
			}
			var attemptID string
			var values [10]sql.NullInt64
			scan := []any{&attemptID}
			for i := range values {
				scan = append(scan, &values[i])
			}
			if err := rows.Scan(scan...); err != nil {
				rows.Close()
				return nil, err
			}
			correction := Correction{}
			fields := []*CorrectionValue{&correction.InputTokens, &correction.OutputTokens, &correction.CacheReadTokens, &correction.CacheWriteTokens, &correction.ReasoningTokens}
			for i, field := range fields {
				field.Delta, field.Set = pointerFromNull(values[i*2]), pointerFromNull(values[i*2+1])
			}
			result[attemptID] = append(result[attemptID], correction)
			if len(result[attemptID]) > MaxAccountingV2Corrections {
				rows.Close()
				return nil, ErrAccountingV2Limit
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (l *Ledger) AccountingV2Report(ctx context.Context, filters AccountingV2Filters, period AccountingV2Period) ([]AccountingV2Report, error) {
	if period != AccountingV2Day && period != AccountingV2Month {
		return nil, ErrInvalid
	}
	type reportKey struct {
		at       time.Time
		currency string
	}
	groups := make(map[reportKey]*AccountingV2Report)
	requests := make(map[reportKey]map[string]struct{})
	const pageSize = 1_000
	var afterKey, afterID string
	processed := 0
	for {
		items, nextKey, nextID, err := l.accountingV2ExportPage(ctx, filters, pageSize, afterKey, afterID)
		if err != nil {
			return nil, err
		}
		processed += len(items)
		if processed > MaxAccountingV2ReportAttempts {
			return nil, ErrAccountingV2Limit
		}
		for _, item := range items {
			start, end := periodBounds(item.FinishedAt, period)
			currency := UnknownCurrency
			if item.Currency != nil {
				currency = *item.Currency
			}
			key := reportKey{start, currency}
			group := groups[key]
			if group == nil {
				group = &AccountingV2Report{PeriodStart: start, PeriodEnd: end, Currency: currency}
				groups[key], requests[key] = group, make(map[string]struct{})
			}
			requests[key][item.RequestID] = struct{}{}
			group.Attempts++
			group.Corrections, err = checkedReportAdd(group.Corrections, item.CorrectionCount)
			if err != nil {
				return nil, err
			}
			if item.EventID == nil {
				group.MissingEvidenceAttempts++
			}
			if item.EstimatedCostMicro == nil {
				group.UnknownCostAttempts++
			} else if group.KnownEstimatedCostMicro, err = checkedReportAdd(group.KnownEstimatedCostMicro, *item.EstimatedCostMicro); err != nil {
				return nil, err
			}
			for _, metric := range []struct {
				value  *int64
				target *KnownValueSummary
			}{
				{item.InputTokens, &group.InputTokens}, {item.OutputTokens, &group.OutputTokens}, {item.CacheReadTokens, &group.CacheReadTokens}, {item.CacheWriteTokens, &group.CacheWriteTokens}, {item.ReasoningTokens, &group.ReasoningTokens},
			} {
				if metric.value == nil {
					metric.target.UnknownAttempts++
				} else if metric.target.KnownTotal, err = checkedReportAdd(metric.target.KnownTotal, *metric.value); err != nil {
					return nil, err
				}
			}
		}
		if len(items) < pageSize {
			break
		}
		afterKey, afterID = nextKey, nextID
	}
	result := make([]AccountingV2Report, 0, len(groups))
	for key, group := range groups {
		group.Requests = int64(len(requests[key]))
		result = append(result, *group)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PeriodStart.Equal(result[j].PeriodStart) {
			return result[i].Currency < result[j].Currency
		}
		return result[i].PeriodStart.Before(result[j].PeriodStart)
	})
	return result, nil
}

type effectiveCounters struct{ input, output, cacheRead, cacheWrite, reasoning *int64 }

func (e effectiveCounters) usage() Usage {
	return Usage{InputTokens: e.input, OutputTokens: e.output, CacheReadTokens: e.cacheRead, CacheWriteTokens: e.cacheWrite}
}

type baseEventScanner interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type correctionQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadBaseEvent(ctx context.Context, query baseEventScanner, attemptID string) (UsageEvent, error) {
	var event UsageEvent
	var finished string
	var source, version, currency sql.NullString
	var input, output, cacheRead, cacheWrite, reasoning, cost sql.NullInt64
	var responseID, taskID, toolRunID sql.NullString
	err := query.QueryRowContext(ctx, `SELECT id,attempt_id,request_id,status,evidence,finished_at,source_event_id,response_id,task_id,tool_run_id,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,price_version,currency,estimated_cost_micro FROM accounting_usage_events WHERE attempt_id=?`, attemptID).
		Scan(&event.ID, &event.AttemptID, &event.RequestID, &event.Status, &event.Evidence, &finished, &source, &responseID, &taskID, &toolRunID, &input, &output, &cacheRead, &cacheWrite, &reasoning, &version, &currency, &cost)
	if errors.Is(err, sql.ErrNoRows) {
		return UsageEvent{}, ErrNotFound
	}
	if err != nil {
		return UsageEvent{}, err
	}
	event.FinishedAt, err = time.Parse(time.RFC3339Nano, finished)
	if err != nil {
		return UsageEvent{}, ErrInvalid
	}
	event.SourceEventID, event.PriceVersion, event.Currency = nullStringPointer(source), nullStringPointer(version), nullStringPointer(currency)
	event.ResponseID, event.TaskID, event.ToolRunID = nullStringPointer(responseID), nullStringPointer(taskID), nullStringPointer(toolRunID)
	event.InputTokens, event.OutputTokens, event.CacheReadTokens, event.CacheWriteTokens, event.ReasoningTokens, event.EstimatedCostMicro = pointerFromNull(input), pointerFromNull(output), pointerFromNull(cacheRead), pointerFromNull(cacheWrite), pointerFromNull(reasoning), pointerFromNull(cost)
	return event, nil
}

func effectiveUsage(ctx context.Context, query correctionQuery, base UsageEvent) (effectiveCounters, *PriceSnapshot, error) {
	current := effectiveCounters{cloneInt64(base.InputTokens), cloneInt64(base.OutputTokens), cloneInt64(base.CacheReadTokens), cloneInt64(base.CacheWriteTokens), cloneInt64(base.ReasoningTokens)}
	rows, err := query.QueryContext(ctx, `SELECT input_delta,input_set,output_delta,output_set,cache_read_delta,cache_read_set,cache_write_delta,cache_write_set,reasoning_delta,reasoning_set FROM accounting_usage_corrections WHERE attempt_id=? ORDER BY sequence`, base.AttemptID)
	if err != nil {
		return effectiveCounters{}, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var values [10]sql.NullInt64
		args := make([]any, len(values))
		for i := range values {
			args[i] = &values[i]
		}
		if err := rows.Scan(args...); err != nil {
			return effectiveCounters{}, nil, err
		}
		fields := []**int64{&current.input, &current.output, &current.cacheRead, &current.cacheWrite, &current.reasoning}
		for i, field := range fields {
			if err := applyStoredCorrection(field, values[i*2], values[i*2+1]); err != nil {
				return effectiveCounters{}, nil, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return effectiveCounters{}, nil, err
	}
	if current.reasoning != nil && current.output != nil && *current.reasoning > *current.output {
		return effectiveCounters{}, nil, ErrInvalid
	}
	var price *PriceSnapshot
	if base.PriceVersion != nil {
		var currency string
		var in, out, read, write int64
		if err := query.(interface {
			QueryRowContext(context.Context, string, ...any) *sql.Row
		}).QueryRowContext(ctx, `SELECT currency,input_rate,output_rate,cache_read_rate,cache_write_rate FROM accounting_attempts WHERE id=?`, base.AttemptID).Scan(&currency, &in, &out, &read, &write); err != nil {
			return effectiveCounters{}, nil, err
		}
		price = &PriceSnapshot{Version: *base.PriceVersion, Currency: currency, InputPerMillionMicro: in, OutputPerMillionMicro: out, CacheReadPerMillionMicro: read, CacheWritePerMillionMicro: write}
	}
	return current, price, nil
}

func applyCorrection(current effectiveCounters, correction Correction) (effectiveCounters, error) {
	result := effectiveCounters{cloneInt64(current.input), cloneInt64(current.output), cloneInt64(current.cacheRead), cloneInt64(current.cacheWrite), cloneInt64(current.reasoning)}
	for _, item := range []struct {
		target **int64
		value  CorrectionValue
	}{{&result.input, correction.InputTokens}, {&result.output, correction.OutputTokens}, {&result.cacheRead, correction.CacheReadTokens}, {&result.cacheWrite, correction.CacheWriteTokens}, {&result.reasoning, correction.ReasoningTokens}} {
		if item.value.Set != nil {
			if *item.target != nil {
				return effectiveCounters{}, ErrInvalid
			}
			value := *item.value.Set
			*item.target = &value
			continue
		}
		if item.value.Delta != nil {
			if *item.target == nil {
				return effectiveCounters{}, ErrInvalid
			}
			value, ok := checkedSignedAdd(**item.target, *item.value.Delta)
			if !ok || value < 0 {
				return effectiveCounters{}, ErrInvalid
			}
			*item.target = &value
		}
	}
	if result.reasoning != nil && result.output != nil && *result.reasoning > *result.output {
		return effectiveCounters{}, ErrInvalid
	}
	return result, nil
}

func applyStoredCorrection(target **int64, delta, set sql.NullInt64) error {
	if delta.Valid && set.Valid {
		return ErrInvalid
	}
	if set.Valid {
		if *target != nil {
			return ErrInvalid
		}
		value := set.Int64
		*target = &value
		return nil
	}
	if delta.Valid {
		if *target == nil {
			return ErrInvalid
		}
		value, ok := checkedSignedAdd(**target, delta.Int64)
		if !ok || value < 0 {
			return ErrInvalid
		}
		*target = &value
	}
	return nil
}

func validCorrection(c Correction) bool {
	if !validID(c.ID) || !validID(c.AttemptID) || !validID(c.TargetEventID) || !validID(c.OperationID) || !validID(c.Actor) || c.CorrectedAt.IsZero() || c.CorrectedAt.Location() != time.UTC {
		return false
	}
	if c.Reason != CorrectionLateProviderUsage && c.Reason != CorrectionAdminReconcile {
		return false
	}
	changed := c.EstimatedCostDeltaMicro != nil && *c.EstimatedCostDeltaMicro != 0
	for _, value := range []CorrectionValue{c.InputTokens, c.OutputTokens, c.CacheReadTokens, c.CacheWriteTokens, c.ReasoningTokens} {
		if value.Delta != nil && value.Set != nil || value.Set != nil && *value.Set < 0 {
			return false
		}
		if value.Set != nil || value.Delta != nil && *value.Delta != 0 {
			changed = true
		}
	}
	return changed
}

func protocolAllowedForProvider(provider Provider, protocol UsageProtocol) bool {
	switch provider {
	case ProviderOpenAI, ProviderOpenAICompatible, ProviderCodex:
		return protocol == ProtocolOpenAIChatCompletions || protocol == ProtocolOpenAIResponses
	case ProviderAnthropic:
		return protocol == ProtocolAnthropicMessages
	case ProviderGemini:
		return protocol == ProtocolGeminiGenerateContent
	default:
		return false
	}
}

func validEffectiveModel(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsRune(value, '\x00') && strings.TrimSpace(value) == value
}
func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullableNullString(value sql.NullString) any {
	if !value.Valid {
		return nil
	}
	return value.String
}
func nullableCurrency(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func finishIdentifier(final *string, started sql.NullString) (*string, error) {
	prior := nullStringPointer(started)
	if final == nil {
		return prior, nil
	}
	if prior != nil && *prior != *final {
		return nil, ErrConflict
	}
	value := *final
	return &value, nil
}
func nullStringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}
func sameNullableString(left, right *string) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func checkedSignedAdd(left, right int64) (int64, bool) {
	if right > 0 && left > math.MaxInt64-right || right < 0 && left < math.MinInt64-right {
		return 0, false
	}
	return left + right, true
}
func checkedSignedSub(left, right int64) (int64, bool) {
	if right == math.MinInt64 {
		if left >= 0 {
			return 0, false
		}
		return left - right, true
	}
	return checkedSignedAdd(left, -right)
}

func usageEventID(attemptID string) string {
	digest := sha256.Sum256([]byte(attemptID))
	return "usage_" + hex.EncodeToString(digest[:16])
}
func checkedReportAdd(left, right int64) (int64, error) {
	value, ok := checkedSignedAdd(left, right)
	if !ok || value < 0 {
		return 0, ErrInvalid
	}
	return value, nil
}

func validV2Filters(f AccountingV2Filters) bool {
	if f.From.IsZero() || f.To.IsZero() || f.From.Location() != time.UTC || f.To.Location() != time.UTC || !f.From.Before(f.To) || f.To.Sub(f.From) > 366*24*time.Hour {
		return false
	}
	for _, v := range []string{f.EmployeeID, f.KeyID, f.ModelID, f.AccountID} {
		if v != "" && !validID(v) {
			return false
		}
	}
	if f.Provider != "" && !validProvider(f.Provider) || f.Protocol != "" && !protocolAllowedForProvider(ProviderOpenAICompatible, f.Protocol) && f.Protocol != ProtocolAnthropicMessages && f.Protocol != ProtocolGeminiGenerateContent || f.Status != "" && !terminalStatus(f.Status) {
		return false
	}
	if f.Currency != "" && f.Currency != UnknownCurrency && !validPrice(&PriceSnapshot{Version: "v", Currency: f.Currency}) {
		return false
	}
	return true
}

func accountingV2Where(f AccountingV2Filters) (string, []any) {
	parts := []string{accountingV2FinishedKeySQL + ">=?", accountingV2FinishedKeySQL + "<?"}
	args := []any{accountingV2TimeKey(f.From), accountingV2TimeKey(f.To)}
	for _, x := range []struct{ c, v string }{{"r.employee_id", f.EmployeeID}, {"r.key_id", f.KeyID}, {"r.model_id", f.ModelID}, {"a.account_id", f.AccountID}, {"a.provider", string(f.Provider)}, {"a.status", string(f.Status)}, {"c.protocol", string(f.Protocol)}} {
		if x.v != "" {
			parts = append(parts, x.c+"=?")
			args = append(args, x.v)
		}
	}
	if f.Currency == UnknownCurrency {
		parts = append(parts, "e.currency IS NULL")
	} else if f.Currency != "" {
		parts = append(parts, "e.currency=?")
		args = append(args, f.Currency)
	}
	return strings.Join(parts, " AND "), args
}

func accountingV2TimeKey(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
func periodBounds(at time.Time, period AccountingV2Period) (time.Time, time.Time) {
	at = at.UTC()
	if period == AccountingV2Month {
		start := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(0, 1, 0)
	}
	start := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(0, 0, 1)
}

type storedCorrection struct {
	Correction
	correctedText string
}

func loadCorrectionIdentity(ctx context.Context, q baseEventScanner, id, operation string) (storedCorrection, bool, error) {
	var s storedCorrection
	var currency sql.NullString
	var values [11]sql.NullInt64
	args := []any{&s.ID, &s.AttemptID, &s.TargetEventID, &s.OperationID, &s.Actor, &s.Reason, &s.correctedText, &currency}
	for i := range values {
		args = append(args, &values[i])
	}
	err := q.QueryRowContext(ctx, `SELECT id,attempt_id,target_event_id,operation_id,actor,reason,corrected_at,currency,input_delta,input_set,output_delta,output_set,cache_read_delta,cache_read_set,cache_write_delta,cache_write_set,reasoning_delta,reasoning_set,estimated_cost_delta_micro FROM accounting_usage_corrections WHERE id=? OR operation_id=? ORDER BY CASE WHEN id=? THEN 0 ELSE 1 END LIMIT 1`, id, operation, id).Scan(args...)
	if errors.Is(err, sql.ErrNoRows) {
		return storedCorrection{}, false, nil
	}
	if err != nil {
		return storedCorrection{}, false, err
	}
	if currency.Valid {
		s.Currency = currency.String
	}
	pairs := []*CorrectionValue{&s.InputTokens, &s.OutputTokens, &s.CacheReadTokens, &s.CacheWriteTokens, &s.ReasoningTokens}
	for i, p := range pairs {
		p.Delta = pointerFromNull(values[i*2])
		p.Set = pointerFromNull(values[i*2+1])
	}
	s.EstimatedCostDeltaMicro = pointerFromNull(values[10])
	return s, true, nil
}
func sameCorrection(stored storedCorrection, input Correction, at string) bool {
	return stored.ID == input.ID && stored.AttemptID == input.AttemptID && stored.TargetEventID == input.TargetEventID && stored.OperationID == input.OperationID && stored.Actor == input.Actor && stored.Reason == input.Reason && stored.correctedText == at && stored.Currency == input.Currency && sameCorrectionValue(stored.InputTokens, input.InputTokens) && sameCorrectionValue(stored.OutputTokens, input.OutputTokens) && sameCorrectionValue(stored.CacheReadTokens, input.CacheReadTokens) && sameCorrectionValue(stored.CacheWriteTokens, input.CacheWriteTokens) && sameCorrectionValue(stored.ReasoningTokens, input.ReasoningTokens) && samePointer(stored.EstimatedCostDeltaMicro, input.EstimatedCostDeltaMicro)
}
func sameCorrectionValue(a, b CorrectionValue) bool {
	return samePointer(a.Delta, b.Delta) && samePointer(a.Set, b.Set)
}

func validateAccountingV2Schema(ctx context.Context, tx *sql.Tx) error {
	tables := []struct {
		name        string
		columns     []ledgerColumn
		constraints []string
	}{
		{"accounting_attempt_contexts", []ledgerColumn{
			{name: "attempt_id", kind: "TEXT", primary: true}, {name: "protocol", kind: "TEXT", notNull: true}, {name: "effective_model", kind: "TEXT", notNull: true}, {name: "evidence", kind: "TEXT", notNull: true}, {name: "response_id", kind: "TEXT"}, {name: "task_id", kind: "TEXT"}, {name: "tool_run_id", kind: "TEXT"},
		}, []string{"referencesaccounting_attempts(id)", "check(protocolin('openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content'))", "check(evidencein('provider_response','provider_stream','background_result','system_terminal'))"}},
		{"accounting_attempt_dispatches", []ledgerColumn{
			{name: "attempt_id", kind: "TEXT", primary: true}, {name: "operation_id", kind: "TEXT", notNull: true}, {name: "dispatched_at", kind: "TEXT", notNull: true},
		}, []string{"referencesaccounting_attempts(id)", "operation_idtextnotnullunique"}},
		{"accounting_usage_events", []ledgerColumn{
			{name: "id", kind: "TEXT", primary: true}, {name: "attempt_id", kind: "TEXT", notNull: true}, {name: "request_id", kind: "TEXT", notNull: true}, {name: "status", kind: "TEXT", notNull: true}, {name: "evidence", kind: "TEXT", notNull: true}, {name: "finished_at", kind: "TEXT", notNull: true}, {name: "source_event_id", kind: "TEXT"}, {name: "response_id", kind: "TEXT"}, {name: "task_id", kind: "TEXT"}, {name: "tool_run_id", kind: "TEXT"}, {name: "input_tokens", kind: "INTEGER"}, {name: "output_tokens", kind: "INTEGER"}, {name: "cache_read_tokens", kind: "INTEGER"}, {name: "cache_write_tokens", kind: "INTEGER"}, {name: "reasoning_tokens", kind: "INTEGER"}, {name: "price_version", kind: "TEXT"}, {name: "currency", kind: "TEXT"}, {name: "estimated_cost_micro", kind: "INTEGER"},
		}, []string{"attempt_idtextnotnullunique", "referencesaccounting_attempts(id)", "referencesaccounting_requests(id)", "check(statusin('succeeded','failed','cancelled','interrupted'))", "check(evidencein('provider_response','provider_stream','background_result','system_terminal'))", "check(input_tokensisnullorinput_tokens>=0)", "check(output_tokensisnulloroutput_tokens>=0)", "check(cache_read_tokensisnullorcache_read_tokens>=0)", "check(cache_write_tokensisnullorcache_write_tokens>=0)", "check(reasoning_tokensisnullorreasoning_tokens>=0)", "check(estimated_cost_microisnullorestimated_cost_micro>=0)", "check(reasoning_tokensisnulloroutput_tokensisnullorreasoning_tokens<=output_tokens)", "check((price_versionisnullandcurrencyisnullandestimated_cost_microisnull)or(price_versionisnotnullandcurrencyisnotnull))", "check(currencyisnullor(length(currency)=3andcurrency=upper(currency)andcurrencynotglob'*[^a-z]*'))", "check(estimated_cost_microisnullor(input_tokensisnotnullandoutput_tokensisnotnullandcache_read_tokensisnotnullandcache_write_tokensisnotnull))"}},
		{"accounting_usage_corrections", []ledgerColumn{
			{name: "id", kind: "TEXT", primary: true}, {name: "attempt_id", kind: "TEXT", notNull: true}, {name: "target_event_id", kind: "TEXT", notNull: true}, {name: "operation_id", kind: "TEXT", notNull: true}, {name: "sequence", kind: "INTEGER", notNull: true}, {name: "actor", kind: "TEXT", notNull: true}, {name: "reason", kind: "TEXT", notNull: true}, {name: "corrected_at", kind: "TEXT", notNull: true}, {name: "currency", kind: "TEXT"}, {name: "input_delta", kind: "INTEGER"}, {name: "input_set", kind: "INTEGER"}, {name: "output_delta", kind: "INTEGER"}, {name: "output_set", kind: "INTEGER"}, {name: "cache_read_delta", kind: "INTEGER"}, {name: "cache_read_set", kind: "INTEGER"}, {name: "cache_write_delta", kind: "INTEGER"}, {name: "cache_write_set", kind: "INTEGER"}, {name: "reasoning_delta", kind: "INTEGER"}, {name: "reasoning_set", kind: "INTEGER"}, {name: "estimated_cost_delta_micro", kind: "INTEGER"},
		}, []string{"operation_idtextnotnullunique", "unique(attempt_id,sequence)", "check(sequencebetween1and9007199254740991)", "referencesaccounting_attempts(id)", "referencesaccounting_usage_events(id)", "check(reasonin('late_provider_usage','admin_reconciliation'))", "check(input_setisnullorinput_set>=0)", "check(output_setisnulloroutput_set>=0)", "check(cache_read_setisnullorcache_read_set>=0)", "check(cache_write_setisnullorcache_write_set>=0)", "check(reasoning_setisnullorreasoning_set>=0)", "check(input_deltaisnullorinput_setisnull)", "check(output_deltaisnulloroutput_setisnull)", "check(cache_read_deltaisnullorcache_read_setisnull)", "check(cache_write_deltaisnullorcache_write_setisnull)", "check(reasoning_deltaisnullorreasoning_setisnull)"}},
	}
	for _, table := range tables {
		if err := validateLedgerTable(ctx, tx, table.name, table.columns, table.constraints); err != nil {
			return errors.New("invalid accounting v2 schema " + table.name + ": " + err.Error())
		}
		for _, suffix := range []string{"_no_update", "_no_delete"} {
			var triggerKind, triggerSQL string
			verb := "UPDATE"
			if suffix == "_no_delete" {
				verb = "DELETE"
			}
			if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, table.name+suffix).Scan(&triggerKind, &triggerSQL); err != nil {
				return errors.New("invalid accounting v2 immutability trigger")
			}
			actual := strings.TrimSuffix(normalizePriceSQL(triggerSQL), ";")
			canonical := `CREATE TRIGGER ` + table.name + suffix + ` BEFORE ` + verb + ` ON ` + table.name + ` BEGIN SELECT RAISE(ABORT,'accounting v2 facts are immutable'); END`
			canonicalIfMissing := `CREATE TRIGGER IF NOT EXISTS ` + table.name + suffix + ` BEFORE ` + verb + ` ON ` + table.name + ` BEGIN SELECT RAISE(ABORT,'accounting v2 facts are immutable'); END`
			expected := strings.TrimSuffix(normalizePriceSQL(canonical), ";")
			expectedIfMissing := strings.TrimSuffix(normalizePriceSQL(canonicalIfMissing), ";")
			if triggerKind != "trigger" || actual != expected && actual != expectedIfMissing {
				return errors.New("invalid accounting v2 immutability trigger")
			}
		}
	}
	indexes := map[string]string{"accounting_usage_events_finished_idx": "onaccounting_usage_events(finished_at,id)", "accounting_usage_corrections_attempt_idx": "onaccounting_usage_corrections(attempt_id,sequence)"}
	for index, expected := range indexes {
		var kind, definition string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, index).Scan(&kind, &definition); err != nil {
			return errors.New("missing accounting v2 index")
		}
		normalized := normalizePriceSQL(definition)
		if kind != "index" || definition == "" || !strings.Contains(normalized, expected) || strings.Contains(normalized, "uniqueindex") {
			return errors.New("missing accounting v2 index")
		}
	}
	for _, table := range []string{"accounting_attempt_contexts", "accounting_attempt_dispatches", "accounting_usage_events", "accounting_usage_corrections"} {
		violations, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(`+table+`)`)
		if err != nil {
			return err
		}
		if violations.Next() {
			violations.Close()
			return errors.New("accounting v2 foreign key violation")
		}
		if err := violations.Close(); err != nil {
			return err
		}
	}
	return nil
}
