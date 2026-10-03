// Independently authored tests for docs/employee-self-admin-adjustment-history-contract.md.
package financial

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func adminAdjustmentQuery(limit int) EmployeeActivityQuery {
	return financialActivityQuery("employee-one", "USD", limit)
}

func postTestAdminAdjustment(t *testing.T, ledger *Ledger, id string, owner Owner, amount int64, at time.Time) {
	t.Helper()
	kind := EntryAdjustmentCredit
	if amount < 0 {
		kind = EntryAdjustmentDebit
	}
	_, err := ledger.Post(context.Background(), Post{OperationID: id, Action: "adjustment", ActorAdminID: "admin-one",
		ResourceKind: "adjustment", ResourceID: id, ObservedAt: at, RequireNonNegative: true,
		Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: kind, AmountMicro: amount,
			ResourceKind: "adjustment", ResourceID: id}}})
	if err != nil {
		t.Fatalf("post %s: %v", id, err)
	}
}

func newAdminAdjustmentLedger(t *testing.T) (*sql.DB, *Ledger) {
	t.Helper()
	db := openFinancialTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	ledger := NewLedger(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db, ledger
}

func TestEmployeeAdminAdjustmentDirectOwnerSignsAndPagination(t *testing.T) {
	_, ledger := newAdminAdjustmentLedger(t)
	query := adminAdjustmentQuery(1)
	missing, err := ledger.ReadEmployeeAdminAdjustments(context.Background(), query)
	if err != nil || missing.HasAccount || len(missing.Items) != 0 {
		t.Fatalf("missing=%+v err=%v", missing, err)
	}
	owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
	postTestAdminAdjustment(t, ledger, "other-employee", Owner{Kind: OwnerEmployee, EmployeeID: "employee-two"}, 99, financialTestTime)
	postTestAdminAdjustment(t, ledger, "key-child", Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}, 77, financialTestTime)
	postTestAdminAdjustment(t, ledger, "resource-child", Owner{Kind: OwnerResource, EmployeeID: "employee-one", ResourceKind: "response", ResourceID: "response-one"}, 66, financialTestTime)
	empty, err := ledger.ReadEmployeeAdminAdjustments(context.Background(), query)
	if err != nil || empty.HasAccount {
		t.Fatalf("no direct account=%+v err=%v", empty, err)
	}
	postTestAdminAdjustment(t, ledger, "direct-credit", owner, 42, financialTestTime.Add(time.Second))
	postTestAdminAdjustment(t, ledger, "direct-debit", owner, -7, financialTestTime.Add(time.Second))
	topup, err := ledger.Post(context.Background(), Post{OperationID: "commercial-topup", Action: "topup", ActorAdminID: "admin-one",
		ResourceKind: "topup", ResourceID: "commercial-topup", ObservedAt: financialTestTime.Add(2 * time.Second),
		Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryTopUp, AmountMicro: 20, ResourceKind: "topup", ResourceID: "commercial-topup"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.Post(context.Background(), Post{OperationID: "commercial-refund", Action: "refund", ActorAdminID: "admin-one",
		ResourceKind: "refund", ResourceID: "commercial-refund", ObservedAt: financialTestTime.Add(3 * time.Second),
		Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentDebit, AmountMicro: -5,
			OriginalEntryID: topup[0].ID, ResourceKind: "refund", ResourceID: "commercial-refund"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ledger.Post(context.Background(), Post{OperationID: "employee-adjustment", Action: "adjustment", Actor: Actor{Kind: ActorEmployee, ID: "employee-one"},
		ResourceKind: "adjustment", ResourceID: "employee-adjustment", ObservedAt: financialTestTime.Add(4 * time.Second), RequireNonNegative: true,
		Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 99,
			ResourceKind: "adjustment", ResourceID: "employee-adjustment"}}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := ledger.ReadEmployeeAdminAdjustments(context.Background(), query)
	if err != nil || !first.HasAccount || len(first.Items) != 1 || first.NextPosition == nil {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	query.BeforeTime, query.BeforeID = first.NextPosition.Time, first.NextPosition.ID
	second, err := ledger.ReadEmployeeAdminAdjustments(context.Background(), query)
	if err != nil || len(second.Items) != 1 || second.NextPosition != nil {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if first.Items[0].DeltaMicro+second.Items[0].DeltaMicro != 35 {
		t.Fatalf("wrong signed page items: %+v %+v", first.Items, second.Items)
	}
}

func TestEmployeeAdminAdjustmentV1DigestAndBadLookahead(t *testing.T) {
	for _, mutation := range []string{"none", "digest", "selected-digest", "actor", "orphan-admin", "extra-entry", "cross-account-entry", "false-nonnegative-digest", "schema"} {
		t.Run(mutation, func(t *testing.T) {
			db, ledger := newAdminAdjustmentLedger(t)
			owner := Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}
			postTestAdminAdjustment(t, ledger, "older-adjustment", owner, 11, financialTestTime)
			postTestAdminAdjustment(t, ledger, "newer-adjustment", owner, 13, financialTestTime.Add(time.Second))
			legacy := Post{OperationID: "older-adjustment", Action: "adjustment", ActorAdminID: "admin-one", ResourceKind: "adjustment", ResourceID: "older-adjustment", RequireNonNegative: true,
				Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 11, ResourceKind: "adjustment", ResourceID: "older-adjustment"}}}
			digest, err := postDigest(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DROP TRIGGER financial_operations_no_update`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE financial_operations SET payload_digest=?,digest_version=1 WHERE operation_id='older-adjustment'`, digest[:]); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(operationsNoUpdateDDL); err != nil {
				t.Fatal(err)
			}
			page, err := ledger.ReadEmployeeAdminAdjustments(context.Background(), adminAdjustmentQuery(1))
			if err != nil || len(page.Items) != 1 || page.NextPosition == nil {
				t.Fatalf("valid page=%+v err=%v", page, err)
			}
			if mutation == "none" {
				query := adminAdjustmentQuery(1)
				query.BeforeTime, query.BeforeID = page.NextPosition.Time, page.NextPosition.ID
				last, err := ledger.ReadEmployeeAdminAdjustments(context.Background(), query)
				if err != nil || len(last.Items) != 1 || last.NextPosition != nil {
					t.Fatalf("v1 continuation=%+v err=%v", last, err)
				}
				return
			}
			switch mutation {
			case "digest":
				_, _ = db.Exec(`DROP TRIGGER financial_operations_no_update`)
				_, err = db.Exec(`UPDATE financial_operations SET payload_digest=randomblob(32) WHERE operation_id='older-adjustment'`)
				if err == nil {
					_, err = db.Exec(operationsNoUpdateDDL)
				}
			case "selected-digest":
				_, _ = db.Exec(`DROP TRIGGER financial_operations_no_update`)
				_, err = db.Exec(`UPDATE financial_operations SET payload_digest=randomblob(32) WHERE operation_id='newer-adjustment'`)
				if err == nil {
					_, err = db.Exec(operationsNoUpdateDDL)
				}
			case "actor":
				if _, err = db.Exec(`INSERT INTO admins(id) VALUES('admin-two')`); err != nil {
					t.Fatal(err)
				}
				_, _ = db.Exec(`DROP TRIGGER financial_operations_no_update`)
				_, err = db.Exec(`UPDATE financial_operations SET actor_admin_id='admin-two' WHERE operation_id='older-adjustment'`)
				if err == nil {
					_, err = db.Exec(operationsNoUpdateDDL)
				}
			case "orphan-admin":
				if _, err = db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`DROP TRIGGER financial_operations_no_update`); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`UPDATE financial_operations SET actor_admin_id='missing-admin' WHERE operation_id='older-adjustment'`); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(operationsNoUpdateDDL); err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec(`PRAGMA foreign_keys=ON`)
			case "extra-entry":
				_, err = db.Exec(`INSERT INTO financial_entries(id,operation_id,account_id,kind,amount_micro,resource_kind,resource_id,created_at) SELECT 'extra-adjustment',operation_id,account_id,'adjustment_debit',-1,resource_kind,resource_id,created_at FROM financial_entries WHERE operation_id='older-adjustment'`)
			case "cross-account-entry":
				postTestAdminAdjustment(t, ledger, "other-owner", Owner{Kind: OwnerEmployee, EmployeeID: "employee-two"}, 3, financialTestTime)
				var inserted sql.Result
				inserted, err = db.Exec(`INSERT INTO financial_entries(id,operation_id,account_id,kind,amount_micro,resource_kind,resource_id,created_at)
					SELECT 'cross-account-adjustment',e.operation_id,other.account_id,'adjustment_credit',1,e.resource_kind,e.resource_id,e.created_at
					FROM financial_entries e JOIN financial_entries other ON other.operation_id='other-owner'
					WHERE e.operation_id='older-adjustment'`)
				if err == nil {
					var affected int64
					affected, err = inserted.RowsAffected()
					if err == nil && affected != 1 {
						t.Fatalf("cross-account fixture affected %d rows", affected)
					}
				}
			case "false-nonnegative-digest":
				falsePost := Post{OperationID: "newer-adjustment", Action: "adjustment", ResourceKind: "adjustment", ResourceID: "newer-adjustment",
					Entries: []EntryInput{{Owner: owner, Currency: "USD", Kind: EntryAdjustmentCredit, AmountMicro: 13,
						ResourceKind: "adjustment", ResourceID: "newer-adjustment"}}}
				falseDigest, digestErr := postDigestV2(falsePost, Actor{Kind: ActorAdmin, ID: "admin-one"})
				if digestErr != nil {
					t.Fatal(digestErr)
				}
				var originalDigest []byte
				if err = db.QueryRow(`SELECT payload_digest FROM financial_operations WHERE operation_id='newer-adjustment'`).Scan(&originalDigest); err != nil {
					t.Fatal(err)
				}
				if equalBytes(originalDigest, falseDigest[:]) {
					t.Fatal("RequireNonNegative fixture digest did not change")
				}
				if _, err = db.Exec(`DROP TRIGGER financial_operations_no_update`); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`UPDATE financial_operations SET payload_digest=? WHERE operation_id='newer-adjustment'`, falseDigest[:]); err == nil {
					_, err = db.Exec(operationsNoUpdateDDL)
				}
			case "schema":
				_, err = db.Exec(`DROP TRIGGER financial_entries_no_update`)
			}
			if err != nil {
				t.Fatal(err)
			}
			broken, readErr := ledger.ReadEmployeeAdminAdjustments(context.Background(), adminAdjustmentQuery(1))
			if !errors.Is(readErr, ErrUnavailable) || broken.HasAccount || len(broken.Items) != 0 || broken.NextPosition != nil {
				t.Fatalf("bad lookahead should fail closed: %+v %v", broken, readErr)
			}
		})
	}
}

