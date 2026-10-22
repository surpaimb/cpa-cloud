// Independently authored from docs/admin-key-revocation-audit-contract.md.
// This test-only connector delegates actual SQLite work. Post-Commit error and
// cancellation modes are controlled adapters, not real I/O-failure evidence.
package service

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

type adminAuditCommitConnector struct {
	base                              driver.Driver
	dsn, mode                         string
	commits                           atomic.Int64
	committed, nativeError, cancelled atomic.Bool
	nativeCode                        atomic.Int64
	cancel                            context.CancelFunc // accessed only by the sequential server request
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
	// Preserve real FK behavior on this newly opened, otherwise unmodified
	// SQLite connection. No product driver registration or dependency changes.
	s, err := c.Prepare("PRAGMA foreign_keys=ON")
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	_, err = s.Exec(nil)
	closeErr := s.Close()
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	if closeErr != nil {
		_ = c.Close()
		return nil, closeErr
	}
	return &adminAuditCommitConn{Conn: c, owner: d}, nil
}

type adminAuditCommitConn struct {
	driver.Conn
	owner *adminAuditCommitConnector
}

func (c *adminAuditCommitConn) Begin() (driver.Tx, error) {
	tx, err := c.Conn.Begin()
	if err != nil {
		return nil, err
	}
	return &adminAuditCommitTx{Tx: tx, owner: c.owner}, nil
}

func (c *adminAuditCommitConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if c.owner.mode == "cancel_begin" {
		c.owner.cancel()
		c.owner.cancelled.Store(true)
		return nil, ctx.Err()
	}
	if begin, ok := c.Conn.(driver.ConnBeginTx); ok {
		tx, err := begin.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &adminAuditCommitTx{Tx: tx, owner: c.owner}, nil
	}
	return c.Begin()
}

type adminAuditCommitTx struct {
	driver.Tx
	owner *adminAuditCommitConnector
}

func (tx *adminAuditCommitTx) Commit() error {
	tx.owner.commits.Add(1)
	if err := tx.Tx.Commit(); err != nil {
		tx.owner.nativeError.Store(true)
		var coded interface{ Code() int }
		if errors.As(err, &coded) {
			tx.owner.nativeCode.Store(int64(coded.Code()))
		}
		return err
	}
	tx.owner.committed.Store(true)
	switch tx.owner.mode {
	case "after_error":
		return errors.New("AKRA_CONTROLLED_AFTER_COMMIT")
	case "after_cancel":
		tx.owner.cancel()
		tx.owner.cancelled.Store(true)
		return context.Canceled
	default:
		return nil
	}
}

func adminAuditAdaptCommit(t *testing.T, f *adminRevokeAuditFixture, mode string) *adminAuditCommitConnector {
	t.Helper()
	original := f.app.store.db
	control := &adminAuditCommitConnector{base: original.Driver(), dsn: filepath.Join(f.dir, "cpa-cloud.db"), mode: mode}
	db := sql.OpenDB(control)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	f.app.store.db = db
	f.server.Close()
	next := f.app.Handler()
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/revoke") {
			ctx, cancel := context.WithCancel(r.Context())
			defer cancel()
			control.cancel = cancel
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	}))
	t.Cleanup(func() { f.server.Close(); f.app.store.db = original; _ = db.Close() })
	return control
}

