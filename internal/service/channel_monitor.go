package service

// Independently authored from docs/channel-monitor-contract.md.
import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type channelMonitorBinding struct {
	ChannelRevision    int64
	ModelRevision      int64
	PoolRevision       int64
	UpstreamRevision   int64
	RouteUpstreamModel string
	RouteWireProtocol  string
	RoutePosition      int64
}

type channelMonitorPlan struct {
	ID              string                `json:"id"`
	Name            string                `json:"name"`
	ChannelID       string                `json:"channel_id"`
	ModelID         string                `json:"model_id"`
	UpstreamID      string                `json:"upstream_id"`
	Scope           string                `json:"scope"`
	IntervalSeconds int64                 `json:"interval_seconds"`
	Enabled         bool                  `json:"enabled"`
	Revision        int64                 `json:"revision"`
	NextRunAt       *string               `json:"next_run_at"`
	CreatedAt       string                `json:"created_at"`
	UpdatedAt       string                `json:"updated_at"`
	ArchivedAt      *string               `json:"archived_at,omitempty"`
	BindingState    string                `json:"binding_state"`
	LatestResult    *channelMonitorRun    `json:"latest_result"`
	Binding         channelMonitorBinding `json:"-"`
}

type channelMonitorRun struct {
	Sequence           int64   `json:"-"`
	PlanRevision       int64   `json:"plan_revision"`
	ChannelID          string  `json:"channel_id"`
	ChannelRevision    int64   `json:"channel_revision"`
	ModelID            string  `json:"model_id"`
	ModelRevision      int64   `json:"model_revision"`
	PoolRevision       int64   `json:"pool_revision"`
	UpstreamID         string  `json:"upstream_id"`
	UpstreamRevision   int64   `json:"upstream_revision"`
	RouteUpstreamModel string  `json:"route_upstream_model"`
	RouteWireProtocol  string  `json:"route_wire_protocol"`
	RoutePosition      int64   `json:"route_position"`
	OperationID        string  `json:"operation_id"`
	Scope              string  `json:"scope"`
	State              string  `json:"state"`
	ResultCode         *string `json:"result_code"`
	StartedAt          string  `json:"started_at"`
	FinishedAt         *string `json:"finished_at"`
	LatencyMS          *int64  `json:"latency_ms"`
}

const channelMonitorPlanSelect = `SELECT id,name,channel_id,model_id,upstream_id,scope,interval_seconds,enabled,revision,next_run_at,channel_revision,model_revision,pool_revision,upstream_revision,route_upstream_model,route_wire_protocol,route_position,created_at,updated_at,archived_at FROM channel_monitor_plans`

const channelMonitorRunSelect = `SELECT sequence,plan_revision,channel_id,channel_revision,model_id,model_revision,pool_revision,upstream_id,upstream_revision,route_upstream_model,route_wire_protocol,route_position,operation_id,scope,state,result_code,started_at,finished_at,latency_ms FROM channel_monitor_runs`

func scanChannelMonitorPlan(scanner interface{ Scan(...any) error }) (channelMonitorPlan, error) {
	var item channelMonitorPlan
	var enabled int
	var next, archived sql.NullString
	err := scanner.Scan(&item.ID, &item.Name, &item.ChannelID, &item.ModelID, &item.UpstreamID, &item.Scope, &item.IntervalSeconds, &enabled, &item.Revision, &next,
		&item.Binding.ChannelRevision, &item.Binding.ModelRevision, &item.Binding.PoolRevision, &item.Binding.UpstreamRevision,
		&item.Binding.RouteUpstreamModel, &item.Binding.RouteWireProtocol, &item.Binding.RoutePosition, &item.CreatedAt, &item.UpdatedAt, &archived)
	if err != nil {
		return channelMonitorPlan{}, err
	}
	item.Enabled = enabled == 1
	if next.Valid {
		item.NextRunAt = &next.String
	}
	if archived.Valid {
		item.ArchivedAt = &archived.String
	}
	if enabled != 0 && enabled != 1 || item.Enabled != (item.NextRunAt != nil) || item.Revision < 1 || item.IntervalSeconds < channelMonitorMinInterval || item.IntervalSeconds > channelMonitorMaxInterval || !validScheduledTestScope(item.Scope) || !validText(item.Name, 1, 120) || item.Binding.ChannelRevision < 1 || item.Binding.ModelRevision < 1 || item.Binding.PoolRevision < 1 || item.Binding.UpstreamRevision < 1 || item.Binding.RoutePosition < 0 || item.Binding.RoutePosition > 63 || item.Binding.RouteUpstreamModel == "" || item.Binding.RouteWireProtocol == "" {
		return channelMonitorPlan{}, errors.New("invalid channel monitor plan")
	}
	for _, stamp := range []*string{item.NextRunAt, item.ArchivedAt, &item.CreatedAt, &item.UpdatedAt} {
		if stamp != nil {
			if _, err := parseTime(*stamp); err != nil {
				return channelMonitorPlan{}, err
			}
		}
	}
	return item, nil
}

