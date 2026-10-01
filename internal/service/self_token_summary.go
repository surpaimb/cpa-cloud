// Independently authored for docs/employee-self-token-summary-contract.md.
package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"time"
)

type selfTokenRequestCounts struct {
	Total       string `json:"total"`
	Pending     string `json:"pending"`
	Succeeded   string `json:"succeeded"`
	Failed      string `json:"failed"`
	Cancelled   string `json:"cancelled"`
	Interrupted string `json:"interrupted"`
}

type selfTokenCounts struct {
	KnownTotal      string `json:"known_total"`
	UnknownAttempts string `json:"unknown_attempts"`
}

type selfTokenAttemptCounts struct {
	Total            string          `json:"total"`
	Pending          string          `json:"pending"`
	InputTokens      selfTokenCounts `json:"input_tokens"`
	OutputTokens     selfTokenCounts `json:"output_tokens"`
	CacheReadTokens  selfTokenCounts `json:"cache_read_tokens"`
	CacheWriteTokens selfTokenCounts `json:"cache_write_tokens"`
}

type selfTokenSummaryResponse struct {
	From     string                 `json:"from"`
	To       string                 `json:"to"`
	Requests selfTokenRequestCounts `json:"requests"`
	Attempts selfTokenAttemptCounts `json:"attempts"`
}

func parseSelfTokenWindow(raw string, now time.Time) (time.Time, time.Time, error) {
	invalid := func() (time.Time, time.Time, error) {
		return time.Time{}, time.Time{}, errors.New("invalid summary window")
	}
	if len(raw) > 256 {
		return invalid()
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return invalid()
	}
	var fromRaw, toRaw string
	for key, list := range values {
		if len(list) != 1 || list[0] == "" {
			return invalid()
		}
		switch key {
		case "from":
			fromRaw = list[0]
		case "to":
			toRaw = list[0]
		default:
			return invalid()
		}
	}
	if fromRaw == "" && toRaw == "" {
		to := now.UTC().Truncate(time.Second).Add(time.Second)
		return to.Add(-24 * time.Hour), to, nil
	}
	if fromRaw == "" || toRaw == "" {
		return invalid()
	}
	from, err := selfRequestWholeSecond(fromRaw)
	if err != nil {
		return invalid()
	}
	to, err := selfRequestWholeSecond(toRaw)
	if err != nil || !selfRequestWindow(from, to) {
		return invalid()
	}
	return from, to, nil
}

func validateSelfTokenRequestCounts(values [7]int64) bool {
	remaining := values[0]
	if remaining < 0 || values[6] != 0 {
		return false
	}
	for _, count := range values[1:6] {
		if count < 0 || count > remaining {
			return false
		}
		remaining -= count
	}
	return remaining == 0
}

func validateSelfTokenAttemptCounts(values [11]int64) bool {
	total, pending := values[0], values[1]
	if total < 0 || pending < 0 || pending > total || values[10] != 0 {
		return false
	}
	terminal := total - pending
	for index := 2; index <= 8; index += 2 {
		if values[index] < 0 || values[index+1] < 0 || values[index+1] > terminal {
			return false
		}
	}
	return true
}

func readSelfTokenSummary(ctx context.Context, db *sql.DB, employeeID string, from, to time.Time) (selfTokenSummaryResponse, error) {
	return readSelfTokenSummaryScope(ctx, db, employeeID, "", from, to, nil, nil)
}

