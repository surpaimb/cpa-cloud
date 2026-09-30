package service

// Independently authored worker for docs/channel-monitor-contract.md. The only
// provider action is the existing bounded upstream-health coordinator.
import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"
)

type channelMonitorClaim struct {
	Plan        channelMonitorPlan
	OperationID string
	StartedAt   time.Time
}

type channelMonitorActive struct {
	revision   int64
	modelID    string
	upstreamID string
	cancel     context.CancelFunc
}

type channelMonitorCoordinator struct {
	app            *App
	now            func() time.Time
	newOperationID func() (string, error)
	execute        func(context.Context, string, string, int64, string) (upstreamTestOperationView, int, string, error)
	ctx            context.Context
	cancel         context.CancelFunc
	done           chan struct{}
	wake           chan struct{}
	mu             sync.Mutex
	active         map[string]channelMonitorActive
	started        bool
	closed         bool
}

func newChannelMonitorCoordinator(ctx context.Context, app *App) (*channelMonitorCoordinator, error) {
	if ctx == nil || app == nil || app.store == nil || app.healthTests == nil {
		return nil, errors.New("channel monitor coordinator unavailable")
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	c := &channelMonitorCoordinator{app: app, now: func() time.Time { return time.Now().UTC() }, newOperationID: newRecoveryOperationID, execute: app.healthTests.run, ctx: workerCtx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), active: make(map[string]channelMonitorActive)}
	migrateCtx, migrateCancel := context.WithTimeout(ctx, 10*time.Second)
	defer migrateCancel()
	if err := migrateChannelMonitors(migrateCtx, app.store.db, c.now()); err != nil {
		cancel()
		return nil, err
	}
	if !app.cfg.ChannelMonitorsEnabled {
		close(c.done)
	}
	return c, nil
}

func (c *channelMonitorCoordinator) Start() error {
	if c == nil || !c.app.cfg.ChannelMonitorsEnabled {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("channel monitor coordinator closed")
	}
	if c.started {
		return nil
	}
	c.started = true
	go c.loop()
	return nil
}

func (c *channelMonitorCoordinator) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.cancel()
	for _, item := range c.active {
		item.cancel()
	}
	if !c.started && c.app.cfg.ChannelMonitorsEnabled {
		close(c.done)
	}
	c.mu.Unlock()
	<-c.done
}

