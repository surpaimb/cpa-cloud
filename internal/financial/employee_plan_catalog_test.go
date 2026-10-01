// Independently authored tests for docs/employee-self-plan-catalog-contract.md.
package financial

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"
)

func setPlanCatalogCommercial(t *testing.T, db *sql.DB, enabled bool) {
	t.Helper()
	value := 0
	if enabled {
		value = 1
	}
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=?,revision=revision+1,updated_at=? WHERE singleton=1`, value, financialTestTime.Add(time.Minute).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

func TestEmployeePlanCatalogCurrentEnabledSwitchAndKeyset(t *testing.T) {
	db, commercial, monthID, onceID := employeeSubscriptionFixture(t)
	defer db.Close()
	off, err := commercial.ReadEmployeePlanCatalog(context.Background(), "USD", "", 1)
	if err != nil || off.Available || len(off.Items) != 0 || off.NextPosition != "" {
		t.Fatalf("commercial off page=%+v err=%v", off, err)
	}
	disabled, _, err := commercial.CreatePlan(context.Background(), CreatePlan{
		Meta: testCommercialMeta(t, "catalog-disabled-plan", map[string]any{"enabled": false}, financialTestTime),
		Name: "Disabled synthetic", Currency: "USD", Interval: "monthly", PriceMicro: 40, CreditMicro: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = commercial.UpdatePlan(context.Background(), UpdatePlan{
		Meta: testCommercialMeta(t, "catalog-update-plan", map[string]any{"price_micro": 13}, financialTestTime.Add(time.Minute)),
		ID:   monthID, Name: "Current monthly", Currency: "USD", Interval: "monthly", PriceMicro: 13, CreditMicro: 29,
		Enabled: true, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	setPlanCatalogCommercial(t, db, true)
	var got []EmployeePlanCatalogItem
	after := ""
	for i := 0; i < 3; i++ {
		page, err := commercial.ReadEmployeePlanCatalog(context.Background(), "USD", after, 1)
		if err != nil || !page.Available {
			t.Fatalf("page=%+v err=%v", page, err)
		}
		got = append(got, page.Items...)
		if page.NextPosition == "" {
			break
		}
		after = page.NextPosition
	}
	if len(got) != 2 {
		t.Fatalf("visible plans=%+v", got)
	}
	ids := []string{monthID, onceID}
	sort.Strings(ids)
	if got[0].PlanID != ids[0] || got[1].PlanID != ids[1] || got[0].PlanID >= got[1].PlanID || got[0].PlanID == disabled.ID || got[1].PlanID == disabled.ID {
		t.Fatalf("unstable or leaked plans=%+v", got)
	}
	for _, item := range got {
		if item.PlanID == monthID && (item.Name != "Current monthly" || item.PriceMicro != "13" || item.CreditMicro != "29" || item.Revision != 2) {
			t.Fatalf("current revision not projected: %+v", item)
		}
		if item.PlanID == onceID && (item.Interval != "one_time" || item.PriceMicro != "10" || item.CreditMicro != "20" || item.Revision != 1) {
			t.Fatalf("one-time plan changed: %+v", item)
		}
	}
	empty, err := commercial.ReadEmployeePlanCatalog(context.Background(), "EUR", "", 20)
	if err != nil || !empty.Available || len(empty.Items) != 0 || empty.NextPosition != "" {
		t.Fatalf("enabled empty page=%+v err=%v", empty, err)
	}
	setPlanCatalogCommercial(t, db, false)
	off, err = commercial.ReadEmployeePlanCatalog(context.Background(), "USD", got[0].PlanID, 1)
	if err != nil || off.Available || len(off.Items) != 0 || off.NextPosition != "" {
		t.Fatalf("commercial disabled continuation=%+v err=%v", off, err)
	}
}

func TestEmployeePlanCatalogFailsClosedOnStorageAndCancellation(t *testing.T) {
	for _, variant := range []string{"schema", "settings", "selected", "lookahead", "iteration", "close", "commit", "cancel", "cancel_after_commit"} {
		t.Run(variant, func(t *testing.T) {
			db, commercial, _, _ := employeeSubscriptionFixture(t)
			defer db.Close()
			setPlanCatalogCommercial(t, db, true)
			limit := 1
			var hooks employeePlanCatalogReadHooks
			switch variant {
			case "schema":
				if _, err := db.Exec(`DROP INDEX financial_subscriptions_account_idx`); err != nil {
					t.Fatal(err)
				}
			case "settings":
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				if _, err := db.Exec(`UPDATE financial_settings SET enabled='bad' WHERE singleton=1`); err != nil {
					t.Fatal(err)
				}
			case "selected", "lookahead":
				if _, err := db.Exec(`PRAGMA ignore_check_constraints=ON`); err != nil {
					t.Fatal(err)
				}
				id := "a-bad"
				if variant == "lookahead" {
					id, limit = "z-bad", 2
				}
				if _, err := db.Exec(`INSERT INTO financial_plans(id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at)
					VALUES(?, 'Bad synthetic', 'USD', 'not-an-integer', 10, 'monthly', 1, 1, ?, ?)`, id, financialTestTime.Format(time.RFC3339Nano), financialTestTime.Format(time.RFC3339Nano)); err != nil {
					t.Fatal(err)
				}
			case "close":
				hooks.closeRows = func(rows *sql.Rows) error { _ = rows.Close(); return errors.New("synthetic close failure") }
			case "iteration":
				hooks.rowsErr = func(*sql.Rows) error { return errors.New("synthetic iteration failure") }
			case "commit":
				hooks.commit = func(*sql.Tx) error { return errors.New("synthetic commit failure") }
			case "cancel", "cancel_after_commit":
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if variant == "cancel" {
					hooks.afterRows = cancel
				} else {
					hooks.commit = func(tx *sql.Tx) error { err := tx.Commit(); cancel(); return err }
				}
				page, err := commercial.readEmployeePlanCatalog(ctx, "USD", "", limit, hooks)
				if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 || page.NextPosition != "" {
					t.Fatalf("%s page=%+v err=%v", variant, page, err)
				}
				return
			}
			page, err := commercial.readEmployeePlanCatalog(context.Background(), "USD", "", limit, hooks)
			if !errors.Is(err, ErrUnavailable) || len(page.Items) != 0 || page.NextPosition != "" {
				t.Fatalf("%s page=%+v err=%v", variant, page, err)
			}
		})
	}
}
