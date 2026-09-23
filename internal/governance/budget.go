package governance

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cpacloud.local/server/internal/accounting"
)

type BudgetProofType string

const (
	BudgetProofFourBuckets            BudgetProofType = "four_buckets"
	BudgetProofMutuallyExclusiveInput BudgetProofType = "mutually_exclusive_input"
)

type BudgetLifecycle string

const (
	BudgetReserved           BudgetLifecycle = "reserved"
	BudgetMayHaveSent        BudgetLifecycle = "may_have_sent"
	BudgetSettled            BudgetLifecycle = "settled"
	BudgetReleasedNotStarted BudgetLifecycle = "released_not_started"
	BudgetInterrupted        BudgetLifecycle = "interrupted"
)

type BudgetSettleMode string

const (
	BudgetSettleFromAttempt       BudgetSettleMode = "from_attempt"
	BudgetSettleReleaseNotStarted BudgetSettleMode = "release_not_started"
)

type BudgetDecisionCode string

const (
	BudgetDecisionNotEnforced        BudgetDecisionCode = "not_enforced"
	BudgetDecisionReserved           BudgetDecisionCode = "reserved"
	BudgetDecisionTPMExceeded        BudgetDecisionCode = "tpm_exceeded"
	BudgetDecisionCostExceeded       BudgetDecisionCode = "cost_exceeded"
	BudgetDecisionBoundUnavailable   BudgetDecisionCode = "bound_unavailable"
	BudgetDecisionProfileQuarantined BudgetDecisionCode = "profile_quarantined"
)

type BudgetProof struct {
	Type                   BudgetProofType
	ActualModel            string
	AccountRevision        int64
	PoolRevision           int64
	TransformRevision      string
	BounderID              string
	BounderRevision        int64
	FourBuckets            *accounting.UpperUsage
	MutuallyExclusiveInput *accounting.MutuallyExclusiveInputUpperUsage
}

type BudgetReserve struct {
	RequestID  string
	AttemptID  string
	Proof      BudgetProof
	ObservedAt time.Time
}

type BudgetMutation struct {
	AttemptID  string
	ObservedAt time.Time
}

type BudgetRenew struct {
	AttemptID         string
	ExpectedExpiresAt time.Time
	ObservedAt        time.Time
}

type BudgetSettle struct {
	AttemptID  string
	ObservedAt time.Time
	Mode       BudgetSettleMode
}

type BudgetScope struct {
	Kind             ScopeKind
	ID               string
	PolicyID         string
	PolicyRevision   int64
	GroupRevision    *int64
	SettingsRevision int64
	HardTPM          *int64
	HardCostMicro    *int64
	HardCurrency     string
	HardWindow       string
	UnknownMode      string
}

type BudgetReservation struct {
	AttemptID            string
	RequestID            string
	AccountID            string
	Provider             accounting.Provider
	Protocol             accounting.UsageProtocol
	Proof                BudgetProof
	Price                *accounting.PriceSnapshot
	TokenUpper           int64
	CostUpper            *int64
	CostCurrency         string
	Lifecycle            BudgetLifecycle
	TokenKnown           bool
	ActualTokens         *int64
	CostKnown            bool
	ActualCostMicro      *int64
	TokenOverage         bool
	CostOverage          bool
	ObservedReservedAt   time.Time
	EffectiveReservedAt  time.Time
	ExecutionExpiresAt   time.Time
	ObservedMarkedAt     *time.Time
	EffectiveMarkedAt    *time.Time
	ObservedSettledAt    *time.Time
	EffectiveSettledAt   *time.Time
	AttributionAt        *time.Time
	SettlementMode       string
	Scopes               []BudgetScope
	lastRenewExpectedAt  *time.Time
	lastRenewObservedAt  *time.Time
	lastRenewEffectiveAt *time.Time
}

type BudgetReserveResult struct {
	Allowed     bool
	Enforced    bool
	Code        BudgetDecisionCode
	Reservation *BudgetReservation
}

type BudgetRecoveryResult struct {
	Released    int64
	Interrupted int64
	EffectiveAt time.Time
}

type Budget struct {
	db *sql.DB
}

func NewBudget(db *sql.DB) *Budget { return &Budget{db: db} }

func (b *Budget) ReserveTx(ctx context.Context, tx *sql.Tx, input BudgetReserve) (BudgetReserveResult, error) {
	if !validBudgetCall(b, ctx, tx) || !validMetadata(input.RequestID, 256) || !validMetadata(input.AttemptID, 256) || !validUTCTime(input.ObservedAt) {
		return BudgetReserveResult{}, ErrInvalid
	}
	if existing, err := loadBudgetReservation(ctx, tx, input.AttemptID); err == nil {
		if !sameBudgetReserve(existing, input) {
			return BudgetReserveResult{}, ErrConflict
		}
		if existing.Lifecycle != BudgetReserved {
			return BudgetReserveResult{}, ErrConflict
		}
		return BudgetReserveResult{Allowed: true, Enforced: true, Code: BudgetDecisionReserved, Reservation: &existing}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return BudgetReserveResult{}, err
	}

	request, scopes, err := loadBudgetRequestAndScopes(ctx, tx, input.RequestID)
	if err != nil {
		return BudgetReserveResult{}, err
	}
	if request.status != accounting.StatusPending {
		return BudgetReserveResult{}, ErrConflict
	}
	enforced := enforcedBudgetScopes(scopes)
	if !request.budgetEnabled || len(enforced) == 0 {
		return BudgetReserveResult{Allowed: true, Code: BudgetDecisionNotEnforced}, nil
	}

	attempt, err := loadBudgetAttempt(ctx, tx, input.AttemptID)
	if err != nil {
		return BudgetReserveResult{}, err
	}
	if attempt.requestID != input.RequestID || attempt.status != accounting.StatusPending || attempt.provider != attempt.parentProvider ||
		attempt.parentStatus != accounting.StatusPending || attempt.employeeID != request.employeeID || attempt.keyID != request.keyID ||
		attempt.modelID != request.publicModel {
		return BudgetReserveResult{}, ErrConflict
	}
	tokenUpper, costUpper, decision, err := calculateBudgetBounds(input.Proof, attempt.price)
	if err != nil {
		return BudgetReserveResult{}, err
	}
	if decision != "" {
		return BudgetReserveResult{Code: decision, Enforced: true}, nil
	}
	for _, scope := range enforced {
		if scope.HardCostMicro != nil {
			if costUpper == nil || attempt.price == nil || scope.HardCurrency != attempt.price.Currency {
				return BudgetReserveResult{Code: BudgetDecisionBoundUnavailable, Enforced: true}, nil
			}
		}
	}
	quarantined, err := budgetProfileQuarantined(ctx, tx, attempt.provider, request.protocol, input.Proof)
	if err != nil {
		return BudgetReserveResult{}, err
	}
	if quarantined {
		return BudgetReserveResult{Code: BudgetDecisionProfileQuarantined, Enforced: true}, nil
	}

	effective, err := lockBudgetClock(ctx, tx, input.ObservedAt, request.effectiveStartedAt, attempt.startedAt)
	if err != nil {
		return BudgetReserveResult{}, err
	}
	for _, scope := range enforced {
		tokenTotal, costTotal, comparable, err := budgetScopeUsage(ctx, tx, scope.Kind, scope.ID, scope.HardCurrency, effective)
		if err != nil {
			return BudgetReserveResult{}, err
		}
		if scope.HardTPM != nil {
			candidate, ok := checkedBudgetAdd(tokenTotal, tokenUpper)
			if !ok {
				return BudgetReserveResult{}, ErrUnavailable
			}
			if candidate > *scope.HardTPM {
				return BudgetReserveResult{Code: BudgetDecisionTPMExceeded, Enforced: true}, nil
			}
		}
		if scope.HardCostMicro != nil {
			if !comparable || costUpper == nil {
				return BudgetReserveResult{Code: BudgetDecisionBoundUnavailable, Enforced: true}, nil
			}
			candidate, ok := checkedBudgetAdd(costTotal, *costUpper)
			if !ok {
				return BudgetReserveResult{}, ErrUnavailable
			}
			if candidate > *scope.HardCostMicro {
				return BudgetReserveResult{Code: BudgetDecisionCostExceeded, Enforced: true}, nil
			}
		}
	}

	reservation := BudgetReservation{
		AttemptID: input.AttemptID, RequestID: input.RequestID, AccountID: attempt.accountID, Provider: attempt.provider,
		Protocol: request.protocol, Proof: cloneBudgetProof(input.Proof), Price: cloneBudgetPrice(attempt.price), TokenUpper: tokenUpper,
		CostUpper: cloneInt64(costUpper), Lifecycle: BudgetReserved, ObservedReservedAt: input.ObservedAt.UTC(),
		EffectiveReservedAt: effective, ExecutionExpiresAt: request.expiresAt,
	}
	if attempt.price != nil {
		reservation.CostCurrency = attempt.price.Currency
	}
	if err := insertBudgetReservation(ctx, tx, reservation); err != nil {
		return BudgetReserveResult{}, err
	}
	for _, scope := range scopes {
		scope.SettingsRevision = request.settingsRevision
		if err := insertBudgetScope(ctx, tx, input.AttemptID, scope); err != nil {
			return BudgetReserveResult{}, err
		}
		reservation.Scopes = append(reservation.Scopes, cloneBudgetScope(scope))
	}
	if err := storeBudgetClock(ctx, tx, effective); err != nil {
		return BudgetReserveResult{}, err
	}
	return BudgetReserveResult{Allowed: true, Enforced: true, Code: BudgetDecisionReserved, Reservation: &reservation}, nil
}

