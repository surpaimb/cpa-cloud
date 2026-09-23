package service

import (
	"context"
	"encoding/json"
	"errors"
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

func (a *App) discoverGeminiUpstreamModels(w http.ResponseWriter, r *http.Request, upstreamID, endpoint string, keyVersion int, ciphertext []byte, revision int64) {
	discoveryContext, cancel := context.WithTimeout(r.Context(), modelDiscoveryTimeout)
	defer cancel()
	items, failure := a.runGeminiModelCatalog(discoveryContext, upstreamID, endpoint, keyVersion, ciphertext, revision)
	if failure != nil {
		writeCatalogRunFailure(w, r, failure)
		return
	}
	if r.Context().Err() == nil {
		writeJSON(w, http.StatusOK, map[string]any{"items": items})
	}
}

func (a *App) runGeminiModelCatalog(ctx context.Context, upstreamID, endpoint string, keyVersion int, ciphertext []byte, revision int64) ([]discoveredModel, *catalogRunFailure) {
	if keyVersion != 2 {
		return nil, &catalogRunFailure{result: "configuration_changed", local: true}
	}
	credential, err := a.secrets.decryptGeminiAPIKey(upstreamID, ciphertext)
	if err != nil || credential == "" {
		return nil, &catalogRunFailure{result: "authentication_failed", local: true}
	}
	validated, err := validateGeminiEndpoint(ctx, endpoint, a.cfg.AllowLoopbackUpstream)
	if err != nil {
		return nil, &catalogRunFailure{result: "configuration_changed"}
	}
	selected := route{AccountID: upstreamID, Endpoint: endpoint, ProviderKind: geminiAPIKeyProvider, Revision: revision}
	frozen, err := a.prepareRouteEgress(ctx, selected)
	if err != nil {
		return nil, &catalogRunFailure{result: "configuration_changed", local: true}
	}
	seenIDs := make(map[string]struct{})
	seenTokens := make(map[string]struct{})
	totalBytes := 0
	pageToken := ""
	for page := 0; ; page++ {
		if page >= geminiDiscoveryMaxPages {
			return nil, &catalogRunFailure{result: "invalid_response"}
		}
		target, err := geminiModelsURL(validated, pageToken)
		if err != nil {
			return nil, &catalogRunFailure{result: "configuration_changed"}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, &catalogRunFailure{result: "configuration_changed"}
		}
		req.Header.Set("x-goog-api-key", credential)
		req.Header.Set("Accept", "application/json")
		if !a.catalogEgressCurrent(ctx, selected, frozen) {
			return nil, &catalogRunFailure{result: "configuration_changed", local: true}
		}
		response, err := frozen.client.Do(req)
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
		pageIDs, nextToken, err := parseGeminiDiscoveryPage(body)
		if err != nil {
			return nil, &catalogRunFailure{result: "invalid_response"}
		}
		for _, id := range pageIDs {
			seenIDs[id] = struct{}{}
			if len(seenIDs) > modelDiscoveryMaxItems {
				return nil, &catalogRunFailure{result: "invalid_response"}
			}
		}
		if nextToken == "" {
			break
		}
		if !validGeminiPageToken(nextToken) {
			return nil, &catalogRunFailure{result: "invalid_response"}
		}
		if _, duplicate := seenTokens[nextToken]; duplicate {
			return nil, &catalogRunFailure{result: "invalid_response"}
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
	return items, nil
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
