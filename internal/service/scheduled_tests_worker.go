package service

// Independently implemented from docs/parity-next-batch-2026-09-24.md.
import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"sync"
	"time"
)

type scheduledTestActive struct {
	revision   int64
	upstreamID string
	cancel     context.CancelFunc
}

type scheduledTestCoordinator struct {
	app            *App
	now            func() time.Time
	newOperationID func() (string, error)
	execute        func(context.Context, string, string, int64, string) (upstreamTestOperationView, int, string, error)
	checkUpstream  func(context.Context, string, int64) (bool, error)
	hasArchived    bool
	afterClaim     func()

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}
	mu     sync.Mutex
	active map[string]scheduledTestActive
	closed bool
}

func newScheduledTestCoordinator(ctx context.Context, app *App) (*scheduledTestCoordinator, error) {
	if ctx == nil || app == nil || app.store == nil || app.healthTests == nil {
		return nil, errors.New("scheduled test coordinator is unavailable")
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	c := &scheduledTestCoordinator{
		app: app, now: func() time.Time { return time.Now().UTC() }, newOperationID: newRecoveryOperationID,
		execute: app.healthTests.run, ctx: workerCtx, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1), active: make(map[string]scheduledTestActive),
	}
	columns, err := tableColumns(ctx, app.store.db, "upstreams")
	if err != nil {
		cancel()
		return nil, err
	}
	c.hasArchived = columns["archived"]
	c.checkUpstream = c.upstreamEligible
	migrateCtx, migrateCancel := context.WithTimeout(ctx, 10*time.Second)
	defer migrateCancel()
	if err := migrateScheduledTests(migrateCtx, app.store.db, c.now()); err != nil {
		cancel()
		return nil, err
	}
	if app.cfg.ScheduledTestsEnabled {
		go c.loop()
	} else {
		close(c.done)
	}
	return c, nil
}

func (c *scheduledTestCoordinator) Close() {
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
	c.mu.Unlock()
	<-c.done
}

