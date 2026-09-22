package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	geminiMaxResponseBody = 16 << 20
	geminiMaxSSEEvent     = 1 << 20
	geminiMaxSSEStream    = 64 << 20
)

func (a *App) listGeminiModels(w http.ResponseWriter, r *http.Request) {
	queryValues := r.URL.Query()
	for key := range queryValues {
		if key != "pageSize" && key != "pageToken" {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid request.")
			return
		}
	}
	pageSize := 50
	if raw := queryValues.Get("pageSize"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 1000 {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid request.")
			return
		}
		pageSize = value
	}
	offset := 0
	if raw := queryValues.Get("pageToken"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid request.")
			return
		}
		offset = value
	}
	if len(queryValues["pageSize"]) > 1 || len(queryValues["pageToken"]) > 1 {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid request.")
		return
	}
	auth, err := a.authenticateEmployeeRequest(r)
	if err != nil {
		writeGeminiError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Invalid API key.")
		return
	}
	query := `SELECT m.id FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.enabled=1 AND u.enabled=1 AND u.provider_kind=?`
	args := []any{geminiAPIKeyProvider}
	if auth.Mode == "selected" {
		query += ` AND EXISTS(SELECT 1 FROM employee_models em WHERE em.employee_id=? AND em.model_id=m.id)`
		args = append(args, auth.EmployeeID)
	}
	query += ` ORDER BY m.id LIMIT ? OFFSET ?`
	args = append(args, pageSize+1, offset)
	rows, err := a.store.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "Service is temporarily unavailable.")
		return
	}
	defer rows.Close()
	models := make([]map[string]any, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "Service is temporarily unavailable.")
			return
		}
		models = append(models, map[string]any{
			"name":                       "models/" + id,
			"baseModelId":                id,
			"displayName":                id,
			"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"},
		})
	}
	if err := rows.Err(); err != nil {
		writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "Service is temporarily unavailable.")
		return
	}
	response := map[string]any{"models": models}
	if len(models) > pageSize {
		response["models"] = models[:pageSize]
		response["nextPageToken"] = strconv.Itoa(offset + pageSize)
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) geminiGenerateContent(w http.ResponseWriter, r *http.Request) {
	operation := r.PathValue("operation")
	stream := false
	var model string
	switch {
	case strings.HasSuffix(operation, ":generateContent"):
		model = strings.TrimSuffix(operation, ":generateContent")
		if r.URL.RawQuery != "" {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid request.")
			return
		}
	case strings.HasSuffix(operation, ":streamGenerateContent"):
		model = strings.TrimSuffix(operation, ":streamGenerateContent")
		stream = true
		if r.URL.RawQuery != "" && r.URL.RawQuery != "alt=sse" {
			writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid request.")
			return
		}
	default:
		writeGeminiError(w, http.StatusNotFound, "NOT_FOUND", "Method was not found.")
		return
	}
	if !validIdentifier(model, 128) || strings.Contains(model, "/") {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid model.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, modelMaxBody)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Invalid request.")
		return
	}
	if err := validateGeminiRequest(body); err != nil {
		status := "INVALID_ARGUMENT"
		if errors.Is(err, errGeminiUnsupported) {
			status = "UNIMPLEMENTED"
		}
		writeGeminiError(w, http.StatusBadRequest, status, "Unsupported or invalid Gemini request.")
		return
	}

	a.admission.RLock()
	auth, err := a.authenticateEmployeeRequest(r)
	if err != nil {
		a.admission.RUnlock()
		writeGeminiError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "Invalid API key.")
		return
	}
	if auth.Mode == "selected" {
		var allowed int
		if err := a.store.db.QueryRowContext(r.Context(), `SELECT 1 FROM employee_models WHERE employee_id=? AND model_id=?`, auth.EmployeeID, model).Scan(&allowed); err != nil {
			a.admission.RUnlock()
			writeGeminiError(w, http.StatusForbidden, "PERMISSION_DENIED", "Model is not allowed for this key.")
			return
		}
	}
	var route route
	err = a.store.db.QueryRowContext(r.Context(), `SELECT u.id,u.endpoint,m.upstream_model,u.credential_ciphertext,u.provider_kind,u.revision,u.credential_state,u.key_version FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.id=? AND m.enabled=1 AND u.enabled=1`, model).Scan(&route.AccountID, &route.Endpoint, &route.UpstreamModel, &route.Ciphertext, &route.ProviderKind, &route.Revision, &route.CredentialState, &route.KeyVersion)
	if err != nil || route.ProviderKind != geminiAPIKeyProvider || route.KeyVersion != 2 {
		a.admission.RUnlock()
		writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "No available route for this model.")
		return
	}
	modelRequestID := requestID(r.Context())
	_, err = a.store.db.ExecContext(r.Context(), `INSERT INTO model_requests(id,employee_id,key_id,model_id,started_at,outcome) VALUES(?,?,?,?,?,'running')`, modelRequestID, auth.EmployeeID, auth.KeyID, model, utcNow())
	a.admission.RUnlock()
	if err != nil {
		writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "Service is temporarily unavailable.")
		return
	}
	credential, err := a.secrets.decryptGeminiAPIKey(route.AccountID, route.Ciphertext)
	if err != nil {
		a.finishRequest(modelRequestID, "failed", 0)
		writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "No available route for this model.")
		return
	}
	endpoint, err := validateGeminiEndpoint(r.Context(), route.Endpoint, a.cfg.AllowLoopbackUpstream)
	if err != nil || !validGeminiUpstreamModel(route.UpstreamModel) {
		a.finishRequest(modelRequestID, "failed", 0)
		writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "No available route for this model.")
		return
	}
	target, err := geminiGenerateURL(endpoint, route.UpstreamModel, stream)
	if err != nil {
		a.finishRequest(modelRequestID, "failed", 0)
		writeGeminiError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "No available route for this model.")
		return
	}
	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		a.finishRequest(modelRequestID, "failed", 0)
		writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", "Upstream is unavailable.")
		return
	}
	upstreamReq.Header.Set("x-goog-api-key", credential)
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
			writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", "Upstream is unavailable.")
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		writeGeminiUpstreamFailure(w, response)
		return
	}
	if stream {
		a.forwardGeminiStream(w, r, response, modelRequestID)
		return
	}
	a.forwardGeminiJSON(w, response, modelRequestID)
}

