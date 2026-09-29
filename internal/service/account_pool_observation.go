// Independently authored from docs/account-pool-runtime-observation-contract.md.
// This read path uses only local SQLite metadata; it never reads credentials or identities.
package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"cpacloud.local/server/internal/scheduling"
)

const poolRuntimeObservationPath = "/admin/api/v1/models/{id}/pool-runtime"

var errPoolRuntimeModelNotFound = errors.New("pool runtime model not found")

type poolRuntimeObservationView struct {
	ModelID       string                        `json:"model_id"`
	ModelRevision int64                         `json:"model_revision"`
	PoolRevision  int64                         `json:"pool_revision"`
	PoolStatus    string                        `json:"pool_status"`
	AsOf          string                        `json:"as_of"`
	Items         []poolRuntimeObservationRoute `json:"items"`
}

type poolRuntimeObservationRoute struct {
	UpstreamID               string   `json:"upstream_id"`
	AccountRevision          int64    `json:"account_revision"`
	ConfiguredMaxConcurrency int      `json:"configured_max_concurrency"`
	GlobalMaxConcurrency     *int     `json:"global_max_concurrency"`
	RequestReservations      int      `json:"request_reservations"`
	MaintenanceReservations  int      `json:"maintenance_reservations"`
	RemainingLocalSlots      *int     `json:"remaining_local_slots"`
	BlockReasons             []string `json:"block_reasons"`
	CooldownUntil            *string  `json:"cooldown_until"`
}

type poolRuntimeModel struct {
	id        string
	revision  int64
	enabled   bool
	modelKind string
	poolRev   int64
}

type poolRuntimeAccount struct {
	poolRuntimeObservationRoute
	provider        string
	credentialState sql.NullString
	enabled         bool
	wire            string
	position        int
	priority        int
	weight          int
}

func (a *App) getModelPoolRuntime(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if a == nil || a.store == nil || a.store.db == nil || a.accountPool == nil {
		writePoolRuntimeStorageError(w)
		return
	}
	if !validOptionalOrigin(r, a.cfg.TLSCert != "") {
		writeAdminError(w, http.StatusForbidden, "origin_rejected", "Request origin is not allowed.")
		return
	}
	if r.URL.RawQuery != "" {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid pool runtime request.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	view, err := a.queryPoolRuntimeObservation(ctx, r.PathValue("id"))
	if errors.Is(err, errPoolRuntimeModelNotFound) {
		writeAdminError(w, http.StatusNotFound, "not_found", "Model was not found.")
		return
	}
	if err != nil {
		writePoolRuntimeStorageError(w)
		return
	}
	writeJSON(w, http.StatusOK, view)
}

func writePoolRuntimeStorageError(w http.ResponseWriter) {
	writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Pool runtime snapshot is temporarily unavailable.")
}

func (a *App) queryPoolRuntimeObservation(ctx context.Context, modelID string) (poolRuntimeObservationView, error) {
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return poolRuntimeObservationView{}, err
	}
	defer tx.Rollback()
	model, err := readPoolRuntimeModelTx(ctx, tx, modelID)
	if errors.Is(err, sql.ErrNoRows) {
		return poolRuntimeObservationView{}, errPoolRuntimeModelNotFound
	}
	if err != nil {
		return poolRuntimeObservationView{}, err
	}
	// The model SELECT anchors SQLite's read snapshot before this single clock read.
	asOf := a.accountPool.clock.Now().UTC()
	view, err := a.readPoolRuntimeObservationTx(ctx, tx, model, asOf)
	if err != nil {
		return poolRuntimeObservationView{}, err
	}
	if err := tx.Commit(); err != nil {
		return poolRuntimeObservationView{}, err
	}
	return view, nil
}

