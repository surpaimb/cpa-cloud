// Independently authored for docs/admin-key-revocation-confirmation-contract.md.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/financial"
)

type adminKeyRevocationFixture struct {
	*adminLogoutFixture
	target, sibling, other      keyView
	employeeID, otherEmployeeID string
}

func newAdminKeyRevocationFixture(t *testing.T) *adminKeyRevocationFixture {
	t.Helper()
	// Reuse the exact PR99-owned real Initialize/Open/login and nil-safe lifecycle.
	f := &adminKeyRevocationFixture{adminLogoutFixture: newAdminLogoutFixture(t)}
	f.employeeID = f.createEmployee("target")
	f.otherEmployeeID = f.createEmployee("control")
	f.target = f.createKey(f.employeeID, "target")
	f.sibling = f.createKey(f.employeeID, "sibling")
	f.other = f.createKey(f.otherEmployeeID, "other")
	if f.models(f.target.Key) != http.StatusOK || f.models(f.sibling.Key) != http.StatusOK || f.models(f.other.Key) != http.StatusOK {
		t.Fatal("AKR_VALID_PRODUCT_KEY_PRECONDITION")
	}
	return f
}

func (f *adminKeyRevocationFixture) admin(method, path, body string) *http.Response {
	f.t.Helper()
	r, err := http.NewRequestWithContext(f.t.Context(), method, f.server.URL+"/admin/api/v1"+path, strings.NewReader(body))
	if err != nil {
		f.t.Fatal("AKR_ADMIN_REQUEST_CREATE")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", f.server.URL)
	r.Header.Set("X-CSRF-Token", f.csrf)
	r.AddCookie(f.cookie)
	response, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal("AKR_ADMIN_REQUEST_HTTP")
	}
	return response
}

func (f *adminKeyRevocationFixture) decode(response *http.Response, status int, target any) {
	f.t.Helper()
	defer adminLogoutClose(response)
	if response.StatusCode != status || len(response.Header.Values("Set-Cookie")) != 0 {
		f.t.Fatal("AKR_PRODUCT_RESPONSE_STATUS")
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(target) != nil {
		f.t.Fatal("AKR_PRODUCT_RESPONSE_JSON")
	}
}

func (f *adminKeyRevocationFixture) createEmployee(suffix string) string {
	f.t.Helper()
	raw, err := json.Marshal(map[string]string{"name": "Synthetic revocation " + suffix})
	if err != nil {
		f.t.Fatal("AKR_EMPLOYEE_BODY")
	}
	var employee employee
	f.decode(f.admin(http.MethodPost, "/employees", string(raw)), http.StatusCreated, &employee)
	if employee.ID == "" || employee.Status != "active" {
		f.t.Fatal("AKR_ACTIVE_EMPLOYEE_PRECONDITION")
	}
	return employee.ID
}

func (f *adminKeyRevocationFixture) createKey(employeeID, suffix string) keyView {
	f.t.Helper()
	raw, err := json.Marshal(map[string]string{"name": "Synthetic " + suffix, "operation_id": "akr-" + suffix})
	if err != nil {
		f.t.Fatal("AKR_KEY_BODY")
	}
	var key keyView
	f.decode(f.admin(http.MethodPost, "/employees/"+employeeID+"/keys", string(raw)), http.StatusCreated, &key)
	if key.ID == "" || key.Key == "" || key.RevokedAt != nil || key.IssuanceState != nil {
		f.t.Fatal("AKR_ORDINARY_KEY_PRECONDITION")
	}
	return key
}

func (f *adminKeyRevocationFixture) models(key string) int {
	f.t.Helper()
	r, err := http.NewRequestWithContext(f.t.Context(), http.MethodGet, f.server.URL+"/v1/models", nil)
	if err != nil {
		f.t.Fatal("AKR_MODELS_REQUEST_CREATE")
	}
	r.Header.Set("Authorization", "Bearer "+key)
	response, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal("AKR_MODELS_REQUEST_HTTP")
	}
	defer adminLogoutClose(response)
	return response.StatusCode
}

func (f *adminKeyRevocationFixture) revoked(keyID string) sql.NullString {
	f.t.Helper()
	var value sql.NullString
	if f.app.store.db.QueryRow(`SELECT revoked_at FROM access_keys WHERE id=?`, keyID).Scan(&value) != nil {
		f.t.Fatal("AKR_REVOKED_READ")
	}
	return value
}

func (f *adminKeyRevocationFixture) controls() [][]any {
	f.t.Helper()
	var facts [][]any
	for _, query := range []string{
		`SELECT * FROM access_keys WHERE id<>? ORDER BY id`,
		`SELECT * FROM employees WHERE ?<>'' ORDER BY id`,
	} {
		rows, err := f.app.store.db.Query(query, f.target.ID)
		if err != nil {
			f.t.Fatal("AKR_CONTROL_QUERY")
		}
		columns, err := rows.Columns()
		if err != nil {
			_ = rows.Close()
			f.t.Fatal("AKR_CONTROL_COLUMNS")
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if rows.Scan(pointers...) != nil {
				_ = rows.Close()
				f.t.Fatal("AKR_CONTROL_SCAN")
			}
			for i, value := range values {
				if b, ok := value.([]byte); ok {
					values[i] = append([]byte(nil), b...)
				}
			}
			facts = append(facts, values)
		}
		readErr, closeErr := rows.Err(), rows.Close()
		if readErr != nil || closeErr != nil {
			f.t.Fatal("AKR_CONTROL_CLOSE")
		}
	}
	return facts
}

func (f *adminKeyRevocationFixture) assertControls(before [][]any) {
	f.t.Helper()
	if !reflect.DeepEqual(before, f.controls()) || f.models(f.sibling.Key) != http.StatusOK || f.models(f.other.Key) != http.StatusOK {
		f.t.Fatal("AKR_OTHER_KEY_EMPLOYEE_CHANGED")
	}
}

func (f *adminKeyRevocationFixture) offlineTrigger(trigger string) {
	f.t.Helper()
	if f.closeOwned() != nil {
		f.t.Fatal("AKR_PRE_TRIGGER_CLOSE")
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "cpa-cloud.db"))
	if err != nil {
		f.t.Fatal("AKR_OFFLINE_OPEN")
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(f.t.Context(), 10*time.Second)
	defer cancel()
	_, writeErr := db.ExecContext(ctx, trigger)
	closeErr := db.Close()
	if writeErr != nil || closeErr != nil {
		f.t.Fatal("AKR_REAL_TRIGGER_INSTALL")
	}
	// Must really reopen successfully; an Open failure is not the expected oracle.
	if f.openOwned() != nil {
		f.t.Fatal("AKR_TRIGGER_PROFILE_OPEN")
	}
}

