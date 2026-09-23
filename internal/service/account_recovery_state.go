package service

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

var errAccountRecoveryConflict = errors.New("account recovery state conflict")

const (
	accountRecoveryStateTable             = "account_recovery_states"
	accountRecoveryDueIndex               = "account_recovery_states_due_idx"
	recoveryRequired                      = "required"
	recoveryInProgress                    = "in_progress"
	recoveryInterrupted                   = "interrupted"
	recoveryAttentionHistoryFull          = "history_full"
	recoveryAttentionRetryLimit           = "retry_limit"
	recoveryAttentionAuthentication       = "authentication_required"
	recoveryAttentionProtocol             = "protocol_error"
	recoveryAttentionUnsupported          = "unsupported"
	recoveryAttentionConfigurationChanged = "configuration_changed"
	recoveryAttentionSettlementPending    = "settlement_pending"
	recoveryAttentionStorageUnavailable   = "storage_unavailable"
)

const accountRecoveryLegacyDDL = `CREATE TABLE IF NOT EXISTS account_recovery_states (
	account_id TEXT PRIMARY KEY REFERENCES upstreams(id) ON DELETE CASCADE,
	cooldown_event_id TEXT NOT NULL UNIQUE,
	operation_id TEXT NOT NULL UNIQUE,
	recovery_revision INTEGER NOT NULL CHECK(recovery_revision >= 1),
	pool_revision INTEGER NOT NULL CHECK(pool_revision >= 1),
	account_revision INTEGER NOT NULL CHECK(account_revision >= 1),
	provider_kind TEXT NOT NULL,
	source_snapshot TEXT NOT NULL CHECK(source_snapshot IN ('api_key','import','authorization_code')),
	client_id TEXT,
	public_model TEXT NOT NULL REFERENCES models(id),
	upstream_model TEXT NOT NULL,
	protocol TEXT NOT NULL,
	state TEXT NOT NULL CHECK(state IN ('required','in_progress','interrupted')),
	next_probe_at TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK(length(account_id) BETWEEN 1 AND 128),
	CHECK(length(cooldown_event_id) BETWEEN 1 AND 128),
	CHECK(length(operation_id) BETWEEN 1 AND 128),
	CHECK(length(provider_kind) BETWEEN 1 AND 64),
	CHECK(length(public_model) BETWEEN 1 AND 128),
	CHECK(length(upstream_model) BETWEEN 1 AND 256),
	CHECK(length(protocol) BETWEEN 1 AND 64),
	CHECK((source_snapshot='authorization_code' AND client_id IS NOT NULL AND length(client_id) BETWEEN 1 AND 256) OR (source_snapshot<>'authorization_code' AND client_id IS NULL))
)`

const accountRecoveryStateDDL = `CREATE TABLE IF NOT EXISTS account_recovery_states (
	account_id TEXT PRIMARY KEY REFERENCES upstreams(id) ON DELETE CASCADE,
	cooldown_event_id TEXT NOT NULL UNIQUE,
	operation_id TEXT NOT NULL UNIQUE,
	recovery_revision INTEGER NOT NULL CHECK(recovery_revision >= 1),
	pool_revision INTEGER NOT NULL CHECK(pool_revision >= 1),
	account_revision INTEGER NOT NULL CHECK(account_revision >= 1),
	provider_kind TEXT NOT NULL,
	source_snapshot TEXT NOT NULL CHECK(source_snapshot IN ('api_key','import','authorization_code')),
	client_id TEXT,
	public_model TEXT NOT NULL REFERENCES models(id),
	upstream_model TEXT NOT NULL,
	protocol TEXT NOT NULL,
	state TEXT NOT NULL CHECK(state IN ('required','in_progress','interrupted')),
	attention_code TEXT CHECK(attention_code IS NULL OR attention_code IN ('history_full','retry_limit','authentication_required','protocol_error','unsupported','configuration_changed','settlement_pending','storage_unavailable')),
	checked_at TEXT,
	next_probe_at TEXT NOT NULL,
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK(length(account_id) BETWEEN 1 AND 128),
	CHECK(length(cooldown_event_id) BETWEEN 1 AND 128),
	CHECK(length(operation_id) BETWEEN 1 AND 128),
	CHECK(length(provider_kind) BETWEEN 1 AND 64),
	CHECK(length(public_model) BETWEEN 1 AND 128),
	CHECK(length(upstream_model) BETWEEN 1 AND 256),
	CHECK(length(protocol) BETWEEN 1 AND 64),
	CHECK((source_snapshot='authorization_code' AND client_id IS NOT NULL AND length(client_id) BETWEEN 1 AND 256) OR (source_snapshot<>'authorization_code' AND client_id IS NULL))
)`

