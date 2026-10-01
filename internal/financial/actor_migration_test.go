package financial

// Independently authored acceptance tests for docs/financial-actor-provenance-contract.md.

import (
	"context"
	"crypto/sha256"
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

// downgradeActorFixtureToL1C3 models a persisted pre-provenance database with
// the real payment/renewal dependency chain left in place. The caller supplies
// independently reconstructed v1 digests for every ledger operation.
func downgradeActorFixtureToL1C3(t *testing.T, db *sql.DB, legacyDigests map[string][32]byte) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
			t.Errorf("restore fixture foreign keys: %v", err)
		}
	}()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, statement := range []string{
		`CREATE TEMP TABLE prior_actor_ledger AS SELECT rowid AS old_rowid,operation_id,action,actor_kind,actor_admin_id,resource_kind,resource_id,created_at FROM financial_operations`,
		`CREATE TEMP TABLE prior_actor_commercial AS SELECT rowid AS old_rowid,operation_id,action,actor_kind,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at FROM financial_commercial_operations`,
		`CREATE TEMP TABLE prior_actor_links AS SELECT * FROM financial_subscription_renewals`,
		`CREATE TEMP TABLE prior_actor_one_shot AS SELECT * FROM financial_subscription_one_shot_renewals`,
		`DROP TABLE financial_subscription_one_shot_renewals`,
		`DROP TABLE financial_subscription_renewals`,
		`DROP TABLE financial_commercial_operations`,
		commercialOperationsBeforeActorDDL,
		`INSERT INTO financial_commercial_operations(rowid,operation_id,action,actor_admin_id,payload_digest,resource_kind,resource_id,revision,created_at) SELECT old_rowid,operation_id,action,CASE WHEN actor_kind='admin' THEN actor_admin_id ELSE NULL END,payload_digest,resource_kind,resource_id,revision,created_at FROM prior_actor_commercial`,
		commercialOperationsNoUpdateDDL, commercialOperationsNoDeleteDDL,
		subscriptionRenewalsDDL,
		`INSERT INTO financial_subscription_renewals SELECT * FROM prior_actor_links`,
		subscriptionRenewalsNoUpdateDDL, subscriptionRenewalsNoDeleteDDL,
		oneShotRenewalsDDL,
		`INSERT INTO financial_subscription_one_shot_renewals SELECT * FROM prior_actor_one_shot`,
		oneShotRenewalsDueIndexDDL, oneShotRenewalsImmutableUpdateDDL, oneShotRenewalsNoDeleteDDL,
		`DROP TABLE prior_actor_commercial`, `DROP TABLE prior_actor_links`, `DROP TABLE prior_actor_one_shot`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("construct C3 actor fixture: %v\n%s", err, statement)
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT old_rowid,operation_id,action,actor_kind,actor_admin_id,resource_kind,resource_id,created_at FROM prior_actor_ledger`)
	if err != nil {
		t.Fatal(err)
	}
	type oldLedgerRow struct {
		rowID                                       int64
		operationID, action, kind, resource, id, at string
		admin                                       sql.NullString
	}
	var oldRows []oldLedgerRow
	for rows.Next() {
		var row oldLedgerRow
		if err := rows.Scan(&row.rowID, &row.operationID, &row.action, &row.kind, &row.admin, &row.resource, &row.id, &row.at); err != nil {
			t.Fatal(err)
		}
		oldRows = append(oldRows, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if len(oldRows) != len(legacyDigests) {
		t.Fatalf("v1 digest fixture count=%d operations=%d", len(legacyDigests), len(oldRows))
	}
	for _, statement := range []string{`DROP TABLE financial_operations`, operationsLegacyDDL} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range oldRows {
		digest, ok := legacyDigests[row.operationID]
		if !ok {
			t.Fatalf("missing v1 digest for %s", row.operationID)
		}
		var admin any
		if row.kind == "admin" {
			if !row.admin.Valid {
				t.Fatalf("admin actor missing ID on %s", row.operationID)
			}
			admin = row.admin.String
		} else if row.kind != "system" {
			t.Fatalf("unexpected new actor %q on %s", row.kind, row.operationID)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO financial_operations(rowid,operation_id,action,actor_admin_id,resource_kind,resource_id,payload_digest,created_at) VALUES(?,?,?,?,?,?,?,?)`, row.rowID, row.operationID, row.action, admin, row.resource, row.id, digest[:], row.at); err != nil {
			t.Fatal(err)
		}
	}
	for _, statement := range []string{operationsNoUpdateDDL, operationsNoDeleteDDL, `DROP TABLE prior_actor_ledger`} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}
	check, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	violated := check.Next()
	if err := check.Close(); err != nil || violated {
		t.Fatalf("legacy fixture foreign keys: violated=%v err=%v", violated, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyNullPaymentCallbackRetryAfterActorMigration(t *testing.T) {
	db := openFinancialTestDB(t)
	defer db.Close()
	ctx := context.Background()
	ledger, commercial := NewLedger(db), NewCommercial(db)
	if err := ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := commercial.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	owner := Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}
	connector, _, err := commercial.CreateConnector(ctx, CreateConnector{Meta: testCommercialMeta(t, "old-callback-connector", "connector", financialTestTime), Name: "Legacy callback fixture", SecretCiphertext: []byte("synthetic"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.SetEnabled(ctx, testCommercialMeta(t, "old-callback-enable", "enable", financialTestTime), 1, true); err != nil {
		t.Fatal(err)
	}
	topup, _, err := commercial.CreateTopUp(ctx, CreateTopUp{Meta: testCommercialMeta(t, "old-callback-topup", "topup", financialTestTime), Owner: owner, ConnectorID: connector.ID, Currency: "USD", AmountMicro: 17})
	if err != nil {
		t.Fatal(err)
	}
	callback := ApplyPayment{ConnectorID: connector.ID, EventID: "old-paid-event", PaymentID: topup.PaymentID, ExternalReference: topup.ExternalReference, Currency: "USD", AmountMicro: 17, PayloadDigest: sha256.Sum256([]byte("old-paid-payload")), SignedAt: financialTestTime.Add(time.Second), ObservedAt: financialTestTime.Add(time.Second)}
	paid, err := commercial.ApplyPaid(ctx, callback)
	if err != nil || paid.PaidEntryID == "" {
		t.Fatalf("initial callback: paid=%+v err=%v", paid, err)
	}
	operationID := "webhook:" + connector.ID + ":" + callback.EventID
	digest, err := postDigest(Post{OperationID: operationID, Action: "payment_callback", ResourceKind: "topup", ResourceID: topup.ID, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryTopUp, AmountMicro: 17, ResourceKind: "topup", ResourceID: topup.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	downgradeActorFixtureToL1C3(t, db, map[string][32]byte{operationID: digest})
	if err := NewCommercial(db).Migrate(ctx); err != nil {
		t.Fatalf("callback L1/C3 migration: %v", err)
	}
	var kind string
	if err := db.QueryRow(`SELECT actor_kind FROM financial_operations WHERE operation_id=?`, operationID).Scan(&kind); err != nil || kind != "legacy_unknown" {
		t.Fatalf("historical callback actor=%q err=%v", kind, err)
	}
	readTx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	legacyPost := Post{OperationID: operationID, Action: "payment_callback", ResourceKind: "topup", ResourceID: topup.ID, ObservedAt: callback.ObservedAt, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryTopUp, AmountMicro: 17, ResourceKind: "topup", ResourceID: topup.ID}}}
	legacyEntries, err := ledger.replayLegacyUnknownPostTx(ctx, readTx, legacyPost)
	if err != nil || len(legacyEntries) != 1 || legacyEntries[0].ID != paid.PaidEntryID {
		t.Fatalf("historical callback private replay: entries=%+v err=%v", legacyEntries, err)
	}
	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// Commit-unknown is simulated by losing the first success response. The
	// durable webhook event is checked before the normal path calls PostTx.
	replayed, err := NewCommercial(db).ApplyPaid(ctx, callback)
	if err != nil || replayed.PaidEntryID != paid.PaidEntryID {
		t.Fatalf("historical callback retry: replayed=%+v err=%v", replayed, err)
	}
	var entries int
	if err := db.QueryRow(`SELECT COUNT(*) FROM financial_entries WHERE operation_id=?`, operationID).Scan(&entries); err != nil || entries != 1 {
		t.Fatalf("historical callback entry count=%d err=%v", entries, err)
	}
	if balance, err := ledger.Balance(ctx, owner, "USD"); err != nil || balance.AmountMicro != 17 {
		t.Fatalf("historical callback balance=%+v err=%v", balance, err)
	}
	if _, err := ledger.Post(ctx, Post{OperationID: operationID, Action: "payment_callback", Actor: Actor{Kind: ActorSystem, ID: "payment_callback"}, ResourceKind: "topup", ResourceID: topup.ID, ObservedAt: callback.ObservedAt, Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryTopUp, AmountMicro: 17, ResourceKind: "topup", ResourceID: topup.ID}}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("normal PostTx must not reclassify old callback: %v", err)
	}
}

func TestLegacyNullOneShotWorkerRetryAfterActorMigration(t *testing.T) {
	c, ledger, due, _ := renewalFixture(t)
	ctx := context.Background()
	if _, err := c.db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return due.Add(-time.Second) }
	if _, _, err := c.ArmOneShotRenewal(ctx, oneShotArmInput(t, "renew-old", "legacy-worker-arm", 1, due)); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return due }
	if count, err := c.ProcessDueOneShotRenewals(ctx, due); err != nil || count != 1 {
		t.Fatalf("initial worker count=%d err=%v", count, err)
	}
	var executionID, successorID string
	if err := c.db.QueryRow(`SELECT execution_operation_id,successor_id FROM financial_subscription_one_shot_renewals WHERE predecessor_id='renew-old'`).Scan(&executionID, &successorID); err != nil {
		t.Fatal(err)
	}
	successor, err := c.GetSubscription(ctx, successorID)
	if err != nil {
		t.Fatal(err)
	}
	owner := Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}
	seedDigest, err := postDigest(Post{OperationID: "expiry-seed", Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "adjustment", ResourceID: "expiry-seed", Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 1000, ResourceKind: "adjustment", ResourceID: "expiry-seed"}}})
	if err != nil {
		t.Fatal(err)
	}
	workerPost := Post{OperationID: executionID, Action: "subscription_purchase", ResourceKind: "subscription", ResourceID: successorID, ObservedAt: due, RequireNonNegative: true, Entries: []EntryInput{{Owner: owner, Currency: successor.Currency, Kind: EntrySubscriptionCharge, AmountMicro: -successor.PriceMicro, ResourceKind: "subscription", ResourceID: successorID}, {Owner: owner, Currency: successor.Currency, Kind: EntrySubscriptionCredit, AmountMicro: successor.CreditMicro, ResourceKind: "subscription", ResourceID: successorID}}}
	workerDigest, err := postDigest(workerPost)
	if err != nil {
		t.Fatal(err)
	}
	downgradeActorFixtureToL1C3(t, c.db, map[string][32]byte{"expiry-seed": seedDigest, executionID: workerDigest})
	if err := NewCommercial(c.db).Migrate(ctx); err != nil {
		t.Fatalf("worker L1/C3 migration: %v", err)
	}
	for _, table := range []string{"financial_operations", "financial_commercial_operations"} {
		var kind string
		if err := c.db.QueryRow(`SELECT actor_kind FROM `+table+` WHERE operation_id=?`, executionID).Scan(&kind); err != nil || kind != "legacy_unknown" {
			t.Fatalf("historical worker %s actor=%q err=%v", table, kind, err)
		}
	}
	readTx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	legacyEntries, err := ledger.replayLegacyUnknownPostTx(ctx, readTx, workerPost)
	if err != nil || len(legacyEntries) != 2 {
		t.Fatalf("historical worker private ledger replay: entries=%+v err=%v", legacyEntries, err)
	}
	var receiptBytes []byte
	if err := readTx.QueryRowContext(ctx, `SELECT payload_digest FROM financial_commercial_operations WHERE operation_id=?`, executionID).Scan(&receiptBytes); err != nil || len(receiptBytes) != 32 {
		t.Fatalf("historical worker receipt digest length=%d err=%v", len(receiptBytes), err)
	}
	var receiptDigest [32]byte
	copy(receiptDigest[:], receiptBytes)
	if receipt, err := replayLegacyUnknownCommercialTx(ctx, readTx, executionID, "subscription.renew", receiptDigest); err != nil || !receipt.Replay || receipt.ResourceID != successorID {
		t.Fatalf("historical worker private commercial replay: receipt=%+v err=%v", receipt, err)
	}
	if err := readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// The succeeded reservation is durable in the same transaction as its
	// receipt and entries, so the due-only retry never reaches PostTx.
	if count, err := NewCommercial(c.db).ProcessDueOneShotRenewals(ctx, due.Add(time.Hour)); err != nil || count != 0 {
		t.Fatalf("historical worker retry count=%d err=%v", count, err)
	}
	if state, err := c.OneShotRenewal(ctx, "renew-old"); err != nil || state.State != "succeeded" || state.SuccessorID != successorID {
		t.Fatalf("historical worker state=%+v err=%v", state, err)
	}
	var entries int
	if err := c.db.QueryRow(`SELECT COUNT(*) FROM financial_entries WHERE operation_id=?`, executionID).Scan(&entries); err != nil || entries != 2 {
		t.Fatalf("historical worker entry count=%d err=%v", entries, err)
	}
	if balance, err := ledger.Balance(ctx, owner, "USD"); err != nil || balance.AmountMicro != 1010 {
		t.Fatalf("historical worker balance=%+v err=%v", balance, err)
	}
	if _, err := ledger.Post(ctx, Post{OperationID: workerPost.OperationID, Action: workerPost.Action, Actor: Actor{Kind: ActorSystem, ID: "subscription_one_shot_worker"}, ResourceKind: workerPost.ResourceKind, ResourceID: workerPost.ResourceID, ObservedAt: due, RequireNonNegative: true, Entries: workerPost.Entries}); !errors.Is(err, ErrConflict) {
		t.Fatalf("normal PostTx must not reclassify old worker: %v", err)
	}
}
