// Independently authored for docs/employee-self-admin-adjustment-history-contract.md.
// This is a read-only projection of typed administrator ledger operations.
package financial

import (
	"context"
	"database/sql"
	"time"
)

type EmployeeAdminAdjustmentItem struct {
	OccurredAt string
	DeltaMicro int64
}

type EmployeeAdminAdjustmentPage struct {
	HasAccount   bool
	Items        []EmployeeAdminAdjustmentItem
	NextPosition *EmployeeActivityPosition
}

type employeeAdminAdjustmentReadHooks struct {
	afterAccount func()
	scan         func() error
	iteration    func() error
	closeRows    func(*sql.Rows) error
	commit       func(*sql.Tx) error
}

func (l *Ledger) ReadEmployeeAdminAdjustments(ctx context.Context, query EmployeeActivityQuery) (EmployeeAdminAdjustmentPage, error) {
	return l.readEmployeeAdminAdjustments(ctx, query, employeeAdminAdjustmentReadHooks{})
}

func (l *Ledger) readEmployeeAdminAdjustments(ctx context.Context, query EmployeeActivityQuery, hooks employeeAdminAdjustmentReadHooks) (EmployeeAdminAdjustmentPage, error) {
	if l == nil || l.db == nil || ctx == nil || !validEmployeeActivityQuery(query) {
		return EmployeeAdminAdjustmentPage{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EmployeeAdminAdjustmentPage{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeAdminAdjustmentPage{}, ErrUnavailable
	}
	accountID, found, err := employeeAdminAdjustmentAccount(ctx, tx, query.EmployeeID, query.Currency)
	if err != nil {
		return EmployeeAdminAdjustmentPage{}, ErrUnavailable
	}
	if hooks.afterAccount != nil {
		hooks.afterAccount()
	}
	page := EmployeeAdminAdjustmentPage{HasAccount: found, Items: []EmployeeAdminAdjustmentItem{}}
	if found {
		page, err = readEmployeeAdminAdjustmentEntries(ctx, tx, accountID, query, hooks)
		if err != nil {
			return EmployeeAdminAdjustmentPage{}, ErrUnavailable
		}
	}
	if ctx.Err() != nil {
		return EmployeeAdminAdjustmentPage{}, ErrUnavailable
	}
	commit := tx.Commit
	if hooks.commit != nil {
		commit = func() error { return hooks.commit(tx) }
	}
	if err := commit(); err != nil {
		return EmployeeAdminAdjustmentPage{}, ErrUnavailable
	}
	return page, nil
}

func employeeAdminAdjustmentAccount(ctx context.Context, tx *sql.Tx, employeeID, currency string) (string, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,typeof(id),owner_kind,typeof(owner_kind),owner_key,typeof(owner_key),
		employee_id,typeof(employee_id),key_id,typeof(key_id),resource_kind,typeof(resource_kind),
		resource_id,typeof(resource_id),currency,typeof(currency),created_at,typeof(created_at)
		FROM financial_accounts WHERE owner_kind='employee' AND employee_id=? AND currency=? LIMIT 2`, employeeID, currency)
	if err != nil {
		return "", false, err
	}
	var accountID string
	found := false
	for rows.Next() {
		if found {
			_ = rows.Close()
			return "", false, ErrSchema
		}
		var idType, ownerKind, ownerKindType, ownerKeyValue, ownerKeyType, employee, employeeType string
		var keyID sql.NullString
		var keyType, resourceKind, resourceKindType, resourceID, resourceIDType, storedCurrency, currencyType, created, createdType string
		if err := rows.Scan(&accountID, &idType, &ownerKind, &ownerKindType, &ownerKeyValue, &ownerKeyType,
			&employee, &employeeType, &keyID, &keyType, &resourceKind, &resourceKindType,
			&resourceID, &resourceIDType, &storedCurrency, &currencyType, &created, &createdType); err != nil {
			_ = rows.Close()
			return "", false, err
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, created)
		if idType != "text" || !validText(accountID, 256) || ownerKindType != "text" || ownerKind != string(OwnerEmployee) ||
			ownerKeyType != "text" || ownerKeyValue != ownerKey(Owner{Kind: OwnerEmployee, EmployeeID: employeeID}) ||
			employeeType != "text" || employee != employeeID || keyType != "null" || keyID.Valid ||
			resourceKindType != "text" || resourceKind != "" || resourceIDType != "text" || resourceID != "" ||
			currencyType != "text" || storedCurrency != currency || createdType != "text" ||
			parseErr != nil || parsed.UTC().Format(time.RFC3339Nano) != created {
			_ = rows.Close()
			return "", false, ErrSchema
		}
		found = true
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return "", false, ErrUnavailable
	}
	return accountID, found, nil
}

func readEmployeeAdminAdjustmentEntries(ctx context.Context, tx *sql.Tx, accountID string, query EmployeeActivityQuery, hooks employeeAdminAdjustmentReadHooks) (EmployeeAdminAdjustmentPage, error) {
	// Action and typed actor define the local administrator source. Entry kind
	// cannot: a commercial refund may also use adjustment_debit.
	statement := `SELECT ` + classificationEntryFields + ` FROM financial_entries e
		JOIN financial_operations o ON o.operation_id=e.operation_id
		WHERE e.account_id=? AND o.action='adjustment' AND o.actor_kind='admin'
		AND e.created_at>=? AND e.created_at<?`
	args := []any{accountID, query.WindowStart.Format("2006-01-02T15:04:05"), query.WindowEnd.Format("2006-01-02T15:04:05")}
	if query.BeforeTime != "" {
		statement += ` AND (e.created_at<? OR (e.created_at=? AND e.id<?))`
		args = append(args, query.BeforeTime, query.BeforeTime, query.BeforeID)
	}
	statement += ` ORDER BY e.created_at DESC,e.id DESC LIMIT ?`
	args = append(args, query.Limit+1)
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return EmployeeAdminAdjustmentPage{}, ErrUnavailable
	}
	entries := make([]classificationEntry, 0, query.Limit+1)
	for rows.Next() {
		entry, scanErr := scanClassificationEntry(rows)
		if scanErr == nil && hooks.scan != nil {
			scanErr = hooks.scan()
		}
		if scanErr != nil {
			_ = rows.Close()
			return EmployeeAdminAdjustmentPage{}, ErrUnavailable
		}
		entries = append(entries, entry)
	}
	iterationErr := rows.Err()
	if iterationErr == nil && hooks.iteration != nil {
		iterationErr = hooks.iteration()
	}
	closeRows := rows.Close
	if hooks.closeRows != nil {
		closeRows = func() error { return hooks.closeRows(rows) }
	}
	closeErr := closeRows()
	if iterationErr != nil || closeErr != nil || ctx.Err() != nil {
		return EmployeeAdminAdjustmentPage{}, ErrUnavailable
	}
	for _, entry := range entries {
		at, parseErr := time.Parse(time.RFC3339Nano, entry.created)
		if !validClassificationEntry(entry) || entry.accountID != accountID || entry.original.Valid ||
			entry.resourceKind != "adjustment" || entry.resourceID != entry.operationID ||
			(entry.kind != EntryAdjustmentCredit && entry.kind != EntryAdjustmentDebit) ||
			parseErr != nil || at.Before(query.WindowStart) || !at.Before(query.WindowEnd) {
			return EmployeeAdminAdjustmentPage{}, ErrSchema
		}
		if err := verifyEmployeeAdminAdjustment(ctx, tx, entry, query.EmployeeID, query.Currency); err != nil {
			return EmployeeAdminAdjustmentPage{}, err
		}
	}
	page := EmployeeAdminAdjustmentPage{HasAccount: true, Items: make([]EmployeeAdminAdjustmentItem, 0, query.Limit)}
	for i, entry := range entries {
		if i == query.Limit {
			last := entries[query.Limit-1]
			page.NextPosition = &EmployeeActivityPosition{Time: last.created, ID: last.id}
			break
		}
		page.Items = append(page.Items, EmployeeAdminAdjustmentItem{OccurredAt: entry.created, DeltaMicro: entry.amount})
	}
	return page, nil
}

func verifyEmployeeAdminAdjustment(ctx context.Context, tx *sql.Tx, entry classificationEntry, employeeID, currency string) error {
	action, err := validateClassificationOperation(ctx, tx, entry)
	if err != nil || action != "adjustment" {
		return ErrSchema
	}
	var count int64
	var onlyID string
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),MIN(id) FROM financial_entries WHERE operation_id=?`, entry.operationID).Scan(&count, &onlyID); err != nil || count != 1 || onlyID != entry.id {
		return ErrSchema
	}
	var adminID string
	var digest []byte
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT actor_admin_id,payload_digest,digest_version FROM financial_operations WHERE operation_id=?`, entry.operationID).
		Scan(&adminID, &digest, &version); err != nil || !validText(adminID, 256) || len(digest) != 32 {
		return ErrSchema
	}
	input := Post{OperationID: entry.operationID, Action: "adjustment", ResourceKind: "adjustment", ResourceID: entry.operationID,
		RequireNonNegative: true, Entries: []EntryInput{{Owner: Owner{Kind: OwnerEmployee, EmployeeID: employeeID},
			Currency: currency, Kind: entry.kind, AmountMicro: entry.amount, ResourceKind: "adjustment", ResourceID: entry.operationID}}}
	var wanted [32]byte
	switch version {
	case 1:
		input.ActorAdminID = adminID
		wanted, err = postDigest(input)
	case 2:
		wanted, err = postDigestV2(input, Actor{Kind: ActorAdmin, ID: adminID})
	default:
		return ErrSchema
	}
	if err != nil || !equalBytes(digest, wanted[:]) {
		return ErrSchema
	}
	return nil
}
