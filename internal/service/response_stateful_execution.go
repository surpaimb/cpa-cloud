package service

// Narrow stateful Responses request support. Only the body shapes covered by
// ADR 0003 are retained; provider-side state identifiers are never trusted or
// forwarded because ownership is enforced against local encrypted resources.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/protocolconv"
)

func (a *App) backgroundResponseProvider(ctx context.Context, model string) (string, error) {
	var provider string
	var wire routeWireProtocol
	err := a.store.db.QueryRowContext(ctx, `SELECT u.provider_kind,m.wire_protocol FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.id=? AND m.enabled=1 AND m.archived=0 AND u.enabled=1 AND u.archived=0 AND NOT EXISTS(SELECT 1 FROM model_account_pool_configs c WHERE c.model_id=m.id)`, model).Scan(&provider, &wire)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errResponseResourceForbidden
		}
		return "", errResponseResourceUnavailable
	}
	if provider != "openai-compatible" && provider != codexMembershipProvider {
		return "", errResponseResourceForbidden
	}
	upstream, err := routeUpstreamProtocol(route{ProviderKind: provider, WireProtocol: wire}, accounting.ProtocolOpenAIResponses)
	if err != nil || upstream != protocolconv.ProtocolOpenAIResponses {
		return "", errResponseResourceForbidden
	}
	return provider, nil
}

type responsesLifecycleRequest struct {
	store      bool
	background bool
	previousID string
}

type responsePersistencePlan struct {
	coordinator *responseResourceCoordinator
	auth        employeeAuth
	model       string
	parentID    string
	items       []responseStateItem
	operationID string
	createdAt   time.Time
}

func durableResponseWriteContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), 3*time.Second)
}

func parseResponsesLifecycle(payload map[string]json.RawMessage, cfg Config, stream bool) (responsesLifecycleRequest, error) {
	var lifecycle responsesLifecycleRequest
	for field, target := range map[string]*bool{"store": &lifecycle.store, "background": &lifecycle.background} {
		if raw, ok := payload[field]; ok {
			if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, target) != nil {
				return lifecycle, errResponseResourceInvalid
			}
		}
	}
	if raw, ok := payload["previous_response_id"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if json.Unmarshal(raw, &lifecycle.previousID) != nil || !validIdentifier(lifecycle.previousID, 128) {
			return lifecycle, errResponseResourceInvalid
		}
	}
	if raw, ok := payload["conversation"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return lifecycle, errResponseResourceInvalid
	}
	if (lifecycle.store || lifecycle.previousID != "") && !cfg.ResponsesStatefulResources {
		return lifecycle, errResponseResourceForbidden
	}
	if lifecycle.background && (!cfg.ResponsesStatefulResources || !cfg.ResponsesBackgroundTasks) {
		return lifecycle, errResponseResourceForbidden
	}
	if stream && (lifecycle.store || lifecycle.background || lifecycle.previousID != "") {
		return lifecycle, errResponseResourceConflict
	}
	if lifecycle.background && !lifecycle.store {
		// Background results need their temporary encrypted body for polling.
		lifecycle.store = true
	}
	if lifecycle.background {
		allowed := map[string]bool{"model": true, "input": true, "instructions": true, "store": true, "background": true, "stream": true, "previous_response_id": true}
		for field := range payload {
			if !allowed[field] {
				return lifecycle, errResponseResourceConflict
			}
		}
	}
	// Native stateless Responses requests are transparent provider passthroughs,
	// so provider-managed tool declarations remain intact there. The service
	// still rejects them whenever it owns lifecycle state, and cross-protocol
	// routes independently reject tool kinds they cannot represent.
	if lifecycle.store || lifecycle.background || lifecycle.previousID != "" {
		if err := validateManagedResponseTools(payload["tools"]); err != nil {
			return lifecycle, err
		}
	}
	return lifecycle, nil
}

func validateManagedResponseTools(raw json.RawMessage) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil
	}
	var tools []struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(raw, &tools) != nil {
		return errResponseResourceInvalid
	}
	for _, tool := range tools {
		if tool.Type == "function" {
			continue
		}
		// D3's preview whitelist is intentionally empty; no configuration or
		// client field can turn a provider-managed tool into execution.
		return errResponseResourceForbidden
	}
	return nil
}

