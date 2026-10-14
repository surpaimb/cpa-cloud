package service

import (
	"net/http"
	"sort"

	"cpacloud.local/server/internal/keypolicy"
)

type clientConfigurationProtocol struct {
	Protocol      string   `json:"protocol"`
	Candidates    []string `json:"candidates"`
	DefaultModel  string   `json:"default_model,omitempty"`
	DefaultSource string   `json:"default_source,omitempty"`
}

type clientConfigurationResponse struct {
	SchemaVersion int                           `json:"schema_version"`
	Protocols     []clientConfigurationProtocol `json:"protocols"`
}

// clientConfiguration exposes only the protocol facts needed by the optional
// local employee config helper. It deliberately does not expose routes,
// providers, policy structure, employee identity, or upstream credentials.
func (a *App) clientConfiguration(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		writeModelError(w, http.StatusBadRequest, "invalid_request", "Invalid request.", requestID(r.Context()))
		return
	}
	auth, ok := a.authenticateEmployee(w, r)
	if !ok {
		return
	}
	auth, failure := a.authorizeKeySource(auth, r)
	if failure != nil {
		writeModelError(w, failure.status, failure.code, failure.message, requestID(r.Context()))
		return
	}

	protocols := []keypolicy.ClientProtocol{
		keypolicy.ProtocolAnthropicMessages,
		keypolicy.ProtocolOpenAIResponses,
		keypolicy.ProtocolGeminiGenerate,
		keypolicy.ProtocolOpenAIChat,
	}
	response := clientConfigurationResponse{SchemaVersion: 1, Protocols: make([]clientConfigurationProtocol, 0, len(protocols))}
	for _, protocol := range protocols {
		models, err := a.clientConfigurationModels(r, auth, protocol)
		if err != nil {
			writeModelError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.", requestID(r.Context()))
			return
		}
		item := clientConfigurationProtocol{Protocol: string(protocol), Candidates: models}
		if len(models) != 0 {
			item.DefaultModel = models[0]
			item.DefaultSource = "public_id_ascending"
		}
		response.Protocols = append(response.Protocols, item)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

func (a *App) clientConfigurationModels(r *http.Request, auth employeeAuth, protocol keypolicy.ClientProtocol) ([]string, error) {
	if !keyPolicyAllowsProtocol(auth.Policy, protocol) {
		return []string{}, nil
	}
	wire := string(protocol)
	geminiOnly := protocol == keypolicy.ProtocolGeminiGenerate
	query := `SELECT DISTINCT m.id FROM models m JOIN upstreams u ON u.id=m.upstream_id
		WHERE m.enabled=1 AND m.archived=0 AND m.model_kind='generation' AND ` + a.availableModelRouteSQL(geminiOnly) + `
		AND ((NOT EXISTS(SELECT 1 FROM model_account_pool_configs pc WHERE pc.model_id=m.id)
			AND m.wire_protocol=? AND u.enabled=1 AND u.archived=0)
		OR EXISTS(SELECT 1 FROM model_account_pool_routes pr JOIN upstreams pu ON pu.id=pr.upstream_id
			WHERE pr.model_id=m.id AND pr.wire_protocol=? AND pu.enabled=1 AND pu.archived=0))`
	args := []any{wire, wire}
	if auth.Mode == "selected" {
		query += ` AND EXISTS(SELECT 1 FROM employee_models em WHERE em.employee_id=? AND em.model_id=m.id)`
		args = append(args, auth.EmployeeID)
	}
	if auth.Policy.ModelMode == keypolicy.ModeSelected {
		query += ` AND EXISTS(SELECT 1 FROM access_key_policy_models kpm WHERE kpm.key_id=? AND kpm.model_id=m.id)`
		args = append(args, auth.KeyID)
	}
	if auth.Policy.AccountGroupMode == keypolicy.ModeSelected {
		query += ` AND ` + a.keyAccountGroupModelFilterSQL("m", geminiOnly)
		args = append(args, auth.KeyID)
	}
	query += ` ORDER BY m.id`
	rows, err := a.store.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	models := make([]string, 0)
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		if keypolicy.Allows(auth.Policy, protocol, model) {
			models = append(models, model)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Strings(models)
	return models, nil
}
