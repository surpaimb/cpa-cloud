package financial

// Independently authored for docs/subscription-period-expiry-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"time"

	expirySQLite "modernc.org/sqlite"
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
	observation := expiryScopeFor(ctx)
	started := observation.enter(ExpiryBegin, 0)
	tx, err := c.db.BeginTx(ctx, nil)
	observation.returned(ExpiryBegin, 0, started, err, ExpiryCountNA)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer func() {
		started := observation.enter(ExpiryRollback, 0)
		err := tx.Rollback()
		observation.returned(ExpiryRollback, 0, started, err, ExpiryCountNA)
	}()
	cutoff := asOf.Format(subscriptionEndLayout)
	started = observation.enter(ExpiryQuery, 0)
	rows, err := tx.QueryContext(ctx, `SELECT id,period_end_at FROM financial_subscriptions WHERE interval='monthly' AND status='active' AND period_end_at<=? ORDER BY period_end_at,id LIMIT 100`, cutoff)
	observation.returned(ExpiryQuery, 0, started, err, ExpiryCountNA)
	if err != nil {
		return 0, ErrUnavailable
	}
	type due struct{ id, end string }
	items := make([]due, 0, 100)
	closeRows := func() error {
		started := observation.enter(ExpiryRowsClose, 0)
		err := rows.Close()
		observation.returned(ExpiryRowsClose, 0, started, err, ExpiryCountNA)
		return err
	}
	for rows.Next() {
		var item due
		ordinal := uint8(len(items) + 1)
		started := observation.enter(ExpiryScan, ordinal)
		scanErr := rows.Scan(&item.id, &item.end)
		observation.returned(ExpiryScan, ordinal, started, scanErr, ExpiryCountNA)
		if scanErr != nil {
			closeRows()
			return 0, ErrSchema
		}
		end, err := parseSubscriptionEnd(item.end)
		if err != nil || end.After(asOf) {
			observation.point(ExpiryValidate, ordinal, ErrSchema)
			closeRows()
			return 0, ErrSchema
		}
		items = append(items, item)
	}
	started = observation.enter(ExpiryRowsErr, 0)
	iterationErr := rows.Err()
	observation.returned(ExpiryRowsErr, 0, started, iterationErr, ExpiryCountNA)
	closeErr := closeRows()
	if iterationErr != nil || closeErr != nil {
		return 0, ErrUnavailable
	}
	for index, item := range items {
		ordinal := uint8(index + 1)
		started := observation.enter(ExpiryExec, ordinal)
		result, err := tx.ExecContext(ctx, `UPDATE financial_subscriptions SET status='expired',revision=revision+1 WHERE id=? AND interval='monthly' AND status='active' AND period_end_at=? AND revision<?`, item.id, item.end, subscriptionRevisionMax)
		observation.returned(ExpiryExec, ordinal, started, err, ExpiryCountNA)
		if err != nil {
			return 0, ErrUnavailable
		}
		started = observation.enter(ExpiryRowsAffected, ordinal)
		changed, err := result.RowsAffected()
		count := ExpiryCountNA
		if err == nil {
			count = expiryCount(changed)
		}
		observation.returned(ExpiryRowsAffected, ordinal, started, err, count)
		if err != nil || changed != 1 {
			return 0, ErrSchema
		}
	}
	started = observation.enter(ExpiryCommit, 0)
	commitErr := tx.Commit()
	observation.returned(ExpiryCommit, 0, started, commitErr, ExpiryCountNA)
	if commitErr != nil {
		return 0, ErrUnavailable
	}
	return len(items), nil
}

// Independently authored for the 2026-10-21 expiry SQL lifecycle contract.
// This is a restricted new internal Go API, not a public product capability.
// Only finite scalars cross the package boundary; no raw error/SQL/handle/value.
type ExpiryPhase uint8
type ExpiryEdge uint8
type ExpiryError uint8
type ExpirySQLite uint8
type ExpiryContext uint8
type ExpiryElapsed uint8
type ExpiryCount uint8

