package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
)

const (
	maxAccountGroups      = 1_000
	maxAccountChannels    = 1_000
	maxModelAccounts      = 64
	maxAccountPriority    = 1_000_000
	minAccountPriority    = -1_000_000
	maxAccountWeight      = 10_000
	maxAccountConcurrency = 1_024
	accountGroupTable     = "account_groups"
	accountChannelTable   = "account_channels"
	modelPoolConfigTable  = "model_account_pool_configs"
	modelPoolRouteTable   = "model_account_pool_routes"
	accountPoolAuditTable = "account_pool_audit"
)

type accountGroupView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
}

type accountChannelView struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	GroupID  *string `json:"group_id"`
	Revision int64   `json:"revision"`
}

type modelAccountView struct {
	UpstreamID     string  `json:"upstream_id"`
	UpstreamModel  string  `json:"upstream_model"`
	Priority       int     `json:"priority"`
	Weight         int     `json:"weight"`
	MaxConcurrency int     `json:"max_concurrency"`
	ChannelID      *string `json:"channel_id"`
	WireProtocol   *string `json:"wire_protocol,omitempty"`
}

type modelAccountsView struct {
	ModelID  string             `json:"model_id"`
	Revision int64              `json:"revision"`
	Items    []modelAccountView `json:"items"`
}

type createAccountGroupRequest struct {
	Name string `json:"name"`
}

type updateAccountGroupRequest struct {
	ExpectedRevision int64  `json:"expected_revision"`
	Name             string `json:"name"`
}

type createAccountChannelRequest struct {
	Name    string  `json:"name"`
	GroupID *string `json:"group_id"`
}

type putModelAccountsRequest struct {
	ExpectedRevision int64              `json:"expected_revision"`
	Items            []modelAccountView `json:"items"`
}

// registerAccountPoolHandlers is intentionally separate from App.Handler so
// the account-pool persistence batch can be integrated without coupling it to
// model execution. App.Handler must call this once on its private mux.
func (a *App) registerAccountPoolHandlers(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/api/v1/account-groups", a.requireAdmin(a.listAccountGroups, false))
	mux.HandleFunc("POST /admin/api/v1/account-groups", a.requireAdmin(a.createAccountGroup, true))
	mux.HandleFunc("PUT /admin/api/v1/account-groups/{id}", a.requireAdmin(a.updateAccountGroup, true))
	mux.HandleFunc("GET /admin/api/v1/channels", a.requireAdmin(a.listAccountChannels, false))
	mux.HandleFunc("POST /admin/api/v1/channels", a.requireAdmin(a.createAccountChannel, true))
	mux.HandleFunc("GET /admin/api/v1/models/{id}/accounts", a.requireAdmin(a.getModelAccounts, false))
	mux.HandleFunc("PUT /admin/api/v1/models/{id}/accounts", a.requireAdmin(a.putModelAccounts, true))
}

