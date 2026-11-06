//go:build linux

package service

// Independently authored regression tests for ADR 0005 and PR78 review findings.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/financial"
	"cpacloud.local/server/internal/keyprovider"
)

func newLinuxBackupFixture(t *testing.T, rehearsal bool, selfEnabled ...bool) (*backupAutomationFixture, string, string, masterKeyMaterial) {
	t.Helper()
	root := t.TempDir()
	credentialDir := t.TempDir()
	installation := testMasterKeyMaterial(0xa5)
	writeLinuxSystemdCredential(t, credentialDir, installation)
	t.Setenv("CREDENTIALS_DIRECTORY", credentialDir)
	cfg := Config{DataDir: filepath.Join(root, "data"), Listen: "127.0.0.1:0", Version: "test", MasterKeyProvider: masterKeyProviderLinuxSystemdCredentialV1}
	cfg.EmployeeSelfServiceEnabled = len(selfEnabled) > 0 && selfEnabled[0]
	if err := InitializeWithConfig(context.Background(), cfg, strings.NewReader("synthetic-admin-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Error(err)
		}
	})
	worker, err := newBackupAutomationCoordinator(context.Background(), app, BackupAutomationConfig{
		Enabled: true, DataDir: cfg.DataDir, OutputRoot: filepath.Join(root, "backups"), ProviderStoreRoot: filepath.Join(root, "keys"), SourceVersion: "test", PrepareRehearsalConfig: prepareBackupRehearsalConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { worker.Close() })
	keys := newFakeBackupKeyStore()
	worker.keys = keys
	fixture := &backupAutomationFixture{app: app, worker: worker, keys: keys, root: root}
	if err := app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&fixture.adminID); err != nil {
		t.Fatal(err)
	}
	providerID, planID := fixture.seedProviderAndPlan(t, 1, rehearsal)
	keys.configured = &keyprovider.Reference{ProviderID: providerID, Kind: keyprovider.KindLinuxSystemdCredentialV1, InstanceID: installation.InstanceID, KeyID: "separate-backup-key", Version: 1}
	for index := range keys.configuredKey {
		keys.configuredKey[index] = byte(index + 1)
	}
	if _, err := app.store.db.Exec(`UPDATE backup_key_providers SET kind=?,scope='systemd-service',instance_id=?,key_id=? WHERE id=?`, keys.configured.Kind, keys.configured.InstanceID, keys.configured.KeyID, providerID); err != nil {
		t.Fatal(err)
	}
	return fixture, planID, credentialDir, installation
}

