// Independently authored for docs/employee-self-topup-credit-history-contract.md.
// This read-only projection proves one local callback credit, not external settlement.
package financial

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type EmployeeTopupCreditItem struct {
	CreditedAt  string
	AmountMicro int64
}

type EmployeeTopupCreditPage struct {
	HasAccount   bool
	Items        []EmployeeTopupCreditItem
	NextPosition *EmployeeActivityPosition
}

type employeeTopupCreditReadHooks struct {
	afterAccount func()
	scan         func() error
	iteration    func() error
	closeRows    func(*sql.Rows) error
	commit       func(*sql.Tx) error
}

func (l *Ledger) ReadEmployeeTopupCredits(ctx context.Context, query EmployeeActivityQuery) (EmployeeTopupCreditPage, error) {
	return l.readEmployeeTopupCredits(ctx, query, employeeTopupCreditReadHooks{})
}

func (l *Ledger) readEmployeeTopupCredits(ctx context.Context, query EmployeeActivityQuery, hooks employeeTopupCreditReadHooks) (EmployeeTopupCreditPage, error) {
	if l == nil || l.db == nil || ctx == nil || !validEmployeeActivityQuery(query) {
		return EmployeeTopupCreditPage{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EmployeeTopupCreditPage{}, ErrUnavailable
	}
	defer tx.Rollback()
	if validateSchema(ctx, tx) != nil || validateCommercialSchema(ctx, tx, commercialOperationsDDL, false) != nil {
		return EmployeeTopupCreditPage{}, ErrUnavailable
	}
	accountID, found, err := employeeAdminAdjustmentAccount(ctx, tx, query.EmployeeID, query.Currency)
	if err != nil {
		return EmployeeTopupCreditPage{}, ErrUnavailable
	}
	if hooks.afterAccount != nil {
		hooks.afterAccount()
	}
	page := EmployeeTopupCreditPage{HasAccount: found, Items: []EmployeeTopupCreditItem{}}
	if found {
		if err := checkEmployeeTopupCreditOrphans(ctx, tx, accountID, query); err != nil {
			return EmployeeTopupCreditPage{}, ErrUnavailable
		}
		page, err = readEmployeeTopupCreditEntries(ctx, tx, accountID, query, hooks)
		if err != nil {
			return EmployeeTopupCreditPage{}, ErrUnavailable
		}
	}
	if ctx.Err() != nil {
		return EmployeeTopupCreditPage{}, ErrUnavailable
	}
	commit := tx.Commit
	if hooks.commit != nil {
		commit = func() error { return hooks.commit(tx) }
	}
	if err := commit(); err != nil {
		return EmployeeTopupCreditPage{}, ErrUnavailable
	}
	return page, nil
}

func checkEmployeeTopupCreditOrphans(ctx context.Context, tx *sql.Tx, accountID string, query EmployeeActivityQuery) error {
	var orphan int
	err := tx.QueryRowContext(ctx, `SELECT 1 FROM financial_entries e WHERE e.account_id=? AND e.created_at>=? AND e.created_at<?
		AND NOT EXISTS (SELECT 1 FROM financial_operations o WHERE o.operation_id=e.operation_id) LIMIT 1`,
		accountID, query.WindowStart.Format("2006-01-02T15:04:05"), query.WindowEnd.Format("2006-01-02T15:04:05")).Scan(&orphan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return ErrUnavailable
	}
	return ErrSchema
}

func readEmployeeTopupCreditEntries(ctx context.Context, tx *sql.Tx, accountID string, query EmployeeActivityQuery, hooks employeeTopupCreditReadHooks) (EmployeeTopupCreditPage, error) {
	// Typed callback origin is the only candidate filter. A malformed kind,
	// amount, version, or resource must reach validation and fail the whole page.
	statement := `SELECT ` + classificationEntryFields + ` FROM financial_entries e
		JOIN financial_operations o ON o.operation_id=e.operation_id
		WHERE e.account_id=? AND o.action='payment_callback' AND o.actor_kind='system' AND o.actor_system_id='payment_callback'
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
		return EmployeeTopupCreditPage{}, ErrUnavailable
	}
	entries := make([]classificationEntry, 0, query.Limit+1)
	for rows.Next() {
		entry, scanErr := scanClassificationEntry(rows)
		if scanErr == nil && hooks.scan != nil {
			scanErr = hooks.scan()
		}
		if scanErr != nil {
			_ = rows.Close()
			return EmployeeTopupCreditPage{}, ErrUnavailable
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
		return EmployeeTopupCreditPage{}, ErrUnavailable
	}
	for _, entry := range entries {
		at, parseErr := time.Parse(time.RFC3339Nano, entry.created)
		if !validClassificationEntry(entry) || entry.accountID != accountID || entry.kind != EntryTopUp || entry.amount <= 0 ||
			entry.original.Valid || entry.resourceKind != "topup" || parseErr != nil ||
			at.Before(query.WindowStart) || !at.Before(query.WindowEnd) {
			return EmployeeTopupCreditPage{}, ErrSchema
		}
		if err := verifyEmployeeTopupCredit(ctx, tx, entry, query.EmployeeID, query.Currency); err != nil {
			return EmployeeTopupCreditPage{}, err
		}
	}
	page := EmployeeTopupCreditPage{HasAccount: true, Items: make([]EmployeeTopupCreditItem, 0, query.Limit)}
	for i, entry := range entries {
		if i == query.Limit {
			last := entries[query.Limit-1]
			page.NextPosition = &EmployeeActivityPosition{Time: last.created, ID: last.id}
			break
		}
		page.Items = append(page.Items, EmployeeTopupCreditItem{CreditedAt: entry.created, AmountMicro: entry.amount})
	}
	return page, nil
}

func verifyEmployeeTopupCredit(ctx context.Context, tx *sql.Tx, entry classificationEntry, employeeID, currency string) error {
	action, err := validateClassificationOperation(ctx, tx, entry)
	if err != nil || action != "payment_callback" {
		return ErrSchema
	}
	var count int64
	var onlyID string
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),MIN(id) FROM financial_entries WHERE operation_id=?`, entry.operationID).Scan(&count, &onlyID); err != nil || count != 1 || onlyID != entry.id {
		return ErrSchema
	}
	var actorKind, actorKindType, systemID, systemType, adminType, employeeType, digestType, versionType string
	var digest []byte
	var version int64
	if err := tx.QueryRowContext(ctx, `SELECT actor_kind,typeof(actor_kind),actor_system_id,typeof(actor_system_id),
		typeof(actor_admin_id),typeof(actor_employee_id),payload_digest,typeof(payload_digest),digest_version,typeof(digest_version)
		FROM financial_operations WHERE operation_id=?`, entry.operationID).Scan(&actorKind, &actorKindType, &systemID, &systemType,
		&adminType, &employeeType, &digest, &digestType, &version, &versionType); err != nil || actorKindType != "text" ||
		actorKind != "system" || systemType != "text" || systemID != "payment_callback" || adminType != "null" ||
		employeeType != "null" || digestType != "blob" || len(digest) != sha256.Size || versionType != "integer" || version != 2 {
		return ErrSchema
	}
	input := Post{OperationID: entry.operationID, Action: "payment_callback", ResourceKind: "topup", ResourceID: entry.resourceID,
		Entries: []EntryInput{{Owner: Owner{Kind: OwnerEmployee, EmployeeID: employeeID}, Currency: currency,
			Kind: EntryTopUp, AmountMicro: entry.amount, ResourceKind: "topup", ResourceID: entry.resourceID}}}
	wanted, err := postDigestV2(input, Actor{Kind: ActorSystem, ID: "payment_callback"})
	if err != nil || !equalBytes(digest, wanted[:]) {
		return ErrSchema
	}
	paymentID, err := topupCreditPayment(ctx, tx, entry, currency)
	if err != nil {
		return err
	}
	return verifyTopupCreditEvent(ctx, tx, entry, paymentID)
}

