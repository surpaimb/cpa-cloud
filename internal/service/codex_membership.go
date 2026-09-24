package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	"cpacloud.local/server/internal/membership"
	"cpacloud.local/server/internal/scheduling"
)

const (
	codexMembershipProvider = "codex-membership"
	codexMembershipEndpoint = "https://chatgpt.com/backend-api/codex"
	codexStateImported      = "imported_unverified"
	codexStateVerified      = "verified"
	codexStateReauth        = "reauth_required"
)

type codexExecutionResult struct {
	Text       string
	ResponseID string
	Usage      membership.CodexUsage
}

type codexExecutionEvent struct {
	Kind       membership.CodexEventKind
	Text       string
	ResponseID string
	Usage      membership.CodexUsage
	ErrorCode  membership.CodexAdapterErrorCode
}

type codexRunError struct {
	Code           membership.CodexAdapterErrorCode
	UpstreamStatus int
	RetryAfter     time.Duration
	// PreflightAccountSpecific is positive evidence that this error belongs to
	// the selected account and happened before model execution. Its zero value
	// is deliberately unsafe for failover.
	PreflightAccountSpecific bool
	PreflightClass           scheduling.FailureClass
}

func (e *codexRunError) Error() string { return "Codex membership request failed." }

type codexExecutor interface {
	Complete(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError)
	Stream(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest, func(codexExecutionEvent) error) *codexRunError
}

type productionCodexExecutor struct {
	adapter *membership.CodexDirectAdapter
}

func newProductionCodexExecutor() codexExecutor {
	return &productionCodexExecutor{adapter: membership.NewCodexDirectAdapter()}
}

func (e *productionCodexExecutor) Complete(ctx context.Context, credential *membership.CodexAuthCredential, request membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
	result, err := e.adapter.Complete(ctx, credential, request)
	if err != nil {
		return codexExecutionResult{}, normalizeCodexRunError(err)
	}
	return codexExecutionResult{Text: result.Text(), ResponseID: result.ResponseID(), Usage: result.Usage()}, nil
}

func (e *productionCodexExecutor) Stream(ctx context.Context, credential *membership.CodexAuthCredential, request membership.CodexTextRequest, consume func(codexExecutionEvent) error) *codexRunError {
	err := e.adapter.Stream(ctx, credential, request, func(event membership.CodexStreamEvent) error {
		return consume(codexExecutionEvent{
			Kind: event.Kind(), Text: event.Text(), ResponseID: event.ResponseID(), Usage: event.Usage(), ErrorCode: event.ErrorCode(),
		})
	})
	return normalizeCodexRunError(err)
}

func normalizeCodexRunError(err error) *codexRunError {
	if err == nil {
		return nil
	}
	result := &codexRunError{Code: membership.CodexErrorUpstream}
	if code, ok := membership.CodexRequestErrorCode(err); ok {
		result.Code = code
	}
	var adapterError *membership.CodexAdapterError
	if errors.As(err, &adapterError) {
		result.UpstreamStatus = adapterError.HTTPStatus()
		result.RetryAfter = adapterError.RetryAfter()
	}
	return result
}

type importCodexRequest struct {
	Name        string `json:"name"`
	AuthJSON    string `json:"auth_json"`
	OperationID string `json:"operation_id"`
}

type replaceCodexRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	AuthJSON         string `json:"auth_json"`
}

