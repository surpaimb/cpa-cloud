// Independently authored from docs/admin-key-revocation-audit-contract.md.
// Owned temporary SQLite and synthetic HTTP only; no reference-project code.
package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type adminRevokeAuditFixture struct {
	app    *App
	server *httptest.Server
	dir    string
	cookie *http.Cookie
	csrf   string
	actor  string
}

func newAdminRevokeAuditFixture(t *testing.T) *adminRevokeAuditFixture {
	t.Helper()
	f := &adminRevokeAuditFixture{dir: t.TempDir()}
	if err := Initialize(context.Background(), f.dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal("AKRA_FIXTURE_INIT")
	}
	app, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, Version: "admin-revoke-audit-test"})
	if err != nil {
		t.Fatal("AKRA_FIXTURE_OPEN")
	}
	f.app = app
	f.server = httptest.NewServer(app.Handler())
	f.cookie, f.csrf = loginTestAdmin(t, f.server.URL)
	if err := app.store.db.QueryRow(`SELECT admin_id FROM sessions WHERE token_digest=?`, app.secrets.digest("admin-session/v1", f.cookie.Value)).Scan(&f.actor); err != nil || f.actor == "" {
		t.Fatal("AKRA_REAL_ADMIN_ID")
	}
	t.Cleanup(func() { f.server.Close(); _ = f.app.Close() })
	return f
}

func (f *adminRevokeAuditFixture) key(t *testing.T, operation string) keyView {
	t.Helper()
	e := selfCreateEmployee(t, f.server.URL, f.cookie, f.csrf)
	return createTestKey(t, f.server.URL, e.ID, operation, f.cookie, f.csrf)
}

func (f *adminRevokeAuditFixture) revoke(t *testing.T, id string) *http.Response {
	t.Helper()
	return requestJSON(t, "POST", f.server.URL+"/admin/api/v1/keys/"+id+"/revoke", `{}`, f.cookie, f.csrf, f.server.URL)
}

func (f *adminRevokeAuditFixture) auditCount(t *testing.T, id string) int {
	t.Helper()
	var count int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_lifecycle_audit WHERE action='access_key.revoke' AND target_type='access_key' AND target_id=?`, id).Scan(&count); err != nil {
		t.Fatal("AKRA_AUDIT_READ")
	}
	return count
}

func TestAdminKeyRevocationAuditFirstTransition(t *testing.T) {
	f := newAdminRevokeAuditFixture(t)
	k := f.key(t, "audit-first")
	if f.auditCount(t, k.ID) != 0 {
		t.Fatal("AKRA_INITIAL_AUDIT")
	}
	r := f.revoke(t, k.ID)
	if r.StatusCode != http.StatusOK {
		r.Body.Close()
		t.Fatal("AKRA_FIRST_HTTP")
	}
	r.Body.Close()
	var revoked bool
	if err := f.app.store.db.QueryRow(`SELECT revoked_at IS NOT NULL FROM access_keys WHERE id=?`, k.ID).Scan(&revoked); err != nil || !revoked {
		t.Fatal("AKRA_FIRST_STATE")
	}
	if f.auditCount(t, k.ID) != 1 {
		t.Fatal("ADMIN_KEY_REVOKE_AUDIT_MISSING")
	}
}

func adminAuditExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal("AKRA_FIXTURE_SQL")
	}
}

// Full typed logical rows, including empty tables, never emitted in diagnostics.
// Sorting encoded rows avoids assuming that every table has a rowid.
func adminAuditFacts(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal("AKRA_FACT_TABLES")
	}
	var tables []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			t.Fatal("AKRA_FACT_NAME")
		}
		tables = append(tables, name)
	}
	if rows.Err() != nil || rows.Close() != nil {
		t.Fatal("AKRA_FACT_TABLE_CLOSE")
	}
	facts := map[string]string{}
	for _, name := range tables {
		r, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`)
		if err != nil {
			t.Fatal("AKRA_FACT_ROWS")
		}
		columns, err := r.Columns()
		if err != nil {
			t.Fatal("AKRA_FACT_COLUMNS")
		}
		encoded := []string{}
		for r.Next() {
			values, dest := make([]any, len(columns)), make([]any, len(columns))
			for i := range values {
				dest[i] = &values[i]
			}
			if r.Scan(dest...) != nil {
				t.Fatal("AKRA_FACT_SCAN")
			}
			typed := make([]any, 0, len(values)*2)
			for _, value := range values {
				typed = append(typed, fmt.Sprintf("%T", value), value)
			}
			data, err := json.Marshal(typed)
			if err != nil {
				t.Fatal("AKRA_FACT_ENCODE")
			}
			encoded = append(encoded, string(data))
		}
		if r.Err() != nil || r.Close() != nil {
			t.Fatal("AKRA_FACT_CLOSE")
		}
		sort.Strings(encoded)
		data, _ := json.Marshal(encoded)
		facts[name] = string(data)
	}
	return facts
}

func adminAuditSeed(t *testing.T, f *adminRevokeAuditFixture, count int, future bool) {
	t.Helper()
	stamp := "2020-01-01T00:00:00Z"
	if future {
		stamp = "2099-01-01T00:00:00Z"
	}
	tx, err := f.app.store.db.Begin()
	if err != nil {
		t.Fatal("AKRA_SEED_BEGIN")
	}
	defer tx.Rollback()
	for i := 0; i < count; i++ {
		if _, err := tx.Exec(`INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,'model.update','model','synthetic-model','succeeded',?)`, fmt.Sprintf("audit-fixture-%04d", i), f.actor, stamp); err != nil {
			t.Fatal("AKRA_SEED_INSERT")
		}
	}
	if tx.Commit() != nil {
		t.Fatal("AKRA_SEED_COMMIT")
	}
}

