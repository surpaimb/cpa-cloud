package financial

// Independently authored acceptance tests for docs/financial-actor-provenance-contract.md.

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func installLegacyLedger(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ddl := range []string{
		accountsDDL, operationsLegacyDDL, entriesDDL,
		accountOwnerIndexDDL, entryAccountIndexDDL, entryResourceIndexDDL,
		accountsNoUpdateDDL, accountsNoDeleteDDL, operationsNoUpdateDDL, operationsNoDeleteDDL,
		entriesNoUpdateDDL, entriesNoDeleteDDL,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
}

func TestActorMigrationPreservesLegacyLedgerAndCommercialFacts(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	installLegacyLedger(t, db)
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	at := financialTestTime.Format(time.RFC3339Nano)
	if _, err := db.Exec(`INSERT INTO financial_accounts(id,owner_kind,owner_key,employee_id,resource_kind,resource_id,currency,created_at) VALUES('old-account','employee',?,'employee-one','','','USD',?)`, ownerKey(owner), at); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		operationID, entryID, adminID string
	}{
		{"old-admin", "old-admin-entry", "admin-one"},
		{"old-unknown", "old-unknown-entry", ""},
	} {
		input := Post{OperationID: row.operationID, Action: "adjustment", ActorAdminID: row.adminID, ResourceKind: "adjustment", ResourceID: row.operationID, ObservedAt: financialTestTime, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 100, ResourceKind: "adjustment", ResourceID: row.operationID}}}
		digest, err := postDigest(input)
		if err != nil {
			t.Fatal(err)
		}
		var admin any
		if row.adminID != "" {
			admin = row.adminID
		}
		if _, err := db.Exec(`INSERT INTO financial_operations(operation_id,action,actor_admin_id,resource_kind,resource_id,payload_digest,created_at) VALUES(?,'adjustment',?,'adjustment',?,?,?)`, row.operationID, admin, row.operationID, digest[:], at); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO financial_entries(id,operation_id,account_id,kind,amount_micro,resource_kind,resource_id,created_at) VALUES(?,?,'old-account','adjustment_credit',100,'adjustment',?,?)`, row.entryID, row.operationID, row.operationID, at); err != nil {
			t.Fatal(err)
		}
	}
	for _, ddl := range []string{commercialOperationsLegacyDDL, commercialOperationsNoUpdateDDL, commercialOperationsNoDeleteDDL} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO financial_commercial_operations(operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at) VALUES('old-commercial','plan.create','admin-one',zeroblob(32),'plan','old-plan',1,?)`, at); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO financial_commercial_operations(operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at) VALUES('old-commercial-unknown','plan.create',NULL,zeroblob(32),'plan','old-unknown-plan',1,?)`, at); err != nil {
		t.Fatal(err)
	}
	if err := NewLedger(db).Migrate(context.Background()); err != nil {
		t.Fatalf("actor migration: %v", err)
	}
	if err := NewCommercial(db).Migrate(context.Background()); err != nil {
		t.Fatalf("commercial restart: %v", err)
	}
	for _, row := range []struct{ id, kind, admin string }{{"old-admin", "admin", "admin-one"}, {"old-unknown", "legacy_unknown", ""}} {
		var kind string
		var admin sql.NullString
		var version int
		if err := db.QueryRow(`SELECT actor_kind,actor_admin_id,digest_version FROM financial_operations WHERE operation_id=?`, row.id).Scan(&kind, &admin, &version); err != nil || kind != row.kind || admin.String != row.admin || version != 1 {
			t.Fatalf("migrated actor %s: kind=%q admin=%v version=%d err=%v", row.id, kind, admin, version, err)
		}
	}
	var entryCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_entries WHERE id IN ('old-admin-entry','old-unknown-entry')`).Scan(&entryCount); err != nil || entryCount != 2 {
		t.Fatalf("entries=%d err=%v", entryCount, err)
	}
	if _, err := db.Exec(`INSERT INTO financial_operations(operation_id,action,actor_kind,resource_kind,resource_id,payload_digest,digest_version,created_at) VALUES('new-unknown','adjustment','legacy_unknown','adjustment','new-unknown',zeroblob(32),1,?)`, at); err == nil {
		t.Fatal("new unknown actor inserted")
	}
	if err := NewLedger(db).Migrate(context.Background()); err != nil {
		t.Fatalf("idempotent ledger restart: %v", err)
	}
	changed := Post{OperationID: "old-admin", Action: "adjustment", Actor: Actor{Kind: ActorEmployee, ID: "employee-one"}, ResourceKind: "adjustment", ResourceID: "old-admin", ObservedAt: financialTestTime, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 100, ResourceKind: "adjustment", ResourceID: "old-admin"}}}
	if _, err := NewLedger(db).Post(context.Background(), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed historical actor: %v", err)
	}
	legacy := Post{OperationID: "old-unknown", Action: "adjustment", ResourceKind: "adjustment", ResourceID: "old-unknown", ObservedAt: financialTestTime, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 100, ResourceKind: "adjustment", ResourceID: "old-unknown"}}}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	entries, err := NewLedger(db).replayLegacyUnknownPostTx(context.Background(), tx, legacy)
	if err != nil || len(entries) != 1 || entries[0].ID != "old-unknown-entry" {
		t.Fatalf("old ledger replay: entries=%+v err=%v", entries, err)
	}
	legacy.Entries[0].AmountMicro++
	if _, err := NewLedger(db).replayLegacyUnknownPostTx(context.Background(), tx, legacy); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed old ledger replay: %v", err)
	}
	legacy.OperationID = "new-operation"
	if _, err := NewLedger(db).replayLegacyUnknownPostTx(context.Background(), tx, legacy); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing old ledger replay: %v", err)
	}
	legacy.OperationID = "old-unknown"
	legacy.Actor = Actor{Kind: ActorAdmin, ID: "admin-one"}
	if _, err := NewLedger(db).replayLegacyUnknownPostTx(context.Background(), tx, legacy); !errors.Is(err, ErrInvalid) {
		t.Fatalf("actor-injected old ledger replay: %v", err)
	}
	var zeroDigest [32]byte
	receipt, err := replayLegacyUnknownCommercialTx(context.Background(), tx, "old-commercial-unknown", "plan.create", zeroDigest)
	if err != nil || !receipt.Replay || receipt.ResourceID != "old-unknown-plan" {
		t.Fatalf("old commercial replay: receipt=%+v err=%v", receipt, err)
	}
	if _, err := replayLegacyUnknownCommercialTx(context.Background(), tx, "old-commercial", "plan.create", zeroDigest); !errors.Is(err, ErrConflict) {
		t.Fatalf("admin receipt through unknown replay: %v", err)
	}
	if _, err := replayLegacyUnknownCommercialTx(context.Background(), tx, "missing-commercial", "plan.create", zeroDigest); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing old commercial replay: %v", err)
	}
}