func (b *Budget) MarkMayHaveSentTx(ctx context.Context, tx *sql.Tx, input BudgetMutation) (*BudgetReservation, error) {
	if !validBudgetMutationCall(b, ctx, tx, input) {
		return nil, ErrInvalid
	}
	reservation, err := loadBudgetReservation(ctx, tx, input.AttemptID)
	if err != nil {
		return nil, err
	}
	if reservation.Lifecycle == BudgetMayHaveSent && reservation.ObservedMarkedAt != nil && reservation.ObservedMarkedAt.Equal(input.ObservedAt) {
		return &reservation, nil
	}
	if reservation.Lifecycle != BudgetReserved {
		return nil, ErrConflict
	}
	effective, err := lockBudgetClock(ctx, tx, input.ObservedAt, reservation.EffectiveReservedAt)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE governance_budget_reservations SET lifecycle='may_have_sent',observed_marked_at=?,effective_marked_at=? WHERE attempt_id=? AND lifecycle='reserved'`,
		formatTime(input.ObservedAt), formatTime(effective), input.AttemptID)
	if err != nil {
		return nil, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return nil, ErrConflict
	}
	if err := storeBudgetClock(ctx, tx, effective); err != nil {
		return nil, err
	}
	return b.GetTx(ctx, tx, input.AttemptID)
}

func (b *Budget) RenewTx(ctx context.Context, tx *sql.Tx, input BudgetRenew) (*BudgetReservation, error) {
	if !validBudgetCall(b, ctx, tx) || !validMetadata(input.AttemptID, 256) || !validUTCTime(input.ExpectedExpiresAt) || !validUTCTime(input.ObservedAt) {
		return nil, ErrInvalid
	}
	reservation, err := loadBudgetReservation(ctx, tx, input.AttemptID)
	if err != nil {
		return nil, err
	}
	if reservation.Lifecycle != BudgetReserved && reservation.Lifecycle != BudgetMayHaveSent {
		return nil, ErrConflict
	}
	var requestExpiryText, requestStatus string
	if err := tx.QueryRowContext(ctx, `SELECT expires_at,status FROM governance_requests WHERE id=?`, reservation.RequestID).Scan(&requestExpiryText, &requestStatus); err != nil {
		return nil, ErrUnavailable
	}
	requestExpiry, err := parseStoredTime(requestExpiryText)
	if err != nil || requestStatus != string(accounting.StatusPending) {
		return nil, ErrConflict
	}
	var lastExpected, lastObserved sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT last_renew_expected_expires_at,last_renew_observed_at FROM governance_budget_reservations WHERE attempt_id=?`, input.AttemptID).Scan(&lastExpected, &lastObserved); err != nil {
		return nil, ErrUnavailable
	}
	if !reservation.ExecutionExpiresAt.Equal(input.ExpectedExpiresAt) {
		if lastExpected.Valid && lastObserved.Valid && lastExpected.String == formatTime(input.ExpectedExpiresAt) && lastObserved.String == formatTime(input.ObservedAt) && reservation.ExecutionExpiresAt.Equal(requestExpiry) {
			return &reservation, nil
		}
		return nil, ErrConflict
	}
	if !requestExpiry.After(reservation.ExecutionExpiresAt) {
		return nil, ErrConflict
	}
	effective, err := lockBudgetClock(ctx, tx, input.ObservedAt, reservation.EffectiveReservedAt)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE governance_budget_reservations SET execution_expires_at=?,last_renew_expected_expires_at=?,last_renew_observed_at=?,last_renew_effective_at=? WHERE attempt_id=? AND execution_expires_at=? AND lifecycle IN ('reserved','may_have_sent')`,
		formatTime(requestExpiry), formatTime(input.ExpectedExpiresAt), formatTime(input.ObservedAt), formatTime(effective), input.AttemptID, formatTime(input.ExpectedExpiresAt))
	if err != nil {
		return nil, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return nil, ErrConflict
	}
	if err := storeBudgetClock(ctx, tx, effective); err != nil {
		return nil, err
	}
	return b.GetTx(ctx, tx, input.AttemptID)
}

func (b *Budget) SettleTx(ctx context.Context, tx *sql.Tx, input BudgetSettle) (*BudgetReservation, error) {
	if !validBudgetCall(b, ctx, tx) || !validMetadata(input.AttemptID, 256) || !validUTCTime(input.ObservedAt) ||
		input.Mode != BudgetSettleFromAttempt && input.Mode != BudgetSettleReleaseNotStarted {
		return nil, ErrInvalid
	}
	reservation, err := loadBudgetReservation(ctx, tx, input.AttemptID)
	if err != nil {
		return nil, err
	}
	if (reservation.Lifecycle == BudgetSettled || reservation.Lifecycle == BudgetReleasedNotStarted) &&
		reservation.ObservedSettledAt != nil && reservation.ObservedSettledAt.Equal(input.ObservedAt) && reservation.SettlementMode == string(input.Mode) {
		return &reservation, nil
	}
	if reservation.Lifecycle != BudgetReserved && reservation.Lifecycle != BudgetMayHaveSent {
		return nil, ErrConflict
	}
	attempt, err := loadBudgetAttempt(ctx, tx, input.AttemptID)
	if err != nil {
		return nil, err
	}
	if !terminalStatus(attempt.status) {
		return nil, ErrConflict
	}
	effective, err := lockBudgetClock(ctx, tx, input.ObservedAt, reservation.EffectiveReservedAt)
	if err != nil {
		return nil, err
	}
	if input.Mode == BudgetSettleReleaseNotStarted {
		if reservation.Lifecycle != BudgetReserved || attempt.hasAnyUsage() || attempt.cost != nil {
			return nil, ErrConflict
		}
		if err := updateBudgetTerminal(ctx, tx, reservation, BudgetReleasedNotStarted, string(input.Mode), input.ObservedAt, effective, effective, false, nil, false, nil, false, false); err != nil {
			return nil, err
		}
	} else {
		tokenKnown := attempt.allUsageKnown()
		var actualTokens *int64
		if tokenKnown {
			value, ok := checkedBudgetSumPointers(attempt.usage...)
			if !ok {
				return nil, ErrUnavailable
			}
			actualTokens = &value
		}
		costKnown := attempt.cost != nil
		tokenOverage, err := budgetTokenOverage(reservation.Proof, attempt.usage)
		if err != nil {
			return nil, err
		}
		costOverage := attempt.cost != nil && (reservation.CostUpper == nil || *attempt.cost > *reservation.CostUpper)
		attribution := effective
		if !tokenKnown || !costKnown {
			attribution = maxBudgetTime(effective, reservation.ExecutionExpiresAt)
		}
		if err := updateBudgetTerminal(ctx, tx, reservation, BudgetSettled, string(input.Mode), input.ObservedAt, effective, attribution,
			tokenKnown, actualTokens, costKnown, attempt.cost, tokenOverage, costOverage); err != nil {
			return nil, err
		}
		if tokenOverage || costOverage {
			if err := quarantineBudgetProfile(ctx, tx, reservation, effective, tokenOverage, costOverage); err != nil {
				return nil, err
			}
		}
	}
	if err := storeBudgetClock(ctx, tx, effective); err != nil {
		return nil, err
	}
	return b.GetTx(ctx, tx, input.AttemptID)
}

func (b *Budget) InterruptTx(ctx context.Context, tx *sql.Tx, input BudgetMutation) (*BudgetReservation, error) {
	if !validBudgetMutationCall(b, ctx, tx, input) {
		return nil, ErrInvalid
	}
	reservation, err := loadBudgetReservation(ctx, tx, input.AttemptID)
	if err != nil {
		return nil, err
	}
	if reservation.Lifecycle == BudgetInterrupted && reservation.SettlementMode == "interrupt" && reservation.ObservedSettledAt != nil && reservation.ObservedSettledAt.Equal(input.ObservedAt) {
		return &reservation, nil
	}
	if reservation.Lifecycle != BudgetReserved && reservation.Lifecycle != BudgetMayHaveSent {
		return nil, ErrConflict
	}
	effective, err := lockBudgetClock(ctx, tx, input.ObservedAt, reservation.EffectiveReservedAt)
	if err != nil {
		return nil, err
	}
	attribution := maxBudgetTime(effective, reservation.ExecutionExpiresAt)
	if err := updateBudgetTerminal(ctx, tx, reservation, BudgetInterrupted, "interrupt", input.ObservedAt, effective, attribution, false, nil, false, nil, false, false); err != nil {
		return nil, err
	}
	if err := storeBudgetClock(ctx, tx, effective); err != nil {
		return nil, err
	}
	return b.GetTx(ctx, tx, input.AttemptID)
}

func (b *Budget) RecoverTx(ctx context.Context, tx *sql.Tx, at time.Time) (BudgetRecoveryResult, error) {
	if !validBudgetCall(b, ctx, tx) || !validUTCTime(at) {
		return BudgetRecoveryResult{}, ErrInvalid
	}
	if err := validateNoMissingBudgetReservations(ctx, tx); err != nil {
		return BudgetRecoveryResult{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT attempt_id FROM governance_budget_reservations WHERE lifecycle IN ('reserved','may_have_sent') ORDER BY attempt_id`)
	if err != nil {
		return BudgetRecoveryResult{}, ErrUnavailable
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return BudgetRecoveryResult{}, ErrUnavailable
		}
		ids = append(ids, id)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return BudgetRecoveryResult{}, ErrUnavailable
	}
	result := BudgetRecoveryResult{}
	effective, err := lockBudgetClock(ctx, tx, at)
	if err != nil {
		return BudgetRecoveryResult{}, err
	}
	for _, id := range ids {
		reservation, err := loadBudgetReservation(ctx, tx, id)
		if err != nil {
			return BudgetRecoveryResult{}, err
		}
		attempt, err := loadBudgetAttempt(ctx, tx, id)
		if err != nil || attempt.status != accounting.StatusPending || attempt.requestID != reservation.RequestID {
			return BudgetRecoveryResult{}, ErrSchema
		}
		var requestStatus string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM governance_requests WHERE id=?`, reservation.RequestID).Scan(&requestStatus); err != nil || requestStatus != string(accounting.StatusPending) {
			return BudgetRecoveryResult{}, ErrSchema
		}
		attribution := maxBudgetTime(effective, reservation.ExecutionExpiresAt)
		if err := updateBudgetTerminal(ctx, tx, reservation, BudgetInterrupted, "recovery", at, effective, attribution, false, nil, false, nil, false, false); err != nil {
			return BudgetRecoveryResult{}, err
		}
		result.Interrupted++
	}
	if err := storeBudgetClock(ctx, tx, effective); err != nil {
		return BudgetRecoveryResult{}, err
	}
	result.EffectiveAt = effective
	return result, nil
}

func (b *Budget) Get(ctx context.Context, attemptID string) (*BudgetReservation, error) {
	if b == nil || b.db == nil || ctx == nil || !validMetadata(attemptID, 256) {
		return nil, ErrInvalid
	}
	reservation, err := loadBudgetReservation(ctx, b.db, attemptID)
	if err != nil {
		return nil, err
	}
	return &reservation, nil
}

func (b *Budget) GetTx(ctx context.Context, tx *sql.Tx, attemptID string) (*BudgetReservation, error) {
	if !validBudgetCall(b, ctx, tx) || !validMetadata(attemptID, 256) {
		return nil, ErrInvalid
	}
	reservation, err := loadBudgetReservation(ctx, tx, attemptID)
	if err != nil {
		return nil, err
	}
	return &reservation, nil
}

type budgetRequestFact struct {
	employeeID, keyID, publicModel string
	protocol                       accounting.UsageProtocol
	settingsRevision               int64
	budgetEnabled                  bool
	effectiveStartedAt, expiresAt  time.Time
	status                         accounting.Status
}

type budgetAttemptFact struct {
	requestID, accountID, employeeID, keyID, modelID string
	provider, parentProvider                         accounting.Provider
	status, parentStatus                             accounting.Status
	startedAt                                        time.Time
	price                                            *accounting.PriceSnapshot
	usage                                            []*int64
	cost                                             *int64
}

func (a budgetAttemptFact) allUsageKnown() bool {
	return len(a.usage) == 4 && a.usage[0] != nil && a.usage[1] != nil && a.usage[2] != nil && a.usage[3] != nil
}
func (a budgetAttemptFact) hasAnyUsage() bool {
	for _, value := range a.usage {
		if value != nil {
			return true
		}
	}
	return false
}

type budgetQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func loadBudgetRequestAndScopes(ctx context.Context, tx *sql.Tx, id string) (budgetRequestFact, []BudgetScope, error) {
	var request budgetRequestFact
	var protocol, status, effective, expires string
	var enabled int
	err := tx.QueryRowContext(ctx, `SELECT employee_id,key_id,public_model,protocol,settings_revision,budget_enabled,effective_started_at,expires_at,status FROM governance_requests WHERE id=?`, id).
		Scan(&request.employeeID, &request.keyID, &request.publicModel, &protocol, &request.settingsRevision, &enabled, &effective, &expires, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return budgetRequestFact{}, nil, ErrNotFound
	}
	if err != nil {
		return budgetRequestFact{}, nil, ErrUnavailable
	}
	request.protocol, request.status, request.budgetEnabled = accounting.UsageProtocol(protocol), accounting.Status(status), enabled == 1
	if enabled != 0 && enabled != 1 || !validProtocol(request.protocol) || !validRevision(request.settingsRevision) {
		return budgetRequestFact{}, nil, ErrSchema
	}
	if request.effectiveStartedAt, err = parseStoredTime(effective); err != nil {
		return budgetRequestFact{}, nil, ErrSchema
	}
	if request.expiresAt, err = parseStoredTime(expires); err != nil {
		return budgetRequestFact{}, nil, ErrSchema
	}
	rows, err := tx.QueryContext(ctx, `SELECT scope_kind,scope_id,policy_id,policy_revision,group_revision,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode FROM governance_request_scopes WHERE request_id=? ORDER BY scope_kind,scope_id`, id)
	if err != nil {
		return budgetRequestFact{}, nil, ErrUnavailable
	}
	defer rows.Close()
	scopes := make([]BudgetScope, 0)
	for rows.Next() {
		var scope BudgetScope
		var groupRevision, hardTPM, hardCost sql.NullInt64
		if err := rows.Scan(&scope.Kind, &scope.ID, &scope.PolicyID, &scope.PolicyRevision, &groupRevision, &hardTPM, &hardCost, &scope.HardCurrency, &scope.HardWindow, &scope.UnknownMode); err != nil {
			return budgetRequestFact{}, nil, ErrUnavailable
		}
		setBudgetOptional(&scope.GroupRevision, groupRevision)
		setBudgetOptional(&scope.HardTPM, hardTPM)
		setBudgetOptional(&scope.HardCostMicro, hardCost)
		if !validBudgetScope(scope) {
			return budgetRequestFact{}, nil, ErrSchema
		}
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return budgetRequestFact{}, nil, ErrUnavailable
	}
	return request, scopes, nil
}

func loadBudgetAttempt(ctx context.Context, tx *sql.Tx, id string) (budgetAttemptFact, error) {
	var fact budgetAttemptFact
	var provider, status, started, parentProvider, parentStatus string
	var input, output, cacheRead, cacheWrite, cost sql.NullInt64
	var priceVersion, currency sql.NullString
	var inputRate, outputRate, cacheReadRate, cacheWriteRate sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT a.request_id,a.account_id,a.provider,a.started_at,a.status,a.input_tokens,a.output_tokens,a.cache_read_tokens,a.cache_write_tokens,
		a.price_version,a.currency,a.input_rate,a.output_rate,a.cache_read_rate,a.cache_write_rate,a.cost_micro,
		r.employee_id,r.key_id,r.model_id,r.provider,r.status
		FROM accounting_attempts a JOIN accounting_requests r ON r.id=a.request_id WHERE a.id=?`, id).Scan(
		&fact.requestID, &fact.accountID, &provider, &started, &status, &input, &output, &cacheRead, &cacheWrite,
		&priceVersion, &currency, &inputRate, &outputRate, &cacheReadRate, &cacheWriteRate, &cost,
		&fact.employeeID, &fact.keyID, &fact.modelID, &parentProvider, &parentStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return budgetAttemptFact{}, ErrNotFound
	}
	if err != nil {
		return budgetAttemptFact{}, ErrUnavailable
	}
	fact.provider, fact.parentProvider = accounting.Provider(provider), accounting.Provider(parentProvider)
	fact.status, fact.parentStatus = accounting.Status(status), accounting.Status(parentStatus)
	parsed, err := time.Parse(time.RFC3339Nano, started)
	if err != nil {
		return budgetAttemptFact{}, ErrSchema
	}
	fact.startedAt = parsed.UTC()
	priceKnown := priceVersion.Valid && currency.Valid && inputRate.Valid && outputRate.Valid && cacheReadRate.Valid && cacheWriteRate.Valid
	priceUnknown := !priceVersion.Valid && !currency.Valid && !inputRate.Valid && !outputRate.Valid && !cacheReadRate.Valid && !cacheWriteRate.Valid
	if !priceKnown && !priceUnknown {
		return budgetAttemptFact{}, ErrSchema
	}
	if priceKnown {
		fact.price = &accounting.PriceSnapshot{Version: priceVersion.String, Currency: currency.String, InputPerMillionMicro: inputRate.Int64,
			OutputPerMillionMicro: outputRate.Int64, CacheReadPerMillionMicro: cacheReadRate.Int64, CacheWritePerMillionMicro: cacheWriteRate.Int64}
	}
	fact.usage = []*int64{budgetPointer(input), budgetPointer(output), budgetPointer(cacheRead), budgetPointer(cacheWrite)}
	fact.cost = budgetPointer(cost)
	return fact, nil
}

