package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

var usageLedgerTestBase = time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)

func TestUsageLedgerProviderAndProtocolIsolation(t *testing.T) {
	db := openUsageLedgerTestDB(t, filepath.Join(t.TempDir(), "provider.db"))
	coordinator := startUsageLedgerTestCoordinator(t, db, usageLedgerTestBase)
	tests := []struct {
		name         string
		providerKind string
		protocol     accounting.UsageProtocol
		provider     accounting.Provider
	}{
		{"openai chat", "openai-compatible", accounting.ProtocolOpenAIChatCompletions, accounting.ProviderOpenAICompatible},
		{"openai responses", "openai-compatible", accounting.ProtocolOpenAIResponses, accounting.ProviderOpenAICompatible},
		{"codex responses", codexMembershipProvider, accounting.ProtocolOpenAIResponses, accounting.ProviderCodex},
		{"codex converted chat", codexMembershipProvider, accounting.ProtocolOpenAIChatCompletions, accounting.ProviderCodex},
		{"anthropic", anthropicAPIKeyProvider, accounting.ProtocolAnthropicMessages, accounting.ProviderAnthropic},
		{"gemini", geminiAPIKeyProvider, accounting.ProtocolGeminiGenerateContent, accounting.ProviderGemini},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id := "request-provider-" + string(rune('a'+index))
			started := usageLedgerTestBase.Add(time.Duration(index+1) * time.Minute)
			request, err := coordinator.beginRequest(context.Background(), usageRequestStart{
				RequestID: id, EmployeeID: "employee-1", KeyID: "key-1", PublicModel: "公司/模型-中文",
				ProviderKind: test.providerKind, Protocol: test.protocol, StartedAt: started,
			})
			if err != nil {
				t.Fatalf("begin request: %v", err)
			}
			attempt, err := request.beginAttempt(context.Background(), "account-"+string(rune('a'+index)), started.Add(time.Second))
			if err != nil {
				t.Fatalf("begin attempt: %v", err)
			}
			if err := attempt.finish(context.Background(), accounting.StatusFailed, started.Add(2*time.Second)); err != nil {
				t.Fatalf("finish: %v", err)
			}
			var requestProvider, attemptProvider, model string
			var version, currency sql.NullString
			if err := db.QueryRow(`SELECT provider,model_id FROM accounting_requests WHERE id=?`, id).Scan(&requestProvider, &model); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT provider,price_version,currency FROM accounting_attempts WHERE id=?`, id+":1").Scan(&attemptProvider, &version, &currency); err != nil {
				t.Fatal(err)
			}
			if requestProvider != string(test.provider) || attemptProvider != string(test.provider) || model != "公司/模型-中文" {
				t.Fatalf("stored provider/model = %q/%q/%q", requestProvider, attemptProvider, model)
			}
			if version.Valid || currency.Valid {
				t.Fatalf("unconfigured price was persisted: version=%v currency=%v", version, currency)
			}
		})
	}

	invalid := []usageRequestStart{
		{RequestID: "bad-openai", EmployeeID: "employee-1", KeyID: "key-1", PublicModel: "model", ProviderKind: "openai-compatible", Protocol: accounting.ProtocolAnthropicMessages, StartedAt: usageLedgerTestBase.Add(time.Hour)},
		{RequestID: "bad-anthropic", EmployeeID: "employee-1", KeyID: "key-1", PublicModel: "model", ProviderKind: anthropicAPIKeyProvider, Protocol: accounting.ProtocolOpenAIResponses, StartedAt: usageLedgerTestBase.Add(time.Hour)},
		{RequestID: "bad-gemini", EmployeeID: "employee-1", KeyID: "key-1", PublicModel: "model", ProviderKind: geminiAPIKeyProvider, Protocol: accounting.ProtocolOpenAIChatCompletions, StartedAt: usageLedgerTestBase.Add(time.Hour)},
		{RequestID: "bad-unknown", EmployeeID: "employee-1", KeyID: "key-1", PublicModel: "model", ProviderKind: "unknown", Protocol: accounting.ProtocolOpenAIResponses, StartedAt: usageLedgerTestBase.Add(time.Hour)},
	}
	for _, input := range invalid {
		if _, err := coordinator.beginRequest(context.Background(), input); !errors.Is(err, errUsageLedgerInvalid) {
			t.Fatalf("provider/protocol %q/%q: %v", input.ProviderKind, input.Protocol, err)
		}
	}
	var invalidRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_requests WHERE id LIKE 'bad-%'`).Scan(&invalidRows); err != nil || invalidRows != 0 {
		t.Fatalf("invalid mappings persisted: count=%d err=%v", invalidRows, err)
	}
}

