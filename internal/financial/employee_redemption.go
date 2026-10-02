package financial

// Independently authored for docs/employee-self-redemption-contract.md.
// The caller owns the single SQLite transaction and its final authorization.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"
)

type EmployeeRedemptionInput struct {
	OperationID string
	Actor       Actor
	Owner       Owner
	CodeDigest  [sha256.Size]byte
	ObservedAt  time.Time
}

type EmployeeRedemptionResult struct {
	Currency    string
	AmountMicro int64
	CreditedAt  time.Time
	Replay      bool
}

func validEmployeeRedemptionInput(input EmployeeRedemptionInput) bool {
	return validText(input.OperationID, 128) && input.Actor.Kind == ActorEmployee &&
		validText(input.Actor.ID, 256) && input.Owner.Kind == OwnerEmployee &&
		input.Owner.EmployeeID == input.Actor.ID && input.Owner.KeyID == "" &&
		input.Owner.ResourceKind == "" && input.Owner.ResourceID == "" && input.ObservedAt.Location() == time.UTC
}

func employeeRedemptionDigest(input EmployeeRedemptionInput) ([sha256.Size]byte, error) {
	return DigestPayload(struct {
		Domain      string `json:"domain"`
		OperationID string `json:"operation_id"`
		Action      string `json:"action"`
		ActorKind   string `json:"actor_kind"`
		ActorID     string `json:"actor_id"`
		OwnerKind   string `json:"owner_kind"`
		OwnerID     string `json:"owner_id"`
		CodeDigest  string `json:"code_digest"`
	}{"employee-redemption/v1", input.OperationID, "redemption.redeem", string(input.Actor.Kind), input.Actor.ID,
		string(input.Owner.Kind), input.Owner.EmployeeID, hex.EncodeToString(input.CodeDigest[:])})
}

func employeeRedemptionPost(input EmployeeRedemptionInput, redemptionID, currency string, amount int64) Post {
	return Post{OperationID: input.OperationID, Action: "redemption", Actor: input.Actor,
		ResourceKind: "redemption", ResourceID: redemptionID, ObservedAt: input.ObservedAt,
		Entries: []EntryInput{{Owner: input.Owner, Currency: currency, Kind: EntryRedemption,
			AmountMicro: amount, ResourceKind: "redemption", ResourceID: redemptionID}}}
}

