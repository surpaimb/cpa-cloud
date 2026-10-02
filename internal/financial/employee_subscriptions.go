// Independently authored for docs/employee-self-subscription-status-contract.md.
package financial

import (
	"context"
	"database/sql"
	"time"
)

type EmployeeSubscriptionItem struct {
	SubscriptionID string
	Interval       string
	Status         string
	StartedAt      string
	PeriodEndAt    *string
	CancelledAt    *string
	Revision       int64
}

type EmployeeSubscriptionPage struct {
	Items        []EmployeeSubscriptionItem
	NextPosition string
}

type employeeSubscriptionReadHooks struct {
	afterRows func()
	closeRows func(*sql.Rows) error
	commit    func(*sql.Tx) error
}

func (c *Commercial) ReadEmployeeSubscriptions(ctx context.Context, employeeID, beforeID string, limit int, asOf time.Time) (EmployeeSubscriptionPage, error) {
	return c.readEmployeeSubscriptions(ctx, employeeID, beforeID, limit, asOf, employeeSubscriptionReadHooks{})
}

func (c *Commercial) readEmployeeSubscriptions(ctx context.Context, employeeID, beforeID string, limit int, asOf time.Time, hooks employeeSubscriptionReadHooks) (EmployeeSubscriptionPage, error) {
	if c == nil || c.db == nil || ctx == nil || !validText(employeeID, 256) || limit < 1 || limit > 50 ||
		(beforeID != "" && !validCommercialText(beforeID, 256)) || asOf.Location() != time.UTC {
		return EmployeeSubscriptionPage{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EmployeeSubscriptionPage{}, ErrUnavailable
	}
	defer tx.Rollback()
	if validateSchema(ctx, tx) != nil || validateCommercialSchema(ctx, tx, commercialOperationsDDL, false) != nil {
		return EmployeeSubscriptionPage{}, ErrUnavailable
	}
	query := `SELECT s.id,typeof(s.id),s.interval,typeof(s.interval),s.status,typeof(s.status),
		s.started_at,typeof(s.started_at),s.period_end_at,typeof(s.period_end_at),s.cancelled_at,typeof(s.cancelled_at),s.revision,typeof(s.revision),
		s.currency,typeof(s.currency),a.id,typeof(a.id),a.owner_key,typeof(a.owner_key),a.key_id,
		a.resource_kind,typeof(a.resource_kind),a.resource_id,typeof(a.resource_id),a.currency,typeof(a.currency),
		a.created_at,typeof(a.created_at)
		FROM financial_subscriptions s JOIN financial_accounts a ON a.id=s.account_id
		WHERE a.owner_kind='employee' AND a.employee_id=?`
	args := []any{employeeID}
	if beforeID != "" {
		query += ` AND s.id<?`
		args = append(args, beforeID)
	}
	query += ` ORDER BY s.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return EmployeeSubscriptionPage{}, ErrUnavailable
	}
	page := EmployeeSubscriptionPage{Items: make([]EmployeeSubscriptionItem, 0, limit+1)}
	for rows.Next() {
		item, err := scanEmployeeSubscription(rows, employeeID, asOf)
		if err != nil {
			rows.Close()
			return EmployeeSubscriptionPage{}, ErrUnavailable
		}
		page.Items = append(page.Items, item)
	}
	iterationErr := rows.Err()
	closeRows := rows.Close
	if hooks.closeRows != nil {
		closeRows = func() error { return hooks.closeRows(rows) }
	}
	closeErr := closeRows()
	if iterationErr != nil || closeErr != nil || ctx.Err() != nil {
		return EmployeeSubscriptionPage{}, ErrUnavailable
	}
	if hooks.afterRows != nil {
		hooks.afterRows()
	}
	if ctx.Err() != nil {
		return EmployeeSubscriptionPage{}, ErrUnavailable
	}
	if len(page.Items) > limit {
		page.Items = page.Items[:limit]
		page.NextPosition = page.Items[limit-1].SubscriptionID
	}
	commit := tx.Commit
	if hooks.commit != nil {
		commit = func() error { return hooks.commit(tx) }
	}
	if err := commit(); err != nil {
		return EmployeeSubscriptionPage{}, ErrUnavailable
	}
	if ctx.Err() != nil {
		return EmployeeSubscriptionPage{}, ErrUnavailable
	}
	return page, nil
}

func scanEmployeeSubscription(rows *sql.Rows, employeeID string, asOf time.Time) (EmployeeSubscriptionItem, error) {
	var id, idType, interval, intervalType, status, statusType, started, startedType string
	var end, cancelled sql.NullString
	var endType, cancelledType, subscriptionCurrency, subscriptionCurrencyType string
	var revision int64
	var revisionType string
	var accountID, accountIDType, accountOwnerKey, accountOwnerKeyType string
	var keyID sql.NullString
	var resourceKind, resourceKindType, resourceID, resourceIDType, accountCurrency, accountCurrencyType string
	var accountCreated, accountCreatedType string
	if err := rows.Scan(&id, &idType, &interval, &intervalType, &status, &statusType,
		&started, &startedType, &end, &endType, &cancelled, &cancelledType, &revision, &revisionType,
		&subscriptionCurrency, &subscriptionCurrencyType, &accountID, &accountIDType,
		&accountOwnerKey, &accountOwnerKeyType, &keyID, &resourceKind, &resourceKindType,
		&resourceID, &resourceIDType, &accountCurrency, &accountCurrencyType,
		&accountCreated, &accountCreatedType); err != nil {
		return EmployeeSubscriptionItem{}, ErrSchema
	}
	created, createdErr := time.Parse(time.RFC3339Nano, accountCreated)
	if idType != "text" || !validCommercialText(id, 256) || intervalType != "text" || statusType != "text" ||
		startedType != "text" || (endType != "null" && endType != "text") ||
		(cancelledType != "null" && cancelledType != "text") || revisionType != "integer" || revision < 1 || revision > subscriptionRevisionMax ||
		subscriptionCurrencyType != "text" || accountCurrencyType != "text" ||
		!validCurrency(subscriptionCurrency) || accountCurrency != subscriptionCurrency ||
		accountIDType != "text" || !validCommercialText(accountID, 256) ||
		accountOwnerKeyType != "text" || accountOwnerKey != ownerKey(Owner{Kind: OwnerEmployee, EmployeeID: employeeID}) || keyID.Valid ||
		resourceKindType != "text" || resourceKind != "" || resourceIDType != "text" || resourceID != "" ||
		accountCreatedType != "text" || createdErr != nil || created.UTC().Format(time.RFC3339Nano) != accountCreated {
		return EmployeeSubscriptionItem{}, ErrSchema
	}
	start, err := parseSubscriptionStart(started)
	if err != nil {
		return EmployeeSubscriptionItem{}, ErrSchema
	}
	subscription := Subscription{Interval: interval, Status: status, StartedAt: start}
	if end.Valid {
		parsed, err := parseSubscriptionEnd(end.String)
		if err != nil {
			return EmployeeSubscriptionItem{}, ErrSchema
		}
		subscription.PeriodEndAt = &parsed
	}
	if cancelled.Valid {
		parsed, err := parseSubscriptionStart(cancelled.String)
		if err != nil || parsed.Before(start) {
			return EmployeeSubscriptionItem{}, ErrSchema
		}
		subscription.CancelledAt = &parsed
	}
	if err := effectiveSubscription(&subscription, asOf); err != nil {
		return EmployeeSubscriptionItem{}, ErrSchema
	}
	return EmployeeSubscriptionItem{SubscriptionID: id, Interval: interval, Status: subscription.Status, Revision: revision,
		StartedAt: started, PeriodEndAt: nullableSubscriptionText(end), CancelledAt: nullableSubscriptionText(cancelled)}, nil
}

func nullableSubscriptionText(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}
