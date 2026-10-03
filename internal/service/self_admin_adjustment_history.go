// Independently authored for docs/employee-self-admin-adjustment-history-contract.md.
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
	"path"
	"strconv"
	"strings"
	"time"

	"cpacloud.local/server/internal/financial"
)

const (
	selfAdminAdjustmentPath          = "/self/api/v1/billing/admin-adjustments"
	selfAdminAdjustmentCursorPurpose = "cpacloud/self-admin-adjustment-history-cursor/v1"
)

var errSelfAdminAdjustmentCursor = errors.New("invalid self admin adjustment cursor")

func selfAdminAdjustmentShapedPath(decoded string) bool {
	for candidate := strings.ToLower(decoded); ; {
		candidate = strings.ReplaceAll(candidate, `\`, "/")
		clean := path.Clean(candidate)
		if clean == selfAdminAdjustmentPath || strings.HasPrefix(clean, selfAdminAdjustmentPath+"/") {
			return true
		}
		parts := make([]string, 0, 10)
		for _, segment := range strings.Split(candidate, "/") {
			if segment != "" && segment != "." {
				parts = append(parts, segment)
			}
		}
		lexical := "/" + strings.Join(parts, "/")
		if lexical == selfAdminAdjustmentPath || strings.HasPrefix(lexical, selfAdminAdjustmentPath+"/") {
			return true
		}
		unescaped, err := url.PathUnescape(candidate)
		if err != nil || unescaped == candidate {
			return false
		}
		candidate = unescaped
	}
}

func (a *App) selfAdminAdjustmentRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !selfAdminAdjustmentShapedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.cfg.EmployeeSelfAdminAdjustmentHistoryEnabled {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != selfAdminAdjustmentPath || r.URL.EscapedPath() != selfAdminAdjustmentPath {
			a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
				selfError(w, http.StatusBadRequest, "invalid_request")
			}, false)(w, r)
			return
		}
		if r.Method != http.MethodGet {
			a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
				w.Header().Set("Allow", http.MethodGet)
				selfError(w, http.StatusMethodNotAllowed, "method_not_allowed")
			}, false)(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func parseSelfAdminAdjustmentRequest(raw string) (selfWalletActivityRequest, bool) {
	if raw == "" || len(raw) > selfWalletActivityQueryMax || strings.Contains(raw, "%") {
		return selfWalletActivityRequest{}, false
	}
	request := selfWalletActivityRequest{limit: 20}
	seen := map[string]bool{}
	for _, pair := range strings.Split(raw, "&") {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || value == "" || seen[key] || strings.Contains(value, "=") {
			return selfWalletActivityRequest{}, false
		}
		seen[key] = true
		switch key {
		case "currency":
			if !selfWalletActivityCurrency(value) {
				return selfWalletActivityRequest{}, false
			}
			request.currency = value
		case "limit":
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 50 || strconv.Itoa(n) != value {
				return selfWalletActivityRequest{}, false
			}
			request.limit = n
		case "cursor":
			if len(value) > selfWalletActivityCursorMax {
				return selfWalletActivityRequest{}, false
			}
			for i := 0; i < len(value); i++ {
				c := value[i]
				switch {
				case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '-', c == '_':
				default:
					return selfWalletActivityRequest{}, false
				}
			}
			request.cursor = value
		default:
			return selfWalletActivityRequest{}, false
		}
	}
	return request, seen["currency"] && (!seen["cursor"] || seen["limit"])
}

func (a *App) selfAdminAdjustmentHistory(w http.ResponseWriter, r *http.Request, session selfSession) {
	request, ok := parseSelfAdminAdjustmentRequest(r.URL.RawQuery)
	if r.Method != http.MethodGet || r.URL.Path != selfAdminAdjustmentPath || r.URL.EscapedPath() != selfAdminAdjustmentPath ||
		r.URL.ForceQuery || !ok || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if a.secrets == nil || a.secrets.aead == nil || a.store == nil || a.store.db == nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	now := time.Now().UTC()
	end := now.Truncate(time.Second).Add(time.Second)
	start := end.Add(-selfWalletActivityWindow)
	position := financial.EmployeeActivityPosition{}
	if request.cursor != "" {
		cursor, err := a.decodeSelfAdminAdjustmentCursor(request.cursor, session, request, now)
		if err != nil {
			selfError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		start, _ = time.Parse(time.RFC3339, cursor.WindowStart)
		end, _ = time.Parse(time.RFC3339, cursor.WindowEnd)
		position = financial.EmployeeActivityPosition{Time: cursor.LastTime, ID: cursor.LastID}
	}
	ctx, cancel := context.WithTimeout(r.Context(), selfWalletActivityTimeout)
	defer cancel()
	page, err := financial.NewLedger(a.store.db).ReadEmployeeAdminAdjustments(ctx, financial.EmployeeActivityQuery{
		EmployeeID: session.EmployeeID, Currency: request.currency, WindowStart: start, WindowEnd: end,
		Limit: request.limit, BeforeTime: position.Time, BeforeID: position.ID,
	})
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	items := make([]map[string]string, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, map[string]string{"occurred_at": item.OccurredAt, "delta_micro": decimal(item.DeltaMicro)})
	}
	var next *string
	if page.NextPosition != nil {
		encoded, err := a.encodeSelfAdminAdjustmentCursor(selfWalletActivityCursor{
			Version: 1, EmployeeID: session.EmployeeID, Session: session.Selector, Currency: request.currency,
			WindowStart: start.Format(time.RFC3339), WindowEnd: end.Format(time.RFC3339), Limit: request.limit,
			LastTime: page.NextPosition.Time, LastID: page.NextPosition.ID,
		})
		if err != nil {
			selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		next = &encoded
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	current, err := a.selfRedemptionHistorySessionCurrent(ctx, session)
	if err != nil || ctx.Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if !current {
		selfError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	if a.selfAdminAdjustmentBeforeWrite != nil {
		a.selfAdminAdjustmentBeforeWrite()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"currency": request.currency, "has_account": page.HasAccount,
		"window_start": start.Format(time.RFC3339), "window_end": end.Format(time.RFC3339),
		"items": items, "next_cursor": next,
	})
}

func (a *App) encodeSelfAdminAdjustmentCursor(cursor selfWalletActivityCursor) (string, error) {
	plain, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.secrets.aead.NonceSize())
	readNonce := rand.Read
	if a.selfAdminAdjustmentNonce != nil {
		readNonce = a.selfAdminAdjustmentNonce
	}
	if n, err := readNonce(nonce); err != nil || n != len(nonce) {
		if err == nil {
			err = errSelfAdminAdjustmentCursor
		}
		return "", err
	}
	sealed := a.secrets.aead.Seal(nil, nonce, plain, []byte(selfAdminAdjustmentCursorPurpose))
	encoded := base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(encoded) > selfWalletActivityCursorMax {
		return "", errSelfAdminAdjustmentCursor
	}
	return encoded, nil
}

func (a *App) decodeSelfAdminAdjustmentCursor(encoded string, session selfSession, request selfWalletActivityRequest, now time.Time) (selfWalletActivityCursor, error) {
	invalid := func() (selfWalletActivityCursor, error) {
		return selfWalletActivityCursor{}, errSelfAdminAdjustmentCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || base64.RawURLEncoding.EncodeToString(data) != encoded {
		return invalid()
	}
	nonceSize := a.secrets.aead.NonceSize()
	if len(data) < nonceSize+a.secrets.aead.Overhead() {
		return invalid()
	}
	plain, err := a.secrets.aead.Open(nil, data[:nonceSize], data[nonceSize:], []byte(selfAdminAdjustmentCursorPurpose))
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
		!validIdentifier(cursor.LastID, 256) {
		return invalid()
	}
	start, startErr := time.Parse(time.RFC3339, cursor.WindowStart)
	end, endErr := time.Parse(time.RFC3339, cursor.WindowEnd)
	last, lastErr := time.Parse(time.RFC3339Nano, cursor.LastTime)
	if startErr != nil || endErr != nil || lastErr != nil || start.UTC().Format(time.RFC3339) != cursor.WindowStart ||
		end.UTC().Format(time.RFC3339) != cursor.WindowEnd || last.UTC().Format(time.RFC3339Nano) != cursor.LastTime ||
		end.Sub(start) != selfWalletActivityWindow || last.Before(start) || !last.Before(end) ||
		end.After(now.Add(time.Second)) || !now.Before(end.Add(selfWalletActivityCursorAge)) {
		return invalid()
	}
	return cursor, nil
}
