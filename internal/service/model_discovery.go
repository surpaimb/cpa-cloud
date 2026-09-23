package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	modelDiscoveryTimeout  = 10 * time.Second
	modelDiscoveryMaxBody  = 1 << 20
	modelDiscoveryMaxItems = 1000
)

type discoveredModel struct {
	ID string `json:"id"`
}

func (a *App) discoverUpstreamModels(w http.ResponseWriter, r *http.Request, _ adminSession) {
	id := r.PathValue("id")
	var endpoint, providerKind string
	var enabled int
	var keyVersion int
	var ciphertext []byte
	a.admission.RLock()
	err := a.store.db.QueryRowContext(r.Context(), `SELECT endpoint,enabled,provider_kind,key_version,credential_ciphertext FROM upstreams WHERE id=?`, id).
		Scan(&endpoint, &enabled, &providerKind, &keyVersion, &ciphertext)
	a.admission.RUnlock()
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Upstream was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if providerKind == codexMembershipProvider {
		a.discoverCodexUpstreamModels(w, r, id)
		return
	}
	if enabled == 0 {
		writeAdminError(w, http.StatusConflict, "upstream_disabled", "Upstream is disabled.")
		return
	}
	if providerKind == geminiAPIKeyProvider {
		a.discoverGeminiUpstreamModels(w, r, id, endpoint, keyVersion, ciphertext)
		return
	}

	if (providerKind != "openai-compatible" && providerKind != anthropicAPIKeyProvider) || keyVersion != 1 {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	credential, err := a.secrets.decryptCredential(id, ciphertext)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}

	discoveryContext, cancel := context.WithTimeout(r.Context(), modelDiscoveryTimeout)
	defer cancel()
	validatedEndpoint, err := validateEndpoint(discoveryContext, endpoint, a.cfg.AllowLoopbackUpstream)
	if err != nil {
		writeModelDiscoveryFailure(w, r)
		return
	}
	target, err := upstreamModelsURL(validatedEndpoint)
	if err != nil {
		writeModelDiscoveryFailure(w, r)
		return
	}
	upstreamRequest, err := http.NewRequestWithContext(discoveryContext, http.MethodGet, target, nil)
	if err != nil {
		writeModelDiscoveryFailure(w, r)
		return
	}
	upstreamRequest.Header.Set("Authorization", "Bearer "+credential)
	if providerKind == anthropicAPIKeyProvider {
		upstreamRequest.Header.Set("Anthropic-Version", "2023-06-01")
	}
	upstreamRequest.Header.Set("Accept", "application/json")

	response, err := a.http.Do(upstreamRequest)
	if err != nil {
		if errors.Is(discoveryContext.Err(), context.DeadlineExceeded) {
			writeModelDiscoveryError(w, r, http.StatusGatewayTimeout, "model_discovery_timeout", "Upstream model discovery timed out.")
		} else {
			writeModelDiscoveryFailure(w, r)
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		switch response.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			writeModelDiscoveryError(w, r, http.StatusBadGateway, "upstream_authentication_failed", "Upstream authentication failed.")
		case http.StatusNotFound, http.StatusMethodNotAllowed:
			writeModelDiscoveryError(w, r, http.StatusBadGateway, "model_discovery_unsupported", "Upstream model discovery is not supported.")
		case http.StatusTooManyRequests:
			if retry := safeRetryAfter(response.Header.Get("Retry-After")); retry != "" {
				w.Header().Set("Retry-After", retry)
			}
			writeModelDiscoveryError(w, r, http.StatusTooManyRequests, "upstream_rate_limited", "Upstream rate limit was reached.")
		default:
			writeModelDiscoveryFailure(w, r)
		}
		return
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, modelDiscoveryMaxBody+1))
	if err != nil {
		if errors.Is(discoveryContext.Err(), context.DeadlineExceeded) {
			writeModelDiscoveryError(w, r, http.StatusGatewayTimeout, "model_discovery_timeout", "Upstream model discovery timed out.")
		} else {
			writeModelDiscoveryFailure(w, r)
		}
		return
	}
	if len(body) > modelDiscoveryMaxBody {
		writeModelDiscoveryError(w, r, http.StatusBadGateway, "invalid_model_response", "Upstream returned an invalid model list.")
		return
	}
	items, err := parseDiscoveredModels(body)
	if err != nil {
		writeModelDiscoveryError(w, r, http.StatusBadGateway, "invalid_model_response", "Upstream returned an invalid model list.")
		return
	}
	if r.Context().Err() != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func writeModelDiscoveryFailure(w http.ResponseWriter, r *http.Request) {
	writeModelDiscoveryError(w, r, http.StatusBadGateway, "model_discovery_failed", "Unable to discover upstream models.")
}

func writeModelDiscoveryError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	if r.Context().Err() != nil {
		return
	}
	writeAdminError(w, status, code, message)
}

func upstreamModelsURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/v1") {
		path += "/models"
	} else {
		path += "/v1/models"
	}
	u.Path = path
	u.RawPath = ""
	return u.String(), nil
}

func parseDiscoveredModels(body []byte) ([]discoveredModel, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, errors.New("invalid model response")
	}
	rawData, ok := envelope["data"]
	if !ok || len(bytes.TrimSpace(rawData)) == 0 || bytes.TrimSpace(rawData)[0] != '[' {
		return nil, errors.New("invalid model response")
	}
	var records []struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(rawData, &records); err != nil || len(records) > modelDiscoveryMaxItems {
		return nil, errors.New("invalid model response")
	}

	seen := make(map[string]struct{}, len(records))
	ids := make([]string, 0, len(records))
	for _, record := range records {
		var id string
		if len(record.ID) == 0 || json.Unmarshal(record.ID, &id) != nil || !validText(id, 1, 256) {
			return nil, errors.New("invalid model response")
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]discoveredModel, 0, len(ids))
	for _, id := range ids {
		items = append(items, discoveredModel{ID: id})
	}
	return items, nil
}
