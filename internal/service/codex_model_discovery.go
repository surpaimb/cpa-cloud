package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"cpacloud.local/server/internal/membership"
)

const codexCatalogTTL = time.Minute

type codexCatalogLister interface {
	List(context.Context, *membership.CodexAuthCredential) (membership.CodexModelCatalog, error)
}

type codexCatalogCacheEntry struct {
	revision int64
	expires  time.Time
	body     []byte
}

type codexCatalogItem struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
	// These describe the upstream, not CPA Cloud's protocol implementation.
	Capabilities codexCatalogCapabilities `json:"upstream_capabilities"`
}

type codexCatalogCapabilities struct {
	SupportedInAPI  *bool                            `json:"supported_in_api,omitempty"`
	InputModalities []string                         `json:"input_modalities,omitempty"`
	ContextWindow   *int64                           `json:"context_window,omitempty"`
	ReasoningLevels []membership.CodexReasoningLevel `json:"reasoning_levels,omitempty"`
	SearchTool      *bool                            `json:"search_tool,omitempty"`
	Verbosity       *bool                            `json:"verbosity,omitempty"`
}

func (a *App) discoverCodexUpstreamModels(w http.ResponseWriter, r *http.Request, id string) {
	if !a.cfg.ExperimentalCodexMembership {
		writeAdminError(w, http.StatusForbidden, "feature_disabled", "Codex membership support is disabled.")
		return
	}
	var selected route
	var enabled int
	err := a.store.db.QueryRowContext(r.Context(), `SELECT id,provider_kind,enabled,revision,credential_ciphertext,key_version,credential_state FROM upstreams WHERE id=?`, id).
		Scan(&selected.AccountID, &selected.ProviderKind, &enabled, &selected.Revision, &selected.Ciphertext, &selected.KeyVersion, &selected.CredentialState)
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Upstream was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if enabled == 0 {
		writeAdminError(w, http.StatusConflict, "upstream_disabled", "Upstream is disabled.")
		return
	}
	if selected.ProviderKind != codexMembershipProvider || selected.KeyVersion != 2 || selected.CredentialState.String == codexStateReauth {
		writeAdminError(w, http.StatusConflict, "upstream_reauthentication_required", "The upstream credential must be authorized or imported again.")
		return
	}
	selected, credential, credentialErr := a.acquireCodexCredential(r.Context(), selected)
	if credentialErr != nil {
		if r.Context().Err() != nil {
			return
		}
		if credentialErr.Code == membership.CodexErrorReauthentication {
			writeAdminError(w, http.StatusConflict, "upstream_reauthentication_required", "The upstream credential must be authorized or imported again.")
		} else {
			writeModelDiscoveryFailure(w, r)
		}
		return
	}
	defer credential.Destroy()
	client, cached := a.codexCatalogClientAndCache(selected)
	if client == nil {
		writeModelDiscoveryFailure(w, r)
		return
	}
	if cached != nil {
		if !a.validateCatalogRevision(w, r, selected) {
			return
		}
		writeCatalogJSON(w, cached)
		return
	}
	catalog, err := client.List(r.Context(), credential)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		status, code, message := http.StatusBadGateway, "model_discovery_failed", "Upstream model discovery failed."
		switch kind, _ := membership.CodexModelsErrorCodeOf(err); kind {
		case membership.CodexModelsUnauthorized:
			if err := a.markCodexReauthentication(id, selected.Revision); err != nil {
				writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
				return
			}
			code, message = "upstream_authentication_failed", "Upstream authentication failed."
		case membership.CodexModelsForbidden:
			code, message = "upstream_access_denied", "Upstream model catalog access was denied."
		case membership.CodexModelsRateLimited:
			status, code, message = http.StatusTooManyRequests, "upstream_rate_limited", "Upstream rate limit was reached."
		case membership.CodexModelsTimeout:
			status, code, message = http.StatusGatewayTimeout, "model_discovery_timeout", "Upstream model discovery timed out."
		}
		writeModelDiscoveryError(w, r, status, code, message)
		return
	}
	items := make([]codexCatalogItem, 0, len(catalog.Models))
	seen := map[string]bool{}
	for _, model := range catalog.Models {
		if !validIdentifier(model.ID, 256) || len(model.DisplayName) > 1024 {
			writeModelDiscoveryError(w, r, http.StatusBadGateway, "invalid_model_response", "Upstream returned an invalid model list.")
			return
		}
		if model.Visibility != nil && *model.Visibility != "list" {
			continue
		}
		if seen[model.ID] {
			continue
		}
		seen[model.ID] = true
		items = append(items, codexCatalogItem{ID: model.ID, DisplayName: model.DisplayName, Capabilities: codexCatalogCapabilities{
			SupportedInAPI: model.SupportedInAPI, InputModalities: model.InputModalities, ContextWindow: model.ContextWindow,
			ReasoningLevels: model.SupportedReasoningLevels, SearchTool: model.SupportsSearchTool, Verbosity: model.SupportsVerbosity,
		}})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	body, err := json.Marshal(map[string]any{"items": items})
	if err != nil {
		writeModelDiscoveryFailure(w, r)
		return
	}
	// Re-import, disabling, or replacement while discovering invalidates this result.
	if !a.validateCatalogRevision(w, r, selected) {
		return
	}
	a.catalogMu.Lock()
	if a.catalogs == nil {
		a.catalogs = make(map[string]codexCatalogCacheEntry)
	}
	if len(a.catalogs) >= 128 {
		for key, entry := range a.catalogs {
			if !time.Now().Before(entry.expires) {
				delete(a.catalogs, key)
			}
		}
	}
	if len(a.catalogs) < 128 {
		a.catalogs[id] = codexCatalogCacheEntry{revision: selected.Revision, expires: time.Now().Add(codexCatalogTTL), body: body}
	}
	a.catalogMu.Unlock()
	writeCatalogJSON(w, body)
}

func (a *App) validateCatalogRevision(w http.ResponseWriter, r *http.Request, selected route) bool {
	var current int
	if err := a.store.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM upstreams WHERE id=? AND revision=? AND enabled=1 AND provider_kind=? AND credential_state<>?`, selected.AccountID, selected.Revision, codexMembershipProvider, codexStateReauth).Scan(&current); err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return false
	}
	if current != 1 {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The upstream changed while discovering models.")
		return false
	}
	return true
}

func (a *App) codexCatalogClientAndCache(selected route) (codexCatalogLister, []byte) {
	a.catalogMu.Lock()
	defer a.catalogMu.Unlock()
	if a.codexCatalog == nil {
		version := strings.SplitN(strings.TrimPrefix(a.cfg.Version, "v"), "-", 2)[0]
		client, err := membership.NewCodexModelsClient(version)
		if err != nil {
			client, _ = membership.NewCodexModelsClient("0.0.0")
		}
		a.codexCatalog = client
	}
	if cached, ok := a.catalogs[selected.AccountID]; ok && cached.revision == selected.Revision && time.Now().Before(cached.expires) {
		return a.codexCatalog, cached.body
	}
	return a.codexCatalog, nil
}

func writeCatalogJSON(w http.ResponseWriter, body []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
