package financial

// Independently authored for docs/employee-self-subscription-renewal-links-contract.md.
// The caller owns the single read-only transaction and its final commit.

import (
	"context"
	"database/sql"
)

type EmployeeSubscriptionRenewalLinks struct {
	SubscriptionID string
	PredecessorID  *string
	SuccessorID    *string
}

type renewalLinkRow struct {
	predecessorID string
	successorID   string
	operationID   string
	createdAt     string
}

func (c *Commercial) ReadEmployeeSubscriptionRenewalLinksTx(ctx context.Context, tx *sql.Tx, employeeID, subscriptionID string) (EmployeeSubscriptionRenewalLinks, error) {
	return c.readEmployeeSubscriptionRenewalLinksTx(ctx, tx, employeeID, subscriptionID, purchaseSnapshotReadHooks{})
}

func (c *Commercial) readEmployeeSubscriptionRenewalLinksTx(ctx context.Context, tx *sql.Tx, employeeID, subscriptionID string, hooks purchaseSnapshotReadHooks) (EmployeeSubscriptionRenewalLinks, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validText(employeeID, 256) || !validCommercialText(subscriptionID, 256) {
		return EmployeeSubscriptionRenewalLinks{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return EmployeeSubscriptionRenewalLinks{}, ErrUnavailable
	}
	// The purchase snapshot first establishes direct ownership, then checks
	// the target's creation receipt, ledger, entries, and incoming-link shape.
	target, err := c.readEmployeeSubscriptionPurchaseSnapshotTx(ctx, tx, employeeID, subscriptionID, hooks)
	if err != nil {
		return EmployeeSubscriptionRenewalLinks{}, err
	}
	incoming, err := renewalLinksForTarget(ctx, tx, "successor_id", subscriptionID, hooks)
	if err != nil {
		return EmployeeSubscriptionRenewalLinks{}, err
	}
	outgoing, err := renewalLinksForTarget(ctx, tx, "predecessor_id", subscriptionID, hooks)
	if err != nil {
		return EmployeeSubscriptionRenewalLinks{}, err
	}
	if target.Interval == "one_time" && (incoming != nil || outgoing != nil) {
		return EmployeeSubscriptionRenewalLinks{}, ErrUnavailable
	}
	result := EmployeeSubscriptionRenewalLinks{SubscriptionID: subscriptionID}
	if incoming != nil {
		if incoming.successorID != subscriptionID || incoming.predecessorID == subscriptionID || incoming.createdAt != target.StartedAt {
			return EmployeeSubscriptionRenewalLinks{}, ErrUnavailable
		}
		prior, accountID, err := snapshotSubscription(ctx, tx, incoming.predecessorID)
		if err != nil {
			return EmployeeSubscriptionRenewalLinks{}, ErrUnavailable
		}
		if err := snapshotDirectAccount(ctx, tx, accountID, employeeID, prior.Currency, prior.StartedAt); err != nil {
			return EmployeeSubscriptionRenewalLinks{}, ErrUnavailable
		}
		if prior.Interval != "monthly" || prior.PlanID != target.PlanID || prior.PeriodEndAt == nil {
			return EmployeeSubscriptionRenewalLinks{}, ErrUnavailable
		}
		result.PredecessorID = &incoming.predecessorID
	}
	if outgoing != nil {
		if outgoing.predecessorID != subscriptionID || outgoing.successorID == subscriptionID || target.Interval != "monthly" {
			return EmployeeSubscriptionRenewalLinks{}, ErrUnavailable
		}
		// The successor's snapshot validates this exact incoming link, stored
		// predecessor expiry, operation/actor/time, and both ledger entries.
		next, err := c.readEmployeeSubscriptionPurchaseSnapshotTx(ctx, tx, employeeID, outgoing.successorID, hooks)
		if err != nil {
			return EmployeeSubscriptionRenewalLinks{}, ErrUnavailable
		}
		if next.Interval != "monthly" || next.PlanID != target.PlanID || next.StartedAt != outgoing.createdAt {
			return EmployeeSubscriptionRenewalLinks{}, ErrUnavailable
		}
		result.SuccessorID = &outgoing.successorID
	}
	if hooks.afterRows != nil {
		hooks.afterRows()
	}
	if ctx.Err() != nil {
		return EmployeeSubscriptionRenewalLinks{}, ErrUnavailable
	}
	return result, nil
}

func renewalLinksForTarget(ctx context.Context, tx *sql.Tx, column, id string, hooks purchaseSnapshotReadHooks) (*renewalLinkRow, error) {
	query := `SELECT predecessor_id,successor_id,operation_id,created_at FROM financial_subscription_renewals WHERE predecessor_id=? LIMIT 2`
	if column == "successor_id" {
		query = `SELECT predecessor_id,successor_id,operation_id,created_at FROM financial_subscription_renewals WHERE successor_id=? LIMIT 2`
	} else if column != "predecessor_id" {
		return nil, ErrInvalid
	}
	rows, err := snapshotRows(ctx, tx, query, 2, 4, hooks, id)
	if err != nil || len(rows) > 1 {
		return nil, ErrUnavailable
	}
	if len(rows) == 0 {
		return nil, nil
	}
	values := rows[0]
	predecessor, predecessorOK := snapshotText(values[0])
	successor, successorOK := snapshotText(values[1])
	operation, operationOK := snapshotText(values[2])
	created, createdOK := snapshotText(values[3])
	if !predecessorOK || !validCommercialText(predecessor, 256) || !successorOK || !validCommercialText(successor, 256) ||
		predecessor == successor || !operationOK || !validCommercialText(operation, 128) || !createdOK {
		return nil, ErrUnavailable
	}
	if _, err := parseSubscriptionStart(created); err != nil {
		return nil, ErrUnavailable
	}
	return &renewalLinkRow{predecessorID: predecessor, successorID: successor, operationID: operation, createdAt: created}, nil
}