func TestActorMigrationSerializesExternalWriterAndRestoresForeignKeys(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	var sequence int
	var name, path string
	if err := db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil || name != "main" || path == "" {
		t.Fatalf("database path: %q %v", path, err)
	}
	other, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(100)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	tx, err := beginActorWriteTx(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Exec(`INSERT INTO admins(id) VALUES('concurrent-writer')`); err == nil {
		t.Fatal("external writer entered actor migration transaction")
	}
	if err := rebuildActorTables(ctx, tx, false); !errors.Is(err, ErrSchema) {
		t.Fatalf("expected injected missing-schema failure, got %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := restoreActorForeignKeys(db, conn); err != nil {
		t.Fatalf("restore foreign keys: %v", err)
	}
	var enabled, lockObjects int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil || enabled != 1 {
		t.Fatalf("foreign keys=%d err=%v", enabled, err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='financial_actor_migration_write_lock'`).Scan(&lockObjects); err != nil || lockObjects != 0 {
		t.Fatalf("transient lock objects=%d err=%v", lockObjects, err)
	}
	if _, err := other.Exec(`INSERT INTO admins(id) VALUES('after-rollback')`); err != nil {
		t.Fatalf("external writer did not recover: %v", err)
	}
}

func TestActorNewWritesEnforceIdentityShapeAndCrossTableConsistency(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ctx := context.Background()
	ledger := NewLedger(db)
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := NewCommercial(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	post := Post{OperationID: "employee-operation", Action: "adjustment", Actor: Actor{Kind: ActorEmployee, ID: "employee-two"}, ResourceKind: "adjustment", ResourceID: "employee-operation", ObservedAt: financialTestTime, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 7, ResourceKind: "adjustment", ResourceID: "employee-operation"}}}
	if _, err := ledger.Post(ctx, post); err != nil {
		t.Fatalf("employee post: %v", err)
	}
	var kind, actorID string
	var version int
	if err := db.QueryRow(`SELECT actor_kind,actor_employee_id,digest_version FROM financial_operations WHERE operation_id=?`, post.OperationID).Scan(&kind, &actorID, &version); err != nil || kind != "employee" || actorID != "employee-two" || version != 2 {
		t.Fatalf("employee actor kind=%q id=%q version=%d err=%v", kind, actorID, version, err)
	}
	if _, err := db.Exec(`DELETE FROM employees WHERE id='employee-two'`); err == nil {
		t.Fatal("actor employee foreign key did not restrict deletion")
	}
	for name, statement := range map[string]string{
		"mixed IDs":       `INSERT INTO financial_operations(operation_id,action,actor_kind,actor_admin_id,actor_employee_id,resource_kind,resource_id,payload_digest,digest_version,created_at) VALUES('mixed','adjustment','admin','admin-one','employee-one','adjustment','mixed',zeroblob(32),2,'2026-01-01T00:00:00Z')`,
		"empty ID":        `INSERT INTO financial_operations(operation_id,action,actor_kind,actor_admin_id,resource_kind,resource_id,payload_digest,digest_version,created_at) VALUES('empty','adjustment','admin','','adjustment','empty',zeroblob(32),2,'2026-01-01T00:00:00Z')`,
		"wrong system ID": `INSERT INTO financial_operations(operation_id,action,actor_kind,actor_system_id,resource_kind,resource_id,payload_digest,digest_version,created_at) VALUES('system-wrong','payment_callback','system','subscription_one_shot_worker','topup','topup-1',zeroblob(32),2,'2026-01-01T00:00:00Z')`,
		"new unknown":     `INSERT INTO financial_operations(operation_id,action,actor_kind,resource_kind,resource_id,payload_digest,digest_version,created_at) VALUES('unknown','adjustment','legacy_unknown','adjustment','unknown',zeroblob(32),1,'2026-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(statement); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
	if _, err := ledger.Post(ctx, Post{OperationID: "no-actor", Action: "adjustment", ResourceKind: "adjustment", ResourceID: "no-actor", ObservedAt: financialTestTime, Entries: post.Entries}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("new missing actor: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO financial_commercial_operations(operation_id,action,actor_kind,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at) VALUES('employee-operation','plan.create','admin','admin-one',zeroblob(32),'plan','plan-1',1,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateActorStored(ctx, tx); !errors.Is(err, ErrSchema) {
		t.Fatalf("cross-table actor mismatch: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func TestActorMigrationFromLinkedLegacyCommercialGeneration(t *testing.T) {
	c, _, due, _ := renewalFixture(t)
	db := c.db
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	input := renewalInput(t, "renew-old", "linked-old-renewal", due)
	successor, _, err := c.RenewSubscription(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	owner := Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}
	seedDigest, err := postDigest(Post{OperationID: "expiry-seed", Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "adjustment", ResourceID: "expiry-seed", Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 1000, ResourceKind: "adjustment", ResourceID: "expiry-seed"}}})
	if err != nil {
		t.Fatal(err)
	}
	renewDigest, err := postDigest(Post{OperationID: input.Meta.OperationID, Action: "subscription_purchase", ActorAdminID: "admin-one", ResourceKind: "subscription", ResourceID: successor.ID, RequireNonNegative: true, Entries: []EntryInput{{Owner: owner, Currency: successor.Currency, Kind: EntrySubscriptionCharge, AmountMicro: -successor.PriceMicro, ResourceKind: "subscription", ResourceID: successor.ID}, {Owner: owner, Currency: successor.Currency, Kind: EntrySubscriptionCredit, AmountMicro: successor.CreditMicro, ResourceKind: "subscription", ResourceID: successor.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	// Build a recognized L1/C2 snapshot with a populated immutable renewal
	// link. The v1 digests are independently reconstructed from the test's
	// original business inputs, not copied from the newer v2 rows.
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = tx.Rollback()
		_, _ = db.Exec(`PRAGMA foreign_keys=ON`)
	}()
	statements := []string{
		`CREATE TEMP TABLE old_ledger_rows AS SELECT rowid AS old_rowid,operation_id,action,actor_admin_id,resource_kind,resource_id,created_at FROM financial_operations`,
		`CREATE TEMP TABLE old_commercial_rows AS SELECT rowid AS old_rowid,operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at FROM financial_commercial_operations`,
		`CREATE TEMP TABLE old_renewal_links AS SELECT predecessor_id,successor_id,operation_id,created_at FROM financial_subscription_renewals`,
		`DROP TABLE financial_subscription_one_shot_renewals`,
		`DROP TABLE financial_subscription_renewals`,
		`DROP TABLE financial_commercial_operations`,
		commercialOperationsBeforeOneShotDDL,
		`INSERT INTO financial_commercial_operations(rowid,operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at) SELECT old_rowid,operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at FROM old_commercial_rows`,
		commercialOperationsNoUpdateDDL, commercialOperationsNoDeleteDDL,
		subscriptionRenewalsDDL,
		`INSERT INTO financial_subscription_renewals SELECT predecessor_id,successor_id,operation_id,created_at FROM old_renewal_links`,
		subscriptionRenewalsNoUpdateDDL, subscriptionRenewalsNoDeleteDDL,
		`DROP TABLE old_commercial_rows`, `DROP TABLE old_renewal_links`,
		`DROP TABLE financial_operations`,
		operationsLegacyDDL,
		`INSERT INTO financial_operations(rowid,operation_id,action,actor_admin_id,resource_kind,resource_id,payload_digest,created_at) SELECT old_rowid,operation_id,action,actor_admin_id,resource_kind,resource_id,CASE operation_id WHEN 'expiry-seed' THEN ? ELSE ? END,created_at FROM old_ledger_rows`,
		operationsNoUpdateDDL, operationsNoDeleteDDL,
		`DROP TABLE old_ledger_rows`,
	}
	for _, statement := range statements {
		var execErr error
		if statement == statements[len(statements)-4] {
			_, execErr = tx.ExecContext(ctx, statement, seedDigest[:], renewDigest[:])
		} else {
			_, execErr = tx.ExecContext(ctx, statement)
		}
		if execErr != nil {
			t.Fatalf("construct linked legacy schema: %v\n%s", execErr, statement)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	if err := NewCommercial(db).Migrate(ctx); err != nil {
		t.Fatalf("linked L1/C2 actor migration: %v", err)
	}
	var linkSuccessor string
	if err := db.QueryRow(`SELECT successor_id FROM financial_subscription_renewals WHERE operation_id=?`, input.Meta.OperationID).Scan(&linkSuccessor); err != nil || linkSuccessor != successor.ID {
		t.Fatalf("linked successor=%q err=%v", linkSuccessor, err)
	}
	var storedDigest []byte
	var version int
	if err := db.QueryRow(`SELECT payload_digest,digest_version FROM financial_operations WHERE operation_id=?`, input.Meta.OperationID).Scan(&storedDigest, &version); err != nil || !equalBytes(storedDigest, renewDigest[:]) || version != 1 {
		t.Fatalf("old renewal digest/version preserved: version=%d err=%v", version, err)
	}
	if _, receipt, err := c.RenewSubscription(ctx, input); err != nil || !receipt.Replay {
		t.Fatalf("linked renewal replay after migration: receipt=%+v err=%v", receipt, err)
	}
}
