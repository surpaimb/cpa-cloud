package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
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
	"cpacloud.local/server/internal/service"
	_ "modernc.org/sqlite"
)

func TestEncryptedBackupRestoresIntoRealAppWithEmployeeKey(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	adminPassword := "synthetic-admin-password"
	if err := service.Initialize(ctx, source, bytes.NewBufferString(adminPassword+"\n")); err != nil {
		t.Fatal(err)
	}
	app, err := service.Open(ctx, service.Config{DataDir: source, Listen: "127.0.0.1:0", Version: "backup-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	client := newCookieClient(t)
	csrf := loginAdmin(t, client, server.URL, adminPassword)

	employee := adminJSON(t, client, http.MethodPost, server.URL+"/admin/api/v1/employees", csrf,
		map[string]any{"name": "Synthetic Employee", "department": "QA", "note": "backup e2e"}, http.StatusCreated)
	employeeID := employee["id"].(string)
	keyView := adminJSON(t, client, http.MethodPost, server.URL+"/admin/api/v1/employees/"+employeeID+"/keys", csrf,
		map[string]any{"name": "Synthetic Key", "operation_id": "backup-e2e-key"}, http.StatusCreated)
	employeeKey := keyView["key"].(string)
	activeUpstream := adminJSON(t, client, http.MethodPost, server.URL+"/admin/api/v1/upstreams", csrf,
		map[string]any{"name": "Synthetic API", "provider_kind": "openai-compatible", "endpoint": "https://example.com", "api_key": "synthetic-upstream-secret"}, http.StatusCreated)
	activeUpstreamID := activeUpstream["id"].(string)
	plan := adminJSON(t, client, http.MethodPost, server.URL+"/admin/api/v1/scheduled-tests", csrf,
		map[string]any{"name": "Backup schedule", "upstream_id": activeUpstreamID, "scope": "local_credential", "interval_seconds": 300, "enabled": true}, http.StatusCreated)
	planID := plan["id"].(string)
	archivedUpstream := adminJSON(t, client, http.MethodPost, server.URL+"/admin/api/v1/upstreams", csrf,
		map[string]any{"name": "Archived API", "provider_kind": "openai-compatible", "endpoint": "https://example.net", "api_key": "synthetic-archived-secret"}, http.StatusCreated)
	archivedUpstreamID := archivedUpstream["id"].(string)
	adminJSON(t, client, http.MethodDelete, server.URL+"/admin/api/v1/upstreams/"+archivedUpstreamID, csrf,
		map[string]any{"expected_revision": archivedUpstream["revision"]}, http.StatusOK)

	observer, err := sql.Open("sqlite", filepath.Join(source, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,operation_id)
		 VALUES('ups_backup_codex','Synthetic Membership','codex-membership','https://chatgpt.com/backend-api/codex',1,x'01020304',2,1,'2026-09-24T00:00:00Z','imported_unverified','00000000-0000-0000-0000-000000000001')`,
		`INSERT INTO codex_oauth_bindings(upstream_id,client_id,source,created_at) VALUES('ups_backup_codex','synthetic-client','authorization_code','2026-09-24T00:00:00Z')`,
		`INSERT INTO codex_oauth_refresh_states(upstream_id,state,reason_code,attempt_revision,updated_at) VALUES('ups_backup_codex','in_progress',NULL,1,'2026-09-24T00:00:00Z')`,
	} {
		if _, err := observer.Exec(statement); err != nil {
			observer.Close()
			t.Fatal(err)
		}
	}
	observer.Close()
	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = service.Open(ctx, service.Config{DataDir: source, Listen: "127.0.0.1:0", Version: "backup-e2e-source-reopen"})
	if err != nil {
		t.Fatalf("reopen synthetic source before backup: %v", err)
	}
	defer app.Close()
	observer, err = sql.Open("sqlite", filepath.Join(source, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Exec(`UPDATE governance_budget_clock SET last_effective_at='2026-01-02T12:34:56.000000000Z' WHERE singleton=1`); err != nil {
		observer.Close()
		t.Fatal(err)
	}
	if _, err := observer.Exec(`INSERT INTO scheduled_test_runs(plan_id,plan_revision,upstream_id,operation_id,scope,state,result_code,started_at,finished_at,latency_ms,actor)
		VALUES(?,?,?,'34000000-0000-4000-8000-000000000001','local_credential','running',NULL,'2026-09-24T00:00:00Z',NULL,NULL,'system')`, planID, 1, activeUpstreamID); err != nil {
		observer.Close()
		t.Fatal(err)
	}
	observer.Close()

	packagePath := filepath.Join(root, "real-app.cpacb")
	packagePassword := []byte("synthetic-package-password")
	if _, err := backup.Create(ctx, source, packagePath, packagePassword, "backup-e2e"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "restored")
	if _, err := backup.Restore(ctx, packagePath, target, packagePassword); err != nil {
		t.Fatal(err)
	}
	preOpenDB, err := sql.Open("sqlite", filepath.Join(target, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	var preservedBudgetClock, preOpenRunState string
	if err := preOpenDB.QueryRow(`SELECT last_effective_at FROM governance_budget_clock WHERE singleton=1`).Scan(&preservedBudgetClock); err != nil {
		preOpenDB.Close()
		t.Fatal(err)
	}
	if err := preOpenDB.QueryRow(`SELECT state FROM scheduled_test_runs WHERE operation_id='34000000-0000-4000-8000-000000000001'`).Scan(&preOpenRunState); err != nil {
		preOpenDB.Close()
		t.Fatal(err)
	}
	preOpenDB.Close()
	if preservedBudgetClock != "2026-01-02T12:34:56.000000000Z" {
		t.Fatalf("pre-start budget clock=%q", preservedBudgetClock)
	}
	if preOpenRunState != "running" {
		t.Fatalf("restore unexpectedly rewrote scheduled run before service startup: %q", preOpenRunState)
	}

	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := service.Open(ctx, service.Config{DataDir: target, Listen: "127.0.0.1:0", Version: "backup-e2e-restored"})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredServer := httptest.NewServer(restored.Handler())
	defer restoredServer.Close()

	req, _ := http.NewRequest(http.MethodGet, restoredServer.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+employeeKey)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("restored employee key status=%d", response.StatusCode)
	}

	oldSessionReq, _ := http.NewRequest(http.MethodGet, restoredServer.URL+"/admin/api/v1/session", nil)
	for _, cookie := range client.Jar.Cookies(oldSessionReq.URL) {
		oldSessionReq.AddCookie(cookie)
	}
	oldSessionResponse, err := http.DefaultClient.Do(oldSessionReq)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, oldSessionResponse.Body)
	oldSessionResponse.Body.Close()
	if oldSessionResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("restored old admin session status=%d", oldSessionResponse.StatusCode)
	}

	newClient := newCookieClient(t)
	loginAdmin(t, newClient, restoredServer.URL, adminPassword)
	db, err := sql.Open("sqlite", filepath.Join(target, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var refreshState, reason string
	if err := db.QueryRow(`SELECT state,reason_code FROM codex_oauth_refresh_states WHERE upstream_id='ups_backup_codex'`).Scan(&refreshState, &reason); err != nil {
		t.Fatal(err)
	}
	if refreshState != "paused" || reason != "backup_restore_uncertain_refresh" {
		t.Fatalf("refresh state=%q reason=%q", refreshState, reason)
	}
	var restoredRunState, restoredRunResult string
	if err := db.QueryRow(`SELECT state,result_code FROM scheduled_test_runs WHERE operation_id='34000000-0000-4000-8000-000000000001'`).Scan(&restoredRunState, &restoredRunResult); err != nil {
		t.Fatal(err)
	}
	if restoredRunState != "completed" || restoredRunResult != "interrupted" {
		t.Fatalf("restored scheduled run state=%q result=%q", restoredRunState, restoredRunResult)
	}
	var archived, enabled, credentialBytes int
	if err := db.QueryRow(`SELECT archived,enabled,length(credential_ciphertext) FROM upstreams WHERE id=?`, archivedUpstreamID).Scan(&archived, &enabled, &credentialBytes); err != nil {
		t.Fatal(err)
	}
	if archived != 1 || enabled != 0 || credentialBytes != 0 {
		t.Fatalf("restored upstream tombstone archived=%d enabled=%d credential_bytes=%d", archived, enabled, credentialBytes)
	}
	var preserved int
	if err := db.QueryRow(`SELECT COUNT(*) FROM codex_oauth_bindings b JOIN upstreams u ON u.id=b.upstream_id WHERE b.upstream_id='ups_backup_codex' AND length(u.credential_ciphertext)>0`).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatalf("membership state count=%d err=%v", preserved, err)
	}

	// Package plaintext must not contain any credential or employee-key secret.
	packageBytes, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{employeeKey, "synthetic-upstream-secret", adminPassword} {
		if bytes.Contains(packageBytes, []byte(secret)) {
			t.Fatalf("encrypted package exposed a secret marker")
		}
	}
}

// Independently authored D2 actual-format/HTTP acceptance. Synthetic wrapping
// bytes in format 2 are not a test of the Windows DPAPI provider.
func TestD2RestoredSelfHTTP(t *testing.T) {
	for _, format := range []int{1, 2} {
		t.Run(fmt.Sprintf("format%d", format), func(t *testing.T) {
			ctx := context.Background()
			root := t.TempDir()
			cfg := service.Config{DataDir: filepath.Join(root, "source"), Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, Version: "d2-synthetic"}
			if err := service.Initialize(ctx, cfg.DataDir, strings.NewReader("synthetic-admin-password\n")); err != nil {
				t.Fatal("D2_INIT")
			}
			app, err := service.Open(ctx, cfg)
			if err != nil {
				t.Fatal("D2_SOURCE_OPEN")
			}
			defer app.Close()
			source := httptest.NewServer(app.Handler())
			defer source.Close()
			admin := newCookieClient(t)
			csrf := loginAdmin(t, admin, source.URL, "synthetic-admin-password")
			ids := make([]string, 5)
			keys := make([]string, 5)
			keyIDs := make([]string, 5)
			for i := range ids {
				e := adminJSON(t, admin, "POST", source.URL+"/admin/api/v1/employees", csrf, map[string]any{"name": "D2 Synthetic"}, 201)
				ids[i] = e["id"].(string)
				k := adminJSON(t, admin, "POST", source.URL+"/admin/api/v1/employees/"+ids[i]+"/keys", csrf, map[string]any{"name": "D2", "operation_id": fmt.Sprintf("d2-key-%d", i)}, 201)
				keys[i], keyIDs[i] = k["key"].(string), k["id"].(string)
			}
			adminJSON(t, admin, "POST", source.URL+"/admin/api/v1/keys/"+keyIDs[1]+"/revoke", csrf, map[string]any{}, 200)
			issue := func(base string, client *http.Client, token, id string) string {
				return adminJSON(t, client, "POST", base+"/admin/api/v1/employees/"+id+"/self-enrollment", token, map[string]any{}, 201)["enrollment_secret"].(string)
			}
			self := newCookieClient(t)
			secretA := issue(source.URL, admin, csrf, ids[0])
			d2HTTP(t, self, source.URL, "POST", "/self/api/v1/enroll", map[string]any{"employee_id": ids[0], "enrollment_secret": secretA, "password": "d2-long-synthetic-password"}, "", 200)
			d2HTTP(t, self, source.URL, "GET", "/self/api/v1/profile", nil, "", 200)
			pending := issue(source.URL, admin, csrf, ids[1])
			db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "cpa-cloud.db"))
			if err != nil {
				t.Fatal("D2_OBSERVER")
			}
			defer db.Close()
			// Nonempty slots, NULLs, BLOBs and public scalar facts are preserved.
			for i, state := range []string{"pending", "cancelled", "armed", "issued"} {
				var fingerprint, armedAt, issuedAt, cancelledAt any
				if i >= 2 {
					fingerprint = bytes.Repeat([]byte{0x23}, 32)
					armedAt = "2026-01-01T00:00:00Z"
				}
				if i == 1 {
					cancelledAt = "2026-01-02T00:00:00Z"
				}
				if i == 3 {
					issuedAt = "2026-01-02T00:00:00Z"
				}
				if _, err := db.Exec(`INSERT INTO employee_self_key_slots(employee_id,key_id,state,arm_fingerprint,created_at,armed_at,issued_at,cancelled_at) VALUES(?,?,?,?,'2026-01-01T00:00:00Z',?,?,?)`, ids[i], keyIDs[i], state, fingerprint, armedAt, issuedAt, cancelledAt); err != nil {
					t.Fatal("D2_SLOT_SEED")
				}
			}
			if _, err := db.Exec(`UPDATE employees SET status='disabled',revision=revision+1 WHERE id=?`, ids[3]); err != nil {
				t.Fatal("D2_DISABLED_SEED")
			}
			for i, key := range keys {
				want := 401
				if i == 4 {
					want = 200
				}
				d2HTTP(t, newCookieClient(t), source.URL, "GET", "/v1/models", nil, key, want)
			}
			ledger := financial.NewLedger(db)
			for i, amount := range []int64{137, -42} {
				kind := financial.EntryAdjustmentCredit
				if amount < 0 {
					kind = financial.EntryAdjustmentDebit
				}
				op := fmt.Sprintf("d2-public-ledger-%d", i)
				_, err := ledger.Post(ctx, financial.Post{OperationID: op, Action: "adjustment", Actor: financial.Actor{Kind: financial.ActorEmployee, ID: ids[0]}, ResourceKind: "adjustment", ResourceID: op, ObservedAt: time.Date(2026, 10, 1, 0, 0, i, 0, time.UTC), Entries: []financial.EntryInput{{Owner: financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: ids[0]}, Currency: "USD", Kind: kind, AmountMicro: amount, ResourceKind: "adjustment", ResourceID: op}}})
				if err != nil {
					t.Fatal("D2_LEDGER_SEED")
				}
			}
			before := d2Facts(t, db)
			credentials := d2Rows(t, db, `SELECT employee_id,password_hash,updated_at FROM employee_self_credentials ORDER BY employee_id`)
			pkg := filepath.Join(root, "snapshot.cpacb")
			material := backup.KeyMaterial{ProviderID: "d2_synthetic", ProviderKind: "windows-dpapi-user", ProviderVersion: 1}
			for i := range material.WrappingKey {
				material.WrappingKey[i] = byte(i + 1)
			}
			if format == 1 {
				_, err = backup.Create(ctx, cfg.DataDir, pkg, []byte("d2-synthetic-package-password"), "d2")
			} else {
				_, err = backup.CreateWithKeyMaterial(ctx, cfg.DataDir, pkg, material, "d2")
			}
			if err != nil {
				t.Fatal("D2_CREATE")
			}
			packageBytes, err := os.ReadFile(pkg)
			if err != nil {
				t.Fatal("D2_PACKAGE_READ")
			}
			// Positive control after snapshot: that snapshotted enrollment really
			// was unconsumed and usable, rather than an already invalid marker.
			d2HTTP(t, newCookieClient(t), source.URL, "POST", "/self/api/v1/enroll", map[string]any{"employee_id": ids[1], "enrollment_secret": pending, "password": "d2-long-synthetic-password"}, "", 200)
			sourceBeforeRestore := d2AllFacts(t, db)
			target := filepath.Join(root, "restored")
			if format == 1 {
				_, err = backup.Restore(ctx, pkg, target, []byte("d2-synthetic-package-password"))
			} else {
				_, err = backup.RestoreWithKeyMaterial(ctx, pkg, target, material)
			}
			if err != nil {
				t.Fatal("D2_RESTORE")
			}
			pre, err := sql.Open("sqlite", filepath.Join(target, "cpa-cloud.db"))
			if err != nil {
				t.Fatal("D2_PREOPEN")
			}
			// Capture before Open, which is itself a schema migrator.
			preserved := reflect.DeepEqual(before, d2Facts(t, pre)) && reflect.DeepEqual(credentials, d2Rows(t, pre, `SELECT employee_id,password_hash,updated_at FROM employee_self_credentials ORDER BY employee_id`))
			var sessions, enrollment int
			if err := pre.QueryRow(`SELECT count(*) FROM employee_self_sessions`).Scan(&sessions); err != nil {
				t.Fatal("D2_PREOPEN_SESSIONS")
			}
			if err := pre.QueryRow(`SELECT count(*) FROM employee_self_credentials WHERE enrollment_digest IS NOT NULL OR enrollment_expires_at IS NOT NULL`).Scan(&enrollment); err != nil {
				t.Fatal("D2_PREOPEN_ENROLLMENT")
			}
			if err := pre.Close(); err != nil {
				t.Fatal("D2_PREOPEN_CLOSE")
			}
			cfg.DataDir = target
			restored, err := service.Open(ctx, cfg)
			if err != nil {
				t.Fatal("D2_RESTORED_OPEN")
			}
			defer restored.Close()
			srv := httptest.NewServer(restored.Handler())
			defer srv.Close()
			// Copy the original cookie explicitly; a fresh jar would be a false
			// negative unrelated to restore sanitization.
			oldReq, _ := http.NewRequest("GET", source.URL+"/self/api/v1/profile", nil)
			old := newCookieClient(t)
			newURL, _ := http.NewRequest("GET", srv.URL+"/self/api/v1/profile", nil)
			old.Jar.SetCookies(newURL.URL, self.Jar.Cookies(oldReq.URL))
			cookieStatus, cookieSet := d2HTTP(t, old, srv.URL, "GET", "/self/api/v1/profile", nil, "", 0)
			enrollStatus, enrollSet := d2HTTP(t, newCookieClient(t), srv.URL, "POST", "/self/api/v1/enroll", map[string]any{"employee_id": ids[1], "enrollment_secret": pending, "password": "d2-long-synthetic-password"}, "", 0)
			if cookieStatus != 401 || enrollStatus != 401 || cookieSet || enrollSet || sessions != 0 || enrollment != 0 {
				t.Fatalf("D2_OLD_STATE_ACCEPTED cookie=%t enrollment=%t preopen_clean=%t", cookieStatus == 200, enrollStatus == 200, sessions == 0 && enrollment == 0)
			}
			if !preserved {
				t.Fatal("D2_NON_TARGET_FACTS_CHANGED")
			}
			negativeDB, err := sql.Open("sqlite", filepath.Join(target, "cpa-cloud.db"))
			if err != nil {
				t.Fatal("D2_NEGATIVE_DB")
			}
			var noPassword, noSession int
			if err := negativeDB.QueryRow(`SELECT password_hash IS NULL FROM employee_self_credentials WHERE employee_id=?`, ids[1]).Scan(&noPassword); err != nil {
				t.Fatal("D2_NEGATIVE_PASSWORD")
			}
			if err := negativeDB.QueryRow(`SELECT count(*)=0 FROM employee_self_sessions`).Scan(&noSession); err != nil {
				t.Fatal("D2_NEGATIVE_SESSION")
			}
			if err := negativeDB.Close(); err != nil || noPassword != 1 || noSession != 1 {
				t.Fatal("D2_NEGATIVE_WRITES")
			}
			fresh := newCookieClient(t)
			d2HTTP(t, fresh, srv.URL, "POST", "/self/api/v1/sessions", map[string]any{"employee_id": ids[0], "password": "d2-long-synthetic-password"}, "", 200)
			d2ProfileOwner(t, fresh, srv.URL, ids[0])
			for i, key := range keys {
				want := 401
				if i == 4 {
					want = 200
				}
				d2HTTP(t, newCookieClient(t), srv.URL, "GET", "/v1/models", nil, key, want)
			}
			oldAdminReq, _ := http.NewRequest("GET", source.URL+"/admin/api/v1/session", nil)
			oldAdmin := newCookieClient(t)
			restoredAdminURL, _ := http.NewRequest("GET", srv.URL+"/admin/api/v1/session", nil)
			oldAdmin.Jar.SetCookies(restoredAdminURL.URL, admin.Jar.Cookies(oldAdminReq.URL))
			d2HTTP(t, oldAdmin, srv.URL, "GET", "/admin/api/v1/session", nil, "", 401)
			newAdmin := newCookieClient(t)
			newCSRF := loginAdmin(t, newAdmin, srv.URL, "synthetic-admin-password")
			issue(srv.URL, newAdmin, newCSRF, ids[1])
			if !reflect.DeepEqual(sourceBeforeRestore, d2AllFacts(t, db)) {
				t.Fatal("D2_SOURCE_CHANGED")
			}
			afterBytes, err := os.ReadFile(pkg)
			if err != nil || !bytes.Equal(packageBytes, afterBytes) {
				t.Fatal("D2_PACKAGE_CHANGED")
			}
		})
	}
}

func d2ProfileOwner(t *testing.T, c *http.Client, base, id string) {
	t.Helper()
	r, err := c.Get(base + "/self/api/v1/profile")
	if err != nil {
		t.Fatal("D2_PROFILE_TRANSPORT")
	}
	defer r.Body.Close()
	var profile struct {
		ID string `json:"id"`
	}
	if r.StatusCode != 200 || json.NewDecoder(r.Body).Decode(&profile) != nil || profile.ID != id {
		t.Fatal("D2_PROFILE_OWNER")
	}
}

func d2AllFacts(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	out := d2Facts(t, db)
	for _, name := range []string{"sessions", "codex_oauth_sessions", "codex_oauth_refresh_states", "employee_self_sessions", "employee_self_credentials"} {
		out[name] = d2Rows(t, db, `SELECT * FROM "`+name+`"`)
	}
	return out
}

func d2HTTP(t *testing.T, c *http.Client, base, method, path string, body any, key string, want int) (int, bool) {
	t.Helper()
	var input io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal("D2_HTTP_INPUT")
		}
		input = bytes.NewReader(b)
	}
	r, err := http.NewRequest(method, base+path, input)
	if err != nil {
		t.Fatal("D2_HTTP_REQUEST")
	}
	r.Header.Set("Origin", base)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Self-Request", "1")
	if key != "" {
		r.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.Do(r)
	if err != nil {
		t.Fatal("D2_HTTP_TRANSPORT")
	}
	_, readErr := io.Copy(io.Discard, resp.Body)
	closeErr := resp.Body.Close()
	if readErr != nil || closeErr != nil {
		t.Fatal("D2_HTTP_BODY")
	}
	if want != 0 && resp.StatusCode != want {
		t.Fatalf("D2_HTTP_STATUS got=%d want=%d", resp.StatusCode, want)
	}
	return resp.StatusCode, len(resp.Header.Values("Set-Cookie")) != 0
}

// Complete logical facts, sorted independently of row order and preserving SQL
// storage types/NULL, not counts or lossy JSON number conversions. No fact dump.
func d2Rows(t *testing.T, db *sql.DB, query string) []string {
	t.Helper()
	rows, err := db.Query(query)
	if err != nil {
		t.Fatal("D2_FACT_QUERY")
	}
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal("D2_FACT_COLUMNS")
	}
	out := []string{}
	for rows.Next() {
		values := make([]any, len(cols))
		refs := make([]any, len(cols))
		for i := range values {
			refs[i] = &values[i]
		}
		if err := rows.Scan(refs...); err != nil {
			t.Fatal("D2_FACT_SCAN")
		}
		var b strings.Builder
		for _, v := range values {
			switch x := v.(type) {
			case nil:
				b.WriteString("N;")
			case []byte:
				fmt.Fprintf(&b, "B%x;", x)
			default:
				fmt.Fprintf(&b, "%T:%q;", x, fmt.Sprint(x))
			}
		}
		out = append(out, b.String())
	}
	if err := rows.Err(); err != nil {
		t.Fatal("D2_FACT_ROWS")
	}
	if err := rows.Close(); err != nil {
		t.Fatal("D2_FACT_CLOSE")
	}
	sort.Strings(out)
	return out
}
func d2Facts(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal("D2_TABLES")
	}
	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal("D2_TABLE_SCAN")
		}
		names = append(names, name)
	}
	if rows.Err() != nil || rows.Close() != nil {
		t.Fatal("D2_TABLE_ROWS")
	}
	out := map[string][]string{"@schema": d2Rows(t, db, `SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`)}
	for _, name := range names {
		switch name {
		case "sessions", "codex_oauth_sessions", "codex_oauth_refresh_states", "employee_self_sessions", "employee_self_credentials":
			continue
		}
		out[name] = d2Rows(t, db, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`"`)
	}
	return out
}

func newCookieClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func loginAdmin(t *testing.T, client *http.Client, baseURL, password string) string {
	t.Helper()
	requestBody, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
	response, err := client.Post(baseURL+"/admin/api/v1/sessions", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("login status=%d body=%s", response.StatusCode, body)
	}
	var result map[string]string
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result["csrf_token"]
}

func adminJSON(t *testing.T, client *http.Client, method, endpoint, csrf string, input map[string]any, want int) map[string]any {
	t.Helper()
	body, _ := json.Marshal(input)
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", originFor(endpoint))
	req.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("%s status=%d want=%d body=%s", endpoint, response.StatusCode, want, responseBody)
	}
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func originFor(endpoint string) string {
	for index := len("http://"); index < len(endpoint); index++ {
		if endpoint[index] == '/' {
			return endpoint[:index]
		}
	}
	panic(fmt.Sprintf("invalid endpoint %q", endpoint))
}
