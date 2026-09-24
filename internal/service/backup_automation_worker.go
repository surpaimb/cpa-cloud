package service

// Independently implemented from docs/adr/0002-automated-backup-key-custody.md.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/keyprovider"
)

type BackupAutomationConfig struct {
	Enabled                bool
	DataDir                string
	OutputRoot             string
	ProviderStoreRoot      string
	SourceVersion          string
	PrepareRehearsalConfig func(*Config)
}

type backupPreparedKeyVersion interface {
	Commit()
	Rollback() error
}

type backupKeyStore interface {
	Ready() (bool, string)
	PrepareVersion(context.Context, string, uint64) (backupPreparedKeyVersion, error)
	Resolve(context.Context, string, uint64) (*keyprovider.Material, error)
	DiscardVersion(context.Context, string, uint64) error
}

type systemBackupKeyStore struct{ store *keyprovider.Store }

func (s systemBackupKeyStore) Ready() (bool, string) { return s.store.Ready() }

func (s systemBackupKeyStore) PrepareVersion(ctx context.Context, id string, version uint64) (backupPreparedKeyVersion, error) {
	return s.store.PrepareVersion(ctx, id, version)
}

func (s systemBackupKeyStore) Resolve(ctx context.Context, id string, version uint64) (*keyprovider.Material, error) {
	return s.store.Resolve(ctx, id, version)
}

func (s systemBackupKeyStore) DiscardVersion(ctx context.Context, id string, version uint64) error {
	return s.store.DiscardVersion(ctx, id, version)
}

type backupAutomationClaim struct {
	RunID            string
	PlanID           string
	PlanRevision     int64
	ProviderID       string
	ProviderKind     string
	ProviderVersion  int64
	TriggerKind      string
	RequestedByAdmin *string
	RetentionCount   int64
	RehearsalEnabled bool
	ScheduledFor     time.Time
	StartedAt        time.Time
	PackageName      string
}

type backupAutomationCoordinator struct {
	app        *App
	cfg        BackupAutomationConfig
	output     *backupOutputRoot
	keys       backupKeyStore
	ctx        context.Context
	cancel     context.CancelFunc
	now        func() time.Time
	wake       chan struct{}
	mu         sync.Mutex
	providerMu sync.Mutex
	started    bool
	closed     bool
	activeID   string
	activeEnd  context.CancelFunc
	wg         sync.WaitGroup

	create    func(context.Context, string, string, backup.KeyMaterial, string) (backup.Info, error)
	verify    func(context.Context, string, backup.KeyMaterial) (backup.Info, error)
	restore   func(context.Context, string, string, backup.KeyMaterial) (backup.Info, error)
	rehearse  func(context.Context, string) error
	commitRun func(*sql.Tx) error
}

func newBackupAutomationCoordinator(ctx context.Context, app *App, cfg BackupAutomationConfig) (*backupAutomationCoordinator, error) {
	if app == nil || app.store == nil || app.store.db == nil {
		return nil, errors.New("backup automation requires an open application store")
	}
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil || cfg.DataDir == "" {
		return nil, errors.New("backup automation data directory is invalid")
	}
	outputPath, err := filepath.Abs(cfg.OutputRoot)
	if err != nil || cfg.OutputRoot == "" {
		return nil, errors.New("backup automation output root is invalid")
	}
	providerPath, err := filepath.Abs(cfg.ProviderStoreRoot)
	if err != nil || cfg.ProviderStoreRoot == "" {
		return nil, errors.New("backup key provider store root is invalid")
	}
	dataDir, outputPath, providerPath = filepath.Clean(dataDir), filepath.Clean(outputPath), filepath.Clean(providerPath)
	if backupPathsOverlap(dataDir, outputPath) || backupPathsOverlap(dataDir, providerPath) || backupPathsOverlap(outputPath, providerPath) {
		return nil, errors.New("backup data, output, and key-provider roots must not overlap")
	}
	output, err := openBackupOutputRoot(outputPath)
	if err != nil {
		return nil, err
	}
	keys, err := keyprovider.Open(providerPath)
	if err != nil {
		return nil, err
	}
	workerCtx, cancel := context.WithCancel(ctx)
	c := &backupAutomationCoordinator{
		app: app, cfg: cfg, output: output, keys: systemBackupKeyStore{store: keys}, ctx: workerCtx, cancel: cancel,
		now: time.Now, wake: make(chan struct{}, 1),
		create: backup.CreateWithKeyMaterial, verify: backup.VerifyWithKeyMaterial, restore: backup.RestoreWithKeyMaterial,
		commitRun: func(tx *sql.Tx) error { return tx.Commit() },
	}
	c.rehearse = func(rehearsalCtx context.Context, restoredDataDir string) error {
		return validateBackupRehearsal(rehearsalCtx, app.cfg, restoredDataDir, cfg.PrepareRehearsalConfig)
	}
	if cfg.Enabled && cfg.PrepareRehearsalConfig == nil {
		cancel()
		return nil, errors.New("backup automation requires a rehearsal configuration guard")
	}
	if err := c.recoverKeyProviderOrphans(ctx); err != nil && cfg.Enabled {
		cancel()
		return nil, err
	}
	if err := c.recoverUnretainedPackages(ctx); err != nil && cfg.Enabled {
		cancel()
		return nil, err
	}
	return c, nil
}

