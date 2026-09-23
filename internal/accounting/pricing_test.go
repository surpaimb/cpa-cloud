package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestPriceCatalogVersioningIdempotencyDisableAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "prices.db")
	db, catalog := openPriceCatalog(t, path)
	insertPriceUpstream(t, db, "ups_price")
	ctx := context.Background()
	missingInput := PriceSave{AccountID: "ups_missing", ActualModel: "model", OperationID: "450e8400-e29b-41d4-a716-446655440000", Price: testCatalogPrice()}
	if _, err := catalog.Save(ctx, missingInput); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing upstream save err=%v", err)
	}
	var leakedRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM account_price_current WHERE upstream_id='ups_missing'`).Scan(&leakedRows); err != nil || leakedRows != 0 {
		t.Fatalf("failed save leaked current rows=%d err=%v", leakedRows, err)
	}
	model := strings.Repeat("m", 140) + " internal space " + strings.Repeat("z", 80)
	firstInput := PriceSave{AccountID: "ups_price", ActualModel: model, OperationID: "550e8400-e29b-41d4-a716-446655440000", Price: testCatalogPrice()}
	first, err := catalog.Save(ctx, firstInput)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || first.Price == nil || first.Price.Version != first.Version {
		t.Fatalf("unexpected first version: %+v", first)
	}
	replay, err := catalog.Save(ctx, firstInput)
	if err != nil || replay.Version != first.Version || !replay.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("idempotent replay=%+v err=%v", replay, err)
	}
	changedOperation := firstInput
	changedOperation.Price = testCatalogPrice()
	changedOperation.Price.OutputPerMillionMicro++
	if _, err := catalog.Save(ctx, changedOperation); !errors.Is(err, ErrPriceOperationConflict) {
		t.Fatalf("changed operation err=%v", err)
	}
	stale := firstInput
	stale.OperationID = "650e8400-e29b-41d4-a716-446655440000"
	if _, err := catalog.Save(ctx, stale); !errors.Is(err, ErrPriceRevisionConflict) {
		t.Fatalf("stale revision err=%v", err)
	}
	disable := PriceSave{AccountID: "ups_price", ActualModel: model, OperationID: "750e8400-e29b-41d4-a716-446655440000", ExpectedRevision: 1, Price: nil}
	disabled, err := catalog.Save(ctx, disable)
	if err != nil || disabled.Revision != 2 || disabled.Price != nil {
		t.Fatalf("disable=%+v err=%v", disabled, err)
	}
	current, err := catalog.Current(ctx, "ups_price", model)
	if err != nil || current != nil {
		t.Fatalf("disabled current=%+v err=%v", current, err)
	}
	items, err := catalog.List(ctx, "ups_price")
	if err != nil || len(items) != 1 || items[0].Version != disabled.Version || items[0].Price != nil {
		t.Fatalf("current list=%+v err=%v", items, err)
	}
	if _, err := db.Exec(`UPDATE account_price_versions SET created_at=created_at WHERE version=?`, first.Version); err == nil {
		t.Fatal("immutable price version accepted update")
	}
	if _, err := db.Exec(`DELETE FROM account_price_versions WHERE version=?`, first.Version); err == nil {
		t.Fatal("immutable price version accepted delete")
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		t.Fatal(err)
	}
	restarted := NewPriceCatalog(db)
	if err := restarted.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	replay, err = restarted.Save(ctx, firstInput)
	if err != nil || replay.Version != first.Version {
		t.Fatalf("restart replay=%+v err=%v", replay, err)
	}
	items, err = restarted.List(ctx, "ups_price")
	if err != nil || len(items) != 1 || items[0].Revision != 2 {
		t.Fatalf("restart list=%+v err=%v", items, err)
	}
}

func TestPriceCatalogConcurrentCASAndReadCompatibility(t *testing.T) {
	db, catalog := openPriceCatalog(t, filepath.Join(t.TempDir(), "concurrent.db"))
	defer db.Close()
	insertPriceUpstream(t, db, "ups_concurrent")
	inputs := []PriceSave{
		{AccountID: "ups_concurrent", ActualModel: "actual model", OperationID: "850e8400-e29b-41d4-a716-446655440000", Price: testCatalogPrice()},
		{AccountID: "ups_concurrent", ActualModel: "actual model", OperationID: "950e8400-e29b-41d4-a716-446655440000", Price: testCatalogPrice()},
	}
	var wg sync.WaitGroup
	errorsOut := make(chan error, 2)
	for _, input := range inputs {
		input := input
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := catalog.Save(context.Background(), input)
			errorsOut <- err
		}()
	}
	wg.Wait()
	close(errorsOut)
	var successes, conflicts int
	for err := range errorsOut {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrPriceRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	sameInput := PriceSave{AccountID: "ups_concurrent", ActualModel: "same operation model", OperationID: "b50e8400-e29b-41d4-a716-446655440000", Price: testCatalogPrice()}
	versions := make(chan string, 2)
	errorsOut = make(chan error, 2)
	for index := 0; index < 2; index++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, err := catalog.Save(context.Background(), sameInput)
			if err == nil {
				versions <- item.Version
			}
			errorsOut <- err
		}()
	}
	wg.Wait()
	close(versions)
	close(errorsOut)
	for err := range errorsOut {
		if err != nil {
			t.Fatalf("concurrent idempotent save: %v", err)
		}
	}
	var sharedVersion string
	for version := range versions {
		if sharedVersion == "" {
			sharedVersion = version
		} else if version != sharedVersion {
			t.Fatalf("concurrent replay returned versions %q and %q", sharedVersion, version)
		}
	}
	legacyModel := strings.Repeat("q", 256)
	if price, err := catalog.Current(context.Background(), "ups_concurrent", legacyModel); err != nil || price != nil {
		t.Fatalf("256-byte compatible lookup price=%+v err=%v", price, err)
	}
	if _, err := catalog.Current(context.Background(), "ups_concurrent", strings.Repeat("q", 257)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized lookup err=%v", err)
	}
	invalid := inputs[0]
	invalid.OperationID = "a50e8400-e29b-41d4-a716-446655440000"
	invalid.ActualModel = " model"
	if _, err := catalog.Save(context.Background(), invalid); !errors.Is(err, ErrInvalid) {
		t.Fatalf("leading-space model err=%v", err)
	}
}

func TestPriceCatalogMigrationRollbackRepairAndListLimit(t *testing.T) {
	db := openPriceDB(t, filepath.Join(t.TempDir(), "migration.db"))
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE upstreams(id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	insertPriceUpstream(t, db, "ups_preserved")
	if _, err := db.Exec(`CREATE TABLE account_price_versions(marker TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	catalog := NewPriceCatalog(db)
	if err := catalog.Migrate(context.Background()); err == nil {
		t.Fatal("incompatible schema migration unexpectedly succeeded")
	}
	var markerColumns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('account_price_versions') WHERE name='marker'`).Scan(&markerColumns); err != nil || markerColumns != 1 {
		t.Fatalf("incompatible table changed columns=%d err=%v", markerColumns, err)
	}
	var currentTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='account_price_current'`).Scan(&currentTables); err != nil || currentTables != 0 {
		t.Fatalf("failed migration left current table count=%d err=%v", currentTables, err)
	}
	var preserved int
	if err := db.QueryRow(`SELECT COUNT(*) FROM upstreams WHERE id='ups_preserved'`).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatalf("failed migration changed upstream count=%d err=%v", preserved, err)
	}
	if _, err := db.Exec(`DROP TABLE account_price_versions`); err != nil {
		t.Fatal(err)
	}
	if err := catalog.Migrate(context.Background()); err != nil {
		t.Fatalf("repair retry: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index <= MaxPriceListItems; index++ {
		version := fmt.Sprintf("price_limit_%04d", index)
		model := fmt.Sprintf("model-%04d", index)
		operation := fmt.Sprintf("%08x-0000-4000-8000-%012x", index+1, index+1)
		if _, err := tx.Exec(`INSERT INTO account_price_versions(version,upstream_id,upstream_model,revision,operation_id,expected_revision,created_at)
			VALUES(?,?,?,1,?,0,?)`, version, "ups_preserved", model, operation, time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`INSERT INTO account_price_current(upstream_id,upstream_model,version,revision) VALUES(?,?,?,1)`, "ups_preserved", model, version); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := catalog.List(context.Background(), "ups_preserved"); !errors.Is(err, ErrPriceListLimit) {
		t.Fatalf("list limit err=%v", err)
	}
	if _, err := catalog.List(context.Background(), "ups_missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing upstream err=%v", err)
	}
}

func openPriceCatalog(t *testing.T, path string) (*sql.DB, *PriceCatalog) {
	t.Helper()
	db := openPriceDB(t, path)
	if _, err := db.Exec(`CREATE TABLE upstreams(id TEXT PRIMARY KEY)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	catalog := NewPriceCatalog(db)
	if err := catalog.Migrate(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, catalog
}

func openPriceDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	for _, statement := range []string{`PRAGMA foreign_keys=ON`, `PRAGMA busy_timeout=5000`} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	return db
}

func insertPriceUpstream(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO upstreams(id) VALUES(?)`, id); err != nil {
		t.Fatal(err)
	}
}

func testCatalogPrice() *PriceSnapshot {
	return &PriceSnapshot{Currency: "USD", InputPerMillionMicro: 10, OutputPerMillionMicro: 20, CacheReadPerMillionMicro: 30, CacheWritePerMillionMicro: MaxPriceRate}
}
