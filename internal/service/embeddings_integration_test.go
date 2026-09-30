package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

func TestOpenAIEmbeddingsExplicitModelRoutePolicyAndUsage(t *testing.T) {
	var calls atomic.Int32
	cancelStarted := make(chan struct{})
	cancelSeen := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/embeddings" || r.Method != http.MethodPost {
			t.Errorf("upstream request=%s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer embedding-secret" {
			t.Errorf("upstream authorization=%q", got)
		}
		var body struct {
			Model          string          `json:"model"`
			Input          json.RawMessage `json:"input"`
			EncodingFormat string          `json:"encoding_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "provider-embedding" || body.EncodingFormat != "float" {
			t.Errorf("upstream body=%+v", body)
		}
		var inputs []string
		count := 0
		if json.Unmarshal(body.Input, &inputs) == nil {
			count = len(inputs)
		} else {
			var one string
			var tokens []int64
			var batch [][]int64
			switch {
			case json.Unmarshal(body.Input, &one) == nil:
				inputs, count = []string{one}, 1
			case json.Unmarshal(body.Input, &tokens) == nil:
				count = 1
			case json.Unmarshal(body.Input, &batch) == nil:
				count = len(batch)
			default:
				t.Errorf("unexpected upstream input shape")
			}
		}
		if count == 0 {
			t.Errorf("empty upstream input")
		}
		if len(inputs) == 1 && inputs[0] == "cancel" || string(body.Input) == `[777,888]` {
			close(cancelStarted)
			<-r.Context().Done()
			close(cancelSeen)
			return
		}
		if len(inputs) == 1 && inputs[0] == "bad-response" {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":["secret-vector"],"index":0}],"model":"provider-embedding","usage":{"prompt_tokens":1,"total_tokens":1}}`))
			return
		}
		data := make([]map[string]any, count)
		for index := range data {
			data[index] = map[string]any{"object": "embedding", "embedding": []float64{float64(index) + 0.25, -0.5}, "index": index}
		}
		if string(body.Input) == `[[7,8],[9]]` {
			data[0], data[1] = data[1], data[0]
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": "provider-embedding", "usage": map[string]int{"prompt_tokens": count, "total_tokens": count}})
	}))
	defer upstream.Close()

	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	account := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "embedding", "openai-compatible", upstream.URL, "embedding-secret")
	create := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models", marshalTestJSON(t, map[string]any{
		"id": "company-embedding", "model_kind": "embedding", "upstream_id": account.ID, "upstream_model": "provider-embedding",
	}), cookie, csrf, server.URL)
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create embedding model status=%d body=%s", create.StatusCode, readBody(create))
	}
	create.Body.Close()
	pool := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/models/company-embedding/accounts", marshalTestJSON(t, map[string]any{
		"expected_revision": 0,
		"items":             []map[string]any{{"upstream_id": account.ID, "upstream_model": "provider-embedding", "wire_protocol": "openai-embeddings", "priority": 0, "weight": 1, "max_concurrency": 1}},
	}), cookie, csrf, server.URL)
	if pool.StatusCode != http.StatusOK {
		t.Fatalf("configure embedding pool status=%d body=%s", pool.StatusCode, readBody(pool))
	}
	pool.Body.Close()
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Embedding Employee")
	legacyKey := createTestKey(t, server.URL, employee.ID, "embedding-legacy-key", cookie, csrf)
	keyResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees/"+employee.ID+"/keys", marshalTestJSON(t, map[string]any{
		"name": "Embedding key", "operation_id": "embedding-explicit-key",
		"policy": map[string]any{"protocol_mode": "selected", "protocols": []string{"openai-embeddings"}, "model_mode": "all", "models": []string{}},
	}), cookie, csrf, server.URL)
	if keyResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create embedding key status=%d body=%s", keyResponse.StatusCode, readBody(keyResponse))
	}
	var embeddingKey keyView
	decodeResponse(t, keyResponse, &embeddingKey)
	modelsRequest, err := http.NewRequest(http.MethodGet, server.URL+"/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	modelsRequest.Header.Set("Authorization", "Bearer "+embeddingKey.Key)
	modelsResponse, err := http.DefaultClient.Do(modelsRequest)
	if err != nil {
		t.Fatal(err)
	}
	modelsBody := readBody(modelsResponse)
	if modelsResponse.StatusCode != http.StatusOK || !strings.Contains(modelsBody, `"id":"company-embedding"`) {
		t.Fatalf("embedding model list status=%d body=%s", modelsResponse.StatusCode, modelsBody)
	}

	denied := embeddingEmployeeRequest(t, server.URL, legacyKey.Key, `{"model":"company-embedding","input":"denied"}`)
	if denied.StatusCode != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("legacy key status=%d calls=%d body=%s", denied.StatusCode, calls.Load(), readBody(denied))
	}
	denied.Body.Close()
	deniedTokens := embeddingEmployeeRequest(t, server.URL, legacyKey.Key, `{"model":"company-embedding","input":[7,8]}`)
	if deniedTokens.StatusCode != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("legacy key token input status=%d calls=%d body=%s", deniedTokens.StatusCode, calls.Load(), readBody(deniedTokens))
	}
	deniedTokens.Body.Close()
	for _, badInput := range []string{
		`[]`, `[[]]`, `[1,"2"]`, `[[1],2]`, `[[1],[]]`,
		`[-1]`, `[-0]`, `[1.0]`, `[1e0]`, `[2147483648]`,
		`[123456789012345678901234567890]`, `[null]`, `[true]`,
	} {
		body := `{"model":"company-embedding","input":` + badInput + `}`
		response := embeddingEmployeeRequest(t, server.URL, embeddingKey.Key, body)
		raw := readBody(response)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(raw, "invalid_request_error") ||
			strings.Contains(raw, embeddingKey.Key) || strings.Contains(raw, "123456789012345678901234567890") || calls.Load() != 0 {
			t.Fatalf("bad token input=%s status=%d calls=%d body=%s", badInput, response.StatusCode, calls.Load(), raw)
		}
	}
	duplicate := embeddingEmployeeRequest(t, server.URL, embeddingKey.Key, `{"model":"company-embedding","input":[7],"input":[8]}`)
	if duplicate.StatusCode != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("duplicate input status=%d calls=%d body=%s", duplicate.StatusCode, calls.Load(), readBody(duplicate))
	}
	duplicate.Body.Close()
	var rejectedAttempts int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&rejectedAttempts); err != nil || rejectedAttempts != 0 {
		t.Fatalf("invalid token input persisted attempts=%d err=%v", rejectedAttempts, err)
	}

	for _, test := range []struct {
		body  string
		count int
	}{
		{`{"model":"company-embedding","input":"hello"}`, 1},
		{`{"model":"company-embedding","input":["hello","world"],"encoding_format":"float"}`, 2},
		{`{"model":"company-embedding","input":[7,8]}`, 1},
		{`{"model":"company-embedding","input":[[7,8],[9]]}`, 2},
	} {
		response := embeddingEmployeeRequest(t, server.URL, embeddingKey.Key, test.body)
		raw := readBody(response)
		if response.StatusCode != http.StatusOK || !strings.Contains(raw, `"model":"company-embedding"`) || strings.Contains(raw, "provider-embedding") {
			t.Fatalf("embedding status=%d body=%s", response.StatusCode, raw)
		}
		var result struct {
			Data []struct {
				Index int `json:"index"`
			} `json:"data"`
			Usage struct {
				PromptTokens int `json:"prompt_tokens"`
				TotalTokens  int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(raw), &result); err != nil || len(result.Data) != test.count || result.Usage.PromptTokens != test.count || result.Usage.TotalTokens != test.count {
			t.Fatalf("embedding count/usage mismatch: %s err=%v", raw, err)
		}
		for index, item := range result.Data {
			if item.Index != index {
				t.Fatalf("embedding indices not normalized: %s", raw)
			}
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
	cancelContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	cancelRequest, err := http.NewRequestWithContext(cancelContext, http.MethodPost, server.URL+"/v1/embeddings", strings.NewReader(`{"model":"company-embedding","input":[777,888]}`))
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest.Header.Set("Authorization", "Bearer "+embeddingKey.Key)
	cancelRequest.Header.Set("Content-Type", "application/json")
	cancelResult := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(cancelRequest)
		if response != nil {
			response.Body.Close()
		}
		cancelResult <- requestErr
	}()
	select {
	case <-cancelStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("embedding request did not reach the cancellable upstream")
	}
	cancel()
	select {
	case requestErr := <-cancelResult:
		if requestErr == nil {
			t.Fatal("cancelled embedding client request unexpectedly succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled embedding client request did not return")
	}
	select {
	case <-cancelSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("embedding cancellation did not reach upstream")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var cancelled int
		if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM model_requests WHERE model_id='company-embedding' AND outcome='cancelled'`).Scan(&cancelled); err != nil {
			t.Fatal(err)
		}
		if cancelled == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled embedding request was not settled")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 5 {
		t.Fatalf("upstream calls after cancellation=%d", calls.Load())
	}
	badResponse := embeddingEmployeeRequest(t, server.URL, embeddingKey.Key, `{"model":"company-embedding","input":"bad-response"}`)
	badBody := readBody(badResponse)
	if badResponse.StatusCode != http.StatusBadGateway || !strings.Contains(badBody, "upstream_protocol_error") || strings.Contains(badBody, "secret-vector") || calls.Load() != 6 {
		t.Fatalf("bad upstream response status=%d calls=%d body=%s", badResponse.StatusCode, calls.Load(), badBody)
	}

	var attempts, embeddingContexts, succeeded int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempt_contexts WHERE protocol='openai-embeddings'`).Scan(&embeddingContexts); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM model_requests WHERE model_id='company-embedding' AND outcome='succeeded'`).Scan(&succeeded); err != nil {
		t.Fatal(err)
	}
	if attempts != 6 || embeddingContexts != 6 || succeeded != 4 {
		t.Fatalf("attempts=%d embedding contexts=%d succeeded=%d", attempts, embeddingContexts, succeeded)
	}
	var unknownCost, unknownOutput int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_usage_events WHERE input_tokens IS NOT NULL AND estimated_cost_micro IS NULL`).Scan(&unknownCost); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_usage_events WHERE input_tokens IS NOT NULL AND output_tokens IS NULL AND cache_read_tokens IS NULL AND cache_write_tokens IS NULL`).Scan(&unknownOutput); err != nil {
		t.Fatal(err)
	}
	if unknownCost != 4 || unknownOutput != 4 {
		t.Fatalf("unknown cost events=%d embedding bucket events=%d", unknownCost, unknownOutput)
	}
	stamp := time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z")
	if _, err := app.store.db.Exec(`UPDATE governance_settings SET enabled=1,budget_enabled=1,revision=revision+1,updated_at=? WHERE singleton=1`, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := app.governance.core.Settings(context.Background()); err != nil {
		t.Fatalf("read enabled governance settings: %v", err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO governance_general_budget_policies(id,scope_kind,scope_id,protocol,model,enabled,token_limit,token_window,cost_limit_micro,currency,cost_window,revision,created_at,updated_at)
		VALUES('responses-only-budget','employee',?,'openai-responses','company-embedding',1,100,'rolling_60s',NULL,'','',1,?,?)`, employee.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	probe, err := app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.governancePolicies.ResolveScopesForRequestTx(context.Background(), probe, employee.ID, embeddingKey.ID, "company-embedding", "openai-embeddings"); err != nil {
		probe.Rollback()
		t.Fatalf("resolve embedding governance selectors: %v", err)
	}
	if matched, err := app.governancePolicies.MatchGeneralBudgetScopesTx(context.Background(), probe, "selector-probe", employee.ID, embeddingKey.ID, "company-embedding", "openai-embeddings"); err != nil || matched {
		probe.Rollback()
		t.Fatalf("responses-only selector matched=%v err=%v", matched, err)
	}
	if err := probe.Rollback(); err != nil {
		t.Fatal(err)
	}
	notMatched := embeddingEmployeeRequest(t, server.URL, embeddingKey.Key, `{"model":"company-embedding","input":"responses budget must not match"}`)
	if notMatched.StatusCode != http.StatusOK || calls.Load() != 7 {
		t.Fatalf("responses-only budget status=%d calls=%d body=%s", notMatched.StatusCode, calls.Load(), readBody(notMatched))
	}
	notMatched.Body.Close()
	if _, err := app.store.db.Exec(`INSERT INTO governance_general_budget_policies(id,scope_kind,scope_id,protocol,model,enabled,token_limit,token_window,cost_limit_micro,currency,cost_window,revision,created_at,updated_at)
		VALUES('embedding-strict-budget','employee',?,'openai-embeddings','company-embedding',1,100,'rolling_60s',NULL,'','',1,?,?)`, employee.ID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	deniedBudget := embeddingEmployeeRequest(t, server.URL, embeddingKey.Key, `{"model":"company-embedding","input":[7,8]}`)
	deniedBudgetBody := readBody(deniedBudget)
	if deniedBudget.StatusCode != http.StatusServiceUnavailable || !strings.Contains(deniedBudgetBody, "budget_bound_unavailable") || calls.Load() != 7 {
		t.Fatalf("strict budget status=%d calls=%d body=%s", deniedBudget.StatusCode, calls.Load(), deniedBudgetBody)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil || attempts != 7 {
		t.Fatalf("strict budget persisted attempt count=%d err=%v", attempts, err)
	}
	config := app.cfg
	if err := app.store.db.Close(); err != nil {
		t.Fatal(err)
	}
	storageFailure := embeddingEmployeeRequest(t, server.URL, embeddingKey.Key, `{"model":"company-embedding","input":[7,8]}`)
	storageFailureBody := readBody(storageFailure)
	if storageFailure.StatusCode == http.StatusOK || calls.Load() != 7 || strings.Contains(storageFailureBody, embeddingKey.Key) || strings.Contains(storageFailureBody, "embedding-secret") {
		t.Fatalf("storage failure status=%d calls=%d body=%s", storageFailure.StatusCode, calls.Load(), storageFailureBody)
	}
	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	restartedServer := httptest.NewServer(restarted.Handler())
	defer restartedServer.Close()
	if _, err := restarted.store.db.Exec(`DELETE FROM governance_general_budget_policies WHERE id='embedding-strict-budget'`); err != nil {
		t.Fatal(err)
	}
	recovered := embeddingEmployeeRequest(t, restartedServer.URL, embeddingKey.Key, `{"model":"company-embedding","input":[7,8]}`)
	if recovered.StatusCode != http.StatusOK || calls.Load() != 8 {
		t.Fatalf("restarted token request status=%d calls=%d body=%s", recovered.StatusCode, calls.Load(), readBody(recovered))
	}
	recovered.Body.Close()
	if err := restarted.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil || attempts != 8 {
		t.Fatalf("restarted attempt count=%d err=%v", attempts, err)
	}
}

func TestOpenAIEmbeddingsPricedInputOnlyUsageAppliesFrozenGroupAllocation(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1]}],"model":"provider-priced-embedding","usage":{"prompt_tokens":2,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	account := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "priced-embedding", "openai-compatible", upstream.URL, "synthetic-embedding-secret")
	groupResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/account-groups", `{"name":"Embedding allocation"}`, cookie, csrf, server.URL)
	var group accountGroupView
	decodeResponse(t, groupResponse, &group)
	groupResponse.Body.Close()
	allocationResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/account-groups/"+group.ID+"/allocation", `{"operation_id":"7a82705f-d6ae-420e-9853-cd87a759e3fd","expected_revision":1,"allocation_multiplier_ppm":"1500000"}`, cookie, csrf, server.URL)
	if allocationResponse.StatusCode != http.StatusOK {
		t.Fatalf("allocation status=%d body=%s", allocationResponse.StatusCode, readBody(allocationResponse))
	}
	var allocation accountGroupAllocationVersionView
	decodeResponse(t, allocationResponse, &allocation)
	allocationResponse.Body.Close()
	channelResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/channels", fmt.Sprintf(`{"name":"Embedding channel","group_id":%q}`, group.ID), cookie, csrf, server.URL)
	var channel accountChannelView
	decodeResponse(t, channelResponse, &channel)
	channelResponse.Body.Close()

	create := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models", marshalTestJSON(t, map[string]any{
		"id": "priced-embedding", "model_kind": "embedding", "upstream_id": account.ID, "upstream_model": "provider-priced-embedding",
	}), cookie, csrf, server.URL)
	if create.StatusCode != http.StatusCreated {
		t.Fatalf("create model status=%d body=%s", create.StatusCode, readBody(create))
	}
	create.Body.Close()
	pool := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/models/priced-embedding/accounts", marshalTestJSON(t, map[string]any{
		"expected_revision": 0,
		"items":             []map[string]any{{"upstream_id": account.ID, "upstream_model": "provider-priced-embedding", "wire_protocol": "openai-embeddings", "priority": 0, "weight": 1, "max_concurrency": 1, "channel_id": channel.ID}},
	}), cookie, csrf, server.URL)
	if pool.StatusCode != http.StatusOK {
		t.Fatalf("configure pool status=%d body=%s", pool.StatusCode, readBody(pool))
	}
	pool.Body.Close()
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Priced Embedding Employee")
	keyResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees/"+employee.ID+"/keys", marshalTestJSON(t, map[string]any{
		"name": "Priced embedding key", "operation_id": "priced-embedding-key", "policy": map[string]any{"protocol_mode": "selected", "protocols": []string{"openai-embeddings"}, "model_mode": "all", "models": []string{}},
	}), cookie, csrf, server.URL)
	if keyResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create key status=%d body=%s", keyResponse.StatusCode, readBody(keyResponse))
	}
	var key keyView
	decodeResponse(t, keyResponse, &key)
	keyResponse.Body.Close()

	catalog := accounting.NewPriceCatalog(app.store.db)
	priceVersion, err := catalog.Save(context.Background(), accounting.PriceSave{
		AccountID: account.ID, ActualModel: "provider-priced-embedding", OperationID: "8b45439f-f735-43b8-8cdf-013eff06bb25", ExpectedRevision: 0,
		Price: &accounting.PriceSnapshot{Currency: "EUR", InputPerMillionMicro: 3_000_000, OutputPerMillionMicro: 9_000_000, CacheReadPerMillionMicro: 9_000_000, CacheWritePerMillionMicro: 9_000_000},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := embeddingEmployeeRequest(t, server.URL, key.Key, `{"model":"priced-embedding","input":"cost me"}`)
	body := readBody(response)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("embedding status=%d body=%s", response.StatusCode, body)
	}

	var input, rawAttempt, rawEvent, adjusted sql.NullInt64
	var output, cacheRead, cacheWrite sql.NullInt64
	var storedGroup, storedVersion, storedPrice string
	var ppm int64
	if err := app.store.db.QueryRow(`SELECT a.input_tokens,a.output_tokens,a.cache_read_tokens,a.cache_write_tokens,a.cost_micro,e.estimated_cost_micro,ae.adjusted_cost_micro,s.account_group_id,s.multiplier_version,s.multiplier_ppm,a.price_version
		FROM accounting_attempts a JOIN accounting_usage_events e ON e.attempt_id=a.id JOIN accounting_usage_allocation_events ae ON ae.event_id=e.id JOIN accounting_attempt_allocation_snapshots s ON s.attempt_id=a.id
		ORDER BY a.started_at DESC,a.id DESC LIMIT 1`).Scan(&input, &output, &cacheRead, &cacheWrite, &rawAttempt, &rawEvent, &adjusted, &storedGroup, &storedVersion, &ppm, &storedPrice); err != nil {
		t.Fatal(err)
	}
	if !input.Valid || input.Int64 != 2 || output.Valid || cacheRead.Valid || cacheWrite.Valid || !rawAttempt.Valid || rawAttempt.Int64 != 6 || !rawEvent.Valid || rawEvent.Int64 != 6 || !adjusted.Valid || adjusted.Int64 != 9 {
		t.Fatalf("usage input/output/read/write=%v/%v/%v/%v raw=%v/%v adjusted=%v", input, output, cacheRead, cacheWrite, rawAttempt, rawEvent, adjusted)
	}
	if storedGroup != group.ID || storedVersion != allocation.Version || ppm != 1_500_000 || storedPrice != priceVersion.Version {
		t.Fatalf("snapshot group/version/ppm/price=%q/%q/%d/%q", storedGroup, storedVersion, ppm, storedPrice)
	}
	reports, err := accounting.NewRequiredAllocationLedger(app.store.db).AccountingV2Report(context.Background(), accounting.AccountingV2Filters{From: time.Now().UTC().Add(-time.Hour), To: time.Now().UTC().Add(time.Hour)}, accounting.AccountingV2Day)
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 || reports[0].Currency != "EUR" || reports[0].KnownEstimatedCostMicro != 6 || reports[0].KnownAdjustedAllocationCostMicro != 9 || reports[0].UnknownCostAttempts != 0 || reports[0].OutputTokens.UnknownAttempts != 1 {
		t.Fatalf("embedding allocation report=%+v", reports)
	}
}

func embeddingEmployeeRequest(t *testing.T, baseURL, key, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/embeddings", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
