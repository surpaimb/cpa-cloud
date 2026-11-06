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
	routeSQL, routeArgs := a.clientConfigurationRouteWitnessSQL("m", "u", wire, geminiOnly, auth.Policy.AccountGroupMode == keypolicy.ModeSelected, auth.KeyID)
	query := `SELECT DISTINCT m.id FROM models m JOIN upstreams u ON u.id=m.upstream_id
		WHERE m.enabled=1 AND m.archived=0 AND m.model_kind='generation' AND ` + routeSQL
	args := routeArgs
	if auth.Mode == "selected" {
		query += ` AND EXISTS(SELECT 1 FROM employee_models em WHERE em.employee_id=? AND em.model_id=m.id)`
		args = append(args, auth.EmployeeID)
	}
	if auth.Policy.ModelMode == keypolicy.ModeSelected {
		query += ` AND EXISTS(SELECT 1 FROM access_key_policy_models kpm WHERE kpm.key_id=? AND kpm.model_id=m.id)`
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

// clientConfigurationRouteWitnessSQL requires one route row to satisfy every
// route-local condition for the requested client protocol and requires all
// active rows in the pool to agree with its wire and provider. The runtime
// rejects a whole pool before group filtering when either value is mixed, so a
// merely eligible row is not enough to advertise a dispatchable model.
func (a *App) clientConfigurationRouteWitnessSQL(modelAlias, upstreamAlias, wire string, geminiOnly, selectedGroup bool, keyID string) (string, []any) {
	providerWireSQL := func(alias string) string {
		switch wire {
		case string(keypolicy.ProtocolOpenAIChat):
			return alias + ".provider_kind='openai-compatible'"
		case string(keypolicy.ProtocolOpenAIResponses):
			return alias + ".provider_kind IN ('openai-compatible','codex-membership')"
		case string(keypolicy.ProtocolAnthropicMessages):
			return alias + ".provider_kind='anthropic-api-key'"
		case string(keypolicy.ProtocolGeminiGenerate):
			return alias + ".provider_kind='gemini-api-key'"
		default:
			return "0"
		}
	}
	if a.accountPool == nil {
		if selectedGroup {
			return "0", nil
		}
		return "(" + modelAlias + ".wire_protocol=? AND " + providerWireSQL(upstreamAlias) + " AND " + a.eligibleUpstreamSQL(upstreamAlias, geminiOnly) + ")", []any{wire}
	}
	if selectedGroup {
		return `EXISTS(
			SELECT 1
			FROM model_account_pool_routes discovery_route
			JOIN upstreams discovery_upstream ON discovery_upstream.id=discovery_route.upstream_id
			JOIN account_channels discovery_channel ON discovery_channel.id=discovery_route.channel_id
			JOIN access_key_policy_account_group_members discovery_group
			  ON discovery_group.account_group_id=discovery_channel.group_id
			WHERE discovery_route.model_id=` + modelAlias + `.id
			  AND discovery_route.wire_protocol=?
			  AND discovery_group.key_id=?
			  AND ` + providerWireSQL("discovery_upstream") + `
			  AND ` + a.eligibleUpstreamSQL("discovery_upstream", geminiOnly) + `
			  AND NOT EXISTS(SELECT 1 FROM account_recovery_states discovery_recovery WHERE discovery_recovery.account_id=discovery_upstream.id)
			  AND NOT EXISTS(
				SELECT 1
				FROM model_account_pool_routes inconsistent_route
				JOIN upstreams inconsistent_upstream ON inconsistent_upstream.id=inconsistent_route.upstream_id
				WHERE inconsistent_route.model_id=` + modelAlias + `.id
				  AND inconsistent_upstream.archived=0
				  AND (inconsistent_route.wire_protocol<>? OR inconsistent_upstream.provider_kind<>discovery_upstream.provider_kind)
			  )
		)`, []any{wire, keyID, wire}
	}
	return `(
		(NOT EXISTS(SELECT 1 FROM model_account_pool_configs discovery_pool WHERE discovery_pool.model_id=` + modelAlias + `.id)
		 AND ` + modelAlias + `.wire_protocol=?
		 AND ` + providerWireSQL(upstreamAlias) + `
		 AND ` + a.eligibleUpstreamSQL(upstreamAlias, geminiOnly) + `)
		OR EXISTS(
			SELECT 1
			FROM model_account_pool_routes discovery_route
			JOIN upstreams discovery_upstream ON discovery_upstream.id=discovery_route.upstream_id
			WHERE discovery_route.model_id=` + modelAlias + `.id
			  AND discovery_route.wire_protocol=?
			  AND ` + providerWireSQL("discovery_upstream") + `
			  AND ` + a.eligibleUpstreamSQL("discovery_upstream", geminiOnly) + `
			  AND NOT EXISTS(SELECT 1 FROM account_recovery_states discovery_recovery WHERE discovery_recovery.account_id=discovery_upstream.id)
			  AND NOT EXISTS(
				SELECT 1
				FROM model_account_pool_routes inconsistent_route
				JOIN upstreams inconsistent_upstream ON inconsistent_upstream.id=inconsistent_route.upstream_id
				WHERE inconsistent_route.model_id=` + modelAlias + `.id
				  AND inconsistent_upstream.archived=0
				  AND (inconsistent_route.wire_protocol<>? OR inconsistent_upstream.provider_kind<>discovery_upstream.provider_kind)
			  )
		)
	)`, []any{wire, wire, wire}
}
