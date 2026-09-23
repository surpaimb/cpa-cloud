package service

// Independently authored from outbound-proxy-test-contract.md. The operation
// stores only bounded handshake metadata. It never reads an upstream secret or
// sends an HTTP request to the selected upstream.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"cpacloud.local/server/internal/egress"
)

const (
	outboundProxyTestPending    = "pending"
	outboundProxyTestInProgress = "in_progress"
	outboundProxyTestCompleted  = "completed"

	outboundProxyTestHandshakeOK          = "handshake_ok"
	outboundProxyTestConfigurationChanged = "configuration_changed"
	outboundProxyTestProxyUnavailable     = "proxy_unavailable"
	outboundProxyTestTargetUnavailable    = "target_unavailable"
	outboundProxyTestAddressRejected      = "address_rejected"
	outboundProxyTestTimeout              = "timeout"
	outboundProxyTestCancelled            = "cancelled"
	outboundProxyTestInterrupted          = "interrupted"
	outboundProxyTestInternalFailure      = "internal_failure"

	outboundProxyTestMaxActive  = 4
	outboundProxyTestMaxHistory = 10000
	outboundProxyTestDeadline   = 10 * time.Second
)

var (
	errOutboundProxyTestInvalid           = errors.New("invalid outbound proxy test")
	errOutboundProxyTestNotFound          = errors.New("outbound proxy test was not found")
	errOutboundProxyTestOperationConflict = errors.New("outbound proxy test operation conflict")
	errOutboundProxyTestInProgress        = errors.New("outbound proxy already has an active test")
	errOutboundProxyTestCapacity          = errors.New("outbound proxy test capacity reached")
	errOutboundProxyTestHistoryFull       = errors.New("outbound proxy test history is full")
	errOutboundProxyTestUnavailable       = errors.New("outbound proxy test storage unavailable")
)

const outboundProxyTestDDL = `CREATE TABLE outbound_proxy_test_operations (
	operation_id TEXT PRIMARY KEY,
	proxy_id TEXT NOT NULL REFERENCES outbound_proxies(id) ON DELETE RESTRICT,
	proxy_revision INTEGER NOT NULL CHECK(proxy_revision BETWEEN 1 AND 9007199254740991),
	connection_revision INTEGER NOT NULL CHECK(connection_revision BETWEEN 1 AND 9007199254740991),
	upstream_id TEXT NOT NULL REFERENCES upstreams(id) ON DELETE RESTRICT,
	upstream_revision INTEGER NOT NULL CHECK(upstream_revision BETWEEN 1 AND 9007199254740991),
	state TEXT NOT NULL CHECK(state IN ('pending','in_progress','completed')),
	result_code TEXT CHECK(result_code IS NULL OR result_code IN ('handshake_ok','configuration_changed','proxy_unavailable','target_unavailable','address_rejected','timeout','cancelled','interrupted','internal_failure')),
	created_at TEXT NOT NULL,
	started_at TEXT,
	finished_at TEXT,
	latency_ms INTEGER CHECK(latency_ms IS NULL OR latency_ms BETWEEN 0 AND 10000),
	CHECK(
		(state = 'pending' AND started_at IS NULL AND finished_at IS NULL AND result_code IS NULL AND latency_ms IS NULL) OR
		(state = 'in_progress' AND started_at IS NOT NULL AND finished_at IS NULL AND result_code IS NULL AND latency_ms IS NULL) OR
		(state = 'completed' AND started_at IS NOT NULL AND finished_at IS NOT NULL AND result_code IS NOT NULL AND latency_ms IS NOT NULL)
	)
)`

const outboundProxyTestActiveIndexDDL = `CREATE INDEX outbound_proxy_test_operations_proxy_active_idx ON outbound_proxy_test_operations(proxy_id,state)`
const outboundProxyTestCreatedIndexDDL = `CREATE INDEX outbound_proxy_test_operations_created_idx ON outbound_proxy_test_operations(created_at,operation_id)`

type outboundProxyTestInput struct {
	OperationID                string `json:"operation_id"`
	ExpectedProxyRevision      int64  `json:"expected_proxy_revision"`
	ExpectedConnectionRevision int64  `json:"expected_connection_revision"`
	UpstreamID                 string `json:"upstream_id"`
	ExpectedUpstreamRevision   int64  `json:"expected_upstream_revision"`
}

