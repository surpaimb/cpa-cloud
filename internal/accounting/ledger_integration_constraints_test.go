package accounting

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLedgerIntegrationModelIDConstraints(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "ledger.db"))
	defer db.Close()

	tests := []struct {
		name    string
		modelID string
		valid   bool
	}{
		{name: "vendor slash", modelID: "vendor/model", valid: true},
		{name: "unicode", modelID: "通义/千问", valid: true},
		{name: "byte boundary", modelID: strings.Repeat("m", 128), valid: true},
		{name: "whitespace", modelID: "vendor model", valid: false},
		{name: "control", modelID: "vendor/model\nshadow", valid: false},
		{name: "invalid UTF-8", modelID: string([]byte{'m', 0xff, 'x'}), valid: false},
		{name: "over byte limit", modelID: strings.Repeat("界", 43), valid: false},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := RequestStart{
				ID:         "request-model-" + string(rune('a'+index)),
				EmployeeID: "employee-1",
				KeyID:      "key-1",
				ModelID:    test.modelID,
				Provider:   ProviderOpenAICompatible,
				StartedAt:  testStart,
			}
			err := ledger.BeginRequest(context.Background(), request)
			if test.valid {
				if err != nil {
					t.Fatalf("valid model ID rejected: %v", err)
				}
				var stored string
				if err := db.QueryRow(`SELECT model_id FROM accounting_requests WHERE id=?`, request.ID).Scan(&stored); err != nil || stored != test.modelID {
					t.Fatalf("stored model=%q err=%v", stored, err)
				}
				return
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid model ID error=%v", err)
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_requests WHERE id=?`, request.ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("invalid model persisted count=%d err=%v", count, err)
			}
		})
	}
}

func TestLedgerIntegrationAttemptProviderMatchesRequest(t *testing.T) {
	db, ledger := openTestLedger(t, filepath.Join(t.TempDir(), "ledger.db"))
	defer db.Close()
	ctx := context.Background()

	request := RequestStart{
		ID: "request-provider", EmployeeID: "employee-1", KeyID: "key-1", ModelID: "vendor/模型",
		Provider: ProviderAnthropic, StartedAt: testStart,
	}
	if err := ledger.BeginRequest(ctx, request); err != nil {
		t.Fatal(err)
	}
	mismatch := AttemptStart{
		ID: "attempt-provider-mismatch", RequestID: request.ID, AccountID: "account-gemini",
		Provider: ProviderGemini, Dispatch: DispatchPrimary, StartedAt: testStart,
	}
	if err := ledger.BeginAttempt(ctx, mismatch); !errors.Is(err, ErrConflict) {
		t.Fatalf("provider mismatch error=%v", err)
	}
	var mismatchCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE id=?`, mismatch.ID).Scan(&mismatchCount); err != nil || mismatchCount != 0 {
		t.Fatalf("provider mismatch persisted count=%d err=%v", mismatchCount, err)
	}

	matching := AttemptStart{
		ID: "attempt-provider-match", RequestID: request.ID, AccountID: "account-anthropic",
		Provider: ProviderAnthropic, Dispatch: DispatchPrimary, StartedAt: testStart,
	}
	if err := ledger.BeginAttempt(ctx, matching); err != nil {
		t.Fatalf("matching provider: %v", err)
	}
	if err := ledger.BeginAttempt(ctx, matching); err != nil {
		t.Fatalf("matching provider replay: %v", err)
	}
	if err := ledger.FinishAttempt(ctx, AttemptFinish{ID: matching.ID, Status: StatusSucceeded, FinishedAt: testFinish, Usage: Usage{}}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequest(ctx, RequestFinish{ID: request.ID, Status: StatusSucceeded, FinishedAt: testFinish.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttempt(ctx, matching); err != nil {
		t.Fatalf("historical exact replay after request terminal: %v", err)
	}
}
