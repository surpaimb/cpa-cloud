// Independently authored for docs/employee-self-plan-catalog-contract.md.
package financial

import (
	"context"
	"database/sql"
	"strconv"
	"time"
)

type EmployeePlanCatalogItem struct {
	PlanID      string
	Name        string
	Interval    string
	PriceMicro  string
	CreditMicro string
	Revision    int64
}

type EmployeePlanCatalogPage struct {
	Available    bool
	Items        []EmployeePlanCatalogItem
	NextPosition string
}

type employeePlanCatalogReadHooks struct {
	afterRows func()
	rowsErr   func(*sql.Rows) error
	closeRows func(*sql.Rows) error
	commit    func(*sql.Tx) error
}

func (c *Commercial) ReadEmployeePlanCatalog(ctx context.Context, currency, afterID string, limit int) (EmployeePlanCatalogPage, error) {
	return c.readEmployeePlanCatalog(ctx, currency, afterID, limit, employeePlanCatalogReadHooks{})
}

func (c *Commercial) readEmployeePlanCatalog(ctx context.Context, currency, afterID string, limit int, hooks employeePlanCatalogReadHooks) (EmployeePlanCatalogPage, error) {
	if c == nil || c.db == nil || ctx == nil || !validCurrency(currency) || limit < 1 || limit > 50 ||
		(afterID != "" && !validCommercialText(afterID, 256)) {
		return EmployeePlanCatalogPage{}, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EmployeePlanCatalogPage{}, ErrUnavailable
	}
	defer tx.Rollback()
	if validateSchema(ctx, tx) != nil || validateCommercialSchema(ctx, tx) != nil {
		return EmployeePlanCatalogPage{}, ErrUnavailable
	}
	var enabled, revision int64
	var enabledType, revisionType, updated, updatedType string
	if err := tx.QueryRowContext(ctx, `SELECT enabled,typeof(enabled),revision,typeof(revision),updated_at,typeof(updated_at) FROM financial_settings WHERE singleton=1`).Scan(
		&enabled, &enabledType, &revision, &revisionType, &updated, &updatedType); err != nil {
		return EmployeePlanCatalogPage{}, ErrUnavailable
	}
	updatedTime, updatedErr := time.Parse(time.RFC3339Nano, updated)
	if enabledType != "integer" || enabled < 0 || enabled > 1 || revisionType != "integer" || revision < 1 || revision > subscriptionRevisionMax ||
		updatedType != "text" || updatedErr != nil || updatedTime.Location() != time.UTC || formatCommercialTime(updatedTime) != updated {
		return EmployeePlanCatalogPage{}, ErrUnavailable
	}
	page := EmployeePlanCatalogPage{Available: enabled == 1, Items: []EmployeePlanCatalogItem{}}
	if page.Available {
		query := `SELECT id,typeof(id),name,typeof(name),currency,typeof(currency),price_micro,typeof(price_micro),
			credit_micro,typeof(credit_micro),interval,typeof(interval),enabled,typeof(enabled),revision,typeof(revision),
			created_at,typeof(created_at),updated_at,typeof(updated_at)
			FROM financial_plans WHERE currency=? AND enabled=1`
		args := []any{currency}
		if afterID != "" {
			query += ` AND id>?`
			args = append(args, afterID)
		}
		query += ` ORDER BY id ASC LIMIT ?`
		args = append(args, limit+1)
		rows, err := tx.QueryContext(ctx, query, args...)
		if err != nil {
			return EmployeePlanCatalogPage{}, ErrUnavailable
		}
		for rows.Next() {
			item, err := scanEmployeePlanCatalog(rows, currency)
			if err != nil {
				rows.Close()
				return EmployeePlanCatalogPage{}, ErrUnavailable
			}
			page.Items = append(page.Items, item)
		}
		iterationErr := rows.Err()
		if hooks.rowsErr != nil {
			iterationErr = hooks.rowsErr(rows)
		}
		closeRows := rows.Close
		if hooks.closeRows != nil {
			closeRows = func() error { return hooks.closeRows(rows) }
		}
		closeErr := closeRows()
		if iterationErr != nil || closeErr != nil || ctx.Err() != nil {
			return EmployeePlanCatalogPage{}, ErrUnavailable
		}
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			page.NextPosition = page.Items[limit-1].PlanID
		}
	}
	if hooks.afterRows != nil {
		hooks.afterRows()
	}
	if ctx.Err() != nil {
		return EmployeePlanCatalogPage{}, ErrUnavailable
	}
	commit := tx.Commit
	if hooks.commit != nil {
		commit = func() error { return hooks.commit(tx) }
	}
	if err := commit(); err != nil || ctx.Err() != nil {
		return EmployeePlanCatalogPage{}, ErrUnavailable
	}
	return page, nil
}

func scanEmployeePlanCatalog(rows *sql.Rows, currency string) (EmployeePlanCatalogItem, error) {
	var id, idType, name, nameType, storedCurrency, currencyType string
	var interval, intervalType, priceType, creditType string
	var enabled, revision, price, credit int64
	var enabledType, revisionType, created, createdType, updated, updatedType string
	if err := rows.Scan(&id, &idType, &name, &nameType, &storedCurrency, &currencyType, &price, &priceType,
		&credit, &creditType, &interval, &intervalType, &enabled, &enabledType, &revision, &revisionType,
		&created, &createdType, &updated, &updatedType); err != nil {
		return EmployeePlanCatalogItem{}, ErrSchema
	}
	createdTime, createdErr := time.Parse(time.RFC3339Nano, created)
	updatedTime, updatedErr := time.Parse(time.RFC3339Nano, updated)
	if idType != "text" || !validCommercialText(id, 256) || nameType != "text" || !validCommercialText(name, 128) ||
		currencyType != "text" || storedCurrency != currency || priceType != "integer" || price < 1 ||
		creditType != "integer" || credit < 1 || intervalType != "text" || (interval != "one_time" && interval != "monthly") ||
		enabledType != "integer" || enabled != 1 || revisionType != "integer" || revision < 1 || revision > subscriptionRevisionMax ||
		createdType != "text" || createdErr != nil || createdTime.Location() != time.UTC || formatCommercialTime(createdTime) != created ||
		updatedType != "text" || updatedErr != nil || updatedTime.Location() != time.UTC || formatCommercialTime(updatedTime) != updated || updatedTime.Before(createdTime) {
		return EmployeePlanCatalogItem{}, ErrSchema
	}
	return EmployeePlanCatalogItem{PlanID: id, Name: name, Interval: interval,
		PriceMicro: strconv.FormatInt(price, 10), CreditMicro: strconv.FormatInt(credit, 10), Revision: revision}, nil
}
