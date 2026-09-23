package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenAIPreflightFailoverUsesSecondActualModelWithoutPhantomAttempt(t *testing.T) {
	protocols := []struct {
		name     string
		path     string
		body     string
		response string
	}{
		{name: "chat", path: "/v1/chat/completions", body: `{"model":"preflight-model","messages":[]}`, response: `{"id":"chat_preflight","object":"chat.completion","choices":[]}`},
		{name: "responses", path: "/v1/responses", body: `{"model":"preflight-model","input":"synthetic"}`, response: `{"id":"resp_preflight","object":"response","status":"completed","output":[]}`},
	}
	for _, protocol := range protocols {
		t.Run(protocol.name, func(t *testing.T) {
			var abandonedCalls, dispatchedCalls atomic.Int32
			abandoned := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { abandonedCalls.Add(1) }))
			defer abandoned.Close()
			dispatched := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				dispatchedCalls.Add(1)
				if r.Header.Get("Authorization") != "Bearer second-secret" {
					t.Errorf("authorization=%q", r.Header.Get("Authorization"))
				}
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if string(payload["model"]) != `"actual-second"` {
					t.Errorf("actual model=%s", payload["model"])
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, protocol.response)
			}))
			defer dispatched.Close()

			app, server, cookie, csrf := newModelAdmissionApp(t, false)
			first := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "broken first", "openai-compatible", abandoned.URL, "first-secret")
			second := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "healthy second", "openai-compatible", dispatched.URL, "second-secret")
			createModelAdmissionModel(t, server.URL, cookie, csrf, "preflight-model", first.ID, "actual-legacy")
			putOpenAIPreflightPool(t, server.URL, cookie, csrf, "preflight-model", first.ID, second.ID)
			if _, err := app.store.db.Exec(`UPDATE upstreams SET credential_ciphertext=X'00' WHERE id=?`, first.ID); err != nil {
				t.Fatal(err)
			}
			employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Preflight employee")
			key := createTestKey(t, server.URL, employee.ID, "preflight-key", cookie, csrf)
			response := employeeRequest(t, http.MethodPost, server.URL+protocol.path, protocol.body, key.Key, context.Background())
			body := readBody(response)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
			if abandonedCalls.Load() != 0 || dispatchedCalls.Load() != 1 {
				t.Fatalf("abandoned calls=%d dispatched calls=%d", abandonedCalls.Load(), dispatchedCalls.Load())
			}
			assertOpenAIPreflightAccounting(t, app, "preflight-model", 1, second.ID, "failover")
		})
	}
}

func TestOpenAIPreflightStopsAfterSecondCandidate(t *testing.T) {
	var thirdCalls atomic.Int32
	thirdServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		thirdCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"unexpected","object":"chat.completion","choices":[]}`)
	}))
	defer thirdServer.Close()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	first := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "broken one", "openai-compatible", thirdServer.URL, "one")
	second := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "broken two", "openai-compatible", thirdServer.URL, "two")
	third := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "unused three", "openai-compatible", thirdServer.URL, "three")
	createModelAdmissionModel(t, server.URL, cookie, csrf, "preflight-cap", first.ID, "legacy")
	putOpenAIPreflightPoolItems(t, server.URL, cookie, csrf, "preflight-cap", []map[string]any{
		{"upstream_id": first.ID, "upstream_model": "actual-one", "priority": 30, "weight": 1, "max_concurrency": 1},
		{"upstream_id": second.ID, "upstream_model": "actual-two", "priority": 20, "weight": 1, "max_concurrency": 1},
		{"upstream_id": third.ID, "upstream_model": "actual-three", "priority": 10, "weight": 1, "max_concurrency": 1},
	})
	if _, err := app.store.db.Exec(`UPDATE upstreams SET credential_ciphertext=X'00' WHERE id IN (?,?)`, first.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Capped employee")
	key := createTestKey(t, server.URL, employee.ID, "preflight-cap-key", cookie, csrf)
	response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"preflight-cap","messages":[]}`, key.Key, context.Background())
	body := readBody(response)
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "no_available_route") {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if thirdCalls.Load() != 0 {
		t.Fatalf("third candidate dispatched %d times", thirdCalls.Load())
	}
	assertOpenAIPreflightAccounting(t, app, "preflight-cap", 0, "", "")
}

func TestOpenAIDispatchStatusDoesNotReplaySecondAccount(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	firstServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		firstCalls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer firstServer.Close()
	secondServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"must_not_run","object":"chat.completion","choices":[]}`)
	}))
	defer secondServer.Close()
	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	first := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "sent first", "openai-compatible", firstServer.URL, "one")
	second := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "must not replay", "openai-compatible", secondServer.URL, "two")
	createModelAdmissionModel(t, server.URL, cookie, csrf, "no-replay", first.ID, "legacy")
	putOpenAIPreflightPool(t, server.URL, cookie, csrf, "no-replay", first.ID, second.ID)
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "No replay employee")
	key := createTestKey(t, server.URL, employee.ID, "no-replay-key", cookie, csrf)
	response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"no-replay","messages":[]}`, key.Key, context.Background())
	body := readBody(response)
	if response.StatusCode != http.StatusBadGateway || !strings.Contains(body, "upstream_error") {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 0 {
		t.Fatalf("first calls=%d second calls=%d", firstCalls.Load(), secondCalls.Load())
	}
	assertOpenAIPreflightAccounting(t, app, "no-replay", 1, first.ID, "primary")
}

func putOpenAIPreflightPool(t *testing.T, baseURL string, cookie *http.Cookie, csrf, model, first, second string) {
	t.Helper()
	putOpenAIPreflightPoolItems(t, baseURL, cookie, csrf, model, []map[string]any{
		{"upstream_id": first, "upstream_model": "actual-first", "priority": 20, "weight": 1, "max_concurrency": 1},
		{"upstream_id": second, "upstream_model": "actual-second", "priority": 10, "weight": 1, "max_concurrency": 1},
	})
}

func putOpenAIPreflightPoolItems(t *testing.T, baseURL string, cookie *http.Cookie, csrf, model string, items []map[string]any) {
	t.Helper()
	response := requestJSON(t, http.MethodPut, baseURL+"/admin/api/v1/models/"+model+"/accounts",
		marshalTestJSON(t, map[string]any{"expected_revision": 0, "items": items}), cookie, csrf, baseURL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("configure pool status=%d body=%s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
}

func assertOpenAIPreflightAccounting(t *testing.T, app *App, model string, attempts int, account, dispatch string) {
	t.Helper()
	var parents int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM model_requests WHERE model_id=?`, model).Scan(&parents); err != nil || parents != 1 {
		t.Fatalf("parents=%d err=%v", parents, err)
	}
	var actual int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts a JOIN accounting_requests r ON r.id=a.request_id WHERE r.model_id=?`, model).Scan(&actual); err != nil || actual != attempts {
		t.Fatalf("attempts=%d wanted=%d err=%v", actual, attempts, err)
	}
	if attempts == 0 {
		return
	}
	var gotAccount, gotDispatch string
	err := app.store.db.QueryRow(`SELECT a.account_id,a.dispatch FROM accounting_attempts a JOIN accounting_requests r ON r.id=a.request_id WHERE r.model_id=?`, model).Scan(&gotAccount, &gotDispatch)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if gotAccount != account || gotDispatch != dispatch {
		t.Fatalf("attempt account=%q dispatch=%q", gotAccount, gotDispatch)
	}
}