func TestEmployeeAdminAdjustmentReadFailures(t *testing.T) {
	_, ledger := newAdminAdjustmentLedger(t)
	postTestAdminAdjustment(t, ledger, "fault-adjustment", Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}, 17, financialTestTime)
	for name, hooks := range map[string]employeeAdminAdjustmentReadHooks{
		"scan":      {scan: func() error { return errors.New("synthetic scan failure") }},
		"iteration": {iteration: func() error { return errors.New("synthetic iteration failure") }},
		"close":     {closeRows: func(rows *sql.Rows) error { _ = rows.Close(); return errors.New("synthetic close failure") }},
		"commit":    {commit: func(tx *sql.Tx) error { _ = tx.Rollback(); return errors.New("synthetic commit failure") }},
	} {
		t.Run(name, func(t *testing.T) {
			page, err := ledger.readEmployeeAdminAdjustments(context.Background(), adminAdjustmentQuery(20), hooks)
			if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 {
				t.Fatalf("fault page=%+v err=%v", page, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	page, err := ledger.readEmployeeAdminAdjustments(ctx, adminAdjustmentQuery(20), employeeAdminAdjustmentReadHooks{afterAccount: cancel})
	if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 {
		t.Fatalf("cancelled page=%+v err=%v", page, err)
	}
}
