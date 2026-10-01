package financial

// Independently authored for docs/financial-actor-provenance-contract.md and
// SQLite's documented table-rebuild/foreign-key procedure.

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"time"
)

func operationTableDDL(ctx context.Context, tx *sql.Tx, name string) (string, bool, error) {
	var kind, ddl string
	err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &ddl)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, ErrUnavailable
	}
	if kind != "table" {
		return "", false, ErrSchema
	}
	return ddl, true, nil
}

func migrateActorProvenance(ctx context.Context, db *sql.DB) error {
	if ctx == nil || db == nil {
		return ErrInvalid
	}
	preflight, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ErrUnavailable
	}
	ledgerDDL, hasLedger, err := operationTableDDL(ctx, preflight, "financial_operations")
	if err != nil {
		preflight.Rollback()
		return err
	}
	commercialDDL, hasCommercial, err := operationTableDDL(ctx, preflight, "financial_commercial_operations")
	if err != nil {
		preflight.Rollback()
		return err
	}
	if !hasLedger {
		preflight.Rollback()
		if hasCommercial {
			return ErrSchema
		}
		return nil
	}
	if normalize(ledgerDDL) == normalize(storedDDL(operationsDDL)) {
		preflight.Rollback()
		if hasCommercial && normalize(commercialDDL) != normalize(storedDDL(commercialOperationsDDL)) {
			return ErrSchema
		}
		return nil
	}
	if normalize(ledgerDDL) != normalize(storedDDL(operationsLegacyDDL)) {
		preflight.Rollback()
		return ErrSchema
	}
	if hasCommercial && normalize(commercialDDL) != normalize(storedDDL(commercialOperationsLegacyDDL)) &&
		normalize(commercialDDL) != normalize(storedDDL(commercialOperationsBeforeOneShotDDL)) &&
		normalize(commercialDDL) != normalize(storedDDL(commercialOperationsBeforeActorDDL)) {
		preflight.Rollback()
		return ErrSchema
	}
	if err := validateSchemaVersion(ctx, preflight, operationsLegacyDDL, true); err != nil {
		preflight.Rollback()
		return err
	}
	if err := validateStored(ctx, preflight); err != nil {
		preflight.Rollback()
		return err
	}
	if err := preflight.Commit(); err != nil {
		return ErrUnavailable
	}
	// The existing exact-generation commercial migrator also verifies all
	// companion tables, links, indexes, triggers and stored rows before the
	// actor-column rebuild. A crash here leaves a recognized L1/C3 database.
	if hasCommercial {
		if err := NewCommercial(db).migrateLegacy(ctx); err != nil {
			return err
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return ErrUnavailable
	}
	defer conn.Close()
	var foreignKeys int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		return ErrSchema
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		return ErrUnavailable
	}
	restore := func() error { return restoreActorForeignKeys(db, conn) }
	tx, err := beginActorWriteTx(ctx, conn)
	if err != nil {
		_ = restore()
		return ErrUnavailable
	}
	migrateErr := rebuildActorTables(ctx, tx, hasCommercial)
	if migrateErr != nil {
		_ = tx.Rollback()
		if restoreErr := restore(); restoreErr != nil {
			return restoreErr
		}
		return migrateErr
	}
	counts, err := migratedActorCounts(ctx, tx, hasCommercial)
	if err != nil {
		_ = tx.Rollback()
		if restoreErr := restore(); restoreErr != nil {
			return restoreErr
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		_ = restore()
		return ErrUnavailable
	}
	if err := restore(); err != nil {
		return err
	}
	log.Printf("financial actor migration committed: ledger admin=%d employee=%d system=%d legacy_unknown=%d; commercial admin=%d employee=%d system=%d legacy_unknown=%d", counts.ledger[0], counts.ledger[1], counts.ledger[2], counts.ledger[3], counts.commercial[0], counts.commercial[1], counts.commercial[2], counts.commercial[3])
	return nil
}

type actorMigrationCounts struct {
	ledger     [4]int64
	commercial [4]int64
}

func migratedActorCounts(ctx context.Context, tx *sql.Tx, hasCommercial bool) (actorMigrationCounts, error) {
	var counts actorMigrationCounts
	query := `SELECT COALESCE(SUM(actor_kind='admin'),0),COALESCE(SUM(actor_kind='employee'),0),COALESCE(SUM(actor_kind='system'),0),COALESCE(SUM(actor_kind='legacy_unknown'),0) FROM `
	if err := tx.QueryRowContext(ctx, query+`financial_operations`).Scan(&counts.ledger[0], &counts.ledger[1], &counts.ledger[2], &counts.ledger[3]); err != nil {
		return actorMigrationCounts{}, ErrUnavailable
	}
	if hasCommercial {
		if err := tx.QueryRowContext(ctx, query+`financial_commercial_operations`).Scan(&counts.commercial[0], &counts.commercial[1], &counts.commercial[2], &counts.commercial[3]); err != nil {
			return actorMigrationCounts{}, ErrUnavailable
		}
	}
	return counts, nil
}

