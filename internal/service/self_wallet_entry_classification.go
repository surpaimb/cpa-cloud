// Independently authored for docs/employee-self-wallet-entry-classification-contract.md.
package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"cpacloud.local/server/internal/financial"
)

const (
	selfClassificationPath          = "/self/api/v1/billing/entry-classifications"
	selfClassificationCursorPurpose = "cpacloud/self-wallet-entry-classifications-cursor/v1"
)

var errSelfClassificationCursor = errors.New("invalid self wallet entry classification cursor")

func (a *App) selfWalletEntryClassifications(w http.ResponseWriter, r *http.Request, session selfSession) {
	request, ok := parseSelfWalletActivityRequest(r.URL.RawQuery)
	if r.Method != http.MethodGet || r.URL.Path != selfClassificationPath || r.URL.EscapedPath() != selfClassificationPath ||
		!ok || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
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
		cursor, err := a.decodeSelfClassificationCursor(request.cursor, session, request, now)
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
	page, err := financial.NewLedger(a.store.db).ReadEmployeeClassifications(ctx, financial.EmployeeActivityQuery{
		EmployeeID: session.EmployeeID, Currency: request.currency, WindowStart: windowStart, WindowEnd: windowEnd,
		Limit: request.limit, BeforeTime: position.Time, BeforeID: position.ID,
	})
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	items := make([]map[string]string, 0, len(page.Items))
	for _, entry := range page.Items {
		items = append(items, map[string]string{"occurred_at": entry.OccurredAt, "delta_micro": decimal(entry.DeltaMicro), "entry_kind": string(entry.EntryKind)})
	}
	var next *string
	if page.NextPosition != nil {
		encoded, err := a.encodeSelfClassificationCursor(selfWalletActivityCursor{
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
	current, err := a.selfClassificationSessionCurrent(ctx, session)
	if err != nil || ctx.Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if !current {
		selfError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"currency": request.currency, "has_account": page.HasAccount,
		"window_start": windowStart.Format(time.RFC3339), "window_end": windowEnd.Format(time.RFC3339),
		"items": items, "next_cursor": next,
	})
}

func (a *App) selfClassificationSessionCurrent(ctx context.Context, session selfSession) (bool, error) {
	var employeeID, expires, status string
	err := a.store.db.QueryRowContext(ctx, `SELECT s.employee_id,s.expires_at,e.status FROM employee_self_sessions s JOIN employees e ON e.id=s.employee_id WHERE s.selector=?`, session.Selector).
		Scan(&employeeID, &expires, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expiry, err := parseTime(expires)
	if err != nil {
		return false, err
	}
	return employeeID == session.EmployeeID && status == "active" && time.Now().UTC().Before(expiry), nil
}

func (a *App) encodeSelfClassificationCursor(cursor selfWalletActivityCursor) (string, error) {
	plain, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.secrets.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := a.secrets.aead.Seal(nil, nonce, plain, []byte(selfClassificationCursorPurpose))
	encoded := base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(encoded) > selfWalletActivityCursorMax {
		return "", errSelfClassificationCursor
	}
	return encoded, nil
}

func (a *App) decodeSelfClassificationCursor(encoded string, session selfSession, request selfWalletActivityRequest, now time.Time) (selfWalletActivityCursor, error) {
	invalid := func() (selfWalletActivityCursor, error) {
		return selfWalletActivityCursor{}, errSelfClassificationCursor
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
	plain, err := a.secrets.aead.Open(nil, data[:nonceSize], data[nonceSize:], []byte(selfClassificationCursorPurpose))
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
	if err != nil || !bytes.Equal(canonical, plain) || cursor.Version != 1 || cursor.EmployeeID != session.EmployeeID ||
		cursor.Session != session.Selector || cursor.Currency != request.currency || cursor.Limit != request.limit ||
		!validIdentifier(cursor.LastID, 256) || cursor.LastTime == "" {
		return invalid()
	}
	start, errStart := time.Parse(time.RFC3339, cursor.WindowStart)
	end, errEnd := time.Parse(time.RFC3339, cursor.WindowEnd)
	last, errLast := time.Parse(time.RFC3339Nano, cursor.LastTime)
	if errStart != nil || errEnd != nil || errLast != nil || start.UTC().Format(time.RFC3339) != cursor.WindowStart ||
		end.UTC().Format(time.RFC3339) != cursor.WindowEnd || last.UTC().Format(time.RFC3339Nano) != cursor.LastTime ||
		end.Sub(start) != selfWalletActivityWindow || !last.Before(end) || last.Before(start) ||
		end.After(now.Add(time.Second)) || !now.Before(end.Add(selfWalletActivityCursorAge)) {
		return invalid()
	}
	return cursor, nil
}
