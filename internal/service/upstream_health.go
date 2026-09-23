package service

// Independently implemented from docs/upstream-health-contract.md. Test runs
// persist only fixed metadata; credentials, response bodies, and raw errors are
// deliberately excluded from the schema and API.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	upstreamHealthTable      = "upstream_test_operations"
	upstreamHealthMaxHistory = 10000
	upstreamHealthMaxRunning = 4
	upstreamHealthTimeout    = 10 * time.Second
)

const upstreamHealthDDL = `CREATE TABLE IF NOT EXISTS upstream_test_operations (
	operation_id TEXT NOT NULL PRIMARY KEY,
	upstream_id TEXT NOT NULL REFERENCES upstreams(id) ON DELETE RESTRICT,
	provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key','codex-membership')),
	source_snapshot TEXT NOT NULL,
	requested_revision INTEGER NOT NULL CHECK(requested_revision >= 1),
	tested_revision INTEGER CHECK(tested_revision IS NULL OR tested_revision >= 1),
	scope TEXT NOT NULL CHECK(scope IN ('local_credential','catalog')),
	state TEXT NOT NULL CHECK(state IN ('pending','in_progress','completed')),
	result_code TEXT CHECK(result_code IS NULL OR result_code IN ('local_credential_ok','catalog_ok','authentication_failed','rate_limited','unsupported','timeout','invalid_response','configuration_changed','stale','cancelled','interrupted','internal_failure')),
	created_at TEXT NOT NULL,
	started_at TEXT,
	finished_at TEXT,
	latency_ms INTEGER CHECK(latency_ms IS NULL OR latency_ms >= 0),
	CHECK((state='pending' AND started_at IS NULL AND finished_at IS NULL AND result_code IS NULL AND tested_revision IS NULL AND latency_ms IS NULL)
	 OR (state='in_progress' AND started_at IS NOT NULL AND finished_at IS NULL AND result_code IS NULL AND latency_ms IS NULL)
	 OR (state='completed' AND finished_at IS NOT NULL AND result_code IS NOT NULL))
)`

var upstreamHealthResultCodes = map[string]bool{
	"local_credential_ok": true, "catalog_ok": true, "authentication_failed": true,
	"rate_limited": true, "unsupported": true, "timeout": true, "invalid_response": true,
	"configuration_changed": true, "stale": true, "cancelled": true, "interrupted": true,
	"internal_failure": true,
}

type upstreamTestOperationView struct {
	OperationID       string  `json:"operation_id"`
	UpstreamID        string  `json:"upstream_id"`
	ProviderKind      string  `json:"provider_kind"`
	RequestedRevision int64   `json:"requested_revision"`
	TestedRevision    *int64  `json:"tested_revision"`
	Scope             string  `json:"scope"`
	State             string  `json:"state"`
	ResultCode        *string `json:"result_code"`
	CreatedAt         string  `json:"created_at"`
	StartedAt         *string `json:"started_at"`
	FinishedAt        *string `json:"finished_at"`
	LatencyMS         *int64  `json:"latency_ms"`
}

type upstreamObservationView struct {
	OperationID     string `json:"operation_id"`
	Scope           string `json:"scope"`
	ResultCode      string `json:"result_code"`
	AccountRevision int64  `json:"account_revision"`
	CheckedAt       string `json:"checked_at"`
	LatencyMS       *int64 `json:"latency_ms"`
}

type upstreamHealthSnapshot struct {
	id              string
	provider        string
	endpoint        string
	revision        int64
	keyVersion      int
	ciphertext      []byte
	credentialState sql.NullString
	source          string
}

type upstreamHealthCoordinator struct {
	app    *App
	ctx    context.Context
	cancel context.CancelFunc

	mu      sync.Mutex
	active  map[string]string
	running int
	closed  bool
	wg      sync.WaitGroup

	// beforeClaim is a deterministic test seam for the only cancellation
	// boundary between durable admission and claiming the operation.
	beforeClaim func()
	// beforeFinalize lets tests inject a durable-write failure after protocol
	// execution without changing the production storage path.
	beforeFinalize func()
}

