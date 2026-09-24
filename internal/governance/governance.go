// Package governance implements CPA Cloud's metadata-only employee request
// admission core. It is independently implemented from this repository's
// governance contract and does not authenticate callers or execute models.
package governance

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cpacloud.local/server/internal/accounting"
)

var (
	ErrInvalid      = errors.New("invalid governance metadata")
	ErrConflict     = errors.New("governance state conflict")
	ErrNotFound     = errors.New("governance record not found")
	ErrLeaseExpired = errors.New("governance lease expired")
	ErrUnavailable  = errors.New("governance storage unavailable")
	ErrSchema       = errors.New("invalid governance schema")
)

const MaxRevision int64 = 9007199254740991

const ShadowWindowRolling24h = "rolling_24h"

type ScopeKind string

const (
	ScopeEmployee ScopeKind = "employee"
	ScopeKey      ScopeKind = "key"
	ScopeGroup    ScopeKind = "group"
)

type DecisionCode string

const (
	DecisionAllowed             DecisionCode = "allowed"
	DecisionRPMExceeded         DecisionCode = "rpm_exceeded"
	DecisionConcurrencyExceeded DecisionCode = "concurrency_exceeded"
	DecisionPolicyChanged       DecisionCode = "policy_changed"
	DecisionAlreadyTerminal     DecisionCode = "already_terminal"
	DecisionLeaseExpired        DecisionCode = "lease_expired"
)

type Config struct {
	LeaseTTL time.Duration
}

type Coordinator struct {
	db       *sql.DB
	leaseTTL time.Duration
}

type Settings struct {
	Enabled                    bool
	BudgetEnabled              bool
	Revision                   int64
	LastEffectiveAdmissionTime *time.Time
	UpdatedAt                  time.Time
}

// SettingsChange updates one or both governance gates with one revision CAS.
// Nil fields retain their stored value.
type SettingsChange struct {
	ExpectedRevision int64
	Enabled          *bool
	BudgetEnabled    *bool
	UpdatedAt        time.Time
}

type SettingsUpdate struct {
	ExpectedRevision int64
	Enabled          bool
	UpdatedAt        time.Time
}

type Subject struct {
	EmployeeID  string
	KeyID       string
	PublicModel string
	Protocol    accounting.UsageProtocol
}

// ScopeSnapshot is an immutable result of the caller's policy lookup. This
// package validates its shape and subject binding, but does not treat it as
// proof that authentication, group membership, or policy lookup occurred.
type ScopeSnapshot struct {
	Kind             ScopeKind
	ID               string
	PolicyID         string
	PolicyRevision   int64
	GroupRevision    *int64
	RPMLimit         *int64
	ConcurrencyLimit *int64
	HardTPM          *int64
	HardCostMicro    *int64
	HardCurrency     string
	HardWindow       string
	UnknownMode      string
	ShadowTPM        *int64
	ShadowCostMicro  *int64
	ShadowCurrency   string
	ShadowWindow     string
}

type AdmissionStart struct {
	RequestID        string
	Subject          Subject
	SettingsRevision int64
	SnapshotComplete bool
	// BudgetSnapshot records a selector-aware strict budget request even when
	// no legacy RPM/concurrency scope applies. The service must freeze its
	// selector rows in the same caller-owned transaction before commit.
	BudgetSnapshot bool
	Scopes         []ScopeSnapshot
	StartedAt      time.Time
	ObservedAt     time.Time
}

type Lease struct {
	RequestID          string
	EffectiveStartedAt time.Time
	ExpiresAt          time.Time
}

type Decision struct {
	Allowed bool
	Code    DecisionCode
	RetryAt *time.Time
}

type Finish struct {
	RequestID  string
	Status     accounting.Status
	FinishedAt time.Time
}

type Renew struct {
	RequestID         string
	ExpectedExpiresAt time.Time
	ObservedAt        time.Time
}

type RecoveryResult struct {
	Interrupted int64
}

type storedRequest struct {
	id                  string
	employeeID          string
	keyID               string
	publicModel         string
	protocol            accounting.UsageProtocol
	settingsRevision    int64
	budgetEnabled       bool
	observedStartedAt   time.Time
	effectiveStartedAt  time.Time
	effectiveLeaseAt    time.Time
	expiresAt           time.Time
	observedFinishedAt  *time.Time
	effectiveFinishedAt *time.Time
	releasedAt          *time.Time
	status              accounting.Status
}

