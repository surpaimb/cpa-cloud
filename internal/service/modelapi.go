package service

import (
	"bytes"
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type employeeAuth struct {
	EmployeeID string
	KeyID      string
	Mode       string
}
type route struct {
	AccountID       string
	Endpoint        string
	UpstreamModel   string
	Ciphertext      []byte
	ProviderKind    string
	Revision        int64
	CredentialState sql.NullString
	KeyVersion      int
}

func (a *App) listModels(w http.ResponseWriter, r *http.Request) {
	auth, ok := a.authenticateEmployee(w, r)
	if !ok {
		return
	}
	query := `SELECT m.id,m.created_at FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.enabled=1 AND u.enabled=1`
	args := []any{}
	if !a.cfg.ExperimentalCodexMembership {
		query += ` AND u.provider_kind<>'codex-membership'`
	} else {
		query += ` AND (u.provider_kind<>'codex-membership' OR u.credential_state<>'reauth_required')`
	}
	if auth.Mode == "selected" {
		query += ` AND EXISTS(SELECT 1 FROM employee_models em WHERE em.employee_id=? AND em.model_id=m.id)`
		args = append(args, auth.EmployeeID)
	}
	query += ` ORDER BY m.id`
	rows, err := a.store.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeModelError(w, 503, "service_unavailable", "Service is temporarily unavailable.", requestID(r.Context()))
		return
	}
	defer rows.Close()
	data := make([]map[string]any, 0)
	for rows.Next() {
		var id, created string
		if err := rows.Scan(&id, &created); err != nil {
			writeModelError(w, 503, "service_unavailable", "Service is temporarily unavailable.", requestID(r.Context()))
			return
		}
		t, _ := parseTime(created)
		data = append(data, map[string]any{"id": id, "object": "model", "created": t.Unix(), "owned_by": "cpa-cloud"})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

func (a *App) authenticateEmployee(w http.ResponseWriter, r *http.Request) (employeeAuth, bool) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || r.Header.Get("X-API-Key") != "" {
		writeModelError(w, 401, "invalid_api_key", "Invalid API key.", requestID(r.Context()))
		return employeeAuth{}, false
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		writeModelError(w, 401, "invalid_api_key", "Invalid API key.", requestID(r.Context()))
		return employeeAuth{}, false
	}
	key := parts[1]
	if !strings.HasPrefix(key, "cpac_") {
		writeModelError(w, 401, "invalid_api_key", "Invalid API key.", requestID(r.Context()))
		return employeeAuth{}, false
	}
	pair := strings.Split(strings.TrimPrefix(key, "cpac_"), ".")
	if len(pair) != 2 || pair[0] == "" || pair[1] == "" {
		writeModelError(w, 401, "invalid_api_key", "Invalid API key.", requestID(r.Context()))
		return employeeAuth{}, false
	}
	var auth employeeAuth
	var digest []byte
	var status string
	var expires, revoked sql.NullString
	err := a.store.db.QueryRowContext(r.Context(), `SELECT k.employee_id,k.id,k.digest,k.expires_at,k.revoked_at,e.status,e.model_mode FROM access_keys k JOIN employees e ON e.id=k.employee_id WHERE k.selector=?`, pair[0]).Scan(&auth.EmployeeID, &auth.KeyID, &digest, &expires, &revoked, &status, &auth.Mode)
	valid := err == nil && subtle.ConstantTimeCompare(a.secrets.digest("employee-key/v1\x00"+pair[0], pair[1]), digest) == 1 && status == "active" && !revoked.Valid
	if valid && expires.Valid {
		t, e := parseTime(expires.String)
		valid = e == nil && time.Now().UTC().Before(t)
	}
	if !valid {
		writeModelError(w, 401, "invalid_api_key", "Invalid API key.", requestID(r.Context()))
		return employeeAuth{}, false
	}
	return auth, true
}

func (a *App) chatCompletions(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, modelMaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeModelError(w, 400, "invalid_request_error", "Invalid request.", requestID(r.Context()))
		return
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		writeModelError(w, 400, "invalid_request_error", "Invalid request.", requestID(r.Context()))
		return
	}
	var model string
	if raw, ok := payload["model"]; !ok || json.Unmarshal(raw, &model) != nil || !validIdentifier(model, 128) {
		writeModelError(w, 400, "invalid_request_error", "A valid model is required.", requestID(r.Context()))
		return
	}
	stream := false
	if raw, ok := payload["stream"]; ok && json.Unmarshal(raw, &stream) != nil {
		writeModelError(w, 400, "invalid_request_error", "The stream field must be boolean.", requestID(r.Context()))
		return
	}
	a.admission.RLock()
	auth, ok := a.authenticateEmployee(w, r)
	if !ok {
		a.admission.RUnlock()
		return
	}
	if auth.Mode == "selected" {
		var allowed int
		if err := a.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM employee_models WHERE employee_id=? AND model_id=?`, auth.EmployeeID, model).Scan(&allowed); err != nil {
			a.admission.RUnlock()
			writeModelError(w, 403, "model_not_allowed", "Model is not allowed for this key.", requestID(r.Context()))
			return
		}
	}
	var route route
	err = a.store.db.QueryRowContext(r.Context(), `SELECT u.id,u.endpoint,m.upstream_model,u.credential_ciphertext,u.provider_kind,u.revision,u.credential_state,u.key_version FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.id=? AND m.enabled=1 AND u.enabled=1`, model).Scan(&route.AccountID, &route.Endpoint, &route.UpstreamModel, &route.Ciphertext, &route.ProviderKind, &route.Revision, &route.CredentialState, &route.KeyVersion)
	if err != nil {
		a.admission.RUnlock()
		writeModelError(w, 503, "no_available_route", "No available route for this model.", requestID(r.Context()))
		return
	}
	modelRequestID := requestID(r.Context())
	_, err = a.store.db.ExecContext(r.Context(), `INSERT INTO model_requests(id,employee_id,key_id,model_id,started_at,outcome) VALUES(?,?,?,?,?,'running')`, modelRequestID, auth.EmployeeID, auth.KeyID, model, utcNow())
	a.admission.RUnlock()
	if err != nil {
		writeModelError(w, 503, "storage_unavailable", "Service is temporarily unavailable.", modelRequestID)
		return
	}
	if route.ProviderKind == codexMembershipProvider {
		a.handleCodexChatCompletion(w, r, payload, model, stream, route, modelRequestID)
		return
	}
	if route.ProviderKind != "openai-compatible" || route.KeyVersion != 1 {
		a.finishRequest(modelRequestID, "failed", 0)
		writeModelError(w, 503, "no_available_route", "No available route for this model.", modelRequestID)
		return
	}
	credential, err := a.secrets.decryptCredential(route.AccountID, route.Ciphertext)
	if err != nil {
		a.finishRequest(modelRequestID, "failed", 0)
		writeModelError(w, 503, "no_available_route", "No available route for this model.", modelRequestID)
		return
	}
	payload["model"], _ = json.Marshal(route.UpstreamModel)
	outgoing, err := json.Marshal(payload)
	if err != nil {
		a.finishRequest(modelRequestID, "failed", 0)
		writeModelError(w, 400, "invalid_request_error", "Invalid request.", modelRequestID)
		return
	}
	endpoint, err := validateEndpoint(r.Context(), route.Endpoint, a.cfg.AllowLoopbackUpstream)
	if err != nil {
		a.finishRequest(modelRequestID, "failed", 0)
		writeModelError(w, 503, "no_available_route", "No available route for this model.", modelRequestID)
		return
	}
	target, err := upstreamChatURL(endpoint)
	if err != nil {
		a.finishRequest(modelRequestID, "failed", 0)
		writeModelError(w, 503, "no_available_route", "No available route for this model.", modelRequestID)
		return
	}
	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(outgoing))
	if err != nil {
		a.finishRequest(modelRequestID, "failed", 0)
		writeModelError(w, 503, "upstream_unavailable", "Upstream is unavailable.", modelRequestID)
		return
	}
	upstreamReq.Header.Set("Authorization", "Bearer "+credential)
	upstreamReq.Header.Set("Content-Type", "application/json")
	if stream {
		upstreamReq.Header.Set("Accept", "text/event-stream")
	} else {
		upstreamReq.Header.Set("Accept", "application/json")
	}
	response, err := a.http.Do(upstreamReq)
	if err != nil {
		outcome := "failed"
		if errors.Is(r.Context().Err(), context.Canceled) {
			outcome = "cancelled"
		}
		a.finishRequest(modelRequestID, outcome, 0)
		if r.Context().Err() == nil {
			writeModelError(w, 502, "upstream_unavailable", "Upstream is unavailable.", modelRequestID)
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		if retry := safeRetryAfter(response.Header.Get("Retry-After")); retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		writeModelError(w, 502, "upstream_error", "Upstream request failed.", modelRequestID)
		return
	}
	if stream {
		a.forwardStream(w, r, response, modelRequestID)
		return
	}
	a.forwardJSON(w, r, response, modelRequestID)
}

func (a *App) forwardJSON(w http.ResponseWriter, r *http.Request, response *http.Response, requestID string) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		a.finishRequest(requestID, "failed", response.StatusCode)
		writeModelError(w, 502, "upstream_error", "Upstream returned an invalid response.", requestID)
		return
	}
	const max = 16 << 20
	body, err := io.ReadAll(io.LimitReader(response.Body, max+1))
	if err != nil || len(body) > max {
		a.finishRequest(requestID, "failed", response.StatusCode)
		writeModelError(w, 502, "upstream_error", "Upstream returned an invalid response.", requestID)
		return
	}
	a.finishRequest(requestID, "succeeded", response.StatusCode)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
}

func (a *App) forwardStream(w http.ResponseWriter, r *http.Request, response *http.Response, requestID string) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		a.finishRequest(requestID, "failed", response.StatusCode)
		writeModelError(w, 502, "upstream_error", "Upstream returned an invalid response.", requestID)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		a.finishRequest(requestID, "failed", response.StatusCode)
		writeModelError(w, 500, "streaming_unavailable", "Streaming is unavailable.", requestID)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(response.StatusCode)
	buffer := make([]byte, 32<<10)
	outcome := "succeeded"
	sawDone := false
	tail := make([]byte, 0, 32)
	for {
		n, err := response.Body.Read(buffer)
		if n > 0 {
			inspection := append(append(make([]byte, 0, len(tail)+n), tail...), buffer[:n]...)
			if bytes.Contains(inspection, []byte("data: [DONE]")) {
				sawDone = true
			}
			if len(inspection) > cap(tail) {
				tail = append(tail[:0], inspection[len(inspection)-cap(tail):]...)
			} else {
				tail = append(tail[:0], inspection...)
			}
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				outcome = "cancelled"
				break
			}
			flusher.Flush()
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				if r.Context().Err() != nil {
					outcome = "cancelled"
				} else {
					outcome = "failed"
				}
			} else if !sawDone {
				outcome = "interrupted"
			}
			break
		}
	}
	a.finishRequest(requestID, outcome, response.StatusCode)
}

func (a *App) finishRequest(id, outcome string, status int) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var upstream any
	if status != 0 {
		upstream = status
	}
	_, _ = a.store.db.ExecContext(ctx, `UPDATE model_requests SET outcome=?,finished_at=?,upstream_status=? WHERE id=? AND outcome='running'`, outcome, utcNow(), upstream, id)
}