func newUpstreamHealthCoordinator(a *App) (*upstreamHealthCoordinator, error) {
	if a == nil || a.store == nil || a.store.db == nil {
		return nil, errors.New("upstream health storage is unavailable")
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &upstreamHealthCoordinator{app: a, ctx: ctx, cancel: cancel, active: make(map[string]string)}
	migrateCtx, migrateCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer migrateCancel()
	if err := c.migrate(migrateCtx); err != nil {
		cancel()
		return nil, err
	}
	return c, nil
}

func (c *upstreamHealthCoordinator) Close() {
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

func (c *upstreamHealthCoordinator) migrate(ctx context.Context) error {
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, upstreamHealthDDL); err != nil {
		return err
	}
	if err := verifyUpstreamHealthSchema(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(`+upstreamHealthTable+`)`)
	if err != nil {
		return err
	}
	violated := rows.Next()
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if violated {
		return errors.New("upstream health migration foreign key check failed")
	}
	now := utcNow()
	if _, err := tx.ExecContext(ctx, `UPDATE upstream_test_operations
		SET state='completed',result_code='interrupted',finished_at=?,latency_ms=NULL
		WHERE state IN ('pending','in_progress')`, now); err != nil {
		return err
	}
	return tx.Commit()
}

type upstreamHealthColumnSpec struct {
	kind    string
	notNull int
	primary int
}

func verifyUpstreamHealthSchema(ctx context.Context, tx *sql.Tx) error {
	expected := map[string]upstreamHealthColumnSpec{
		"operation_id": {"TEXT", 1, 1}, "upstream_id": {"TEXT", 1, 0}, "provider_kind": {"TEXT", 1, 0},
		"source_snapshot": {"TEXT", 1, 0}, "requested_revision": {"INTEGER", 1, 0}, "tested_revision": {"INTEGER", 0, 0},
		"scope": {"TEXT", 1, 0}, "state": {"TEXT", 1, 0}, "result_code": {"TEXT", 0, 0}, "created_at": {"TEXT", 1, 0},
		"started_at": {"TEXT", 0, 0}, "finished_at": {"TEXT", 0, 0}, "latency_ms": {"INTEGER", 0, 0},
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+upstreamHealthTable+`)`)
	if err != nil {
		return err
	}
	seen := make(map[string]bool, len(expected))
	for rows.Next() {
		var cid, notNull, primary int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primary); err != nil {
			rows.Close()
			return err
		}
		spec, ok := expected[name]
		if !ok || strings.ToUpper(kind) != spec.kind || notNull != spec.notNull || primary != spec.primary || defaultValue != nil {
			rows.Close()
			return errors.New("existing upstream health table has an incompatible schema")
		}
		seen[name] = true
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(seen) != len(expected) {
		return errors.New("existing upstream health table has an incompatible schema")
	}
	var objectType, schema string
	if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, upstreamHealthTable).Scan(&objectType, &schema); err != nil {
		return err
	}
	if objectType != "table" {
		return errors.New("existing upstream health object is not a table")
	}
	normalized := normalizeHealthDDL(schema)
	expectedDDL := normalizeHealthDDL(strings.Replace(upstreamHealthDDL, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1))
	expectedWithClause := normalizeHealthDDL(upstreamHealthDDL)
	if normalized != expectedDDL && normalized != expectedWithClause {
		return errors.New("existing upstream health table has an incompatible schema")
	}
	fkRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(`+upstreamHealthTable+`)`)
	if err != nil {
		return err
	}
	foreignKeys := 0
	for fkRows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := fkRows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			fkRows.Close()
			return err
		}
		if table != "upstreams" || from != "upstream_id" || to != "id" || onDelete != "RESTRICT" || onUpdate != "NO ACTION" {
			fkRows.Close()
			return errors.New("existing upstream health table has an incompatible foreign key")
		}
		foreignKeys++
	}
	iterationErr = fkRows.Err()
	closeErr = fkRows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if foreignKeys != 1 {
		return errors.New("existing upstream health table has an incompatible foreign key")
	}
	indexRows, err := tx.QueryContext(ctx, `PRAGMA index_list(`+upstreamHealthTable+`)`)
	if err != nil {
		return err
	}
	primaryUnique := 0
	for indexRows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := indexRows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			indexRows.Close()
			return err
		}
		if unique != 0 {
			if origin != "pk" || partial != 0 {
				indexRows.Close()
				return errors.New("existing upstream health table has an incompatible unique constraint")
			}
			primaryUnique++
		}
	}
	iterationErr = indexRows.Err()
	closeErr = indexRows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if primaryUnique != 1 {
		return errors.New("existing upstream health table is missing its primary key")
	}
	var invalid int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM upstream_test_operations WHERE
		typeof(requested_revision)<>'integer' OR requested_revision<1 OR
		(tested_revision IS NOT NULL AND (typeof(tested_revision)<>'integer' OR tested_revision<1)) OR
		typeof(latency_ms) NOT IN ('null','integer') OR (latency_ms IS NOT NULL AND latency_ms<0) OR
		provider_kind NOT IN ('openai-compatible','anthropic-api-key','gemini-api-key','codex-membership') OR
		scope NOT IN ('local_credential','catalog') OR state NOT IN ('pending','in_progress','completed') OR
		(result_code IS NOT NULL AND result_code NOT IN ('local_credential_ok','catalog_ok','authentication_failed','rate_limited','unsupported','timeout','invalid_response','configuration_changed','stale','cancelled','interrupted','internal_failure')) OR
		NOT ((state='pending' AND started_at IS NULL AND finished_at IS NULL AND result_code IS NULL AND tested_revision IS NULL AND latency_ms IS NULL)
		 OR (state='in_progress' AND started_at IS NOT NULL AND finished_at IS NULL AND result_code IS NULL AND latency_ms IS NULL)
		 OR (state='completed' AND finished_at IS NOT NULL AND result_code IS NOT NULL))`).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return errors.New("existing upstream health table contains invalid rows")
	}
	return nil
}

func normalizeHealthDDL(value string) string {
	// SQL keywords are case-insensitive; quoted CHECK values are not. Splitting
	// at every quote also preserves doubled quotes inside a string literal.
	parts := strings.Split(value, "'")
	for index := 0; index < len(parts); index += 2 {
		parts[index] = strings.ToLower(strings.Join(strings.Fields(parts[index]), " "))
	}
	return strings.Join(parts, "'")
}

func (a *App) runUpstreamTest(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if a.healthTests == nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "operation_id", "expected_revision", "scope") {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid upstream test request.")
		return
	}
	operationID, operationOK := object["operation_id"].(string)
	scope, scopeOK := object["scope"].(string)
	revision, revisionOK := strictPositiveJSONInt64(object["expected_revision"])
	if !operationOK || !validUUIDOperation(operationID) || !scopeOK || (scope != "local_credential" && scope != "catalog") || !revisionOK {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid upstream test request.")
		return
	}
	view, status, code, err := a.healthTests.run(r.Context(), r.PathValue("id"), operationID, revision, scope)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if code != "" {
		writeUpstreamTestError(w, status, code)
		return
	}
	writeJSON(w, status, view)
}

func (a *App) getUpstreamTest(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if a.healthTests == nil || !validUUIDOperation(r.PathValue("operation_id")) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid upstream test request.")
		return
	}
	view, err := a.healthTests.get(r.Context(), r.PathValue("operation_id"))
	if errors.Is(err, sql.ErrNoRows) || (err == nil && view.UpstreamID != r.PathValue("id")) {
		writeUpstreamTestError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	writeJSON(w, map[bool]int{true: http.StatusAccepted, false: http.StatusOK}[view.State != "completed"], view)
}

func strictPositiveJSONInt64(value any) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok || strings.ContainsAny(number.String(), ".eE+") {
		return 0, false
	}
	parsed, err := number.Int64()
	return parsed, err == nil && parsed >= 1
}

func writeUpstreamTestError(w http.ResponseWriter, status int, code string) {
	messages := map[string]string{
		"not_found": "Upstream test was not found.", "revision_conflict": "The upstream was changed by another request.",
		"operation_conflict": "The operation was already used with different input.", "test_in_progress": "An upstream test is already running.",
		"test_capacity_exceeded": "Too many upstream tests are running.", "test_history_full": "Upstream test history is full.",
	}
	message := messages[code]
	if message == "" {
		message = "Unable to run upstream test."
	}
	writeAdminError(w, status, code, message)
}

func (c *upstreamHealthCoordinator) run(requestCtx context.Context, upstreamID, operationID string, requestedRevision int64, scope string) (upstreamTestOperationView, int, string, error) {
	c.mu.Lock()
	view, err := c.loadRecoveringLocked(requestCtx, operationID)
	if err == nil {
		c.mu.Unlock()
		if view.UpstreamID != upstreamID || view.RequestedRevision != requestedRevision || view.Scope != scope {
			return upstreamTestOperationView{}, http.StatusConflict, "operation_conflict", nil
		}
		status := http.StatusOK
		if view.State != "completed" {
			status = http.StatusAccepted
		}
		return view, status, "", nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		c.mu.Unlock()
		return upstreamTestOperationView{}, 0, "", err
	}
	if c.closed {
		c.mu.Unlock()
		return upstreamTestOperationView{}, 0, "", errors.New("coordinator closed")
	}
	if err := c.recoverAccountOrphansLocked(upstreamID); err != nil {
		c.mu.Unlock()
		return upstreamTestOperationView{}, 0, "", err
	}
	snapshot, err := c.loadSnapshot(requestCtx, upstreamID)
	if errors.Is(err, sql.ErrNoRows) {
		c.mu.Unlock()
		return upstreamTestOperationView{}, http.StatusNotFound, "not_found", nil
	}
	if err != nil {
		c.mu.Unlock()
		return upstreamTestOperationView{}, 0, "", err
	}
	if snapshot.revision != requestedRevision {
		c.mu.Unlock()
		return upstreamTestOperationView{}, http.StatusConflict, "revision_conflict", nil
	}
	if _, active := c.active[upstreamID]; active {
		c.mu.Unlock()
		return upstreamTestOperationView{}, http.StatusConflict, "test_in_progress", nil
	}
	if c.running >= upstreamHealthMaxRunning {
		c.mu.Unlock()
		return upstreamTestOperationView{}, http.StatusTooManyRequests, "test_capacity_exceeded", nil
	}
	var count int
	if err := c.app.store.db.QueryRowContext(requestCtx, `SELECT COUNT(*) FROM upstream_test_operations`).Scan(&count); err != nil {
		c.mu.Unlock()
		return upstreamTestOperationView{}, 0, "", err
	}
	if count >= upstreamHealthMaxHistory {
		c.mu.Unlock()
		return upstreamTestOperationView{}, http.StatusConflict, "test_history_full", nil
	}
	created := utcNow()
	if _, err := c.app.store.db.ExecContext(requestCtx, `INSERT INTO upstream_test_operations
		(operation_id,upstream_id,provider_kind,source_snapshot,requested_revision,tested_revision,scope,state,result_code,created_at,started_at,finished_at,latency_ms)
		VALUES(?,?,?,?,?,NULL,?,'pending',NULL,?,NULL,NULL,NULL)`, operationID, upstreamID, snapshot.provider, snapshot.source, requestedRevision, scope, created); err != nil {
		c.mu.Unlock()
		return upstreamTestOperationView{}, 0, "", err
	}
	c.active[upstreamID] = operationID
	c.running++
	c.wg.Add(1)
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.active, upstreamID)
		c.running--
		c.mu.Unlock()
		c.wg.Done()
	}()
	if c.beforeClaim != nil {
		c.beforeClaim()
	}
	started := utcNow()
	transitionCtx, transitionCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer transitionCancel()
	if _, err := c.app.store.db.ExecContext(transitionCtx, `UPDATE upstream_test_operations SET state='in_progress',started_at=? WHERE operation_id=? AND state='pending'`, started, operationID); err != nil {
		return upstreamTestOperationView{}, 0, "", err
	}
	testCtx, cancel := context.WithTimeout(c.ctx, upstreamHealthTimeout)
	stopRequestCancel := context.AfterFunc(requestCtx, cancel)
	if requestCtx.Err() != nil {
		cancel()
	}
	startTime := time.Now()
	testedRevision, result := c.execute(testCtx, snapshot, scope)
	stopRequestCancel()
	cancel()
	latency := time.Since(startTime).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	if !upstreamHealthResultCodes[result] {
		result = "internal_failure"
	}
	if c.beforeFinalize != nil {
		c.beforeFinalize()
	}
	finalCtx, finalCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer finalCancel()
	finished := utcNow()
	if err := c.finalize(finalCtx, snapshot, testedRevision, operationID, result, finished, latency); err != nil {
		return upstreamTestOperationView{}, 0, "", err
	}
	view, err = c.load(finalCtx, operationID)
	return view, http.StatusOK, "", err
}

func (c *upstreamHealthCoordinator) execute(ctx context.Context, snapshot upstreamHealthSnapshot, scope string) (*int64, string) {
	if ctx.Err() != nil {
		revision := snapshot.revision
		return &revision, healthResultForContext(ctx)
	}
	if snapshot.provider == codexMembershipProvider && !c.app.cfg.ExperimentalCodexMembership {
		revision := snapshot.revision
		return &revision, "unsupported"
	}
	if scope == "local_credential" {
		revision := snapshot.revision
		return &revision, c.localCredential(snapshot)
	}
	switch snapshot.provider {
	case "openai-compatible", anthropicAPIKeyProvider:
		revision := snapshot.revision
		return &revision, c.app.runAPIKeyCatalogTest(ctx, snapshot)
	case geminiAPIKeyProvider:
		revision := snapshot.revision
		return &revision, c.app.runGeminiCatalogTest(ctx, snapshot)
	case codexMembershipProvider:
		return c.app.runCodexCatalogTest(ctx, snapshot)
	default:
		return nil, "unsupported"
	}
}

func (c *upstreamHealthCoordinator) localCredential(snapshot upstreamHealthSnapshot) string {
	switch snapshot.provider {
	case "openai-compatible", anthropicAPIKeyProvider:
		if snapshot.keyVersion != 1 {
			return "configuration_changed"
		}
		secret, err := c.app.secrets.decryptCredential(snapshot.id, snapshot.ciphertext)
		if err != nil || !validUpstreamHeaderValue(secret) {
			return "authentication_failed"
		}
		return "local_credential_ok"
	case geminiAPIKeyProvider:
		if snapshot.keyVersion != 2 {
			return "configuration_changed"
		}
		secret, err := c.app.secrets.decryptGeminiAPIKey(snapshot.id, snapshot.ciphertext)
		if err != nil || !validUpstreamHeaderValue(secret) {
			return "authentication_failed"
		}
		return "local_credential_ok"
	case codexMembershipProvider:
		if snapshot.keyVersion != 2 || !snapshot.credentialState.Valid || (snapshot.credentialState.String != codexStateImported && snapshot.credentialState.String != codexStateVerified) {
			return "authentication_failed"
		}
		plaintext, err := c.app.secrets.decryptCodexAuth(snapshot.id, snapshot.ciphertext)
		if err != nil {
			return "authentication_failed"
		}
		credential, err := parseSchedulableCodexAuth(plaintext)
		clear(plaintext)
		if err != nil {
			return "authentication_failed"
		}
		credential.Destroy()
		return "local_credential_ok"
	default:
		return "unsupported"
	}
}

func (c *upstreamHealthCoordinator) finalize(ctx context.Context, original upstreamHealthSnapshot, tested *int64, operationID, result, finished string, latency int64) error {
	var unlock func()
	var err error
	if original.provider == codexMembershipProvider {
		unlock, err = c.app.acquireCodexMutationLock(ctx, original.id)
		if err != nil {
			return err
		}
		defer unlock()
	}
	c.app.admission.RLock()
	defer c.app.admission.RUnlock()
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if tested != nil {
		current, loadErr := c.loadSnapshotFrom(ctx, tx, original.id)
		if errors.Is(loadErr, sql.ErrNoRows) || (loadErr == nil && (current.provider != original.provider || current.source != original.source || current.revision != *tested)) {
			result = "stale"
		} else if loadErr != nil {
			return loadErr
		}
	}
	var testedValue any
	if tested != nil {
		testedValue = *tested
	}
	updated, err := tx.ExecContext(ctx, `UPDATE upstream_test_operations
		SET state='completed',tested_revision=?,result_code=?,finished_at=?,latency_ms=?
		WHERE operation_id=? AND state='in_progress'`, testedValue, result, finished, latency, operationID)
	if err != nil {
		return err
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("upstream test state conflict")
	}
	return tx.Commit()
}

func (c *upstreamHealthCoordinator) loadSnapshot(ctx context.Context, id string) (upstreamHealthSnapshot, error) {
	return c.loadSnapshotFrom(ctx, c.app.store.db, id)
}

func (c *upstreamHealthCoordinator) loadSnapshotFrom(ctx context.Context, query queryRower, id string) (upstreamHealthSnapshot, error) {
	var snapshot upstreamHealthSnapshot
	var clientID, bindingSource sql.NullString
	err := query.QueryRowContext(ctx, `SELECT u.id,u.provider_kind,u.endpoint,u.revision,u.key_version,u.credential_ciphertext,u.credential_state,b.client_id,b.source
		FROM upstreams u LEFT JOIN codex_oauth_bindings b ON b.upstream_id=u.id WHERE u.id=?`, id).
		Scan(&snapshot.id, &snapshot.provider, &snapshot.endpoint, &snapshot.revision, &snapshot.keyVersion, &snapshot.ciphertext, &snapshot.credentialState, &clientID, &bindingSource)
	if err != nil {
		return snapshot, err
	}
	snapshot.source = upstreamHealthSource(snapshot.provider, clientID, bindingSource)
	return snapshot, nil
}

func upstreamHealthSource(provider string, clientID, source sql.NullString) string {
	if provider == codexMembershipProvider {
		if clientID.Valid && source.Valid {
			return source.String + ":" + clientID.String
		}
		return "import"
	}
	return "api_key"
}

func (c *upstreamHealthCoordinator) load(ctx context.Context, operationID string) (upstreamTestOperationView, error) {
	var view upstreamTestOperationView
	var tested sql.NullInt64
	var result, started, finished sql.NullString
	var latency sql.NullInt64
	err := c.app.store.db.QueryRowContext(ctx, `SELECT operation_id,upstream_id,provider_kind,requested_revision,tested_revision,scope,state,result_code,created_at,started_at,finished_at,latency_ms
		FROM upstream_test_operations WHERE operation_id=?`, operationID).
		Scan(&view.OperationID, &view.UpstreamID, &view.ProviderKind, &view.RequestedRevision, &tested, &view.Scope, &view.State, &result, &view.CreatedAt, &started, &finished, &latency)
	if tested.Valid {
		view.TestedRevision = &tested.Int64
	}
	if result.Valid {
		view.ResultCode = &result.String
	}
	if started.Valid {
		view.StartedAt = &started.String
	}
	if finished.Valid {
		view.FinishedAt = &finished.String
	}
	if latency.Valid {
		view.LatencyMS = &latency.Int64
	}
	return view, err
}

func (c *upstreamHealthCoordinator) get(ctx context.Context, operationID string) (upstreamTestOperationView, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loadRecoveringLocked(ctx, operationID)
}

func (c *upstreamHealthCoordinator) loadRecoveringLocked(ctx context.Context, operationID string) (upstreamTestOperationView, error) {
	view, err := c.load(ctx, operationID)
	if err != nil || view.State == "completed" {
		return view, err
	}
	if activeOperation, active := c.active[view.UpstreamID]; active && activeOperation == operationID {
		return view, nil
	}
	if err := c.recoverOperationLocked(operationID); err != nil {
		return upstreamTestOperationView{}, err
	}
	return c.load(ctx, operationID)
}

func (c *upstreamHealthCoordinator) recoverAccountOrphansLocked(upstreamID string) error {
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	finished := utcNow()
	if activeOperation, active := c.active[upstreamID]; active {
		_, err := c.app.store.db.ExecContext(recoveryCtx, `UPDATE upstream_test_operations
			SET state='completed',result_code='interrupted',finished_at=?,latency_ms=NULL
			WHERE upstream_id=? AND state IN ('pending','in_progress') AND operation_id<>?`, finished, upstreamID, activeOperation)
		return err
	}
	_, err := c.app.store.db.ExecContext(recoveryCtx, `UPDATE upstream_test_operations
		SET state='completed',result_code='interrupted',finished_at=?,latency_ms=NULL
		WHERE upstream_id=? AND state IN ('pending','in_progress')`, finished, upstreamID)
	return err
}

func (c *upstreamHealthCoordinator) recoverOperationLocked(operationID string) error {
	recoveryCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	updated, err := c.app.store.db.ExecContext(recoveryCtx, `UPDATE upstream_test_operations
		SET state='completed',result_code='interrupted',finished_at=?,latency_ms=NULL
		WHERE operation_id=? AND state IN ('pending','in_progress')`, utcNow(), operationID)
	if err != nil {
		return err
	}
	changed, err := updated.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("upstream test recovery conflict")
	}
	return nil
}

func validUpstreamHeaderValue(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == 0x7f || character < 0x20 && character != '\t' {
			return false
		}
	}
	return true
}

func (a *App) decorateUpstreamObservations(ctx context.Context, items []upstreamView) error {
	if len(items) == 0 {
		return nil
	}
	wanted := make(map[string]*upstreamView, len(items))
	for index := range items {
		wanted[items[index].ID] = &items[index]
	}
	rows, err := a.store.db.QueryContext(ctx, `SELECT o.operation_id,o.upstream_id,o.provider_kind,o.scope,o.result_code,o.tested_revision,o.finished_at,o.latency_ms
		FROM upstream_test_operations o
		JOIN upstreams u ON u.id=o.upstream_id
		LEFT JOIN codex_oauth_bindings b ON b.upstream_id=u.id
		WHERE o.state='completed' AND o.tested_revision=u.revision AND o.provider_kind=u.provider_kind
		  AND o.source_snapshot=CASE WHEN u.provider_kind='codex-membership'
		    THEN CASE WHEN b.client_id IS NOT NULL AND b.source IS NOT NULL THEN b.source||':'||b.client_id ELSE 'import' END
		    ELSE 'api_key' END
		  AND o.result_code NOT IN ('stale','interrupted')
		ORDER BY o.finished_at DESC,o.operation_id DESC`)
	if err != nil {
		return err
	}
	defer rows.Close()
	seen := make(map[string]bool, len(items))
	for rows.Next() {
		var operationID, upstreamID, provider, scope, result, checked string
		var revision int64
		var latency sql.NullInt64
		if err := rows.Scan(&operationID, &upstreamID, &provider, &scope, &result, &revision, &checked, &latency); err != nil {
			return err
		}
		item := wanted[upstreamID]
		if item == nil || seen[upstreamID] || item.Revision != revision || item.ProviderKind != provider {
			continue
		}
		observation := &upstreamObservationView{OperationID: operationID, Scope: scope, ResultCode: result, AccountRevision: revision, CheckedAt: checked}
		if latency.Valid {
			observation.LatencyMS = &latency.Int64
		}
		item.LatestObservation = observation
		seen[upstreamID] = true
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	return closeErr
}

func healthResultForContext(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if ctx.Err() != nil {
		return "cancelled"
	}
	return "internal_failure"
}