func New(db *sql.DB, config Config) (*Coordinator, error) {
	if db == nil || config.LeaseTTL <= 0 || config.LeaseTTL > 365*24*time.Hour {
		return nil, ErrInvalid
	}
	return &Coordinator{db: db, leaseTTL: config.LeaseTTL}, nil
}

func (c *Coordinator) Settings(ctx context.Context) (Settings, error) {
	if c == nil || c.db == nil || ctx == nil {
		return Settings{}, ErrInvalid
	}
	return readSettings(ctx, c.db)
}

// SetEnabledTx is a revision CAS primitive. It does not provide management
// operation-ID deduplication; a caller with an uncertain commit must reload
// settings rather than treating a repeated CAS as an idempotent network retry.
func (c *Coordinator) SetEnabledTx(ctx context.Context, tx *sql.Tx, input SettingsUpdate) (Settings, error) {
	enabled := input.Enabled
	return c.SetSettingsTx(ctx, tx, SettingsChange{ExpectedRevision: input.ExpectedRevision, Enabled: &enabled, UpdatedAt: input.UpdatedAt})
}

// SetSettingsTx atomically updates governance gates while preserving omitted
// fields. Operation-ID idempotency remains the management caller's concern.
func (c *Coordinator) SetSettingsTx(ctx context.Context, tx *sql.Tx, input SettingsChange) (Settings, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validRevision(input.ExpectedRevision) || !validUTCTime(input.UpdatedAt) {
		return Settings{}, ErrInvalid
	}
	if input.Enabled == nil && input.BudgetEnabled == nil {
		return Settings{}, ErrInvalid
	}
	stored, err := readSettings(ctx, tx)
	if err != nil {
		return Settings{}, err
	}
	enabled := stored.Enabled
	budgetEnabled := stored.BudgetEnabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if input.BudgetEnabled != nil {
		budgetEnabled = *input.BudgetEnabled
	}
	updatedAt := formatTime(input.UpdatedAt)
	result, err := tx.ExecContext(ctx, `UPDATE governance_settings
		SET enabled=?,budget_enabled=?,revision=revision+1,updated_at=?
		WHERE singleton=1 AND revision=? AND revision<?`, boolInteger(enabled), boolInteger(budgetEnabled), updatedAt, input.ExpectedRevision, MaxRevision)
	if err != nil {
		return Settings{}, ErrUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return Settings{}, ErrUnavailable
	}
	if changed != 1 {
		return Settings{}, ErrConflict
	}
	return readSettings(ctx, tx)
}

