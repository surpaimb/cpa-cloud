package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

func (a *App) discoverGeminiUpstreamModels(w http.ResponseWriter, r *http.Request, upstreamID, endpoint string, keyVersion int, ciphertext []byte) {
	if keyVersion != 2 {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	credential, err := a.secrets.decryptGeminiAPIKey(upstreamID, ciphertext)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	discoveryContext, cancel := context.WithTimeout(r.Context(), modelDiscoveryTimeout)
	defer cancel()
	validated, err := validateGeminiEndpoint(discoveryContext, endpoint, a.cfg.AllowLoopbackUpstream)
	if err != nil {
		writeModelDiscoveryFailure(w, r)
		return
	}
	target, err := geminiModelsURL(validated)
	if err != nil {
		writeModelDiscoveryFailure(w, r)
		return
	}
	req, err := http.NewRequestWithContext(discoveryContext, http.MethodGet, target, nil)
	if err != nil {
		writeModelDiscoveryFailure(w, r)
		return
	}
	req.Header.Set("x-goog-api-key", credential)
	req.Header.Set("Accept", "application/json")
	response, err := a.http.Do(req)
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
	if err != nil || len(body) > modelDiscoveryMaxBody {
		writeModelDiscoveryError(w, r, http.StatusBadGateway, "invalid_model_response", "Upstream returned an invalid model list.")
		return
	}
	items, err := parseGeminiDiscoveredModels(body)
	if err != nil {
		writeModelDiscoveryError(w, r, http.StatusBadGateway, "invalid_model_response", "Upstream returned an invalid model list.")
		return
	}
	if r.Context().Err() == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func geminiModelsURL(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1beta/models"
	u.RawPath = ""
	q := u.Query()
	q.Set("pageSize", "1000")
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func parseGeminiDiscoveredModels(body []byte) ([]discoveredModel, error) {
	var envelope struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Models) > modelDiscoveryMaxItems || envelope.NextPageToken != "" {
		return nil, errors.New("invalid Gemini model response")
	}
	seen := make(map[string]struct{}, len(envelope.Models))
	ids := make([]string, 0, len(envelope.Models))
	for _, model := range envelope.Models {
		if !containsString(model.SupportedGenerationMethods, "generateContent") {
			continue
		}
		id := strings.TrimPrefix(model.Name, "models/")
		if id == model.Name || !validIdentifier(id, 256) {
			return nil, errors.New("invalid Gemini model response")
		}
		if _, ok := seen[id]; ok {
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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
