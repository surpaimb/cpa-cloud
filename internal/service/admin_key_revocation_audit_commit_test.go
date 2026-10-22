// Independently authored from docs/admin-key-revocation-audit-contract.md.
// This test-only connector delegates actual SQLite work. Post-Commit error and
// cancellation modes are controlled adapters, not real I/O-failure evidence.
package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Deterministic regression for the exact original publication defect. This is
// a structural guard, not a claim to prove all possible Go races statically.
func TestAdminKeyRevocationAuditCommitFixturePublicationInvariant(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "admin_key_revocation_audit_commit_test.go", nil, 0)
	if err != nil {
		t.Fatal("AKRA_FIXTURE_SOURCE_PARSE")
	}
	rebinds, fullFixtures, appOpens := 0, 0, 0
	ast.Inspect(file, func(node ast.Node) bool {
		if assignment, ok := node.(*ast.AssignStmt); ok {
			for _, lhs := range assignment.Lhs {
				if db, ok := lhs.(*ast.SelectorExpr); ok && db.Sel.Name == "db" {
					if store, ok := db.X.(*ast.SelectorExpr); ok && store.Sel.Name == "store" {
						rebinds++
					}
				}
			}
		}
		if call, ok := node.(*ast.CallExpr); ok {
			if name, ok := call.Fun.(*ast.Ident); ok {
				if name.Name == "newAdminRevokeAuditFixture" {
					fullFixtures++
				}
				if name.Name == "Open" {
					appOpens++
				}
			}
		}
		return true
	})
	if rebinds != 0 || fullFixtures != 0 || appOpens != 0 {
		if rebinds == 2 && fullFixtures == 3 && appOpens == 0 {
			t.Log("AKRA_OLD_REBINDS_2_FULL_FIXTURES_3")
		}
		t.Fatal("AKRA_OLD_FIXTURE_PUBLICATION_INVARIANT")
	}
}

// Each authenticated target request owns its cancellation and transaction tag.
// No published App store pointer is replaced, even during cleanup.
type adminAuditCommitRequestKey struct{}

type adminAuditCommitRequest struct {
	owner  *adminAuditCommitConnector
	keyID  string
	serial uint64
	cancel context.CancelFunc
}

type adminAuditCommitConnector struct {
	base     driver.Driver
	dsn      string
	mode     string // immutable after construction
	targetID string
	inject   atomic.Bool

	requests, lastBegin, lastCommit   atomic.Uint64
	begins, realBegins, commits       atomic.Int64
	otherBegins, otherCommits         atomic.Int64
	active                            atomic.Int64
	committed, nativeError, cancelled atomic.Bool
	nativeCode                        atomic.Int64
}

func (d *adminAuditCommitConnector) Driver() driver.Driver { return d.base }

func (d *adminAuditCommitConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c, err := d.base.Open(d.dsn)
	if err != nil {
		return nil, err
	}
	for _, query := range []string{"PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		statement, err := c.Prepare(query)
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		_, err = statement.Exec(nil)
		closeErr := statement.Close()
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		if closeErr != nil {
			_ = c.Close()
			return nil, closeErr
		}
	}
	return &adminAuditCommitConn{Conn: c, owner: d}, nil
}

type adminAuditCommitConn struct {
	driver.Conn
	owner *adminAuditCommitConnector
}

func (c *adminAuditCommitConn) Begin() (driver.Tx, error) {
	c.owner.otherBegins.Add(1)
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	c.owner.active.Add(1)
	return &adminAuditCommitTx{Tx: tx, owner: c.owner}, nil
}

func (c *adminAuditCommitConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	request, _ := ctx.Value(adminAuditCommitRequestKey{}).(*adminAuditCommitRequest)
	if request == nil || request.owner != c.owner || request.keyID != c.owner.targetID {
		request = nil
		c.owner.otherBegins.Add(1)
	} else {
		c.owner.begins.Add(1)
		c.owner.lastBegin.Store(request.serial)
		if c.owner.inject.Load() && c.owner.mode == "cancel_begin" {
			request.cancel()
			c.owner.cancelled.Store(true)
			return nil, ctx.Err()
		}
	}
	var tx driver.Tx
	var err error
	if begin, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err = begin.BeginTx(ctx, opts)
	} else {
		tx, err = c.Conn.Begin()
	}
	if err != nil {
		return nil, err
	}
	if request != nil {
		c.owner.realBegins.Add(1)
	}
	c.owner.active.Add(1)
	return &adminAuditCommitTx{Tx: tx, owner: c.owner, request: request}, nil
}

