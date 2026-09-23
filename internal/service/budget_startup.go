package service

// Independently composes the governance configuration and budget migrations.
import (
	"context"

	"cpacloud.local/server/internal/governance"
)

func (a *App) migrateGovernanceBudget(ctx context.Context, core *governance.Coordinator, policies *governanceManagementStore, budget *governance.Budget) error {
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return governance.ErrUnavailable
	}
	defer tx.Rollback()
	coreVersion, err := core.SchemaVersionTx(ctx, tx)
	if err != nil {
		return err
	}
	managementVersion, err := policies.schemaVersionTx(ctx, tx)
	if err != nil {
		return err
	}
	// Complete old, complete new, or wholly absent configurations only.
	// Accepting a mixed pair could conceal a previously partial migration.
	if coreVersion != managementVersion {
		return governance.ErrSchema
	}
	if err := core.MigrateTx(ctx, tx); err != nil {
		return err
	}
	if err := policies.MigrateTx(ctx, tx); err != nil {
		return err
	}
	if err := budget.MigrateTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return governance.ErrUnavailable
	}
	return nil
}