func writeGeminiError(w http.ResponseWriter, code int, status, message string) {
	writeJSON(w, code, map[string]any{"error": map[string]any{"code": code, "message": message, "status": status}})
}

func writeGeminiUpstreamFailure(w http.ResponseWriter, response *http.Response) {
	if retry := safeRetryAfter(response.Header.Get("Retry-After")); retry != "" {
		w.Header().Set("Retry-After", retry)
	}
	switch response.StatusCode {
	case http.StatusBadRequest:
		writeGeminiError(w, http.StatusBadRequest, "INVALID_ARGUMENT", "Upstream rejected the request.")
	case http.StatusTooManyRequests:
		writeGeminiError(w, http.StatusTooManyRequests, "RESOURCE_EXHAUSTED", "Upstream rate limit was reached.")
	case http.StatusUnauthorized, http.StatusForbidden:
		writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", "Upstream authentication failed.")
	default:
		writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", "Upstream request failed.")
	}
}

func (a *App) forwardGeminiJSON(w http.ResponseWriter, response *http.Response, modelRequestID string) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "application/json") {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", "Upstream returned an invalid response.")
		return
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, geminiMaxResponseBody+1))
	trimmed := bytes.TrimSpace(body)
	if err != nil || len(body) > geminiMaxResponseBody || len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", "Upstream returned an invalid response.")
		return
	}
	a.finishRequest(modelRequestID, "succeeded", response.StatusCode)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func (a *App) forwardGeminiStream(w http.ResponseWriter, r *http.Request, response *http.Response, modelRequestID string) {
	if !strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", "Upstream returned an invalid response.")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		writeGeminiError(w, http.StatusInternalServerError, "INTERNAL", "Streaming is unavailable.")
		return
	}
	reader := bufio.NewReaderSize(response.Body, 32<<10)
	first, err := readGeminiSSEEvent(reader)
	if err != nil {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		writeGeminiError(w, http.StatusBadGateway, "UNAVAILABLE", "Upstream returned an invalid stream.")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	total := 0
	outcome := "succeeded"
	event := first
	for {
		total += len(event)
		if total > geminiMaxSSEStream {
			outcome = "failed"
			break
		}
		if _, err := w.Write(event); err != nil {
			outcome = "cancelled"
			break
		}
		flusher.Flush()
		event, err = readGeminiSSEEvent(reader)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			if r.Context().Err() != nil {
				outcome = "cancelled"
			} else {
				outcome = "interrupted"
			}
			break
		}
	}
	a.finishRequest(modelRequestID, outcome, response.StatusCode)
}

func readGeminiSSEEvent(reader *bufio.Reader) ([]byte, error) {
	var event bytes.Buffer
	var data bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			if event.Len()+len(line) > geminiMaxSSEEvent {
				return nil, errors.New("SSE event is too large")
			}
			event.WriteString(line)
			trimmed := strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
			if strings.HasPrefix(trimmed, "data:") {
				value := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(value)
			}
			if trimmed == "" {
				if data.Len() == 0 {
					event.Reset()
					continue
				}
				if !json.Valid(data.Bytes()) {
					return nil, errors.New("invalid SSE data")
				}
				return event.Bytes(), nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && event.Len() == 0 {
				return nil, io.EOF
			}
			if errors.Is(err, io.EOF) && data.Len() > 0 && json.Valid(data.Bytes()) {
				return event.Bytes(), nil
			}
			return nil, err
		}
	}
}