type adminAuditCommitTx struct {
	driver.Tx
	owner   *adminAuditCommitConnector
	request *adminAuditCommitRequest
	done    atomic.Bool
}

func (tx *adminAuditCommitTx) Commit() error {
	if !tx.done.CompareAndSwap(false, true) {
		return errors.New("AKRA_ADAPTER_DOUBLE_FINISH")
	}
	defer tx.owner.active.Add(-1)
	if tx.request == nil {
		tx.owner.otherCommits.Add(1)
		return tx.Tx.Commit()
	}
	tx.owner.commits.Add(1)
	tx.owner.lastCommit.Store(tx.request.serial)
	if err := tx.Tx.Commit(); err != nil {
		tx.owner.nativeError.Store(true)
		var coded interface{ Code() int }
		if errors.As(err, &coded) {
			tx.owner.nativeCode.Store(int64(coded.Code()))
		}
		return err
	}
	tx.owner.committed.Store(true)
	if tx.owner.inject.Load() {
		switch tx.owner.mode {
		case "after_error":
			return errors.New("AKRA_CONTROLLED_AFTER_COMMIT")
		case "after_cancel":
			tx.request.cancel()
			tx.owner.cancelled.Store(true)
			return context.Canceled
		}
	}
	return nil
}

func (tx *adminAuditCommitTx) Rollback() error {
	if !tx.done.CompareAndSwap(false, true) {
		return errors.New("AKRA_ADAPTER_DOUBLE_FINISH")
	}
	defer tx.owner.active.Add(-1)
	return tx.Tx.Rollback()
}

type adminAuditCommitFixture struct {
	*adminRevokeAuditFixture
	control  *adminAuditCommitConnector
	db       *sql.DB
	handlers sync.WaitGroup
	finished chan struct{}
	closing  atomic.Bool
}

