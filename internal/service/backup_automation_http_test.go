package service

// Independently authored acceptance tests for docs/adr/0002-automated-backup-key-custody.md.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/keyprovider"
)

func TestBackupKeyProviderIDUsesProtectedStoreAlphabet(t *testing.T) {
	for range 100 {
		id, err := newBackupKeyProviderID()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "bkp_") || len(id) > 64 {
			t.Fatalf("unexpected backup key provider id %q", id)
		}
		for _, character := range id {
			if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || character == '-') {
				t.Fatalf("backup key provider id %q contains an unsupported character", id)
			}
		}
	}
}

func TestBackupAutomationAdminAPIKeyRotationPlanAndRun(t *testing.T) {
	fixture := newBackupAutomationFixture(t, true)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", fixture.app.login)
	registerBackupAutomationHandlers(fixture.app, fixture.worker, mux)
	server := httptest.NewServer(requestMiddleware(mux))
	defer server.Close()

	response, err := http.Get(server.URL + "/admin/api/v1/backups/plans")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", response.StatusCode)
	}
	response.Body.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)

	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/backups/key-providers", `{"kind":"windows-dpapi-user"}`, cookie, "", server.URL)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("missing CSRF status=%d body=%s", response.StatusCode, readBody(response))
	}
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/backups/key-providers", `{"kind":"windows-dpapi-user","path":"C:\\forbidden"}`, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("path injection status=%d body=%s", response.StatusCode, readBody(response))
	}
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/backups/key-providers", `{"kind":"windows-dpapi-user"}`, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create provider status=%d body=%s", response.StatusCode, readBody(response))
	}
	var provider backupKeyProviderView
	decodeResponse(t, response, &provider)
	if provider.ActiveVersion != 1 || provider.Revision != 1 || provider.Status != "ready" {
		t.Fatalf("provider=%+v", provider)
	}

	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/backups/key-providers/"+provider.ID+"/rotate", `{"expected_revision":1}`, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("rotate status=%d body=%s", response.StatusCode, readBody(response))
	}
	decodeResponse(t, response, &provider)
	if provider.ActiveVersion != 2 || provider.Revision != 2 {
		t.Fatalf("rotated provider=%+v", provider)
	}
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/backups/key-providers/"+provider.ID+"/activate-version", `{"expected_revision":2,"version":1}`, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("activate old version status=%d body=%s", response.StatusCode, readBody(response))
	}
	decodeResponse(t, response, &provider)
	if provider.ActiveVersion != 1 || provider.Revision != 3 {
		t.Fatalf("rolled back provider=%+v", provider)
	}

	planBody := fmt.Sprintf(`{"name":"Nightly","key_provider_id":%q,"interval_seconds":900,"retention_count":2,"rehearsal_enabled":false,"enabled":false}`, provider.ID)
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/backups/plans", planBody, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create plan status=%d body=%s", response.StatusCode, readBody(response))
	}
	var plan backupPlanView
	decodeResponse(t, response, &plan)
	if plan.Enabled || plan.Revision != 1 || plan.KeyProviderID != provider.ID {
		t.Fatalf("plan=%+v", plan)
	}
	client := server.Client()
	client.Timeout = 2 * time.Second
	for _, path := range []string{"/admin/api/v1/backups/key-providers", "/admin/api/v1/backups/plans"} {
		request, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(cookie)
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("list %s after create: %v", path, err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("list %s after create status=%d body=%s", path, response.StatusCode, readBody(response))
		}
		response.Body.Close()
	}
	if err := fixture.worker.Start(); err != nil {
		t.Fatal(err)
	}
	runBody := fmt.Sprintf(`{"plan_id":%q,"expected_revision":1}`, plan.ID)
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/backups/runs", runBody, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("run now status=%d body=%s", response.StatusCode, readBody(response))
	}
	var run backupRunView
	decodeResponse(t, response, &run)
	run = waitBackupRunComplete(t, fixture.app.store.db, run.ID)
	if run.Status != "succeeded" || !run.PackageRetained || run.RehearsalStatus != "skipped" || run.TriggerKind != "manual" || run.RequestedByAdminID == nil || *run.RequestedByAdminID != fixture.adminID {
		t.Fatalf("run=%+v", run)
	}

	response = requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/backups/runs?limit=1", "", cookie, "", server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list runs status=%d body=%s", response.StatusCode, readBody(response))
	}
	var page struct {
		Items []backupRunView `json:"items"`
	}
	decodeResponse(t, response, &page)
	if len(page.Items) != 1 || page.Items[0].ID != run.ID {
		t.Fatalf("runs=%+v", page.Items)
	}
	response = requestJSON(t, http.MethodDelete, server.URL+"/admin/api/v1/backups/plans/"+plan.ID, `{"expected_revision":1}`, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("disable plan status=%d body=%s", response.StatusCode, readBody(response))
	}
}

