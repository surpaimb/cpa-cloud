package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"cpacloud.local/server/internal/scheduling"
)

const cooldownEventMaxLength = 128

type upstreamCooldownView struct {
	EventID       string `json:"event_id"`
	FailureClass  string `json:"failure_class"`
	CooldownUntil string `json:"cooldown_until"`
	UpdatedAt     string `json:"updated_at"`
	Active        bool   `json:"active"`
}

type cooldownClearCode string

const (
	cooldownCleared          cooldownClearCode = "cleared"
	cooldownAlreadyClear     cooldownClearCode = "already_clear"
	cooldownNotFound         cooldownClearCode = "not_found"
	cooldownRevisionConflict cooldownClearCode = "revision_conflict"
	cooldownEventConflict    cooldownClearCode = "cooldown_conflict"
	cooldownStorageFailure   cooldownClearCode = "storage_unavailable"
)

func migrateAccountPoolCooldownSchema(ctx context.Context, tx *sql.Tx) error {
	var objectType string
	err := tx.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name=?`, accountPoolCooldownTable).Scan(&objectType)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if err := createAccountPoolCooldownTable(ctx, tx); err != nil {
			return err
		}
	case err != nil:
		return err
	case objectType != "table":
		return fmt.Errorf("existing %s object is not a table", accountPoolCooldownTable)
	default:
		columns, err := cooldownTableColumns(ctx, tx)
		if err != nil {
			return err
		}
		if exactCooldownColumns(columns, "account_id", "event_id", "failure_class", "cooldown_until", "updated_at") {
			// The common verifier below validates the complete new schema.
			break
		}
		if !exactCooldownColumns(columns, "account_id", "failure_class", "cooldown_until", "updated_at") {
			return fmt.Errorf("existing %s table has an incompatible schema", accountPoolCooldownTable)
		}
		if err := verifyAccountPoolTable(ctx, tx, accountPoolCooldownTable,
			[]string{"account_id", "failure_class", "cooldown_until", "updated_at"},
			[]string{"references upstreams(id) on delete cascade"}); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS account_pool_runtime_cooldown_expiry_idx`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE account_pool_runtime_cooldowns RENAME TO account_pool_runtime_cooldowns_legacy`); err != nil {
			return err
		}
		if err := createAccountPoolCooldownTable(ctx, tx); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT account_id,failure_class,cooldown_until,updated_at FROM account_pool_runtime_cooldowns_legacy ORDER BY account_id`)
		if err != nil {
			return err
		}
		type legacyCooldown struct{ accountID, failure, until, updated string }
		legacy := make([]legacyCooldown, 0)
		for rows.Next() {
			var item legacyCooldown
			if err := rows.Scan(&item.accountID, &item.failure, &item.until, &item.updated); err != nil {
				rows.Close()
				return err
			}
			legacy = append(legacy, item)
		}
		iterationErr, closeErr := rows.Err(), rows.Close()
		if iterationErr != nil {
			return iterationErr
		}
		if closeErr != nil {
			return closeErr
		}
		for _, item := range legacy {
			eventID, err := newID("cool")
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO account_pool_runtime_cooldowns(account_id,event_id,failure_class,cooldown_until,updated_at) VALUES(?,?,?,?,?)`, item.accountID, eventID, item.failure, item.until, item.updated); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE account_pool_runtime_cooldowns_legacy`); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS account_pool_runtime_cooldown_expiry_idx ON account_pool_runtime_cooldowns(cooldown_until)`); err != nil {
		return err
	}
	return verifyCooldownExpiryIndex(ctx, tx)
}

func createAccountPoolCooldownTable(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE account_pool_runtime_cooldowns (
		account_id TEXT PRIMARY KEY REFERENCES upstreams(id) ON DELETE CASCADE,
		event_id TEXT NOT NULL UNIQUE,
		failure_class TEXT NOT NULL CHECK(failure_class IN ('rate_limited','overloaded','transient','authentication','permanent')),
		cooldown_until TEXT NOT NULL,
		updated_at TEXT NOT NULL
	)`)
	return err
}

func cooldownTableColumns(ctx context.Context, tx *sql.Tx) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `PRAGMA table_info(account_pool_runtime_cooldowns)`)
	if err != nil {
		return nil, err
	}
	columns := make([]string, 0, 5)
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return nil, err
		}
		columns = append(columns, name)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return nil, iterationErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return columns, nil
}

func exactCooldownColumns(actual []string, expected ...string) bool {
	if len(actual) != len(expected) {
		return false
	}
	seen := make(map[string]bool, len(actual))
	for _, column := range actual {
		seen[column] = true
	}
	for _, column := range expected {
		if !seen[column] {
			return false
		}
	}
	return true
}

