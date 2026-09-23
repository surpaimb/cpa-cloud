package governance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strconv"
	"time"

	"cpacloud.local/server/internal/accounting"
)

const (
	MaxObservationPageSize = 100
	observationTimeout     = 5 * time.Second
	observationCursorLimit = 2048
	observationCursorV1    = 1
)

type ObservationErrorKind string

const (
	ObservationInvalidQuery  ObservationErrorKind = "invalid_query"
	ObservationInvalidCursor ObservationErrorKind = "invalid_cursor"
	ObservationUnavailable   ObservationErrorKind = "unavailable"
	ObservationSchema        ObservationErrorKind = "schema"
	ObservationOverflow      ObservationErrorKind = "overflow"
)

// ObservationError deliberately exposes only a fixed classification. Database
// diagnostics and cursor contents must not cross the service boundary.
type ObservationError struct {
	Kind ObservationErrorKind
}

func (e *ObservationError) Error() string {
	if e == nil {
		return "governance observation error"
	}
	return "governance observation " + string(e.Kind)
}

func (e *ObservationError) Unwrap() error {
	if e == nil {
		return nil
	}
	switch e.Kind {
	case ObservationInvalidQuery, ObservationInvalidCursor:
		return ErrInvalid
	case ObservationSchema:
		return ErrSchema
	default:
		return ErrUnavailable
	}
}

type ObservationQuery struct {
	Limit      int
	Cursor     string
	ScopeKind  ScopeKind
	ScopeID    string
	PolicyID   string
	ObservedAt time.Time
}

type ObservationPage struct {
	WindowEnd  string
	ObservedAt string
	TPMFrom    string
	CostFrom   string
	Items      []ObservationItem
	NextCursor string
}

type ObservationItem struct {
	Snapshot       ObservationSnapshot
	ScopeTotals    ObservationScopeTotals
	Interpretation ObservationInterpretation
}

type ObservationSnapshot struct {
	SettingsRevision string
	ScopeKind        ScopeKind
	ScopeID          string
	PolicyID         string
	PolicyRevision   string
	GroupRevision    *string
	ShadowTPM        *string
	ShadowCostMicro  *string
	ShadowCurrency   string
	ShadowWindow     string
}

type ObservationScopeTotals struct {
	TPM  ObservationTPMTotals
	Cost ObservationCostTotals
}

type ObservationTPMTotals struct {
	KnownTokens                   string
	KnownAttempts                 string
	UnknownTokenAttempts          string
	PendingAttempts               string
	PendingRequestsWithoutAttempt string
	ZeroAttemptRequests           string
}

type ObservationCostTotals struct {
	KnownAttempts                 string
	UnknownCostAttempts           string
	PendingAttempts               string
	PendingRequestsWithoutAttempt string
	ZeroAttemptRequests           string
	ByCurrency                    []ObservationCurrencyTotal
}

type ObservationCurrencyTotal struct {
	Currency       string
	KnownCostMicro string
	Attempts       string
}

type ObservationState string

const (
	ObservationExceeded ObservationState = "exceeded"
	ObservationUnknown  ObservationState = "unknown"
	ObservationBelow    ObservationState = "below"
)

type ObservationInterpretation struct {
	TPMState                     *ObservationState
	CostState                    *ObservationState
	IncomparableCurrencyAttempts *string
}

type observationKey struct {
	scopeKind        ScopeKind
	scopeID          string
	policyID         string
	policyRevision   int64
	groupRevision    *int64
	settingsRevision int64
}

type observationSnapshot struct {
	key              observationKey
	rpmLimit         *int64
	concurrencyLimit *int64
	shadowTPM        *int64
	shadowCostMicro  *int64
	currency         string
	window           string
}

type observationCursor struct {
	Version              int    `json:"v"`
	WindowEnd            string `json:"w"`
	FilterFingerprint    string `json:"f"`
	LastScopeKind        string `json:"lk"`
	LastScopeID          string `json:"li"`
	LastPolicyID         string `json:"lp"`
	LastPolicyRevision   int64  `json:"lr"`
	LastGroupRevision    *int64 `json:"lg"`
	LastSettingsRevision int64  `json:"ls"`
}

type observationCounts struct {
	knownAttempts                 int64
	unknownAttempts               int64
	pendingAttempts               int64
	pendingRequestsWithoutAttempt int64
	zeroAttemptRequests           int64
}

