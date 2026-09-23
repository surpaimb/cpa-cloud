package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestGovernanceManagementHTTPAuthParsingPaginationAndReceipts(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	if err := createRootKey(f.dir); err != nil {
		t.Fatal(err)
	}
	secrets, err := loadSecrets(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	token, csrf := "governance-admin-token", "governance-csrf-token"
	if _, err := f.base.db.Exec(`INSERT INTO sessions(id,admin_id,token_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)`,
		"governance-session", "admin-one", secrets.digest("admin-session/v1", token), csrf,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), governanceManagementTestTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{}, store: f.base, secrets: secrets}
	mux := http.NewServeMux()
	f.manager.Register(app, mux)
	server := httptest.NewServer(requestMiddleware(mux))
	defer server.Close()
	cookie := &http.Cookie{Name: adminCookieName, Value: token}

	response := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/governance/settings", "", nil, "", "")
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", response.StatusCode, readBody(response))
	}
	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/governance/settings", "", cookie, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", response.StatusCode, readBody(response))
	}
	var settings map[string]any
	decodeResponse(t, response, &settings)
	if settings["enabled"] != false || settings["revision"] != float64(1) {
		t.Fatalf("settings=%v", settings)
	}

	duplicateJSON := `{"operation_id":"` + governanceOperationID(40) + `","operation_id":"` + governanceOperationID(40) + `","name":"Team","employee_ids":[]}`
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/governance/groups", duplicateJSON, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate JSON status=%d body=%s", response.StatusCode, readBody(response))
	}
	oversized := `{"operation_id":"` + governanceOperationID(40) + `","name":"` + strings.Repeat("x", governanceManagementMaxBody) + `","employee_ids":[]}`
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/governance/groups", oversized, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("oversized status=%d body=%s", response.StatusCode, readBody(response))
	}

	createGroup := func(operation, name string) governanceOperationReceipt {
		body := `{"operation_id":` + quoteJSON(operation) + `,"name":` + quoteJSON(name) + `,"employee_ids":["employee-one"]}`
		response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/governance/groups", body, cookie, csrf, server.URL)
		if response.StatusCode != http.StatusOK {
			t.Fatalf("create group status=%d body=%s", response.StatusCode, readBody(response))
		}
		var receipt governanceOperationReceipt
		decodeResponse(t, response, &receipt)
		return receipt
	}
	firstGroup := createGroup(governanceOperationID(41), "HTTP Team A")
	_ = createGroup(governanceOperationID(42), "HTTP Team B")

	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/governance/groups?limit=1&limit=2", "", cookie, "", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate query status=%d body=%s", response.StatusCode, readBody(response))
	}
	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/governance/groups?unknown=1", "", cookie, "", "")
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown query status=%d body=%s", response.StatusCode, readBody(response))
	}
	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/governance/groups?limit=1", "", cookie, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("group page status=%d body=%s", response.StatusCode, readBody(response))
	}
	var groupPage struct {
		Items      []governanceGroupView `json:"items"`
		NextCursor *string               `json:"next_cursor"`
	}
	decodeResponse(t, response, &groupPage)
	if len(groupPage.Items) != 1 || groupPage.NextCursor == nil {
		t.Fatalf("group page=%+v", groupPage)
	}

	badSafeInteger := `{"operation_id":"` + governanceOperationID(43) + `","scope_kind":"employee","scope_id":"employee-one","enabled":true,"hard":{"rpm":9007199254740992,"concurrency":null},"shadow":{"tpm":null,"cost_micro":null,"currency":null,"window":null}}`
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/governance/policies", badSafeInteger, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("unsafe integer status=%d body=%s", response.StatusCode, readBody(response))
	}
	badCostNumber := `{"operation_id":"` + governanceOperationID(43) + `","scope_kind":"employee","scope_id":"employee-one","enabled":true,"hard":{"rpm":null,"concurrency":null},"shadow":{"tpm":null,"cost_micro":42,"currency":"USD","window":"rolling_24h"}}`
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/governance/policies", badCostNumber, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("numeric cost status=%d body=%s", response.StatusCode, readBody(response))
	}

	policyBody := `{"operation_id":"` + governanceOperationID(44) + `","scope_kind":"group","scope_id":` + quoteJSON(firstGroup.ResourceID) + `,"enabled":true,"hard":{"rpm":null,"concurrency":null},"shadow":{"tpm":null,"cost_micro":"9223372036854775807","currency":"USD","window":"rolling_24h"}}`
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/governance/policies", policyBody, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("create policy status=%d body=%s", response.StatusCode, readBody(response))
	}
	var policyReceipt governanceOperationReceipt
	decodeResponse(t, response, &policyReceipt)
	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/governance/policies/"+policyReceipt.ResourceID, "", cookie, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("get policy status=%d body=%s", response.StatusCode, readBody(response))
	}
	var policy map[string]any
	decodeResponse(t, response, &policy)
	shadow, ok := policy["shadow"].(map[string]any)
	if !ok || shadow["cost_micro"] != "9223372036854775807" {
		t.Fatalf("policy shadow=%v", policy["shadow"])
	}

	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/governance/operations/"+governanceOperationID(44), "", cookie, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("operation status=%d body=%s", response.StatusCode, readBody(response))
	}
	var operation map[string]any
	decodeResponse(t, response, &operation)
	encoded, _ := json.Marshal(operation)
	if strings.Contains(string(encoded), "employee-one") || strings.Contains(string(encoded), "9223372036854775807") {
		t.Fatalf("operation leaked payload: %s", encoded)
	}
}

