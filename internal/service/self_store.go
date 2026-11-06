// Independently authored for docs/employee-self-service-foundation-contract.md.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"cpacloud.local/server/internal/selfschema"
)

// These definitions are intentionally audited verbatim. An existing object with
// the right name but weaker constraints must not be silently accepted.
var selfSchema = []struct{ name, kind, ddl string }{
	{"employee_self_credentials", "table", selfschema.Credentials},
	{"employee_self_sessions", "table", selfschema.Sessions},
	{"employee_self_sessions_expiry_idx", "index", selfschema.ExpiryIndex},
}

func (s *store) migrateSelfService(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, object := range selfSchema {
		var actual string
		err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, object.kind, object.name).Scan(&actual)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err = tx.ExecContext(ctx, object.ddl); err != nil {
				return fmt.Errorf("create self-service schema %s: %w", object.name, err)
			}
			actual = object.ddl
		} else if err != nil {
			return err
		}
		if strings.TrimSpace(actual) != strings.TrimSpace(object.ddl) {
			return fmt.Errorf("self-service schema %s is incompatible", object.name)
		}
	}
	// Expired digests are not needed for verification or recovery.
	now := selfTime(time.Now())
	if _, err := tx.ExecContext(ctx, `UPDATE employee_self_credentials SET enrollment_digest=NULL,enrollment_expires_at=NULL WHERE enrollment_expires_at<=?`, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM employee_self_sessions WHERE expires_at<=?`, now); err != nil {
		return err
	}
	return tx.Commit()
}

// Fixed-width UTC timestamps retain chronological order in SQLite TEXT
// comparisons even when the instant is at an exact second boundary.
func selfTime(value time.Time) string {
	return value.UTC().Format("2006-01-02T15:04:05.000000000Z")
}
