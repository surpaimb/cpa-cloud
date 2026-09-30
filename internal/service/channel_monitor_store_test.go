package service

// Independently authored acceptance for docs/channel-monitor-contract.md.
import (
	"context"
	"testing"
	"time"
)

func TestChannelMonitorMigrationCreatesExactTablesAndRejectsPartialSchema(t *testing.T) {
	app, _, _, _ := newModelAdmissionApp(t, false)
	now := time.Date(2026, 9, 30, 1, 2, 3, 0, time.UTC)
	if err := migrateChannelMonitors(context.Background(), app.store.db, now); err != nil {
		t.Fatal(err)
	}
	if err := migrateChannelMonitors(context.Background(), app.store.db, now); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`DROP TABLE channel_monitor_runs`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`CREATE TABLE channel_monitor_runs(sequence INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateChannelMonitors(context.Background(), app.store.db, now); err == nil {
		t.Fatal("partial channel monitor schema was accepted")
	}
	var count int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='channel_monitor_plans'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("failed migration changed unrelated plan table: count=%d err=%v", count, err)
	}
}