func adminAuditInstallSlot(t *testing.T, f *adminRevokeAuditFixture, id, state string) {
	t.Helper()
	var owner string
	if f.app.store.db.QueryRow(`SELECT employee_id FROM access_keys WHERE id=?`, id).Scan(&owner) != nil {
		t.Fatal("AKRA_SLOT_OWNER")
	}
	var fingerprint, armed, issued, cancelled, revision any
	if state == "armed" || state == "issued" {
		fingerprint = make([]byte, 32)
		armed = "2020-02-01T00:00:00Z"
		revision = 1
	}
	if state == "issued" {
		issued = "2020-02-02T00:00:00Z"
	}
	if state == "cancelled" {
		cancelled = "2020-02-03T00:00:00Z"
	}
	// Inactive slots have no activation snapshot, matching the real slot API
	// and the PR100 validation retained by the integration.
	adminAuditExec(t, f.app.store.db, `INSERT INTO employee_self_key_slots(employee_id,key_id,state,arm_fingerprint,arm_employee_revision,arm_key_policy_revision,arm_governance_revision,arm_budget_revision,created_at,armed_at,issued_at,cancelled_at) VALUES(?,?,?,?,?,?,?,?,'2020-01-01T00:00:00Z',?,?,?)`, owner, id, state, fingerprint, revision, revision, revision, revision, armed, issued, cancelled)
}

func adminAuditBody(t *testing.T, r *http.Response, status int, forbidden ...string) []byte {
	t.Helper()
	defer r.Body.Close()
	data, err := io.ReadAll(r.Body)
	if err != nil || r.StatusCode != status {
		t.Fatal("AKRA_HTTP_RESULT")
	}
	for _, value := range forbidden {
		if value != "" && strings.Contains(string(data), value) {
			t.Fatal("AKRA_CANARY_ECHO")
		}
	}
	return data
}

func (f *adminRevokeAuditFixture) reader(t *testing.T, id, actor string) {
	t.Helper()
	r := requestJSON(t, "GET", f.server.URL+"/admin/api/v1/audit/events?sources=account_lifecycle&action=access_key.revoke&target_type=access_key&target_id="+id+"&actor_id="+actor, "", f.cookie, "", f.server.URL)
	data := adminAuditBody(t, r, 200, f.cookie.Value, f.csrf)
	var page adminAuditPageView
	if json.Unmarshal(data, &page) != nil || len(page.Items) != 1 {
		t.Fatal("AKRA_READER_COUNT")
	}
	e := page.Items[0]
	if e.Source != "account_lifecycle" || e.ActorKind != "admin" || e.ActorID == nil || *e.ActorID != actor || e.TargetID != id || e.TargetType != "access_key" || e.Action != "access_key.revoke" || e.Result != "succeeded" || e.Revision != nil {
		t.Fatal("AKRA_READER_PROVENANCE")
	}
	if _, err := parseTime(e.OccurredAt); err != nil {
		t.Fatal("AKRA_READER_TIME")
	}
}

func TestAdminKeyRevocationAuditRetryReaderAndRestart(t *testing.T) {
	f := newAdminRevokeAuditFixture(t)
	k, other := f.key(t, "audit-retry"), f.key(t, "audit-other")
	_, notify := f.app.accountPool.changeSnapshot()
	adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key, f.cookie.Value, f.csrf)
	if notify.Err() == nil {
		t.Fatal("AKRA_SUCCESS_NOTIFY")
	}
	f.reader(t, k.ID, f.actor)
	var owner string
	if f.app.store.db.QueryRow(`SELECT employee_id FROM access_keys WHERE id=?`, k.ID).Scan(&owner) != nil {
		t.Fatal("AKRA_FUTURE_OWNER")
	}
	future := createTestKey(t, f.server.URL, owner, "audit-future-key", f.cookie, f.csrf)
	adminAuditBody(t, employeeRequest(t, "GET", f.server.URL+"/v1/models", "", future.Key, context.Background()), 200, future.Key)
	before := adminAuditFacts(t, f.app.store.db)
	adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key)
	if !reflect.DeepEqual(before, adminAuditFacts(t, f.app.store.db)) {
		t.Fatal("AKRA_RETRY_MUTATED")
	}
	if f.auditCount(t, k.ID) != 1 {
		t.Fatal("AKRA_RETRY_DUPLICATE")
	}
	r := employeeRequest(t, "GET", f.server.URL+"/v1/models", "", other.Key, context.Background())
	adminAuditBody(t, r, 200, k.Key, other.Key)
	r = employeeRequest(t, "GET", f.server.URL+"/v1/models", "", k.Key, context.Background())
	adminAuditBody(t, r, 401, k.Key)
	f.server.Close()
	if f.app.Close() != nil {
		t.Fatal("AKRA_RESTART_CLOSE")
	}
	app, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, Version: "audit-reopen"})
	if err != nil {
		t.Fatal("AKRA_RESTART_OPEN")
	}
	f.app = app
	f.server = httptest.NewServer(app.Handler())
	f.cookie, f.csrf = loginTestAdmin(t, f.server.URL)
	before = adminAuditFacts(t, app.store.db)
	adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key)
	if !reflect.DeepEqual(before, adminAuditFacts(t, app.store.db)) || f.auditCount(t, k.ID) != 1 {
		t.Fatal("AKRA_RESTART_DUPLICATE")
	}
	f.reader(t, k.ID, f.actor)
}

func TestAdminKeyRevocationAuditSlotStates(t *testing.T) {
	for _, state := range []string{"pending", "armed", "issued", "cancelled"} {
		for _, revoked := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pre_revoked_%t", state, revoked), func(t *testing.T) {
				f := newAdminRevokeAuditFixture(t)
				k := f.key(t, "slot-case")
				adminAuditInstallSlot(t, f, k.ID, state)
				if revoked {
					adminAuditExec(t, f.app.store.db, `UPDATE access_keys SET revoked_at='2020-03-01T00:00:00Z' WHERE id=?`, k.ID)
				}
				adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key)
				var actualState, stamp string
				var armsNull bool
				if f.app.store.db.QueryRow(`SELECT s.state,k.revoked_at,(s.armed_at IS NULL AND s.arm_fingerprint IS NULL AND s.arm_employee_revision IS NULL AND s.arm_key_policy_revision IS NULL AND s.arm_governance_revision IS NULL AND s.arm_budget_revision IS NULL) FROM employee_self_key_slots s JOIN access_keys k ON k.id=s.key_id WHERE s.key_id=?`, k.ID).Scan(&actualState, &stamp, &armsNull) != nil {
					t.Fatal("AKRA_SLOT_FINAL_READ")
				}
				mutable := state == "pending" || state == "armed"
				wanted := state
				if mutable {
					wanted = "cancelled"
				}
				if actualState != wanted || (mutable && !armsNull) || (revoked && stamp != "2020-03-01T00:00:00Z") {
					t.Fatal("AKRA_SLOT_FINAL")
				}
				wantCount := 1
				if revoked && !mutable {
					wantCount = 0
				}
				if f.auditCount(t, k.ID) != wantCount {
					t.Fatal("AKRA_SLOT_AUDIT")
				}
				facts := adminAuditFacts(t, f.app.store.db)
				adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key)
				if !reflect.DeepEqual(facts, adminAuditFacts(t, f.app.store.db)) {
					t.Fatal("AKRA_SLOT_REPEAT")
				}
			})
		}
	}
}