type outboundProxyTestOperationView struct {
	OperationID        string     `json:"operation_id"`
	ProxyID            string     `json:"proxy_id"`
	ProxyRevision      int64      `json:"proxy_revision"`
	ConnectionRevision int64      `json:"connection_revision"`
	UpstreamID         string     `json:"upstream_id"`
	UpstreamRevision   int64      `json:"upstream_revision"`
	State              string     `json:"state"`
	ResultCode         *string    `json:"result_code"`
	CreatedAt          time.Time  `json:"created_at"`
	StartedAt          *time.Time `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at"`
	LatencyMS          *int64     `json:"latency_ms"`
}

type outboundProxyTestTarget struct {
	operation outboundProxyTestOperationView
	client    *egress.Client
	host      string
	port      string
}

type outboundProxyTestCoordinator struct {
	app      *App
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	closed   bool
	running  map[string]struct{}
	wg       sync.WaitGroup
	now      func() time.Time
	probeTLS func(context.Context, *egress.Client, string, string) error
	// Tests use this seam to exercise commit uncertainty without changing the
	// network operation or exposing a production hook.
	beforeFinalize func(int) error
}

func newOutboundProxyTestCoordinator(a *App) (*outboundProxyTestCoordinator, error) {
	if a == nil || a.store == nil || a.store.db == nil || a.secrets == nil || a.outboundProxies == nil || a.proxyClients == nil {
		return nil, errOutboundProxyTestUnavailable
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &outboundProxyTestCoordinator{
		app: a, ctx: ctx, cancel: cancel, running: make(map[string]struct{}),
		now: func() time.Time { return time.Now().UTC() },
		probeTLS: func(ctx context.Context, client *egress.Client, host, port string) error {
			return client.ProbeTLS(ctx, host, port)
		},
	}
	if err := c.migrate(context.Background()); err != nil {
		cancel()
		return nil, err
	}
	return c, nil
}

func (c *outboundProxyTestCoordinator) Register(mux *http.ServeMux) {
	if c == nil || mux == nil || c.app == nil {
		return
	}
	mux.HandleFunc("POST /admin/api/v1/outbound-proxies/{id}/tests", c.app.requireAdmin(c.post, true))
	mux.HandleFunc("GET /admin/api/v1/outbound-proxies/{id}/tests/{operation_id}", c.app.requireAdmin(c.get, false))
}

func (c *outboundProxyTestCoordinator) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		c.cancel()
	}
	c.mu.Unlock()
	c.wg.Wait()
}

func (c *outboundProxyTestCoordinator) post(w http.ResponseWriter, r *http.Request, _ adminSession) {
	object, err := decodeUniqueJSONObject(w, r, 16<<10)
	if err != nil || !exactJSONKeys(object, "operation_id", "expected_proxy_revision", "expected_connection_revision", "upstream_id", "expected_upstream_revision") {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "The proxy test request is invalid.")
		return
	}
	operationID, operationOK := object["operation_id"].(string)
	upstreamID, upstreamOK := object["upstream_id"].(string)
	proxyRevision, proxyRevisionOK := outboundProxyTestJSONRevision(object["expected_proxy_revision"])
	connectionRevision, connectionRevisionOK := outboundProxyTestJSONRevision(object["expected_connection_revision"])
	upstreamRevision, upstreamRevisionOK := outboundProxyTestJSONRevision(object["expected_upstream_revision"])
	if !operationOK || !upstreamOK || !proxyRevisionOK || !connectionRevisionOK || !upstreamRevisionOK {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "The proxy test request is invalid.")
		return
	}
	input := outboundProxyTestInput{OperationID: operationID, ExpectedProxyRevision: proxyRevision, ExpectedConnectionRevision: connectionRevision, UpstreamID: upstreamID, ExpectedUpstreamRevision: upstreamRevision}
	view, replay, err := c.create(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeOutboundProxyTestError(w, err)
		return
	}
	if replay {
		writeJSON(w, http.StatusOK, view)
		return
	}
	writeJSON(w, http.StatusAccepted, view)
}

func outboundProxyTestJSONRevision(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok || strings.ContainsAny(number.String(), ".eE+") {
		return 0, false
	}
	parsed, err := number.Int64()
	return parsed, err == nil && parsed >= 1 && parsed <= outboundProxyMaxRevision
}