func verifyCooldownExpiryIndex(ctx context.Context, tx *sql.Tx) error {
	var table string
	if err := tx.QueryRowContext(ctx, `SELECT tbl_name FROM sqlite_master WHERE type='index' AND name='account_pool_runtime_cooldown_expiry_idx'`).Scan(&table); err != nil {
		return err
	}
	if table != accountPoolCooldownTable {
		return errors.New("account pool cooldown expiry index belongs to an incompatible table")
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA index_info(account_pool_runtime_cooldown_expiry_idx)`)
	if err != nil {
		return err
	}
	columns := make([]string, 0, 1)
	for rows.Next() {
		var sequence, cid int
		var name string
		if err := rows.Scan(&sequence, &cid, &name); err != nil {
			rows.Close()
			return err
		}
		columns = append(columns, name)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(columns) != 1 || columns[0] != "cooldown_until" {
		return errors.New("account pool cooldown expiry index has an incompatible schema")
	}
	return nil
}

func validCooldownFailure(failure scheduling.FailureClass) bool {
	switch failure {
	case scheduling.FailureRateLimit, scheduling.FailureOverloaded, scheduling.FailureTransient, scheduling.FailureAuth, scheduling.FailurePermanent:
		return true
	default:
		return false
	}
}

func (a *App) decorateUpstreamCooldowns(ctx context.Context, items []upstreamView, now time.Time) error {
	rows, err := a.store.db.QueryContext(ctx, `SELECT account_id,event_id,failure_class,cooldown_until,updated_at FROM account_pool_runtime_cooldowns ORDER BY account_id`)
	if err != nil {
		return err
	}
	views := make(map[string]*upstreamCooldownView, len(items))
	for rows.Next() {
		var accountID, eventID, failure, untilText, updatedText string
		if err := rows.Scan(&accountID, &eventID, &failure, &untilText, &updatedText); err != nil {
			rows.Close()
			return err
		}
		until, err := parseTime(untilText)
		if err != nil {
			rows.Close()
			return err
		}
		updated, err := parseTime(updatedText)
		if err != nil || eventID == "" || !validCooldownFailure(scheduling.FailureClass(failure)) {
			rows.Close()
			return errors.New("account pool cooldown contains invalid metadata")
		}
		views[accountID] = &upstreamCooldownView{EventID: eventID, FailureClass: failure,
			CooldownUntil: until.UTC().Format(time.RFC3339Nano), UpdatedAt: updated.UTC().Format(time.RFC3339Nano), Active: until.After(now)}
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil {
		return iterationErr
	}
	if closeErr != nil {
		return closeErr
	}
	for index := range items {
		items[index].Cooldown = views[items[index].ID]
	}
	return nil
}

func (a *App) clearUpstreamCooldown(w http.ResponseWriter, r *http.Request, _ adminSession) {
	if r.URL.RawQuery != "" || !validIdentifier(r.PathValue("id"), 128) {
		writeCooldownInvalid(w)
		return
	}
	object, err := decodeUniqueJSONObject(w, r, adminMaxBody)
	if err != nil || !exactJSONKeys(object, "expected_revision", "expected_cooldown_event_id") {
		writeCooldownInvalid(w)
		return
	}
	expectedRevision, ok := parseSafeJSONInteger(object["expected_revision"])
	eventID, eventOK := object["expected_cooldown_event_id"].(string)
	if !ok || expectedRevision < 1 || !eventOK || !validIdentifier(eventID, cooldownEventMaxLength) {
		writeCooldownInvalid(w)
		return
	}
	if a.accountPool == nil {
		writeCooldownStorageError(w)
		return
	}
	result, revision, code := a.accountPool.clearCooldown(r.Context(), r.PathValue("id"), expectedRevision, eventID)
	switch code {
	case cooldownCleared, cooldownAlreadyClear:
		writeJSON(w, http.StatusOK, map[string]any{"result": string(result), "upstream_id": r.PathValue("id"), "revision": revision, "server_time": time.Now().UTC().Format(time.RFC3339Nano)})
	case cooldownNotFound:
		writeAdminError(w, http.StatusNotFound, "not_found", "Upstream was not found.")
	case cooldownRevisionConflict:
		writeAdminError(w, http.StatusConflict, "revision_conflict", "The upstream was changed by another request.")
	case cooldownEventConflict:
		writeAdminError(w, http.StatusConflict, "cooldown_conflict", "The cooldown was changed by another request.")
	default:
		writeCooldownStorageError(w)
	}
}

func (rt *accountPoolRuntime) clearCooldown(ctx context.Context, accountID string, expectedRevision int64, expectedEventID string) (cooldownClearCode, int64, cooldownClearCode) {
	if !rt.begin() {
		return "", 0, cooldownStorageFailure
	}
	defer rt.wg.Done()
	rt.cooldownTransition.Lock()
	defer rt.cooldownTransition.Unlock()
	if ctx.Err() != nil {
		return "", 0, cooldownStorageFailure
	}
	tx, err := rt.app.store.db.BeginTx(ctx, nil)
	if err != nil {
		return "", 0, cooldownStorageFailure
	}
	defer tx.Rollback()
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM upstreams WHERE id=?`, accountID).Scan(&revision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", 0, cooldownNotFound
		}
		return "", 0, cooldownStorageFailure
	}
	if revision != expectedRevision {
		return "", revision, cooldownRevisionConflict
	}
	var eventID string
	if err := tx.QueryRowContext(ctx, `SELECT event_id FROM account_pool_runtime_cooldowns WHERE account_id=?`, accountID).Scan(&eventID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return "", revision, cooldownStorageFailure
		}
		if err := tx.Commit(); err != nil {
			return "", revision, cooldownStorageFailure
		}
		return cooldownAlreadyClear, revision, cooldownAlreadyClear
	}
	if eventID != expectedEventID {
		return "", revision, cooldownEventConflict
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM account_pool_runtime_cooldowns WHERE account_id=? AND event_id=?`, accountID, expectedEventID)
	if err != nil {
		return "", revision, cooldownStorageFailure
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return "", revision, cooldownStorageFailure
	}
	if err := tx.Commit(); err != nil {
		return "", revision, cooldownStorageFailure
	}
	rt.NotifyChanged()
	rt.scheduler.ClearCooldown(accountID, expectedEventID)
	return cooldownCleared, revision, cooldownCleared
}

func writeCooldownInvalid(w http.ResponseWriter) {
	writeAdminError(w, http.StatusBadRequest, "invalid_request", "Invalid cooldown request.")
}

func writeCooldownStorageError(w http.ResponseWriter) {
	writeAdminError(w, http.StatusServiceUnavailable, "storage_unavailable", "Service is temporarily unavailable.")
}
