// Independently implemented from docs/usage-management-contract.md.
package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	usageQueryTimeout = 5 * time.Second
	usageMaxRange     = 31 * 24 * time.Hour
	usageMaxIDLength  = 200
	usageMaxCursor    = 2048
)

// SQLite compares the stored RFC3339Nano values as TEXT. Padding the fractional
// part gives whole-second and variable-width timestamps an exact nanosecond key.
const usageRequestTimeKeySQL = `(substr(r.started_at,1,19)||'.'||substr((CASE WHEN instr(r.started_at,'.')>0 THEN substr(r.started_at,instr(r.started_at,'.')+1,length(r.started_at)-instr(r.started_at,'.')-1) ELSE '' END)||'000000000',1,9)||'Z')`
const usageAttemptTimeKeySQL = `(substr(a.started_at,1,19)||'.'||substr((CASE WHEN instr(a.started_at,'.')>0 THEN substr(a.started_at,instr(a.started_at,'.')+1,length(a.started_at)-instr(a.started_at,'.')-1) ELSE '' END)||'000000000',1,9)||'Z')`

type usageFilters struct {
	From       time.Time
	To         time.Time
	EmployeeID string
	KeyID      string
	ModelID    string
	UpstreamID string
	Provider   string
	Status     string
}

type usageCursor struct {
	Version     int    `json:"v"`
	StartedKey  string `json:"started_key"`
	ID          string `json:"id"`
	From        string `json:"from"`
	To          string `json:"to"`
	Fingerprint string `json:"fingerprint"`
}

type usageStatusCounts struct {
	Total       string `json:"total"`
	Pending     string `json:"pending"`
	Succeeded   string `json:"succeeded"`
	Failed      string `json:"failed"`
	Cancelled   string `json:"cancelled"`
	Interrupted string `json:"interrupted"`
}

type usageTokenCounts struct {
	KnownTotal      string `json:"known_total"`
	UnknownAttempts string `json:"unknown_attempts"`
}

type usageAttemptSummary struct {
	Currency            string           `json:"currency"`
	Total               string           `json:"total"`
	Pending             string           `json:"pending"`
	Succeeded           string           `json:"succeeded"`
	Failed              string           `json:"failed"`
	Cancelled           string           `json:"cancelled"`
	Interrupted         string           `json:"interrupted"`
	KnownCostMicro      string           `json:"known_cost_micro"`
	UnknownCostAttempts string           `json:"unknown_cost_attempts"`
	InputTokens         usageTokenCounts `json:"input_tokens"`
	OutputTokens        usageTokenCounts `json:"output_tokens"`
	CacheReadTokens     usageTokenCounts `json:"cache_read_tokens"`
	CacheWriteTokens    usageTokenCounts `json:"cache_write_tokens"`
}

