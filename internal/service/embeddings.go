package service

// The embeddings entry is intentionally separate from the generation
// conversion runtime. It accepts only the independently documented text and
// token-array float subset and can dispatch only an explicit embedding model through an explicit
// openai-embeddings account-pool route.
import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/embeddingwire"
	"cpacloud.local/server/internal/keypolicy"
	"cpacloud.local/server/internal/scheduling"
)

func (a *App) embeddings(w http.ResponseWriter, r *http.Request) {
	mediaType, _, mediaTypeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaTypeErr != nil || mediaType != "application/json" || r.Header.Get("Content-Encoding") != "" {
		writeModelError(w, http.StatusUnsupportedMediaType, "invalid_request_error", "Embedding requests require uncompressed application/json.", requestID(r.Context()))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, embeddingwire.MaxRequestBytes+1))
	if err != nil || len(body) > embeddingwire.MaxRequestBytes {
		writeModelError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Embedding request exceeds the local request limit.", requestID(r.Context()))
		return
	}
	request, err := embeddingwire.DecodeRequest(body)
	if err != nil {
		status, code, message := http.StatusBadRequest, "invalid_request_error", "Invalid embedding request."
		if errors.Is(err, embeddingwire.ErrUnsupportedFeature) {
			code, message = "unsupported_feature", "The embedding request uses a feature outside the supported subset."
		} else if errors.Is(err, embeddingwire.ErrLimitExceeded) {
			status, code, message = http.StatusRequestEntityTooLarge, "request_too_large", "Embedding request exceeds a local resource limit."
		}
		writeModelError(w, status, code, message, requestID(r.Context()))
		return
	}
	if !validIdentifier(request.Model, 128) {
		writeModelError(w, http.StatusBadRequest, "invalid_request_error", "A valid model is required.", requestID(r.Context()))
		return
	}
	auth, ok := a.authenticateEmployee(w, r)
	if !ok {
		return
	}
	auth, sourceFailure := a.authorizeKeySource(auth, r)
	if sourceFailure != nil {
		writeModelError(w, sourceFailure.status, sourceFailure.code, sourceFailure.message, requestID(r.Context()))
		return
	}
	auth, policyFailure := authorizeKeyPolicy(auth, keypolicy.ProtocolOpenAIEmbeddings, request.Model)
	if policyFailure != nil {
		writeModelError(w, policyFailure.status, policyFailure.code, policyFailure.message, requestID(r.Context()))
		return
	}
	if failure := a.validatePoolSession(r, auth, request.Model); failure != nil {
		writeModelError(w, failure.status, failure.code, failure.message, requestID(r.Context()))
		return
	}
	strictBudget, err := a.embeddingStrictBudgetMatched(r.Context(), auth, request.Model)
	if err != nil {
		writeModelError(w, http.StatusServiceUnavailable, "governance_unavailable", "Request governance is temporarily unavailable.", requestID(r.Context()))
		return
	}
	if strictBudget {
		writeModelError(w, http.StatusServiceUnavailable, "budget_bound_unavailable", "This request has no supported budget bound or matching price.", requestID(r.Context()))
		return
	}

	// Core governance policies are protocol-independent today. The durable core
	// lease uses the established Responses label, while selector-aware budgets
	// receive the actual embedding protocol so a Responses-only policy cannot
	// affect this request. Accounting and route selection also retain the actual
	// embedding wire below.
	governed, guard, governanceFailure := a.admitGovernedModelWithSelector(r, auth, request.Model, accounting.ProtocolOpenAIResponses, accounting.ProtocolOpenAIEmbeddings)
	if governanceFailure != nil {
		if r.Context().Err() == nil {
			writeModelError(w, governanceFailure.status, governanceFailure.code, governanceFailure.message, requestID(r.Context()))
		}
		return
	}
	r = governed
	if guard != nil {
		defer guard.Close()
	}

	modelRequestID := requestID(r.Context())
	var upstreamRequest *http.Request
	selected, lease, failure := a.prepareModelRoute(r, auth, request.Model, []string{"openai-compatible"}, accounting.ProtocolOpenAIEmbeddings, true, func(candidateRequest *http.Request, candidate route) (route, *modelPreflightError) {
		if candidate.ProviderKind != "openai-compatible" || candidate.WireProtocol != wireProtocolEmbeddings || candidate.ModelKind != "embedding" || candidate.KeyVersion != 1 {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
		}
		credential, err := a.secrets.decryptCredential(candidate.AccountID, candidate.Ciphertext)
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailureAuth)
		}
		outgoing, err := embeddingwire.MarshalRequest(request, candidate.UpstreamModel)
		if err != nil {
			return route{}, requestPreflightFailure(http.StatusBadRequest, "invalid_request_error", "Invalid embedding request.")
		}
		endpoint, err := validateEndpoint(candidateRequest.Context(), candidate.Endpoint, a.cfg.AllowLoopbackUpstream)
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
		}
		target, err := upstreamEmbeddingsURL(endpoint)
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "no_available_route", "No available route for this model.", scheduling.FailurePermanent)
		}
		prepared, err := http.NewRequestWithContext(candidateRequest.Context(), http.MethodPost, target, strings.NewReader(string(outgoing)))
		if err != nil {
			return route{}, accountPreflightFailure(http.StatusServiceUnavailable, "upstream_unavailable", "Upstream is unavailable.", scheduling.FailurePermanent)
		}
		prepared.Header.Set("Authorization", "Bearer "+credential)
		prepared.Header.Set("Content-Type", "application/json")
		prepared.Header.Set("Accept", "application/json")
		prepared.Header.Set("Accept-Encoding", "identity")
		upstreamRequest = prepared
		return candidate, nil
	})
	if failure != nil {
		if r.Context().Err() == nil {
			writeModelError(w, failure.status, failure.code, failure.message, modelRequestID)
		}
		return
	}
	if lease != nil {
		r = r.WithContext(lease.Context())
	}
	defer a.releaseModelLease(lease, modelRequestID, true)
	client, dispatchFailure := a.dispatchModelRoute(r, auth, request.Model, selected, lease, true, upstreamRequest)
	if dispatchFailure != nil {
		if a.finishDispatchFailure(r, modelRequestID, true) {
			writeModelError(w, dispatchFailure.status, dispatchFailure.code, dispatchFailure.message, modelRequestID)
		}
		return
	}
	response, err := client.Do(upstreamRequest)
	if err != nil {
		outcome := "failed"
		if errors.Is(r.Context().Err(), context.Canceled) {
			outcome = "cancelled"
		}
		a.finishRequest(modelRequestID, outcome, 0)
		if r.Context().Err() == nil {
			writeModelError(w, http.StatusBadGateway, "upstream_unavailable", "Upstream is unavailable.", modelRequestID)
		}
		return
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		if retry := safeRetryAfter(response.Header.Get("Retry-After")); retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		writeModelError(w, http.StatusBadGateway, "upstream_error", "Upstream request failed.", modelRequestID)
		return
	}
	responseType, _, responseTypeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	encoding := strings.TrimSpace(response.Header.Get("Content-Encoding"))
	if responseTypeErr != nil || responseType != "application/json" || encoding != "" && !strings.EqualFold(encoding, "identity") {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		writeModelError(w, http.StatusBadGateway, "upstream_protocol_error", "Upstream returned an invalid embedding response.", modelRequestID)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, embeddingwire.MaxResponseBytes+1))
	if err != nil || len(raw) > embeddingwire.MaxResponseBytes || r.Context().Err() != nil {
		outcome := "failed"
		if r.Context().Err() != nil {
			outcome = "cancelled"
		}
		a.finishRequest(modelRequestID, outcome, response.StatusCode)
		if r.Context().Err() == nil {
			writeModelError(w, http.StatusBadGateway, "upstream_protocol_error", "Upstream returned an invalid embedding response.", modelRequestID)
		}
		return
	}
	parsed, err := embeddingwire.DecodeResponse(raw)
	if err == nil {
		_, err = embeddingwire.ValidateResponse(parsed, selected.UpstreamModel, request.Input.Count())
	}
	if err != nil {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		writeModelError(w, http.StatusBadGateway, "upstream_protocol_error", "Upstream returned an invalid embedding response.", modelRequestID)
		return
	}
	a.observeRequestUsage(modelRequestID, raw)
	encoded, err := embeddingwire.MarshalResponse(parsed, request.Model)
	if err != nil {
		a.finishRequest(modelRequestID, "failed", response.StatusCode)
		writeModelError(w, http.StatusBadGateway, "upstream_protocol_error", "Upstream returned an invalid embedding response.", modelRequestID)
		return
	}
	if err := a.finishRequestChecked(modelRequestID, "succeeded", response.StatusCode); err != nil {
		writeModelError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.", modelRequestID)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}

func (a *App) embeddingStrictBudgetMatched(ctx context.Context, auth employeeAuth, model string) (bool, error) {
	var enabled, budgetEnabled int
	if err := a.store.db.QueryRowContext(ctx, `SELECT enabled,budget_enabled FROM governance_settings WHERE singleton=1`).Scan(&enabled, &budgetEnabled); err != nil {
		return false, err
	}
	if enabled != 1 || budgetEnabled != 1 {
		return false, nil
	}
	scope := `(p.scope_kind='employee' AND p.scope_id=?) OR (p.scope_kind='key' AND p.scope_id=?) OR
		(p.scope_kind='group' AND EXISTS(SELECT 1 FROM governance_group_members gm WHERE gm.group_id=p.scope_id AND gm.employee_id=?))`
	var count int
	query := `SELECT
		(SELECT COUNT(*) FROM governance_general_budget_policies p WHERE p.enabled=1 AND (p.protocol='' OR p.protocol='openai-embeddings') AND (p.model='' OR p.model=?) AND (` + scope + `)) +
		(SELECT COUNT(*) FROM governance_policies p WHERE p.enabled=1 AND p.unknown_mode='deny_unknown' AND (p.hard_tpm IS NOT NULL OR p.hard_cost_micro IS NOT NULL) AND (` + scope + `))`
	err := a.store.db.QueryRowContext(ctx, query,
		model, auth.EmployeeID, auth.KeyID, auth.EmployeeID,
		auth.EmployeeID, auth.KeyID, auth.EmployeeID,
	).Scan(&count)
	return count != 0, err
}