func (c *backupAutomationCoordinator) recoverUnretainedPackages(ctx context.Context) error {
	rows, err := c.app.store.db.QueryContext(ctx, `SELECT id,plan_id,package_name FROM backup_runs WHERE status!='running' AND package_retained=0 AND package_deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return err
	}
	type candidate struct{ id, planID, name string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.planID, &item.name); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range candidates {
		if err := c.output.removeOwnedPackage(item.planID, item.id, item.name); err != nil {
			return err
		}
		result, err := c.app.store.db.ExecContext(ctx, `UPDATE backup_runs SET package_deleted_at=? WHERE id=? AND plan_id=? AND package_name=? AND status!='running' AND package_retained=0 AND package_deleted_at IS NULL`, formatAccountPoolTime(c.now().UTC()), item.id, item.planID, item.name)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("backup package cleanup state conflict")
		}
	}
	return nil
}

func (c *backupAutomationCoordinator) recoverKeyProviderOrphans(ctx context.Context) error {
	c.providerMu.Lock()
	defer c.providerMu.Unlock()
	return c.recoverKeyProviderOrphansLocked(ctx)
}

func (c *backupAutomationCoordinator) recoverKeyProviderOrphansLocked(ctx context.Context) error {
	ready, _ := c.keys.Ready()
	if !ready {
		return nil
	}
	rows, err := c.app.store.db.QueryContext(ctx, `SELECT id,status,reason_code,active_version FROM backup_key_providers WHERE status='degraded' AND reason_code IN ('provisioning_interrupted','rotation_interrupted') ORDER BY id`)
	if err != nil {
		return err
	}
	type candidate struct {
		id, status string
		reason     sql.NullString
		active     int64
	}
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.id, &item.status, &item.reason, &item.active); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range candidates {
		version := item.active
		if item.reason.String == "rotation_interrupted" {
			if version >= backupMaxRevision {
				return errors.New("interrupted backup key rotation has an invalid version")
			}
			version++
			var references int
			if err := c.app.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM backup_runs WHERE key_provider_id=? AND key_provider_version=?`, item.id, version).Scan(&references); err != nil {
				return err
			}
			if references != 0 {
				continue
			}
		}
		material, err := c.keys.Resolve(ctx, item.id, uint64(version))
		if errors.Is(err, os.ErrNotExist) {
			if item.reason.String == "provisioning_interrupted" {
				if _, err := c.app.store.db.ExecContext(ctx, `DELETE FROM backup_key_providers WHERE id=? AND status='degraded' AND reason_code='provisioning_interrupted'`, item.id); err != nil {
					return err
				}
			} else {
				if _, err := c.app.store.db.ExecContext(ctx, `UPDATE backup_key_providers SET status='ready',reason_code=NULL WHERE id=? AND status='degraded' AND reason_code='rotation_interrupted'`, item.id); err != nil {
					return err
				}
			}
			continue
		}
		if err != nil {
			return err
		}
		material.Destroy()
		if err := c.keys.DiscardVersion(ctx, item.id, uint64(version)); err != nil {
			return err
		}
		if item.reason.String == "provisioning_interrupted" {
			if _, err := c.app.store.db.ExecContext(ctx, `DELETE FROM backup_key_providers WHERE id=? AND status='degraded' AND reason_code='provisioning_interrupted'`, item.id); err != nil {
				return err
			}
		} else {
			if _, err := c.app.store.db.ExecContext(ctx, `UPDATE backup_key_providers SET status='ready',reason_code=NULL WHERE id=? AND status='degraded' AND reason_code='rotation_interrupted'`, item.id); err != nil {
				return err
			}
		}
	}
	return nil
}