func TestBackupAutomationNilCoordinatorRegistersFailClosedRoutes(t *testing.T) {
	fixture := newBackupAutomationFixture(t, false)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", fixture.app.login)
	registerBackupAutomationHandlers(fixture.app, nil, mux)
	server := httptest.NewServer(requestMiddleware(mux))
	defer server.Close()
	cookie, _ := loginTestAdmin(t, server.URL)
	response := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/backups/key-providers", "", cookie, "", server.URL)
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("nil coordinator status=%d body=%s", response.StatusCode, readBody(response))
	}
	var envelope map[string]any
	decodeResponse(t, response, &envelope)
	errorObject, _ := envelope["error"].(map[string]any)
	if errorObject["code"] != "backup_automation_unavailable" {
		encoded, _ := json.Marshal(envelope)
		t.Fatalf("unexpected error envelope: %s", encoded)
	}
}

func TestLinuxSystemdBackupProviderBindsConfiguredReferenceAndRejectsRotation(t *testing.T) {
	fixture := newBackupAutomationFixture(t, true)
	reference := keyprovider.Reference{ProviderID: "bkp_linux", Kind: keyprovider.KindLinuxSystemdCredentialV1, InstanceID: "01234567-89ab-cdef-0123-456789abcdef", KeyID: "backup-key", Version: 4}
	fixture.keys.configured = &reference
	for index := range fixture.keys.configuredKey {
		fixture.keys.configuredKey[index] = byte(index + 1)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", fixture.app.login)
	registerBackupAutomationHandlers(fixture.app, fixture.worker, mux)
	server := httptest.NewServer(requestMiddleware(mux))
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)

	// Reject a mismatched platform kind before inserting a provisioning row.
	for attempt := 0; attempt < 2; attempt++ {
		rejected := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/backups/key-providers", `{"kind":"windows-dpapi-user"}`, cookie, csrf, server.URL)
		if rejected.StatusCode != http.StatusConflict {
			t.Fatalf("wrong kind status=%d", rejected.StatusCode)
		}
		rejected.Body.Close()
	}
	var count int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM backup_key_providers`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("wrong kind changed providers: count=%d err=%v", count, err)
	}
	response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/backups/key-providers", `{"kind":"linux-systemd-credential-v1"}`, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.StatusCode, readBody(response))
	}
	var provider backupKeyProviderView
	decodeResponse(t, response, &provider)
	if provider.ID != reference.ProviderID || provider.Kind != reference.Kind || provider.Scope != keyprovider.ScopeSystemdService || provider.InstanceID == nil || *provider.InstanceID != reference.InstanceID || provider.KeyID == nil || *provider.KeyID != reference.KeyID || provider.ActiveVersion != int64(reference.Version) {
		t.Fatalf("provider=%+v", provider)
	}
	response = requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/backups/key-providers/"+provider.ID+"/rotate", `{"expected_revision":1}`, cookie, csrf, server.URL)
	if response.StatusCode != http.StatusConflict || !strings.Contains(readBody(response), "key_provider_rotation_external") {
		t.Fatalf("rotate status=%d", response.StatusCode)
	}
}