func TestGovernanceManagementHTTPAdmissionLockBoundaries(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	if err := createRootKey(f.dir); err != nil {
		t.Fatal(err)
	}
	secrets, err := loadSecrets(f.dir)
	if err != nil {
		t.Fatal(err)
	}
	token, csrf := "governance-lock-token", "governance-lock-csrf"
	if _, err := f.base.db.Exec(`INSERT INTO sessions(id,admin_id,token_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)`,
		"governance-lock-session", "admin-one", secrets.digest("admin-session/v1", token), csrf,
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano), governanceManagementTestTime.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	app := &App{cfg: Config{}, store: f.base, secrets: secrets}
	mux := http.NewServeMux()
	f.manager.Register(app, mux)
	groupBody := func(operation, name string) string {
		return `{"operation_id":` + quoteJSON(operation) + `,"name":` + quoteJSON(name) + `,"employee_ids":[]}`
	}
	request := func(body io.Reader) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://governance.test/admin/api/v1/governance/groups", body)
		r.AddCookie(&http.Cookie{Name: adminCookieName, Value: token})
		r.Header.Set("Origin", "http://governance.test")
		r.Header.Set("X-CSRF-Token", csrf)
		return r
	}

	slowBody := &governanceGatedReader{
		started: make(chan struct{}), release: make(chan struct{}),
		data: []byte(groupBody(governanceOperationID(70), "Slow body")),
	}
	slowDone := make(chan struct{})
	go func() {
		mux.ServeHTTP(httptest.NewRecorder(), request(slowBody))
		close(slowDone)
	}()
	governanceWait(t, slowBody.started, "slow body read")
	assertGovernanceAdmissionReadable(t, app, "slow body")
	close(slowBody.release)
	governanceWait(t, slowDone, "slow body completion")

	app.admission.RLock()
	storeRecorder := httptest.NewRecorder()
	storeDone := make(chan struct{})
	go func() {
		mux.ServeHTTP(storeRecorder, request(strings.NewReader(groupBody(governanceOperationID(71), "Store lock"))))
		close(storeDone)
	}()
	select {
	case <-storeDone:
		app.admission.RUnlock()
		t.Fatal("store write completed while admission read lock was held")
	case <-time.After(100 * time.Millisecond):
	}
	var stored int
	if err := f.base.db.QueryRow(`SELECT COUNT(*) FROM governance_management_operations WHERE operation_id=?`, governanceOperationID(71)).Scan(&stored); err != nil || stored != 0 {
		app.admission.RUnlock()
		t.Fatalf("operation entered store before write lock count=%d err=%v", stored, err)
	}
	app.admission.RUnlock()
	governanceWait(t, storeDone, "store write completion")
	if storeRecorder.Code != http.StatusOK {
		t.Fatalf("store response status=%d body=%s", storeRecorder.Code, storeRecorder.Body.String())
	}

	blockedWriter := newGovernanceBlockingWriter()
	responseDone := make(chan struct{})
	go func() {
		mux.ServeHTTP(blockedWriter, request(strings.NewReader(groupBody(governanceOperationID(72), "Blocked response"))))
		close(responseDone)
	}()
	governanceWait(t, blockedWriter.writeStarted, "response write")
	assertGovernanceAdmissionReadable(t, app, "blocked response")
	if err := f.base.db.QueryRow(`SELECT COUNT(*) FROM governance_management_operations WHERE operation_id=?`, governanceOperationID(72)).Scan(&stored); err != nil || stored != 1 {
		close(blockedWriter.release)
		t.Fatalf("operation not committed before response count=%d err=%v", stored, err)
	}
	close(blockedWriter.release)
	governanceWait(t, responseDone, "response completion")
}

type governanceGatedReader struct {
	started chan struct{}
	release chan struct{}
	data    []byte
	once    sync.Once
	done    bool
}

func (r *governanceGatedReader) Read(target []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.once.Do(func() { close(r.started) })
	<-r.release
	r.done = true
	return copy(target, r.data), nil
}

type governanceBlockingWriter struct {
	header       http.Header
	status       int
	writeStarted chan struct{}
	release      chan struct{}
	once         sync.Once
}

func newGovernanceBlockingWriter() *governanceBlockingWriter {
	return &governanceBlockingWriter{header: make(http.Header), writeStarted: make(chan struct{}), release: make(chan struct{})}
}

func (w *governanceBlockingWriter) Header() http.Header    { return w.header }
func (w *governanceBlockingWriter) WriteHeader(status int) { w.status = status }
func (w *governanceBlockingWriter) Write(content []byte) (int, error) {
	w.once.Do(func() { close(w.writeStarted) })
	<-w.release
	return len(content), nil
}

func assertGovernanceAdmissionReadable(t *testing.T, app *App, stage string) {
	t.Helper()
	acquired := make(chan struct{})
	go func() {
		app.admission.RLock()
		app.admission.RUnlock()
		close(acquired)
	}()
	governanceWait(t, acquired, stage+" admission read lock")
}

func governanceWait(t *testing.T, ready <-chan struct{}, stage string) {
	t.Helper()
	select {
	case <-ready:
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for %s", stage)
	}
}