func backupPathsOverlap(left, right string) bool {
	if sameBackupPath(left, right) {
		return true
	}
	leftToRight, leftErr := filepath.Rel(left, right)
	rightToLeft, rightErr := filepath.Rel(right, left)
	inside := func(relative string, err error) bool {
		return err == nil && relative != "." && relative != ".." && !stringsHasParentPrefix(relative)
	}
	return inside(leftToRight, leftErr) || inside(rightToLeft, rightErr)
}

func stringsHasParentPrefix(value string) bool {
	return len(value) > 3 && value[:3] == ".."+string(os.PathSeparator)
}

func (c *backupAutomationCoordinator) Start() error {
	if c == nil {
		return errors.New("backup automation coordinator is unavailable")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("backup automation coordinator is closed")
	}
	if c.started {
		return nil
	}
	c.started = true
	if !c.cfg.Enabled {
		return nil
	}
	c.wg.Add(1)
	go c.loop()
	c.notify()
	return nil
}

func (c *backupAutomationCoordinator) Close() {
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
	if c.activeEnd != nil {
		c.activeEnd()
	}
	c.mu.Unlock()
	c.wg.Wait()
}

func (c *backupAutomationCoordinator) Ready() bool {
	if c == nil || c.keys == nil {
		return false
	}
	if ready, _ := c.keys.Ready(); !ready {
		return false
	}
	rows, err := c.app.store.db.QueryContext(context.Background(), `SELECT id,active_version FROM backup_key_providers WHERE status='ready' ORDER BY id`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var version int64
		if err := rows.Scan(&id, &version); err != nil {
			return false
		}
		material, err := c.keys.Resolve(context.Background(), id, uint64(version))
		if err == nil {
			material.Destroy()
			return true
		}
	}
	return false
}

func (c *backupAutomationCoordinator) Running() bool {
	if c == nil || !c.cfg.Enabled || !c.Ready() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started && !c.closed
}

func (c *backupAutomationCoordinator) notify() {
	if c == nil {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

func (c *backupAutomationCoordinator) loop() {
	defer c.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		case <-ticker.C:
		}
		claim, err := c.claimDue(c.ctx)
		if err == nil && claim != nil {
			c.run(*claim)
		}
	}
}

func (c *backupAutomationCoordinator) claimDue(ctx context.Context) (*backupAutomationClaim, error) {
	return c.claim(ctx, "", 0, "", false)
}

func (c *backupAutomationCoordinator) claimPlan(ctx context.Context, planID string, expectedRevision int64, adminID string) (*backupAutomationClaim, error) {
	if adminID == "" {
		return nil, errors.New("manual backup claim requires an administrator")
	}
	return c.claim(ctx, planID, expectedRevision, adminID, true)
}

