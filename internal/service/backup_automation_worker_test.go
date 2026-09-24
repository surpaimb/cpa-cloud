package service

// Independently authored worker tests for docs/adr/0002-automated-backup-key-custody.md.

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/keyprovider"
)

type fakeBackupKeyStore struct {
	mu       sync.Mutex
	ready    bool
	versions map[string]map[uint64][32]byte
}

type fakePreparedKey struct {
	store   *fakeBackupKeyStore
	id      string
	version uint64
	key     [32]byte
	done    bool
}

func newFakeBackupKeyStore() *fakeBackupKeyStore {
	return &fakeBackupKeyStore{ready: true, versions: make(map[string]map[uint64][32]byte)}
}

func (s *fakeBackupKeyStore) Ready() (bool, string) {
	if s.ready {
		return true, ""
	}
	return false, keyprovider.ReasonUnsupported
}

func (s *fakeBackupKeyStore) PrepareVersion(_ context.Context, id string, version uint64) (backupPreparedKeyVersion, error) {
	if !s.ready {
		return nil, keyprovider.ErrUnavailable
	}
	prepared := &fakePreparedKey{store: s, id: id, version: version}
	for index := range prepared.key {
		prepared.key[index] = byte(index + 1 + int(version))
	}
	return prepared, nil
}

func (s *fakeBackupKeyStore) Resolve(_ context.Context, id string, version uint64) (*keyprovider.Material, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := s.versions[id]
	key, ok := versions[version]
	if !ok {
		return nil, keyprovider.ErrUnavailable
	}
	return &keyprovider.Material{ProviderID: id, Kind: keyprovider.KindWindowsDPAPIUser, Version: version, Key: key}, nil
}

func (s *fakeBackupKeyStore) DiscardVersion(_ context.Context, id string, version uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	versions := s.versions[id]
	if _, ok := versions[version]; !ok {
		return os.ErrNotExist
	}
	delete(versions, version)
	return nil
}

func (p *fakePreparedKey) Commit() {
	if p == nil || p.done {
		return
	}
	p.store.mu.Lock()
	defer p.store.mu.Unlock()
	if p.store.versions[p.id] == nil {
		p.store.versions[p.id] = make(map[uint64][32]byte)
	}
	p.store.versions[p.id][p.version] = p.key
	p.done = true
}

func (p *fakePreparedKey) Rollback() error {
	if p != nil {
		p.done = true
	}
	return nil
}

type backupAutomationFixture struct {
	app     *App
	worker  *backupAutomationCoordinator
	keys    *fakeBackupKeyStore
	root    string
	adminID string
}

func newBackupAutomationFixture(t *testing.T, enabled bool) *backupAutomationFixture {
	t.Helper()
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateBackupAutomation(context.Background(), app.store.db); err != nil {
		app.Close()
		t.Fatal(err)
	}
	worker, err := newBackupAutomationCoordinator(context.Background(), app, BackupAutomationConfig{
		Enabled: enabled, DataDir: dataDir, OutputRoot: filepath.Join(root, "backups"),
		ProviderStoreRoot: filepath.Join(root, "keys"), SourceVersion: "test",
		PrepareRehearsalConfig: func(*Config) {},
	})
	if err != nil {
		app.Close()
		t.Fatal(err)
	}
	keys := newFakeBackupKeyStore()
	worker.keys = keys
	var adminID string
	if err := app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		worker.Close()
		app.Close()
		t.Fatal(err)
	}
	fixture := &backupAutomationFixture{app: app, worker: worker, keys: keys, root: root, adminID: adminID}
	t.Cleanup(func() {
		worker.Close()
		if err := app.Close(); err != nil {
			t.Errorf("close app: %v", err)
		}
	})
	return fixture
}

