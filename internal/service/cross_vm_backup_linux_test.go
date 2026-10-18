//go:build linux && cpacloud_crossvm_acceptance && cpacloud_systemd_acceptance

package service

// Independently authored acceptance only. See docs/linux-cross-vm-backup-contract.md.
// Both production credential readers remain unchanged. Never print compared data.
import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/keypolicy"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/sys/unix"
)

const crossRecord = "synthetic-cross-vm-record-only"
const crossPassword = "synthetic-cross-vm-admin-only"
const crossSession = "synthetic-cross-vm-admin-session-only"
const crossCSRF = "synthetic-cross-vm-csrf-only"
const crossOAuth = "synthetic-cross-vm-oauth-only"
const crossSelector = "cross-public-selector"
const crossSecret = "synthetic-cross-vm-employee-secret-only"

func crossCheck(t *testing.T, ok bool, label string) {
	t.Helper()
	if !ok {
		t.Fatal(label)
	}
}

func TestCrossVMBackup(t *testing.T) {
	t.Log("CROSS_ENTRY")
	uid, err := strconv.Atoi(os.Getenv("CPA_CROSS_UID"))
	crossCheck(t, err == nil && uid != 0 && os.Geteuid() == uid && os.Getenv("INVOCATION_ID") != "", "cross-identity-failed")
	root := os.Getenv("CPA_CROSS_ROOT")
	crossCheck(t, filepath.IsAbs(root) && filepath.Clean(root) == root, "cross-root-failed")
	assertActivationPrivate(t, root, true)
	crossNoNetwork(t)
	mode := os.Getenv("CPA_CROSS_CASE")
	if mode == "source" {
		crossSource(t, root)
		return
	}
	if mode == "reopen-one" || mode == "reopen-two" {
		crossReopen(t, filepath.Join(root, "restored"))
		t.Log("CROSS_REOPEN_OK")
		return
	}
	want, known := map[string]string{
		"restore": "", "missing-master": "binding", "wrong-master": "binding",
		"swapped-master": "binding", "swapped-both": "binding", "master-instance": "binding",
		"master-id": "binding", "master-version": "binding", "binding-instance": "binding",
		"binding-id": "binding", "binding-version": "binding", "binding-canary": "binding",
		"missing-backup": "provider", "swapped-backup": "provider", "same-value": "provider",
		"backup-instance": "provider", "overlap-id": "provider", "wrong-backup": "package",
		"backup-id": "package", "backup-version": "package", "header": "package",
		"ciphertext": "package", "truncate-header": "package", "truncate-body": "package",
		"publish-binding": "publish", "publish-credential": "publish", "publish-parent": "parent",
		"target-exists": "target",
	}[mode]
	crossCheck(t, known, "cross-case-unknown")
	target := filepath.Join(root, "parent", "restored")
	crossCheck(t, os.Mkdir(filepath.Dir(target), 0o700) == nil, "cross-parent-create-failed")
	var prior []byte
	if mode == "target-exists" {
		crossCheck(t, os.Mkdir(target, 0o700) == nil, "cross-existing-create-failed")
		prior = []byte("public-existing-target-sentinel")
		crossCheck(t, os.WriteFile(filepath.Join(target, "public.sentinel"), prior, 0o600) == nil, "cross-existing-write-failed")
	} else {
		assertActivationAbsent(t, target)
	}
	material, stage := activationMaterial(filepath.Join(root, "seed"))
	defer clearBytes(material.WrappingKey[:])
	if stage != "" {
		crossCheck(t, stage == want, "cross-preparation-stage-failed")
		if stage == "binding" {
			// Production Open must authenticate installation material before SQLite.
			app, openErr := Open(context.Background(), activationConfig(filepath.Join(root, "seed")))
			if app != nil {
				_ = app.Close()
			}
			crossCheck(t, openErr != nil && app == nil, "cross-invalid-installation-opened")
			assertActivationAbsent(t, filepath.Join(root, "seed", "cpa-cloud.db"))
		}
		assertActivationAbsent(t, target)
		t.Log("CROSS_REJECT_" + stage)
		return
	}
	crossCheck(t, want != "binding" && want != "provider", "cross-invalid-credential-admitted")
	t.Log("CROSS_MATERIAL_OK")
	input := filepath.Join(root, "input.cpacb")
	if want == "package" {
		_, verifyErr := backup.VerifyWithKeyMaterial(context.Background(), input, material)
		_, restoreErr := backup.RestoreWithKeyMaterial(context.Background(), input, target, material)
		crossCheck(t, verifyErr != nil && restoreErr != nil, "cross-invalid-package-admitted")
		assertActivationAbsent(t, target)
		t.Log("CROSS_REJECT_package")
		return
	}
	info, verifyErr := backup.VerifyWithKeyMaterial(context.Background(), input, material)
	crossCheck(t, verifyErr == nil, "cross-package-verify-failed")
	crossInfo(t, info)
	t.Log("CROSS_PACKAGE_OK")
	if want == "target" {
		_, err := backup.RestoreWithKeyMaterial(context.Background(), input, target, material)
		current, readErr := os.ReadFile(filepath.Join(target, "public.sentinel"))
		entries, listErr := os.ReadDir(target)
		crossCheck(t, err != nil && readErr == nil && bytes.Equal(current, prior) && listErr == nil && len(entries) == 1, "cross-existing-target-changed")
		t.Log("CROSS_REJECT_target")
		return
	}
	validator := material.ValidateInstallationBinding
	calls, injected := 0, false
	material.ValidateInstallationBinding = func(encoded []byte) error {
		calls++
		if (mode == "publish-binding" || mode == "publish-credential") && calls == 2 {
			if mode == "publish-binding" {
				// Replace the actual staged file, not the in-memory validator input.
				entries, err := os.ReadDir(filepath.Dir(target))
				crossCheck(t, err == nil, "cross-stage-inspect-failed")
				found := 0
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), ".cpa-cloud-restore-stage-") {
						path := filepath.Join(filepath.Dir(target), entry.Name(), masterKeyBindingFilename)
						crossCheck(t, os.Rename(path, filepath.Join(root, "staged-binding-before")) == nil, "cross-binding-replace-failed")
						changed := append([]byte(nil), encoded...)
						changed[0] ^= 1
						crossCheck(t, os.WriteFile(path, changed, 0o600) == nil, "cross-binding-replace-failed")
						clearBytes(changed)
						found++
					}
				}
				crossCheck(t, found == 1, "cross-stage-count-failed")
			} else {
				path := filepath.Join(root, "empty-credential-source")
				crossCheck(t, os.Mkdir(path, 0o700) == nil && os.Setenv("CREDENTIALS_DIRECTORY", path) == nil, "cross-credential-source-replace-failed")
			}
			injected = true
		}
		err := validator(encoded) // unchanged production reader, including final reload
		if mode == "publish-parent" && calls == 1 && err == nil {
			parent := filepath.Dir(target)
			crossCheck(t, os.Rename(parent, parent+"-before") == nil && os.Mkdir(parent, 0o700) == nil, "cross-parent-replace-failed")
			injected = true
		}
		return err
	}
	if want == "parent" {
		crossCheck(t, os.WriteFile(filepath.Join(filepath.Dir(target), "public.sentinel"), []byte("public-parent-sentinel"), 0o600) == nil, "cross-parent-sentinel-failed")
	}
	info, restoreErr := backup.RestoreWithKeyMaterial(context.Background(), input, target, material)
	if want == "publish" || want == "parent" {
		crossCheck(t, injected && restoreErr != nil, "cross-publish-rejection-failed")
		assertActivationAbsent(t, target)
		if want == "parent" {
			crossCheck(t, calls == 1, "cross-parent-rejection-stage-failed")
			previous := filepath.Dir(target) + "-before"
			content, err := os.ReadFile(filepath.Join(previous, "public.sentinel"))
			crossCheck(t, err == nil && string(content) == "public-parent-sentinel", "cross-parent-old-data-changed")
			assertActivationAbsent(t, filepath.Join(previous, "restored"))
		} else {
			crossCheck(t, calls == 2, "cross-publish-rejection-stage-failed")
		}
		t.Log("CROSS_REJECT_" + want)
		return
	}
	crossCheck(t, restoreErr == nil && calls == 2, "cross-restore-failed")
	crossInfo(t, info)
	assertActivationPrivate(t, target, true)
	for _, name := range []string{"cpa-cloud.db", masterKeyBindingFilename} {
		assertActivationPrivate(t, filepath.Join(target, name), false)
	}
	assertActivationAbsent(t, filepath.Join(target, rootKeyFilename))
	crossCheck(t, os.Rename(target, filepath.Join(root, "restored")) == nil, "cross-positive-placement-failed")
	t.Log("CROSS_RESTORE_OK")
}