func prepareResponsePersistence(ctx context.Context, coordinator *responseResourceCoordinator, auth employeeAuth, model, operationID string, payload map[string]json.RawMessage, lifecycle responsesLifecycleRequest, now time.Time) (*responsePersistencePlan, error) {
	plan := &responsePersistencePlan{coordinator: coordinator, auth: auth, model: model, parentID: lifecycle.previousID, operationID: operationID, createdAt: now.UTC()}
	if lifecycle.previousID != "" {
		parent, err := coordinator.Get(ctx, auth, lifecycle.previousID, true)
		if err != nil {
			return nil, err
		}
		if parent.PublicModel != model || parent.Status != "completed" {
			return nil, errResponseResourceForbidden
		}
		plan.items = append(plan.items, parent.Items...)
	}
	current, err := responseInputStateItems(payload)
	if err != nil {
		return nil, err
	}
	plan.items = append(plan.items, current...)
	if lifecycle.previousID != "" {
		input := make([]json.RawMessage, 0, len(plan.items))
		for _, item := range plan.items {
			if item.Type != "instructions" {
				input = append(input, append(json.RawMessage(nil), item.Payload...))
			}
		}
		payload["input"], _ = json.Marshal(input)
		delete(payload, "previous_response_id")
	}
	payload["store"] = json.RawMessage("false")
	delete(payload, "background")
	return plan, nil
}

func responseInputStateItems(payload map[string]json.RawMessage) ([]responseStateItem, error) {
	items := make([]responseStateItem, 0, 8)
	if raw, ok := payload["instructions"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var instructions string
		if json.Unmarshal(raw, &instructions) != nil || instructions == "" {
			return nil, errResponseResourceInvalid
		}
		items = append(items, responseStateItem{Type: "instructions", Payload: append([]byte(nil), raw...)})
	}
	raw, ok := payload["input"]
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, errResponseResourceInvalid
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		message, _ := json.Marshal(map[string]any{"type": "message", "role": "user", "content": text})
		return append(items, responseStateItem{Type: "message", Payload: message}), nil
	}
	var input []json.RawMessage
	if json.Unmarshal(raw, &input) != nil || len(input) == 0 {
		return nil, errResponseResourceInvalid
	}
	for _, value := range input {
		item, err := validatedResponseStateItem(value, false)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func validatedResponseStateItem(raw json.RawMessage, output bool) (responseStateItem, error) {
	var head struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments *string         `json:"arguments"`
		Output    *string         `json:"output"`
		Content   json.RawMessage `json:"content"`
	}
	if json.Unmarshal(raw, &head) != nil {
		return responseStateItem{}, errResponseResourceInvalid
	}
	switch head.Type {
	case "message":
		if output && head.Role != "assistant" || !output && head.Role != "user" && head.Role != "assistant" && head.Role != "system" {
			return responseStateItem{}, errResponseResourceInvalid
		}
		if !validStoredTextContent(head.Content) {
			return responseStateItem{}, errResponseResourceInvalid
		}
	case "function_call":
		if head.CallID == "" || head.Name == "" || head.Arguments == nil {
			return responseStateItem{}, errResponseResourceInvalid
		}
	case "function_call_output":
		if output || head.CallID == "" || head.Output == nil {
			return responseStateItem{}, errResponseResourceInvalid
		}
	default:
		return responseStateItem{}, errResponseResourceInvalid
	}
	return responseStateItem{Type: head.Type, Payload: append([]byte(nil), raw...)}, nil
}

func validStoredTextContent(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return true
	}
	var parts []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if (part.Type != "input_text" && part.Type != "output_text") || part.Text == nil {
			return false
		}
	}
	return true
}

func (p *responsePersistencePlan) persistCompleted(ctx context.Context, providerKind string, body []byte) ([]byte, error) {
	if p == nil || p.coordinator == nil {
		return nil, errResponseResourceUnavailable
	}
	var envelope struct {
		Output []json.RawMessage `json:"output"`
	}
	if json.Unmarshal(body, &envelope) != nil {
		return nil, errResponseResourceInvalid
	}
	items := append([]responseStateItem(nil), p.items...)
	for _, raw := range envelope.Output {
		item, err := validatedResponseStateItem(raw, true)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	terminal := time.Now().UTC()
	if terminal.Before(p.createdAt) {
		terminal = p.createdAt
	}
	view, err := p.coordinator.Create(ctx, responseResourceCreateInput{
		OperationID: p.operationID, EmployeeID: p.auth.EmployeeID, KeyID: p.auth.KeyID,
		PublicModel: p.model, ParentResponseID: p.parentID, ProviderKind: providerKind,
		SourceAddr: p.auth.SourceAddr, PolicyRevision: p.auth.Policy.Revision,
		StoreBody: true, Items: items, CreatedAt: p.createdAt, TerminalAt: &terminal,
	})
	if err != nil {
		return nil, err
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(body, &response) != nil {
		return nil, errResponseResourceInvalid
	}
	response["id"], _ = json.Marshal(view.ID)
	response["store"] = json.RawMessage("true")
	if p.parentID != "" {
		response["previous_response_id"], _ = json.Marshal(p.parentID)
	}
	stored, err := json.Marshal(response)
	if err != nil || !json.Valid(stored) {
		return nil, errors.New("marshal stored response")
	}
	return stored, nil
}
