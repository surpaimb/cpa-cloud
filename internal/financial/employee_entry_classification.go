// Independently authored for docs/employee-self-wallet-entry-classification-contract.md.
package financial

import (
	"context"
	"database/sql"
	"math"
	"time"
)

const employeeClassificationReversalLimit = 100000

type EmployeeClassificationItem struct {
	OccurredAt string
	DeltaMicro int64
	EntryKind  EntryKind
}

type EmployeeClassificationPage struct {
	HasAccount   bool
	Items        []EmployeeClassificationItem
	NextPosition *EmployeeActivityPosition
}

type employeeClassificationReadHooks struct {
	afterAccount func()
	commit       func(*sql.Tx) error
}

// The query is an already authenticated direct employee-wallet projection.
// It deliberately does not change ReadEmployeeActivity or its response shape.
func (l *Ledger) ReadEmployeeClassifications(ctx context.Context, query EmployeeActivityQuery) (EmployeeClassificationPage, error) {
	return l.readEmployeeClassifications(ctx, query, employeeClassificationReadHooks{})
}

func (l *Ledger) readEmployeeClassifications(ctx context.Context, query EmployeeActivityQuery, hooks employeeClassificationReadHooks) (EmployeeClassificationPage, error) {
	if l == nil || l.db == nil || ctx == nil || !validEmployeeActivityQuery(query) {
		return EmployeeClassificationPage{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EmployeeClassificationPage{}, ErrUnavailable
	}
	defer tx.Rollback()
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeClassificationPage{}, ErrUnavailable
	}
	accountID, found, err := employeeActivityAccount(ctx, tx, query.EmployeeID, query.Currency)
	if err != nil {
		return EmployeeClassificationPage{}, ErrUnavailable
	}
	if hooks.afterAccount != nil {
		hooks.afterAccount()
	}
	page := EmployeeClassificationPage{HasAccount: found, Items: []EmployeeClassificationItem{}}
	if found {
		page, err = employeeClassificationEntries(ctx, tx, accountID, query)
		if err != nil {
			return EmployeeClassificationPage{}, ErrUnavailable
		}
	}
	if ctx.Err() != nil {
		return EmployeeClassificationPage{}, ErrUnavailable
	}
	commit := tx.Commit
	if hooks.commit != nil {
		commit = func() error { return hooks.commit(tx) }
	}
	if err := commit(); err != nil {
		return EmployeeClassificationPage{}, ErrUnavailable
	}
	return page, nil
}

type classificationEntry struct {
	id, operationID, accountID, resourceKind, resourceID, created          string
	kind                                                                   EntryKind
	amount                                                                 int64
	original                                                               sql.NullString
	idType, operationType, accountType, kindType, amountType, originalType string
	resourceKindType, resourceIDType, createdType                          string
}

const classificationEntryFields = `e.id,typeof(e.id),e.operation_id,typeof(e.operation_id),e.account_id,typeof(e.account_id),e.kind,typeof(e.kind),e.amount_micro,typeof(e.amount_micro),e.original_entry_id,typeof(e.original_entry_id),e.resource_kind,typeof(e.resource_kind),e.resource_id,typeof(e.resource_id),e.created_at,typeof(e.created_at)`

type classificationScanner interface{ Scan(...any) error }

func scanClassificationEntry(scanner classificationScanner) (classificationEntry, error) {
	var entry classificationEntry
	err := scanner.Scan(&entry.id, &entry.idType, &entry.operationID, &entry.operationType, &entry.accountID, &entry.accountType,
		&entry.kind, &entry.kindType, &entry.amount, &entry.amountType, &entry.original, &entry.originalType,
		&entry.resourceKind, &entry.resourceKindType, &entry.resourceID, &entry.resourceIDType, &entry.created, &entry.createdType)
	return entry, err
}

func validClassificationEntry(entry classificationEntry) bool {
	parsed, err := time.Parse(time.RFC3339Nano, entry.created)
	credit := entry.kind == EntryAdjustmentCredit || entry.kind == EntryTopUp || entry.kind == EntryRedemption || entry.kind == EntrySubscriptionCredit || entry.kind == EntryRefund
	return entry.idType == "text" && validText(entry.id, 256) && entry.operationType == "text" && validText(entry.operationID, 128) &&
		entry.accountType == "text" && validText(entry.accountID, 256) && entry.kindType == "text" && validEntryKind(entry.kind) &&
		entry.amountType == "integer" && entry.amount != 0 && credit == (entry.amount > 0) &&
		(entry.originalType == "null" && !entry.original.Valid || entry.originalType == "text" && entry.original.Valid && validText(entry.original.String, 256)) &&
		entry.resourceKindType == "text" && validText(entry.resourceKind, 64) && entry.resourceIDType == "text" && validText(entry.resourceID, 256) &&
		entry.createdType == "text" && err == nil && parsed.UTC().Format(time.RFC3339Nano) == entry.created
}

