package service

// Independently authored integration of Key policy with the existing account
// pool group graph. This file owns service-level joins only; the policy value
// object and its persistence remain in internal/keypolicy.

import (
	"context"
	"database/sql"

	"cpacloud.local/server/internal/keypolicy"
)

// keyAccountGroupModelFilterSQL returns an EXISTS predicate for a public model
// with at least one currently eligible route in a group selected by the Key.
// The caller must bind keyID after any arguments preceding this predicate.
func (a *App) keyAccountGroupModelFilterSQL(modelAlias string, geminiOnly bool) string {
	return `EXISTS(
		SELECT 1
		FROM model_account_pool_routes key_group_route
		JOIN account_channels key_group_channel ON key_group_channel.id=key_group_route.channel_id
		JOIN access_key_policy_account_group_members key_group_member ON key_group_member.account_group_id=key_group_channel.group_id
		JOIN upstreams key_group_upstream ON key_group_upstream.id=key_group_route.upstream_id
		WHERE key_group_route.model_id=` + modelAlias + `.id
		  AND key_group_member.key_id=?
		  AND ` + a.eligibleUpstreamSQL("key_group_upstream", geminiOnly) + `
		  AND NOT EXISTS(SELECT 1 FROM account_recovery_states key_group_recovery WHERE key_group_recovery.account_id=key_group_upstream.id)
	)`
}

func (a *App) keyAccountGroupModelEligibleTx(ctx context.Context, tx *sql.Tx, policy keypolicy.Policy, keyID, model string, geminiOnly bool) (bool, error) {
	if policy.AccountGroupMode == keypolicy.ModeAll {
		return true, nil
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM models m WHERE m.id=? AND m.enabled=1 AND m.archived=0 AND `+a.keyAccountGroupModelFilterSQL("m", geminiOnly), model, keyID).Scan(&count)
	return count == 1, err
}

func keyAccountGroupRouteAllowedTx(ctx context.Context, tx *sql.Tx, policy keypolicy.Policy, keyID, model, accountID string) (bool, error) {
	if policy.AccountGroupMode == keypolicy.ModeAll {
		return true, nil
	}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
		FROM model_account_pool_routes r
		JOIN account_channels c ON c.id=r.channel_id
		JOIN access_key_policy_account_group_members m ON m.account_group_id=c.group_id
		WHERE r.model_id=? AND r.upstream_id=? AND m.key_id=?`, model, accountID, keyID).Scan(&count)
	return count == 1, err
}