// AdmitTx atomically checks every caller-supplied scope and writes one logical
// request reservation. The caller owns authentication, authorization, policy
// lookup, the transaction, and its commit or rollback.
func (c *Coordinator) AdmitTx(ctx context.Context, tx *sql.Tx, input AdmissionStart) (*Lease, Decision, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil {
		return nil, Decision{}, ErrInvalid
	}
	scopes, err := validateAdmission(input)
	if err != nil {
		return nil, Decision{}, err
	}
	if err := lockSettingsRow(ctx, tx); err != nil {
		return nil, Decision{}, err
	}
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return nil, Decision{}, err
	}

	existing, found, err := loadRequest(ctx, tx, input.RequestID)
	if err != nil {
		return nil, Decision{}, err
	}
	if found {
		storedScopes, err := loadScopes(ctx, tx, input.RequestID)
		if err != nil {
			return nil, Decision{}, err
		}
		if !sameAdmission(existing, storedScopes, input, scopes) {
			return nil, Decision{}, ErrConflict
		}
		lease := &Lease{RequestID: existing.id, EffectiveStartedAt: existing.effectiveStartedAt, ExpiresAt: existing.expiresAt}
		if existing.status != accounting.StatusPending {
			return lease, Decision{Code: DecisionAlreadyTerminal}, nil
		}
		current := effectiveClock(input.ObservedAt, settings.LastEffectiveAdmissionTime, existing.effectiveLeaseAt)
		if existing.releasedAt != nil || !existing.expiresAt.After(current) {
			return lease, Decision{Code: DecisionLeaseExpired}, nil
		}
		return lease, Decision{Allowed: true, Code: DecisionAllowed}, nil
	}

	if settings.Revision != input.SettingsRevision {
		return nil, Decision{Code: DecisionPolicyChanged}, nil
	}
	if !settings.Enabled || len(scopes) == 0 && !input.BudgetSnapshot {
		return nil, Decision{Allowed: true, Code: DecisionAllowed}, nil
	}

	effective := effectiveClock(input.ObservedAt, settings.LastEffectiveAdmissionTime, input.StartedAt)
	expires := effective.Add(c.leaseTTL)
	if !validUTCTime(expires) || !expires.After(effective) {
		return nil, Decision{}, ErrInvalid
	}

	var retryAt *time.Time
	for _, scope := range scopes {
		if scope.RPMLimit == nil {
			continue
		}
		count, latest, err := rpmState(ctx, tx, scope, effective)
		if err != nil {
			return nil, Decision{}, err
		}
		if count >= *scope.RPMLimit {
			candidate := latest.Add(time.Minute)
			if retryAt == nil || candidate.After(*retryAt) {
				retryAt = &candidate
			}
		}
	}
	if retryAt != nil {
		return nil, Decision{Code: DecisionRPMExceeded, RetryAt: retryAt}, nil
	}
	for _, scope := range scopes {
		if scope.ConcurrencyLimit == nil {
			continue
		}
		count, err := concurrencyCount(ctx, tx, scope, effective)
		if err != nil {
			return nil, Decision{}, err
		}
		if count >= *scope.ConcurrencyLimit {
			return nil, Decision{Code: DecisionConcurrencyExceeded}, nil
		}
	}

	formattedEffective := formatTime(effective)
	if _, err := tx.ExecContext(ctx, `UPDATE governance_settings SET last_effective_admission_at=? WHERE singleton=1`, formattedEffective); err != nil {
		return nil, Decision{}, ErrUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO governance_requests(
		id,employee_id,key_id,public_model,protocol,settings_revision,budget_enabled,observed_started_at,effective_started_at,effective_lease_at,expires_at,status
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,'pending')`, input.RequestID, input.Subject.EmployeeID, input.Subject.KeyID, input.Subject.PublicModel,
		string(input.Subject.Protocol), input.SettingsRevision, boolInteger(settings.BudgetEnabled), formatTime(input.StartedAt), formattedEffective, formattedEffective, formatTime(expires)); err != nil {
		return nil, Decision{}, ErrUnavailable
	}
	for _, scope := range scopes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO governance_request_scopes(
			request_id,scope_kind,scope_id,policy_id,policy_revision,group_revision,rpm_limit,concurrency_limit,
			hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode,shadow_tpm,shadow_cost_micro,shadow_currency,shadow_window
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, input.RequestID, string(scope.Kind), scope.ID, scope.PolicyID, scope.PolicyRevision,
			nullableLimit(scope.GroupRevision), nullableLimit(scope.RPMLimit), nullableLimit(scope.ConcurrencyLimit),
			nullableLimit(scope.HardTPM), nullableLimit(scope.HardCostMicro), scope.HardCurrency, scope.HardWindow, normalizeUnknownMode(scope.UnknownMode),
			nullableLimit(scope.ShadowTPM), nullableLimit(scope.ShadowCostMicro), scope.ShadowCurrency, scope.ShadowWindow); err != nil {
			return nil, Decision{}, ErrUnavailable
		}
	}
	return &Lease{RequestID: input.RequestID, EffectiveStartedAt: effective, ExpiresAt: expires}, Decision{Allowed: true, Code: DecisionAllowed}, nil
}

func (c *Coordinator) FinishTx(ctx context.Context, tx *sql.Tx, input Finish) error {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validMetadata(input.RequestID, 256) || !terminalStatus(input.Status) || !validUTCTime(input.FinishedAt) {
		return ErrInvalid
	}
	if err := lockSettingsRow(ctx, tx); err != nil {
		return err
	}
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return err
	}
	request, found, err := loadRequest(ctx, tx, input.RequestID)
	if err != nil {
		return err
	}
	if !found {
		return ErrNotFound
	}
	observed := input.FinishedAt.UTC()
	if request.status != accounting.StatusPending {
		if request.status == input.Status && request.observedFinishedAt != nil && request.observedFinishedAt.Equal(observed) {
			return nil
		}
		return ErrConflict
	}
	effective := effectiveClock(observed, settings.LastEffectiveAdmissionTime, request.effectiveLeaseAt)
	result, err := tx.ExecContext(ctx, `UPDATE governance_requests SET
		observed_finished_at=?,effective_finished_at=?,released_at=?,status=?
		WHERE id=? AND status='pending'`, formatTime(observed), formatTime(effective), formatTime(effective), string(input.Status), input.RequestID)
	if err != nil {
		return ErrUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return ErrUnavailable
	}
	if changed != 1 {
		return ErrConflict
	}
	return nil
}

// RenewTx extends an active request lease using an expiry compare-and-swap.
// It never reopens terminal, released, recovered, or expired requests. Replaying
// AdmitTx does not call this method and cannot extend a lease.
func (c *Coordinator) RenewTx(ctx context.Context, tx *sql.Tx, input Renew) (*Lease, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validMetadata(input.RequestID, 256) ||
		!validUTCTime(input.ExpectedExpiresAt) || !validUTCTime(input.ObservedAt) {
		return nil, ErrInvalid
	}
	if err := lockSettingsRow(ctx, tx); err != nil {
		return nil, err
	}
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return nil, err
	}
	request, found, err := loadRequest(ctx, tx, input.RequestID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrNotFound
	}
	if request.status != accounting.StatusPending || request.releasedAt != nil {
		return nil, ErrConflict
	}
	if !request.expiresAt.Equal(input.ExpectedExpiresAt.UTC()) {
		return nil, ErrConflict
	}
	current := effectiveClock(input.ObservedAt, settings.LastEffectiveAdmissionTime, request.effectiveLeaseAt)
	if !request.expiresAt.After(current) {
		return nil, ErrLeaseExpired
	}
	newExpiry := current.Add(c.leaseTTL)
	if !validUTCTime(newExpiry) || !newExpiry.After(current) {
		return nil, ErrInvalid
	}
	if !newExpiry.After(request.expiresAt) {
		return &Lease{RequestID: request.id, EffectiveStartedAt: request.effectiveStartedAt, ExpiresAt: request.expiresAt}, nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE governance_requests SET effective_lease_at=?,expires_at=?
		WHERE id=? AND status='pending' AND released_at IS NULL AND expires_at=? AND effective_lease_at=?`,
		formatTime(current), formatTime(newExpiry), request.id, formatTime(input.ExpectedExpiresAt), formatTime(request.effectiveLeaseAt))
	if err != nil {
		return nil, ErrUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, ErrUnavailable
	}
	if changed != 1 {
		return nil, ErrConflict
	}
	return &Lease{RequestID: request.id, EffectiveStartedAt: request.effectiveStartedAt, ExpiresAt: newExpiry}, nil
}