func (c *outboundProxyTestCoordinator) get(w http.ResponseWriter, r *http.Request, _ adminSession) {
	view, err := c.load(r.Context(), r.PathValue("operation_id"))
	if err == nil && view.ProxyID != r.PathValue("id") {
		err = errOutboundProxyTestNotFound
	}
	if err != nil {
		writeOutboundProxyTestError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func writeOutboundProxyTestError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errOutboundProxyTestInvalid):
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "The proxy test request is invalid.")
	case errors.Is(err, errOutboundProxyTestNotFound):
		writeAdminError(w, http.StatusNotFound, "not_found", "The requested proxy test was not found.")
	case errors.Is(err, errOutboundProxyTestOperationConflict):
		writeAdminError(w, http.StatusConflict, "operation_conflict", "The operation ID was already used with different input.")
	case errors.Is(err, errOutboundProxyTestInProgress):
		writeAdminError(w, http.StatusConflict, "test_in_progress", "This proxy already has an active handshake test.")
	case errors.Is(err, errOutboundProxyTestCapacity):
		writeAdminError(w, http.StatusTooManyRequests, "test_capacity_exceeded", "The handshake test capacity is currently full.")
	case errors.Is(err, errOutboundProxyTestHistoryFull):
		writeAdminError(w, http.StatusConflict, "test_history_full", "The handshake test history limit was reached.")
	case errors.Is(err, errOutboundProxyConflict), errors.Is(err, errUpstreamProxyBindingConflict), errors.Is(err, errOutboundProxyUnavailable):
		writeAdminError(w, http.StatusConflict, "configuration_changed", "The selected proxy or upstream configuration changed.")
	default:
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
	}
}

func validOutboundProxyTestInput(proxyID string, input outboundProxyTestInput) bool {
	return validIdentifier(proxyID, 128) && validUUIDOperation(input.OperationID) && validIdentifier(input.UpstreamID, 128) &&
		input.ExpectedProxyRevision >= 1 && input.ExpectedProxyRevision <= outboundProxyMaxRevision &&
		input.ExpectedConnectionRevision >= 1 && input.ExpectedConnectionRevision <= outboundProxyMaxRevision &&
		input.ExpectedUpstreamRevision >= 1 && input.ExpectedUpstreamRevision <= outboundProxyMaxRevision
}

func (c *outboundProxyTestCoordinator) create(ctx context.Context, proxyID string, input outboundProxyTestInput) (outboundProxyTestOperationView, bool, error) {
	if c == nil || !validOutboundProxyTestInput(proxyID, input) {
		return outboundProxyTestOperationView{}, false, errOutboundProxyTestInvalid
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return outboundProxyTestOperationView{}, false, errOutboundProxyTestUnavailable
	}
	c.app.admission.RLock()
	defer c.app.admission.RUnlock()
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return outboundProxyTestOperationView{}, false, errOutboundProxyTestUnavailable
	}
	defer tx.Rollback()
	stored, err := loadOutboundProxyTestTx(ctx, tx, input.OperationID)
	if err == nil {
		if !sameOutboundProxyTestInput(stored, proxyID, input) {
			return outboundProxyTestOperationView{}, false, errOutboundProxyTestOperationConflict
		}
		return stored, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return outboundProxyTestOperationView{}, false, errOutboundProxyTestUnavailable
	}
	var total, active, forProxy int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*),COALESCE(SUM(CASE WHEN state IN ('pending','in_progress') THEN 1 ELSE 0 END),0),COALESCE(SUM(CASE WHEN proxy_id=? AND state IN ('pending','in_progress') THEN 1 ELSE 0 END),0) FROM outbound_proxy_test_operations`, proxyID).Scan(&total, &active, &forProxy); err != nil {
		return outboundProxyTestOperationView{}, false, errOutboundProxyTestUnavailable
	}
	if total >= outboundProxyTestMaxHistory {
		return outboundProxyTestOperationView{}, false, errOutboundProxyTestHistoryFull
	}
	if active >= outboundProxyTestMaxActive {
		return outboundProxyTestOperationView{}, false, errOutboundProxyTestCapacity
	}
	if forProxy != 0 {
		return outboundProxyTestOperationView{}, false, errOutboundProxyTestInProgress
	}
	if err := validateOutboundProxyTestSelection(ctx, tx, proxyID, input); err != nil {
		return outboundProxyTestOperationView{}, false, err
	}
	created := c.now().UTC()
	createdValue := formatAccountPoolTime(created)
	if _, err := tx.ExecContext(ctx, `INSERT INTO outbound_proxy_test_operations(operation_id,proxy_id,proxy_revision,connection_revision,upstream_id,upstream_revision,state,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		input.OperationID, proxyID, input.ExpectedProxyRevision, input.ExpectedConnectionRevision, input.UpstreamID, input.ExpectedUpstreamRevision, outboundProxyTestPending, createdValue); err != nil {
		return outboundProxyTestOperationView{}, false, errOutboundProxyTestUnavailable
	}
	if err := tx.Commit(); err != nil {
		return outboundProxyTestOperationView{}, false, errOutboundProxyTestUnavailable
	}
	view := outboundProxyTestOperationView{OperationID: input.OperationID, ProxyID: proxyID, ProxyRevision: input.ExpectedProxyRevision, ConnectionRevision: input.ExpectedConnectionRevision, UpstreamID: input.UpstreamID, UpstreamRevision: input.ExpectedUpstreamRevision, State: outboundProxyTestPending, CreatedAt: created}
	c.running[input.OperationID] = struct{}{}
	c.wg.Add(1)
	go c.run(view)
	return view, false, nil
}

