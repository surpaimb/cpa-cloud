package service

// Independently authored migration for docs/scheduled-tests-daily-timezone-contract.md.
// ALTER preserves the legacy plan table, its rowids, foreign keys and run history.
import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

var scheduledDailyColumnDDL = []string{
	"schedule_mode TEXT NOT NULL DEFAULT 'interval' CHECK(schedule_mode IN ('interval','daily_local'))",
	"time_zone TEXT",
	"local_time TEXT",
}

var scheduledLegacyPlanColumns = []string{"id", "name", "upstream_id", "scope", "interval_seconds", "enabled", "revision", "next_run_at", "created_by_admin_id", "updated_by_admin_id", "created_at", "updated_at", "archived_at"}
var scheduledRunColumns = []string{"sequence", "plan_id", "plan_revision", "upstream_id", "operation_id", "scope", "state", "result_code", "started_at", "finished_at", "latency_ms", "actor"}

func scheduledTestPlanFinalDDL() string {
	return strings.Replace(scheduledTestPlanDDL, ",\n\tCHECK((enabled", ",\n\t"+strings.Join(scheduledDailyColumnDDL, ",\n\t")+",\n\tCHECK((enabled", 1)
}

func migrateScheduledDailyColumns(ctx context.Context, tx *sql.Tx) error {
	if err := verifyScheduledTZArchive(); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(scheduled_test_plans)`)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		seen[name] = true
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	extra := 0
	for _, column := range []string{"schedule_mode", "time_zone", "local_time"} {
		if seen[column] {
			extra++
		}
	}
	if extra == 3 {
		return nil
	}
	if extra != 0 {
		return errors.New("partially migrated scheduled test plan schema")
	}
	if err := verifyScheduledTestColumns(ctx, tx, "scheduled_test_plans", scheduledLegacyPlanColumns); err != nil {
		return err
	}
	if err := verifyScheduledTestColumns(ctx, tx, "scheduled_test_runs", scheduledRunColumns); err != nil {
		return err
	}
	if err := verifyScheduledTestIndexes(ctx, tx); err != nil {
		return err
	}
	for _, definition := range scheduledDailyColumnDDL {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE scheduled_test_plans ADD COLUMN `+definition); err != nil {
			return err
		}
	}
	return nil
}

func verifyScheduledDailyPlanRows(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT schedule_mode,time_zone,local_time,next_run_at FROM scheduled_test_plans`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var mode string
		var zone, local, next sql.NullString
		if err := rows.Scan(&mode, &zone, &local, &next); err != nil {
			return err
		}
		if mode == "interval" {
			if zone.Valid || local.Valid {
				return errors.New("invalid interval scheduled test row")
			}
		} else if mode == "daily_local" {
			if !zone.Valid || !local.Valid {
				return errors.New("incomplete daily scheduled test row")
			}
			if _, err := loadScheduledTimeZone(zone.String); err != nil {
				return err
			}
			if _, _, err := parseScheduledLocalTime(local.String); err != nil {
				return err
			}
		} else {
			return errors.New("unknown scheduled test mode")
		}
		if next.Valid {
			if _, err := parseTime(next.String); err != nil {
				return err
			}
		}
	}
	return rows.Err()
}
