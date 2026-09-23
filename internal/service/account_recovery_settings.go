package service

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

var (
	errAccountRecoveryNotAllowed      = errors.New("account recovery is not allowed by process configuration")
	errAccountRecoverySettingConflict = errors.New("account recovery setting conflict")
)

const accountRecoverySettingsDDL = `CREATE TABLE IF NOT EXISTS account_recovery_settings (
	singleton INTEGER NOT NULL PRIMARY KEY CHECK(singleton=1),
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
	updated_at TEXT NOT NULL
)`

type accountRecoverySettings struct {
	Enabled   bool      `json:"enabled"`
	Revision  int64     `json:"revision"`
	UpdatedAt time.Time `json:"updated_at"`
}

func migrateAccountRecoverySettingsTx(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, accountRecoverySettingsDDL); err != nil {
		return err
	}
	if err := verifyCanonicalTable(ctx, tx, "account_recovery_settings", accountRecoverySettingsDDL); err != nil {
		return err
	}
	var count int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM account_recovery_settings`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO account_recovery_settings(singleton,enabled,revision,updated_at) VALUES(1,0,1,?)`, formatAccountPoolTime(time.Now().UTC())); err != nil {
			return err
		}
	} else if count != 1 {
		return errors.New("account recovery settings must contain one row")
	}
	_, err := loadAccountRecoverySettingsTx(ctx, tx)
	return err
}

func loadAccountRecoverySettingsTx(ctx context.Context, tx *sql.Tx) (accountRecoverySettings, error) {
	if ctx == nil || tx == nil {
		return accountRecoverySettings{}, errors.New("invalid account recovery settings lookup")
	}
	var item accountRecoverySettings
	var enabled int
	var updated string
	if err := tx.QueryRowContext(ctx, `SELECT enabled,revision,updated_at FROM account_recovery_settings WHERE singleton=1`).Scan(&enabled, &item.Revision, &updated); err != nil {
		return accountRecoverySettings{}, err
	}
	if enabled != 0 && enabled != 1 || item.Revision < 1 {
		return accountRecoverySettings{}, errors.New("invalid stored account recovery settings")
	}
	parsed, err := parseTime(updated)
	if err != nil || updated != formatAccountPoolTime(parsed) {
		return accountRecoverySettings{}, errors.New("invalid stored account recovery settings timestamp")
	}
	item.Enabled = enabled == 1
	item.UpdatedAt = parsed
	return item, nil
}