// D2 uses the real synthetic installation provider, binding/canary validator,
// format-3 codec, Restore and service.Open. No unconditional binding validator.
func TestD2LinuxV3RestoredSelfHTTP(t *testing.T) {
	f, _, _, _ := newLinuxBackupFixture(t, false, true)
	ctx := context.Background()
	source := httptest.NewServer(f.app.Handler())
	defer source.Close()
	login := func(base string) (*http.Cookie, string) {
		r := requestJSON(t, "POST", base+"/admin/api/v1/sessions", `{"username":"admin","password":"synthetic-admin-password"}`, nil, "", base)
		if r.StatusCode != 200 {
			t.Fatal("D2_V3_ADMIN_LOGIN")
		}
		var v map[string]string
		decodeResponse(t, r, &v)
		if len(r.Cookies()) != 1 {
			t.Fatal("D2_V3_ADMIN_COOKIE")
		}
		return r.Cookies()[0], v["csrf_token"]
	}
	admin, csrf := login(source.URL)
	ids := make([]string, 5)
	keys := make([]keyView, 5)
	for i := range ids {
		e := selfCreateEmployee(t, source.URL, admin, csrf)
		ids[i] = e.ID
		keys[i] = createTestKey(t, source.URL, e.ID, fmt.Sprintf("d2-v3-key-%d", i), admin, csrf)
	}
	r := requestJSON(t, "POST", source.URL+"/admin/api/v1/keys/"+keys[1].ID+"/revoke", `{}`, admin, csrf, source.URL)
	if r.StatusCode != 200 {
		t.Fatal("D2_V3_REVOKE")
	}
	r.Body.Close()
	secret := selfIssue(t, source.URL, ids[0], admin, csrf)
	oldCookie, _ := selfRedeem(t, source.URL, ids[0], secret)
	r = selfRequestTest(t, "GET", source.URL+"/self/api/v1/profile", "", "", oldCookie, "")
	if r.StatusCode != 200 {
		t.Fatal("D2_V3_SOURCE_COOKIE")
	}
	r.Body.Close()
	pending := selfIssue(t, source.URL, ids[1], admin, csrf)
	for i, state := range []string{"pending", "cancelled", "armed", "issued"} {
		var fp, armed, issued, cancelled any
		if i >= 2 {
			fp = bytes.Repeat([]byte{0x24}, 32)
			armed = "2026-01-01T00:00:00Z"
		}
		if i == 1 {
			cancelled = "2026-01-02T00:00:00Z"
		}
		if i == 3 {
			issued = "2026-01-02T00:00:00Z"
		}
		if _, err := f.app.store.db.Exec(`INSERT INTO employee_self_key_slots(employee_id,key_id,state,arm_fingerprint,created_at,armed_at,issued_at,cancelled_at) VALUES(?,?,?,?,'2026-01-01T00:00:00Z',?,?,?)`, ids[i], keys[i].ID, state, fp, armed, issued, cancelled); err != nil {
			t.Fatal("D2_V3_SLOTS")
		}
	}
	if _, err := f.app.store.db.Exec(`UPDATE employees SET status='disabled',revision=revision+1 WHERE id=?`, ids[3]); err != nil {
		t.Fatal("D2_V3_DISABLED")
	}
	for i, amount := range []int64{137, -42} {
		kind := financial.EntryAdjustmentCredit
		if amount < 0 {
			kind = financial.EntryAdjustmentDebit
		}
		op := fmt.Sprintf("d2-v3-ledger-%d", i)
		_, err := financial.NewLedger(f.app.store.db).Post(ctx, financial.Post{OperationID: op, Action: "adjustment", Actor: financial.Actor{Kind: financial.ActorEmployee, ID: ids[0]}, ResourceKind: "adjustment", ResourceID: op, ObservedAt: time.Date(2026, 10, 1, 0, 0, i, 0, time.UTC), Entries: []financial.EntryInput{{Owner: financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: ids[0]}, Currency: "USD", Kind: kind, AmountMicro: amount, ResourceKind: "adjustment", ResourceID: op}}})
		if err != nil {
			t.Fatal("D2_V3_LEDGER")
		}
	}
	models := func(base string, k keyView, want int) {
		req, _ := http.NewRequest("GET", base+"/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+k.Key)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal("D2_V3_MODELS_TRANSPORT")
		}
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatal("D2_V3_MODELS_STATUS")
		}
	}
	for i, k := range keys {
		want := 401
		if i == 4 {
			want = 200
		}
		models(source.URL, k, want)
	}
	before := d2LinuxFacts(t, f.app.store.db, false)
	credentialBefore := d2LinuxRows(t, f.app.store.db, `SELECT employee_id,password_hash,updated_at FROM employee_self_credentials ORDER BY employee_id`)
	ref := f.keys.configured
	material := backup.KeyMaterial{ProviderID: ref.ProviderID, ProviderKind: ref.Kind, InstanceID: ref.InstanceID, KeyID: ref.KeyID, ProviderVersion: ref.Version, WrappingKey: f.keys.configuredKey, ValidateInstallationBinding: f.worker.validateInstallationBinding}
	pkg := filepath.Join(f.root, "d2-v3.cpacb")
	info, err := backup.CreateWithKeyMaterial(ctx, f.app.cfg.DataDir, pkg, material, "d2")
	if err != nil || info.FormatVersion != 3 {
		t.Fatal("D2_V3_CREATE")
	}
	packageBefore, err := os.ReadFile(pkg)
	if err != nil {
		t.Fatal("D2_V3_PACKAGE_READ")
	}
	bindingBefore, err := os.ReadFile(filepath.Join(f.app.cfg.DataDir, masterKeyBindingFilename))
	if err != nil {
		t.Fatal("D2_V3_BINDING_READ")
	}
	selfRedeem(t, source.URL, ids[1], pending) // usable after snapshot, not an expired marker
	sourceBefore := d2LinuxFacts(t, f.app.store.db, true)
	target := filepath.Join(f.root, "d2-restored")
	info, err = backup.RestoreWithKeyMaterial(ctx, pkg, target, material)
	if err != nil || info.FormatVersion != 3 {
		t.Fatal("D2_V3_RESTORE")
	}
	if _, err := os.Lstat(filepath.Join(target, rootKeyFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("D2_V3_LEGACY_KEY_CREATED")
	}
	bindingAfter, err := os.ReadFile(filepath.Join(target, masterKeyBindingFilename))
	if err != nil || !bytes.Equal(bindingBefore, bindingAfter) {
		t.Fatal("D2_V3_BINDING_CHANGED")
	}
	pre, err := sql.Open("sqlite", filepath.Join(target, "cpa-cloud.db"))
	if err != nil {
		t.Fatal("D2_V3_PREOPEN")
	}
	if !reflect.DeepEqual(before, d2LinuxFacts(t, pre, false)) || !reflect.DeepEqual(credentialBefore, d2LinuxRows(t, pre, `SELECT employee_id,password_hash,updated_at FROM employee_self_credentials ORDER BY employee_id`)) {
		t.Fatal("D2_V3_NON_TARGET_CHANGED")
	}
	var clean int
	if err := pre.QueryRow(`SELECT (SELECT count(*) FROM employee_self_sessions)=0 AND (SELECT count(*) FROM employee_self_credentials WHERE enrollment_digest IS NOT NULL OR enrollment_expires_at IS NOT NULL)=0`).Scan(&clean); err != nil || clean != 1 {
		t.Fatal("D2_V3_PREOPEN_NOT_CLEAN")
	}
	if pre.Close() != nil {
		t.Fatal("D2_V3_PREOPEN_CLOSE")
	}
	cfg := f.app.cfg
	cfg.DataDir = target
	restored, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal("D2_V3_RESTORED_OPEN")
	}
	defer restored.Close()
	srv := httptest.NewServer(restored.Handler())
	defer srv.Close()
	r = selfRequestTest(t, "GET", srv.URL+"/self/api/v1/profile", "", "", oldCookie, "")
	if r.StatusCode != 401 || len(r.Cookies()) != 0 {
		t.Fatal("D2_V3_OLD_COOKIE_ACCEPTED")
	}
	r.Body.Close()
	r = selfRequestTest(t, "POST", srv.URL+"/self/api/v1/enroll", `{"employee_id":`+quoteJSON(ids[1])+`,"enrollment_secret":`+quoteJSON(pending)+`,"password":"a-long-self-password"}`, srv.URL, nil, "")
	if r.StatusCode != 401 || len(r.Cookies()) != 0 {
		t.Fatal("D2_V3_OLD_ENROLLMENT_ACCEPTED")
	}
	r.Body.Close()
	if err := restored.store.db.QueryRow(`SELECT (SELECT count(*) FROM employee_self_sessions)=0 AND (SELECT password_hash IS NULL FROM employee_self_credentials WHERE employee_id=?)`, ids[1]).Scan(&clean); err != nil || clean != 1 {
		t.Fatal("D2_V3_NEGATIVE_WROTE_STATE")
	}
	fresh, _ := selfLoginTest(t, srv.URL, ids[0], "a-long-self-password")
	r = selfRequestTest(t, "GET", srv.URL+"/self/api/v1/profile", "", "", fresh, "")
	if r.StatusCode != 200 {
		t.Fatal("D2_V3_FRESH_COOKIE")
	}
	var profile selfProfile
	decodeResponse(t, r, &profile)
	if profile.ID != ids[0] {
		t.Fatal("D2_V3_PROFILE_OWNER")
	}
	for i, k := range keys {
		want := 401
		if i == 4 {
			want = 200
		}
		models(srv.URL, k, want)
	}
	r = requestJSON(t, "GET", srv.URL+"/admin/api/v1/session", "", admin, "", "")
	if r.StatusCode != 401 {
		t.Fatal("D2_V3_OLD_ADMIN_ACCEPTED")
	}
	r.Body.Close()
	newAdmin, newCSRF := login(srv.URL)
	selfIssue(t, srv.URL, ids[1], newAdmin, newCSRF)
	packageAfter, err := os.ReadFile(pkg)
	if err != nil || !bytes.Equal(packageBefore, packageAfter) || !reflect.DeepEqual(sourceBefore, d2LinuxFacts(t, f.app.store.db, true)) {
		t.Fatal("D2_V3_SOURCE_PACKAGE_CHANGED")
	}
	t.Log("D2_V3_REAL_RESTORE_HTTP_PASS")
}

