package service

// Independently authored from docs/channel-monitor-contract.md. No provider
// protocol or credential implementation is introduced by this package file.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Independently authored retained-window read model for
// docs/channel-monitor-summary-contract.md. It is never persisted.
type channelMonitorSummary struct {
	PlanID             string                    `json:"plan_id"`
	AsOf               string                    `json:"as_of"`
	ThroughSequence    int64                     `json:"through_sequence"`
	RetainedCompleted  int                       `json:"retained_completed"`
	RetainedWindowFull bool                      `json:"retained_window_full"`
	Running            int                       `json:"running"`
	EarliestFinishedAt *string                   `json:"earliest_finished_at"`
	LatestFinishedAt   *string                   `json:"latest_finished_at"`
	Counts             map[string]map[string]int `json:"counts"`
}

var channelMonitorSummaryCodes = [...]string{
	"local_credential_ok", "catalog_ok", "authentication_failed", "rate_limited", "unsupported",
	"timeout", "invalid_response", "configuration_changed", "stale", "cancelled", "interrupted",
	"test_in_progress", "capacity_exceeded", "storage_unavailable", "internal_failure",
}

// Only an absent plan is a 404. Missing schema objects also surface as
// sql.ErrNoRows, but must remain storage failures at the HTTP boundary.
var errChannelMonitorSummaryPlanNotFound = errors.New("channel monitor summary plan not found")

func newChannelMonitorSummary(id string, now time.Time) channelMonitorSummary {
	result := channelMonitorSummary{PlanID: id, AsOf: now.UTC().Format(time.RFC3339Nano), Counts: make(map[string]map[string]int, 2)}
	for _, scope := range []string{"local_credential", "catalog"} {
		result.Counts[scope] = make(map[string]int, len(channelMonitorSummaryCodes))
		for _, code := range channelMonitorSummaryCodes {
			result.Counts[scope][code] = 0
		}
	}
	return result
}

