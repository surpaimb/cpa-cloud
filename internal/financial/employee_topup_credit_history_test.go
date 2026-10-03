// Independently authored synthetic tests for docs/employee-self-topup-credit-history-contract.md.
package financial

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func topupCreditQuery(employee string, limit int) EmployeeActivityQuery {
	end := financialTestTime.Add(24 * time.Hour)
	return EmployeeActivityQuery{EmployeeID: employee, Currency: "USD", WindowStart: end.Add(-31 * 24 * time.Hour), WindowEnd: end, Limit: limit}
}

func newTopupCreditFinancialFixture(t *testing.T) (*sql.DB, *Commercial, string) {
	t.Helper()
	db := openFinancialTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	if err := NewLedger(db).Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	commercial := NewCommercial(db)
	if err := commercial.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	connector, _, err := commercial.CreateConnector(ctx, CreateConnector{Meta: testCommercialMeta(t, "topup-history-connector", "connector", financialTestTime),
		Name: "Synthetic", SecretCiphertext: []byte("synthetic-ciphertext"), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := commercial.SetEnabled(ctx, testCommercialMeta(t, "topup-history-enable", "enable", financialTestTime), 1, true); err != nil {
		t.Fatal(err)
	}
	return db, commercial, connector.ID
}

func postTopupCredit(t *testing.T, commercial *Commercial, connectorID string, owner Owner, suffix string, amount int64, at time.Time) TopUp {
	t.Helper()
	ctx := context.Background()
	topup, _, err := commercial.CreateTopUp(ctx, CreateTopUp{
		Meta:  testCommercialMeta(t, "topup-create-"+suffix, suffix, at.Add(-time.Second)),
		Owner: owner, ConnectorID: connectorID, Currency: "USD", AmountMicro: amount,
	})
	if err != nil {
		t.Fatal(err)
	}
	event := ApplyPayment{ConnectorID: connectorID, EventID: "event-" + suffix, PaymentID: topup.PaymentID,
		ExternalReference: topup.ExternalReference, Currency: "USD", AmountMicro: amount,
		PayloadDigest: sha256.Sum256([]byte("synthetic-body-" + suffix)), SignedAt: at.Add(-time.Second), ObservedAt: at}
	paid, err := commercial.ApplyPaid(ctx, event)
	if err != nil || paid.PaidEntryID == "" {
		t.Fatalf("paid=%+v err=%v", paid, err)
	}
	return paid
}

func TestEmployeeTopupCreditHistoryGrossAndIsolation(t *testing.T) {
	db, commercial, connector := newTopupCreditFinancialFixture(t)
	ledger := NewLedger(db)
	query := topupCreditQuery("employee-one", 20)
	page, err := ledger.ReadEmployeeTopupCredits(context.Background(), query)
	if err != nil || page.HasAccount || len(page.Items) != 0 {
		t.Fatalf("missing page=%+v err=%v", page, err)
	}
	if _, err := ledger.Post(context.Background(), Post{OperationID: "older-topup-action", Action: "topup", ActorAdminID: "admin-one",
		ResourceKind: "topup", ResourceID: "older-topup-action", ObservedAt: financialTestTime.Add(2 * time.Second),
		Entries: []EntryInput{{Owner: Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, Currency: "USD", Kind: EntryTopUp,
			AmountMicro: 99, ResourceKind: "topup", ResourceID: "older-topup-action"}}}); err != nil {
		t.Fatal(err)
	}
	oldOnly, err := ledger.ReadEmployeeTopupCredits(context.Background(), query)
	if err != nil || !oldOnly.HasAccount || len(oldOnly.Items) != 0 {
		t.Fatalf("non-callback topup classified as callback credit: page=%+v err=%v", oldOnly, err)
	}
	first := postTopupCredit(t, commercial, connector, Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, "own", 42, financialTestTime.Add(3*time.Second))
	postTopupCredit(t, commercial, connector, Owner{Kind: OwnerEmployee, EmployeeID: "employee-two"}, "other", 77, financialTestTime.Add(4*time.Second))
	postTopupCredit(t, commercial, connector, Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}, "key", 99, financialTestTime.Add(5*time.Second))
	postTopupCredit(t, commercial, connector, Owner{Kind: OwnerResource, EmployeeID: "employee-one", KeyID: "key-one", ResourceKind: "response", ResourceID: "one"}, "resource", 105, financialTestTime.Add(6*time.Second))
	page, err = ledger.ReadEmployeeTopupCredits(context.Background(), query)
	if err != nil || !page.HasAccount || len(page.Items) != 1 || page.Items[0].AmountMicro != 42 {
		t.Fatalf("own page=%+v err=%v", page, err)
	}
	if _, _, err := commercial.RefundPayment(context.Background(), CreateRefund{
		Meta:      testCommercialMeta(t, "refund-own", "refund", financialTestTime.Add(7*time.Second)),
		PaymentID: first.PaymentID, AmountMicro: 42,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=0 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE financial_payment_connectors SET enabled=0 WHERE id=?`, connector); err != nil {
		t.Fatal(err)
	}
	var before, after int64
	if err := db.QueryRow(`SELECT total_changes()`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	retained, err := ledger.ReadEmployeeTopupCredits(context.Background(), query)
	if scanErr := db.QueryRow(`SELECT total_changes()`).Scan(&after); scanErr != nil {
		t.Fatal(scanErr)
	}
	if err != nil || len(retained.Items) != 1 || retained.Items[0].AmountMicro != 42 {
		t.Fatalf("gross after refund/off=%+v err=%v", retained, err)
	}
	if before != after {
		t.Fatalf("read wrote financial database: before=%d after=%d", before, after)
	}
}

func TestEmployeeTopupCreditHistoryValidatesLookaheadAndFaults(t *testing.T) {
	db, commercial, connector := newTopupCreditFinancialFixture(t)
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	// Mixed-precision timestamps in one second follow SQLite stored-key order,
	// not the underlying nanosecond time order.
	older := postTopupCredit(t, commercial, connector, owner, "older", 41, financialTestTime.Add(3*time.Second+time.Nanosecond))
	newer := postTopupCredit(t, commercial, connector, owner, "newer", 42, financialTestTime.Add(3*time.Second))
	query := topupCreditQuery("employee-one", 1)
	ledger := NewLedger(db)
	first, err := ledger.ReadEmployeeTopupCredits(context.Background(), query)
	if err != nil || len(first.Items) != 1 || first.Items[0].AmountMicro != 42 || first.NextPosition == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	query.BeforeTime, query.BeforeID = first.NextPosition.Time, first.NextPosition.ID
	second, err := ledger.ReadEmployeeTopupCredits(context.Background(), query)
	if err != nil || len(second.Items) != 1 || second.Items[0].AmountMicro != 41 || second.NextPosition != nil {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	query.BeforeTime, query.BeforeID = "", ""
	if _, err := db.Exec(`UPDATE financial_payments SET paid_entry_id=? WHERE id=?`, newer.PaidEntryID, older.PaymentID); err != nil {
		t.Fatal(err)
	}
	if page, err := ledger.ReadEmployeeTopupCredits(context.Background(), query); !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 {
		t.Fatalf("broken lookahead leaked page=%+v err=%v", page, err)
	}
	if _, err := db.Exec(`UPDATE financial_payments SET paid_entry_id=? WHERE id=?`, older.PaidEntryID, older.PaymentID); err != nil {
		t.Fatal(err)
	}
	for name, hooks := range map[string]employeeTopupCreditReadHooks{
		"scan":      {scan: func() error { return errors.New("scan fault") }},
		"iteration": {iteration: func() error { return errors.New("iteration fault") }},
		"close":     {closeRows: func(rows *sql.Rows) error { _ = rows.Close(); return errors.New("close fault") }},
		"commit":    {commit: func(*sql.Tx) error { return errors.New("commit fault") }},
	} {
		t.Run(name, func(t *testing.T) {
			if page, err := ledger.readEmployeeTopupCredits(context.Background(), query, hooks); !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 {
				t.Fatalf("fault leaked page=%+v err=%v", page, err)
			}
		})
	}
}

func TestEmployeeTopupCreditHistoryRejectsBrokenSelectedProof(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*testing.T, *sql.DB, TopUp)
	}{
		{"entry-kind", func(t *testing.T, db *sql.DB, paid TopUp) {
			mutateTopupImmutable(t, db, "financial_entries_no_update", entriesNoUpdateDDL,
				`UPDATE financial_entries SET kind='adjustment_credit' WHERE id=?`, paid.PaidEntryID)
		}},
		{"operation-v2-digest", func(t *testing.T, db *sql.DB, paid TopUp) {
			mutateTopupImmutable(t, db, "financial_operations_no_update", operationsNoUpdateDDL,
				`UPDATE financial_operations SET payload_digest=zeroblob(32) WHERE operation_id=(SELECT operation_id FROM financial_entries WHERE id=?)`, paid.PaidEntryID)
		}},
		{"operation-extra-entry", func(t *testing.T, db *sql.DB, paid TopUp) {
			if _, err := db.Exec(`INSERT INTO financial_entries(id,operation_id,account_id,kind,amount_micro,original_entry_id,resource_kind,resource_id,created_at)
				SELECT 'extra-callback-entry',operation_id,account_id,'adjustment_credit',1,NULL,resource_kind,resource_id,'2020-01-01T00:00:00Z'
				FROM financial_entries WHERE id=?`, paid.PaidEntryID); err != nil {
				t.Fatal(err)
			}
		}},
		{"topup-created-time", func(t *testing.T, db *sql.DB, paid TopUp) {
			mutateTopupImmutable(t, db, "financial_topups_no_update", topupsNoUpdateDDL,
				`UPDATE financial_topups SET created_at='2026-01-01T00:00:00Z' WHERE payment_id=?`, paid.PaymentID)
		}},
		{"event-signed-too-early", func(t *testing.T, db *sql.DB, paid TopUp) {
			mutateTopupImmutable(t, db, "financial_webhook_events_no_update", webhookEventsNoUpdateDDL,
				`UPDATE financial_webhook_events SET signed_at='2020-01-01T00:00:00Z' WHERE payment_id=?`, paid.PaymentID)
		}},
		{"event-processed-mismatch", func(t *testing.T, db *sql.DB, paid TopUp) {
			mutateTopupImmutable(t, db, "financial_webhook_events_no_update", webhookEventsNoUpdateDDL,
				`UPDATE financial_webhook_events SET processed_at='2026-01-01T00:00:00Z' WHERE payment_id=?`, paid.PaymentID)
		}},
		{"event-wrong-payment", func(t *testing.T, db *sql.DB, paid TopUp) {
			if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
				t.Fatal(err)
			}
			mutateTopupImmutable(t, db, "financial_webhook_events_no_update", webhookEventsNoUpdateDDL,
				`UPDATE financial_webhook_events SET payment_id='foreign-payment' WHERE payment_id=?`, paid.PaymentID)
			if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
				t.Fatal(err)
			}
		}},
		{"refund-state-mismatch", func(t *testing.T, db *sql.DB, paid TopUp) {
			if _, err := db.Exec(`UPDATE financial_payments SET status='refunded' WHERE id=?`, paid.PaymentID); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, commercial, connector := newTopupCreditFinancialFixture(t)
			paid := postTopupCredit(t, commercial, connector, Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, tc.name, 42, financialTestTime.Add(3*time.Second))
			tc.mutate(t, db, paid)
			page, err := NewLedger(db).ReadEmployeeTopupCredits(context.Background(), topupCreditQuery("employee-one", 20))
			if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 {
				t.Fatalf("broken proof exposed page=%+v err=%v", page, err)
			}
		})
	}
}

func mutateTopupImmutable(t *testing.T, db *sql.DB, trigger, restore, update string, args ...any) {
	t.Helper()
	if _, err := db.Exec(`DROP TRIGGER ` + trigger); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(update, args...); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(restore); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeTopupCreditHistorySchemaAndCancellationFailClosed(t *testing.T) {
	for _, mode := range []string{"schema", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			db, commercial, connector := newTopupCreditFinancialFixture(t)
			postTopupCredit(t, commercial, connector, Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, mode, 42, financialTestTime.Add(3*time.Second))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			hooks := employeeTopupCreditReadHooks{}
			if mode == "schema" {
				if _, err := db.Exec(`DROP TRIGGER financial_webhook_events_no_update`); err != nil {
					t.Fatal(err)
				}
			} else {
				hooks.afterAccount = cancel
			}
			page, err := NewLedger(db).readEmployeeTopupCredits(ctx, topupCreditQuery("employee-one", 20), hooks)
			if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 {
				t.Fatalf("%s exposed page=%+v err=%v", mode, page, err)
			}
		})
	}
}

func TestEmployeeTopupCreditHistoryOrphanOperationDoesNotBecomeEmptyPage(t *testing.T) {
	db, commercial, connector := newTopupCreditFinancialFixture(t)
	paid := postTopupCredit(t, commercial, connector, Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, "orphan", 42, financialTestTime.Add(3*time.Second))
	for _, statement := range []string{`PRAGMA foreign_keys=OFF`, `DROP TRIGGER financial_operations_no_delete`} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`DELETE FROM financial_operations WHERE operation_id=(SELECT operation_id FROM financial_entries WHERE id=?)`, paid.PaidEntryID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(operationsNoDeleteDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	page, err := NewLedger(db).ReadEmployeeTopupCredits(context.Background(), topupCreditQuery("employee-one", 20))
	if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 {
		t.Fatalf("orphan operation silently skipped: page=%+v err=%v", page, err)
	}
}
