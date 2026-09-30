package service

// Independently authored integration tests for Key-to-account-pool-group
// narrowing. All upstreams are local synthetic handlers.

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/keypolicy"
)

func TestKeyAccountGroupPolicyFiltersCatalogAndDispatch(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_group","object":"response","created_at":1,"model":"actual-model","status":"completed","output":[{"id":"msg_group","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	installKeyAccountGroupPool(t, fixture.app, "wire-model", "grp_allowed", "chn_allowed")
	installAccountGroup(t, fixture.app, "grp_other", "chn_other")
	policy := replaceKeyAccountGroups(t, fixture.app, fixture.key.ID, 1, keypolicy.ModeSelected, []string{"grp_allowed"})

	catalog := employeeRequest(t, http.MethodGet, fixture.server.URL+"/v1/models", "", fixture.key.Key, context.Background())
	catalogBody := readBody(catalog)
	if catalog.StatusCode != http.StatusOK || !strings.Contains(catalogBody, `"wire-model"`) {
		t.Fatalf("allowed catalog status=%d body=%s", catalog.StatusCode, catalogBody)
	}

	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"wire-model","input":"hello"}`, fixture.key.Key, context.Background())
	responseBody := readBody(response)
	if response.StatusCode != http.StatusOK || !strings.Contains(responseBody, `"id":"resp_group"`) || calls.Load() != 1 {
		t.Fatalf("allowed response status=%d body=%s calls=%d", response.StatusCode, responseBody, calls.Load())
	}
	var attemptsBefore int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attemptsBefore); err != nil || attemptsBefore != 1 {
		t.Fatalf("allowed attempts=%d err=%v", attemptsBefore, err)
	}

	policy = replaceKeyAccountGroups(t, fixture.app, fixture.key.ID, policy.Revision, keypolicy.ModeSelected, []string{"grp_other"})
	deniedCatalog := employeeRequest(t, http.MethodGet, fixture.server.URL+"/v1/models", "", fixture.key.Key, context.Background())
	deniedCatalogBody := readBody(deniedCatalog)
	if deniedCatalog.StatusCode != http.StatusOK || strings.Contains(deniedCatalogBody, `"wire-model"`) {
		t.Fatalf("cross-group catalog status=%d body=%s", deniedCatalog.StatusCode, deniedCatalogBody)
	}
	denied := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"wire-model","input":"blocked"}`, fixture.key.Key, context.Background())
	deniedBody := readBody(denied)
	if denied.StatusCode != http.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatalf("cross-group response status=%d body=%s calls=%d", denied.StatusCode, deniedBody, calls.Load())
	}

	policy = replaceKeyAccountGroups(t, fixture.app, fixture.key.ID, policy.Revision, keypolicy.ModeSelected, []string{})
	if policy.AccountGroupMode != keypolicy.ModeSelected || len(policy.AccountGroupIDs) != 0 {
		t.Fatalf("selected-empty policy=%+v", policy)
	}
	deniedEmpty := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"wire-model","input":"blocked"}`, fixture.key.Key, context.Background())
	deniedEmptyBody := readBody(deniedEmpty)
	if deniedEmpty.StatusCode != http.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatalf("selected-empty response status=%d body=%s calls=%d", deniedEmpty.StatusCode, deniedEmptyBody, calls.Load())
	}
	var attemptsAfter int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attemptsAfter); err != nil || attemptsAfter != attemptsBefore {
		t.Fatalf("denied attempts before=%d after=%d err=%v", attemptsBefore, attemptsAfter, err)
	}
}

func TestKeyAccountGroupPolicyAppliesAcrossAllClientProtocols(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_group_protocols","object":"response","created_at":1,"model":"actual-model","status":"completed","output":[{"id":"msg_group_protocols","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
	}))
	installKeyAccountGroupPool(t, fixture.app, "wire-model", "grp_protocols", "chn_protocols")
	policy := replaceKeyAccountGroups(t, fixture.app, fixture.key.ID, 1, keypolicy.ModeSelected, []string{"grp_protocols"})

	requests := []struct {
		name string
		do   func() *http.Response
	}{
		{"openai-chat", func() *http.Response {
			return employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", `{"model":"wire-model","messages":[{"role":"user","content":"hi"}]}`, fixture.key.Key, context.Background())
		}},
		{"openai-responses", func() *http.Response {
			return employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"wire-model","input":"hi"}`, fixture.key.Key, context.Background())
		}},
		{"anthropic-messages", func() *http.Response {
			return anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", `{"model":"wire-model","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`, fixture.key.Key, "x-api-key")
		}},
		{"gemini-generate-content", func() *http.Response {
			return geminiEmployeeKeyRequest(t, http.MethodPost, fixture.server.URL+"/v1beta/models/wire-model:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, fixture.key.Key, context.Background())
		}},
	}
	for index, test := range requests {
		t.Run(test.name+"/allowed", func(t *testing.T) {
			response := test.do()
			body := readBody(response)
			if response.StatusCode != http.StatusOK || calls.Load() != int32(index+1) {
				t.Fatalf("status=%d body=%s calls=%d", response.StatusCode, body, calls.Load())
			}
		})
	}
	var attemptsBefore int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attemptsBefore); err != nil || attemptsBefore != len(requests) {
		t.Fatalf("allowed attempts=%d err=%v", attemptsBefore, err)
	}

	replaceKeyAccountGroups(t, fixture.app, fixture.key.ID, policy.Revision, keypolicy.ModeSelected, []string{})
	for _, test := range requests {
		t.Run(test.name+"/selected-empty", func(t *testing.T) {
			response := test.do()
			body := readBody(response)
			if response.StatusCode != http.StatusServiceUnavailable || calls.Load() != int32(len(requests)) {
				t.Fatalf("status=%d body=%s calls=%d", response.StatusCode, body, calls.Load())
			}
		})
	}
	var attemptsAfter int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attemptsAfter); err != nil || attemptsAfter != attemptsBefore {
		t.Fatalf("selected-empty attempts before=%d after=%d err=%v", attemptsBefore, attemptsAfter, err)
	}
}

func TestKeyAccountGroupPolicyAppliesToMessagesCountTokens(t *testing.T) {
	var calls atomic.Int32
	fixture := newAnthropicFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":7}`)
	}))
	installKeyAccountGroupPool(t, fixture.app, "company-claude", "grp_count_tokens", "chn_count_tokens")
	policy := replaceKeyAccountGroups(t, fixture.app, fixture.key.ID, 1, keypolicy.ModeSelected, []string{"grp_count_tokens"})
	body := `{"model":"company-claude","max_tokens":8,"messages":[{"role":"user","content":"count"}]}`
	allowed := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages/count_tokens", body, fixture.key.Key, "bearer")
	allowedBody := readBody(allowed)
	if allowed.StatusCode != http.StatusOK || allowedBody != `{"input_tokens":7}` || calls.Load() != 1 {
		t.Fatalf("allowed count status=%d body=%s calls=%d", allowed.StatusCode, allowedBody, calls.Load())
	}
	replaceKeyAccountGroups(t, fixture.app, fixture.key.ID, policy.Revision, keypolicy.ModeSelected, []string{})
	denied := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages/count_tokens", body, fixture.key.Key, "bearer")
	deniedBody := readBody(denied)
	if denied.StatusCode != http.StatusServiceUnavailable || calls.Load() != 1 {
		t.Fatalf("selected-empty count status=%d body=%s calls=%d", denied.StatusCode, deniedBody, calls.Load())
	}
	var leases int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_runtime_leases`).Scan(&leases); err != nil || leases != 0 {
		t.Fatalf("count token leases=%d err=%v", leases, err)
	}
}

func TestKeyAccountGroupAdminOmissionPreserveResetAndValidation(t *testing.T) {
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	installAccountGroup(t, fixture.app, "grp_admin", "chn_admin")
	cookie, csrf := loginTestAdmin(t, fixture.server.URL)

	initial := requestJSON(t, http.MethodGet, fixture.server.URL+"/admin/api/v1/keys/"+fixture.key.ID+"/policy", "", cookie, "", "")
	if initial.StatusCode != http.StatusOK {
		t.Fatalf("initial status=%d body=%s", initial.StatusCode, readBody(initial))
	}
	var initialView keyPolicyView
	decodeResponse(t, initial, &initialView)
	if initialView.AccountGroupMode != keypolicy.ModeAll || len(initialView.AccountGroupIDs) != 0 {
		t.Fatalf("default account group policy=%+v", initialView)
	}
	selected := replaceKeyAccountGroups(t, fixture.app, fixture.key.ID, initialView.Revision, keypolicy.ModeSelected, []string{"grp_admin"})

	preservedBody := marshalTestJSON(t, map[string]any{
		"expected_revision": selected.Revision,
		"protocol_mode":     "all", "protocols": []string{},
		"model_mode": "all", "models": []string{},
		"source_mode": "all", "source_cidrs": []string{},
	})
	preserved := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/"+fixture.key.ID+"/policy", preservedBody, cookie, csrf, fixture.server.URL)
	if preserved.StatusCode != http.StatusOK {
		t.Fatalf("preserve status=%d body=%s", preserved.StatusCode, readBody(preserved))
	}
	var preservedView keyPolicyView
	decodeResponse(t, preserved, &preservedView)
	if preservedView.AccountGroupMode != keypolicy.ModeSelected || len(preservedView.AccountGroupIDs) != 1 || preservedView.AccountGroupIDs[0] != "grp_admin" {
		t.Fatalf("preserved account group policy=%+v", preservedView)
	}

	oneSidedBody := marshalTestJSON(t, map[string]any{
		"expected_revision": preservedView.Revision,
		"protocol_mode":     "all", "protocols": []string{},
		"model_mode": "all", "models": []string{},
		"account_group_mode": "all",
	})
	oneSided := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/"+fixture.key.ID+"/policy", oneSidedBody, cookie, csrf, fixture.server.URL)
	if oneSided.StatusCode != http.StatusBadRequest {
		t.Fatalf("one-sided status=%d body=%s", oneSided.StatusCode, readBody(oneSided))
	}
	oneSided.Body.Close()

	unknownBody := marshalTestJSON(t, map[string]any{
		"expected_revision": preservedView.Revision,
		"protocol_mode":     "all", "protocols": []string{},
		"model_mode": "all", "models": []string{},
		"account_group_mode": "selected", "account_group_ids": []string{"grp_missing"},
	})
	unknown := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/"+fixture.key.ID+"/policy", unknownBody, cookie, csrf, fixture.server.URL)
	if unknown.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown status=%d body=%s", unknown.StatusCode, readBody(unknown))
	}
	unknown.Body.Close()

	resetBody := marshalTestJSON(t, map[string]any{
		"expected_revision": preservedView.Revision,
		"protocol_mode":     "all", "protocols": []string{},
		"model_mode": "all", "models": []string{},
		"account_group_mode": "all", "account_group_ids": []string{},
	})
	reset := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/keys/"+fixture.key.ID+"/policy", resetBody, cookie, csrf, fixture.server.URL)
	if reset.StatusCode != http.StatusOK {
		t.Fatalf("reset status=%d body=%s", reset.StatusCode, readBody(reset))
	}
	var resetView keyPolicyView
	decodeResponse(t, reset, &resetView)
	if resetView.AccountGroupMode != keypolicy.ModeAll || len(resetView.AccountGroupIDs) != 0 || resetView.Revision != preservedView.Revision+1 {
		t.Fatalf("reset account group policy=%+v", resetView)
	}
	var updates int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_audit WHERE action='key_policy.update' AND target_id=?`, fixture.key.ID).Scan(&updates); err != nil || updates != 2 {
		t.Fatalf("minimal policy audits=%d err=%v", updates, err)
	}
}

