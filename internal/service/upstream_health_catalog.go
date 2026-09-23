package service

import (
	"context"
	"sort"

	"cpacloud.local/server/internal/membership"
)

func (a *App) runAPIKeyCatalogTest(ctx context.Context, snapshot upstreamHealthSnapshot) string {
	_, failure := a.runAPIKeyModelCatalog(ctx, snapshot.id, snapshot.provider, snapshot.endpoint, snapshot.keyVersion, snapshot.ciphertext)
	if failure != nil {
		return failure.result
	}
	return "catalog_ok"
}

func (a *App) runGeminiCatalogTest(ctx context.Context, snapshot upstreamHealthSnapshot) string {
	_, failure := a.runGeminiModelCatalog(ctx, snapshot.id, snapshot.endpoint, snapshot.keyVersion, snapshot.ciphertext)
	if failure != nil {
		return failure.result
	}
	return "catalog_ok"
}

func (a *App) runCodexCatalogTest(ctx context.Context, snapshot upstreamHealthSnapshot) (*int64, string) {
	selected := route{
		AccountID: snapshot.id, Endpoint: snapshot.endpoint, ProviderKind: snapshot.provider,
		Revision: snapshot.revision, Ciphertext: snapshot.ciphertext, KeyVersion: snapshot.keyVersion,
		CredentialState: snapshot.credentialState,
	}
	selected, credential, runErr := a.acquireCodexCredential(ctx, selected)
	tested := selected.Revision
	if tested < 1 {
		tested = snapshot.revision
	}
	if runErr != nil {
		if ctx.Err() != nil {
			return &tested, healthResultForContext(ctx)
		}
		if runErr.Code == membership.CodexErrorReauthentication {
			return &tested, "authentication_failed"
		}
		if runErr.PreflightAccountSpecific {
			return &tested, "authentication_failed"
		}
		return &tested, "internal_failure"
	}
	defer credential.Destroy()
	client := a.codexCatalogClientNoCache()
	if client == nil {
		return &tested, "internal_failure"
	}
	catalog, err := client.List(ctx, credential)
	if err != nil {
		if ctx.Err() != nil {
			return &tested, healthResultForContext(ctx)
		}
		return &tested, codexHealthCatalogErrorResult(err)
	}
	seen := make(map[string]struct{}, len(catalog.Models))
	ids := make([]string, 0, len(catalog.Models))
	for _, model := range catalog.Models {
		if !validIdentifier(model.ID, 256) || len(model.DisplayName) > 1024 {
			return &tested, "invalid_response"
		}
		if model.Visibility != nil && *model.Visibility != "list" {
			continue
		}
		if _, exists := seen[model.ID]; exists {
			continue
		}
		seen[model.ID] = struct{}{}
		ids = append(ids, model.ID)
		if len(ids) > modelDiscoveryMaxItems {
			return &tested, "invalid_response"
		}
	}
	sort.Strings(ids)
	return &tested, "catalog_ok"
}

func codexHealthCatalogErrorResult(err error) string {
	switch kind, _ := membership.CodexModelsErrorCodeOf(err); kind {
	case membership.CodexModelsUnauthorized, membership.CodexModelsForbidden:
		return "authentication_failed"
	case membership.CodexModelsRateLimited:
		return "rate_limited"
	case membership.CodexModelsTimeout:
		return "timeout"
	case membership.CodexModelsInvalidRequest:
		return "configuration_changed"
	case membership.CodexModelsBodyTooLarge, membership.CodexModelsTooManyItems, membership.CodexModelsInvalidPayload:
		return "invalid_response"
	default:
		return "internal_failure"
	}
}