func calculateBudgetBounds(proof BudgetProof, price *accounting.PriceSnapshot) (int64, *int64, BudgetDecisionCode, error) {
	if !validBudgetProofIdentity(proof) {
		return 0, nil, BudgetDecisionBoundUnavailable, nil
	}
	if price != nil {
		if _, err := accounting.CalculateUpperCost(accounting.UpperUsage{}, *price); err != nil {
			return 0, nil, "", ErrSchema
		}
	}
	var tokenUpper, cost int64
	var err error
	switch proof.Type {
	case BudgetProofFourBuckets:
		if proof.FourBuckets == nil || proof.MutuallyExclusiveInput != nil {
			return 0, nil, BudgetDecisionBoundUnavailable, nil
		}
		values := []int64{proof.FourBuckets.InputTokens, proof.FourBuckets.OutputTokens, proof.FourBuckets.CacheReadTokens, proof.FourBuckets.CacheWriteTokens}
		for _, value := range values {
			if value < 0 {
				return 0, nil, BudgetDecisionBoundUnavailable, nil
			}
		}
		var ok bool
		if tokenUpper, ok = checkedBudgetSum(values...); !ok {
			return 0, nil, "", ErrUnavailable
		}
		if price != nil {
			cost, err = accounting.CalculateUpperCost(*proof.FourBuckets, *price)
		}
	case BudgetProofMutuallyExclusiveInput:
		if proof.MutuallyExclusiveInput == nil || proof.FourBuckets != nil {
			return 0, nil, BudgetDecisionBoundUnavailable, nil
		}
		if proof.MutuallyExclusiveInput.InputMax < 0 || proof.MutuallyExclusiveInput.OutputMax < 0 {
			return 0, nil, BudgetDecisionBoundUnavailable, nil
		}
		if price != nil {
			tokenUpper, cost, err = accounting.CalculateMutuallyExclusiveInputUpperBound(*proof.MutuallyExclusiveInput, *price)
		} else {
			var ok bool
			tokenUpper, ok = checkedBudgetSum(proof.MutuallyExclusiveInput.InputMax, proof.MutuallyExclusiveInput.OutputMax)
			if !ok {
				return 0, nil, "", ErrUnavailable
			}
		}
	default:
		return 0, nil, BudgetDecisionBoundUnavailable, nil
	}
	if err != nil {
		return 0, nil, "", ErrUnavailable
	}
	if price == nil {
		return tokenUpper, nil, "", nil
	}
	return tokenUpper, &cost, "", nil
}

