package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

type codexRecoveryRefreshFixture struct {
	app      *App
	selected route
	state    accountRecoveryState
}

func newCodexRecoveryRefreshFixture(t *testing.T, suffix, source string) codexRecoveryRefreshFixture {
	t.Helper()
	app := openOAuthTestApp(t, t.TempDir())
	t.Cleanup(func() { _ = app.Close() })
	id := insertRefreshLifecycleAccount(t, app, "ups_recovery_refresh_"+suffix, time.Now().Add(time.Minute), true)
	if source == "import" {
		if _, err := app.store.db.Exec(`DELETE FROM codex_oauth_refresh_states WHERE upstream_id=?`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := app.store.db.Exec(`DELETE FROM codex_oauth_bindings WHERE upstream_id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	model := "recovery-refresh-" + suffix
	if _, err := app.store.db.Exec(`INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES(?,?,?,?,?)`, model, id, "actual-"+suffix, 1, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES(?,1,?)`, model, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,priority,weight,max_concurrency,position) VALUES(?,?,?,0,1,1,0)`, model, id, "actual-"+suffix); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state := accountRecoveryState{
		AccountID: id, CooldownEventID: "event-" + suffix, OperationID: "operation-recovery-" + suffix,
		RecoveryRevision: 1, PoolRevision: 1, AccountRevision: 1, ProviderKind: codexMembershipProvider,
		SourceSnapshot: source, PublicModel: model, UpstreamModel: "actual-" + suffix,
		Protocol: string(accounting.ProtocolOpenAIResponses), State: recoveryRequired,
		NextProbeAt: now, CreatedAt: now, UpdatedAt: now,
	}
	if source == "authorization_code" {
		state.ClientID = testOAuthClientID
	}
	seedRecoveryExecutionState(t, app, state)
	selected := route{AccountID: id, Endpoint: codexMembershipEndpoint, UpstreamModel: state.UpstreamModel, ProviderKind: codexMembershipProvider, Revision: 1, KeyVersion: 2}
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext,credential_state FROM upstreams WHERE id=?`, id).Scan(&selected.Ciphertext, &selected.CredentialState); err != nil {
		t.Fatal(err)
	}
	return codexRecoveryRefreshFixture{app: app, selected: selected, state: state}
}

func successfulRecoveryOAuthResponse(t *testing.T, marker string) *http.Response {
	t.Helper()
	access := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	idToken := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-" + marker}})
	return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": access, "id_token": idToken, "refresh_token": "refresh-" + marker})
}

func TestAcquireCodexRecoveryCredentialAdoptsOnlyItsRefresh(t *testing.T) {
	f := newCodexRecoveryRefreshFixture(t, "adopt", "authorization_code")
	var calls atomic.Int32
	f.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return successfulRecoveryOAuthResponse(t, "adopted"), nil
	})}

	selected, credential, adopted, failure := f.app.acquireCodexRecoveryCredential(context.Background(), f.selected, f.state)
	if failure != nil || credential == nil {
		t.Fatalf("acquire failure=%v", failure)
	}
	defer credential.Destroy()
	if calls.Load() != 1 || selected.Revision != 2 || adopted.AccountRevision != 2 || adopted.RecoveryRevision != 2 {
		t.Fatalf("calls=%d selected=%d adopted account=%d recovery=%d", calls.Load(), selected.Revision, adopted.AccountRevision, adopted.RecoveryRevision)
	}
	stored, err := scanAccountRecoveryState(f.app.store.db.QueryRow(accountRecoverySelect+` WHERE account_id=?`, f.state.AccountID))
	if err != nil {
		t.Fatal(err)
	}
	if stored.AccountRevision != 2 || stored.RecoveryRevision != 2 || stored.OperationID != f.state.OperationID || stored.State != recoveryRequired {
		t.Fatalf("stored adoption=%+v", stored)
	}
	var upstreamRevision, leases int
	if err := f.app.store.db.QueryRow(`SELECT revision FROM upstreams WHERE id=?`, f.state.AccountID).Scan(&upstreamRevision); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_maintenance_leases WHERE account_id=?`, f.state.AccountID).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if upstreamRevision != 2 || leases != 0 {
		t.Fatalf("upstream revision=%d maintenance leases=%d", upstreamRevision, leases)
	}
}

func TestAcquireCodexRecoveryCredentialRejectsStaleGuardBeforeOAuth(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, codexRecoveryRefreshFixture)
	}{
		{name: "account revision", mutate: func(t *testing.T, f codexRecoveryRefreshFixture) {
			if _, err := f.app.store.db.Exec(`UPDATE upstreams SET revision=2 WHERE id=?`, f.state.AccountID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "client binding", mutate: func(t *testing.T, f codexRecoveryRefreshFixture) {
			if _, err := f.app.store.db.Exec(`UPDATE codex_oauth_bindings SET client_id='another-client' WHERE upstream_id=?`, f.state.AccountID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "operation", mutate: func(t *testing.T, f codexRecoveryRefreshFixture) {
			if _, err := f.app.store.db.Exec(`UPDATE account_recovery_states SET operation_id='different-operation' WHERE account_id=?`, f.state.AccountID); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCodexRecoveryRefreshFixture(t, strings.ReplaceAll(test.name, " ", "-"), "authorization_code")
			test.mutate(t, f)
			var calls atomic.Int32
			f.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, errors.New("OAuth must not run")
			})}
			_, credential, _, failure := f.app.acquireCodexRecoveryCredential(context.Background(), f.selected, f.state)
			if credential != nil || failure == nil || failure.code != "revision_conflict" || calls.Load() != 0 {
				t.Fatalf("credential=%v failure=%v calls=%d", credential != nil, failure, calls.Load())
			}
		})
	}
}

func TestAcquireCodexRecoveryImportNeverBorrowsOAuthBinding(t *testing.T) {
	f := newCodexRecoveryRefreshFixture(t, "import", "import")
	var calls atomic.Int32
	f.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("OAuth must not run")
	})}
	_, credential, state, failure := f.app.acquireCodexRecoveryCredential(context.Background(), f.selected, f.state)
	if failure != nil || credential == nil || state.AccountRevision != 1 || calls.Load() != 0 {
		t.Fatalf("credential=%v failure=%v state=%+v calls=%d", credential != nil, failure, state, calls.Load())
	}
	credential.Destroy()
	if _, err := f.app.store.db.Exec(`INSERT INTO codex_oauth_bindings(upstream_id,client_id,source,created_at) VALUES(?,?,?,?)`, f.state.AccountID, testOAuthClientID, "authorization_code", utcNow()); err != nil {
		t.Fatal(err)
	}
	_, credential, _, failure = f.app.acquireCodexRecoveryCredential(context.Background(), f.selected, f.state)
	if credential != nil || failure == nil || failure.code != "revision_conflict" || calls.Load() != 0 {
		t.Fatalf("bound import credential=%v failure=%v calls=%d", credential != nil, failure, calls.Load())
	}
}

func TestAcquireCodexRecoveryRefreshConflictRollsBackAndDoesNotReplay(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, codexRecoveryRefreshFixture)
	}{
		{name: "clear", mutate: func(t *testing.T, f codexRecoveryRefreshFixture) {
			if _, err := f.app.store.db.Exec(`DELETE FROM account_recovery_states WHERE account_id=?`, f.state.AccountID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.app.store.db.Exec(`DELETE FROM account_pool_runtime_cooldowns WHERE account_id=?`, f.state.AccountID); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hook failure", mutate: func(t *testing.T, f codexRecoveryRefreshFixture) {
			if _, err := f.app.store.db.Exec(`CREATE TRIGGER reject_recovery_adoption BEFORE UPDATE OF account_revision ON account_recovery_states BEGIN SELECT RAISE(ABORT,'private-refresh-token-sentinel'); END`); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCodexRecoveryRefreshFixture(t, "conflict-"+strings.ReplaceAll(test.name, " ", "-"), "authorization_code")
			started := make(chan struct{})
			release := make(chan struct{})
			var calls atomic.Int32
			f.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				close(started)
				<-release
				return successfulRecoveryOAuthResponse(t, "conflict"), nil
			})}
			type result struct{ failure *codexRefreshFailure }
			done := make(chan result, 1)
			go func() {
				_, credential, _, failure := f.app.acquireCodexRecoveryCredential(context.Background(), f.selected, f.state)
				if credential != nil {
					credential.Destroy()
				}
				done <- result{failure: failure}
			}()
			<-started
			test.mutate(t, f)
			close(release)
			got := <-done
			if got.failure == nil || strings.Contains(got.failure.Error(), "private-refresh-token-sentinel") || calls.Load() != 1 {
				t.Fatalf("failure=%v calls=%d", got.failure, calls.Load())
			}
			var revision int64
			var refreshState string
			if err := f.app.store.db.QueryRow(`SELECT u.revision,r.state FROM upstreams u JOIN codex_oauth_refresh_states r ON r.upstream_id=u.id WHERE u.id=?`, f.state.AccountID).Scan(&revision, &refreshState); err != nil {
				t.Fatal(err)
			}
			if revision != 1 || refreshState != "paused" {
				t.Fatalf("partial refresh revision=%d state=%q", revision, refreshState)
			}
			_, credential, _, second := f.app.acquireCodexRecoveryCredential(context.Background(), f.selected, f.state)
			if credential != nil || second == nil || calls.Load() != 1 {
				t.Fatalf("paused refresh replayed credential=%v failure=%v calls=%d", credential != nil, second, calls.Load())
			}
		})
	}
}

func TestAcquireCodexRecoveryReimportWaitsForRefreshAndCannotBeAdopted(t *testing.T) {
	f := newCodexRecoveryRefreshFixture(t, "reimport", "authorization_code")
	started := make(chan struct{})
	release := make(chan struct{})
	f.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return successfulRecoveryOAuthResponse(t, "before-reimport"), nil
	})}
	type acquireResult struct {
		selected route
		state    accountRecoveryState
		failure  *codexRefreshFailure
	}
	acquired := make(chan acquireResult, 1)
	go func() {
		selected, credential, state, failure := f.app.acquireCodexRecoveryCredential(context.Background(), f.selected, f.state)
		if credential != nil {
			credential.Destroy()
		}
		acquired <- acquireResult{selected: selected, state: state, failure: failure}
	}()
	<-started
	reimportStarted := make(chan struct{})
	reimportDone := make(chan error, 1)
	go func() {
		close(reimportStarted)
		unlock, err := f.app.acquireCodexMutationLock(context.Background(), f.state.AccountID)
		if err != nil {
			reimportDone <- err
			return
		}
		defer unlock()
		_, err = f.app.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id=?`, f.state.AccountID)
		if err == nil {
			_, err = f.app.store.db.Exec(`DELETE FROM codex_oauth_bindings WHERE upstream_id=?`, f.state.AccountID)
		}
		if err == nil {
			_, err = f.app.store.db.Exec(`DELETE FROM codex_oauth_refresh_states WHERE upstream_id=?`, f.state.AccountID)
		}
		reimportDone <- err
	}()
	<-reimportStarted
	select {
	case err := <-reimportDone:
		t.Fatalf("reimport crossed refresh mutation lock: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	got := <-acquired
	if got.failure != nil || got.selected.Revision != 2 || got.state.AccountRevision != 2 || got.state.RecoveryRevision != 2 {
		t.Fatalf("refresh proof=%+v state=%+v failure=%v", got.selected, got.state, got.failure)
	}
	if err := <-reimportDone; err != nil {
		t.Fatal(err)
	}
	var current int64
	if err := f.app.store.db.QueryRow(`SELECT revision FROM upstreams WHERE id=?`, f.state.AccountID).Scan(&current); err != nil || current != 3 {
		t.Fatalf("reimport revision=%d err=%v", current, err)
	}
	_, credential, _, failure := f.app.acquireCodexRecoveryCredential(context.Background(), got.selected, got.state)
	if credential != nil || failure == nil || failure.code != "revision_conflict" {
		t.Fatalf("another operation revision was adopted credential=%v failure=%v", credential != nil, failure)
	}
}

func TestAcquireCodexRecoveryUncertainOutcomePausesWithoutSecretOrReplay(t *testing.T) {
	f := newCodexRecoveryRefreshFixture(t, "uncertain", "authorization_code")
	const secret = "private-network-secret-sentinel"
	var calls atomic.Int32
	f.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New(secret)
	})}
	for attempt := 0; attempt < 2; attempt++ {
		_, credential, _, failure := f.app.acquireCodexRecoveryCredential(context.Background(), f.selected, f.state)
		if credential != nil || failure == nil || strings.Contains(failure.Error(), secret) {
			t.Fatalf("attempt=%d credential=%v failure=%v", attempt, credential != nil, failure)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("uncertain refresh replayed %d times", calls.Load())
	}
	var state, reason string
	if err := f.app.store.db.QueryRow(`SELECT state,reason_code FROM codex_oauth_refresh_states WHERE upstream_id=?`, f.state.AccountID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "paused" || reason != "uncertain_refresh_outcome" || strings.Contains(reason, secret) {
		t.Fatalf("state=%q reason=%q", state, reason)
	}
}