func topupCreditPayment(ctx context.Context, tx *sql.Tx, entry classificationEntry, requestedCurrency string) (string, error) {
	var topupID, topupIDType, paymentID, paymentIDType, topupCreated, topupCreatedType string
	if err := tx.QueryRowContext(ctx, `SELECT id,typeof(id),payment_id,typeof(payment_id),created_at,typeof(created_at)
		FROM financial_topups WHERE id=?`, entry.resourceID).Scan(&topupID, &topupIDType, &paymentID, &paymentIDType,
		&topupCreated, &topupCreatedType); err != nil || topupIDType != "text" || topupID != entry.resourceID ||
		paymentIDType != "text" || !validCommercialText(paymentID, 256) || topupCreatedType != "text" || !canonicalTopupCreditTime(topupCreated) {
		return "", ErrSchema
	}
	var storedID, idType, connectorID, connectorType, accountID, accountType, external, externalType string
	var amountType, currency, currencyType, status, statusType, paidEntryType, created, createdType, paidAtType, refundedType, revisionType string
	var amount, refunded, revision int64
	var paidEntry, paidAt sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT id,typeof(id),connector_id,typeof(connector_id),account_id,typeof(account_id),
		external_reference,typeof(external_reference),amount_micro,typeof(amount_micro),currency,typeof(currency),
		status,typeof(status),paid_entry_id,typeof(paid_entry_id),created_at,typeof(created_at),paid_at,typeof(paid_at),
		refunded_micro,typeof(refunded_micro),revision,typeof(revision) FROM financial_payments WHERE id=?`, paymentID).
		Scan(&storedID, &idType, &connectorID, &connectorType, &accountID, &accountType, &external, &externalType,
			&amount, &amountType, &currency, &currencyType, &status, &statusType, &paidEntry, &paidEntryType,
			&created, &createdType, &paidAt, &paidAtType, &refunded, &refundedType, &revision, &revisionType); err != nil ||
		idType != "text" || storedID != paymentID || connectorType != "text" || !validCommercialText(connectorID, 256) ||
		accountType != "text" || accountID != entry.accountID || externalType != "text" || !validCommercialText(external, 256) ||
		amountType != "integer" || amount <= 0 || amount != entry.amount || currencyType != "text" || currency != requestedCurrency ||
		statusType != "text" || !validTopupCreditPaymentStatus(status, refunded, amount) ||
		paidEntryType != "text" || !paidEntry.Valid || paidEntry.String != entry.id ||
		createdType != "text" || !canonicalTopupCreditTime(created) || created != topupCreated ||
		paidAtType != "text" || !paidAt.Valid || paidAt.String != entry.created ||
		refundedType != "integer" || revisionType != "integer" || revision < 2 {
		return "", ErrSchema
	}
	var foundConnector, foundType string
	if err := tx.QueryRowContext(ctx, `SELECT id,typeof(id) FROM financial_payment_connectors WHERE id=?`, connectorID).
		Scan(&foundConnector, &foundType); err != nil || foundType != "text" || foundConnector != connectorID {
		return "", ErrSchema
	}
	return paymentID, nil
}

func validTopupCreditPaymentStatus(status string, refunded, amount int64) bool {
	switch status {
	case "paid":
		return refunded == 0
	case "partially_refunded":
		return refunded > 0 && refunded < amount
	case "refunded":
		return refunded == amount
	default:
		return false
	}
}

func verifyTopupCreditEvent(ctx context.Context, tx *sql.Tx, entry classificationEntry, paymentID string) error {
	var connectorID string
	if err := tx.QueryRowContext(ctx, `SELECT connector_id FROM financial_payments WHERE id=?`, paymentID).Scan(&connectorID); err != nil || !validCommercialText(connectorID, 256) {
		return ErrSchema
	}
	prefix := "webhook:" + connectorID + ":"
	if !strings.HasPrefix(entry.operationID, prefix) {
		return ErrSchema
	}
	eventID := strings.TrimPrefix(entry.operationID, prefix)
	if !validCommercialText(eventID, 256) {
		return ErrSchema
	}
	var storedConnector, connectorType, storedEvent, eventType, storedPayment, paymentType, digestType string
	var signedAt, signedType, processedAt, processedType string
	var digest []byte
	if err := tx.QueryRowContext(ctx, `SELECT connector_id,typeof(connector_id),event_id,typeof(event_id),payment_id,typeof(payment_id),
		payload_digest,typeof(payload_digest),signed_at,typeof(signed_at),processed_at,typeof(processed_at)
		FROM financial_webhook_events WHERE connector_id=? AND event_id=?`, connectorID, eventID).
		Scan(&storedConnector, &connectorType, &storedEvent, &eventType, &storedPayment, &paymentType,
			&digest, &digestType, &signedAt, &signedType, &processedAt, &processedType); err != nil ||
		connectorType != "text" || storedConnector != connectorID || eventType != "text" || storedEvent != eventID ||
		paymentType != "text" || storedPayment != paymentID || digestType != "blob" || len(digest) != sha256.Size ||
		signedType != "text" || processedType != "text" || !canonicalTopupCreditTime(signedAt) ||
		!canonicalTopupCreditTime(processedAt) || processedAt != entry.created {
		return ErrSchema
	}
	signed, _ := time.Parse(time.RFC3339Nano, signedAt)
	processed, _ := time.Parse(time.RFC3339Nano, processedAt)
	if signed.Before(processed.Add(-5*time.Minute)) || signed.After(processed.Add(5*time.Minute)) {
		return ErrSchema
	}
	return nil
}

func canonicalTopupCreditTime(raw string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, raw)
	return err == nil && parsed.UTC().Format(time.RFC3339Nano) == raw
}
