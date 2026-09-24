package governance

import (
	"context"
	"database/sql"

	"cpacloud.local/server/internal/accounting"
)

// The selector-aware configuration is owned by the service management layer,
// while reservation arithmetic remains in this package. An absent table means
// the optional E2 component is not installed; a present malformed table fails
// closed through the scans and scope validation below.
func loadGeneralBudgetRequestScopes(ctx context.Context, query budgetQuery, requestID string) ([]BudgetScope, error) {
	present, err := generalBudgetTablePresent(ctx, query, "governance_general_budget_request_scopes")
	if err != nil || !present {
		return nil, err
	}
	rows, err := query.QueryContext(ctx, `SELECT scope_kind,scope_id,protocol,model,policy_id,policy_revision,group_revision,settings_revision,selector_revision,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode
		FROM governance_general_budget_request_scopes WHERE request_id=? ORDER BY scope_kind,scope_id,protocol,model,policy_id`, requestID)
	if err != nil {
		return nil, ErrUnavailable
	}
	return scanGeneralBudgetScopes(rows)
}

func loadGeneralBudgetReservationScopes(ctx context.Context, query budgetQuery, attemptID string) ([]BudgetScope, error) {
	present, err := generalBudgetTablePresent(ctx, query, "governance_general_budget_reservation_scopes")
	if err != nil || !present {
		return nil, err
	}
	rows, err := query.QueryContext(ctx, `SELECT scope_kind,scope_id,protocol,model,policy_id,policy_revision,group_revision,settings_revision,selector_revision,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode
		FROM governance_general_budget_reservation_scopes WHERE attempt_id=? ORDER BY scope_kind,scope_id,protocol,model,policy_id`, attemptID)
	if err != nil {
		return nil, ErrUnavailable
	}
	return scanGeneralBudgetScopes(rows)
}

func generalBudgetTablePresent(ctx context.Context, query budgetQuery, table string) (bool, error) {
	var count int
	if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil {
		return false, ErrUnavailable
	}
	if count != 0 && count != 1 {
		return false, ErrSchema
	}
	return count == 1, nil
}

func scanGeneralBudgetScopes(rows *sql.Rows) ([]BudgetScope, error) {
	result := make([]BudgetScope, 0)
	for rows.Next() {
		scope := BudgetScope{General: true}
		var protocol string
		var groupRevision, hardTPM, hardCost sql.NullInt64
		if err := rows.Scan(&scope.Kind, &scope.ID, &protocol, &scope.SelectorModel, &scope.PolicyID, &scope.PolicyRevision, &groupRevision, &scope.SettingsRevision, &scope.SelectorRevision,
			&hardTPM, &hardCost, &scope.HardCurrency, &scope.HardWindow, &scope.UnknownMode); err != nil {
			rows.Close()
			return nil, ErrUnavailable
		}
		scope.SelectorProtocol = accounting.UsageProtocol(protocol)
		setBudgetOptional(&scope.GroupRevision, groupRevision)
		setBudgetOptional(&scope.HardTPM, hardTPM)
		setBudgetOptional(&scope.HardCostMicro, hardCost)
		if !validBudgetScope(scope) || !validRevision(scope.SettingsRevision) {
			rows.Close()
			return nil, ErrSchema
		}
		result = append(result, scope)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, ErrUnavailable
	}
	if err := rows.Close(); err != nil {
		return nil, ErrUnavailable
	}
	return result, nil
}
