// Independently authored for docs/employee-self-subscription-status-contract.md.
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
	selfSubscriptionTimeout       = 5 * time.Second
	selfSubscriptionCursorAge     = 15 * time.Minute
	selfSubscriptionQueryMax      = 2048
	selfSubscriptionCursorMax     = 1024
	selfSubscriptionCursorPurpose = "cpacloud/self-subscription-status-cursor/v1"
)

var errSelfSubscriptionCursor = errors.New("invalid self subscription cursor")

type selfSubscriptionParams struct {
	limit  int
	cursor string
}

type selfSubscriptionCursor struct {
	Version    int    `json:"v"`
	EmployeeID string `json:"e"`
	Session    string `json:"s"`
	IssuedAt   string `json:"t"`
	Limit      int    `json:"l"`
	LastID     string `json:"i"`
}

func parseSelfSubscriptionRequest(raw string) (selfSubscriptionParams, bool) {
	if len(raw) > selfSubscriptionQueryMax || strings.HasPrefix(raw, "&") || strings.HasSuffix(raw, "&") || strings.Contains(raw, "&&") {
		return selfSubscriptionParams{}, false
	}
	request := selfSubscriptionParams{limit: 20}
	if raw == "" {
		return request, true
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) < 1 || len(values) > 2 {
		return selfSubscriptionParams{}, false
	}
	for key, items := range values {
		if (key != "limit" && key != "cursor") || len(items) != 1 || items[0] == "" {
			return selfSubscriptionParams{}, false
		}
	}
	if items, exists := values["limit"]; exists {
		value, err := strconv.Atoi(items[0])
		if err != nil || value < 1 || value > 50 || strconv.Itoa(value) != items[0] {
			return selfSubscriptionParams{}, false
		}
		request.limit = value
	}
	if items, exists := values["cursor"]; exists {
		if _, hasLimit := values["limit"]; !hasLimit || len(items[0]) > selfSubscriptionCursorMax {
			return selfSubscriptionParams{}, false
		}
		request.cursor = items[0]
	}
	return request, true
}

func (a *App) selfSubscriptionStatus(w http.ResponseWriter, r *http.Request, session selfSession) {
	request, ok := parseSelfSubscriptionRequest(r.URL.RawQuery)
	if !ok || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if a == nil || a.secrets == nil || a.secrets.aead == nil || a.store == nil || a.store.db == nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	now := time.Now().UTC()
	issuedAt := now.Format(time.RFC3339Nano)
	beforeID := ""
	if request.cursor != "" {
		cursor, err := a.decodeSelfSubscriptionCursor(request.cursor, session, request, now)
		if err != nil {
			selfError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		issuedAt, beforeID = cursor.IssuedAt, cursor.LastID
	}
	ctx, cancel := context.WithTimeout(r.Context(), selfSubscriptionTimeout)
	defer cancel()
	page, err := financial.NewCommercial(a.store.db).ReadEmployeeSubscriptions(ctx, session.EmployeeID, beforeID, request.limit, now)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, map[string]any{
			"subscription_id": item.SubscriptionID, "interval": item.Interval, "status": item.Status,
			"started_at": item.StartedAt, "period_end_at": item.PeriodEndAt, "cancelled_at": item.CancelledAt,
		})
	}
	var next *string
	if page.NextPosition != "" {
		encoded, err := a.encodeSelfSubscriptionCursor(selfSubscriptionCursor{
			Version: 1, EmployeeID: session.EmployeeID, Session: session.Selector,
			IssuedAt: issuedAt, Limit: request.limit, LastID: page.NextPosition,
		})
		if err != nil {
			selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		next = &encoded
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (a *App) encodeSelfSubscriptionCursor(cursor selfSubscriptionCursor) (string, error) {
	plain, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.secrets.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := a.secrets.aead.Seal(nil, nonce, plain, []byte(selfSubscriptionCursorPurpose))
	encoded := base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(encoded) > selfSubscriptionCursorMax {
		return "", errors.New("self subscription cursor exceeds limit")
	}
	return encoded, nil
}

func (a *App) decodeSelfSubscriptionCursor(encoded string, session selfSession, request selfSubscriptionParams, now time.Time) (selfSubscriptionCursor, error) {
	invalid := func() (selfSubscriptionCursor, error) { return selfSubscriptionCursor{}, errSelfSubscriptionCursor }
	if encoded == "" || len(encoded) > selfSubscriptionCursorMax {
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
	plain, err := a.secrets.aead.Open(nil, data[:nonceSize], data[nonceSize:], []byte(selfSubscriptionCursorPurpose))
	if err != nil {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	var cursor selfSubscriptionCursor
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
		cursor.Limit != request.limit || !validIdentifier(cursor.LastID, 256) {
		return invalid()
	}
	issued, err := time.Parse(time.RFC3339Nano, cursor.IssuedAt)
	if err != nil || issued.UTC().Format(time.RFC3339Nano) != cursor.IssuedAt ||
		issued.After(now.Add(time.Second)) || !now.Before(issued.Add(selfSubscriptionCursorAge)) {
		return invalid()
	}
	return cursor, nil
}