func validateAdmission(input AdmissionStart) ([]ScopeSnapshot, error) {
	if !validMetadata(input.RequestID, 256) || !validMetadata(input.Subject.EmployeeID, 256) || !validMetadata(input.Subject.KeyID, 256) ||
		!validMetadata(input.Subject.PublicModel, 256) || !validProtocol(input.Subject.Protocol) || !validRevision(input.SettingsRevision) ||
		!input.SnapshotComplete || !validUTCTime(input.StartedAt) || !validUTCTime(input.ObservedAt) {
		return nil, ErrInvalid
	}
	scopes := append([]ScopeSnapshot(nil), input.Scopes...)
	sort.Slice(scopes, func(i, j int) bool {
		if scopes[i].Kind != scopes[j].Kind {
			return scopes[i].Kind < scopes[j].Kind
		}
		return scopes[i].ID < scopes[j].ID
	})
	for index, scope := range scopes {
		scope.UnknownMode = normalizeUnknownMode(scope.UnknownMode)
		scopes[index] = scope
		if !validScopeKind(scope.Kind) || !validMetadata(scope.ID, 256) || !validMetadata(scope.PolicyID, 256) || !validRevision(scope.PolicyRevision) ||
			!validScopeGroupRevision(scope) || !validLimit(scope.RPMLimit) || !validLimit(scope.ConcurrencyLimit) ||
			!validSafeLimit(scope.HardTPM) || !validLimit(scope.HardCostMicro) || !validHardCost(scope) || !validUnknownMode(scope.UnknownMode) ||
			!validLimit(scope.ShadowTPM) || !validLimit(scope.ShadowCostMicro) || !validShadowCost(scope) ||
			scope.RPMLimit == nil && scope.ConcurrencyLimit == nil && scope.HardTPM == nil && scope.HardCostMicro == nil && scope.ShadowTPM == nil && scope.ShadowCostMicro == nil {
			return nil, ErrInvalid
		}
		if scope.Kind == ScopeEmployee && scope.ID != input.Subject.EmployeeID || scope.Kind == ScopeKey && scope.ID != input.Subject.KeyID {
			return nil, ErrInvalid
		}
		if index > 0 && scopes[index-1].Kind == scope.Kind && scopes[index-1].ID == scope.ID {
			return nil, ErrInvalid
		}
	}
	return scopes, nil
}

