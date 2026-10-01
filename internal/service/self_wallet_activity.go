// Independently authored for docs/employee-self-wallet-activity-contract.md.
package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cpacloud.local/server/internal/financial"
)

const (
	selfWalletActivityTimeout       = 5 * time.Second
	selfWalletActivityWindow        = 31 * 24 * time.Hour
	selfWalletActivityCursorAge     = 15 * time.Minute
	selfWalletActivityQueryMax      = 2048
	selfWalletActivityCursorMax     = 1024
	selfWalletActivityCursorPurpose = "cpacloud/self-wallet-activity-cursor/v1"
)

var errSelfWalletActivityCursor = errors.New("invalid self wallet activity cursor")

type selfWalletActivityRequest struct {
	currency string
	limit    int
	cursor   string
}

type selfWalletActivityCursor struct {
	Version     int    `json:"v"`
	EmployeeID  string `json:"e"`
	Session     string `json:"s"`
	Currency    string `json:"c"`
	WindowStart string `json:"ws"`
	WindowEnd   string `json:"we"`
	Limit       int    `json:"l"`
	LastTime    string `json:"t"`
	LastID      string `json:"i"`
}

func parseSelfWalletActivityRequest(raw string) (selfWalletActivityRequest, bool) {
	if raw == "" || len(raw) > selfWalletActivityQueryMax || strings.HasPrefix(raw, "&") || strings.HasSuffix(raw, "&") || strings.Contains(raw, "&&") {
		return selfWalletActivityRequest{}, false
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) < 1 || len(values) > 3 {
		return selfWalletActivityRequest{}, false
	}
	for key, items := range values {
		if (key != "currency" && key != "limit" && key != "cursor") || len(items) != 1 {
			return selfWalletActivityRequest{}, false
		}
	}
	currencies := values["currency"]
	if len(currencies) != 1 || !selfWalletActivityCurrency(currencies[0]) {
		return selfWalletActivityRequest{}, false
	}
	request := selfWalletActivityRequest{currency: currencies[0], limit: 20}
	if items, exists := values["limit"]; exists {
		value, err := strconv.Atoi(items[0])
		if err != nil || value < 1 || value > 50 || strconv.Itoa(value) != items[0] {
			return selfWalletActivityRequest{}, false
		}
		request.limit = value
	}
	if items, exists := values["cursor"]; exists {
		if _, hasLimit := values["limit"]; !hasLimit || items[0] == "" || len(items[0]) > selfWalletActivityCursorMax {
			return selfWalletActivityRequest{}, false
		}
		request.cursor = items[0]
	}
	return request, true
}

func selfWalletActivityCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if value[i] < 'A' || value[i] > 'Z' {
			return false
		}
	}
	return true
}

func (a *App) selfWalletActivity(w http.ResponseWriter, r *http.Request, session selfSession) {
	request, ok := parseSelfWalletActivityRequest(r.URL.RawQuery)
	if !ok || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if a == nil || a.secrets == nil || a.secrets.aead == nil || a.store == nil || a.store.db == nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	now := time.Now().UTC()
	windowEnd := now.Truncate(time.Second).Add(time.Second)
	windowStart := windowEnd.Add(-selfWalletActivityWindow)
	position := financial.EmployeeActivityPosition{}
	if request.cursor != "" {
		cursor, err := a.decodeSelfWalletActivityCursor(request.cursor, session, request, now)
		if err != nil {
			selfError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		var startErr, endErr error
		windowStart, startErr = time.Parse(time.RFC3339, cursor.WindowStart)
		windowEnd, endErr = time.Parse(time.RFC3339, cursor.WindowEnd)
		if startErr != nil || endErr != nil {
			selfError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		position = financial.EmployeeActivityPosition{Time: cursor.LastTime, ID: cursor.LastID}
	}
	ctx, cancel := context.WithTimeout(r.Context(), selfWalletActivityTimeout)
	defer cancel()
	page, err := financial.NewLedger(a.store.db).ReadEmployeeActivity(ctx, financial.EmployeeActivityQuery{
		EmployeeID: session.EmployeeID, Currency: request.currency, WindowStart: windowStart, WindowEnd: windowEnd,
		Limit: request.limit, BeforeTime: position.Time, BeforeID: position.ID,
	})
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	items := make([]map[string]string, 0, len(page.Items))
	for _, entry := range page.Items {
		items = append(items, map[string]string{"occurred_at": entry.OccurredAt, "delta_micro": decimal(entry.DeltaMicro)})
	}
	var next *string
	if page.NextPosition != nil {
		encoded, err := a.encodeSelfWalletActivityCursor(selfWalletActivityCursor{
			Version: 1, EmployeeID: session.EmployeeID, Session: session.Selector,
			Currency: request.currency, WindowStart: windowStart.Format(time.RFC3339), WindowEnd: windowEnd.Format(time.RFC3339),
			Limit: request.limit, LastTime: page.NextPosition.Time, LastID: page.NextPosition.ID,
		})
		if err != nil {
			selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		next = &encoded
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"currency": request.currency, "has_account": page.HasAccount,
		"window_start": windowStart.Format(time.RFC3339), "window_end": windowEnd.Format(time.RFC3339),
		"items": items, "next_cursor": next,
	})
}

func (a *App) encodeSelfWalletActivityCursor(cursor selfWalletActivityCursor) (string, error) {
	plain, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.secrets.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := a.secrets.aead.Seal(nil, nonce, plain, []byte(selfWalletActivityCursorPurpose))
	encoded := base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(encoded) > selfWalletActivityCursorMax {
		return "", errors.New("self wallet activity cursor exceeds limit")
	}
	return encoded, nil
}

func (a *App) decodeSelfWalletActivityCursor(encoded string, session selfSession, request selfWalletActivityRequest, now time.Time) (selfWalletActivityCursor, error) {
	invalid := func() (selfWalletActivityCursor, error) {
		return selfWalletActivityCursor{}, errSelfWalletActivityCursor
	}
	if encoded == "" || len(encoded) > selfWalletActivityCursorMax {
		return invalid()
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != encoded {
		return invalid()
	}
	nonceSize := a.secrets.aead.NonceSize()
	if len(data) < nonceSize+a.secrets.aead.Overhead() {
		return invalid()
	}
	plain, err := a.secrets.aead.Open(nil, data[:nonceSize], data[nonceSize:], []byte(selfWalletActivityCursorPurpose))
	if err != nil {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	var cursor selfWalletActivityCursor
	if err := decoder.Decode(&cursor); err != nil {
		return invalid()
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid()
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(canonical, plain) || cursor.Version != 1 ||
		cursor.EmployeeID != session.EmployeeID || cursor.Session != session.Selector ||
		cursor.Currency != request.currency || cursor.Limit != request.limit || !validIdentifier(cursor.LastID, 256) || cursor.LastTime == "" {
		return invalid()
	}
	start, errStart := time.Parse(time.RFC3339, cursor.WindowStart)
	end, errEnd := time.Parse(time.RFC3339, cursor.WindowEnd)
	last, errLast := time.Parse(time.RFC3339Nano, cursor.LastTime)
	if errStart != nil || errEnd != nil || errLast != nil ||
		start.UTC().Format(time.RFC3339) != cursor.WindowStart || end.UTC().Format(time.RFC3339) != cursor.WindowEnd ||
		last.UTC().Format(time.RFC3339Nano) != cursor.LastTime || end.Sub(start) != selfWalletActivityWindow ||
		!last.Before(end) || last.Before(start) || end.After(now.Add(time.Second)) || !now.Before(end.Add(selfWalletActivityCursorAge)) {
		return invalid()
	}
	return cursor, nil
}
