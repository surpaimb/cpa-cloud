package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/egress"
)

type outboundProxyHandshakeFixture struct {
	base        outboundProxyTestStore
	app         *App
	coordinator *outboundProxyTestCoordinator
	proxy       outboundProxyView
	upstreamID  string
	upstreamRev int64
}

func newOutboundProxyHandshakeFixture(t *testing.T) *outboundProxyHandshakeFixture {
	t.Helper()
	base := newOutboundProxyTestStore(t)
	base.store.allowLoopbackForTesting = true
	proxy, err := base.store.Create(context.Background(), testProxyCreate("21000000-0000-4000-8000-000000000001", "proxy.example"))
	if err != nil {
		t.Fatal(err)
	}
	insertProxyTestUpstream(t, base.db, "ups_handshake", "openai-compatible", "https://api.example/v1", 1)
	binding, err := base.store.SetBinding(context.Background(), upstreamProxyBindingInput{UpstreamID: "ups_handshake", ExpectedUpstreamRevision: 1, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: true})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := egress.NewClientCache(8)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{AllowLoopbackUpstream: true}, store: &store{db: base.db}, secrets: base.secrets, outboundProxies: base.store, proxyClients: cache}
	coordinator, err := newOutboundProxyTestCoordinator(app)
	if err != nil {
		cache.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		coordinator.Close()
		cache.Close()
	})
	return &outboundProxyHandshakeFixture{base: base, app: app, coordinator: coordinator, proxy: proxy, upstreamID: "ups_handshake", upstreamRev: binding.UpstreamRevision}
}

func (f *outboundProxyHandshakeFixture) input(operation string) outboundProxyTestInput {
	return outboundProxyTestInput{OperationID: operation, ExpectedProxyRevision: f.proxy.Revision, ExpectedConnectionRevision: f.proxy.ConnectionRevision, UpstreamID: f.upstreamID, ExpectedUpstreamRevision: f.upstreamRev}
}

func waitOutboundProxyTest(t *testing.T, c *outboundProxyTestCoordinator, operation, state string) outboundProxyTestOperationView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		view, err := c.load(context.Background(), operation)
		if err == nil && view.State == state {
			return view
		}
		time.Sleep(5 * time.Millisecond)
	}
	view, err := c.load(context.Background(), operation)
	t.Fatalf("operation %s state=%+v err=%v, wanted %s", operation, view, err, state)
	return outboundProxyTestOperationView{}
}