func (f *adminKeyRevocationFixture) assert200(response *http.Response) {
	f.t.Helper()
	var result map[string]bool
	f.decode(response, http.StatusOK, &result)
	if len(result) != 1 || !result["ok"] {
		f.t.Fatal("AKR_CONFIRMED_SUCCESS_BODY")
	}
}

func TestAdminKeyRevocationTriggerReversal(t *testing.T) {
	f := newAdminKeyRevocationFixture(t)
	before := f.controls()
	if f.revoked(f.target.ID).Valid {
		t.Fatal("AKR_INITIAL_TARGET_REVOKED")
	}
	target := strings.ReplaceAll(f.target.ID, "'", "''")
	f.offlineTrigger(`CREATE TRIGGER akr_restore_key AFTER UPDATE OF revoked_at ON access_keys WHEN NEW.id='` + target + `' AND NEW.revoked_at IS NOT NULL BEGIN UPDATE access_keys SET revoked_at=NULL WHERE id=NEW.id; END`)
	if f.revoked(f.target.ID).Valid || f.models(f.target.Key) != http.StatusOK {
		t.Fatal("AKR_REOPENED_VALID_TARGET_PRECONDITION")
	}
	response := f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`)
	status := response.StatusCode
	if status == http.StatusOK {
		f.assert200(response)
	} else if status == http.StatusServiceUnavailable {
		f.assert503(response)
	} else {
		adminLogoutClose(response)
		t.Fatal("AKR_TRIGGER_UNEXPECTED_HTTP_STATUS")
	}
	presentActive := !f.revoked(f.target.ID).Valid && f.models(f.target.Key) == http.StatusOK
	f.assertControls(before)
	f.reopen()
	reopenedActive := !f.revoked(f.target.ID).Valid && f.models(f.target.Key) == http.StatusOK
	f.assertControls(before)
	if status == http.StatusOK && presentActive && reopenedActive {
		t.Fatal("ADMIN_KEY_REVOKE_OLD_TRIGGER_ACCEPTED_ACTIVE_KEY")
	}
	if status != http.StatusServiceUnavailable || !presentActive || !reopenedActive {
		t.Fatal("AKR_TRIGGER_FAIL_CLOSED_ORACLE")
	}
}

func (f *adminKeyRevocationFixture) setHooks(hooks adminKeyRevocationHooks) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.transform = func(r *http.Request) *http.Request {
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/revoke") {
			return r.WithContext(context.WithValue(r.Context(), adminKeyRevocationHooksKey{}, hooks))
		}
		return r
	}
}

func (f *adminKeyRevocationFixture) reopenConfigured(self, loopback bool) {
	f.t.Helper()
	cfg := f.app.cfg
	cfg.EmployeeSelfServiceEnabled = self
	cfg.AllowLoopbackUpstream = loopback
	if f.closeOwned() != nil {
		f.t.Fatal("AKR_CONFIGURED_CLOSE")
	}
	ctx, cancel := context.WithTimeout(f.t.Context(), 30*time.Second)
	defer cancel()
	next, err := Open(ctx, cfg)
	if err != nil || next == nil {
		f.t.Fatal("AKR_CONFIGURED_OPEN")
	}
	f.mu.Lock()
	f.transform = nil
	f.mu.Unlock()
	f.app, f.appCloseAttempted = next, false
	f.startServer()
}

func keyRevocationError(t *testing.T, response *http.Response, status int, code, message string) {
	t.Helper()
	defer adminLogoutClose(response)
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	var body struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err != nil || len(raw) > 4096 || response.StatusCode != status || len(response.Header.Values("Set-Cookie")) != 0 || json.Unmarshal(raw, &body) != nil || body.Error.Code != code || body.Error.Message != message {
		t.Fatal("AKR_FIXED_ERROR_RESPONSE")
	}
}

func keyRevocationHTTP(t *testing.T, app *App, baseURL string, cookie *http.Cookie, csrf, keyID string) *http.Response {
	t.Helper()
	r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, baseURL+"/admin/api/v1/keys/"+keyID+"/revoke", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal("AKR_ADAPTER_REQUEST")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", baseURL)
	r.Header.Set("X-CSRF-Token", csrf)
	r.AddCookie(cookie)
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(r)
	if err != nil {
		t.Fatal("AKR_ADAPTER_HTTP")
	}
	return response
}

func (f *adminKeyRevocationFixture) slotFacts() [32]byte {
	return retirementQueryDigest(f.t, f.app.store.db, `SELECT * FROM employee_self_key_slots WHERE key_id=?`, f.target.ID)
}

func (f *adminKeyRevocationFixture) setupSlot(state string) (*http.Cookie, string) {
	f.t.Helper()
	f.reopenConfigured(true, true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"synthetic","object":"chat.completion","choices":[]}`))
	}))
	f.t.Cleanup(upstream.Close)
	var upstreamObject upstreamView
	f.decode(f.admin(http.MethodPost, "/upstreams", `{"name":"Synthetic slot","provider_kind":"openai-compatible","endpoint":`+quoteJSON(upstream.URL)+`,"api_key":"synthetic-key"}`), http.StatusCreated, &upstreamObject)
	response := f.admin(http.MethodPost, "/models", `{"id":"slot-model","upstream_id":`+quoteJSON(upstreamObject.ID)+`,"upstream_model":"synthetic-model"}`)
	if response.StatusCode != http.StatusCreated {
		adminLogoutClose(response)
		f.t.Fatal("AKR_SLOT_MODEL_CREATE")
	}
	adminLogoutClose(response)
	selfCookie, selfCSRF := f.enrollSelf()
	var reserved keyView
	policy := `{"protocol_mode":"selected","protocols":["openai-chat"],"model_mode":"selected","models":["slot-model"],"source_mode":"all","source_cidrs":[],"account_group_mode":"all","account_group_ids":[]}`
	f.decode(f.admin(http.MethodPost, "/employees/"+f.employeeID+"/self-key-slot", `{"name":"Synthetic reserved Key","operation_id":"akr-slot","expires_at":null,"policy":`+policy+`}`), http.StatusCreated, &reserved)
	if reserved.ID == "" || reserved.Key != "" || reserved.IssuanceState == nil || *reserved.IssuanceState != "pending" {
		f.t.Fatal("AKR_SLOT_RESERVATION")
	}
	f.target = reserved
	if state == "pending" {
		return selfCookie, selfCSRF
	}
	adapter := selfSlotFixture{selfPasswordFixture: selfPasswordFixture{app: f.app, server: f.server, id: f.employeeID, adminCookie: f.cookie, adminCSRF: f.csrf, cookie: selfCookie, csrf: selfCSRF, dir: f.dir}, keyID: f.target.ID}
	retirementSlotLimits(f.t, adapter) // Existing helper uses real administration receipts, not orphan SQL policies.
	if adapter.arm(f.t) != http.StatusOK {
		f.t.Fatal("AKR_SLOT_ARM")
	}
	if state == "armed" {
		return selfCookie, selfCSRF
	}
	if state == "issued" {
		var issued struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		}
		f.decode(adapter.issue(f.t, selfOldPassword), http.StatusCreated, &issued)
		if issued.ID != f.target.ID || issued.Key == "" {
			f.t.Fatal("AKR_SLOT_ISSUE_ID")
		}
		f.target.Key = issued.Key
		if f.models(f.target.Key) != http.StatusOK {
			f.t.Fatal("AKR_ISSUED_KEY_AUTH_PRECONDITION")
		}
		return selfCookie, selfCSRF
	}
	if state == "cancelled" {
		response := f.admin(http.MethodPost, "/keys/"+f.target.ID+"/self-key-slot/cancel", `{}`)
		if response.StatusCode != http.StatusNoContent {
			adminLogoutClose(response)
			f.t.Fatal("AKR_SLOT_CANCEL_SETUP")
		}
		adminLogoutClose(response)
		return selfCookie, selfCSRF
	}
	f.t.Fatal("AKR_UNKNOWN_SLOT_SETUP")
	return nil, ""
}