// A Key-scoped summary validates ownership and reads both aggregates in the
// same transaction. An empty keyID preserves the existing all-Key projection.
func readSelfTokenSummaryScope(ctx context.Context, db *sql.DB, employeeID, keyID string, from, to time.Time, afterOwnership func(), commitHook func(*sql.Tx) error) (selfTokenSummaryResponse, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return selfTokenSummaryResponse{}, err
	}
	defer tx.Rollback()
	where := `r.employee_id=?`
	args := []any{employeeID}
	if keyID != "" {
		var idType, employeeType string
		err = tx.QueryRowContext(ctx, `SELECT typeof(k.id),typeof(k.employee_id) FROM access_keys k
			WHERE k.id=? AND k.employee_id=? AND NOT EXISTS(
				SELECT 1 FROM employee_self_key_slots slot WHERE slot.key_id=k.id
				AND (slot.state<>'issued' OR slot.employee_id<>k.employee_id))`, keyID, employeeID).Scan(&idType, &employeeType)
		if err != nil {
			return selfTokenSummaryResponse{}, err
		}
		if idType != "text" || employeeType != "text" {
			return selfTokenSummaryResponse{}, errors.New("invalid key metadata")
		}
		if afterOwnership != nil {
			afterOwnership()
		}
		where += ` AND r.key_id=?`
		args = append(args, keyID)
	}
	where += ` AND ` + selfRequestStartedKeySQL + `>=? AND ` + selfRequestStartedKeySQL + `<?`
	args = append(args, selfRequestSortTime(from), selfRequestSortTime(to))
	var requests [7]int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN r.status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN r.status='succeeded' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN r.status='failed' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN r.status='cancelled' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN r.status='interrupted' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN typeof(r.started_at)<>'text' OR strftime('%Y',r.started_at) IS NULL OR typeof(r.status)<>'text' OR r.status NOT IN ('pending','succeeded','failed','cancelled','interrupted') THEN 1 ELSE 0 END),0)
		FROM accounting_requests r WHERE `+where, args...).Scan(&requests[0], &requests[1], &requests[2], &requests[3], &requests[4], &requests[5], &requests[6])
	if err != nil || !validateSelfTokenRequestCounts(requests) {
		return selfTokenSummaryResponse{}, errors.New("request summary unavailable")
	}
	var attempts [11]int64
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN a.status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(a.input_tokens),0),COALESCE(SUM(CASE WHEN a.status<>'pending' AND a.input_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(a.output_tokens),0),COALESCE(SUM(CASE WHEN a.status<>'pending' AND a.output_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(a.cache_read_tokens),0),COALESCE(SUM(CASE WHEN a.status<>'pending' AND a.cache_read_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(a.cache_write_tokens),0),COALESCE(SUM(CASE WHEN a.status<>'pending' AND a.cache_write_tokens IS NULL THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN typeof(a.status)<>'text' OR a.status NOT IN ('pending','succeeded','failed','cancelled','interrupted') OR
			(a.status='pending' AND (a.input_tokens IS NOT NULL OR a.output_tokens IS NOT NULL OR a.cache_read_tokens IS NOT NULL OR a.cache_write_tokens IS NOT NULL)) OR
			(a.input_tokens IS NOT NULL AND (typeof(a.input_tokens)<>'integer' OR a.input_tokens<0)) OR
			(a.output_tokens IS NOT NULL AND (typeof(a.output_tokens)<>'integer' OR a.output_tokens<0)) OR
			(a.cache_read_tokens IS NOT NULL AND (typeof(a.cache_read_tokens)<>'integer' OR a.cache_read_tokens<0)) OR
			(a.cache_write_tokens IS NOT NULL AND (typeof(a.cache_write_tokens)<>'integer' OR a.cache_write_tokens<0)) THEN 1 ELSE 0 END),0)
		FROM accounting_attempts a JOIN accounting_requests r ON r.id=a.request_id WHERE `+where, args...).Scan(&attempts[0], &attempts[1], &attempts[2], &attempts[3], &attempts[4], &attempts[5], &attempts[6], &attempts[7], &attempts[8], &attempts[9], &attempts[10])
	if err != nil || !validateSelfTokenAttemptCounts(attempts) {
		return selfTokenSummaryResponse{}, errors.New("attempt summary unavailable")
	}
	if ctx.Err() != nil {
		return selfTokenSummaryResponse{}, ctx.Err()
	}
	commit := tx.Commit
	if commitHook != nil {
		commit = func() error { return commitHook(tx) }
	}
	if err := commit(); err != nil {
		return selfTokenSummaryResponse{}, err
	}
	return selfTokenSummaryResponse{
		From: from.UTC().Format(time.RFC3339), To: to.UTC().Format(time.RFC3339),
		Requests: selfTokenRequestCounts{Total: decimal(requests[0]), Pending: decimal(requests[1]), Succeeded: decimal(requests[2]), Failed: decimal(requests[3]), Cancelled: decimal(requests[4]), Interrupted: decimal(requests[5])},
		Attempts: selfTokenAttemptCounts{Total: decimal(attempts[0]), Pending: decimal(attempts[1]),
			InputTokens:      selfTokenCounts{KnownTotal: decimal(attempts[2]), UnknownAttempts: decimal(attempts[3])},
			OutputTokens:     selfTokenCounts{KnownTotal: decimal(attempts[4]), UnknownAttempts: decimal(attempts[5])},
			CacheReadTokens:  selfTokenCounts{KnownTotal: decimal(attempts[6]), UnknownAttempts: decimal(attempts[7])},
			CacheWriteTokens: selfTokenCounts{KnownTotal: decimal(attempts[8]), UnknownAttempts: decimal(attempts[9])}},
	}, nil
}

func (a *App) selfTokenSummary(w http.ResponseWriter, r *http.Request, session selfSession) {
	from, to, err := parseSelfTokenWindow(r.URL.RawQuery, time.Now())
	if err != nil || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageQueryTimeout)
	defer cancel()
	response, err := readSelfTokenSummary(ctx, a.store.db, session.EmployeeID, from, to)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// Independently authored for docs/employee-self-key-token-summary-contract.md.
func (a *App) selfKeyTokenSummary(w http.ResponseWriter, r *http.Request, session selfSession) {
	keyID := r.PathValue("id")
	from, to, err := parseSelfTokenWindow(r.URL.RawQuery, time.Now())
	if !validSelfKeyID(keyID) || err != nil || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), usageQueryTimeout)
	defer cancel()
	response, err := readSelfTokenSummaryScope(ctx, a.store.db, session.EmployeeID, keyID, from, to, a.selfKeyTokenSummaryAfterOwnership, a.selfKeyTokenSummaryCommit)
	if errors.Is(err, sql.ErrNoRows) {
		selfError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, response)
}
