package service

// Independent acceptance for dispatch-time prices; synthetic loopback upstream only.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

func TestUsagePricingStartupMigrationRollbackRetry(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	if err := Initialize(ctx, directory, strings.NewReader("synthetic-pricing-password\n")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(directory, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE account_price_current(marker TEXT); INSERT INTO account_price_current VALUES('preserve')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	if app, err := Open(ctx, Config{DataDir: directory}); err == nil {
		app.Close()
		t.Fatal("incompatible catalog accepted")
	}
	db, err = sql.Open("sqlite", filepath.Join(directory, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	var marker string
	if err := db.QueryRow(`SELECT marker FROM account_price_current`).Scan(&marker); err != nil || marker != "preserve" {
		t.Fatalf("old data lost: %s %v", marker, err)
	}
	var tables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name='account_price_versions'`).Scan(&tables); err != nil || tables != 0 {
		t.Fatalf("partial migration committed: %d %v", tables, err)
	}
	if _, err := db.Exec(`DROP TABLE account_price_current`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	for range 2 {
		app, err := Open(ctx, Config{DataDir: directory})
		if err != nil {
			t.Fatal(err)
		}
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUsagePricingHTTPDispatchSnapshotAndFailure(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			close(entered)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		if n == 4 {
			_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":50,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":10}}}`)
	}))
	defer upstream.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	app, server, key, _ := newUsageHTTPChatFixture(t, upstream.URL)
	ctx := context.Background()
	var accountID string
	if err := app.store.db.QueryRow(`SELECT upstream_id FROM models WHERE id='usage-chat'`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	catalog := accounting.NewPriceCatalog(app.store.db)
	var failLookup atomic.Bool
	app.usage.priceLookup = func(ctx context.Context, account, model string) (*accounting.PriceSnapshot, error) {
		if failLookup.Load() {
			return nil, errors.New("synthetic private catalog failure")
		}
		return catalog.Current(ctx, account, model)
	}
	save := func(revision int64, model string, price *accounting.PriceSnapshot) accounting.PriceVersion {
		t.Helper()
		version, err := catalog.Save(ctx, accounting.PriceSave{AccountID: accountID, ActualModel: model, OperationID: fmt.Sprintf("aaaaaaaa-aaaa-4aaa-8aaa-%012d", revision+int64(len(model))*100), ExpectedRevision: revision, Price: price})
		if err != nil {
			t.Fatal(err)
		}
		return version
	}
	price := &accounting.PriceSnapshot{Currency: "USD", InputPerMillionMicro: 1000000, OutputPerMillionMicro: 2000000, CacheReadPerMillionMicro: 100000, CacheWritePerMillionMicro: 1500000}
	// Public alias deliberately has a different price; dispatch must use actual model.
	wrong := *price
	wrong.InputPerMillionMicro = 9000000
	save(0, "usage-chat", &wrong)
	first := save(0, "provider-usage", price)
	request := func() (*http.Response, error) {
		req, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"usage-chat","messages":[]}`))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+key.Key)
		req.Header.Set("Content-Type", "application/json")
		return (&http.Client{Timeout: 10 * time.Second}).Do(req)
	}
	done := make(chan error, 1)
	go func() {
		response, err := request()
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			if response.StatusCode != 200 {
				err = fmt.Errorf("status %d", response.StatusCode)
			}
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach upstream")
	}
	doubled := *price
	doubled.InputPerMillionMicro *= 2
	doubled.OutputPerMillionMicro *= 2
	doubled.CacheReadPerMillionMicro *= 2
	doubled.CacheWritePerMillionMicro *= 2
	second := save(1, "provider-usage", &doubled)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	response, err := request()
	if err != nil {
		t.Fatal(err)
	}
	readBody(response)
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	save(2, "provider-usage", nil)
	response, err = request()
	if err != nil {
		t.Fatal(err)
	}
	readBody(response)
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	save(3, "provider-usage", price)
	response, err = request()
	if err != nil {
		t.Fatal(err)
	}
	readBody(response)
	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}
	rows, err := app.store.db.Query(`SELECT price_version,cost_micro FROM accounting_attempts ORDER BY started_at,id`)
	if err != nil {
		t.Fatal(err)
	}
	var versions []sql.NullString
	var costs []sql.NullInt64
	for rows.Next() {
		var version sql.NullString
		var cost sql.NullInt64
		if err := rows.Scan(&version, &cost); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
		costs = append(costs, cost)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(costs) != 4 || !costs[0].Valid || costs[0].Int64 != 187 || !costs[1].Valid || costs[1].Int64 != 374 || costs[2].Valid || costs[3].Valid || versions[0].String != first.Version || versions[1].String != second.Version || versions[2].Valid {
		t.Fatalf("versions=%v costs=%v", versions, costs)
	}
	// A failed lookup is never interpreted as an absent (free) price.
	failLookup.Store(true)
	response, err = request()
	if err != nil {
		t.Fatal(err)
	}
	body := readBody(response)
	if response.StatusCode != 503 || calls.Load() != 4 {
		t.Fatalf("lookup failure status=%d calls=%d body=%s", response.StatusCode, calls.Load(), body)
	}
	assertUsageHTTPCounts(t, app, 5, 4)
	assertUsageHTTPActiveCleared(t, app)
	failLookup.Store(false)
	// Existing upstream identifiers can be longer than the public alias limit.
	longModel := strings.Repeat("x", 200) + " internal-name"
	if _, err := app.store.db.Exec(`UPDATE models SET upstream_model=? WHERE id='usage-chat'`, longModel); err != nil {
		t.Fatal(err)
	}
	response, err = request()
	if err != nil {
		t.Fatal(err)
	}
	readBody(response)
	if response.StatusCode != 200 {
		t.Fatalf("legacy long unpriced model: %d", response.StatusCode)
	}
	save(0, longModel, price)
	response, err = request()
	if err != nil {
		t.Fatal(err)
	}
	readBody(response)
	if response.StatusCode != 200 {
		t.Fatalf("long priced model: %d", response.StatusCode)
	}
	var longCost sql.NullInt64
	if err := app.store.db.QueryRow(`SELECT cost_micro FROM accounting_attempts ORDER BY started_at DESC,id DESC LIMIT 1`).Scan(&longCost); err != nil {
		t.Fatal(err)
	}
	if !longCost.Valid || longCost.Int64 != 187 {
		t.Fatalf("long model price=%v", longCost)
	}
}
