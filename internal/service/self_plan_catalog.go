// Independently authored for docs/employee-self-plan-catalog-contract.md.
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
	selfPlanCatalogTimeout       = 5 * time.Second
	selfPlanCatalogCursorAge     = 15 * time.Minute
	selfPlanCatalogQueryMax      = 2048
	selfPlanCatalogCursorMax     = 1024
	selfPlanCatalogCursorPurpose = "cpacloud/self-plan-catalog-cursor/v1"
)

var errSelfPlanCatalogCursor = errors.New("invalid self plan catalog cursor")

type selfPlanCatalogParams struct {
	currency string
	limit    int
	cursor   string
}

type selfPlanCatalogCursor struct {
	Version    int    `json:"v"`
	EmployeeID string `json:"e"`
	Session    string `json:"s"`
	Currency   string `json:"c"`
	IssuedAt   string `json:"t"`
	Limit      int    `json:"l"`
	LastID     string `json:"i"`
}

func validSelfPlanCatalogCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, ch := range []byte(value) {
		if ch < 'A' || ch > 'Z' {
			return false
		}
	}
	return true
}

func parseSelfPlanCatalogRequest(raw string) (selfPlanCatalogParams, bool) {
	if raw == "" || len(raw) > selfPlanCatalogQueryMax || strings.HasPrefix(raw, "&") || strings.HasSuffix(raw, "&") || strings.Contains(raw, "&&") {
		return selfPlanCatalogParams{}, false
	}
	values, err := url.ParseQuery(raw)
	if err != nil || len(values) < 1 || len(values) > 3 {
		return selfPlanCatalogParams{}, false
	}
	for key, items := range values {
		if (key != "currency" && key != "limit" && key != "cursor") || len(items) != 1 || items[0] == "" {
			return selfPlanCatalogParams{}, false
		}
	}
	currencies := values["currency"]
	if len(currencies) != 1 || !validSelfPlanCatalogCurrency(currencies[0]) {
		return selfPlanCatalogParams{}, false
	}
	request := selfPlanCatalogParams{currency: currencies[0], limit: 20}
	if items, exists := values["limit"]; exists {
		value, err := strconv.Atoi(items[0])
		if err != nil || value < 1 || value > 50 || strconv.Itoa(value) != items[0] {
			return selfPlanCatalogParams{}, false
		}
		request.limit = value
	}
	if items, exists := values["cursor"]; exists {
		if _, hasLimit := values["limit"]; !hasLimit || len(items[0]) > selfPlanCatalogCursorMax {
			return selfPlanCatalogParams{}, false
		}
		request.cursor = items[0]
	}
	return request, true
}

func (a *App) selfPlanCatalog(w http.ResponseWriter, r *http.Request, session selfSession) {
	request, ok := parseSelfPlanCatalogRequest(r.URL.RawQuery)
	if !ok || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if a == nil || a.secrets == nil || a.secrets.aead == nil || a.store == nil || a.store.db == nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	now := time.Now().UTC()
	issuedAt, afterID := now.Format(time.RFC3339Nano), ""
	if request.cursor != "" {
		cursor, err := a.decodeSelfPlanCatalogCursor(request.cursor, session, request, now)
		if err != nil {
			selfError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		issuedAt, afterID = cursor.IssuedAt, cursor.LastID
	}
	ctx, cancel := context.WithTimeout(r.Context(), selfPlanCatalogTimeout)
	defer cancel()
	page, err := financial.NewCommercial(a.store.db).ReadEmployeePlanCatalog(ctx, request.currency, afterID, request.limit)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	items := make([]map[string]any, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, map[string]any{
			"plan_id": item.PlanID, "name": item.Name, "interval": item.Interval,
			"price_micro": item.PriceMicro, "credit_micro": item.CreditMicro, "revision": item.Revision,
		})
	}
	var next *string
	if page.NextPosition != "" {
		encoded, err := a.encodeSelfPlanCatalogCursor(selfPlanCatalogCursor{
			Version: 1, EmployeeID: session.EmployeeID, Session: session.Selector,
			Currency: request.currency, IssuedAt: issuedAt, Limit: request.limit, LastID: page.NextPosition,
		})
		if err != nil {
			selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		next = &encoded
	}
	if ctx.Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"currency": request.currency, "available": page.Available, "items": items, "next_cursor": next,
	})
}

func (a *App) encodeSelfPlanCatalogCursor(cursor selfPlanCatalogCursor) (string, error) {
	plain, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.secrets.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := a.secrets.aead.Seal(nil, nonce, plain, []byte(selfPlanCatalogCursorPurpose))
	encoded := base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(encoded) > selfPlanCatalogCursorMax {
		return "", errSelfPlanCatalogCursor
	}
	return encoded, nil
}

func (a *App) decodeSelfPlanCatalogCursor(encoded string, session selfSession, request selfPlanCatalogParams, now time.Time) (selfPlanCatalogCursor, error) {
	invalid := func() (selfPlanCatalogCursor, error) { return selfPlanCatalogCursor{}, errSelfPlanCatalogCursor }
	if encoded == "" || len(encoded) > selfPlanCatalogCursorMax {
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
	plain, err := a.secrets.aead.Open(nil, data[:nonceSize], data[nonceSize:], []byte(selfPlanCatalogCursorPurpose))
	if err != nil {
		return invalid()
	}
	decoder := json.NewDecoder(bytes.NewReader(plain))
	decoder.DisallowUnknownFields()
	var cursor selfPlanCatalogCursor
	if err := decoder.Decode(&cursor); err != nil {
		return invalid()
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid()
	}
	canonical, err := json.Marshal(cursor)
	if err != nil || !bytes.Equal(canonical, plain) || cursor.Version != 1 ||
		cursor.EmployeeID != session.EmployeeID || cursor.Session != session.Selector || cursor.Currency != request.currency ||
		cursor.Limit != request.limit || !validIdentifier(cursor.LastID, 256) {
		return invalid()
	}
	issued, err := time.Parse(time.RFC3339Nano, cursor.IssuedAt)
	if err != nil || issued.UTC().Format(time.RFC3339Nano) != cursor.IssuedAt ||
		issued.After(now.Add(time.Second)) || !now.Before(issued.Add(selfPlanCatalogCursorAge)) {
		return invalid()
	}
	return cursor, nil
}
