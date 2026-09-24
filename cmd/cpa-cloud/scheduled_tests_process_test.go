package main

// Independently authored process acceptance for the scheduled-test contract.
// It uses only a temporary data directory, random loopback ports and synthetic
// credentials; the fake upstream accepts catalog reads and rejects generation.
import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/service"
)

func TestScheduledTestsProcessHelper(t *testing.T) {
	if os.Getenv("CPA_SCHEDULED_TEST_HELPER") != "1" {
		return
	}
	var args []string
	if err := json.Unmarshal([]byte(os.Getenv("CPA_SCHEDULED_TEST_ARGS")), &args); err != nil {
		os.Exit(91)
	}
	code, err := runCLI(args, os.Stdin, io.Discard)
	if err != nil {
		os.Exit(92)
	}
	os.Exit(code)
}

func TestScheduledTestsRealProcessDefaultOffCatalogAndRestart(t *testing.T) {
	dataDir := t.TempDir()
	if err := service.Initialize(context.Background(), dataDir, strings.NewReader("a-valid-admin-password\n")); err != nil {
		t.Fatal(err)
	}
	var catalogCalls atomic.Int32
	var generationCalls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			catalogCalls.Add(1)
			_, _ = io.WriteString(w, `{"data":[{"id":"synthetic-model"}]}`)
			return
		}
		generationCalls.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer upstream.Close()

	address := randomProcessAddress(t)
	process := startScheduledTestProcess(t, dataDir, address, false)
	baseURL := "http://" + address
	waitProcessReady(t, baseURL)
	cookie, csrf := processLogin(t, baseURL)
	upstreamID := processCreateUpstream(t, baseURL, upstream.URL, cookie, csrf)
	planID := processCreatePlan(t, baseURL, upstreamID, cookie, csrf)
	setPlanDue(t, dataDir, planID)
	time.Sleep(250 * time.Millisecond)
	if catalogCalls.Load() != 0 || generationCalls.Load() != 0 {
		t.Fatalf("default-off process made catalog=%d generation=%d calls", catalogCalls.Load(), generationCalls.Load())
	}
	stopScheduledTestProcess(t, process)

	address = randomProcessAddress(t)
	process = startScheduledTestProcess(t, dataDir, address, true)
	baseURL = "http://" + address
	waitProcessReady(t, baseURL)
	cookie, _ = processLogin(t, baseURL)
	deadline := time.Now().Add(10 * time.Second)
	for {
		response := processRequest(t, http.MethodGet, baseURL+"/admin/api/v1/scheduled-tests/"+planID+"/runs?limit=10", "", cookie, "", "")
		var page struct {
			Items []struct {
				State      string  `json:"state"`
				ResultCode *string `json:"result_code"`
			} `json:"items"`
		}
		_ = json.NewDecoder(response.Body).Decode(&page)
		response.Body.Close()
		if len(page.Items) == 1 && page.Items[0].State == "completed" && page.Items[0].ResultCode != nil && *page.Items[0].ResultCode == "catalog_ok" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduled catalog run did not complete: %+v", page)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if catalogCalls.Load() != 1 || generationCalls.Load() != 0 {
		t.Fatalf("enabled process made catalog=%d generation=%d calls", catalogCalls.Load(), generationCalls.Load())
	}
	stopScheduledTestProcess(t, process)

	address = randomProcessAddress(t)
	process = startScheduledTestProcess(t, dataDir, address, true)
	waitProcessReady(t, "http://"+address)
	time.Sleep(250 * time.Millisecond)
	if catalogCalls.Load() != 1 || generationCalls.Load() != 0 {
		t.Fatalf("restart replayed operation: catalog=%d generation=%d", catalogCalls.Load(), generationCalls.Load())
	}
	stopScheduledTestProcess(t, process)
}

type scheduledTestProcess struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

func startScheduledTestProcess(t *testing.T, dataDir, address string, enabled bool) *scheduledTestProcess {
	t.Helper()
	args := []string{"--data-dir", dataDir, "--listen", address, "--allow-loopback-upstream", "--shutdown-on-stdin-eof"}
	if enabled {
		args = append(args, "--scheduled-tests-enabled")
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestScheduledTestsProcessHelper$")
	cmd.Env = append(os.Environ(), "CPA_SCHEDULED_TEST_HELPER=1", "CPA_SCHEDULED_TEST_ARGS="+string(encoded))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return &scheduledTestProcess{cmd: cmd, stdin: stdin}
}

func stopScheduledTestProcess(t *testing.T, process *scheduledTestProcess) {
	t.Helper()
	if err := process.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- process.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		_ = process.cmd.Process.Kill()
		t.Fatal("scheduled test process did not stop")
	}
}

func randomProcessAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	return address
}

func waitProcessReady(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		response, err := (&http.Client{Timeout: 200 * time.Millisecond}).Get(baseURL + "/healthz")
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduled test process did not become ready")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func processLogin(t *testing.T, baseURL string) (*http.Cookie, string) {
	t.Helper()
	response := processRequest(t, http.MethodPost, baseURL+"/admin/api/v1/sessions", `{"username":"admin","password":"a-valid-admin-password"}`, nil, "", baseURL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d", response.StatusCode)
	}
	var body map[string]string
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	cookies := response.Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies=%d", len(cookies))
	}
	return cookies[0], body["csrf_token"]
}

func processCreateUpstream(t *testing.T, baseURL, endpoint string, cookie *http.Cookie, csrf string) string {
	t.Helper()
	body := fmt.Sprintf(`{"name":"Synthetic catalog","provider_kind":"openai-compatible","endpoint":%q,"api_key":"synthetic-process-key"}`, endpoint)
	response := processRequest(t, http.MethodPost, baseURL+"/admin/api/v1/upstreams", body, cookie, csrf, baseURL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create upstream status=%d", response.StatusCode)
	}
	var item struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return item.ID
}

func processCreatePlan(t *testing.T, baseURL, upstreamID string, cookie *http.Cookie, csrf string) string {
	t.Helper()
	body := fmt.Sprintf(`{"name":"Synthetic catalog schedule","upstream_id":%q,"scope":"catalog","interval_seconds":300,"enabled":true}`, upstreamID)
	response := processRequest(t, http.MethodPost, baseURL+"/admin/api/v1/scheduled-tests", body, cookie, csrf, baseURL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create plan status=%d", response.StatusCode)
	}
	var item struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&item); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return item.ID
}

func setPlanDue(t *testing.T, dataDir, planID string) {
	t.Helper()
	db, err := sql.Open("sqlite", dataDir+string(os.PathSeparator)+"cpa-cloud.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE scheduled_test_plans SET next_run_at=? WHERE id=?`, time.Now().UTC().Add(-time.Second).Format("2006-01-02T15:04:05.000000000Z"), planID); err != nil {
		t.Fatal(err)
	}
}

func processRequest(t *testing.T, method, target, body string, cookie *http.Cookie, csrf, origin string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