func (a *App) importCodexUpstream(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !a.cfg.ExperimentalCodexMembership {
		writeAdminError(w, http.StatusForbidden, "feature_disabled", "Codex membership import is disabled.")
		return
	}
	var input importCodexRequest
	if !decodeJSON(w, r, codexImportMaxBody, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	input.OperationID = strings.TrimSpace(input.OperationID)
	if !validText(input.Name, 1, 120) || !validUUIDOperation(input.OperationID) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid Codex import fields.")
		return
	}
	if prior, err := a.loadUpstreamByOperation(r.Context(), input.OperationID); err == nil {
		writeJSON(w, http.StatusOK, prior)
		return
	} else if !errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	credential, err := parseSchedulableCodexAuth([]byte(input.AuthJSON))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_codex_auth", "The Codex authorization file is invalid or cannot be scheduled.")
		return
	}
	defer credential.Destroy()
	id, err := newID("ups")
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	ciphertext, err := a.secrets.encryptCodexAuth(id, credential.RawAuthJSONSecret())
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	state := codexStateImported
	item := upstreamView{
		ID: id, Name: input.Name, ProviderKind: codexMembershipProvider, Endpoint: codexMembershipEndpoint,
		Enabled: true, Revision: 1, CredentialState: &state,
	}
	item.OAuthRefresh = a.codexOAuthRefreshView(codexMembershipProvider, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{})
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	_, err = tx.ExecContext(r.Context(), `INSERT INTO upstreams(
		id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id
	) VALUES(?,?,?,?,?,?,?,?,?,?,NULL,?)`, item.ID, item.Name, item.ProviderKind, item.Endpoint, 1, ciphertext, 2, item.Revision, utcNow(), state, input.OperationID)
	if err == nil {
		err = tx.Commit()
	} else {
		_ = tx.Rollback()
	}
	if err != nil {
		if isConflict(err) {
			if prior, lookupErr := a.loadUpstreamByOperation(r.Context(), input.OperationID); lookupErr == nil {
				writeJSON(w, http.StatusOK, prior)
				return
			}
			writeAdminError(w, http.StatusConflict, "already_exists", "A conflicting upstream already exists.")
			return
		}
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *App) replaceCodexCredential(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if !a.cfg.ExperimentalCodexMembership {
		writeAdminError(w, http.StatusForbidden, "feature_disabled", "Codex membership import is disabled.")
		return
	}
	var input replaceCodexRequest
	if !decodeJSON(w, r, codexImportMaxBody, &input) {
		return
	}
	if input.ExpectedRevision < 1 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "A valid expected_revision is required.")
		return
	}
	credential, err := parseSchedulableCodexAuth([]byte(input.AuthJSON))
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_codex_auth", "The Codex authorization file is invalid or cannot be scheduled.")
		return
	}
	defer credential.Destroy()
	id := r.PathValue("id")
	ciphertext, err := a.secrets.encryptCodexAuth(id, credential.RawAuthJSONSecret())
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
		return
	}
	unlock, err := a.acquireCodexMutationLock(r.Context(), id)
	if err != nil {
		writeAdminError(w, http.StatusRequestTimeout, "request_cancelled", "The request was cancelled.")
		return
	}
	defer unlock()
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer tx.Rollback()
	item, err := loadUpstreamView(r.Context(), tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Upstream was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if item.ProviderKind != codexMembershipProvider {
		writeAdminError(w, http.StatusBadRequest, "invalid_upstream_type", "This upstream does not use Codex membership credentials.")
		return
	}
	if item.Archived {
		writeAdminError(w, http.StatusConflict, "upstream_archived", "Archived upstreams cannot accept credentials.")
		return
	}
	if item.Revision != input.ExpectedRevision {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	item.Revision++
	state := codexStateImported
	item.CredentialState = &state
	item.VerifiedAt = nil
	result, err := tx.ExecContext(r.Context(), `UPDATE upstreams SET
		credential_ciphertext=?,key_version=2,revision=?,credential_state=?,verified_at=NULL
		WHERE id=? AND provider_kind=? AND revision=?`, ciphertext, item.Revision, state, id, codexMembershipProvider, input.ExpectedRevision)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM codex_oauth_bindings WHERE upstream_id=?`, id); err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if _, err := tx.ExecContext(r.Context(), `DELETE FROM codex_oauth_refresh_states WHERE upstream_id=?`, id); err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if err := tx.Commit(); err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	a.notifyAccountPoolChanged()
	item.OAuthRefresh = a.codexOAuthRefreshView(codexMembershipProvider, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullString{})
	writeJSON(w, http.StatusOK, item)
}

func parseSchedulableCodexAuth(raw []byte) (*membership.CodexAuthCredential, error) {
	credential, err := membership.ParseCodexAuthJSON(raw)
	if err != nil {
		return nil, err
	}
	if err := membership.NewCodexDirectAdapter().ValidateCredentialForScheduling(credential); err != nil {
		credential.Destroy()
		return nil, err
	}
	return credential, nil
}

func (a *App) loadUpstreamByOperation(ctx context.Context, operationID string) (upstreamView, error) {
	var id string
	err := a.store.db.QueryRowContext(ctx, `SELECT id FROM upstreams WHERE provider_kind=? AND operation_id=? AND archived=0`, codexMembershipProvider, operationID).Scan(&id)
	if err != nil {
		return upstreamView{}, err
	}
	item, err := loadUpstreamView(ctx, a.store.db, id)
	if err != nil {
		return upstreamView{}, err
	}
	if err := a.decorateUpstreamOAuthRefresh(ctx, &item); err != nil {
		return upstreamView{}, err
	}
	return item, nil
}

func loadUpstreamView(ctx context.Context, query queryRower, id string) (upstreamView, error) {
	var item upstreamView
	var enabled, archived int
	var state, verified, archivedAt sql.NullString
	err := query.QueryRowContext(ctx, `SELECT id,name,provider_kind,endpoint,enabled,revision,credential_state,verified_at,archived,archived_at FROM upstreams WHERE id=?`, id).
		Scan(&item.ID, &item.Name, &item.ProviderKind, &item.Endpoint, &enabled, &item.Revision, &state, &verified, &archived, &archivedAt)
	item.Enabled = enabled != 0
	item.Archived = archived != 0
	item.ArchivedAt = nullString(archivedAt)
	item.CredentialState = nullString(state)
	item.VerifiedAt = nullString(verified)
	return item, err
}

func validUUIDOperation(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, character := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return true
}