func (s *store) migrateAccountPools(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS account_groups (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			revision INTEGER NOT NULL CHECK(revision >= 1),
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS account_channels (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			group_id TEXT REFERENCES account_groups(id) ON DELETE SET NULL,
			revision INTEGER NOT NULL CHECK(revision >= 1),
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS model_account_pool_configs (
			model_id TEXT PRIMARY KEY REFERENCES models(id) ON DELETE CASCADE,
			revision INTEGER NOT NULL CHECK(revision >= 1),
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS model_account_pool_routes (
			model_id TEXT NOT NULL REFERENCES model_account_pool_configs(model_id) ON DELETE CASCADE,
			upstream_id TEXT NOT NULL REFERENCES upstreams(id),
			upstream_model TEXT NOT NULL,
			wire_protocol TEXT NOT NULL DEFAULT 'legacy-native' CHECK(wire_protocol IN ('legacy-native','openai-chat','openai-responses','anthropic-messages','gemini-generate-content')),
			priority INTEGER NOT NULL CHECK(priority BETWEEN -1000000 AND 1000000),
			weight INTEGER NOT NULL CHECK(weight BETWEEN 1 AND 10000),
			max_concurrency INTEGER NOT NULL CHECK(max_concurrency BETWEEN 1 AND 1024),
			channel_id TEXT REFERENCES account_channels(id) ON DELETE SET NULL,
			position INTEGER NOT NULL CHECK(position BETWEEN 0 AND 63),
			PRIMARY KEY(model_id, upstream_id),
			UNIQUE(model_id, position)
		)`,
		`CREATE TABLE IF NOT EXISTS account_pool_audit (
			id TEXT PRIMARY KEY,
			actor_id TEXT NOT NULL REFERENCES admins(id),
			action TEXT NOT NULL,
			target_type TEXT NOT NULL,
			target_id TEXT NOT NULL,
			result TEXT NOT NULL CHECK(result = 'succeeded'),
			occurred_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS account_channels_group_idx ON account_channels(group_id)`,
		`CREATE INDEX IF NOT EXISTS model_account_pool_routes_channel_idx ON model_account_pool_routes(channel_id)`,
		`CREATE INDEX IF NOT EXISTS account_pool_audit_time_idx ON account_pool_audit(occurred_at,id)`,
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	poolSchema, err := schemaColumns(ctx, tx, modelPoolRouteTable)
	if err != nil {
		return err
	}
	if _, ok := poolSchema["wire_protocol"]; !ok {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE model_account_pool_routes ADD COLUMN wire_protocol TEXT NOT NULL DEFAULT 'legacy-native' CHECK(wire_protocol IN ('legacy-native','openai-chat','openai-responses','anthropic-messages','gemini-generate-content'))`); err != nil {
			return err
		}
	}
	type tableRequirement struct {
		columns   []string
		fragments []string
	}
	required := map[string]tableRequirement{
		accountGroupTable: {
			columns:   []string{"id", "name", "revision", "created_at"},
			fragments: []string{"name text not null unique", "check(revision >= 1)"},
		},
		accountChannelTable: {
			columns:   []string{"id", "name", "group_id", "revision", "created_at"},
			fragments: []string{"name text not null unique", "references account_groups(id) on delete set null", "check(revision >= 1)"},
		},
		modelPoolConfigTable: {
			columns:   []string{"model_id", "revision", "updated_at"},
			fragments: []string{"references models(id) on delete cascade", "check(revision >= 1)"},
		},
		modelPoolRouteTable: {
			columns: []string{"model_id", "upstream_id", "upstream_model", "wire_protocol", "priority", "weight", "max_concurrency", "channel_id", "position"},
			fragments: []string{
				"references model_account_pool_configs(model_id) on delete cascade",
				"references upstreams(id)",
				"references account_channels(id) on delete set null",
				"primary key(model_id, upstream_id)",
				"unique(model_id, position)",
				"check(wire_protocol in ('legacy-native','openai-chat','openai-responses','anthropic-messages','gemini-generate-content'))",
			},
		},
		accountPoolAuditTable: {
			columns:   []string{"id", "actor_id", "action", "target_type", "target_id", "result", "occurred_at"},
			fragments: []string{"references admins(id)", "check(result = 'succeeded')"},
		},
	}
	for table, requirement := range required {
		if err := verifyAccountPoolTable(ctx, tx, table, requirement.columns, requirement.fragments); err != nil {
			return err
		}
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
		return errors.New("account pool migration foreign key check failed")
	}
	if closeErr != nil {
		return closeErr
	}
	return tx.Commit()
}

func verifyAccountPoolTable(ctx context.Context, tx *sql.Tx, table string, required, fragments []string) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return err
	}
	columns := make(map[string]bool)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		columns[name] = true
	}
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	for _, column := range required {
		if !columns[column] {
			return fmt.Errorf("existing %s table has an incompatible schema", table)
		}
	}
	if len(columns) != len(required) {
		return fmt.Errorf("existing %s table has an incompatible schema", table)
	}
	var schema string
	if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&schema); err != nil {
		return err
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(schema), " "))
	for _, fragment := range fragments {
		if !strings.Contains(normalized, fragment) {
			return fmt.Errorf("existing %s table has an incompatible schema", table)
		}
	}
	return nil
}