const accountRecoveryDueIndexDDL = `CREATE INDEX account_recovery_states_due_idx ON account_recovery_states(state,attention_code,next_probe_at,account_id)`

type accountRecoveryState struct {
	AccountID        string
	CooldownEventID  string
	OperationID      string
	RecoveryRevision int64
	PoolRevision     int64
	AccountRevision  int64
	ProviderKind     string
	SourceSnapshot   string
	ClientID         string
	PublicModel      string
	UpstreamModel    string
	Protocol         string
	State            string
	AttentionCode    string
	CheckedAt        time.Time
	NextProbeAt      time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func migrateAccountRecoveryStateTx(ctx context.Context, tx *sql.Tx) error {
	if err := migrateAccountRecoveryStateSchemaTx(ctx, tx); err != nil {
		return err
	}
	if err := migrateAccountRecoverySettingsTx(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, accountRecoverySelect+` ORDER BY account_id`)
	if err != nil {
		return err
	}
	items := make([]accountRecoveryState, 0)
	for rows.Next() {
		item, scanErr := scanAccountRecoveryState(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, item)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, item := range items {
		if err := validatePersistedRecoveryAnchorTx(ctx, tx, item); err != nil {
			return err
		}
	}
	return nil
}

func migrateAccountRecoveryStateSchemaTx(ctx context.Context, tx *sql.Tx) error {
	var objectType, ddl string
	err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, accountRecoveryStateTable).Scan(&objectType, &ddl)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, accountRecoveryStateDDL); err != nil {
			return err
		}
	case err != nil:
		return err
	case objectType != "table":
		return errors.New("account recovery schema is incompatible")
	case normalizeRecoveryDDL(ddl) == normalizeRecoveryDDL(accountRecoveryLegacyDDL):
		if err := verifyCanonicalTable(ctx, tx, accountRecoveryStateTable, accountRecoveryLegacyDDL); err != nil {
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name='account_recovery_states_legacy'`).Scan(&count); err != nil || count != 0 {
			if err != nil {
				return err
			}
			return errors.New("account recovery legacy migration object exists")
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE account_recovery_states RENAME TO account_recovery_states_legacy`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, accountRecoveryStateDDL); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_recovery_states(account_id,cooldown_event_id,operation_id,recovery_revision,pool_revision,account_revision,provider_kind,source_snapshot,client_id,public_model,upstream_model,protocol,state,attention_code,checked_at,next_probe_at,created_at,updated_at)
			SELECT account_id,cooldown_event_id,operation_id,recovery_revision,pool_revision,account_revision,provider_kind,source_snapshot,client_id,public_model,upstream_model,protocol,state,NULL,NULL,next_probe_at,created_at,updated_at FROM account_recovery_states_legacy`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE account_recovery_states_legacy`); err != nil {
			return err
		}
	case normalizeRecoveryDDL(ddl) != normalizeRecoveryDDL(accountRecoveryStateDDL):
		return errors.New("account recovery schema is incompatible")
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS account_recovery_states_due_idx ON account_recovery_states(state,attention_code,next_probe_at,account_id)`); err != nil {
		return err
	}
	return verifyRecoveryStateTable(ctx, tx)
}

func verifyRecoveryStateTable(ctx context.Context, tx *sql.Tx) error {
	var objectType, ddl string
	if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, accountRecoveryStateTable).Scan(&objectType, &ddl); err != nil {
		return err
	}
	if objectType != "table" || normalizeRecoveryDDL(ddl) != normalizeRecoveryDDL(accountRecoveryStateDDL) {
		return errors.New("account recovery schema is incompatible")
	}
	var indexType, indexDDL string
	if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, accountRecoveryDueIndex).Scan(&indexType, &indexDDL); err != nil {
		return err
	}
	if indexType != "index" || normalizeRecoveryDDL(indexDDL) != normalizeRecoveryDDL(accountRecoveryDueIndexDDL) {
		return errors.New("account recovery due index is incompatible")
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA index_list(account_recovery_states)`)
	if err != nil {
		return err
	}
	seenDue := false
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return err
		}
		if origin == "c" {
			if name != accountRecoveryDueIndex || unique != 0 || partial != 0 || seenDue {
				rows.Close()
				return errors.New("account recovery schema has an unexpected index")
			}
			seenDue = true
		}
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !seenDue {
		return errors.New("account recovery due index is missing")
	}
	return nil
}

