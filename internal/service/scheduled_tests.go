package service

// Independently implemented from docs/parity-next-batch-2026-09-24.md.
// Scheduled tests persist metadata only and delegate all credential and network
// work to upstreamHealthCoordinator.
import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	scheduledTestMinInterval = int64(300)
	scheduledTestMaxInterval = int64(86400)
	scheduledTestMaxPlans    = int64(100)
	scheduledTestMaxRunning  = int64(2)
	scheduledTestMaxHistory  = int64(200)
)

const scheduledTestPlanDDL = `CREATE TABLE IF NOT EXISTS scheduled_test_plans (
	id TEXT NOT NULL PRIMARY KEY,
	name TEXT NOT NULL,
	upstream_id TEXT NOT NULL REFERENCES upstreams(id) ON DELETE RESTRICT,
	scope TEXT NOT NULL CHECK(scope IN ('local_credential','catalog')),
	interval_seconds INTEGER NOT NULL CHECK(interval_seconds BETWEEN 300 AND 86400),
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(revision >= 1),
	next_run_at TEXT,
	created_by_admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	updated_by_admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	archived_at TEXT,
	CHECK((enabled=0 AND next_run_at IS NULL) OR (enabled=1 AND next_run_at IS NOT NULL)),
	CHECK(archived_at IS NULL OR (enabled=0 AND next_run_at IS NULL))
)`

const scheduledTestRunDDL = `CREATE TABLE IF NOT EXISTS scheduled_test_runs (
	sequence INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
	plan_id TEXT NOT NULL REFERENCES scheduled_test_plans(id) ON DELETE RESTRICT,
	plan_revision INTEGER NOT NULL CHECK(plan_revision >= 1),
	upstream_id TEXT NOT NULL REFERENCES upstreams(id) ON DELETE RESTRICT,
	operation_id TEXT NOT NULL UNIQUE,
	scope TEXT NOT NULL CHECK(scope IN ('local_credential','catalog')),
	state TEXT NOT NULL CHECK(state IN ('running','completed')),
	result_code TEXT CHECK(result_code IS NULL OR result_code IN ('local_credential_ok','catalog_ok','authentication_failed','rate_limited','unsupported','timeout','invalid_response','configuration_changed','stale','cancelled','interrupted','test_in_progress','capacity_exceeded','storage_unavailable','internal_failure')),
	started_at TEXT NOT NULL,
	finished_at TEXT,
	latency_ms INTEGER CHECK(latency_ms IS NULL OR latency_ms >= 0),
	actor TEXT NOT NULL CHECK(actor='system'),
	CHECK((state='running' AND result_code IS NULL AND finished_at IS NULL AND latency_ms IS NULL)
	 OR (state='completed' AND result_code IS NOT NULL AND finished_at IS NOT NULL AND latency_ms IS NOT NULL))
)`

type scheduledTestPlan struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	UpstreamID      string            `json:"upstream_id"`
	Scope           string            `json:"scope"`
	IntervalSeconds int64             `json:"interval_seconds"`
	Enabled         bool              `json:"enabled"`
	Revision        int64             `json:"revision"`
	NextRunAt       *string           `json:"next_run_at"`
	LatestResult    *scheduledTestRun `json:"latest_result"`
	CreatedAt       string            `json:"created_at"`
	UpdatedAt       string            `json:"updated_at"`
	ArchivedAt      *string           `json:"-"`
}