func TestUsageLedgerV2FactsAcrossFourProtocols(t *testing.T) {
	db := openUsageLedgerTestDB(t, filepath.Join(t.TempDir(), "v2-facts.db"))
	coordinator := startUsageLedgerTestCoordinator(t, db, usageLedgerTestBase)
	tests := []struct {
		name, provider, model, actual, body, responseID string
		protocol                                        accounting.UsageProtocol
		evidence                                        accounting.UsageEvidence
		reasoning                                       int64
	}{
		{"chat", "openai-compatible", "chat-public", "chat-actual", `{"id":"chat-provider-1","usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8,"completion_tokens_details":{"reasoning_tokens":1}}}`, "chat-provider-1", accounting.ProtocolOpenAIChatCompletions, accounting.EvidenceProviderResponse, 1},
		{"responses stream", "openai-compatible", "responses-public", "responses-actual", `{"type":"response.completed","response":{"id":"resp-provider-1","usage":{"input_tokens":5,"output_tokens":3,"total_tokens":8,"output_tokens_details":{"reasoning_tokens":2}}}}`, "resp-provider-1", accounting.ProtocolOpenAIResponses, accounting.EvidenceProviderStream, 2},
		{"anthropic", anthropicAPIKeyProvider, "claude-public", "claude-actual", `{"id":"msg-provider-1","type":"message","usage":{"input_tokens":5,"output_tokens":3}}`, "msg-provider-1", accounting.ProtocolAnthropicMessages, accounting.EvidenceProviderResponse, 0},
		{"gemini stream", geminiAPIKeyProvider, "gemini-public", "gemini-actual", `{"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":2,"thoughtsTokenCount":1,"totalTokenCount":8}}`, "", accounting.ProtocolGeminiGenerateContent, accounting.EvidenceProviderStream, 1},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := usageLedgerTestBase.Add(time.Duration(index+1) * time.Hour)
			id := "request-v2-" + string(rune('a'+index))
			request, err := coordinator.beginRequest(context.Background(), usageRequestStart{RequestID: id, EmployeeID: "employee-v2", KeyID: "key-v2", PublicModel: test.model, ProviderKind: test.provider, Protocol: test.protocol, Evidence: test.evidence, StartedAt: started})
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := request.beginDispatchedAttempt(context.Background(), "account-v2-"+string(rune('a'+index)), test.actual, started.Add(time.Second), nil, accounting.DispatchPrimary)
			if err != nil {
				t.Fatal(err)
			}
			if err := attempt.observe([]byte(test.body)); err != nil {
				t.Fatal(err)
			}
			if err := attempt.finish(context.Background(), accounting.StatusSucceeded, started.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := request.finishAfterAttempt(context.Background(), accounting.StatusSucceeded, started.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}
			var protocol, actual, evidence string
			var dispatched int
			if err := db.QueryRow(`SELECT c.protocol,c.effective_model,c.evidence,(SELECT COUNT(*) FROM accounting_attempt_dispatches d WHERE d.attempt_id=c.attempt_id) FROM accounting_attempt_contexts c WHERE c.attempt_id=?`, id+":1").Scan(&protocol, &actual, &evidence, &dispatched); err != nil {
				t.Fatal(err)
			}
			if protocol != string(test.protocol) || actual != test.actual || evidence != string(test.evidence) || dispatched != 1 {
				t.Fatalf("context protocol=%q model=%q evidence=%q dispatched=%d", protocol, actual, evidence, dispatched)
			}
			var responseID sql.NullString
			var reasoning sql.NullInt64
			if err := db.QueryRow(`SELECT response_id,reasoning_tokens FROM accounting_usage_events WHERE attempt_id=?`, id+":1").Scan(&responseID, &reasoning); err != nil {
				t.Fatal(err)
			}
			if responseID.String != test.responseID || responseID.Valid != (test.responseID != "") || reasoning.Int64 != test.reasoning || reasoning.Valid != (test.reasoning != 0) {
				t.Fatalf("response=%v reasoning=%v", responseID, reasoning)
			}
		})
	}
}