func newAdminAuditCommitFixture(t *testing.T, operation, mode string) (*adminAuditCommitFixture, keyView) {
	t.Helper()
	f := &adminAuditCommitFixture{
		adminRevokeAuditFixture: &adminRevokeAuditFixture{dir: t.TempDir()},
		finished:                make(chan struct{}, 16),
	}
	var bootstrap *store
	t.Cleanup(func() {
		f.closing.Store(true)
		if f.server != nil {
			f.server.Close()
		}
		f.handlers.Wait()
		if f.control != nil && f.control.active.Load() != 0 {
			t.Error("AKRA_CLEANUP_ACTIVE_TRANSACTION")
		}
		if f.app != nil {
			if f.app.Close() != nil {
				t.Error("AKRA_CLEANUP_APP_CLOSE")
			}
		} else if f.db != nil {
			if f.db.Close() != nil {
				t.Error("AKRA_CLEANUP_DB_CLOSE")
			}
		}
		if bootstrap != nil {
			if bootstrap.close() != nil {
				t.Error("AKRA_CLEANUP_BOOTSTRAP_CLOSE")
			}
		}
		if f.db != nil && f.db.PingContext(context.Background()) == nil {
			t.Error("AKRA_CLEANUP_DB_REMAINED_OPEN")
		}
	})
	if Initialize(t.Context(), f.dir, strings.NewReader("a-strong-preview-password\n")) != nil {
		t.Fatal("AKRA_COMMIT_FIXTURE_INIT")
	}
	var err error
	bootstrap, err = openStore(f.dir)
	if err != nil {
		t.Fatal("AKRA_COMMIT_FIXTURE_STORE")
	}
	cfg := Config{DataDir: f.dir, Listen: "127.0.0.1:0", Version: "isolated-admin-commit-test"}
	sec, err := loadSecretsForConfig(cfg)
	if err != nil {
		t.Fatal("AKRA_COMMIT_FIXTURE_SECRETS")
	}
	employeeID, err := newID("emp")
	if err != nil {
		t.Fatal("AKRA_COMMIT_FIXTURE_EMPLOYEE_ID")
	}
	keyID, err := newID("key")
	if err != nil {
		t.Fatal("AKRA_COMMIT_FIXTURE_KEY_ID")
	}
	selector, err := randomToken(12)
	if err != nil {
		t.Fatal("AKRA_COMMIT_FIXTURE_SELECTOR")
	}
	secret, err := randomToken(32)
	if err != nil {
		t.Fatal("AKRA_COMMIT_FIXTURE_SECRET")
	}
	// Owned fixture seeding is deliberately not Key-creation API acceptance.
	adminAuditExec(t, bootstrap.db, "INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES(?,'Commit fixture','active','all',1,?)", employeeID, utcNow())
	adminAuditExec(t, bootstrap.db, "INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES(?,?,'Commit fixture',?,?,1,?,?)",
		keyID, employeeID, selector, sec.digest("employee-key/v1\x00"+selector, secret), operation, utcNow())
	base := bootstrap.db.Driver()
	if bootstrap.close() != nil {
		t.Fatal("AKRA_COMMIT_BOOTSTRAP_CLOSE")
	}
	bootstrap = nil
	f.control = &adminAuditCommitConnector{
		base: base, dsn: filepath.Join(f.dir, "cpa-cloud.db"), mode: mode, targetID: keyID,
	}
	f.control.inject.Store(true)
	f.db = sql.OpenDB(f.control)
	f.db.SetMaxOpenConns(1)
	f.db.SetMaxIdleConns(1)
	if f.db.PingContext(t.Context()) != nil {
		t.Fatal("AKRA_COMMIT_ADAPTER_OPEN")
	}
	// This is the only store assignment: a fresh App composite, before the
	// server is published. No full App/coordinator constructor is invoked.
	f.app = &App{cfg: cfg, store: &store{db: f.db}, secrets: sec, logins: make(map[string]*loginAttempt)}
	runtimeCtx, runtimeCancel := context.WithCancel(context.Background())
	changeCtx, changeCancel := context.WithCancel(context.Background())
	f.app.accountPool = &accountPoolRuntime{
		app: f.app, ctx: runtimeCtx, cancel: runtimeCancel,
		changeContext: changeCtx, changeCancel: changeCancel,
		active:            make(map[*accountPoolLease]struct{}),
		maintenanceActive: make(map[*accountMaintenanceLease]struct{}),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", f.app.login)
	mux.HandleFunc("POST /admin/api/v1/keys/{id}/revoke", f.app.requireAdmin(func(w http.ResponseWriter, r *http.Request, session adminSession) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		request := &adminAuditCommitRequest{
			owner: f.control, keyID: r.PathValue("id"), serial: f.control.requests.Add(1), cancel: cancel,
		}
		f.app.revokeKey(w, r.WithContext(context.WithValue(ctx, adminAuditCommitRequestKey{}, request)), session)
	}, true))
	f.server = httptest.NewServer(requestMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.closing.Load() {
			writeAdminError(w, 503, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
		f.handlers.Add(1)
		target := r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/revoke")
		defer func() {
			f.handlers.Done()
			if target {
				f.finished <- struct{}{}
			}
		}()
		mux.ServeHTTP(w, r)
	})))
	f.cookie, f.csrf = loginTestAdmin(t, f.server.URL)
	if f.db.QueryRow("SELECT admin_id FROM sessions WHERE token_digest=?", sec.digest("admin-session/v1", f.cookie.Value)).Scan(&f.actor) != nil || f.actor == "" || f.actor == employeeID || f.actor == keyID {
		t.Fatal("AKRA_COMMIT_REAL_ADMIN")
	}
	if f.app.subscriptionExpiry != nil || f.app.subscriptionOneShot != nil || f.app.refresh != nil ||
		f.app.recovery != nil || f.app.scheduledTests != nil || f.app.channelMonitors != nil ||
		f.app.backupAutomation != nil || f.app.backgroundResponses != nil || f.app.healthTests != nil ||
		f.app.proxyTests != nil || f.app.governance != nil || f.app.usage != nil {
		t.Fatal("AKRA_COMMIT_BACKGROUND_CONSTRUCTOR")
	}
	f.ownership(t, 0, 0, 0, 0)
	return f, keyView{ID: keyID, Name: "Commit fixture", Key: "cpac_" + selector + "." + secret}
}