func sameAdmission(stored storedRequest, storedScopes []ScopeSnapshot, input AdmissionStart, scopes []ScopeSnapshot) bool {
	return stored.employeeID == input.Subject.EmployeeID && stored.keyID == input.Subject.KeyID && stored.publicModel == input.Subject.PublicModel &&
		stored.protocol == input.Subject.Protocol && stored.settingsRevision == input.SettingsRevision && stored.observedStartedAt.Equal(input.StartedAt.UTC()) &&
		sameScopes(storedScopes, scopes)
}

func sameScopes(left, right []ScopeSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Kind != right[index].Kind || left[index].ID != right[index].ID || left[index].PolicyID != right[index].PolicyID ||
			left[index].PolicyRevision != right[index].PolicyRevision || !sameLimit(left[index].GroupRevision, right[index].GroupRevision) ||
			!sameLimit(left[index].RPMLimit, right[index].RPMLimit) || !sameLimit(left[index].ConcurrencyLimit, right[index].ConcurrencyLimit) ||
			!sameLimit(left[index].HardTPM, right[index].HardTPM) || !sameLimit(left[index].HardCostMicro, right[index].HardCostMicro) ||
			left[index].HardCurrency != right[index].HardCurrency || left[index].HardWindow != right[index].HardWindow ||
			normalizeUnknownMode(left[index].UnknownMode) != normalizeUnknownMode(right[index].UnknownMode) ||
			!sameLimit(left[index].ShadowTPM, right[index].ShadowTPM) || !sameLimit(left[index].ShadowCostMicro, right[index].ShadowCostMicro) ||
			left[index].ShadowCurrency != right[index].ShadowCurrency || left[index].ShadowWindow != right[index].ShadowWindow {
			return false
		}
	}
	return true
}