func sameOutboundProxyTestInput(stored outboundProxyTestOperationView, proxyID string, input outboundProxyTestInput) bool {
	return stored.OperationID == input.OperationID && stored.ProxyID == proxyID && stored.ProxyRevision == input.ExpectedProxyRevision &&
		stored.ConnectionRevision == input.ExpectedConnectionRevision && stored.UpstreamID == input.UpstreamID && stored.UpstreamRevision == input.ExpectedUpstreamRevision
}

func validateOutboundProxyTestSelection(ctx context.Context, tx *sql.Tx, proxyID string, input outboundProxyTestInput) error {
	var proxyEnabled, upstreamEnabled int
	var proxyRevision, connectionRevision, upstreamRevision, boundConnection int64
	var provider, endpoint, boundProxy string
	err := tx.QueryRowContext(ctx, `SELECT p.enabled,p.revision,p.connection_revision,u.enabled,u.revision,u.provider_kind,u.endpoint,b.proxy_id,b.proxy_connection_revision
		FROM outbound_proxies p JOIN upstreams u ON u.id=? JOIN upstream_proxy_bindings b ON b.upstream_id=u.id WHERE p.id=?`, input.UpstreamID, proxyID).
		Scan(&proxyEnabled, &proxyRevision, &connectionRevision, &upstreamEnabled, &upstreamRevision, &provider, &endpoint, &boundProxy, &boundConnection)
	if errors.Is(err, sql.ErrNoRows) {
		return errUpstreamProxyBindingConflict
	}
	if err != nil {
		return errOutboundProxyTestUnavailable
	}
	if proxyEnabled != 1 || upstreamEnabled != 1 || proxyRevision != input.ExpectedProxyRevision || connectionRevision != input.ExpectedConnectionRevision ||
		upstreamRevision != input.ExpectedUpstreamRevision || boundProxy != proxyID || boundConnection != connectionRevision || !validProxyBindingProvider(provider) || !validOutboundProxyTestEndpoint(endpoint) {
		return errUpstreamProxyBindingConflict
	}
	return nil
}

func validOutboundProxyTestEndpoint(endpoint string) bool {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Fragment != "" || parsed.RawFragment != "" {
		return false
	}
	port := parsed.Port()
	if port == "" {
		port = "443"
	}
	value, err := strconv.Atoi(port)
	return err == nil && value >= 1 && value <= 65535
}

func (c *outboundProxyTestCoordinator) run(operation outboundProxyTestOperationView) {
	defer c.wg.Done()
	defer func() {
		c.mu.Lock()
		delete(c.running, operation.OperationID)
		c.mu.Unlock()
	}()
	started := c.now().UTC()
	if started.Before(operation.CreatedAt) {
		started = operation.CreatedAt
	}
	claimed, err := c.claim(operation, started)
	if err != nil || !claimed {
		return
	}
	operation.State = outboundProxyTestInProgress
	operation.StartedAt = &started
	target, code := c.freeze(operation)
	networkStarted := time.Now()
	if code == "" {
		ctx, cancel := context.WithTimeout(c.ctx, outboundProxyTestDeadline)
		err = c.probeTLS(ctx, target.client, target.host, target.port)
		code = outboundProxyTestResult(err, ctx.Err())
		cancel()
	}
	elapsed := time.Since(networkStarted)
	if target.client == nil {
		elapsed = 0
	}
	latency := elapsed.Milliseconds()
	if latency < 0 {
		latency = 0
	}
	if latency > 10000 {
		latency = 10000
	}
	finished := c.now().UTC()
	if finished.Before(started) {
		finished = started
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if c.beforeFinalize != nil {
			if err := c.beforeFinalize(attempt); err != nil {
				if attempt < 3 {
					time.Sleep(time.Duration(attempt) * 10 * time.Millisecond)
				}
				continue
			}
		}
		if err := c.finalize(operation, code, finished, latency); err == nil {
			return
		}
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 10 * time.Millisecond)
		}
	}
}

