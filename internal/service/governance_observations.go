package service

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cpacloud.local/server/internal/governance"
)

const (
	governanceObservationPath          = "/admin/api/v1/governance/observations"
	governanceObservationDefaultLimit  = 50
	governanceObservationCursorLimit   = 2048
	governanceObservationCursorVersion = byte(1)
	governanceObservationCursorPurpose = "governance-observation-cursor/v1"
)

type governanceObservationQuerier interface {
	QueryObservations(context.Context, governance.ObservationQuery) (governance.ObservationPage, error)
}

type governanceObservationHTTP struct {
	app   *App
	query governanceObservationQuerier
	now   func() time.Time
}

func (a *App) registerGovernanceObservationHandlers(mux *http.ServeMux) {
	if a == nil || mux == nil {
		return
	}
	var query governanceObservationQuerier
	if a.governance != nil {
		query = a.governance.core
	}
	handler := &governanceObservationHTTP{app: a, query: query, now: time.Now}
	mux.HandleFunc("GET "+governanceObservationPath, a.requireAdmin(handler.get, false))
}

func (h *governanceObservationHTTP) get(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if h == nil || h.app == nil || h.app.secrets == nil || h.query == nil || h.now == nil {
		writeGovernanceObservationError(w, &governance.ObservationError{Kind: governance.ObservationUnavailable})
		return
	}
	query, signedCursor, err := parseGovernanceObservationQuery(r)
	if err != nil {
		writeGovernanceObservationError(w, err)
		return
	}
	if signedCursor != "" {
		query.Cursor, err = h.decodeCursor(signedCursor)
		if err != nil {
			writeGovernanceObservationError(w, err)
			return
		}
	}
	query.ObservedAt = h.now().UTC()
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	page, err := h.query.QueryObservations(ctx, query)
	if err != nil {
		writeGovernanceObservationError(w, err)
		return
	}
	view, err := h.pageView(page)
	if err != nil {
		writeGovernanceObservationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func parseGovernanceObservationQuery(r *http.Request) (governance.ObservationQuery, string, error) {
	invalid := func() (governance.ObservationQuery, string, error) {
		return governance.ObservationQuery{}, "", &governance.ObservationError{Kind: governance.ObservationInvalidQuery}
	}
	if r == nil || r.URL == nil {
		return invalid()
	}
	if raw := r.URL.RawQuery; raw != "" {
		for _, part := range strings.Split(raw, "&") {
			if part == "" {
				return invalid()
			}
		}
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return invalid()
	}
	for key, entries := range values {
		switch key {
		case "limit", "cursor", "scope_kind", "scope_id", "policy_id":
		default:
			return invalid()
		}
		if len(entries) != 1 {
			return invalid()
		}
	}
	query := governance.ObservationQuery{Limit: governanceObservationDefaultLimit}
	if entries, ok := values["limit"]; ok {
		value := entries[0]
		if value == "" || len(value) > 1 && value[0] == '0' {
			return invalid()
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return invalid()
			}
		}
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed < 1 || parsed > governance.MaxObservationPageSize {
			return invalid()
		}
		query.Limit = parsed
	}
	signedCursor := ""
	if entries, ok := values["cursor"]; ok {
		signedCursor = entries[0]
		if signedCursor == "" || len([]byte(signedCursor)) > governanceObservationCursorLimit {
			return invalid()
		}
	}
	kindValues, hasKind := values["scope_kind"]
	idValues, hasID := values["scope_id"]
	if hasID && !hasKind {
		return invalid()
	}
	if hasKind {
		query.ScopeKind = governance.ScopeKind(kindValues[0])
		if query.ScopeKind != governance.ScopeEmployee && query.ScopeKind != governance.ScopeKey && query.ScopeKind != governance.ScopeGroup {
			return invalid()
		}
		if hasID {
			if !validGovernanceMetadata(idValues[0], 200) {
				return invalid()
			}
			query.ScopeID = idValues[0]
		}
	}
	if entries, ok := values["policy_id"]; ok {
		if !validGovernanceMetadata(entries[0], 200) {
			return invalid()
		}
		query.PolicyID = entries[0]
	}
	return query, signedCursor, nil
}

func (h *governanceObservationHTTP) encodeCursor(coreCursor string) (string, error) {
	if h == nil || h.app == nil || h.app.secrets == nil || coreCursor == "" {
		return "", &governance.ObservationError{Kind: governance.ObservationUnavailable}
	}
	coreLength := base64.RawURLEncoding.DecodedLen(len(coreCursor))
	if coreLength < 1 || coreLength > base64.RawURLEncoding.DecodedLen(governanceObservationCursorLimit) {
		return "", &governance.ObservationError{Kind: governance.ObservationUnavailable}
	}
	core := make([]byte, coreLength)
	written, err := base64.RawURLEncoding.Strict().Decode(core, []byte(coreCursor))
	if err != nil || written != coreLength {
		return "", &governance.ObservationError{Kind: governance.ObservationUnavailable}
	}
	core = core[:written]
	payload := make([]byte, 1+len(core))
	payload[0] = governanceObservationCursorVersion
	copy(payload[1:], core)
	mac := h.app.secrets.digest(governanceObservationCursorPurpose, string(payload))
	raw := append(payload, mac...)
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	if len(encoded) > governanceObservationCursorLimit {
		return "", &governance.ObservationError{Kind: governance.ObservationUnavailable}
	}
	return encoded, nil
}

func (h *governanceObservationHTTP) decodeCursor(encoded string) (string, error) {
	invalid := func() (string, error) {
		return "", &governance.ObservationError{Kind: governance.ObservationInvalidCursor}
	}
	if h == nil || h.app == nil || h.app.secrets == nil || encoded == "" || len(encoded) > governanceObservationCursorLimit {
		return invalid()
	}
	decodedLength := base64.RawURLEncoding.DecodedLen(len(encoded))
	if decodedLength < 1+32 || decodedLength > base64.RawURLEncoding.DecodedLen(governanceObservationCursorLimit) {
		return invalid()
	}
	raw := make([]byte, decodedLength)
	written, err := base64.RawURLEncoding.Strict().Decode(raw, []byte(encoded))
	if err != nil || written != decodedLength {
		return invalid()
	}
	raw = raw[:written]
	if raw[0] != governanceObservationCursorVersion || len(raw) <= 1+32 {
		return invalid()
	}
	payload, provided := raw[:len(raw)-32], raw[len(raw)-32:]
	expected := h.app.secrets.digest(governanceObservationCursorPurpose, string(payload))
	if subtle.ConstantTimeCompare(expected, provided) != 1 {
		return invalid()
	}
	core := payload[1:]
	if len(core) == 0 {
		return invalid()
	}
	return base64.RawURLEncoding.EncodeToString(core), nil
}

type governanceObservationPageView struct {
	WindowEnd  string                          `json:"window_end"`
	ObservedAt string                          `json:"observed_at"`
	TPMFrom    string                          `json:"tpm_from"`
	CostFrom   string                          `json:"cost_from"`
	Items      []governanceObservationItemView `json:"items"`
	NextCursor *string                         `json:"next_cursor"`
}

type governanceObservationItemView struct {
	Snapshot       governanceObservationSnapshotView       `json:"snapshot"`
	ScopeTotals    governanceObservationScopeTotalsView    `json:"scope_totals"`
	Interpretation governanceObservationInterpretationView `json:"interpretation"`
}

type governanceObservationSnapshotView struct {
	SettingsRevision string               `json:"settings_revision"`
	ScopeKind        governance.ScopeKind `json:"scope_kind"`
	ScopeID          string               `json:"scope_id"`
	PolicyID         string               `json:"policy_id"`
	PolicyRevision   string               `json:"policy_revision"`
	GroupRevision    *string              `json:"group_revision"`
	ShadowTPM        *string              `json:"shadow_tpm"`
	ShadowCostMicro  *string              `json:"shadow_cost_micro"`
	ShadowCurrency   string               `json:"shadow_currency"`
	ShadowWindow     string               `json:"shadow_window"`
}

type governanceObservationScopeTotalsView struct {
	TPM  governanceObservationTPMView  `json:"tpm"`
	Cost governanceObservationCostView `json:"cost"`
}

type governanceObservationTPMView struct {
	KnownTokens                   string `json:"known_tokens"`
	KnownAttempts                 string `json:"known_attempts"`
	UnknownTokenAttempts          string `json:"unknown_token_attempts"`
	PendingAttempts               string `json:"pending_attempts"`
	PendingRequestsWithoutAttempt string `json:"pending_requests_without_attempt"`
	ZeroAttemptRequests           string `json:"zero_attempt_requests"`
}

type governanceObservationCostView struct {
	KnownAttempts                 string                                   `json:"known_attempts"`
	UnknownCostAttempts           string                                   `json:"unknown_cost_attempts"`
	PendingAttempts               string                                   `json:"pending_attempts"`
	PendingRequestsWithoutAttempt string                                   `json:"pending_requests_without_attempt"`
	ZeroAttemptRequests           string                                   `json:"zero_attempt_requests"`
	ByCurrency                    []governanceObservationCurrencyTotalView `json:"by_currency"`
}

type governanceObservationCurrencyTotalView struct {
	Currency       string `json:"currency"`
	KnownCostMicro string `json:"known_cost_micro"`
	Attempts       string `json:"attempts"`
}

type governanceObservationInterpretationView struct {
	TPMState                     *governance.ObservationState `json:"tpm_state"`
	CostState                    *governance.ObservationState `json:"cost_state"`
	IncomparableCurrencyAttempts *string                      `json:"incomparable_currency_attempts"`
}

func (h *governanceObservationHTTP) pageView(page governance.ObservationPage) (governanceObservationPageView, error) {
	view := governanceObservationPageView{
		WindowEnd: page.WindowEnd, ObservedAt: page.ObservedAt, TPMFrom: page.TPMFrom, CostFrom: page.CostFrom,
		Items: make([]governanceObservationItemView, 0, len(page.Items)),
	}
	for _, item := range page.Items {
		currencies := make([]governanceObservationCurrencyTotalView, 0, len(item.ScopeTotals.Cost.ByCurrency))
		for _, currency := range item.ScopeTotals.Cost.ByCurrency {
			currencies = append(currencies, governanceObservationCurrencyTotalView{currency.Currency, currency.KnownCostMicro, currency.Attempts})
		}
		view.Items = append(view.Items, governanceObservationItemView{
			Snapshot: governanceObservationSnapshotView{
				item.Snapshot.SettingsRevision, item.Snapshot.ScopeKind, item.Snapshot.ScopeID, item.Snapshot.PolicyID,
				item.Snapshot.PolicyRevision, item.Snapshot.GroupRevision, item.Snapshot.ShadowTPM, item.Snapshot.ShadowCostMicro,
				item.Snapshot.ShadowCurrency, item.Snapshot.ShadowWindow,
			},
			ScopeTotals: governanceObservationScopeTotalsView{
				TPM: governanceObservationTPMView{
					item.ScopeTotals.TPM.KnownTokens, item.ScopeTotals.TPM.KnownAttempts, item.ScopeTotals.TPM.UnknownTokenAttempts,
					item.ScopeTotals.TPM.PendingAttempts, item.ScopeTotals.TPM.PendingRequestsWithoutAttempt, item.ScopeTotals.TPM.ZeroAttemptRequests,
				},
				Cost: governanceObservationCostView{
					item.ScopeTotals.Cost.KnownAttempts, item.ScopeTotals.Cost.UnknownCostAttempts, item.ScopeTotals.Cost.PendingAttempts,
					item.ScopeTotals.Cost.PendingRequestsWithoutAttempt, item.ScopeTotals.Cost.ZeroAttemptRequests, currencies,
				},
			},
			Interpretation: governanceObservationInterpretationView{
				item.Interpretation.TPMState, item.Interpretation.CostState, item.Interpretation.IncomparableCurrencyAttempts,
			},
		})
	}
	if page.NextCursor != "" {
		next, err := h.encodeCursor(page.NextCursor)
		if err != nil {
			return governanceObservationPageView{}, err
		}
		view.NextCursor = &next
	}
	return view, nil
}

func writeGovernanceObservationError(w http.ResponseWriter, err error) {
	var observationError *governance.ObservationError
	if errors.As(err, &observationError) && (observationError.Kind == governance.ObservationInvalidQuery || observationError.Kind == governance.ObservationInvalidCursor) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid governance observation request.")
		return
	}
	writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Governance observations are temporarily unavailable.")
}