func crossNoNetwork(t *testing.T) {
	t.Helper()
	ns, err := os.Readlink("/proc/self/ns/net")
	crossCheck(t, err == nil && os.Getenv("CPA_CROSS_HOST_NETNS") != "" && ns != os.Getenv("CPA_CROSS_HOST_NETNS"), "cross-netns-failed")
	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		fd, err := unix.Socket(family, unix.SOCK_STREAM, 0)
		if err == nil {
			_ = unix.Close(fd)
		}
		crossCheck(t, errors.Is(err, unix.EAFNOSUPPORT) || errors.Is(err, unix.EPERM), "cross-socket-denial-failed")
	}
	for _, name := range []string{"tcp", "tcp6", "udp", "udp6"} {
		value, err := os.ReadFile("/proc/self/net/" + name)
		crossCheck(t, err == nil && len(strings.Split(strings.TrimSpace(string(value)), "\n")) == 1, "cross-unexpected-socket")
	}
}

func crossSource(t *testing.T, root string) {
	ctx := context.Background()
	cfg := activationConfig(filepath.Join(root, "data"))
	cfg.Version = os.Getenv("CPA_CROSS_SHA")
	crossCheck(t, len(cfg.Version) == 40 && InitializeWithConfig(ctx, cfg, strings.NewReader(crossPassword+"\n")) == nil, "cross-source-init-failed")
	app, err := Open(ctx, cfg)
	crossCheck(t, err == nil, "cross-source-open-failed")
	closed := false
	defer func() {
		if !closed {
			_ = app.Close()
		}
	}()
	crossWorkers(t, app)
	ciphertext, err := app.secrets.encryptCredential("cross-record", crossRecord)
	crossCheck(t, err == nil, "cross-source-encrypt-failed")
	_, err = app.store.db.Exec(`CREATE TABLE cross_vm_acceptance(id INTEGER PRIMARY KEY, credential BLOB NOT NULL)`)
	crossCheck(t, err == nil, "cross-source-record-failed")
	_, err = app.store.db.Exec(`INSERT INTO cross_vm_acceptance VALUES(1,?)`, ciphertext)
	crossCheck(t, err == nil, "cross-source-record-failed")
	var admin string
	crossCheck(t, app.store.db.QueryRow(`SELECT id FROM admins WHERE username='admin'`).Scan(&admin) == nil, "cross-source-admin-failed")
	_, err = app.store.db.Exec(`INSERT INTO sessions(id,admin_id,token_digest,csrf_token,expires_at,created_at) VALUES('cross-session',?,?,?,?,?)`,
		admin, app.secrets.digest("admin-session/v1", crossSession), crossCSRF, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), utcNow())
	crossCheck(t, err == nil && crossSessionStatus(app) == 200, "cross-source-session-failed")
	oauthCiphertext, err := app.secrets.encryptCodexOAuthSession("cross-oauth", []byte(crossOAuth))
	crossCheck(t, err == nil, "cross-source-session-failed")
	_, err = app.store.db.Exec(`INSERT INTO codex_oauth_sessions(id,operation_id,admin_id,admin_session_id,state_digest,secret_ciphertext,name,expires_at,created_at,status) VALUES('cross-oauth','cross-oauth-operation',?,'cross-session',?,?,'Synthetic',?,?,'pending')`,
		admin, app.secrets.digest("cross-oauth-state", crossOAuth), oauthCiphertext, time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), utcNow())
	crossCheck(t, err == nil, "cross-source-session-failed")
	crossSeedPolicy(t, app)
	crossPermissions(t, app)
	material, stage := activationMaterial(cfg.DataDir)
	defer clearBytes(material.WrappingKey[:])
	crossCheck(t, stage == "", "cross-source-material-failed")
	output := filepath.Join(root, "output.cpacb")
	info, err := backup.CreateWithKeyMaterial(ctx, cfg.DataDir, output, material, cfg.Version)
	crossCheck(t, err == nil, "cross-source-create-failed")
	crossInfo(t, info)
	info, err = backup.VerifyWithKeyMaterial(ctx, output, material)
	crossCheck(t, err == nil, "cross-source-verify-failed")
	crossInfo(t, info)
	crossCheck(t, app.Close() == nil, "cross-source-close-failed")
	closed = true
	t.Log("CROSS_SOURCE_OK")
}

