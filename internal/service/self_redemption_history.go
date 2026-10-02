// Independently authored for docs/employee-self-redemption-credit-history-contract.md.
package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"cpacloud.local/server/internal/financial"
)

const (
	selfRedemptionHistoryPath          = "/self/api/v1/billing/redemption-credits"
	selfRedemptionHistoryCursorPurpose = "cpacloud/self-redemption-credit-history-cursor/v1"
)

var errSelfRedemptionHistoryCursor = errors.New("invalid self redemption credit history cursor")

func selfRedemptionHistoryShapedPath(decoded string) bool {
	// Go decodes URL.Path once; a second encoded separator must not escape
	// this guard and fall through to the web SPA handler. This normalization
	// is only for rejecting shaped paths, never for accepting an API route.
	for candidate := strings.ToLower(decoded); ; {
		candidate = strings.ReplaceAll(candidate, `\`, "/")
		clean := path.Clean(candidate)
		if clean == selfRedemptionHistoryPath || strings.HasPrefix(clean, selfRedemptionHistoryPath+"/") {
			return true
		}
		parts := make([]string, 0, 10)
		for _, segment := range strings.Split(candidate, "/") {
			if segment != "" && segment != "." {
				parts = append(parts, segment)
			}
		}
		lexical := "/" + strings.Join(parts, "/")
		if lexical == selfRedemptionHistoryPath || strings.HasPrefix(lexical, selfRedemptionHistoryPath+"/") {
			return true
		}
		unescaped, err := url.PathUnescape(candidate)
		if err != nil || unescaped == candidate {
			return false
		}
		candidate = unescaped
	}
}

func (a *App) selfRedemptionHistoryRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !selfRedemptionHistoryShapedPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if !a.cfg.EmployeeSelfRedemptionCreditHistoryEnabled {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != selfRedemptionHistoryPath || r.URL.EscapedPath() != selfRedemptionHistoryPath {
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

func (a *App) selfRedemptionCreditHistory(w http.ResponseWriter, r *http.Request, session selfSession) {
	request, ok := parseSelfWalletActivityRequest(r.URL.RawQuery)
	if r.Method != http.MethodGet || r.URL.Path != selfRedemptionHistoryPath || r.URL.EscapedPath() != selfRedemptionHistoryPath ||
		r.URL.ForceQuery || !ok || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
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
		cursor, err := a.decodeSelfRedemptionHistoryCursor(request.cursor, session, request, now)
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
	page, err := financial.NewLedger(a.store.db).ReadEmployeeRedemptionCredits(ctx, financial.EmployeeActivityQuery{
		EmployeeID: session.EmployeeID, Currency: request.currency, WindowStart: windowStart, WindowEnd: windowEnd,
		Limit: request.limit, BeforeTime: position.Time, BeforeID: position.ID,
	})
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	items := make([]map[string]string, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, map[string]string{"credited_at": item.CreditedAt, "amount_micro": decimal(item.AmountMicro)})
	}
	var next *string
	if page.NextPosition != nil {
		encoded, err := a.encodeSelfRedemptionHistoryCursor(selfWalletActivityCursor{
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
	// The initial read releases admission. Serialize this final check and
	// success output with logout, password changes, and administrative disable.
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
	if a.selfRedemptionHistoryBeforeWrite != nil {
		a.selfRedemptionHistoryBeforeWrite()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"currency": request.currency, "has_account": page.HasAccount,
		"window_start": windowStart.Format(time.RFC3339), "window_end": windowEnd.Format(time.RFC3339),
		"items": items, "next_cursor": next,
	})
}

func (a *App) selfRedemptionHistorySessionCurrent(ctx context.Context, session selfSession) (bool, error) {
	var employeeID, csrf, expires, status, enrolledID string
	var verifier []byte
	err := a.store.db.QueryRowContext(ctx, `SELECT s.employee_id,s.verifier_digest,s.csrf_token,s.expires_at,e.status,c.employee_id
		FROM employee_self_sessions s JOIN employees e ON e.id=s.employee_id
		JOIN employee_self_credentials c ON c.employee_id=e.id WHERE s.selector=?`, session.Selector).
		Scan(&employeeID, &verifier, &csrf, &expires, &status, &enrolledID)
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
	return employeeID == session.EmployeeID && enrolledID == session.EmployeeID && csrf == session.CSRF &&
		subtle.ConstantTimeCompare(verifier, session.VerifierDigest) == 1 && status == "active" &&
		time.Now().UTC().Before(expiry), nil
}

func (a *App) encodeSelfRedemptionHistoryCursor(cursor selfWalletActivityCursor) (string, error) {
	plain, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, a.secrets.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := a.secrets.aead.Seal(nil, nonce, plain, []byte(selfRedemptionHistoryCursorPurpose))
	encoded := base64.RawURLEncoding.EncodeToString(append(nonce, sealed...))
	if len(encoded) > selfWalletActivityCursorMax {
		return "", errSelfRedemptionHistoryCursor
	}
	return encoded, nil
}

func (a *App) decodeSelfRedemptionHistoryCursor(encoded string, session selfSession, request selfWalletActivityRequest, now time.Time) (selfWalletActivityCursor, error) {
	invalid := func() (selfWalletActivityCursor, error) {
		return selfWalletActivityCursor{}, errSelfRedemptionHistoryCursor
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
	plain, err := a.secrets.aead.Open(nil, data[:nonceSize], data[nonceSize:], []byte(selfRedemptionHistoryCursorPurpose))
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
