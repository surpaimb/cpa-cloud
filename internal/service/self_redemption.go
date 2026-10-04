package service

// Independently authored for docs/employee-self-redemption-contract.md.
// The self credential and immutable financial chain share one commit boundary.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"cpacloud.local/server/internal/financial"
	"golang.org/x/crypto/bcrypt"
)

const selfRedemptionPath = "/self/api/v1/billing/redemptions"

const (
	selfRedemptionShapeMaxBytes  = 8192
	selfRedemptionShapeMaxRounds = 16
)

func selfRedemptionASCIILower(value string) string {
	bytes := []byte(value)
	for i, letter := range bytes {
		if letter >= 'A' && letter <= 'Z' {
			bytes[i] = letter + ('a' - 'A')
		}
	}
	return string(bytes)
}

func selfRedemptionShapedSegment(value string) bool {
	if value == selfRedemptionPath || strings.HasPrefix(value, selfRedemptionPath+"/") {
		return true
	}
	// An encoded '?' or '#' remains path data, not a query or fragment. It is
	// still an alias of this write-route stem, never a sibling route.
	return strings.HasPrefix(value, selfRedemptionPath+"?") || strings.HasPrefix(value, selfRedemptionPath+"#")
}

func selfRedemptionShapedPath(decoded string, truncated bool) bool {
	// Backslashes are separators only in this reject-only view. Never route a
	// decoded alias or rewrite the request that reaches another handler.
	lower := selfRedemptionASCIILower(strings.ReplaceAll(decoded, `\`, "/"))
	if truncated {
		// A synthetic non-boundary byte prevents path.Clean from treating the
		// byte cap as a segment ending. This view is never decoded or forwarded.
		lower += "x"
	}
	clean := path.Clean(lower)
	if selfRedemptionShapedSegment(clean) {
		return true
	}
	// Keep parent segments for classification. Cleaning them first can turn a
	// code-bearing "/redemptions/.." POST into a ServeMux redirect.
	parts := make([]string, 0, 10)
	for _, segment := range strings.Split(lower, "/") {
		if segment != "" && segment != "." {
			parts = append(parts, segment)
		}
	}
	lexical := "/" + strings.Join(parts, "/")
	return selfRedemptionShapedSegment(lexical)
}

func selfRedemptionHex(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

// selfRedemptionResidualByte proves one byte without constructing another
// decoded path. A nested escape is '%' followed by zero or more '25' pairs
// and one final hex pair; every input byte is consumed at most once.
func selfRedemptionResidualByte(value string, at int, truncated bool) (byte, int, bool) {
	if at >= len(value) {
		return 0, at, false
	}
	if value[at] != '%' {
		return value[at], at + 1, true
	}
	cursor := at + 1
	for {
		if cursor+1 >= len(value) {
			return 0, at, false
		}
		hi, okHi := selfRedemptionHex(value[cursor])
		lo, okLo := selfRedemptionHex(value[cursor+1])
		if !okHi || !okLo {
			return 0, at, false
		}
		decoded := hi<<4 | lo
		cursor += 2
		if decoded != '%' || cursor+1 >= len(value) {
			if decoded == '%' && truncated && cursor == len(value) {
				// More hex pairs may be beyond the byte cap.
				return 0, at, false
			}
			return decoded, cursor, true
		}
		if _, ok := selfRedemptionHex(value[cursor]); !ok {
			return decoded, cursor, true
		}
		if _, ok := selfRedemptionHex(value[cursor+1]); !ok {
			return decoded, cursor, true
		}
	}
}

func selfRedemptionResidualMatch(value string, truncated bool) bool {
	segments := [...]string{"self", "api", "v1", "billing", "redemptions"}
	// Only fixed-stem match states are kept; no decoded path is constructed.
	cleanPrefix := []int{0}
	lexicalPrefix := 0
	at := 0
	first := true
	for at < len(value) {
		separators := 0
		for at < len(value) {
			letter, next, ok := selfRedemptionResidualByte(value, at, truncated)
			if !ok || (letter != '/' && letter != '\\') {
				break
			}
			at = next
			separators++
		}
		if first && separators == 0 {
			return false
		}
		first = false
		if at == len(value) {
			break
		}
		matches := [5]bool{true, true, true, true, true}
		isDot, isDotDot := true, true
		length := 0
		priorClean := cleanPrefix[len(cleanPrefix)-1]
		priorLexical := lexicalPrefix
		for at < len(value) {
			letter, next, ok := selfRedemptionResidualByte(value, at, truncated)
			if !ok {
				return false
			}
			if letter == '/' || letter == '\\' {
				break
			}
			if letter >= 'A' && letter <= 'Z' {
				letter += 'a' - 'A'
			}
			if length == len(segments[4]) && matches[4] && (priorClean == 4 || priorLexical == 4) &&
				(letter == '?' || letter == '#') {
				return true
			}
			for i, segment := range segments {
				if length >= len(segment) || letter != segment[length] {
					matches[i] = false
				}
			}
			if length != 0 || letter != '.' {
				isDot = false
			}
			if length >= 2 || letter != '.' {
				isDotDot = false
			}
			at = next
			length++
		}
		for i, segment := range segments {
			matches[i] = matches[i] && length == len(segment)
		}
		if matches[4] && (priorClean == 4 || priorLexical == 4) && (at < len(value) || !truncated) {
			return true
		}
		if isDot && length == 1 {
			continue
		}
		if isDotDot && length == 2 {
			if len(cleanPrefix) > 1 {
				cleanPrefix = cleanPrefix[:len(cleanPrefix)-1]
			}
		} else {
			next := -1
			if priorClean >= 0 && priorClean < len(segments) && matches[priorClean] {
				next = priorClean + 1
			}
			cleanPrefix = append(cleanPrefix, next)
		}
		if priorLexical >= 0 && priorLexical < len(segments) && matches[priorLexical] {
			lexicalPrefix++
		} else {
			lexicalPrefix = -1
		}
	}
	return false
}

func selfRedemptionShapedView(value string) bool {
	truncated := len(value) > selfRedemptionShapeMaxBytes
	if truncated {
		value = value[:selfRedemptionShapeMaxBytes]
	}
	for round := 0; round <= selfRedemptionShapeMaxRounds; round++ {
		if selfRedemptionShapedPath(value, truncated) {
			return true
		}
		// A recognized stem with an unresolved escape must not pass to ServeMux
		// when the decode budget is exhausted or an escape is malformed.
		if round == selfRedemptionShapeMaxRounds {
			break
		}
		next, err := url.PathUnescape(value)
		if err != nil || next == value {
			break
		}
		value = next
	}
	return selfRedemptionResidualMatch(value, truncated)
}

func selfRedemptionShapedRequest(r *http.Request) bool {
	if r.URL == nil {
		return false
	}
	views := []string{r.URL.Path, r.URL.EscapedPath()}
	if r.URL.RawPath != "" {
		views = append(views, r.URL.RawPath)
	}
	if r.RequestURI != "" {
		rawPath, _, _ := strings.Cut(r.RequestURI, "?")
		views = append(views, rawPath)
	}
	for _, view := range views {
		if selfRedemptionShapedView(view) {
			return true
		}
	}
	return false
}

func selfRedemptionCanonicalRequest(r *http.Request) bool {
	if r.URL == nil || r.URL.Path != selfRedemptionPath || r.URL.EscapedPath() != selfRedemptionPath ||
		(r.URL.RawPath != "" && r.URL.RawPath != selfRedemptionPath) || r.URL.RawQuery != "" || r.URL.ForceQuery {
		return false
	}
	if r.RequestURI != "" {
		return r.RequestURI == selfRedemptionPath
	}
	return true
}

func (a *App) selfRedemptionRouteGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ServeMux can canonicalize encoded or doubled path separators before
		// a route is selected; a code-bearing POST must never be redirected.
		if selfRedemptionShapedRequest(r) {
			if !a.cfg.EmployeeSelfRedemptionEnabled {
				http.NotFound(w, r)
				return
			}
			if !selfRedemptionCanonicalRequest(r) {
				a.requireSelfRedemption(func(w http.ResponseWriter, r *http.Request, session selfSession) {
					peer, ok := a.selfGate(w, r, session.EmployeeID)
					if !ok {
						return
					}
					a.selfFailure(peer, session.EmployeeID)
					selfError(w, http.StatusBadRequest, "invalid_request")
				}, true)(w, r)
				return
			}
			if r.Method != http.MethodPost {
				a.requireSelf(func(w http.ResponseWriter, _ *http.Request, _ selfSession) {
					w.Header().Set("Allow", http.MethodPost)
					selfError(w, http.StatusMethodNotAllowed, "method_not_allowed")
				}, false)(w, r)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) redemptionTime() time.Time {
	if a.selfRedemptionNow != nil {
		return a.selfRedemptionNow().UTC()
	}
	return time.Now().UTC()
}

func selfRedemptionFinancialError(w http.ResponseWriter, err error) {
	if errors.Is(err, financial.ErrConflict) || errors.Is(err, financial.ErrNotFound) || errors.Is(err, financial.ErrInsufficient) {
		selfError(w, http.StatusConflict, "redemption_unavailable")
		return
	}
	selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
}

func (a *App) selfRedeemCode(w http.ResponseWriter, r *http.Request, session selfSession) {
	if r.URL.Path != selfRedemptionPath || r.URL.EscapedPath() != selfRedemptionPath || r.URL.RawQuery != "" ||
		r.URL.ForceQuery || r.ContentLength < 0 || r.ContentLength > selfMaxBody || len(r.TransferEncoding) != 0 {
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	peer, ok := a.selfGate(w, r, session.EmployeeID)
	if !ok {
		return
	}
	fields, ok := readSelfFields(w, r, "operation_id", "code", "current_password")
	if !ok {
		a.selfFailure(peer, session.EmployeeID)
		return
	}
	operationID, code, password := fields["operation_id"], fields["code"], fields["current_password"]
	if !validSelfPurchaseID(operationID, 128) || len(code) > 256 {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !validSelfPassword(password) {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	var priorHash []byte
	var hashType string
	err := a.store.db.QueryRowContext(r.Context(), `SELECT password_hash,typeof(password_hash) FROM employee_self_credentials WHERE employee_id=?`, session.EmployeeID).Scan(&priorHash, &hashType)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	comparison := priorHash
	if len(comparison) == 0 {
		comparison = selfDummyHash
	}
	passwordOK := bcrypt.CompareHashAndPassword(comparison, []byte(password)) == nil
	if err == nil && hashType != "blob" {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if !passwordOK || len(priorHash) == 0 {
		a.selfFailure(peer, session.EmployeeID)
		selfError(w, http.StatusUnauthorized, "invalid_credentials")
		return
	}
	if a.secrets == nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	digest := a.secrets.digest(billingWebhookPurpose, code)
	if len(digest) != sha256.Size {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	var codeDigest [sha256.Size]byte
	copy(codeDigest[:], digest)
	if a.selfRedemptionBeforeTx != nil {
		a.selfRedemptionBeforeTx()
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), selfPlanPurchaseTimeout)
	defer cancel()
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	if err := a.recheckSelfPlanPurchaseTx(ctx, tx, r, session, priorHash); err != nil {
		a.selfPlanPurchaseAuthError(w, err)
		return
	}
	input := financial.EmployeeRedemptionInput{OperationID: operationID,
		Actor:      financial.Actor{Kind: financial.ActorEmployee, ID: session.EmployeeID},
		Owner:      financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: session.EmployeeID},
		CodeDigest: codeDigest, ObservedAt: a.redemptionTime()}
	result, err := financial.NewCommercial(a.store.db).RedeemEmployeeCodeTx(ctx, tx, input)
	if err != nil {
		if errors.Is(err, financial.ErrConflict) {
			a.selfFailure(peer, session.EmployeeID)
		}
		selfRedemptionFinancialError(w, err)
		return
	}
	if err := a.recheckSelfPlanPurchaseTx(ctx, tx, r, session, priorHash); err != nil {
		a.selfPlanPurchaseAuthError(w, err)
		return
	}
	if ctx.Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	commit := tx.Commit
	if a.selfRedemptionCommit != nil {
		commit = func() error { return a.selfRedemptionCommit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil || r.Context().Err() != nil {
		selfError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.clearSelfFailures(peer, session.EmployeeID)
	status := http.StatusCreated
	if result.Replay {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"operation_id": operationID, "replay": result.Replay,
		"currency": result.Currency, "amount_micro": strconv.FormatInt(result.AmountMicro, 10),
		"credited_at": result.CreditedAt.Format(time.RFC3339Nano)})
}
