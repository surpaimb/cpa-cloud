package service

// Independently authored administrative API for docs/channel-monitor-contract.md.
import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"
)

func (a *App) registerChannelMonitorHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/channel-monitors", a.requireAdmin(a.listChannelMonitors, false))
	mux.HandleFunc("POST /admin/api/v1/channel-monitors", a.requireAdmin(a.createChannelMonitor, true))
	mux.HandleFunc("GET /admin/api/v1/channel-monitors/{id}", a.requireAdmin(a.getChannelMonitor, false))
	mux.HandleFunc("PATCH /admin/api/v1/channel-monitors/{id}", a.requireAdmin(a.updateChannelMonitor, true))
	mux.HandleFunc("DELETE /admin/api/v1/channel-monitors/{id}", a.requireAdmin(a.deleteChannelMonitor, true))
	mux.HandleFunc("GET /admin/api/v1/channel-monitors/{id}/runs", a.requireAdmin(a.listChannelMonitorRuns, false))
	mux.HandleFunc("GET /admin/api/v1/channel-monitors/{id}/summary", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		a.requireAdmin(a.getChannelMonitorSummary, false)(w, r)
	})
}

// Independently authored for docs/channel-monitor-summary-contract.md.
func (a *App) getChannelMonitorSummary(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" || r.ContentLength > 0 || len(r.TransferEncoding) != 0 || !validIdentifier(r.PathValue("id"), 128) {
		writeChannelMonitorError(w, http.StatusBadRequest, "invalid_request")
		return
	}
	if !validOptionalOrigin(r, a.cfg.TLSCert != "") {
		writeAdminError(w, http.StatusForbidden, "origin_rejected", "Request origin is not allowed.")
		return
	}
	result, err := loadChannelMonitorSummary(r.Context(), a.store.db, r.PathValue("id"), time.Now().UTC())
	if errors.Is(err, sql.ErrNoRows) {
		writeChannelMonitorError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		writeChannelMonitorError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeChannelMonitorError(w http.ResponseWriter, status int, code string) {
	messages := map[string]string{
		"invalid_request":     "Invalid channel monitor request.",
		"not_found":           "Channel monitor was not found.",
		"revision_conflict":   "The channel monitor changed; reload before updating.",
		"binding_changed":     "The selected route or account changed; confirm a new binding.",
		"plan_limit":          "The channel monitor plan limit has been reached.",
		"storage_unavailable": "Channel monitors are temporarily unavailable.",
	}
	message := messages[code]
	if message == "" {
		message = "Unable to manage channel monitors."
	}
	writeAdminError(w, status, code, message)
}

func noChannelMonitorQuery(r *http.Request) bool { return len(r.URL.Query()) == 0 }

func (a *App) listChannelMonitors(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !noChannelMonitorQuery(r) {
		writeChannelMonitorError(w, 400, "invalid_request")
		return
	}
	rows, err := a.store.db.QueryContext(r.Context(), channelMonitorPlanSelect+` WHERE archived_at IS NULL ORDER BY created_at,id LIMIT ?`, channelMonitorMaxPlans+1)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	items := make([]channelMonitorPlan, 0)
	for rows.Next() {
		item, err := scanChannelMonitorPlan(rows)
		if err != nil {
			rows.Close()
			writeChannelMonitorError(w, 503, "storage_unavailable")
			return
		}
		items = append(items, item)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil || len(items) > channelMonitorMaxPlans {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	for index := range items {
		if err := decorateChannelMonitorPlan(r.Context(), a.store.db, &items[index]); err != nil {
			writeChannelMonitorError(w, 503, "storage_unavailable")
			return
		}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (a *App) getChannelMonitor(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !noChannelMonitorQuery(r) {
		writeChannelMonitorError(w, 400, "invalid_request")
		return
	}
	item, err := loadChannelMonitorPlan(r.Context(), a.store.db, r.PathValue("id"), false)
	if errors.Is(err, sql.ErrNoRows) {
		writeChannelMonitorError(w, 404, "not_found")
		return
	}
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	if err := decorateChannelMonitorPlan(r.Context(), a.store.db, &item); err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	writeJSON(w, 200, item)
}

func channelMonitorInput(object map[string]any, plan *channelMonitorPlan) bool {
	name, nameOK := object["name"].(string)
	channelID, channelOK := object["channel_id"].(string)
	modelID, modelOK := object["model_id"].(string)
	upstreamID, upstreamOK := object["upstream_id"].(string)
	scope, scopeOK := object["scope"].(string)
	interval, intervalOK := strictJSONInt64Range(object["interval_seconds"], channelMonitorMinInterval, channelMonitorMaxInterval)
	enabled, enabledOK := object["enabled"].(bool)
	if !nameOK || !validText(name, 1, 120) || !channelOK || !validIdentifier(channelID, 128) || !modelOK || !validIdentifier(modelID, 128) || !upstreamOK || !validIdentifier(upstreamID, 128) || !scopeOK || !validScheduledTestScope(scope) || !intervalOK || !enabledOK {
		return false
	}
	plan.Name, plan.ChannelID, plan.ModelID, plan.UpstreamID, plan.Scope, plan.IntervalSeconds, plan.Enabled = name, channelID, modelID, upstreamID, scope, interval, enabled
	return true
}

func (a *App) createChannelMonitor(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "name", "channel_id", "model_id", "upstream_id", "scope", "interval_seconds", "enabled") {
		writeChannelMonitorError(w, 400, "invalid_request")
		return
	}
	var plan channelMonitorPlan
	if !channelMonitorInput(object, &plan) {
		writeChannelMonitorError(w, 400, "invalid_request")
		return
	}
	plan.ID, err = newID("mon")
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM channel_monitor_plans WHERE archived_at IS NULL`).Scan(&count); err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	if count >= channelMonitorMaxPlans {
		writeChannelMonitorError(w, 409, "plan_limit")
		return
	}
	var valid bool
	plan.Binding, valid, err = captureChannelMonitorBinding(r.Context(), tx, plan.ChannelID, plan.ModelID, plan.UpstreamID)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	if !valid {
		writeChannelMonitorError(w, 409, "binding_changed")
		return
	}
	plan.Revision = 1
	now := a.channelMonitors.now().UTC()
	stamp := formatAccountPoolTime(now)
	var next any
	if plan.Enabled {
		next = channelMonitorNextRun(now, plan.IntervalSeconds)
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO channel_monitor_plans(id,name,channel_id,model_id,upstream_id,scope,interval_seconds,enabled,revision,next_run_at,channel_revision,model_revision,pool_revision,upstream_revision,route_upstream_model,route_wire_protocol,route_position,created_by_admin_id,updated_by_admin_id,created_at,updated_at,archived_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL)`,
		plan.ID, plan.Name, plan.ChannelID, plan.ModelID, plan.UpstreamID, plan.Scope, plan.IntervalSeconds, boolInt(plan.Enabled), 1, next,
		plan.Binding.ChannelRevision, plan.Binding.ModelRevision, plan.Binding.PoolRevision, plan.Binding.UpstreamRevision, plan.Binding.RouteUpstreamModel, plan.Binding.RouteWireProtocol, plan.Binding.RoutePosition,
		session.AdminID, session.AdminID, stamp, stamp)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	if err := tx.Commit(); err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	item, err := loadChannelMonitorPlan(r.Context(), a.store.db, plan.ID, false)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	if err := decorateChannelMonitorPlan(r.Context(), a.store.db, &item); err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	a.channelMonitors.notify()
	writeJSON(w, 201, item)
}

func channelMonitorPatchKeys(object map[string]any) bool {
	if len(object) < 2 || len(object) > 9 {
		return false
	}
	allowed := map[string]bool{"expected_revision": true, "name": true, "channel_id": true, "model_id": true, "upstream_id": true, "scope": true, "interval_seconds": true, "enabled": true, "rebind": true}
	for key := range object {
		if !allowed[key] {
			return false
		}
	}
	_, ok := object["expected_revision"]
	return ok
}

func (a *App) updateChannelMonitor(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !channelMonitorPatchKeys(object) {
		writeChannelMonitorError(w, 400, "invalid_request")
		return
	}
	expected, ok := strictPositiveJSONInt64(object["expected_revision"])
	if !ok {
		writeChannelMonitorError(w, 400, "invalid_request")
		return
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	current, err := loadChannelMonitorPlan(r.Context(), tx, r.PathValue("id"), false)
	if errors.Is(err, sql.ErrNoRows) {
		writeChannelMonitorError(w, 404, "not_found")
		return
	}
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	if current.Revision != expected {
		writeChannelMonitorError(w, 409, "revision_conflict")
		return
	}
	updated := current
	if value, ok := object["name"]; ok {
		text, valid := value.(string)
		if !valid || !validText(text, 1, 120) {
			writeChannelMonitorError(w, 400, "invalid_request")
			return
		}
		updated.Name = text
	}
	for _, field := range []struct {
		key         string
		destination *string
	}{{"channel_id", &updated.ChannelID}, {"model_id", &updated.ModelID}, {"upstream_id", &updated.UpstreamID}} {
		if value, exists := object[field.key]; exists {
			text, valid := value.(string)
			if !valid || !validIdentifier(text, 128) {
				writeChannelMonitorError(w, 400, "invalid_request")
				return
			}
			*field.destination = text
		}
	}
	if value, ok := object["scope"]; ok {
		text, valid := value.(string)
		if !valid || !validScheduledTestScope(text) {
			writeChannelMonitorError(w, 400, "invalid_request")
			return
		}
		updated.Scope = text
	}
	if value, ok := object["interval_seconds"]; ok {
		number, valid := strictJSONInt64Range(value, channelMonitorMinInterval, channelMonitorMaxInterval)
		if !valid {
			writeChannelMonitorError(w, 400, "invalid_request")
			return
		}
		updated.IntervalSeconds = number
	}
	if value, ok := object["enabled"]; ok {
		enabled, valid := value.(bool)
		if !valid {
			writeChannelMonitorError(w, 400, "invalid_request")
			return
		}
		updated.Enabled = enabled
	}
	rebind := false
	if value, ok := object["rebind"]; ok {
		truth, valid := value.(bool)
		if !valid || !truth {
			writeChannelMonitorError(w, 400, "invalid_request")
			return
		}
		rebind = true
	}
	identityChanged := updated.ChannelID != current.ChannelID || updated.ModelID != current.ModelID || updated.UpstreamID != current.UpstreamID
	if identityChanged && !rebind {
		writeChannelMonitorError(w, 400, "invalid_request")
		return
	}
	if rebind {
		binding, valid, err := captureChannelMonitorBinding(r.Context(), tx, updated.ChannelID, updated.ModelID, updated.UpstreamID)
		if err != nil {
			writeChannelMonitorError(w, 503, "storage_unavailable")
			return
		}
		if !valid {
			writeChannelMonitorError(w, 409, "binding_changed")
			return
		}
		updated.Binding = binding
	} else if updated.Enabled {
		valid, err := channelMonitorBindingValid(r.Context(), tx, updated)
		if err != nil {
			writeChannelMonitorError(w, 503, "storage_unavailable")
			return
		}
		if !valid {
			writeChannelMonitorError(w, 409, "binding_changed")
			return
		}
	}
	now := a.channelMonitors.now().UTC()
	stamp := formatAccountPoolTime(now)
	var next any
	if updated.Enabled {
		next = channelMonitorNextRun(now, updated.IntervalSeconds)
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE channel_monitor_plans SET name=?,channel_id=?,model_id=?,upstream_id=?,scope=?,interval_seconds=?,enabled=?,revision=revision+1,next_run_at=?,channel_revision=?,model_revision=?,pool_revision=?,upstream_revision=?,route_upstream_model=?,route_wire_protocol=?,route_position=?,updated_by_admin_id=?,updated_at=? WHERE id=? AND revision=? AND archived_at IS NULL`,
		updated.Name, updated.ChannelID, updated.ModelID, updated.UpstreamID, updated.Scope, updated.IntervalSeconds, boolInt(updated.Enabled), next,
		updated.Binding.ChannelRevision, updated.Binding.ModelRevision, updated.Binding.PoolRevision, updated.Binding.UpstreamRevision, updated.Binding.RouteUpstreamModel, updated.Binding.RouteWireProtocol, updated.Binding.RoutePosition,
		session.AdminID, stamp, updated.ID, expected)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		writeChannelMonitorError(w, 409, "revision_conflict")
		return
	}
	if err := tx.Commit(); err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	item, err := loadChannelMonitorPlan(r.Context(), a.store.db, updated.ID, false)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	if err := decorateChannelMonitorPlan(r.Context(), a.store.db, &item); err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	a.channelMonitors.cancelPlan(updated.ID, expected+1)
	writeJSON(w, 200, item)
}

func (a *App) deleteChannelMonitor(w http.ResponseWriter, r *http.Request, session adminSession) {
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "expected_revision") {
		writeChannelMonitorError(w, 400, "invalid_request")
		return
	}
	expected, ok := strictPositiveJSONInt64(object["expected_revision"])
	if !ok {
		writeChannelMonitorError(w, 400, "invalid_request")
		return
	}
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	defer tx.Rollback()
	current, err := loadChannelMonitorPlan(r.Context(), tx, r.PathValue("id"), true)
	if errors.Is(err, sql.ErrNoRows) {
		writeChannelMonitorError(w, 404, "not_found")
		return
	}
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	if current.ArchivedAt != nil {
		writeJSON(w, 200, map[string]any{"result": "already_archived", "id": current.ID, "revision": current.Revision})
		return
	}
	if current.Revision != expected {
		writeChannelMonitorError(w, 409, "revision_conflict")
		return
	}
	stamp := formatAccountPoolTime(a.channelMonitors.now().UTC())
	result, err := tx.ExecContext(r.Context(), `UPDATE channel_monitor_plans SET enabled=0,revision=revision+1,next_run_at=NULL,updated_by_admin_id=?,updated_at=?,archived_at=? WHERE id=? AND revision=? AND archived_at IS NULL`, session.AdminID, stamp, stamp, current.ID, expected)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		writeChannelMonitorError(w, 409, "revision_conflict")
		return
	}
	if err := tx.Commit(); err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	a.channelMonitors.cancelPlan(current.ID, expected+1)
	writeJSON(w, 200, map[string]any{"result": "archived", "id": current.ID, "revision": expected + 1})
}

func (a *App) listChannelMonitorRuns(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if _, err := loadChannelMonitorPlan(r.Context(), a.store.db, r.PathValue("id"), true); errors.Is(err, sql.ErrNoRows) {
		writeChannelMonitorError(w, 404, "not_found")
		return
	} else if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	queryValues := r.URL.Query()
	for key, values := range queryValues {
		if (key != "limit" && key != "cursor") || len(values) != 1 {
			writeChannelMonitorError(w, 400, "invalid_request")
			return
		}
	}
	limit := int64(50)
	if values, exists := queryValues["limit"]; exists {
		value := values[0]
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 1 || parsed > 100 {
			writeChannelMonitorError(w, 400, "invalid_request")
			return
		}
		limit = parsed
	}
	var cursor int64
	if values, exists := queryValues["cursor"]; exists {
		value := values[0]
		var valid bool
		cursor, valid = decodeScheduledRunCursor(value)
		if !valid {
			writeChannelMonitorError(w, 400, "invalid_request")
			return
		}
	}
	sqlText := channelMonitorRunSelect + ` WHERE plan_id=?`
	args := []any{r.PathValue("id")}
	if cursor > 0 {
		sqlText += ` AND sequence<?`
		args = append(args, cursor)
	}
	sqlText += ` ORDER BY sequence DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := a.store.db.QueryContext(r.Context(), sqlText, args...)
	if err != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	items := make([]channelMonitorRun, 0, limit)
	for rows.Next() {
		item, err := scanChannelMonitorRun(rows)
		if err != nil {
			rows.Close()
			writeChannelMonitorError(w, 503, "storage_unavailable")
			return
		}
		items = append(items, item)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		writeChannelMonitorError(w, 503, "storage_unavailable")
		return
	}
	var next *string
	if int64(len(items)) > limit {
		items = items[:limit]
		value := encodeScheduledRunCursor(items[len(items)-1].Sequence)
		next = &value
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next})
}