func (f *adminAuditCommitFixture) waitHTTP(t *testing.T) {
	t.Helper()
	select {
	case <-f.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("AKRA_COMMIT_HANDLER_DID_NOT_FINISH")
	}
}

func (f *adminAuditCommitFixture) ownership(t *testing.T, begins, realBegins, commits int64, serial uint64) {
	t.Helper()
	c := f.control
	if f.app.store.db != f.db || c.begins.Load() != begins || c.realBegins.Load() != realBegins ||
		c.commits.Load() != commits || c.otherBegins.Load() != 0 || c.otherCommits.Load() != 0 ||
		c.active.Load() != 0 || c.requests.Load() != serial || c.lastBegin.Load() != serial {
		t.Fatal("AKRA_TARGET_TRANSACTION_OWNERSHIP")
	}
	if (commits == 0 && c.lastCommit.Load() != 0) || (commits != 0 && c.lastCommit.Load() != serial) {
		t.Fatal("AKRA_TARGET_COMMIT_REQUEST")
	}
}

func (f *adminAuditCommitFixture) notification(t *testing.T, epoch uint64, prior context.Context, changed bool) {
	t.Helper()
	currentEpoch, current := f.app.accountPool.changeSnapshot()
	if current == nil || current.Err() != nil || prior == nil {
		t.Fatal("AKRA_NOTIFICATION_CONTEXT")
	}
	if changed {
		if currentEpoch != epoch+1 || prior.Err() != context.Canceled || current == prior {
			t.Fatal("AKRA_NOTIFICATION_POSITIVE")
		}
	} else if currentEpoch != epoch || prior.Err() != nil || current != prior {
		t.Fatal("AKRA_NOTIFICATION_NEGATIVE")
	}
}

func (f *adminAuditCommitFixture) retainedActor(t *testing.T, id string) {
	t.Helper()
	var count int
	if f.db.QueryRow("SELECT COUNT(*) FROM account_lifecycle_audit WHERE action='access_key.revoke' AND target_type='access_key' AND target_id=? AND actor_id=? AND result='succeeded'", id, f.actor).Scan(&count) != nil || count != 1 || f.auditCount(t, id) != 1 {
		t.Fatal("AKRA_COMMIT_RETAINED_REAL_ACTOR")
	}
}

func TestAdminKeyRevocationAuditCommitFixtureAuthenticationAndOwnership(t *testing.T) {
	f, k := newAdminAuditCommitFixture(t, "isolated-auth", "native")
	for _, tc := range []struct {
		name         string
		cookie       *http.Cookie
		csrf, origin string
		status       int
	}{
		{"anonymous", nil, "", f.server.URL, 401},
		{"wrong_csrf", f.cookie, "synthetic-invalid", f.server.URL, 403},
		{"foreign_origin", f.cookie, f.csrf, "http://foreign.invalid", 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := adminAuditFacts(t, f.db)
			r := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/keys/"+k.ID+"/revoke", "{}", tc.cookie, tc.csrf, tc.origin)
			adminAuditBody(t, r, tc.status, k.Key, f.cookie.Value, f.csrf)
			f.waitHTTP(t)
			f.ownership(t, 0, 0, 0, 0)
			if !reflect.DeepEqual(before, adminAuditFacts(t, f.db)) {
				t.Fatal("AKRA_COMMIT_AUTH_MUTATED")
			}
		})
	}
	epoch, notify := f.app.accountPool.changeSnapshot()
	adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key)
	f.waitHTTP(t)
	f.ownership(t, 1, 1, 1, 1)
	f.notification(t, epoch, notify, true)
	f.retainedActor(t, k.ID)
}

