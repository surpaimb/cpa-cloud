package accounting

// Independently implemented from docs/account-group-cost-allocation-contract.md.
import (
	"context"
	"database/sql"
	"errors"
)

var allocationLedgerTables = []string{
	"accounting_attempt_allocation_snapshots",
	"accounting_usage_allocation_events",
	"accounting_usage_allocation_corrections",
	"account_group_allocation_legacy_attempts",
}

// Allocation tables are composed by the service migration. The accounting
// package remains usable in isolation: no tables means the optional projection
// is absent, while a partial projection fails closed.
func allocationLedgerAvailableTx(ctx context.Context, tx *sql.Tx, required bool) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('accounting_attempt_allocation_snapshots','accounting_usage_allocation_events','accounting_usage_allocation_corrections','account_group_allocation_legacy_attempts')`).Scan(&count)
	if err != nil {
		return false, err
	}
	if count == 0 {
		if required {
			return false, ErrAllocationUnavailable
		}
		return false, nil
	}
	if count != len(allocationLedgerTables) {
		if required {
			return false, ErrAllocationUnavailable
		}
		return false, ErrInvalid
	}
	return true, nil
}

func (l *Ledger) recordUsageAllocationEventTx(ctx context.Context, tx *sql.Tx, attemptID, eventID string, baseCost *int64) error {
	available, err := allocationLedgerAvailableTx(ctx, tx, l.requireAllocationProjection)
	if err != nil || !available {
		return err
	}
	multiplier, snapshot, legacy, err := loadAttemptAllocationMultiplierTx(ctx, tx, attemptID)
	if err != nil {
		return err
	}
	if legacy {
		return nil // Explicitly marked pre-migration attempt: never infer or backfill.
	}
	adjusted, err := multiplier.Apply(baseCost)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO accounting_usage_allocation_events(event_id,attempt_id,adjusted_cost_micro) VALUES(?,?,?) ON CONFLICT(event_id) DO NOTHING`, eventID, attemptID, nullableValue(adjusted))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 1 {
		return err
	}
	var storedAttempt string
	var stored sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT attempt_id,adjusted_cost_micro FROM accounting_usage_allocation_events WHERE event_id=?`, eventID).Scan(&storedAttempt, &stored); err != nil {
		return err
	}
	if storedAttempt != attemptID || !sameNullableInt(stored, adjusted) || snapshot == "" {
		return ErrConflict
	}
	return nil
}

func (l *Ledger) recordUsageAllocationCorrectionTx(ctx context.Context, tx *sql.Tx, correctionID, attemptID string, effectiveBaseCost *int64) error {
	available, err := allocationLedgerAvailableTx(ctx, tx, l.requireAllocationProjection)
	if err != nil || !available {
		return err
	}
	multiplier, _, legacy, err := loadAttemptAllocationMultiplierTx(ctx, tx, attemptID)
	if err != nil {
		return err
	}
	if legacy {
		return nil
	}
	adjusted, err := multiplier.Apply(effectiveBaseCost)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO accounting_usage_allocation_corrections(correction_id,attempt_id,adjusted_cost_micro) VALUES(?,?,?) ON CONFLICT(correction_id) DO NOTHING`, correctionID, attemptID, nullableValue(adjusted))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 1 {
		return err
	}
	var storedAttempt string
	var stored sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT attempt_id,adjusted_cost_micro FROM accounting_usage_allocation_corrections WHERE correction_id=?`, correctionID).Scan(&storedAttempt, &stored); err != nil {
		return err
	}
	if storedAttempt != attemptID || !sameNullableInt(stored, adjusted) {
		return ErrConflict
	}
	return nil
}

func loadAttemptAllocationMultiplierTx(ctx context.Context, tx *sql.Tx, attemptID string) (AllocationMultiplier, string, bool, error) {
	var ppm int64
	var version sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT multiplier_ppm,multiplier_version FROM accounting_attempt_allocation_snapshots WHERE attempt_id=?`, attemptID).Scan(&ppm, &version)
	if errors.Is(err, sql.ErrNoRows) {
		var legacy int
		legacyErr := tx.QueryRowContext(ctx, `SELECT 1 FROM account_group_allocation_legacy_attempts WHERE attempt_id=?`, attemptID).Scan(&legacy)
		if legacyErr == nil && legacy == 1 {
			return AllocationMultiplier{}, "", true, nil
		}
		if errors.Is(legacyErr, sql.ErrNoRows) {
			return AllocationMultiplier{}, "", false, ErrAllocationUnavailable
		}
		return AllocationMultiplier{}, "", false, legacyErr
	}
	if err != nil {
		return AllocationMultiplier{}, "", false, err
	}
	multiplier, err := NewAllocationMultiplier(ppm)
	if err != nil {
		return AllocationMultiplier{}, "", false, err
	}
	snapshot := "builtin:1x"
	if version.Valid {
		snapshot = version.String
	}
	return multiplier, snapshot, false, nil
}

func allocationLedgerAvailable(ctx context.Context, db *sql.DB, required bool) (bool, error) {
	if ctx == nil || db == nil {
		return false, ErrInvalid
	}
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('accounting_attempt_allocation_snapshots','accounting_usage_allocation_events','accounting_usage_allocation_corrections','account_group_allocation_legacy_attempts')`).Scan(&count)
	if err != nil {
		return false, err
	}
	if count == 0 {
		if required {
			return false, ErrAllocationUnavailable
		}
		return false, nil
	}
	if count != len(allocationLedgerTables) {
		if required {
			return false, ErrAllocationUnavailable
		}
		return false, ErrInvalid
	}
	if required {
		var invalid int
		err := db.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM accounting_attempts a
				LEFT JOIN accounting_attempt_allocation_snapshots s ON s.attempt_id=a.id
				LEFT JOIN account_group_allocation_legacy_attempts l ON l.attempt_id=a.id
				WHERE a.status<>'pending' AND s.attempt_id IS NULL AND l.attempt_id IS NULL)
			+
			(SELECT COUNT(*) FROM accounting_usage_events e
				JOIN accounting_attempt_allocation_snapshots s ON s.attempt_id=e.attempt_id
				LEFT JOIN accounting_usage_allocation_events ae ON ae.event_id=e.id
				WHERE ae.event_id IS NULL)
			+
			(SELECT COUNT(*) FROM accounting_usage_corrections c
				JOIN accounting_attempt_allocation_snapshots s ON s.attempt_id=c.attempt_id
				LEFT JOIN accounting_usage_allocation_corrections ac ON ac.correction_id=c.id
				WHERE ac.correction_id IS NULL)`).Scan(&invalid)
		if err != nil {
			return false, err
		}
		if invalid != 0 {
			return false, ErrAllocationUnavailable
		}
	}
	return true, nil
}
