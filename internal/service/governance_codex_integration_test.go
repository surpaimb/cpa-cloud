package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/governance"
	"cpacloud.local/server/internal/membership"
)

func TestGovernanceCodexChatAndResponsesShareRPMAndRefreshRevision(t *testing.T) {
	var chatCalls, responseCalls, refreshCalls atomic.Int32
	rotatedAccess := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	rotatedID := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "governance-codex"}})

	chat := &fakeCodexExecutor{}
	checkCredential := func(credential *membership.CodexAuthCredential) {
		t.Helper()
		if credential.AccessTokenSecret() != rotatedAccess || credential.RefreshTokenSecret() != "governance-rotated-refresh" {
			t.Errorf("executor received a credential other than the request-refreshed generation")
		}
	}
	chat.setComplete(func(_ context.Context, credential *membership.CodexAuthCredential, request membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		chatCalls.Add(1)
		checkCredential(credential)
		if request.Model != "gpt-codex-provider" {
			t.Errorf("Chat model=%q", request.Model)
		}
		input, output, total := int64(3), int64(2), int64(5)
		return codexExecutionResult{Text: "governed chat", Usage: membership.CodexUsage{InputTokens: &input, OutputTokens: &output, TotalTokens: &total}}, nil
	})
	chat.streamFn = func(_ context.Context, credential *membership.CodexAuthCredential, request membership.CodexTextRequest, consume func(codexExecutionEvent) error) *codexRunError {
		chatCalls.Add(1)
		checkCredential(credential)
		if request.Model != "gpt-codex-provider" {
			t.Errorf("streaming Chat model=%q", request.Model)
		}
		for _, event := range []codexExecutionEvent{
			{Kind: membership.CodexEventStarted},
			{Kind: membership.CodexEventTextDelta, Text: "governed stream"},
			{Kind: membership.CodexEventCompleted},
		} {
			if err := consume(event); err != nil {
				return &codexRunError{Code: membership.CodexErrorEventConsumerStopped}
			}
		}
		return nil
	}

	fixture := newCodexServiceFixture(t, chat)
	defer fixture.close()
	fixture.app.responses = fakeCodexResponsesExecutor{fn: func(_ context.Context, credential *membership.CodexAuthCredential, body []byte, consume func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
		responseCalls.Add(1)
		checkCredential(credential)
		if !strings.Contains(string(body), `"model":"gpt-codex-provider"`) {
			t.Errorf("Responses model was not mapped: %s", body)
		}
		final := json.RawMessage(`{"id":"resp_governed_codex","object":"response","status":"completed","output":[]}`)
		if consume != nil {
			for _, event := range []json.RawMessage{
				json.RawMessage(`{"type":"response.created","response":{"id":"resp_governed_codex"}}`),
				json.RawMessage(`{"type":"response.completed","response":{"id":"resp_governed_codex","object":"response","status":"completed","output":[]}}`),
			} {
				if err := consume(event); err != nil {
					return nil, &codexRunError{Code: membership.CodexErrorEventConsumerStopped}
				}
			}
		}
		return final, nil
	}}

	configureExpiredCodexRefresh(t, fixture)
	fixture.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		refreshCalls.Add(1)
		return oauthHTTPResponse(http.StatusOK, map[string]string{
			"access_token": rotatedAccess, "id_token": rotatedID, "refresh_token": "governance-rotated-refresh",
		}), nil
	})}
	enableCodexGovernanceRPM(t, fixture, 4)

	tests := []struct {
		name string
		path string
		body string
		want string
	}{
		{"chat json", "/v1/chat/completions", `{"model":"company-codex","messages":[{"role":"user","content":"json"}]}`, `"content":"governed chat"`},
		{"chat sse", "/v1/chat/completions", `{"model":"company-codex","stream":true,"messages":[{"role":"user","content":"sse"}]}`, "data: [DONE]"},
		{"responses json", "/v1/responses", `{"model":"company-codex","input":"json"}`, `"id":"resp_governed_codex"`},
		{"responses sse", "/v1/responses", `{"model":"company-codex","stream":true,"input":"sse"}`, "response.completed"},
	}
	var firstRequestID string
	for index, test := range tests {
		response := employeeRequest(t, http.MethodPost, fixture.server.URL+test.path, test.body, fixture.employeeKey.Key, context.Background())
		body := readBody(response)
		if response.StatusCode != http.StatusOK || !strings.Contains(body, test.want) {
			t.Fatalf("%s status=%d body=%s", test.name, response.StatusCode, body)
		}
		if index == 0 {
			firstRequestID = response.Header.Get("X-Request-ID")
		}
	}
	if firstRequestID == "" {
		t.Fatal("first successful request did not return a request ID")
	}
	if chatCalls.Load() != 2 || responseCalls.Load() != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("successful calls chat=%d responses=%d refresh=%d", chatCalls.Load(), responseCalls.Load(), refreshCalls.Load())
	}
	var revision int64
	if err := fixture.app.store.db.QueryRow(`SELECT revision FROM upstreams WHERE id=?`, fixture.upstream.ID).Scan(&revision); err != nil || revision != 2 {
		t.Fatalf("request refresh revision=%d err=%v", revision, err)
	}
	assertGovernedCodexRows(t, fixture, firstRequestID, 4)

	beforeAttempts := countGovernanceCodexRows(t, fixture.app.store.db, `SELECT COUNT(*) FROM accounting_attempts`)
	overLimit := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"company-codex","input":"over limit"}`, fixture.employeeKey.Key, context.Background())
	overLimitBody := readBody(overLimit)
	if overLimit.StatusCode != http.StatusTooManyRequests || !strings.Contains(overLimitBody, `"code":"request_limit_exceeded"`) {
		t.Fatalf("over-limit status=%d body=%s", overLimit.StatusCode, overLimitBody)
	}
	if chatCalls.Load() != 2 || responseCalls.Load() != 2 || refreshCalls.Load() != 1 {
		t.Fatalf("over-limit request reached executor or refresh: chat=%d responses=%d refresh=%d", chatCalls.Load(), responseCalls.Load(), refreshCalls.Load())
	}
	if got := countGovernanceCodexRows(t, fixture.app.store.db, `SELECT COUNT(*) FROM accounting_attempts`); got != beforeAttempts {
		t.Fatalf("over-limit request created an attempt: before=%d after=%d", beforeAttempts, got)
	}
	assertGovernedCodexRows(t, fixture, firstRequestID, 4)
}

func enableCodexGovernanceRPM(t *testing.T, fixture *codexServiceFixture, limit int64) {
	t.Helper()
	var actorID string
	if err := fixture.app.store.db.QueryRow(`SELECT id FROM admins WHERE username='admin'`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if receipt, err := fixture.app.governancePolicies.updateSettings(context.Background(), actorID, governanceOperationID(950), 1, true); err != nil || receipt.Revision != 2 {
		t.Fatalf("enable governance receipt=%+v err=%v", receipt, err)
	}
	for index, scope := range []struct {
		kind governance.ScopeKind
		id   string
	}{{governance.ScopeEmployee, fixture.employee.ID}, {governance.ScopeKey, fixture.employeeKey.ID}} {
		input := governancePolicyInput{ScopeKind: scope.kind, ScopeID: scope.id, Enabled: true, Hard: governanceHardLimits{RPM: &limit}}
		if receipt, err := fixture.app.governancePolicies.createPolicy(context.Background(), actorID, governanceOperationID(951+index), input); err != nil || receipt.Revision != 1 {
			t.Fatalf("create %s policy receipt=%+v err=%v", scope.kind, receipt, err)
		}
	}
}

func assertGovernedCodexRows(t *testing.T, fixture *codexServiceFixture, firstRequestID string, want int) {
	t.Helper()
	db := fixture.app.store.db
	queries := []struct {
		name  string
		query string
		args  []any
		want  int
	}{
		{"governance parents", `SELECT COUNT(*) FROM governance_requests WHERE employee_id=? AND key_id=?`, []any{fixture.employee.ID, fixture.employeeKey.ID}, want},
		{"released governance parents", `SELECT COUNT(*) FROM governance_requests WHERE employee_id=? AND key_id=? AND status='succeeded' AND released_at IS NOT NULL`, []any{fixture.employee.ID, fixture.employeeKey.ID}, want},
		{"governance scopes", `SELECT COUNT(*) FROM governance_request_scopes s JOIN governance_requests r ON r.id=s.request_id WHERE r.employee_id=? AND r.key_id=?`, []any{fixture.employee.ID, fixture.employeeKey.ID}, want * 2},
		{"accounting parents", `SELECT COUNT(*) FROM accounting_requests WHERE employee_id=? AND key_id=? AND status='succeeded'`, []any{fixture.employee.ID, fixture.employeeKey.ID}, want},
		{"legacy parents", `SELECT COUNT(*) FROM model_requests WHERE employee_id=? AND key_id=? AND outcome='succeeded'`, []any{fixture.employee.ID, fixture.employeeKey.ID}, want},
		{"attempts", `SELECT COUNT(*) FROM accounting_attempts a JOIN accounting_requests r ON r.id=a.request_id WHERE r.employee_id=? AND r.key_id=? AND a.status='succeeded'`, []any{fixture.employee.ID, fixture.employeeKey.ID}, want},
		{"shared parent IDs", `SELECT COUNT(*) FROM governance_requests g JOIN accounting_requests a ON a.id=g.id JOIN model_requests m ON m.id=g.id WHERE g.employee_id=? AND g.key_id=?`, []any{fixture.employee.ID, fixture.employeeKey.ID}, want},
	}
	for _, check := range queries {
		var got int
		if err := db.QueryRow(check.query, check.args...).Scan(&got); err != nil || got != check.want {
			t.Fatalf("%s=%d want=%d err=%v", check.name, got, check.want, err)
		}
	}
	for protocol, expected := range map[string]int{"openai-chat-completions": 2, "openai-responses": 2} {
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM governance_requests WHERE employee_id=? AND key_id=? AND protocol=?`, fixture.employee.ID, fixture.employeeKey.ID, protocol).Scan(&got); err != nil || got != expected {
			t.Fatalf("protocol %s count=%d want=%d err=%v", protocol, got, expected, err)
		}
	}
	var responseAttempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_attempt_contexts c JOIN accounting_attempts a ON a.id=c.attempt_id JOIN accounting_requests r ON r.id=a.request_id WHERE r.employee_id=? AND r.key_id=? AND c.protocol='openai-responses'`, fixture.employee.ID, fixture.employeeKey.ID).Scan(&responseAttempts); err != nil || responseAttempts != want {
		t.Fatalf("Codex upstream Responses attempts=%d want=%d err=%v", responseAttempts, want, err)
	}
	var firstRows, firstScopes, firstAttempts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM governance_requests WHERE id=?`, firstRequestID).Scan(&firstRows); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM governance_request_scopes WHERE request_id=?`, firstRequestID).Scan(&firstScopes); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE request_id=?`, firstRequestID).Scan(&firstAttempts); err != nil {
		t.Fatal(err)
	}
	if firstRows != 1 || firstScopes != 2 || firstAttempts != 1 {
		t.Fatalf("request-refreshed parent rows=%d scopes=%d attempts=%d", firstRows, firstScopes, firstAttempts)
	}
}

func countGovernanceCodexRows(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var count int
	if err := db.QueryRow(query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
