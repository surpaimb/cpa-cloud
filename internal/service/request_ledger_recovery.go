package service

// Independent composition of CPA Cloud's caller-owned recovery transactions.
import (
	"context"
	"database/sql"
	"time"

	"cpacloud.local/server/internal/governance"
)

// Startup calls this before creating workers or accepting requests. A failure in
// any sibling leaves all pending records available for the next startup retry.
func (a *App) recoverRequestLedgers(ctx context.Context, core *governance.Coordinator) error {
	if a == nil || a.store == nil || a.usage == nil || core == nil {
		return errUsageLedgerUnavailable
	}
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errUsageLedgerUnavailable
	}
	defer tx.Rollback()
	at, err := requestRecoveryTimeTx(ctx, tx, time.Now().UTC())
	if err != nil {
		return errUsageLedgerUnavailable
	}
	if a.budget != nil {
		recovered, err := a.budget.RecoverTx(ctx, tx, at)
		if err != nil {
			return errUsageLedgerUnavailable
		}
		at = recovered.EffectiveAt
	}
	if _, err := a.usage.ledger.RecoverInterruptedTx(ctx, tx, at); err != nil {
		return errUsageLedgerUnavailable
	}
	if _, err := core.RecoverInterruptedTx(ctx, tx, at); err != nil {
		return errUsageLedgerUnavailable
	}
	if _, err := tx.ExecContext(ctx, `UPDATE model_requests SET outcome='interrupted',finished_at=? WHERE outcome='running'`, at.Format(time.RFC3339Nano)); err != nil {
		return errUsageLedgerUnavailable
	}
	if err := tx.Commit(); err != nil {
		return errUsageLedgerUnavailable
	}
	return nil
}

// A wall-clock rollback must not violate either ledger's timestamp invariant.
// Read and validate each relevant timestamp instead of comparing textual MAXs
// whose legacy fractional-second representations may differ.
func requestRecoveryTimeTx(ctx context.Context, tx *sql.Tx, now time.Time) (time.Time, error) {
	rows, err := tx.QueryContext(ctx, `SELECT started_at FROM accounting_requests WHERE status='pending'
		UNION ALL SELECT started_at FROM accounting_attempts WHERE status='pending'
		UNION ALL SELECT a.finished_at FROM accounting_attempts a JOIN accounting_requests r ON r.id=a.request_id WHERE r.status='pending' AND a.status<>'pending'
		UNION ALL SELECT effective_lease_at FROM governance_requests WHERE status='pending'
		UNION ALL SELECT last_effective_admission_at FROM governance_settings WHERE last_effective_admission_at IS NOT NULL
		UNION ALL SELECT started_at FROM model_requests WHERE outcome='running'`)
	if err != nil {
		return time.Time{}, errUsageLedgerUnavailable
	}
	defer rows.Close()
	at := now.UTC()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return time.Time{}, errUsageLedgerUnavailable
		}
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || parsed.IsZero() || parsed.Year() < 1 || parsed.Year() > 9999 {
			return time.Time{}, errUsageLedgerUnavailable
		}
		if parsed.After(at) {
			at = parsed.UTC()
		}
	}
	if err := rows.Err(); err != nil {
		return time.Time{}, errUsageLedgerUnavailable
	}
	if err := rows.Close(); err != nil {
		return time.Time{}, errUsageLedgerUnavailable
	}
	return at, nil
}