type observationCurrency struct {
	cost     int64
	attempts int64
}

type observationTotals struct {
	tpm        observationCounts
	tokens     int64
	cost       observationCounts
	currencies map[string]observationCurrency
}

type observationAttempt struct {
	id         sql.NullString
	status     sql.NullString
	input      sql.NullInt64
	output     sql.NullInt64
	cacheRead  sql.NullInt64
	cacheWrite sql.NullInt64
	currency   sql.NullString
	cost       sql.NullInt64
}

// QueryObservations reads one internally consistent page. Its cursor is strict
// and opaque but deliberately unsigned; the HTTP layer wraps it with its own
// persistent HMAC before accepting it from an administrator.
func (c *Coordinator) QueryObservations(ctx context.Context, query ObservationQuery) (ObservationPage, error) {
	if c == nil || c.db == nil || ctx == nil {
		return ObservationPage{}, observationError(ObservationInvalidQuery)
	}
	if err := validateObservationQuery(query); err != nil {
		return ObservationPage{}, err
	}
	queryCtx, cancel := context.WithTimeout(ctx, observationTimeout)
	defer cancel()

	tx, err := c.db.BeginTx(queryCtx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ObservationPage{}, observationError(ObservationUnavailable)
	}
	defer tx.Rollback()

	settings, err := readSettings(queryCtx, tx)
	if err != nil {
		return ObservationPage{}, classifyObservationError(err)
	}
	windowEnd := query.ObservedAt.UTC()
	var after *observationKey
	if query.Cursor == "" {
		if settings.LastEffectiveAdmissionTime != nil && settings.LastEffectiveAdmissionTime.After(windowEnd) {
			windowEnd = *settings.LastEffectiveAdmissionTime
		}
	} else {
		decoded, err := decodeObservationCursor(query.Cursor, query)
		if err != nil {
			return ObservationPage{}, err
		}
		windowEnd, err = parseStoredTime(decoded.WindowEnd)
		if err != nil || windowEnd.Before(time.Unix(0, 0).UTC()) {
			return ObservationPage{}, observationError(ObservationInvalidCursor)
		}
		key := cursorKey(decoded)
		after = &key
	}

	observedAt := query.ObservedAt.UTC()
	tpmFrom := windowEnd.Add(-time.Minute)
	costFrom := windowEnd.Add(-24 * time.Hour)
	if !validUTCTime(tpmFrom) || !validUTCTime(costFrom) {
		return ObservationPage{}, observationError(ObservationInvalidQuery)
	}
	snapshots, more, err := loadObservationSnapshots(queryCtx, tx, query, costFrom, windowEnd, after)
	if err != nil {
		return ObservationPage{}, err
	}

	totalsByScope := make(map[string]observationTotals)
	items := make([]ObservationItem, 0, len(snapshots))
	for _, snapshot := range snapshots {
		mapKey := string(snapshot.key.scopeKind) + "\x00" + snapshot.key.scopeID
		totals, ok := totalsByScope[mapKey]
		if !ok {
			totals, err = loadObservationTotals(queryCtx, tx, snapshot.key.scopeKind, snapshot.key.scopeID, tpmFrom, costFrom, windowEnd)
			if err != nil {
				return ObservationPage{}, err
			}
			totalsByScope[mapKey] = totals
		}
		item, err := makeObservationItem(snapshot, totals)
		if err != nil {
			return ObservationPage{}, err
		}
		items = append(items, item)
	}

	page := ObservationPage{
		WindowEnd: formatTime(windowEnd), ObservedAt: formatTime(observedAt),
		TPMFrom: formatTime(tpmFrom), CostFrom: formatTime(costFrom), Items: items,
	}
	if more && len(snapshots) > 0 {
		page.NextCursor, err = encodeObservationCursor(query, windowEnd, snapshots[len(snapshots)-1].key)
		if err != nil {
			return ObservationPage{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ObservationPage{}, observationError(ObservationUnavailable)
	}
	return page, nil
}

func validateObservationQuery(query ObservationQuery) error {
	if query.Limit < 1 || query.Limit > MaxObservationPageSize || len(query.Cursor) > observationCursorLimit ||
		!validUTCTime(query.ObservedAt) || query.ObservedAt.Year() < 1970 || query.ObservedAt.Year() > 9999 {
		return observationError(ObservationInvalidQuery)
	}
	if query.ScopeID != "" && query.ScopeKind == "" || query.ScopeKind != "" && !validScopeKind(query.ScopeKind) ||
		query.ScopeID != "" && !validMetadata(query.ScopeID, 200) || query.PolicyID != "" && !validMetadata(query.PolicyID, 200) {
		return observationError(ObservationInvalidQuery)
	}
	return nil
}

func loadObservationSnapshots(ctx context.Context, tx *sql.Tx, query ObservationQuery, costFrom, windowEnd time.Time, after *observationKey) ([]observationSnapshot, bool, error) {
	statement := `SELECT r.settings_revision,s.scope_kind,s.scope_id,s.policy_id,s.policy_revision,s.group_revision,
		MIN(s.rpm_limit),MIN(s.concurrency_limit),MIN(s.shadow_tpm),MIN(s.shadow_cost_micro),MIN(s.shadow_currency),MIN(s.shadow_window),
		CASE WHEN COUNT(DISTINCT COALESCE(CAST(s.rpm_limit AS TEXT),'#'))>1
			OR COUNT(DISTINCT COALESCE(CAST(s.concurrency_limit AS TEXT),'#'))>1
			OR COUNT(DISTINCT COALESCE(CAST(s.shadow_tpm AS TEXT),'#'))>1
			OR COUNT(DISTINCT COALESCE(CAST(s.shadow_cost_micro AS TEXT),'#'))>1
			OR COUNT(DISTINCT s.shadow_currency)>1
			OR COUNT(DISTINCT s.shadow_window)>1 THEN 1 ELSE 0 END
		FROM governance_request_scopes s JOIN governance_requests r ON r.id=s.request_id
		WHERE r.effective_started_at>? AND r.effective_started_at<=?`
	arguments := []any{formatTime(costFrom), formatTime(windowEnd)}
	if query.ScopeKind != "" {
		statement += ` AND s.scope_kind=?`
		arguments = append(arguments, string(query.ScopeKind))
	}
	if query.ScopeID != "" {
		statement += ` AND s.scope_id=?`
		arguments = append(arguments, query.ScopeID)
	}
	if query.PolicyID != "" {
		statement += ` AND s.policy_id=?`
		arguments = append(arguments, query.PolicyID)
	}
	if after != nil {
		statement += ` AND (s.scope_kind>? OR (s.scope_kind=? AND (s.scope_id>? OR (s.scope_id=? AND
			(s.policy_id>? OR (s.policy_id=? AND (s.policy_revision>? OR (s.policy_revision=? AND
			(COALESCE(s.group_revision,0)>? OR (COALESCE(s.group_revision,0)=? AND r.settings_revision>?))))))))))`
		groupRevision := int64(0)
		if after.groupRevision != nil {
			groupRevision = *after.groupRevision
		}
		arguments = append(arguments, string(after.scopeKind), string(after.scopeKind), after.scopeID, after.scopeID,
			after.policyID, after.policyID, after.policyRevision, after.policyRevision, groupRevision, groupRevision, after.settingsRevision)
	}
	statement += ` GROUP BY r.settings_revision,s.scope_kind,s.scope_id,s.policy_id,s.policy_revision,s.group_revision
		ORDER BY s.scope_kind,s.scope_id,s.policy_id,s.policy_revision,COALESCE(s.group_revision,0),r.settings_revision LIMIT ?`
	arguments = append(arguments, query.Limit+1)
	rows, err := tx.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, false, observationError(ObservationUnavailable)
	}
	snapshots := make([]observationSnapshot, 0, query.Limit+1)
	for rows.Next() {
		var snapshot observationSnapshot
		var kind string
		var groupRevision, rpmLimit, concurrencyLimit, shadowTPM, shadowCost sql.NullInt64
		var mismatch int
		if err := rows.Scan(&snapshot.key.settingsRevision, &kind, &snapshot.key.scopeID, &snapshot.key.policyID,
			&snapshot.key.policyRevision, &groupRevision, &rpmLimit, &concurrencyLimit, &shadowTPM, &shadowCost,
			&snapshot.currency, &snapshot.window, &mismatch); err != nil {
			rows.Close()
			return nil, false, observationError(ObservationUnavailable)
		}
		snapshot.key.scopeKind = ScopeKind(kind)
		setOptionalInt64(&snapshot.key.groupRevision, groupRevision)
		setOptionalInt64(&snapshot.rpmLimit, rpmLimit)
		setOptionalInt64(&snapshot.concurrencyLimit, concurrencyLimit)
		setOptionalInt64(&snapshot.shadowTPM, shadowTPM)
		setOptionalInt64(&snapshot.shadowCostMicro, shadowCost)
		if mismatch != 0 || !validObservationSnapshot(snapshot) {
			rows.Close()
			return nil, false, observationError(ObservationSchema)
		}
		snapshots = append(snapshots, snapshot)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return nil, false, observationError(ObservationUnavailable)
	}
	more := len(snapshots) > query.Limit
	if more {
		snapshots = snapshots[:query.Limit]
	}
	return snapshots, more, nil
}

func validObservationSnapshot(snapshot observationSnapshot) bool {
	key := snapshot.key
	if !validRevision(key.settingsRevision) || !validScopeKind(key.scopeKind) || !validMetadata(key.scopeID, 256) ||
		!validMetadata(key.policyID, 256) || !validRevision(key.policyRevision) ||
		key.scopeKind == ScopeGroup && (key.groupRevision == nil || !validRevision(*key.groupRevision)) ||
		key.scopeKind != ScopeGroup && key.groupRevision != nil ||
		snapshot.rpmLimit == nil && snapshot.concurrencyLimit == nil && snapshot.shadowTPM == nil && snapshot.shadowCostMicro == nil ||
		!validLimit(snapshot.rpmLimit) || !validLimit(snapshot.concurrencyLimit) || !validLimit(snapshot.shadowTPM) || !validLimit(snapshot.shadowCostMicro) {
		return false
	}
	return validShadowCost(ScopeSnapshot{ShadowCostMicro: snapshot.shadowCostMicro, ShadowCurrency: snapshot.currency, ShadowWindow: snapshot.window})
}

func loadObservationTotals(ctx context.Context, tx *sql.Tx, kind ScopeKind, scopeID string, tpmFrom, costFrom, windowEnd time.Time) (observationTotals, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.id,r.employee_id,r.key_id,r.public_model,r.status,r.effective_started_at,
		ar.id,ar.employee_id,ar.key_id,ar.model_id,ar.status,
		a.id,a.status,a.input_tokens,a.output_tokens,a.cache_read_tokens,a.cache_write_tokens,a.currency,a.cost_micro
		FROM governance_request_scopes s
		JOIN governance_requests r ON r.id=s.request_id
		LEFT JOIN accounting_requests ar ON ar.id=r.id
		LEFT JOIN accounting_attempts a ON a.request_id=ar.id
		WHERE s.scope_kind=? AND s.scope_id=? AND r.effective_started_at>? AND r.effective_started_at<=?
		ORDER BY r.id,a.id`, string(kind), scopeID, formatTime(costFrom), formatTime(windowEnd))
	if err != nil {
		return observationTotals{}, observationError(ObservationUnavailable)
	}
	totals := observationTotals{currencies: make(map[string]observationCurrency)}
	var previousRequest string
	var previousHasAttempt bool
	var previousStatus accounting.Status
	var previousInTPM bool
	for rows.Next() {
		var requestID, employeeID, keyID, modelID, requestStatus, effectiveStarted string
		var accountingID, accountingEmployee, accountingKey, accountingModel, accountingStatus sql.NullString
		var attempt observationAttempt
		if err := rows.Scan(&requestID, &employeeID, &keyID, &modelID, &requestStatus, &effectiveStarted,
			&accountingID, &accountingEmployee, &accountingKey, &accountingModel, &accountingStatus,
			&attempt.id, &attempt.status, &attempt.input, &attempt.output, &attempt.cacheRead, &attempt.cacheWrite,
			&attempt.currency, &attempt.cost); err != nil {
			rows.Close()
			return observationTotals{}, observationError(ObservationUnavailable)
		}
		effective, err := parseStoredTime(effectiveStarted)
		if err != nil {
			rows.Close()
			return observationTotals{}, observationError(ObservationSchema)
		}
		status := accounting.Status(requestStatus)
		if !validObservationRelation(requestID, employeeID, keyID, modelID, status, accountingID, accountingEmployee, accountingKey, accountingModel, accountingStatus) {
			rows.Close()
			return observationTotals{}, observationError(ObservationSchema)
		}
		if requestID != previousRequest {
			if previousRequest != "" {
				if err := finishObservationRequest(&totals, previousStatus, previousHasAttempt, previousInTPM); err != nil {
					rows.Close()
					return observationTotals{}, err
				}
			}
			previousRequest, previousStatus, previousHasAttempt = requestID, status, false
			previousInTPM = effective.After(tpmFrom)
		} else if status != previousStatus || previousInTPM != effective.After(tpmFrom) {
			rows.Close()
			return observationTotals{}, observationError(ObservationSchema)
		}
		if attempt.id.Valid {
			previousHasAttempt = true
			if err := addObservationAttempt(&totals, attempt, previousInTPM); err != nil {
				rows.Close()
				return observationTotals{}, err
			}
		} else if attempt.status.Valid || attempt.input.Valid || attempt.output.Valid || attempt.cacheRead.Valid || attempt.cacheWrite.Valid || attempt.currency.Valid || attempt.cost.Valid {
			rows.Close()
			return observationTotals{}, observationError(ObservationSchema)
		}
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return observationTotals{}, observationError(ObservationUnavailable)
	}
	if previousRequest != "" {
		if err := finishObservationRequest(&totals, previousStatus, previousHasAttempt, previousInTPM); err != nil {
			return observationTotals{}, err
		}
	}
	return totals, nil
}

func validObservationRelation(requestID, employeeID, keyID, modelID string, status accounting.Status, accountingID, accountingEmployee, accountingKey, accountingModel, accountingStatus sql.NullString) bool {
	if !validMetadata(requestID, 256) || !validMetadata(employeeID, 256) || !validMetadata(keyID, 256) || !validMetadata(modelID, 256) ||
		status != accounting.StatusPending && !terminalStatus(status) {
		return false
	}
	if !accountingID.Valid {
		return !accountingEmployee.Valid && !accountingKey.Valid && !accountingModel.Valid && !accountingStatus.Valid
	}
	return accountingID.String == requestID && accountingEmployee.Valid && accountingEmployee.String == employeeID &&
		accountingKey.Valid && accountingKey.String == keyID && accountingModel.Valid && accountingModel.String == modelID &&
		accountingStatus.Valid && accounting.Status(accountingStatus.String) == status
}

func finishObservationRequest(totals *observationTotals, status accounting.Status, hasAttempt, inTPM bool) error {
	if hasAttempt {
		return nil
	}
	pending := status == accounting.StatusPending
	if pending {
		if err := checkedIncrement(&totals.cost.pendingRequestsWithoutAttempt); err != nil {
			return err
		}
		if inTPM {
			return checkedIncrement(&totals.tpm.pendingRequestsWithoutAttempt)
		}
		return nil
	}
	if err := checkedIncrement(&totals.cost.zeroAttemptRequests); err != nil {
		return err
	}
	if inTPM {
		return checkedIncrement(&totals.tpm.zeroAttemptRequests)
	}
	return nil
}

func addObservationAttempt(totals *observationTotals, attempt observationAttempt, inTPM bool) error {
	status := accounting.Status(attempt.status.String)
	if !attempt.status.Valid || status != accounting.StatusPending && !terminalStatus(status) {
		return observationError(ObservationSchema)
	}
	if status == accounting.StatusPending {
		if attempt.input.Valid || attempt.output.Valid || attempt.cacheRead.Valid || attempt.cacheWrite.Valid || attempt.cost.Valid {
			return observationError(ObservationSchema)
		}
		if err := checkedIncrement(&totals.cost.pendingAttempts); err != nil {
			return err
		}
		if inTPM {
			return checkedIncrement(&totals.tpm.pendingAttempts)
		}
		return nil
	}
	usageKnown := attempt.input.Valid && attempt.output.Valid && attempt.cacheRead.Valid && attempt.cacheWrite.Valid
	if usageKnown {
		attemptTokens, err := checkedSum(attempt.input.Int64, attempt.output.Int64, attempt.cacheRead.Int64, attempt.cacheWrite.Int64)
		if err != nil {
			return err
		}
		if inTPM {
			if totals.tokens, err = checkedAdd(totals.tokens, attemptTokens); err != nil {
				return err
			}
			if err := checkedIncrement(&totals.tpm.knownAttempts); err != nil {
				return err
			}
		}
	} else if inTPM {
		if err := checkedIncrement(&totals.tpm.unknownAttempts); err != nil {
			return err
		}
	}
	if attempt.cost.Valid {
		if !usageKnown || !attempt.currency.Valid || !validObservationCurrency(attempt.currency.String) || attempt.cost.Int64 < 0 {
			return observationError(ObservationSchema)
		}
		currency := totals.currencies[attempt.currency.String]
		var err error
		if currency.cost, err = checkedAdd(currency.cost, attempt.cost.Int64); err != nil {
			return err
		}
		if err := checkedIncrement(&currency.attempts); err != nil {
			return err
		}
		totals.currencies[attempt.currency.String] = currency
		return checkedIncrement(&totals.cost.knownAttempts)
	}
	return checkedIncrement(&totals.cost.unknownAttempts)
}

func makeObservationItem(snapshot observationSnapshot, totals observationTotals) (ObservationItem, error) {
	item := ObservationItem{
		Snapshot: ObservationSnapshot{
			SettingsRevision: canonicalInt(snapshot.key.settingsRevision), ScopeKind: snapshot.key.scopeKind,
			ScopeID: snapshot.key.scopeID, PolicyID: snapshot.key.policyID, PolicyRevision: canonicalInt(snapshot.key.policyRevision),
			GroupRevision: canonicalOptionalInt(snapshot.key.groupRevision), ShadowTPM: canonicalOptionalInt(snapshot.shadowTPM),
			ShadowCostMicro: canonicalOptionalInt(snapshot.shadowCostMicro), ShadowCurrency: snapshot.currency, ShadowWindow: snapshot.window,
		},
		ScopeTotals: ObservationScopeTotals{
			TPM: ObservationTPMTotals{
				KnownTokens: canonicalInt(totals.tokens), KnownAttempts: canonicalInt(totals.tpm.knownAttempts),
				UnknownTokenAttempts: canonicalInt(totals.tpm.unknownAttempts), PendingAttempts: canonicalInt(totals.tpm.pendingAttempts),
				PendingRequestsWithoutAttempt: canonicalInt(totals.tpm.pendingRequestsWithoutAttempt), ZeroAttemptRequests: canonicalInt(totals.tpm.zeroAttemptRequests),
			},
			Cost: ObservationCostTotals{
				KnownAttempts: canonicalInt(totals.cost.knownAttempts), UnknownCostAttempts: canonicalInt(totals.cost.unknownAttempts),
				PendingAttempts: canonicalInt(totals.cost.pendingAttempts), PendingRequestsWithoutAttempt: canonicalInt(totals.cost.pendingRequestsWithoutAttempt),
				ZeroAttemptRequests: canonicalInt(totals.cost.zeroAttemptRequests), ByCurrency: make([]ObservationCurrencyTotal, 0, len(totals.currencies)),
			},
		},
	}
	for _, currency := range sortedCurrencyKeys(totals.currencies) {
		value := totals.currencies[currency]
		item.ScopeTotals.Cost.ByCurrency = append(item.ScopeTotals.Cost.ByCurrency, ObservationCurrencyTotal{
			Currency: currency, KnownCostMicro: canonicalInt(value.cost), Attempts: canonicalInt(value.attempts),
		})
	}
	if snapshot.shadowTPM != nil {
		state := observationState(totals.tokens, *snapshot.shadowTPM,
			totals.tpm.unknownAttempts, totals.tpm.pendingAttempts, totals.tpm.pendingRequestsWithoutAttempt)
		item.Interpretation.TPMState = &state
	}
	if snapshot.shadowCostMicro != nil {
		configured := totals.currencies[snapshot.currency]
		incomparable := int64(0)
		for currency, value := range totals.currencies {
			if currency != snapshot.currency {
				var err error
				incomparable, err = checkedAdd(incomparable, value.attempts)
				if err != nil {
					return ObservationItem{}, err
				}
			}
		}
		state := observationState(configured.cost, *snapshot.shadowCostMicro, totals.cost.unknownAttempts,
			totals.cost.pendingAttempts, totals.cost.pendingRequestsWithoutAttempt, incomparable)
		item.Interpretation.CostState = &state
		formatted := canonicalInt(incomparable)
		item.Interpretation.IncomparableCurrencyAttempts = &formatted
	}
	return item, nil
}

func observationState(known, threshold int64, unknown ...int64) ObservationState {
	if known > threshold {
		return ObservationExceeded
	}
	for _, value := range unknown {
		if value > 0 {
			return ObservationUnknown
		}
	}
	return ObservationBelow
}

func encodeObservationCursor(query ObservationQuery, windowEnd time.Time, key observationKey) (string, error) {
	cursor := observationCursor{
		Version: observationCursorV1, WindowEnd: formatTime(windowEnd), FilterFingerprint: observationFilterFingerprint(query),
		LastScopeKind: string(key.scopeKind),
		LastScopeID:   key.scopeID, LastPolicyID: key.policyID, LastPolicyRevision: key.policyRevision,
		LastGroupRevision: key.groupRevision, LastSettingsRevision: key.settingsRevision,
	}
	encoded, err := marshalObservationCursor(cursor)
	if err != nil {
		return "", observationError(ObservationUnavailable)
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeObservationCursor(encoded string, query ObservationQuery) (observationCursor, error) {
	if encoded == "" || len(encoded) > observationCursorLimit {
		return observationCursor{}, observationError(ObservationInvalidCursor)
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw) > observationCursorLimit || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return observationCursor{}, observationError(ObservationInvalidCursor)
	}
	var cursor observationCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return observationCursor{}, observationError(ObservationInvalidCursor)
	}
	canonical, err := marshalObservationCursor(cursor)
	if err != nil || !bytes.Equal(raw, canonical) {
		return observationCursor{}, observationError(ObservationInvalidCursor)
	}
	if cursor.Version != observationCursorV1 || cursor.FilterFingerprint != observationFilterFingerprint(query) {
		return observationCursor{}, observationError(ObservationInvalidCursor)
	}
	key := cursorKey(cursor)
	if !validObservationKey(key) {
		return observationCursor{}, observationError(ObservationInvalidCursor)
	}
	if parsed, err := parseStoredTime(cursor.WindowEnd); err != nil || parsed.Before(time.Unix(0, 0).UTC()) {
		return observationCursor{}, observationError(ObservationInvalidCursor)
	}
	return cursor, nil
}

func observationFilterFingerprint(query ObservationQuery) string {
	sum := sha256.Sum256([]byte(string(query.ScopeKind) + "\x00" + query.ScopeID + "\x00" + query.PolicyID))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func marshalObservationCursor(cursor observationCursor) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(cursor); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'}), nil
}

func cursorKey(cursor observationCursor) observationKey {
	return observationKey{scopeKind: ScopeKind(cursor.LastScopeKind), scopeID: cursor.LastScopeID, policyID: cursor.LastPolicyID,
		policyRevision: cursor.LastPolicyRevision, groupRevision: cursor.LastGroupRevision, settingsRevision: cursor.LastSettingsRevision}
}

func validObservationKey(key observationKey) bool {
	return validScopeKind(key.scopeKind) && validMetadata(key.scopeID, 256) && validMetadata(key.policyID, 256) &&
		validRevision(key.policyRevision) && validRevision(key.settingsRevision) &&
		(key.scopeKind == ScopeGroup && key.groupRevision != nil && validRevision(*key.groupRevision) || key.scopeKind != ScopeGroup && key.groupRevision == nil)
}

func sortedCurrencyKeys(values map[string]observationCurrency) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validObservationCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for index := range currency {
		if currency[index] < 'A' || currency[index] > 'Z' {
			return false
		}
	}
	return true
}

func canonicalOptionalInt(value *int64) *string {
	if value == nil {
		return nil
	}
	formatted := canonicalInt(*value)
	return &formatted
}

func canonicalInt(value int64) string { return strconv.FormatInt(value, 10) }

func checkedIncrement(value *int64) error {
	if *value == math.MaxInt64 {
		return observationError(ObservationOverflow)
	}
	*value++
	return nil
}

func checkedSum(values ...int64) (int64, error) {
	total := int64(0)
	var err error
	for _, value := range values {
		if value < 0 {
			return 0, observationError(ObservationSchema)
		}
		total, err = checkedAdd(total, value)
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

func checkedAdd(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right || right < 0 && left < math.MinInt64-right {
		return 0, observationError(ObservationOverflow)
	}
	return left + right, nil
}

func observationError(kind ObservationErrorKind) error { return &ObservationError{Kind: kind} }

func classifyObservationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrSchema):
		return observationError(ObservationSchema)
	case errors.Is(err, ErrInvalid):
		return observationError(ObservationInvalidQuery)
	default:
		return observationError(ObservationUnavailable)
	}
}
