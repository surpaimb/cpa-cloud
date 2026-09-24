package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cpacloud.local/server/internal/accounting"
)

const accountingV2MaxExportBytes = 4 << 20

// migrateAccountingV2 is the single integration hook for E accounting and
// selector-aware budget schema. The App owner controls when it runs relative
// to the existing governance budget migrations.
func migrateAccountingV2(ctx context.Context, db *sql.DB) error {
	return accounting.NewLedger(db).MigrateV2(ctx)
}

// registerAccountingV2Handlers is intentionally separate from App.Handler so
// the integration owner can register shared routes exactly once.
func (a *App) registerAccountingV2Handlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/usage/daily", a.requireAdmin(a.accountingV2Daily, false))
	mux.HandleFunc("GET /admin/api/v1/usage/monthly", a.requireAdmin(a.accountingV2Monthly, false))
	mux.HandleFunc("GET /admin/api/v1/usage/export", a.requireAdmin(a.accountingV2Export, false))
	mux.HandleFunc("POST /admin/api/v1/billing/usage-corrections", a.requireAdmin(a.accountingV2Correction, true))
	a.registerGeneralBudgetHandlers(mux)
}

type accountingV2ReportView struct {
	PeriodStart             string           `json:"period_start"`
	PeriodEnd               string           `json:"period_end"`
	Currency                string           `json:"currency"`
	Requests                string           `json:"requests"`
	Attempts                string           `json:"attempts"`
	Corrections             string           `json:"corrections"`
	MissingEvidenceAttempts string           `json:"missing_evidence_attempts"`
	KnownEstimatedCostMicro string           `json:"known_estimated_cost_micro"`
	UnknownCostAttempts     string           `json:"unknown_cost_attempts"`
	InputTokens             usageTokenCounts `json:"input_tokens"`
	OutputTokens            usageTokenCounts `json:"output_tokens"`
	CacheReadTokens         usageTokenCounts `json:"cache_read_tokens"`
	CacheWriteTokens        usageTokenCounts `json:"cache_write_tokens"`
	ReasoningTokens         usageTokenCounts `json:"reasoning_tokens"`
}

func (a *App) accountingV2Daily(w http.ResponseWriter, r *http.Request, _ adminSession) {
	a.accountingV2Report(w, r, accounting.AccountingV2Day)
}

func (a *App) accountingV2Monthly(w http.ResponseWriter, r *http.Request, _ adminSession) {
	a.accountingV2Report(w, r, accounting.AccountingV2Month)
}