func (f *adminKeyRevocationFixture) issueSlot(cookie *http.Cookie, csrf string) *http.Response {
	f.t.Helper()
	r, err := http.NewRequestWithContext(f.t.Context(), http.MethodPost, f.server.URL+"/self/api/v1/keys/issue", strings.NewReader(`{"slot_id":`+quoteJSON(f.target.ID)+`,"current_password":`+quoteJSON(selfOldPassword)+`}`))
	if err != nil {
		f.t.Fatal("AKR_SELF_ISSUE_REQUEST")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", f.server.URL)
	r.Header.Set("X-Self-Request", "1")
	r.Header.Set("X-CSRF-Token", csrf)
	r.AddCookie(cookie)
	response, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal("AKR_SELF_ISSUE_HTTP")
	}
	return response
}

func (f *adminKeyRevocationFixture) assertSlotFinal(originalState string, before [32]byte) {
	f.t.Helper()
	if originalState == "issued" || originalState == "cancelled" {
		if f.slotFacts() != before {
			f.t.Fatal("AKR_TERMINAL_SLOT_TOMBSTONE_CHANGED")
		}
		return
	}
	var owner, keyID, state, created string
	var fp []byte
	var e, p, g, b sql.NullInt64
	var armed, issued, cancelled sql.NullString
	err := f.app.store.db.QueryRow(`SELECT employee_id,key_id,state,created_at,arm_fingerprint,arm_employee_revision,arm_key_policy_revision,arm_governance_revision,arm_budget_revision,armed_at,issued_at,cancelled_at FROM employee_self_key_slots WHERE key_id=?`, f.target.ID).Scan(&owner, &keyID, &state, &created, &fp, &e, &p, &g, &b, &armed, &issued, &cancelled)
	when, timeErr := parseTime(cancelled.String)
	if err != nil || owner != f.employeeID || keyID != f.target.ID || state != "cancelled" || created == "" || !cancelled.Valid || timeErr != nil || when.IsZero() || len(fp) != 0 || e.Valid || p.Valid || g.Valid || b.Valid || armed.Valid || issued.Valid {
		f.t.Fatal("AKR_CANCELLED_SLOT_POSTCONDITIONS")
	}
}

func (f *adminKeyRevocationFixture) assertNoReissue(cookie *http.Cookie, csrf string) {
	f.t.Helper()
	response := f.issueSlot(cookie, csrf)
	defer adminLogoutClose(response)
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4097))
	if response.StatusCode != http.StatusConflict || err != nil || len(raw) > 4096 || strings.Contains(string(raw), "cpac_") {
		f.t.Fatal("AKR_TERMINAL_SLOT_REISSUED")
	}
}