func budgetTokenOverage(proof BudgetProof, usage []*int64) (bool, error) {
	if len(usage) != 4 {
		return false, ErrSchema
	}
	for _, value := range usage {
		if value != nil && *value < 0 {
			return false, ErrSchema
		}
	}
	switch proof.Type {
	case BudgetProofFourBuckets:
		if proof.FourBuckets == nil {
			return false, ErrSchema
		}
		bounds := []int64{proof.FourBuckets.InputTokens, proof.FourBuckets.OutputTokens, proof.FourBuckets.CacheReadTokens, proof.FourBuckets.CacheWriteTokens}
		for index, value := range usage {
			if value != nil && *value > bounds[index] {
				return true, nil
			}
		}
		return false, nil
	case BudgetProofMutuallyExclusiveInput:
		if proof.MutuallyExclusiveInput == nil {
			return false, ErrSchema
		}
		if usage[1] != nil && *usage[1] > proof.MutuallyExclusiveInput.OutputMax {
			return true, nil
		}
		var inputTotal int64
		for _, value := range []*int64{usage[0], usage[2], usage[3]} {
			if value == nil {
				continue
			}
			var ok bool
			inputTotal, ok = checkedBudgetAdd(inputTotal, *value)
			if !ok {
				return false, ErrUnavailable
			}
			if inputTotal > proof.MutuallyExclusiveInput.InputMax {
				return true, nil
			}
		}
		return false, nil
	default:
		return false, ErrSchema
	}
}