// loadChannelMonitorSummary takes one SQLite snapshot and scans only the
// bounded retained history for this plan. In particular, it never fetches
// upstream credentials, endpoints or probe payloads.
func loadChannelMonitorSummary(ctx context.Context, db *sql.DB, id string, now time.Time) (channelMonitorSummary, error) {
	result := newChannelMonitorSummary(id, now)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return channelMonitorSummary{}, err
	}
	defer tx.Rollback()
	for name, ddl := range map[string]string{"channel_monitor_plans": channelMonitorPlanDDL, "channel_monitor_runs": channelMonitorRunDDL} {
		if err := verifyChannelMonitorDDL(ctx, tx, "table", name, ddl); err != nil {
			return channelMonitorSummary{}, err
		}
	}
	for name, ddl := range channelMonitorIndexes {
		if err := verifyChannelMonitorDDL(ctx, tx, "index", name, ddl); err != nil {
			return channelMonitorSummary{}, err
		}
	}
	if err := verifyChannelMonitorObjects(ctx, tx); err != nil {
		return channelMonitorSummary{}, err
	}
	if _, err := loadChannelMonitorPlan(ctx, tx, id, true); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return channelMonitorSummary{}, errChannelMonitorSummaryPlanNotFound
		}
		return channelMonitorSummary{}, err
	}
	var highWater sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(sequence) FROM channel_monitor_runs WHERE plan_id=?`, id).Scan(&highWater); err != nil {
		return channelMonitorSummary{}, err
	}
	if highWater.Valid {
		if highWater.Int64 < 1 {
			return channelMonitorSummary{}, errors.New("invalid channel monitor sequence")
		}
		result.ThroughSequence = highWater.Int64
	}
	rows, err := tx.QueryContext(ctx, channelMonitorRunSelect+` WHERE plan_id=? AND sequence<=? ORDER BY sequence DESC LIMIT ?`, id, result.ThroughSequence, channelMonitorMaxHistory+channelMonitorMaxRunning+1)
	if err != nil {
		return channelMonitorSummary{}, err
	}
	var earliest, latest time.Time
	seen := 0
	for rows.Next() {
		seen++
		if seen > channelMonitorMaxHistory+channelMonitorMaxRunning {
			rows.Close()
			return channelMonitorSummary{}, errors.New("channel monitor history limit exceeded")
		}
		run, err := scanChannelMonitorRun(rows)
		if err != nil || run.Sequence < 1 || run.Sequence > result.ThroughSequence || !validIdentifier(run.OperationID, 128) || run.ChannelID == "" || run.ModelID == "" || run.UpstreamID == "" || run.RouteUpstreamModel == "" || run.RouteWireProtocol == "" || run.LatencyMS != nil && *run.LatencyMS < 0 {
			rows.Close()
			return channelMonitorSummary{}, errors.New("invalid channel monitor summary row")
		}
		if run.State == "running" {
			result.Running++
			if result.Running > channelMonitorMaxRunning {
				rows.Close()
				return channelMonitorSummary{}, errors.New("multiple running channel monitor rows")
			}
			continue
		}
		result.RetainedCompleted++
		if result.RetainedCompleted > channelMonitorMaxHistory {
			rows.Close()
			return channelMonitorSummary{}, errors.New("channel monitor completion limit exceeded")
		}
		if _, ok := result.Counts[run.Scope][*run.ResultCode]; !ok {
			rows.Close()
			return channelMonitorSummary{}, errors.New("invalid channel monitor summary code")
		}
		result.Counts[run.Scope][*run.ResultCode]++
		started, _ := parseTime(run.StartedAt)
		finished, _ := parseTime(*run.FinishedAt)
		if finished.Before(started) {
			rows.Close()
			return channelMonitorSummary{}, errors.New("invalid channel monitor completion time")
		}
		if earliest.IsZero() || finished.Before(earliest) {
			earliest = finished
		}
		if latest.IsZero() || finished.After(latest) {
			latest = finished
		}
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return channelMonitorSummary{}, iterationErr
	}
	if closeErr != nil {
		return channelMonitorSummary{}, closeErr
	}
	if !earliest.IsZero() {
		earliestValue := earliest.UTC().Format(time.RFC3339Nano)
		latestValue := latest.UTC().Format(time.RFC3339Nano)
		result.EarliestFinishedAt = &earliestValue
		result.LatestFinishedAt = &latestValue
	}
	result.RetainedWindowFull = result.RetainedCompleted == channelMonitorMaxHistory
	if err := tx.Commit(); err != nil {
		return channelMonitorSummary{}, err
	}
	return result, nil
}

const (
	channelMonitorMaxPlans    = 100
	channelMonitorMaxHistory  = 200
	channelMonitorMaxRunning  = 2
	channelMonitorMinInterval = 300
	channelMonitorMaxInterval = 86400
)

const channelMonitorPlanDDL = `CREATE TABLE IF NOT EXISTS channel_monitor_plans (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL CHECK(length(name) BETWEEN 1 AND 120),
	channel_id TEXT NOT NULL REFERENCES account_channels(id),
	model_id TEXT NOT NULL REFERENCES models(id),
	upstream_id TEXT NOT NULL REFERENCES upstreams(id),
	scope TEXT NOT NULL CHECK(scope IN ('local_credential','catalog')),
	interval_seconds INTEGER NOT NULL CHECK(interval_seconds BETWEEN 300 AND 86400),
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(revision >= 1),
	next_run_at TEXT,
	channel_revision INTEGER NOT NULL CHECK(channel_revision >= 1),
	model_revision INTEGER NOT NULL CHECK(model_revision >= 1),
	pool_revision INTEGER NOT NULL CHECK(pool_revision >= 1),
	upstream_revision INTEGER NOT NULL CHECK(upstream_revision >= 1),
	route_upstream_model TEXT NOT NULL,
	route_wire_protocol TEXT NOT NULL,
	route_position INTEGER NOT NULL CHECK(route_position BETWEEN 0 AND 63),
	created_by_admin_id TEXT NOT NULL REFERENCES admins(id),
	updated_by_admin_id TEXT NOT NULL REFERENCES admins(id),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	archived_at TEXT,
	CHECK((enabled=0 AND next_run_at IS NULL) OR (enabled=1 AND next_run_at IS NOT NULL)),
	CHECK(archived_at IS NULL OR enabled=0)
)`

const channelMonitorRunDDL = `CREATE TABLE IF NOT EXISTS channel_monitor_runs (
	sequence INTEGER PRIMARY KEY AUTOINCREMENT,
	plan_id TEXT NOT NULL REFERENCES channel_monitor_plans(id),
	plan_revision INTEGER NOT NULL CHECK(plan_revision >= 1),
	channel_id TEXT NOT NULL,
	channel_revision INTEGER NOT NULL CHECK(channel_revision >= 1),
	model_id TEXT NOT NULL,
	model_revision INTEGER NOT NULL CHECK(model_revision >= 1),
	pool_revision INTEGER NOT NULL CHECK(pool_revision >= 1),
	upstream_id TEXT NOT NULL,
	upstream_revision INTEGER NOT NULL CHECK(upstream_revision >= 1),
	route_upstream_model TEXT NOT NULL,
	route_wire_protocol TEXT NOT NULL,
	route_position INTEGER NOT NULL CHECK(route_position BETWEEN 0 AND 63),
	operation_id TEXT NOT NULL UNIQUE,
	scope TEXT NOT NULL CHECK(scope IN ('local_credential','catalog')),
	state TEXT NOT NULL CHECK(state IN ('running','completed')),
	result_code TEXT,
	started_at TEXT NOT NULL,
	finished_at TEXT,
	latency_ms INTEGER CHECK(latency_ms IS NULL OR latency_ms >= 0),
	actor TEXT NOT NULL CHECK(actor='system'),
	CHECK((state='running' AND result_code IS NULL AND finished_at IS NULL AND latency_ms IS NULL)
	 OR (state='completed' AND result_code IS NOT NULL AND finished_at IS NOT NULL AND latency_ms IS NOT NULL))
)`

var channelMonitorIndexes = map[string]string{
	"channel_monitor_plans_due_idx":           `CREATE INDEX IF NOT EXISTS channel_monitor_plans_due_idx ON channel_monitor_plans(enabled,archived_at,next_run_at,id)`,
	"channel_monitor_runs_plan_idx":           `CREATE INDEX IF NOT EXISTS channel_monitor_runs_plan_idx ON channel_monitor_runs(plan_id,sequence DESC)`,
	"channel_monitor_runs_account_active_idx": `CREATE UNIQUE INDEX IF NOT EXISTS channel_monitor_runs_account_active_idx ON channel_monitor_runs(upstream_id) WHERE state='running'`,
}

func migrateChannelMonitors(ctx context.Context, db *sql.DB, now time.Time) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := verifyChannelMonitorDependencies(ctx, tx); err != nil {
		return err
	}
	for _, ddl := range []string{channelMonitorPlanDDL, channelMonitorRunDDL} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	for _, ddl := range channelMonitorIndexes {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	for name, ddl := range map[string]string{"channel_monitor_plans": channelMonitorPlanDDL, "channel_monitor_runs": channelMonitorRunDDL} {
		if err := verifyChannelMonitorDDL(ctx, tx, "table", name, ddl); err != nil {
			return err
		}
	}
	for name, ddl := range channelMonitorIndexes {
		if err := verifyChannelMonitorDDL(ctx, tx, "index", name, ddl); err != nil {
			return err
		}
	}
	if err := verifyChannelMonitorObjects(ctx, tx); err != nil {
		return err
	}
	if err := verifyChannelMonitorRows(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	broken := rows.Next()
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if broken {
		return errors.New("channel monitor foreign key violation")
	}
	interruptedRows, err := tx.QueryContext(ctx, `SELECT DISTINCT p.id,p.interval_seconds,p.enabled FROM channel_monitor_plans p JOIN channel_monitor_runs r ON r.plan_id=p.id WHERE r.state='running'`)
	if err != nil {
		return err
	}
	type interruptedPlan struct {
		id       string
		interval int64
		enabled  int
	}
	var interrupted []interruptedPlan
	for interruptedRows.Next() {
		var item interruptedPlan
		if err := interruptedRows.Scan(&item.id, &item.interval, &item.enabled); err != nil {
			interruptedRows.Close()
			return err
		}
		interrupted = append(interrupted, item)
	}
	iterationErr, closeErr = interruptedRows.Err(), interruptedRows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	stamp := formatAccountPoolTime(now.UTC())
	if _, err := tx.ExecContext(ctx, `UPDATE channel_monitor_runs SET state='completed',result_code='interrupted',finished_at=?,latency_ms=0 WHERE state='running'`, stamp); err != nil {
		return err
	}
	for _, item := range interrupted {
		if item.enabled == 1 {
			if _, err := tx.ExecContext(ctx, `UPDATE channel_monitor_plans SET next_run_at=?,updated_at=? WHERE id=? AND enabled=1 AND archived_at IS NULL`, formatAccountPoolTime(now.UTC().Add(time.Duration(item.interval)*time.Second)), stamp, item.id); err != nil {
				return err
			}
		}
		if err := trimChannelMonitorHistory(ctx, tx, item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func verifyChannelMonitorDependencies(ctx context.Context, tx *sql.Tx) error {
	// Existing store/account-pool migrations run first. Confirm the exact join
	// columns we depend on before creating a plan that could misattribute runs.
	for _, item := range []struct {
		table   string
		columns []string
	}{
		{"account_channels", []string{"id", "revision"}},
		{"models", []string{"id", "enabled", "archived", "revision"}},
		{"upstreams", []string{"id", "enabled", "archived", "revision"}},
		{"model_account_pool_configs", []string{"model_id", "revision"}},
		{"model_account_pool_routes", []string{"model_id", "upstream_id", "upstream_model", "wire_protocol", "channel_id", "position"}},
	} {
		columns, err := schemaColumns(ctx, tx, item.table)
		if err != nil {
			return err
		}
		for _, name := range item.columns {
			if _, ok := columns[name]; !ok {
				return fmt.Errorf("channel monitor dependency %s is incomplete", item.table)
			}
		}
	}
	return nil
}

func verifyChannelMonitorDDL(ctx context.Context, tx *sql.Tx, kind, name, expected string) error {
	var actual string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type=? AND name=?`, kind, name).Scan(&actual); err != nil {
		return err
	}
	if normalizeHealthDDL(actual) != normalizeHealthDDL(strings.Replace(expected, " IF NOT EXISTS", "", 1)) {
		return fmt.Errorf("incompatible channel monitor %s: %s", kind, name)
	}
	return nil
}