func crossSeedPolicy(t *testing.T, app *App) {
	t.Helper()
	ctx := context.Background()
	tx, err := app.store.db.BeginTx(ctx, nil)
	crossCheck(t, err == nil, "cross-policy-seed-failed")
	defer tx.Rollback()
	ciphertext, err := app.secrets.encryptCredential("cross-upstream", crossRecord)
	crossCheck(t, err == nil, "cross-policy-seed-failed")
	_, err = tx.Exec(`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('cross-employee','Synthetic','active','all',1,?)`, utcNow())
	crossCheck(t, err == nil, "cross-policy-seed-failed")
	_, err = tx.Exec(`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES('cross-upstream','Synthetic','openai-compatible','https://unused.invalid',1,?,1,1,?)`, ciphertext, utcNow())
	crossCheck(t, err == nil, "cross-policy-seed-failed")
	for _, model := range []string{"cross-allowed", "cross-denied"} {
		_, err = tx.Exec(`INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES(?,'cross-upstream',?,1,?)`, model, model, utcNow())
		crossCheck(t, err == nil, "cross-policy-seed-failed")
	}
	_, err = tx.Exec(`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('cross-key','cross-employee','Synthetic',?,?,1,'cross-operation',?)`, crossSelector, app.secrets.digest("employee-key/v1\x00"+crossSelector, crossSecret), utcNow())
	crossCheck(t, err == nil, "cross-policy-seed-failed")
	_, err = keypolicy.CreateTx(ctx, tx, "cross-key", keypolicy.Replacement{ProtocolMode: "selected", Protocols: []keypolicy.ClientProtocol{keypolicy.ProtocolOpenAIChat}, ModelMode: "selected", Models: []string{"cross-allowed"}, SourceMode: "all", SourceCIDRs: []string{}, AccountGroupMode: "all", AccountGroupIDs: []string{}}, time.Now())
	crossCheck(t, err == nil && tx.Commit() == nil, "cross-policy-seed-failed")
}

