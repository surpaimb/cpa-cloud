package financial

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

var (
	ErrInvalid      = errors.New("financial: invalid input")
	ErrNotFound     = errors.New("financial: not found")
	ErrConflict     = errors.New("financial: conflict")
	ErrInsufficient = errors.New("financial: insufficient balance")
	ErrUnavailable  = errors.New("financial: unavailable")
	ErrSchema       = errors.New("financial: incompatible schema")
)

type OwnerKind string

const (
	OwnerEmployee OwnerKind = "employee"
	OwnerKey      OwnerKind = "key"
	OwnerResource OwnerKind = "resource"
)

type Owner struct {
	Kind         OwnerKind `json:"kind"`
	EmployeeID   string    `json:"employee_id"`
	KeyID        string    `json:"key_id,omitempty"`
	ResourceKind string    `json:"resource_kind,omitempty"`
	ResourceID   string    `json:"resource_id,omitempty"`
}

type EntryKind string

const (
	EntryAdjustmentCredit   EntryKind = "adjustment_credit"
	EntryAdjustmentDebit    EntryKind = "adjustment_debit"
	EntryTopUp              EntryKind = "topup"
	EntryRedemption         EntryKind = "redemption"
	EntrySubscriptionCharge EntryKind = "subscription_charge"
	EntrySubscriptionCredit EntryKind = "subscription_credit"
	EntryRefund             EntryKind = "refund"
	EntryUsageCharge        EntryKind = "usage_charge"
)

type EntryInput struct {
	Owner           Owner     `json:"owner"`
	Currency        string    `json:"currency"`
	Kind            EntryKind `json:"kind"`
	AmountMicro     int64     `json:"amount_micro"`
	OriginalEntryID string    `json:"original_entry_id,omitempty"`
	ResourceKind    string    `json:"resource_kind,omitempty"`
	ResourceID      string    `json:"resource_id,omitempty"`
}

type Post struct {
	OperationID        string       `json:"operation_id"`
	Action             string       `json:"action"`
	ActorAdminID       string       `json:"actor_admin_id,omitempty"`
	ResourceKind       string       `json:"resource_kind"`
	ResourceID         string       `json:"resource_id"`
	ObservedAt         time.Time    `json:"observed_at"`
	RequireNonNegative bool         `json:"require_non_negative"`
	Entries            []EntryInput `json:"entries"`
}

type Entry struct {
	ID              string
	OperationID     string
	AccountID       string
	Owner           Owner
	Currency        string
	Kind            EntryKind
	AmountMicro     int64
	OriginalEntryID string
	ResourceKind    string
	ResourceID      string
	CreatedAt       time.Time
}

type Balance struct {
	AccountID   string
	Owner       Owner
	Currency    string
	AmountMicro int64
}

// EnsureAccountTx validates the single-instance owner boundary and returns a
// stable currency account in the caller-owned transaction. It never posts a
// money entry.
func (l *Ledger) EnsureAccountTx(ctx context.Context, tx *sql.Tx, owner Owner, currency string, at time.Time) (string, Owner, error) {
	if l == nil || l.db == nil || ctx == nil || tx == nil || !validCurrency(currency) || at.Location() != time.UTC {
		return "", Owner{}, ErrInvalid
	}
	resolved, err := resolveOwner(ctx, tx, owner)
	if err != nil {
		return "", Owner{}, err
	}
	id, err := ensureAccount(ctx, tx, resolved, currency, at)
	return id, resolved, err
}