func TestAdminKeyRevocationAuditRejections(t *testing.T) {
	f := newAdminRevokeAuditFixture(t)
	k := f.key(t, "audit-denials")
	for _, tc := range []struct {
		name, body, origin, csrf, id string
		cookie                       *http.Cookie
		status                       int
	}{
		{"anonymous", `{}`, f.server.URL, "", k.ID, nil, 401},
		{"missing_origin", `{}`, "", f.csrf, k.ID, f.cookie, 403},
		{"foreign_origin", `{}`, "http://foreign.invalid", f.csrf, k.ID, f.cookie, 403},
		{"missing_csrf", `{}`, f.server.URL, "", k.ID, f.cookie, 403},
		{"wrong_csrf", `{}`, f.server.URL, "synthetic-invalid", k.ID, f.cookie, 403},
		{"missing_target", `{}`, f.server.URL, f.csrf, "key_missing", f.cookie, 404},
		{"unknown_field", `{"actor_id":"synthetic-body-not-authority"}`, f.server.URL, f.csrf, k.ID, f.cookie, 400},
		{"trailing_json", `{} {}`, f.server.URL, f.csrf, k.ID, f.cookie, 400},
		{"wrong_type", `[]`, f.server.URL, f.csrf, k.ID, f.cookie, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := adminAuditFacts(t, f.app.store.db)
			r := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/keys/"+tc.id+"/revoke", tc.body, tc.cookie, tc.csrf, tc.origin)
			adminAuditBody(t, r, tc.status, k.Key, f.cookie.Value, f.csrf)
			if !reflect.DeepEqual(before, adminAuditFacts(t, f.app.store.db)) {
				t.Fatal("AKRA_REJECT_WRITE")
			}
		})
	}
	var owner string
	if f.app.store.db.QueryRow(`SELECT employee_id FROM access_keys WHERE id=?`, k.ID).Scan(&owner) != nil {
		t.Fatal("AKRA_SELF_OWNER")
	}
	enrollment := selfIssue(t, f.server.URL, owner, f.cookie, f.csrf)
	selfCookie, selfCSRF := selfRedeem(t, f.server.URL, owner, enrollment)
	for _, role := range []string{"self", "bearer"} {
		t.Run(role, func(t *testing.T) {
			before := adminAuditFacts(t, f.app.store.db)
			req, err := http.NewRequest("POST", f.server.URL+"/admin/api/v1/keys/"+k.ID+"/revoke", strings.NewReader(`{}`))
			if err != nil {
				t.Fatal("AKRA_ROLE_REQUEST")
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Origin", f.server.URL)
			if role == "self" {
				req.AddCookie(selfCookie)
				req.Header.Set("X-CSRF-Token", selfCSRF)
			} else {
				req.Header.Set("Authorization", "Bearer "+k.Key)
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal("AKRA_ROLE_HTTP")
			}
			adminAuditBody(t, r, 401, k.Key, selfCookie.Value, selfCSRF)
			if !reflect.DeepEqual(before, adminAuditFacts(t, f.app.store.db)) {
				t.Fatal("AKRA_ROLE_WRITE")
			}
		})
	}
}

func TestAdminKeyRevocationAuditConcurrentRealActors(t *testing.T) {
	f := newAdminRevokeAuditFixture(t)
	k := f.key(t, "audit-concurrent")
	adminAuditExec(t, f.app.store.db, `INSERT INTO admins(id,username,password_hash,created_at) SELECT 'admin-audit-second','audit-second',password_hash,created_at FROM admins WHERE id=?`, f.actor)
	r := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/sessions", `{"username":"audit-second","password":"a-strong-preview-password"}`, nil, "", f.server.URL)
	if r.StatusCode != 200 || len(r.Cookies()) != 1 {
		r.Body.Close()
		t.Fatal("AKRA_SECOND_ADMIN_LOGIN")
	}
	secondCookie := r.Cookies()[0]
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	decodeResponse(t, r, &login)
	var wg sync.WaitGroup
	results := make(chan bool, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(second bool) {
			defer wg.Done()
			cookie, csrf := f.cookie, f.csrf
			if second {
				cookie, csrf = secondCookie, login.CSRF
			}
			req, err := http.NewRequest("POST", f.server.URL+"/admin/api/v1/keys/"+k.ID+"/revoke", strings.NewReader(`{}`))
			if err != nil {
				results <- false
				return
			}
			req.AddCookie(cookie)
			req.Header.Set("Origin", f.server.URL)
			req.Header.Set("X-CSRF-Token", csrf)
			req.Header.Set("Content-Type", "application/json")
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				results <- false
				return
			}
			_, readErr := io.Copy(io.Discard, r.Body)
			closeErr := r.Body.Close()
			results <- r.StatusCode == 200 && readErr == nil && closeErr == nil
		}(i%2 == 1)
	}
	wg.Wait()
	close(results)
	for ok := range results {
		if !ok {
			t.Fatal("AKRA_CONCURRENT_HTTP")
		}
	}
	var actor string
	if f.app.store.db.QueryRow(`SELECT actor_id FROM account_lifecycle_audit WHERE action='access_key.revoke' AND target_id=?`, k.ID).Scan(&actor) != nil || (actor != f.actor && actor != "admin-audit-second") || f.auditCount(t, k.ID) != 1 {
		t.Fatal("AKRA_CONCURRENT_ACTOR")
	}
	f.reader(t, k.ID, actor)
}