func (c *outboundProxyTestCoordinator) claim(operation outboundProxyTestOperationView, started time.Time) (bool, error) {
	stamp := formatAccountPoolTime(started)
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result, err := c.app.store.db.ExecContext(ctx, `UPDATE outbound_proxy_test_operations SET state='in_progress',started_at=? WHERE operation_id=? AND state='pending'`, stamp, operation.OperationID)
		if err == nil {
			var count int64
			count, err = result.RowsAffected()
			if err == nil && count == 1 {
				cancel()
				return true, nil
			}
		}
		// An UPDATE whose commit result was uncertain must be resolved before
		// deciding whether a handshake may begin. Reading the exact frozen
		// claim cannot cause a second network attempt.
		stored, readErr := c.load(ctx, operation.OperationID)
		cancel()
		if readErr == nil && stored.State == outboundProxyTestInProgress && stored.StartedAt != nil && stored.StartedAt.Equal(started) {
			return true, nil
		}
		if readErr == nil && stored.State != outboundProxyTestPending {
			return false, nil
		}
		if err != nil {
			lastErr = err
		} else if readErr != nil {
			lastErr = readErr
		} else {
			lastErr = errOutboundProxyTestUnavailable
		}
		if attempt < 3 {
			time.Sleep(time.Duration(attempt) * 10 * time.Millisecond)
		}
	}
	return false, lastErr
}

func (c *outboundProxyTestCoordinator) freeze(operation outboundProxyTestOperationView) (outboundProxyTestTarget, string) {
	target := outboundProxyTestTarget{operation: operation}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c.app.admission.RLock()
	defer c.app.admission.RUnlock()
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return target, outboundProxyTestInternalFailure
	}
	defer tx.Rollback()
	if err := validateOutboundProxyTestSelection(ctx, tx, operation.ProxyID, outboundProxyTestInput{OperationID: operation.OperationID, ExpectedProxyRevision: operation.ProxyRevision, ExpectedConnectionRevision: operation.ConnectionRevision, UpstreamID: operation.UpstreamID, ExpectedUpstreamRevision: operation.UpstreamRevision}); err != nil {
		if errors.Is(err, errUpstreamProxyBindingConflict) {
			return target, outboundProxyTestConfigurationChanged
		}
		return target, outboundProxyTestInternalFailure
	}
	var host, scope, endpoint string
	var port int
	var credentialVersion sql.NullInt64
	var ciphertext []byte
	err = tx.QueryRowContext(ctx, `SELECT p.host,p.port,p.address_scope,p.credential_ciphertext,p.credential_key_version,u.endpoint
		FROM outbound_proxies p JOIN upstreams u ON u.id=? WHERE p.id=?`, operation.UpstreamID, operation.ProxyID).
		Scan(&host, &port, &scope, &ciphertext, &credentialVersion, &endpoint)
	if err != nil {
		return target, outboundProxyTestInternalFailure
	}
	var credentials egress.BasicCredentials
	if credentialVersion.Valid || len(ciphertext) != 0 {
		if !credentialVersion.Valid || credentialVersion.Int64 != outboundProxyCredentialVersion || len(ciphertext) == 0 {
			return target, outboundProxyTestInternalFailure
		}
		credential, decryptErr := c.app.secrets.decryptOutboundProxyCredential(operation.ProxyID, int(credentialVersion.Int64), ciphertext)
		if decryptErr != nil {
			return target, outboundProxyTestInternalFailure
		}
		credentials, err = egress.NewBasicCredentials(credential.username, credential.password)
		credential = outboundProxyCredential{}
	} else {
		credentials, err = egress.NewBasicCredentials("", "")
	}
	if err != nil {
		return target, outboundProxyTestInternalFailure
	}
	config := egress.ProxyConfig{ProxyID: operation.ProxyID, ConnectionRevision: operation.ConnectionRevision, Host: host, Port: uint16(port), Scope: egress.AddressScope(scope), Credentials: credentials, AllowLoopbackForTesting: c.app.cfg.AllowLoopbackUpstream}
	target.client, err = c.app.proxyClients.Client(config)
	if err != nil {
		return outboundProxyTestTarget{operation: operation}, outboundProxyTestInternalFailure
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || !validOutboundProxyTestEndpoint(endpoint) {
		return outboundProxyTestTarget{operation: operation}, outboundProxyTestConfigurationChanged
	}
	target.host, target.port = parsed.Hostname(), parsed.Port()
	if target.port == "" {
		target.port = "443"
	}
	return target, ""
}