func geminiGenerateURL(endpoint, upstreamModel string, stream bool) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	method := "generateContent"
	if stream {
		method = "streamGenerateContent"
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1beta/models/" + url.PathEscape(strings.TrimPrefix(upstreamModel, "models/")) + ":" + method
	u.RawPath = ""
	if stream {
		u.RawQuery = "alt=sse"
	}
	return u.String(), nil
}

func validGeminiUpstreamModel(model string) bool {
	model = strings.TrimPrefix(model, "models/")
	return validIdentifier(model, 256) && !strings.ContainsAny(model, "/:")
}

var errGeminiUnsupported = errors.New("unsupported Gemini field")

func validateGeminiRequest(body []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil || root == nil {
		return errors.New("invalid JSON object")
	}
	allowed := map[string]bool{"contents": true, "systemInstruction": true, "tools": true, "toolConfig": true, "generationConfig": true, "safetySettings": true}
	for key := range root {
		if !allowed[key] {
			return errGeminiUnsupported
		}
	}
	contents, ok := root["contents"]
	if !ok {
		return errors.New("contents is required")
	}
	if err := validateGeminiContents(contents, false); err != nil {
		return err
	}
	if raw, ok := root["systemInstruction"]; ok {
		if err := validateGeminiContents(json.RawMessage("["+string(raw)+"]"), true); err != nil {
			return err
		}
	}
	if raw, ok := root["tools"]; ok {
		if err := validateGeminiTools(raw); err != nil {
			return err
		}
	}
	if raw, ok := root["toolConfig"]; ok {
		if err := validateGeminiToolConfig(raw); err != nil {
			return err
		}
	}
	if raw, ok := root["generationConfig"]; ok {
		allowedGeneration := map[string]bool{"candidateCount": true, "stopSequences": true, "maxOutputTokens": true, "temperature": true, "topP": true, "topK": true, "seed": true, "presencePenalty": true, "frequencyPenalty": true, "responseMimeType": true, "responseSchema": true}
		generation, err := decodeGeminiObject(raw, allowedGeneration)
		if err != nil {
			return err
		}
		if err := validateGeminiGenerationConfig(generation); err != nil {
			return err
		}
	}
	if raw, ok := root["safetySettings"]; ok {
		var values []json.RawMessage
		if err := json.Unmarshal(raw, &values); err != nil {
			return errors.New("invalid safetySettings")
		}
	}
	return nil
}

func validateGeminiGenerationConfig(config map[string]json.RawMessage) error {
	for _, field := range []string{"candidateCount", "maxOutputTokens", "topK", "seed"} {
		if raw, ok := config[field]; ok {
			var value int64
			if json.Unmarshal(raw, &value) != nil {
				return errors.New("invalid integer generation setting")
			}
		}
	}
	for _, field := range []string{"temperature", "topP", "presencePenalty", "frequencyPenalty"} {
		if raw, ok := config[field]; ok {
			var value float64
			if json.Unmarshal(raw, &value) != nil {
				return errors.New("invalid numeric generation setting")
			}
		}
	}
	if raw, ok := config["stopSequences"]; ok {
		var values []string
		if json.Unmarshal(raw, &values) != nil || len(values) > 64 {
			return errors.New("invalid stop sequences")
		}
	}
	if raw, ok := config["responseMimeType"]; ok {
		var value string
		if json.Unmarshal(raw, &value) != nil || !validText(value, 1, 256) {
			return errors.New("invalid response MIME type")
		}
	}
	if raw, ok := config["responseSchema"]; ok {
		var value map[string]json.RawMessage
		if json.Unmarshal(raw, &value) != nil || value == nil {
			return errors.New("invalid response schema")
		}
	}
	return nil
}

