package service

// Independently implemented from docs/account-group-cost-allocation-contract.md.
import (
	"context"
	"database/sql"
	"errors"

	"cpacloud.local/server/internal/accounting"
)

func (a *App) recordAttemptAllocationSnapshotTx(ctx context.Context, tx *sql.Tx, attemptID, publicModel string, selected route) error {
	if a == nil || tx == nil || ctx == nil || attemptID == "" || publicModel == "" || selected.AccountID == "" {
		return accounting.ErrInvalid
	}
	available, err := accountGroupAllocationRuntimeAvailableTx(ctx, tx)
	if err != nil || !available {
		return err
	}
	snapshot, err := a.currentAttemptAllocationSnapshotTx(ctx, tx, publicModel, selected)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO accounting_attempt_allocation_snapshots(attempt_id,account_group_id,multiplier_version,multiplier_ppm)
		VALUES(?,?,?,?) ON CONFLICT(attempt_id) DO NOTHING`, attemptID, nullableTextValue(snapshot.GroupID), nullableTextValue(snapshot.Version), snapshot.Multiplier.PartsPerMillion())
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted == 1 {
		return err
	}
	var groupID, version sql.NullString
	var ppm int64
	if err := tx.QueryRowContext(ctx, `SELECT account_group_id,multiplier_version,multiplier_ppm FROM accounting_attempt_allocation_snapshots WHERE attempt_id=?`, attemptID).Scan(&groupID, &version, &ppm); err != nil {
		return err
	}
	if !sameNullableText(groupID, snapshot.GroupID) || !sameNullableText(version, snapshot.Version) || ppm != snapshot.Multiplier.PartsPerMillion() {
		return accounting.ErrConflict
	}
	return nil
}

func (a *App) currentAttemptAllocationSnapshotTx(ctx context.Context, tx *sql.Tx, publicModel string, selected route) (attemptAllocationSnapshot, error) {
	var explicit int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM model_account_pool_configs WHERE model_id=?`, publicModel).Scan(&explicit); err != nil {
		return attemptAllocationSnapshot{}, err
	}
	if explicit == 0 {
		return attemptAllocationSnapshot{Multiplier: accounting.DefaultAllocationMultiplier()}, nil
	}
	if explicit != 1 {
		return attemptAllocationSnapshot{}, accounting.ErrInvalid
	}
	var groupID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT ch.group_id FROM model_account_pool_routes r
		LEFT JOIN account_channels ch ON ch.id=r.channel_id
		WHERE r.model_id=? AND r.upstream_id=? AND r.upstream_model=? AND r.wire_protocol=?`, publicModel, selected.AccountID, selected.UpstreamModel, string(selected.WireProtocol)).Scan(&groupID)
	if err != nil {
		return attemptAllocationSnapshot{}, err
	}
	if !groupID.Valid {
		return attemptAllocationSnapshot{Multiplier: accounting.DefaultAllocationMultiplier()}, nil
	}
	view, multiplier, _, err := loadAccountGroupAllocationTx(ctx, tx, groupID.String)
	if err != nil {
		return attemptAllocationSnapshot{}, err
	}
	group, version := groupID.String, view.Version
	return attemptAllocationSnapshot{GroupID: &group, Version: &version, Multiplier: multiplier}, nil
}

func nullableTextValue(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func sameNullableText(value sql.NullString, expected *string) bool {
	if expected == nil {
		return !value.Valid
	}
	return value.Valid && value.String == *expected
}

func requireAttemptAllocationSnapshotTx(ctx context.Context, tx *sql.Tx, attemptID string) error {
	available, err := accountGroupAllocationRuntimeAvailableTx(ctx, tx)
	if err != nil || !available {
		return err
	}
	var ppm int64
	var groupID, version sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT account_group_id,multiplier_version,multiplier_ppm FROM accounting_attempt_allocation_snapshots WHERE attempt_id=?`, attemptID).Scan(&groupID, &version, &ppm)
	if errors.Is(err, sql.ErrNoRows) {
		return accounting.ErrNotFound
	}
	if err != nil {
		return err
	}
	if _, err := accounting.NewAllocationMultiplier(ppm); err != nil {
		return err
	}
	if groupID.Valid != version.Valid || !groupID.Valid && ppm != accounting.AllocationMultiplierScale {
		return accounting.ErrInvalid
	}
	return nil
}

func accountGroupAllocationRuntimeAvailableTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var tables int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN (
		'account_group_allocation_migration_state','account_group_allocation_versions','account_group_allocation_current','accounting_attempt_allocation_snapshots','accounting_usage_allocation_events','accounting_usage_allocation_corrections')`).Scan(&tables)
	if err != nil {
		return false, err
	}
	if tables == 0 {
		return false, nil // Package-isolated legacy fixtures do not compose the feature migration.
	}
	if tables != 6 {
		return false, accounting.ErrInvalid
	}
	return true, nil
}
