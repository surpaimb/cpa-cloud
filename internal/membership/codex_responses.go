package membership

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"
)

// Responses sends a validated native Responses request to the pinned Codex
// endpoint. Codex always receives an ephemeral streaming request. When consume
// is non-nil it receives each complete SSE data object, without SSE framing.
func (a *CodexDirectAdapter) Responses(ctx context.Context, credential *CodexAuthCredential, body []byte, consume func(json.RawMessage) error) (json.RawMessage, error) {
	if ctx == nil || a == nil || a.client == nil || a.endpoint == "" || a.now == nil {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	accessToken, accountID, err := a.validateCredential(credential)
	if err != nil {
		return nil, err
	}
	normalized, err := validateCodexResponsesRequest(body)
	if err != nil {
		return nil, err
	}
	requestContext := ctx
	cancel := func() {}
	if a.requestTimeout > 0 {
		requestContext, cancel = context.WithTimeout(ctx, a.requestTimeout)
	}
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodPost, a.endpoint, bytes.NewReader(normalized))
	if err != nil {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("ChatGPT-Account-ID", accountID)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, classifyCodexTransportError(requestContext, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, classifyCodexHTTPError(resp)
	}
	mediaType, _, parseErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if parseErr != nil || !strings.EqualFold(mediaType, "text/event-stream") {
		return nil, newCodexAdapterError(CodexErrorProtocol)
	}
	return a.parseNativeResponsesSSE(requestContext, resp.Body, consume)
}

func validateCodexResponsesRequest(body []byte) ([]byte, error) {
	if len(body) == 0 || len(body) > maxCodexRequestBytes {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	var root map[string]json.RawMessage
	if decodeStrictJSON(body, &root) != nil || root == nil {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	allowed := map[string]bool{"model": true, "input": true, "instructions": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true, "reasoning": true, "text": true, "include": true, "stream": true, "store": true}
	for key := range root {
		if !allowed[key] {
			return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
	}
	var model string
	if raw, ok := root["model"]; !ok || json.Unmarshal(raw, &model) != nil || strings.TrimSpace(model) != model || model == "" || len(model) > 256 {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	input, ok := root["input"]
	if !ok {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	normalizedInput, err := validateResponsesInput(input)
	if err != nil {
		return nil, err
	}
	root["input"] = normalizedInput
	if raw, ok := root["instructions"]; ok {
		var s string
		if isJSONNull(raw) || json.Unmarshal(raw, &s) != nil {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
	}
	if raw, ok := root["tools"]; ok {
		if err := validateResponseTools(raw); err != nil {
			return nil, err
		}
	}
	if raw, ok := root["tool_choice"]; ok {
		if err := validateToolChoice(raw); err != nil {
			return nil, err
		}
	}
	if raw, ok := root["parallel_tool_calls"]; ok {
		var v bool
		if isJSONNull(raw) || json.Unmarshal(raw, &v) != nil {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
	}
	if raw, ok := root["reasoning"]; ok {
		if err := validateReasoning(raw); err != nil {
			return nil, err
		}
	}
	if raw, ok := root["text"]; ok {
		if err := validateTextConfig(raw); err != nil {
			return nil, err
		}
	}
	if raw, ok := root["include"]; ok {
		var values []string
		if json.Unmarshal(raw, &values) != nil || values == nil {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
		for _, value := range values {
			if value != "reasoning.encrypted_content" {
				return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
			}
		}
	}
	if raw, ok := root["stream"]; ok {
		var v bool
		if isJSONNull(raw) || json.Unmarshal(raw, &v) != nil {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
	}
	if raw, ok := root["store"]; ok {
		var v bool
		if isJSONNull(raw) || json.Unmarshal(raw, &v) != nil {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
		if v {
			return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
	}
	root["stream"] = json.RawMessage("true")
	root["store"] = json.RawMessage("false")
	out, err := json.Marshal(root)
	if err != nil || len(out) > maxCodexRequestBytes {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	return out, nil
}

func validateResponsesInput(raw json.RawMessage) (json.RawMessage, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if text == "" {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
		return json.Marshal([]any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}})
	}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil || items == nil || len(items) == 0 || len(items) > 4096 {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	normalized := make([]json.RawMessage, 0, len(items))
	for _, item := range items {
		value, err := validateResponseInputItem(item)
		if err != nil {
			return nil, err
		}
		normalized = append(normalized, value)
	}
	return json.Marshal(normalized)
}

func validateResponseInputItem(raw json.RawMessage) (json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if decodeStrictJSON(raw, &obj) != nil {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	var typ string
	if typeRaw, ok := obj["type"]; ok {
		if isJSONNull(typeRaw) || json.Unmarshal(typeRaw, &typ) != nil {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
	} else if _, hasRole := obj["role"]; hasRole {
		typ = "message"
		obj["type"] = json.RawMessage(`"message"`)
	} else {
		return nil, newCodexAdapterError(CodexErrorInvalidRequest)
	}
	switch typ {
	case "message":
		if unknownKeys(obj, "type", "role", "content", "id", "status") {
			return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		var role string
		if json.Unmarshal(obj["role"], &role) != nil || (role != "user" && role != "assistant" && role != "system" && role != "developer") {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
		var easy string
		if !isJSONNull(obj["content"]) && json.Unmarshal(obj["content"], &easy) == nil {
			partType := "input_text"
			if role == "assistant" {
				partType = "output_text"
			}
			content, _ := json.Marshal([]any{map[string]any{"type": partType, "text": easy}})
			obj["content"] = content
		}
		var content []json.RawMessage
		if json.Unmarshal(obj["content"], &content) != nil || content == nil {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
		for _, part := range content {
			var p map[string]json.RawMessage
			if decodeStrictJSON(part, &p) != nil || unknownKeys(p, "type", "text", "annotations") {
				return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
			}
			var pt, txt string
			if isJSONNull(p["type"]) || isJSONNull(p["text"]) || json.Unmarshal(p["type"], &pt) != nil || json.Unmarshal(p["text"], &txt) != nil {
				return nil, newCodexAdapterError(CodexErrorInvalidRequest)
			}
			if pt != "input_text" && pt != "output_text" {
				return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
			}
			if annotations, ok := p["annotations"]; ok {
				var values []json.RawMessage
				if json.Unmarshal(annotations, &values) != nil || values == nil {
					return nil, newCodexAdapterError(CodexErrorInvalidRequest)
				}
			}
		}
	case "function_call":
		if unknownKeys(obj, "type", "id", "call_id", "name", "arguments", "status") {
			return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		if !requiredStrings(obj, "call_id", "name", "arguments") {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
	case "function_call_output":
		if unknownKeys(obj, "type", "id", "call_id", "output", "status") {
			return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		if !requiredStrings(obj, "call_id") || !stringField(obj, "output") {
			return nil, newCodexAdapterError(CodexErrorInvalidRequest)
		}
	case "reasoning":
		if unknownKeys(obj, "type", "id", "summary", "content", "encrypted_content", "status") {
			return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		var encrypted string
		if value, ok := obj["encrypted_content"]; !ok || json.Unmarshal(value, &encrypted) != nil || encrypted == "" {
			return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		for _, key := range []string{"summary", "content"} {
			if value, ok := obj[key]; ok {
				var parts []map[string]json.RawMessage
				if json.Unmarshal(value, &parts) != nil {
					return nil, newCodexAdapterError(CodexErrorInvalidRequest)
				}
				for _, part := range parts {
					if unknownKeys(part, "type", "text") || !requiredStrings(part, "type", "text") {
						return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
					}
				}
			}
		}
	default:
		return nil, newCodexAdapterError(CodexErrorUnsupportedFeature)
	}
	return json.Marshal(obj)
}

func validateResponseTools(raw json.RawMessage) error {
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || tools == nil {
		return newCodexAdapterError(CodexErrorInvalidRequest)
	}
	for _, tool := range tools {
		var o map[string]json.RawMessage
		if decodeStrictJSON(tool, &o) != nil {
			return newCodexAdapterError(CodexErrorInvalidRequest)
		}
		if unknownKeys(o, "type", "name", "description", "parameters", "strict") {
			return newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		var typ string
		if json.Unmarshal(o["type"], &typ) != nil || typ != "function" {
			return newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		if !requiredStrings(o, "name") {
			return newCodexAdapterError(CodexErrorInvalidRequest)
		}
		if description, ok := o["description"]; ok {
			var value string
			if isJSONNull(description) || json.Unmarshal(description, &value) != nil {
				return newCodexAdapterError(CodexErrorInvalidRequest)
			}
		}
		if p, ok := o["parameters"]; ok {
			var v map[string]any
			if isJSONNull(p) || json.Unmarshal(p, &v) != nil || v == nil {
				return newCodexAdapterError(CodexErrorInvalidRequest)
			}
		}
		if s, ok := o["strict"]; ok {
			var v bool
			if isJSONNull(s) || json.Unmarshal(s, &v) != nil {
				return newCodexAdapterError(CodexErrorInvalidRequest)
			}
		}
	}
	return nil
}

func validateToolChoice(raw json.RawMessage) error {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "auto" || s == "none" || s == "required" {
			return nil
		}
		return newCodexAdapterError(CodexErrorUnsupportedFeature)
	}
	var o map[string]json.RawMessage
	if decodeStrictJSON(raw, &o) != nil || unknownKeys(o, "type", "name") {
		return newCodexAdapterError(CodexErrorUnsupportedFeature)
	}
	var typ string
	if json.Unmarshal(o["type"], &typ) != nil || !requiredStrings(o, "name") {
		return newCodexAdapterError(CodexErrorInvalidRequest)
	}
	if typ != "function" {
		return newCodexAdapterError(CodexErrorUnsupportedFeature)
	}
	return nil
}
func validateReasoning(raw json.RawMessage) error {
	var o map[string]json.RawMessage
	if decodeStrictJSON(raw, &o) != nil || o == nil {
		return newCodexAdapterError(CodexErrorInvalidRequest)
	}
	if unknownKeys(o, "effort", "summary") {
		return newCodexAdapterError(CodexErrorUnsupportedFeature)
	}
	for k, v := range o {
		var s string
		if json.Unmarshal(v, &s) != nil {
			return newCodexAdapterError(CodexErrorInvalidRequest)
		}
		if k == "effort" && (s != "minimal" && s != "low" && s != "medium" && s != "high" && s != "xhigh") {
			return newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
		if k == "summary" && (s != "auto" && s != "concise" && s != "detailed") {
			return newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
	}
	return nil
}
func validateTextConfig(raw json.RawMessage) error {
	var o map[string]json.RawMessage
	if decodeStrictJSON(raw, &o) != nil || o == nil {
		return newCodexAdapterError(CodexErrorInvalidRequest)
	}
	if unknownKeys(o, "verbosity") {
		return newCodexAdapterError(CodexErrorUnsupportedFeature)
	}
	if v, ok := o["verbosity"]; ok {
		var s string
		if json.Unmarshal(v, &s) != nil {
			return newCodexAdapterError(CodexErrorInvalidRequest)
		}
		if s != "low" && s != "medium" && s != "high" {
			return newCodexAdapterError(CodexErrorUnsupportedFeature)
		}
	}
	return nil
}
func decodeStrictJSON(raw []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(dst); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func unknownKeys(obj map[string]json.RawMessage, allowed ...string) bool {
	set := map[string]bool{}
	for _, k := range allowed {
		set[k] = true
	}
	for k := range obj {
		if !set[k] {
			return true
		}
	}
	return false
}
func requiredStrings(obj map[string]json.RawMessage, keys ...string) bool {
	for _, k := range keys {
		var s string
		if isJSONNull(obj[k]) || json.Unmarshal(obj[k], &s) != nil || s == "" {
			return false
		}
	}
	return true
}
func stringField(obj map[string]json.RawMessage, key string) bool {
	var s string
	raw, ok := obj[key]
	return ok && !isJSONNull(raw) && json.Unmarshal(raw, &s) == nil
}

func (a *CodexDirectAdapter) parseNativeResponsesSSE(ctx context.Context, body io.Reader, consume func(json.RawMessage) error) (json.RawMessage, error) {
	limited := &io.LimitedReader{R: body, N: a.maxResponse + 1}
	scanner := bufio.NewScanner(limited)
	initial := 64 << 10
	if a.maxLine < initial {
		initial = a.maxLine
	}
	scanner.Buffer(make([]byte, initial), a.maxLine)
	var data bytes.Buffer
	var final json.RawMessage
	completed := false
	dispatch := func() error {
		if data.Len() == 0 {
			return nil
		}
		raw := bytes.Clone(bytes.TrimSuffix(data.Bytes(), []byte("\n")))
		data.Reset()
		if len(raw) == 0 {
			return nil
		}
		if bytes.Equal(raw, []byte("[DONE]")) || validateJSON(raw) != nil {
			return newCodexAdapterError(CodexErrorProtocol)
		}
		var env struct {
			Type     string          `json:"type"`
			Response json.RawMessage `json:"response"`
		}
		if json.Unmarshal(raw, &env) != nil || env.Type == "" {
			return newCodexAdapterError(CodexErrorProtocol)
		}
		switch env.Type {
		case "response.completed":
			if completed || len(env.Response) == 0 || validateJSON(env.Response) != nil {
				return newCodexAdapterError(CodexErrorProtocol)
			}
			var r struct {
				ID     string            `json:"id"`
				Object string            `json:"object"`
				Status string            `json:"status"`
				Output []json.RawMessage `json:"output"`
			}
			if json.Unmarshal(env.Response, &r) != nil || r.ID == "" || r.Object != "response" || r.Status != "completed" || r.Output == nil {
				return newCodexAdapterError(CodexErrorProtocol)
			}
			if consume != nil && consume(raw) != nil {
				return newCodexAdapterError(CodexErrorEventConsumerStopped)
			}
			final = bytes.Clone(env.Response)
			completed = true
			return nil
		case "response.failed":
			return nativeTerminalError(env.Response, CodexErrorUpstream)
		case "response.incomplete":
			return nativeTerminalError(env.Response, CodexErrorUpstream)
		case "error":
			return newCodexAdapterError(CodexErrorUpstream)
		default:
			if consume != nil && consume(raw) != nil {
				return newCodexAdapterError(CodexErrorEventConsumerStopped)
			}
			return nil
		}
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, classifyCodexTransportError(ctx, err)
		}
		line := scanner.Bytes()
		if len(line) == 0 {
			if err := dispatch(); err != nil {
				return nil, err
			}
			if completed {
				return final, nil
			}
			continue
		}
		if line[0] == ':' {
			continue
		}
		field, value, found := bytes.Cut(line, []byte(":"))
		if !found {
			field = line
			value = nil
		}
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
		if bytes.Equal(field, []byte("data")) {
			if data.Len()+len(value)+1 > a.maxEvent {
				return nil, newCodexAdapterError(CodexErrorSSEFrameTooLarge)
			}
			data.Write(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return nil, classifyCodexTransportError(ctx, err)
		}
		if limited.N <= 0 {
			return nil, newCodexAdapterError(CodexErrorResponseTooLarge)
		}
		return nil, newCodexAdapterError(CodexErrorSSEFrameTooLarge)
	}
	if limited.N <= 0 {
		return nil, newCodexAdapterError(CodexErrorResponseTooLarge)
	}
	if err := dispatch(); err != nil {
		return nil, err
	}
	if completed {
		return final, nil
	}
	return nil, newCodexAdapterError(CodexErrorProtocol)
}
func nativeTerminalError(raw json.RawMessage, fallback CodexAdapterErrorCode) error {
	var v struct {
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
		IncompleteDetails *struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &v)
	}
	code := ""
	if v.Error != nil {
		code = allowlistedCodexCode(v.Error.Code)
	}
	if v.IncompleteDetails != nil {
		code = allowlistedCodexCode(v.IncompleteDetails.Reason)
	}
	return &CodexAdapterError{code: fallback, providerCode: code}
}
