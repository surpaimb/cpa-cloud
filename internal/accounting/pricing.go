package accounting

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxPriceRate      int64 = 9_007_199_254_740_991
	MaxPriceListItems       = 1_000
)

var (
	ErrPriceRevisionConflict  = errors.New("price revision conflict")
	ErrPriceOperationConflict = errors.New("price operation conflict")
	ErrPriceListLimit         = errors.New("price list limit exceeded")
)

type PriceCatalog struct {
	db *sql.DB
}

type PriceSave struct {
	AccountID        string
	ActualModel      string
	OperationID      string
	ExpectedRevision int64
	Price            *PriceSnapshot
}

type PriceVersion struct {
	AccountID   string
	ActualModel string
	Version     string
	Revision    int64
	CreatedAt   time.Time
	Price       *PriceSnapshot
}

func NewPriceCatalog(db *sql.DB) *PriceCatalog {
	return &PriceCatalog{db: db}
}

func (c *PriceCatalog) Migrate(ctx context.Context) error {
	if c == nil || c.db == nil || ctx == nil {
		return ErrInvalid
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`CREATE TABLE IF NOT EXISTS account_price_versions (
			version TEXT PRIMARY KEY,
			upstream_id TEXT NOT NULL REFERENCES upstreams(id),
			upstream_model TEXT NOT NULL,
			revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
			operation_id TEXT NOT NULL,
			expected_revision INTEGER NOT NULL CHECK(expected_revision BETWEEN 0 AND 9007199254740991),
			currency TEXT,
			input_rate INTEGER,
			output_rate INTEGER,
			cache_read_rate INTEGER,
			cache_write_rate INTEGER,
			created_at TEXT NOT NULL,
			UNIQUE(operation_id),
			UNIQUE(upstream_id,upstream_model,revision),
			UNIQUE(version,upstream_id,upstream_model,revision),
			CHECK(revision=expected_revision+1),
			CHECK(
				(currency IS NULL AND input_rate IS NULL AND output_rate IS NULL AND cache_read_rate IS NULL AND cache_write_rate IS NULL)
				OR
				(currency IS NOT NULL AND input_rate IS NOT NULL AND output_rate IS NOT NULL AND cache_read_rate IS NOT NULL AND cache_write_rate IS NOT NULL)
			),
			CHECK(currency IS NULL OR (length(currency)=3 AND currency=upper(currency) AND currency NOT GLOB '*[^A-Z]*')),
			CHECK(input_rate IS NULL OR input_rate BETWEEN 0 AND 9007199254740991),
			CHECK(output_rate IS NULL OR output_rate BETWEEN 0 AND 9007199254740991),
			CHECK(cache_read_rate IS NULL OR cache_read_rate BETWEEN 0 AND 9007199254740991),
			CHECK(cache_write_rate IS NULL OR cache_write_rate BETWEEN 0 AND 9007199254740991)
		)`,
		`CREATE TABLE IF NOT EXISTS account_price_current (
			upstream_id TEXT NOT NULL,
			upstream_model TEXT NOT NULL,
			version TEXT NOT NULL,
			revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
			PRIMARY KEY(upstream_id,upstream_model),
			UNIQUE(version),
			FOREIGN KEY(version,upstream_id,upstream_model,revision)
				REFERENCES account_price_versions(version,upstream_id,upstream_model,revision)
				DEFERRABLE INITIALLY DEFERRED
		)`,
		`CREATE INDEX IF NOT EXISTS account_price_versions_key_idx ON account_price_versions(upstream_id,upstream_model,revision)`,
		`CREATE TRIGGER IF NOT EXISTS account_price_versions_no_update
			BEFORE UPDATE ON account_price_versions BEGIN SELECT RAISE(ABORT,'price versions are immutable'); END`,
		`CREATE TRIGGER IF NOT EXISTS account_price_versions_no_delete
			BEFORE DELETE ON account_price_versions BEGIN SELECT RAISE(ABORT,'price versions are immutable'); END`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("migrate price catalog: %w", err)
		}
	}
	if err := validatePriceCatalogSchema(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}