// modernc.org/sqlite v1.38.2 uses plain BEGIN (DEFERRED) unless its DSN has
// _txlock. The first statement must therefore write the main database before
// any validation reads. SQLite permits only one concurrent write transaction.
func beginActorWriteTx(ctx context.Context, conn *sql.Conn) (*sql.Tx, error) {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE financial_actor_migration_write_lock (id INTEGER PRIMARY KEY)`); err != nil {
		tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func restoreActorForeignKeys(db *sql.DB, conn *sql.Conn) error {
	restoreCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := conn.ExecContext(restoreCtx, `PRAGMA foreign_keys=ON`); err != nil {
		db.Close()
		return ErrUnavailable
	}
	var enabled int
	if err := conn.QueryRowContext(restoreCtx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		db.Close()
		return ErrUnavailable
	}
	return nil
}

func rebuildActorTables(ctx context.Context, tx *sql.Tx, hasCommercial bool) error {
	if err := validateSchemaVersion(ctx, tx, operationsLegacyDDL, true); err != nil {
		return err
	}
	if err := validateStored(ctx, tx); err != nil {
		return err
	}
	if hasCommercial {
		if err := validateCommercialSchema(ctx, tx, commercialOperationsBeforeActorDDL, true); err != nil {
			return err
		}
		if err := validateCommercialStored(ctx, tx); err != nil {
			return err
		}
	}
	ledgerStatements := []string{
		`CREATE TEMP TABLE actor_ledger_stage AS SELECT rowid AS old_rowid,operation_id,action,actor_admin_id,resource_kind,resource_id,payload_digest,created_at FROM financial_operations`,
		`DROP TABLE financial_operations`,
		operationsDDL,
		`INSERT INTO financial_operations(rowid,operation_id,action,actor_kind,actor_admin_id,actor_employee_id,actor_system_id,resource_kind,resource_id,payload_digest,digest_version,created_at) SELECT old_rowid,operation_id,action,CASE WHEN actor_admin_id IS NOT NULL THEN 'admin' ELSE 'legacy_unknown' END,actor_admin_id,NULL,NULL,resource_kind,resource_id,payload_digest,1,created_at FROM actor_ledger_stage`,
		`DROP TABLE actor_ledger_stage`,
		operationsNoUpdateDDL, operationsNoDeleteDDL, operationsNoUnknownInsertDDL,
	}
	for _, statement := range ledgerStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return ErrSchema
		}
	}
	if hasCommercial {
		commercialStatements := []string{
			`CREATE TEMP TABLE actor_commercial_stage AS SELECT rowid AS old_rowid,operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at FROM financial_commercial_operations`,
			`DROP TABLE financial_commercial_operations`,
			commercialOperationsDDL,
			`INSERT INTO financial_commercial_operations(rowid,operation_id,action,actor_kind,actor_admin_id,actor_employee_id,actor_system_id,payload_digest,resource_kind,resource_id,revision,created_at) SELECT old_rowid,operation_id,action,CASE WHEN actor_admin_id IS NOT NULL THEN 'admin' ELSE 'legacy_unknown' END,actor_admin_id,NULL,NULL,payload_digest,resource_kind,resource_id,revision,created_at FROM actor_commercial_stage`,
			`DROP TABLE actor_commercial_stage`,
			commercialOperationsNoUpdateDDL, commercialOperationsNoDeleteDDL, commercialOperationsNoUnknownInsertDDL,
		}
		for _, statement := range commercialStatements {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return ErrSchema
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE financial_actor_migration_write_lock`); err != nil {
		return ErrSchema
	}
	if err := validateSchema(ctx, tx); err != nil {
		return err
	}
	if err := validateStored(ctx, tx); err != nil {
		return err
	}
	if hasCommercial {
		if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
			return err
		}
		if err := validateCommercialStored(ctx, tx); err != nil {
			return err
		}
	}
	if err := validateActorStored(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return ErrUnavailable
	}
	violated := rows.Next()
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if violated {
		return ErrSchema
	}
	return nil
}

func validateActorStored(ctx context.Context, tx *sql.Tx) error {
	var invalid int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_operations WHERE
		(actor_kind='system' AND NOT ((action='payment_callback' AND actor_system_id='payment_callback') OR (action='subscription_purchase' AND actor_system_id='subscription_one_shot_worker'))) OR
		(actor_kind='legacy_unknown' AND digest_version<>1) OR digest_version NOT IN (1,2)`).Scan(&invalid); err != nil || invalid != 0 {
		return ErrSchema
	}
	var commercialDDL string
	err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='financial_commercial_operations'`).Scan(&commercialDDL)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil || normalize(commercialDDL) != normalize(storedDDL(commercialOperationsDDL)) {
		return ErrSchema
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_commercial_operations WHERE actor_kind='system' AND (action<>'subscription.renew' OR actor_system_id<>'subscription_one_shot_worker')`).Scan(&invalid); err != nil || invalid != 0 {
		return ErrSchema
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_operations l JOIN financial_commercial_operations c ON c.operation_id=l.operation_id WHERE
		l.actor_kind<>c.actor_kind OR COALESCE(l.actor_admin_id,'')<>COALESCE(c.actor_admin_id,'') OR
		COALESCE(l.actor_employee_id,'')<>COALESCE(c.actor_employee_id,'') OR COALESCE(l.actor_system_id,'')<>COALESCE(c.actor_system_id,'')`).Scan(&invalid); err != nil || invalid != 0 {
		return ErrSchema
	}
	return nil
}
