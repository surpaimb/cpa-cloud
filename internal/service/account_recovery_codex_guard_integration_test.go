package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/membership"
)

const successfulCodexRecoveryResponse = `{"id":"probe-response","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"OK"}]}],"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`

func enableRecoveryExecutionForTest(t *testing.T, app *App) {
	t.Helper()
	app.accountPool.mu.Lock()
	closed := app.accountPool.closed
	app.accountPool.mu.Unlock()
	if closed {
		t.Fatal("test account pool was unexpectedly closed")
	}
	app.cfg.AccountRecoveryEnabled = true
	if _, err := app.store.db.Exec(`UPDATE account_recovery_settings SET enabled=1,revision=revision+1,updated_at=? WHERE singleton=1`, formatAccountPoolTime(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	tx, err := app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	setting, err := loadAccountRecoverySettingsTx(context.Background(), tx)
	_ = tx.Rollback()
	if err != nil || !setting.Enabled {
		t.Fatalf("enable recovery setting: %+v %v", setting, err)
	}
}

func recoveryAcquireRequest(state accountRecoveryState) accountMaintenanceAcquireRequest {
	return accountMaintenanceAcquireRequest{
		OperationID: state.OperationID, AccountID: state.AccountID, CooldownEventID: state.CooldownEventID,
		ExpectedPoolRevision: state.PoolRevision, ExpectedAccountRevision: state.AccountRevision,
		ExpectedRecoveryRevision: state.RecoveryRevision,
	}
}

func newCodexRecoveryExecutionFixture(t *testing.T, suffix, operationID string) codexRecoveryRefreshFixture {
	t.Helper()
	f := newCodexRecoveryRefreshFixture(t, suffix, "authorization_code")
	if _, err := f.app.store.db.Exec(`UPDATE account_recovery_states SET operation_id=? WHERE account_id=? AND operation_id=?`, operationID, f.state.AccountID, f.state.OperationID); err != nil {
		t.Fatal(err)
	}
	f.state.OperationID = operationID
	return f
}

func TestRecoveryExecutionUsesSameTransactionCodexRefreshProof(t *testing.T) {
	f := newCodexRecoveryExecutionFixture(t, "execution-proof", "550e8400-e29b-41d4-a716-446655440001")
	enableRecoveryExecutionForTest(t, f.app)
	var oauthCalls, generationCalls atomic.Int32
	f.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		oauthCalls.Add(1)
		return successfulRecoveryOAuthResponse(t, "execution-proof"), nil
	})}
	f.app.responses = fakeCodexResponsesExecutor{fn: func(_ context.Context, credential *membership.CodexAuthCredential, body []byte, consume func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
		generationCalls.Add(1)
		if credential == nil || credential.RefreshTokenSecret() != "refresh-execution-proof" || consume != nil {
			t.Error("generation did not receive the credential proven by this refresh")
		}
		var request map[string]json.RawMessage
		if json.Unmarshal(body, &request) != nil {
			t.Error("invalid synthetic Responses request")
		} else {
			var model, input string
			_ = json.Unmarshal(request["model"], &model)
			_ = json.Unmarshal(request["input"], &input)
			if model != f.state.UpstreamModel || input != generationProbePrompt {
				t.Errorf("unexpected fixed probe mapping model=%q input=%q", model, input)
			}
		}
		return json.RawMessage(successfulCodexRecoveryResponse), nil
	}}

	code, receipt := f.app.executeRecoveryOperation(context.Background(), recoveryAcquireRequest(f.state))
	if code != accountPoolReleased || receipt == nil || oauthCalls.Load() != 1 || generationCalls.Load() != 1 {
		t.Fatalf("code=%s receipt=%v oauth=%d generation=%d", code, receipt != nil, oauthCalls.Load(), generationCalls.Load())
	}
	attempt, err := f.app.systemProbes.Get(context.Background(), f.state.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if attempt.Status != accounting.SystemProbeSucceeded || attempt.Result == nil || *attempt.Result != accounting.SystemProbeGenerationOK || attempt.AccountRevision != 2 {
		t.Fatalf("probe did not persist adopted revision: %+v", attempt)
	}
	assertRecoveryExecutionCounts(t, f.app, f.state.AccountID, 0, 0)
}

func TestRecoveryExecutionRejectsReimportAfterGuardedGetter(t *testing.T) {
	f := newCodexRecoveryExecutionFixture(t, "post-getter-reimport", "550e8400-e29b-41d4-a716-446655440002")
	enableRecoveryExecutionForTest(t, f.app)
	var oauthCalls, generationCalls atomic.Int32
	f.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		oauthCalls.Add(1)
		return successfulRecoveryOAuthResponse(t, "before-execution-reimport"), nil
	})}
	f.app.responses = fakeCodexResponsesExecutor{fn: func(context.Context, *membership.CodexAuthCredential, []byte, func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
		generationCalls.Add(1)
		return json.RawMessage(successfulCodexRecoveryResponse), nil
	}}

	selected, credential, adopted, failure := f.app.acquireCodexRecoveryCredential(context.Background(), f.selected, f.state)
	if failure != nil || credential == nil || selected.Revision != 2 || adopted.AccountRevision != 2 || adopted.RecoveryRevision != 2 {
		t.Fatalf("guarded getter selected=%d adopted=%+v failure=%v", selected.Revision, adopted, failure)
	}
	credential.Destroy()

	raw := []byte(syntheticCodexAuth(t, "replacement-after-getter", time.Now().Add(time.Hour)))
	replacement, err := f.app.secrets.encryptCodexAuth(f.state.AccountID, raw)
	clear(raw)
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := f.app.acquireCodexMutationLock(context.Background(), f.state.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.app.store.db.BeginTx(context.Background(), nil)
	if err == nil {
		_, err = tx.Exec(`UPDATE upstreams SET credential_ciphertext=?,revision=3,credential_state=? WHERE id=? AND revision=2`, replacement, codexStateImported, f.state.AccountID)
	}
	if err == nil {
		_, err = tx.Exec(`DELETE FROM codex_oauth_bindings WHERE upstream_id=?`, f.state.AccountID)
	}
	if err == nil {
		_, err = tx.Exec(`DELETE FROM codex_oauth_refresh_states WHERE upstream_id=?`, f.state.AccountID)
	}
	if err == nil {
		err = tx.Commit()
	} else if tx != nil {
		_ = tx.Rollback()
	}
	unlock()
	if err != nil {
		t.Fatal(err)
	}

	code, receipt := f.app.executeRecoveryOperation(context.Background(), recoveryAcquireRequest(adopted))
	if code != accountPoolConfigurationChanged || receipt != nil || generationCalls.Load() != 0 || oauthCalls.Load() != 1 {
		t.Fatalf("code=%s receipt=%v oauth=%d generation=%d", code, receipt != nil, oauthCalls.Load(), generationCalls.Load())
	}
	if _, err := f.app.systemProbes.Get(context.Background(), adopted.OperationID); !errors.Is(err, accounting.ErrNotFound) {
		t.Fatalf("stale credential created a probe attempt: %v", err)
	}
	stored, err := scanAccountRecoveryState(f.app.store.db.QueryRow(accountRecoverySelect+` WHERE account_id=?`, adopted.AccountID))
	if err != nil || stored.AccountRevision != 2 || stored.RecoveryRevision != 2 || stored.State != recoveryRequired {
		t.Fatalf("reimport overwrote recovery proof: %+v %v", stored, err)
	}
}