func TestAdminKeyRevocationAuditSQLFaults(t *testing.T) {
	for _, tc := range []struct {
		name, trigger string
		slot, full    bool
	}{
		{"key_abort", `CREATE TRIGGER akra_fault BEFORE UPDATE OF revoked_at ON access_keys BEGIN SELECT RAISE(ABORT,'synthetic'); END`, false, false},
		{"key_ignore", `CREATE TRIGGER akra_fault BEFORE UPDATE OF revoked_at ON access_keys BEGIN SELECT RAISE(IGNORE); END`, false, false},
		{"key_reverse", `CREATE TRIGGER akra_fault AFTER UPDATE OF revoked_at ON access_keys BEGIN UPDATE access_keys SET revoked_at=NULL WHERE id=NEW.id; END`, false, false},
		{"slot_ignore", `CREATE TRIGGER akra_fault BEFORE UPDATE OF state ON employee_self_key_slots BEGIN SELECT RAISE(IGNORE); END`, true, false},
		{"slot_reverse", `CREATE TRIGGER akra_fault AFTER UPDATE OF state ON employee_self_key_slots BEGIN UPDATE employee_self_key_slots SET state='pending',cancelled_at=NULL WHERE key_id=NEW.key_id; END`, true, false},
		{"audit_abort", `CREATE TRIGGER akra_fault BEFORE INSERT ON account_lifecycle_audit BEGIN SELECT RAISE(ABORT,'synthetic'); END`, false, false},
		{"audit_ignore", `CREATE TRIGGER akra_fault BEFORE INSERT ON account_lifecycle_audit BEGIN SELECT RAISE(IGNORE); END`, false, false},
		{"audit_delete", `CREATE TRIGGER akra_fault AFTER INSERT ON account_lifecycle_audit BEGIN DELETE FROM account_lifecycle_audit WHERE id=NEW.id; END`, false, false},
		{"audit_reverses_key", `CREATE TRIGGER akra_fault AFTER INSERT ON account_lifecycle_audit BEGIN UPDATE access_keys SET revoked_at=NULL WHERE id=NEW.target_id; END`, false, false},
		{"trim_abort", `CREATE TRIGGER akra_fault BEFORE DELETE ON account_lifecycle_audit BEGIN SELECT RAISE(ABORT,'synthetic'); END`, false, true},
		{"trim_ignore", `CREATE TRIGGER akra_fault BEFORE DELETE ON account_lifecycle_audit BEGIN SELECT RAISE(IGNORE); END`, false, true},
		{"trim_reverses_slot", `CREATE TRIGGER akra_fault AFTER DELETE ON account_lifecycle_audit BEGIN UPDATE employee_self_key_slots SET state='pending',cancelled_at=NULL; END`, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newAdminRevokeAuditFixture(t)
			k := f.key(t, "audit-sql-fault")
			if tc.slot {
				adminAuditInstallSlot(t, f, k.ID, "pending")
			}
			if tc.full {
				adminAuditSeed(t, f, 1000, false)
			}
			adminAuditExec(t, f.app.store.db, tc.trigger)
			before := adminAuditFacts(t, f.app.store.db)
			_, notify := f.app.accountPool.changeSnapshot()
			adminAuditBody(t, f.revoke(t, k.ID), 503, k.Key, f.csrf, f.cookie.Value)
			if notify.Err() != nil || !reflect.DeepEqual(before, adminAuditFacts(t, f.app.store.db)) {
				t.Fatal("AKRA_FAULT_NOT_ATOMIC")
			}
		})
	}
}

