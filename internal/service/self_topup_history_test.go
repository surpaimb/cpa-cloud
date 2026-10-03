// Independently authored tests for docs/employee-self-topup-credit-history-contract.md.
package service

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

func TestSelfTopupCreditHistoryDisabledPreMuxNoRedirect(t *testing.T) {
	f := newSelfAdminAdjustmentFixture(t) // Existing self session; the new capability stays off.
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("synthetic SPA"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.app.cfg.WebDir = webDir
	server := httptest.NewServer(f.app.Handler())
	defer server.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, path := range []string{
		"/self/api/v1/billing/topup-credits",
		"/self/api/v1/billing/topup-credits/",
		"/self/api/v1/billing/./topup-credits",
		"/self//api/v1/billing/topup-credits",
		"/self/api/v1/billing%2Ftopup-credits",
		"/self/api/v1/billing%255Ctopup-credits",
		"/self/api/v1/billing/topup-credits/../other",
		"/SELF/api/v1/billing/topup-credits",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			request, err := http.NewRequest(method, server.URL+path+"?currency=USD", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != http.StatusNotFound || response.Header.Get("Cache-Control") != "no-store" ||
				response.Header.Get("Location") != "" || response.Header.Get("Allow") != "" {
				t.Fatalf("%s %q: status=%d cache=%q location=%q allow=%q", method, path, response.StatusCode,
					response.Header.Get("Cache-Control"), response.Header.Get("Location"), response.Header.Get("Allow"))
			}
		}
	}
}

func newSelfTopupCreditFixture(t *testing.T) selfWalletFixture {
	t.Helper()
	f := newSelfAdminAdjustmentFixture(t)
	f.app.cfg.EmployeeSelfTopupCreditHistoryEnabled = true
	return f
}

func postSelfTopupCredit(t *testing.T, f selfWalletFixture, operation string, amount int64, at time.Time) financial.TopUp {
	t.Helper()
	ctx := context.Background()
	commercial := financial.NewCommercial(f.app.store.db)
	var adminID string
	if err := f.app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	meta := func(id string) financial.WriteMeta {
		digest := sha256.Sum256([]byte("synthetic-" + id))
		return financial.WriteMeta{OperationID: id, ActorAdminID: adminID, PayloadDigest: digest, ObservedAt: at}
	}
	var enabled int
	var revision int64
	if err := f.app.store.db.QueryRow(`SELECT enabled,revision FROM financial_settings WHERE singleton=1`).Scan(&enabled, &revision); err != nil {
		t.Fatal(err)
	}
	if enabled == 0 {
		if _, err := commercial.SetEnabled(ctx, meta("topup-enable-"+operation), revision, true); err != nil {
			t.Fatal(err)
		}
	}
	var connectorID string
	if err := f.app.store.db.QueryRow(`SELECT id FROM financial_payment_connectors LIMIT 1`).Scan(&connectorID); err != nil {
		connector, _, createErr := commercial.CreateConnector(ctx, financial.CreateConnector{Meta: meta("connector-" + operation),
			Name: "Synthetic", SecretCiphertext: []byte("synthetic-encrypted-secret"), Enabled: true})
		if createErr != nil {
			t.Fatal(createErr)
		}
		connectorID = connector.ID
	}
	topup, _, err := commercial.CreateTopUp(ctx, financial.CreateTopUp{Meta: meta("create-" + operation),
		Owner: financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}, ConnectorID: connectorID, Currency: "USD", AmountMicro: amount})
	if err != nil {
		t.Fatal(err)
	}
	paid, err := commercial.ApplyPaid(ctx, financial.ApplyPayment{ConnectorID: connectorID, EventID: "event-" + operation,
		PaymentID: topup.PaymentID, ExternalReference: topup.ExternalReference, Currency: "USD", AmountMicro: amount,
		PayloadDigest: sha256.Sum256([]byte("synthetic-event-" + operation)), SignedAt: at, ObservedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	return paid
}

func selfTopupRequest(t *testing.T, base, query string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, base+selfTopupCreditPath+query, "", "", cookie, "")
}

func TestSelfTopupCreditHistoryStrictRouteAndCursor(t *testing.T) {
	for _, cfg := range []Config{
		{EmployeeSelfTopupCreditHistoryEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfTopupCreditHistoryEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfTopupCreditHistoryEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true, EmployeeSelfTopupCreditHistoryEnabled: true},
	} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing prerequisite: %+v", cfg)
		}
	}
	f := newSelfTopupCreditFixture(t)
	webDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(webDir, "index.html"), []byte("synthetic SPA"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.app.cfg.WebDir = webDir
	server := httptest.NewServer(f.app.Handler())
	defer server.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, shaped := range []string{"/self/api/v1/billing/./topup-credits", "/self//api/v1/billing/topup-credits",
		"/self/api/v1/billing%2Ftopup-credits", "/self/api/v1/billing%255Ctopup-credits",
		"/self/api/v1/billing/topup-credits/../other", "/SELF/api/v1/billing/topup-credits"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			request, err := http.NewRequest(method, server.URL+shaped+"?currency=USD", http.NoBody)
			if err != nil {
				t.Fatal(err)
			}
			request.AddCookie(f.cookie)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if response.StatusCode != 400 || response.Header.Get("Location") != "" || response.Header.Get("Allow") != "" || response.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("%s %s: status=%d location=%q allow=%q", method, shaped, response.StatusCode, response.Header.Get("Location"), response.Header.Get("Allow"))
			}
		}
	}
	readSelfWalletResponse(t, selfTopupRequest(t, server.URL, "?currency=USD", nil), 401)
	readSelfWalletResponse(t, selfTopupRequest(t, server.URL, "?currency=USD", f.adminCookie), 401)
	readSelfWalletResponse(t, selfTopupRequest(t, server.URL, "?currency=USD&limit=1", f.cookie), 200)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, server.URL+selfTopupCreditPath+"?currency=USD", "", "http://invalid.example", f.cookie, ""), 403)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, server.URL+"/self/api/v1/billing/./topup-credits?currency=USD", "", "", nil, ""), 401)
	for _, query := range []string{"", "?", "?currency=usd", "?currency=USD&currency=EUR", "?currency=US%44",
		"?currency=USD&limit=01", "?currency=USD&cursor=abc", "?currency=USD&other=x"} {
		readSelfWalletResponse(t, selfTopupRequest(t, server.URL, query, f.cookie), 400)
	}
	wrong := selfRequestTest(t, http.MethodPost, server.URL+selfTopupCreditPath+"?currency=USD", "", "", f.cookie, "")
	if wrong.StatusCode != 405 || wrong.Header.Get("Allow") != "GET" {
		t.Fatalf("wrong method=%d allow=%q", wrong.StatusCode, wrong.Header.Get("Allow"))
	}
	wrong.Body.Close()
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, server.URL+selfTopupCreditPath+"?currency=USD", `{}`, "", f.cookie, ""), 400)
	missing := readSelfWalletResponse(t, selfTopupRequest(t, server.URL, "?currency=USD", f.cookie), 200)
	if len(missing) != 6 || missing["has_account"] != false || len(missing["items"].([]any)) != 0 {
		t.Fatalf("missing=%+v", missing)
	}
	postSelfTopupCredit(t, f, "first", 42, time.Now().UTC().Add(-2*time.Second))
	postSelfTopupCredit(t, f, "second", 43, time.Now().UTC().Add(-time.Second))
	first := readSelfWalletResponse(t, selfTopupRequest(t, server.URL, "?currency=USD&limit=1", f.cookie), 200)
	if len(first) != 6 || len(first["items"].([]any)) != 1 || first["items"].([]any)[0].(map[string]any)["amount_micro"] != "43" {
		t.Fatalf("first=%+v", first)
	}
	cursor, ok := first["next_cursor"].(string)
	if !ok || cursor == "" || strings.Contains(cursor, f.id) {
		t.Fatalf("cursor=%q", cursor)
	}
	second := readSelfWalletResponse(t, selfTopupRequest(t, server.URL, "?currency=USD&limit=1&cursor="+cursor, f.cookie), 200)
	if second["next_cursor"] != nil || second["items"].([]any)[0].(map[string]any)["amount_micro"] != "42" {
		t.Fatalf("second=%+v", second)
	}
	readSelfWalletResponse(t, selfTopupRequest(t, server.URL, "?currency=EUR&limit=1&cursor="+cursor, f.cookie), 400)
	readSelfWalletResponse(t, selfTopupRequest(t, server.URL, "?currency=USD&limit=2&cursor="+cursor, f.cookie), 400)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, server.URL+"/self/api/v1/billing/entries?currency=USD&limit=1&cursor="+cursor, "", "", f.cookie, ""), 400)
	selector := strings.Split(f.cookie.Value, ".")[0]
	if _, err := f.app.decodeSelfTopupCreditCursor(cursor, selfSession{Selector: selector, EmployeeID: f.id},
		selfWalletActivityRequest{currency: "USD", limit: 1}, time.Now().UTC().Add(16*time.Minute)); err == nil {
		t.Fatal("expired cursor accepted")
	}
	if _, err := f.app.decodeSelfTopupCreditCursor(cursor, selfSession{Selector: "different-session", EmployeeID: f.id},
		selfWalletActivityRequest{currency: "USD", limit: 1}, time.Now().UTC()); err == nil {
		t.Fatal("cross-session cursor accepted")
	}
	otherInstall := newSelfTopupCreditFixture(t)
	if _, err := otherInstall.app.decodeSelfTopupCreditCursor(cursor, selfSession{Selector: selector, EmployeeID: f.id},
		selfWalletActivityRequest{currency: "USD", limit: 1}, time.Now().UTC()); err == nil {
		t.Fatal("cross-install cursor accepted")
	}
	f.app.selfTopupCreditNonce = func([]byte) (int, error) { return 0, errors.New("synthetic RNG failure") }
	readSelfWalletResponse(t, selfTopupRequest(t, server.URL, "?currency=USD&limit=1", f.cookie), 503)
	f.app.selfTopupCreditNonce = nil
}