func (c *backupAutomationCoordinator) claim(ctx context.Context, requestedPlan string, expectedRevision int64, requestedAdmin string, manual bool) (*backupAutomationClaim, error) {
	if !c.cfg.Enabled {
		return nil, errors.New("backup_worker_disabled")
	}
	now := c.now().UTC()
	runID, err := newID("brn")
	if err != nil {
		return nil, err
	}
	conn, err := c.app.store.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	claim := backupAutomationClaim{RunID: runID, StartedAt: now, TriggerKind: "scheduled"}
	if manual {
		claim.TriggerKind = "manual"
		claim.RequestedByAdmin = &requestedAdmin
	}
	var interval int64
	var rehearsal int
	var scheduled sql.NullString
	query := `SELECT p.id,p.revision,p.interval_seconds,p.retention_count,p.rehearsal_enabled,p.next_run_at,k.id,k.kind,k.active_version
		FROM backup_plans p JOIN backup_key_providers k ON k.id=p.key_provider_id
		WHERE k.status='ready' AND NOT EXISTS(SELECT 1 FROM backup_runs WHERE status='running')`
	args := []any{}
	if manual {
		query += ` AND p.id=? AND p.revision=?`
		args = append(args, requestedPlan, expectedRevision)
	} else {
		query += ` AND p.enabled=1 AND p.next_run_at<=?`
		args = append(args, formatAccountPoolTime(now))
	}
	query += ` ORDER BY p.next_run_at,p.id LIMIT 1`
	err = conn.QueryRowContext(ctx, query, args...).Scan(&claim.PlanID, &claim.PlanRevision, &interval, &claim.RetentionCount, &rehearsal, &scheduled, &claim.ProviderID, &claim.ProviderKind, &claim.ProviderVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	claim.RehearsalEnabled = rehearsal == 1
	claim.ScheduledFor = now
	if !manual {
		if !scheduled.Valid {
			return nil, errors.New("enabled backup plan is missing its next run time")
		}
		parsed, err := parseTime(scheduled.String)
		if err != nil {
			return nil, err
		}
		claim.ScheduledFor = parsed.UTC()
		next := nextBackupOccurrence(claim.ScheduledFor, now, interval)
		result, err := conn.ExecContext(ctx, `UPDATE backup_plans SET next_run_at=?,updated_at=? WHERE id=? AND revision=? AND enabled=1 AND next_run_at=?`, formatAccountPoolTime(next), formatAccountPoolTime(now), claim.PlanID, claim.PlanRevision, scheduled.String)
		if err != nil {
			return nil, err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return nil, errors.New("backup claim conflict")
		}
	}
	claim.PackageName = fmt.Sprintf("cpa-cloud-%s-%s-%s.cpacb", claim.PlanID, now.Format("20060102T150405Z"), claim.RunID)
	stamp := formatAccountPoolTime(now)
	if _, err := conn.ExecContext(ctx, `INSERT INTO backup_runs(id,plan_id,plan_revision,key_provider_id,key_provider_kind,key_provider_version,trigger_kind,requested_by_admin_id,scheduled_for,started_at,finished_at,status,package_name,package_size,package_retained,package_deleted_at,verified_at,rehearsal_status,rehearsed_at,error_code,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,NULL,'running',?,NULL,0,NULL,NULL,'pending',NULL,NULL,?)`, claim.RunID, claim.PlanID, claim.PlanRevision, claim.ProviderID, claim.ProviderKind, claim.ProviderVersion, claim.TriggerKind, claim.RequestedByAdmin, formatAccountPoolTime(claim.ScheduledFor), stamp, claim.PackageName, stamp); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	committed = true
	return &claim, nil
}

func nextBackupOccurrence(scheduled, now time.Time, intervalSeconds int64) time.Time {
	interval := time.Duration(intervalSeconds) * time.Second
	if scheduled.After(now) {
		return scheduled
	}
	steps := now.Sub(scheduled)/interval + 1
	return scheduled.Add(steps * interval)
}

func (c *backupAutomationCoordinator) run(claim backupAutomationClaim) {
	runCtx, cancel := context.WithCancel(c.ctx)
	c.mu.Lock()
	if c.closed || c.activeID != "" {
		c.mu.Unlock()
		cancel()
		c.finish(claim, "interrupted", "process_interrupted", nil, nil, "failed", nil)
		return
	}
	c.activeID, c.activeEnd = claim.RunID, cancel
	c.wg.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		defer func() {
			cancel()
			c.mu.Lock()
			if c.activeID == claim.RunID {
				c.activeID, c.activeEnd = "", nil
			}
			c.mu.Unlock()
			c.notify()
		}()
		c.execute(runCtx, claim)
	}()
}