func TestAdminKeyRevocationAuditRetention(t *testing.T) {
	for _, count := range []int{999, 1000, 1001} {
		t.Run(fmt.Sprintf("rows_%d", count), func(t *testing.T) {
			f := newAdminRevokeAuditFixture(t)
			k := f.key(t, "audit-retention")
			adminAuditSeed(t, f, count, false)
			adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key)
			var total int
			if f.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_lifecycle_audit`).Scan(&total) != nil || total != 1000 || f.auditCount(t, k.ID) != 1 {
				t.Fatal("AKRA_RETENTION_BOUND")
			}
			rows, err := f.app.store.db.Query(`SELECT id,actor_id,action,target_type,target_id,result,occurred_at FROM account_lifecycle_audit WHERE action='model.update' ORDER BY id`)
			if err != nil {
				t.Fatal("AKRA_RETAINED_READ")
			}
			index := count - 999
			for rows.Next() {
				var fields [7]string
				if rows.Scan(&fields[0], &fields[1], &fields[2], &fields[3], &fields[4], &fields[5], &fields[6]) != nil || fields != [7]string{fmt.Sprintf("audit-fixture-%04d", index), f.actor, "model.update", "model", "synthetic-model", "succeeded", "2020-01-01T00:00:00Z"} {
					t.Fatal("AKRA_RETAINED_CHANGED")
				}
				index++
			}
			if rows.Err() != nil || rows.Close() != nil || index != count {
				t.Fatal("AKRA_RETAINED_INCOMPLETE")
			}
			adminAuditExec(t, f.app.store.db, `DELETE FROM account_lifecycle_audit WHERE action='access_key.revoke' AND target_id=?`, k.ID)
			before := adminAuditFacts(t, f.app.store.db)
			adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key)
			if !reflect.DeepEqual(before, adminAuditFacts(t, f.app.store.db)) || f.auditCount(t, k.ID) != 0 {
				t.Fatal("AKRA_RETENTION_BACKFILL")
			}
		})
	}
	t.Run("own_event_evicted", func(t *testing.T) {
		f := newAdminRevokeAuditFixture(t)
		k := f.key(t, "audit-future")
		adminAuditSeed(t, f, 1000, true)
		before := adminAuditFacts(t, f.app.store.db)
		adminAuditBody(t, f.revoke(t, k.ID), 503, k.Key)
		if !reflect.DeepEqual(before, adminAuditFacts(t, f.app.store.db)) {
			t.Fatal("AKRA_EVICTED_EVENT_COMMITTED")
		}
	})
}

func TestAdminKeyRevocationAuditIssuanceCompetition(t *testing.T) {
	for _, order := range []string{"revoke_wins", "issue_wins"} {
		t.Run(order, func(t *testing.T) {
			s := newSelfSlotFixture(t)
			if s.reserve(t, "audit-issue-slot") != 201 {
				t.Fatal("AKRA_ISSUE_RESERVE")
			}
			retirementSlotLimits(t, s)
			if s.arm(t) != 200 {
				t.Fatal("AKRA_ISSUE_ARM")
			}
			f := &adminRevokeAuditFixture{app: s.app, server: s.server, cookie: s.adminCookie, csrf: s.adminCSRF}
			if order == "revoke_wins" {
				arrived, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				s.app.selfKeyIssueBeforeTx = func() { once.Do(func() { close(arrived) }); <-release }
				defer func() {
					once.Do(func() { close(arrived) })
					select {
					case <-release:
					default:
						close(release)
					}
				}()
				result := retirementAsync("POST", s.server.URL+"/self/api/v1/keys/issue", `{"slot_id":`+quoteJSON(s.keyID)+`,"current_password":`+quoteJSON(selfOldPassword)+`}`, s.cookie, s.csrf, s.server.URL)
				select {
				case <-arrived:
				case <-time.After(10 * time.Second):
					t.Fatal("AKRA_ISSUE_BARRIER")
				}
				adminAuditBody(t, f.revoke(t, s.keyID), 200)
				close(release)
				r := retirementAwait(t, result)
				adminAuditBody(t, r, 409, s.cookie.Value, s.csrf)
			} else {
				r := s.issue(t, selfOldPassword)
				data := adminAuditBody(t, r, 201)
				var issued struct {
					Key string `json:"key"`
				}
				if json.Unmarshal(data, &issued) != nil || issued.Key == "" {
					t.Fatal("AKRA_ISSUE_SECRET_ABSENT")
				}
				adminAuditBody(t, f.revoke(t, s.keyID), 200, issued.Key)
				r = employeeRequest(t, "GET", s.server.URL+"/v1/models", "", issued.Key, context.Background())
				adminAuditBody(t, r, 401, issued.Key)
			}
			if f.auditCount(t, s.keyID) != 1 {
				t.Fatal("AKRA_ISSUE_AUDIT")
			}
			var state string
			if s.app.store.db.QueryRow(`SELECT state FROM employee_self_key_slots WHERE key_id=?`, s.keyID).Scan(&state) != nil || (order == "revoke_wins" && state != "cancelled") || (order == "issue_wins" && state != "issued") {
				t.Fatal("AKRA_ISSUE_TOMBSTONE")
			}
		})
	}
}

func TestAdminKeyRevocationAuditMetadataCanary(t *testing.T) {
	f := newAdminRevokeAuditFixture(t)
	k := f.key(t, "audit-metadata")
	var digest []byte
	if f.app.store.db.QueryRow(`SELECT digest FROM access_keys WHERE id=?`, k.ID).Scan(&digest) != nil {
		t.Fatal("AKRA_DIGEST_READ")
	}
	adminAuditBody(t, f.revoke(t, k.ID), 200, k.Key, f.cookie.Value, f.csrf)
	var fields [7]string
	if f.app.store.db.QueryRow(`SELECT id,actor_id,action,target_type,target_id,result,occurred_at FROM account_lifecycle_audit WHERE target_id=?`, k.ID).Scan(&fields[0], &fields[1], &fields[2], &fields[3], &fields[4], &fields[5], &fields[6]) != nil {
		t.Fatal("AKRA_METADATA_READ")
	}
	encoded, _ := json.Marshal(fields)
	sum := sha256.Sum256([]byte(k.Key))
	for _, secret := range []string{k.Key, f.cookie.Value, f.csrf, hex.EncodeToString(digest), hex.EncodeToString(sum[:]), "a-strong-preview-password"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatal("AKRA_METADATA_SECRET")
		}
	}
	f.reader(t, k.ID, f.actor)
}

// Independently authored from the 2026-11-06 PR136 E/F findings and CPA Cloud's
// confirmation/audit contracts. These are synthetic owned SQLite/HTTP fixtures.
type akrp2Event struct {
	id, actor, action, kind, target, result, stamp string
	rowid                                          int64
}

func akrp2Events(t *testing.T, db *sql.DB) []akrp2Event {
	t.Helper()
	rows, err := db.QueryContext(t.Context(), "SELECT id,actor_id,action,target_type,target_id,result,occurred_at,rowid FROM account_lifecycle_audit ORDER BY rowid")
	if err != nil {
		t.Fatal("AKRP2_EVENT_QUERY")
	}
	defer rows.Close()
	events := []akrp2Event{}
	for rows.Next() {
		var e akrp2Event
		if rows.Scan(&e.id, &e.actor, &e.action, &e.kind, &e.target, &e.result, &e.stamp, &e.rowid) != nil {
			t.Fatal("AKRP2_EVENT_SCAN")
		}
		events = append(events, e)
	}
	rowErr, closeErr := rows.Err(), rows.Close()
	if rowErr != nil || closeErr != nil {
		t.Fatal("AKRP2_EVENT_EOF")
	}
	return events
}

func akrp2NewEvents(t *testing.T, f *adminKeyRevocationFixture, before []akrp2Event, count int) {
	t.Helper()
	after := akrp2Events(t, f.app.store.db)
	if len(after) != len(before)+count || !reflect.DeepEqual(before, after[:len(before)]) {
		t.Fatal("AKRP2_EVENT_DELTA")
	}
	var high int64
	ids := map[string]bool{}
	for _, e := range before {
		ids[e.id] = true
		if e.rowid > high {
			high = e.rowid
		}
	}
	for _, e := range after[len(before):] {
		if ids[e.id] || e.id == "" || e.rowid <= high || e.actor != f.adminLogoutFixture.admin || e.action != "access_key.revoke" || e.kind != "access_key" || e.target != f.target.ID || e.result != "succeeded" {
			t.Fatal("AKRP2_EVENT_IDENTITY")
		}
		if _, err := parseTime(e.stamp); err != nil {
			t.Fatal("AKRP2_EVENT_TIME")
		}
		ids[e.id] = true
	}
}

func akrp2Protected(facts map[string]string) map[string]string {
	result := map[string]string{}
	for k, v := range facts {
		if k != "access_keys" && k != "employee_self_key_slots" && k != "account_lifecycle_audit" {
			result[k] = v
		}
	}
	return result
}

func TestAdminKeyRevocationAuditP2Windows(t *testing.T) {
	old := os.Getenv("CPA_REVOKE_P2_OLD_ORACLE") == "1"
	names := []string{"ordinary", "write_trigger_extra", "before_commit_delete", "retry_hook_changes"}
	if old {
		names = names[:3]
	}
	for _, name := range names {
		expectedOld := false
		passed := t.Run(name, func(t *testing.T) { expectedOld = akrp2Case(t, name, old) })
		if !passed && !expectedOld {
			t.Fatal("AKRP2_STOP_UNEXPECTED")
		}
	}
}

func akrp2Case(t *testing.T, name string, old bool) bool {
	expectedOld := old && (name == "write_trigger_extra" || name == "before_commit_delete")
	if expectedOld {
		label := "REVOKE_P2_OLD_200_TWO_AUDITS"
		if name == "before_commit_delete" {
			label = "REVOKE_P2_OLD_200_WITHOUT_AUDIT"
		}
		// Registered before fixture cleanup: LIFO emits the expected label only
		// after every original/reopened owner has closed without another failure.
		t.Cleanup(func() {
			if !t.Failed() {
				t.Error(label)
			}
		})
	}
	modes := []string{"key"}
	if name == "write_trigger_extra" {
		modes = []string{"key", "slot"}
	}
	if name == "retry_hook_changes" {
		modes = []string{"insert", "delete"}
	}
	for _, mode := range modes {
		f := newAdminKeyRevocationFixture(t)
		var version string
		var fk int
		if f.app.store.db.QueryRow("SELECT sqlite_version()").Scan(&version) != nil || version != "3.51.3" || f.app.store.db.QueryRow("PRAGMA foreign_keys").Scan(&fk) != nil || fk != 1 {
			t.Fatal("AKRP2_NATIVE_SQLITE_PROFILE")
		}
		t.Log("AKRP2_SQLITE_VERSION=" + version)
		if mode == "slot" {
			adminAuditInstallSlot(t, &adminRevokeAuditFixture{app: f.app}, f.target.ID, "armed")
		}
		if name == "retry_hook_changes" {
			f.assert200(f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`))
		}
		beforeEvents := akrp2Events(t, f.app.store.db)
		var high int64
		for _, e := range beforeEvents {
			if e.rowid > high {
				high = e.rowid
			}
			if e.id == "akrp2-extra" {
				t.Fatal("AKRP2_EXTRA_PREEXISTS")
			}
		}
		quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "''") + "'" }
		insert := "INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES('akrp2-extra'," + quote(f.adminLogoutFixture.admin) + ",'access_key.revoke','access_key'," + quote(f.target.ID) + ",'succeeded'," + quote(utcNow()) + ")"
		if name == "write_trigger_extra" {
			trigger := "CREATE TRIGGER akrp2_extra AFTER UPDATE OF revoked_at ON access_keys WHEN NEW.id=" + quote(f.target.ID) + " AND OLD.revoked_at IS NULL BEGIN " + insert + "; END"
			if mode == "slot" {
				trigger = "CREATE TRIGGER akrp2_extra AFTER UPDATE OF state ON employee_self_key_slots WHEN NEW.key_id=" + quote(f.target.ID) + " AND OLD.state='armed' AND NEW.state='cancelled' BEGIN " + insert + "; END"
			}
			f.exec(trigger)
			var installed string
			if f.app.store.db.QueryRow("SELECT sql FROM sqlite_master WHERE type='trigger' AND name='akrp2_extra'").Scan(&installed) != nil || installed != trigger {
				t.Fatal("AKRP2_TRIGGER_INSTALL")
			}
		}
		before := adminAuditFacts(t, f.app.store.db)
		controls := f.controls()
		priorRevoked := f.revoked(f.target.ID)
		var commits, notifications, hookCalls, hookRows atomic.Int32
		var nativeError atomic.Bool
		hooks := adminKeyRevocationHooks{
			afterCommit: func(real error) error { commits.Add(1); nativeError.Store(real != nil); return real },
			afterNotify: func() { notifications.Add(1) },
		}
		if name == "before_commit_delete" || name == "retry_hook_changes" {
			hooks.beforeCommit = func(tx *sql.Tx) error {
				hookCalls.Add(1)
				var r sql.Result
				var err error
				if name == "retry_hook_changes" && mode == "insert" {
					r, err = tx.ExecContext(t.Context(), insert)
				} else {
					minimum := high
					if name == "retry_hook_changes" {
						minimum = 0
					}
					r, err = tx.ExecContext(t.Context(), "DELETE FROM account_lifecycle_audit WHERE rowid>? AND actor_id=? AND action='access_key.revoke' AND target_type='access_key' AND target_id=?", minimum, f.adminLogoutFixture.admin, f.target.ID)
				}
				if err != nil {
					return err
				}
				n, err := r.RowsAffected()
				if err != nil || n != 1 {
					return fmt.Errorf("AKRP2_HOOK_AFFECTED")
				}
				hookRows.Add(1)
				return nil
			}
		}
		f.setHooks(hooks)
		response := f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`)
		fault := name != "ordinary"
		rejected := fault && !old
		if rejected {
			f.assert503(response)
		} else {
			f.assert200(response)
		}
		if (name == "before_commit_delete" || name == "retry_hook_changes") && (hookCalls.Load() != 1 || hookRows.Load() != 1) {
			t.Fatal("AKRP2_LAST_HOOK_NOT_HIT")
		}
		if nativeError.Load() {
			t.Fatal("AKRP2_UNEXPECTED_NATIVE_COMMIT_ERROR")
		}
		after := adminAuditFacts(t, f.app.store.db)
		if rejected {
			if commits.Load() != 0 || notifications.Load() != 0 || !reflect.DeepEqual(before, after) || f.revoked(f.target.ID) != priorRevoked {
				t.Fatal("AKRP2_REJECTION_ROLLBACK")
			}
		} else {
			if commits.Load() != 1 || notifications.Load() != 1 || !f.revoked(f.target.ID).Valid || !reflect.DeepEqual(akrp2Protected(before), akrp2Protected(after)) {
				t.Fatal("AKRP2_COMMITTED_FACTS")
			}
			count := 1
			if name == "write_trigger_extra" {
				count = 2
			}
			if name == "before_commit_delete" {
				count = 0
			}
			akrp2NewEvents(t, f, beforeEvents, count)
			if mode == "slot" {
				var state string
				var cleared bool
				if f.app.store.db.QueryRow("SELECT state,armed_at IS NULL FROM employee_self_key_slots WHERE key_id=?", f.target.ID).Scan(&state, &cleared) != nil || state != "cancelled" || !cleared {
					t.Fatal("AKRP2_SLOT_COMMITTED")
				}
			}
			if name == "ordinary" {
				f.assert200(f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`))
				if commits.Load() != 2 || notifications.Load() != 2 || !reflect.DeepEqual(after, adminAuditFacts(t, f.app.store.db)) {
					t.Fatal("AKRP2_RETRY_ZERO_NEW")
				}
			}
		}
		f.assertControls(controls)
		persisted := adminAuditFacts(t, f.app.store.db)
		var reopened map[string]string
		if name == "ordinary" || name == "write_trigger_extra" || name == "before_commit_delete" || name == "retry_hook_changes" {
			reopened = akrp2ReopenFacts(t, f, persisted)
		} else {
			f.reopen()
			reopened = adminAuditFacts(t, f.app.store.db)
		}
		if !reflect.DeepEqual(persisted, reopened) {
			t.Fatal("AKRP2_REOPEN_FACTS")
		}
		f.assertControls(controls)
	}
	return expectedOld
}