type usageRequestItem struct {
	ID           string  `json:"id"`
	EmployeeID   string  `json:"employee_id"`
	KeyID        string  `json:"key_id"`
	ModelID      string  `json:"model_id"`
	Provider     string  `json:"provider"`
	Status       string  `json:"status"`
	StartedAt    string  `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
	AttemptCount string  `json:"attempt_count"`
	startedKey   string
}

type usageAttemptItem struct {
	ID               string  `json:"id"`
	RequestID        string  `json:"request_id"`
	AccountID        string  `json:"account_id"`
	Provider         string  `json:"provider"`
	Dispatch         string  `json:"dispatch"`
	Status           string  `json:"status"`
	StartedAt        string  `json:"started_at"`
	FinishedAt       *string `json:"finished_at"`
	PriceVersion     *string `json:"price_version"`
	Currency         *string `json:"currency"`
	InputTokens      *string `json:"input_tokens"`
	OutputTokens     *string `json:"output_tokens"`
	CacheReadTokens  *string `json:"cache_read_tokens"`
	CacheWriteTokens *string `json:"cache_write_tokens"`
	CostMicro        *string `json:"cost_micro"`
}

func (a *App) registerUsageHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/usage/summary", a.requireAdmin(a.usageSummary, false))
	mux.HandleFunc("GET /admin/api/v1/usage/requests", a.requireAdmin(a.usageRequestsList, false))
	mux.HandleFunc("GET /admin/api/v1/usage/requests/{id}/attempts", a.requireAdmin(a.usageRequestAttempts, false))
}

func (a *App) usageSummary(w http.ResponseWriter, r *http.Request, _ adminSession) {
	ctx, cancel := context.WithTimeout(r.Context(), usageQueryTimeout)
	defer cancel()
	filters, _, err := parseUsageFilters(r, false)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid usage query.")
		return
	}
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeUsageStorageError(w)
		return
	}
	defer tx.Rollback()

	where, args := usageRequestWhere(filters)
	var counts [6]int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN r.status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN r.status='succeeded' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN r.status='failed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN r.status='cancelled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN r.status='interrupted' THEN 1 ELSE 0 END),0)
		FROM accounting_requests r WHERE `+where, args...).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5])
	if err != nil {
		writeUsageStorageError(w)
		return
	}

	attemptWhere, attemptArgs := usageAttemptWhere(filters)
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(a.currency,'UNKNOWN'),COUNT(*),
		COALESCE(SUM(CASE WHEN a.status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN a.status='succeeded' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN a.status='failed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN a.status='cancelled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN a.status='interrupted' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(a.cost_micro),0),COALESCE(SUM(CASE WHEN a.status<>'pending' AND a.cost_micro IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(a.input_tokens),0),COALESCE(SUM(CASE WHEN a.status<>'pending' AND a.input_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(a.output_tokens),0),COALESCE(SUM(CASE WHEN a.status<>'pending' AND a.output_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(a.cache_read_tokens),0),COALESCE(SUM(CASE WHEN a.status<>'pending' AND a.cache_read_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(a.cache_write_tokens),0),COALESCE(SUM(CASE WHEN a.status<>'pending' AND a.cache_write_tokens IS NULL THEN 1 ELSE 0 END),0)
		FROM accounting_attempts a JOIN accounting_requests r ON r.id=a.request_id
		WHERE `+attemptWhere+` GROUP BY COALESCE(a.currency,'UNKNOWN') ORDER BY COALESCE(a.currency,'UNKNOWN')`, attemptArgs...)
	if err != nil {
		writeUsageStorageError(w)
		return
	}
	attempts := make([]usageAttemptSummary, 0)
	for rows.Next() {
		var currency string
		var values [16]int64
		if err := rows.Scan(&currency, &values[0], &values[1], &values[2], &values[3], &values[4], &values[5], &values[6], &values[7], &values[8], &values[9], &values[10], &values[11], &values[12], &values[13], &values[14], &values[15]); err != nil {
			rows.Close()
			writeUsageStorageError(w)
			return
		}
		attempts = append(attempts, usageAttemptSummary{
			Currency: currency, Total: decimal(values[0]), Pending: decimal(values[1]), Succeeded: decimal(values[2]), Failed: decimal(values[3]), Cancelled: decimal(values[4]), Interrupted: decimal(values[5]),
			KnownCostMicro: decimal(values[6]), UnknownCostAttempts: decimal(values[7]),
			InputTokens:      usageTokenCounts{KnownTotal: decimal(values[8]), UnknownAttempts: decimal(values[9])},
			OutputTokens:     usageTokenCounts{KnownTotal: decimal(values[10]), UnknownAttempts: decimal(values[11])},
			CacheReadTokens:  usageTokenCounts{KnownTotal: decimal(values[12]), UnknownAttempts: decimal(values[13])},
			CacheWriteTokens: usageTokenCounts{KnownTotal: decimal(values[14]), UnknownAttempts: decimal(values[15])},
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeUsageStorageError(w)
		return
	}
	if err := rows.Close(); err != nil {
		writeUsageStorageError(w)
		return
	}
	if err := tx.Commit(); err != nil {
		writeUsageStorageError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": filters.From.Format(time.RFC3339), "to": filters.To.Format(time.RFC3339),
		"requests": usageStatusCounts{Total: decimal(counts[0]), Pending: decimal(counts[1]), Succeeded: decimal(counts[2]), Failed: decimal(counts[3]), Cancelled: decimal(counts[4]), Interrupted: decimal(counts[5])},
		"attempts": attempts,
	})
}

func (a *App) usageRequestsList(w http.ResponseWriter, r *http.Request, _ adminSession) {
	ctx, cancel := context.WithTimeout(r.Context(), usageQueryTimeout)
	defer cancel()
	filters, cursor, err := parseUsageFilters(r, true)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid usage query.")
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid usage query.")
			return
		}
	}
	where, args := usageRequestWhere(filters)
	if cursor != nil {
		where += ` AND (` + usageRequestTimeKeySQL + `<? OR (` + usageRequestTimeKeySQL + `=? AND r.id<?))`
		args = append(args, cursor.StartedKey, cursor.StartedKey, cursor.ID)
	}
	attemptCount := `(SELECT COUNT(*) FROM accounting_attempts ac WHERE ac.request_id=r.id)`
	if filters.UpstreamID != "" {
		attemptCount = `(SELECT COUNT(*) FROM accounting_attempts ac WHERE ac.request_id=r.id AND ac.account_id=?)`
		args = append([]any{filters.UpstreamID}, args...)
	}
	query := `SELECT r.id,r.employee_id,r.key_id,r.model_id,r.provider,r.status,r.started_at,r.finished_at,` + attemptCount + `,` + usageRequestTimeKeySQL + `
		FROM accounting_requests r WHERE ` + where + ` ORDER BY ` + usageRequestTimeKeySQL + ` DESC,r.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := a.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		writeUsageStorageError(w)
		return
	}
	defer rows.Close()
	items := make([]usageRequestItem, 0, limit)
	for rows.Next() {
		var item usageRequestItem
		var finished sql.NullString
		var attemptCount int64
		if err := rows.Scan(&item.ID, &item.EmployeeID, &item.KeyID, &item.ModelID, &item.Provider, &item.Status, &item.StartedAt, &finished, &attemptCount, &item.startedKey); err != nil {
			writeUsageStorageError(w)
			return
		}
		if item.StartedAt, err = normalizeStoredUsageTime(item.StartedAt); err != nil {
			writeUsageStorageError(w)
			return
		}
		if finished.Valid {
			value, parseErr := normalizeStoredUsageTime(finished.String)
			if parseErr != nil {
				writeUsageStorageError(w)
				return
			}
			item.FinishedAt = &value
		}
		item.AttemptCount = decimal(attemptCount)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeUsageStorageError(w)
		return
	}
	if err := rows.Close(); err != nil {
		writeUsageStorageError(w)
		return
	}
	var next *string
	if len(items) > limit {
		last := items[limit-1]
		encoded, encodeErr := encodeUsageCursor(usageCursor{Version: 1, StartedKey: last.startedKey, ID: last.ID, From: filters.From.Format(time.RFC3339), To: filters.To.Format(time.RFC3339), Fingerprint: usageFilterFingerprint(filters)})
		if encodeErr != nil {
			writeUsageStorageError(w)
			return
		}
		next = &encoded
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next, "from": filters.From.Format(time.RFC3339), "to": filters.To.Format(time.RFC3339)})
}

func (a *App) usageRequestAttempts(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" || !validIdentifier(r.PathValue("id"), usageMaxIDLength) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid usage request identifier.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageQueryTimeout)
	defer cancel()
	requestID := r.PathValue("id")
	var exists int
	if err := a.store.db.QueryRowContext(ctx, `SELECT 1 FROM accounting_requests WHERE id=?`, requestID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Usage request was not found.")
		return
	} else if err != nil {
		writeUsageStorageError(w)
		return
	}
	rows, err := a.store.db.QueryContext(ctx, `SELECT a.id,a.request_id,a.account_id,a.provider,a.dispatch,a.status,a.started_at,a.finished_at,
		a.price_version,a.currency,a.input_tokens,a.output_tokens,a.cache_read_tokens,a.cache_write_tokens,a.cost_micro
		FROM accounting_attempts a WHERE a.request_id=? ORDER BY `+usageAttemptTimeKeySQL+`,a.id LIMIT 101`, requestID)
	if err != nil {
		writeUsageStorageError(w)
		return
	}
	defer rows.Close()
	items := make([]usageAttemptItem, 0)
	for rows.Next() {
		var item usageAttemptItem
		var finished, price, currency sql.NullString
		var input, output, cacheRead, cacheWrite, cost sql.NullInt64
		if err := rows.Scan(&item.ID, &item.RequestID, &item.AccountID, &item.Provider, &item.Dispatch, &item.Status, &item.StartedAt, &finished, &price, &currency, &input, &output, &cacheRead, &cacheWrite, &cost); err != nil {
			writeUsageStorageError(w)
			return
		}
		if item.StartedAt, err = normalizeStoredUsageTime(item.StartedAt); err != nil {
			writeUsageStorageError(w)
			return
		}
		if finished.Valid {
			value, parseErr := normalizeStoredUsageTime(finished.String)
			if parseErr != nil {
				writeUsageStorageError(w)
				return
			}
			item.FinishedAt = &value
		}
		item.PriceVersion = nullableText(price)
		item.Currency = nullableText(currency)
		item.InputTokens = nullableDecimal(input)
		item.OutputTokens = nullableDecimal(output)
		item.CacheReadTokens = nullableDecimal(cacheRead)
		item.CacheWriteTokens = nullableDecimal(cacheWrite)
		item.CostMicro = nullableDecimal(cost)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeUsageStorageError(w)
		return
	}
	if err := rows.Close(); err != nil {
		writeUsageStorageError(w)
		return
	}
	if len(items) > 100 {
		writeAdminError(w, http.StatusConflict, "read_limit_exceeded", "Too many attempts to return.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func parseUsageFilters(r *http.Request, listing bool) (usageFilters, *usageCursor, error) {
	allowed := map[string]bool{"from": true, "to": true, "employee_id": true, "key_id": true, "model_id": true, "upstream_id": true, "provider": true, "status": true}
	if listing {
		allowed["limit"], allowed["cursor"] = true, true
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return usageFilters{}, nil, errors.New("invalid query encoding")
	}
	for key, values := range query {
		if !allowed[key] || len(values) != 1 || values[0] == "" {
			return usageFilters{}, nil, errors.New("invalid query")
		}
	}
	var cursor *usageCursor
	if raw := query.Get("cursor"); raw != "" {
		if !listing {
			return usageFilters{}, nil, errors.New("cursor not allowed")
		}
		decoded, err := decodeUsageCursor(raw)
		if err != nil {
			return usageFilters{}, nil, err
		}
		cursor = &decoded
	}
	rawFrom, rawTo := query.Get("from"), query.Get("to")
	if rawFrom == "" && rawTo == "" && cursor != nil {
		rawFrom, rawTo = cursor.From, cursor.To
	}
	var from, to time.Time
	if rawFrom == "" && rawTo == "" {
		to = time.Now().UTC().Truncate(time.Second)
		from = to.Add(-24 * time.Hour)
	} else {
		if rawFrom == "" || rawTo == "" {
			return usageFilters{}, nil, errors.New("incomplete range")
		}
		from, err = parseWholeSecond(rawFrom)
		if err != nil {
			return usageFilters{}, nil, err
		}
		to, err = parseWholeSecond(rawTo)
		if err != nil {
			return usageFilters{}, nil, err
		}
		if !from.Before(to) || to.After(from.Add(usageMaxRange)) {
			return usageFilters{}, nil, errors.New("invalid range")
		}
	}
	filters := usageFilters{From: from, To: to, EmployeeID: query.Get("employee_id"), KeyID: query.Get("key_id"), ModelID: query.Get("model_id"), UpstreamID: query.Get("upstream_id"), Provider: query.Get("provider"), Status: query.Get("status")}
	for _, id := range []string{filters.EmployeeID, filters.KeyID, filters.ModelID, filters.UpstreamID} {
		if id != "" && !validIdentifier(id, usageMaxIDLength) {
			return usageFilters{}, nil, errors.New("invalid identifier")
		}
	}
	if filters.Provider != "" && !usageEnum(filters.Provider, "openai", "openai-compatible", "anthropic", "gemini", "codex") {
		return usageFilters{}, nil, errors.New("invalid provider")
	}
	if filters.Status != "" && !usageEnum(filters.Status, "pending", "succeeded", "failed", "cancelled", "interrupted") {
		return usageFilters{}, nil, errors.New("invalid status")
	}
	if cursor != nil {
		if cursor.From != filters.From.Format(time.RFC3339) || cursor.To != filters.To.Format(time.RFC3339) || cursor.Fingerprint != usageFilterFingerprint(filters) {
			return usageFilters{}, nil, errors.New("cursor filter mismatch")
		}
	}
	return filters, cursor, nil
}

func usageRequestWhere(filters usageFilters) (string, []any) {
	where := usageRequestTimeKeySQL + `>=? AND ` + usageRequestTimeKeySQL + `<?`
	args := []any{usageTimeKey(filters.From), usageTimeKey(filters.To)}
	for _, filter := range []struct{ column, value string }{{"r.employee_id", filters.EmployeeID}, {"r.key_id", filters.KeyID}, {"r.model_id", filters.ModelID}, {"r.provider", filters.Provider}, {"r.status", filters.Status}} {
		if filter.value != "" {
			where += ` AND ` + filter.column + `=?`
			args = append(args, filter.value)
		}
	}
	if filters.UpstreamID != "" {
		where += ` AND EXISTS(SELECT 1 FROM accounting_attempts af WHERE af.request_id=r.id AND af.account_id=?)`
		args = append(args, filters.UpstreamID)
	}
	return where, args
}

func usageAttemptWhere(filters usageFilters) (string, []any) {
	where, args := usageRequestWhere(filters)
	if filters.UpstreamID != "" {
		// The parent EXISTS is redundant for joined attempts and would do extra work.
		marker := ` AND EXISTS(SELECT 1 FROM accounting_attempts af WHERE af.request_id=r.id AND af.account_id=?)`
		where = strings.TrimSuffix(where, marker)
		args = args[:len(args)-1]
		where += ` AND a.account_id=?`
		args = append(args, filters.UpstreamID)
	}
	return where, args
}

func parseWholeSecond(value string) (time.Time, error) {
	if len(value) > 35 || strings.Contains(value, ".") {
		return time.Time{}, errors.New("fractional time")
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil || parsed.Nanosecond() != 0 {
		return time.Time{}, errors.New("invalid time")
	}
	return parsed.UTC(), nil
}

func usageTimeKey(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

func normalizeStoredUsageTime(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", err
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func usageFilterFingerprint(filters usageFilters) string {
	canonical := strings.Join([]string{filters.From.Format(time.RFC3339), filters.To.Format(time.RFC3339), filters.EmployeeID, filters.KeyID, filters.ModelID, filters.UpstreamID, filters.Provider, filters.Status}, "\x00")
	digest := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(digest[:])
}

func encodeUsageCursor(cursor usageCursor) (string, error) {
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeUsageCursor(raw string) (usageCursor, error) {
	if len(raw) == 0 || len(raw) > usageMaxCursor {
		return usageCursor{}, errors.New("invalid cursor length")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(decoded) > usageMaxCursor {
		return usageCursor{}, errors.New("invalid cursor encoding")
	}
	var cursor usageCursor
	dec := json.NewDecoder(strings.NewReader(string(decoded)))
	opening, err := dec.Token()
	if err != nil || opening != json.Delim('{') {
		return usageCursor{}, errors.New("invalid cursor object")
	}
	seen := make(map[string]bool, 6)
	for dec.More() {
		keyToken, err := dec.Token()
		key, ok := keyToken.(string)
		if err != nil || !ok || seen[key] {
			return usageCursor{}, errors.New("invalid cursor key")
		}
		seen[key] = true
		switch key {
		case "v":
			err = dec.Decode(&cursor.Version)
		case "started_key":
			err = dec.Decode(&cursor.StartedKey)
		case "id":
			err = dec.Decode(&cursor.ID)
		case "from":
			err = dec.Decode(&cursor.From)
		case "to":
			err = dec.Decode(&cursor.To)
		case "fingerprint":
			err = dec.Decode(&cursor.Fingerprint)
		default:
			return usageCursor{}, errors.New("unknown cursor key")
		}
		if err != nil {
			return usageCursor{}, errors.New("invalid cursor value")
		}
	}
	closing, err := dec.Token()
	if err != nil || closing != json.Delim('}') || len(seen) != 6 {
		return usageCursor{}, errors.New("invalid cursor shape")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return usageCursor{}, errors.New("invalid cursor trailing data")
	}
	if cursor.Version != 1 || len(cursor.StartedKey) != len("2006-01-02T15:04:05.000000000Z") || !validIdentifier(cursor.ID, usageMaxIDLength) || len(cursor.Fingerprint) != sha256.Size*2 {
		return usageCursor{}, errors.New("invalid cursor shape")
	}
	if parsed, err := time.Parse("2006-01-02T15:04:05.000000000Z", cursor.StartedKey); err != nil || usageTimeKey(parsed) != cursor.StartedKey {
		return usageCursor{}, errors.New("invalid cursor time")
	}
	from, err := parseWholeSecond(cursor.From)
	if err != nil || from.Format(time.RFC3339) != cursor.From {
		return usageCursor{}, errors.New("invalid cursor range")
	}
	to, err := parseWholeSecond(cursor.To)
	if err != nil || to.Format(time.RFC3339) != cursor.To || !from.Before(to) || to.After(from.Add(usageMaxRange)) {
		return usageCursor{}, errors.New("invalid cursor range")
	}
	if _, err := hex.DecodeString(cursor.Fingerprint); err != nil {
		return usageCursor{}, errors.New("invalid cursor fingerprint")
	}
	return cursor, nil
}

func usageEnum(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func decimal(value int64) string { return strconv.FormatInt(value, 10) }

func nullableText(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableDecimal(value sql.NullInt64) *string {
	if !value.Valid {
		return nil
	}
	formatted := decimal(value.Int64)
	return &formatted
}

func writeUsageStorageError(w http.ResponseWriter) {
	writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Usage data is temporarily unavailable.")
}
