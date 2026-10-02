package financial

// Independently authored for docs/employee-self-subscription-purchase-snapshot-contract.md.
// The caller owns the one read-only transaction and its final commit.

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
)

type EmployeeSubscriptionPurchaseSnapshot struct {
	SubscriptionID string
	PlanID         string
	PlanRevision   int64
	Currency       string
	Interval       string
	PriceMicro     int64
	CreditMicro    int64
	StartedAt      string
	PeriodEndAt    *string
}

type purchaseSnapshotReadHooks struct {
	closeRows func(*sql.Rows) error
	afterRows func()
}

func (c *Commercial) ReadEmployeeSubscriptionPurchaseSnapshotTx(ctx context.Context, tx *sql.Tx, employeeID, subscriptionID string) (EmployeeSubscriptionPurchaseSnapshot, error) {
	return c.readEmployeeSubscriptionPurchaseSnapshotTx(ctx, tx, employeeID, subscriptionID, purchaseSnapshotReadHooks{})
}

func (c *Commercial) readEmployeeSubscriptionPurchaseSnapshotTx(ctx context.Context, tx *sql.Tx, employeeID, subscriptionID string, hooks purchaseSnapshotReadHooks) (EmployeeSubscriptionPurchaseSnapshot, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validText(employeeID, 256) || !validCommercialText(subscriptionID, 256) {
		return EmployeeSubscriptionPurchaseSnapshot{}, ErrInvalid
	}
	if ctx.Err() != nil || validateSchema(ctx, tx) != nil || validateCommercialSchema(ctx, tx, commercialOperationsDDL, false) != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, ErrUnavailable
	}
	// Establish the caller's direct ownership before validating mutable
	// subscription fields. A foreign or Key-owned row must not reveal whether
	// its purchase chain is malformed through a different response status.
	if err := snapshotOwnershipBoundary(ctx, tx, employeeID, subscriptionID); err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, err
	}
	subscription, accountID, err := snapshotSubscription(ctx, tx, subscriptionID)
	if err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, err
	}
	if err := snapshotDirectAccount(ctx, tx, accountID, employeeID, subscription.Currency, subscription.StartedAt); err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, err
	}
	var planID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM financial_plans WHERE id=?`, subscription.PlanID).Scan(&planID); err != nil || planID != subscription.PlanID {
		return EmployeeSubscriptionPurchaseSnapshot{}, ErrUnavailable
	}
	receipt, err := snapshotCreationReceipt(ctx, tx, subscription, hooks)
	if err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, err
	}
	if err := snapshotLedgerOperation(ctx, tx, subscription, receipt); err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, err
	}
	if err := snapshotPurchaseEntries(ctx, tx, subscription, accountID, receipt.OperationID, hooks); err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, err
	}
	if err := snapshotIncomingRenewal(ctx, tx, subscription, employeeID, receipt, hooks); err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, err
	}
	if hooks.afterRows != nil {
		hooks.afterRows()
	}
	if ctx.Err() != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, ErrUnavailable
	}
	return subscription, nil
}

func snapshotOwnershipBoundary(ctx context.Context, tx *sql.Tx, employeeID, subscriptionID string) error {
	values, err := snapshotScan(tx.QueryRowContext(ctx, `SELECT account_id FROM financial_subscriptions WHERE id=?`, subscriptionID), 1)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return ErrUnavailable
	}
	accountID, ok := snapshotText(values[0])
	if !ok || !validCommercialText(accountID, 256) {
		return ErrUnavailable
	}
	owner, err := snapshotScan(tx.QueryRowContext(ctx, `SELECT owner_kind,employee_id FROM financial_accounts WHERE id=?`, accountID), 2)
	if err != nil {
		return ErrUnavailable
	}
	kind, kindOK := snapshotText(owner[0])
	employee, employeeOK := snapshotText(owner[1])
	if !kindOK || !employeeOK {
		return ErrUnavailable
	}
	if kind != string(OwnerEmployee) || employee != employeeID {
		return ErrNotFound
	}
	return nil
}

func snapshotScan(scanner interface{ Scan(...any) error }, count int) ([]any, error) {
	values := make([]any, count)
	dest := make([]any, count)
	for i := range values {
		dest[i] = &values[i]
	}
	if err := scanner.Scan(dest...); err != nil {
		return nil, err
	}
	return values, nil
}

func snapshotText(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok
}

func snapshotInt(value any) (int64, bool) {
	number, ok := value.(int64)
	return number, ok
}

func snapshotNullableText(value any) (*string, bool) {
	if value == nil {
		return nil, true
	}
	text, ok := snapshotText(value)
	if !ok {
		return nil, false
	}
	return &text, true
}

func snapshotRows(ctx context.Context, tx *sql.Tx, query string, limit, fields int, hooks purchaseSnapshotReadHooks, args ...any) ([][]any, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, ErrUnavailable
	}
	items := make([][]any, 0, limit)
	for rows.Next() {
		if len(items) == limit {
			_ = rows.Close()
			return nil, ErrUnavailable
		}
		values, err := snapshotScan(rows, fields)
		if err != nil {
			_ = rows.Close()
			return nil, ErrUnavailable
		}
		items = append(items, values)
	}
	iterationErr := rows.Err()
	closeRows := rows.Close
	if hooks.closeRows != nil {
		closeRows = func() error { return hooks.closeRows(rows) }
	}
	closeErr := closeRows()
	if iterationErr != nil || closeErr != nil || ctx.Err() != nil {
		return nil, ErrUnavailable
	}
	return items, nil
}

func snapshotSubscription(ctx context.Context, tx *sql.Tx, id string) (EmployeeSubscriptionPurchaseSnapshot, string, error) {
	values, err := snapshotScan(tx.QueryRowContext(ctx, `SELECT id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,cancelled_at,revision FROM financial_subscriptions WHERE id=?`, id), 13)
	if errors.Is(err, sql.ErrNoRows) {
		return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrNotFound
	}
	if err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrUnavailable
	}
	subscriptionID, idOK := snapshotText(values[0])
	accountID, accountOK := snapshotText(values[1])
	planID, planOK := snapshotText(values[2])
	planRevision, revisionOK := snapshotInt(values[3])
	price, priceOK := snapshotInt(values[4])
	credit, creditOK := snapshotInt(values[5])
	currency, currencyOK := snapshotText(values[6])
	interval, intervalOK := snapshotText(values[7])
	status, statusOK := snapshotText(values[8])
	startedText, startedOK := snapshotText(values[9])
	endText, endOK := snapshotNullableText(values[10])
	cancelledText, cancelledOK := snapshotNullableText(values[11])
	storedRevision, storedRevisionOK := snapshotInt(values[12])
	if !idOK || subscriptionID != id || !validCommercialText(subscriptionID, 256) ||
		!accountOK || !validCommercialText(accountID, 256) || !planOK || !validCommercialText(planID, 256) ||
		!revisionOK || planRevision < 1 || planRevision > subscriptionRevisionMax ||
		!priceOK || price <= 0 || !creditOK || credit <= 0 || !currencyOK || !validCurrency(currency) ||
		!intervalOK || !statusOK || !startedOK || !endOK || !cancelledOK ||
		!storedRevisionOK || storedRevision < 1 || storedRevision > subscriptionRevisionMax {
		return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrUnavailable
	}
	started, err := parseSubscriptionStart(startedText)
	if err != nil {
		return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrUnavailable
	}
	if interval == "one_time" {
		if endText != nil || status == "expired" {
			return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrUnavailable
		}
	} else if interval == "monthly" {
		if endText == nil {
			return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrUnavailable
		}
		end, parseErr := parseSubscriptionEnd(*endText)
		frozen, freezeErr := monthlyPeriodEnd(started)
		if parseErr != nil || freezeErr != nil || !end.Equal(frozen) || !end.After(started) {
			return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrUnavailable
		}
	} else {
		return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrUnavailable
	}
	if status != "active" && status != "cancelled" && status != "expired" || (status == "cancelled") != (cancelledText != nil) {
		return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrUnavailable
	}
	if cancelledText != nil {
		cancelled, err := parseSubscriptionStart(*cancelledText)
		if err != nil || cancelled.Before(started) {
			return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrUnavailable
		}
		if endText != nil {
			end, _ := parseSubscriptionEnd(*endText)
			if !cancelled.Before(end) {
				return EmployeeSubscriptionPurchaseSnapshot{}, "", ErrUnavailable
			}
		}
	}
	return EmployeeSubscriptionPurchaseSnapshot{
		SubscriptionID: subscriptionID, PlanID: planID, PlanRevision: planRevision, Currency: currency,
		Interval: interval, PriceMicro: price, CreditMicro: credit, StartedAt: startedText, PeriodEndAt: endText,
	}, accountID, nil
}

func snapshotDirectAccount(ctx context.Context, tx *sql.Tx, accountID, employeeID, currency, startedText string) error {
	values, err := snapshotScan(tx.QueryRowContext(ctx, `SELECT id,owner_kind,owner_key,employee_id,key_id,resource_kind,resource_id,currency,created_at FROM financial_accounts WHERE id=?`, accountID), 9)
	if err != nil {
		return ErrUnavailable
	}
	kind, kindOK := snapshotText(values[1])
	employee, employeeOK := snapshotText(values[3])
	if !kindOK || !employeeOK {
		return ErrUnavailable
	}
	if kind != string(OwnerEmployee) || employee != employeeID {
		return ErrNotFound
	}
	id, idOK := snapshotText(values[0])
	key, keyOK := snapshotText(values[2])
	resourceKind, resourceKindOK := snapshotText(values[5])
	resourceID, resourceIDOK := snapshotText(values[6])
	accountCurrency, currencyOK := snapshotText(values[7])
	createdText, createdOK := snapshotText(values[8])
	created, createdErr := parseSubscriptionStart(createdText)
	started, startedErr := parseSubscriptionStart(startedText)
	if !idOK || id != accountID || !validCommercialText(id, 256) || !keyOK ||
		key != ownerKey(Owner{Kind: OwnerEmployee, EmployeeID: employeeID}) || values[4] != nil ||
		!resourceKindOK || resourceKind != "" || !resourceIDOK || resourceID != "" ||
		!currencyOK || accountCurrency != currency || !createdOK || createdErr != nil || startedErr != nil || created.After(started) {
		return ErrUnavailable
	}
	return nil
}

type snapshotReceipt struct {
	OperationID string
	Action      string
	Actor       snapshotActor
	Digest      [32]byte
}

type snapshotActor struct {
	Kind string
	ID   string
}

func snapshotStoredActor(kindValue, adminValue, employeeValue, systemValue any) (snapshotActor, bool) {
	kind, ok := snapshotText(kindValue)
	if !ok {
		return snapshotActor{}, false
	}
	switch kind {
	case "admin":
		id, valid := snapshotText(adminValue)
		return snapshotActor{Kind: kind, ID: id}, valid && validText(id, 256) && employeeValue == nil && systemValue == nil
	case "employee":
		id, valid := snapshotText(employeeValue)
		return snapshotActor{Kind: kind, ID: id}, valid && validText(id, 256) && adminValue == nil && systemValue == nil
	case "system":
		id, valid := snapshotText(systemValue)
		return snapshotActor{Kind: kind, ID: id}, valid && id == "subscription_one_shot_worker" && adminValue == nil && employeeValue == nil
	case "legacy_unknown":
		return snapshotActor{Kind: kind}, adminValue == nil && employeeValue == nil && systemValue == nil
	default:
		return snapshotActor{}, false
	}
}

func snapshotCreationReceipt(ctx context.Context, tx *sql.Tx, subscription EmployeeSubscriptionPurchaseSnapshot, hooks purchaseSnapshotReadHooks) (snapshotReceipt, error) {
	rows, err := snapshotRows(ctx, tx, `SELECT operation_id,action,actor_kind,actor_admin_id,actor_employee_id,actor_system_id,payload_digest,resource_kind,resource_id,revision,created_at FROM financial_commercial_operations WHERE action IN ('subscription.create','subscription.renew') AND resource_id=? LIMIT 2`, 2, 11, hooks, subscription.SubscriptionID)
	if err != nil || len(rows) != 1 {
		return snapshotReceipt{}, ErrUnavailable
	}
	values := rows[0]
	operationID, operationOK := snapshotText(values[0])
	action, actionOK := snapshotText(values[1])
	actor, actorOK := snapshotStoredActor(values[2], values[3], values[4], values[5])
	digest, digestOK := values[6].([]byte)
	kind, kindOK := snapshotText(values[7])
	resourceID, resourceOK := snapshotText(values[8])
	revision, revisionOK := snapshotInt(values[9])
	created, createdOK := snapshotText(values[10])
	if !operationOK || !validCommercialText(operationID, 128) || !actionOK || !actorOK ||
		!digestOK || len(digest) != 32 || !kindOK || kind != "subscription" ||
		!resourceOK || resourceID != subscription.SubscriptionID || !revisionOK || revision != 1 ||
		!createdOK || created != subscription.StartedAt {
		return snapshotReceipt{}, ErrUnavailable
	}
	if action != "subscription.create" && action != "subscription.renew" ||
		action == "subscription.create" && actor.Kind == "system" {
		return snapshotReceipt{}, ErrUnavailable
	}
	var digestValue [32]byte
	copy(digestValue[:], digest)
	return snapshotReceipt{OperationID: operationID, Action: action, Actor: actor, Digest: digestValue}, nil
}

func snapshotLedgerOperation(ctx context.Context, tx *sql.Tx, subscription EmployeeSubscriptionPurchaseSnapshot, receipt snapshotReceipt) error {
	values, err := snapshotScan(tx.QueryRowContext(ctx, `SELECT operation_id,action,actor_kind,actor_admin_id,actor_employee_id,actor_system_id,resource_kind,resource_id,payload_digest,digest_version,created_at FROM financial_operations WHERE operation_id=?`, receipt.OperationID), 11)
	if err != nil {
		return ErrUnavailable
	}
	operationID, operationOK := snapshotText(values[0])
	action, actionOK := snapshotText(values[1])
	actor, actorOK := snapshotStoredActor(values[2], values[3], values[4], values[5])
	kind, kindOK := snapshotText(values[6])
	resourceID, resourceOK := snapshotText(values[7])
	digest, digestOK := values[8].([]byte)
	version, versionOK := snapshotInt(values[9])
	created, createdOK := snapshotText(values[10])
	if !operationOK || operationID != receipt.OperationID || !actionOK || action != "subscription_purchase" ||
		!actorOK || actor != receipt.Actor || !kindOK || kind != "subscription" || !resourceOK ||
		resourceID != subscription.SubscriptionID || !digestOK || len(digest) != 32 ||
		!versionOK || (version != 1 && version != 2) || !createdOK || created != subscription.StartedAt ||
		actor.Kind == "legacy_unknown" && version != 1 ||
		(actor.Kind == "employee" || actor.Kind == "system") && version != 2 {
		return ErrUnavailable
	}
	if receipt.Action == "subscription.renew" && actor.Kind == "employee" {
		started, err := parseSubscriptionStart(subscription.StartedAt)
		if err != nil {
			return ErrUnavailable
		}
		owner := Owner{Kind: OwnerEmployee, EmployeeID: actor.ID}
		post := employeePurchasePost(EmployeePurchaseInput{OperationID: operationID, Actor: Actor{Kind: ActorEmployee, ID: actor.ID}, Owner: owner,
			Expected: ExpectedPurchasePlan{PlanID: subscription.PlanID, Revision: subscription.PlanRevision, Currency: subscription.Currency,
				Interval: "monthly", PriceMicro: subscription.PriceMicro, CreditMicro: subscription.CreditMicro}, ObservedAt: started}, subscription.SubscriptionID)
		wanted, err := postDigestV2(post, post.Actor)
		if err != nil || !bytes.Equal(digest, wanted[:]) {
			return ErrUnavailable
		}
	}
	return nil
}

func snapshotPurchaseEntries(ctx context.Context, tx *sql.Tx, subscription EmployeeSubscriptionPurchaseSnapshot, accountID, operationID string, hooks purchaseSnapshotReadHooks) error {
	rows, err := snapshotRows(ctx, tx, `SELECT id,operation_id,account_id,kind,amount_micro,original_entry_id,resource_kind,resource_id,created_at FROM financial_entries WHERE operation_id=? ORDER BY id LIMIT 3`, 3, 9, hooks, operationID)
	if err != nil || len(rows) != 2 {
		return ErrUnavailable
	}
	charge, credit := false, false
	for _, values := range rows {
		id, idOK := snapshotText(values[0])
		storedOperation, operationOK := snapshotText(values[1])
		storedAccount, accountOK := snapshotText(values[2])
		kind, kindOK := snapshotText(values[3])
		amount, amountOK := snapshotInt(values[4])
		resourceKind, resourceKindOK := snapshotText(values[6])
		resourceID, resourceOK := snapshotText(values[7])
		created, createdOK := snapshotText(values[8])
		if !idOK || !validCommercialText(id, 256) || !operationOK || storedOperation != operationID ||
			!accountOK || storedAccount != accountID || !kindOK || !amountOK || values[5] != nil ||
			!resourceKindOK || resourceKind != "subscription" || !resourceOK ||
			resourceID != subscription.SubscriptionID || !createdOK || created != subscription.StartedAt {
			return ErrUnavailable
		}
		switch kind {
		case "subscription_charge":
			if charge || amount != -subscription.PriceMicro {
				return ErrUnavailable
			}
			charge = true
		case "subscription_credit":
			if credit || amount != subscription.CreditMicro {
				return ErrUnavailable
			}
			credit = true
		default:
			return ErrUnavailable
		}
	}
	if !charge || !credit {
		return ErrUnavailable
	}
	return nil
}

func snapshotIncomingRenewal(ctx context.Context, tx *sql.Tx, subscription EmployeeSubscriptionPurchaseSnapshot, employeeID string, receipt snapshotReceipt, hooks purchaseSnapshotReadHooks) error {
	if receipt.Action == "subscription.renew" && receipt.Actor.Kind == "employee" && receipt.Actor.ID != employeeID {
		return ErrUnavailable
	}
	links, err := snapshotRows(ctx, tx, `SELECT predecessor_id,successor_id,operation_id,created_at FROM financial_subscription_renewals WHERE successor_id=? LIMIT 2`, 2, 4, hooks, subscription.SubscriptionID)
	if err != nil {
		return err
	}
	if receipt.Action == "subscription.create" {
		if len(links) != 0 {
			return ErrUnavailable
		}
		return nil
	}
	if len(links) != 1 {
		return ErrUnavailable
	}
	values := links[0]
	predecessorID, predecessorOK := snapshotText(values[0])
	successorID, successorOK := snapshotText(values[1])
	operationID, operationOK := snapshotText(values[2])
	created, createdOK := snapshotText(values[3])
	if !predecessorOK || !validCommercialText(predecessorID, 256) || predecessorID == subscription.SubscriptionID ||
		!successorOK || successorID != subscription.SubscriptionID || !operationOK || operationID != receipt.OperationID ||
		!createdOK || created != subscription.StartedAt || subscription.Interval != "monthly" {
		return ErrUnavailable
	}
	prior, err := snapshotScan(tx.QueryRowContext(ctx, `SELECT p.id,p.plan_id,p.interval,p.status,p.started_at,p.period_end_at,p.cancelled_at,a.owner_kind,a.owner_key,a.employee_id,a.key_id,a.resource_kind,a.resource_id FROM financial_subscriptions p JOIN financial_accounts a ON a.id=p.account_id WHERE p.id=?`, predecessorID), 13)
	if err != nil {
		return ErrUnavailable
	}
	id, idOK := snapshotText(prior[0])
	plan, planOK := snapshotText(prior[1])
	interval, intervalOK := snapshotText(prior[2])
	status, statusOK := snapshotText(prior[3])
	started, startedOK := snapshotText(prior[4])
	endText, endOK := snapshotText(prior[5])
	ownerKind, ownerKindOK := snapshotText(prior[7])
	ownerKeyValue, ownerKeyOK := snapshotText(prior[8])
	ownerEmployee, ownerEmployeeOK := snapshotText(prior[9])
	resourceKind, resourceKindOK := snapshotText(prior[11])
	resourceID, resourceIDOK := snapshotText(prior[12])
	startTime, startErr := parseSubscriptionStart(started)
	endTime, endErr := parseSubscriptionEnd(endText)
	expectedEnd, frozenErr := monthlyPeriodEnd(startTime)
	successorStart, successorErr := parseSubscriptionStart(subscription.StartedAt)
	if !idOK || id != predecessorID || !planOK || plan != subscription.PlanID ||
		!intervalOK || interval != "monthly" || !statusOK || status != "expired" ||
		!startedOK || !endOK || prior[6] != nil || startErr != nil || endErr != nil ||
		frozenErr != nil || !endTime.Equal(expectedEnd) || successorErr != nil || endTime.After(successorStart) ||
		!ownerKindOK || ownerKind != string(OwnerEmployee) || !ownerKeyOK ||
		ownerKeyValue != ownerKey(Owner{Kind: OwnerEmployee, EmployeeID: employeeID}) ||
		!ownerEmployeeOK || ownerEmployee != employeeID || prior[10] != nil ||
		!resourceKindOK || resourceKind != "" || !resourceIDOK || resourceID != "" {
		return ErrUnavailable
	}
	if receipt.Actor.Kind == "employee" {
		input := EmployeeMonthlyRenewalInput{OperationID: receipt.OperationID, Actor: Actor{Kind: ActorEmployee, ID: employeeID},
			Owner: Owner{Kind: OwnerEmployee, EmployeeID: employeeID}, PredecessorID: predecessorID, PredecessorEnd: endText,
			Expected: ExpectedPurchasePlan{PlanID: subscription.PlanID, Revision: subscription.PlanRevision, Currency: subscription.Currency,
				Interval: "monthly", PriceMicro: subscription.PriceMicro, CreditMicro: subscription.CreditMicro}, ObservedAt: successorStart}
		wanted, err := employeeMonthlyRenewalDigest(input)
		if err != nil || !bytes.Equal(receipt.Digest[:], wanted[:]) {
			return ErrUnavailable
		}
	}
	return nil
}
