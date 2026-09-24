package service

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenWiresBackupAutomationDefaultOff(t *testing.T) {
	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if app.backupAutomation == nil || app.backupAutomation.Running() {
		t.Fatalf("default-off coordinator=%v running=%v", app.backupAutomation != nil, app.backupAutomation != nil && app.backupAutomation.Running())
	}
	for _, table := range []string{"backup_key_providers", "backup_plans", "backup_runs"} {
		var count int
		if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("table %s count=%d err=%v", table, count, err)
		}
	}
	if backupPathsOverlap(dataDir, app.backupAutomation.output.path) {
		t.Fatalf("backup output overlaps data directory: %s", app.backupAutomation.output.path)
	}
}

func TestSystemStatusReportsBackupCapabilities(t *testing.T) {
	app := &App{cfg: Config{}, backupAutomation: &backupAutomationCoordinator{cfg: BackupAutomationConfig{Enabled: false}}}
	recorder := httptest.NewRecorder()
	app.systemStatus(recorder, httptest.NewRequest("GET", "/admin/api/v1/system/status", nil), adminSession{})
	var response struct {
		Features map[string]bool `json:"features"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Features["automated_backups_configuration"] || response.Features["backup_key_provider_ready"] || response.Features["automated_backups_running"] {
		t.Fatalf("unexpected backup features: %+v", response.Features)
	}
	for _, feature := range []string{"reliable_usage_accounting", "general_budget_enforcement", "single_instance_billing"} {
		if !response.Features[feature] {
			t.Fatalf("expected %s capability: %+v", feature, response.Features)
		}
	}
	for _, feature := range []string{"key_access_policy", "key_source_policy"} {
		if !response.Features[feature] {
			t.Fatalf("expected %s capability: %+v", feature, response.Features)
		}
	}
	if response.Features["managed_tools"] || response.Features["responses_stateful_resources"] || response.Features["responses_background_tasks"] {
		t.Fatalf("unexpected default Responses features: %+v", response.Features)
	}
}

func TestPrepareBackupRehearsalConfigDisablesBackgroundAndNetworkFeatures(t *testing.T) {
	cfg := Config{
		Listen: "0.0.0.0:8787", TLSCert: "cert", TLSKey: "key", AllowLoopbackUpstream: true,
		AccountRecoveryEnabled: true, ScheduledTestsEnabled: true, AutomatedBackupsEnabled: true,
		ResponsesStatefulResources: true, ResponsesBackgroundTasks: true,
		ExperimentalCodexMembership: true, CodexOAuthClientID: "client", CodexOAuthRedirectURI: "https://example.test/admin/api/v1/codex/oauth/callback",
	}
	prepareBackupRehearsalConfig(&cfg)
	if cfg.Listen != "127.0.0.1:0" || cfg.TLSCert != "" || cfg.TLSKey != "" || cfg.AllowLoopbackUpstream || cfg.AccountRecoveryEnabled || cfg.ScheduledTestsEnabled || cfg.AutomatedBackupsEnabled || cfg.ResponsesStatefulResources || cfg.ResponsesBackgroundTasks || cfg.ExperimentalCodexMembership || cfg.CodexOAuthClientID != "" || cfg.CodexOAuthRedirectURI != "" || !cfg.backupAutomationRehearsal {
		t.Fatalf("unsafe rehearsal config: %+v", cfg)
	}
}
