// Command acceptance-set-scheduled-due is an independently authored test-only
// helper. It may update only a database beneath the explicit acceptance root.
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

func main() {
	var database, plan string
	flag.StringVar(&database, "db", "", "isolated CPA Cloud database")
	flag.StringVar(&plan, "plan", "", "scheduled plan ID")
	flag.Parse()
	root, err := filepath.Abs(os.Getenv("CPA_CLOUD_ACCEPTANCE_TEMP_ROOT"))
	if err != nil || root == "" {
		fatal("explicit acceptance root is required")
	}
	database, err = filepath.Abs(database)
	if err != nil {
		fatal("invalid database path")
	}
	rel, err := filepath.Rel(root, database)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		fatal("database escaped acceptance root")
	}
	if len(plan) < 1 || len(plan) > 128 {
		fatal("invalid plan ID")
	}
	db, err := sql.Open("sqlite", database)
	if err != nil {
		fatal(err.Error())
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		fatal(err.Error())
	}
	stamp := time.Now().UTC().Add(-time.Second).Format("2006-01-02T15:04:05.000000000Z")
	result, err := db.Exec(`UPDATE scheduled_test_plans SET next_run_at=? WHERE id=? AND enabled=1 AND archived_at IS NULL`, stamp, plan)
	if err != nil {
		fatal(err.Error())
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		fatal("expected exactly one enabled plan")
	}
}

func fatal(message string) {
	_, _ = fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
