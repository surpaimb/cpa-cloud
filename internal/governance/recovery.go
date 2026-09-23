package governance

import (
	"context"
	"database/sql"
	"time"
)

func (c *Coordinator) RecoverInterrupted(ctx context.Context, at time.Time) (RecoveryResult, error) {
	if c == nil || c.db == nil || ctx == nil || !validUTCTime(at) {
		return RecoveryResult{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return RecoveryResult{}, ErrUnavailable
	}
	defer tx.Rollback()
	result, err := c.RecoverInterruptedTx(ctx, tx, at)
	if err != nil {
		return RecoveryResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return RecoveryResult{}, ErrUnavailable
	}
	return result, nil
}

// RecoverInterruptedTx records restart recovery in the caller-owned transaction.
// A nil error means only that the writes are present in tx; the caller must still
// commit the transaction and handle an uncertain commit outcome conservatively.
func (c *Coordinator) RecoverInterruptedTx(ctx context.Context, tx *sql.Tx, at time.Time) (RecoveryResult, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validUTCTime(at) {
		return RecoveryResult{}, ErrInvalid
	}
	return c.recoverInterrupted(ctx, tx, at)
}

func (c *Coordinator) recoverInterrupted(ctx context.Context, tx *sql.Tx, at time.Time) (RecoveryResult, error) {
	if err := lockSettingsRow(ctx, tx); err != nil {
		return RecoveryResult{}, err
	}
	settings, err := readSettings(ctx, tx)
	if err != nil {
		return RecoveryResult{}, err
	}
	effectiveAt := at.UTC()
	if settings.LastEffectiveAdmissionTime != nil && settings.LastEffectiveAdmissionTime.After(effectiveAt) {
		effectiveAt = *settings.LastEffectiveAdmissionTime
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,effective_lease_at,expires_at FROM governance_requests WHERE status='pending' ORDER BY id`)
	if err != nil {
		return RecoveryResult{}, ErrUnavailable
	}
	type pending struct {
		id      string
		started time.Time
		expires time.Time
	}
	items := make([]pending, 0)
	for rows.Next() {
		var item pending
		var started, expires string
		if err := rows.Scan(&item.id, &started, &expires); err != nil {
			rows.Close()
			return RecoveryResult{}, ErrUnavailable
		}
		item.started, err = parseStoredTime(started)
		if err != nil {
			rows.Close()
			return RecoveryResult{}, err
		}
		item.expires, err = parseStoredTime(expires)
		if err != nil {
			rows.Close()
			return RecoveryResult{}, err
		}
		items = append(items, item)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		return RecoveryResult{}, ErrUnavailable
	}
	result := RecoveryResult{}
	for _, item := range items {
		finished := effectiveAt
		if item.started.After(finished) {
			finished = item.started
		}
		var released any
		if !item.expires.After(effectiveAt) {
			released = formatTime(item.expires)
		}
		write, err := tx.ExecContext(ctx, `UPDATE governance_requests SET
			observed_finished_at=?,effective_finished_at=?,released_at=?,status='interrupted'
			WHERE id=? AND status='pending'`, formatTime(at), formatTime(finished), released, item.id)
		if err != nil {
			return RecoveryResult{}, ErrUnavailable
		}
		changed, err := write.RowsAffected()
		if err != nil || changed != 1 {
			return RecoveryResult{}, ErrUnavailable
		}
		result.Interrupted++
	}
	return result, nil
}