func (f *adminKeyRevocationFixture) enrollSelf() (*http.Cookie, string) {
	f.t.Helper()
	var issued struct {
		Secret string `json:"enrollment_secret"`
	}
	f.decode(f.admin(http.MethodPost, "/employees/"+f.employeeID+"/self-enrollment", `{}`), http.StatusCreated, &issued)
	if issued.Secret == "" {
		f.t.Fatal("AKR_ENROLLMENT_SECRET_MISSING")
	}
	r, err := http.NewRequestWithContext(f.t.Context(), http.MethodPost, f.server.URL+"/self/api/v1/enroll", strings.NewReader(`{"employee_id":`+quoteJSON(f.employeeID)+`,"enrollment_secret":`+quoteJSON(issued.Secret)+`,"password":`+quoteJSON(selfOldPassword)+`}`))
	if err != nil {
		f.t.Fatal("AKR_ENROLL_REQUEST")
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", f.server.URL)
	r.Header.Set("X-Self-Request", "1")
	response, err := f.client.Do(r)
	if err != nil {
		f.t.Fatal("AKR_ENROLL_HTTP")
	}
	defer adminLogoutClose(response)
	var result struct {
		CSRF    string `json:"csrf_token"`
		Profile struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"profile"`
	}
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result) != nil || result.CSRF == "" || result.Profile.ID != f.employeeID || result.Profile.Status != "active" || len(response.Cookies()) != 1 || response.Cookies()[0].Value == "" {
		f.t.Fatal("AKR_ENROLL_RESPONSE")
	}
	return response.Cookies()[0], result.CSRF
}

func TestAdminKeyRevocationPersistenceAndOldTime(t *testing.T) {
	for _, name := range []string{"fresh", "already-revoked"} {
		t.Run(name, func(t *testing.T) {
			f := newAdminKeyRevocationFixture(t)
			if name == "already-revoked" {
				f.exec(`UPDATE access_keys SET revoked_at='2026-01-01T00:00:00Z' WHERE id=?`, f.target.ID)
			}
			prior := f.revoked(f.target.ID)
			ledger := financial.NewLedger(f.app.store.db)
			_, err := ledger.Post(t.Context(), financial.Post{OperationID: "akr-financial", Action: "adjustment", Actor: financial.Actor{Kind: financial.ActorEmployee, ID: f.employeeID}, ResourceKind: "adjustment", ResourceID: f.employeeID, ObservedAt: time.Now().UTC(), Entries: []financial.EntryInput{{Owner: financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.employeeID}, Currency: "USD", Kind: financial.EntryAdjustmentCredit, AmountMicro: 42, ResourceKind: "adjustment", ResourceID: f.employeeID}}})
			if err != nil {
				t.Fatal("AKR_FINANCIAL_CONTROL_SETUP")
			}
			before := f.controls()
			stable := retirementSnapshot(t, f.app.store.db, true)
			audit := retirementQueryDigest(t, f.app.store.db, `SELECT * FROM account_lifecycle_audit ORDER BY rowid`)
			var notified atomic.Int32
			f.setHooks(adminKeyRevocationHooks{afterNotify: func() { notified.Add(1) }})
			f.assert200(f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`))
			confirmed := f.revoked(f.target.ID)
			stamp, err := parseTime(confirmed.String)
			if !confirmed.Valid || err != nil || stamp.IsZero() || (prior.Valid && confirmed != prior) || f.models(f.target.Key) != http.StatusUnauthorized || notified.Load() != 1 {
				t.Fatal("AKR_DURABLE_REVOKE_POSTCONDITION")
			}
			f.assertControls(before)
			if !reflect.DeepEqual(stable, retirementSnapshot(t, f.app.store.db, true)) || audit != retirementQueryDigest(t, f.app.store.db, `SELECT * FROM account_lifecycle_audit ORDER BY rowid`) {
				t.Fatal("AKR_QUOTA_LEDGER_OR_AUDIT_CHANGED")
			}
			f.assert200(f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`))
			if f.revoked(f.target.ID) != confirmed || notified.Load() != 2 {
				t.Fatal("AKR_IDEMPOTENT_TIME_RESIGNED")
			}
			f.reopen()
			if f.revoked(f.target.ID) != confirmed || f.models(f.target.Key) != http.StatusUnauthorized {
				t.Fatal("AKR_REOPEN_REVOKE_PERSISTENCE")
			}
			f.assertControls(before)
		})
	}
}

func TestAdminKeyRevocationNativeErrorsAndMissing(t *testing.T) {
	for _, name := range []string{"abort", "ignore", "zero-after-read", "key-query-error", "slot-query-error", "missing-key", "begin-real-after-auth", "begin-adapter", "old-time-empty", "old-time-malformed", "old-time-blob"} {
		t.Run(name, func(t *testing.T) {
			f := newAdminKeyRevocationFixture(t)
			var entered, removed atomic.Bool
			var notified atomic.Int32
			h := adminKeyRevocationHooks{afterNotify: func() { notified.Add(1) }}
			switch name {
			case "abort", "ignore":
				raise := `RAISE(ABORT,'synthetic-key-revocation')`
				if name == "ignore" {
					raise = `RAISE(IGNORE)`
				}
				f.offlineTrigger(`CREATE TRIGGER akr_native_error BEFORE UPDATE OF revoked_at ON access_keys WHEN NEW.id='` + strings.ReplaceAll(f.target.ID, "'", "''") + `' BEGIN SELECT ` + raise + `; END`)
			case "zero-after-read":
				h.afterRead = func(tx *sql.Tx) error {
					r, e := tx.Exec(`DELETE FROM access_keys WHERE id=?`, f.target.ID)
					if e != nil {
						return e
					}
					n, e := r.RowsAffected()
					removed.Store(e == nil && n == 1)
					return e
				}
			case "key-query-error", "slot-query-error":
				h.afterBegin = func(tx *sql.Tx) error {
					q := `ALTER TABLE access_keys RENAME TO akr_hidden_keys`
					if name == "slot-query-error" {
						q = `ALTER TABLE employee_self_key_slots RENAME TO akr_hidden_slots`
					}
					_, e := tx.Exec(q)
					entered.Store(e == nil)
					return e
				}
			case "begin-real-after-auth":
				h.beforeBegin = func(context.Context) error { e := f.app.store.db.Close(); entered.Store(e == nil); return e }
			case "begin-adapter":
				h.beforeBegin = func(context.Context) error { entered.Store(true); return errors.New("controlled begin refusal") }
			case "old-time-empty":
				f.exec(`UPDATE access_keys SET revoked_at='' WHERE id=?`, f.target.ID)
			case "old-time-malformed":
				f.exec(`UPDATE access_keys SET revoked_at='not-a-time' WHERE id=?`, f.target.ID)
			case "old-time-blob":
				f.exec(`UPDATE access_keys SET revoked_at=x'0102' WHERE id=?`, f.target.ID)
			}
			before := f.controls()
			prior := f.revoked(f.target.ID)
			keyID := f.target.ID
			if name == "missing-key" {
				keyID = "key_akr_missing"
			}
			f.setHooks(h)
			response := f.admin(http.MethodPost, "/keys/"+keyID+"/revoke", `{}`)
			if name == "missing-key" {
				keyRevocationError(t, response, http.StatusNotFound, "not_found", "Key was not found.")
			} else {
				f.assert503(response)
			}
			if notified.Load() != 0 {
				t.Fatal("AKR_FAILURE_NOTIFIED_SUCCESS")
			}
			if name == "zero-after-read" && !removed.Load() {
				t.Fatal("AKR_ZERO_REAL_DELETE_PREREQUISITE")
			}
			if (name == "key-query-error" || name == "slot-query-error" || name == "begin-real-after-auth" || name == "begin-adapter") && !entered.Load() {
				t.Fatal("AKR_FAILURE_BOUNDARY_NOT_ENTERED")
			}
			if name == "begin-real-after-auth" {
				f.reopen()
			}
			if f.revoked(f.target.ID) != prior {
				t.Fatal("AKR_FAILURE_CHANGED_REVOCATION_FACT")
			}
			f.assertControls(before)
			if !strings.HasPrefix(name, "old-time-") {
				f.reopen()
				if f.revoked(f.target.ID) != prior {
					t.Fatal("AKR_FAILURE_REOPEN_FACT")
				}
				f.assertControls(before)
			}
		})
	}
}