func loadChannelMonitorPlan(ctx context.Context, query queryRower, id string, includeArchived bool) (channelMonitorPlan, error) {
	sqlText := channelMonitorPlanSelect + ` WHERE id=?`
	if !includeArchived {
		sqlText += ` AND archived_at IS NULL`
	}
	return scanChannelMonitorPlan(query.QueryRowContext(ctx, sqlText, id))
}

func scanChannelMonitorRun(scanner interface{ Scan(...any) error }) (channelMonitorRun, error) {
	var item channelMonitorRun
	var result, finished sql.NullString
	var latency sql.NullInt64
	err := scanner.Scan(&item.Sequence, &item.PlanRevision, &item.ChannelID, &item.ChannelRevision, &item.ModelID, &item.ModelRevision, &item.PoolRevision,
		&item.UpstreamID, &item.UpstreamRevision, &item.RouteUpstreamModel, &item.RouteWireProtocol, &item.RoutePosition, &item.OperationID,
		&item.Scope, &item.State, &result, &item.StartedAt, &finished, &latency)
	if err != nil {
		return channelMonitorRun{}, err
	}
	if result.Valid {
		item.ResultCode = &result.String
	}
	if finished.Valid {
		item.FinishedAt = &finished.String
	}
	if latency.Valid {
		item.LatencyMS = &latency.Int64
	}
	if item.PlanRevision < 1 || item.ChannelRevision < 1 || item.ModelRevision < 1 || item.PoolRevision < 1 || item.UpstreamRevision < 1 || item.RoutePosition < 0 || item.RoutePosition > 63 || !validScheduledTestScope(item.Scope) || (item.State != "running" && item.State != "completed") || item.State == "completed" && (item.ResultCode == nil || !scheduledTestResultCode(*item.ResultCode) || item.FinishedAt == nil || item.LatencyMS == nil) || item.State == "running" && (item.ResultCode != nil || item.FinishedAt != nil || item.LatencyMS != nil) {
		return channelMonitorRun{}, errors.New("invalid channel monitor run")
	}
	if _, err := parseTime(item.StartedAt); err != nil {
		return channelMonitorRun{}, err
	}
	if item.FinishedAt != nil {
		if _, err := parseTime(*item.FinishedAt); err != nil {
			return channelMonitorRun{}, err
		}
	}
	return item, nil
}

// captureChannelMonitorBinding never chooses an alternative account. A route
// exists only in an explicitly configured pool, not the legacy model fallback.
func captureChannelMonitorBinding(ctx context.Context, query queryRower, channelID, modelID, upstreamID string) (channelMonitorBinding, bool, error) {
	var result channelMonitorBinding
	var modelEnabled, modelArchived, upstreamEnabled, upstreamArchived int
	err := query.QueryRowContext(ctx, `SELECT c.revision,m.revision,p.revision,u.revision,r.upstream_model,r.wire_protocol,r.position,m.enabled,m.archived,u.enabled,u.archived
		FROM account_channels c JOIN models m ON m.id=? JOIN model_account_pool_configs p ON p.model_id=m.id
		JOIN model_account_pool_routes r ON r.model_id=p.model_id AND r.upstream_id=? AND r.channel_id=c.id
		JOIN upstreams u ON u.id=r.upstream_id WHERE c.id=?`, modelID, upstreamID, channelID).Scan(
		&result.ChannelRevision, &result.ModelRevision, &result.PoolRevision, &result.UpstreamRevision, &result.RouteUpstreamModel, &result.RouteWireProtocol, &result.RoutePosition,
		&modelEnabled, &modelArchived, &upstreamEnabled, &upstreamArchived)
	if errors.Is(err, sql.ErrNoRows) {
		return channelMonitorBinding{}, false, nil
	}
	if err != nil {
		return channelMonitorBinding{}, false, err
	}
	if modelEnabled != 1 || modelArchived != 0 || upstreamEnabled != 1 || upstreamArchived != 0 || result.ChannelRevision < 1 || result.ModelRevision < 1 || result.PoolRevision < 1 || result.UpstreamRevision < 1 {
		return channelMonitorBinding{}, false, nil
	}
	return result, true, nil
}

func channelMonitorBindingValid(ctx context.Context, query queryRower, plan channelMonitorPlan) (bool, error) {
	current, exists, err := captureChannelMonitorBinding(ctx, query, plan.ChannelID, plan.ModelID, plan.UpstreamID)
	if err != nil || !exists {
		return false, err
	}
	return current == plan.Binding, nil
}

func decorateChannelMonitorPlan(ctx context.Context, db *sql.DB, plan *channelMonitorPlan) error {
	valid, err := channelMonitorBindingValid(ctx, db, *plan)
	if err != nil {
		return err
	}
	plan.BindingState = "stale"
	if !valid {
		return nil
	}
	plan.BindingState = "valid"
	run, err := scanChannelMonitorRun(db.QueryRowContext(ctx, channelMonitorRunSelect+` WHERE plan_id=? AND plan_revision=? ORDER BY sequence DESC LIMIT 1`, plan.ID, plan.Revision))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	plan.LatestResult = &run
	return nil
}

func channelMonitorNextRun(now time.Time, interval int64) string {
	return formatAccountPoolTime(now.UTC().Add(time.Duration(interval) * time.Second))
}