func (f *backupAutomationFixture) seedProviderAndPlan(t *testing.T, retention int64, rehearsal bool) (string, string) {
	t.Helper()
	providerID, planID := "bkp_synthetic", "bpl_synthetic"
	prepared, err := f.keys.PrepareVersion(context.Background(), providerID, 1)
	if err != nil {
		t.Fatal(err)
	}
	prepared.Commit()
	stamp := formatAccountPoolTime(time.Now().UTC())
	if _, err := f.app.store.db.Exec(`INSERT INTO backup_key_providers(id,kind,scope,status,reason_code,active_version,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES(?,?,'current-user','ready',NULL,1,1,?,?,?,?)`, providerID, keyprovider.KindWindowsDPAPIUser, f.adminID, f.adminID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO backup_plans(id,name,key_provider_id,interval_seconds,retention_count,rehearsal_enabled,enabled,next_run_at,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES(?,?,?,900,?,?,1,?,1,?,?,?,?)`, planID, "Synthetic backup", providerID, retention, boolInt(rehearsal), stamp, f.adminID, f.adminID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	return providerID, planID
}

func TestBackupAutomationManualRunVerifyRehearsalAndRetention(t *testing.T) {
	fixture := newBackupAutomationFixture(t, true)
	_, planID := fixture.seedProviderAndPlan(t, 1, true)
	oldRunID := "brn_old"
	oldName := "cpa-cloud-" + planID + "-20260924T000000Z-" + oldRunID + ".cpacb"
	if err := os.WriteFile(filepath.Join(fixture.worker.output.path, oldName), []byte("old-owned-package"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := formatAccountPoolTime(time.Now().Add(-time.Hour).UTC())
	if _, err := fixture.app.store.db.Exec(`INSERT INTO backup_runs(id,plan_id,plan_revision,key_provider_id,key_provider_kind,key_provider_version,trigger_kind,requested_by_admin_id,scheduled_for,started_at,finished_at,status,package_name,package_size,package_retained,package_deleted_at,verified_at,rehearsal_status,rehearsed_at,error_code,created_at) VALUES(?,?,1,'bkp_synthetic','windows-dpapi-user',1,'scheduled',NULL,?,?,?,'succeeded',?,17,1,NULL,?,'skipped',NULL,NULL,?)`, oldRunID, planID, stamp, stamp, stamp, oldName, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	rehearsed := false
	fixture.worker.rehearse = func(context.Context, string) error {
		rehearsed = true
		return nil
	}
	claim, err := fixture.worker.claimPlan(context.Background(), planID, 1, fixture.adminID)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	fixture.worker.run(*claim)
	run := waitBackupRunComplete(t, fixture.app.store.db, claim.RunID)
	if run.Status != "succeeded" || run.ErrorCode != nil || run.VerifiedAt == nil || run.RehearsalStatus != "succeeded" || run.RehearsedAt == nil || !run.PackageRetained || !rehearsed {
		t.Fatalf("unexpected completed run: %+v rehearsed=%v", run, rehearsed)
	}
	if _, err := os.Lstat(filepath.Join(fixture.worker.output.path, run.PackageName)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.worker.output.path, oldName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old owned package remains: %v", err)
	}
	old, err := loadBackupRun(context.Background(), fixture.app.store.db, oldRunID)
	if err != nil {
		t.Fatal(err)
	}
	if old.PackageRetained || old.PackageDeletedAt == nil {
		t.Fatalf("retention state was not persisted: %+v", old)
	}
	leftovers, err := filepath.Glob(filepath.Join(fixture.worker.output.path, ".cpa-cloud-rehearsal-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("rehearsal plaintext remains: %v %v", leftovers, err)
	}
}

func TestBackupAutomationMutualExclusionCancellationAndNoSecondClaim(t *testing.T) {
	fixture := newBackupAutomationFixture(t, true)
	_, planID := fixture.seedProviderAndPlan(t, 2, false)
	entered := make(chan struct{})
	fixture.worker.create = func(ctx context.Context, _, _ string, _ backup.KeyMaterial, _ string) (backup.Info, error) {
		close(entered)
		<-ctx.Done()
		return backup.Info{}, ctx.Err()
	}
	claim, err := fixture.worker.claimPlan(context.Background(), planID, 1, fixture.adminID)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	fixture.worker.run(*claim)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("backup execution did not start")
	}
	second, err := fixture.worker.claimPlan(context.Background(), planID, 1, fixture.adminID)
	if err != nil {
		t.Fatal(err)
	}
	if second != nil {
		t.Fatalf("second concurrent run was claimed: %+v", second)
	}
	if !fixture.worker.cancelRun(claim.RunID) {
		t.Fatal("active run could not be cancelled")
	}
	run := waitBackupRunComplete(t, fixture.app.store.db, claim.RunID)
	if run.Status != "cancelled" || run.ErrorCode == nil || *run.ErrorCode != "backup_cancelled" {
		t.Fatalf("cancelled run=%+v", run)
	}
}

func TestBackupAutomationVerificationFailureRemovesUntrustedPackage(t *testing.T) {
	fixture := newBackupAutomationFixture(t, true)
	_, planID := fixture.seedProviderAndPlan(t, 2, false)
	fixture.worker.create = func(_ context.Context, _, output string, _ backup.KeyMaterial, _ string) (backup.Info, error) {
		return backup.Info{}, os.WriteFile(output, []byte("not-an-authenticated-backup"), 0o600)
	}
	fixture.worker.verify = func(context.Context, string, backup.KeyMaterial) (backup.Info, error) {
		return backup.Info{}, errors.New("synthetic authentication failure")
	}
	claim, err := fixture.worker.claimPlan(context.Background(), planID, 1, fixture.adminID)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	fixture.worker.run(*claim)
	run := waitBackupRunComplete(t, fixture.app.store.db, claim.RunID)
	if run.Status != "failed" || run.ErrorCode == nil || *run.ErrorCode != "backup_verify_failed" || run.PackageRetained || run.PackageSize != nil {
		t.Fatalf("verification failure was recorded unsafely: %+v", run)
	}
	if _, err := os.Lstat(filepath.Join(fixture.worker.output.path, claim.PackageName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unverified package remains: %v", err)
	}
}

func TestBackupAutomationFinalizationFailureRemovesPackageAndRestartRecoversRun(t *testing.T) {
	fixture := newBackupAutomationFixture(t, true)
	_, planID := fixture.seedProviderAndPlan(t, 2, false)
	fixture.worker.commitRun = func(*sql.Tx) error { return errors.New("synthetic commit failure") }
	claim, err := fixture.worker.claimPlan(context.Background(), planID, 1, fixture.adminID)
	if err != nil || claim == nil {
		t.Fatalf("claim=%+v err=%v", claim, err)
	}
	fixture.worker.run(*claim)
	deadline := time.Now().Add(10 * time.Second)
	for {
		fixture.worker.mu.Lock()
		active := fixture.worker.activeID
		fixture.worker.mu.Unlock()
		if active == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("backup execution did not stop after finalization failure")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Lstat(filepath.Join(fixture.worker.output.path, claim.PackageName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("package orphaned after finalization failure: %v", err)
	}
	run, err := loadBackupRun(context.Background(), fixture.app.store.db, claim.RunID)
	if err != nil || run.Status != "running" || run.PackageRetained {
		t.Fatalf("unexpected pre-restart state: %+v err=%v", run, err)
	}
	fixture.worker.commitRun = func(tx *sql.Tx) error { return tx.Commit() }
	if err := migrateBackupAutomation(context.Background(), fixture.app.store.db); err != nil {
		t.Fatal(err)
	}
	if err := fixture.worker.recoverUnretainedPackages(context.Background()); err != nil {
		t.Fatal(err)
	}
	run, err = loadBackupRun(context.Background(), fixture.app.store.db, claim.RunID)
	if err != nil || run.Status != "interrupted" || run.ErrorCode == nil || *run.ErrorCode != "process_interrupted" || run.PackageDeletedAt == nil {
		t.Fatalf("restart did not recover finalization failure: %+v err=%v", run, err)
	}
}

func TestBackupAutomationRecoversDatabaseFirstRetentionCleanup(t *testing.T) {
	fixture := newBackupAutomationFixture(t, false)
	_, planID := fixture.seedProviderAndPlan(t, 2, false)
	runID := "brn_cleanup_pending"
	name := "cpa-cloud-" + planID + "-20260924T000000Z-" + runID + ".cpacb"
	if err := os.WriteFile(filepath.Join(fixture.worker.output.path, name), []byte("owned-package-pending-delete"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamp := formatAccountPoolTime(time.Now().UTC())
	if _, err := fixture.app.store.db.Exec(`INSERT INTO backup_runs(id,plan_id,plan_revision,key_provider_id,key_provider_kind,key_provider_version,trigger_kind,requested_by_admin_id,scheduled_for,started_at,finished_at,status,package_name,package_size,package_retained,package_deleted_at,verified_at,rehearsal_status,rehearsed_at,error_code,created_at) VALUES(?,?,1,'bkp_synthetic','windows-dpapi-user',1,'scheduled',NULL,?,?,?,'succeeded',?,32,0,NULL,?,'skipped',NULL,NULL,?)`, runID, planID, stamp, stamp, stamp, name, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := fixture.worker.recoverUnretainedPackages(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(fixture.worker.output.path, name)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending retained cleanup remains: %v", err)
	}
	run, err := loadBackupRun(context.Background(), fixture.app.store.db, runID)
	if err != nil || run.PackageRetained || run.PackageDeletedAt == nil {
		t.Fatalf("cleanup state was not finalized: %+v err=%v", run, err)
	}
}

func TestBackupAutomationDisabledAndOwnedRetentionFailure(t *testing.T) {
	fixture := newBackupAutomationFixture(t, false)
	_, planID := fixture.seedProviderAndPlan(t, 1, false)
	if err := fixture.worker.Start(); err != nil {
		t.Fatal(err)
	}
	if fixture.worker.Running() {
		t.Fatal("disabled worker reported running")
	}
	if _, err := fixture.worker.claimPlan(context.Background(), planID, 1, fixture.adminID); err == nil || err.Error() != "backup_worker_disabled" {
		t.Fatalf("disabled claim error=%v", err)
	}
	badRunID := "brn_bad"
	stamp := formatAccountPoolTime(time.Now().UTC())
	if _, err := fixture.app.store.db.Exec(`INSERT INTO backup_runs(id,plan_id,plan_revision,key_provider_id,key_provider_kind,key_provider_version,trigger_kind,requested_by_admin_id,scheduled_for,started_at,finished_at,status,package_name,package_size,package_retained,package_deleted_at,verified_at,rehearsal_status,rehearsed_at,error_code,created_at) VALUES(?,?,1,'bkp_synthetic','windows-dpapi-user',1,'scheduled',NULL,?,?,?,'succeeded','not-owned.cpacb',1,1,NULL,?,'skipped',NULL,NULL,?)`, badRunID, planID, stamp, stamp, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	claim := backupAutomationClaim{PlanID: planID, RetentionCount: 1}
	if err := fixture.worker.applyRetention(context.Background(), claim); err == nil {
		t.Fatal("retention accepted a package without provable ownership")
	}
}

func TestBackupAutomationRecoversOnlyExactProviderOrphans(t *testing.T) {
	fixture := newBackupAutomationFixture(t, false)
	providerID, _ := fixture.seedProviderAndPlan(t, 2, false)
	orphan, err := fixture.keys.PrepareVersion(context.Background(), providerID, 2)
	if err != nil {
		t.Fatal(err)
	}
	orphan.Commit()
	if _, err := fixture.app.store.db.Exec(`UPDATE backup_key_providers SET status='degraded',reason_code='rotation_interrupted' WHERE id=?`, providerID); err != nil {
		t.Fatal(err)
	}
	unknown, err := fixture.keys.PrepareVersion(context.Background(), providerID, 4)
	if err != nil {
		t.Fatal(err)
	}
	unknown.Commit()
	provisioningID := "bkp_provisioning"
	provisioning, err := fixture.keys.PrepareVersion(context.Background(), provisioningID, 1)
	if err != nil {
		t.Fatal(err)
	}
	provisioning.Commit()
	stamp := formatAccountPoolTime(time.Now().UTC())
	if _, err := fixture.app.store.db.Exec(`INSERT INTO backup_key_providers(id,kind,scope,status,reason_code,active_version,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES(?,?,'current-user','degraded','provisioning_interrupted',1,1,?,?,?,?)`, provisioningID, keyprovider.KindWindowsDPAPIUser, fixture.adminID, fixture.adminID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := fixture.worker.recoverKeyProviderOrphans(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.keys.Resolve(context.Background(), providerID, 2); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("active+1 orphan remains: %v", err)
	}
	material, err := fixture.keys.Resolve(context.Background(), providerID, 4)
	if err != nil {
		t.Fatalf("unknown version was deleted: %v", err)
	}
	material.Destroy()
	if _, err := fixture.keys.Resolve(context.Background(), provisioningID, 1); !errors.Is(err, keyprovider.ErrUnavailable) {
		t.Fatalf("provisioning orphan remains: %v", err)
	}
	if _, err := loadBackupKeyProvider(context.Background(), fixture.app.store.db, provisioningID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("provisioning metadata remains: %v", err)
	}
	provider, err := loadBackupKeyProvider(context.Background(), fixture.app.store.db, providerID)
	if err != nil || provider.ActiveVersion != 1 || provider.Status != "ready" {
		t.Fatalf("active provider changed: %+v err=%v", provider, err)
	}
}

func TestBackupAutomationRecoveryPreservesCommittedInactiveVersion(t *testing.T) {
	fixture := newBackupAutomationFixture(t, false)
	providerID, _ := fixture.seedProviderAndPlan(t, 2, false)
	newer, err := fixture.keys.PrepareVersion(context.Background(), providerID, 2)
	if err != nil {
		t.Fatal(err)
	}
	newer.Commit()
	if err := fixture.worker.recoverKeyProviderOrphans(context.Background()); err != nil {
		t.Fatal(err)
	}
	material, err := fixture.keys.Resolve(context.Background(), providerID, 2)
	if err != nil {
		t.Fatalf("committed inactive version was deleted: %v", err)
	}
	material.Destroy()
}

func waitBackupRunComplete(t *testing.T, db *sql.DB, id string) backupRunView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		item, err := loadBackupRun(context.Background(), db, id)
		if err == nil && item.Status != "running" {
			return item
		}
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("backup run did not finish")
	return backupRunView{}
}
