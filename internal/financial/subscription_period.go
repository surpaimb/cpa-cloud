package financial

// Independently authored for docs/subscription-period-expiry-contract.md.

import (
	"context"
	"database/sql"
	"time"
)

const subscriptionEndLayout = "2006-01-02T15:04:05.000000000Z"
const subscriptionRevisionMax = int64(9007199254740991)

func monthlyPeriodEnd(start time.Time) (time.Time, error) {
	if start.Location() != time.UTC || start.Year() < 1 || start.Year() > 9999 {
		return time.Time{}, ErrSchema
	}
	year, month, day := start.Date()
	if month == time.December {
		year++
		month = time.January
	} else {
		month++
	}
	if year > 9999 {
		return time.Time{}, ErrSchema
	}
	last := time.Date(year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
	if day > last {
		day = last
	}
	return time.Date(year, month, day, start.Hour(), start.Minute(), start.Second(), start.Nanosecond(), time.UTC), nil
}

func parseSubscriptionStart(raw string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || parsed.Location() != time.UTC || parsed.Year() < 1 || parsed.Year() > 9999 || formatCommercialTime(parsed) != raw {
		return time.Time{}, ErrSchema
	}
	return parsed, nil
}

func parseSubscriptionEnd(raw string) (time.Time, error) {
	parsed, err := time.Parse(subscriptionEndLayout, raw)
	if err != nil || parsed.Location() != time.UTC || parsed.Format(subscriptionEndLayout) != raw || parsed.Year() < 1 || parsed.Year() > 9999 {
		return time.Time{}, ErrSchema
	}
	return parsed, nil
}

func subscriptionLegacySchema(ctx context.Context, tx *sql.Tx) (bool, error) {
	var actual string
	err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='financial_subscriptions'`).Scan(&actual)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, ErrUnavailable
	}
	if normalize(actual) == normalize(storedDDL(subscriptionsDDL)) {
		return false, nil
	}
	if normalize(actual) != normalize(storedDDL(subscriptionsLegacyDDL)) {
		return false, ErrSchema
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE tbl_name='financial_subscriptions' AND (type<>'table' OR name<>'financial_subscriptions')`).Scan(&count); err != nil || count != 2 { // one automatic PK index and one declared account index
		return false, ErrSchema
	}
	var indexSQL string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name='financial_subscriptions_account_idx'`).Scan(&indexSQL); err != nil || normalize(indexSQL) != normalize(storedDDL(subscriptionAccountIndexDDL)) {
		return false, ErrSchema
	}
	return true, nil
}

func migrateLegacySubscriptions(ctx context.Context, tx *sql.Tx) error {
	// The stage is transaction-local and is dropped before commit. No foreign key
	// references subscriptions, so rebuilding the child table keeps FK checks on.
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE subscription_expiry_stage AS SELECT *, CAST(NULL AS TEXT) AS period_end_at FROM financial_subscriptions`); err != nil {
		return ErrSchema
	}
	var cursor string
	for {
		rows, err := tx.QueryContext(ctx, `SELECT id,interval,status,started_at,cancelled_at,revision FROM subscription_expiry_stage WHERE id>? ORDER BY id LIMIT 100`, cursor)
		if err != nil {
			return ErrUnavailable
		}
		type candidate struct{ id, end string }
		batch := make([]candidate, 0, 100)
		for rows.Next() {
			var id, interval, status, started string
			var cancelled sql.NullString
			var revision int64
			if err := rows.Scan(&id, &interval, &status, &started, &cancelled, &revision); err != nil {
				rows.Close()
				return ErrSchema
			}
			start, err := parseSubscriptionStart(started)
			if err != nil || revision < 1 || revision > subscriptionRevisionMax || (status != "active" && status != "cancelled") || (status == "active" && cancelled.Valid) || (status == "cancelled" && !cancelled.Valid) {
				rows.Close()
				return ErrSchema
			}
			if cancelled.Valid {
				when, err := parseSubscriptionStart(cancelled.String)
				if err != nil || when.Before(start) {
					rows.Close()
					return ErrSchema
				}
			}
			end := ""
			switch interval {
			case "monthly":
				value, err := monthlyPeriodEnd(start)
				if err != nil {
					rows.Close()
					return ErrSchema
				}
				end = value.Format(subscriptionEndLayout)
			case "one_time":
			default:
				rows.Close()
				return ErrSchema
			}
			batch = append(batch, candidate{id, end})
		}
		iterationErr, closeErr := rows.Err(), rows.Close()
		if iterationErr != nil || closeErr != nil {
			return ErrUnavailable
		}
		if len(batch) == 0 {
			break
		}
		for _, item := range batch {
			if item.end != "" {
				if _, err := tx.ExecContext(ctx, `UPDATE subscription_expiry_stage SET period_end_at=? WHERE id=?`, item.end, item.id); err != nil {
					return ErrUnavailable
				}
			}
		}
		cursor = batch[len(batch)-1].id
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE financial_subscriptions`); err != nil {
		return ErrSchema
	}
	if _, err := tx.ExecContext(ctx, subscriptionsDDL); err != nil {
		return ErrSchema
	}
	columns := `id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,cancelled_at,revision`
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_subscriptions(`+columns+`) SELECT `+columns+` FROM subscription_expiry_stage`); err != nil {
		return ErrSchema
	}
	if _, err := tx.ExecContext(ctx, `DROP TABLE subscription_expiry_stage`); err != nil {
		return ErrSchema
	}
	if _, err := tx.ExecContext(ctx, subscriptionAccountIndexDDL); err != nil {
		return ErrSchema
	}
	return nil
}