// These observations retain the original complete typed-row facts.
// Column summaries are sorted typed-value multisets, not row-identity traces.
// The P2 transition and no-op post-Open checks share the independently observed
// budget clock exception; every other typed cell still participates in full equality.
type akrp2DiagnosticFacts struct {
	facts       map[string]string
	columns     map[string][]string
	columnSHA   map[string][]string
	rowCounts   map[string]int
	rows        int
	completeSHA string
}

func akrp2DiagnosticSnapshot(t *testing.T, db *sql.DB, facts map[string]string) akrp2DiagnosticFacts {
	t.Helper()
	result := akrp2DiagnosticFacts{facts: facts, columns: map[string][]string{}, columnSHA: map[string][]string{}, rowCounts: map[string]int{}}
	for table, encoded := range facts {
		rows, err := db.Query(`SELECT * FROM "` + strings.ReplaceAll(table, `"`, `""`) + `" LIMIT 0`)
		if err != nil {
			t.Fatal("AKRP2_DIAGNOSTIC_COLUMNS")
		}
		columns, columnsErr := rows.Columns()
		closeErr := rows.Close()
		if columnsErr != nil || closeErr != nil {
			t.Fatal("AKRP2_DIAGNOSTIC_COLUMNS_CLOSE")
		}
		result.columns[table] = columns
		var encodedRows []string
		if json.Unmarshal([]byte(encoded), &encodedRows) != nil {
			t.Fatal("AKRP2_DIAGNOSTIC_ROWS_DECODE")
		}
		result.rowCounts[table] = len(encodedRows)
		result.rows += len(encodedRows)
		values := make([][]string, len(columns))
		for i := range values {
			values[i] = []string{}
		}
		for _, encodedRow := range encodedRows {
			var typed []json.RawMessage
			if json.Unmarshal([]byte(encodedRow), &typed) != nil || len(typed) != 2*len(columns) {
				t.Fatal("AKRP2_DIAGNOSTIC_TYPED_ROW")
			}
			for i := range columns {
				pair, err := json.Marshal([2]json.RawMessage{typed[2*i], typed[2*i+1]})
				if err != nil {
					t.Fatal("AKRP2_DIAGNOSTIC_COLUMN_ENCODE")
				}
				values[i] = append(values[i], string(pair))
			}
		}
		for _, column := range values {
			sort.Strings(column)
			encodedColumn, err := json.Marshal(column)
			if err != nil {
				t.Fatal("AKRP2_DIAGNOSTIC_COLUMN_SUMMARY")
			}
			digest := sha256.Sum256(encodedColumn)
			result.columnSHA[table] = append(result.columnSHA[table], hex.EncodeToString(digest[:]))
		}
	}
	encoded, err := json.Marshal(facts)
	if err != nil {
		t.Fatal("AKRP2_DIAGNOSTIC_SNAPSHOT_SUMMARY")
	}
	digest := sha256.Sum256(encoded)
	result.completeSHA = hex.EncodeToString(digest[:])
	return result
}

