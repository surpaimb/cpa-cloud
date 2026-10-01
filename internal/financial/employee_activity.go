// Independently authored for docs/employee-self-wallet-activity-contract.md.
package financial

import (
	"context"
	"database/sql"
	"time"
)

const employeeActivityWindow = 31 * 24 * time.Hour

// EmployeeActivityQuery is an already authenticated employee-only projection.
// BeforeTime/BeforeID are an authenticated position, not caller selectors.
type EmployeeActivityQuery struct {
	EmployeeID  string
	Currency    string
	WindowStart time.Time
	WindowEnd   time.Time
	Limit       int
	BeforeTime  string
	BeforeID    string
}

type EmployeeActivityItem struct {
	OccurredAt string
	DeltaMicro int64
}

type EmployeeActivityPosition struct {
	Time string
	ID   string
}

type EmployeeActivityPage struct {
	HasAccount   bool
	Items        []EmployeeActivityItem
	NextPosition *EmployeeActivityPosition
}

type employeeActivityReadHooks struct {
	afterAccount func()
	commit       func(*sql.Tx) error
}

func (l *Ledger) ReadEmployeeActivity(ctx context.Context, query EmployeeActivityQuery) (EmployeeActivityPage, error) {
	return l.readEmployeeActivity(ctx, query, employeeActivityReadHooks{})
}