func TestAdminKeyRevocationAuditNativeCommit(t *testing.T) {
	for _, valid := range []bool{false, true} {
		name := "deferred_fk_refusal"
		if valid {
			name = "deferred_fk_positive"
		}
		t.Run(name, func(t *testing.T) {
			f, k := newAdminAuditCommitFixture(t, "audit-native-commit", "native")
			adminAuditExec(t, f.db, "CREATE TABLE akra_parent(id TEXT PRIMARY KEY)")
			adminAuditExec(t, f.db, "CREATE TABLE akra_child(id TEXT PRIMARY KEY,parent_id TEXT REFERENCES akra_parent(id) DEFERRABLE INITIALLY DEFERRED)")
			if valid {
				adminAuditExec(t, f.db, "INSERT INTO akra_parent VALUES('fixture-parent')")
			}
			adminAuditExec(t, f.db, "CREATE TRIGGER akra_commit_guard AFTER INSERT ON account_lifecycle_audit WHEN NEW.action='access_key.revoke' BEGIN INSERT INTO akra_child VALUES(NEW.id,'fixture-parent'); END")
			var foreignKeys int
			if f.db.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys) != nil || foreignKeys != 1 {
				t.Fatal("AKRA_NATIVE_FK_DISABLED")
			}
			f.ownership(t, 0, 0, 0, 0)
			before := adminAuditFacts(t, f.db)
			epoch, notify := f.app.accountPool.changeSnapshot()
			want := 503
			if valid {
				want = 200
			}
			adminAuditBody(t, f.revoke(t, k.ID), want, k.Key, f.cookie.Value, f.csrf)
			f.waitHTTP(t)
			f.ownership(t, 1, 1, 1, 1)
			f.notification(t, epoch, notify, valid)
			control := f.control
			if valid {
				if !control.committed.Load() || control.nativeError.Load() {
					t.Fatal("AKRA_NATIVE_POSITIVE")
				}
				f.retainedActor(t, k.ID)
			} else if control.committed.Load() || !control.nativeError.Load() || control.nativeCode.Load()&255 != 19 ||
				!reflect.DeepEqual(before, adminAuditFacts(t, f.db)) {
				t.Fatal("AKRA_NATIVE_REFUSAL_FACTS")
			}
		})
	}
}

func TestAdminKeyRevocationAuditUncertainCommit(t *testing.T) {
	for _, mode := range []string{"after_error", "after_cancel"} {
		t.Run(mode, func(t *testing.T) {
			f, k := newAdminAuditCommitFixture(t, "audit-uncertain", mode)
			epoch, notify := f.app.accountPool.changeSnapshot()
			adminAuditBody(t, f.revoke(t, k.ID), 503, k.Key, f.cookie.Value, f.csrf)
			f.waitHTTP(t)
			f.ownership(t, 1, 1, 1, 1)
			f.notification(t, epoch, notify, false)
			var revoked bool
			if f.db.QueryRow("SELECT revoked_at IS NOT NULL FROM access_keys WHERE id=?", k.ID).Scan(&revoked) != nil ||
				!revoked || !f.control.committed.Load() || f.control.nativeError.Load() {
				t.Fatal("AKRA_UNCERTAIN_COMMITTED_PAIR")
			}
			f.retainedActor(t, k.ID)
			if mode == "after_cancel" && !f.control.cancelled.Load() {
				t.Fatal("AKRA_UNCERTAIN_CANCEL_NOT_REACHED")
			}
			// The first handler has completed. Only this separate request disables
			// the controlled fault; immutable mode and DB ownership never change.
			f.control.inject.Store(false)
			before := adminAuditFacts(t, f.db)
			epoch, notify = f.app.accountPool.changeSnapshot()
			adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key)
			f.waitHTTP(t)
			f.ownership(t, 2, 2, 2, 2)
			f.notification(t, epoch, notify, true)
			if !reflect.DeepEqual(before, adminAuditFacts(t, f.db)) {
				t.Fatal("AKRA_UNCERTAIN_DUPLICATE")
			}
		})
	}
}

func TestAdminKeyRevocationAuditCancelledBegin(t *testing.T) {
	f, k := newAdminAuditCommitFixture(t, "audit-cancel-begin", "cancel_begin")
	before := adminAuditFacts(t, f.db)
	epoch, notify := f.app.accountPool.changeSnapshot()
	adminAuditBody(t, f.revoke(t, k.ID), 503, k.Key, f.cookie.Value, f.csrf)
	f.waitHTTP(t)
	f.ownership(t, 1, 0, 0, 1)
	f.notification(t, epoch, notify, false)
	if !f.control.cancelled.Load() || !reflect.DeepEqual(before, adminAuditFacts(t, f.db)) {
		t.Fatal("AKRA_CANCELLED_BEGIN_FACTS")
	}
}
