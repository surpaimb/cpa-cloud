package financial

// Independently authored for docs/employee-self-redemption-credit-history-contract.md.
// Every code, account, and actor in these tests is synthetic.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func redemptionHistoryQuery(employee string, limit int) EmployeeActivityQuery {
	end := financialTestTime.Add(24 * time.Hour)
	return EmployeeActivityQuery{EmployeeID: employee, Currency: "EUR", WindowStart: end.Add(-31 * 24 * time.Hour), WindowEnd: end, Limit: limit}
}

func issueRedemptionHistoryCode(t *testing.T, c *Commercial, operation string, at time.Time) [32]byte {
	t.Helper()
	digest := sha256.Sum256([]byte("synthetic-history-" + operation))
	if _, _, err := c.CreateCode(context.Background(), CreateRedemptionCode{
		Meta: testCommercialMeta(t, operation, operation, at), CodeDigest: digest,
		Currency: "EUR", AmountMicro: 43, MaxUses: 2,
	}); err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestEmployeeRedemptionHistoryOnlyTypedOwnSelfCreditsAndClosedGates(t *testing.T) {
	db, commercial, digest := employeeRedemptionFixture(t, 3)
	ledger := NewLedger(db)
	ctx := context.Background()
	before, err := ledger.ReadEmployeeRedemptionCredits(ctx, redemptionHistoryQuery("employee-one", 20))
	if err != nil || before.HasAccount || len(before.Items) != 0 {
		t.Fatalf("missing account page=%+v err=%v", before, err)
	}
	if _, err := callEmployeeRedemptionTx(ctx, db, commercial, employeeRedemptionInput("history-self", "employee-one", digest)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := commercial.Redeem(ctx, RedeemCode{Meta: WriteMeta{OperationID: "history-admin", Actor: Actor{Kind: ActorAdmin, ID: "admin-one"},
		PayloadDigest: sha256.Sum256([]byte("synthetic-admin-history")), ObservedAt: financialTestTime.Add(3 * time.Second)},
		CodeDigest: digest, Owner: Owner{Kind: OwnerEmployee, EmployeeID: "employee-two"}}); err != nil {
		t.Fatal(err)
	}
	adminCode := issueRedemptionHistoryCode(t, commercial, "history-admin-code", financialTestTime.Add(3*time.Second))
	if _, _, err := commercial.Redeem(ctx, RedeemCode{Meta: WriteMeta{OperationID: "history-admin-same-account", Actor: Actor{Kind: ActorAdmin, ID: "admin-one"},
		PayloadDigest: sha256.Sum256([]byte("synthetic-admin-same-account")), ObservedAt: financialTestTime.Add(4 * time.Second)},
		CodeDigest: adminCode, Owner: Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}}); err != nil {
		t.Fatal(err)
	}
	self, err := ledger.ReadEmployeeRedemptionCredits(ctx, redemptionHistoryQuery("employee-one", 20))
	if err != nil || !self.HasAccount || len(self.Items) != 1 || self.Items[0].AmountMicro != 43 ||
		self.Items[0].CreditedAt != financialTestTime.Add(2*time.Second).Format(time.RFC3339Nano) || self.NextPosition != nil {
		t.Fatalf("self page=%+v err=%v", self, err)
	}
	admin, err := ledger.ReadEmployeeRedemptionCredits(ctx, redemptionHistoryQuery("employee-two", 20))
	if err != nil || !admin.HasAccount || len(admin.Items) != 0 || admin.NextPosition != nil {
		t.Fatalf("admin-owned credit leaked into self view page=%+v err=%v", admin, err)
	}
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=0 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE financial_redemption_codes SET enabled=0,expires_at=?,max_uses=uses`, financialTestTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	after, err := ledger.ReadEmployeeRedemptionCredits(ctx, redemptionHistoryQuery("employee-one", 20))
	if err != nil || len(after.Items) != 1 || after.Items[0] != self.Items[0] {
		t.Fatalf("historic credit hidden by current gate page=%+v err=%v", after, err)
	}
}

func TestEmployeeRedemptionHistoryValidatesSelectedAndLookaheadChains(t *testing.T) {
	for _, changed := range []string{"commercial", "selected-commercial", "commercial-actor", "ledger", "code-count", "missing-redemption", "schema-trigger"} {
		t.Run(changed, func(t *testing.T) {
			db, commercial, first := employeeRedemptionFixture(t, 2)
			second := issueRedemptionHistoryCode(t, commercial, "history-code-two", financialTestTime.Add(time.Second))
			if _, err := callEmployeeRedemptionTx(context.Background(), db, commercial, employeeRedemptionInput("history-first", "employee-one", first)); err != nil {
				t.Fatal(err)
			}
			input := employeeRedemptionInput("history-second", "employee-one", second)
			input.ObservedAt = financialTestTime.Add(3 * time.Second)
			if _, err := callEmployeeRedemptionTx(context.Background(), db, commercial, input); err != nil {
				t.Fatal(err)
			}
			query := redemptionHistoryQuery("employee-one", 1)
			page, err := NewLedger(db).ReadEmployeeRedemptionCredits(context.Background(), query)
			if err != nil || len(page.Items) != 1 || page.NextPosition == nil || page.Items[0].CreditedAt != input.ObservedAt.Format(time.RFC3339Nano) {
				t.Fatalf("valid first page=%+v err=%v", page, err)
			}
			query.BeforeTime, query.BeforeID = page.NextPosition.Time, page.NextPosition.ID
			last, err := NewLedger(db).ReadEmployeeRedemptionCredits(context.Background(), query)
			if err != nil || len(last.Items) != 1 || last.NextPosition != nil {
				t.Fatalf("valid continuation=%+v err=%v", last, err)
			}
			switch changed {
			case "commercial", "selected-commercial":
				if _, err := db.Exec(`DROP TRIGGER financial_commercial_operations_no_update`); err != nil {
					t.Fatal(err)
				}
				operation := "history-first"
				if changed == "selected-commercial" {
					operation = "history-second"
				}
				if _, err := db.Exec(`UPDATE financial_commercial_operations SET payload_digest=randomblob(32) WHERE operation_id=?`, operation); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(commercialOperationsNoUpdateDDL); err != nil {
					t.Fatal(err)
				}
			case "commercial-actor":
				if _, err := db.Exec(`DROP TRIGGER financial_commercial_operations_no_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE financial_commercial_operations SET actor_employee_id='employee-two' WHERE operation_id='history-first'`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(commercialOperationsNoUpdateDDL); err != nil {
					t.Fatal(err)
				}
			case "ledger":
				if _, err := db.Exec(`DROP TRIGGER financial_operations_no_update`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE financial_operations SET payload_digest=randomblob(32) WHERE operation_id='history-first'`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(operationsNoUpdateDDL); err != nil {
					t.Fatal(err)
				}
			case "code-count":
				if _, err := db.Exec(`UPDATE financial_redemption_codes SET uses=0 WHERE code_digest=?`, first[:]); err != nil {
					t.Fatal(err)
				}
			case "missing-redemption":
				if _, err := db.Exec(`DROP TRIGGER financial_redemptions_no_delete`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`DELETE FROM financial_redemptions WHERE entry_id=(SELECT id FROM financial_entries WHERE operation_id='history-first')`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(redemptionsNoDeleteDDL); err != nil {
					t.Fatal(err)
				}
			case "schema-trigger":
				if _, err := db.Exec(`DROP TRIGGER financial_commercial_operations_no_update`); err != nil {
					t.Fatal(err)
				}
			}
			if broken, err := NewLedger(db).ReadEmployeeRedemptionCredits(context.Background(), redemptionHistoryQuery("employee-one", 1)); !errors.Is(err, ErrUnavailable) || len(broken.Items) != 0 {
				t.Fatalf("corrupt lookahead should fail whole page page=%+v err=%v", broken, err)
			}
		})
	}
}