func TestAdminKeyRevocationCommitOutcomes(t *testing.T) {
	for _, name := range []string{"before-real-commit", "after-real-commit-report", "real-native-refusal"} {
		t.Run(name, func(t *testing.T) {
			f := newAdminKeyRevocationFixture(t)
			before := f.controls()
			live := f.app
			var entered, observed, rejected atomic.Bool
			var notified atomic.Int32
			h := adminKeyRevocationHooks{afterNotify: func() { notified.Add(1) }}
			if name == "before-real-commit" {
				h.beforeCommit = func(*sql.Tx) error { entered.Store(true); return errors.New("controlled pre-commit refusal") }
			} else if name == "after-real-commit-report" {
				h.afterCommit = func(real error) error {
					observed.Store(true)
					rejected.Store(real != nil)
					return errors.New("controlled post-real-commit report")
				}
			} else {
				f.exec(`CREATE TABLE akr_commit_guard(owner TEXT REFERENCES admins(id) DEFERRABLE INITIALLY DEFERRED)`)
				h.beforeCommit = func(tx *sql.Tx) error {
					_, e := tx.Exec(`INSERT INTO akr_commit_guard(owner) VALUES('akr-no-such-admin')`)
					entered.Store(e == nil)
					return e
				}
				h.afterCommit = func(real error) error { observed.Store(true); rejected.Store(real != nil); return nil }
			}
			f.setHooks(h)
			f.assert503(f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`))
			committed := name == "after-real-commit-report"
			if notified.Load() != 0 || (name == "before-real-commit" && (!entered.Load() || observed.Load())) || (name == "after-real-commit-report" && (!observed.Load() || rejected.Load())) || (name == "real-native-refusal" && (!entered.Load() || !observed.Load() || !rejected.Load())) {
				t.Fatal("AKR_COMMIT_BOUNDARY_OBSERVATION")
			}
			if name == "real-native-refusal" {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				tx, err := f.app.store.db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal("AKR_NATIVE_LIVE_NEXT_BEGIN")
				}
				defer tx.Rollback()
				var fk, rows int
				if tx.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk) != nil || fk != 1 || tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM akr_commit_guard`).Scan(&rows) != nil || rows != 0 {
					t.Fatal("AKR_NATIVE_LIVE_NEXT_READ")
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO akr_commit_guard(owner) VALUES(?)`, f.adminLogoutFixture.admin); err != nil {
					t.Fatal("AKR_NATIVE_LIVE_NEXT_WRITE")
				}
				if tx.Commit() != nil || f.app != live || f.appCloseAttempted {
					t.Fatal("AKR_NATIVE_LIVE_NEXT_COMMIT")
				}
			}
			if f.revoked(f.target.ID).Valid != committed {
				t.Fatal("AKR_COMMIT_UNCERTAINTY_FACT")
			}
			want := http.StatusOK
			if committed {
				want = http.StatusUnauthorized
			}
			if f.models(f.target.Key) != want {
				t.Fatal("AKR_COMMIT_UNCERTAINTY_AUTH")
			}
			f.assertControls(before)
			f.reopen()
			if f.revoked(f.target.ID).Valid != committed || f.models(f.target.Key) != want {
				t.Fatal("AKR_COMMIT_UNCERTAINTY_REOPEN")
			}
			f.assertControls(before)
		})
	}
}

func TestAdminKeyRevocationAuthPrecedence(t *testing.T) {
	for _, name := range []string{"anonymous", "wrong-cookie", "missing-origin", "wrong-origin", "missing-csrf", "wrong-csrf", "invalid-json"} {
		t.Run(name, func(t *testing.T) {
			f := newAdminKeyRevocationFixture(t)
			before := f.controls()
			var entered atomic.Int32
			f.setHooks(adminKeyRevocationHooks{beforeBegin: func(context.Context) error { entered.Add(1); return errors.New("unauthorized boundary must not run") }})
			cookie, csrf, origin, body := f.cookie, f.csrf, f.server.URL, `{}`
			want := http.StatusForbidden
			switch name {
			case "anonymous":
				cookie = nil
				want = http.StatusUnauthorized
			case "wrong-cookie":
				copy := *cookie
				copy.Value = "synthetic-wrong-cookie"
				cookie = &copy
				want = http.StatusUnauthorized
			case "missing-origin":
				origin = ""
			case "wrong-origin":
				origin = "http://wrong-origin.invalid"
			case "missing-csrf":
				csrf = ""
			case "wrong-csrf":
				csrf = "synthetic-wrong-csrf"
			case "invalid-json":
				body = `{"unknown":1}`
				want = http.StatusBadRequest
			}
			r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, f.server.URL+"/admin/api/v1/keys/"+f.target.ID+"/revoke", strings.NewReader(body))
			if err != nil {
				t.Fatal("AKR_AUTH_REQUEST")
			}
			r.Header.Set("Content-Type", "application/json")
			if origin != "" {
				r.Header.Set("Origin", origin)
			}
			if csrf != "" {
				r.Header.Set("X-CSRF-Token", csrf)
			}
			if cookie != nil {
				r.AddCookie(cookie)
			}
			response, err := f.client.Do(r)
			if err != nil {
				t.Fatal("AKR_AUTH_HTTP")
			}
			if response.StatusCode != want || len(response.Header.Values("Set-Cookie")) != 0 {
				adminLogoutClose(response)
				t.Fatal("AKR_AUTH_PRECEDENCE")
			}
			adminLogoutClose(response)
			if entered.Load() != 0 || f.revoked(f.target.ID).Valid || f.models(f.target.Key) != http.StatusOK {
				t.Fatal("AKR_AUTH_MUTATED_OR_ENTERED")
			}
			f.assertControls(before)
		})
	}
}

// Diagnostic-only continuation: no Error() text, SQL or identity reaches output.
// Code() fixtures below are synthetic classifiers, not native failure injection.
type adminKeyRevocationSlotTriggerDiagnostic struct {
	result, errorContext, contextState, sqliteCode, elapsed string
}

func adminKeyRevocationTriggerContextClass(err error) string {
	switch {
	case err == nil:
		return "none"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	default:
		return "other"
	}
}

func adminKeyRevocationTriggerSQLiteCode(err error) string {
	if err == nil {
		return "none"
	}
	var coded interface{ Code() int }
	if errors.As(err, &coded) {
		// Exact reviewed SQLite codes only; do not print arbitrary Code() values.
		switch coded.Code() {
		case 1:
			return "1"
		case 4:
			return "4"
		case 5:
			return "5"
		case 6:
			return "6"
		case 9:
			return "9"
		case 10:
			return "10"
		case 19:
			return "19"
		case 261:
			return "261"
		case 517:
			return "517"
		case 773:
			return "773"
		case 262:
			return "262"
		case 518:
			return "518"
		case 4106:
			return "4106"
		}
	}
	return "unknown"
}

func adminKeyRevocationTriggerElapsed(elapsed time.Duration) string {
	switch {
	case elapsed < 0:
		return "invalid"
	case elapsed < time.Millisecond:
		return "lt1ms"
	case elapsed < 100*time.Millisecond:
		return "1ms_lt100ms"
	case elapsed < time.Second:
		return "100ms_lt1s"
	case elapsed < 10*time.Second:
		return "1s_lt10s"
	case elapsed < time.Minute:
		return "10s_lt60s"
	default:
		return "ge60s"
	}
}

func adminKeyRevocationClassifyTrigger(err, contextErr error, elapsed time.Duration) adminKeyRevocationSlotTriggerDiagnostic {
	result := "ok"
	if err != nil {
		result = "error"
	}
	return adminKeyRevocationSlotTriggerDiagnostic{
		result: result, errorContext: adminKeyRevocationTriggerContextClass(err),
		contextState: adminKeyRevocationTriggerContextClass(contextErr),
		sqliteCode:   adminKeyRevocationTriggerSQLiteCode(err),
		elapsed:      adminKeyRevocationTriggerElapsed(elapsed),
	}
}

func adminKeyRevocationSlotTriggerFailurePhase(execErr, closeErr error) string {
	switch {
	case execErr != nil && closeErr != nil:
		return "exec_and_close"
	case execErr != nil:
		return "exec"
	case closeErr != nil:
		return "close"
	default:
		return "none"
	}
}

type adminKeyRevocationDiagnosticCodeError int

func (e adminKeyRevocationDiagnosticCodeError) Code() int { return int(e) }
func (adminKeyRevocationDiagnosticCodeError) Error() string {
	panic("AKR_DIAGNOSTIC_ERROR_STRING_USED")
}

type adminKeyRevocationDiagnosticUnknownError struct{}

func (adminKeyRevocationDiagnosticUnknownError) Error() string {
	panic("AKR_DIAGNOSTIC_ERROR_STRING_USED")
}

func TestAdminKeyRevocationSlotTriggerDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, phase                          string
		execErr, closeErr, execCtx, closeCtx error
		execWant, closeWant                  adminKeyRevocationSlotTriggerDiagnostic
	}{
		{
			name: "exec-only", phase: "exec",
			execErr: errors.Join(context.DeadlineExceeded, adminKeyRevocationDiagnosticCodeError(5)),
			execCtx: context.DeadlineExceeded, closeCtx: context.DeadlineExceeded,
			execWant:  adminKeyRevocationSlotTriggerDiagnostic{"error", "deadline", "deadline", "5", "1ms_lt100ms"},
			closeWant: adminKeyRevocationSlotTriggerDiagnostic{"ok", "none", "deadline", "none", "1ms_lt100ms"},
		},
		{
			name: "close-only", phase: "close", closeErr: adminKeyRevocationDiagnosticCodeError(4106),
			execWant:  adminKeyRevocationSlotTriggerDiagnostic{"ok", "none", "none", "none", "1ms_lt100ms"},
			closeWant: adminKeyRevocationSlotTriggerDiagnostic{"error", "other", "none", "4106", "1ms_lt100ms"},
		},
		{
			name: "exec-and-close", phase: "exec_and_close",
			execErr: errors.Join(adminKeyRevocationDiagnosticCodeError(517)), closeErr: context.Canceled, closeCtx: context.Canceled,
			execWant:  adminKeyRevocationSlotTriggerDiagnostic{"error", "other", "none", "517", "1ms_lt100ms"},
			closeWant: adminKeyRevocationSlotTriggerDiagnostic{"error", "cancelled", "cancelled", "unknown", "1ms_lt100ms"},
		},
		{
			name: "unknown", phase: "exec", execErr: adminKeyRevocationDiagnosticUnknownError{},
			execWant:  adminKeyRevocationSlotTriggerDiagnostic{"error", "other", "none", "unknown", "1ms_lt100ms"},
			closeWant: adminKeyRevocationSlotTriggerDiagnostic{"ok", "none", "none", "none", "1ms_lt100ms"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec := adminKeyRevocationClassifyTrigger(tc.execErr, tc.execCtx, 2*time.Millisecond)
			closed := adminKeyRevocationClassifyTrigger(tc.closeErr, tc.closeCtx, 3*time.Millisecond)
			if exec != tc.execWant || closed != tc.closeWant || adminKeyRevocationSlotTriggerFailurePhase(tc.execErr, tc.closeErr) != tc.phase {
				t.Fatal("AKR_SLOT_TRIGGER_DIAGNOSTIC_CLASSIFICATION")
			}
		})
	}
}

func TestAdminKeyRevocationSlotTriggerDiagnosticBoundaries(t *testing.T) {
	for _, tc := range []struct {
		code int
		want string
	}{{1, "1"}, {4, "4"}, {5, "5"}, {6, "6"}, {9, "9"}, {10, "10"}, {19, "19"}, {261, "261"}, {517, "517"}, {773, "773"}, {262, "262"}, {518, "518"}, {4106, "4106"}, {-1, "unknown"}, {0, "unknown"}, {999, "unknown"}, {1 << 30, "unknown"}} {
		if adminKeyRevocationTriggerSQLiteCode(errors.Join(adminKeyRevocationDiagnosticCodeError(tc.code))) != tc.want {
			t.Fatal("AKR_SLOT_TRIGGER_DIAGNOSTIC_CODE_ALLOWLIST")
		}
	}
	for _, tc := range []struct {
		elapsed time.Duration
		want    string
	}{{-1, "invalid"}, {0, "lt1ms"}, {time.Millisecond - 1, "lt1ms"}, {time.Millisecond, "1ms_lt100ms"}, {100*time.Millisecond - 1, "1ms_lt100ms"}, {100 * time.Millisecond, "100ms_lt1s"}, {time.Second - 1, "100ms_lt1s"}, {time.Second, "1s_lt10s"}, {10*time.Second - 1, "1s_lt10s"}, {10 * time.Second, "10s_lt60s"}, {time.Minute - 1, "10s_lt60s"}, {time.Minute, "ge60s"}, {time.Hour, "ge60s"}} {
		if adminKeyRevocationTriggerElapsed(tc.elapsed) != tc.want {
			t.Fatal("AKR_SLOT_TRIGGER_DIAGNOSTIC_ELAPSED_BOUNDARY")
		}
	}
	if adminKeyRevocationSlotTriggerFailurePhase(nil, nil) != "none" || adminKeyRevocationTriggerSQLiteCode(nil) != "none" || adminKeyRevocationTriggerContextClass(errors.Join(context.Canceled)) != "cancelled" || adminKeyRevocationTriggerContextClass(errors.Join(context.Canceled, context.DeadlineExceeded)) != "deadline" {
		t.Fatal("AKR_SLOT_TRIGGER_DIAGNOSTIC_CONTEXT_BOUNDARY")
	}
}

func (f *adminKeyRevocationFixture) offlineSlotTrigger(trigger string) {
	f.t.Helper()
	self, loopback := f.app.cfg.EmployeeSelfServiceEnabled, f.app.cfg.AllowLoopbackUpstream
	if f.closeOwned() != nil {
		f.t.Fatal("AKR_SLOT_TRIGGER_CLOSE")
	}
	db, err := sql.Open("sqlite", filepath.Join(f.dir, "cpa-cloud.db"))
	if err != nil {
		f.t.Fatal("AKR_SLOT_TRIGGER_RAW_OPEN")
	}
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(f.t.Context(), 10*time.Second)
	defer cancel()
	execStarted := time.Now()
	_, writeErr := db.ExecContext(ctx, trigger)
	execElapsed, execContextErr := time.Since(execStarted), ctx.Err()
	closeStarted := time.Now()
	closeErr := db.Close()
	closeElapsed, closeContextErr := time.Since(closeStarted), ctx.Err()
	// Close has already been attempted regardless of the real ExecContext result.
	if phase := adminKeyRevocationSlotTriggerFailurePhase(writeErr, closeErr); phase != "none" {
		exec := adminKeyRevocationClassifyTrigger(writeErr, execContextErr, execElapsed)
		closed := adminKeyRevocationClassifyTrigger(closeErr, closeContextErr, closeElapsed)
		f.t.Logf("AKR_SLOT_TRIGGER_DIAGNOSTIC phase=exec result=%s error_context=%s context=%s sqlite_code=%s elapsed=%s", exec.result, exec.errorContext, exec.contextState, exec.sqliteCode, exec.elapsed)
		f.t.Logf("AKR_SLOT_TRIGGER_DIAGNOSTIC phase=close result=%s error_context=%s context=%s sqlite_code=%s elapsed=%s", closed.result, closed.errorContext, closed.contextState, closed.sqliteCode, closed.elapsed)
		f.t.Fatalf("AKR_SLOT_TRIGGER_INSTALL phase=%s", phase)
	}
	f.reopenConfigured(self, loopback)
}

func TestAdminKeyRevocationAllSlotStates(t *testing.T) {
	for _, state := range []string{"pending", "armed", "issued", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			f := newAdminKeyRevocationFixture(t)
			cookie, csrf := f.setupSlot(state)
			before := f.controls()
			slotBefore := f.slotFacts()
			stable := retirementSnapshot(t, f.app.store.db, true)
			prior := f.revoked(f.target.ID)
			var notified atomic.Int32
			f.setHooks(adminKeyRevocationHooks{afterNotify: func() { notified.Add(1) }})
			f.assert200(f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`))
			confirmed := f.revoked(f.target.ID)
			parsed, err := parseTime(confirmed.String)
			if !confirmed.Valid || err != nil || parsed.IsZero() || (prior.Valid && confirmed != prior) || notified.Load() != 1 {
				t.Fatal("AKR_SLOT_FINAL_KEY_REVOKED")
			}
			f.assertSlotFinal(state, slotBefore)
			f.assertControls(before)
			if !reflect.DeepEqual(stable, retirementSnapshot(t, f.app.store.db, true)) {
				t.Fatal("AKR_SLOT_QUOTA_OR_LEDGER_CHANGED")
			}
			terminal := f.slotFacts()
			f.assert200(f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`))
			if f.revoked(f.target.ID) != confirmed || f.slotFacts() != terminal || notified.Load() != 2 {
				t.Fatal("AKR_SLOT_IDEMPOTENT_TOMBSTONE")
			}
			f.reopenConfigured(true, true)
			f.assertSlotFinal(state, slotBefore)
			f.assertControls(before)
			if f.revoked(f.target.ID) != confirmed || (state == "issued" && f.models(f.target.Key) != http.StatusUnauthorized) {
				t.Fatal("AKR_SLOT_REOPEN_KEY_FACT")
			}
			f.assertNoReissue(cookie, csrf)
			response := f.admin(http.MethodPost, "/employees/"+f.employeeID+"/self-key-slot", `{"name":"No recycling","operation_id":"akr-slot-new","policy":{"protocol_mode":"selected","protocols":["openai-chat"],"model_mode":"selected","models":["slot-model"],"source_mode":"all","source_cidrs":[],"account_group_mode":"all","account_group_ids":[]}}`)
			if response.StatusCode != http.StatusConflict {
				adminLogoutClose(response)
				t.Fatal("AKR_SLOT_LIFETIME_RECYCLED")
			}
			adminLogoutClose(response)
		})
	}
}

func TestAdminKeyRevocationSlotOwnerMismatch(t *testing.T) {
	f := newAdminKeyRevocationFixture(t)
	f.setupSlot("pending")
	f.exec(`UPDATE employee_self_key_slots SET employee_id=? WHERE key_id=?`, f.otherEmployeeID, f.target.ID)
	before := f.controls()
	slot := f.slotFacts()
	var notified atomic.Int32
	f.setHooks(adminKeyRevocationHooks{afterNotify: func() { notified.Add(1) }})
	f.assert503(f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`))
	if f.revoked(f.target.ID).Valid || slot != f.slotFacts() || notified.Load() != 0 {
		t.Fatal("AKR_SLOT_OWNER_MISMATCH_MUTATED")
	}
	f.assertControls(before)
}

func TestAdminKeyRevocationSlotTriggerReversal(t *testing.T) {
	for _, state := range []string{"pending", "armed", "issued"} {
		t.Run(state, func(t *testing.T) {
			f := newAdminKeyRevocationFixture(t)
			f.setupSlot(state)
			id := strings.ReplaceAll(f.target.ID, "'", "''")
			if state == "issued" {
				f.offlineSlotTrigger(`CREATE TRIGGER akr_restore_issued AFTER UPDATE OF revoked_at ON access_keys WHEN NEW.id='` + id + `' AND NEW.revoked_at IS NOT NULL BEGIN UPDATE access_keys SET revoked_at=NULL WHERE id=NEW.id; END`)
			} else {
				f.offlineSlotTrigger(`CREATE TRIGGER akr_restore_slot AFTER UPDATE OF state ON employee_self_key_slots WHEN NEW.key_id='` + id + `' AND NEW.state='cancelled' BEGIN UPDATE employee_self_key_slots SET state=OLD.state,arm_fingerprint=OLD.arm_fingerprint,arm_employee_revision=OLD.arm_employee_revision,arm_key_policy_revision=OLD.arm_key_policy_revision,arm_governance_revision=OLD.arm_governance_revision,arm_budget_revision=OLD.arm_budget_revision,armed_at=OLD.armed_at,issued_at=OLD.issued_at,cancelled_at=OLD.cancelled_at WHERE key_id=NEW.key_id; END`)
			}
			before := f.controls()
			slot := f.slotFacts()
			var notified atomic.Int32
			f.setHooks(adminKeyRevocationHooks{afterNotify: func() { notified.Add(1) }})
			f.assert503(f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`))
			if f.revoked(f.target.ID).Valid || slot != f.slotFacts() || notified.Load() != 0 {
				t.Fatal("AKR_SLOT_TRIGGER_FALSE_SUCCESS_OR_PARTIAL_WRITE")
			}
			f.assertControls(before)
			if state == "issued" && f.models(f.target.Key) != http.StatusOK {
				t.Fatal("AKR_ISSUED_TRIGGER_FAILURE_FACT")
			}
			f.reopenConfigured(true, true)
			if f.revoked(f.target.ID).Valid || slot != f.slotFacts() {
				t.Fatal("AKR_SLOT_TRIGGER_REOPEN_FACT")
			}
			f.assertControls(before)
			if state == "issued" && f.models(f.target.Key) != http.StatusOK {
				t.Fatal("AKR_ISSUED_TRIGGER_REOPEN_AUTH")
			}
			// No matching dormant credential is manufactured; NULL alone is not an auth claim for pending/armed.
		})
	}
}

func TestAdminKeyRevocationSelfIssueBothOrders(t *testing.T) {
	for _, order := range []string{"revoke-first", "issue-first"} {
		t.Run(order, func(t *testing.T) {
			f := newAdminKeyRevocationFixture(t)
			cookie, csrf := f.setupSlot("armed")
			before := f.controls()
			if order == "issue-first" {
				var issued struct {
					ID  string `json:"id"`
					Key string `json:"key"`
				}
				f.decode(f.issueSlot(cookie, csrf), http.StatusCreated, &issued)
				if issued.ID != f.target.ID || issued.Key == "" {
					t.Fatal("AKR_ORDER_ISSUE_TARGET")
				}
				f.target.Key = issued.Key
				if f.models(issued.Key) != http.StatusOK {
					t.Fatal("AKR_ORDER_ISSUE_AUTH")
				}
			}
			slot := f.slotFacts()
			f.assert200(f.admin(http.MethodPost, "/keys/"+f.target.ID+"/revoke", `{}`))
			state := "armed"
			if order == "issue-first" {
				state = "issued"
			}
			f.assertSlotFinal(state, slot)
			if !f.revoked(f.target.ID).Valid || (order == "issue-first" && f.models(f.target.Key) != http.StatusUnauthorized) {
				t.Fatal("AKR_SELF_ISSUE_ORDER_REVOKE_FACT")
			}
			f.assertNoReissue(cookie, csrf)
			f.assertControls(before)
			f.reopenConfigured(true, true)
			f.assertSlotFinal(state, slot)
			f.assertNoReissue(cookie, csrf)
		})
	}
}

func TestAdminKeyRevocationFinalDispatchBothOrders(t *testing.T) {
	for _, order := range []string{"revoke-first", "dispatch-first"} {
		t.Run(order, func(t *testing.T) {
			base := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
			a := base.base.app
			base.base.enableRuntimeAdminHTTP(t)
			if a.accountPool.Close() != nil {
				t.Fatal("AKR_DISPATCH_ORIGINAL_POOL_CLOSE")
			}
			a.accountPool = base.rt
			base.insertAccount(t, "ups_akr_dispatch", true)
			base.insertModelPool(t, "akr-dispatch-model", "ups_akr_dispatch", 1, modelAccountView{UpstreamID: "ups_akr_dispatch", UpstreamModel: "synthetic-model", Priority: 1, Weight: 1, MaxConcurrency: 1})
			server := httptest.NewServer(a.Handler())
			t.Cleanup(server.Close)
			ctx := context.WithValue(t.Context(), requestIDKey{}, "akr-dispatch-request")
			r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)).WithContext(ctx)
			selected, lease, failure := a.prepareModelRoute(r, base.auth1, "akr-dispatch-model", []string{"openai-compatible"}, accounting.ProtocolOpenAIResponses, true, func(_ *http.Request, s route) (route, *modelPreflightError) { return s, nil })
			if failure != nil || lease == nil {
				t.Fatal("AKR_DISPATCH_PREPARE")
			}
			defer a.releaseModelLease(lease, requestID(ctx), true)
			revoke := func() {
				response := keyRevocationHTTP(t, a, server.URL, base.base.cookie, base.base.csrf, base.auth1.KeyID)
				var body map[string]bool
				defer adminLogoutClose(response)
				if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&body) != nil || len(body) != 1 || !body["ok"] {
					t.Fatal("AKR_DISPATCH_ADMIN_REVOKE")
				}
			}
			if order == "revoke-first" {
				revoke()
				if _, failed := a.dispatchModelRoute(r, base.auth1, "akr-dispatch-model", selected, lease, true); failed == nil || failed.status != http.StatusUnauthorized {
					t.Fatal("AKR_REVOKE_FIRST_DISPATCH_ACCEPTED")
				}
				if retirementCount(t, a.store.db, `SELECT COUNT(*) FROM accounting_attempts WHERE request_id=?`, requestID(ctx)) != 0 {
					t.Fatal("AKR_REVOKE_FIRST_CREATED_ATTEMPT")
				}
			} else {
				if _, failed := a.dispatchModelRoute(r, base.auth1, "akr-dispatch-model", selected, lease, true); failed != nil {
					t.Fatal("AKR_DISPATCH_FIRST_REJECTED")
				}
				revoke()
				if retirementCount(t, a.store.db, `SELECT COUNT(*) FROM accounting_attempt_dispatches d JOIN accounting_attempts a ON a.id=d.attempt_id WHERE a.request_id=?`, requestID(ctx)) != 1 {
					t.Fatal("AKR_MAY_HAVE_SENT_MARKER_REWRITTEN")
				}
				if _, failed := a.dispatchModelRoute(httptest.NewRequest(http.MethodPost, "/v1/responses", nil), base.auth1, "akr-dispatch-model", selected, nil, false); failed == nil || failed.status != http.StatusUnauthorized {
					t.Fatal("AKR_LATER_DISPATCH_ACCEPTED")
				}
			}
			a.finishRequest(requestID(ctx), "cancelled", 0)
		})
	}
}
