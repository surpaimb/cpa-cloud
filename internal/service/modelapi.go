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

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/scheduling"
)

type employeeAuth struct {
	EmployeeID string
	KeyID      string
	Mode       string
}
type route struct {
	egress          *routeEgress
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
	query := `SELECT m.id,m.created_at FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.enabled=1 AND ` + a.availableModelRouteSQL(false)
	args := []any{}
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
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil || closeErr != nil {
		writeModelError(w, 503, "service_unavailable", "Service is temporarily unavailable.", requestID(r.Context()))
		return
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
	auth, valid := a.lookupEmployeeKey(r.Context(), parts[1])
	if !valid {
		writeModelError(w, 401, "invalid_api_key", "Invalid API key.", requestID(r.Context()))
		return employeeAuth{}, false
	}
	return auth, true
}

func (a *App) lookupEmployeeKey(ctx context.Context, key string) (employeeAuth, bool) {
	if !strings.HasPrefix(key, "cpac_") {
		return employeeAuth{}, false
	}
	pair := strings.Split(strings.TrimPrefix(key, "cpac_"), ".")
	if len(pair) != 2 || pair[0] == "" || pair[1] == "" {
		return employeeAuth{}, false
	}
	var auth employeeAuth
	var digest []byte
	var status string
	var expires, revoked sql.NullString
	err := a.store.db.QueryRowContext(ctx, `SELECT k.employee_id,k.id,k.digest,k.expires_at,k.revoked_at,e.status,e.model_mode FROM access_keys k JOIN employees e ON e.id=k.employee_id WHERE k.selector=?`, pair[0]).Scan(&auth.EmployeeID, &auth.KeyID, &digest, &expires, &revoked, &status, &auth.Mode)
	valid := err == nil && subtle.ConstantTimeCompare(a.secrets.digest("employee-key/v1\x00"+pair[0], pair[1]), digest) == 1 && status == "active" && !revoked.Valid
	if valid && expires.Valid {
		t, e := parseTime(expires.String)
		valid = e == nil && time.Now().UTC().Before(t)
	}
	if !valid {
		return employeeAuth{}, false
	}
	return auth, true
}

func (a *App) authenticateEmployeeRequest(r *http.Request) (employeeAuth, error) {
	values := r.Header.Values("Authorization")
	if len(values) != 1 || len(r.Header.Values("X-API-Key")) != 0 {
		return employeeAuth{}, errors.New("invalid employee key")
	}
	parts := strings.Fields(values[0])
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return employeeAuth{}, errors.New("invalid employee key")
	}
	auth, valid := a.lookupEmployeeKey(r.Context(), parts[1])
	if !valid {
		return employeeAuth{}, errors.New("invalid employee key")
	}
	return auth, nil
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
	auth, ok := a.authenticateEmployee(w, r)
	if !ok {
		return
	}
	if stream {
		if _, ok := w.(http.Flusher); !ok {
			writeModelError(w, http.StatusInternalServerError, "streaming_unavailable", "Streaming is unavailable.", requestID(r.Context()))
			return
		}
	}
	modelRequestID := requestID(r.Context())
	var upstreamReq *http.Request
	var codexPrepared *codexChatPreflight
	selected, lease, failure := a.prepareModelRoute(r, auth, model, []string{"openai-compatible", codexMembershipProvider}, accounting.ProtocolOpenAIChatCompletions, true, func(candidateRequest *http.Request, candidate route) (route, *modelPreflightError) {
		if candidate.ProviderKind == codexMembershipProvider {
			prepared, failed := a.prepareCodexChatCompletion(candidateRequest.Context(), payload, candidate)
			if failed != nil {
				return route{}, failed
			}
			codexPrepared = prepared
			return prepared.selected, nil
		}
		if candidate.ProviderKind != "openai-compatible" || candidate.KeyVersion != 1 {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
		}
		credential, err := a.secrets.decryptCredential(candidate.AccountID, candidate.Ciphertext)
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailureAuth)
		}
		candidatePayload := make(map[string]json.RawMessage, len(payload))
		for key, value := range payload {
			candidatePayload[key] = value
		}
		candidatePayload["model"], _ = json.Marshal(candidate.UpstreamModel)
		outgoing, err := json.Marshal(candidatePayload)
		if err != nil {
			return route{}, requestPreflightFailure(http.StatusBadRequest, "invalid_request_error", "Invalid request.")
		}
		endpoint, err := validateEndpoint(candidateRequest.Context(), candidate.Endpoint, a.cfg.AllowLoopbackUpstream)
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
		}
		target, err := upstreamChatURL(endpoint)
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
		}
		prepared, err := http.NewRequestWithContext(candidateRequest.Context(), http.MethodPost, target, bytes.NewReader(outgoing))
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "upstream_unavailable", "Upstream is unavailable.", scheduling.FailurePermanent)
		}
		prepared.Header.Set("Authorization", "Bearer "+credential)
		prepared.Header.Set("Content-Type", "application/json")
		if stream {
			prepared.Header.Set("Accept", "text/event-stream")
		} else {
			prepared.Header.Set("Accept", "application/json")
		}
		upstreamReq = prepared
		return candidate, nil
	})
	if failure != nil {
		if codexPrepared != nil {
			codexPrepared.Destroy()
		}
		if r.Context().Err() != nil {
			return
		}
		writeModelError(w, failure.status, failure.code, failure.message, modelRequestID)
		return
	}
	if lease != nil {
		r = r.WithContext(lease.Context())
	}
	defer a.releaseModelLease(lease, modelRequestID, true)
	if codexPrepared != nil {
		defer codexPrepared.Destroy()
	}
	client, dispatchFailure := a.dispatchModelRoute(r, auth, model, selected, lease, true)
	if dispatchFailure != nil {
		a.finishRequest(modelRequestID, "failed", 0)
		writeModelError(w, dispatchFailure.status, dispatchFailure.code, dispatchFailure.message, modelRequestID)
		return
	}
	if codexPrepared != nil {
		a.handleCodexChatCompletion(w, r, model, stream, codexPrepared, modelRequestID)
		return
	}
	response, err := client.Do(upstreamReq)
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

func (a *App) finishRequest(id, outcome string, status int) {
	_ = a.finishRequestChecked(id, outcome, status)
}

func (a *App) finishRequestChecked(id, outcome string, status int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, exists := a.usageRequests.Load(id); exists {
		return a.finishRequestUsage(ctx, id, outcome, status)
	}
	var upstream any
	if status != 0 {
		upstream = status
	}
	result, err := a.store.db.ExecContext(ctx, `UPDATE model_requests SET outcome=?,finished_at=?,upstream_status=? WHERE id=? AND outcome='running'`, outcome, utcNow(), upstream, id)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("model request state changed during request")
	}
	return nil
}
