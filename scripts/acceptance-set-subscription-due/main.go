// Synthetic acceptance-only fixture: make one purchased monthly period due.
// This is not linked into the service and must never run against production data.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	var databasePath, subscriptionID string
	flag.StringVar(&databasePath, "db", "", "isolated acceptance database path")
	flag.StringVar(&subscriptionID, "subscription", "", "monthly subscription ID")
	flag.Parse()
	if databasePath == "" || subscriptionID == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "--db and --subscription are required")
		os.Exit(2)
	}
	db, err := sql.Open("sqlite", databasePath+"?_pragma=foreign_keys(1)")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()
	result, err := db.Exec(`UPDATE financial_subscriptions SET started_at='2026-01-31T08:00:00Z',period_end_at='2026-02-28T08:00:00.000000000Z' WHERE id=? AND interval='monthly' AND status='active' AND period_end_at IS NOT NULL`, subscriptionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		fmt.Fprintln(os.Stderr, "expected exactly one active monthly subscription")
		os.Exit(1)
	}
}