func d2LinuxRows(t *testing.T, db *sql.DB, q string) []string {
	t.Helper()
	r, err := db.Query(q)
	if err != nil {
		t.Fatal("D2_V3_FACT_QUERY")
	}
	cols, err := r.Columns()
	if err != nil {
		t.Fatal("D2_V3_FACT_COLUMNS")
	}
	out := []string{}
	for r.Next() {
		v := make([]any, len(cols))
		refs := make([]any, len(cols))
		for i := range v {
			refs[i] = &v[i]
		}
		if r.Scan(refs...) != nil {
			t.Fatal("D2_V3_FACT_SCAN")
		}
		var b strings.Builder
		for _, x := range v {
			switch v := x.(type) {
			case nil:
				b.WriteString("N;")
			case []byte:
				fmt.Fprintf(&b, "B%x;", v)
			default:
				fmt.Fprintf(&b, "%T:%q;", v, fmt.Sprint(v))
			}
		}
		out = append(out, b.String())
	}
	if r.Err() != nil || r.Close() != nil {
		t.Fatal("D2_V3_FACT_ROWS")
	}
	sort.Strings(out)
	return out
}
func d2LinuxFacts(t *testing.T, db *sql.DB, all bool) map[string][]string {
	t.Helper()
	r, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal("D2_V3_TABLES")
	}
	var names []string
	for r.Next() {
		var s string
		if r.Scan(&s) != nil {
			t.Fatal("D2_V3_TABLE_SCAN")
		}
		names = append(names, s)
	}
	if r.Err() != nil || r.Close() != nil {
		t.Fatal("D2_V3_TABLE_ROWS")
	}
	out := map[string][]string{"@schema": d2LinuxRows(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`)}
	for _, name := range names {
		if !all {
			switch name {
			case "sessions", "codex_oauth_sessions", "codex_oauth_refresh_states", "employee_self_sessions", "employee_self_credentials":
				continue
			}
		}
		out[name] = d2LinuxRows(t, db, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`"`)
	}
	return out
}

