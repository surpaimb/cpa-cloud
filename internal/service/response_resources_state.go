package service

// Independently authored from CPA Cloud's Responses state ADR and the public
// OpenAI Responses API. This file owns only encrypted resource persistence
// primitives. It does not register routes, start workers, or perform network I/O.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const (
	responseStateSchemaVersion = 1
	responseStateMaxPlaintext  = 4 << 20
	responseStateMaxItems      = 4096
)

var errResponseStateUnavailable = errors.New("response state unavailable")

const responseResourcesDDL = `CREATE TABLE response_resources (
	id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 1 AND 128),
	operation_id TEXT NOT NULL CHECK(length(operation_id) BETWEEN 1 AND 128),
	input_fingerprint BLOB NOT NULL CHECK(length(input_fingerprint) = 32),
	employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE RESTRICT,
	key_id TEXT NOT NULL REFERENCES access_keys(id) ON DELETE RESTRICT,
	public_model TEXT NOT NULL REFERENCES models(id) ON DELETE RESTRICT CHECK(length(public_model) BETWEEN 1 AND 128),
	parent_response_id TEXT REFERENCES response_resources(id) ON DELETE RESTRICT,
	background INTEGER NOT NULL CHECK(background IN (0,1)),
	store_body INTEGER NOT NULL CHECK(store_body IN (0,1)),
	status TEXT NOT NULL CHECK(status IN ('queued','dispatch_authorized','in_progress','completed','failed','cancelled','interrupted')),
	revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
	schema_version INTEGER NOT NULL CHECK(schema_version = 1),
	dek_wrap_nonce BLOB CHECK(dek_wrap_nonce IS NULL OR length(dek_wrap_nonce) = 12),
	wrapped_dek BLOB CHECK(wrapped_dek IS NULL OR length(wrapped_dek) = 48),
	error_code TEXT CHECK(error_code IS NULL OR length(error_code) BETWEEN 1 AND 64),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	terminal_at TEXT,
	expires_at TEXT NOT NULL,
	tombstoned_at TEXT,
	CHECK((dek_wrap_nonce IS NULL) = (wrapped_dek IS NULL)),
	CHECK((tombstoned_at IS NULL AND dek_wrap_nonce IS NOT NULL) OR (tombstoned_at IS NOT NULL AND dek_wrap_nonce IS NULL)),
	CHECK((status IN ('completed','failed','cancelled','interrupted') AND terminal_at IS NOT NULL) OR (status NOT IN ('completed','failed','cancelled','interrupted') AND terminal_at IS NULL)),
	CHECK(background = 1 OR status NOT IN ('queued','dispatch_authorized','in_progress')),
	CHECK(store_body = 1 OR background = 1),
	UNIQUE(employee_id,key_id,operation_id)
)`

const responseResourceItemsDDL = `CREATE TABLE response_resource_items (
	response_id TEXT NOT NULL REFERENCES response_resources(id) ON DELETE CASCADE,
	sequence INTEGER NOT NULL CHECK(sequence BETWEEN 0 AND 1000000),
	item_type TEXT NOT NULL CHECK(item_type IN ('instructions','message','function_call','function_call_output')),
	schema_version INTEGER NOT NULL CHECK(schema_version = 1),
	nonce BLOB NOT NULL CHECK(length(nonce) = 12),
	ciphertext BLOB NOT NULL CHECK(length(ciphertext) BETWEEN 17 AND 4194320),
	created_at TEXT NOT NULL,
	PRIMARY KEY(response_id,sequence)
)`

const backgroundTasksDDL = `CREATE TABLE background_tasks (
	id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 1 AND 128),
	response_id TEXT NOT NULL UNIQUE REFERENCES response_resources(id) ON DELETE RESTRICT,
	request_id TEXT NOT NULL UNIQUE CHECK(length(request_id) BETWEEN 1 AND 128),
	attempt_id TEXT UNIQUE CHECK(attempt_id IS NULL OR length(attempt_id) BETWEEN 1 AND 128),
	dispatch_operation_id TEXT UNIQUE CHECK(dispatch_operation_id IS NULL OR length(dispatch_operation_id) BETWEEN 1 AND 128),
	employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE RESTRICT,
	key_id TEXT NOT NULL REFERENCES access_keys(id) ON DELETE RESTRICT,
	public_model TEXT NOT NULL REFERENCES models(id) ON DELETE RESTRICT CHECK(length(public_model) BETWEEN 1 AND 128),
	provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','codex-membership')),
	account_id TEXT REFERENCES upstreams(id) ON DELETE RESTRICT,
	account_revision INTEGER CHECK(account_revision IS NULL OR account_revision BETWEEN 1 AND 9007199254740991),
	status TEXT NOT NULL CHECK(status IN ('queued','dispatch_authorized','in_progress','completed','failed','cancelled','interrupted')),
	revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
	claim_token TEXT UNIQUE CHECK(claim_token IS NULL OR length(claim_token) BETWEEN 1 AND 128),
	claimed_at TEXT,
	cancel_requested_at TEXT,
	dispatch_authorized_at TEXT,
	started_at TEXT,
	finished_at TEXT,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	CHECK((claim_token IS NULL) = (claimed_at IS NULL)),
	CHECK((status IN ('completed','failed','cancelled','interrupted') AND finished_at IS NOT NULL) OR (status NOT IN ('completed','failed','cancelled','interrupted') AND finished_at IS NULL)),
	CHECK((dispatch_authorized_at IS NULL AND attempt_id IS NULL AND dispatch_operation_id IS NULL) OR (dispatch_authorized_at IS NOT NULL AND attempt_id IS NOT NULL AND dispatch_operation_id IS NOT NULL)),
	CHECK((account_id IS NULL) = (account_revision IS NULL))
)`

