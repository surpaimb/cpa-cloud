// Independently authored for docs/admin-audit-financial-source-contract.md.
package financial

import (
	"context"
	"database/sql"
)

// ValidateCommercialAuditSource checks the existing immutable receipt source in
// the caller's read transaction. It neither migrates nor reads receipt content.
func ValidateCommercialAuditSource(ctx context.Context, tx *sql.Tx) error {
	if ctx == nil || tx == nil {
		return ErrSchema
	}
	objects := map[string]struct{ kind, table, ddl string }{
		"financial_commercial_operations":                   {"table", "financial_commercial_operations", commercialOperationsDDL},
		"financial_commercial_operations_no_update":         {"trigger", "financial_commercial_operations", commercialOperationsNoUpdateDDL},
		"financial_commercial_operations_no_delete":         {"trigger", "financial_commercial_operations", commercialOperationsNoDeleteDDL},
		"financial_commercial_operations_no_unknown_insert": {"trigger", "financial_commercial_operations", commercialOperationsNoUnknownInsertDDL},
	}
	for name, expected := range objects {
		var kind, table, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,tbl_name,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &table, &actual); err != nil || kind != expected.kind || table != expected.table || normalize(actual) != normalize(storedDDL(expected.ddl)) {
			return ErrSchema
		}
	}
	var triggers int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name='financial_commercial_operations'`).Scan(&triggers); err != nil || triggers != 3 {
		return ErrSchema
	}
	return nil
}
