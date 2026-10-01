// Independently authored for docs/employee-self-key-issuance-contract.md.
package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const selfKeySlotsDDL = `CREATE TABLE employee_self_key_slots (
	employee_id TEXT PRIMARY KEY REFERENCES employees(id) ON DELETE RESTRICT,
	key_id TEXT NOT NULL UNIQUE REFERENCES access_keys(id) ON DELETE RESTRICT,
	state TEXT NOT NULL CHECK(state IN ('pending','armed','issued','cancelled')),
	arm_fingerprint BLOB CHECK(arm_fingerprint IS NULL OR (typeof(arm_fingerprint)='blob' AND length(arm_fingerprint)=32)),
	arm_employee_revision INTEGER,
	arm_key_policy_revision INTEGER,
	arm_governance_revision INTEGER,
	arm_budget_revision INTEGER,
	created_at TEXT NOT NULL,
	armed_at TEXT,
	issued_at TEXT,
	cancelled_at TEXT,
	CHECK((state='armed' OR state='issued') = (arm_fingerprint IS NOT NULL)),
	CHECK((state='issued') = (issued_at IS NOT NULL)),
	CHECK((state='cancelled') = (cancelled_at IS NOT NULL))
)`

func (s *store) migrateSelfKeySlots(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var actual string
	err = tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='employee_self_key_slots'`).Scan(&actual)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err = tx.ExecContext(ctx, selfKeySlotsDDL); err != nil {
			return fmt.Errorf("create self Key slot schema: %w", err)
		}
		actual = selfKeySlotsDDL
	} else if err != nil {
		return err
	}
	if strings.TrimSpace(actual) != strings.TrimSpace(selfKeySlotsDDL) {
		return errors.New("self Key slot schema is incompatible")
	}
	return tx.Commit()
}
