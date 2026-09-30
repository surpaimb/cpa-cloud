// Independently authored for docs/employee-self-request-history-contract.md.
package service

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	selfRequestDefaultLimit  = 20
	selfRequestMaximumLimit  = 50
	selfRequestCursorMaximum = 1400
	selfRequestCursorPurpose = "employee-self-request-history-cursor/v1"
	selfRequestTimeLayout    = "2006-01-02T15:04:05.000000000Z"
	// Ledger writers persist UTC RFC3339Nano, whose fractional part has variable
	// width. Padding it to nine digits makes SQLite byte ordering temporal.
	selfRequestStartedKeySQL = `(substr(r.started_at,1,19)||'.'||substr((CASE WHEN substr(r.started_at,20,1)='.' THEN substr(r.started_at,21,length(r.started_at)-21) ELSE '' END)||'000000000',1,9)||'Z')`
)

type selfRequestItem struct {
	ID         string  `json:"id"`
	KeyID      string  `json:"key_id"`
	ModelID    string  `json:"model_id"`
	Status     string  `json:"status"`
	StartedAt  string  `json:"started_at"`
	FinishedAt *string `json:"finished_at"`
}

type selfRequestPage struct {
	Items      []selfRequestItem `json:"items"`
	NextCursor *string           `json:"next_cursor"`
}

type selfRequestCursor struct {
	Version  int    `json:"v"`
	Employee string `json:"e"`
	From     string `json:"f"`
	To       string `json:"t"`
	LastTime string `json:"s"`
	LastID   string `json:"i"`
}

type selfRequestQuery struct {
	Limit    int
	From     time.Time
	To       time.Time
	LastTime string
	LastID   string
}

func selfRequestSortTime(t time.Time) string { return t.UTC().Format(selfRequestTimeLayout) }

func selfRequestWholeSecond(value string) (time.Time, error) {
	if len(value) != 20 && len(value) != 25 {
		return time.Time{}, errors.New("invalid window time")
	}
	if value[19] != 'Z' && value[19] != '+' && value[19] != '-' {
		return time.Time{}, errors.New("invalid window time")
	}
	t, err := time.Parse(time.RFC3339, value)
	if err != nil || t.Nanosecond() != 0 {
		return time.Time{}, errors.New("invalid window time")
	}
	return t.UTC(), nil
}

func selfRequestWindow(from, to time.Time) bool {
	return from.Before(to) && to.Sub(from) <= 31*24*time.Hour
}

func selfRequestID(value string) bool {
	if len(value) == 0 || len(value) > 256 {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-_.:/@+", c) {
			continue
		}
		return false
	}
	return true
}

func selfRequestModelID(value string) bool {
	if value == "" || len(value) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return false
		}
	}
	return true
}

func selfRequestStatus(value string) bool {
	switch value {
	case "pending", "succeeded", "failed", "cancelled", "interrupted":
		return true
	default:
		return false
	}
}

func selfRequestStoredTime(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, value)
	return t, err == nil && t.UTC().Format(time.RFC3339Nano) == value
}

func (a *App) encodeSelfRequestCursor(c selfRequestCursor) (string, error) {
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sealed := append(payload, a.secrets.digest(selfRequestCursorPurpose, string(payload))...)
	value := "v1." + base64.RawURLEncoding.EncodeToString(sealed)
	if len(value) > selfRequestCursorMaximum {
		return "", errors.New("cursor too large")
	}
	return value, nil
}

func (a *App) decodeSelfRequestCursor(value, employeeID string) (selfRequestCursor, error) {
	invalid := func() (selfRequestCursor, error) { return selfRequestCursor{}, errors.New("invalid cursor") }
	if !strings.HasPrefix(value, "v1.") || len(value) > selfRequestCursorMaximum {
		return invalid()
	}
	encoded := strings.TrimPrefix(value, "v1.")
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(raw) != encoded || len(raw) <= 32 {
		return invalid()
	}
	payload, provided := raw[:len(raw)-32], raw[len(raw)-32:]
	if subtle.ConstantTimeCompare(a.secrets.digest(selfRequestCursorPurpose, string(payload)), provided) != 1 {
		return invalid()
	}
	var c selfRequestCursor
	if err := json.Unmarshal(payload, &c); err != nil {
		return invalid()
	}
	canonical, err := json.Marshal(c)
	if err != nil || !bytes.Equal(payload, canonical) || c.Version != 1 || c.Employee != employeeID || !selfRequestID(c.LastID) {
		return invalid()
	}
	from, err := selfRequestWholeSecond(c.From)
	if err != nil {
		return invalid()
	}
	to, err := selfRequestWholeSecond(c.To)
	if err != nil || !selfRequestWindow(from, to) || c.From != from.Format(time.RFC3339) || c.To != to.Format(time.RFC3339) {
		return invalid()
	}
	last, err := time.Parse(selfRequestTimeLayout, c.LastTime)
	if err != nil || selfRequestSortTime(last) != c.LastTime || last.Before(from) || !last.Before(to) {
		return invalid()
	}
	return c, nil
}

