package service

// Independently implemented from docs/adr/0002-automated-backup-key-custody.md.

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cpacloud.local/server/internal/keyprovider"
)

type backupAutomationAPI struct {
	app         *App
	coordinator *backupAutomationCoordinator
}

func registerBackupAutomationHandlers(app *App, coordinator *backupAutomationCoordinator, mux *http.ServeMux) {
	api := &backupAutomationAPI{app: app, coordinator: coordinator}
	mux.HandleFunc("GET /admin/api/v1/backups/key-providers", app.requireAdmin(api.listKeyProviders, false))
	mux.HandleFunc("POST /admin/api/v1/backups/key-providers", app.requireAdmin(api.createKeyProvider, true))
	mux.HandleFunc("POST /admin/api/v1/backups/key-providers/{id}/rotate", app.requireAdmin(api.rotateKeyProvider, true))
	mux.HandleFunc("POST /admin/api/v1/backups/key-providers/{id}/activate-version", app.requireAdmin(api.activateKeyProviderVersion, true))
	mux.HandleFunc("GET /admin/api/v1/backups/plans", app.requireAdmin(api.listPlans, false))
	mux.HandleFunc("POST /admin/api/v1/backups/plans", app.requireAdmin(api.createPlan, true))
	mux.HandleFunc("GET /admin/api/v1/backups/plans/{id}", app.requireAdmin(api.getPlan, false))
	mux.HandleFunc("PATCH /admin/api/v1/backups/plans/{id}", app.requireAdmin(api.updatePlan, true))
	mux.HandleFunc("DELETE /admin/api/v1/backups/plans/{id}", app.requireAdmin(api.deletePlan, true))
	mux.HandleFunc("GET /admin/api/v1/backups/runs", app.requireAdmin(api.listRuns, false))
	mux.HandleFunc("POST /admin/api/v1/backups/runs", app.requireAdmin(api.createRun, true))
	mux.HandleFunc("GET /admin/api/v1/backups/runs/{id}", app.requireAdmin(api.getRun, false))
	mux.HandleFunc("POST /admin/api/v1/backups/runs/{id}/cancel", app.requireAdmin(api.cancelRun, true))
}

func (api *backupAutomationAPI) available(w http.ResponseWriter) bool {
	if api.coordinator == nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "backup_automation_unavailable")
		return false
	}
	return true
}

func (api *backupAutomationAPI) listKeyProviders(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !api.available(w) {
		return
	}
	rows, err := api.app.store.db.QueryContext(r.Context(), `SELECT id,kind,scope,status,reason_code,active_version,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at FROM backup_key_providers ORDER BY created_at,id`)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	items := make([]backupKeyProviderView, 0)
	for rows.Next() {
		var item backupKeyProviderView
		if err := rows.Scan(&item.ID, &item.Kind, &item.Scope, &item.Status, &item.ReasonCode, &item.ActiveVersion, &item.Revision, &item.CreatedByAdminID, &item.UpdatedByAdminID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if err := rows.Close(); err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	for index := range items {
		api.decorateProvider(r.Context(), &items[index])
	}
	ready, reason := api.coordinator.keys.Ready()
	var reasonValue *string
	if !ready {
		reasonValue = &reason
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "ready": api.coordinator.Ready(), "store_ready": ready, "reason_code": reasonValue})
}

func (api *backupAutomationAPI) decorateProvider(ctx context.Context, item *backupKeyProviderView) {
	if item.Status != "ready" {
		return
	}
	material, err := api.coordinator.keys.Resolve(ctx, item.ID, uint64(item.ActiveVersion))
	if err == nil {
		material.Destroy()
		return
	}
	reason := "protected_store_invalid"
	item.Status, item.ReasonCode = "degraded", &reason
}

func (api *backupAutomationAPI) createKeyProvider(w http.ResponseWriter, r *http.Request, session adminSession) {
	if !api.available(w) {
		return
	}
	api.coordinator.providerMu.Lock()
	defer api.coordinator.providerMu.Unlock()
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "kind") {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	kind, ok := object["kind"].(string)
	if !ok || kind != keyprovider.KindWindowsDPAPIUser {
		writeBackupAutomationError(w, http.StatusBadRequest, "unsupported_key_provider")
		return
	}
	if ready, _ := api.coordinator.keys.Ready(); !ready {
		writeBackupAutomationError(w, http.StatusConflict, "key_provider_unavailable")
		return
	}
	id, err := newBackupKeyProviderID()
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	stamp := formatAccountPoolTime(api.coordinator.now().UTC())
	if _, err := api.app.store.db.ExecContext(r.Context(), `INSERT INTO backup_key_providers(id,kind,scope,status,reason_code,active_version,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES(?,?,'current-user','degraded','provisioning_interrupted',1,1,?,?,?,?)`, id, kind, session.AdminID, session.AdminID, stamp, stamp); err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	prepared, err := api.coordinator.keys.PrepareVersion(r.Context(), id, 1)
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = api.coordinator.recoverKeyProviderOrphansLocked(cleanupCtx)
		writeBackupAutomationError(w, http.StatusConflict, "key_provider_unavailable")
		return
	}
	committed := false
	defer func() {
		if !committed {
			if prepared.Rollback() == nil {
				cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_, _ = api.app.store.db.ExecContext(cleanupCtx, `DELETE FROM backup_key_providers WHERE id=? AND status='degraded' AND reason_code='provisioning_interrupted'`, id)
			}
		}
	}()
	result, err := api.app.store.db.ExecContext(r.Context(), `UPDATE backup_key_providers SET status='ready',reason_code=NULL,updated_at=? WHERE id=? AND status='degraded' AND reason_code='provisioning_interrupted' AND revision=1`, stamp, id)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		writeBackupAutomationError(w, http.StatusConflict, "revision_conflict")
		return
	}
	prepared.Commit()
	committed = true
	item, err := loadBackupKeyProvider(r.Context(), api.app.store.db, id)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	api.coordinator.notify()
	writeJSON(w, http.StatusCreated, item)
}

