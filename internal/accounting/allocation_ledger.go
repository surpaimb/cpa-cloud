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
}

// Allocation tables are composed by the service migration. The accounting
// package remains usable in isolation: no tables means the optional projection
// is absent, while a partial projection fails closed.
func allocationLedgerAvailableTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('accounting_attempt_allocation_snapshots','accounting_usage_allocation_events','accounting_usage_allocation_corrections')`).Scan(&count)
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if count != len(allocationLedgerTables) {
		return false, ErrInvalid
	}
	return true, nil
}

func recordUsageAllocationEventTx(ctx context.Context, tx *sql.Tx, attemptID, eventID string, baseCost *int64) error {
	available, err := allocationLedgerAvailableTx(ctx, tx)
	if err != nil || !available {
		return err
	}
	multiplier, snapshot, err := loadAttemptAllocationMultiplierTx(ctx, tx, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // Pre-migration historical attempt: never infer or backfill.
	}
	if err != nil {
		return err
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

func recordUsageAllocationCorrectionTx(ctx context.Context, tx *sql.Tx, correctionID, attemptID string, effectiveBaseCost *int64) error {
	available, err := allocationLedgerAvailableTx(ctx, tx)
	if err != nil || !available {
		return err
	}
	multiplier, _, err := loadAttemptAllocationMultiplierTx(ctx, tx, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
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

func loadAttemptAllocationMultiplierTx(ctx context.Context, tx *sql.Tx, attemptID string) (AllocationMultiplier, string, error) {
	var ppm int64
	var version sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT multiplier_ppm,multiplier_version FROM accounting_attempt_allocation_snapshots WHERE attempt_id=?`, attemptID).Scan(&ppm, &version)
	if err != nil {
		return AllocationMultiplier{}, "", err
	}
	multiplier, err := NewAllocationMultiplier(ppm)
	if err != nil {
		return AllocationMultiplier{}, "", err
	}
	snapshot := "builtin:1x"
	if version.Valid {
		snapshot = version.String
	}
	return multiplier, snapshot, nil
}

func allocationLedgerAvailable(ctx context.Context, db *sql.DB) (bool, error) {
	if ctx == nil || db == nil {
		return false, ErrInvalid
	}
	var count int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('accounting_attempt_allocation_snapshots','accounting_usage_allocation_events','accounting_usage_allocation_corrections')`).Scan(&count)
	if err != nil {
		return false, err
	}
	if count == 0 {
		return false, nil
	}
	if count != len(allocationLedgerTables) {
		return false, ErrInvalid
	}
	return true, nil
}