func readPoolRuntimeModelTx(ctx context.Context, tx *sql.Tx, modelID string) (poolRuntimeModel, error) {
	var model poolRuntimeModel
	var enabled, archived int
	err := tx.QueryRowContext(ctx, `SELECT revision,enabled,archived,model_kind FROM models WHERE id=?`, modelID).Scan(&model.revision, &enabled, &archived, &model.modelKind)
	if err != nil {
		return poolRuntimeModel{}, err
	}
	if !validIdentifier(modelID, 128) || model.revision < 1 || model.revision > 9_007_199_254_740_991 || (enabled != 0 && enabled != 1) || (archived != 0 && archived != 1) || (model.modelKind != "generation" && model.modelKind != "embedding") {
		return poolRuntimeModel{}, errors.New("invalid model metadata")
	}
	if archived == 1 {
		return poolRuntimeModel{}, sql.ErrNoRows
	}
	model.id, model.enabled = modelID, enabled == 1
	return model, nil
}

func (a *App) readPoolRuntimeObservationTx(ctx context.Context, tx *sql.Tx, model poolRuntimeModel, asOf time.Time) (poolRuntimeObservationView, error) {
	if err := validatePoolRuntimeObservationSchema(ctx, tx); err != nil {
		return poolRuntimeObservationView{}, err
	}
	view := poolRuntimeObservationView{ModelID: model.id, ModelRevision: model.revision, AsOf: asOf.Format(time.RFC3339Nano), Items: []poolRuntimeObservationRoute{}}
	err := tx.QueryRowContext(ctx, `SELECT revision FROM model_account_pool_configs WHERE model_id=?`, model.id).Scan(&view.PoolRevision)
	if errors.Is(err, sql.ErrNoRows) {
		view.PoolStatus = "legacy_no_pool"
		return view, nil
	}
	if err != nil || view.PoolRevision < 1 || view.PoolRevision > 9_007_199_254_740_991 {
		return poolRuntimeObservationView{}, errors.New("invalid pool revision")
	}
	if model.enabled {
		view.PoolStatus = "explicit_pool"
	} else {
		view.PoolStatus = "model_disabled"
	}
	accounts, err := readPoolRuntimeAccountsTx(ctx, tx, model)
	if err != nil {
		return poolRuntimeObservationView{}, err
	}
	for _, account := range accounts {
		item := account.poolRuntimeObservationRoute
		item.BlockReasons = []string{}
		if !model.enabled {
			item.BlockReasons = append(item.BlockReasons, "model_disabled")
		} else {
			capacity, err := readGlobalPoolCapacityTx(ctx, tx, account.UpstreamID)
			if err != nil {
				return poolRuntimeObservationView{}, err
			}
			item.GlobalMaxConcurrency = &capacity
		}
		seenLeases := make(map[string]bool)
		item.RequestReservations, err = readPoolReservationCountTx(ctx, tx, accountPoolLeaseTable, account.UpstreamID, asOf, seenLeases)
		if err != nil {
			return poolRuntimeObservationView{}, err
		}
		item.MaintenanceReservations, err = readPoolReservationCountTx(ctx, tx, accountPoolMaintenanceLeaseTable, account.UpstreamID, asOf, seenLeases)
		if err != nil {
			return poolRuntimeObservationView{}, err
		}
		cooldown, cooldownEvent, err := readPoolObservationCooldownTx(ctx, tx, account.UpstreamID, asOf)
		if err != nil {
			return poolRuntimeObservationView{}, err
		}
		item.CooldownUntil = cooldown
		recovery, err := readPoolObservationRecoveryTx(ctx, tx, account.UpstreamID, cooldownEvent)
		if err != nil {
			return poolRuntimeObservationView{}, err
		}
		if !account.enabled {
			item.BlockReasons = append(item.BlockReasons, "upstream_disabled")
		}
		if cooldown != nil {
			item.BlockReasons = append(item.BlockReasons, "cooldown_active")
		}
		if recovery {
			item.BlockReasons = append(item.BlockReasons, "recovery_isolated")
		}
		if account.provider == codexMembershipProvider {
			if !a.cfg.ExperimentalCodexMembership {
				item.BlockReasons = append(item.BlockReasons, "membership_disabled")
			}
			if account.credentialState.String == codexStateReauth {
				item.BlockReasons = append(item.BlockReasons, "reauth_required")
			}
		}
		if item.GlobalMaxConcurrency != nil {
			remaining := *item.GlobalMaxConcurrency - item.RequestReservations - item.MaintenanceReservations
			if remaining <= 0 {
				remaining = 0
				item.BlockReasons = append(item.BlockReasons, "capacity_reserved")
			}
			item.RemainingLocalSlots = &remaining
		}
		view.Items = append(view.Items, item)
	}
	return view, nil
}