func newBackupKeyProviderID() (string, error) {
	id, err := newID("bkp")
	if err != nil {
		return "", err
	}
	// Host-protected stores use this identifier in authenticated file names and
	// intentionally accept only the portable lower-case identifier alphabet.
	return strings.ToLower(id), nil
}

func (api *backupAutomationAPI) rotateKeyProvider(w http.ResponseWriter, r *http.Request, session adminSession) {
	if !api.available(w) {
		return
	}
	api.coordinator.providerMu.Lock()
	defer api.coordinator.providerMu.Unlock()
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "expected_revision") {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	expected, ok := strictJSONInt64Range(object["expected_revision"], 1, backupMaxRevision)
	if !ok {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	current, err := loadBackupKeyProvider(r.Context(), api.app.store.db, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeBackupAutomationError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if current.Revision != expected {
		writeBackupAutomationError(w, http.StatusConflict, "revision_conflict")
		return
	}
	if current.ActiveVersion >= backupMaxRevision || current.Revision >= backupMaxRevision {
		writeBackupAutomationError(w, http.StatusConflict, "revision_limit")
		return
	}
	stamp := formatAccountPoolTime(api.coordinator.now().UTC())
	result, err := api.app.store.db.ExecContext(r.Context(), `UPDATE backup_key_providers SET status='degraded',reason_code='rotation_interrupted',updated_by_admin_id=?,updated_at=? WHERE id=? AND revision=? AND status='ready'`, session.AdminID, stamp, current.ID, expected)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		writeBackupAutomationError(w, http.StatusConflict, "revision_conflict")
		return
	}
	restoreProvider := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = api.app.store.db.ExecContext(cleanupCtx, `UPDATE backup_key_providers SET status='ready',reason_code=NULL WHERE id=? AND revision=? AND status='degraded' AND reason_code='rotation_interrupted'`, current.ID, expected)
	}
	next := current.ActiveVersion + 1
	prepared, err := api.coordinator.keys.PrepareVersion(r.Context(), current.ID, uint64(next))
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = api.coordinator.recoverKeyProviderOrphansLocked(cleanupCtx)
		writeBackupAutomationError(w, http.StatusConflict, "key_provider_unavailable")
		return
	}
	committed := false
	defer func() {
		if !committed {
			if prepared.Rollback() == nil {
				restoreProvider()
			}
		}
	}()
	result, err = api.app.store.db.ExecContext(r.Context(), `UPDATE backup_key_providers SET active_version=?,revision=revision+1,status='ready',reason_code=NULL,updated_by_admin_id=?,updated_at=? WHERE id=? AND revision=? AND status='degraded' AND reason_code='rotation_interrupted'`, next, session.AdminID, stamp, current.ID, expected)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err = result.RowsAffected()
	if err != nil || changed != 1 {
		writeBackupAutomationError(w, http.StatusConflict, "revision_conflict")
		return
	}
	prepared.Commit()
	committed = true
	item, err := loadBackupKeyProvider(r.Context(), api.app.store.db, current.ID)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *backupAutomationAPI) activateKeyProviderVersion(w http.ResponseWriter, r *http.Request, session adminSession) {
	if !api.available(w) {
		return
	}
	api.coordinator.providerMu.Lock()
	defer api.coordinator.providerMu.Unlock()
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "expected_revision", "version") {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	expected, expectedOK := strictJSONInt64Range(object["expected_revision"], 1, backupMaxRevision)
	version, versionOK := strictJSONInt64Range(object["version"], 1, backupMaxRevision)
	if !expectedOK || !versionOK {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	current, err := loadBackupKeyProvider(r.Context(), api.app.store.db, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeBackupAutomationError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if current.Revision != expected || current.Revision >= backupMaxRevision {
		writeBackupAutomationError(w, http.StatusConflict, "revision_conflict")
		return
	}
	material, err := api.coordinator.keys.Resolve(r.Context(), current.ID, uint64(version))
	if err != nil {
		writeBackupAutomationError(w, http.StatusConflict, "key_provider_version_unavailable")
		return
	}
	material.Destroy()
	stamp := formatAccountPoolTime(api.coordinator.now().UTC())
	result, err := api.app.store.db.ExecContext(r.Context(), `UPDATE backup_key_providers SET active_version=?,revision=revision+1,status='ready',reason_code=NULL,updated_by_admin_id=?,updated_at=? WHERE id=? AND revision=?`, version, session.AdminID, stamp, current.ID, expected)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		writeBackupAutomationError(w, http.StatusConflict, "revision_conflict")
		return
	}
	item, err := loadBackupKeyProvider(r.Context(), api.app.store.db, current.ID)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *backupAutomationAPI) listPlans(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !api.available(w) {
		return
	}
	rows, err := api.app.store.db.QueryContext(r.Context(), `SELECT id,name,key_provider_id,interval_seconds,retention_count,rehearsal_enabled,enabled,next_run_at,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at FROM backup_plans ORDER BY created_at,id`)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	items := make([]backupPlanView, 0)
	for rows.Next() {
		var item backupPlanView
		var rehearsal, enabled int
		if err := rows.Scan(&item.ID, &item.Name, &item.KeyProviderID, &item.IntervalSeconds, &item.RetentionCount, &rehearsal, &enabled, &item.NextRunAt, &item.Revision, &item.CreatedByAdminID, &item.UpdatedByAdminID, &item.CreatedAt, &item.UpdatedAt); err != nil {
			rows.Close()
			writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		item.RehearsalEnabled, item.Enabled = rehearsal == 1, enabled == 1
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if err := rows.Close(); err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	for index := range items {
		latest, err := latestBackupRun(r.Context(), api.app.store.db, items[index].ID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		if err == nil {
			items[index].LatestRun = &latest
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (api *backupAutomationAPI) getPlan(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !api.available(w) {
		return
	}
	item, err := loadBackupPlan(r.Context(), api.app.store.db, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeBackupAutomationError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if latest, err := latestBackupRun(r.Context(), api.app.store.db, item.ID); err == nil {
		item.LatestRun = &latest
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *backupAutomationAPI) createPlan(w http.ResponseWriter, r *http.Request, session adminSession) {
	if !api.available(w) {
		return
	}
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "name", "key_provider_id", "interval_seconds", "retention_count", "rehearsal_enabled", "enabled") {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	name, nameOK := object["name"].(string)
	providerID, providerOK := object["key_provider_id"].(string)
	interval, intervalOK := strictJSONInt64Range(object["interval_seconds"], backupMinInterval, backupMaxInterval)
	retention, retentionOK := strictJSONInt64Range(object["retention_count"], 1, backupMaxRetention)
	rehearsal, rehearsalOK := object["rehearsal_enabled"].(bool)
	enabled, enabledOK := object["enabled"].(bool)
	if !nameOK || !validText(name, 1, 120) || !providerOK || !validIdentifier(providerID, 128) || !intervalOK || !retentionOK || !rehearsalOK || !enabledOK {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if _, err := loadBackupKeyProvider(r.Context(), api.app.store.db, providerID); errors.Is(err, sql.ErrNoRows) {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_key_provider")
		return
	} else if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	var count int64
	if err := api.app.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM backup_plans`).Scan(&count); err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if count >= backupMaxPlans {
		writeBackupAutomationError(w, http.StatusConflict, "plan_limit")
		return
	}
	id, err := newID("bpl")
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	now := api.coordinator.now().UTC()
	stamp := formatAccountPoolTime(now)
	var next any
	if enabled {
		next = formatAccountPoolTime(now.Add(time.Duration(interval) * time.Second))
	}
	if _, err := api.app.store.db.ExecContext(r.Context(), `INSERT INTO backup_plans(id,name,key_provider_id,interval_seconds,retention_count,rehearsal_enabled,enabled,next_run_at,revision,created_by_admin_id,updated_by_admin_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,1,?,?,?,?)`, id, name, providerID, interval, retention, boolInt(rehearsal), boolInt(enabled), next, session.AdminID, session.AdminID, stamp, stamp); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			writeBackupAutomationError(w, http.StatusConflict, "name_conflict")
		} else {
			writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		}
		return
	}
	item, err := loadBackupPlan(r.Context(), api.app.store.db, id)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	api.coordinator.notify()
	writeJSON(w, http.StatusCreated, item)
}

func (api *backupAutomationAPI) updatePlan(w http.ResponseWriter, r *http.Request, session adminSession) {
	if !api.available(w) {
		return
	}
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !backupPlanPatchKeys(object) {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	expected, ok := strictJSONInt64Range(object["expected_revision"], 1, backupMaxRevision)
	if !ok {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	current, err := loadBackupPlan(r.Context(), api.app.store.db, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeBackupAutomationError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if current.Revision != expected || current.Revision >= backupMaxRevision {
		writeBackupAutomationError(w, http.StatusConflict, "revision_conflict")
		return
	}
	updated := current
	if value, exists := object["name"]; exists {
		text, ok := value.(string)
		if !ok || !validText(text, 1, 120) {
			writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		updated.Name = text
	}
	if value, exists := object["key_provider_id"]; exists {
		text, ok := value.(string)
		if !ok || !validIdentifier(text, 128) {
			writeBackupAutomationError(w, http.StatusBadRequest, "invalid_key_provider")
			return
		}
		if _, err := loadBackupKeyProvider(r.Context(), api.app.store.db, text); err != nil {
			writeBackupAutomationError(w, http.StatusBadRequest, "invalid_key_provider")
			return
		}
		updated.KeyProviderID = text
	}
	if value, exists := object["interval_seconds"]; exists {
		interval, ok := strictJSONInt64Range(value, backupMinInterval, backupMaxInterval)
		if !ok {
			writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		updated.IntervalSeconds = interval
	}
	if value, exists := object["retention_count"]; exists {
		retention, ok := strictJSONInt64Range(value, 1, backupMaxRetention)
		if !ok {
			writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		updated.RetentionCount = retention
	}
	if value, exists := object["rehearsal_enabled"]; exists {
		rehearsal, ok := value.(bool)
		if !ok {
			writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		updated.RehearsalEnabled = rehearsal
	}
	if value, exists := object["enabled"]; exists {
		enabled, ok := value.(bool)
		if !ok {
			writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		updated.Enabled = enabled
	}
	now := api.coordinator.now().UTC()
	stamp := formatAccountPoolTime(now)
	var next any
	if updated.Enabled {
		next = formatAccountPoolTime(now.Add(time.Duration(updated.IntervalSeconds) * time.Second))
	}
	result, err := api.app.store.db.ExecContext(r.Context(), `UPDATE backup_plans SET name=?,key_provider_id=?,interval_seconds=?,retention_count=?,rehearsal_enabled=?,enabled=?,next_run_at=?,revision=revision+1,updated_by_admin_id=?,updated_at=? WHERE id=? AND revision=?`, updated.Name, updated.KeyProviderID, updated.IntervalSeconds, updated.RetentionCount, boolInt(updated.RehearsalEnabled), boolInt(updated.Enabled), next, session.AdminID, stamp, updated.ID, expected)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		writeBackupAutomationError(w, http.StatusConflict, "revision_conflict")
		return
	}
	item, err := loadBackupPlan(r.Context(), api.app.store.db, updated.ID)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	api.coordinator.notify()
	writeJSON(w, http.StatusOK, item)
}

func (api *backupAutomationAPI) deletePlan(w http.ResponseWriter, r *http.Request, session adminSession) {
	if !api.available(w) {
		return
	}
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "expected_revision") {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	expected, ok := strictJSONInt64Range(object["expected_revision"], 1, backupMaxRevision)
	if !ok || expected >= backupMaxRevision {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	result, err := api.app.store.db.ExecContext(r.Context(), `UPDATE backup_plans SET enabled=0,next_run_at=NULL,revision=revision+1,updated_by_admin_id=?,updated_at=? WHERE id=? AND revision=?`, session.AdminID, formatAccountPoolTime(api.coordinator.now().UTC()), r.PathValue("id"), expected)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if changed != 1 {
		if _, err := loadBackupPlan(r.Context(), api.app.store.db, r.PathValue("id")); errors.Is(err, sql.ErrNoRows) {
			writeBackupAutomationError(w, http.StatusNotFound, "not_found")
		} else {
			writeBackupAutomationError(w, http.StatusConflict, "revision_conflict")
		}
		return
	}
	api.coordinator.notify()
	writeJSON(w, http.StatusOK, map[string]any{"result": "disabled", "id": r.PathValue("id"), "revision": expected + 1})
}

func (api *backupAutomationAPI) createRun(w http.ResponseWriter, r *http.Request, session adminSession) {
	if !api.available(w) {
		return
	}
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "plan_id", "expected_revision") {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	planID, planOK := object["plan_id"].(string)
	expected, revisionOK := strictJSONInt64Range(object["expected_revision"], 1, backupMaxRevision)
	if !planOK || !validIdentifier(planID, 128) || !revisionOK {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !api.coordinator.cfg.Enabled {
		writeBackupAutomationError(w, http.StatusConflict, "backup_worker_disabled")
		return
	}
	api.coordinator.mu.Lock()
	started := api.coordinator.started && !api.coordinator.closed
	api.coordinator.mu.Unlock()
	if !started {
		writeBackupAutomationError(w, http.StatusConflict, "backup_worker_disabled")
		return
	}
	if !api.coordinator.Ready() {
		writeBackupAutomationError(w, http.StatusConflict, "key_provider_unavailable")
		return
	}
	claim, err := api.coordinator.claimPlan(r.Context(), planID, expected, session.AdminID)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if claim == nil {
		if _, err := loadBackupPlan(r.Context(), api.app.store.db, planID); errors.Is(err, sql.ErrNoRows) {
			writeBackupAutomationError(w, http.StatusNotFound, "not_found")
		} else {
			writeBackupAutomationError(w, http.StatusConflict, "backup_not_runnable")
		}
		return
	}
	api.coordinator.run(*claim)
	item, err := loadBackupRun(r.Context(), api.app.store.db, claim.RunID)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusAccepted, item)
}

func (api *backupAutomationAPI) listRuns(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !api.available(w) {
		return
	}
	limit := int64(50)
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 1 || parsed > 100 {
			writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		limit = parsed
	}
	planID := r.URL.Query().Get("plan_id")
	if planID != "" && !validIdentifier(planID, 128) {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	cursorTime, cursorID, cursorOK := decodeBackupRunCursor(r.URL.Query().Get("cursor"))
	if !cursorOK {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	query := `SELECT id,plan_id,plan_revision,key_provider_id,key_provider_kind,key_provider_version,trigger_kind,requested_by_admin_id,scheduled_for,started_at,finished_at,status,package_name,package_size,package_retained,package_deleted_at,verified_at,rehearsal_status,rehearsed_at,error_code,created_at FROM backup_runs WHERE 1=1`
	args := []any{}
	if planID != "" {
		query += ` AND plan_id=?`
		args = append(args, planID)
	}
	if cursorID != "" {
		query += ` AND (started_at<? OR (started_at=? AND id<?))`
		args = append(args, cursorTime, cursorTime, cursorID)
	}
	query += ` ORDER BY started_at DESC,id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := api.app.store.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer rows.Close()
	items := make([]backupRunView, 0, limit+1)
	for rows.Next() {
		item, err := scanBackupRun(rows)
		if err != nil {
			writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	var next *string
	if int64(len(items)) > limit {
		items = items[:limit]
		value := encodeBackupRunCursor(items[len(items)-1].StartedAt, items[len(items)-1].ID)
		next = &value
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

func (api *backupAutomationAPI) getRun(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !api.available(w) {
		return
	}
	item, err := loadBackupRun(r.Context(), api.app.store.db, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeBackupAutomationError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (api *backupAutomationAPI) cancelRun(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !api.available(w) {
		return
	}
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || len(object) != 0 {
		writeBackupAutomationError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	item, err := loadBackupRun(r.Context(), api.app.store.db, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeBackupAutomationError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeBackupAutomationError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if item.Status != "running" || !api.coordinator.cancelRun(item.ID) {
		writeBackupAutomationError(w, http.StatusConflict, "backup_not_running")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"result": "cancellation_requested", "id": item.ID})
}

type backupRunScanner interface{ Scan(...any) error }

func scanBackupRun(scanner backupRunScanner) (backupRunView, error) {
	var item backupRunView
	var retained int
	err := scanner.Scan(&item.ID, &item.PlanID, &item.PlanRevision, &item.KeyProviderID, &item.KeyProviderKind, &item.KeyProviderVersion, &item.TriggerKind, &item.RequestedByAdminID, &item.ScheduledFor, &item.StartedAt, &item.FinishedAt, &item.Status, &item.PackageName, &item.PackageSize, &retained, &item.PackageDeletedAt, &item.VerifiedAt, &item.RehearsalStatus, &item.RehearsedAt, &item.ErrorCode, &item.CreatedAt)
	item.PackageRetained = retained == 1
	return item, err
}

func latestBackupRun(ctx context.Context, db *sql.DB, planID string) (backupRunView, error) {
	return scanBackupRun(db.QueryRowContext(ctx, `SELECT id,plan_id,plan_revision,key_provider_id,key_provider_kind,key_provider_version,trigger_kind,requested_by_admin_id,scheduled_for,started_at,finished_at,status,package_name,package_size,package_retained,package_deleted_at,verified_at,rehearsal_status,rehearsed_at,error_code,created_at FROM backup_runs WHERE plan_id=? ORDER BY started_at DESC,id DESC LIMIT 1`, planID))
}

func backupPlanPatchKeys(object map[string]any) bool {
	if _, ok := object["expected_revision"]; !ok || len(object) < 2 {
		return false
	}
	allowed := map[string]bool{"expected_revision": true, "name": true, "key_provider_id": true, "interval_seconds": true, "retention_count": true, "rehearsal_enabled": true, "enabled": true}
	for key := range object {
		if !allowed[key] {
			return false
		}
	}
	return true
}

func encodeBackupRunCursor(startedAt, id string) string {
	value, _ := json.Marshal([]string{startedAt, id})
	return base64.RawURLEncoding.EncodeToString(value)
}

func decodeBackupRunCursor(value string) (string, string, bool) {
	if value == "" {
		return "", "", true
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) > 1024 {
		return "", "", false
	}
	var parts []string
	if err := json.Unmarshal(decoded, &parts); err != nil || len(parts) != 2 || !validIdentifier(parts[1], 128) {
		return "", "", false
	}
	if _, err := parseTime(parts[0]); err != nil {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func writeBackupAutomationError(w http.ResponseWriter, status int, code string) {
	message := "Backup operation could not be completed."
	switch code {
	case "invalid_request":
		message = "Invalid request."
	case "not_found":
		message = "Backup resource was not found."
	case "revision_conflict":
		message = "Backup resource changed; reload and retry."
	case "key_provider_unavailable", "key_provider_version_unavailable":
		message = "The protected backup key provider is unavailable."
	case "backup_worker_disabled":
		message = "Automatic backup execution is disabled by server configuration."
	case "backup_not_running":
		message = "The backup run is not active in this process."
	}
	writeAdminError(w, status, code, message)
}