func validateGeminiContents(raw json.RawMessage, system bool) error {
	var contents []json.RawMessage
	if err := json.Unmarshal(raw, &contents); err != nil || len(contents) == 0 || len(contents) > 256 {
		return errors.New("invalid contents")
	}
	for _, contentRaw := range contents {
		content, err := decodeGeminiObject(contentRaw, map[string]bool{"role": true, "parts": true})
		if err != nil {
			return err
		}
		if roleRaw, ok := content["role"]; ok {
			var role string
			if json.Unmarshal(roleRaw, &role) != nil || (!system && role != "user" && role != "model") || (system && role != "system" && role != "user") {
				return errors.New("invalid content role")
			}
		}
		var parts []json.RawMessage
		if rawParts, ok := content["parts"]; !ok || json.Unmarshal(rawParts, &parts) != nil || len(parts) == 0 || len(parts) > 512 {
			return errors.New("invalid content parts")
		}
		for _, partRaw := range parts {
			part, err := decodeGeminiObject(partRaw, map[string]bool{"text": true, "functionCall": true, "functionResponse": true})
			if err != nil || len(part) != 1 {
				return errGeminiUnsupported
			}
			if textRaw, ok := part["text"]; ok {
				var text string
				if json.Unmarshal(textRaw, &text) != nil {
					return errors.New("invalid text part")
				}
				continue
			}
			if system {
				return errGeminiUnsupported
			}
			if call, ok := part["functionCall"]; ok {
				if err := validateGeminiFunctionCall(call, false); err != nil {
					return err
				}
			}
			if response, ok := part["functionResponse"]; ok {
				if err := validateGeminiFunctionCall(response, true); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func validateGeminiFunctionCall(raw json.RawMessage, response bool) error {
	allowed := map[string]bool{"id": true, "name": true, "args": true}
	if response {
		allowed = map[string]bool{"id": true, "name": true, "response": true}
	}
	value, err := decodeGeminiObject(raw, allowed)
	if err != nil {
		return err
	}
	var name string
	if nameRaw, ok := value["name"]; !ok || json.Unmarshal(nameRaw, &name) != nil || !validGeminiFunctionName(name) {
		return errors.New("invalid function name")
	}
	required := "args"
	if response {
		required = "response"
	}
	if objectRaw, ok := value[required]; response && !ok {
		return errors.New("function response is required")
	} else if ok {
		var object map[string]json.RawMessage
		if json.Unmarshal(objectRaw, &object) != nil || object == nil {
			return errors.New("invalid function object")
		}
	}
	return nil
}

func validateGeminiTools(raw json.RawMessage) error {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || len(tools) == 0 || len(tools) > 64 {
		return errors.New("invalid tools")
	}
	for _, toolRaw := range tools {
		tool, err := decodeGeminiObject(toolRaw, map[string]bool{"functionDeclarations": true})
		if err != nil || len(tool) != 1 {
			return errGeminiUnsupported
		}
		var declarations []json.RawMessage
		if json.Unmarshal(tool["functionDeclarations"], &declarations) != nil || len(declarations) == 0 || len(declarations) > 128 {
			return errors.New("invalid function declarations")
		}
		for _, declarationRaw := range declarations {
			declaration, err := decodeGeminiObject(declarationRaw, map[string]bool{"name": true, "description": true, "parameters": true, "response": true})
			if err != nil {
				return err
			}
			var name, description string
			if json.Unmarshal(declaration["name"], &name) != nil || !validGeminiFunctionName(name) || json.Unmarshal(declaration["description"], &description) != nil || !validText(description, 1, 8192) {
				return errors.New("invalid function declaration")
			}
			for _, field := range []string{"parameters", "response"} {
				if schema, ok := declaration[field]; ok {
					var object map[string]json.RawMessage
					if json.Unmarshal(schema, &object) != nil || object == nil {
						return errors.New("invalid function schema")
					}
				}
			}
		}
	}
	return nil
}

func validateGeminiToolConfig(raw json.RawMessage) error {
	config, err := decodeGeminiObject(raw, map[string]bool{"functionCallingConfig": true})
	if err != nil || len(config) != 1 {
		return errGeminiUnsupported
	}
	callConfig, err := decodeGeminiObject(config["functionCallingConfig"], map[string]bool{"mode": true, "allowedFunctionNames": true})
	if err != nil {
		return err
	}
	if rawMode, ok := callConfig["mode"]; ok {
		var mode string
		if json.Unmarshal(rawMode, &mode) != nil || !containsString([]string{"AUTO", "ANY", "NONE", "VALIDATED"}, mode) {
			return errors.New("invalid function calling mode")
		}
	}
	if namesRaw, ok := callConfig["allowedFunctionNames"]; ok {
		var names []string
		if json.Unmarshal(namesRaw, &names) != nil || len(names) > 128 {
			return errors.New("invalid allowed function names")
		}
		for _, name := range names {
			if !validGeminiFunctionName(name) {
				return errors.New("invalid allowed function name")
			}
		}
	}
	return nil
}

func decodeGeminiObject(raw json.RawMessage, allowed map[string]bool) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, errors.New("invalid object")
	}
	for key := range value {
		if !allowed[key] {
			return nil, errGeminiUnsupported
		}
	}
	return value, nil
}

func validGeminiFunctionName(name string) bool {
	if !validText(name, 1, 128) {
		return false
	}
	for _, character := range name {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || strings.ContainsRune("_:.-", character)) {
			return false
		}
	}
	return true
}