func verifyChannelMonitorObjects(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT type,name FROM sqlite_master WHERE tbl_name IN ('channel_monitor_plans','channel_monitor_runs') AND type IN ('index','trigger')`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var kind, name string
		if err := rows.Scan(&kind, &name); err != nil {
			rows.Close()
			return err
		}
		if kind == "trigger" || !strings.HasPrefix(name, "sqlite_autoindex_channel_monitor_") && channelMonitorIndexes[name] == "" {
			rows.Close()
			return errors.New("unexpected channel monitor schema object")
		}
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	return closeErr
}

func verifyChannelMonitorRows(ctx context.Context, tx *sql.Tx) error {
	var bad int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_plans WHERE
		typeof(revision)<>'integer' OR revision<1 OR typeof(channel_revision)<>'integer' OR channel_revision<1 OR
		typeof(model_revision)<>'integer' OR model_revision<1 OR typeof(pool_revision)<>'integer' OR pool_revision<1 OR
		typeof(upstream_revision)<>'integer' OR upstream_revision<1 OR
		typeof(enabled)<>'integer' OR enabled NOT IN (0,1) OR
		NOT ((enabled=0 AND next_run_at IS NULL) OR (enabled=1 AND next_run_at IS NOT NULL)) OR
		(archived_at IS NOT NULL AND enabled<>0)`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return errors.New("invalid channel monitor plans")
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_runs WHERE
		(state='running' AND (result_code IS NOT NULL OR finished_at IS NOT NULL OR latency_ms IS NOT NULL)) OR
		(state='completed' AND (result_code IS NULL OR finished_at IS NULL OR latency_ms IS NULL)) OR
		(result_code IS NOT NULL AND result_code NOT IN ('local_credential_ok','catalog_ok','authentication_failed','rate_limited','unsupported','timeout','invalid_response','configuration_changed','stale','cancelled','interrupted','test_in_progress','capacity_exceeded','storage_unavailable','internal_failure'))`).Scan(&bad); err != nil {
		return err
	}
	if bad != 0 {
		return errors.New("invalid channel monitor runs")
	}
	planRows, err := tx.QueryContext(ctx, channelMonitorPlanSelect)
	if err != nil {
		return err
	}
	for planRows.Next() {
		if _, err := scanChannelMonitorPlan(planRows); err != nil {
			planRows.Close()
			return err
		}
	}
	iterationErr, closeErr := planRows.Err(), planRows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	runRows, err := tx.QueryContext(ctx, channelMonitorRunSelect)
	if err != nil {
		return err
	}
	for runRows.Next() {
		if _, err := scanChannelMonitorRun(runRows); err != nil {
			runRows.Close()
			return err
		}
	}
	iterationErr, closeErr = runRows.Err(), runRows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func archiveChannelMonitorsForUpstreamTx(ctx context.Context, tx *sql.Tx, upstreamID, adminID string, when time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE channel_monitor_plans SET enabled=0,revision=revision+1,next_run_at=NULL,updated_by_admin_id=?,updated_at=? WHERE upstream_id=? AND archived_at IS NULL`, adminID, formatAccountPoolTime(when.UTC()), upstreamID)
	return err
}