// Redemption rows are immutable while uses is mutable. Never trust a lowered
// counter as new inventory, including when reconstructing a committed replay.
func employeeRedemptionInventory(ctx context.Context, tx *sql.Tx, codeID string, uses, maxUses int64) error {
	var committed int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_redemptions WHERE code_id=?`, codeID).Scan(&committed); err != nil {
		return ErrUnavailable
	}
	if uses < 0 || maxUses < 1 || uses > maxUses || committed != uses {
		return ErrSchema
	}
	return nil
}

// RedeemEmployeeCodeTx checks exact committed replay before any gate for a new
// redemption. A successful return is provisional until the caller commits.
func (c *Commercial) RedeemEmployeeCodeTx(ctx context.Context, tx *sql.Tx, input EmployeeRedemptionInput) (EmployeeRedemptionResult, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validEmployeeRedemptionInput(input) {
		return EmployeeRedemptionResult{}, ErrInvalid
	}
	if ctx.Err() != nil {
		return EmployeeRedemptionResult{}, ErrUnavailable
	}
	if err := validateSchema(ctx, tx); err != nil {
		return EmployeeRedemptionResult{}, err
	}
	if err := validateCommercialSchema(ctx, tx, commercialOperationsDDL, false); err != nil {
		return EmployeeRedemptionResult{}, err
	}
	digest, err := employeeRedemptionDigest(input)
	if err != nil {
		return EmployeeRedemptionResult{}, ErrUnavailable
	}
	meta := WriteMeta{OperationID: input.OperationID, Actor: input.Actor, PayloadDigest: digest, ObservedAt: input.ObservedAt}
	receipt, found, err := existingCommercialOperation(ctx, tx, meta, "redemption.redeem")
	if err != nil {
		return EmployeeRedemptionResult{}, err
	}
	if found {
		return replayEmployeeRedemption(ctx, tx, input, receipt)
	}
	// A ledger-only expected operation is corrupt, not a fresh opportunity to
	// credit. A different global operation is an opaque business conflict.
	stored, ledgerFound, err := operationDigest(ctx, tx, input.OperationID)
	if err != nil {
		return EmployeeRedemptionResult{}, err
	}
	if ledgerFound {
		var action string
		if err := tx.QueryRowContext(ctx, `SELECT action FROM financial_operations WHERE operation_id=?`, input.OperationID).Scan(&action); err != nil {
			return EmployeeRedemptionResult{}, ErrUnavailable
		}
		if action == "redemption" && stored.Actor == input.Actor {
			return EmployeeRedemptionResult{}, ErrSchema
		}
		return EmployeeRedemptionResult{}, ErrConflict
	}
	if err := requireEmployeePurchaseEnabled(ctx, tx); err != nil {
		return EmployeeRedemptionResult{}, err
	}
	var codeID, currency, expiryText string
	var amount, maxUses, uses int64
	var enabled int
	var storedDigest []byte
	var expiry sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,code_digest,amount_micro,currency,max_uses,uses,expires_at,enabled
		FROM financial_redemption_codes WHERE code_digest=?`, input.CodeDigest[:]).
		Scan(&codeID, &storedDigest, &amount, &currency, &maxUses, &uses, &expiry, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return EmployeeRedemptionResult{}, ErrConflict
	}
	if err != nil {
		return EmployeeRedemptionResult{}, ErrUnavailable
	}
	if !validText(codeID, 256) || !equalBytes(storedDigest, input.CodeDigest[:]) || amount <= 0 ||
		!validCurrency(currency) || maxUses < 1 || uses < 0 || uses > maxUses || (enabled != 0 && enabled != 1) {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	if err := employeeRedemptionInventory(ctx, tx, codeID, uses, maxUses); err != nil {
		return EmployeeRedemptionResult{}, err
	}
	if enabled == 0 || uses >= maxUses {
		return EmployeeRedemptionResult{}, ErrConflict
	}
	if expiry.Valid {
		expiryText = expiry.String
		if !validPurchaseStoredTime(expiryText) {
			return EmployeeRedemptionResult{}, ErrSchema
		}
		until, _ := time.Parse(time.RFC3339Nano, expiryText)
		if !input.ObservedAt.Before(until) {
			return EmployeeRedemptionResult{}, ErrConflict
		}
	}
	if _, err := resolveOwner(ctx, tx, input.Owner); err != nil {
		return EmployeeRedemptionResult{}, err
	}
	var existingAccount string
	err = tx.QueryRowContext(ctx, `SELECT id FROM financial_accounts WHERE owner_key=? AND currency=?`, ownerKey(input.Owner), currency).Scan(&existingAccount)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return EmployeeRedemptionResult{}, ErrUnavailable
	}
	if err == nil {
		accountID, err := employeePurchaseWallet(ctx, tx, input.Owner, currency)
		if err != nil || accountID != existingAccount {
			return EmployeeRedemptionResult{}, ErrSchema
		}
		var prior int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM financial_redemptions WHERE code_id=? AND account_id=?`, codeID, accountID).Scan(&prior)
		if err == nil {
			return EmployeeRedemptionResult{}, ErrConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return EmployeeRedemptionResult{}, ErrUnavailable
		}
	}
	// No account is created until all rejection gates above have passed.
	accountID, owner, err := NewLedger(c.db).EnsureAccountTx(ctx, tx, input.Owner, currency, input.ObservedAt)
	if err != nil {
		return EmployeeRedemptionResult{}, err
	}
	if owner != input.Owner {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	redemptionID, err := randomID("redemption")
	if err != nil {
		return EmployeeRedemptionResult{}, ErrUnavailable
	}
	posted, err := NewLedger(c.db).PostTx(ctx, tx, employeeRedemptionPost(input, redemptionID, currency, amount))
	if err != nil {
		return EmployeeRedemptionResult{}, err
	}
	if len(posted) != 1 || posted[0].AccountID != accountID {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	cas, err := tx.ExecContext(ctx, `UPDATE financial_redemption_codes SET uses=uses+1 WHERE id=? AND uses=? AND enabled=1 AND uses<max_uses`, codeID, uses)
	if err != nil {
		return EmployeeRedemptionResult{}, ErrUnavailable
	}
	changed, err := cas.RowsAffected()
	if err != nil {
		return EmployeeRedemptionResult{}, ErrUnavailable
	}
	if changed != 1 {
		return EmployeeRedemptionResult{}, ErrConflict
	}
	if c.employeeRedemptionBeforeInsert != nil {
		if err := c.employeeRedemptionBeforeInsert(ctx, tx); err != nil {
			return EmployeeRedemptionResult{}, ErrUnavailable
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_redemptions(id,code_id,account_id,entry_id,created_at) VALUES(?,?,?,?,?)`,
		redemptionID, codeID, accountID, posted[0].ID, formatCommercialTime(input.ObservedAt)); err != nil {
		// Business reuse was checked above. An arbitrary insert failure may be
		// busy, cancelled, corrupt, or a random-ID collision: fail closed.
		return EmployeeRedemptionResult{}, ErrUnavailable
	}
	receipt = CommercialReceipt{OperationID: input.OperationID, ResourceKind: "redemption", ResourceID: redemptionID,
		Revision: 1, CreatedAt: input.ObservedAt}
	if err := insertCommercialOperation(ctx, tx, meta, "redemption.redeem", receipt); err != nil {
		return EmployeeRedemptionResult{}, err
	}
	return EmployeeRedemptionResult{Currency: currency, AmountMicro: amount, CreditedAt: input.ObservedAt}, nil
}

