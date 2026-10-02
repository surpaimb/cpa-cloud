// Independently authored for docs/employee-self-redemption-credit-history-contract.md.
// This projection proves the existing self-redemption chain without posting money.
package financial

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"time"
)

type EmployeeRedemptionCreditItem struct {
	CreditedAt  string
	AmountMicro int64
}

type EmployeeRedemptionCreditPage struct {
	HasAccount   bool
	Items        []EmployeeRedemptionCreditItem
	NextPosition *EmployeeActivityPosition
}

type employeeRedemptionHistoryReadHooks struct {
	afterAccount func()
	scan         func() error
	closeRows    func(*sql.Rows) error
	commit       func(*sql.Tx) error
}

func (l *Ledger) ReadEmployeeRedemptionCredits(ctx context.Context, query EmployeeActivityQuery) (EmployeeRedemptionCreditPage, error) {
	return l.readEmployeeRedemptionCredits(ctx, query, employeeRedemptionHistoryReadHooks{})
}

func (l *Ledger) readEmployeeRedemptionCredits(ctx context.Context, query EmployeeActivityQuery, hooks employeeRedemptionHistoryReadHooks) (EmployeeRedemptionCreditPage, error) {
	if l == nil || l.db == nil || ctx == nil || !validEmployeeActivityQuery(query) {
		return EmployeeRedemptionCreditPage{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EmployeeRedemptionCreditPage{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeRedemptionCreditPage{}, ErrUnavailable
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return EmployeeRedemptionCreditPage{}, ErrUnavailable
	}
	accountID, found, err := employeeActivityAccount(ctx, tx, query.EmployeeID, query.Currency)
	if err != nil {
		return EmployeeRedemptionCreditPage{}, ErrUnavailable
	}
	if hooks.afterAccount != nil {
		hooks.afterAccount()
	}
	page := EmployeeRedemptionCreditPage{HasAccount: found, Items: []EmployeeRedemptionCreditItem{}}
	if found {
		page, err = readEmployeeRedemptionCreditEntries(ctx, tx, accountID, query, hooks)
		if err != nil {
			return EmployeeRedemptionCreditPage{}, ErrUnavailable
		}
	}
	if ctx.Err() != nil {
		return EmployeeRedemptionCreditPage{}, ErrUnavailable
	}
	commit := tx.Commit
	if hooks.commit != nil {
		commit = func() error { return hooks.commit(tx) }
	}
	if err := commit(); err != nil {
		return EmployeeRedemptionCreditPage{}, ErrUnavailable
	}
	return page, nil
}

func readEmployeeRedemptionCreditEntries(ctx context.Context, tx *sql.Tx, accountID string, query EmployeeActivityQuery, hooks employeeRedemptionHistoryReadHooks) (EmployeeRedemptionCreditPage, error) {
	// The typed actor is the inclusion boundary. Do not prefilter amount,
	// action, or digest version: a selected bad chain must fail the whole page.
	statement := `SELECT ` + classificationEntryFields + ` FROM financial_entries e
		JOIN financial_operations o ON o.operation_id=e.operation_id
		WHERE e.account_id=? AND e.kind='redemption' AND o.actor_kind='employee' AND o.actor_employee_id=?
		AND e.created_at>=? AND e.created_at<?`
	args := []any{accountID, query.EmployeeID, query.WindowStart.Format("2006-01-02T15:04:05"), query.WindowEnd.Format("2006-01-02T15:04:05")}
	if query.BeforeTime != "" {
		statement += ` AND (e.created_at<? OR (e.created_at=? AND e.id<?))`
		args = append(args, query.BeforeTime, query.BeforeTime, query.BeforeID)
	}
	statement += ` ORDER BY e.created_at DESC,e.id DESC LIMIT ?`
	args = append(args, query.Limit+1)
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return EmployeeRedemptionCreditPage{}, ErrUnavailable
	}
	entries := make([]classificationEntry, 0, query.Limit+1)
	for rows.Next() {
		entry, scanErr := scanClassificationEntry(rows)
		if scanErr == nil && hooks.scan != nil {
			scanErr = hooks.scan()
		}
		if scanErr != nil {
			rows.Close()
			return EmployeeRedemptionCreditPage{}, ErrUnavailable
		}
		entries = append(entries, entry)
	}
	iterationErr := rows.Err()
	closeRows := rows.Close
	if hooks.closeRows != nil {
		closeRows = func() error { return hooks.closeRows(rows) }
	}
	closeErr := closeRows()
	if iterationErr != nil || closeErr != nil || ctx.Err() != nil {
		return EmployeeRedemptionCreditPage{}, ErrUnavailable
	}
	for _, entry := range entries {
		creditedAt, parseErr := time.Parse(time.RFC3339Nano, entry.created)
		if !validClassificationEntry(entry) || entry.kind != EntryRedemption || entry.amount <= 0 || entry.original.Valid ||
			entry.accountID != accountID || entry.resourceKind != "redemption" || parseErr != nil ||
			creditedAt.Before(query.WindowStart) || !creditedAt.Before(query.WindowEnd) {
			return EmployeeRedemptionCreditPage{}, ErrSchema
		}
		if err := verifyEmployeeRedemptionCredit(ctx, tx, entry, query.EmployeeID, query.Currency); err != nil {
			return EmployeeRedemptionCreditPage{}, err
		}
	}
	page := EmployeeRedemptionCreditPage{HasAccount: true, Items: make([]EmployeeRedemptionCreditItem, 0, query.Limit)}
	for i, entry := range entries {
		if i == query.Limit {
			last := entries[query.Limit-1]
			page.NextPosition = &EmployeeActivityPosition{Time: last.created, ID: last.id}
			break
		}
		page.Items = append(page.Items, EmployeeRedemptionCreditItem{CreditedAt: entry.created, AmountMicro: entry.amount})
	}
	return page, nil
}

func verifyEmployeeRedemptionCredit(ctx context.Context, tx *sql.Tx, entry classificationEntry, employeeID, currency string) error {
	action, err := validateClassificationOperation(ctx, tx, entry)
	if err != nil || action != "redemption" {
		return ErrSchema
	}
	var redemptionID, redemptionIDType, codeID, codeIDType, accountID, accountIDType, entryID, entryIDType, redeemedAt, redeemedAtType string
	var codeDigest, codeDigestType, codeCurrency, codeCurrencyType, codeAmountType, maxUsesType, usesType string
	var codeAmount, maxUses, uses int64
	err = tx.QueryRowContext(ctx, `SELECT r.id,typeof(r.id),r.code_id,typeof(r.code_id),r.account_id,typeof(r.account_id),
		r.entry_id,typeof(r.entry_id),r.created_at,typeof(r.created_at),c.code_digest,typeof(c.code_digest),
		c.currency,typeof(c.currency),c.amount_micro,typeof(c.amount_micro),c.max_uses,typeof(c.max_uses),c.uses,typeof(c.uses)
		FROM financial_redemptions r JOIN financial_redemption_codes c ON c.id=r.code_id WHERE r.entry_id=?`, entry.id).
		Scan(&redemptionID, &redemptionIDType, &codeID, &codeIDType, &accountID, &accountIDType, &entryID, &entryIDType,
			&redeemedAt, &redeemedAtType, &codeDigest, &codeDigestType, &codeCurrency, &codeCurrencyType, &codeAmount,
			&codeAmountType, &maxUses, &maxUsesType, &uses, &usesType)
	if err != nil || redemptionIDType != "text" || !validText(redemptionID, 256) || codeIDType != "text" || !validText(codeID, 256) ||
		accountIDType != "text" || accountID != entry.accountID || entryIDType != "text" || entryID != entry.id ||
		redeemedAtType != "text" || redeemedAt != entry.created || codeDigestType != "blob" || len(codeDigest) != sha256.Size ||
		codeCurrencyType != "text" || codeCurrency != currency || !validCurrency(codeCurrency) || codeAmountType != "integer" ||
		codeAmount <= 0 || codeAmount != entry.amount || maxUsesType != "integer" || maxUses < 1 ||
		usesType != "integer" || uses < 0 || uses > maxUses || entry.resourceID != redemptionID {
		return ErrSchema
	}
	var inventory int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_redemptions WHERE code_id=?`, codeID).Scan(&inventory); err != nil || inventory != uses {
		return ErrSchema
	}
	var rawDigest [sha256.Size]byte
	copy(rawDigest[:], codeDigest)
	creditedAt, _ := time.Parse(time.RFC3339Nano, entry.created)
	input := EmployeeRedemptionInput{OperationID: entry.operationID, Actor: Actor{Kind: ActorEmployee, ID: employeeID},
		Owner: Owner{Kind: OwnerEmployee, EmployeeID: employeeID}, CodeDigest: rawDigest, ObservedAt: creditedAt}
	wantCommercial, err := employeeRedemptionDigest(input)
	if err != nil {
		return ErrSchema
	}
	meta := WriteMeta{OperationID: entry.operationID, Actor: input.Actor, PayloadDigest: wantCommercial, ObservedAt: creditedAt}
	receipt, found, err := existingCommercialOperation(ctx, tx, meta, "redemption.redeem")
	if err != nil || !found || receipt.ResourceID != redemptionID || receipt.Revision != 1 {
		return ErrSchema
	}
	var actionType, actorKindType, adminType, employeeType, systemType, digestType, resourceKindType, resourceIDType, revisionType, createdType string
	if err := tx.QueryRowContext(ctx, `SELECT typeof(action),typeof(actor_kind),typeof(actor_admin_id),typeof(actor_employee_id),
		typeof(actor_system_id),typeof(payload_digest),typeof(resource_kind),typeof(resource_id),typeof(revision),typeof(created_at)
		FROM financial_commercial_operations WHERE operation_id=?`, entry.operationID).
		Scan(&actionType, &actorKindType, &adminType, &employeeType, &systemType, &digestType, &resourceKindType, &resourceIDType, &revisionType, &createdType); err != nil ||
		actionType != "text" || actorKindType != "text" || adminType != "null" || employeeType != "text" || systemType != "null" ||
		digestType != "blob" || resourceKindType != "text" || resourceIDType != "text" || revisionType != "integer" || createdType != "text" {
		return ErrSchema
	}
	verified, err := replayEmployeeRedemption(ctx, tx, input, receipt)
	if err != nil || verified.Currency != currency || verified.AmountMicro != entry.amount ||
		verified.CreditedAt.UTC().Format(time.RFC3339Nano) != entry.created {
		return ErrSchema
	}
	return nil
}