func insertBudgetReservation(ctx context.Context, tx *sql.Tx, row BudgetReservation) error {
	proof := row.Proof
	var upperInput, upperOutput, upperCacheRead, upperCacheWrite, groupInput, groupOutput any
	if proof.Type == BudgetProofFourBuckets {
		upperInput, upperOutput, upperCacheRead, upperCacheWrite = proof.FourBuckets.InputTokens, proof.FourBuckets.OutputTokens, proof.FourBuckets.CacheReadTokens, proof.FourBuckets.CacheWriteTokens
	} else {
		groupInput, groupOutput = proof.MutuallyExclusiveInput.InputMax, proof.MutuallyExclusiveInput.OutputMax
	}
	priceValues := []any{nil, nil, nil, nil, nil, nil}
	if row.Price != nil {
		priceValues = []any{row.Price.Version, row.Price.Currency, row.Price.InputPerMillionMicro, row.Price.OutputPerMillionMicro, row.Price.CacheReadPerMillionMicro, row.Price.CacheWritePerMillionMicro}
	}
	args := []any{row.AttemptID, row.RequestID, row.AccountID, string(row.Provider), string(row.Protocol), proof.ActualModel, proof.AccountRevision,
		proof.PoolRevision, proof.TransformRevision, proof.BounderID, proof.BounderRevision, string(proof.Type), upperInput, upperOutput, upperCacheRead,
		upperCacheWrite, groupInput, groupOutput, row.TokenUpper, nullableBudgetInt(row.CostUpper), row.CostCurrency}
	args = append(args, priceValues...)
	args = append(args, string(row.Lifecycle), 0, nil, 0, nil, 0, 0, formatTime(row.ObservedReservedAt), formatTime(row.EffectiveReservedAt),
		formatTime(row.ExecutionExpiresAt), nil, nil, nil, nil, nil, "", nil, nil, nil)
	_, err := tx.ExecContext(ctx, `INSERT INTO governance_budget_reservations(
		attempt_id,request_id,account_id,provider,protocol,actual_model,account_revision,pool_revision,transform_revision,bounder_id,bounder_revision,proof_type,
		upper_input,upper_output,upper_cache_read,upper_cache_write,upper_group_input,upper_group_output,token_upper,cost_upper,cost_currency,
		price_version,price_currency,price_input_rate,price_output_rate,price_cache_read_rate,price_cache_write_rate,lifecycle,token_known,actual_tokens,
		cost_known,actual_cost_micro,token_overage,cost_overage,observed_reserved_at,effective_reserved_at,execution_expires_at,observed_marked_at,
		effective_marked_at,observed_settled_at,effective_settled_at,attribution_at,settlement_mode,last_renew_expected_expires_at,last_renew_observed_at,last_renew_effective_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, args...)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func insertBudgetScope(ctx context.Context, tx *sql.Tx, attemptID string, scope BudgetScope) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO governance_budget_reservation_scopes(
		attempt_id,scope_kind,scope_id,policy_id,policy_revision,group_revision,settings_revision,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, attemptID, string(scope.Kind), scope.ID, scope.PolicyID, scope.PolicyRevision, nullableBudgetInt(scope.GroupRevision),
		scope.SettingsRevision, nullableBudgetInt(scope.HardTPM), nullableBudgetInt(scope.HardCostMicro), scope.HardCurrency, scope.HardWindow, scope.UnknownMode)
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func updateBudgetTerminal(ctx context.Context, tx *sql.Tx, current BudgetReservation, lifecycle BudgetLifecycle, mode string, observed, effective, attribution time.Time,
	tokenKnown bool, actualTokens *int64, costKnown bool, actualCost *int64, tokenOverage, costOverage bool) error {
	result, err := tx.ExecContext(ctx, `UPDATE governance_budget_reservations SET lifecycle=?,token_known=?,actual_tokens=?,cost_known=?,actual_cost_micro=?,
		token_overage=?,cost_overage=?,observed_settled_at=?,effective_settled_at=?,attribution_at=?,settlement_mode=?
		WHERE attempt_id=? AND lifecycle=?`, string(lifecycle), boolInteger(tokenKnown), nullableBudgetInt(actualTokens), boolInteger(costKnown), nullableBudgetInt(actualCost),
		boolInteger(tokenOverage), boolInteger(costOverage), formatTime(observed), formatTime(effective), formatTime(attribution), mode, current.AttemptID, string(current.Lifecycle))
	if err != nil {
		return ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return ErrConflict
	}
	return nil
}

func quarantineBudgetProfile(ctx context.Context, tx *sql.Tx, row BudgetReservation, at time.Time, token, cost bool) error {
	reason := "token_overage"
	if cost && !token {
		reason = "cost_overage"
	} else if token && cost {
		reason = "token_and_cost_overage"
	}
	_, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO governance_budget_profile_quarantine(
		provider,protocol,actual_model,transform_revision,bounder_id,bounder_revision,reason,attempt_id,quarantined_at
	) VALUES(?,?,?,?,?,?,?,?,?)`, string(row.Provider), string(row.Protocol), row.Proof.ActualModel, row.Proof.TransformRevision, row.Proof.BounderID,
		row.Proof.BounderRevision, reason, row.AttemptID, formatTime(at))
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func budgetProfileQuarantined(ctx context.Context, tx *sql.Tx, provider accounting.Provider, protocol accounting.UsageProtocol, proof BudgetProof) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM governance_budget_profile_quarantine WHERE provider=? AND protocol=? AND actual_model=? AND transform_revision=? AND bounder_id=? AND bounder_revision=?`,
		string(provider), string(protocol), proof.ActualModel, proof.TransformRevision, proof.BounderID, proof.BounderRevision).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, ErrUnavailable
	}
	return true, nil
}

func budgetScopeUsage(ctx context.Context, tx *sql.Tx, kind ScopeKind, id, currency string, now time.Time) (int64, int64, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.lifecycle,r.token_upper,r.cost_upper,r.cost_currency,r.token_known,r.actual_tokens,r.cost_known,r.actual_cost_micro,
		r.effective_settled_at,r.attribution_at FROM governance_budget_reservation_scopes s JOIN governance_budget_reservations r ON r.attempt_id=s.attempt_id
		WHERE s.scope_kind=? AND s.scope_id=? ORDER BY r.attempt_id`, string(kind), id)
	if err != nil {
		return 0, 0, false, ErrUnavailable
	}
	defer rows.Close()
	var tokens, cost int64
	comparable := true
	for rows.Next() {
		var lifecycle string
		var tokenUpper int64
		var costUpper, actualTokens, actualCost sql.NullInt64
		var costCurrency string
		var tokenKnown, costKnown int
		var settled, attribution sql.NullString
		if err := rows.Scan(&lifecycle, &tokenUpper, &costUpper, &costCurrency, &tokenKnown, &actualTokens, &costKnown, &actualCost, &settled, &attribution); err != nil {
			return 0, 0, false, ErrUnavailable
		}
		active := lifecycle == string(BudgetReserved) || lifecycle == string(BudgetMayHaveSent)
		if lifecycle == string(BudgetReleasedNotStarted) {
			continue
		}
		var tokenValue *int64
		if active {
			tokenValue = &tokenUpper
		} else {
			base, err := budgetContributionTime(tokenKnown == 1, settled, attribution)
			if err != nil {
				return 0, 0, false, err
			}
			if now.Before(base.Add(time.Minute)) {
				if tokenKnown == 1 && actualTokens.Valid {
					tokenValue = &actualTokens.Int64
				} else {
					tokenValue = &tokenUpper
				}
			}
		}
		if tokenValue != nil {
			var ok bool
			tokens, ok = checkedBudgetAdd(tokens, *tokenValue)
			if !ok {
				return 0, 0, false, ErrUnavailable
			}
		}
		costRelevant := active
		if !active {
			base, err := budgetContributionTime(costKnown == 1, settled, attribution)
			if err != nil {
				return 0, 0, false, err
			}
			costRelevant = now.Before(base.Add(24 * time.Hour))
		}
		if !costRelevant || currency == "" {
			continue
		}
		if costCurrency != currency || !costUpper.Valid {
			comparable = false
			continue
		}
		value := costUpper.Int64
		if !active && costKnown == 1 && actualCost.Valid {
			value = actualCost.Int64
		}
		var ok bool
		cost, ok = checkedBudgetAdd(cost, value)
		if !ok {
			return 0, 0, false, ErrUnavailable
		}
	}
	if err := rows.Err(); err != nil {
		return 0, 0, false, ErrUnavailable
	}
	return tokens, cost, comparable, nil
}