func (l *Ledger) BalanceTx(ctx context.Context, tx *sql.Tx, accountID string) (int64, error) {
	if l == nil || l.db == nil || ctx == nil || tx == nil || !validText(accountID, 256) {
		return 0, ErrInvalid
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT 1 FROM financial_accounts WHERE id=?`, accountID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	} else if err != nil {
		return 0, ErrUnavailable
	}
	return accountBalance(ctx, tx, accountID)
}

type Ledger struct{ db *sql.DB }

func NewLedger(db *sql.DB) *Ledger { return &Ledger{db: db} }

const accountsDDL = `CREATE TABLE IF NOT EXISTS financial_accounts (
	id TEXT PRIMARY KEY,
	owner_kind TEXT NOT NULL CHECK(owner_kind IN ('employee','key','resource')),
	owner_key TEXT NOT NULL,
	employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE RESTRICT,
	key_id TEXT REFERENCES access_keys(id) ON DELETE RESTRICT,
	resource_kind TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	currency TEXT NOT NULL CHECK(length(currency)=3 AND currency GLOB '[A-Z][A-Z][A-Z]'),
	created_at TEXT NOT NULL,
	UNIQUE(owner_key,currency),
	CHECK((owner_kind='employee' AND key_id IS NULL AND resource_kind='' AND resource_id='') OR
		(owner_kind='key' AND key_id IS NOT NULL AND resource_kind='' AND resource_id='') OR
		(owner_kind='resource' AND resource_kind<>'' AND resource_id<>''))
)`

const operationsDDL = `CREATE TABLE IF NOT EXISTS financial_operations (
	operation_id TEXT PRIMARY KEY,
	action TEXT NOT NULL CHECK(action IN ('adjustment','topup','redemption','subscription_purchase','payment_callback','refund','usage_charge')),
	actor_admin_id TEXT REFERENCES admins(id) ON DELETE RESTRICT,
	resource_kind TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
	created_at TEXT NOT NULL
)`

const entriesDDL = `CREATE TABLE IF NOT EXISTS financial_entries (
	id TEXT PRIMARY KEY,
	operation_id TEXT NOT NULL REFERENCES financial_operations(operation_id) ON DELETE RESTRICT,
	account_id TEXT NOT NULL REFERENCES financial_accounts(id) ON DELETE RESTRICT,
	kind TEXT NOT NULL CHECK(kind IN ('adjustment_credit','adjustment_debit','topup','redemption','subscription_charge','subscription_credit','refund','usage_charge')),
	amount_micro INTEGER NOT NULL CHECK(typeof(amount_micro)='integer' AND amount_micro<>0),
	original_entry_id TEXT REFERENCES financial_entries(id) ON DELETE RESTRICT,
	resource_kind TEXT NOT NULL,
	resource_id TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE(operation_id,account_id,kind,resource_kind,resource_id)
)`

const accountOwnerIndexDDL = `CREATE INDEX IF NOT EXISTS financial_accounts_owner_idx ON financial_accounts(employee_id,key_id,resource_kind,resource_id,currency)`
const entryAccountIndexDDL = `CREATE INDEX IF NOT EXISTS financial_entries_account_idx ON financial_entries(account_id,created_at,id)`
const entryResourceIndexDDL = `CREATE INDEX IF NOT EXISTS financial_entries_resource_idx ON financial_entries(resource_kind,resource_id,created_at,id)`
const entriesNoUpdateDDL = `CREATE TRIGGER IF NOT EXISTS financial_entries_no_update BEFORE UPDATE ON financial_entries BEGIN SELECT RAISE(ABORT,'financial entries are immutable'); END`
const entriesNoDeleteDDL = `CREATE TRIGGER IF NOT EXISTS financial_entries_no_delete BEFORE DELETE ON financial_entries BEGIN SELECT RAISE(ABORT,'financial entries are immutable'); END`
const operationsNoUpdateDDL = `CREATE TRIGGER IF NOT EXISTS financial_operations_no_update BEFORE UPDATE ON financial_operations BEGIN SELECT RAISE(ABORT,'financial operations are immutable'); END`
const operationsNoDeleteDDL = `CREATE TRIGGER IF NOT EXISTS financial_operations_no_delete BEFORE DELETE ON financial_operations BEGIN SELECT RAISE(ABORT,'financial operations are immutable'); END`
const accountsNoUpdateDDL = `CREATE TRIGGER IF NOT EXISTS financial_accounts_no_update BEFORE UPDATE ON financial_accounts BEGIN SELECT RAISE(ABORT,'financial accounts are immutable'); END`
const accountsNoDeleteDDL = `CREATE TRIGGER IF NOT EXISTS financial_accounts_no_delete BEFORE DELETE ON financial_accounts BEGIN SELECT RAISE(ABORT,'financial accounts are immutable'); END`

func (l *Ledger) Migrate(ctx context.Context) error {
	if l == nil || l.db == nil || ctx == nil {
		return ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	defer tx.Rollback()
	for _, prerequisite := range []string{"employees", "access_keys", "admins"} {
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, prerequisite).Scan(&count); err != nil || count != 1 {
			return ErrSchema
		}
	}
	for _, statement := range []string{accountsDDL, operationsDDL, entriesDDL, accountOwnerIndexDDL, entryAccountIndexDDL, entryResourceIndexDDL, entriesNoUpdateDDL, entriesNoDeleteDDL, operationsNoUpdateDDL, operationsNoDeleteDDL, accountsNoUpdateDDL, accountsNoDeleteDDL} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return ErrSchema
		}
	}
	if err := validateSchema(ctx, tx); err != nil {
		return err
	}
	if err := validateStored(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ErrUnavailable
	}
	return nil
}

func (l *Ledger) Post(ctx context.Context, input Post) ([]Entry, error) {
	if l == nil || l.db == nil || ctx == nil {
		return nil, ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer tx.Rollback()
	entries, err := l.PostTx(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, ErrUnavailable
	}
	return entries, nil
}

func (l *Ledger) PostTx(ctx context.Context, tx *sql.Tx, input Post) ([]Entry, error) {
	if l == nil || l.db == nil || ctx == nil || tx == nil || !validPost(input) {
		return nil, ErrInvalid
	}
	digest, err := postDigest(input)
	if err != nil {
		return nil, ErrInvalid
	}
	storedDigest, found, err := operationDigest(ctx, tx, input.OperationID)
	if err != nil {
		return nil, err
	}
	if found {
		if !equalBytes(storedDigest, digest[:]) {
			return nil, ErrConflict
		}
		return loadOperationEntries(ctx, tx, input.OperationID)
	}

	type pending struct {
		accountID string
		input     EntryInput
	}
	pendingEntries := make([]pending, 0, len(input.Entries))
	deltas := make(map[string]int64)
	for _, item := range input.Entries {
		owner, err := resolveOwner(ctx, tx, item.Owner)
		if err != nil {
			return nil, err
		}
		accountID, err := ensureAccount(ctx, tx, owner, item.Currency, input.ObservedAt)
		if err != nil {
			return nil, err
		}
		if item.OriginalEntryID != "" {
			var originalAccount, originalKind string
			var originalAmount int64
			if err := tx.QueryRowContext(ctx, `SELECT account_id,kind,amount_micro FROM financial_entries WHERE id=?`, item.OriginalEntryID).Scan(&originalAccount, &originalKind, &originalAmount); errors.Is(err, sql.ErrNoRows) {
				return nil, ErrNotFound
			} else if err != nil {
				return nil, ErrUnavailable
			} else if originalAccount != accountID {
				return nil, ErrConflict
			}
			if err := validateReversalLimit(ctx, tx, item, EntryKind(originalKind), originalAmount); err != nil {
				return nil, err
			}
		}
		current := deltas[accountID]
		next, ok := checkedAdd(current, item.AmountMicro)
		if !ok {
			return nil, ErrUnavailable
		}
		deltas[accountID] = next
		item.Owner = owner
		pendingEntries = append(pendingEntries, pending{accountID: accountID, input: item})
	}
	for accountID, delta := range deltas {
		balance, err := accountBalance(ctx, tx, accountID)
		if err != nil {
			return nil, err
		}
		projected, ok := checkedAdd(balance, delta)
		if !ok {
			return nil, ErrUnavailable
		}
		if input.RequireNonNegative && projected < 0 {
			return nil, ErrInsufficient
		}
	}
	actor := any(nil)
	if input.ActorAdminID != "" {
		actor = input.ActorAdminID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_operations(operation_id,action,actor_admin_id,resource_kind,resource_id,payload_digest,created_at) VALUES(?,?,?,?,?,?,?)`, input.OperationID, input.Action, actor, input.ResourceKind, input.ResourceID, digest[:], input.ObservedAt.UTC().Format(time.RFC3339Nano)); err != nil {
		return nil, ErrUnavailable
	}
	result := make([]Entry, 0, len(pendingEntries))
	for _, pending := range pendingEntries {
		id, err := randomID("fe")
		if err != nil {
			return nil, ErrUnavailable
		}
		original := any(nil)
		if pending.input.OriginalEntryID != "" {
			original = pending.input.OriginalEntryID
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO financial_entries(id,operation_id,account_id,kind,amount_micro,original_entry_id,resource_kind,resource_id,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, input.OperationID, pending.accountID, pending.input.Kind, pending.input.AmountMicro, original, pending.input.ResourceKind, pending.input.ResourceID, input.ObservedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return nil, ErrUnavailable
		}
		result = append(result, Entry{ID: id, OperationID: input.OperationID, AccountID: pending.accountID, Owner: pending.input.Owner, Currency: pending.input.Currency, Kind: pending.input.Kind, AmountMicro: pending.input.AmountMicro, OriginalEntryID: pending.input.OriginalEntryID, ResourceKind: pending.input.ResourceKind, ResourceID: pending.input.ResourceID, CreatedAt: input.ObservedAt.UTC()})
	}
	return result, nil
}

func (l *Ledger) Balance(ctx context.Context, owner Owner, currency string) (Balance, error) {
	if l == nil || l.db == nil || ctx == nil || !validCurrency(currency) {
		return Balance{}, ErrInvalid
	}
	tx, err := l.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Balance{}, ErrUnavailable
	}
	defer tx.Rollback()
	resolved, err := resolveOwner(ctx, tx, owner)
	if err != nil {
		return Balance{}, err
	}
	var accountID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM financial_accounts WHERE owner_key=? AND currency=?`, ownerKey(resolved), currency).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return Balance{Owner: resolved, Currency: currency}, nil
	}
	if err != nil {
		return Balance{}, ErrUnavailable
	}
	amount, err := accountBalance(ctx, tx, accountID)
	if err != nil {
		return Balance{}, err
	}
	return Balance{AccountID: accountID, Owner: resolved, Currency: currency, AmountMicro: amount}, nil
}

func resolveOwner(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, owner Owner) (Owner, error) {
	if !validOwnerShape(owner) {
		return Owner{}, ErrInvalid
	}
	var employeeID string
	switch owner.Kind {
	case OwnerEmployee:
		if err := query.QueryRowContext(ctx, `SELECT id FROM employees WHERE id=?`, owner.EmployeeID).Scan(&employeeID); errors.Is(err, sql.ErrNoRows) {
			return Owner{}, ErrNotFound
		} else if err != nil {
			return Owner{}, ErrUnavailable
		}
	case OwnerKey:
		if err := query.QueryRowContext(ctx, `SELECT employee_id FROM access_keys WHERE id=?`, owner.KeyID).Scan(&employeeID); errors.Is(err, sql.ErrNoRows) {
			return Owner{}, ErrNotFound
		} else if err != nil {
			return Owner{}, ErrUnavailable
		}
		if employeeID != owner.EmployeeID {
			return Owner{}, ErrConflict
		}
	case OwnerResource:
		if owner.KeyID == "" {
			if err := query.QueryRowContext(ctx, `SELECT id FROM employees WHERE id=?`, owner.EmployeeID).Scan(&employeeID); errors.Is(err, sql.ErrNoRows) {
				return Owner{}, ErrNotFound
			} else if err != nil {
				return Owner{}, ErrUnavailable
			}
		} else {
			if err := query.QueryRowContext(ctx, `SELECT employee_id FROM access_keys WHERE id=?`, owner.KeyID).Scan(&employeeID); errors.Is(err, sql.ErrNoRows) {
				return Owner{}, ErrNotFound
			} else if err != nil {
				return Owner{}, ErrUnavailable
			}
			if employeeID != owner.EmployeeID {
				return Owner{}, ErrConflict
			}
		}
	}
	return owner, nil
}

func ensureAccount(ctx context.Context, tx *sql.Tx, owner Owner, currency string, at time.Time) (string, error) {
	key := ownerKey(owner)
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM financial_accounts WHERE owner_key=? AND currency=?`, key, currency).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", ErrUnavailable
	}
	id, err = randomID("fa")
	if err != nil {
		return "", ErrUnavailable
	}
	keyValue := any(nil)
	if owner.KeyID != "" {
		keyValue = owner.KeyID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO financial_accounts(id,owner_kind,owner_key,employee_id,key_id,resource_kind,resource_id,currency,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, id, owner.Kind, key, owner.EmployeeID, keyValue, owner.ResourceKind, owner.ResourceID, currency, at.UTC().Format(time.RFC3339Nano)); err != nil {
		return "", ErrUnavailable
	}
	return id, nil
}