func TestOutboundProxyTestIdempotencyConflictAndSingleProbe(t *testing.T) {
	f := newOutboundProxyHandshakeFixture(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	f.coordinator.probeTLS = func(ctx context.Context, _ *egress.Client, host, port string) error {
		calls.Add(1)
		if host != "api.example" || port != "443" {
			t.Errorf("target=%s:%s", host, port)
		}
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	input := f.input("21000000-0000-4000-8000-000000000002")
	created, replay, err := f.coordinator.create(context.Background(), f.proxy.ID, input)
	if err != nil || replay || created.State != outboundProxyTestPending {
		t.Fatalf("create=%+v replay=%v err=%v", created, replay, err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("probe did not start")
	}
	replayed, replay, err := f.coordinator.create(context.Background(), f.proxy.ID, input)
	if err != nil || !replay || replayed.OperationID != input.OperationID {
		t.Fatalf("replay=%+v replay=%v err=%v", replayed, replay, err)
	}
	changed := input
	changed.ExpectedUpstreamRevision++
	if _, _, err := f.coordinator.create(context.Background(), f.proxy.ID, changed); !errors.Is(err, errOutboundProxyTestOperationConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
	second := f.input("21000000-0000-4000-8000-000000000003")
	if _, _, err := f.coordinator.create(context.Background(), f.proxy.ID, second); !errors.Is(err, errOutboundProxyTestInProgress) {
		t.Fatalf("same proxy active error=%v", err)
	}
	close(release)
	finished := waitOutboundProxyTest(t, f.coordinator, input.OperationID, outboundProxyTestCompleted)
	if finished.ResultCode == nil || *finished.ResultCode != outboundProxyTestHandshakeOK || calls.Load() != 1 || finished.LatencyMS == nil {
		t.Fatalf("finished=%+v calls=%d", finished, calls.Load())
	}
}

func TestOutboundProxyTestVersionChangeWinsOverHandshake(t *testing.T) {
	f := newOutboundProxyHandshakeFixture(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	f.coordinator.probeTLS = func(ctx context.Context, _ *egress.Client, _, _ string) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	operation := "21000000-0000-4000-8000-000000000004"
	if _, _, err := f.coordinator.create(context.Background(), f.proxy.ID, f.input(operation)); err != nil {
		t.Fatal(err)
	}
	<-entered
	updated, err := f.base.store.Update(context.Background(), outboundProxyUpdateInput{ID: f.proxy.ID, ExpectedRevision: 1, Name: f.proxy.Name, Scheme: f.proxy.Scheme, Host: f.proxy.Host, Port: f.proxy.Port, AddressScope: f.proxy.AddressScope, Enabled: false, CredentialMode: outboundProxyCredentialKeep})
	if err != nil || !updated.ConnectionChanged {
		t.Fatalf("disable=%+v err=%v", updated, err)
	}
	close(release)
	finished := waitOutboundProxyTest(t, f.coordinator, operation, outboundProxyTestCompleted)
	if finished.ResultCode == nil || *finished.ResultCode != outboundProxyTestConfigurationChanged {
		t.Fatalf("finished=%+v", finished)
	}
	// Resolve the exact row as if the preceding configuration_changed commit
	// had returned an uncertain error to a worker holding handshake_ok.
	if err := f.coordinator.finalize(finished, outboundProxyTestHandshakeOK, *finished.FinishedAt, *finished.LatencyMS); err != nil {
		t.Fatalf("resolve uncertain configuration-changed commit: %v", err)
	}
}

func TestOutboundProxyTestFinalizeRetriesMetadataWithoutReconnect(t *testing.T) {
	f := newOutboundProxyHandshakeFixture(t)
	var probes, finalizes atomic.Int32
	f.coordinator.probeTLS = func(context.Context, *egress.Client, string, string) error {
		probes.Add(1)
		return egress.ErrProxyRejected
	}
	f.coordinator.beforeFinalize = func(attempt int) error {
		finalizes.Add(1)
		if attempt < 3 {
			return errors.New("synthetic storage interruption")
		}
		return nil
	}
	operation := "21000000-0000-4000-8000-000000000005"
	if _, _, err := f.coordinator.create(context.Background(), f.proxy.ID, f.input(operation)); err != nil {
		t.Fatal(err)
	}
	finished := waitOutboundProxyTest(t, f.coordinator, operation, outboundProxyTestCompleted)
	if probes.Load() != 1 || finalizes.Load() != 3 || finished.ResultCode == nil || *finished.ResultCode != outboundProxyTestProxyUnavailable {
		t.Fatalf("probes=%d finalizes=%d result=%+v", probes.Load(), finalizes.Load(), finished)
	}
	originalFinished, originalLatency := *finished.FinishedAt, *finished.LatencyMS
	if err := f.coordinator.finalize(finished, *finished.ResultCode, originalFinished.Add(time.Second), originalLatency+1); err == nil {
		t.Fatal("conflicting repeated settlement was accepted")
	}
	reloaded, err := f.coordinator.load(context.Background(), operation)
	if err != nil || !reloaded.FinishedAt.Equal(originalFinished) || *reloaded.LatencyMS != originalLatency {
		t.Fatalf("terminal metadata changed: %+v err=%v", reloaded, err)
	}
}

func TestOutboundProxyTestUnsettledFinishInterruptsOnRestartWithoutReconnect(t *testing.T) {
	f := newOutboundProxyHandshakeFixture(t)
	var probes atomic.Int32
	f.coordinator.probeTLS = func(context.Context, *egress.Client, string, string) error {
		probes.Add(1)
		return nil
	}
	f.coordinator.beforeFinalize = func(int) error { return errors.New("synthetic persistent storage interruption") }
	operation := "21000000-0000-4000-8000-000000000011"
	if _, _, err := f.coordinator.create(context.Background(), f.proxy.ID, f.input(operation)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		f.coordinator.mu.Lock()
		_, running := f.coordinator.running[operation]
		f.coordinator.mu.Unlock()
		if !running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	unfinished, err := f.coordinator.load(context.Background(), operation)
	if err != nil || unfinished.State != outboundProxyTestInProgress || probes.Load() != 1 {
		t.Fatalf("unfinished=%+v probes=%d err=%v", unfinished, probes.Load(), err)
	}
	f.coordinator.Close()
	restarted, err := newOutboundProxyTestCoordinator(f.app)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	finished, err := restarted.load(context.Background(), operation)
	if err != nil || finished.ResultCode == nil || *finished.ResultCode != outboundProxyTestInterrupted || probes.Load() != 1 {
		t.Fatalf("recovered=%+v probes=%d err=%v", finished, probes.Load(), err)
	}
}

func TestOutboundProxyTestCloseCancelsAndWaitsForSettlement(t *testing.T) {
	f := newOutboundProxyHandshakeFixture(t)
	entered := make(chan struct{})
	f.coordinator.probeTLS = func(ctx context.Context, _ *egress.Client, _, _ string) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	operation := "21000000-0000-4000-8000-000000000006"
	if _, _, err := f.coordinator.create(context.Background(), f.proxy.ID, f.input(operation)); err != nil {
		t.Fatal(err)
	}
	<-entered
	f.coordinator.Close()
	finished := waitOutboundProxyTest(t, f.coordinator, operation, outboundProxyTestCompleted)
	if finished.ResultCode == nil || *finished.ResultCode != outboundProxyTestCancelled {
		t.Fatalf("finished=%+v", finished)
	}
	if _, _, err := f.coordinator.create(context.Background(), f.proxy.ID, f.input("21000000-0000-4000-8000-000000000007")); !errors.Is(err, errOutboundProxyTestUnavailable) {
		t.Fatalf("create after close error=%v", err)
	}
}

func TestOutboundProxyTestRestartInterruptsWithoutProbeAndClampsClock(t *testing.T) {
	f := newOutboundProxyHandshakeFixture(t)
	f.coordinator.Close()
	secondProxy, err := f.base.store.Create(context.Background(), testProxyCreate("21000000-0000-4000-8000-000000000012", "second-proxy.example"))
	if err != nil {
		t.Fatal(err)
	}
	insertProxyTestUpstream(t, f.base.db, "ups_handshake_second", "anthropic-api-key", "https://second-api.example/v1", 1)
	secondBinding, err := f.base.store.SetBinding(context.Background(), upstreamProxyBindingInput{UpstreamID: "ups_handshake_second", ExpectedUpstreamRevision: 1, ProxyID: secondProxy.ID, ExpectedProxyRevision: 1, Bind: true})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(time.Hour)
	started := created.Add(time.Minute)
	for i, state := range []string{outboundProxyTestPending, outboundProxyTestInProgress} {
		operation := []string{"21000000-0000-4000-8000-000000000008", "21000000-0000-4000-8000-000000000009"}[i]
		proxyID, upstreamID, upstreamRevision := f.proxy.ID, f.upstreamID, f.upstreamRev
		if i == 1 {
			proxyID, upstreamID, upstreamRevision = secondProxy.ID, "ups_handshake_second", secondBinding.UpstreamRevision
		}
		var startedValue any
		if state == outboundProxyTestInProgress {
			startedValue = formatAccountPoolTime(started)
		}
		if _, err := f.base.db.Exec(`INSERT INTO outbound_proxy_test_operations(operation_id,proxy_id,proxy_revision,connection_revision,upstream_id,upstream_revision,state,created_at,started_at) VALUES(?,?,?,?,?,?,?,?,?)`, operation, proxyID, 1, 1, upstreamID, upstreamRevision, state, formatAccountPoolTime(created), startedValue); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	restarted := &outboundProxyTestCoordinator{app: f.app, ctx: ctx, cancel: cancel, running: make(map[string]struct{}), now: func() time.Time { return created.Add(-time.Hour) }}
	if err := restarted.migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	for _, operation := range []string{"21000000-0000-4000-8000-000000000008", "21000000-0000-4000-8000-000000000009"} {
		view, err := restarted.load(context.Background(), operation)
		if err != nil || view.ResultCode == nil || *view.ResultCode != outboundProxyTestInterrupted || view.FinishedAt == nil || view.StartedAt == nil || view.FinishedAt.Before(*view.StartedAt) || view.LatencyMS == nil || *view.LatencyMS != 0 {
			t.Fatalf("recovered=%+v err=%v", view, err)
		}
	}
}

func TestOutboundProxyTestCapacityAndHistoryReplayPriority(t *testing.T) {
	f := newOutboundProxyHandshakeFixture(t)
	f.coordinator.Close()
	stamp := formatAccountPoolTime(time.Now().UTC())
	for i := 0; i < outboundProxyTestMaxActive; i++ {
		operation := "22000000-0000-4000-8000-00000000000" + strconv.Itoa(i)
		if _, err := f.base.db.Exec(`INSERT INTO outbound_proxy_test_operations(operation_id,proxy_id,proxy_revision,connection_revision,upstream_id,upstream_revision,state,created_at) VALUES(?,?,?,?,?,?,?,?)`, operation, f.proxy.ID, 1, 1, f.upstreamID, f.upstreamRev, outboundProxyTestPending, stamp); err != nil {
			t.Fatal(err)
		}
	}
	f.coordinator.closed = false
	input := f.input("22000000-0000-4000-8000-000000000000")
	view, replay, err := f.coordinator.create(context.Background(), f.proxy.ID, input)
	if err != nil || !replay || view.OperationID != input.OperationID {
		t.Fatalf("replay before cap=%+v replay=%v err=%v", view, replay, err)
	}
	if _, _, err := f.coordinator.create(context.Background(), f.proxy.ID, f.input("22000000-0000-4000-8000-000000000099")); !errors.Is(err, errOutboundProxyTestCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
}

func TestOutboundProxyTestHistoryLimitDoesNotDeleteIdempotencyKeys(t *testing.T) {
	f := newOutboundProxyHandshakeFixture(t)
	f.coordinator.Close()
	stamp := formatAccountPoolTime(time.Now().UTC())
	_, err := f.base.db.Exec(`WITH RECURSIVE ids(n) AS (SELECT 0 UNION ALL SELECT n+1 FROM ids WHERE n<9999)
		INSERT INTO outbound_proxy_test_operations(operation_id,proxy_id,proxy_revision,connection_revision,upstream_id,upstream_revision,state,result_code,created_at,started_at,finished_at,latency_ms)
		SELECT printf('25000000-0000-4000-8000-%012d',n),?,?,?,?,?,'completed','interrupted',?,?,?,0 FROM ids`, f.proxy.ID, 1, 1, f.upstreamID, f.upstreamRev, stamp, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	f.coordinator.closed = false
	replayInput := f.input("25000000-0000-4000-8000-000000000123")
	if view, replay, err := f.coordinator.create(context.Background(), f.proxy.ID, replayInput); err != nil || !replay || view.OperationID != replayInput.OperationID {
		t.Fatalf("history replay=%+v replay=%v err=%v", view, replay, err)
	}
	if _, _, err := f.coordinator.create(context.Background(), f.proxy.ID, f.input("25000000-0000-4000-8000-000000099999")); !errors.Is(err, errOutboundProxyTestHistoryFull) {
		t.Fatalf("history limit error=%v", err)
	}
	var count int
	if err := f.base.db.QueryRow(`SELECT COUNT(*) FROM outbound_proxy_test_operations`).Scan(&count); err != nil || count != outboundProxyTestMaxHistory {
		t.Fatalf("history count=%d err=%v", count, err)
	}
}

func TestOutboundProxyTestMigrationRejectsQuotedCheckAndRepairs(t *testing.T) {
	f := newOutboundProxyHandshakeFixture(t)
	f.coordinator.Close()
	for _, name := range []string{"outbound_proxy_test_operations_proxy_active_idx", "outbound_proxy_test_operations_created_idx"} {
		if _, err := f.base.db.Exec(`DROP INDEX ` + name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.base.db.Exec(`ALTER TABLE outbound_proxy_test_operations RENAME TO outbound_proxy_test_bad`); err != nil {
		t.Fatal(err)
	}
	badDDL := strings.Replace(outboundProxyTestDDL, "'handshake_ok'", "'hand shake_ok'", 1)
	if _, err := f.base.db.Exec(badDDL); err != nil {
		t.Fatal(err)
	}
	broken, err := newOutboundProxyTestCoordinator(f.app)
	if err == nil {
		broken.Close()
		t.Fatal("schema with changed quoted result literal was accepted")
	}
	var leakedIndexes int
	if err := f.base.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name LIKE 'outbound_proxy_test_operations_%_idx'`).Scan(&leakedIndexes); err != nil || leakedIndexes != 0 {
		t.Fatalf("failed migration leaked indexes=%d err=%v", leakedIndexes, err)
	}
	if _, err := f.base.db.Exec(`DROP TABLE outbound_proxy_test_operations`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.db.Exec(`DROP TABLE outbound_proxy_test_bad`); err != nil {
		t.Fatal(err)
	}
	repaired, err := newOutboundProxyTestCoordinator(f.app)
	if err != nil {
		t.Fatalf("repair retry: %v", err)
	}
	repaired.Close()
	if _, err := f.base.db.Exec(`CREATE UNIQUE INDEX outbound_proxy_test_unexpected_idx ON outbound_proxy_test_operations(proxy_id)`); err != nil {
		t.Fatal(err)
	}
	if incompatible, err := newOutboundProxyTestCoordinator(f.app); err == nil {
		incompatible.Close()
		t.Fatal("unexpected unique index was accepted")
	}
	if _, err := f.base.db.Exec(`DROP INDEX outbound_proxy_test_unexpected_idx`); err != nil {
		t.Fatal(err)
	}
	if repairedAgain, err := newOutboundProxyTestCoordinator(f.app); err != nil {
		t.Fatalf("index repair retry: %v", err)
	} else {
		repairedAgain.Close()
	}
}

func TestOutboundProxyTestResultClassificationIsConservative(t *testing.T) {
	tests := []struct {
		name string
		err  error
		ctx  error
		want string
	}{
		{"success", nil, nil, outboundProxyTestHandshakeOK},
		{"deadline", context.DeadlineExceeded, context.DeadlineExceeded, outboundProxyTestTimeout},
		{"cancelled", context.Canceled, context.Canceled, outboundProxyTestCancelled},
		{"address", egress.ErrAddressNotPermitted, nil, outboundProxyTestAddressRejected},
		{"proxy rejection", egress.ErrProxyRejected, nil, outboundProxyTestProxyUnavailable},
		{"ambiguous TLS or proxy", egress.ErrProxyUnavailable, nil, outboundProxyTestInternalFailure},
		{"resolution ambiguity", egress.ErrResolutionFailed, nil, outboundProxyTestInternalFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := outboundProxyTestResult(test.err, test.ctx); got != test.want {
				t.Fatalf("result=%q want %q", got, test.want)
			}
		})
	}
}

func TestOutboundProxyTestRejectsCodexHTTPAndDoesNotReadUpstreamSecret(t *testing.T) {
	f := newOutboundProxyHandshakeFixture(t)
	if _, err := f.base.db.Exec(`UPDATE upstreams SET credential_ciphertext=X'00' WHERE id=?`, f.upstreamID); err != nil {
		t.Fatal(err)
	}
	f.coordinator.probeTLS = func(context.Context, *egress.Client, string, string) error { return nil }
	operation := "21000000-0000-4000-8000-000000000010"
	if _, _, err := f.coordinator.create(context.Background(), f.proxy.ID, f.input(operation)); err != nil {
		t.Fatalf("unread upstream ciphertext affected test: %v", err)
	}
	finished := waitOutboundProxyTest(t, f.coordinator, operation, outboundProxyTestCompleted)
	if finished.ResultCode == nil || *finished.ResultCode != outboundProxyTestHandshakeOK {
		t.Fatalf("finished=%+v", finished)
	}
	insertProxyTestUpstream(t, f.base.db, "ups_codex_test", codexMembershipProvider, "https://api.example/v1", 1)
	if _, err := f.base.store.SetBinding(context.Background(), upstreamProxyBindingInput{UpstreamID: "ups_codex_test", ExpectedUpstreamRevision: 1, ProxyID: f.proxy.ID, ExpectedProxyRevision: 1, Bind: true}); !errors.Is(err, errOutboundProxyInvalid) {
		t.Fatalf("store unexpectedly bound Codex: %v", err)
	}
	insertProxyTestUpstream(t, f.base.db, "ups_http_test", "openai-compatible", "http://api.example/v1", 1)
	if _, err := f.base.store.SetBinding(context.Background(), upstreamProxyBindingInput{UpstreamID: "ups_http_test", ExpectedUpstreamRevision: 1, ProxyID: f.proxy.ID, ExpectedProxyRevision: 1, Bind: true}); !errors.Is(err, errOutboundProxyInvalid) {
		t.Fatalf("store unexpectedly bound HTTP upstream: %v", err)
	}
}

func TestOutboundProxyTestAdminAPIContract(t *testing.T) {
	dataDir := runtimeTestDataDir(t)
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := newOutboundProxyTestCoordinator(app)
	if err != nil {
		app.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { coordinator.Close(); _ = app.Close() })
	credential := outboundProxyCredential{username: "api-proxy", password: "api-proxy-secret"}
	proxy, err := app.outboundProxies.Create(context.Background(), outboundProxyCreateInput{OperationID: "23000000-0000-4000-8000-000000000001", Name: "API proxy", Scheme: "https", Host: "proxy.example", Port: 443, AddressScope: "public", Enabled: true, Credential: &credential})
	if err != nil {
		t.Fatal(err)
	}
	insertProxyTestUpstream(t, app.store.db, "ups_api_handshake", "anthropic-api-key", "https://api.example/v1", 1)
	binding, err := app.outboundProxies.SetBinding(context.Background(), upstreamProxyBindingInput{UpstreamID: "ups_api_handshake", ExpectedUpstreamRevision: 1, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: true})
	if err != nil {
		t.Fatal(err)
	}
	coordinator.probeTLS = func(context.Context, *egress.Client, string, string) error { return nil }
	mux := http.NewServeMux()
	coordinator.Register(mux)
	cookie, csrf := installRuntimeAdminSession(t, app, time.Now().UTC().Add(time.Hour))
	input := outboundProxyTestInput{OperationID: "23000000-0000-4000-8000-000000000002", ExpectedProxyRevision: 1, ExpectedConnectionRevision: 1, UpstreamID: "ups_api_handshake", ExpectedUpstreamRevision: binding.UpstreamRevision}
	body, _ := json.Marshal(input)
	request := httptest.NewRequest(http.MethodPost, "http://admin.test/admin/api/v1/outbound-proxies/"+proxy.ID+"/tests", bytes.NewReader(body))
	request.Host = "admin.test"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://admin.test")
	request.Header.Set("X-CSRF-Token", csrf)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "http://admin.test/admin/api/v1/outbound-proxies/"+proxy.ID+"/tests", bytes.NewReader(body))
	request.Host = "admin.test"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://admin.test")
	request.Header.Set("X-CSRF-Token", csrf)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	bad := strings.Replace(string(body), `"upstream_id":`, `"upstream_id":"duplicate","upstream_id":`, 1)
	request = httptest.NewRequest(http.MethodPost, "http://admin.test/admin/api/v1/outbound-proxies/"+proxy.ID+"/tests", strings.NewReader(bad))
	request.Host = "admin.test"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://admin.test")
	request.Header.Set("X-CSRF-Token", csrf)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("duplicate JSON status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	extra := strings.TrimSuffix(string(body), "}") + `,"target_url":"https://attacker.invalid"}`
	request = httptest.NewRequest(http.MethodPost, "http://admin.test/admin/api/v1/outbound-proxies/"+proxy.ID+"/tests", strings.NewReader(extra))
	request.Host = "admin.test"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://admin.test")
	request.Header.Set("X-CSRF-Token", csrf)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("arbitrary target status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "http://admin.test/admin/api/v1/outbound-proxies/"+proxy.ID+"/tests", bytes.NewReader(body))
	request.Host = "admin.test"
	request.AddCookie(cookie)
	request.Header.Set("Origin", "http://evil.test")
	request.Header.Set("X-CSRF-Token", csrf)
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("bad origin status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	get := httptest.NewRequest(http.MethodGet, "http://admin.test/admin/api/v1/outbound-proxies/"+proxy.ID+"/tests/"+input.OperationID, nil)
	get.Host = "admin.test"
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, get)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