func budgetContributionTime(known bool, settled, attribution sql.NullString) (time.Time, error) {
	value := attribution
	if known {
		value = settled
	}
	if !value.Valid {
		return time.Time{}, ErrSchema
	}
	parsed, err := parseStoredTime(value.String)
	if err != nil {
		return time.Time{}, ErrSchema
	}
	return parsed, nil
}

func lockBudgetClock(ctx context.Context, tx *sql.Tx, observed time.Time, floors ...time.Time) (time.Time, error) {
	result, err := tx.ExecContext(ctx, `UPDATE governance_budget_clock SET last_effective_at=last_effective_at WHERE singleton=1`)
	if err != nil {
		return time.Time{}, ErrUnavailable
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return time.Time{}, ErrSchema
	}
	var stored string
	if err := tx.QueryRowContext(ctx, `SELECT last_effective_at FROM governance_budget_clock WHERE singleton=1`).Scan(&stored); err != nil {
		return time.Time{}, ErrUnavailable
	}
	last, err := parseStoredTime(stored)
	if err != nil {
		return time.Time{}, ErrSchema
	}
	effective := observed.UTC()
	if last.After(effective) {
		effective = last
	}
	for _, floor := range floors {
		if floor.After(effective) {
			effective = floor
		}
	}
	return effective, nil
}

func storeBudgetClock(ctx context.Context, tx *sql.Tx, effective time.Time) error {
	if _, err := tx.ExecContext(ctx, `UPDATE governance_budget_clock SET last_effective_at=? WHERE singleton=1`, formatTime(effective)); err != nil {
		return ErrUnavailable
	}
	return nil
}