func validatePoolRuntimeObservationSchema(ctx context.Context, tx *sql.Tx) error {
	if err := verifyAccountPoolTable(ctx, tx, modelPoolConfigTable, []string{"model_id", "revision", "updated_at"}, []string{"references models(id) on delete cascade", "check(revision >= 1)"}); err != nil {
		return err
	}
	if err := verifyAccountPoolTable(ctx, tx, modelPoolRouteTable,
		[]string{"model_id", "upstream_id", "upstream_model", "wire_protocol", "priority", "weight", "max_concurrency", "channel_id", "position"},
		[]string{"references model_account_pool_configs(model_id) on delete cascade", "references upstreams(id)", "primary key(model_id, upstream_id)", "unique(model_id, position)"}); err != nil {
		return err
	}
	if err := verifyAccountPoolTable(ctx, tx, accountPoolLeaseTable,
		[]string{"lease_id", "account_id", "public_model", "employee_id", "key_id", "pool_revision", "account_revision", "expires_at", "created_at"},
		[]string{"references upstreams(id)", "references models(id)", "references employees(id)", "references access_keys(id)"}); err != nil {
		return err
	}
	if err := verifyCanonicalTable(ctx, tx, accountPoolMaintenanceLeaseTable, accountPoolMaintenanceLeaseDDL); err != nil {
		return err
	}
	if err := verifyCooldownTableSchema(ctx, tx, false); err != nil {
		return err
	}
	if err := verifyCooldownExpiryIndex(ctx, tx); err != nil {
		return err
	}
	return verifyRecoveryStateTable(ctx, tx)
}