func (l *Ledger) readEmployeeActivity(ctx context.Context, query EmployeeActivityQuery, hooks employeeActivityReadHooks) (EmployeeActivityPage, error) {
	if l == nil || l.db == nil || ctx == nil || !validEmployeeActivityQuery(query) {
		return EmployeeActivityPage{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EmployeeActivityPage{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeActivityPage{}, ErrUnavailable
	}

	accountID, found, err := employeeActivityAccount(ctx, tx, query.EmployeeID, query.Currency)
	if err != nil {
		return EmployeeActivityPage{}, ErrUnavailable
	}
	if hooks.afterAccount != nil {
		hooks.afterAccount()
	}
	page := EmployeeActivityPage{HasAccount: found, Items: []EmployeeActivityItem{}}
	if found {
		page, err = employeeActivityEntries(ctx, tx, accountID, query)
		if err != nil {
			return EmployeeActivityPage{}, ErrUnavailable
		}
	}
	if ctx.Err() != nil {
		return EmployeeActivityPage{}, ErrUnavailable
	}
	commit := tx.Commit
	if hooks.commit != nil {
		commit = func() error { return hooks.commit(tx) }
	}
	if err := commit(); err != nil {
		return EmployeeActivityPage{}, ErrUnavailable
	}
	return page, nil
}

func validEmployeeActivityQuery(query EmployeeActivityQuery) bool {
	if !validText(query.EmployeeID, 256) || !validCurrency(query.Currency) || query.Limit < 1 || query.Limit > 50 ||
		query.WindowStart.Location() != time.UTC || query.WindowEnd.Location() != time.UTC ||
		query.WindowStart.Nanosecond() != 0 || query.WindowEnd.Nanosecond() != 0 ||
		query.WindowEnd.Sub(query.WindowStart) != employeeActivityWindow {
		return false
	}
	if query.BeforeTime == "" && query.BeforeID == "" {
		return true
	}
	if query.BeforeTime == "" || !validText(query.BeforeID, 256) {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, query.BeforeTime)
	return err == nil && parsed.UTC().Format(time.RFC3339Nano) == query.BeforeTime &&
		!parsed.Before(query.WindowStart) && parsed.Before(query.WindowEnd)
}

func employeeActivityAccount(ctx context.Context, tx *sql.Tx, employeeID, currency string) (string, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,typeof(id),owner_key,typeof(owner_key),key_id,resource_kind,typeof(resource_kind),resource_id,typeof(resource_id),created_at,typeof(created_at)
		FROM financial_accounts WHERE owner_kind='employee' AND employee_id=? AND currency=? LIMIT 2`, employeeID, currency)
	if err != nil {
		return "", false, err
	}
	var accountID string
	var found bool
	for rows.Next() {
		if found {
			rows.Close()
			return "", false, ErrUnavailable
		}
		var idType, ownerKeyValue, ownerKeyType, resourceKind, resourceKindType, resourceID, resourceIDType, created, createdType string
		var keyID sql.NullString
		if err := rows.Scan(&accountID, &idType, &ownerKeyValue, &ownerKeyType, &keyID, &resourceKind, &resourceKindType, &resourceID, &resourceIDType, &created, &createdType); err != nil {
			rows.Close()
			return "", false, err
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, created)
		if idType != "text" || !validText(accountID, 256) || ownerKeyType != "text" ||
			ownerKeyValue != ownerKey(Owner{Kind: OwnerEmployee, EmployeeID: employeeID}) || keyID.Valid ||
			resourceKindType != "text" || resourceKind != "" || resourceIDType != "text" || resourceID != "" ||
			createdType != "text" || parseErr != nil || parsed.UTC().Format(time.RFC3339Nano) != created {
			rows.Close()
			return "", false, ErrUnavailable
		}
		found = true
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return "", false, ErrUnavailable
	}
	return accountID, found, nil
}

func employeeActivityEntries(ctx context.Context, tx *sql.Tx, accountID string, query EmployeeActivityQuery) (EmployeeActivityPage, error) {
	// RFC3339Nano has variable fractional precision. Whole-second prefixes make
	// the inclusive/exclusive window correct without assuming text is a strict
	// nanosecond key. The existing index supplies stable text/id ordering.
	arguments := []any{accountID, query.WindowStart.Format("2006-01-02T15:04:05"), query.WindowEnd.Format("2006-01-02T15:04:05")}
	statement := `SELECT id,typeof(id),operation_id,typeof(operation_id),kind,typeof(kind),amount_micro,typeof(amount_micro),resource_kind,typeof(resource_kind),resource_id,typeof(resource_id),created_at,typeof(created_at)
		FROM financial_entries WHERE account_id=? AND created_at>=? AND created_at<?`
	if query.BeforeTime != "" {
		statement += ` AND (created_at<? OR (created_at=? AND id<?))`
		arguments = append(arguments, query.BeforeTime, query.BeforeTime, query.BeforeID)
	}
	statement += ` ORDER BY created_at DESC,id DESC LIMIT ?`
	arguments = append(arguments, query.Limit+1)
	rows, err := tx.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return EmployeeActivityPage{}, err
	}
	page := EmployeeActivityPage{HasAccount: true, Items: make([]EmployeeActivityItem, 0, query.Limit+1)}
	positions := make([]EmployeeActivityPosition, 0, query.Limit+1)
	for rows.Next() {
		var id, idType, operationID, operationType, kindType, amountType, resourceKind, resourceKindType, resourceID, resourceIDType, created, createdType string
		var kind EntryKind
		var amount int64
		if err := rows.Scan(&id, &idType, &operationID, &operationType, &kind, &kindType, &amount, &amountType, &resourceKind, &resourceKindType, &resourceID, &resourceIDType, &created, &createdType); err != nil {
			rows.Close()
			return EmployeeActivityPage{}, err
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, created)
		credit := kind == EntryAdjustmentCredit || kind == EntryTopUp || kind == EntryRedemption || kind == EntrySubscriptionCredit || kind == EntryRefund
		if idType != "text" || !validText(id, 256) || operationType != "text" || !validText(operationID, 256) ||
			kindType != "text" || !validEntryKind(kind) || amountType != "integer" || amount == 0 || credit != (amount > 0) ||
			resourceKindType != "text" || !validActivityResourceText(resourceKind, 64) ||
			resourceIDType != "text" || !validActivityResourceText(resourceID, 256) ||
			createdType != "text" || parseErr != nil || parsed.UTC().Format(time.RFC3339Nano) != created ||
			parsed.Before(query.WindowStart) || !parsed.Before(query.WindowEnd) {
			rows.Close()
			return EmployeeActivityPage{}, ErrUnavailable
		}
		page.Items = append(page.Items, EmployeeActivityItem{OccurredAt: created, DeltaMicro: amount})
		positions = append(positions, EmployeeActivityPosition{Time: created, ID: id})
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil || ctx.Err() != nil {
		return EmployeeActivityPage{}, ErrUnavailable
	}
	if len(page.Items) > query.Limit {
		page.Items = page.Items[:query.Limit]
		position := positions[query.Limit-1]
		page.NextPosition = &position
	}
	return page, nil
}

func validActivityResourceText(value string, limit int) bool {
	return value == "" || validText(value, limit)
}
