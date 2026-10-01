// Synthetic acceptance-only fixture for docs/subscription-one-shot-renewal-contract.md.
// It modifies only a cpac-one-shot-* database under the OS temporary directory.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const immutableTrigger = `CREATE TRIGGER financial_subscription_one_shot_immutable_update BEFORE UPDATE OF predecessor_id,arm_operation_id,execution_operation_id,armed_by_admin_id,predecessor_revision,due_at,armed_at ON financial_subscription_one_shot_renewals BEGIN SELECT RAISE(ABORT,'one-shot renewal identity is immutable'); END`

func main() {
	var databasePath, subscriptionID string
	flag.StringVar(&databasePath, "db", "", "isolated acceptance database path")
	flag.StringVar(&subscriptionID, "subscription", "", "armed subscription ID")
	flag.Parse()
	absolute, err := filepath.Abs(databasePath)
	if err != nil || subscriptionID == "" || flag.NArg() != 0 || filepath.Base(absolute) != "cpa-cloud.db" || !strings.HasPrefix(filepath.Base(filepath.Dir(absolute)), "cpac-one-shot-") {
		fmt.Fprintln(os.Stderr, "an isolated cpac-one-shot-* database and --subscription are required")
		os.Exit(2)
	}
	relative, err := filepath.Rel(os.TempDir(), absolute)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		fmt.Fprintln(os.Stderr, "acceptance database must be under the OS temporary directory")
		os.Exit(2)
	}
	db, err := sql.Open("sqlite", absolute+"?_pragma=foreign_keys(1)")
	if err != nil {
		panic(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		panic(err)
	}
	defer tx.Rollback()
	var armedAt string
	if err := tx.QueryRow(`SELECT armed_at FROM financial_subscription_one_shot_renewals WHERE predecessor_id=? AND state='armed'`, subscriptionID).Scan(&armedAt); err != nil {
		panic(err)
	}
	armed, err := time.Parse(time.RFC3339Nano, armedAt)
	if err != nil {
		panic(err)
	}
	due := time.Now().UTC().Add(2 * time.Second)
	if !armed.Before(due) {
		panic("reservation was not armed before synthetic due instant")
	}
	start := due.AddDate(0, -1, 0)
	if start.AddDate(0, 1, 0) != due {
		panic("synthetic due instant cannot be represented by a prior monthly start")
	}
	dueText := due.Format("2006-01-02T15:04:05.000000000Z")
	if _, err := tx.Exec(`DROP TRIGGER financial_subscription_one_shot_immutable_update`); err != nil {
		panic(err)
	}
	result, err := tx.Exec(`UPDATE financial_subscriptions SET started_at=?,period_end_at=? WHERE id=? AND status='active' AND interval='monthly'`, start.Format(time.RFC3339Nano), dueText, subscriptionID)
	if err != nil {
		panic(err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		panic("expected exactly one active monthly subscription")
	}
	if _, err := tx.Exec(`UPDATE financial_subscription_one_shot_renewals SET due_at=? WHERE predecessor_id=? AND state='armed'`, dueText, subscriptionID); err != nil {
		panic(err)
	}
	if _, err := tx.Exec(immutableTrigger); err != nil {
		panic(err)
	}
	if err := tx.Commit(); err != nil {
		panic(err)
	}
}
