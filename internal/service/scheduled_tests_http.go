package service

// Independently implemented from docs/parity-next-batch-2026-09-24.md.
import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"
)

func (a *App) registerScheduledTestHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/scheduled-tests", a.requireAdmin(a.listScheduledTests, false))
	mux.HandleFunc("POST /admin/api/v1/scheduled-tests", a.requireAdmin(a.createScheduledTest, true))
	mux.HandleFunc("GET /admin/api/v1/scheduled-tests/{id}", a.requireAdmin(a.getScheduledTest, false))
	mux.HandleFunc("PATCH /admin/api/v1/scheduled-tests/{id}", a.requireAdmin(a.updateScheduledTest, true))
	mux.HandleFunc("DELETE /admin/api/v1/scheduled-tests/{id}", a.requireAdmin(a.deleteScheduledTest, true))
	mux.HandleFunc("GET /admin/api/v1/scheduled-tests/{id}/runs", a.requireAdmin(a.listScheduledTestRuns, false))
}

func (a *App) listScheduledTests(w http.ResponseWriter, r *http.Request, _ adminSession) {
	rows, err := a.store.db.QueryContext(r.Context(), scheduledTestPlanSelect+` WHERE archived_at IS NULL ORDER BY created_at,id`)
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer rows.Close()
	items := make([]scheduledTestPlan, 0)
	for rows.Next() {
		item, err := scanScheduledTestPlan(rows)
		if err != nil {
			writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if err := decorateScheduledTestLatest(r.Context(), a.store.db, items); err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) getScheduledTest(w http.ResponseWriter, r *http.Request, _ adminSession) {
	item, err := loadScheduledTestPlan(r.Context(), a.store.db, r.PathValue("id"), false)
	if errors.Is(err, sql.ErrNoRows) {
		writeScheduledTestError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	items := []scheduledTestPlan{item}
	if err := decorateScheduledTestLatest(r.Context(), a.store.db, items); err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, items[0])
}

func (a *App) createScheduledTest(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "name", "upstream_id", "scope", "interval_seconds", "enabled") {
		writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	name, nameOK := object["name"].(string)
	upstreamID, upstreamOK := object["upstream_id"].(string)
	scope, scopeOK := object["scope"].(string)
	interval, intervalOK := strictJSONInt64Range(object["interval_seconds"], scheduledTestMinInterval, scheduledTestMaxInterval)
	enabled, enabledOK := object["enabled"].(bool)
	if !nameOK || !validText(name, 1, 120) || !upstreamOK || !validIdentifier(upstreamID, 128) || !scopeOK || !validScheduledTestScope(scope) || !intervalOK || !enabledOK {
		writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	id, err := newID("sch")
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	now := a.scheduledTests.now().UTC()
	stamp := formatAccountPoolTime(now)
	var next any
	if enabled {
		next = formatAccountPoolTime(now.Add(time.Duration(interval) * time.Second))
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	upstreamExists, err := scheduledTestUpstreamExists(r.Context(), tx, upstreamID)
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if !upstreamExists {
		writeScheduledTestError(w, http.StatusBadRequest, "invalid_upstream")
		return
	}
	var count int64
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM scheduled_test_plans WHERE archived_at IS NULL`).Scan(&count); err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if count >= scheduledTestMaxPlans {
		writeScheduledTestError(w, http.StatusConflict, "plan_limit")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO scheduled_test_plans(id,name,upstream_id,scope,interval_seconds,enabled,revision,next_run_at,created_by_admin_id,updated_by_admin_id,created_at,updated_at,archived_at) VALUES(?,?,?,?,?,?,1,?,?,?,?,?,NULL)`, id, name, upstreamID, scope, interval, boolInt(enabled), next, session.AdminID, session.AdminID, stamp, stamp); err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if err := tx.Commit(); err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	item, err := loadScheduledTestPlan(r.Context(), a.store.db, id, false)
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.scheduledTests.notify()
	writeJSON(w, http.StatusCreated, item)
}

func (a *App) updateScheduledTest(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !scheduledTestPatchKeys(object) {
		writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	expected, ok := strictPositiveJSONInt64(object["expected_revision"])
	if !ok {
		writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	current, err := loadScheduledTestPlan(r.Context(), tx, r.PathValue("id"), false)
	if errors.Is(err, sql.ErrNoRows) {
		writeScheduledTestError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if current.Revision != expected {
		writeScheduledTestError(w, http.StatusConflict, "revision_conflict")
		return
	}
	updated := current
	if value, exists := object["name"]; exists {
		text, ok := value.(string)
		if !ok || !validText(text, 1, 120) {
			writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		updated.Name = text
	}
	if value, exists := object["upstream_id"]; exists {
		text, ok := value.(string)
		if !ok || !validIdentifier(text, 128) {
			writeScheduledTestError(w, http.StatusBadRequest, "invalid_upstream")
			return
		}
		exists, err := scheduledTestUpstreamExists(r.Context(), tx, text)
		if err != nil {
			writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		if !exists {
			writeScheduledTestError(w, http.StatusBadRequest, "invalid_upstream")
			return
		}
		updated.UpstreamID = text
	}
	if value, exists := object["scope"]; exists {
		text, ok := value.(string)
		if !ok || !validScheduledTestScope(text) {
			writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		updated.Scope = text
	}
	if value, exists := object["interval_seconds"]; exists {
		interval, ok := strictJSONInt64Range(value, scheduledTestMinInterval, scheduledTestMaxInterval)
		if !ok {
			writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		updated.IntervalSeconds = interval
	}
	if value, exists := object["enabled"]; exists {
		enabled, ok := value.(bool)
		if !ok {
			writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		updated.Enabled = enabled
	}
	now := a.scheduledTests.now().UTC()
	stamp := formatAccountPoolTime(now)
	var next any
	if updated.Enabled {
		next = formatAccountPoolTime(now.Add(time.Duration(updated.IntervalSeconds) * time.Second))
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE scheduled_test_plans SET name=?,upstream_id=?,scope=?,interval_seconds=?,enabled=?,revision=revision+1,next_run_at=?,updated_by_admin_id=?,updated_at=? WHERE id=? AND revision=? AND archived_at IS NULL`, updated.Name, updated.UpstreamID, updated.Scope, updated.IntervalSeconds, boolInt(updated.Enabled), next, session.AdminID, stamp, updated.ID, expected)
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		writeScheduledTestError(w, http.StatusConflict, "revision_conflict")
		return
	}
	if err := tx.Commit(); err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	item, err := loadScheduledTestPlan(r.Context(), a.store.db, updated.ID, false)
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.scheduledTests.cancelPlan(updated.ID, item.Revision)
	writeJSON(w, http.StatusOK, item)
}

func (a *App) deleteScheduledTest(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "expected_revision") {
		writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	expected, ok := strictPositiveJSONInt64(object["expected_revision"])
	if !ok {
		writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	current, err := loadScheduledTestPlan(r.Context(), tx, r.PathValue("id"), true)
	if errors.Is(err, sql.ErrNoRows) {
		writeScheduledTestError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	if current.ArchivedAt != nil {
		writeJSON(w, http.StatusOK, map[string]any{"result": "already_archived", "id": current.ID, "revision": current.Revision})
		return
	}
	if current.Revision != expected {
		writeScheduledTestError(w, http.StatusConflict, "revision_conflict")
		return
	}
	now := a.scheduledTests.now().UTC()
	stamp := formatAccountPoolTime(now)
	result, err := tx.ExecContext(r.Context(), `UPDATE scheduled_test_plans SET enabled=0,revision=revision+1,next_run_at=NULL,updated_by_admin_id=?,updated_at=?,archived_at=? WHERE id=? AND revision=? AND archived_at IS NULL`, session.AdminID, stamp, stamp, current.ID, expected)
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		writeScheduledTestError(w, http.StatusConflict, "revision_conflict")
		return
	}
	if err := tx.Commit(); err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	a.scheduledTests.cancelPlan(current.ID, expected+1)
	writeJSON(w, http.StatusOK, map[string]any{"result": "archived", "id": current.ID, "revision": expected + 1})
}

func (a *App) listScheduledTestRuns(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if _, err := loadScheduledTestPlan(r.Context(), a.store.db, r.PathValue("id"), true); errors.Is(err, sql.ErrNoRows) {
		writeScheduledTestError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	limit := int64(50)
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 1 || parsed > 100 {
			writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
			return
		}
		limit = parsed
	}
	var cursor int64
	if value := r.URL.Query().Get("cursor"); value != "" {
		var ok bool
		cursor, ok = decodeScheduledRunCursor(value)
		if !ok {
			writeScheduledTestError(w, http.StatusBadRequest, "invalid_request")
			return
		}
	}
	query := `SELECT sequence,plan_revision,operation_id,scope,state,result_code,started_at,finished_at,latency_ms FROM scheduled_test_runs WHERE plan_id=?`
	args := []any{r.PathValue("id")}
	if cursor > 0 {
		query += ` AND sequence<?`
		args = append(args, cursor)
	}
	query += ` ORDER BY sequence DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := a.store.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	defer rows.Close()
	items := make([]scheduledTestRun, 0, limit)
	var sequences []int64
	for rows.Next() {
		sequence, item, err := loadScheduledTestRun(rows)
		if err != nil {
			writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
			return
		}
		sequences = append(sequences, sequence)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeScheduledTestError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	var next *string
	if int64(len(items)) > limit {
		items = items[:limit]
		value := encodeScheduledRunCursor(sequences[limit-1])
		next = &value
	}
	writeJSON(w, http.StatusOK, scheduledTestRunsPage{Items: items, NextCursor: next})
}

func scheduledTestPatchKeys(object map[string]any) bool {
	if len(object) < 2 || len(object) > 6 {
		return false
	}
	allowed := map[string]bool{"expected_revision": true, "name": true, "upstream_id": true, "scope": true, "interval_seconds": true, "enabled": true}
	for key := range object {
		if !allowed[key] {
			return false
		}
	}
	_, expected := object["expected_revision"]
	return expected
}

func strictJSONInt64Range(value any, minimum, maximum int64) (int64, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	parsed, err := number.Int64()
	return parsed, err == nil && parsed >= minimum && parsed <= maximum
}

func validScheduledTestScope(value string) bool {
	return value == "local_credential" || value == "catalog"
}

func scheduledTestUpstreamExists(ctx context.Context, query queryRower, id string) (bool, error) {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT 1 FROM upstreams WHERE id=? AND archived=0`, id).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && exists == 1, err
}

func writeScheduledTestError(w http.ResponseWriter, status int, code string) {
	messages := map[string]string{
		"invalid_request":     "Invalid scheduled test request.",
		"invalid_upstream":    "The selected upstream does not exist.",
		"not_found":           "Scheduled test was not found.",
		"revision_conflict":   "The scheduled test changed; reload before updating.",
		"plan_limit":          "The active scheduled test limit has been reached.",
		"storage_unavailable": "Scheduled tests are temporarily unavailable.",
	}
	message := messages[code]
	if message == "" {
		message = "Unable to manage scheduled tests."
	}
	writeAdminError(w, status, code, message)
}
