// Independently authored synthetic acceptance fixture for renewal-links reads.
// It only runs when explicitly invoked with an isolated database path; it is
// not linked into the service or used for production data.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

const (
	fixtureStart = "2026-01-31T08:00:00Z"
	fixtureEnd   = "2026-02-28T08:00:00.000000000Z"
)

func main() {
	var databasePath, subscriptionID string
	flag.StringVar(&databasePath, "db", "", "isolated acceptance database path")
	flag.StringVar(&subscriptionID, "subscription", "", "synthetic monthly subscription ID")
	flag.Parse()
	if databasePath == "" || subscriptionID == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "--db and --subscription are required")
		os.Exit(2)
	}
	if err := setDue(databasePath, subscriptionID); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func setDue(databasePath, subscriptionID string) error {
	db, err := sql.Open("sqlite", databasePath+"?_pragma=foreign_keys(1)")
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var accountID, operationID string
	err = tx.QueryRowContext(ctx, `SELECT s.account_id,c.operation_id FROM financial_subscriptions s
		JOIN financial_commercial_operations c ON c.resource_kind='subscription' AND c.resource_id=s.id AND c.action='subscription.create'
		WHERE s.id=? AND s.interval='monthly' AND s.status='active' AND s.period_end_at IS NOT NULL`, subscriptionID).
		Scan(&accountID, &operationID)
	if err != nil {
		return fmt.Errorf("expected one synthetic monthly purchase: %w", err)
	}
	// The fixture temporarily removes the service's immutable-row triggers in
	// this transaction, then restores their exact SQL before committing. A
	// failed mutation rolls back both row values and trigger definitions.
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND tbl_name IN
		('financial_subscriptions','financial_commercial_operations','financial_operations','financial_entries','financial_accounts') ORDER BY name`)
	if err != nil {
		return err
	}
	type trigger struct{ name, sql string }
	var triggers []trigger
	for rows.Next() {
		var item trigger
		if err := rows.Scan(&item.name, &item.sql); err != nil {
			rows.Close()
			return err
		}
		triggers = append(triggers, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range triggers {
		if _, err := tx.ExecContext(ctx, `DROP TRIGGER "`+strings.ReplaceAll(item.name, `"`, `""`)+`"`); err != nil {
			return err
		}
	}
	updates := []struct {
		query string
		args  []any
		count int64
	}{
		{`UPDATE financial_subscriptions SET started_at=?,period_end_at=? WHERE id=? AND status='active'`, []any{fixtureStart, fixtureEnd, subscriptionID}, 1},
		{`UPDATE financial_commercial_operations SET created_at=? WHERE operation_id=? AND action='subscription.create' AND resource_id=?`, []any{fixtureStart, operationID, subscriptionID}, 1},
		{`UPDATE financial_operations SET created_at=? WHERE operation_id=? AND action='subscription_purchase' AND resource_id=?`, []any{fixtureStart, operationID, subscriptionID}, 1},
		{`UPDATE financial_entries SET created_at=? WHERE operation_id=? AND resource_id=?`, []any{fixtureStart, operationID, subscriptionID}, 2},
		{`UPDATE financial_accounts SET created_at=? WHERE id=?`, []any{fixtureStart, accountID}, 1},
	}
	for _, update := range updates {
		result, err := tx.ExecContext(ctx, update.query, update.args...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if count != update.count {
			return fmt.Errorf("synthetic fixture row count mismatch: got %d, expected %d", count, update.count)
		}
	}
	for _, item := range triggers {
		if _, err := tx.ExecContext(ctx, item.sql); err != nil {
			return err
		}
	}
	return tx.Commit()
}