func TestAdminKeyRevocationAuditNativeCommit(t *testing.T) {
	for _, valid := range []bool{false, true} {
		name := "deferred_fk_refusal"
		if valid {
			name = "deferred_fk_positive"
		}
		t.Run(name, func(t *testing.T) {
			f := newAdminRevokeAuditFixture(t)
			k := f.key(t, "audit-native-commit")
			adminAuditExec(t, f.app.store.db, `CREATE TABLE akra_parent(id TEXT PRIMARY KEY)`)
			adminAuditExec(t, f.app.store.db, `CREATE TABLE akra_child(id TEXT PRIMARY KEY,parent_id TEXT REFERENCES akra_parent(id) DEFERRABLE INITIALLY DEFERRED)`)
			if valid {
				adminAuditExec(t, f.app.store.db, `INSERT INTO akra_parent VALUES('fixture-parent')`)
			}
			adminAuditExec(t, f.app.store.db, `CREATE TRIGGER akra_commit_guard AFTER INSERT ON account_lifecycle_audit WHEN NEW.action='access_key.revoke' BEGIN INSERT INTO akra_child VALUES(NEW.id,'fixture-parent'); END`)
			control := adminAuditAdaptCommit(t, f, "native")
			var foreignKeys int
			if f.app.store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys) != nil || foreignKeys != 1 {
				t.Fatal("AKRA_NATIVE_FK_DISABLED")
			}
			before := adminAuditFacts(t, f.app.store.db)
			_, notify := f.app.accountPool.changeSnapshot()
			want := 503
			if valid {
				want = 200
			}
			adminAuditBody(t, f.revoke(t, k.ID), want, k.Key, f.cookie.Value, f.csrf)
			if control.commits.Load() != 1 {
				t.Fatal("AKRA_NATIVE_COMMIT_COUNT")
			}
			if valid {
				if !control.committed.Load() || control.nativeError.Load() || notify.Err() == nil || f.auditCount(t, k.ID) != 1 {
					t.Fatal("AKRA_NATIVE_POSITIVE")
				}
			} else {
				if control.committed.Load() || !control.nativeError.Load() || control.nativeCode.Load()&255 != 19 || notify.Err() != nil || !reflect.DeepEqual(before, adminAuditFacts(t, f.app.store.db)) {
					t.Fatal("AKRA_NATIVE_REFUSAL_FACTS")
				}
			}
		})
	}
}

func TestAdminKeyRevocationAuditUncertainCommit(t *testing.T) {
	for _, mode := range []string{"after_error", "after_cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := newAdminRevokeAuditFixture(t)
			k := f.key(t, "audit-uncertain")
			control := adminAuditAdaptCommit(t, f, mode)
			_, notify := f.app.accountPool.changeSnapshot()
			adminAuditBody(t, f.revoke(t, k.ID), 503, k.Key, f.cookie.Value, f.csrf)
			var revoked bool
			if f.app.store.db.QueryRow(`SELECT revoked_at IS NOT NULL FROM access_keys WHERE id=?`, k.ID).Scan(&revoked) != nil || !revoked || f.auditCount(t, k.ID) != 1 || !control.committed.Load() || control.nativeError.Load() || control.commits.Load() != 1 || notify.Err() != nil {
				t.Fatal("AKRA_UNCERTAIN_COMMITTED_PAIR")
			}
			if mode == "after_cancel" && !control.cancelled.Load() {
				t.Fatal("AKRA_UNCERTAIN_CANCEL_NOT_REACHED")
			}
			// This is a separate authorized request, not a server automatic retry.
			control.mode = "native"
			before := adminAuditFacts(t, f.app.store.db)
			adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key)
			if control.commits.Load() != 2 || !reflect.DeepEqual(before, adminAuditFacts(t, f.app.store.db)) {
				t.Fatal("AKRA_UNCERTAIN_DUPLICATE")
			}
		})
	}
}

func TestAdminKeyRevocationAuditCancelledBegin(t *testing.T) {
	f := newAdminRevokeAuditFixture(t)
	k := f.key(t, "audit-cancel-begin")
	control := adminAuditAdaptCommit(t, f, "cancel_begin")
	before := adminAuditFacts(t, f.app.store.db)
	_, notify := f.app.accountPool.changeSnapshot()
	adminAuditBody(t, f.revoke(t, k.ID), 503, k.Key, f.cookie.Value, f.csrf)
	if !control.cancelled.Load() || control.commits.Load() != 0 || notify.Err() != nil || !reflect.DeepEqual(before, adminAuditFacts(t, f.app.store.db)) {
		t.Fatal("AKRA_CANCELLED_BEGIN_FACTS")
	}
}
