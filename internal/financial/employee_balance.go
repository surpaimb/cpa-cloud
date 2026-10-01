// Independently authored for docs/employee-self-wallet-balance-contract.md.
package financial

import (
	"context"
	"database/sql"
	"time"
)

const employeeBalanceEntryLimit = 100000

// EmployeeBalance contains only the employee-owned wallet projection. A
// missing account is distinct from an existing account whose entries sum to 0.
type EmployeeBalance struct {
	HasAccount  bool
	AmountMicro int64
}

type employeeBalanceReadHooks struct {
	afterAccount func()
	commit       func(*sql.Tx) error
}

// ReadEmployeeBalance never creates an account or folds Key/resource-owned
// subaccounts into the employee-owned wallet.
func (l *Ledger) ReadEmployeeBalance(ctx context.Context, employeeID, currency string) (EmployeeBalance, error) {
	return l.readEmployeeBalance(ctx, employeeID, currency, employeeBalanceReadHooks{})
}

func (l *Ledger) readEmployeeBalance(ctx context.Context, employeeID, currency string, hooks employeeBalanceReadHooks) (EmployeeBalance, error) {
	if l == nil || l.db == nil || ctx == nil || !validText(employeeID, 256) || !validCurrency(currency) {
		return EmployeeBalance{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EmployeeBalance{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeBalance{}, ErrUnavailable
	}

	// The owner-key equality is checked after selecting by the explicit owner
	// columns: a malformed matching account must not masquerade as "missing".
	rows, err := tx.QueryContext(ctx, `SELECT id,typeof(id),owner_key,typeof(owner_key),key_id,resource_kind,typeof(resource_kind),resource_id,typeof(resource_id),created_at,typeof(created_at)
		FROM financial_accounts WHERE owner_kind='employee' AND employee_id=? AND currency=? LIMIT 2`, employeeID, currency)
	if err != nil {
		return EmployeeBalance{}, ErrUnavailable
	}
	var accountID string
	var found bool
	for rows.Next() {
		if found {
			rows.Close()
			return EmployeeBalance{}, ErrUnavailable
		}
		var idType, ownerKeyValue, ownerKeyType, resourceKind, resourceKindType, resourceID, resourceIDType, created, createdType string
		var keyID sql.NullString
		if err := rows.Scan(&accountID, &idType, &ownerKeyValue, &ownerKeyType, &keyID, &resourceKind, &resourceKindType, &resourceID, &resourceIDType, &created, &createdType); err != nil {
			rows.Close()
			return EmployeeBalance{}, ErrUnavailable
		}
		parsed, parseErr := time.Parse(time.RFC3339Nano, created)
		if idType != "text" || !validText(accountID, 256) || ownerKeyType != "text" ||
			ownerKeyValue != ownerKey(Owner{Kind: OwnerEmployee, EmployeeID: employeeID}) || keyID.Valid ||
			resourceKindType != "text" || resourceKind != "" || resourceIDType != "text" || resourceID != "" ||
			createdType != "text" || parseErr != nil || parsed.UTC().Format(time.RFC3339Nano) != created {
			rows.Close()
			return EmployeeBalance{}, ErrUnavailable
		}
		found = true
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return EmployeeBalance{}, ErrUnavailable
	}
	if hooks.afterAccount != nil {
		hooks.afterAccount()
	}

	result := EmployeeBalance{HasAccount: found}
	if found {
		result.AmountMicro, err = employeeAccountSum(ctx, tx, accountID)
		if err != nil {
			return EmployeeBalance{}, ErrUnavailable
		}
	}
	if ctx.Err() != nil {
		return EmployeeBalance{}, ErrUnavailable
	}
	commit := tx.Commit
	if hooks.commit != nil {
		commit = func() error { return hooks.commit(tx) }
	}
	if err := commit(); err != nil {
		return EmployeeBalance{}, ErrUnavailable
	}
	return result, nil
}

func employeeAccountSum(ctx context.Context, tx *sql.Tx, accountID string) (int64, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id,typeof(id),operation_id,typeof(operation_id),kind,typeof(kind),amount_micro,typeof(amount_micro),created_at,typeof(created_at)
		FROM financial_entries WHERE account_id=? ORDER BY created_at,id LIMIT ?`, accountID, employeeBalanceEntryLimit+1)
	if err != nil {
		return 0, err
	}
	var total int64
	count := 0
	for rows.Next() {
		count++
		if count > employeeBalanceEntryLimit {
			rows.Close()
			return 0, ErrUnavailable
		}
		var id, idType, operationID, operationType, kindType, amountType, created, createdType string
		var kind EntryKind
		var amount int64
		if err := rows.Scan(&id, &idType, &operationID, &operationType, &kind, &kindType, &amount, &amountType, &created, &createdType); err != nil {
			rows.Close()
			return 0, err
		}
		credit := kind == EntryAdjustmentCredit || kind == EntryTopUp || kind == EntryRedemption || kind == EntrySubscriptionCredit || kind == EntryRefund
		parsed, parseErr := time.Parse(time.RFC3339Nano, created)
		if idType != "text" || !validText(id, 256) || operationType != "text" || !validText(operationID, 256) ||
			kindType != "text" || !validEntryKind(kind) || amountType != "integer" || amount == 0 || credit != (amount > 0) ||
			createdType != "text" || parseErr != nil || parsed.UTC().Format(time.RFC3339Nano) != created {
			rows.Close()
			return 0, ErrUnavailable
		}
		var ok bool
		total, ok = checkedAdd(total, amount)
		if !ok {
			rows.Close()
			return 0, ErrUnavailable
		}
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil || ctx.Err() != nil {
		return 0, ErrUnavailable
	}
	return total, nil
}
