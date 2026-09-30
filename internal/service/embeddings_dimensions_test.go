package service

// Independent synthetic-provider tests for the public OpenAI Create embeddings
// dimensions parameter, scoped by CPA Cloud's 2026-09-30 preview contract.
import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/embeddingwire"
)

func TestOpenAIEmbeddingsDimensionsSelectedModelAndExactVectors(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer dimensions-secret" {
			t.Errorf("unexpected upstream request path=%q or authorization", r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		var model string
		if err := json.Unmarshal(body["model"], &model); err != nil {
			t.Error(err)
			return
		}
		if string(body["encoding_format"]) != `"float"` {
			t.Errorf("unexpected encoding: %s", body["encoding_format"])
		}
		dimension := 2
		if raw, present := body["dimensions"]; present {
			if err := json.Unmarshal(raw, &dimension); err != nil {
				t.Error(err)
				return
			}
		}
		var inputCount int
		var text string
		var texts []string
		var tokens []int64
		var batches [][]int64
		switch {
		case json.Unmarshal(body["input"], &text) == nil:
			inputCount = 1
		case json.Unmarshal(body["input"], &texts) == nil:
			inputCount = len(texts)
		case json.Unmarshal(body["input"], &tokens) == nil:
			inputCount = 1
		case json.Unmarshal(body["input"], &batches) == nil:
			inputCount = len(batches)
		default:
			t.Errorf("unexpected input: %s", body["input"])
			return
		}
		if text == "wrong-dimension" {
			dimension++
		}
		data := make([]map[string]any, inputCount)
		for index := range data {
			data[index] = map[string]any{"object": "embedding", "index": index, "embedding": make([]float64, dimension)}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list", "data": data, "model": model,
			"usage": map[string]int{"prompt_tokens": inputCount, "total_tokens": inputCount},
		})
	}))
	defer upstream.Close()

	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	account := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "dimensions", "openai-compatible", upstream.URL, "dimensions-secret")
	for _, pair := range []struct{ public, actual string }{
		{"public-small-alias", "text-embedding-3-small"},
		{"public-large-alias", "text-embedding-3-large"},
		{"public-other-alias", "text-embedding-ada-002"},
	} {
		response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models", marshalTestJSON(t, map[string]any{
			"id": pair.public, "model_kind": "embedding", "upstream_id": account.ID, "upstream_model": pair.actual,
		}), cookie, csrf, server.URL)
		if response.StatusCode != http.StatusCreated {
			t.Fatalf("create model %s status=%d body=%s", pair.public, response.StatusCode, readBody(response))
		}
		response.Body.Close()
		response = requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/models/"+pair.public+"/accounts", marshalTestJSON(t, map[string]any{
			"expected_revision": 0,
			"items":             []map[string]any{{"upstream_id": account.ID, "upstream_model": pair.actual, "wire_protocol": "openai-embeddings", "priority": 0, "weight": 1, "max_concurrency": 1}},
		}), cookie, csrf, server.URL)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("configure pool %s status=%d body=%s", pair.public, response.StatusCode, readBody(response))
		}
		response.Body.Close()
	}
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Dimensions Employee")
	legacy := createTestKey(t, server.URL, employee.ID, "dimensions-legacy", cookie, csrf)
	keyResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees/"+employee.ID+"/keys", marshalTestJSON(t, map[string]any{
		"name": "Dimensions Key", "operation_id": "dimensions-explicit-key",
		"policy": map[string]any{"protocol_mode": "selected", "protocols": []string{"openai-embeddings"}, "model_mode": "all", "models": []string{}},
	}), cookie, csrf, server.URL)
	if keyResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create key status=%d body=%s", keyResponse.StatusCode, readBody(keyResponse))
	}
	var key keyView
	decodeResponse(t, keyResponse, &key)

	request := func(key, model, input, suffix string) (int, string) {
		t.Helper()
		response := embeddingEmployeeRequest(t, server.URL, key, `{"model":"`+model+`","input":`+input+suffix+`}`)
		return response.StatusCode, readBody(response)
	}
	status, body := request(legacy.Key, "public-small-alias", `"x"`, `,"dimensions":1`)
	if status != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("legacy key status=%d calls=%d body=%s", status, calls.Load(), body)
	}
	for _, raw := range []string{"0", "-0", "-1", "1.5", "1e0", "2147483648", "true", `"1"`, "null"} {
		status, body = request(key.Key, "public-small-alias", `"x"`, `,"dimensions":`+raw)
		if status != http.StatusBadRequest || !strings.Contains(body, "invalid_request_error") || calls.Load() != 0 {
			t.Fatalf("invalid dimensions=%s status=%d calls=%d body=%s", raw, status, calls.Load(), body)
		}
	}
	status, body = request(key.Key, "public-small-alias", `"x"`, `,"dimensions":1,"dimensions":2`)
	if status != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("duplicate dimensions status=%d calls=%d body=%s", status, calls.Load(), body)
	}
	for _, pair := range []struct{ model, suffix string }{
		{"public-small-alias", `,"dimensions":1537`},
		{"public-large-alias", `,"dimensions":3073`},
		{"public-other-alias", `,"dimensions":1`},
	} {
		status, body = request(key.Key, pair.model, `"x"`, pair.suffix)
		if status != http.StatusBadRequest || !strings.Contains(body, "unsupported_feature") || calls.Load() != 0 {
			t.Fatalf("unsupported %s%s status=%d calls=%d body=%s", pair.model, pair.suffix, status, calls.Load(), body)
		}
	}
	var attempts int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("rejected dimensions persisted attempts=%d err=%v", attempts, err)
	}
	for _, test := range []struct {
		model, input, suffix string
		count, dimension     int
	}{
		{"public-small-alias", `"hello"`, `,"dimensions":1`, 1, 1},
		{"public-small-alias", `["hello","world"]`, `,"dimensions":1536`, 2, 1536},
		{"public-large-alias", `[0,12]`, `,"dimensions":1`, 1, 1},
		{"public-large-alias", `[[12,34],[56]]`, `,"dimensions":3072`, 2, 3072},
		{"public-other-alias", `"legacy"`, "", 1, 2},
	} {
		status, body = request(key.Key, test.model, test.input, test.suffix)
		if status != http.StatusOK {
			t.Fatalf("valid %s status=%d body=%s", test.model, status, body)
		}
		var result struct {
			Model string `json:"model"`
			Data  []struct {
				Embedding []float64 `json:"embedding"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &result); err != nil || result.Model != test.model || len(result.Data) != test.count {
			t.Fatalf("unexpected result model/count body=%s err=%v", body, err)
		}
		for _, item := range result.Data {
			if len(item.Embedding) != test.dimension {
				t.Fatalf("dimension=%d want %d", len(item.Embedding), test.dimension)
			}
		}
	}
	before := calls.Load()
	status, body = request(key.Key, "public-small-alias", `"wrong-dimension"`, `,"dimensions":1`)
	if status != http.StatusBadGateway || !strings.Contains(body, "upstream_protocol_error") || calls.Load() != before+1 {
		t.Fatalf("bad dimension status=%d calls=%d body=%s", status, calls.Load(), body)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil || attempts != int(calls.Load()) {
		t.Fatalf("attempts=%d calls=%d err=%v", attempts, calls.Load(), err)
	}
	if !embeddingwire.DimensionsAllowed(nil, "text-embedding-ada-002") {
		t.Fatal("omitted dimensions changed old-model behavior")
	}
}

func TestEmbeddingDimensionFinalDispatchGuardRejectsBeforeAttempt(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
	a := f.base.app
	a.accountPool.Close()
	a.accountPool = f.rt
	f.insertAccount(t, "ups_dimensions_final", true)
	f.insertModelPool(t, "dimensions-final-model", "ups_dimensions_final", 1, modelAccountView{
		UpstreamID: "ups_dimensions_final", UpstreamModel: "text-embedding-3-small", Priority: 1, Weight: 1, MaxConcurrency: 1,
	})
	ctx := context.WithValue(context.Background(), requestIDKey{}, "request-dimensions-final")
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)).WithContext(ctx)
	selected, lease, failure := a.prepareModelRoute(r, f.auth1, "dimensions-final-model", []string{"openai-compatible"}, accounting.ProtocolOpenAIResponses, true, func(_ *http.Request, selected route) (route, *modelPreflightError) {
		return selected, nil
	})
	if failure != nil || lease == nil {
		t.Fatalf("prepare failure=%+v lease=%v", failure, lease)
	}
	defer a.releaseModelLease(lease, requestID(ctx), true)
	dimensions := 1537
	_, failure = a.dispatchModelRouteGuarded(r, f.auth1, "dimensions-final-model", selected, lease, true, func(current route) bool {
		return embeddingwire.DimensionsAllowed(&dimensions, current.UpstreamModel)
	})
	if failure == nil || failure.code != "unsupported_feature" {
		t.Fatalf("final guard failure=%+v", failure)
	}
	var attempts int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE request_id=?`, requestID(ctx)).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	a.finishRequest(requestID(ctx), "failed", 0)
}
