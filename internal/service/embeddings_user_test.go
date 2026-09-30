package service

// Independent synthetic-provider tests for the public OpenAI Create embeddings
// user parameter, scoped by CPA Cloud's 2026-09-30 preview contract.
import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestOpenAIEmbeddingsExplicitUserHintOnlyReachesQualifiedUpstream(t *testing.T) {
	type seenRequest struct {
		userPresent bool
		user        string
		input       string
		dimensions  string
	}
	seen := make(chan seenRequest, 8)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/embeddings" || r.Header.Get("Authorization") != "Bearer user-hint-upstream-secret" {
			t.Error("unexpected upstream method, path, or authorization")
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if string(body["model"]) != `"text-embedding-3-small"` || string(body["encoding_format"]) != `"float"` {
			t.Error("wrong upstream model or encoding")
		}
		observation := seenRequest{input: string(body["input"]), dimensions: string(body["dimensions"])}
		if rawUser, present := body["user"]; present {
			observation.userPresent = true
			if err := json.Unmarshal(rawUser, &observation.user); err != nil {
				t.Error(err)
			}
		}
		seen <- observation
		if observation.input == `"echo"` {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.25,0.5]}],"model":"text-embedding-3-small","usage":{"prompt_tokens":1,"total_tokens":1},"user":"hint_echo"}`))
			return
		}
		count := 1
		if observation.input == `["a","b"]` || observation.input == `[[1,2],[3]]` {
			count = 2
		}
		data := make([]map[string]any, count)
		for index := range data {
			data[index] = map[string]any{"object": "embedding", "index": index, "embedding": []float64{0.25, 0.5}}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object": "list", "data": data, "model": "text-embedding-3-small",
			"usage": map[string]int{"prompt_tokens": count, "total_tokens": count},
		})
	}))
	defer upstream.Close()

	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	account := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "user-hint", "openai-compatible", upstream.URL, "user-hint-upstream-secret")
	model := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models", marshalTestJSON(t, map[string]any{
		"id": "user-hint-model", "model_kind": "embedding", "upstream_id": account.ID, "upstream_model": "text-embedding-3-small",
	}), cookie, csrf, server.URL)
	if model.StatusCode != http.StatusCreated {
		t.Fatalf("create model status=%d body=%s", model.StatusCode, readBody(model))
	}
	model.Body.Close()
	pool := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/models/user-hint-model/accounts", marshalTestJSON(t, map[string]any{
		"expected_revision": 0,
		"items":             []map[string]any{{"upstream_id": account.ID, "upstream_model": "text-embedding-3-small", "wire_protocol": "openai-embeddings", "priority": 0, "weight": 1, "max_concurrency": 1}},
	}), cookie, csrf, server.URL)
	if pool.StatusCode != http.StatusOK {
		t.Fatalf("configure pool status=%d body=%s", pool.StatusCode, readBody(pool))
	}
	pool.Body.Close()
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "User Hint Employee")
	legacy := createTestKey(t, server.URL, employee.ID, "user-hint-legacy", cookie, csrf)
	keyResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees/"+employee.ID+"/keys", marshalTestJSON(t, map[string]any{
		"name": "User hint key", "operation_id": "user-hint-explicit-key",
		"policy": map[string]any{"protocol_mode": "selected", "protocols": []string{"openai-embeddings"}, "model_mode": "all", "models": []string{}},
	}), cookie, csrf, server.URL)
	if keyResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create key status=%d body=%s", keyResponse.StatusCode, readBody(keyResponse))
	}
	var key keyView
	decodeResponse(t, keyResponse, &key)

	request := func(employeeKey, suffix string) (int, string) {
		t.Helper()
		response := embeddingEmployeeRequest(t, server.URL, employeeKey, `{"model":"user-hint-model","input":"x"`+suffix+`}`)
		return response.StatusCode, readBody(response)
	}
	status, body := request(legacy.Key, `,"user":"random_1"`)
	if status != http.StatusForbidden || calls.Load() != 0 {
		t.Fatalf("legacy key status=%d calls=%d body=%s", status, calls.Load(), body)
	}
	for _, invalid := range []string{`""`, `null`, `123`, `"a@b"`, `"\u00e9"`, `"` + strings.Repeat("x", 129) + `"`} {
		status, body = request(key.Key, `,"user":`+invalid)
		if status != http.StatusBadRequest || calls.Load() != 0 || strings.Contains(body, "a@b") {
			t.Fatalf("invalid hint status=%d calls=%d body=%s", status, calls.Load(), body)
		}
	}
	status, body = request(key.Key, `,"user":"a","user":"b"`)
	if status != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("duplicate hint status=%d calls=%d body=%s", status, calls.Load(), body)
	}
	var attempts int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("rejected hint persisted attempts=%d err=%v", attempts, err)
	}

	for _, test := range []struct {
		input, suffix, wantHint, wantDimensions string
		present                                 bool
	}{
		{`"x"`, `,"user":"random_1"`, "random_1", "", true},
		{`["a","b"]`, `,"user":"Team-2","encoding_format":"float"`, "Team-2", "", true},
		{`[1,2]`, `,"user":"token_3","dimensions":2`, "token_3", "2", true},
		{`[[1,2],[3]]`, `,"user":"batch_4"`, "batch_4", "", true},
		{`"x"`, "", "", "", false},
	} {
		response := embeddingEmployeeRequest(t, server.URL, key.Key, `{"model":"user-hint-model","input":`+test.input+test.suffix+`}`)
		body := readBody(response)
		if response.StatusCode != http.StatusOK || strings.Contains(body, "random_1") || strings.Contains(body, "Team-2") || strings.Contains(body, "token_3") || strings.Contains(body, "batch_4") || strings.Contains(body, `"user"`) {
			t.Fatalf("embedding status=%d body=%s", response.StatusCode, body)
		}
		observation := <-seen
		if observation.userPresent != test.present || observation.user != test.wantHint || observation.input != test.input || observation.dimensions != test.wantDimensions {
			t.Fatal("upstream user hint, input, or dimensions changed")
		}
	}
	response := embeddingEmployeeRequest(t, server.URL, key.Key, `{"model":"user-hint-model","input":"echo","user":"hint_echo"}`)
	body = readBody(response)
	if response.StatusCode != http.StatusBadGateway || !strings.Contains(body, "upstream_protocol_error") || strings.Contains(body, "hint_echo") {
		t.Fatalf("echo response status=%d body=%s", response.StatusCode, body)
	}
	<-seen
	if calls.Load() != 6 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil || attempts != 6 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}
