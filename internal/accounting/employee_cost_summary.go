// Independently authored for docs/employee-self-upstream-estimated-cost-summary-contract.md.
package accounting

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
)

type EmployeeCostGroup struct {
	Currency     *string
	Attempts     int64
	KnownMicro   int64
	UnknownCount int64
}

type EmployeeCostSummary struct {
	Total    int64
	Pending  int64
	Terminal int64
	Groups   []EmployeeCostGroup
}

type employeeCostAttempt struct {
	id, requestID string
	provider      Provider
	status        Status
	started       time.Time
	finished      *time.Time
	price         *PriceSnapshot
	legacyUsage   Usage
	legacyCost    *int64
}

func costText(raw any) (string, error) {
	value, ok := raw.(string)
	if !ok {
		return "", ErrInvalid
	}
	return value, nil
}

func costOptionalText(raw any) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	value, err := costText(raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func costOptionalInt(raw any) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	value, ok := raw.(int64)
	if !ok || value < 0 {
		return nil, ErrInvalid
	}
	return &value, nil
}

func costSignedInt(raw any) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	value, ok := raw.(int64)
	if !ok {
		return nil, ErrInvalid
	}
	return &value, nil
}

func costTime(raw any) (time.Time, error) {
	value, err := costText(raw)
	if err != nil {
		return time.Time{}, err
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.UTC().Format(time.RFC3339Nano) != value {
		return time.Time{}, ErrInvalid
	}
	return parsed, nil
}

func costOptionalTime(raw any) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	value, err := costTime(raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func costPrice(values []any) (*PriceSnapshot, error) {
	if len(values) != 6 {
		return nil, ErrInvalid
	}
	version, err := costOptionalText(values[0])
	if err != nil {
		return nil, err
	}
	currency, err := costOptionalText(values[1])
	if err != nil {
		return nil, err
	}
	var rates [4]*int64
	for i := range rates {
		rates[i], err = costOptionalInt(values[i+2])
		if err != nil {
			return nil, err
		}
	}
	if version == nil && currency == nil && rates[0] == nil && rates[1] == nil && rates[2] == nil && rates[3] == nil {
		return nil, nil
	}
	if version == nil || currency == nil || rates[0] == nil || rates[1] == nil || rates[2] == nil || rates[3] == nil {
		return nil, ErrInvalid
	}
	price := &PriceSnapshot{Version: *version, Currency: *currency, InputPerMillionMicro: *rates[0], OutputPerMillionMicro: *rates[1], CacheReadPerMillionMicro: *rates[2], CacheWritePerMillionMicro: *rates[3]}
	if !validUpperPrice(*price) {
		return nil, ErrInvalid
	}
	return price, nil
}

func costRawRow(rows *sql.Rows, count int) ([]any, error) {
	values := make([]any, count)
	args := make([]any, count)
	for i := range values {
		args[i] = &values[i]
	}
	if err := rows.Scan(args...); err != nil {
		return nil, err
	}
	return values, nil
}

func costAttempt(values []any, employeeID string, from, to time.Time) (employeeCostAttempt, error) {
	var item employeeCostAttempt
	if len(values) != 27 {
		return item, ErrInvalid
	}
	requestID, err := costText(values[0])
	if err != nil {
		return item, err
	}
	owner, err := costText(values[1])
	if err != nil || owner != employeeID || !validID(requestID) {
		return item, ErrInvalid
	}
	keyID, err := costText(values[2])
	if err != nil || !validID(keyID) {
		return item, ErrInvalid
	}
	model, err := costText(values[3])
	if err != nil || !validModelID(model) {
		return item, ErrInvalid
	}
	parentProvider, err := costText(values[4])
	if err != nil || !validProvider(Provider(parentProvider)) {
		return item, ErrInvalid
	}
	parentStart, err := costTime(values[5])
	if err != nil || parentStart.Before(from) || !parentStart.Before(to) {
		return item, ErrInvalid
	}
	parentFinish, err := costOptionalTime(values[6])
	if err != nil {
		return item, err
	}
	parentStatus, err := costText(values[7])
	if err != nil || !(parentStatus == string(StatusPending) && parentFinish == nil || terminalStatus(Status(parentStatus)) && parentFinish != nil && !parentFinish.Before(parentStart)) {
		return item, ErrInvalid
	}
	item.id, err = costText(values[8])
	if err != nil || !validID(item.id) {
		return item, ErrInvalid
	}
	item.requestID, err = costText(values[9])
	if err != nil || item.requestID != requestID {
		return item, ErrInvalid
	}
	accountID, err := costText(values[10])
	if err != nil || !validID(accountID) {
		return item, ErrInvalid
	}
	provider, err := costText(values[11])
	if err != nil || !validProvider(Provider(provider)) || provider != parentProvider {
		return item, ErrInvalid
	}
	item.provider = Provider(provider)
	dispatch, err := costText(values[12])
	if err != nil || !validDispatch(Dispatch(dispatch)) {
		return item, ErrInvalid
	}
	item.started, err = costTime(values[13])
	if err != nil || item.started.Before(parentStart) {
		return item, ErrInvalid
	}
	item.finished, err = costOptionalTime(values[14])
	if err != nil {
		return item, err
	}
	status, err := costText(values[15])
	if err != nil {
		return item, err
	}
	item.status = Status(status)
	if !(item.status == StatusPending && item.finished == nil || terminalStatus(item.status) && item.finished != nil && !item.finished.Before(item.started)) {
		return item, ErrInvalid
	}
	if parentFinish != nil && (item.finished == nil || item.started.After(*parentFinish) || item.finished.After(*parentFinish)) {
		return item, ErrInvalid
	}
	fields := []**int64{&item.legacyUsage.InputTokens, &item.legacyUsage.OutputTokens, &item.legacyUsage.CacheReadTokens, &item.legacyUsage.CacheWriteTokens}
	for i, field := range fields {
		*field, err = costOptionalInt(values[16+i])
		if err != nil {
			return item, err
		}
	}
	item.price, err = costPrice(values[20:26])
	if err != nil {
		return item, err
	}
	item.legacyCost, err = costOptionalInt(values[26])
	if err != nil || item.status == StatusPending && (item.legacyUsage.InputTokens != nil || item.legacyUsage.OutputTokens != nil || item.legacyUsage.CacheReadTokens != nil || item.legacyUsage.CacheWriteTokens != nil || item.legacyCost != nil) || item.legacyCost != nil && item.price == nil {
		return item, ErrInvalid
	}
	return item, nil
}

// ReadEmployeeEstimatedCost owns one read-only transaction and returns only
// aggregate fields. The caller supplies an already authenticated employee ID.
func (l *Ledger) ReadEmployeeEstimatedCost(ctx context.Context, employeeID string, from, to time.Time) (EmployeeCostSummary, error) {
	var summary EmployeeCostSummary
	if l == nil || l.db == nil || ctx == nil || !validID(employeeID) || from.IsZero() || to.Sub(from) != 24*time.Hour || from.Location() != time.UTC || to.Location() != time.UTC || from.Nanosecond() != 0 || to.Nanosecond() != 0 {
		return summary, ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return summary, err
	}
	defer tx.Rollback()
	if err := validateLedgerSchema(ctx, tx); err != nil {
		return summary, err
	}
	if err := validateAccountingV2Schema(ctx, tx); err != nil {
		return summary, err
	}
	if err := costRequireBaseIndexes(ctx, tx); err != nil {
		return summary, err
	}
	if err := costValidateAttemptForeignKeys(ctx, tx); err != nil {
		return summary, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.employee_id,r.key_id,r.model_id,r.provider,r.started_at,r.finished_at,r.status,
		a.id,a.request_id,a.account_id,a.provider,a.dispatch,a.started_at,a.finished_at,a.status,
		a.input_tokens,a.output_tokens,a.cache_read_tokens,a.cache_write_tokens,
		a.price_version,a.currency,a.input_rate,a.output_rate,a.cache_read_rate,a.cache_write_rate,a.cost_micro
		FROM accounting_requests r INDEXED BY accounting_requests_employee_idx JOIN accounting_attempts a ON a.request_id=r.id
		WHERE r.employee_id=? AND r.started_at>=? AND r.started_at<?
		ORDER BY r.id,a.id LIMIT ?`, employeeID, from.Format("2006-01-02T15:04:05"), to.Format("2006-01-02T15:04:05"), MaxAccountingV2ReportAttempts+1)
	if err != nil {
		return summary, err
	}
	attempts := make([]employeeCostAttempt, 0)
	for rows.Next() {
		if len(attempts) == MaxAccountingV2ReportAttempts {
			_ = rows.Close()
			return summary, ErrAccountingV2Limit
		}
		values, err := costRawRow(rows, 27)
		if err != nil {
			_ = rows.Close()
			return summary, err
		}
		attempt, err := costAttempt(values, employeeID, from, to)
		if err != nil {
			_ = rows.Close()
			return summary, err
		}
		attempts = append(attempts, attempt)
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return summary, err
	}
	if closeErr != nil {
		return summary, closeErr
	}
	var unsupportedSuccess string
	err = tx.QueryRowContext(ctx, `SELECT r.id FROM accounting_requests r INDEXED BY accounting_requests_employee_idx
		WHERE r.employee_id=? AND r.started_at>=? AND r.started_at<? AND r.status='succeeded'
		AND NOT EXISTS (SELECT 1 FROM accounting_attempts a WHERE a.request_id=r.id AND a.status='succeeded') LIMIT 1`,
		employeeID, from.Format("2006-01-02T15:04:05"), to.Format("2006-01-02T15:04:05")).Scan(&unsupportedSuccess)
	if err == nil {
		return EmployeeCostSummary{}, ErrInvalid
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return EmployeeCostSummary{}, err
	}
	groups := make(map[string]*EmployeeCostGroup)
	for _, attempt := range attempts {
		if summary.Total, err = checkedReportAdd(summary.Total, 1); err != nil {
			return EmployeeCostSummary{}, err
		}
		if attempt.status == StatusPending {
			if err := costPendingFacts(ctx, tx, attempt); err != nil {
				return EmployeeCostSummary{}, err
			}
			summary.Pending, err = checkedReportAdd(summary.Pending, 1)
			if err != nil {
				return EmployeeCostSummary{}, err
			}
			continue
		}
		summary.Terminal, err = checkedReportAdd(summary.Terminal, 1)
		if err != nil {
			return EmployeeCostSummary{}, err
		}
		currency := ""
		if attempt.price != nil {
			currency = attempt.price.Currency
		}
		group := groups[currency]
		if group == nil {
			group = &EmployeeCostGroup{}
			if currency != "" {
				group.Currency = &currency
			}
			groups[currency] = group
		}
		group.Attempts, err = checkedReportAdd(group.Attempts, 1)
		if err != nil {
			return EmployeeCostSummary{}, err
		}
		cost, err := costTerminal(ctx, tx, attempt)
		if err != nil {
			return EmployeeCostSummary{}, err
		}
		if cost == nil {
			group.UnknownCount, err = checkedReportAdd(group.UnknownCount, 1)
		} else {
			group.KnownMicro, err = checkedReportAdd(group.KnownMicro, *cost)
		}
		if err != nil {
			return EmployeeCostSummary{}, err
		}
	}
	for _, group := range groups {
		summary.Groups = append(summary.Groups, *group)
	}
	sort.Slice(summary.Groups, func(i, j int) bool {
		left, right := summary.Groups[i].Currency, summary.Groups[j].Currency
		return left != nil && (right == nil || *left < *right)
	})
	if summary.Groups == nil {
		summary.Groups = []EmployeeCostGroup{}
	}
	if err := ctx.Err(); err != nil {
		return EmployeeCostSummary{}, err
	}
	if err := tx.Commit(); err != nil {
		return EmployeeCostSummary{}, err
	}
	return summary, nil
}

func costRequireBaseIndexes(ctx context.Context, tx *sql.Tx) error {
	for _, index := range []struct{ name, table, fields string }{
		{"accounting_requests_employee_idx", "accounting_requests", "employee_id, started_at"},
		{"accounting_attempts_request_idx", "accounting_attempts", "request_id, started_at"},
	} {
		var kind, definition string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, index.name).Scan(&kind, &definition); err != nil {
			return ErrInvalid
		}
		canonical := normalizePriceSQL("CREATE INDEX IF NOT EXISTS " + index.name + " ON " + index.table + "(" + index.fields + ")")
		withoutGuard := normalizePriceSQL("CREATE INDEX " + index.name + " ON " + index.table + "(" + index.fields + ")")
		actual := strings.TrimSuffix(normalizePriceSQL(definition), ";")
		if kind != "index" || actual != canonical && actual != withoutGuard {
			return ErrInvalid
		}
	}
	return nil
}

func costValidateAttemptForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(accounting_attempts)`)
	if err != nil {
		return err
	}
	if rows.Next() {
		_ = rows.Close()
		return ErrInvalid
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func costPendingFacts(ctx context.Context, tx *sql.Tx, attempt employeeCostAttempt) error {
	started, err := costContextFor(ctx, tx, attempt)
	if err != nil {
		return err
	}
	dispatched, err := costDispatchFor(ctx, tx, attempt)
	if err != nil || dispatched != nil && started == nil {
		return ErrInvalid
	}
	var events, corrections int64
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM accounting_usage_events WHERE attempt_id=?),(SELECT COUNT(*) FROM accounting_usage_corrections WHERE attempt_id=?)`, attempt.id, attempt.id).Scan(&events, &corrections); err != nil {
		return err
	}
	if events != 0 || corrections != 0 {
		return ErrInvalid
	}
	return nil
}

type costContext struct {
	protocol             UsageProtocol
	evidence             UsageEvidence
	response, task, tool *string
}

func costContextFor(ctx context.Context, tx *sql.Tx, attempt employeeCostAttempt) (*costContext, error) {
	var raw [6]any
	err := tx.QueryRowContext(ctx, `SELECT protocol,effective_model,evidence,response_id,task_id,tool_run_id FROM accounting_attempt_contexts WHERE attempt_id=?`, attempt.id).
		Scan(&raw[0], &raw[1], &raw[2], &raw[3], &raw[4], &raw[5])
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	protocol, err := costText(raw[0])
	if err != nil {
		return nil, err
	}
	model, err := costText(raw[1])
	if err != nil {
		return nil, err
	}
	evidence, err := costText(raw[2])
	if err != nil {
		return nil, err
	}
	response, err := costOptionalText(raw[3])
	if err != nil {
		return nil, err
	}
	task, err := costOptionalText(raw[4])
	if err != nil {
		return nil, err
	}
	tool, err := costOptionalText(raw[5])
	if err != nil {
		return nil, err
	}
	if !validAttemptContext(AttemptStart{Provider: attempt.provider, Protocol: UsageProtocol(protocol), EffectiveModel: model, Evidence: UsageEvidence(evidence), ResponseID: response, TaskID: task, ToolRunID: tool}) {
		return nil, ErrInvalid
	}
	return &costContext{UsageProtocol(protocol), UsageEvidence(evidence), response, task, tool}, nil
}

func costDispatchFor(ctx context.Context, tx *sql.Tx, attempt employeeCostAttempt) (*time.Time, error) {
	var operation, rawTime any
	err := tx.QueryRowContext(ctx, `SELECT operation_id,dispatched_at FROM accounting_attempt_dispatches WHERE attempt_id=?`, attempt.id).Scan(&operation, &rawTime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id, err := costText(operation)
	if err != nil || !validID(id) {
		return nil, ErrInvalid
	}
	at, err := costTime(rawTime)
	if err != nil || at.Before(attempt.started) || attempt.finished != nil && at.After(*attempt.finished) {
		return nil, ErrInvalid
	}
	return &at, nil
}

type costEvent struct {
	id       string
	status   Status
	evidence UsageEvidence
	finished time.Time
	usage    effectiveCounters
	cost     *int64
}

func costEventFor(ctx context.Context, tx *sql.Tx, attempt employeeCostAttempt, started *costContext) (*costEvent, error) {
	var raw [18]any
	args := make([]any, len(raw))
	for i := range raw {
		args[i] = &raw[i]
	}
	err := tx.QueryRowContext(ctx, `SELECT id,attempt_id,request_id,status,evidence,finished_at,source_event_id,response_id,task_id,tool_run_id,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,reasoning_tokens,price_version,currency,estimated_cost_micro FROM accounting_usage_events WHERE attempt_id=?`, attempt.id).Scan(args...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	id, err := costText(raw[0])
	if err != nil {
		return nil, err
	}
	attemptID, err := costText(raw[1])
	if err != nil {
		return nil, err
	}
	requestID, err := costText(raw[2])
	if err != nil {
		return nil, err
	}
	status, err := costText(raw[3])
	if err != nil {
		return nil, err
	}
	evidence, err := costText(raw[4])
	if err != nil {
		return nil, err
	}
	finished, err := costTime(raw[5])
	if err != nil {
		return nil, err
	}
	if id != usageEventID(attempt.id) || attemptID != attempt.id || requestID != attempt.requestID || Status(status) != attempt.status || attempt.finished == nil || !finished.Equal(*attempt.finished) || started == nil {
		return nil, ErrInvalid
	}
	source, err := costOptionalText(raw[6])
	if err != nil || source != nil && !validID(*source) {
		return nil, ErrInvalid
	}
	ids := [3]*string{}
	for i := range ids {
		ids[i], err = costOptionalText(raw[7+i])
		if err != nil || ids[i] != nil && !validID(*ids[i]) {
			return nil, ErrInvalid
		}
	}
	for i, prior := range [3]*string{started.response, started.task, started.tool} {
		if prior != nil && !sameNullableString(prior, ids[i]) {
			return nil, ErrInvalid
		}
	}
	if UsageEvidence(evidence) != started.evidence && !(UsageEvidence(evidence) == EvidenceSystemTerminal && attempt.status == StatusInterrupted) {
		return nil, ErrInvalid
	}
	if UsageEvidence(evidence) != EvidenceProviderResponse && UsageEvidence(evidence) != EvidenceProviderStream && UsageEvidence(evidence) != EvidenceBackgroundResult && UsageEvidence(evidence) != EvidenceSystemTerminal {
		return nil, ErrInvalid
	}
	version, err := costOptionalText(raw[15])
	if err != nil {
		return nil, err
	}
	currency, err := costOptionalText(raw[16])
	if err != nil {
		return nil, err
	}
	if attempt.price == nil && (version != nil || currency != nil) || attempt.price != nil && (version == nil || currency == nil || *version != attempt.price.Version || *currency != attempt.price.Currency) {
		return nil, ErrInvalid
	}
	values := [5]*int64{}
	for i := range values {
		values[i], err = costOptionalInt(raw[10+i])
		if err != nil {
			return nil, err
		}
	}
	if values[4] != nil && values[1] != nil && *values[4] > *values[1] {
		return nil, ErrInvalid
	}
	cost, err := costOptionalInt(raw[17])
	if err != nil {
		return nil, err
	}
	return &costEvent{id: id, status: Status(status), evidence: UsageEvidence(evidence), finished: finished, usage: effectiveCounters{values[0], values[1], values[2], values[3], values[4]}, cost: cost}, nil
}

func costCorrectionCount(ctx context.Context, tx *sql.Tx, attemptID string) (int64, error) {
	var count int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounting_usage_corrections WHERE attempt_id=?`, attemptID).Scan(&count)
	return count, err
}

func costTerminal(ctx context.Context, tx *sql.Tx, attempt employeeCostAttempt) (*int64, error) {
	started, err := costContextFor(ctx, tx, attempt)
	if err != nil {
		return nil, err
	}
	dispatched, err := costDispatchFor(ctx, tx, attempt)
	if err != nil {
		return nil, err
	}
	event, err := costEventFor(ctx, tx, attempt, started)
	if err != nil {
		return nil, err
	}
	if event == nil {
		count, err := costCorrectionCount(ctx, tx, attempt.id)
		if err != nil {
			return nil, err
		}
		if count != 0 || dispatched != nil || started != nil {
			return nil, ErrInvalid
		}
		// Only a completely unadorned terminal attempt is a legacy row.
		return nil, nil
	}
	if dispatched == nil && (event.evidence != EvidenceSystemTerminal || started.evidence != EvidenceSystemTerminal || attempt.status == StatusSucceeded) {
		return nil, ErrInvalid
	}
	if dispatched != nil && event.finished.Before(*dispatched) {
		return nil, ErrInvalid
	}
	baseCost, err := calculateCost(started.protocol, event.usage.usage(), attempt.price)
	if err != nil || !samePointer(baseCost, event.cost) || !samePointer(baseCost, attempt.legacyCost) || !sameUsage(attempt.legacyUsage, event.usage.usage()) {
		return nil, ErrInvalid
	}
	current := event.usage
	rows, err := tx.QueryContext(ctx, `SELECT id,attempt_id,target_event_id,operation_id,sequence,actor,reason,corrected_at,currency,
		input_delta,input_set,output_delta,output_set,cache_read_delta,cache_read_set,cache_write_delta,cache_write_set,reasoning_delta,reasoning_set,estimated_cost_delta_micro
		FROM accounting_usage_corrections WHERE attempt_id=? ORDER BY sequence LIMIT ?`, attempt.id, MaxAccountingV2Corrections+1)
	if err != nil {
		return nil, err
	}
	sequence := int64(0)
	previous := event.finished
	for rows.Next() {
		sequence++
		if sequence > MaxAccountingV2Corrections {
			_ = rows.Close()
			return nil, ErrAccountingV2Limit
		}
		raw, err := costRawRow(rows, 20)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		correction, number, corrected, err := costCorrection(raw, attempt, event, previous)
		if err != nil || number != sequence {
			_ = rows.Close()
			return nil, ErrInvalid
		}
		updated, err := applyCorrection(current, correction)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		oldCost, err := calculateCost(started.protocol, current.usage(), attempt.price)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		newCost, err := calculateCost(started.protocol, updated.usage(), attempt.price)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		var delta *int64
		if oldCost != nil && newCost != nil {
			value, ok := checkedSignedSub(*newCost, *oldCost)
			if !ok {
				_ = rows.Close()
				return nil, ErrInvalid
			}
			delta = &value
		}
		if !samePointer(delta, correction.EstimatedCostDeltaMicro) {
			_ = rows.Close()
			return nil, ErrInvalid
		}
		current, previous = updated, corrected
	}
	err = rows.Err()
	closeErr := rows.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if dispatched == nil || event.evidence == EvidenceSystemTerminal {
		return nil, nil
	}
	return calculateCost(started.protocol, current.usage(), attempt.price)
}

func costCorrection(raw []any, attempt employeeCostAttempt, event *costEvent, prior time.Time) (Correction, int64, time.Time, error) {
	var correction Correction
	invalid := func() (Correction, int64, time.Time, error) { return Correction{}, 0, time.Time{}, ErrInvalid }
	if len(raw) != 20 {
		return invalid()
	}
	var err error
	correction.ID, err = costText(raw[0])
	if err != nil {
		return invalid()
	}
	correction.AttemptID, err = costText(raw[1])
	if err != nil {
		return invalid()
	}
	correction.TargetEventID, err = costText(raw[2])
	if err != nil {
		return invalid()
	}
	correction.OperationID, err = costText(raw[3])
	if err != nil {
		return invalid()
	}
	sequence, ok := raw[4].(int64)
	if !ok {
		return invalid()
	}
	correction.Actor, err = costText(raw[5])
	if err != nil {
		return invalid()
	}
	reason, err := costText(raw[6])
	if err != nil {
		return invalid()
	}
	correction.Reason = CorrectionReason(reason)
	corrected, err := costTime(raw[7])
	if err != nil || !corrected.After(prior) {
		return invalid()
	}
	correction.CorrectedAt = corrected
	currency, err := costOptionalText(raw[8])
	if err != nil {
		return invalid()
	}
	if attempt.price == nil && currency != nil || attempt.price != nil && (currency == nil || *currency != attempt.price.Currency) {
		return invalid()
	}
	if currency != nil {
		correction.Currency = *currency
	}
	fields := []*CorrectionValue{&correction.InputTokens, &correction.OutputTokens, &correction.CacheReadTokens, &correction.CacheWriteTokens, &correction.ReasoningTokens}
	for i, field := range fields {
		field.Delta, err = costSignedInt(raw[9+2*i])
		if err != nil {
			return invalid()
		}
		field.Set, err = costOptionalInt(raw[10+2*i])
		if err != nil {
			return invalid()
		}
	}
	correction.EstimatedCostDeltaMicro, err = costSignedInt(raw[19])
	if err != nil {
		return invalid()
	}
	if correction.AttemptID != attempt.id || correction.TargetEventID != event.id || sequence < 1 || !validCorrection(correction) {
		return invalid()
	}
	return correction, sequence, corrected, nil
}