func (c *backupAutomationCoordinator) execute(ctx context.Context, claim backupAutomationClaim) {
	material, err := c.keys.Resolve(ctx, claim.ProviderID, uint64(claim.ProviderVersion))
	if err != nil {
		c.finishFailure(claim, ctx, "key_provider_unavailable", nil, nil, "failed", nil)
		return
	}
	keyMaterial := backup.KeyMaterial{ProviderID: material.ProviderID, ProviderKind: material.Kind, ProviderVersion: material.Version}
	copy(keyMaterial.WrappingKey[:], material.Key[:])
	material.Destroy()
	defer func() {
		for index := range keyMaterial.WrappingKey {
			keyMaterial.WrappingKey[index] = 0
		}
	}()
	packagePath, err := c.output.packagePath(claim.PackageName)
	if err != nil {
		c.finishFailure(claim, ctx, "output_root_invalid", nil, nil, "failed", nil)
		return
	}
	if _, err := c.create(ctx, c.cfg.DataDir, packagePath, keyMaterial, c.cfg.SourceVersion); err != nil {
		c.finishUnretainedFailure(claim, ctx, "backup_create_failed")
		return
	}
	fileInfo, err := os.Lstat(packagePath)
	if err != nil || !fileInfo.Mode().IsRegular() {
		c.finishUnretainedFailure(claim, ctx, "backup_create_failed")
		return
	}
	size := fileInfo.Size()
	if _, err := c.verify(ctx, packagePath, keyMaterial); err != nil {
		c.finishUnretainedFailure(claim, ctx, "backup_verify_failed")
		return
	}
	verified := formatAccountPoolTime(c.now().UTC())
	rehearsalStatus := "skipped"
	var rehearsed *string
	if claim.RehearsalEnabled {
		rehearsalStatus = "failed"
		when, err := c.runRehearsal(ctx, packagePath, keyMaterial, claim.RunID)
		if err != nil {
			if !c.finishFailure(claim, ctx, "backup_rehearsal_failed", &size, &verified, rehearsalStatus, when) {
				_ = c.cleanupUnretainedPackage(claim)
			}
			return
		}
		rehearsalStatus = "succeeded"
		rehearsed = when
	}
	if err := c.applyRetention(ctx, claim); err != nil {
		if !c.finishFailure(claim, ctx, "backup_retention_failed", &size, &verified, rehearsalStatus, rehearsed) {
			_ = c.cleanupUnretainedPackage(claim)
		}
		return
	}
	if !c.finish(claim, "succeeded", "", &size, &verified, rehearsalStatus, rehearsed) {
		_ = c.cleanupUnretainedPackage(claim)
	}
}

func (c *backupAutomationCoordinator) cleanupUnretainedPackage(claim backupAutomationClaim) error {
	return c.output.removeOwnedPackage(claim.PlanID, claim.RunID, claim.PackageName)
}

func (c *backupAutomationCoordinator) finishUnretainedFailure(claim backupAutomationClaim, runCtx context.Context, code string) {
	_ = c.cleanupUnretainedPackage(claim)
	if !c.finishFailure(claim, runCtx, code, nil, nil, "failed", nil) {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = c.recoverUnretainedPackages(cleanupCtx)
}

func (c *backupAutomationCoordinator) finishFailure(claim backupAutomationClaim, ctx context.Context, code string, size *int64, verified *string, rehearsal string, rehearsed *string) bool {
	status := "failed"
	if errors.Is(ctx.Err(), context.Canceled) {
		status, code = "cancelled", "backup_cancelled"
	}
	return c.finish(claim, status, code, size, verified, rehearsal, rehearsed)
}

func (c *backupAutomationCoordinator) finish(claim backupAutomationClaim, status, errorCode string, size *int64, verified *string, rehearsal string, rehearsed *string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var errorValue any
	if errorCode != "" {
		errorValue = errorCode
	}
	finished := formatAccountPoolTime(c.now().UTC())
	retained := 0
	if size != nil {
		retained = 1
	}
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return false
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE backup_runs SET status=?,finished_at=?,package_size=?,package_retained=?,verified_at=?,rehearsal_status=?,rehearsed_at=?,error_code=? WHERE id=? AND status='running'`, status, finished, size, retained, verified, rehearsal, rehearsed, errorValue, claim.RunID)
	if err != nil {
		return false
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return false
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM backup_runs WHERE plan_id=? AND status!='running' AND package_retained=0 AND package_deleted_at IS NOT NULL AND id NOT IN (SELECT id FROM backup_runs WHERE plan_id=? AND status!='running' ORDER BY started_at DESC,id DESC LIMIT ?)`, claim.PlanID, claim.PlanID, backupMaxHistory); err != nil {
		return false
	}
	if err := c.commitRun(tx); err == nil {
		return true
	}
	var storedStatus string
	var storedRetained int
	err = c.app.store.db.QueryRowContext(ctx, `SELECT status,package_retained FROM backup_runs WHERE id=?`, claim.RunID).Scan(&storedStatus, &storedRetained)
	return err == nil && storedStatus == status && storedRetained == retained
}