func verifyCanonicalTable(ctx context.Context, tx *sql.Tx, name, expected string) error {
	var objectType, ddl string
	if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&objectType, &ddl); err != nil {
		return err
	}
	if objectType != "table" || normalizeRecoveryDDL(ddl) != normalizeRecoveryDDL(expected) {
		return errors.New("account recovery schema is incompatible")
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA index_list(`+name+`)`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var sequence, unique, partial int
		var indexName, origin string
		if err := rows.Scan(&sequence, &indexName, &unique, &origin, &partial); err != nil {
			rows.Close()
			return err
		}
		if origin == "c" {
			rows.Close()
			return errors.New("account recovery schema has an unexpected index")
		}
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func normalizeRecoveryDDL(value string) string {
	value = normalizeCooldownDDL(value)
	return strings.Replace(value, "create table if not exists ", "create table ", 1)
}

func validRecoveryState(item accountRecoveryState) bool {
	if !validIdentifier(item.AccountID, 128) || !validIdentifier(item.CooldownEventID, 128) || !validIdentifier(item.OperationID, 128) ||
		item.RecoveryRevision < 1 || item.PoolRevision < 1 || item.AccountRevision < 1 ||
		!validIdentifier(item.ProviderKind, 64) || !validIdentifier(item.PublicModel, 128) ||
		strings.TrimSpace(item.UpstreamModel) == "" || len(item.UpstreamModel) > 256 || !validIdentifier(item.Protocol, 64) ||
		!validRecoveryProviderProtocol(item.ProviderKind, item.Protocol) ||
		(item.State != recoveryRequired && item.State != recoveryInProgress && item.State != recoveryInterrupted) ||
		!validRecoveryAttention(item.AttentionCode) ||
		item.NextProbeAt.IsZero() || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() || item.UpdatedAt.Before(item.CreatedAt) {
		return false
	}
	switch item.SourceSnapshot {
	case "authorization_code":
		return item.ProviderKind == codexMembershipProvider && item.ClientID != "" && len(item.ClientID) <= 256
	case "import":
		return item.ProviderKind == codexMembershipProvider && item.ClientID == ""
	case "api_key":
		return item.ProviderKind != codexMembershipProvider && item.ClientID == ""
	default:
		return false
	}
}

func validRecoveryAttention(code string) bool {
	switch code {
	case "", recoveryAttentionHistoryFull, recoveryAttentionRetryLimit, recoveryAttentionAuthentication,
		recoveryAttentionProtocol, recoveryAttentionUnsupported, recoveryAttentionConfigurationChanged,
		recoveryAttentionSettlementPending, recoveryAttentionStorageUnavailable:
		return true
	default:
		return false
	}
}

func validRecoveryProviderProtocol(provider, protocol string) bool {
	switch provider {
	case "openai-compatible", codexMembershipProvider:
		return protocol == "openai-chat-completions" || protocol == "openai-responses"
	case anthropicAPIKeyProvider:
		return protocol == "anthropic-messages"
	case geminiAPIKeyProvider:
		return protocol == "gemini-generate-content"
	default:
		return false
	}
}

// putAccountRecoveryStateTx is the persistence seam used by the later
// coordinator. It never commits and therefore can share the cooldown event
// transaction which creates or advances the isolation.
func putAccountRecoveryStateTx(ctx context.Context, tx *sql.Tx, item accountRecoveryState) error {
	if tx == nil || !validRecoveryState(item) {
		return errors.New("invalid account recovery state")
	}
	if item.State != recoveryRequired {
		return errors.New("new account recovery operation must be required")
	}
	var currentRevision int64
	var currentOperation string
	err := tx.QueryRowContext(ctx, `SELECT recovery_revision,operation_id FROM account_recovery_states WHERE account_id=?`, item.AccountID).Scan(&currentRevision, &currentOperation)
	if errors.Is(err, sql.ErrNoRows) {
		if item.RecoveryRevision != 1 {
			return errAccountRecoveryConflict
		}
	} else if err != nil {
		return err
	} else if currentRevision == 0 || currentRevision == int64(^uint64(0)>>1) || item.RecoveryRevision != currentRevision+1 || item.OperationID == currentOperation {
		return errAccountRecoveryConflict
	}
	if err := validateAccountRecoveryRelationsTx(ctx, tx, item); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `INSERT INTO account_recovery_states(
		account_id,cooldown_event_id,operation_id,recovery_revision,pool_revision,account_revision,provider_kind,source_snapshot,client_id,public_model,upstream_model,protocol,state,attention_code,checked_at,next_probe_at,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
	ON CONFLICT(account_id) DO UPDATE SET
		cooldown_event_id=excluded.cooldown_event_id,operation_id=excluded.operation_id,recovery_revision=excluded.recovery_revision,
		pool_revision=excluded.pool_revision,account_revision=excluded.account_revision,provider_kind=excluded.provider_kind,
		source_snapshot=excluded.source_snapshot,client_id=excluded.client_id,public_model=excluded.public_model,
		upstream_model=excluded.upstream_model,protocol=excluded.protocol,state=excluded.state,attention_code=excluded.attention_code,checked_at=excluded.checked_at,next_probe_at=excluded.next_probe_at,updated_at=excluded.updated_at
	WHERE account_recovery_states.recovery_revision + 1 = excluded.recovery_revision`,
		item.AccountID, item.CooldownEventID, item.OperationID, item.RecoveryRevision, item.PoolRevision, item.AccountRevision,
		item.ProviderKind, item.SourceSnapshot, nullableRecoveryClient(item), item.PublicModel, item.UpstreamModel, item.Protocol,
		item.State, nullableRecoveryString(item.AttentionCode), nullableRecoveryTime(item.CheckedAt), formatAccountPoolTime(item.NextProbeAt), formatAccountPoolTime(item.CreatedAt), formatAccountPoolTime(item.UpdatedAt))
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errAccountRecoveryConflict
	}
	return nil
}

func loadAccountRecoveryStateTx(ctx context.Context, tx *sql.Tx, accountID string) (accountRecoveryState, error) {
	if tx == nil || !validIdentifier(accountID, 128) {
		return accountRecoveryState{}, errors.New("invalid account recovery lookup")
	}
	return scanAccountRecoveryState(tx.QueryRowContext(ctx, accountRecoverySelect+` WHERE account_id=?`, accountID))
}

func validateAccountRecoveryRelationsTx(ctx context.Context, tx *sql.Tx, item accountRecoveryState) error {
	if err := validatePersistedRecoveryAnchorTx(ctx, tx, item); err != nil {
		return err
	}
	var provider, upstreamModel string
	var accountRevision, poolRevision, enabled int64
	if err := tx.QueryRowContext(ctx, `SELECT u.provider_kind,u.revision,u.enabled,c.revision,r.upstream_model
		FROM upstreams u JOIN model_account_pool_routes r ON r.upstream_id=u.id JOIN model_account_pool_configs c ON c.model_id=r.model_id
		JOIN models m ON m.id=r.model_id WHERE u.id=? AND r.model_id=? AND m.enabled=1`, item.AccountID, item.PublicModel).Scan(&provider, &accountRevision, &enabled, &poolRevision, &upstreamModel); err != nil {
		return err
	}
	if enabled != 1 || provider != item.ProviderKind || accountRevision != item.AccountRevision || poolRevision != item.PoolRevision || upstreamModel != item.UpstreamModel {
		return errAccountRecoveryConflict
	}
	if code := validateRecoverySourceTx(ctx, tx, item); code != "" {
		if code == accountPoolStorageUnavailable {
			return errors.New("account recovery source validation failed")
		}
		return errAccountRecoveryConflict
	}
	return nil
}

func validatePersistedRecoveryAnchorTx(ctx context.Context, tx *sql.Tx, item accountRecoveryState) error {
	var eventID string
	if err := tx.QueryRowContext(ctx, `SELECT event_id FROM account_pool_runtime_cooldowns WHERE account_id=?`, item.AccountID).Scan(&eventID); err != nil {
		return err
	}
	if eventID != item.CooldownEventID {
		return errAccountRecoveryConflict
	}
	return nil
}

func nullableRecoveryClient(item accountRecoveryState) any {
	if item.ClientID == "" {
		return nil
	}
	return item.ClientID
}

func nullableRecoveryString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func nullableRecoveryTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return formatAccountPoolTime(value)
}

func scanAccountRecoveryState(scanner interface{ Scan(...any) error }) (accountRecoveryState, error) {
	var item accountRecoveryState
	var client sql.NullString
	var attention, checked sql.NullString
	var nextProbe, created, updated string
	err := scanner.Scan(&item.AccountID, &item.CooldownEventID, &item.OperationID, &item.RecoveryRevision, &item.PoolRevision,
		&item.AccountRevision, &item.ProviderKind, &item.SourceSnapshot, &client, &item.PublicModel, &item.UpstreamModel,
		&item.Protocol, &item.State, &attention, &checked, &nextProbe, &created, &updated)
	if err != nil {
		return item, err
	}
	item.ClientID = client.String
	item.AttentionCode = attention.String
	if checked.Valid {
		if item.CheckedAt, err = parseTime(checked.String); err != nil || checked.String != formatAccountPoolTime(item.CheckedAt) {
			return item, errors.New("invalid stored recovery checked timestamp")
		}
	}
	if item.NextProbeAt, err = parseTime(nextProbe); err != nil {
		return item, err
	}
	if nextProbe != formatAccountPoolTime(item.NextProbeAt) {
		return item, errors.New("invalid stored recovery next probe timestamp")
	}
	if item.CreatedAt, err = parseTime(created); err != nil {
		return item, err
	}
	if created != formatAccountPoolTime(item.CreatedAt) {
		return item, errors.New("invalid stored recovery created timestamp")
	}
	if item.UpdatedAt, err = parseTime(updated); err != nil {
		return item, err
	}
	if updated != formatAccountPoolTime(item.UpdatedAt) {
		return item, errors.New("invalid stored recovery updated timestamp")
	}
	if !validRecoveryState(item) {
		return item, errors.New("invalid stored account recovery state")
	}
	return item, nil
}

const accountRecoverySelect = `SELECT account_id,cooldown_event_id,operation_id,recovery_revision,pool_revision,account_revision,provider_kind,source_snapshot,client_id,public_model,upstream_model,protocol,state,attention_code,checked_at,next_probe_at,created_at,updated_at FROM account_recovery_states`