func outboundProxyTestResult(probeErr, contextErr error) string {
	if probeErr == nil {
		return outboundProxyTestHandshakeOK
	}
	if errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(probeErr, context.DeadlineExceeded) {
		return outboundProxyTestTimeout
	}
	if errors.Is(contextErr, context.Canceled) || errors.Is(probeErr, context.Canceled) {
		return outboundProxyTestCancelled
	}
	if errors.Is(probeErr, egress.ErrAddressNotPermitted) || errors.Is(probeErr, egress.ErrTooManyAddresses) {
		return outboundProxyTestAddressRejected
	}
	if errors.Is(probeErr, egress.ErrProxyRejected) || errors.Is(probeErr, egress.ErrConnectResponse) || errors.Is(probeErr, egress.ErrConnectHeaderLarge) {
		return outboundProxyTestProxyUnavailable
	}
	// ErrProxyUnavailable also covers the target TLS handshake, so it cannot
	// safely distinguish the failed side.
	return outboundProxyTestInternalFailure
}

func (c *outboundProxyTestCoordinator) finalize(operation outboundProxyTestOperationView, proposedCode string, finished time.Time, latency int64) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c.app.admission.RLock()
	defer c.app.admission.RUnlock()
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errOutboundProxyTestUnavailable
	}
	defer tx.Rollback()
	stored, err := loadOutboundProxyTestTx(ctx, tx, operation.OperationID)
	if err != nil {
		return errOutboundProxyTestUnavailable
	}
	if stored.State == outboundProxyTestCompleted {
		if stored.ResultCode != nil && *stored.ResultCode == proposedCode && stored.FinishedAt != nil && stored.FinishedAt.Equal(finished) && stored.LatencyMS != nil && *stored.LatencyMS == latency {
			return nil
		}
		return errOutboundProxyTestUnavailable
	}
	if !sameOutboundProxyTestInput(stored, operation.ProxyID, outboundProxyTestInput{OperationID: operation.OperationID, ExpectedProxyRevision: operation.ProxyRevision, ExpectedConnectionRevision: operation.ConnectionRevision, UpstreamID: operation.UpstreamID, ExpectedUpstreamRevision: operation.UpstreamRevision}) {
		return errOutboundProxyTestUnavailable
	}
	if err := validateOutboundProxyTestSelection(ctx, tx, operation.ProxyID, outboundProxyTestInput{OperationID: operation.OperationID, ExpectedProxyRevision: operation.ProxyRevision, ExpectedConnectionRevision: operation.ConnectionRevision, UpstreamID: operation.UpstreamID, ExpectedUpstreamRevision: operation.UpstreamRevision}); err != nil {
		if errors.Is(err, errUpstreamProxyBindingConflict) {
			proposedCode = outboundProxyTestConfigurationChanged
		} else {
			return errOutboundProxyTestUnavailable
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE outbound_proxy_test_operations SET state='completed',result_code=?,finished_at=?,latency_ms=? WHERE operation_id=? AND state='in_progress'`, proposedCode, formatAccountPoolTime(finished), latency, operation.OperationID)
	if err != nil {
		return errOutboundProxyTestUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return errOutboundProxyTestUnavailable
	}
	if err := tx.Commit(); err != nil {
		return errOutboundProxyTestUnavailable
	}
	return nil
}

func (c *outboundProxyTestCoordinator) load(ctx context.Context, operationID string) (outboundProxyTestOperationView, error) {
	if c == nil || !validUUIDOperation(operationID) {
		return outboundProxyTestOperationView{}, errOutboundProxyTestInvalid
	}
	view, err := scanOutboundProxyTest(c.app.store.db.QueryRowContext(ctx, outboundProxyTestSelect+` WHERE operation_id=?`, operationID))
	if errors.Is(err, sql.ErrNoRows) {
		return outboundProxyTestOperationView{}, errOutboundProxyTestNotFound
	}
	if err != nil {
		return outboundProxyTestOperationView{}, errOutboundProxyTestUnavailable
	}
	return view, nil
}

const outboundProxyTestSelect = `SELECT operation_id,proxy_id,proxy_revision,connection_revision,upstream_id,upstream_revision,state,result_code,created_at,started_at,finished_at,latency_ms FROM outbound_proxy_test_operations`

type outboundProxyTestScanner interface{ Scan(...any) error }

func scanOutboundProxyTest(scanner outboundProxyTestScanner) (outboundProxyTestOperationView, error) {
	var view outboundProxyTestOperationView
	var result, started, finished sql.NullString
	var latency sql.NullInt64
	var created string
	err := scanner.Scan(&view.OperationID, &view.ProxyID, &view.ProxyRevision, &view.ConnectionRevision, &view.UpstreamID, &view.UpstreamRevision, &view.State, &result, &created, &started, &finished, &latency)
	if err != nil {
		return view, err
	}
	view.CreatedAt, err = parseCanonicalProxyTime(created)
	if err != nil {
		return outboundProxyTestOperationView{}, err
	}
	if result.Valid {
		value := result.String
		view.ResultCode = &value
	}
	if started.Valid {
		value, parseErr := parseCanonicalProxyTime(started.String)
		if parseErr != nil {
			return outboundProxyTestOperationView{}, parseErr
		}
		view.StartedAt = &value
	}
	if finished.Valid {
		value, parseErr := parseCanonicalProxyTime(finished.String)
		if parseErr != nil {
			return outboundProxyTestOperationView{}, parseErr
		}
		view.FinishedAt = &value
	}
	if latency.Valid {
		value := latency.Int64
		view.LatencyMS = &value
	}
	return view, nil
}

func loadOutboundProxyTestTx(ctx context.Context, tx *sql.Tx, operationID string) (outboundProxyTestOperationView, error) {
	return scanOutboundProxyTest(tx.QueryRowContext(ctx, outboundProxyTestSelect+` WHERE operation_id=?`, operationID))
}

func (c *outboundProxyTestCoordinator) migrate(ctx context.Context) error {
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return errOutboundProxyTestUnavailable
	}
	defer tx.Rollback()
	for _, statement := range []struct{ sql, prefix string }{
		{outboundProxyTestDDL, "CREATE TABLE "},
		{outboundProxyTestActiveIndexDDL, "CREATE INDEX "},
		{outboundProxyTestCreatedIndexDDL, "CREATE INDEX "},
	} {
		if _, err := tx.ExecContext(ctx, strings.Replace(statement.sql, statement.prefix, statement.prefix+"IF NOT EXISTS ", 1)); err != nil {
			return errOutboundProxyTestUnavailable
		}
	}
	if err := validateOutboundProxyTestSchema(ctx, tx); err != nil {
		return err
	}
	now := c.now().UTC()
	rows, err := tx.QueryContext(ctx, outboundProxyTestSelect+` WHERE state IN ('pending','in_progress') ORDER BY operation_id`)
	if err != nil {
		return errOutboundProxyTestUnavailable
	}
	var incomplete []outboundProxyTestOperationView
	for rows.Next() {
		item, scanErr := scanOutboundProxyTest(rows)
		if scanErr != nil {
			rows.Close()
			return errOutboundProxyTestUnavailable
		}
		incomplete = append(incomplete, item)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return errOutboundProxyTestUnavailable
	}
	for _, item := range incomplete {
		started := item.CreatedAt
		if item.StartedAt != nil && item.StartedAt.After(started) {
			started = *item.StartedAt
		}
		finished := now
		if finished.Before(started) {
			finished = started
		}
		if _, err := tx.ExecContext(ctx, `UPDATE outbound_proxy_test_operations SET state='completed',result_code='interrupted',started_at=?,finished_at=?,latency_ms=0 WHERE operation_id=? AND state IN ('pending','in_progress')`, formatAccountPoolTime(started), formatAccountPoolTime(finished), item.OperationID); err != nil {
			return errOutboundProxyTestUnavailable
		}
	}
	if err := validateStoredOutboundProxyTests(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errOutboundProxyTestUnavailable
	}
	return nil
}

func validateOutboundProxyTestSchema(ctx context.Context, tx *sql.Tx) error {
	if err := validateProxyTable(ctx, tx, "outbound_proxy_test_operations", outboundProxyTestDDL, map[string]string{
		"outbound_proxy_test_operations_proxy_active_idx": outboundProxyTestActiveIndexDDL,
		"outbound_proxy_test_operations_created_idx":      outboundProxyTestCreatedIndexDDL,
	}, 1); err != nil {
		return errOutboundProxyTestUnavailable
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(outbound_proxy_test_operations)`)
	if err != nil {
		return errOutboundProxyTestUnavailable
	}
	wanted := map[string]string{"proxy_id": "outbound_proxies", "upstream_id": "upstreams"}
	seen := make(map[string]bool)
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil || wanted[from] != table || to != "id" || onUpdate != "NO ACTION" || onDelete != "RESTRICT" || seen[from] {
			rows.Close()
			return errOutboundProxyTestUnavailable
		}
		seen[from] = true
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil || len(seen) != 2 {
		return errOutboundProxyTestUnavailable
	}
	violations, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(outbound_proxy_test_operations)`)
	if err != nil {
		return errOutboundProxyTestUnavailable
	}
	bad := violations.Next()
	iterationErr, closeErr = violations.Err(), violations.Close()
	if bad || iterationErr != nil || closeErr != nil {
		return errOutboundProxyTestUnavailable
	}
	return validateStoredOutboundProxyTests(ctx, tx)
}

func validateStoredOutboundProxyTests(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, outboundProxyTestSelect+` ORDER BY operation_id`)
	if err != nil {
		return errOutboundProxyTestUnavailable
	}
	count := 0
	active := 0
	activeByProxy := make(map[string]int)
	for rows.Next() {
		item, scanErr := scanOutboundProxyTest(rows)
		if scanErr != nil || !validUUIDOperation(item.OperationID) || !validIdentifier(item.ProxyID, 128) || !validIdentifier(item.UpstreamID, 128) ||
			item.ProxyRevision < 1 || item.ProxyRevision > outboundProxyMaxRevision || item.ConnectionRevision < 1 || item.ConnectionRevision > outboundProxyMaxRevision || item.UpstreamRevision < 1 || item.UpstreamRevision > outboundProxyMaxRevision ||
			!validOutboundProxyTestStoredState(item) {
			rows.Close()
			return errOutboundProxyTestUnavailable
		}
		count++
		if item.State == outboundProxyTestPending || item.State == outboundProxyTestInProgress {
			active++
			activeByProxy[item.ProxyID]++
			if active > outboundProxyTestMaxActive || activeByProxy[item.ProxyID] > 1 {
				rows.Close()
				return errOutboundProxyTestUnavailable
			}
		}
		if count > outboundProxyTestMaxHistory {
			rows.Close()
			return errOutboundProxyTestUnavailable
		}
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return errOutboundProxyTestUnavailable
	}
	return nil
}

func validOutboundProxyTestStoredState(item outboundProxyTestOperationView) bool {
	if item.CreatedAt.IsZero() {
		return false
	}
	switch item.State {
	case outboundProxyTestPending:
		return item.ResultCode == nil && item.StartedAt == nil && item.FinishedAt == nil && item.LatencyMS == nil
	case outboundProxyTestInProgress:
		return item.ResultCode == nil && item.StartedAt != nil && !item.StartedAt.Before(item.CreatedAt) && item.FinishedAt == nil && item.LatencyMS == nil
	case outboundProxyTestCompleted:
		return item.ResultCode != nil && validOutboundProxyTestResult(*item.ResultCode) && item.StartedAt != nil && !item.StartedAt.Before(item.CreatedAt) && item.FinishedAt != nil && !item.FinishedAt.Before(*item.StartedAt) && item.LatencyMS != nil && *item.LatencyMS >= 0 && *item.LatencyMS <= 10000
	default:
		return false
	}
}

func validOutboundProxyTestResult(code string) bool {
	switch code {
	case outboundProxyTestHandshakeOK, outboundProxyTestConfigurationChanged, outboundProxyTestProxyUnavailable, outboundProxyTestTargetUnavailable,
		outboundProxyTestAddressRejected, outboundProxyTestTimeout, outboundProxyTestCancelled, outboundProxyTestInterrupted, outboundProxyTestInternalFailure:
		return true
	default:
		return false
	}
}