type scheduledTestRun struct {
	PlanRevision int64   `json:"plan_revision"`
	OperationID  string  `json:"operation_id"`
	ResultCode   *string `json:"result_code"`
	StartedAt    string  `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
	LatencyMS    *int64  `json:"latency_ms"`
	State        string  `json:"state"`
	Scope        string  `json:"scope"`
}

type scheduledTestRunsPage struct {
	Items      []scheduledTestRun `json:"items"`
	NextCursor *string            `json:"next_cursor"`
}

type scheduledTestClaim struct {
	PlanID           string
	PlanRevision     int64
	UpstreamID       string
	UpstreamRevision int64
	Scope            string
	OperationID      string
	StartedAt        time.Time
}

// archiveScheduledTestsForUpstreamTx is the caller-owned transaction hook used
// by upstream archival. It deliberately performs SQL only: the caller must
// commit first, then call cancelUpstream so no database lock waits on network I/O.
func archiveScheduledTestsForUpstreamTx(ctx context.Context, tx *sql.Tx, upstreamID, adminID string, now time.Time) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM scheduled_test_plans WHERE upstream_id=? AND archived_at IS NULL ORDER BY id`, upstreamID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return nil, iterationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if len(ids) == 0 {
		return ids, nil
	}
	stamp := formatAccountPoolTime(now.UTC())
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_test_plans SET enabled=0,revision=revision+1,next_run_at=NULL,updated_by_admin_id=?,updated_at=? WHERE upstream_id=? AND archived_at IS NULL`, adminID, stamp, upstreamID)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != int64(len(ids)) {
		return nil, errors.New("scheduled test upstream archive conflict")
	}
	return ids, nil
}

func migrateScheduledTests(ctx context.Context, db *sql.DB, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range []string{
		scheduledTestPlanDDL,
		scheduledTestRunDDL,
		`CREATE INDEX IF NOT EXISTS scheduled_test_plans_due_idx ON scheduled_test_plans(enabled,archived_at,next_run_at,id)`,
		`CREATE INDEX IF NOT EXISTS scheduled_test_runs_plan_idx ON scheduled_test_runs(plan_id,sequence DESC)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS scheduled_test_runs_account_active_idx ON scheduled_test_runs(upstream_id) WHERE state='running'`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate scheduled tests: %w", err)
		}
	}
	if err := verifyScheduledTestColumns(ctx, tx, "scheduled_test_plans", []string{"id", "name", "upstream_id", "scope", "interval_seconds", "enabled", "revision", "next_run_at", "created_by_admin_id", "updated_by_admin_id", "created_at", "updated_at", "archived_at"}); err != nil {
		return err
	}
	if err := verifyScheduledTestColumns(ctx, tx, "scheduled_test_runs", []string{"sequence", "plan_id", "plan_revision", "upstream_id", "operation_id", "scope", "state", "result_code", "started_at", "finished_at", "latency_ms", "actor"}); err != nil {
		return err
	}
	if err := verifyScheduledTestIndexes(ctx, tx); err != nil {
		return err
	}
	stamp := formatAccountPoolTime(now.UTC())
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT plan_id FROM scheduled_test_runs WHERE state='running'`)
	if err != nil {
		return err
	}
	var interrupted []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		interrupted = append(interrupted, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE scheduled_test_runs SET state='completed',result_code='interrupted',finished_at=?,latency_ms=0 WHERE state='running'`, stamp); err != nil {
		return err
	}
	for _, id := range interrupted {
		if _, err := tx.ExecContext(ctx, `UPDATE scheduled_test_plans SET next_run_at=? WHERE id=? AND enabled=1 AND archived_at IS NULL`, stamp, id); err != nil {
			return err
		}
	}
	foreignRows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	violated := foreignRows.Next()
	iterationErr := foreignRows.Err()
	closeErr := foreignRows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if violated {
		return errors.New("scheduled test migration foreign key check failed")
	}
	return tx.Commit()
}

func verifyScheduledTestColumns(ctx context.Context, tx *sql.Tx, table string, expected []string) error {
	expectedSpecs := map[string]map[string]upstreamHealthColumnSpec{
		"scheduled_test_plans": {
			"id": {"TEXT", 1, 1}, "name": {"TEXT", 1, 0}, "upstream_id": {"TEXT", 1, 0}, "scope": {"TEXT", 1, 0},
			"interval_seconds": {"INTEGER", 1, 0}, "enabled": {"INTEGER", 1, 0}, "revision": {"INTEGER", 1, 0}, "next_run_at": {"TEXT", 0, 0},
			"created_by_admin_id": {"TEXT", 1, 0}, "updated_by_admin_id": {"TEXT", 1, 0}, "created_at": {"TEXT", 1, 0}, "updated_at": {"TEXT", 1, 0}, "archived_at": {"TEXT", 0, 0},
		},
		"scheduled_test_runs": {
			"sequence": {"INTEGER", 1, 1}, "plan_id": {"TEXT", 1, 0}, "plan_revision": {"INTEGER", 1, 0}, "upstream_id": {"TEXT", 1, 0},
			"operation_id": {"TEXT", 1, 0}, "scope": {"TEXT", 1, 0}, "state": {"TEXT", 1, 0}, "result_code": {"TEXT", 0, 0},
			"started_at": {"TEXT", 1, 0}, "finished_at": {"TEXT", 0, 0}, "latency_ms": {"INTEGER", 0, 0}, "actor": {"TEXT", 1, 0},
		},
	}
	specs := expectedSpecs[table]
	if len(specs) != len(expected) {
		return errors.New("scheduled test schema verifier configuration is invalid")
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
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
		spec, ok := specs[name]
		if !ok || strings.ToUpper(kind) != spec.kind || notNull != spec.notNull || primary != spec.primary || defaultValue != nil {
			rows.Close()
			return errors.New("existing " + table + " table has an incompatible schema")
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
		return errors.New("existing " + table + " table has an incompatible schema")
	}
	for _, name := range expected {
		if !seen[name] {
			return errors.New("existing " + table + " table has an incompatible schema")
		}
	}
	var objectType, schema string
	if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, table).Scan(&objectType, &schema); err != nil {
		return err
	}
	if objectType != "table" {
		return errors.New("existing " + table + " object is not a table")
	}
	want := scheduledTestPlanDDL
	if table == "scheduled_test_runs" {
		want = scheduledTestRunDDL
	}
	normalized := normalizeHealthDDL(schema)
	expectedDDL := normalizeHealthDDL(strings.Replace(want, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1))
	if normalized != expectedDDL && normalized != normalizeHealthDDL(want) {
		return errors.New("existing " + table + " table has an incompatible definition")
	}
	if err := verifyScheduledTestForeignKeys(ctx, tx, table); err != nil {
		return err
	}
	return verifyScheduledTestRows(ctx, tx, table)
}

func verifyScheduledTestForeignKeys(ctx context.Context, tx *sql.Tx, table string) error {
	type foreignKey struct{ table, to, onDelete, onUpdate string }
	wanted := map[string]foreignKey{}
	if table == "scheduled_test_plans" {
		wanted = map[string]foreignKey{
			"upstream_id":         {"upstreams", "id", "RESTRICT", "NO ACTION"},
			"created_by_admin_id": {"admins", "id", "RESTRICT", "NO ACTION"},
			"updated_by_admin_id": {"admins", "id", "RESTRICT", "NO ACTION"},
		}
	} else {
		wanted = map[string]foreignKey{
			"plan_id":     {"scheduled_test_plans", "id", "RESTRICT", "NO ACTION"},
			"upstream_id": {"upstreams", "id", "RESTRICT", "NO ACTION"},
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(`+table+`)`)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var id, sequence int
		var target, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &target, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return err
		}
		want, ok := wanted[from]
		if !ok || sequence != 0 || target != want.table || to != want.to || onDelete != want.onDelete || onUpdate != want.onUpdate || match != "NONE" {
			rows.Close()
			return errors.New("existing " + table + " table has incompatible foreign keys")
		}
		seen[from] = true
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(seen) != len(wanted) {
		return errors.New("existing " + table + " table has incompatible foreign keys")
	}
	return nil
}

