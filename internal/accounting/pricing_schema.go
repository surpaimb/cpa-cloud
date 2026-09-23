package accounting

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

func validatePriceCatalogSchema(ctx context.Context, tx *sql.Tx) error {
	versionColumns := []ledgerColumn{
		{name: "version", kind: "TEXT", primary: true},
		{name: "upstream_id", kind: "TEXT", notNull: true},
		{name: "upstream_model", kind: "TEXT", notNull: true},
		{name: "revision", kind: "INTEGER", notNull: true},
		{name: "operation_id", kind: "TEXT", notNull: true},
		{name: "expected_revision", kind: "INTEGER", notNull: true},
		{name: "currency", kind: "TEXT"},
		{name: "input_rate", kind: "INTEGER"},
		{name: "output_rate", kind: "INTEGER"},
		{name: "cache_read_rate", kind: "INTEGER"},
		{name: "cache_write_rate", kind: "INTEGER"},
		{name: "created_at", kind: "TEXT", notNull: true},
	}
	versionConstraints := []string{
		"check(revisionbetween1and9007199254740991)",
		"unique(operation_id)",
		"unique(upstream_id,upstream_model,revision)",
		"unique(version,upstream_id,upstream_model,revision)",
		"check(expected_revisionbetween0and9007199254740991)",
		"check(revision=expected_revision+1)",
		"check((currencyisnullandinput_rateisnullandoutput_rateisnullandcache_read_rateisnullandcache_write_rateisnull)or(currencyisnotnullandinput_rateisnotnullandoutput_rateisnotnullandcache_read_rateisnotnullandcache_write_rateisnotnull))",
		"check(currencyisnullor(length(currency)=3andcurrency=upper(currency)andcurrencynotglob'*[^a-z]*'))",
		"check(input_rateisnullorinput_ratebetween0and9007199254740991)",
		"check(output_rateisnulloroutput_ratebetween0and9007199254740991)",
		"check(cache_read_rateisnullorcache_read_ratebetween0and9007199254740991)",
		"check(cache_write_rateisnullorcache_write_ratebetween0and9007199254740991)",
	}
	if err := validateLedgerTable(ctx, tx, "account_price_versions", versionColumns, versionConstraints); err != nil {
		return errors.New("invalid price version schema: " + err.Error())
	}
	currentColumns := []ledgerColumn{
		{name: "upstream_id", kind: "TEXT", notNull: true, primary: true},
		{name: "upstream_model", kind: "TEXT", notNull: true},
		{name: "version", kind: "TEXT", notNull: true},
		{name: "revision", kind: "INTEGER", notNull: true},
	}
	currentConstraints := []string{
		"unique(version)",
		"check(revisionbetween1and9007199254740991)",
		"primarykey(upstream_id,upstream_model)",
		"foreignkey(version,upstream_id,upstream_model,revision)referencesaccount_price_versions(version,upstream_id,upstream_model,revision)deferrableinitiallydeferred",
	}
	if err := validateLedgerTable(ctx, tx, "account_price_current", currentColumns, currentConstraints); err != nil {
		return errors.New("invalid current price schema: " + err.Error())
	}
	if err := validatePriceForeignKeys(ctx, tx); err != nil {
		return err
	}
	for _, trigger := range []string{"account_price_versions_no_update", "account_price_versions_no_delete"} {
		var kind, definition string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, trigger).Scan(&kind, &definition); err != nil {
			return errors.New("missing immutable price trigger")
		}
		normalized := normalizePriceSQL(definition)
		verb := "beforeupdateonaccount_price_versions"
		if strings.HasSuffix(trigger, "no_delete") {
			verb = "beforedeleteonaccount_price_versions"
		}
		if kind != "trigger" || !strings.Contains(normalized, verb) || !strings.Contains(normalized, "raise(abort,'priceversionsareimmutable')") {
			return errors.New("invalid immutable price trigger")
		}
	}
	var indexKind, indexDefinition string
	if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name='account_price_versions_key_idx'`).Scan(&indexKind, &indexDefinition); err != nil {
		return errors.New("missing price key index")
	}
	if indexKind != "index" || !strings.Contains(normalizePriceSQL(indexDefinition), "onaccount_price_versions(upstream_id,upstream_model,revision)") {
		return errors.New("invalid price key index")
	}
	if err := validateStoredPrices(ctx, tx); err != nil {
		return err
	}
	return nil
}

func validatePriceForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(account_price_versions)`)
	if err != nil {
		return err
	}
	count := 0
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return err
		}
		if table != "upstreams" || from != "upstream_id" || to != "id" || sequence != 0 {
			rows.Close()
			return errors.New("invalid price upstream foreign key")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if count != 1 {
		return errors.New("invalid price upstream foreign key")
	}

	rows, err = tx.QueryContext(ctx, `PRAGMA foreign_key_list(account_price_current)`)
	if err != nil {
		return err
	}
	want := map[string]string{"version": "version", "upstream_id": "upstream_id", "upstream_model": "upstream_model", "revision": "revision"}
	count = 0
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return err
		}
		if table != "account_price_versions" || want[from] != to {
			rows.Close()
			return errors.New("invalid current price foreign key")
		}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if count != 4 {
		return errors.New("invalid current price foreign key")
	}
	for _, table := range []string{"account_price_versions", "account_price_current"} {
		violations, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(`+table+`)`)
		if err != nil {
			return err
		}
		if violations.Next() {
			violations.Close()
			return errors.New("price catalog foreign key violation")
		}
		if err := violations.Err(); err != nil {
			violations.Close()
			return err
		}
		if err := violations.Close(); err != nil {
			return err
		}
	}
	return nil
}

func validateStoredPrices(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT version,upstream_id,upstream_model,revision,operation_id,expected_revision,created_at,
		currency,input_rate,output_rate,cache_read_rate,cache_write_rate FROM account_price_versions`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var version, accountID, model, operationID, created string
		var revision, expected int64
		var currency sql.NullString
		var inputRate, outputRate, cacheReadRate, cacheWriteRate sql.NullInt64
		if err := rows.Scan(&version, &accountID, &model, &revision, &operationID, &expected, &created,
			&currency, &inputRate, &outputRate, &cacheReadRate, &cacheWriteRate); err != nil {
			return err
		}
		if !validID(version) || !validID(accountID) || !validActualModelSave(model) || !validPriceOperationID(operationID) || revision != expected+1 {
			return errors.New("invalid stored price metadata")
		}
		if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
			return errors.New("invalid stored price timestamp")
		}
		if _, err := storedPrice(version, currency, inputRate, outputRate, cacheReadRate, cacheWriteRate); err != nil {
			return err
		}
	}
	return rows.Err()
}

func normalizePriceSQL(value string) string {
	return strings.Map(func(character rune) rune {
		if character == ' ' || character == '\t' || character == '\r' || character == '\n' {
			return -1
		}
		return character
	}, strings.ToLower(value))
}
