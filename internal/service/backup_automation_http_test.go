package service

// Independently authored acceptance tests for docs/adr/0002-automated-backup-key-custody.md.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

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