func akrp2DiagnosticDifference(t *testing.T, stage string, before, after akrp2DiagnosticFacts) {
	t.Helper()
	tables := map[string]bool{}
	for table := range before.facts {
		tables[table] = true
	}
	for table := range after.facts {
		tables[table] = true
	}
	ordered := make([]string, 0, len(tables))
	for table := range tables {
		ordered = append(ordered, table)
	}
	sort.Strings(ordered)
	changed := 0
	for _, table := range ordered {
		prior, priorExists := before.facts[table]
		next, nextExists := after.facts[table]
		if priorExists == nextExists && prior == next && reflect.DeepEqual(before.columns[table], after.columns[table]) {
			continue
		}
		changed++
		columns := []string{}
		if reflect.DeepEqual(before.columns[table], after.columns[table]) {
			for i, column := range before.columns[table] {
				if before.columnSHA[table][i] != after.columnSHA[table][i] {
					columns = append(columns, column)
				}
			}
		}
		priorSHA, nextSHA := sha256.Sum256([]byte(prior)), sha256.Sum256([]byte(next))
		encodedColumns, err := json.Marshal(columns)
		if err != nil {
			t.Fatal("AKRP2_DIAGNOSTIC_CHANGED_COLUMNS")
		}
		t.Logf("AKRP2_DIAGNOSTIC_TABLE stage=%s table=%s before_exists=%t after_exists=%t before_rows=%d after_rows=%d columns=%s column_basis=typed_multiset schema_equal=%t before_sha256=%x after_sha256=%x", stage, table, priorExists, nextExists, before.rowCounts[table], after.rowCounts[table], encodedColumns, reflect.DeepEqual(before.columns[table], after.columns[table]), priorSHA, nextSHA)
	}
	t.Logf("AKRP2_DIAGNOSTIC_STAGE stage=%s changed_tables=%d before_tables=%d after_tables=%d before_rows=%d after_rows=%d before_sha256=%s after_sha256=%s", stage, changed, len(before.facts), len(after.facts), before.rows, after.rows, before.completeSHA, after.completeSHA)
}