func (c *Commercial) ExpireDueSubscriptions(ctx context.Context, asOf time.Time) (int, error) {
	if c == nil || c.db == nil || ctx == nil || asOf.Location() != time.UTC {
		return 0, ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer tx.Rollback()
	cutoff := asOf.Format(subscriptionEndLayout)
	rows, err := tx.QueryContext(ctx, `SELECT id,period_end_at FROM financial_subscriptions WHERE interval='monthly' AND status='active' AND period_end_at<=? ORDER BY period_end_at,id LIMIT 100`, cutoff)
	if err != nil {
		return 0, ErrUnavailable
	}
	type due struct{ id, end string }
	items := make([]due, 0, 100)
	for rows.Next() {
		var item due
		if err := rows.Scan(&item.id, &item.end); err != nil {
			rows.Close()
			return 0, ErrSchema
		}
		end, err := parseSubscriptionEnd(item.end)
		if err != nil || end.After(asOf) {
			rows.Close()
			return 0, ErrSchema
		}
		items = append(items, item)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return 0, ErrUnavailable
	}
	for _, item := range items {
		result, err := tx.ExecContext(ctx, `UPDATE financial_subscriptions SET status='expired',revision=revision+1 WHERE id=? AND interval='monthly' AND status='active' AND period_end_at=? AND revision<?`, item.id, item.end, subscriptionRevisionMax)
		if err != nil {
			return 0, ErrUnavailable
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return 0, ErrSchema
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, ErrUnavailable
	}
	return len(items), nil
}

func effectiveSubscription(item *Subscription, asOf time.Time) error {
	if item.Interval == "monthly" {
		if item.PeriodEndAt == nil || !item.PeriodEndAt.After(item.StartedAt) {
			return ErrSchema
		}
		frozen, err := monthlyPeriodEnd(item.StartedAt)
		if err != nil || !frozen.Equal(*item.PeriodEndAt) {
			return ErrSchema
		}
		if item.Status == "active" && !asOf.Before(*item.PeriodEndAt) {
			item.Status = "expired"
		}
	} else if item.Interval != "one_time" || item.PeriodEndAt != nil || item.Status == "expired" {
		return ErrSchema
	}
	if item.Status != "active" && item.Status != "cancelled" && item.Status != "expired" {
		return ErrSchema
	}
	if (item.Status == "cancelled") != (item.CancelledAt != nil) {
		return ErrSchema
	}
	return nil
}

func scanSubscription(scanner interface{ Scan(...any) error }, asOf time.Time) (Subscription, error) {
	var item Subscription
	var started string
	var end, cancelled, predecessor, successor sql.NullString
	if err := scanner.Scan(&item.ID, &item.AccountID, &item.PlanID, &item.PlanRevision, &item.PriceMicro, &item.CreditMicro, &item.Currency, &item.Interval, &item.Status, &started, &end, &cancelled, &item.Revision, &predecessor, &successor); err != nil {
		return Subscription{}, err
	}
	if predecessor.Valid {
		item.PredecessorID = predecessor.String
	}
	if successor.Valid {
		item.SuccessorID = successor.String
	}
	var err error
	item.StartedAt, err = parseSubscriptionStart(started)
	if err != nil {
		return Subscription{}, err
	}
	if end.Valid {
		value, err := parseSubscriptionEnd(end.String)
		if err != nil {
			return Subscription{}, err
		}
		item.PeriodEndAt = &value
	}
	if cancelled.Valid {
		value, err := parseSubscriptionStart(cancelled.String)
		if err != nil || value.Before(item.StartedAt) {
			return Subscription{}, ErrSchema
		}
		item.CancelledAt = &value
	}
	if item.Revision < 1 || item.Revision > subscriptionRevisionMax {
		return Subscription{}, ErrSchema
	}
	if err := effectiveSubscription(&item, asOf); err != nil {
		return Subscription{}, err
	}
	return item, nil
}

const subscriptionSelect = `SELECT s.id,s.account_id,s.plan_id,s.plan_revision,s.price_micro,s.credit_micro,s.currency,s.interval,s.status,s.started_at,s.period_end_at,s.cancelled_at,s.revision,prior.predecessor_id,next.successor_id FROM financial_subscriptions s LEFT JOIN financial_subscription_renewals prior ON prior.successor_id=s.id LEFT JOIN financial_subscription_renewals next ON next.predecessor_id=s.id`