func TestSelfTopupCreditHistoryFinalOutputSerializesLogout(t *testing.T) {
	f := newSelfTopupCreditFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f.app.selfTopupCreditBeforeWrite = func() { close(entered); <-release }
	defer func() { f.app.selfTopupCreditBeforeWrite = nil }()
	server := httptest.NewServer(f.app.Handler())
	defer server.Close()
	type outcome struct {
		status int
		err    error
	}
	readDone := make(chan outcome, 1)
	go func() {
		request, err := http.NewRequest(http.MethodGet, server.URL+selfTopupCreditPath+"?currency=USD", nil)
		if err != nil {
			readDone <- outcome{err: err}
			return
		}
		request.AddCookie(f.cookie)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			readDone <- outcome{err: err}
			return
		}
		defer response.Body.Close()
		readDone <- outcome{status: response.StatusCode}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("read did not reach final output")
	}
	logoutDone := make(chan outcome, 1)
	go func() {
		request, err := http.NewRequest(http.MethodDelete, server.URL+"/self/api/v1/sessions", nil)
		if err != nil {
			logoutDone <- outcome{err: err}
			return
		}
		request.AddCookie(f.cookie)
		request.Header.Set("Origin", server.URL)
		request.Header.Set("X-Self-Request", "1")
		request.Header.Set("X-CSRF-Token", f.csrf)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			logoutDone <- outcome{err: err}
			return
		}
		defer response.Body.Close()
		logoutDone <- outcome{status: response.StatusCode}
	}()
	select {
	case result := <-logoutDone:
		t.Fatalf("logout overtook final output: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for _, result := range []struct {
		name string
		want int
		done chan outcome
	}{{"read", 200, readDone}, {"logout", 204, logoutDone}} {
		select {
		case got := <-result.done:
			if got.err != nil || got.status != result.want {
				t.Fatalf("%s result=%+v", result.name, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not complete", result.name)
		}
	}
	readSelfWalletResponse(t, selfTopupRequest(t, server.URL, "?currency=USD", f.cookie), 401)
}