func akrp2ReopenFacts(t *testing.T, f *adminKeyRevocationFixture, persisted map[string]string) map[string]string {
	t.Helper()
	beforeClose := akrp2DiagnosticSnapshot(t, f.app.store.db, persisted)
	if f.closeOwned() != nil {
		t.Fatal("AKRP2_DIAGNOSTIC_CLOSE")
	}
	dbPath := filepath.Join(f.dir, "cpa-cloud.db")
	info, err := os.Stat(dbPath)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("AKRP2_DIAGNOSTIC_EXISTING_DB")
	}
	filePath := filepath.ToSlash(dbPath)
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	query := url.Values{"mode": {"ro"}, "_pragma": {"query_only(1)", "foreign_keys(1)", "busy_timeout(5000)"}}
	dsn := (&url.URL{Scheme: "file", Path: filePath, RawQuery: query.Encode()}).String()
	readOnly, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal("AKRP2_DIAGNOSTIC_RO_OPEN")
	}
	readOnly.SetMaxOpenConns(1)
	readOnly.SetMaxIdleConns(1)
	roCloseAttempted := false
	t.Cleanup(func() {
		if !roCloseAttempted {
			roCloseAttempted = true
			if readOnly.Close() != nil {
				t.Error("AKRP2_DIAGNOSTIC_RO_CLEANUP")
			}
		}
	})
	var queryOnly, foreignKeys int
	if readOnly.QueryRow("PRAGMA query_only").Scan(&queryOnly) != nil || queryOnly != 1 || readOnly.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys) != nil || foreignKeys != 1 {
		t.Fatal("AKRP2_DIAGNOSTIC_RO_PROFILE")
	}
	var databaseIndex int
	var databaseName, openedPath string
	if readOnly.QueryRow("PRAGMA database_list").Scan(&databaseIndex, &databaseName, &openedPath) != nil || databaseIndex != 0 || databaseName != "main" || !strings.EqualFold(filepath.Clean(openedPath), filepath.Clean(dbPath)) {
		t.Fatal("AKRP2_DIAGNOSTIC_RO_IDENTITY")
	}
	afterClose := akrp2DiagnosticSnapshot(t, readOnly, adminAuditFacts(t, readOnly))
	roCloseAttempted = true
	if readOnly.Close() != nil {
		t.Fatal("AKRP2_DIAGNOSTIC_RO_CLOSE")
	}
	akrp2DiagnosticDifference(t, "before_close_to_after_close_ro", beforeClose, afterClose)
	if !reflect.DeepEqual(beforeClose.facts, afterClose.facts) || !reflect.DeepEqual(beforeClose.columns, afterClose.columns) {
		t.Fatal("AKRP2_REOPEN_RO_FACTS")
	}
	// closeOwned is now a guarded no-op; the original reopen clears request hooks
	// and performs exactly the same complete Open without creating another login.
	f.reopen()
	afterOpen := akrp2DiagnosticSnapshot(t, f.app.store.db, adminAuditFacts(t, f.app.store.db))
	akrp2DiagnosticDifference(t, "after_close_ro_to_after_open", afterClose, afterOpen)
	akrp2DiagnosticDifference(t, "before_close_to_after_open", beforeClose, afterOpen)
	t.Log("AKRP2_DIAGNOSTIC_RO_VERIFIED mode_ro=true no_create=true query_only=true same_database=true closed=true")
	return akrp2ReopenClockFacts(t, beforeClose, afterOpen)
}

func akrp2ReopenClockFacts(t *testing.T, before, after akrp2DiagnosticFacts) map[string]string {
	t.Helper()
	const table = "governance_budget_clock"
	const layout = "2006-01-02T15:04:05.000000000Z"
	if !reflect.DeepEqual(before.columns, after.columns) {
		t.Fatal("AKRP2_REOPEN_COLUMNS")
	}
	clockColumn := -1
	for i, column := range before.columns[table] {
		if column == "last_effective_at" {
			if clockColumn != -1 {
				t.Fatal("AKRP2_REOPEN_CLOCK_COLUMN")
			}
			clockColumn = i
		}
	}
	if clockColumn == -1 || before.rowCounts[table] != 1 || after.rowCounts[table] != 1 {
		t.Fatal("AKRP2_REOPEN_CLOCK_SHAPE")
	}
	var priorRows, nextRows []string
	if json.Unmarshal([]byte(before.facts[table]), &priorRows) != nil || json.Unmarshal([]byte(after.facts[table]), &nextRows) != nil || len(priorRows) != 1 || len(nextRows) != 1 {
		t.Fatal("AKRP2_REOPEN_CLOCK_ROWS")
	}
	var priorTyped, nextTyped []json.RawMessage
	if json.Unmarshal([]byte(priorRows[0]), &priorTyped) != nil || json.Unmarshal([]byte(nextRows[0]), &nextTyped) != nil || len(priorTyped) != 2*len(before.columns[table]) || len(nextTyped) != len(priorTyped) {
		t.Fatal("AKRP2_REOPEN_CLOCK_TYPED_ROW")
	}
	typeIndex, valueIndex := 2*clockColumn, 2*clockColumn+1
	var priorType, nextType, priorValue, nextValue string
	if json.Unmarshal(priorTyped[typeIndex], &priorType) != nil || json.Unmarshal(nextTyped[typeIndex], &nextType) != nil || priorType != "string" || nextType != priorType || json.Unmarshal(priorTyped[valueIndex], &priorValue) != nil || json.Unmarshal(nextTyped[valueIndex], &nextValue) != nil {
		t.Fatal("AKRP2_REOPEN_CLOCK_TYPE")
	}
	priorTime, priorErr := time.Parse(layout, priorValue)
	nextTime, nextErr := time.Parse(layout, nextValue)
	if priorErr != nil || nextErr != nil || priorTime.IsZero() || nextTime.IsZero() || priorTime.Year() < 1 || priorTime.Year() > 9999 || nextTime.Year() < 1 || nextTime.Year() > 9999 || priorTime.UTC().Format(layout) != priorValue || nextTime.UTC().Format(layout) != nextValue {
		t.Fatal("AKRP2_REOPEN_CLOCK_INVALID")
	}
	if nextTime.Before(priorTime) {
		t.Fatal("AKRP2_REOPEN_CLOCK_NONMONOTONIC")
	}
	// Restore only the validated clock value for the original full-map oracle.
	// Its type, singleton identity, every other cell and every table remain exact.
	nextTyped[valueIndex] = priorTyped[valueIndex]
	normalizedRow, err := json.Marshal(nextTyped)
	if err != nil {
		t.Fatal("AKRP2_REOPEN_CLOCK_ROW_ENCODE")
	}
	normalizedTable, err := json.Marshal([]string{string(normalizedRow)})
	if err != nil {
		t.Fatal("AKRP2_REOPEN_CLOCK_TABLE_ENCODE")
	}
	normalized := make(map[string]string, len(after.facts))
	for name, facts := range after.facts {
		normalized[name] = facts
	}
	normalized[table] = string(normalizedTable)
	if !reflect.DeepEqual(before.facts, normalized) {
		t.Fatal("AKRP2_REOPEN_OTHER_FACTS")
	}
	t.Logf("AKRP2_REOPEN_CLOCK_VALIDATED valid=true monotonic=true changed=%t other_typed_facts_equal=true", !nextTime.Equal(priorTime))
	return normalized
}