func (a *App) listAccountGroups(w http.ResponseWriter, r *http.Request, _ adminSession) {
	rows, err := a.store.db.QueryContext(r.Context(), `SELECT id,name,revision FROM account_groups ORDER BY created_at,id LIMIT ?`, maxAccountGroups+1)
	if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	defer rows.Close()
	items := make([]accountGroupView, 0)
	for rows.Next() {
		if len(items) == maxAccountGroups {
			writeAdminError(w, http.StatusConflict, "read_limit_exceeded", "Too many account groups to return.")
			return
		}
		var item accountGroupView
		if err := rows.Scan(&item.ID, &item.Name, &item.Revision); err != nil {
			writeAccountPoolStorageError(w)
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) createAccountGroup(w http.ResponseWriter, r *http.Request, session adminSession) {
	var input createAccountGroupRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validText(input.Name, 1, 120) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid account group fields.")
		return
	}
	id, err := newID("grp")
	if err != nil {
		writeAccountPoolServiceError(w)
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	defer tx.Rollback()
	if limited, err := accountPoolTableFull(r.Context(), tx, accountGroupTable, maxAccountGroups); err != nil {
		writeAccountPoolStorageError(w)
		return
	} else if limited {
		writeAdminError(w, http.StatusConflict, "resource_limit", "Account group limit reached.")
		return
	}
	item := accountGroupView{ID: id, Name: input.Name, Revision: 1}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO account_groups(id,name,revision,created_at) VALUES(?,?,?,?)`, item.ID, item.Name, item.Revision, utcNow()); err != nil {
		if isConflict(err) {
			writeAdminError(w, http.StatusConflict, "already_exists", "Account group already exists.")
		} else {
			writeAccountPoolStorageError(w)
		}
		return
	}
	if err := recordAccountPoolAudit(r.Context(), tx, session.AdminID, "account_group.create", "account_group", item.ID); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	if err := tx.Commit(); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *App) updateAccountGroup(w http.ResponseWriter, r *http.Request, session adminSession) {
	var input updateAccountGroupRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.ExpectedRevision < 1 || !validText(input.Name, 1, 120) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid account group fields.")
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRowContext(r.Context(), `SELECT revision FROM account_groups WHERE id=?`, r.PathValue("id")).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Account group was not found.")
		return
	} else if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	if current != input.ExpectedRevision {
		writeAccountPoolRevisionError(w)
		return
	}
	next := current + 1
	result, err := tx.ExecContext(r.Context(), `UPDATE account_groups SET name=?,revision=? WHERE id=? AND revision=?`, input.Name, next, r.PathValue("id"), current)
	if err != nil {
		if isConflict(err) {
			writeAdminError(w, http.StatusConflict, "already_exists", "Account group already exists.")
		} else {
			writeAccountPoolStorageError(w)
		}
		return
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		writeAccountPoolRevisionError(w)
		return
	}
	if err := recordAccountPoolAudit(r.Context(), tx, session.AdminID, "account_group.update", "account_group", r.PathValue("id")); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	if err := tx.Commit(); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	writeJSON(w, http.StatusOK, accountGroupView{ID: r.PathValue("id"), Name: input.Name, Revision: next})
}

func (a *App) listAccountChannels(w http.ResponseWriter, r *http.Request, _ adminSession) {
	rows, err := a.store.db.QueryContext(r.Context(), `SELECT id,name,group_id,revision FROM account_channels ORDER BY created_at,id LIMIT ?`, maxAccountChannels+1)
	if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	defer rows.Close()
	items := make([]accountChannelView, 0)
	for rows.Next() {
		if len(items) == maxAccountChannels {
			writeAdminError(w, http.StatusConflict, "read_limit_exceeded", "Too many channels to return.")
			return
		}
		var item accountChannelView
		var groupID sql.NullString
		if err := rows.Scan(&item.ID, &item.Name, &groupID, &item.Revision); err != nil {
			writeAccountPoolStorageError(w)
			return
		}
		item.GroupID = nullString(groupID)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (a *App) createAccountChannel(w http.ResponseWriter, r *http.Request, session adminSession) {
	var input createAccountChannelRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if !validText(input.Name, 1, 120) || (input.GroupID != nil && !validIdentifier(*input.GroupID, 128)) {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid channel fields.")
		return
	}
	id, err := newID("chn")
	if err != nil {
		writeAccountPoolServiceError(w)
		return
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	defer tx.Rollback()
	if limited, err := accountPoolTableFull(r.Context(), tx, accountChannelTable, maxAccountChannels); err != nil {
		writeAccountPoolStorageError(w)
		return
	} else if limited {
		writeAdminError(w, http.StatusConflict, "resource_limit", "Channel limit reached.")
		return
	}
	if input.GroupID != nil {
		var exists int
		if err := tx.QueryRowContext(r.Context(), `SELECT 1 FROM account_groups WHERE id=?`, *input.GroupID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "Channel refers to an unknown account group.")
			return
		} else if err != nil {
			writeAccountPoolStorageError(w)
			return
		}
	}
	item := accountChannelView{ID: id, Name: input.Name, GroupID: input.GroupID, Revision: 1}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO account_channels(id,name,group_id,revision,created_at) VALUES(?,?,?,?,?)`, item.ID, item.Name, nullableString(input.GroupID), item.Revision, utcNow()); err != nil {
		if isConflict(err) {
			writeAdminError(w, http.StatusConflict, "already_exists", "Channel already exists.")
		} else {
			writeAccountPoolStorageError(w)
		}
		return
	}
	if err := recordAccountPoolAudit(r.Context(), tx, session.AdminID, "channel.create", "channel", item.ID); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	if err := tx.Commit(); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (a *App) getModelAccounts(w http.ResponseWriter, r *http.Request, _ adminSession) {
	tx, err := a.store.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	defer tx.Rollback()
	view, err := loadModelAccounts(r.Context(), tx, r.PathValue("id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Model was not found.")
		return
	}
	if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	if err := tx.Commit(); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func (a *App) putModelAccounts(w http.ResponseWriter, r *http.Request, session adminSession) {
	var input putModelAccountsRequest
	if !decodeJSON(w, r, adminMaxBody, &input) {
		return
	}
	if input.ExpectedRevision < 0 || len(input.Items) < 1 || len(input.Items) > maxModelAccounts {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid model account configuration.")
		return
	}
	seen := make(map[string]struct{}, len(input.Items))
	for i := range input.Items {
		item := &input.Items[i]
		if !validIdentifier(item.UpstreamID, 128) || !validPoolModelName(item.UpstreamModel) || item.Priority < minAccountPriority || item.Priority > maxAccountPriority || item.Weight < 1 || item.Weight > maxAccountWeight || item.MaxConcurrency < 1 || item.MaxConcurrency > maxAccountConcurrency || (item.ChannelID != nil && !validIdentifier(*item.ChannelID, 128)) {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid model account configuration.")
			return
		}
		if _, duplicate := seen[item.UpstreamID]; duplicate {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "Duplicate upstream accounts are not allowed.")
			return
		}
		seen[item.UpstreamID] = struct{}{}
	}
	a.admission.Lock()
	defer a.admission.Unlock()
	tx, err := a.store.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	defer tx.Rollback()
	var modelExists int
	if err := tx.QueryRowContext(r.Context(), `SELECT 1 FROM models WHERE id=? AND archived=0`, r.PathValue("id")).Scan(&modelExists); errors.Is(err, sql.ErrNoRows) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Model was not found.")
		return
	} else if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	var current int64
	err = tx.QueryRowContext(r.Context(), `SELECT revision FROM model_account_pool_configs WHERE model_id=?`, r.PathValue("id")).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		current = 0
	} else if err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	if current != input.ExpectedRevision {
		writeAccountPoolRevisionError(w)
		return
	}
	existingWire := make(map[string]string)
	if current == 0 {
		var upstreamID, wire string
		if err := tx.QueryRowContext(r.Context(), `SELECT upstream_id,wire_protocol FROM models WHERE id=?`, r.PathValue("id")).Scan(&upstreamID, &wire); err != nil {
			writeAccountPoolStorageError(w)
			return
		}
		existingWire[upstreamID] = wire
	} else {
		rows, err := tx.QueryContext(r.Context(), `SELECT upstream_id,wire_protocol FROM model_account_pool_routes WHERE model_id=?`, r.PathValue("id"))
		if err != nil {
			writeAccountPoolStorageError(w)
			return
		}
		for rows.Next() {
			var upstreamID, wire string
			if err := rows.Scan(&upstreamID, &wire); err != nil {
				rows.Close()
				writeAccountPoolStorageError(w)
				return
			}
			existingWire[upstreamID] = wire
		}
		iterationErr, closeErr := rows.Err(), rows.Close()
		if iterationErr != nil || closeErr != nil {
			writeAccountPoolStorageError(w)
			return
		}
	}
	var provider, poolWire string
	for index := range input.Items {
		item := &input.Items[index]
		var itemProvider string
		if err := tx.QueryRowContext(r.Context(), `SELECT provider_kind FROM upstreams WHERE id=? AND archived=0`, item.UpstreamID).Scan(&itemProvider); errors.Is(err, sql.ErrNoRows) {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "Model account configuration refers to an unknown upstream.")
			return
		} else if err != nil {
			writeAccountPoolStorageError(w)
			return
		}
		if itemProvider == codexMembershipProvider && !a.cfg.ExperimentalCodexMembership {
			writeAdminError(w, http.StatusBadRequest, "unsupported_feature", "Codex membership account pools are disabled.")
			return
		}
		if !validProviderModelName(itemProvider, item.UpstreamModel) {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid upstream model name.")
			return
		}
		wire := string(wireProtocolLegacyNative)
		if item.WireProtocol != nil {
			wire = strings.TrimSpace(*item.WireProtocol)
		} else if saved, ok := existingWire[item.UpstreamID]; ok {
			wire = saved
		}
		if !validRouteWireProtocol(wire) || !providerSupportsWire(itemProvider, wire) {
			writeAdminError(w, http.StatusBadRequest, "invalid_request", "The wire protocol is not supported by that provider.")
			return
		}
		item.WireProtocol = &wire
		if provider == "" {
			provider = itemProvider
		} else if provider != itemProvider {
			writeAdminError(w, http.StatusBadRequest, "provider_mismatch", "All accounts in a model pool must use the same provider kind.")
			return
		}
		if poolWire == "" {
			poolWire = wire
		} else if poolWire != wire {
			writeAdminError(w, http.StatusBadRequest, "wire_protocol_mismatch", "All accounts in a model pool must use the same wire protocol.")
			return
		}
		if item.ChannelID != nil {
			var channelExists int
			if err := tx.QueryRowContext(r.Context(), `SELECT 1 FROM account_channels WHERE id=?`, *item.ChannelID).Scan(&channelExists); errors.Is(err, sql.ErrNoRows) {
				writeAdminError(w, http.StatusBadRequest, "invalid_request", "Model account configuration refers to an unknown channel.")
				return
			} else if err != nil {
				writeAccountPoolStorageError(w)
				return
			}
		}
	}
	next := current + 1
	if current == 0 {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES(?,?,?)`, r.PathValue("id"), next, utcNow()); err != nil {
			if isConflict(err) {
				writeAccountPoolRevisionError(w)
			} else {
				writeAccountPoolStorageError(w)
			}
			return
		}
	} else {
		result, err := tx.ExecContext(r.Context(), `UPDATE model_account_pool_configs SET revision=?,updated_at=? WHERE model_id=? AND revision=?`, next, utcNow(), r.PathValue("id"), current)
		if err != nil {
			writeAccountPoolStorageError(w)
			return
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			writeAccountPoolRevisionError(w)
			return
		}
		if _, err := tx.ExecContext(r.Context(), `DELETE FROM model_account_pool_routes WHERE model_id=?`, r.PathValue("id")); err != nil {
			writeAccountPoolStorageError(w)
			return
		}
	}
	for position, item := range input.Items {
		if _, err := tx.ExecContext(r.Context(), `INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,wire_protocol,priority,weight,max_concurrency,channel_id,position) VALUES(?,?,?,?,?,?,?,?,?)`, r.PathValue("id"), item.UpstreamID, item.UpstreamModel, *item.WireProtocol, item.Priority, item.Weight, item.MaxConcurrency, nullableString(item.ChannelID), position); err != nil {
			writeAccountPoolStorageError(w)
			return
		}
	}
	if err := recordAccountPoolAudit(r.Context(), tx, session.AdminID, "model_accounts.replace", "model", r.PathValue("id")); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	if err := tx.Commit(); err != nil {
		writeAccountPoolStorageError(w)
		return
	}
	a.notifyAccountPoolChanged()
	writeJSON(w, http.StatusOK, modelAccountsView{ModelID: r.PathValue("id"), Revision: next, Items: input.Items})
}

func loadModelAccounts(ctx context.Context, tx *sql.Tx, modelID string) (modelAccountsView, error) {
	var legacy modelAccountView
	var provider string
	var legacyWire string
	if err := tx.QueryRowContext(ctx, `SELECT m.upstream_id,m.upstream_model,m.wire_protocol,u.provider_kind FROM models m JOIN upstreams u ON u.id=m.upstream_id WHERE m.id=?`, modelID).Scan(&legacy.UpstreamID, &legacy.UpstreamModel, &legacyWire, &provider); err != nil {
		return modelAccountsView{}, err
	}
	legacy.WireProtocol = &legacyWire
	view := modelAccountsView{ModelID: modelID, Items: []modelAccountView{legacy}}
	legacy.Weight = 1
	legacy.MaxConcurrency = 1
	view.Items[0] = legacy
	err := tx.QueryRowContext(ctx, `SELECT revision FROM model_account_pool_configs WHERE model_id=?`, modelID).Scan(&view.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return view, nil
	}
	if err != nil {
		return modelAccountsView{}, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT upstream_id,upstream_model,wire_protocol,priority,weight,max_concurrency,channel_id FROM model_account_pool_routes WHERE model_id=? ORDER BY position LIMIT ?`, modelID, maxModelAccounts+1)
	if err != nil {
		return modelAccountsView{}, err
	}
	defer rows.Close()
	view.Items = make([]modelAccountView, 0)
	for rows.Next() {
		if len(view.Items) == maxModelAccounts {
			return modelAccountsView{}, errors.New("model account read limit exceeded")
		}
		var item modelAccountView
		var channelID sql.NullString
		var wire string
		if err := rows.Scan(&item.UpstreamID, &item.UpstreamModel, &wire, &item.Priority, &item.Weight, &item.MaxConcurrency, &channelID); err != nil {
			return modelAccountsView{}, err
		}
		item.WireProtocol = &wire
		item.ChannelID = nullString(channelID)
		view.Items = append(view.Items, item)
	}
	if err := rows.Err(); err != nil {
		return modelAccountsView{}, err
	}
	if len(view.Items) == 0 {
		return modelAccountsView{}, errors.New("model account configuration is empty")
	}
	return view, nil
}

func accountPoolTableFull(ctx context.Context, tx *sql.Tx, table string, limit int) (bool, error) {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&count); err != nil {
		return false, err
	}
	return count >= limit, nil
}

func recordAccountPoolAudit(ctx context.Context, tx *sql.Tx, actorID, action, targetType, targetID string) error {
	id, err := newID("aud")
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_pool_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,?,?,?,?,'succeeded',?)`, id, actorID, action, targetType, targetID, utcNow())
	return err
}

func validPoolModelName(value string) bool {
	if value != strings.TrimSpace(value) || !validText(value, 1, 256) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validProviderModelName(provider, model string) bool {
	if !validPoolModelName(model) {
		return false
	}
	if provider == geminiAPIKeyProvider {
		return validGeminiUpstreamModel(model)
	}
	return true
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func writeAccountPoolRevisionError(w http.ResponseWriter) {
	writeAdminError(w, http.StatusConflict, "revision_conflict", "The object was changed by another request.")
}

func writeAccountPoolStorageError(w http.ResponseWriter) {
	writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
}

func writeAccountPoolServiceError(w http.ResponseWriter) {
	writeAdminError(w, http.StatusServiceUnavailable, "service_unavailable", "Service is temporarily unavailable.")
}