const (
	ExpiryBind ExpiryPhase = iota + 1
	ExpiryStart
	ExpiryPass
	ExpiryStop
	ExpiryCancel
	ExpiryWait
	ExpiryDoneReady
	ExpiryBegin
	ExpiryQuery
	ExpiryScan
	ExpiryValidate
	ExpiryRowsErr
	ExpiryRowsClose
	ExpiryExec
	ExpiryRowsAffected
	ExpiryCommit
	ExpiryRollback
)
const (
	ExpiryPoint ExpiryEdge = iota + 1
	ExpiryEnter
	ExpiryReturn
)
const (
	ExpiryErrorNone ExpiryError = iota + 1
	ExpiryErrorCancelled
	ExpiryErrorDeadline
	ExpiryErrorTxDone
	ExpiryErrorConnDone
	ExpiryErrorSQLite
	ExpiryErrorOther
)
const (
	ExpirySQLiteNone ExpirySQLite = iota
	ExpirySQLiteUnknown
	ExpirySQLite1
	ExpirySQLite4
	ExpirySQLite5
	ExpirySQLite6
	ExpirySQLite9
	ExpirySQLite10
	ExpirySQLite19
	ExpirySQLite261
	ExpirySQLite517
	ExpirySQLite773
	ExpirySQLite262
	ExpirySQLite518
	ExpirySQLite4106
)
const (
	ExpiryContextNone ExpiryContext = iota + 1
	ExpiryContextCancelled
	ExpiryContextDeadline
	ExpiryContextOther
)
const (
	ExpiryElapsedNA ExpiryElapsed = iota
	ExpiryLT1MS
	Expiry1MSLT100MS
	Expiry100MSLT1S
	Expiry1SLT10S
	Expiry10SLT60S
	ExpiryGE60S
	ExpiryElapsedInvalid
)
const (
	ExpiryCountNA ExpiryCount = iota
	ExpiryCountZero
	ExpiryCountOne
	ExpiryCountMany
	ExpiryCountInvalid
)

type ExpiryObservation struct {
	Pass    uint16
	Item    uint8
	Phase   ExpiryPhase
	Edge    ExpiryEdge
	Error   ExpiryError
	SQLite  ExpirySQLite
	Context ExpiryContext
	Elapsed ExpiryElapsed
	Count   ExpiryCount
}

type ExpiryObserver func(ExpiryObservation)

// Valid checks finite vocabulary/shape only, not business success or causal
// ownership. A registered OTHER is not a claim about its underlying cause.
func (o ExpiryObservation) Valid() bool {
	if o.Phase < ExpiryBind || o.Phase > ExpiryRollback || o.Edge < ExpiryPoint || o.Edge > ExpiryReturn || o.Error < ExpiryErrorNone || o.Error > ExpiryErrorOther || o.Context < ExpiryContextNone || o.Context > ExpiryContextOther || o.Count > ExpiryCountMany {
		return false
	}
	point := o.Phase == ExpiryBind || o.Phase == ExpiryStart || o.Phase == ExpiryDoneReady || o.Phase == ExpiryValidate
	if point != (o.Edge == ExpiryPoint) || (o.Edge == ExpiryReturn && (o.Elapsed < ExpiryLT1MS || o.Elapsed > ExpiryGE60S)) || (o.Edge != ExpiryReturn && o.Elapsed != ExpiryElapsedNA) {
		return false
	}
	if o.Error == ExpiryErrorSQLite {
		if o.SQLite < ExpirySQLite1 || o.SQLite > ExpirySQLite4106 {
			return false
		}
	} else if o.SQLite != ExpirySQLiteNone {
		return false
	}
	if o.Edge == ExpiryEnter && o.Error != ExpiryErrorNone {
		return false
	}
	if point && o.Phase != ExpiryValidate && o.Error != ExpiryErrorNone {
		return false
	}
	if o.Phase == ExpiryValidate && o.Error != ExpiryErrorOther {
		return false
	}
	zeroPass := o.Phase == ExpiryBind || o.Phase == ExpiryStart || o.Phase == ExpiryDoneReady || o.Phase == ExpiryCancel || o.Phase == ExpiryWait
	if zeroPass != (o.Pass == 0) {
		return false
	}
	item := o.Phase == ExpiryScan || o.Phase == ExpiryValidate || o.Phase == ExpiryExec || o.Phase == ExpiryRowsAffected
	if item && (o.Item < 1 || o.Item > 100) || !item && o.Item != 0 {
		return false
	}
	counted := o.Edge == ExpiryReturn && (o.Phase == ExpiryPass || o.Phase == ExpiryRowsAffected && o.Error == ExpiryErrorNone)
	return counted && o.Count >= ExpiryCountZero || !counted && o.Count == ExpiryCountNA
}

type expiryObservationKey struct{}
type expiryObservationBinding struct {
	observer ExpiryObserver
	pass     uint16
}

// WithExpiryObservation attaches only to the existing expiry pass context.
// Nil observer preserves the exact original interface; no cancel/deadline is
// introduced and the binder is not used by initialization or another worker.
func WithExpiryObservation(ctx context.Context, observer ExpiryObserver, pass uint16) context.Context {
	if observer == nil || ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, expiryObservationKey{}, expiryObservationBinding{observer: observer, pass: pass})
}