func TestLinuxSystemdBackupWorkerRealV3RehearsalCleanupAndRetention(t *testing.T) {
	fixture, planID, _, _ := newLinuxBackupFixture(t, true)
	var previous backupRunView
	for iteration := 0; iteration < 2; iteration++ {
		claim, err := fixture.worker.claimPlan(context.Background(), planID, 1, fixture.adminID)
		if err != nil || claim == nil {
			t.Fatalf("claim=%v err=%v", claim, err)
		}
		// Execute synchronously, with the real v3 codec and real service Open/Close.
		fixture.worker.execute(context.Background(), *claim)
		run, err := loadBackupRun(context.Background(), fixture.app.store.db, claim.RunID)
		if err != nil || run.Status != "succeeded" || run.RehearsalStatus != "succeeded" || run.ErrorCode != nil || !run.PackageRetained {
			t.Fatalf("run=%+v err=%v", run, err)
		}
		leftovers, err := filepath.Glob(filepath.Join(fixture.worker.output.path, ".cpa-cloud-rehearsal-*"))
		if err != nil || len(leftovers) != 0 {
			t.Fatalf("rehearsal leftovers=%v err=%v", leftovers, err)
		}
		if previous.ID != "" {
			old, err := loadBackupRun(context.Background(), fixture.app.store.db, previous.ID)
			if err != nil || old.PackageRetained || old.PackageDeletedAt == nil {
				t.Fatalf("retention=%+v err=%v", old, err)
			}
			if _, err := os.Lstat(filepath.Join(fixture.worker.output.path, previous.PackageName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old package remains: %v", err)
			}
		}
		previous = run
	}
}

func TestLinuxSystemdBackupWorkerRejectsPostStartupBindingChanges(t *testing.T) {
	for _, mutation := range []string{"canary", "key-id", "version", "authenticated-replacement"} {
		t.Run(mutation, func(t *testing.T) {
			fixture, planID, _, installation := newLinuxBackupFixture(t, false)
			path := filepath.Join(fixture.app.cfg.DataDir, masterKeyBindingFilename)
			encoded, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var binding masterKeyBinding
			if err := json.Unmarshal(encoded, &binding); err != nil {
				t.Fatal(err)
			}
			switch mutation {
			case "canary":
				binding.CanaryCiphertext = base64.RawStdEncoding.EncodeToString(make([]byte, 44))
			case "key-id":
				binding.KeyID = "different-installation-key"
			case "version":
				binding.KeyVersion++
			}
			encoded, err = json.Marshal(binding)
			if mutation == "authenticated-replacement" {
				encoded, err = createMasterKeyBinding(&installation)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			claim, err := fixture.worker.claimPlan(context.Background(), planID, 1, fixture.adminID)
			if err != nil || claim == nil {
				t.Fatalf("claim=%v err=%v", claim, err)
			}
			fixture.worker.execute(context.Background(), *claim)
			run, err := loadBackupRun(context.Background(), fixture.app.store.db, claim.RunID)
			if err != nil || run.Status != "failed" || run.ErrorCode == nil || *run.ErrorCode != "backup_create_failed" || run.PackageRetained {
				t.Fatalf("run=%+v err=%v", run, err)
			}
			if _, err := os.Lstat(filepath.Join(fixture.worker.output.path, claim.PackageName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("corrupt backup published: %v", err)
			}
		})
	}
}

func TestLinuxSystemdBackupRestoreRejectsChangedOrMissingInstallationCredential(t *testing.T) {
	fixture, _, credentialDir, installation := newLinuxBackupFixture(t, false)
	ref := fixture.keys.configured
	material := backup.KeyMaterial{ProviderID: ref.ProviderID, ProviderKind: ref.Kind, InstanceID: ref.InstanceID, KeyID: ref.KeyID, ProviderVersion: ref.Version, WrappingKey: fixture.keys.configuredKey, ValidateInstallationBinding: fixture.worker.validateInstallationBinding}
	packagePath := filepath.Join(fixture.root, "valid.cpacb")
	if _, err := backup.CreateWithKeyMaterial(context.Background(), fixture.app.cfg.DataDir, packagePath, material, "test"); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []string{"wrong-key", "wrong-version", "missing"} {
		t.Run(mutation, func(t *testing.T) {
			candidate := installation
			if mutation == "wrong-key" {
				candidate.Key[0] ^= 0xff
			}
			if mutation == "wrong-version" {
				candidate.KeyVersion++
			}
			writeLinuxSystemdCredential(t, credentialDir, candidate)
			if mutation == "missing" {
				if err := os.Remove(filepath.Join(credentialDir, masterKeyCredentialName)); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(fixture.root, "restore-"+mutation)
			if _, err := backup.RestoreWithKeyMaterial(context.Background(), packagePath, target, material); err == nil {
				t.Fatal("invalid installation credential restored")
			}
			if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("target published: %v", err)
			}
		})
	}
}