func (c *backupAutomationCoordinator) runRehearsal(ctx context.Context, packagePath string, material backup.KeyMaterial, runID string) (*string, error) {
	parent, err := os.MkdirTemp(c.output.path, ".cpa-cloud-rehearsal-")
	if err != nil {
		return nil, err
	}
	if err := hardenBackupPath(parent, true); err != nil {
		_ = os.Remove(parent)
		return nil, err
	}
	target := filepath.Join(parent, "restored-"+runID)
	cleanup := func() error { return removeBackupRehearsal(parent, target) }
	if _, err := c.restore(ctx, packagePath, target, material); err != nil {
		_ = cleanup()
		return nil, err
	}
	validationErr := c.rehearse(ctx, target)
	cleanupErr := cleanup()
	if validationErr != nil {
		return nil, validationErr
	}
	if cleanupErr != nil {
		return nil, cleanupErr
	}
	stamp := formatAccountPoolTime(c.now().UTC())
	return &stamp, nil
}

func validateBackupRehearsal(ctx context.Context, base Config, dataDir string, prepare func(*Config)) error {
	base.DataDir = dataDir
	base.WebDir = ""
	base.InstanceID = ""
	base.AccountRecoveryEnabled = false
	base.ScheduledTestsEnabled = false
	base.AutomatedBackupsEnabled = false
	base.ExperimentalCodexMembership = false
	if prepare != nil {
		prepare(&base)
	}
	rehearsal, err := Open(ctx, base)
	if err != nil {
		return err
	}
	return rehearsal.Close()
}

func resolveBackupAutomationPaths(cfg Config) (string, string) {
	dataDir := filepath.Clean(cfg.DataDir)
	outputDir := strings.TrimSpace(cfg.AutomatedBackupsOutputDir)
	if outputDir == "" {
		outputDir = dataDir + "-automated-backups"
	}
	keyStoreDir := strings.TrimSpace(cfg.BackupKeyProviderStoreDir)
	if keyStoreDir == "" {
		keyStoreDir = dataDir + "-backup-key-provider"
	}
	return outputDir, keyStoreDir
}

func prepareBackupRehearsalConfig(cfg *Config) {
	if cfg == nil {
		return
	}
	cfg.Listen = "127.0.0.1:0"
	cfg.TLSCert = ""
	cfg.TLSKey = ""
	cfg.AllowLoopbackUpstream = false
	cfg.AccountRecoveryEnabled = false
	cfg.ScheduledTestsEnabled = false
	cfg.AutomatedBackupsEnabled = false
	cfg.ExperimentalCodexMembership = false
	cfg.CodexOAuthClientID = ""
	cfg.CodexOAuthRedirectURI = ""
	cfg.backupAutomationRehearsal = true
}

func removeBackupRehearsal(parent, target string) error {
	known := []string{"cpa-cloud.db-wal", "cpa-cloud.db-shm", "cpa-cloud.db-journal", "cpa-cloud.db", "master.key"}
	for _, name := range known {
		path := filepath.Join(target, name)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(parent); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (c *backupAutomationCoordinator) applyRetention(ctx context.Context, claim backupAutomationClaim) error {
	offset := claim.RetentionCount - 1
	rows, err := c.app.store.db.QueryContext(ctx, `SELECT id,package_name FROM backup_runs WHERE plan_id=? AND package_retained=1 AND status!='running' ORDER BY started_at DESC,id DESC LIMIT -1 OFFSET ?`, claim.PlanID, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	type owned struct{ id, name string }
	var old []owned
	for rows.Next() {
		var item owned
		if err := rows.Scan(&item.id, &item.name); err != nil {
			return err
		}
		old = append(old, item)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(old) == 0 {
		return nil
	}
	tx, err := c.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range old {
		result, err := tx.ExecContext(ctx, `UPDATE backup_runs SET package_retained=0,package_deleted_at=NULL WHERE id=? AND plan_id=? AND package_name=? AND package_retained=1 AND status!='running'`, item.id, claim.PlanID, item.name)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("backup retention state conflict")
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, item := range old {
		if err := c.output.removeOwnedPackage(claim.PlanID, item.id, item.name); err != nil {
			return err
		}
		result, err := c.app.store.db.ExecContext(ctx, `UPDATE backup_runs SET package_deleted_at=? WHERE id=? AND plan_id=? AND package_name=? AND package_retained=0 AND package_deleted_at IS NULL`, formatAccountPoolTime(c.now().UTC()), item.id, claim.PlanID, item.name)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("backup retention cleanup state conflict")
		}
	}
	return nil
}

func (c *backupAutomationCoordinator) cancelRun(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.activeID != id || c.activeEnd == nil {
		return false
	}
	c.activeEnd()
	return true
}
