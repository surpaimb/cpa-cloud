package service

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const (
	upstreamBatchMaxBody  = 8 << 20
	upstreamBatchMaxItems = 100
	upstreamBatchTable    = "upstream_batch_items"
)

type upstreamBatchRequest struct {
	OperationID string               `json:"operation_id"`
	Items       []upstreamBatchInput `json:"items"`
}

type upstreamBatchInput struct {
	ItemID       string `json:"item_id"`
	Name         string `json:"name"`
	ProviderKind string `json:"provider_kind"`
	Endpoint     string `json:"endpoint"`
	APIKey       string `json:"api_key"`
	AuthJSON     string `json:"auth_json"`
}

type upstreamBatchResult struct {
	ItemID     string `json:"item_id"`
	Status     string `json:"status"`
	UpstreamID string `json:"upstream_id,omitempty"`
	ErrorCode  string `json:"error_code,omitempty"`
}

type upstreamBatchRecord struct {
	Digest     []byte
	UpstreamID string
}

func (s *store) migrateUpstreamBatchItems(ctx context.Context) error {
	var existing int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, upstreamBatchTable).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		columns, err := tableColumns(ctx, s.db, upstreamBatchTable)
		if err != nil {
			return err
		}
		var schema string
		if err := s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, upstreamBatchTable).Scan(&schema); err != nil {
			return err
		}
		normalized := strings.ToLower(strings.Join(strings.Fields(schema), " "))
		if !columns["operation_id"] || !columns["item_id"] || !columns["input_digest"] || !columns["upstream_id"] || !columns["created_at"] ||
			!strings.Contains(normalized, "primary key(operation_id,item_id)") ||
			!strings.Contains(normalized, "references upstreams(id) on delete restrict") {
			return errors.New("existing upstream batch table has an incompatible schema")
		}
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS upstream_batch_items (
		operation_id TEXT NOT NULL,
		item_id TEXT NOT NULL,
		input_digest BLOB NOT NULL,
		upstream_id TEXT NOT NULL UNIQUE REFERENCES upstreams(id) ON DELETE RESTRICT,
		created_at TEXT NOT NULL,
		PRIMARY KEY(operation_id,item_id)
	)`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	violated := rows.Next()
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if violated {
		return errors.New("foreign key check failed")
	}
	if closeErr != nil {
		return closeErr
	}
	return tx.Commit()
}

func (a *App) batchImportUpstreams(w http.ResponseWriter, r *http.Request, _ adminSession) {
	var input upstreamBatchRequest
	if !decodeJSON(w, r, upstreamBatchMaxBody, &input) {
		return
	}
	input.OperationID = strings.TrimSpace(input.OperationID)
	if !validUUIDOperation(input.OperationID) || len(input.Items) == 0 || len(input.Items) > upstreamBatchMaxItems {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid upstream batch request.")
		return
	}
	seen := make(map[string]struct{}, len(input.Items))
	for i := range input.Items {
		input.Items[i].ItemID = strings.TrimSpace(input.Items[i].ItemID)
		if !validText(input.Items[i].ItemID, 1, 120) {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid upstream batch request.")
			return
		}
		if _, duplicate := seen[input.Items[i].ItemID]; duplicate {
			writeAdminError(w, http.StatusBadRequest, "duplicate_item_id", "Batch item IDs must be unique.")
			return
		}
		seen[input.Items[i].ItemID] = struct{}{}
	}

	digests := make([][]byte, len(input.Items))
	for i := range input.Items {
		digest, err := a.upstreamBatchDigest(input.OperationID, &input.Items[i])
		if err != nil {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid upstream batch request.")
			return
		}
		digests[i] = digest
	}

	// Serializing the preflight and item transactions makes conflicting
	// concurrent requests observe a complete prior result before creating data.
	a.admission.Lock()
	defer a.admission.Unlock()

	for i := range input.Items {
		record, err := loadUpstreamBatchRecord(r.Context(), a.store.db, input.OperationID, input.Items[i].ItemID)
		if err == nil && subtle.ConstantTimeCompare(record.Digest, digests[i]) != 1 {
			writeAdminError(w, http.StatusConflict, "operation_conflict", "The operation item was already used with different input.")
			return
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
			return
		}
	}

	results := make([]upstreamBatchResult, len(input.Items))
	for i := range input.Items {
		if record, err := loadUpstreamBatchRecord(r.Context(), a.store.db, input.OperationID, input.Items[i].ItemID); err == nil {
			results[i] = upstreamBatchResult{ItemID: input.Items[i].ItemID, Status: "existing", UpstreamID: record.UpstreamID}
			continue
		} else if !errors.Is(err, sql.ErrNoRows) {
			results[i] = failedUpstreamBatchItem(input.Items[i].ItemID, "storage_unavailable")
			continue
		}
		results[i] = a.createUpstreamBatchItem(r.Context(), input.OperationID, input.Items[i], digests[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": results})
}

func (a *App) upstreamBatchDigest(operationID string, item *upstreamBatchInput) ([]byte, error) {
	normalized := upstreamBatchInput{
		ItemID: strings.TrimSpace(item.ItemID), Name: strings.TrimSpace(item.Name),
		ProviderKind: strings.TrimSpace(item.ProviderKind), Endpoint: strings.TrimSpace(item.Endpoint),
		APIKey: item.APIKey, AuthJSON: item.AuthJSON,
	}
	*item = normalized
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return nil, err
	}
	defer clear(encoded)
	return a.secrets.digest("upstream-batch-input/v1", operationID+"\x00"+string(encoded)), nil
}

func (a *App) createUpstreamBatchItem(ctx context.Context, operationID string, input upstreamBatchInput, digest []byte) upstreamBatchResult {
	result := upstreamBatchResult{ItemID: input.ItemID}
	if !validText(input.Name, 1, 120) {
		return failedUpstreamBatchItem(input.ItemID, "invalid_item")
	}

	endpoint, keyVersion, credentialState, operationValue, credential, errorCode := a.prepareUpstreamBatchCredential(ctx, operationID, input)
	if errorCode != "" {
		return failedUpstreamBatchItem(input.ItemID, errorCode)
	}
	defer clear(credential)

	id, err := newID("ups")
	if err != nil {
		return failedUpstreamBatchItem(input.ItemID, "service_unavailable")
	}
	var ciphertext []byte
	if input.ProviderKind == codexMembershipProvider {
		ciphertext, err = a.secrets.encryptCodexAuth(id, credential)
	} else if input.ProviderKind == geminiAPIKeyProvider {
		ciphertext, err = a.secrets.encryptGeminiAPIKey(id, string(credential))
	} else {
		ciphertext, err = a.secrets.encryptCredential(id, string(credential))
	}
	if err != nil {
		return failedUpstreamBatchItem(input.ItemID, "service_unavailable")
	}

	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		return failedUpstreamBatchItem(input.ItemID, "storage_unavailable")
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO upstreams(
		id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id
	) VALUES(?,?,?,?,?,?,?,?,?,?,NULL,?)`, id, input.Name, input.ProviderKind, endpoint, 1, ciphertext, keyVersion, 1, utcNow(), credentialState, operationValue)
	if err == nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO upstream_batch_items(operation_id,item_id,input_digest,upstream_id,created_at) VALUES(?,?,?,?,?)`, operationID, input.ItemID, digest, id, utcNow())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		_ = tx.Rollback()
		if isConflict(err) {
			if record, lookupErr := loadUpstreamBatchRecord(ctx, a.store.db, operationID, input.ItemID); lookupErr == nil && subtle.ConstantTimeCompare(record.Digest, digest) == 1 {
				return upstreamBatchResult{ItemID: input.ItemID, Status: "existing", UpstreamID: record.UpstreamID}
			}
		}
		return failedUpstreamBatchItem(input.ItemID, "storage_unavailable")
	}
	result.Status = "created"
	result.UpstreamID = id
	return result
}

func (a *App) prepareUpstreamBatchCredential(ctx context.Context, operationID string, input upstreamBatchInput) (endpoint string, keyVersion int, credentialState any, operationValue any, credential []byte, errorCode string) {
	switch input.ProviderKind {
	case "openai-compatible", anthropicAPIKeyProvider:
		if input.AuthJSON != "" || !validText(input.APIKey, 1, 4096) {
			return "", 0, nil, nil, nil, "invalid_credential"
		}
		validated, err := validateEndpoint(ctx, input.Endpoint, a.cfg.AllowLoopbackUpstream)
		if err != nil {
			return "", 0, nil, nil, nil, "invalid_endpoint"
		}
		return validated, 1, nil, nil, []byte(input.APIKey), ""
	case geminiAPIKeyProvider:
		if input.AuthJSON != "" || !validText(input.APIKey, 1, 4096) {
			return "", 0, nil, nil, nil, "invalid_credential"
		}
		validated, err := validateGeminiEndpoint(ctx, input.Endpoint, a.cfg.AllowLoopbackUpstream)
		if err != nil {
			return "", 0, nil, nil, nil, "invalid_endpoint"
		}
		return validated, 2, nil, nil, []byte(input.APIKey), ""
	case codexMembershipProvider:
		if !a.cfg.ExperimentalCodexMembership {
			return "", 0, nil, nil, nil, "feature_disabled"
		}
		if input.APIKey != "" || input.Endpoint != "" || input.AuthJSON == "" {
			return "", 0, nil, nil, nil, "invalid_credential"
		}
		parsed, err := parseSchedulableCodexAuth([]byte(input.AuthJSON))
		if err != nil {
			return "", 0, nil, nil, nil, "invalid_codex_auth"
		}
		defer parsed.Destroy()
		raw := parsed.RawAuthJSONSecret()
		operationDigest := a.secrets.digest("upstream-batch-codex-operation/v1", operationID+"\x00"+input.ItemID)
		return codexMembershipEndpoint, 2, codexStateImported, "batch-" + hex.EncodeToString(operationDigest), raw, ""
	default:
		return "", 0, nil, nil, nil, "unsupported_provider"
	}
}

func loadUpstreamBatchRecord(ctx context.Context, query queryRower, operationID, itemID string) (upstreamBatchRecord, error) {
	var record upstreamBatchRecord
	err := query.QueryRowContext(ctx, `SELECT input_digest,upstream_id FROM upstream_batch_items WHERE operation_id=? AND item_id=?`, operationID, itemID).
		Scan(&record.Digest, &record.UpstreamID)
	return record, err
}

func failedUpstreamBatchItem(itemID, code string) upstreamBatchResult {
	return upstreamBatchResult{ItemID: itemID, Status: "failed", ErrorCode: code}
}

func (r upstreamBatchResult) String() string {
	return fmt.Sprintf("upstreamBatchResult{item_id:%q,status:%q,upstream_id_present:%t,error_code:%q}", r.ItemID, r.Status, r.UpstreamID != "", r.ErrorCode)
}