func verifyScheduledTestRows(ctx context.Context, tx *sql.Tx, table string) error {
	query := `SELECT COUNT(*) FROM scheduled_test_plans WHERE
		typeof(name)<>'text' OR length(name)<1 OR length(name)>120 OR
		typeof(interval_seconds)<>'integer' OR interval_seconds<300 OR interval_seconds>86400 OR
		typeof(enabled)<>'integer' OR enabled NOT IN (0,1) OR typeof(revision)<>'integer' OR revision<1 OR
		scope NOT IN ('local_credential','catalog') OR
		NOT ((enabled=0 AND next_run_at IS NULL) OR (enabled=1 AND next_run_at IS NOT NULL)) OR
		(archived_at IS NOT NULL AND (enabled<>0 OR next_run_at IS NOT NULL))`
	if table == "scheduled_test_runs" {
		query = `SELECT COUNT(*) FROM scheduled_test_runs WHERE
			typeof(sequence)<>'integer' OR sequence<1 OR typeof(plan_revision)<>'integer' OR plan_revision<1 OR
			scope NOT IN ('local_credential','catalog') OR state NOT IN ('running','completed') OR actor<>'system' OR
			(result_code IS NOT NULL AND result_code NOT IN ('local_credential_ok','catalog_ok','authentication_failed','rate_limited','unsupported','timeout','invalid_response','configuration_changed','stale','cancelled','interrupted','test_in_progress','capacity_exceeded','storage_unavailable','internal_failure')) OR
			(latency_ms IS NOT NULL AND (typeof(latency_ms)<>'integer' OR latency_ms<0)) OR
			NOT ((state='running' AND result_code IS NULL AND finished_at IS NULL AND latency_ms IS NULL)
			 OR (state='completed' AND result_code IS NOT NULL AND finished_at IS NOT NULL AND latency_ms IS NOT NULL))`
	}
	var invalid int
	if err := tx.QueryRowContext(ctx, query).Scan(&invalid); err != nil {
		return err
	}
	if invalid != 0 {
		return errors.New("existing " + table + " table contains invalid rows")
	}
	return nil
}

