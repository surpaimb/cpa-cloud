package service

// Account and route lifecycle management is independently authored from the
// CPA Cloud lifecycle contract. Tombstones preserve identifiers and ledger
// joins; recoverable credentials are destroyed when an upstream is archived.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

const lifecycleAuditLimit = 1000

type expectedRevisionRequest struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

type updateModelRequest struct {
	ExpectedRevision int64   `json:"expected_revision"`
	UpstreamID       *string `json:"upstream_id"`
	UpstreamModel    *string `json:"upstream_model"`
	Enabled          *bool   `json:"enabled"`
	WireProtocol     *string `json:"wire_protocol"`
}

func includeArchivedQuery(w http.ResponseWriter, r *http.Request) (bool, bool) {
	values, present := r.URL.Query()["include_archived"]
	if len(values) > 1 {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "include_archived must be supplied at most once.")
		return false, false
	}
	value := ""
	if present && len(values) == 1 {
		value = values[0]
	}
	switch value {
	case "":
		return false, true
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "include_archived must be true or false.")
		return false, false
	}
}

func recordLifecycleAudit(ctx context.Context, tx *sql.Tx, actorID, action, targetType, targetID, result string) error {
	id, err := newID("aud")
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,?,?,?,?,?)`,
		id, actorID, action, targetType, targetID, result, utcNow()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM account_lifecycle_audit WHERE id IN (
		SELECT id FROM account_lifecycle_audit ORDER BY occurred_at DESC,id DESC LIMIT -1 OFFSET ?
	)`, lifecycleAuditLimit)
	return err
}