func TestUsageLedgerJSONAndSSECumulativeUsage(t *testing.T) {
	db := openUsageLedgerTestDB(t, filepath.Join(t.TempDir(), "usage.db"))
	coordinator := startUsageLedgerTestCoordinator(t, db, usageLedgerTestBase)

	chat := beginUsageLedgerTestAttempt(t, coordinator, usageRequestStart{
		RequestID: "request-json", EmployeeID: "employee-json", KeyID: "key-json", PublicModel: "chat/中文",
		ProviderKind: "openai-compatible", Protocol: accounting.ProtocolOpenAIChatCompletions, StartedAt: usageLedgerTestBase.Add(time.Minute),
	}, "account-json")
	if err := chat.observe([]byte(`{"choices":[{"message":{"content":"not stored"}}],"usage":{"prompt_tokens":100,"completion_tokens":30,"total_tokens":130,"prompt_tokens_details":{"cached_tokens":20,"cache_write_tokens":10},"completion_tokens_details":{"reasoning_tokens":12}}}`)); err != nil {
		t.Fatal(err)
	}
	if err := chat.finish(context.Background(), accounting.StatusSucceeded, usageLedgerTestBase.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertUsageLedgerTokens(t, db, "request-json:1", 70, 30, 20, 10)

	anthropic := beginUsageLedgerTestAttempt(t, coordinator, usageRequestStart{
		RequestID: "request-sse", EmployeeID: "employee-sse", KeyID: "key-sse", PublicModel: "claude/中文",
		ProviderKind: anthropicAPIKeyProvider, Protocol: accounting.ProtocolAnthropicMessages, StartedAt: usageLedgerTestBase.Add(3 * time.Minute),
	}, "account-sse")
	events := []string{
		`{"type":"message_start","message":{"content":[],"usage":{"input_tokens":25,"output_tokens":1,"cache_read_input_tokens":100,"cache_creation_input_tokens":50}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"not stored"}}`,
		`{"type":"message_delta","usage":{"output_tokens":15}}`,
		`{"type":"message_delta","usage":{"output_tokens":15}}`,
	}
	for _, event := range events {
		if err := anthropic.observe([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	if err := anthropic.finish(context.Background(), accounting.StatusSucceeded, usageLedgerTestBase.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	assertUsageLedgerTokens(t, db, "request-sse:1", 25, 15, 100, 50)
}

func TestUsageLedgerFinishIsIdempotentAndUsesShutdownContext(t *testing.T) {
	db := openUsageLedgerTestDB(t, filepath.Join(t.TempDir(), "finish.db"))
	coordinator := startUsageLedgerTestCoordinator(t, db, usageLedgerTestBase)
	start := usageLedgerTestBase.Add(time.Minute)
	request, err := coordinator.beginRequest(context.Background(), usageRequestStart{
		RequestID: "request-finish", EmployeeID: "employee-finish", KeyID: "key-finish", PublicModel: "model/finish",
		ProviderKind: "openai-compatible", Protocol: accounting.ProtocolOpenAIResponses, StartedAt: start,
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := request.beginAttempt(context.Background(), "account-finish", start.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := request.beginAttempt(context.Background(), "account-finish", start.Add(time.Second))
	if err != nil || repeated != attempt {
		t.Fatalf("idempotent begin = %p, %v", repeated, err)
	}
	if _, err := request.beginAttempt(context.Background(), "another-account", start.Add(time.Second)); !errors.Is(err, errUsageLedgerConflict) {
		t.Fatalf("different repeated begin: %v", err)
	}
	if err := attempt.observe([]byte(`{"object":"response","usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0},"output_tokens_details":{"reasoning_tokens":1}}}`)); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	firstFinish := start.Add(2 * time.Second)
	if err := attempt.finish(cancelled, accounting.StatusSucceeded, firstFinish); err != nil {
		t.Fatalf("shutdown finish: %v", err)
	}
	if err := attempt.finish(context.Background(), accounting.StatusSucceeded, firstFinish.Add(time.Hour)); err != nil {
		t.Fatalf("repeated finish: %v", err)
	}
	if err := attempt.finish(context.Background(), accounting.StatusFailed, firstFinish); !errors.Is(err, errUsageLedgerConflict) {
		t.Fatalf("different finish: %v", err)
	}
	var attemptAt, requestAt string
	var attempts int
	if err := db.QueryRow(`SELECT finished_at FROM accounting_attempts WHERE id='request-finish:1'`).Scan(&attemptAt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT finished_at FROM accounting_requests WHERE id='request-finish'`).Scan(&requestAt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE request_id='request-finish'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	wantAt := firstFinish.UTC().Format(time.RFC3339Nano)
	if attemptAt != wantAt || requestAt != wantAt || attempts != 1 {
		t.Fatalf("finish snapshot attempt=%q request=%q count=%d, want %q/1", attemptAt, requestAt, attempts, wantAt)
	}
}

func TestUsageLedgerLocalFailureHasNoAttempt(t *testing.T) {
	db := openUsageLedgerTestDB(t, filepath.Join(t.TempDir(), "local-failure.db"))
	coordinator := startUsageLedgerTestCoordinator(t, db, usageLedgerTestBase)
	started := usageLedgerTestBase.Add(time.Minute)
	request, err := coordinator.beginRequest(context.Background(), usageRequestStart{
		RequestID: "request-local", EmployeeID: "employee-local", KeyID: "key-local", PublicModel: "model/本地失败",
		ProviderKind: geminiAPIKeyProvider, Protocol: accounting.ProtocolGeminiGenerateContent, StartedAt: started,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstFinish := started.Add(time.Second)
	if err := request.finishWithoutAttempt(context.Background(), accounting.StatusFailed, firstFinish); err != nil {
		t.Fatal(err)
	}
	if err := request.finishWithoutAttempt(context.Background(), accounting.StatusFailed, firstFinish.Add(time.Hour)); err != nil {
		t.Fatalf("idempotent local finish: %v", err)
	}
	if err := request.finishWithoutAttempt(context.Background(), accounting.StatusSucceeded, firstFinish); !errors.Is(err, errUsageLedgerInvalid) {
		t.Fatalf("local success: %v", err)
	}
	var attempts int
	var status, finished string
	if err := db.QueryRow(`SELECT status,finished_at FROM accounting_requests WHERE id='request-local'`).Scan(&status, &finished); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE request_id='request-local'`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if status != string(accounting.StatusFailed) || finished != firstFinish.Format(time.RFC3339Nano) || attempts != 0 {
		t.Fatalf("local failure status=%q finished=%q attempts=%d", status, finished, attempts)
	}
	if _, err := request.beginAttempt(context.Background(), "late-account", firstFinish); !errors.Is(err, errUsageLedgerConflict) {
		t.Fatalf("attempt after finish: %v", err)
	}

	cancelledRequest, err := coordinator.beginRequest(context.Background(), usageRequestStart{
		RequestID: "request-local-cancelled", EmployeeID: "employee-local", KeyID: "key-local", PublicModel: "model/cancelled",
		ProviderKind: geminiAPIKeyProvider, Protocol: accounting.ProtocolGeminiGenerateContent, StartedAt: started,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cancelledRequest.finishWithoutAttempt(context.Background(), accounting.StatusCancelled, firstFinish); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status FROM accounting_requests WHERE id='request-local-cancelled'`).Scan(&status); err != nil || status != string(accounting.StatusCancelled) {
		t.Fatalf("cancelled request status=%q err=%v", status, err)
	}
}

func TestUsageLedgerFailureKeepsUnknownUsageAndNoBody(t *testing.T) {
	db := openUsageLedgerTestDB(t, filepath.Join(t.TempDir(), "unknown.db"))
	coordinator := startUsageLedgerTestCoordinator(t, db, usageLedgerTestBase)
	attempt := beginUsageLedgerTestAttempt(t, coordinator, usageRequestStart{
		RequestID: "request-unknown", EmployeeID: "employee-unknown", KeyID: "key-unknown", PublicModel: "model/unknown",
		ProviderKind: anthropicAPIKeyProvider, Protocol: accounting.ProtocolAnthropicMessages, StartedAt: usageLedgerTestBase.Add(time.Minute),
	}, "account-unknown")
	const marker = "synthetic-secret-error-body"
	if err := attempt.observe([]byte(`{"type":"error","error":{"message":"` + marker + `"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := attempt.finish(context.Background(), accounting.StatusFailed, usageLedgerTestBase.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var input, output, cacheRead, cacheWrite, cost sql.NullInt64
	if err := db.QueryRow(`SELECT input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cost_micro FROM accounting_attempts WHERE id='request-unknown:1'`).Scan(&input, &output, &cacheRead, &cacheWrite, &cost); err != nil {
		t.Fatal(err)
	}
	if input.Valid || output.Valid || cacheRead.Valid || cacheWrite.Valid || cost.Valid {
		t.Fatalf("unknown usage became known: %v %v %v %v %v", input, output, cacheRead, cacheWrite, cost)
	}
	var schema string
	if err := db.QueryRow(`SELECT group_concat(sql,' ') FROM sqlite_master WHERE name IN ('accounting_requests','accounting_attempts')`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(schema, marker) {
		t.Fatal("response body was persisted")
	}
	if strings.Contains(fmt.Sprintf("%#v", attempt), marker) {
		t.Fatal("response body was retained in the coordinator")
	}
}

func TestUsageLedgerRestartRecoversInterrupted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")
	db := openUsageLedgerTestDB(t, path)
	coordinator := startUsageLedgerTestCoordinator(t, db, usageLedgerTestBase)
	started := usageLedgerTestBase.Add(time.Minute)
	request, err := coordinator.beginRequest(context.Background(), usageRequestStart{
		RequestID: "request-pending-attempt", EmployeeID: "employee-restart", KeyID: "key-restart", PublicModel: "model/restart",
		ProviderKind: "openai-compatible", Protocol: accounting.ProtocolOpenAIChatCompletions, StartedAt: started,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := request.beginAttempt(context.Background(), "account-restart", started.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.beginRequest(context.Background(), usageRequestStart{
		RequestID: "request-pending-local", EmployeeID: "employee-restart", KeyID: "key-restart", PublicModel: "model/restart",
		ProviderKind: geminiAPIKeyProvider, Protocol: accounting.ProtocolGeminiGenerateContent, StartedAt: started,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	restartedDB := openUsageLedgerTestDB(t, path)
	restarted := startUsageLedgerTestCoordinator(t, restartedDB, usageLedgerTestBase.Add(time.Hour))
	_ = restarted
	for _, tableAndID := range [][2]string{{"accounting_requests", "request-pending-attempt"}, {"accounting_requests", "request-pending-local"}, {"accounting_attempts", "request-pending-attempt:1"}} {
		var status string
		if err := restartedDB.QueryRow(`SELECT status FROM `+tableAndID[0]+` WHERE id=?`, tableAndID[1]).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status != string(accounting.StatusInterrupted) {
			t.Fatalf("%s/%s status = %q", tableAndID[0], tableAndID[1], status)
		}
	}
	var input sql.NullInt64
	if err := restartedDB.QueryRow(`SELECT input_tokens FROM accounting_attempts WHERE id='request-pending-attempt:1'`).Scan(&input); err != nil {
		t.Fatal(err)
	}
	if input.Valid {
		t.Fatalf("recovery invented usage: %v", input)
	}
}

func TestUsageLedgerPersistenceFailureIsReportedWithoutSQLDetail(t *testing.T) {
	db := openUsageLedgerTestDB(t, filepath.Join(t.TempDir(), "closed.db"))
	coordinator := startUsageLedgerTestCoordinator(t, db, usageLedgerTestBase)
	attempt := beginUsageLedgerTestAttempt(t, coordinator, usageRequestStart{
		RequestID: "request-closed", EmployeeID: "employee-closed", KeyID: "key-closed", PublicModel: "model/closed",
		ProviderKind: geminiAPIKeyProvider, Protocol: accounting.ProtocolGeminiGenerateContent, StartedAt: usageLedgerTestBase.Add(time.Minute),
	}, "account-closed")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	err := attempt.finish(context.Background(), accounting.StatusFailed, usageLedgerTestBase.Add(2*time.Minute))
	if !errors.Is(err, errUsageLedgerUnavailable) || err.Error() != errUsageLedgerUnavailable.Error() {
		t.Fatalf("persistence error = %v", err)
	}
	if strings.Contains(strings.ToLower(err.Error()), "database") || strings.Contains(strings.ToLower(err.Error()), "sql") {
		t.Fatalf("persistence detail leaked: %v", err)
	}

	bad := newUsageLedgerCoordinator(nil)
	if err := bad.start(context.Background()); !errors.Is(err, errUsageLedgerInvalid) && !errors.Is(err, errUsageLedgerUnavailable) {
		t.Fatalf("nil startup error = %v", err)
	}
}

func openUsageLedgerTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func startUsageLedgerTestCoordinator(t *testing.T, db *sql.DB, now time.Time) *usageLedgerCoordinator {
	t.Helper()
	coordinator := newUsageLedgerCoordinator(db)
	coordinator.now = func() time.Time { return now }
	if err := coordinator.start(context.Background()); err != nil {
		t.Fatalf("start coordinator: %v", err)
	}
	return coordinator
}

func beginUsageLedgerTestAttempt(t *testing.T, coordinator *usageLedgerCoordinator, input usageRequestStart, accountID string) *usageLedgerAttempt {
	t.Helper()
	request, err := coordinator.beginRequest(context.Background(), input)
	if err != nil {
		t.Fatalf("begin request: %v", err)
	}
	attempt, err := request.beginAttempt(context.Background(), accountID, input.StartedAt.Add(time.Second))
	if err != nil {
		t.Fatalf("begin attempt: %v", err)
	}
	return attempt
}

func assertUsageLedgerTokens(t *testing.T, db *sql.DB, attemptID string, input, output, cacheRead, cacheWrite int64) {
	t.Helper()
	var actualInput, actualOutput, actualCacheRead, actualCacheWrite int64
	var cost sql.NullInt64
	if err := db.QueryRow(`SELECT input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,cost_micro FROM accounting_attempts WHERE id=?`, attemptID).
		Scan(&actualInput, &actualOutput, &actualCacheRead, &actualCacheWrite, &cost); err != nil {
		t.Fatal(err)
	}
	if actualInput != input || actualOutput != output || actualCacheRead != cacheRead || actualCacheWrite != cacheWrite || cost.Valid {
		t.Fatalf("usage = %d/%d/%d/%d cost=%v, want %d/%d/%d/%d and unknown cost", actualInput, actualOutput, actualCacheRead, actualCacheWrite, cost, input, output, cacheRead, cacheWrite)
	}
}