func crossPermissions(t *testing.T, app *App) {
	t.Helper()
	auth, ok := app.lookupEmployeeKey(context.Background(), "cpac_"+crossSelector+"."+crossSecret)
	crossCheck(t, ok && auth.EmployeeID == "cross-employee" && auth.KeyID == "cross-key", "cross-key-authentication-failed")
	_, allowed := authorizeKeyPolicy(auth, keypolicy.ProtocolOpenAIChat, "cross-allowed")
	_, denied := authorizeKeyPolicy(auth, keypolicy.ProtocolOpenAIChat, "cross-denied")
	_, protocol := authorizeKeyPolicy(auth, keypolicy.ProtocolAnthropicMessages, "cross-allowed")
	crossCheck(t, allowed == nil && denied != nil && protocol != nil, "cross-policy-permission-failed")
}

func crossSessionStatus(app *App) int {
	r := httptest.NewRequest(http.MethodGet, "http://localhost/admin/api/v1/session", nil)
	r.AddCookie(&http.Cookie{Name: adminCookieName, Value: crossSession})
	w := httptest.NewRecorder()
	app.Handler().ServeHTTP(w, r)
	return w.Code
}

func crossWorkers(t *testing.T, app *App) {
	t.Helper()
	crossCheck(t, app.cfg.backupAutomationRehearsal && !app.cfg.ExperimentalCodexMembership && !app.cfg.AccountRecoveryEnabled &&
		!app.cfg.ScheduledTestsEnabled && !app.cfg.ChannelMonitorsEnabled && !app.cfg.AutomatedBackupsEnabled &&
		app.backupAutomation == nil && app.backgroundResponses == nil, "cross-workers-enabled")
}

func crossReopen(t *testing.T, directory string) {
	t.Helper()
	app, err := Open(context.Background(), activationConfig(directory))
	crossCheck(t, err == nil, "cross-reopen-failed")
	closed := false
	defer func() {
		if !closed {
			_ = app.Close()
		}
	}()
	crossWorkers(t, app)
	var ciphertext, hash []byte
	var admins, sessions, oauth int
	err = app.store.db.QueryRow(`SELECT credential FROM cross_vm_acceptance WHERE id=1`).Scan(&ciphertext)
	plaintext, decryptErr := app.secrets.decryptCredential("cross-record", ciphertext)
	crossCheck(t, err == nil && decryptErr == nil && plaintext == crossRecord, "cross-record-decrypt-failed")
	crossCheck(t, app.store.db.QueryRow(`SELECT COUNT(*) FROM admins`).Scan(&admins) == nil && admins == 1 &&
		app.store.db.QueryRow(`SELECT password_hash FROM admins WHERE username='admin'`).Scan(&hash) == nil &&
		bcrypt.CompareHashAndPassword(hash, []byte(crossPassword)) == nil, "cross-admin-state-failed")
	crossCheck(t, app.store.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions) == nil && sessions == 0 &&
		app.store.db.QueryRow(`SELECT COUNT(*) FROM codex_oauth_sessions`).Scan(&oauth) == nil && oauth == 0 &&
		crossSessionStatus(app) == 401, "cross-session-cleanup-failed")
	crossPermissions(t, app)
	crossNoNetwork(t)
	crossCheck(t, app.Close() == nil, "cross-reopen-close-failed")
	closed = true
}

func crossInfo(t *testing.T, info backup.Info) {
	t.Helper()
	assertActivationV3(t, info)
	crossCheck(t, info.SourceVersion == os.Getenv("CPA_CROSS_SHA"), "cross-source-version-failed")
}