func (a *App) updateModel(w http.ResponseWriter, r *http.Request, session adminSession) {
	var input updateModelRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	if input.ExpectedRevision < 1 {
		writeAdminError(w, http.StatusBadRequest, "invalid_revision", "A valid expected_revision is required.")
		return
	}
	if input.UpstreamID == nil && input.UpstreamModel == nil && input.Enabled == nil && input.WireProtocol == nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "No model changes were provided.")
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer tx.Rollback()
	item, err := loadModelView(r.Context(), tx, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Model was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if item.Archived {
		writeAdminError(w, http.StatusConflict, "model_archived", "Archived models cannot be changed.")
		return
	}
	if item.Revision != input.ExpectedRevision {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	nextUpstream, nextModel, nextEnabled, nextWire := item.UpstreamID, item.UpstreamModel, item.Enabled, item.WireProtocol
	if input.UpstreamID != nil {
		nextUpstream = strings.TrimSpace(*input.UpstreamID)
	}
	if input.UpstreamModel != nil {
		nextModel = strings.TrimSpace(*input.UpstreamModel)
	}
	if input.Enabled != nil {
		nextEnabled = *input.Enabled
	}
	if input.WireProtocol != nil {
		nextWire = strings.TrimSpace(*input.WireProtocol)
	}
	if !validText(nextUpstream, 1, 128) || !validText(nextModel, 1, 256) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid model fields.")
		return
	}
	if nextUpstream != item.UpstreamID || nextModel != item.UpstreamModel {
		var poolEntries int
		if err := tx.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM model_account_pool_routes WHERE model_id=?`, item.ID).Scan(&poolEntries); err != nil {
			writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
		if poolEntries != 0 {
			writeAdminError(w, http.StatusConflict, "model_pool_not_empty", "Clear the model account pool before changing its default target.")
			return
		}
	}
	var provider string
	if err := tx.QueryRowContext(r.Context(), `SELECT provider_kind FROM upstreams WHERE id=? AND archived=0`, nextUpstream).Scan(&provider); errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Upstream was not found.")
		return
	} else if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if !validProviderModelName(provider, nextModel) || provider == geminiAPIKeyProvider && strings.Contains(item.ID, "/") || !validRouteWireProtocol(nextWire) || !providerSupportsWire(provider, nextWire) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "The upstream model is not supported by that provider.")
		return
	}
	item.UpstreamID, item.UpstreamModel, item.Enabled, item.WireProtocol, item.Revision = nextUpstream, nextModel, nextEnabled, nextWire, item.Revision+1
	result, err := tx.ExecContext(r.Context(), `UPDATE models SET upstream_id=?,upstream_model=?,wire_protocol=?,enabled=?,revision=? WHERE id=? AND revision=? AND archived=0`,
		item.UpstreamID, item.UpstreamModel, item.WireProtocol, boolInt(item.Enabled), item.Revision, item.ID, input.ExpectedRevision)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	if err := recordLifecycleAudit(r.Context(), tx, session.AdminID, "model.update", "model", item.ID, "succeeded"); err != nil || tx.Commit() != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	a.notifyAccountPoolChanged()
	writeJSON(w, http.StatusOK, item)
}

func (a *App) archiveModel(w http.ResponseWriter, r *http.Request, session adminSession) {
	var input expectedRevisionRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	if input.ExpectedRevision < 1 {
		writeAdminError(w, http.StatusBadRequest, "invalid_revision", "A valid expected_revision is required.")
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	defer tx.Rollback()
	item, err := loadModelView(r.Context(), tx, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Model was not found.")
		return
	}
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if item.Archived {
		item.ArchiveResult = "already_archived"
		writeJSON(w, http.StatusOK, item)
		return
	}
	if item.Revision != input.ExpectedRevision {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	archivedAt := utcNow()
	result, err := tx.ExecContext(r.Context(), `UPDATE models SET enabled=0,archived=1,archived_at=?,revision=revision+1 WHERE id=? AND revision=? AND archived=0`, archivedAt, item.ID, input.ExpectedRevision)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	if err := recordLifecycleAudit(r.Context(), tx, session.AdminID, "model.archive", "model", item.ID, "succeeded"); err != nil || tx.Commit() != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	item.Enabled, item.Archived, item.ArchivedAt, item.Revision, item.ArchiveResult = false, true, &archivedAt, item.Revision+1, "archived"
	a.notifyAccountPoolChanged()
	writeJSON(w, http.StatusOK, item)
}

func loadModelView(ctx context.Context, query queryRower, id string) (modelView, error) {
	var item modelView
	var enabled, archived int
	var archivedAt sql.NullString
	err := query.QueryRowContext(ctx, `SELECT id,upstream_id,upstream_model,wire_protocol,enabled,revision,archived,archived_at FROM models WHERE id=?`, id).
		Scan(&item.ID, &item.UpstreamID, &item.UpstreamModel, &item.WireProtocol, &enabled, &item.Revision, &archived, &archivedAt)
	item.Enabled, item.Archived, item.ArchivedAt = enabled != 0, archived != 0, nullString(archivedAt)
	return item, err
}

func (a *App) archiveUpstream(w http.ResponseWriter, r *http.Request, session adminSession) {
	var input expectedRevisionRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	if input.ExpectedRevision < 1 {
		writeAdminError(w, http.StatusBadRequest, "invalid_revision", "A valid expected_revision is required.")
		return
	}
	id := r.PathValue("id")
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
	if item.Archived {
		item.ArchiveResult = "already_archived"
		writeJSON(w, http.StatusOK, item)
		return
	}
	if item.Revision != input.ExpectedRevision {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	var references int
	if err := tx.QueryRowContext(r.Context(), `SELECT
		(SELECT COUNT(*) FROM models WHERE archived=0 AND upstream_id=?) +
		(SELECT COUNT(*) FROM model_account_pool_routes r JOIN models m ON m.id=r.model_id WHERE m.archived=0 AND r.upstream_id=?)`, id, id).Scan(&references); err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if references != 0 {
		writeAdminError(w, http.StatusConflict, "upstream_in_use", "The upstream is still referenced by an active model route.")
		return
	}
	archivedAt := utcNow()
	credentialState := any(nil)
	if item.ProviderKind == codexMembershipProvider {
		credentialState = codexStateReauth
	}
	result, err := tx.ExecContext(r.Context(), `UPDATE upstreams SET enabled=0,archived=1,archived_at=?,revision=revision+1,credential_ciphertext=?,key_version=0,credential_state=?,verified_at=NULL WHERE id=? AND revision=? AND archived=0`,
		archivedAt, []byte{}, credentialState, id, input.ExpectedRevision)
	if err != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
		return
	}
	for _, statement := range []string{
		`DELETE FROM codex_oauth_bindings WHERE upstream_id=?`,
		`DELETE FROM codex_oauth_refresh_states WHERE upstream_id=?`,
		`DELETE FROM upstream_proxy_bindings WHERE upstream_id=?`,
		`DELETE FROM account_recovery_states WHERE account_id=?`,
	} {
		if _, err := tx.ExecContext(r.Context(), statement, id); err != nil {
			writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
	}
	if a.archiveUpstreamTxHook != nil {
		if err := a.archiveUpstreamTxHook(r.Context(), tx, id, session.AdminID, archivedAt); err != nil {
			writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
	}
	if err := recordLifecycleAudit(r.Context(), tx, session.AdminID, "upstream.archive", "upstream", id, "succeeded"); err != nil || tx.Commit() != nil {
		writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
		return
	}
	item.Enabled, item.Archived, item.ArchivedAt, item.Revision, item.ArchiveResult = false, true, &archivedAt, item.Revision+1, "archived"
	item.VerifiedAt, item.OAuthRefresh = nil, nil
	if item.ProviderKind == codexMembershipProvider {
		state := codexStateReauth
		item.CredentialState = &state
	}
	a.catalogMu.Lock()
	delete(a.catalogs, id)
	a.catalogMu.Unlock()
	a.notifyAccountPoolChanged()
	if a.upstreamArchivedHook != nil {
		a.upstreamArchivedHook(id)
	}
	writeJSON(w, http.StatusOK, item)
}