func accountBalance(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, accountID string) (int64, error) {
	rows, err := query.QueryContext(ctx, `SELECT amount_micro FROM financial_entries WHERE account_id=? ORDER BY created_at,id`, accountID)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer rows.Close()
	var total int64
	for rows.Next() {
		var amount int64
		if err := rows.Scan(&amount); err != nil {
			return 0, ErrUnavailable
		}
		var ok bool
		total, ok = checkedAdd(total, amount)
		if !ok {
			return 0, ErrUnavailable
		}
	}
	if err := rows.Err(); err != nil {
		return 0, ErrUnavailable
	}
	return total, nil
}

func operationDigest(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, operationID string) ([]byte, bool, error) {
	var digest []byte
	err := query.QueryRowContext(ctx, `SELECT payload_digest FROM financial_operations WHERE operation_id=?`, operationID).Scan(&digest)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, ErrUnavailable
	}
	return digest, true, nil
}

func loadOperationEntries(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, operationID string) ([]Entry, error) {
	rows, err := query.QueryContext(ctx, `SELECT e.id,e.operation_id,e.account_id,a.owner_kind,a.employee_id,COALESCE(a.key_id,''),a.resource_kind,a.resource_id,a.currency,e.kind,e.amount_micro,COALESCE(e.original_entry_id,''),e.resource_kind,e.resource_id,e.created_at FROM financial_entries e JOIN financial_accounts a ON a.id=e.account_id WHERE e.operation_id=? ORDER BY e.id`, operationID)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	result := make([]Entry, 0)
	for rows.Next() {
		var item Entry
		var created string
		if err := rows.Scan(&item.ID, &item.OperationID, &item.AccountID, &item.Owner.Kind, &item.Owner.EmployeeID, &item.Owner.KeyID, &item.Owner.ResourceKind, &item.Owner.ResourceID, &item.Currency, &item.Kind, &item.AmountMicro, &item.OriginalEntryID, &item.ResourceKind, &item.ResourceID, &created); err != nil {
			return nil, ErrUnavailable
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrSchema
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return result, nil
}

func validPost(input Post) bool {
	if !validText(input.OperationID, 128) || !validAction(input.Action) || input.ActorAdminID != "" && !validText(input.ActorAdminID, 256) || !validText(input.ResourceKind, 64) || !validText(input.ResourceID, 256) || input.ObservedAt.Location() != time.UTC || len(input.Entries) == 0 || len(input.Entries) > 32 {
		return false
	}
	seen := make(map[string]bool)
	for _, item := range input.Entries {
		if !validOwnerShape(item.Owner) || !validCurrency(item.Currency) || !validEntryKind(item.Kind) || !validPostEntry(input.Action, item) || !validText(item.ResourceKind, 64) || !validText(item.ResourceID, 256) || item.ResourceKind != input.ResourceKind || item.ResourceID != input.ResourceID {
			return false
		}
		key := ownerKey(item.Owner) + "\x00" + item.Currency + "\x00" + string(item.Kind) + "\x00" + item.ResourceKind + "\x00" + item.ResourceID
		if seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

func validOwnerShape(owner Owner) bool {
	if !validText(owner.EmployeeID, 256) {
		return false
	}
	switch owner.Kind {
	case OwnerEmployee:
		return owner.KeyID == "" && owner.ResourceKind == "" && owner.ResourceID == ""
	case OwnerKey:
		return validText(owner.KeyID, 256) && owner.ResourceKind == "" && owner.ResourceID == ""
	case OwnerResource:
		return (owner.KeyID == "" || validText(owner.KeyID, 256)) && validText(owner.ResourceKind, 64) && validText(owner.ResourceID, 256)
	default:
		return false
	}
}

func ownerKey(owner Owner) string {
	encoded, _ := json.Marshal(owner)
	return "v1:" + string(encoded)
}

func postDigest(input Post) ([32]byte, error) {
	payload := struct {
		OperationID        string       `json:"operation_id"`
		Action             string       `json:"action"`
		ActorAdminID       string       `json:"actor_admin_id,omitempty"`
		ResourceKind       string       `json:"resource_kind"`
		ResourceID         string       `json:"resource_id"`
		RequireNonNegative bool         `json:"require_non_negative"`
		Entries            []EntryInput `json:"entries"`
	}{input.OperationID, input.Action, input.ActorAdminID, input.ResourceKind, input.ResourceID, input.RequireNonNegative, input.Entries}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(encoded), nil
}

func validAction(value string) bool {
	switch value {
	case "adjustment", "topup", "redemption", "subscription_purchase", "payment_callback", "refund", "usage_charge":
		return true
	}
	return false
}

func validEntryKind(value EntryKind) bool {
	switch value {
	case EntryAdjustmentCredit, EntryAdjustmentDebit, EntryTopUp, EntryRedemption, EntrySubscriptionCharge, EntrySubscriptionCredit, EntryRefund, EntryUsageCharge:
		return true
	}
	return false
}

func validPostEntry(action string, item EntryInput) bool {
	credit := item.Kind == EntryAdjustmentCredit || item.Kind == EntryTopUp || item.Kind == EntryRedemption || item.Kind == EntrySubscriptionCredit || item.Kind == EntryRefund
	if credit != (item.AmountMicro > 0) {
		return false
	}
	if item.Kind == EntryRefund || action == "refund" && item.Kind == EntryAdjustmentDebit {
		if !validText(item.OriginalEntryID, 256) {
			return false
		}
	} else if item.OriginalEntryID != "" {
		return false
	}
	switch action {
	case "adjustment":
		return item.Kind == EntryAdjustmentCredit || item.Kind == EntryAdjustmentDebit
	case "topup", "payment_callback":
		return item.Kind == EntryTopUp
	case "redemption":
		return item.Kind == EntryRedemption
	case "subscription_purchase":
		return item.Kind == EntrySubscriptionCharge || item.Kind == EntrySubscriptionCredit
	case "refund":
		return item.Kind == EntryRefund || item.Kind == EntryAdjustmentDebit
	case "usage_charge":
		return item.Kind == EntryUsageCharge
	default:
		return false
	}
}

func validateReversalLimit(ctx context.Context, tx *sql.Tx, item EntryInput, originalKind EntryKind, originalAmount int64) error {
	eligible := false
	if item.Kind == EntryRefund {
		eligible = originalAmount < 0 && (originalKind == EntryAdjustmentDebit || originalKind == EntrySubscriptionCharge || originalKind == EntryUsageCharge)
	} else if item.Kind == EntryAdjustmentDebit {
		eligible = originalAmount > 0 && originalKind == EntryTopUp
	}
	if !eligible {
		return ErrConflict
	}
	rows, err := tx.QueryContext(ctx, `SELECT amount_micro FROM financial_entries WHERE original_entry_id=? ORDER BY id`, item.OriginalEntryID)
	if err != nil {
		return ErrUnavailable
	}
	defer rows.Close()
	var reversed int64
	for rows.Next() {
		var amount int64
		if err := rows.Scan(&amount); err != nil {
			return ErrUnavailable
		}
		magnitude := amount
		if magnitude < 0 {
			if magnitude == math.MinInt64 {
				return ErrUnavailable
			}
			magnitude = -magnitude
		}
		var ok bool
		reversed, ok = checkedAdd(reversed, magnitude)
		if !ok {
			return ErrUnavailable
		}
	}
	if err := rows.Err(); err != nil {
		return ErrUnavailable
	}
	current := item.AmountMicro
	if current < 0 {
		if current == math.MinInt64 {
			return ErrInvalid
		}
		current = -current
	}
	limit := originalAmount
	if limit < 0 {
		if limit == math.MinInt64 {
			return ErrUnavailable
		}
		limit = -limit
	}
	total, ok := checkedAdd(reversed, current)
	if !ok {
		return ErrUnavailable
	}
	if total > limit {
		return ErrConflict
	}
	return nil
}

func validCurrency(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, char := range []byte(value) {
		if char < 'A' || char > 'Z' {
			return false
		}
	}
	return true
}

func validText(value string, limit int) bool {
	return value != "" && strings.TrimSpace(value) == value && len(value) <= limit && !strings.ContainsRune(value, 0)
}
func checkedAdd(left, right int64) (int64, bool) {
	if right > 0 && left > math.MaxInt64-right || right < 0 && left < math.MinInt64-right {
		return 0, false
	}
	return left + right, true
}
func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var diff byte
	for i := range left {
		diff |= left[i] ^ right[i]
	}
	return diff == 0
}
func randomID(prefix string) (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(value), nil
}

func validateSchema(ctx context.Context, tx *sql.Tx) error {
	tables := map[string]string{"financial_accounts": accountsDDL, "financial_operations": operationsDDL, "financial_entries": entriesDDL}
	for name, ddl := range tables {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil || kind != "table" || normalize(actual) != normalize(storedDDL(ddl)) {
			return ErrSchema
		}
	}
	indexes := map[string]string{"financial_accounts_owner_idx": accountOwnerIndexDDL, "financial_entries_account_idx": entryAccountIndexDDL, "financial_entries_resource_idx": entryResourceIndexDDL}
	for name, ddl := range indexes {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil || kind != "index" || normalize(actual) != normalize(storedDDL(ddl)) {
			return ErrSchema
		}
	}
	triggers := map[string]string{"financial_entries_no_update": entriesNoUpdateDDL, "financial_entries_no_delete": entriesNoDeleteDDL, "financial_operations_no_update": operationsNoUpdateDDL, "financial_operations_no_delete": operationsNoDeleteDDL, "financial_accounts_no_update": accountsNoUpdateDDL, "financial_accounts_no_delete": accountsNoDeleteDDL}
	for name, ddl := range triggers {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil || kind != "trigger" || normalize(actual) != normalize(storedDDL(ddl)) {
			return ErrSchema
		}
	}
	var explicitIndexes, triggerCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND sql IS NOT NULL AND name IN ('financial_accounts_owner_idx','financial_entries_account_idx','financial_entries_resource_idx')`).Scan(&explicitIndexes); err != nil || explicitIndexes != 3 {
		return ErrSchema
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name IN ('financial_entries_no_update','financial_entries_no_delete','financial_operations_no_update','financial_operations_no_delete','financial_accounts_no_update','financial_accounts_no_delete')`).Scan(&triggerCount); err != nil || triggerCount != 6 {
		return ErrSchema
	}
	return nil
}

func validateStored(ctx context.Context, tx *sql.Tx) error {
	accountRows, err := tx.QueryContext(ctx, `SELECT id,owner_kind,owner_key,employee_id,COALESCE(key_id,''),resource_kind,resource_id,currency,created_at FROM financial_accounts ORDER BY id`)
	if err != nil {
		return ErrUnavailable
	}
	type storedAccount struct {
		id, key, currency, created string
		owner                      Owner
	}
	accounts := make([]storedAccount, 0)
	for accountRows.Next() {
		var item storedAccount
		if err := accountRows.Scan(&item.id, &item.owner.Kind, &item.key, &item.owner.EmployeeID, &item.owner.KeyID, &item.owner.ResourceKind, &item.owner.ResourceID, &item.currency, &item.created); err != nil {
			accountRows.Close()
			return ErrUnavailable
		}
		accounts = append(accounts, item)
	}
	if err := accountRows.Err(); err != nil {
		accountRows.Close()
		return ErrUnavailable
	}
	if err := accountRows.Close(); err != nil {
		return ErrUnavailable
	}
	for _, item := range accounts {
		if !validText(item.id, 256) || !validOwnerShape(item.owner) || item.key != ownerKey(item.owner) || !validCurrency(item.currency) {
			return ErrSchema
		}
		if _, err := time.Parse(time.RFC3339Nano, item.created); err != nil {
			return ErrSchema
		}
		if _, err := resolveOwner(ctx, tx, item.owner); err != nil {
			return ErrSchema
		}
	}
	var mismatches int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM financial_operations o LEFT JOIN financial_entries e ON e.operation_id=o.operation_id WHERE e.id IS NULL`).Scan(&mismatches); err != nil || mismatches != 0 {
		return ErrSchema
	}
	rows, err := tx.QueryContext(ctx, `SELECT id FROM financial_accounts ORDER BY id`)
	if err != nil {
		return ErrUnavailable
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return ErrUnavailable
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ErrUnavailable
	}
	if err := rows.Close(); err != nil {
		return ErrUnavailable
	}
	for _, id := range ids {
		if _, err := accountBalance(ctx, tx, id); err != nil {
			return ErrSchema
		}
	}
	foreignRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return ErrUnavailable
	}
	violated := foreignRows.Next()
	iterationErr, closeErr := foreignRows.Err(), foreignRows.Close()
	if iterationErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if violated {
		return ErrSchema
	}
	return nil
}

func normalize(value string) string { return strings.ToLower(strings.Join(strings.Fields(value), "")) }
func storedDDL(value string) string { return strings.Replace(value, " IF NOT EXISTS", "", 1) }