func replayEmployeeRedemption(ctx context.Context, tx *sql.Tx, input EmployeeRedemptionInput, receipt CommercialReceipt) (EmployeeRedemptionResult, error) {
	if receipt.ResourceKind != "redemption" || !validText(receipt.ResourceID, 256) || receipt.Revision != 1 {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	var receiptTime, receiptTimeType string
	if err := tx.QueryRowContext(ctx, `SELECT created_at,typeof(created_at) FROM financial_commercial_operations WHERE operation_id=?`, input.OperationID).
		Scan(&receiptTime, &receiptTimeType); err != nil || receiptTimeType != "text" ||
		!validPurchaseStoredTime(receiptTime) || receiptTime != formatCommercialTime(receipt.CreatedAt) {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	var codeID, accountID, entryID, redeemedAt, codeCurrency, accountKind, accountKey, employeeID, resourceKind, resourceID, accountCurrency string
	var entryOperation, entryAccount, entryKind, entryResourceKind, entryResourceID, entryAt string
	var codeDigest []byte
	var codeAmount, codeMaxUses, codeUses, entryAmount int64
	var keyID sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT r.code_id,r.account_id,r.entry_id,r.created_at,
		c.code_digest,c.amount_micro,c.currency,c.max_uses,c.uses,
		a.owner_kind,a.owner_key,a.employee_id,a.key_id,a.resource_kind,a.resource_id,a.currency,
		e.operation_id,e.account_id,e.kind,e.amount_micro,e.resource_kind,e.resource_id,e.created_at
		FROM financial_redemptions r JOIN financial_redemption_codes c ON c.id=r.code_id
		JOIN financial_accounts a ON a.id=r.account_id JOIN financial_entries e ON e.id=r.entry_id WHERE r.id=?`, receipt.ResourceID).
		Scan(&codeID, &accountID, &entryID, &redeemedAt, &codeDigest, &codeAmount, &codeCurrency, &codeMaxUses, &codeUses,
			&accountKind, &accountKey, &employeeID, &keyID, &resourceKind, &resourceID, &accountCurrency,
			&entryOperation, &entryAccount, &entryKind, &entryAmount, &entryResourceKind, &entryResourceID, &entryAt)
	if err != nil {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	if !equalBytes(codeDigest, input.CodeDigest[:]) {
		return EmployeeRedemptionResult{}, ErrConflict
	}
	if err := employeeRedemptionInventory(ctx, tx, codeID, codeUses, codeMaxUses); err != nil {
		return EmployeeRedemptionResult{}, err
	}
	if !validText(codeID, 256) || !validText(entryID, 256) || !validText(accountID, 256) ||
		!validCurrency(codeCurrency) || codeAmount <= 0 || accountKind != string(OwnerEmployee) ||
		accountKey != ownerKey(input.Owner) || employeeID != input.Owner.EmployeeID || keyID.Valid ||
		resourceKind != "" || resourceID != "" || accountCurrency != codeCurrency ||
		entryOperation != input.OperationID || entryAccount != accountID || entryKind != string(EntryRedemption) ||
		entryAmount != codeAmount || entryResourceKind != "redemption" || entryResourceID != receipt.ResourceID ||
		redeemedAt != entryAt || redeemedAt != formatCommercialTime(receipt.CreatedAt) || !validPurchaseStoredTime(redeemedAt) {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	var entryCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_entries WHERE operation_id=?`, input.OperationID).Scan(&entryCount); err != nil || entryCount != 1 {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	stored, found, err := operationDigest(ctx, tx, input.OperationID)
	if err != nil {
		return EmployeeRedemptionResult{}, err
	}
	if !found || stored.Version != 2 || stored.Actor != input.Actor {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	var action, kind, resource, created string
	if err := tx.QueryRowContext(ctx, `SELECT action,resource_kind,resource_id,created_at FROM financial_operations WHERE operation_id=?`, input.OperationID).
		Scan(&action, &kind, &resource, &created); err != nil || action != "redemption" || kind != "redemption" ||
		resource != receipt.ResourceID || created != redeemedAt {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	post := employeeRedemptionPost(input, receipt.ResourceID, codeCurrency, codeAmount)
	expected, err := postDigestV2(post, input.Actor)
	if err != nil || !equalBytes(stored.Digest, expected[:]) {
		return EmployeeRedemptionResult{}, ErrSchema
	}
	return EmployeeRedemptionResult{Currency: codeCurrency, AmountMicro: codeAmount, CreditedAt: receipt.CreatedAt, Replay: true}, nil
}