func TestEmployeeRedemptionHistoryReadFaultsFailClosed(t *testing.T) {
	db, commercial, digest := employeeRedemptionFixture(t, 1)
	if _, err := callEmployeeRedemptionTx(context.Background(), db, commercial, employeeRedemptionInput("history-fault", "employee-one", digest)); err != nil {
		t.Fatal(err)
	}
	query := redemptionHistoryQuery("employee-one", 20)
	for name, hooks := range map[string]employeeRedemptionHistoryReadHooks{
		"scan":   {scan: func() error { return errors.New("synthetic scan fault") }},
		"close":  {closeRows: func(rows *sql.Rows) error { _ = rows.Close(); return errors.New("synthetic close fault") }},
		"commit": {commit: func(tx *sql.Tx) error { _ = tx.Rollback(); return errors.New("synthetic commit fault") }},
	} {
		t.Run(name, func(t *testing.T) {
			page, err := NewLedger(db).readEmployeeRedemptionCredits(context.Background(), query, hooks)
			if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 {
				t.Fatalf("fault page=%+v err=%v", page, err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	page, err := NewLedger(db).readEmployeeRedemptionCredits(ctx, query, employeeRedemptionHistoryReadHooks{afterAccount: cancel})
	if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 {
		t.Fatalf("cancelled page=%+v err=%v", page, err)
	}
}