func (a *App) accountingV2Report(w http.ResponseWriter, r *http.Request, period accounting.AccountingV2Period) {
	filters, _, err := parseAccountingV2Filters(r.URL.RawQuery, false)
	if err != nil {
		writeAccountingV2Invalid(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageQueryTimeout)
	defer cancel()
	items, err := accounting.NewLedger(a.store.db).AccountingV2Report(ctx, filters, period)
	if err != nil {
		writeAccountingV2Error(w, err)
		return
	}
	views := make([]accountingV2ReportView, 0, len(items))
	for _, item := range items {
		views = append(views, accountingV2ReportView{
			PeriodStart: item.PeriodStart.Format(time.RFC3339), PeriodEnd: item.PeriodEnd.Format(time.RFC3339), Currency: item.Currency,
			Requests: decimal(item.Requests), Attempts: decimal(item.Attempts), Corrections: decimal(item.Corrections), MissingEvidenceAttempts: decimal(item.MissingEvidenceAttempts),
			KnownEstimatedCostMicro: decimal(item.KnownEstimatedCostMicro), UnknownCostAttempts: decimal(item.UnknownCostAttempts),
			InputTokens: tokenSummaryView(item.InputTokens), OutputTokens: tokenSummaryView(item.OutputTokens), CacheReadTokens: tokenSummaryView(item.CacheReadTokens), CacheWriteTokens: tokenSummaryView(item.CacheWriteTokens), ReasoningTokens: tokenSummaryView(item.ReasoningTokens),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": filters.From.Format(time.RFC3339), "to": filters.To.Format(time.RFC3339), "granularity": string(period), "items": views})
}

func tokenSummaryView(value accounting.KnownValueSummary) usageTokenCounts {
	return usageTokenCounts{KnownTotal: decimal(value.KnownTotal), UnknownAttempts: decimal(value.UnknownAttempts)}
}

func (a *App) accountingV2Export(w http.ResponseWriter, r *http.Request, _ adminSession) {
	filters, limit, err := parseAccountingV2Filters(r.URL.RawQuery, true)
	if err != nil {
		writeAccountingV2Invalid(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageQueryTimeout)
	defer cancel()
	items, err := accounting.NewLedger(a.store.db).AccountingV2Export(ctx, filters, limit)
	if err != nil {
		writeAccountingV2Error(w, err)
		return
	}
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	_ = writer.Write([]string{"attempt_id", "request_id", "employee_id", "key_id", "public_model", "effective_model", "account_id", "provider", "protocol", "dispatch_kind", "status", "started_at", "durable_dispatched_at", "finished_at", "event_id", "evidence", "response_id", "task_id", "tool_run_id", "corrections", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens", "reasoning_tokens", "price_version", "currency", "estimated_cost_micro"})
	for _, item := range items {
		_ = writer.Write(accountingV2CSVRecord([]string{
			item.AttemptID, item.RequestID, item.EmployeeID, item.KeyID, item.PublicModel, stringValue(item.EffectiveModel), item.AccountID, string(item.Provider), protocolValue(item.Protocol), string(item.Dispatch), string(item.Status), item.StartedAt.Format(time.RFC3339Nano), timeValue(item.DispatchedAt), item.FinishedAt.Format(time.RFC3339Nano),
			stringValue(item.EventID), evidenceValue(item.Evidence), stringValue(item.ResponseID), stringValue(item.TaskID), stringValue(item.ToolRunID), decimal(item.CorrectionCount), decimalValue(item.InputTokens), decimalValue(item.OutputTokens), decimalValue(item.CacheReadTokens), decimalValue(item.CacheWriteTokens), decimalValue(item.ReasoningTokens), stringValue(item.PriceVersion), stringValue(item.Currency), decimalValue(item.EstimatedCostMicro),
		}))
		writer.Flush()
		if writer.Error() != nil || output.Len() > accountingV2MaxExportBytes {
			writeAdminError(w, http.StatusConflict, "export_limit_exceeded", "The export is too large; narrow the time range or filters.")
			return
		}
	}
	writer.Flush()
	if writer.Error() != nil {
		writeAccountingV2Error(w, writer.Error())
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="usage-settlement.csv"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(output.Bytes())
}

func (a *App) accountingV2Correction(w http.ResponseWriter, r *http.Request, session adminSession) {
	if r.URL.RawQuery != "" {
		writeAccountingV2Invalid(w)
		return
	}
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil {
		writeAccountingV2Invalid(w)
		return
	}
	allowed := map[string]bool{
		"id": true, "attempt_id": true, "target_event_id": true, "operation_id": true, "reason": true, "corrected_at": true, "currency": true,
		"input_tokens_delta": true, "input_tokens_set": true, "output_tokens_delta": true, "output_tokens_set": true,
		"cache_read_tokens_delta": true, "cache_read_tokens_set": true, "cache_write_tokens_delta": true, "cache_write_tokens_set": true,
		"reasoning_tokens_delta": true, "reasoning_tokens_set": true, "estimated_cost_delta_micro": true,
	}
	for key := range object {
		if !allowed[key] {
			writeAccountingV2Invalid(w)
			return
		}
	}
	for _, required := range []string{"id", "attempt_id", "target_event_id", "operation_id", "reason", "corrected_at", "currency"} {
		if _, ok := object[required]; !ok {
			writeAccountingV2Invalid(w)
			return
		}
	}
	id, okID := object["id"].(string)
	attemptID, okAttempt := object["attempt_id"].(string)
	targetID, okTarget := object["target_event_id"].(string)
	operationID, okOperation := object["operation_id"].(string)
	reasonText, okReason := object["reason"].(string)
	correctedText, okCorrected := object["corrected_at"].(string)
	currency, okCurrency := object["currency"].(string)
	correctedAt, timeErr := time.Parse(time.RFC3339Nano, correctedText)
	if !okID || !okAttempt || !okTarget || !okOperation || !okReason || !okCorrected || !okCurrency || timeErr != nil || correctedAt.Location() != time.UTC || correctedAt.Format(time.RFC3339Nano) != correctedText {
		writeAccountingV2Invalid(w)
		return
	}
	reason := accounting.CorrectionReason(reasonText)
	correction := accounting.Correction{ID: id, AttemptID: attemptID, TargetEventID: targetID, OperationID: operationID, Actor: session.AdminID, Reason: reason, CorrectedAt: correctedAt, Currency: currency}
	for prefix, destination := range map[string]*accounting.CorrectionValue{
		"input_tokens": &correction.InputTokens, "output_tokens": &correction.OutputTokens, "cache_read_tokens": &correction.CacheReadTokens, "cache_write_tokens": &correction.CacheWriteTokens, "reasoning_tokens": &correction.ReasoningTokens,
	} {
		value, ok := parseCorrectionValue(object, prefix)
		if !ok {
			writeAccountingV2Invalid(w)
			return
		}
		*destination = value
	}
	if value, present := object["estimated_cost_delta_micro"]; present {
		parsed, ok := parseCanonicalSignedDecimal(value)
		if !ok {
			writeAccountingV2Invalid(w)
			return
		}
		correction.EstimatedCostDeltaMicro = &parsed
	}
	if err := accounting.NewLedger(a.store.db).AppendCorrection(r.Context(), correction); err != nil {
		writeAccountingV2Error(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "attempt_id": attemptID, "operation_id": operationID, "ok": true})
}

func parseCorrectionValue(object map[string]any, prefix string) (accounting.CorrectionValue, bool) {
	deltaRaw, hasDelta := object[prefix+"_delta"]
	setRaw, hasSet := object[prefix+"_set"]
	if hasDelta && hasSet {
		return accounting.CorrectionValue{}, false
	}
	var result accounting.CorrectionValue
	if hasDelta {
		value, ok := parseCanonicalSignedDecimal(deltaRaw)
		if !ok {
			return accounting.CorrectionValue{}, false
		}
		result.Delta = &value
	}
	if hasSet {
		value, ok := parseCanonicalUnsignedDecimal(setRaw)
		if !ok {
			return accounting.CorrectionValue{}, false
		}
		result.Set = &value
	}
	return result, true
}

func parseCanonicalSignedDecimal(raw any) (int64, bool) {
	text, ok := raw.(string)
	if !ok || text == "" || text == "-0" || text[0] == '+' {
		return 0, false
	}
	digits := text
	if text[0] == '-' {
		digits = text[1:]
	}
	if digits == "" || len(digits) > 1 && digits[0] == '0' {
		return 0, false
	}
	for _, character := range digits {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	value, err := strconv.ParseInt(text, 10, 64)
	return value, err == nil
}

func parseCanonicalUnsignedDecimal(raw any) (int64, bool) {
	value, ok := parseCanonicalSignedDecimal(raw)
	return value, ok && value >= 0
}

func parseAccountingV2Filters(rawQuery string, export bool) (accounting.AccountingV2Filters, int, error) {
	query, err := url.ParseQuery(rawQuery)
	if err != nil {
		return accounting.AccountingV2Filters{}, 0, err
	}
	allowed := map[string]bool{"from": true, "to": true, "employee_id": true, "key_id": true, "model_id": true, "upstream_id": true, "provider": true, "protocol": true, "status": true, "currency": true}
	if export {
		allowed["limit"] = true
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || values[0] == "" {
			return accounting.AccountingV2Filters{}, 0, errors.New("invalid query")
		}
	}
	to := time.Now().UTC().Truncate(time.Second)
	from := to.Add(-31 * 24 * time.Hour)
	if query.Get("from") != "" || query.Get("to") != "" {
		if query.Get("from") == "" || query.Get("to") == "" {
			return accounting.AccountingV2Filters{}, 0, errors.New("incomplete range")
		}
		from, err = parseWholeSecond(query.Get("from"))
		if err != nil {
			return accounting.AccountingV2Filters{}, 0, err
		}
		to, err = parseWholeSecond(query.Get("to"))
		if err != nil {
			return accounting.AccountingV2Filters{}, 0, err
		}
	}
	filters := accounting.AccountingV2Filters{From: from, To: to, EmployeeID: query.Get("employee_id"), KeyID: query.Get("key_id"), ModelID: query.Get("model_id"), AccountID: query.Get("upstream_id"), Provider: accounting.Provider(query.Get("provider")), Protocol: accounting.UsageProtocol(query.Get("protocol")), Status: accounting.Status(query.Get("status")), Currency: query.Get("currency")}
	limit := accounting.MaxAccountingV2ExportRows
	if rawLimit := query.Get("limit"); rawLimit != "" {
		parsed, parseErr := strconv.Atoi(rawLimit)
		if parseErr != nil || parsed < 1 || parsed > accounting.MaxAccountingV2ExportRows || strconv.Itoa(parsed) != rawLimit {
			return accounting.AccountingV2Filters{}, 0, errors.New("invalid limit")
		}
		limit = parsed
	}
	return filters, limit, nil
}

func decimalValue(value *int64) string {
	if value == nil {
		return ""
	}
	return decimal(*value)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func protocolValue(value *accounting.UsageProtocol) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func timeValue(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func evidenceValue(value *accounting.UsageEvidence) string {
	if value == nil {
		return ""
	}
	return string(*value)
}

func accountingV2CSVRecord(values []string) []string {
	for index, value := range values {
		if value != "" && strings.ContainsRune("=+-@", rune(value[0])) {
			values[index] = "'" + value
		}
	}
	return values
}

func writeAccountingV2Error(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, accounting.ErrInvalid):
		writeAccountingV2Invalid(w)
	case errors.Is(err, accounting.ErrNotFound):
		writeAdminError(w, http.StatusNotFound, "not_found", "The accounting event was not found.")
	case errors.Is(err, accounting.ErrConflict):
		writeAdminError(w, http.StatusConflict, "accounting_conflict", "The accounting facts changed or the operation conflicts with existing data.")
	case errors.Is(err, accounting.ErrAccountingV2Limit):
		writeAdminError(w, http.StatusConflict, "export_limit_exceeded", "The export is too large; narrow the time range or filters.")
	default:
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Accounting data is temporarily unavailable.")
	}
}

func writeAccountingV2Invalid(w http.ResponseWriter) {
	writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid accounting request.")
}