func (c *scheduledTestCoordinator) notify() {
	if c == nil || !c.app.cfg.ScheduledTestsEnabled {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *scheduledTestCoordinator) cancelPlan(id string, revision int64) {
	c.mu.Lock()
	item, ok := c.active[id]
	if ok && item.revision < revision {
		item.cancel()
	}
	c.mu.Unlock()
	c.notify()
}

func (c *scheduledTestCoordinator) cancelUpstream(upstreamID string) {
	c.mu.Lock()
	for _, item := range c.active {
		if item.upstreamID == upstreamID {
			item.cancel()
		}
	}
	c.mu.Unlock()
	c.notify()
}

func (c *scheduledTestCoordinator) loop() {
	defer close(c.done)
	for {
		for {
			claim, err := c.claimDue(c.ctx)
			if err != nil || claim == nil {
				break
			}
			if c.afterClaim != nil {
				c.afterClaim()
			}
			c.start(*claim)
		}
		delay := c.nextDelay()
		timer := time.NewTimer(delay)
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

func (c *scheduledTestCoordinator) nextDelay() time.Duration {
	var value sql.NullString
	err := c.app.store.db.QueryRowContext(c.ctx, `SELECT MIN(next_run_at) FROM scheduled_test_plans WHERE enabled=1 AND archived_at IS NULL`).Scan(&value)
	if err != nil || !value.Valid {
		return 30 * time.Second
	}
	next, err := parseTime(value.String)
	if err != nil {
		return time.Second
	}
	delay := next.Sub(c.now())
	if delay < 0 {
		return 0
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func (c *scheduledTestCoordinator) claimDue(ctx context.Context) (*scheduledTestClaim, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	operationID, err := c.newOperationID()
	if err != nil {
		return nil, err
	}
	now := c.now().UTC()
	stamp := formatAccountPoolTime(now)
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var running int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM scheduled_test_runs WHERE state='running'`).Scan(&running); err != nil {
		return nil, err
	}
	if running >= scheduledTestMaxRunning {
		return nil, nil
	}
	var claim scheduledTestClaim
	var interval int64
	claimSQL := `SELECT p.id,p.revision,p.upstream_id,u.revision,p.scope,p.interval_seconds
		FROM scheduled_test_plans p JOIN upstreams u ON u.id=p.upstream_id
		WHERE p.enabled=1 AND p.archived_at IS NULL AND p.next_run_at<=? AND u.enabled=1`
	if c.hasArchived {
		claimSQL += ` AND u.archived=0`
	}
	claimSQL += `
		  AND NOT EXISTS(SELECT 1 FROM scheduled_test_runs r WHERE r.upstream_id=p.upstream_id AND r.state='running')
		ORDER BY p.next_run_at,p.id LIMIT 1`
	err = tx.QueryRowContext(ctx, claimSQL, stamp).Scan(&claim.PlanID, &claim.PlanRevision, &claim.UpstreamID, &claim.UpstreamRevision, &claim.Scope, &interval)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	claim.OperationID = operationID
	claim.StartedAt = now
	next := formatAccountPoolTime(now.Add(time.Duration(interval) * time.Second))
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_test_plans SET next_run_at=?,updated_at=? WHERE id=? AND revision=? AND enabled=1 AND archived_at IS NULL AND next_run_at<=?`, next, stamp, claim.PlanID, claim.PlanRevision, stamp)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		if err == nil {
			err = errors.New("scheduled test claim conflict")
		}
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO scheduled_test_runs(plan_id,plan_revision,upstream_id,operation_id,scope,state,result_code,started_at,finished_at,latency_ms,actor) VALUES(?,?,?,?,?,'running',NULL,?,NULL,NULL,'system')`, claim.PlanID, claim.PlanRevision, claim.UpstreamID, claim.OperationID, claim.Scope, stamp); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &claim, nil
}

func (c *scheduledTestCoordinator) start(claim scheduledTestClaim) {
	ctx, cancel := context.WithCancel(c.ctx)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancel()
		finalCtx, finalCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer finalCancel()
		_ = c.finalize(finalCtx, claim, "interrupted", c.now().UTC(), 0)
		return
	}
	c.active[claim.PlanID] = scheduledTestActive{revision: claim.PlanRevision, upstreamID: claim.UpstreamID, cancel: cancel}
	c.mu.Unlock()
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.active, claim.PlanID)
			c.mu.Unlock()
			cancel()
			c.notify()
		}()
		c.executeClaim(ctx, claim)
	}()
}

func (c *scheduledTestCoordinator) executeClaim(ctx context.Context, claim scheduledTestClaim) {
	resultCode := "internal_failure"
	eligible, eligibilityErr := c.checkUpstream(ctx, claim.UpstreamID, claim.UpstreamRevision)
	if eligibilityErr != nil {
		resultCode = "storage_unavailable"
		c.finishClaim(claim, resultCode)
		return
	}
	if !eligible {
		resultCode = "configuration_changed"
		c.finishClaim(claim, resultCode)
		return
	}
	view, status, code, err := c.execute(ctx, claim.UpstreamID, claim.OperationID, claim.UpstreamRevision, claim.Scope)
	if err != nil {
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
	} else if ctx.Err() != nil {
		resultCode = "cancelled"
	}
	c.finishClaim(claim, resultCode)
}

func (c *scheduledTestCoordinator) finishClaim(claim scheduledTestClaim, resultCode string) {
	finished := c.now().UTC()
	latency := finished.Sub(claim.StartedAt).Milliseconds()
	if latency < 0 {
		latency = 0
	}
	finalCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = c.finalize(finalCtx, claim, resultCode, finished, latency)
}

func (c *scheduledTestCoordinator) upstreamEligible(ctx context.Context, id string, expectedRevision int64) (bool, error) {
	var enabled int
	var revision int64
	query := `SELECT enabled,revision FROM upstreams WHERE id=?`
	if c.hasArchived {
		query += ` AND archived=0`
	}
	if err := c.app.store.db.QueryRowContext(ctx, query, id).Scan(&enabled, &revision); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return enabled == 1 && revision == expectedRevision, nil
}

func (c *scheduledTestCoordinator) finalize(ctx context.Context, claim scheduledTestClaim, resultCode string, finished time.Time, latency int64) error {
	if !scheduledTestResultCode(resultCode) {
		resultCode = "internal_failure"
	}
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_test_runs SET state='completed',result_code=?,finished_at=?,latency_ms=? WHERE plan_id=? AND plan_revision=? AND operation_id=? AND state='running'`, resultCode, formatAccountPoolTime(finished), latency, claim.PlanID, claim.PlanRevision, claim.OperationID)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		if err == nil {
			err = errors.New("scheduled test finalize conflict")
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM scheduled_test_runs WHERE plan_id=? AND state='completed' AND sequence NOT IN (SELECT sequence FROM scheduled_test_runs WHERE plan_id=? AND state='completed' ORDER BY sequence DESC LIMIT ?)`, claim.PlanID, claim.PlanID, scheduledTestMaxHistory); err != nil {
		return err
	}
	return tx.Commit()
}

func scheduledTestResultCode(value string) bool {
	switch value {
	case "local_credential_ok", "catalog_ok", "authentication_failed", "rate_limited", "unsupported", "timeout", "invalid_response", "configuration_changed", "stale", "cancelled", "interrupted", "test_in_progress", "capacity_exceeded", "storage_unavailable", "internal_failure":
		return true
	default:
		return false
	}
}

func (c *scheduledTestCoordinator) waitActive() {
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