func validateNoMissingBudgetReservations(ctx context.Context, tx *sql.Tx) error {
	var missing int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM accounting_attempts a
		JOIN governance_requests r ON r.id=a.request_id
		WHERE a.status='pending' AND r.status='pending' AND r.budget_enabled=1
		AND EXISTS(SELECT 1 FROM governance_request_scopes s WHERE s.request_id=r.id AND s.unknown_mode='deny_unknown' AND (s.hard_tpm IS NOT NULL OR s.hard_cost_micro IS NOT NULL))
		AND NOT EXISTS(SELECT 1 FROM governance_budget_reservations b WHERE b.attempt_id=a.id)`).Scan(&missing)
	if err != nil {
		return ErrUnavailable
	}
	if missing != 0 {
		return ErrSchema
	}
	return nil
}

func loadBudgetReservation(ctx context.Context, query budgetQuery, attemptID string) (BudgetReservation, error) {
	var row BudgetReservation
	var provider, protocol, proofType, lifecycle string
	var upperInput, upperOutput, upperRead, upperWrite, groupInput, groupOutput, costUpper sql.NullInt64
	var priceVersion, priceCurrency sql.NullString
	var inputRate, outputRate, readRate, writeRate sql.NullInt64
	var tokenKnown, costKnown, tokenOverage, costOverage int
	var actualTokens, actualCost sql.NullInt64
	var observedReserved, effectiveReserved, expires string
	var observedMarked, effectiveMarked, observedSettled, effectiveSettled, attribution sql.NullString
	var lastExpected, lastObserved, lastEffective sql.NullString
	err := query.QueryRowContext(ctx, `SELECT attempt_id,request_id,account_id,provider,protocol,actual_model,account_revision,pool_revision,transform_revision,bounder_id,bounder_revision,proof_type,
		upper_input,upper_output,upper_cache_read,upper_cache_write,upper_group_input,upper_group_output,token_upper,cost_upper,cost_currency,
		price_version,price_currency,price_input_rate,price_output_rate,price_cache_read_rate,price_cache_write_rate,lifecycle,token_known,actual_tokens,cost_known,actual_cost_micro,
		token_overage,cost_overage,observed_reserved_at,effective_reserved_at,execution_expires_at,observed_marked_at,effective_marked_at,observed_settled_at,effective_settled_at,
		attribution_at,settlement_mode,last_renew_expected_expires_at,last_renew_observed_at,last_renew_effective_at
		FROM governance_budget_reservations WHERE attempt_id=?`, attemptID).Scan(
		&row.AttemptID, &row.RequestID, &row.AccountID, &provider, &protocol, &row.Proof.ActualModel, &row.Proof.AccountRevision, &row.Proof.PoolRevision,
		&row.Proof.TransformRevision, &row.Proof.BounderID, &row.Proof.BounderRevision, &proofType, &upperInput, &upperOutput, &upperRead, &upperWrite,
		&groupInput, &groupOutput, &row.TokenUpper, &costUpper, &row.CostCurrency, &priceVersion, &priceCurrency, &inputRate, &outputRate, &readRate, &writeRate,
		&lifecycle, &tokenKnown, &actualTokens, &costKnown, &actualCost, &tokenOverage, &costOverage, &observedReserved, &effectiveReserved, &expires,
		&observedMarked, &effectiveMarked, &observedSettled, &effectiveSettled, &attribution, &row.SettlementMode, &lastExpected, &lastObserved, &lastEffective)
	if errors.Is(err, sql.ErrNoRows) {
		return BudgetReservation{}, ErrNotFound
	}
	if err != nil {
		return BudgetReservation{}, ErrUnavailable
	}
	row.Provider, row.Protocol, row.Proof.Type, row.Lifecycle = accounting.Provider(provider), accounting.UsageProtocol(protocol), BudgetProofType(proofType), BudgetLifecycle(lifecycle)
	if row.Proof.Type == BudgetProofFourBuckets && upperInput.Valid && upperOutput.Valid && upperRead.Valid && upperWrite.Valid {
		row.Proof.FourBuckets = &accounting.UpperUsage{InputTokens: upperInput.Int64, OutputTokens: upperOutput.Int64, CacheReadTokens: upperRead.Int64, CacheWriteTokens: upperWrite.Int64}
	} else if row.Proof.Type == BudgetProofMutuallyExclusiveInput && groupInput.Valid && groupOutput.Valid {
		row.Proof.MutuallyExclusiveInput = &accounting.MutuallyExclusiveInputUpperUsage{InputMax: groupInput.Int64, OutputMax: groupOutput.Int64}
	}
	if costUpper.Valid {
		row.CostUpper = cloneInt64(&costUpper.Int64)
	}
	if priceVersion.Valid && priceCurrency.Valid && inputRate.Valid && outputRate.Valid && readRate.Valid && writeRate.Valid {
		row.Price = &accounting.PriceSnapshot{Version: priceVersion.String, Currency: priceCurrency.String, InputPerMillionMicro: inputRate.Int64,
			OutputPerMillionMicro: outputRate.Int64, CacheReadPerMillionMicro: readRate.Int64, CacheWritePerMillionMicro: writeRate.Int64}
	}
	row.TokenKnown, row.CostKnown, row.TokenOverage, row.CostOverage = tokenKnown == 1, costKnown == 1, tokenOverage == 1, costOverage == 1
	row.ActualTokens, row.ActualCostMicro = budgetPointer(actualTokens), budgetPointer(actualCost)
	if row.ObservedReservedAt, err = parseStoredTime(observedReserved); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	if row.EffectiveReservedAt, err = parseStoredTime(effectiveReserved); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	if row.ExecutionExpiresAt, err = parseStoredTime(expires); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	if row.ObservedMarkedAt, err = pointerTime(observedMarked); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	if row.EffectiveMarkedAt, err = pointerTime(effectiveMarked); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	if row.ObservedSettledAt, err = pointerTime(observedSettled); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	if row.EffectiveSettledAt, err = pointerTime(effectiveSettled); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	if row.AttributionAt, err = pointerTime(attribution); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	if row.lastRenewExpectedAt, err = pointerTime(lastExpected); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	if row.lastRenewObservedAt, err = pointerTime(lastObserved); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	if row.lastRenewEffectiveAt, err = pointerTime(lastEffective); err != nil {
		return BudgetReservation{}, ErrSchema
	}
	scopes, err := loadBudgetScopes(ctx, query, attemptID)
	if err != nil {
		return BudgetReservation{}, err
	}
	row.Scopes = scopes
	if !validLoadedBudgetReservation(row) {
		return BudgetReservation{}, ErrSchema
	}
	return row, nil
}

func loadBudgetScopes(ctx context.Context, query budgetQuery, attemptID string) ([]BudgetScope, error) {
	rows, err := query.QueryContext(ctx, `SELECT scope_kind,scope_id,policy_id,policy_revision,group_revision,settings_revision,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode FROM governance_budget_reservation_scopes WHERE attempt_id=? ORDER BY scope_kind,scope_id`, attemptID)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	result := make([]BudgetScope, 0)
	for rows.Next() {
		var scope BudgetScope
		var groupRevision, hardTPM, hardCost sql.NullInt64
		if err := rows.Scan(&scope.Kind, &scope.ID, &scope.PolicyID, &scope.PolicyRevision, &groupRevision, &scope.SettingsRevision, &hardTPM, &hardCost,
			&scope.HardCurrency, &scope.HardWindow, &scope.UnknownMode); err != nil {
			return nil, ErrUnavailable
		}
		setBudgetOptional(&scope.GroupRevision, groupRevision)
		setBudgetOptional(&scope.HardTPM, hardTPM)
		setBudgetOptional(&scope.HardCostMicro, hardCost)
		result = append(result, scope)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return result, nil
}

func validLoadedBudgetReservation(row BudgetReservation) bool {
	if !validMetadata(row.AttemptID, 256) || !validMetadata(row.RequestID, 256) || !validMetadata(row.AccountID, 256) || !validBudgetProvider(string(row.Provider)) ||
		!validProtocol(row.Protocol) || !validBudgetProofIdentity(row.Proof) || !validUTCTime(row.ObservedReservedAt) || !validUTCTime(row.EffectiveReservedAt) ||
		!validUTCTime(row.ExecutionExpiresAt) || row.EffectiveReservedAt.Before(row.ObservedReservedAt) || !row.ExecutionExpiresAt.After(row.EffectiveReservedAt) || len(row.Scopes) == 0 {
		return false
	}
	for _, scope := range row.Scopes {
		if !validBudgetScope(scope) || !validRevision(scope.SettingsRevision) {
			return false
		}
	}
	if row.Price == nil != (row.CostUpper == nil) || row.Price == nil && row.CostCurrency != "" || row.Price != nil && row.CostCurrency != row.Price.Currency {
		return false
	}
	tokenUpper, costUpper, decision, err := calculateBudgetBounds(row.Proof, row.Price)
	if err != nil || decision != "" || tokenUpper != row.TokenUpper || !sameInt64Pointer(costUpper, row.CostUpper) {
		return false
	}
	renewCount := 0
	for _, value := range []*time.Time{row.lastRenewExpectedAt, row.lastRenewObservedAt, row.lastRenewEffectiveAt} {
		if value != nil {
			renewCount++
		}
	}
	if renewCount != 0 && renewCount != 3 || row.lastRenewEffectiveAt != nil && row.lastRenewEffectiveAt.Before(row.EffectiveReservedAt) {
		return false
	}
	if row.ObservedMarkedAt != nil != (row.EffectiveMarkedAt != nil) || row.EffectiveMarkedAt != nil && row.EffectiveMarkedAt.Before(row.EffectiveReservedAt) {
		return false
	}
	if row.ObservedSettledAt != nil != (row.EffectiveSettledAt != nil) || row.EffectiveSettledAt != nil && row.EffectiveSettledAt.Before(row.EffectiveReservedAt) {
		return false
	}
	if row.AttributionAt != nil && row.EffectiveSettledAt != nil && row.AttributionAt.Before(*row.EffectiveSettledAt) {
		return false
	}
	if row.EffectiveMarkedAt != nil && row.ObservedMarkedAt != nil && row.EffectiveMarkedAt.Before(*row.ObservedMarkedAt) ||
		row.EffectiveSettledAt != nil && row.ObservedSettledAt != nil && row.EffectiveSettledAt.Before(*row.ObservedSettledAt) {
		return false
	}
	if renewCount == 3 && (row.lastRenewEffectiveAt.Before(*row.lastRenewObservedAt) || !row.lastRenewExpectedAt.Before(row.ExecutionExpiresAt)) {
		return false
	}
	switch row.Lifecycle {
	case BudgetReserved:
		if row.ObservedMarkedAt != nil || row.ObservedSettledAt != nil || row.SettlementMode != "" {
			return false
		}
	case BudgetMayHaveSent:
		if row.ObservedMarkedAt == nil || row.ObservedSettledAt != nil || row.SettlementMode != "" {
			return false
		}
	case BudgetSettled:
		if row.ObservedSettledAt == nil || row.AttributionAt == nil || row.SettlementMode != string(BudgetSettleFromAttempt) {
			return false
		}
		expected := *row.EffectiveSettledAt
		if !row.TokenKnown || !row.CostKnown {
			expected = maxBudgetTime(expected, row.ExecutionExpiresAt)
		}
		if !row.AttributionAt.Equal(expected) {
			return false
		}
	case BudgetReleasedNotStarted:
		if row.ObservedSettledAt == nil || row.AttributionAt == nil || row.SettlementMode != string(BudgetSettleReleaseNotStarted) || row.TokenKnown || row.CostKnown {
			return false
		}
		if !row.AttributionAt.Equal(*row.EffectiveSettledAt) {
			return false
		}
	case BudgetInterrupted:
		if row.ObservedSettledAt == nil || row.AttributionAt == nil || row.SettlementMode != "interrupt" && row.SettlementMode != "recovery" || row.TokenKnown || row.CostKnown {
			return false
		}
		if !row.AttributionAt.Equal(maxBudgetTime(*row.EffectiveSettledAt, row.ExecutionExpiresAt)) {
			return false
		}
	default:
		return false
	}
	return true
}

func validBudgetProofIdentity(proof BudgetProof) bool {
	return validBudgetText(proof.ActualModel, 256) && validRevision(proof.AccountRevision) && proof.PoolRevision >= 0 && proof.PoolRevision <= MaxRevision &&
		validBudgetText(proof.TransformRevision, 128) && validBudgetText(proof.BounderID, 128) && validRevision(proof.BounderRevision)
}

func validBudgetScope(scope BudgetScope) bool {
	if scope.Kind != ScopeEmployee && scope.Kind != ScopeKey && scope.Kind != ScopeGroup || !validMetadata(scope.ID, 256) || !validMetadata(scope.PolicyID, 256) ||
		!validRevision(scope.PolicyRevision) || scope.Kind == ScopeGroup != (scope.GroupRevision != nil) || scope.GroupRevision != nil && !validRevision(*scope.GroupRevision) ||
		scope.HardTPM != nil && (*scope.HardTPM < 1 || *scope.HardTPM > MaxRevision) || scope.HardCostMicro != nil && *scope.HardCostMicro < 1 ||
		scope.UnknownMode != "shadow" && scope.UnknownMode != "deny_unknown" {
		return false
	}
	if scope.HardCostMicro == nil {
		return scope.HardCurrency == "" && scope.HardWindow == ""
	}
	return validBudgetCurrency(scope.HardCurrency) && scope.HardWindow == ShadowWindowRolling24h
}

func enforcedBudgetScopes(scopes []BudgetScope) []BudgetScope {
	result := make([]BudgetScope, 0, len(scopes))
	for _, scope := range scopes {
		if scope.UnknownMode == "deny_unknown" && (scope.HardTPM != nil || scope.HardCostMicro != nil) {
			result = append(result, scope)
		}
	}
	return result
}

func sameBudgetReserve(row BudgetReservation, input BudgetReserve) bool {
	return row.RequestID == input.RequestID && row.ObservedReservedAt.Equal(input.ObservedAt) && sameBudgetProof(row.Proof, input.Proof)
}

func sameBudgetProof(left, right BudgetProof) bool {
	if left.Type != right.Type || left.ActualModel != right.ActualModel || left.AccountRevision != right.AccountRevision || left.PoolRevision != right.PoolRevision ||
		left.TransformRevision != right.TransformRevision || left.BounderID != right.BounderID || left.BounderRevision != right.BounderRevision {
		return false
	}
	if left.FourBuckets == nil != (right.FourBuckets == nil) || left.MutuallyExclusiveInput == nil != (right.MutuallyExclusiveInput == nil) {
		return false
	}
	return (left.FourBuckets == nil || *left.FourBuckets == *right.FourBuckets) &&
		(left.MutuallyExclusiveInput == nil || *left.MutuallyExclusiveInput == *right.MutuallyExclusiveInput)
}

func cloneBudgetProof(value BudgetProof) BudgetProof {
	copy := value
	if value.FourBuckets != nil {
		v := *value.FourBuckets
		copy.FourBuckets = &v
	}
	if value.MutuallyExclusiveInput != nil {
		v := *value.MutuallyExclusiveInput
		copy.MutuallyExclusiveInput = &v
	}
	return copy
}

func cloneBudgetScope(value BudgetScope) BudgetScope {
	value.GroupRevision = cloneInt64(value.GroupRevision)
	value.HardTPM = cloneInt64(value.HardTPM)
	value.HardCostMicro = cloneInt64(value.HardCostMicro)
	return value
}

func cloneBudgetPrice(value *accounting.PriceSnapshot) *accounting.PriceSnapshot {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func sameInt64Pointer(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func checkedBudgetSum(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		var ok bool
		total, ok = checkedBudgetAdd(total, value)
		if !ok {
			return 0, false
		}
	}
	return total, true
}

func checkedBudgetSumPointers(values ...*int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value == nil {
			return 0, false
		}
		var ok bool
		total, ok = checkedBudgetAdd(total, *value)
		if !ok {
			return 0, false
		}
	}
	return total, true
}

func checkedBudgetAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func maxBudgetTime(left, right time.Time) time.Time {
	if right.After(left) {
		return right
	}
	return left
}

func validBudgetCall(b *Budget, ctx context.Context, tx *sql.Tx) bool {
	return b != nil && b.db != nil && ctx != nil && tx != nil
}
func validBudgetMutationCall(b *Budget, ctx context.Context, tx *sql.Tx, input BudgetMutation) bool {
	return validBudgetCall(b, ctx, tx) && validMetadata(input.AttemptID, 256) && validUTCTime(input.ObservedAt)
}
func validBudgetProvider(value string) bool {
	switch accounting.Provider(value) {
	case accounting.ProviderOpenAI, accounting.ProviderOpenAICompatible, accounting.ProviderAnthropic, accounting.ProviderGemini, accounting.ProviderCodex:
		return true
	default:
		return false
	}
}
func validProtocolValue(value string) bool { return validProtocol(accounting.UsageProtocol(value)) }
func validBudgetCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, character := range value {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	return true
}
func validBudgetText(value string, maximum int) bool {
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
func validQuarantineReason(value string) bool {
	return value == "token_overage" || value == "cost_overage" || value == "token_and_cost_overage"
}
func budgetPointer(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	copy := value.Int64
	return &copy
}
func setBudgetOptional(target **int64, source sql.NullInt64) {
	if source.Valid {
		copy := source.Int64
		*target = &copy
	}
}
func nullableBudgetInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
