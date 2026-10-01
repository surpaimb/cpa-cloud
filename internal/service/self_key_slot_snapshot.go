// Independently authored for docs/employee-self-key-issuance-contract.md.
package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cpacloud.local/server/internal/keypolicy"
)

var errSelfSlotNotReady = errors.New("self Key slot is not ready")

type selfSlotSnapshot struct {
	Digest             [32]byte
	EmployeeRevision   int64
	KeyPolicyRevision  int64
	GovernanceRevision int64
	BudgetRevision     int64
	Name               string
	ExpiresAt          *string
}

// The snapshot covers values as well as revisions. In particular, a direct
// database edit that changes a limit without incrementing its revision cannot
// silently authorize an already armed slot.
func captureSelfSlotSnapshot(ctx context.Context, tx *sql.Tx, employeeID, keyID string) (selfSlotSnapshot, error) {
	var result selfSlotSnapshot
	var expiry, revoked sql.NullString
	var employeeStatus string
	var passwordHash []byte
	err := tx.QueryRowContext(ctx, `SELECT k.name,k.expires_at,k.revoked_at,e.revision,e.status,c.password_hash
		FROM access_keys k JOIN employees e ON e.id=k.employee_id
		LEFT JOIN employee_self_credentials c ON c.employee_id=e.id
		WHERE k.id=? AND k.employee_id=?`, keyID, employeeID).
		Scan(&result.Name, &expiry, &revoked, &result.EmployeeRevision, &employeeStatus, &passwordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return selfSlotSnapshot{}, errSelfSlotNotReady
	}
	if err != nil {
		return selfSlotSnapshot{}, err
	}
	if employeeStatus != "active" || len(passwordHash) == 0 || revoked.Valid || !validText(result.Name, 1, 120) || result.EmployeeRevision < 1 {
		return selfSlotSnapshot{}, errSelfSlotNotReady
	}
	if expiry.Valid {
		when, err := parseTime(expiry.String)
		if err != nil || !time.Now().UTC().Before(when) {
			return selfSlotSnapshot{}, errSelfSlotNotReady
		}
		result.ExpiresAt = &expiry.String
	}
	policy, err := keypolicy.LoadTx(ctx, tx, keyID)
	if err != nil {
		return selfSlotSnapshot{}, err
	}
	if policy.ProtocolMode != keypolicy.ModeSelected || len(policy.Protocols) == 0 || policy.ModelMode != keypolicy.ModeSelected || len(policy.Models) == 0 {
		return selfSlotSnapshot{}, errSelfSlotNotReady
	}
	result.KeyPolicyRevision = policy.Revision
	var governanceOn, budgetOn int64
	if err := tx.QueryRowContext(ctx, `SELECT enabled,budget_enabled,revision FROM governance_settings WHERE singleton=1`).Scan(&governanceOn, &budgetOn, &result.GovernanceRevision); err != nil {
		return selfSlotSnapshot{}, err
	}
	if governanceOn != 1 || budgetOn != 1 || result.GovernanceRevision < 1 {
		return selfSlotSnapshot{}, errSelfSlotNotReady
	}
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM governance_general_budget_settings WHERE singleton=1`).Scan(&result.BudgetRevision); err != nil {
		return selfSlotSnapshot{}, err
	}
	if result.BudgetRevision < 1 {
		return selfSlotSnapshot{}, errSelfSlotNotReady
	}
	var governanceBound int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_policies WHERE scope_kind='key' AND scope_id=? AND enabled=1 AND rpm_limit IS NOT NULL AND concurrency_limit IS NOT NULL`, keyID).Scan(&governanceBound); err != nil {
		return selfSlotSnapshot{}, err
	}
	if governanceBound != 1 {
		return selfSlotSnapshot{}, errSelfSlotNotReady
	}
	var budgetBound int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_general_budget_policies WHERE scope_kind='key' AND scope_id=? AND protocol='' AND model='' AND enabled=1 AND (token_limit IS NOT NULL OR cost_limit_micro IS NOT NULL)`, keyID).Scan(&budgetBound); err != nil {
		return selfSlotSnapshot{}, err
	}
	if budgetBound != 1 {
		return selfSlotSnapshot{}, errSelfSlotNotReady
	}
	governanceRows, err := readSelfSlotPolicyRows(ctx, tx, `SELECT * FROM governance_policies WHERE scope_kind='key' AND scope_id=? ORDER BY id`, keyID)
	if err != nil {
		return selfSlotSnapshot{}, err
	}
	budgetRows, err := readSelfSlotPolicyRows(ctx, tx, `SELECT * FROM governance_general_budget_policies WHERE scope_kind='key' AND scope_id=? ORDER BY id`, keyID)
	if err != nil {
		return selfSlotSnapshot{}, err
	}
	canonical := struct {
		EmployeeID         string
		KeyID              string
		Name               string
		ExpiresAt          *string
		EmployeeRevision   int64
		KeyPolicy          keypolicy.Policy
		GovernanceRevision int64
		BudgetRevision     int64
		GovernanceRows     [][]any
		BudgetRows         [][]any
	}{employeeID, keyID, result.Name, result.ExpiresAt, result.EmployeeRevision, policy, result.GovernanceRevision, result.BudgetRevision, governanceRows, budgetRows}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return selfSlotSnapshot{}, err
	}
	result.Digest = sha256.Sum256(raw)
	return result, nil
}

func readSelfSlotPolicyRows(ctx context.Context, tx *sql.Tx, statement, keyID string) ([][]any, error) {
	rows, err := tx.QueryContext(ctx, statement, keyID)
	if err != nil {
		return nil, err
	}
	columns, err := rows.Columns()
	if err != nil {
		rows.Close()
		return nil, err
	}
	items := make([][]any, 0, 4)
	for rows.Next() {
		if len(items) >= 1024 {
			rows.Close()
			return nil, errSelfSlotNotReady
		}
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, values)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return items, nil
}
