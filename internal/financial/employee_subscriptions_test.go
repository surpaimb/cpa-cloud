// Independently authored tests for docs/employee-self-subscription-status-contract.md.
package financial

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"
)

func employeeSubscriptionFixture(t *testing.T) (*sql.DB, *Commercial, string, string) {
	t.Helper()
	db := openFinancialTestDB(t)
	ledger, commercial := NewLedger(db), NewCommercial(db)
	if err := ledger.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := commercial.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, seed := range []struct {
		id    string
		owner Owner
	}{
		{"subscription-employee-one", Owner{Kind: OwnerEmployee, EmployeeID: "employee-one"}},
		{"subscription-employee-two", Owner{Kind: OwnerEmployee, EmployeeID: "employee-two"}},
		{"subscription-key", Owner{Kind: OwnerKey, EmployeeID: "employee-one", KeyID: "key-one"}},
		{"subscription-resource", Owner{Kind: OwnerResource, EmployeeID: "employee-one", ResourceKind: "response", ResourceID: "response-one"}},
	} {
		postEmployeeBalanceEntry(t, ledger, seed.id, seed.owner, "USD", EntryAdjustmentCredit, 100, financialTestTime)
	}
	monthly, _, err := commercial.CreatePlan(context.Background(), CreatePlan{
		Meta: testCommercialMeta(t, "self-sub-month-plan", map[string]any{"interval": "monthly"}, financialTestTime),
		Name: "Synthetic monthly", Currency: "USD", Interval: "monthly", PriceMicro: 10, CreditMicro: 20, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	oneTime, _, err := commercial.CreatePlan(context.Background(), CreatePlan{
		Meta: testCommercialMeta(t, "self-sub-once-plan", map[string]any{"interval": "one_time"}, financialTestTime),
		Name: "Synthetic once", Currency: "USD", Interval: "one_time", PriceMicro: 10, CreditMicro: 20, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return db, commercial, monthly.ID, oneTime.ID
}

func accountForSubscriptionTest(t *testing.T, db *sql.DB, kind, employee string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM financial_accounts WHERE owner_kind=? AND employee_id=? AND currency='USD'`, kind, employee).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func insertEmployeeSubscriptionTest(t *testing.T, db *sql.DB, id, accountID, planID, interval, status string, start time.Time, cancelled *time.Time) {
	t.Helper()
	var end, cancellation any
	if interval == "monthly" {
		value, err := monthlyPeriodEnd(start)
		if err != nil {
			t.Fatal(err)
		}
		end = value.Format(subscriptionEndLayout)
	}
	if cancelled != nil {
		cancellation = formatCommercialTime(*cancelled)
	}
	if _, err := db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,cancelled_at,revision)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,1)`, id, accountID, planID, 1, 10, 20, "USD", interval, status, formatCommercialTime(start), end, cancellation); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeeSubscriptionsDirectOwnerStatusesAndKeyset(t *testing.T) {
	db, commercial, monthPlan, oncePlan := employeeSubscriptionFixture(t)
	defer db.Close()
	start := time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC)
	now := time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)
	ownAccount := accountForSubscriptionTest(t, db, "employee", "employee-one")
	otherAccount := accountForSubscriptionTest(t, db, "employee", "employee-two")
	keyAccount := accountForSubscriptionTest(t, db, "key", "employee-one")
	resourceAccount := accountForSubscriptionTest(t, db, "resource", "employee-one")
	cancelled := start.Add(24 * time.Hour)
	insertEmployeeSubscriptionTest(t, db, "sub-z-once", ownAccount, oncePlan, "one_time", "active", start, nil)
	insertEmployeeSubscriptionTest(t, db, "sub-y-cancelled", ownAccount, monthPlan, "monthly", "cancelled", start, &cancelled)
	insertEmployeeSubscriptionTest(t, db, "sub-x-due", ownAccount, monthPlan, "monthly", "active", start, nil)
	insertEmployeeSubscriptionTest(t, db, "sub-w-other", otherAccount, monthPlan, "monthly", "active", start, nil)
	insertEmployeeSubscriptionTest(t, db, "sub-v-key", keyAccount, monthPlan, "monthly", "active", start, nil)
	insertEmployeeSubscriptionTest(t, db, "sub-u-resource", resourceAccount, monthPlan, "monthly", "active", start, nil)
	if enabled, _, err := commercial.Settings(context.Background()); err != nil || enabled {
		t.Fatalf("commercial switch enabled=%v err=%v", enabled, err)
	}
	var got []EmployeeSubscriptionItem
	before := ""
	for i := 0; i < 4; i++ {
		page, err := commercial.ReadEmployeeSubscriptions(context.Background(), "employee-one", before, 1, now)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, page.Items...)
		if page.NextPosition == "" {
			break
		}
		before = page.NextPosition
	}
	if len(got) != 3 || !reflect.DeepEqual([]string{got[0].SubscriptionID, got[1].SubscriptionID, got[2].SubscriptionID}, []string{"sub-z-once", "sub-y-cancelled", "sub-x-due"}) ||
		got[0].Status != "active" || got[0].PeriodEndAt != nil || got[1].Status != "cancelled" || got[1].CancelledAt == nil || got[2].Status != "expired" || got[2].PeriodEndAt == nil {
		t.Fatalf("isolated page items=%+v", got)
	}
	page, err := commercial.ReadEmployeeSubscriptions(context.Background(), "employee-one", "", 50, start.Add(24*time.Hour))
	if err != nil || page.Items[2].Status != "active" {
		t.Fatalf("before due page=%+v err=%v", page, err)
	}
	page, err = commercial.ReadEmployeeSubscriptions(context.Background(), "absent-employee", "", 20, now)
	if err != nil || len(page.Items) != 0 || page.NextPosition != "" {
		t.Fatalf("absent page=%+v err=%v", page, err)
	}
	var stored string
	if err := db.QueryRow(`SELECT status FROM financial_subscriptions WHERE id='sub-x-due'`).Scan(&stored); err != nil || stored != "active" {
		t.Fatalf("read changed stored status=%q err=%v", stored, err)
	}
}

func TestEmployeeSubscriptionsFailsClosedOnSchemaLookaheadCloseCommitAndCancel(t *testing.T) {
	for _, variant := range []string{"schema", "lookahead", "close", "commit", "cancel"} {
		t.Run(variant, func(t *testing.T) {
			db, commercial, monthPlan, _ := employeeSubscriptionFixture(t)
			defer db.Close()
			start := time.Date(2026, 1, 31, 8, 0, 0, 0, time.UTC)
			account := accountForSubscriptionTest(t, db, "employee", "employee-one")
			insertEmployeeSubscriptionTest(t, db, "sub-z-good", account, monthPlan, "monthly", "active", start, nil)
			var hooks employeeSubscriptionReadHooks
			switch variant {
			case "schema":
				if _, err := db.Exec(`DROP INDEX financial_subscriptions_account_idx`); err != nil {
					t.Fatal(err)
				}
			case "lookahead":
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision)
					VALUES('sub-a-bad',?,?,1,10,20,'USD','monthly','active','not-a-time','2026-02-28T08:00:00.000000000Z',1)`, account, monthPlan); err != nil {
					t.Fatal(err)
				}
			case "close":
				hooks.closeRows = func(rows *sql.Rows) error { _ = rows.Close(); return errors.New("synthetic close failure") }
			case "commit":
				hooks.commit = func(*sql.Tx) error { return errors.New("synthetic commit failure") }
			case "cancel":
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				hooks.afterRows = cancel
				page, err := commercial.readEmployeeSubscriptions(ctx, "employee-one", "", 1, start, hooks)
				if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 || page.NextPosition != "" {
					t.Fatalf("cancel page=%+v err=%v", page, err)
				}
				return
			}
			page, err := commercial.readEmployeeSubscriptions(context.Background(), "employee-one", "", 1, start, hooks)
			if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 || page.NextPosition != "" {
				t.Fatalf("%s page=%+v err=%v", variant, page, err)
			}
		})
	}
}