func verifyScheduledTestIndexes(ctx context.Context, tx *sql.Tx) error {
	wanted := map[string]string{
		"scheduled_test_plans_due_idx":           `CREATE INDEX scheduled_test_plans_due_idx ON scheduled_test_plans(enabled,archived_at,next_run_at,id)`,
		"scheduled_test_runs_plan_idx":           `CREATE INDEX scheduled_test_runs_plan_idx ON scheduled_test_runs(plan_id,sequence DESC)`,
		"scheduled_test_runs_account_active_idx": `CREATE UNIQUE INDEX scheduled_test_runs_account_active_idx ON scheduled_test_runs(upstream_id) WHERE state='running'`,
	}
	for name, definition := range wanted {
		var objectType, schema string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&objectType, &schema); err != nil {
			return err
		}
		if objectType != "index" || normalizeHealthDDL(schema) != normalizeHealthDDL(definition) {
			return errors.New("existing scheduled test index has an incompatible definition")
		}
	}
	return nil
}

func scanScheduledTestPlan(scanner interface{ Scan(...any) error }) (scheduledTestPlan, error) {
	var plan scheduledTestPlan
	var enabled int
	var next, archived sql.NullString
	err := scanner.Scan(&plan.ID, &plan.Name, &plan.UpstreamID, &plan.Scope, &plan.IntervalSeconds, &enabled, &plan.Revision, &next, &plan.CreatedAt, &plan.UpdatedAt, &archived)
	plan.Enabled = enabled == 1
	if next.Valid {
		plan.NextRunAt = &next.String
	}
	if archived.Valid {
		plan.ArchivedAt = &archived.String
	}
	return plan, err
}

const scheduledTestPlanSelect = `SELECT id,name,upstream_id,scope,interval_seconds,enabled,revision,next_run_at,created_at,updated_at,archived_at FROM scheduled_test_plans`

func loadScheduledTestPlan(ctx context.Context, query queryRower, id string, includeArchived bool) (scheduledTestPlan, error) {
	sqlText := scheduledTestPlanSelect + ` WHERE id=?`
	if !includeArchived {
		sqlText += ` AND archived_at IS NULL`
	}
	plan, err := scanScheduledTestPlan(query.QueryRowContext(ctx, sqlText, id))
	return plan, err
}

func loadScheduledTestRun(scanner interface{ Scan(...any) error }) (int64, scheduledTestRun, error) {
	var sequence int64
	var item scheduledTestRun
	var result, finished sql.NullString
	var latency sql.NullInt64
	err := scanner.Scan(&sequence, &item.PlanRevision, &item.OperationID, &item.Scope, &item.State, &result, &item.StartedAt, &finished, &latency)
	if result.Valid {
		item.ResultCode = &result.String
	}
	if finished.Valid {
		item.FinishedAt = &finished.String
	}
	if latency.Valid {
		item.LatencyMS = &latency.Int64
	}
	return sequence, item, err
}

func decorateScheduledTestLatest(ctx context.Context, db *sql.DB, plans []scheduledTestPlan) error {
	for index := range plans {
		_, item, err := loadScheduledTestRun(db.QueryRowContext(ctx, `SELECT sequence,plan_revision,operation_id,scope,state,result_code,started_at,finished_at,latency_ms FROM scheduled_test_runs WHERE plan_id=? AND plan_revision=? ORDER BY sequence DESC LIMIT 1`, plans[index].ID, plans[index].Revision))
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		plans[index].LatestResult = &item
	}
	return nil
}

func encodeScheduledRunCursor(sequence int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(sequence, 10)))
}

func decodeScheduledRunCursor(value string) (int64, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) == 0 || len(decoded) > 20 || strings.HasPrefix(string(decoded), "+") {
		return 0, false
	}
	sequence, err := strconv.ParseInt(string(decoded), 10, 64)
	return sequence, err == nil && sequence > 0
}