// Current returns a detached immutable snapshot. A missing key and an explicit
// disabled version both return nil without an error.
func (c *PriceCatalog) Current(ctx context.Context, accountID, actualModel string) (*PriceSnapshot, error) {
	if c == nil || c.db == nil || ctx == nil || !validID(accountID) || !validActualModelRead(actualModel) {
		return nil, ErrInvalid
	}
	return currentPrice(ctx, c.db, accountID, actualModel)
}

// CurrentTx reads the current immutable price snapshot through a caller-owned
// transaction. It neither commits nor rolls back tx.
func (c *PriceCatalog) CurrentTx(ctx context.Context, tx *sql.Tx, accountID, actualModel string) (*PriceSnapshot, error) {
	if c == nil || c.db == nil || ctx == nil || tx == nil || !validID(accountID) || !validActualModelRead(actualModel) {
		return nil, ErrInvalid
	}
	return currentPrice(ctx, tx, accountID, actualModel)
}

func currentPrice(ctx context.Context, query priceQuery, accountID, actualModel string) (*PriceSnapshot, error) {
	var version string
	var currency sql.NullString
	var inputRate, outputRate, cacheReadRate, cacheWriteRate sql.NullInt64
	err := query.QueryRowContext(ctx, `SELECT v.version,v.currency,v.input_rate,v.output_rate,v.cache_read_rate,v.cache_write_rate
		FROM account_price_current c JOIN account_price_versions v
		ON v.version=c.version AND v.upstream_id=c.upstream_id AND v.upstream_model=c.upstream_model AND v.revision=c.revision
		WHERE c.upstream_id=? AND c.upstream_model=?`, accountID, actualModel).
		Scan(&version, &currency, &inputRate, &outputRate, &cacheReadRate, &cacheWriteRate)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return storedPrice(version, currency, inputRate, outputRate, cacheReadRate, cacheWriteRate)
}