const managedToolRunsDDL = `CREATE TABLE managed_tool_runs (
	id TEXT PRIMARY KEY CHECK(length(id) BETWEEN 1 AND 128),
	response_id TEXT NOT NULL REFERENCES response_resources(id) ON DELETE RESTRICT,
	task_id TEXT REFERENCES background_tasks(id) ON DELETE RESTRICT,
	request_id TEXT NOT NULL CHECK(length(request_id) BETWEEN 1 AND 128),
	attempt_id TEXT UNIQUE CHECK(attempt_id IS NULL OR length(attempt_id) BETWEEN 1 AND 128),
	employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE RESTRICT,
	key_id TEXT NOT NULL REFERENCES access_keys(id) ON DELETE RESTRICT,
	provider_kind TEXT NOT NULL CHECK(length(provider_kind) BETWEEN 1 AND 64),
	effective_model TEXT NOT NULL CHECK(length(effective_model) BETWEEN 1 AND 256),
	tool_type TEXT NOT NULL CHECK(length(tool_type) BETWEEN 1 AND 64),
	whitelist_revision INTEGER NOT NULL CHECK(whitelist_revision BETWEEN 1 AND 9007199254740991),
	status TEXT NOT NULL CHECK(status IN ('queued','dispatch_authorized','in_progress','completed','failed','cancelled','interrupted')),
	revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	dispatched_at TEXT,
	finished_at TEXT,
	CHECK((status IN ('completed','failed','cancelled','interrupted') AND finished_at IS NOT NULL) OR (status NOT IN ('completed','failed','cancelled','interrupted') AND finished_at IS NULL))
)`

const responseResourcesOwnerIndexDDL = `CREATE INDEX response_resources_owner_idx ON response_resources(employee_id,key_id,created_at,id)`
const responseResourcesExpiryIndexDDL = `CREATE INDEX response_resources_expiry_idx ON response_resources(status,expires_at,id)`
const backgroundTasksQueueIndexDDL = `CREATE INDEX background_tasks_queue_idx ON background_tasks(status,created_at,id)`
const managedToolRunsResponseIndexDDL = `CREATE INDEX managed_tool_runs_response_idx ON managed_tool_runs(response_id,created_at,id)`

var responseResourceSchemaObjects = map[string]string{
	"response_resources":             responseResourcesDDL,
	"response_resource_items":        responseResourceItemsDDL,
	"background_tasks":               backgroundTasksDDL,
	"managed_tool_runs":              managedToolRunsDDL,
	"response_resources_owner_idx":   responseResourcesOwnerIndexDDL,
	"response_resources_expiry_idx":  responseResourcesExpiryIndexDDL,
	"background_tasks_queue_idx":     backgroundTasksQueueIndexDDL,
	"managed_tool_runs_response_idx": managedToolRunsResponseIndexDDL,
}

func migrateResponseResources(ctx context.Context, db *sql.DB) error {
	if ctx == nil || db == nil {
		return errResponseStateUnavailable
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errResponseStateUnavailable
	}
	defer tx.Rollback()
	for _, ddl := range []string{
		responseResourcesDDL,
		responseResourceItemsDDL,
		backgroundTasksDDL,
		managedToolRunsDDL,
		responseResourcesOwnerIndexDDL,
		responseResourcesExpiryIndexDDL,
		backgroundTasksQueueIndexDDL,
		managedToolRunsResponseIndexDDL,
	} {
		statement := ddl
		if strings.HasPrefix(ddl, "CREATE TABLE ") {
			statement = strings.Replace(ddl, "CREATE TABLE ", "CREATE TABLE IF NOT EXISTS ", 1)
		} else {
			statement = strings.Replace(ddl, "CREATE INDEX ", "CREATE INDEX IF NOT EXISTS ", 1)
		}
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return errResponseStateUnavailable
		}
	}
	if err := validateResponseResourceSchema(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errResponseStateUnavailable
	}
	return nil
}

