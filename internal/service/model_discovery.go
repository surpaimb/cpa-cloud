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

type catalogRunFailure struct {
	result     string
	retryAfter string
	local      bool
}

func (a *App) discoverUpstreamModels(w http.ResponseWriter, r *http.Request, _ adminSession) {
	id := r.PathValue("id")
	var endpoint, providerKind string
	var enabled int
	var keyVersion int
	var revision int64
	var ciphertext []byte
	a.admission.RLock()
	err := a.store.db.QueryRowContext(r.Context(), `SELECT endpoint,enabled,provider_kind,key_version,credential_ciphertext,revision FROM upstreams WHERE id=?`, id).
		Scan(&endpoint, &enabled, &providerKind, &keyVersion, &ciphertext, &revision)
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
		a.discoverGeminiUpstreamModels(w, r, id, endpoint, keyVersion, ciphertext, revision)
		return
	}

	discoveryContext, cancel := context.WithTimeout(r.Context(), modelDiscoveryTimeout)
	defer cancel()
	items, failure := a.runAPIKeyModelCatalog(discoveryContext, id, providerKind, endpoint, keyVersion, ciphertext, revision)
	if failure != nil {
		writeCatalogRunFailure(w, r, failure)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func writeCatalogRunFailure(w http.ResponseWriter, r *http.Request, failure *catalogRunFailure) {
	if failure.local {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	switch failure.result {
	case "cancelled":
		return
	case "timeout":
		writeModelDiscoveryError(w, r, http.StatusGatewayTimeout, "model_discovery_timeout", "Upstream model discovery timed out.")
	case "authentication_failed":
		writeModelDiscoveryError(w, r, http.StatusBadGateway, "upstream_authentication_failed", "Upstream authentication failed.")
	case "rate_limited":
		if failure.retryAfter != "" {
			w.Header().Set("Retry-After", failure.retryAfter)
		}
		writeModelDiscoveryError(w, r, http.StatusTooManyRequests, "upstream_rate_limited", "Upstream rate limit was reached.")
	case "unsupported":
		writeModelDiscoveryError(w, r, http.StatusBadGateway, "model_discovery_unsupported", "Upstream model discovery is not supported.")
	case "invalid_response":
		writeModelDiscoveryError(w, r, http.StatusBadGateway, "invalid_model_response", "Upstream returned an invalid model list.")
	default:
		writeModelDiscoveryFailure(w, r)
	}
}

func (a *App) runAPIKeyModelCatalog(ctx context.Context, upstreamID, provider, endpoint string, keyVersion int, ciphertext []byte, revision int64) ([]discoveredModel, *catalogRunFailure) {
	if (provider != "openai-compatible" && provider != anthropicAPIKeyProvider) || keyVersion != 1 {
		return nil, &catalogRunFailure{result: "configuration_changed", local: true}
	}
	credential, err := a.secrets.decryptCredential(upstreamID, ciphertext)
	if err != nil || credential == "" {
		return nil, &catalogRunFailure{result: "authentication_failed", local: true}
	}
	validatedEndpoint, err := validateEndpoint(ctx, endpoint, a.cfg.AllowLoopbackUpstream)
	if err != nil {
		return nil, &catalogRunFailure{result: "configuration_changed"}
	}
	target, err := upstreamModelsURL(validatedEndpoint)
	if err != nil {
		return nil, &catalogRunFailure{result: "configuration_changed"}
	}
	selected := route{AccountID: upstreamID, Endpoint: endpoint, ProviderKind: provider, Revision: revision}
	frozen, err := a.prepareRouteEgress(ctx, selected)
	if err != nil {
		return nil, &catalogRunFailure{result: "configuration_changed", local: true}
	}
	seenIDs := make(map[string]struct{})
	seenCursors := make(map[string]struct{})
	totalBytes := 0
	cursor := ""
	for page := 0; ; page++ {
		if page >= geminiDiscoveryMaxPages {
			return nil, &catalogRunFailure{result: "invalid_response"}
		}
		pageTarget := target
		if provider == anthropicAPIKeyProvider {
			pageTarget, err = anthropicModelsPageURL(target, cursor)
			if err != nil {
				return nil, &catalogRunFailure{result: "configuration_changed"}
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, pageTarget, nil)
		if err != nil {
			return nil, &catalogRunFailure{result: "configuration_changed"}
		}
		if provider == anthropicAPIKeyProvider {
			request.Header.Set("X-Api-Key", credential)
			request.Header.Set("Anthropic-Version", "2023-06-01")
		} else {
			request.Header.Set("Authorization", "Bearer "+credential)
		}
		request.Header.Set("Accept", "application/json")
		if !a.catalogEgressCurrent(ctx, selected, frozen) {
			return nil, &catalogRunFailure{result: "configuration_changed", local: true}
		}
		response, err := frozen.client.Do(request)
		if err != nil {
			return nil, catalogContextFailure(ctx)
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			failure := catalogHTTPFailure(response.StatusCode, response.Header.Get("Retry-After"))
			_ = response.Body.Close()
			return nil, failure
		}
		remaining := modelDiscoveryMaxBody - totalBytes
		body, failure := readModelCatalogBody(ctx, response, remaining)
		if failure != nil {
			return nil, failure
		}
		totalBytes += len(body)
		pageItems, hasMore, lastID, err := parseDiscoveredModelPage(body, provider == anthropicAPIKeyProvider)
		if err != nil {
			return nil, &catalogRunFailure{result: "invalid_response"}
		}
		for _, item := range pageItems {
			seenIDs[item.ID] = struct{}{}
			if len(seenIDs) > modelDiscoveryMaxItems {
				return nil, &catalogRunFailure{result: "invalid_response"}
			}
		}
		if provider != anthropicAPIKeyProvider || !hasMore {
			break
		}
		if !validGeminiPageToken(lastID) {
			return nil, &catalogRunFailure{result: "invalid_response"}
		}
		if _, duplicate := seenCursors[lastID]; duplicate {
			return nil, &catalogRunFailure{result: "invalid_response"}
		}
		seenCursors[lastID] = struct{}{}
		cursor = lastID
	}
	ids := make([]string, 0, len(seenIDs))
	for id := range seenIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	items := make([]discoveredModel, 0, len(ids))
	for _, id := range ids {
		items = append(items, discoveredModel{ID: id})
	}
	return items, nil
}

func anthropicModelsPageURL(target, cursor string) (string, error) {
	u, err := url.Parse(target)
	if err != nil {
		return "", err
	}
	query := u.Query()
	query.Set("limit", "1000")
	if cursor != "" {
		query.Set("after_id", cursor)
	}
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func catalogContextFailure(ctx context.Context) *catalogRunFailure {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &catalogRunFailure{result: "timeout"}
	}
	if ctx.Err() != nil {
		return &catalogRunFailure{result: "cancelled"}
	}
	return &catalogRunFailure{result: "internal_failure"}
}

func catalogHTTPFailure(status int, retryAfter string) *catalogRunFailure {
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return &catalogRunFailure{result: "authentication_failed"}
	case http.StatusTooManyRequests:
		return &catalogRunFailure{result: "rate_limited", retryAfter: safeRetryAfter(retryAfter)}
	case http.StatusNotFound, http.StatusMethodNotAllowed:
		return &catalogRunFailure{result: "unsupported"}
	default:
		return &catalogRunFailure{result: "internal_failure"}
	}
}

func readModelCatalogBody(ctx context.Context, response *http.Response, remaining int) ([]byte, *catalogRunFailure) {
	body, err := io.ReadAll(io.LimitReader(response.Body, int64(remaining)+1))
	closeErr := response.Body.Close()
	if err != nil || closeErr != nil {
		return nil, catalogContextFailure(ctx)
	}
	if len(body) > remaining {
		return nil, &catalogRunFailure{result: "invalid_response"}
	}
	return body, nil
}

func parseDiscoveredModelPage(body []byte, requirePagination bool) ([]discoveredModel, bool, string, error) {
	items, err := parseDiscoveredModels(body)
	if err != nil {
		return nil, false, "", err
	}
	if !requirePagination {
		return items, false, "", nil
	}
	var envelope struct {
		HasMore *bool  `json:"has_more"`
		LastID  string `json:"last_id"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.HasMore == nil {
		return nil, false, "", errors.New("invalid model response")
	}
	if *envelope.HasMore && envelope.LastID == "" {
		return nil, false, "", errors.New("invalid model response")
	}
	return items, *envelope.HasMore, envelope.LastID, nil
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
