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
	"unicode"
)

const (
	geminiDiscoveryPageSize    = "1000"
	geminiDiscoveryMaxPages    = 1000
	geminiDiscoveryMaxTokenLen = 4096
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
	seenIDs := make(map[string]struct{})
	seenTokens := make(map[string]struct{})
	totalBytes := 0
	pageToken := ""
	for page := 0; ; page++ {
		if page >= geminiDiscoveryMaxPages {
			writeInvalidGeminiDiscovery(w, r)
			return
		}
		target, err := geminiModelsURL(validated, pageToken)
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
			} else if r.Context().Err() == nil {
				writeModelDiscoveryFailure(w, r)
			}
			return
		}
		remaining := modelDiscoveryMaxBody - totalBytes
		body, readErr := io.ReadAll(io.LimitReader(response.Body, int64(remaining)+1))
		closeErr := response.Body.Close()
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
		if readErr != nil || closeErr != nil || len(body) > remaining {
			writeInvalidGeminiDiscovery(w, r)
			return
		}
		totalBytes += len(body)
		pageIDs, nextToken, err := parseGeminiDiscoveryPage(body)
		if err != nil {
			writeInvalidGeminiDiscovery(w, r)
			return
		}
		for _, id := range pageIDs {
			seenIDs[id] = struct{}{}
			if len(seenIDs) > modelDiscoveryMaxItems {
				writeInvalidGeminiDiscovery(w, r)
				return
			}
		}
		if nextToken == "" {
			break
		}
		if !validGeminiPageToken(nextToken) {
			writeInvalidGeminiDiscovery(w, r)
			return
		}
		if _, duplicate := seenTokens[nextToken]; duplicate {
			writeInvalidGeminiDiscovery(w, r)
			return
		}
		seenTokens[nextToken] = struct{}{}
		pageToken = nextToken
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
	if r.Context().Err() == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func geminiModelsURL(endpoint, pageToken string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1beta/models"
	u.RawPath = ""
	q := u.Query()
	q.Set("pageSize", geminiDiscoveryPageSize)
	if pageToken != "" {
		q.Set("pageToken", pageToken)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func parseGeminiDiscoveryPage(body []byte) ([]string, string, error) {
	var envelope struct {
		Models []struct {
			Name                       string   `json:"name"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
		NextPageToken string `json:"nextPageToken"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Models) > modelDiscoveryMaxItems {
		return nil, "", errors.New("invalid Gemini model response")
	}
	seen := make(map[string]struct{}, len(envelope.Models))
	ids := make([]string, 0, len(envelope.Models))
	for _, model := range envelope.Models {
		if !containsString(model.SupportedGenerationMethods, "generateContent") {
			continue
		}
		id := strings.TrimPrefix(model.Name, "models/")
		if id == model.Name || !validIdentifier(id, 256) {
			return nil, "", errors.New("invalid Gemini model response")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, envelope.NextPageToken, nil
}

func validGeminiPageToken(token string) bool {
	if token == "" || len(token) > geminiDiscoveryMaxTokenLen || strings.TrimSpace(token) != token {
		return false
	}
	for _, character := range token {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func writeInvalidGeminiDiscovery(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() == nil {
		writeModelDiscoveryError(w, r, http.StatusBadGateway, "invalid_model_response", "Upstream returned an invalid model list.")
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