func validateResponseResourceSchema(ctx context.Context, tx *sql.Tx) error {
	for name, expected := range responseResourceSchemaObjects {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil {
			return errResponseStateUnavailable
		}
		expectedKind := "table"
		if strings.HasPrefix(expected, "CREATE INDEX ") {
			expectedKind = "index"
		}
		if kind != expectedKind || normalizeResponseResourceSQL(actual) != normalizeResponseResourceSQL(expected) {
			return errResponseStateUnavailable
		}
	}
	for table, indexes := range map[string]map[string]bool{
		"response_resources":      {"response_resources_owner_idx": true, "response_resources_expiry_idx": true},
		"response_resource_items": {},
		"background_tasks":        {"background_tasks_queue_idx": true},
		"managed_tool_runs":       {"managed_tool_runs_response_idx": true},
	} {
		rows, err := tx.QueryContext(ctx, `PRAGMA index_list(`+table+`)`)
		if err != nil {
			return errResponseStateUnavailable
		}
		seen := make(map[string]bool)
		for rows.Next() {
			var sequence, unique, partial int
			var name, origin string
			if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
				rows.Close()
				return errResponseStateUnavailable
			}
			if origin == "c" {
				if !indexes[name] || seen[name] || partial != 0 {
					rows.Close()
					return errResponseStateUnavailable
				}
				seen[name] = true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return errResponseStateUnavailable
		}
		if err := rows.Close(); err != nil || len(seen) != len(indexes) {
			return errResponseStateUnavailable
		}
	}
	return nil
}

func normalizeResponseResourceSQL(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSuffix(value, ";")), ""))
}

type responseDEKBinding struct {
	employeeID string
	keyID      string
	responseID string
}

type responseItemBinding struct {
	responseDEKBinding
	sequence int64
	itemType string
}

func validResponseDEKBinding(value responseDEKBinding) bool {
	return validIdentifier(value.employeeID, 128) && validIdentifier(value.keyID, 128) && validIdentifier(value.responseID, 128)
}

func validResponseItemBinding(value responseItemBinding) bool {
	if !validResponseDEKBinding(value.responseDEKBinding) || value.sequence < 0 || value.sequence > 1000000 {
		return false
	}
	switch value.itemType {
	case "instructions", "message", "function_call", "function_call_output":
		return true
	default:
		return false
	}
}

func responseDEKWrapAAD(value responseDEKBinding) []byte {
	return []byte(fmt.Sprintf("cpacloud/response-state-wrap/v1\x00%d\x00%s\x00%s\x00%s", responseStateSchemaVersion, value.employeeID, value.keyID, value.responseID))
}

func responseItemAAD(value responseItemBinding) []byte {
	return []byte(fmt.Sprintf("cpacloud/response-state-item/v1\x00%d\x00%s\x00%s\x00%s\x00%d\x00%s", responseStateSchemaVersion, value.employeeID, value.keyID, value.responseID, value.sequence, value.itemType))
}

func newResponseDEK() ([]byte, error) {
	dek := make([]byte, 32)
	if _, err := rand.Read(dek); err != nil {
		return nil, errResponseStateUnavailable
	}
	return dek, nil
}

func (s *secrets) wrapResponseDEK(binding responseDEKBinding, dek []byte) ([]byte, []byte, error) {
	if s == nil || s.responseWrapAEAD == nil || !validResponseDEKBinding(binding) || len(dek) != 32 {
		return nil, nil, errResponseStateUnavailable
	}
	nonce := make([]byte, s.responseWrapAEAD.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, errResponseStateUnavailable
	}
	return nonce, s.responseWrapAEAD.Seal(nil, nonce, dek, responseDEKWrapAAD(binding)), nil
}

func (s *secrets) unwrapResponseDEK(binding responseDEKBinding, nonce, ciphertext []byte) ([]byte, error) {
	if s == nil || s.responseWrapAEAD == nil || !validResponseDEKBinding(binding) || len(nonce) != s.responseWrapAEAD.NonceSize() || len(ciphertext) != 32+s.responseWrapAEAD.Overhead() {
		return nil, errResponseStateUnavailable
	}
	dek, err := s.responseWrapAEAD.Open(nil, nonce, ciphertext, responseDEKWrapAAD(binding))
	if err != nil || len(dek) != 32 {
		clear(dek)
		return nil, errResponseStateUnavailable
	}
	return dek, nil
}

func sealResponseItem(dek []byte, binding responseItemBinding, plaintext []byte) ([]byte, []byte, error) {
	if len(dek) != 32 || !validResponseItemBinding(binding) || len(plaintext) == 0 || len(plaintext) > responseStateMaxPlaintext {
		return nil, nil, errResponseStateUnavailable
	}
	aead, err := responseItemAEAD(dek)
	if err != nil {
		return nil, nil, errResponseStateUnavailable
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, errResponseStateUnavailable
	}
	return nonce, aead.Seal(nil, nonce, plaintext, responseItemAAD(binding)), nil
}

func openResponseItem(dek []byte, binding responseItemBinding, nonce, ciphertext []byte) ([]byte, error) {
	if len(dek) != 32 || !validResponseItemBinding(binding) || len(ciphertext) < 17 || len(ciphertext) > responseStateMaxPlaintext+16 {
		return nil, errResponseStateUnavailable
	}
	aead, err := responseItemAEAD(dek)
	if err != nil || len(nonce) != aead.NonceSize() {
		return nil, errResponseStateUnavailable
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, responseItemAAD(binding))
	if err != nil || len(plaintext) == 0 || len(plaintext) > responseStateMaxPlaintext {
		clear(plaintext)
		return nil, errResponseStateUnavailable
	}
	return plaintext, nil
}

func responseItemAEAD(dek []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
