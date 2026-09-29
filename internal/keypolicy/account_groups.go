// Independently authored KEY-02 account-pool group policy normalization and persistence.
package keypolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

const (
	MaxAccountGroupIDs      = 64
	MaxAccountGroupIDBytes  = 128
	MaxAccountGroupIDsBytes = 8192
)

// AllowsAccountGroup evaluates only the Key account-pool group layer. Service
// integration remains responsible for proving that a candidate route has a
// channel and that the channel belongs to the supplied group.
func AllowsAccountGroup(policy Policy, groupID string) bool {
	if validateStored(policy) != nil {
		return false
	}
	if policy.AccountGroupMode == ModeAll {
		return true
	}
	return groupID != "" && containsString(policy.AccountGroupIDs, groupID)
}

func normalizeAccountGroups(mode Mode, values []string) ([]string, error) {
	if values == nil || !validMode(mode) || mode == ModeAll && len(values) != 0 || len(values) > MaxAccountGroupIDs {
		return nil, ErrInvalidPolicy
	}
	totalBytes := 0
	seen := make(map[string]struct{}, len(values))
	result := make([]string, len(values))
	copy(result, values)
	for _, value := range result {
		valueBytes := len([]byte(value))
		totalBytes += valueBytes
		if !validAccountGroupID(value) || valueBytes > MaxAccountGroupIDBytes || totalBytes > MaxAccountGroupIDsBytes {
			return nil, ErrInvalidPolicy
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, ErrInvalidPolicy
		}
		seen[value] = struct{}{}
	}
	sort.Strings(result)
	return result, nil
}

func validAccountGroupID(value string) bool {
	if value == "" || len([]byte(value)) > MaxAccountGroupIDBytes {
		return false
	}
	for _, character := range value {
		if !(character == '-' || character == '_' || character == '.' || character == ':' || character == '/' || character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9') {
			return false
		}
	}
	return true
}

func validateAccountGroupsExist(ctx context.Context, tx *sql.Tx, mode Mode, groupIDs []string) error {
	if mode != ModeSelected {
		return nil
	}
	for _, groupID := range groupIDs {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM account_groups WHERE id=?`, groupID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidPolicy
		} else if err != nil {
			return fmt.Errorf("validate key policy account group: %w", err)
		}
	}
	return nil
}

func createAccountGroupTx(ctx context.Context, tx *sql.Tx, keyID string, replacement Replacement) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_account_groups(key_id,account_group_mode) VALUES(?,?)`, keyID, replacement.AccountGroupMode); err != nil {
		return fmt.Errorf("create key policy account groups: %w", err)
	}
	return replaceAccountGroupMembersTx(ctx, tx, keyID, replacement.AccountGroupIDs)
}

func replaceAccountGroupTx(ctx context.Context, tx *sql.Tx, keyID string, replacement Replacement) error {
	result, err := tx.ExecContext(ctx, `UPDATE access_key_policy_account_groups SET account_group_mode=? WHERE key_id=?`, replacement.AccountGroupMode, keyID)
	if err != nil {
		return fmt.Errorf("replace key policy account groups: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read key policy account group replacement result: %w", err)
	}
	if changed != 1 {
		return ErrPolicyMissing
	}
	return replaceAccountGroupMembersTx(ctx, tx, keyID, replacement.AccountGroupIDs)
}

func replaceAccountGroupMembersTx(ctx context.Context, tx *sql.Tx, keyID string, groupIDs []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM access_key_policy_account_group_members WHERE key_id=?`, keyID); err != nil {
		return fmt.Errorf("clear key policy account group members: %w", err)
	}
	for _, groupID := range groupIDs {
		if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_account_group_members(key_id,account_group_id) VALUES(?,?)`, keyID, groupID); err != nil {
			return fmt.Errorf("store key policy account group member: %w", err)
		}
	}
	return nil
}

func loadAccountGroupTx(ctx context.Context, tx *sql.Tx, keyID string, policy *Policy) error {
	err := tx.QueryRowContext(ctx, `SELECT account_group_mode FROM access_key_policy_account_groups WHERE key_id=?`, keyID).Scan(&policy.AccountGroupMode)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrPolicyMissing
	}
	if err != nil {
		return fmt.Errorf("load key policy account groups: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT account_group_id FROM access_key_policy_account_group_members WHERE key_id=? ORDER BY account_group_id`, keyID)
	if err != nil {
		return fmt.Errorf("load key policy account group members: %w", err)
	}
	defer rows.Close()
	policy.AccountGroupIDs = make([]string, 0)
	for rows.Next() {
		var groupID string
		if err := rows.Scan(&groupID); err != nil {
			return fmt.Errorf("scan key policy account group member: %w", err)
		}
		policy.AccountGroupIDs = append(policy.AccountGroupIDs, groupID)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate key policy account group members: %w", err)
	}
	return nil
}