type expirySQLScope struct {
	ctx      context.Context
	observer ExpiryObserver
	pass     uint16
}

func expiryScopeFor(ctx context.Context) expirySQLScope {
	binding, _ := ctx.Value(expiryObservationKey{}).(expiryObservationBinding)
	return expirySQLScope{ctx: ctx, observer: binding.observer, pass: binding.pass}
}

func (o expirySQLScope) enter(phase ExpiryPhase, item uint8) time.Time {
	if o.observer == nil {
		return time.Time{}
	}
	started := time.Now()
	o.observer(ExpiryObservation{Pass: o.pass, Item: item, Phase: phase, Edge: ExpiryEnter, Error: ExpiryErrorNone, Context: expiryContextState(o.ctx)})
	return started
}

func (o expirySQLScope) returned(phase ExpiryPhase, item uint8, started time.Time, err error, count ExpiryCount) {
	if o.observer != nil {
		state := expiryContextState(o.ctx)
		kind, code := expiryErrorClass(err)
		o.observer(ExpiryObservation{Pass: o.pass, Item: item, Phase: phase, Edge: ExpiryReturn, Error: kind, SQLite: code, Context: state, Elapsed: expiryElapsedBucket(time.Since(started)), Count: count})
	}
}

func (o expirySQLScope) point(phase ExpiryPhase, item uint8, err error) {
	if o.observer != nil {
		state := expiryContextState(o.ctx)
		kind, code := expiryErrorClass(err)
		o.observer(ExpiryObservation{Pass: o.pass, Item: item, Phase: phase, Edge: ExpiryPoint, Error: kind, SQLite: code, Context: state})
	}
}

func expiryErrorClass(err error) (ExpiryError, ExpirySQLite) {
	switch {
	case err == nil:
		return ExpiryErrorNone, ExpirySQLiteNone
	case errors.Is(err, context.Canceled):
		return ExpiryErrorCancelled, ExpirySQLiteNone
	case errors.Is(err, context.DeadlineExceeded):
		return ExpiryErrorDeadline, ExpirySQLiteNone
	case errors.Is(err, sql.ErrTxDone):
		return ExpiryErrorTxDone, ExpirySQLiteNone
	case errors.Is(err, sql.ErrConnDone):
		return ExpiryErrorConnDone, ExpirySQLiteNone
	}
	if value, ok := err.(*expirySQLite.Error); ok && value != nil {
		return ExpiryErrorSQLite, expirySQLiteClass(value.Code())
	}
	return ExpiryErrorOther, ExpirySQLiteNone
}

func expirySQLiteClass(code int) ExpirySQLite {
	switch code {
	case 1:
		return ExpirySQLite1
	case 4:
		return ExpirySQLite4
	case 5:
		return ExpirySQLite5
	case 6:
		return ExpirySQLite6
	case 9:
		return ExpirySQLite9
	case 10:
		return ExpirySQLite10
	case 19:
		return ExpirySQLite19
	case 261:
		return ExpirySQLite261
	case 517:
		return ExpirySQLite517
	case 773:
		return ExpirySQLite773
	case 262:
		return ExpirySQLite262
	case 518:
		return ExpirySQLite518
	case 4106:
		return ExpirySQLite4106
	default:
		return ExpirySQLiteUnknown
	}
}

func expiryContextState(ctx context.Context) ExpiryContext {
	switch err := ctx.Err(); {
	case err == nil:
		return ExpiryContextNone
	case errors.Is(err, context.Canceled):
		return ExpiryContextCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return ExpiryContextDeadline
	default:
		return ExpiryContextOther
	}
}

func expiryElapsedBucket(d time.Duration) ExpiryElapsed {
	switch {
	case d < 0:
		return ExpiryElapsedInvalid
	case d < time.Millisecond:
		return ExpiryLT1MS
	case d < 100*time.Millisecond:
		return Expiry1MSLT100MS
	case d < time.Second:
		return Expiry100MSLT1S
	case d < 10*time.Second:
		return Expiry1SLT10S
	case d < time.Minute:
		return Expiry10SLT60S
	default:
		return ExpiryGE60S
	}
}

func expiryCount(n int64) ExpiryCount {
	switch {
	case n < 0:
		return ExpiryCountInvalid
	case n == 0:
		return ExpiryCountZero
	case n == 1:
		return ExpiryCountOne
	default:
		return ExpiryCountMany
	}
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