func employeeClassificationEntries(ctx context.Context, tx *sql.Tx, accountID string, query EmployeeActivityQuery) (EmployeeClassificationPage, error) {
	args := []any{accountID, query.WindowStart.Format("2006-01-02T15:04:05"), query.WindowEnd.Format("2006-01-02T15:04:05")}
	statement := `SELECT ` + classificationEntryFields + ` FROM financial_entries e WHERE e.account_id=? AND e.created_at>=? AND e.created_at<?`
	if query.BeforeTime != "" {
		statement += ` AND (e.created_at<? OR (e.created_at=? AND e.id<?))`
		args = append(args, query.BeforeTime, query.BeforeTime, query.BeforeID)
	}
	statement += ` ORDER BY e.created_at DESC,e.id DESC LIMIT ?`
	args = append(args, query.Limit+1)
	rows, err := tx.QueryContext(ctx, statement, args...)
	if err != nil {
		return EmployeeClassificationPage{}, err
	}
	entries := make([]classificationEntry, 0, query.Limit+1)
	for rows.Next() {
		entry, scanErr := scanClassificationEntry(rows)
		if scanErr != nil {
			rows.Close()
			return EmployeeClassificationPage{}, scanErr
		}
		entries = append(entries, entry)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil || ctx.Err() != nil {
		return EmployeeClassificationPage{}, ErrUnavailable
	}
	reversals := make(map[string]classificationEntry)
	for _, entry := range entries {
		parsed, _ := time.Parse(time.RFC3339Nano, entry.created)
		if !validClassificationEntry(entry) || entry.accountID != accountID || parsed.Before(query.WindowStart) || !parsed.Before(query.WindowEnd) {
			return EmployeeClassificationPage{}, ErrSchema
		}
		action, err := validateClassificationOperation(ctx, tx, entry)
		if err != nil {
			return EmployeeClassificationPage{}, err
		}
		if action == "refund" {
			reversals[entry.original.String] = entry
		}
	}
	remaining := employeeClassificationReversalLimit
	for originalID, selected := range reversals {
		n, err := validateClassificationReversals(ctx, tx, originalID, selected, remaining)
		if err != nil {
			return EmployeeClassificationPage{}, err
		}
		remaining -= n
	}
	page := EmployeeClassificationPage{HasAccount: true, Items: make([]EmployeeClassificationItem, 0, query.Limit)}
	for i, entry := range entries {
		if i == query.Limit {
			last := entries[query.Limit-1]
			page.NextPosition = &EmployeeActivityPosition{Time: last.created, ID: last.id}
			break
		}
		page.Items = append(page.Items, EmployeeClassificationItem{OccurredAt: entry.created, DeltaMicro: entry.amount, EntryKind: entry.kind})
	}
	return page, nil
}

func validateClassificationOperation(ctx context.Context, tx *sql.Tx, entry classificationEntry) (string, error) {
	var action, actionType, actorKind, actorKindType, adminType, employeeType, systemType string
	var resourceKind, resourceKindType, resourceID, resourceIDType, created, createdType string
	var digestType, versionType string
	var adminID, employeeID, systemID sql.NullString
	var digest []byte
	var version int64
	err := tx.QueryRowContext(ctx, `SELECT action,typeof(action),actor_kind,typeof(actor_kind),actor_admin_id,typeof(actor_admin_id),actor_employee_id,typeof(actor_employee_id),actor_system_id,typeof(actor_system_id),resource_kind,typeof(resource_kind),resource_id,typeof(resource_id),payload_digest,typeof(payload_digest),digest_version,typeof(digest_version),created_at,typeof(created_at) FROM financial_operations WHERE operation_id=?`, entry.operationID).
		Scan(&action, &actionType, &actorKind, &actorKindType, &adminID, &adminType, &employeeID, &employeeType, &systemID, &systemType,
			&resourceKind, &resourceKindType, &resourceID, &resourceIDType, &digest, &digestType, &version, &versionType, &created, &createdType)
	if err != nil {
		return "", ErrSchema
	}
	parsed, parseErr := time.Parse(time.RFC3339Nano, created)
	if actionType != "text" || !validAction(action) || actorKindType != "text" || digestType != "blob" || len(digest) != 32 ||
		versionType != "integer" || version != 1 && version != 2 ||
		resourceKindType != "text" || !validText(resourceKind, 64) || resourceIDType != "text" || !validText(resourceID, 256) ||
		createdType != "text" || parseErr != nil || parsed.UTC().Format(time.RFC3339Nano) != created ||
		created != entry.created || resourceKind != entry.resourceKind || resourceID != entry.resourceID ||
		!validClassificationActor(action, actorKind, adminID, adminType, employeeID, employeeType, systemID, systemType, version) {
		return "", ErrSchema
	}
	wantsOriginal := entry.kind == EntryRefund || action == "refund" && entry.kind == EntryAdjustmentDebit
	if wantsOriginal != entry.original.Valid || !validPostEntry(action, EntryInput{Kind: entry.kind, AmountMicro: entry.amount, OriginalEntryID: entry.original.String}) {
		return "", ErrSchema
	}
	return action, nil
}

func validClassificationActor(action, kind string, admin sql.NullString, adminType string, employee sql.NullString, employeeType string, system sql.NullString, systemType string, version int64) bool {
	switch kind {
	case "admin":
		return admin.Valid && adminType == "text" && validText(admin.String, 256) && employeeType == "null" && !employee.Valid && systemType == "null" && !system.Valid
	case "employee":
		return adminType == "null" && !admin.Valid && employee.Valid && employeeType == "text" && validText(employee.String, 256) && systemType == "null" && !system.Valid
	case "system":
		return adminType == "null" && !admin.Valid && employeeType == "null" && !employee.Valid && system.Valid && systemType == "text" &&
			(action == "payment_callback" && system.String == "payment_callback" || action == "subscription_purchase" && system.String == "subscription_one_shot_worker")
	case "legacy_unknown":
		return version == 1 && adminType == "null" && !admin.Valid && employeeType == "null" && !employee.Valid && systemType == "null" && !system.Valid
	default:
		return false
	}
}

func validateClassificationReversals(ctx context.Context, tx *sql.Tx, originalID string, selected classificationEntry, remaining int) (int, error) {
	if remaining < 1 || !validText(originalID, 256) {
		return 0, ErrSchema
	}
	original, err := scanClassificationEntry(tx.QueryRowContext(ctx, `SELECT `+classificationEntryFields+` FROM financial_entries e WHERE e.id=?`, originalID))
	if err != nil || !validClassificationEntry(original) || original.id != originalID || original.accountID != selected.accountID {
		return 0, ErrSchema
	}
	if _, err := validateClassificationOperation(ctx, tx, original); err != nil {
		return 0, err
	}
	var wantedKind EntryKind
	if selected.kind == EntryRefund {
		if original.amount >= 0 || original.kind != EntryAdjustmentDebit && original.kind != EntrySubscriptionCharge && original.kind != EntryUsageCharge {
			return 0, ErrSchema
		}
		wantedKind = EntryRefund
	} else {
		if selected.kind != EntryAdjustmentDebit || original.amount <= 0 || original.kind != EntryTopUp {
			return 0, ErrSchema
		}
		wantedKind = EntryAdjustmentDebit
	}
	maximum, ok := classificationMagnitude(original.amount)
	if !ok {
		return 0, ErrSchema
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+classificationEntryFields+` FROM financial_entries e WHERE e.original_entry_id=? ORDER BY e.id LIMIT ?`, originalID, remaining+1)
	if err != nil {
		return 0, ErrUnavailable
	}
	siblings := make([]classificationEntry, 0)
	for rows.Next() {
		sibling, scanErr := scanClassificationEntry(rows)
		if scanErr != nil {
			rows.Close()
			return 0, ErrUnavailable
		}
		siblings = append(siblings, sibling)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil || ctx.Err() != nil || len(siblings) > remaining {
		return 0, ErrUnavailable
	}
	var total int64
	seenSelected := false
	for _, sibling := range siblings {
		if !validClassificationEntry(sibling) || sibling.original.String != originalID || sibling.accountID != original.accountID || sibling.kind != wantedKind {
			return 0, ErrSchema
		}
		if sibling.id == selected.id {
			seenSelected = true
		}
		if action, err := validateClassificationOperation(ctx, tx, sibling); err != nil || action != "refund" {
			return 0, ErrSchema
		}
		magnitude, ok := classificationMagnitude(sibling.amount)
		if !ok || magnitude > maximum-total {
			return 0, ErrSchema
		}
		total += magnitude
	}
	if !seenSelected {
		return 0, ErrSchema
	}
	return len(siblings), nil
}

func classificationMagnitude(amount int64) (int64, bool) {
	if amount == math.MinInt64 {
		return 0, false
	}
	if amount < 0 {
		return -amount, true
	}
	return amount, true
}
