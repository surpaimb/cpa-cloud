package service

// Independently authored regression for the startup SQLite busy-timeout order.

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreInitializeWaitsForBriefStartupSQLiteLock(t *testing.T) {
	directory := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(directory, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `CREATE TABLE startup_lock_fixture(id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `BEGIN EXCLUSIVE`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(context.Background(), `ROLLBACK`)
	type result struct {
		store *store
		err   error
	}
	done := make(chan result, 1)
	go func() {
		store, err := openStore(directory)
		done <- result{store, err}
	}()
	select {
	case result := <-done:
		if result.store != nil {
			result.store.close()
		}
		t.Fatalf("startup returned while SQLite exclusive lock was held: %v", result.err)
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := conn.ExecContext(context.Background(), `COMMIT`); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("startup did not recover after brief lock: %v", result.err)
		}
		if err := result.store.close(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("startup did not resume after SQLite lock release")
	}
}