func TestKeyAccountGroupFinalTransactionRejectsMappingTightening(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	accountID := installKeyAccountGroupPool(t, fixture.app, "wire-model", "grp_initial", "chn_initial")
	installAccountGroup(t, fixture.app, "grp_tightened", "chn_tightened")
	policy := replaceKeyAccountGroups(t, fixture.app, fixture.key.ID, 1, keypolicy.ModeSelected, []string{"grp_initial"})

	auth, valid := fixture.app.lookupEmployeeKey(context.Background(), fixture.key.Key)
	if !valid || auth.Policy.Revision != policy.Revision {
		t.Fatalf("lookup valid=%v auth=%+v", valid, auth)
	}
	ctx := context.WithValue(context.Background(), requestIDKey{}, "request-key-group-final-tx")
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)).WithContext(ctx)
	var failure *modelAdmissionError
	auth, failure = fixture.app.resolveKeySource(auth, r, true)
	if failure != nil {
		t.Fatalf("source failure=%+v", failure)
	}
	auth, failure = authorizeKeyPolicy(auth, keypolicy.ProtocolOpenAIResponses, "wire-model")
	if failure != nil {
		t.Fatalf("policy failure=%+v", failure)
	}
	selected, lease, failure := fixture.app.prepareModelRoute(r, auth, "wire-model", []string{"openai-compatible"}, accounting.ProtocolOpenAIResponses, true, func(_ *http.Request, selected route) (route, *modelPreflightError) {
		return selected, nil
	})
	if failure != nil || lease == nil || selected.AccountID != accountID {
		t.Fatalf("prepare selected=%+v lease=%v failure=%+v", selected, lease, failure)
	}
	defer fixture.app.releaseModelLease(lease, requestID(ctx), true)
	if _, err := fixture.app.store.db.Exec(`UPDATE model_account_pool_routes SET channel_id='chn_tightened' WHERE model_id='wire-model' AND upstream_id=?`, accountID); err != nil {
		t.Fatal(err)
	}
	client, dispatchFailure := fixture.app.dispatchModelRoute(r, auth, "wire-model", selected, lease, true)
	if client != nil || dispatchFailure == nil {
		t.Fatalf("tightened mapping dispatched client=%v failure=%+v", client, dispatchFailure)
	}
	assertDispatchAttemptCount(t, fixture.app, requestID(ctx), 0)
	if calls.Load() != 0 {
		t.Fatalf("tightened mapping made %d upstream calls", calls.Load())
	}
	fixture.app.finishRequest(requestID(ctx), "failed", 0)
}