func (c *PriceCatalog) List(ctx context.Context, accountID string) ([]PriceVersion, error) {
	if c == nil || c.db == nil || ctx == nil || !validID(accountID) {
		return nil, ErrInvalid
	}
	var exists int
	if err := c.db.QueryRowContext(ctx, `SELECT 1 FROM upstreams WHERE id=?`, accountID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx, `SELECT v.upstream_id,v.upstream_model,v.version,v.revision,v.created_at,
		v.currency,v.input_rate,v.output_rate,v.cache_read_rate,v.cache_write_rate
		FROM account_price_current c JOIN account_price_versions v
		ON v.version=c.version AND v.upstream_id=c.upstream_id AND v.upstream_model=c.upstream_model AND v.revision=c.revision
		WHERE c.upstream_id=? ORDER BY v.upstream_model LIMIT ?`, accountID, MaxPriceListItems+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]PriceVersion, 0)
	for rows.Next() {
		var item PriceVersion
		var created string
		var currency sql.NullString
		var inputRate, outputRate, cacheReadRate, cacheWriteRate sql.NullInt64
		if err := rows.Scan(&item.AccountID, &item.ActualModel, &item.Version, &item.Revision, &created,
			&currency, &inputRate, &outputRate, &cacheReadRate, &cacheWriteRate); err != nil {
			return nil, err
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, errors.New("invalid stored price timestamp")
		}
		item.Price, err = storedPrice(item.Version, currency, inputRate, outputRate, cacheReadRate, cacheWriteRate)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
		if len(items) > MaxPriceListItems {
			return nil, ErrPriceListLimit
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (c *PriceCatalog) Save(ctx context.Context, input PriceSave) (PriceVersion, error) {
	if c == nil || c.db == nil || ctx == nil || !validPriceSave(input) {
		return PriceVersion{}, ErrInvalid
	}
	if prior, found, err := c.findOperation(ctx, c.db, input.OperationID); err != nil {
		return PriceVersion{}, err
	} else if found {
		if samePriceOperation(prior, input) {
			return prior, nil
		}
		return PriceVersion{}, ErrPriceOperationConflict
	}
	if input.ExpectedRevision == MaxPriceRate {
		return PriceVersion{}, ErrPriceRevisionConflict
	}
	versionID, err := newPriceVersionID()
	if err != nil {
		return PriceVersion{}, err
	}
	createdAt := time.Now().UTC()
	createdText := createdAt.Format(time.RFC3339Nano)
	revision := input.ExpectedRevision + 1
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return PriceVersion{}, err
	}
	defer tx.Rollback()
	var result sql.Result
	if input.ExpectedRevision == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO account_price_current(upstream_id,upstream_model,version,revision)
			VALUES(?,?,?,?) ON CONFLICT(upstream_id,upstream_model) DO NOTHING`, input.AccountID, input.ActualModel, versionID, revision)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE account_price_current SET version=?,revision=?
			WHERE upstream_id=? AND upstream_model=? AND revision=?`, versionID, revision, input.AccountID, input.ActualModel, input.ExpectedRevision)
	}
	if err != nil {
		return PriceVersion{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return PriceVersion{}, err
	}
	if changed != 1 {
		_ = tx.Rollback()
		return c.resolveFailedSave(ctx, input, ErrPriceRevisionConflict)
	}
	var currency any
	var inputRate, outputRate, cacheReadRate, cacheWriteRate any
	if input.Price != nil {
		currency = input.Price.Currency
		inputRate = input.Price.InputPerMillionMicro
		outputRate = input.Price.OutputPerMillionMicro
		cacheReadRate = input.Price.CacheReadPerMillionMicro
		cacheWriteRate = input.Price.CacheWritePerMillionMicro
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO account_price_versions(
		version,upstream_id,upstream_model,revision,operation_id,expected_revision,currency,input_rate,output_rate,cache_read_rate,cache_write_rate,created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, versionID, input.AccountID, input.ActualModel, revision, input.OperationID,
		input.ExpectedRevision, currency, inputRate, outputRate, cacheReadRate, cacheWriteRate, createdText)
	if err != nil {
		_ = tx.Rollback()
		return c.resolveFailedSave(ctx, input, err)
	}
	if err := tx.Commit(); err != nil {
		return c.resolveFailedSave(ctx, input, err)
	}
	return makePriceVersion(input, versionID, revision, createdAt), nil
}

type priceQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (c *PriceCatalog) findOperation(ctx context.Context, query priceQuery, operationID string) (PriceVersion, bool, error) {
	var item PriceVersion
	var expected int64
	var created string
	var currency sql.NullString
	var inputRate, outputRate, cacheReadRate, cacheWriteRate sql.NullInt64
	err := query.QueryRowContext(ctx, `SELECT upstream_id,upstream_model,version,revision,expected_revision,created_at,
		currency,input_rate,output_rate,cache_read_rate,cache_write_rate FROM account_price_versions WHERE operation_id=?`, operationID).
		Scan(&item.AccountID, &item.ActualModel, &item.Version, &item.Revision, &expected, &created,
			&currency, &inputRate, &outputRate, &cacheReadRate, &cacheWriteRate)
	if errors.Is(err, sql.ErrNoRows) {
		return PriceVersion{}, false, nil
	}
	if err != nil {
		return PriceVersion{}, false, err
	}
	item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return PriceVersion{}, false, errors.New("invalid stored price timestamp")
	}
	item.Price, err = storedPrice(item.Version, currency, inputRate, outputRate, cacheReadRate, cacheWriteRate)
	if err != nil {
		return PriceVersion{}, false, err
	}
	// Expected revision is encoded by the immutable revision relation. Retain it
	// in the comparison without adding it to the public version result.
	if item.Revision != expected+1 {
		return PriceVersion{}, false, errors.New("invalid stored price revision")
	}
	return item, true, nil
}