func sameLimit(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func validLimit(value *int64) bool { return value == nil || *value > 0 }

func validSafeLimit(value *int64) bool { return value == nil || *value > 0 && *value <= MaxRevision }

func validScopeGroupRevision(scope ScopeSnapshot) bool {
	if scope.Kind == ScopeGroup {
		return scope.GroupRevision != nil && validRevision(*scope.GroupRevision)
	}
	return scope.GroupRevision == nil
}

func validShadowCost(scope ScopeSnapshot) bool {
	if scope.ShadowCostMicro == nil {
		return scope.ShadowCurrency == "" && scope.ShadowWindow == ""
	}
	if len(scope.ShadowCurrency) != 3 || scope.ShadowWindow != ShadowWindowRolling24h {
		return false
	}
	for index := 0; index < len(scope.ShadowCurrency); index++ {
		if scope.ShadowCurrency[index] < 'A' || scope.ShadowCurrency[index] > 'Z' {
			return false
		}
	}
	return true
}

func validHardCost(scope ScopeSnapshot) bool {
	if scope.HardCostMicro == nil {
		return scope.HardCurrency == "" && scope.HardWindow == ""
	}
	if len(scope.HardCurrency) != 3 || scope.HardWindow != ShadowWindowRolling24h {
		return false
	}
	for index := 0; index < len(scope.HardCurrency); index++ {
		if scope.HardCurrency[index] < 'A' || scope.HardCurrency[index] > 'Z' {
			return false
		}
	}
	return true
}

func normalizeUnknownMode(value string) string {
	if value == "" {
		return "shadow"
	}
	return value
}

func validUnknownMode(value string) bool { return value == "shadow" || value == "deny_unknown" }

func validRevision(value int64) bool { return value >= 1 && value <= MaxRevision }

func validScopeKind(kind ScopeKind) bool {
	return kind == ScopeEmployee || kind == ScopeKey || kind == ScopeGroup
}

func validProtocol(protocol accounting.UsageProtocol) bool {
	switch protocol {
	case accounting.ProtocolOpenAIChatCompletions, accounting.ProtocolOpenAIResponses, accounting.ProtocolAnthropicMessages, accounting.ProtocolGeminiGenerateContent:
		return true
	default:
		return false
	}
}

func terminalStatus(status accounting.Status) bool {
	return status == accounting.StatusSucceeded || status == accounting.StatusFailed || status == accounting.StatusCancelled || status == accounting.StatusInterrupted
}

func validMetadata(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validTime(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 1 && value.Year() <= 9999
}

func validUTCTime(value time.Time) bool {
	if !validTime(value) {
		return false
	}
	_, offset := value.Zone()
	return offset == 0
}

func effectiveClock(observed time.Time, last *time.Time, floor time.Time) time.Time {
	effective := observed.UTC()
	if last != nil && last.After(effective) {
		effective = *last
	}
	if floor.After(effective) {
		effective = floor
	}
	return effective
}

func formatTime(value time.Time) string { return value.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func parseStoredTime(value string) (time.Time, error) {
	parsed, err := time.Parse("2006-01-02T15:04:05.000000000Z", value)
	if err != nil {
		return time.Time{}, ErrSchema
	}
	return parsed, nil
}

func pointerTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	parsed, err := parseStoredTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nullableLimit(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func readSettings(ctx context.Context, query queryRower) (Settings, error) {
	var enabled, budgetEnabled int
	var revision int64
	var last sql.NullString
	var updated string
	if err := query.QueryRowContext(ctx, `SELECT enabled,budget_enabled,revision,last_effective_admission_at,updated_at FROM governance_settings WHERE singleton=1`).Scan(&enabled, &budgetEnabled, &revision, &last, &updated); err != nil {
		return Settings{}, ErrUnavailable
	}
	if enabled != 0 && enabled != 1 || budgetEnabled != 0 && budgetEnabled != 1 || !validRevision(revision) {
		return Settings{}, ErrSchema
	}
	updatedAt, err := parseStoredTime(updated)
	if err != nil {
		return Settings{}, err
	}
	lastAt, err := pointerTime(last)
	if err != nil {
		return Settings{}, err
	}
	return Settings{Enabled: enabled == 1, BudgetEnabled: budgetEnabled == 1, Revision: revision, LastEffectiveAdmissionTime: lastAt, UpdatedAt: updatedAt}, nil
}

func lockSettingsRow(ctx context.Context, tx *sql.Tx) error {
	result, err := tx.ExecContext(ctx, `UPDATE governance_settings SET last_effective_admission_at=last_effective_admission_at WHERE singleton=1`)
	if err != nil {
		return ErrUnavailable
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return ErrUnavailable
	}
	return nil
}

func loadRequest(ctx context.Context, tx *sql.Tx, id string) (storedRequest, bool, error) {
	var row storedRequest
	var protocol string
	var budgetEnabled int
	var observedStart, effectiveStart, effectiveLease, expires string
	var observedFinish, effectiveFinish, released sql.NullString
	var status string
	err := tx.QueryRowContext(ctx, `SELECT id,employee_id,key_id,public_model,protocol,settings_revision,budget_enabled,
		observed_started_at,effective_started_at,effective_lease_at,expires_at,observed_finished_at,effective_finished_at,released_at,status
		FROM governance_requests WHERE id=?`, id).Scan(&row.id, &row.employeeID, &row.keyID, &row.publicModel, &protocol, &row.settingsRevision,
		&budgetEnabled, &observedStart, &effectiveStart, &effectiveLease, &expires, &observedFinish, &effectiveFinish, &released, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return storedRequest{}, false, nil
	}
	if err != nil {
		return storedRequest{}, false, ErrUnavailable
	}
	row.protocol = accounting.UsageProtocol(protocol)
	if budgetEnabled != 0 && budgetEnabled != 1 {
		return storedRequest{}, false, ErrSchema
	}
	row.budgetEnabled = budgetEnabled == 1
	row.status = accounting.Status(status)
	row.observedStartedAt, err = parseStoredTime(observedStart)
	if err != nil {
		return storedRequest{}, false, err
	}
	row.effectiveStartedAt, err = parseStoredTime(effectiveStart)
	if err != nil {
		return storedRequest{}, false, err
	}
	row.effectiveLeaseAt, err = parseStoredTime(effectiveLease)
	if err != nil {
		return storedRequest{}, false, err
	}
	row.expiresAt, err = parseStoredTime(expires)
	if err != nil {
		return storedRequest{}, false, err
	}
	if row.observedFinishedAt, err = pointerTime(observedFinish); err != nil {
		return storedRequest{}, false, err
	}
	if row.effectiveFinishedAt, err = pointerTime(effectiveFinish); err != nil {
		return storedRequest{}, false, err
	}
	if row.releasedAt, err = pointerTime(released); err != nil {
		return storedRequest{}, false, err
	}
	return row, true, nil
}

func loadScopes(ctx context.Context, tx *sql.Tx, requestID string) ([]ScopeSnapshot, error) {
	rows, err := tx.QueryContext(ctx, `SELECT scope_kind,scope_id,policy_id,policy_revision,group_revision,rpm_limit,concurrency_limit,
		hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode,shadow_tpm,shadow_cost_micro,shadow_currency,shadow_window
		FROM governance_request_scopes WHERE request_id=? ORDER BY scope_kind,scope_id`, requestID)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	scopes := make([]ScopeSnapshot, 0)
	for rows.Next() {
		var scope ScopeSnapshot
		var groupRevision, rpm, concurrency, hardTPM, hardCost, shadowTPM, shadowCost sql.NullInt64
		if err := rows.Scan(&scope.Kind, &scope.ID, &scope.PolicyID, &scope.PolicyRevision, &groupRevision, &rpm, &concurrency,
			&hardTPM, &hardCost, &scope.HardCurrency, &scope.HardWindow, &scope.UnknownMode,
			&shadowTPM, &shadowCost, &scope.ShadowCurrency, &scope.ShadowWindow); err != nil {
			return nil, ErrUnavailable
		}
		setOptionalInt64(&scope.GroupRevision, groupRevision)
		setOptionalInt64(&scope.RPMLimit, rpm)
		setOptionalInt64(&scope.ConcurrencyLimit, concurrency)
		setOptionalInt64(&scope.HardTPM, hardTPM)
		setOptionalInt64(&scope.HardCostMicro, hardCost)
		setOptionalInt64(&scope.ShadowTPM, shadowTPM)
		setOptionalInt64(&scope.ShadowCostMicro, shadowCost)
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return scopes, nil
}

func setOptionalInt64(target **int64, value sql.NullInt64) {
	if value.Valid {
		copy := value.Int64
		*target = &copy
	}
}

func rpmState(ctx context.Context, tx *sql.Tx, scope ScopeSnapshot, effective time.Time) (int64, time.Time, error) {
	lower := formatTime(effective.Add(-time.Minute))
	upper := formatTime(effective)
	var count int64
	var latest sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*),MAX(r.effective_started_at)
		FROM governance_request_scopes s JOIN governance_requests r ON r.id=s.request_id
		WHERE s.scope_kind=? AND s.scope_id=? AND r.effective_started_at>? AND r.effective_started_at<=?`,
		string(scope.Kind), scope.ID, lower, upper).Scan(&count, &latest)
	if err != nil {
		return 0, time.Time{}, ErrUnavailable
	}
	if count == 0 {
		return 0, time.Time{}, nil
	}
	parsed, err := parseStoredTime(latest.String)
	if err != nil {
		return 0, time.Time{}, err
	}
	return count, parsed, nil
}

func concurrencyCount(ctx context.Context, tx *sql.Tx, scope ScopeSnapshot, effective time.Time) (int64, error) {
	var count int64
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM governance_request_scopes s JOIN governance_requests r ON r.id=s.request_id
		WHERE s.scope_kind=? AND s.scope_id=? AND r.released_at IS NULL AND r.expires_at>?`,
		string(scope.Kind), scope.ID, formatTime(effective)).Scan(&count)
	if err != nil {
		return 0, ErrUnavailable
	}
	return count, nil
}