func TestKeyAccountGroupPolicyNarrowsResourcesButPreservesOwnerStop(t *testing.T) {
	coordinator, _ := newResponseResourceTestCoordinator(t)
	now := time.Date(2026, 9, 29, 8, 0, 0, 0, time.UTC)
	coordinator.now = func() time.Time { return now }
	completed, err := coordinator.Create(context.Background(), responseResourceCreateInput{
		OperationID: "op_group_resource_completed", EmployeeID: "emp_one", KeyID: "key_one", PublicModel: "model_one",
		ProviderKind: "openai-compatible", StoreBody: true, CreatedAt: now, TerminalAt: responseTimePointer(now.Add(time.Second)),
		SourceAddr: netip.MustParseAddr("127.0.0.1"), PolicyRevision: 1,
		Items: []responseStateItem{{Type: "message", Payload: []byte(`{"type":"message","role":"user","content":"synthetic"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	queued, err := coordinator.Create(context.Background(), responseResourceCreateInput{
		OperationID: "op_group_resource_queued", EmployeeID: "emp_one", KeyID: "key_one", PublicModel: "model_one",
		ProviderKind: "openai-compatible", Background: true, StoreBody: true, CreatedAt: now,
		SourceAddr: netip.MustParseAddr("127.0.0.1"), PolicyRevision: 1,
		Items: []responseStateItem{{Type: "message", Payload: []byte(`{"type":"message","role":"user","content":"synthetic"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	installKeyAccountGroupPool(t, coordinator.app, "model_one", "grp_resource_allowed", "chn_resource_allowed")
	installAccountGroup(t, coordinator.app, "grp_resource_other", "chn_resource_other")
	replaceKeyAccountGroups(t, coordinator.app, "key_one", 1, keypolicy.ModeSelected, []string{"grp_resource_other"})

	auth := employeeAuth{EmployeeID: "emp_one", KeyID: "key_one", SourceAddr: netip.MustParseAddr("127.0.0.1")}
	if _, err := coordinator.Get(context.Background(), auth, completed.ID, true); !errors.Is(err, errResponseResourceForbidden) {
		t.Fatalf("cross-group resource body read err=%v", err)
	}
	if cancelled, err := coordinator.Cancel(context.Background(), auth, queued.ID); err != nil || cancelled.Status != "cancelled" {
		t.Fatalf("cross-group cancel status=%q err=%v", cancelled.Status, err)
	}
	if err := coordinator.Delete(context.Background(), auth, completed.ID); err != nil {
		t.Fatalf("cross-group delete: %v", err)
	}
}

func TestKeyAccountGroupMappingTighteningInterruptsBackgroundBeforeDispatch(t *testing.T) {
	coordinator, db := newResponseResourceTestCoordinator(t)
	// claimOne uses the real clock and only claims unexpired tasks. Keep this
	// queued task live so the test exercises the tightened policy, not TTL.
	now := time.Now().UTC().Truncate(time.Second)
	coordinator.now = func() time.Time { return now }
	installKeyAccountGroupPool(t, coordinator.app, "model_one", "grp_background_allowed", "chn_background_allowed")
	installAccountGroup(t, coordinator.app, "grp_background_tightened", "chn_background_tightened")
	policy := replaceKeyAccountGroups(t, coordinator.app, "key_one", 1, keypolicy.ModeSelected, []string{"grp_background_allowed"})
	created, err := coordinator.Create(context.Background(), responseResourceCreateInput{
		OperationID: "op_group_background", EmployeeID: "emp_one", KeyID: "key_one", PublicModel: "model_one",
		ProviderKind: "openai-compatible", Background: true, StoreBody: true, CreatedAt: now,
		SourceAddr: netip.MustParseAddr("127.0.0.1"), PolicyRevision: policy.Revision,
		Items: []responseStateItem{{Type: "message", Payload: []byte(`{"type":"message","role":"user","content":"queued"}`)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE model_account_pool_routes SET channel_id='chn_background_tightened' WHERE model_id='model_one' AND upstream_id='ups_one'`); err != nil {
		t.Fatal(err)
	}
	coordinator.app.responseResources = coordinator
	worker := &backgroundResponseWorker{app: coordinator.app, ctx: context.Background()}
	if claim, err := worker.claimOne(); err == nil || claim != nil {
		t.Fatalf("tightened background claim=%+v err=%v", claim, err)
	}
	var taskStatus, responseStatus, requestStatus string
	var claimToken sql.NullString
	if err := db.QueryRow(`SELECT t.status,r.status,a.status,t.claim_token FROM background_tasks t JOIN response_resources r ON r.id=t.response_id JOIN accounting_requests a ON a.id=t.request_id WHERE t.id=?`, created.TaskID).
		Scan(&taskStatus, &responseStatus, &requestStatus, &claimToken); err != nil {
		t.Fatal(err)
	}
	if taskStatus != "interrupted" || responseStatus != "interrupted" || requestStatus != "interrupted" || claimToken.Valid {
		t.Fatalf("tightened background statuses=%s/%s/%s claim=%v", taskStatus, responseStatus, requestStatus, claimToken)
	}
	for _, table := range []string{"model_requests", "accounting_attempts"} {
		var count int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d err=%v", table, count, err)
		}
	}
}

func installAccountGroup(t *testing.T, app *App, groupID, channelID string) {
	t.Helper()
	tx, err := app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	createdAt := time.Now().UTC()
	if _, err := tx.Exec(`INSERT INTO account_groups(id,name,revision,created_at) VALUES(?,?,1,?)`, groupID, groupID, createdAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var allocationTables int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='account_group_allocation_versions'`).Scan(&allocationTables); err != nil {
		t.Fatal(err)
	}
	if allocationTables == 1 {
		if _, err := createDefaultAccountGroupAllocationTx(context.Background(), tx, groupID, 1, createdAt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO account_channels(id,name,group_id,revision,created_at) VALUES(?,?,?,1,?)`, channelID, channelID, groupID, createdAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func installKeyAccountGroupPool(t *testing.T, app *App, model, groupID, channelID string) string {
	t.Helper()
	installAccountGroup(t, app, groupID, channelID)
	var accountID, upstreamModel, wire string
	if err := app.store.db.QueryRow(`SELECT upstream_id,upstream_model,wire_protocol FROM models WHERE id=?`, model).Scan(&accountID, &upstreamModel, &wire); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES(?,1,?)`, model, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,wire_protocol,priority,weight,max_concurrency,channel_id,position) VALUES(?,?,?,?,0,1,4,?,0)`, model, accountID, upstreamModel, wire, channelID); err != nil {
		t.Fatal(err)
	}
	app.notifyAccountPoolChanged()
	return accountID
}

func replaceKeyAccountGroups(t *testing.T, app *App, keyID string, expected int64, mode keypolicy.Mode, groupIDs []string) keypolicy.Policy {
	t.Helper()
	policy, err := keypolicy.Replace(context.Background(), app.store.db, keyID, expected, keypolicy.Replacement{
		ProtocolMode: keypolicy.ModeAll, Protocols: []keypolicy.ClientProtocol{},
		ModelMode: keypolicy.ModeAll, Models: []string{},
		SourceMode: keypolicy.ModeAll, SourceCIDRs: []string{},
		AccountGroupMode: mode, AccountGroupIDs: groupIDs,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	app.notifyAccountPoolChanged()
	return policy
}
