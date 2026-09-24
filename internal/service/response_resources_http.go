package service

// Employee-facing Responses resource endpoints. They deliberately expose no
// administrator body export and always re-run the owner/key/model checks in the
// coordinator before returning functional content.

import (
	"encoding/json"
	"errors"
	"net/http"
)

func (a *App) getResponseResource(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.ResponsesStatefulResources || a.responseResources == nil {
		writeModelError(w, http.StatusBadRequest, "unsupported_feature", "Stored Responses resources are disabled.", requestID(r.Context()))
		return
	}
	auth, ok := a.authenticateEmployee(w, r)
	if !ok {
		return
	}
	view, err := a.responseResources.Get(r.Context(), auth, r.PathValue("id"), true)
	if err != nil {
		writeResponseResourceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, responseResourcePayload(view))
}

func (a *App) cancelResponseResource(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.ResponsesStatefulResources || !a.cfg.ResponsesBackgroundTasks || a.responseResources == nil {
		writeModelError(w, http.StatusBadRequest, "unsupported_feature", "Background Responses tasks are disabled.", requestID(r.Context()))
		return
	}
	auth, ok := a.authenticateEmployee(w, r)
	if !ok {
		return
	}
	view, err := a.responseResources.Cancel(r.Context(), auth, r.PathValue("id"))
	if err != nil {
		writeResponseResourceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, responseResourcePayload(view))
}

func (a *App) deleteResponseResource(w http.ResponseWriter, r *http.Request) {
	if !a.cfg.ResponsesStatefulResources || a.responseResources == nil {
		writeModelError(w, http.StatusBadRequest, "unsupported_feature", "Stored Responses resources are disabled.", requestID(r.Context()))
		return
	}
	auth, ok := a.authenticateEmployee(w, r)
	if !ok {
		return
	}
	responseID := r.PathValue("id")
	view, err := a.responseResources.Get(r.Context(), auth, responseID, false)
	if err != nil {
		writeResponseResourceError(w, r, err)
		return
	}
	if view.Background && !responseTerminalStatus(view.Status) {
		view, err = a.responseResources.Cancel(r.Context(), auth, responseID)
		if err != nil {
			writeResponseResourceError(w, r, err)
			return
		}
		if !responseTerminalStatus(view.Status) {
			writeModelError(w, http.StatusConflict, "response_not_terminal", "The background response is still stopping.", requestID(r.Context()))
			return
		}
	}
	if err := a.responseResources.Delete(r.Context(), auth, responseID); err != nil {
		writeResponseResourceError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": responseID, "object": "response.deleted", "deleted": true})
}

func writeResponseResourceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errResponseResourceNotFound):
		writeModelError(w, http.StatusNotFound, "response_not_found", "Response not found.", requestID(r.Context()))
	case errors.Is(err, errResponseResourceForbidden):
		writeModelError(w, http.StatusForbidden, "permission_denied", "The response is not available to this key.", requestID(r.Context()))
	case errors.Is(err, errResponseResourceConflict):
		writeModelError(w, http.StatusConflict, "response_conflict", "The response state changed; retry the operation.", requestID(r.Context()))
	case errors.Is(err, errResponseResourceInvalid):
		writeModelError(w, http.StatusBadRequest, "invalid_request_error", "Invalid response resource request.", requestID(r.Context()))
	default:
		writeModelError(w, http.StatusServiceUnavailable, "storage_unavailable", "Response storage is temporarily unavailable.", requestID(r.Context()))
	}
}

func responseResourcePayload(view responseResourceView) map[string]any {
	output := make([]json.RawMessage, 0, len(view.Items))
	for _, item := range view.Items {
		if responseItemIsOutput(item) {
			output = append(output, append(json.RawMessage(nil), item.Payload...))
		}
	}
	payload := map[string]any{
		"id": view.ID, "object": "response", "created_at": view.CreatedAt.Unix(),
		"model": view.PublicModel, "status": view.Status, "background": view.Background,
		"store": view.StoreBody, "output": output,
	}
	if view.ParentResponseID != "" {
		payload["previous_response_id"] = view.ParentResponseID
	}
	return payload
}

func responseItemIsOutput(item responseStateItem) bool {
	if item.Type == "function_call" {
		return true
	}
	if item.Type != "message" {
		return false
	}
	var head struct {
		Role string `json:"role"`
	}
	return json.Unmarshal(item.Payload, &head) == nil && head.Role == "assistant"
}