func (a *App) parseSelfRequestQuery(raw, employeeID string, now time.Time) (selfRequestQuery, error) {
	invalid := func() (selfRequestQuery, error) { return selfRequestQuery{}, errors.New("invalid query") }
	if len(raw) > 2400 {
		return invalid()
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return invalid()
	}
	query := selfRequestQuery{Limit: selfRequestDefaultLimit}
	var fromRaw, toRaw, cursorRaw string
	for key, list := range values {
		if len(list) != 1 || list[0] == "" {
			return invalid()
		}
		switch key {
		case "limit":
			v := list[0]
			if len(v) > 2 || v[0] < '1' || v[0] > '9' {
				return invalid()
			}
			for _, c := range v {
				if c < '0' || c > '9' {
					return invalid()
				}
			}
			query.Limit, err = strconv.Atoi(v)
			if err != nil || query.Limit > selfRequestMaximumLimit {
				return invalid()
			}
		case "from":
			fromRaw = list[0]
		case "to":
			toRaw = list[0]
		case "cursor":
			cursorRaw = list[0]
		default:
			return invalid()
		}
	}
	if (fromRaw == "") != (toRaw == "") {
		return invalid()
	}
	if fromRaw != "" {
		query.From, err = selfRequestWholeSecond(fromRaw)
		if err != nil {
			return invalid()
		}
		query.To, err = selfRequestWholeSecond(toRaw)
		if err != nil || !selfRequestWindow(query.From, query.To) {
			return invalid()
		}
	}
	if cursorRaw != "" {
		c, err := a.decodeSelfRequestCursor(cursorRaw, employeeID)
		if err != nil {
			return invalid()
		}
		cursorFrom, _ := selfRequestWholeSecond(c.From)
		cursorTo, _ := selfRequestWholeSecond(c.To)
		if fromRaw != "" && (!query.From.Equal(cursorFrom) || !query.To.Equal(cursorTo)) {
			return invalid()
		}
		query.From, query.To, query.LastTime, query.LastID = cursorFrom, cursorTo, c.LastTime, c.LastID
	} else if fromRaw == "" {
		query.To = now.UTC().Truncate(time.Second).Add(time.Second)
		query.From = query.To.Add(-24 * time.Hour)
	}
	return query, nil
}

func (a *App) readSelfRequestPage(ctx context.Context, employeeID string, q selfRequestQuery) (selfRequestPage, error) {
	page := selfRequestPage{Items: make([]selfRequestItem, 0, q.Limit)}
	statement := `SELECT r.id,r.key_id,r.model_id,r.status,r.started_at,r.finished_at,typeof(r.id),typeof(r.key_id),typeof(r.model_id),typeof(r.status),typeof(r.started_at),typeof(r.finished_at)
		FROM accounting_requests r WHERE r.employee_id=? AND ` + selfRequestStartedKeySQL + `>=? AND ` + selfRequestStartedKeySQL + `<?`
	args := []any{employeeID, selfRequestSortTime(q.From), selfRequestSortTime(q.To)}
	if q.LastID != "" {
		statement += ` AND (` + selfRequestStartedKeySQL + `<? OR (` + selfRequestStartedKeySQL + `=? AND r.id COLLATE BINARY<?))`
		args = append(args, q.LastTime, q.LastTime, q.LastID)
	}
	statement += ` ORDER BY ` + selfRequestStartedKeySQL + ` DESC,r.id COLLATE BINARY DESC LIMIT ?`
	args = append(args, q.Limit+1)
	rows, err := a.store.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return selfRequestPage{}, err
	}
	for rows.Next() {
		var item selfRequestItem
		var started string
		var finished sql.NullString
		var idType, keyType, modelType, statusType, startType, finishType string
		if err := rows.Scan(&item.ID, &item.KeyID, &item.ModelID, &item.Status, &started, &finished, &idType, &keyType, &modelType, &statusType, &startType, &finishType); err != nil {
			_ = rows.Close()
			return selfRequestPage{}, err
		}
		if idType != "text" || keyType != "text" || modelType != "text" || statusType != "text" || startType != "text" || finishType != "null" && finishType != "text" ||
			!selfRequestID(item.ID) || !selfRequestID(item.KeyID) || !selfRequestModelID(item.ModelID) || !selfRequestStatus(item.Status) {
			_ = rows.Close()
			return selfRequestPage{}, errors.New("invalid request row")
		}
		start, ok := selfRequestStoredTime(started)
		if !ok || start.Before(q.From) || !start.Before(q.To) {
			_ = rows.Close()
			return selfRequestPage{}, errors.New("invalid start time")
		}
		item.StartedAt = started
		if finished.Valid {
			end, ok := selfRequestStoredTime(finished.String)
			if !ok || end.Before(start) || item.Status == "pending" {
				_ = rows.Close()
				return selfRequestPage{}, errors.New("invalid finish time")
			}
			item.FinishedAt = &finished.String
		} else if item.Status != "pending" {
			_ = rows.Close()
			return selfRequestPage{}, errors.New("missing finish time")
		}
		page.Items = append(page.Items, item)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil || ctx.Err() != nil {
		return selfRequestPage{}, errors.New("request history read failed")
	}
	if len(page.Items) > q.Limit {
		page.Items = page.Items[:q.Limit]
		last := page.Items[len(page.Items)-1]
		lastTime, _ := selfRequestStoredTime(last.StartedAt)
		cursor, err := a.encodeSelfRequestCursor(selfRequestCursor{Version: 1, Employee: employeeID, From: q.From.Format(time.RFC3339), To: q.To.Format(time.RFC3339), LastTime: selfRequestSortTime(lastTime), LastID: last.ID})
		if err != nil {
			return selfRequestPage{}, err
		}
		page.NextCursor = &cursor
	}
	return page, nil
}

func (a *App) selfRequestHistory(w http.ResponseWriter, r *http.Request, session selfSession) {
	q, err := a.parseSelfRequestQuery(r.URL.RawQuery, session.EmployeeID, time.Now())
	if err != nil || r.ContentLength != 0 || len(r.TransferEncoding) > 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	page, err := a.readSelfRequestPage(r.Context(), session.EmployeeID, q)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, page)
}