func (c *PriceCatalog) resolveFailedSave(ctx context.Context, input PriceSave, fallback error) (PriceVersion, error) {
	prior, found, err := c.findOperation(ctx, c.db, input.OperationID)
	if err != nil {
		return PriceVersion{}, err
	}
	if found {
		if samePriceOperation(prior, input) {
			return prior, nil
		}
		return PriceVersion{}, ErrPriceOperationConflict
	}
	var exists int
	if err := c.db.QueryRowContext(ctx, `SELECT 1 FROM upstreams WHERE id=?`, input.AccountID).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return PriceVersion{}, ErrNotFound
		}
		return PriceVersion{}, err
	}
	return PriceVersion{}, fallback
}

func validPriceSave(input PriceSave) bool {
	if !validID(input.AccountID) || !validActualModelSave(input.ActualModel) || !validPriceOperationID(input.OperationID) || input.ExpectedRevision < 0 || input.ExpectedRevision > MaxPriceRate {
		return false
	}
	if input.Price == nil {
		return true
	}
	if input.Price.Version != "" || len(input.Price.Currency) != 3 || input.Price.Currency != strings.ToUpper(input.Price.Currency) {
		return false
	}
	for _, character := range input.Price.Currency {
		if character < 'A' || character > 'Z' {
			return false
		}
	}
	for _, rate := range []int64{input.Price.InputPerMillionMicro, input.Price.OutputPerMillionMicro, input.Price.CacheReadPerMillionMicro, input.Price.CacheWritePerMillionMicro} {
		if rate < 0 || rate > MaxPriceRate {
			return false
		}
	}
	return true
}

// Current must remain able to look up every legacy model name accepted by the
// upstream store. Writes use the narrower canonical rule below.
func validActualModelRead(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsRune(value, '\x00')
}

func validActualModelSave(value string) bool {
	if value == "" || len(value) > 256 || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func validPriceOperationID(value string) bool {
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

func newPriceVersionID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "price_" + hex.EncodeToString(random), nil
}

func makePriceVersion(input PriceSave, version string, revision int64, createdAt time.Time) PriceVersion {
	item := PriceVersion{AccountID: input.AccountID, ActualModel: input.ActualModel, Version: version, Revision: revision, CreatedAt: createdAt}
	if input.Price != nil {
		copy := *input.Price
		copy.Version = version
		item.Price = &copy
	}
	return item
}

func samePriceOperation(item PriceVersion, input PriceSave) bool {
	if item.AccountID != input.AccountID || item.ActualModel != input.ActualModel || item.Revision != input.ExpectedRevision+1 {
		return false
	}
	if item.Price == nil || input.Price == nil {
		return item.Price == nil && input.Price == nil
	}
	return item.Price.Currency == input.Price.Currency &&
		item.Price.InputPerMillionMicro == input.Price.InputPerMillionMicro &&
		item.Price.OutputPerMillionMicro == input.Price.OutputPerMillionMicro &&
		item.Price.CacheReadPerMillionMicro == input.Price.CacheReadPerMillionMicro &&
		item.Price.CacheWritePerMillionMicro == input.Price.CacheWritePerMillionMicro
}

func storedPrice(version string, currency sql.NullString, inputRate, outputRate, cacheReadRate, cacheWriteRate sql.NullInt64) (*PriceSnapshot, error) {
	allNull := !currency.Valid && !inputRate.Valid && !outputRate.Valid && !cacheReadRate.Valid && !cacheWriteRate.Valid
	if allNull {
		return nil, nil
	}
	if !currency.Valid || !inputRate.Valid || !outputRate.Valid || !cacheReadRate.Valid || !cacheWriteRate.Valid {
		return nil, errors.New("invalid stored price")
	}
	price := &PriceSnapshot{Version: version, Currency: currency.String, InputPerMillionMicro: inputRate.Int64,
		OutputPerMillionMicro: outputRate.Int64, CacheReadPerMillionMicro: cacheReadRate.Int64, CacheWritePerMillionMicro: cacheWriteRate.Int64}
	if !validPrice(price) || inputRate.Int64 > MaxPriceRate || outputRate.Int64 > MaxPriceRate || cacheReadRate.Int64 > MaxPriceRate || cacheWriteRate.Int64 > MaxPriceRate {
		return nil, errors.New("invalid stored price")
	}
	return price, nil
}
