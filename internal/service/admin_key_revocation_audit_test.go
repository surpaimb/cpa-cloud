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
	"reflect"
	"sort"
	"strings"
	"sync"
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
	var fingerprint, armed, issued, cancelled any
	if state == "armed" || state == "issued" {
		fingerprint = make([]byte, 32)
		armed = "2020-02-01T00:00:00Z"
	}
	if state == "issued" {
		issued = "2020-02-02T00:00:00Z"
	}
	if state == "cancelled" {
		cancelled = "2020-02-03T00:00:00Z"
	}
	adminAuditExec(t, f.app.store.db, `INSERT INTO employee_self_key_slots(employee_id,key_id,state,arm_fingerprint,arm_employee_revision,arm_key_policy_revision,arm_governance_revision,arm_budget_revision,created_at,armed_at,issued_at,cancelled_at) VALUES(?,?,?,?,1,1,1,1,'2020-01-01T00:00:00Z',?,?,?)`, owner, id, state, fingerprint, armed, issued, cancelled)
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
				if f.app.store.db.QueryRow(`SELECT s.state,k.revoked_at,(s.arm_fingerprint IS NULL AND s.arm_employee_revision IS NULL AND s.arm_key_policy_revision IS NULL AND s.arm_governance_revision IS NULL AND s.arm_budget_revision IS NULL) FROM employee_self_key_slots s JOIN access_keys k ON k.id=s.key_id WHERE s.key_id=?`, k.ID).Scan(&actualState, &stamp, &armsNull) != nil {
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