func (c *channelMonitorCoordinator) notify() {
	if c == nil || !c.app.cfg.ChannelMonitorsEnabled {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *channelMonitorCoordinator) cancelPlan(id string, revision int64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	if item, ok := c.active[id]; ok && item.revision < revision {
		item.cancel()
	}
	c.mu.Unlock()
	c.notify()
}

func (c *channelMonitorCoordinator) cancelModel(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	for _, item := range c.active {
		if item.modelID == id {
			item.cancel()
		}
	}
	c.mu.Unlock()
	c.notify()
}

func (c *channelMonitorCoordinator) cancelUpstream(id string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	for _, item := range c.active {
		if item.upstreamID == id {
			item.cancel()
		}
	}
	c.mu.Unlock()
	c.notify()
}

func (c *channelMonitorCoordinator) loop() {
	defer close(c.done)
	for {
		for {
			claim, err := c.claimDue(c.ctx)
			if err != nil || claim == nil {
				break
			}
			c.start(*claim)
		}
		timer := time.NewTimer(c.nextDelay())
		select {
		case <-c.ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			c.waitActive()
			return
		case <-c.wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
	}
}

func (c *channelMonitorCoordinator) nextDelay() time.Duration {
	var next sql.NullString
	if err := c.app.store.db.QueryRowContext(c.ctx, `SELECT MIN(next_run_at) FROM channel_monitor_plans WHERE enabled=1 AND archived_at IS NULL`).Scan(&next); err != nil || !next.Valid {
		return 30 * time.Second
	}
	when, err := parseTime(next.String)
	if err != nil {
		return time.Second
	}
	delay := when.Sub(c.now())
	if delay < time.Second {
		return time.Second
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func (c *channelMonitorCoordinator) claimDue(ctx context.Context) (*channelMonitorClaim, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := c.now().UTC()
	stamp := formatAccountPoolTime(now)
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var running int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_monitor_runs WHERE state='running'`).Scan(&running); err != nil {
		return nil, err
	}
	if running >= channelMonitorMaxRunning {
		return nil, nil
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT p.id FROM channel_monitor_plans p WHERE p.enabled=1 AND p.archived_at IS NULL AND p.next_run_at<=?
		AND NOT EXISTS(SELECT 1 FROM channel_monitor_runs r WHERE r.upstream_id=p.upstream_id AND r.state='running')
		ORDER BY p.next_run_at,p.id LIMIT 1`, stamp).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plan, err := loadChannelMonitorPlan(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	valid, err := channelMonitorBindingValid(ctx, tx, plan)
	if err != nil {
		return nil, err
	}
	operationID, err := c.newOperationID()
	if err != nil {
		return nil, err
	}
	if !valid {
		result, err := tx.ExecContext(ctx, `UPDATE channel_monitor_plans SET enabled=0,revision=revision+1,next_run_at=NULL,updated_at=? WHERE id=? AND revision=? AND enabled=1 AND next_run_at<=?`, stamp, plan.ID, plan.Revision, stamp)
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return nil, err
		}
		if changed != 1 {
			return nil, nil
		}
		if err := insertChannelMonitorRun(ctx, tx, plan, operationID, stamp, true, "configuration_changed"); err != nil {
			return nil, err
		}
		if err := trimChannelMonitorHistory(ctx, tx, plan.ID); err != nil {
			return nil, err
		}
		return nil, tx.Commit()
	}
	result, err := tx.ExecContext(ctx, `UPDATE channel_monitor_plans SET next_run_at=?,updated_at=? WHERE id=? AND revision=? AND enabled=1 AND next_run_at<=?`, channelMonitorNextRun(now, plan.IntervalSeconds), stamp, plan.ID, plan.Revision, stamp)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, nil
	}
	if err := insertChannelMonitorRun(ctx, tx, plan, operationID, stamp, false, ""); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &channelMonitorClaim{Plan: plan, OperationID: operationID, StartedAt: now}, nil
}

func insertChannelMonitorRun(ctx context.Context, tx *sql.Tx, plan channelMonitorPlan, operationID, stamp string, completed bool, code string) error {
	state := "running"
	var resultCode, finished, latency any
	if completed {
		state, resultCode, finished, latency = "completed", code, stamp, int64(0)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO channel_monitor_runs(plan_id,plan_revision,channel_id,channel_revision,model_id,model_revision,pool_revision,upstream_id,upstream_revision,route_upstream_model,route_wire_protocol,route_position,operation_id,scope,state,result_code,started_at,finished_at,latency_ms,actor) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'system')`,
		plan.ID, plan.Revision, plan.ChannelID, plan.Binding.ChannelRevision, plan.ModelID, plan.Binding.ModelRevision, plan.Binding.PoolRevision, plan.UpstreamID, plan.Binding.UpstreamRevision, plan.Binding.RouteUpstreamModel, plan.Binding.RouteWireProtocol, plan.Binding.RoutePosition, operationID, plan.Scope, state, resultCode, stamp, finished, latency)
	return err
}

func trimChannelMonitorHistory(ctx context.Context, tx *sql.Tx, planID string) error {
	_, err := tx.ExecContext(ctx, `DELETE FROM channel_monitor_runs WHERE plan_id=? AND state='completed' AND sequence NOT IN (SELECT sequence FROM channel_monitor_runs WHERE plan_id=? AND state='completed' ORDER BY sequence DESC LIMIT ?)`, planID, planID, channelMonitorMaxHistory)
	return err
}

func (c *channelMonitorCoordinator) start(claim channelMonitorClaim) {
	ctx, cancel := context.WithCancel(c.ctx)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		finalCtx, done := context.WithTimeout(context.Background(), 3*time.Second)
		defer done()
		_ = c.finalize(finalCtx, claim, "interrupted", c.now().UTC(), 0)
		return
	}
	c.active[claim.Plan.ID] = channelMonitorActive{revision: claim.Plan.Revision, modelID: claim.Plan.ModelID, upstreamID: claim.Plan.UpstreamID, cancel: cancel}
	c.mu.Unlock()
	go func() {
		defer func() { c.mu.Lock(); delete(c.active, claim.Plan.ID); c.mu.Unlock(); cancel(); c.notify() }()
		c.executeClaim(ctx, claim)
	}()
}

func (c *channelMonitorCoordinator) executeClaim(ctx context.Context, claim channelMonitorClaim) {
	valid, err := c.claimStillValid(ctx, claim)
	if err != nil {
		if ctx.Err() != nil {
			c.finishClaim(claim, "cancelled")
		} else {
			c.finishClaim(claim, "storage_unavailable")
		}
		return
	}
	if !valid {
		c.finishClaim(claim, "configuration_changed")
		return
	}
	view, status, code, err := c.execute(ctx, claim.Plan.UpstreamID, claim.OperationID, claim.Plan.Binding.UpstreamRevision, claim.Plan.Scope)
	resultCode := "internal_failure"
	if ctx.Err() != nil {
		resultCode = "cancelled"
	} else if err != nil {
		resultCode = "storage_unavailable"
	} else if code != "" {
		switch code {
		case "test_in_progress":
			resultCode = "test_in_progress"
		case "test_capacity_exceeded":
			resultCode = "capacity_exceeded"
		case "revision_conflict", "not_found":
			resultCode = "configuration_changed"
		default:
			resultCode = "internal_failure"
		}
	} else if status == http.StatusOK && view.ResultCode != nil {
		resultCode = *view.ResultCode
	}
	c.finishClaim(claim, resultCode)
}

func (c *channelMonitorCoordinator) claimStillValid(ctx context.Context, claim channelMonitorClaim) (bool, error) {
	current, err := loadChannelMonitorPlan(ctx, c.app.store.db, claim.Plan.ID, false)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.Revision != claim.Plan.Revision || !current.Enabled || current.ChannelID != claim.Plan.ChannelID || current.ModelID != claim.Plan.ModelID || current.UpstreamID != claim.Plan.UpstreamID {
		return false, nil
	}
	return channelMonitorBindingValid(ctx, c.app.store.db, current)
}

func (c *channelMonitorCoordinator) finishClaim(claim channelMonitorClaim, resultCode string) {
	finished := c.now().UTC()
	latency := finished.Sub(claim.StartedAt).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	ctx, done := context.WithTimeout(context.Background(), 3*time.Second)
	defer done()
	_ = c.finalize(ctx, claim, resultCode, finished, latency)
}

func (c *channelMonitorCoordinator) finalize(ctx context.Context, claim channelMonitorClaim, resultCode string, finished time.Time, latency int64) error {
	if !scheduledTestResultCode(resultCode) {
		resultCode = "internal_failure"
	}
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := loadChannelMonitorPlan(ctx, tx, claim.Plan.ID, true)
	if err != nil {
		return err
	}
	valid := current.Revision == claim.Plan.Revision && current.Enabled && current.ArchivedAt == nil && current.ChannelID == claim.Plan.ChannelID && current.ModelID == claim.Plan.ModelID && current.UpstreamID == claim.Plan.UpstreamID
	if valid {
		valid, err = channelMonitorBindingValid(ctx, tx, current)
		if err != nil {
			return err
		}
	}
	if !valid {
		resultCode = "configuration_changed"
		if current.Revision == claim.Plan.Revision && current.Enabled && current.ArchivedAt == nil {
			_, err = tx.ExecContext(ctx, `UPDATE channel_monitor_plans SET enabled=0,revision=revision+1,next_run_at=NULL,updated_at=? WHERE id=? AND revision=?`, formatAccountPoolTime(finished), current.ID, current.Revision)
			if err != nil {
				return err
			}
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE channel_monitor_runs SET state='completed',result_code=?,finished_at=?,latency_ms=? WHERE plan_id=? AND plan_revision=? AND operation_id=? AND state='running'`, resultCode, formatAccountPoolTime(finished), latency, claim.Plan.ID, claim.Plan.Revision, claim.OperationID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("channel monitor finalize conflict")
	}
	if err := trimChannelMonitorHistory(ctx, tx, claim.Plan.ID); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *channelMonitorCoordinator) waitActive() {
	for {
		c.mu.Lock()
		count := len(c.active)
		c.mu.Unlock()
		if count == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