func readPoolRuntimeAccountsTx(ctx context.Context, tx *sql.Tx, model poolRuntimeModel) ([]poolRuntimeAccount, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.upstream_id,r.upstream_model,r.wire_protocol,r.priority,r.weight,r.max_concurrency,r.position,u.revision,u.enabled,u.archived,u.provider_kind,u.credential_state
		FROM model_account_pool_routes r LEFT JOIN upstreams u ON u.id=r.upstream_id WHERE r.model_id=? ORDER BY r.position LIMIT ?`, model.id, maxModelAccounts+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	accounts := make([]poolRuntimeAccount, 0)
	seen := make(map[string]bool)
	provider, wire := "", ""
	for rows.Next() {
		if len(accounts) == maxModelAccounts {
			return nil, errors.New("pool route limit exceeded")
		}
		var item poolRuntimeAccount
		var upstreamModel string
		var revision, enabled, archived sql.NullInt64
		var itemProvider sql.NullString
		if err := rows.Scan(&item.UpstreamID, &upstreamModel, &item.wire, &item.priority, &item.weight, &item.ConfiguredMaxConcurrency, &item.position, &revision, &enabled, &archived, &itemProvider, &item.credentialState); err != nil {
			return nil, err
		}
		if !validIdentifier(item.UpstreamID, 128) || seen[item.UpstreamID] || item.position != len(accounts) || item.priority < minAccountPriority || item.priority > maxAccountPriority || item.weight < 1 || item.weight > maxAccountWeight || item.ConfiguredMaxConcurrency < 1 || item.ConfiguredMaxConcurrency > maxAccountConcurrency || !revision.Valid || revision.Int64 < 1 || revision.Int64 > 9_007_199_254_740_991 || !enabled.Valid || (enabled.Int64 != 0 && enabled.Int64 != 1) || !archived.Valid || archived.Int64 != 0 || !itemProvider.Valid || !validProviderModelName(itemProvider.String, upstreamModel) || !validRouteWireProtocol(item.wire) || !providerSupportsWire(itemProvider.String, item.wire) {
			return nil, errors.New("invalid pool route metadata")
		}
		if model.modelKind == "embedding" && (itemProvider.String != "openai-compatible" || item.wire != string(wireProtocolEmbeddings)) || model.modelKind == "generation" && item.wire == string(wireProtocolEmbeddings) {
			return nil, errors.New("invalid model and pool wire combination")
		}
		if provider != "" && provider != itemProvider.String || wire != "" && wire != item.wire {
			return nil, errors.New("mixed pool provider or wire")
		}
		provider, wire = itemProvider.String, item.wire
		if itemProvider.String == codexMembershipProvider {
			if !item.credentialState.Valid || (item.credentialState.String != codexStateImported && item.credentialState.String != codexStateVerified && item.credentialState.String != codexStateReauth) {
				return nil, errors.New("invalid membership credential state")
			}
		} else if item.credentialState.Valid {
			return nil, errors.New("invalid api key credential state")
		}
		item.AccountRevision, item.enabled, item.provider = revision.Int64, enabled.Int64 == 1, itemProvider.String
		seen[item.UpstreamID] = true
		accounts = append(accounts, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, errors.New("empty explicit pool")
	}
	return accounts, nil
}

func readGlobalPoolCapacityTx(ctx context.Context, tx *sql.Tx, accountID string) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT r.max_concurrency,m.archived FROM model_account_pool_routes r JOIN models m ON m.id=r.model_id WHERE r.upstream_id=? AND m.enabled=1`, accountID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	minimum := 0
	for rows.Next() {
		var capacity, archived int
		if err := rows.Scan(&capacity, &archived); err != nil {
			return 0, err
		}
		if capacity < 1 || capacity > maxAccountConcurrency || archived != 0 {
			return 0, errors.New("invalid global pool capacity")
		}
		if minimum == 0 || capacity < minimum {
			minimum = capacity
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if minimum == 0 {
		return 0, errors.New("missing global pool capacity")
	}
	return minimum, nil
}

func readPoolReservationCountTx(ctx context.Context, tx *sql.Tx, table, accountID string, asOf time.Time, seen map[string]bool) (int, error) {
	rows, err := tx.QueryContext(ctx, `SELECT lease_id,expires_at,created_at FROM `+table+` WHERE account_id=?`, accountID)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, expiryText, createdText string
		if err := rows.Scan(&id, &expiryText, &createdText); err != nil {
			return 0, err
		}
		expiry, err := parseTime(expiryText)
		if err != nil {
			return 0, err
		}
		created, err := parseTime(createdText)
		if err != nil || !validIdentifier(id, 128) || seen[id] || created.After(expiry) {
			return 0, errors.New("invalid pool lease metadata")
		}
		seen[id] = true
		if expiry.After(asOf) {
			count++
			if count > 65_536 {
				return 0, errors.New("pool lease count limit exceeded")
			}
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, rows.Close()
}

func readPoolObservationCooldownTx(ctx context.Context, tx *sql.Tx, accountID string, asOf time.Time) (*string, string, error) {
	var event, failure, untilText, updatedText string
	err := tx.QueryRowContext(ctx, `SELECT event_id,failure_class,cooldown_until,updated_at FROM account_pool_runtime_cooldowns WHERE account_id=?`, accountID).Scan(&event, &failure, &untilText, &updatedText)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	until, err := parseTime(untilText)
	if err != nil {
		return nil, "", err
	}
	if _, err := parseTime(updatedText); err != nil || !validIdentifier(event, 128) || !validCooldownFailure(scheduling.FailureClass(failure)) {
		return nil, "", errors.New("invalid pool cooldown metadata")
	}
	if !until.After(asOf) {
		return nil, event, nil
	}
	value := until.UTC().Format(time.RFC3339Nano)
	return &value, event, nil
}

func readPoolObservationRecoveryTx(ctx context.Context, tx *sql.Tx, accountID, cooldownEvent string) (bool, error) {
	var event, state, nextText, createdText, updatedText string
	err := tx.QueryRowContext(ctx, `SELECT cooldown_event_id,state,next_probe_at,created_at,updated_at FROM account_recovery_states WHERE account_id=?`, accountID).Scan(&event, &state, &nextText, &createdText, &updatedText)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if event == "" || event != cooldownEvent || (state != recoveryRequired && state != recoveryInProgress && state != recoveryInterrupted) {
		return false, errors.New("invalid pool recovery metadata")
	}
	for _, value := range []string{nextText, createdText, updatedText} {
		if _, err := parseTime(value); err != nil {
			return false, err
		}
	}
	return true, nil
}
