package service

// Independently authored from CPA Cloud's outbound proxy contract. This file
// only owns durable proxy metadata and bindings; it never opens a network
// connection or changes application-level admission state.
import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	outboundProxyMaxRevision = int64(9007199254740991)

	outboundProxyCredentialKeep    = "keep"
	outboundProxyCredentialReplace = "replace"
	outboundProxyCredentialClear   = "clear"
)

var (
	errOutboundProxyInvalid           = errors.New("invalid outbound proxy")
	errOutboundProxyNotFound          = errors.New("outbound proxy was not found")
	errOutboundProxyConflict          = errors.New("outbound proxy revision conflict")
	errOutboundProxyOperationConflict = errors.New("outbound proxy operation conflict")
	errOutboundProxyRevisionOverflow  = errors.New("outbound proxy revision overflow")
	errUpstreamProxyBindingConflict   = errors.New("upstream proxy binding conflict")
	errOutboundProxyUnavailable       = errors.New("outbound proxy is unavailable")
)

const outboundProxyDDL = `CREATE TABLE outbound_proxies (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	scheme TEXT NOT NULL CHECK(scheme = 'https'),
	host TEXT NOT NULL,
	port INTEGER NOT NULL CHECK(port BETWEEN 1 AND 65535),
	address_scope TEXT NOT NULL CHECK(address_scope IN ('public','private')),
	enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(revision BETWEEN 1 AND 9007199254740991),
	connection_revision INTEGER NOT NULL CHECK(connection_revision BETWEEN 1 AND 9007199254740991),
	credential_ciphertext BLOB,
	credential_key_version INTEGER,
	operation_id TEXT NOT NULL UNIQUE,
	input_fingerprint BLOB NOT NULL CHECK(length(input_fingerprint) = 32),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK((credential_ciphertext IS NULL AND credential_key_version IS NULL) OR (credential_ciphertext IS NOT NULL AND credential_key_version IS NOT NULL))
)`

const upstreamProxyBindingDDL = `CREATE TABLE upstream_proxy_bindings (
	upstream_id TEXT PRIMARY KEY REFERENCES upstreams(id) ON DELETE RESTRICT,
	proxy_id TEXT NOT NULL REFERENCES outbound_proxies(id) ON DELETE RESTRICT,
	proxy_connection_revision INTEGER NOT NULL CHECK(proxy_connection_revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
)`

const outboundProxyCreatedIndexDDL = `CREATE INDEX outbound_proxies_created_idx ON outbound_proxies(created_at,id)`
const upstreamProxyProxyIndexDDL = `CREATE INDEX upstream_proxy_bindings_proxy_idx ON upstream_proxy_bindings(proxy_id,upstream_id)`

type outboundProxyStore struct {
	db                      *sql.DB
	secrets                 *secrets
	now                     func() time.Time
	allowLoopbackForTesting bool
}

type outboundProxyView struct {
	ID                 string
	Name               string
	Scheme             string
	Host               string
	Port               int
	AddressScope       string
	Enabled            bool
	Revision           int64
	ConnectionRevision int64
	HasCredentials     bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type outboundProxyCreateInput struct {
	OperationID  string
	Name         string
	Scheme       string
	Host         string
	Port         int
	AddressScope string
	Enabled      bool
	Credential   *outboundProxyCredential
}

type outboundProxyUpdateInput struct {
	ID               string
	ExpectedRevision int64
	Name             string
	Scheme           string
	Host             string
	Port             int
	AddressScope     string
	Enabled          bool
	CredentialMode   string
	Credential       *outboundProxyCredential
}

type outboundProxyUpdateResult struct {
	View                   outboundProxyView
	ConnectionChanged      bool
	ChangedBoundAccountIDs []string
}

type upstreamProxyBindingInput struct {
	UpstreamID               string
	ExpectedUpstreamRevision int64
	ProxyID                  string
	ExpectedProxyRevision    int64
	Bind                     bool
}

type upstreamProxyBindingView struct {
	UpstreamID              string
	ProxyID                 string
	ProxyConnectionRevision int64
	CreatedAt               time.Time
	UpdatedAt               time.Time
}

type upstreamProxyBindingResult struct {
	Binding          *upstreamProxyBindingView
	UpstreamRevision int64
	PreviousProxyID  string
}

type outboundProxyBindingSnapshot struct {
	UpstreamID       string
	UpstreamRevision int64
	Proxy            outboundProxyView
	Credential       *outboundProxyCredential
}

func newOutboundProxyStore(db *sql.DB, secretStore *secrets, allowLoopbackForTesting ...bool) *outboundProxyStore {
	allowLoopback := len(allowLoopbackForTesting) == 1 && allowLoopbackForTesting[0]
	return &outboundProxyStore{db: db, secrets: secretStore, now: func() time.Time { return time.Now().UTC() }, allowLoopbackForTesting: allowLoopback}
}

func (s *outboundProxyStore) Migrate(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errOutboundProxyInvalid
	}
	return migrateOutboundProxyStore(ctx, s.db, s.allowLoopbackForTesting)
}

func migrateOutboundProxyStore(ctx context.Context, db *sql.DB, allowLoopbackForTesting ...bool) error {
	if db == nil {
		return errOutboundProxyInvalid
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("outbound proxy migration failed")
	}
	defer tx.Rollback()
	for _, statement := range []struct{ create, prefix string }{
		{outboundProxyDDL, "CREATE TABLE "},
		{outboundProxyCreatedIndexDDL, "CREATE INDEX "},
		{upstreamProxyBindingDDL, "CREATE TABLE "},
		{upstreamProxyProxyIndexDDL, "CREATE INDEX "},
	} {
		if _, err := tx.ExecContext(ctx, strings.Replace(statement.create, statement.prefix, statement.prefix+"IF NOT EXISTS ", 1)); err != nil {
			return errors.New("outbound proxy migration failed")
		}
	}
	allowLoopback := len(allowLoopbackForTesting) == 1 && allowLoopbackForTesting[0]
	if err := validateOutboundProxySchema(ctx, tx, allowLoopback); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errors.New("outbound proxy migration failed")
	}
	return nil
}

func (s *outboundProxyStore) Create(ctx context.Context, input outboundProxyCreateInput) (outboundProxyView, error) {
	var zero outboundProxyView
	if s == nil || s.db == nil || s.secrets == nil || !validUUIDOperation(input.OperationID) {
		return zero, errOutboundProxyInvalid
	}
	canonical, ok := canonicalOutboundProxyWithLoopback(input.Name, input.Scheme, input.Host, input.Port, input.AddressScope, s.allowLoopbackForTesting)
	if !ok || input.Credential != nil && !validOutboundProxyCredential(*input.Credential) {
		return zero, errOutboundProxyInvalid
	}
	input.Name, input.Scheme, input.Host, input.AddressScope = canonical.name, canonical.scheme, canonical.host, canonical.scope
	fingerprint, err := s.createFingerprint(input)
	if err != nil {
		return zero, errOutboundProxyInvalid
	}
	if existing, storedFingerprint, found, err := s.findByOperation(ctx, input.OperationID); err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	} else if found {
		if subtle.ConstantTimeCompare(fingerprint, storedFingerprint) != 1 {
			return zero, errOutboundProxyOperationConflict
		}
		return existing, nil
	}
	id, err := newID("proxy")
	if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	var ciphertext []byte
	var keyVersion any
	if input.Credential != nil {
		ciphertext, err = s.secrets.encryptOutboundProxyCredential(id, *input.Credential)
		if err != nil {
			return zero, errOutboundProxyInvalid
		}
		keyVersion = outboundProxyCredentialVersion
	}
	now := s.now().UTC()
	stamp := formatAccountPoolTime(now)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO outbound_proxies(id,name,scheme,host,port,address_scope,enabled,revision,connection_revision,credential_ciphertext,credential_key_version,operation_id,input_fingerprint,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, input.Name, input.Scheme, input.Host, input.Port, input.AddressScope, boolInt(input.Enabled), 1, 1, nullableProxyBlob(ciphertext), keyVersion, input.OperationID, fingerprint, stamp, stamp)
	if err != nil {
		// Resolve a concurrent replay only after releasing this transaction's snapshot.
		tx.Rollback()
		if existing, storedFingerprint, found, lookupErr := s.findByOperation(ctx, input.OperationID); lookupErr == nil && found {
			if subtle.ConstantTimeCompare(fingerprint, storedFingerprint) != 1 {
				return zero, errOutboundProxyOperationConflict
			}
			return existing, nil
		}
		return zero, errors.New("outbound proxy storage unavailable")
	}
	if err := tx.Commit(); err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	return outboundProxyView{id, input.Name, input.Scheme, input.Host, input.Port, input.AddressScope, input.Enabled, 1, 1, input.Credential != nil, now, now}, nil
}

func (s *outboundProxyStore) Get(ctx context.Context, id string) (outboundProxyView, error) {
	if s == nil || s.db == nil || !validIdentifier(id, 128) {
		return outboundProxyView{}, errOutboundProxyInvalid
	}
	view, _, err := scanOutboundProxy(s.db.QueryRowContext(ctx, outboundProxySelect+` WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return outboundProxyView{}, errOutboundProxyNotFound
	}
	if err != nil {
		return outboundProxyView{}, errors.New("outbound proxy storage unavailable")
	}
	return view, nil
}

func (s *outboundProxyStore) List(ctx context.Context, afterID string, limit int) ([]outboundProxyView, error) {
	if s == nil || s.db == nil || limit < 1 || limit > 200 || afterID != "" && !validIdentifier(afterID, 128) {
		return nil, errOutboundProxyInvalid
	}
	rows, err := s.db.QueryContext(ctx, outboundProxySelect+` WHERE id>? ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, errors.New("outbound proxy storage unavailable")
	}
	defer rows.Close()
	items := make([]outboundProxyView, 0)
	for rows.Next() {
		item, _, scanErr := scanOutboundProxy(rows)
		if scanErr != nil {
			return nil, errors.New("outbound proxy storage unavailable")
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("outbound proxy storage unavailable")
	}
	return items, nil
}

func (s *outboundProxyStore) Update(ctx context.Context, input outboundProxyUpdateInput) (outboundProxyUpdateResult, error) {
	var zero outboundProxyUpdateResult
	if s == nil || s.db == nil || s.secrets == nil || !validIdentifier(input.ID, 128) || input.ExpectedRevision < 1 || input.ExpectedRevision > outboundProxyMaxRevision {
		return zero, errOutboundProxyInvalid
	}
	canonical, ok := canonicalOutboundProxyWithLoopback(input.Name, input.Scheme, input.Host, input.Port, input.AddressScope, s.allowLoopbackForTesting)
	if !ok || input.CredentialMode != outboundProxyCredentialKeep && input.CredentialMode != outboundProxyCredentialReplace && input.CredentialMode != outboundProxyCredentialClear ||
		input.CredentialMode == outboundProxyCredentialReplace && (input.Credential == nil || !validOutboundProxyCredential(*input.Credential)) ||
		input.CredentialMode != outboundProxyCredentialReplace && input.Credential != nil {
		return zero, errOutboundProxyInvalid
	}
	input.Name, input.Scheme, input.Host, input.AddressScope = canonical.name, canonical.scheme, canonical.host, canonical.scope
	var replacement []byte
	if input.CredentialMode == outboundProxyCredentialReplace {
		var err error
		replacement, err = s.secrets.encryptOutboundProxyCredential(input.ID, *input.Credential)
		if err != nil {
			return zero, errOutboundProxyInvalid
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	defer tx.Rollback()
	current, _, err := scanOutboundProxy(tx.QueryRowContext(ctx, outboundProxySelect+` WHERE id=?`, input.ID))
	if errors.Is(err, sql.ErrNoRows) {
		return zero, errOutboundProxyNotFound
	}
	if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	if current.Revision != input.ExpectedRevision {
		return zero, errOutboundProxyConflict
	}
	if current.Revision >= outboundProxyMaxRevision {
		return zero, errOutboundProxyRevisionOverflow
	}
	credentialChanged := input.CredentialMode == outboundProxyCredentialReplace || input.CredentialMode == outboundProxyCredentialClear && current.HasCredentials
	connectionChanged := current.Scheme != input.Scheme || current.Host != input.Host || current.Port != input.Port || current.AddressScope != input.AddressScope || current.Enabled != input.Enabled || credentialChanged
	if connectionChanged && current.ConnectionRevision >= outboundProxyMaxRevision {
		return zero, errOutboundProxyRevisionOverflow
	}
	boundIDs, err := proxyBoundAccounts(ctx, tx, input.ID)
	if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	if connectionChanged && len(boundIDs) != 0 {
		var overflow int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM upstreams u JOIN upstream_proxy_bindings b ON b.upstream_id=u.id WHERE b.proxy_id=? AND u.revision>=?`, input.ID, outboundProxyMaxRevision).Scan(&overflow); err != nil {
			return zero, errors.New("outbound proxy storage unavailable")
		}
		if overflow != 0 {
			return zero, errOutboundProxyRevisionOverflow
		}
	}
	now := s.now().UTC()
	if now.Before(current.UpdatedAt) {
		now = current.UpdatedAt
	}
	connectionRevision := current.ConnectionRevision
	if connectionChanged {
		connectionRevision++
	}
	credentialCiphertext, credentialVersion := any(nil), any(nil)
	switch input.CredentialMode {
	case outboundProxyCredentialKeep:
		// Preserve without reading it into application memory.
	case outboundProxyCredentialReplace:
		credentialCiphertext, credentialVersion = replacement, outboundProxyCredentialVersion
	case outboundProxyCredentialClear:
		credentialCiphertext, credentialVersion = nil, nil
	}
	query := `UPDATE outbound_proxies SET name=?,scheme=?,host=?,port=?,address_scope=?,enabled=?,revision=revision+1,connection_revision=?,updated_at=?`
	args := []any{input.Name, input.Scheme, input.Host, input.Port, input.AddressScope, boolInt(input.Enabled), connectionRevision, formatAccountPoolTime(now)}
	if input.CredentialMode != outboundProxyCredentialKeep {
		query += `,credential_ciphertext=?,credential_key_version=?`
		args = append(args, credentialCiphertext, credentialVersion)
	}
	query += ` WHERE id=? AND revision=?`
	args = append(args, input.ID, input.ExpectedRevision)
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return zero, errOutboundProxyConflict
	}
	if connectionChanged && len(boundIDs) != 0 {
		changedAccounts, err := tx.ExecContext(ctx, `UPDATE upstreams SET revision=revision+1 WHERE id IN (SELECT upstream_id FROM upstream_proxy_bindings WHERE proxy_id=?)`, input.ID)
		if err != nil {
			return zero, errors.New("outbound proxy storage unavailable")
		}
		if affected, err := changedAccounts.RowsAffected(); err != nil || affected != int64(len(boundIDs)) {
			return zero, errOutboundProxyConflict
		}
		changedBindings, err := tx.ExecContext(ctx, `UPDATE upstream_proxy_bindings SET proxy_connection_revision=?,updated_at=? WHERE proxy_id=?`, connectionRevision, formatAccountPoolTime(now), input.ID)
		if err != nil {
			return zero, errors.New("outbound proxy storage unavailable")
		}
		if affected, err := changedBindings.RowsAffected(); err != nil || affected != int64(len(boundIDs)) {
			return zero, errOutboundProxyConflict
		}
	}
	view, _, err := scanOutboundProxy(tx.QueryRowContext(ctx, outboundProxySelect+` WHERE id=?`, input.ID))
	if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	if err := tx.Commit(); err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	if !connectionChanged {
		boundIDs = nil
	}
	return outboundProxyUpdateResult{View: view, ConnectionChanged: connectionChanged, ChangedBoundAccountIDs: boundIDs}, nil
}

func (s *outboundProxyStore) SetBinding(ctx context.Context, input upstreamProxyBindingInput) (upstreamProxyBindingResult, error) {
	var zero upstreamProxyBindingResult
	if s == nil || s.db == nil || s.secrets == nil || !validIdentifier(input.UpstreamID, 128) || !validIdentifier(input.ProxyID, 128) ||
		input.ExpectedUpstreamRevision < 1 || input.ExpectedUpstreamRevision > outboundProxyMaxRevision || input.ExpectedProxyRevision < 1 || input.ExpectedProxyRevision > outboundProxyMaxRevision {
		return zero, errOutboundProxyInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	defer tx.Rollback()
	var provider, endpoint string
	var upstreamRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT provider_kind,endpoint,revision FROM upstreams WHERE id=?`, input.UpstreamID).Scan(&provider, &endpoint, &upstreamRevision); errors.Is(err, sql.ErrNoRows) {
		return zero, errOutboundProxyNotFound
	} else if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	if upstreamRevision != input.ExpectedUpstreamRevision {
		return zero, errUpstreamProxyBindingConflict
	}
	if upstreamRevision >= outboundProxyMaxRevision {
		return zero, errOutboundProxyRevisionOverflow
	}
	if !validProxyBindingProvider(provider) || !validHTTPSUpstreamEndpoint(endpoint) {
		return zero, errOutboundProxyInvalid
	}
	proxy, storedCredential, err := scanOutboundProxy(tx.QueryRowContext(ctx, outboundProxySelect+` WHERE id=?`, input.ProxyID))
	if errors.Is(err, sql.ErrNoRows) {
		return zero, errOutboundProxyNotFound
	}
	if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	if proxy.Revision != input.ExpectedProxyRevision {
		return zero, errUpstreamProxyBindingConflict
	}
	var existing upstreamProxyBindingView
	hasExisting := true
	err = scanBinding(tx.QueryRowContext(ctx, upstreamProxyBindingSelect+` WHERE upstream_id=?`, input.UpstreamID), &existing)
	if errors.Is(err, sql.ErrNoRows) {
		hasExisting = false
	} else if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	if !input.Bind {
		if !hasExisting || existing.ProxyID != input.ProxyID {
			return zero, errUpstreamProxyBindingConflict
		}
		deleted, err := tx.ExecContext(ctx, `DELETE FROM upstream_proxy_bindings WHERE upstream_id=? AND proxy_id=?`, input.UpstreamID, input.ProxyID)
		if err != nil {
			return zero, errors.New("outbound proxy storage unavailable")
		}
		if affected, err := deleted.RowsAffected(); err != nil || affected != 1 {
			return zero, errUpstreamProxyBindingConflict
		}
		changed, err := tx.ExecContext(ctx, `UPDATE upstreams SET revision=revision+1 WHERE id=? AND revision=?`, input.UpstreamID, input.ExpectedUpstreamRevision)
		if err != nil {
			return zero, errors.New("outbound proxy storage unavailable")
		}
		if affected, err := changed.RowsAffected(); err != nil || affected != 1 {
			return zero, errUpstreamProxyBindingConflict
		}
		if err := tx.Commit(); err != nil {
			return zero, errors.New("outbound proxy storage unavailable")
		}
		return upstreamProxyBindingResult{UpstreamRevision: upstreamRevision + 1, PreviousProxyID: existing.ProxyID}, nil
	}
	canonical, proxyValid := canonicalOutboundProxyWithLoopback(proxy.Name, proxy.Scheme, proxy.Host, proxy.Port, proxy.AddressScope, s.allowLoopbackForTesting)
	if !proxyValid || canonical.host != proxy.Host {
		return zero, errOutboundProxyInvalid
	}
	if hasExisting && existing.ProxyID == input.ProxyID {
		return zero, errUpstreamProxyBindingConflict
	}
	if storedCredential.present {
		credential, decryptErr := s.secrets.decryptOutboundProxyCredential(input.ProxyID, storedCredential.version, storedCredential.ciphertext)
		if decryptErr != nil || !validOutboundProxyCredential(credential) {
			return zero, errOutboundProxyInvalid
		}
	}
	now := s.now().UTC()
	if hasExisting && now.Before(existing.UpdatedAt) {
		now = existing.UpdatedAt
	}
	stamp := formatAccountPoolTime(now)
	created := stamp
	previous := ""
	if hasExisting {
		created = formatAccountPoolTime(existing.CreatedAt)
		previous = existing.ProxyID
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO upstream_proxy_bindings(upstream_id,proxy_id,proxy_connection_revision,created_at,updated_at) VALUES(?,?,?,?,?) ON CONFLICT(upstream_id) DO UPDATE SET proxy_id=excluded.proxy_id,proxy_connection_revision=excluded.proxy_connection_revision,updated_at=excluded.updated_at`, input.UpstreamID, input.ProxyID, proxy.ConnectionRevision, created, stamp); err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	changed, err := tx.ExecContext(ctx, `UPDATE upstreams SET revision=revision+1 WHERE id=? AND revision=?`, input.UpstreamID, input.ExpectedUpstreamRevision)
	if err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	if affected, err := changed.RowsAffected(); err != nil || affected != 1 {
		return zero, errUpstreamProxyBindingConflict
	}
	binding := &upstreamProxyBindingView{input.UpstreamID, input.ProxyID, proxy.ConnectionRevision, mustProxyTime(created), now}
	if err := tx.Commit(); err != nil {
		return zero, errors.New("outbound proxy storage unavailable")
	}
	return upstreamProxyBindingResult{Binding: binding, UpstreamRevision: upstreamRevision + 1, PreviousProxyID: previous}, nil
}

func (s *outboundProxyStore) Binding(ctx context.Context, upstreamID string) (*upstreamProxyBindingView, error) {
	if s == nil || s.db == nil || !validIdentifier(upstreamID, 128) {
		return nil, errOutboundProxyInvalid
	}
	var view upstreamProxyBindingView
	err := scanBinding(s.db.QueryRowContext(ctx, upstreamProxyBindingSelect+` WHERE upstream_id=?`, upstreamID), &view)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("outbound proxy storage unavailable")
	}
	return &view, nil
}

// LoadBindingTx freezes the current binding and decrypted proxy credential in
// the caller's admission transaction. A nil snapshot is the only direct-route
// result. Bound but unusable proxies fail closed and are never treated as nil.
func (s *outboundProxyStore) LoadBindingTx(ctx context.Context, tx *sql.Tx, upstreamID string, expectedUpstreamRevision int64) (*outboundProxyBindingSnapshot, error) {
	if s == nil || s.secrets == nil || tx == nil || !validIdentifier(upstreamID, 128) || expectedUpstreamRevision < 1 || expectedUpstreamRevision > outboundProxyMaxRevision {
		return nil, errOutboundProxyInvalid
	}
	var provider, endpoint string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT provider_kind,endpoint,revision FROM upstreams WHERE id=?`, upstreamID).Scan(&provider, &endpoint, &revision); errors.Is(err, sql.ErrNoRows) {
		return nil, errOutboundProxyNotFound
	} else if err != nil {
		return nil, errors.New("outbound proxy storage unavailable")
	}
	if revision != expectedUpstreamRevision {
		return nil, errUpstreamProxyBindingConflict
	}
	var binding upstreamProxyBindingView
	if err := scanBinding(tx.QueryRowContext(ctx, upstreamProxyBindingSelect+` WHERE upstream_id=?`, upstreamID), &binding); errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, errors.New("outbound proxy storage unavailable")
	}
	if !validProxyBindingProvider(provider) || !validHTTPSUpstreamEndpoint(endpoint) {
		return nil, errOutboundProxyUnavailable
	}
	proxy, storedCredential, err := scanOutboundProxy(tx.QueryRowContext(ctx, outboundProxySelect+` WHERE id=?`, binding.ProxyID))
	if err != nil {
		return nil, errOutboundProxyUnavailable
	}
	canonical, proxyValid := canonicalOutboundProxyWithLoopback(proxy.Name, proxy.Scheme, proxy.Host, proxy.Port, proxy.AddressScope, s.allowLoopbackForTesting)
	if !proxyValid || canonical.host != proxy.Host || !proxy.Enabled || proxy.ConnectionRevision != binding.ProxyConnectionRevision {
		return nil, errOutboundProxyUnavailable
	}
	var credential *outboundProxyCredential
	if storedCredential.present {
		value, err := s.secrets.decryptOutboundProxyCredential(proxy.ID, storedCredential.version, storedCredential.ciphertext)
		if err != nil {
			return nil, errOutboundProxyUnavailable
		}
		credential = &value
	}
	return &outboundProxyBindingSnapshot{UpstreamID: upstreamID, UpstreamRevision: revision, Proxy: proxy, Credential: credential}, nil
}

type canonicalProxy struct{ name, scheme, host, scope string }

func canonicalOutboundProxy(name, scheme, host string, port int, scope string) (canonicalProxy, bool) {
	return canonicalOutboundProxyWithLoopback(name, scheme, host, port, scope, false)
}

func canonicalOutboundProxyWithLoopback(name, scheme, host string, port int, scope string, allowLoopbackForTesting bool) (canonicalProxy, bool) {
	if !validProxyName(name) || scheme != "https" || port < 1 || port > 65535 || scope != "public" && scope != "private" {
		return canonicalProxy{}, false
	}
	canonicalHost, literalPrivate, literal, ok := canonicalProxyHost(host, allowLoopbackForTesting)
	if !ok || literal && (scope == "private") != literalPrivate {
		return canonicalProxy{}, false
	}
	return canonicalProxy{name, scheme, canonicalHost, scope}, true
}

func validProxyName(value string) bool {
	if strings.TrimSpace(value) != value || !utf8.ValidString(value) || len(value) < 1 || len(value) > 120 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func canonicalProxyHost(host string, allowLoopbackForTesting bool) (canonical string, private, literal, ok bool) {
	if host == "" || len(host) > 253 || strings.TrimSpace(host) != host || strings.ContainsAny(host, "@/?#[]%") {
		return "", false, false, false
	}
	for _, r := range host {
		if r > unicode.MaxASCII || unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", false, false, false
		}
	}
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if address.IsLoopback() && allowLoopbackForTesting {
			return address.String(), true, true, true
		}
		if address.IsUnspecified() || address.IsLoopback() || address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() || address.IsMulticast() || isCGNAT(address) {
			return "", false, true, false
		}
		isPrivate := address.IsPrivate()
		if !isPrivate && !address.IsGlobalUnicast() {
			return "", false, true, false
		}
		return address.String(), isPrivate, true, true
	}
	if strings.Contains(host, ":") || strings.HasSuffix(host, ".") {
		return "", false, false, false
	}
	allNumeric := true
	for _, ch := range host {
		if ch != '.' && (ch < '0' || ch > '9') {
			allNumeric = false
			break
		}
	}
	if allNumeric {
		return "", false, false, false
	}
	lower := strings.ToLower(host)
	if lower == "localhost" {
		return "", false, false, false
	}
	for _, label := range strings.Split(lower, ".") {
		if len(label) < 1 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false, false, false
		}
		for _, ch := range label {
			if !(ch == '-' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9') {
				return "", false, false, false
			}
		}
	}
	return lower, false, false, true
}

func isCGNAT(address netip.Addr) bool {
	prefix := netip.MustParsePrefix("100.64.0.0/10")
	return address.Is4() && prefix.Contains(address)
}

func validHTTPSUpstreamEndpoint(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && !strings.ContainsAny(value, "\r\n\x00")
}

func validProxyBindingProvider(provider string) bool {
	return provider == "openai-compatible" || provider == "anthropic-api-key" || provider == "gemini-api-key"
}

func (s *outboundProxyStore) createFingerprint(input outboundProxyCreateInput) ([]byte, error) {
	if s.secrets == nil || len(s.secrets.digestKey) == 0 {
		return nil, errOutboundProxyInvalid
	}
	payload, err := json.Marshal(struct {
		Name, Scheme, Host, Scope string
		Port                      int
		Enabled                   bool
		Username, Password        string
	}{input.Name, input.Scheme, input.Host, input.AddressScope, input.Port, input.Enabled, proxyCredentialUsername(input.Credential), proxyCredentialPassword(input.Credential)})
	if err != nil {
		return nil, err
	}
	defer clear(payload)
	h := hmac.New(sha256.New, s.secrets.digestKey)
	h.Write([]byte("cpacloud/outbound-proxy-create/v1\x00"))
	h.Write(payload)
	return h.Sum(nil), nil
}

func proxyCredentialUsername(value *outboundProxyCredential) string {
	if value == nil {
		return ""
	}
	return value.username
}
func proxyCredentialPassword(value *outboundProxyCredential) string {
	if value == nil {
		return ""
	}
	return value.password
}
func nullableProxyBlob(value []byte) any {
	if value == nil {
		return nil
	}
	return value
}

const outboundProxySelect = `SELECT id,name,scheme,host,port,address_scope,enabled,revision,connection_revision,credential_ciphertext,credential_key_version,created_at,updated_at FROM outbound_proxies`

type storedProxyCredential struct {
	present    bool
	ciphertext []byte
	version    int
}

type proxyScanner interface{ Scan(...any) error }

func scanOutboundProxy(scanner proxyScanner) (outboundProxyView, storedProxyCredential, error) {
	var item outboundProxyView
	var enabled int
	var ciphertext []byte
	var version sql.NullInt64
	var created, updated string
	err := scanner.Scan(&item.ID, &item.Name, &item.Scheme, &item.Host, &item.Port, &item.AddressScope, &enabled, &item.Revision, &item.ConnectionRevision, &ciphertext, &version, &created, &updated)
	if err != nil {
		return item, storedProxyCredential{}, err
	}
	item.Enabled = enabled != 0
	item.HasCredentials = version.Valid
	item.CreatedAt, err = parseCanonicalProxyTime(created)
	if err == nil {
		item.UpdatedAt, err = parseCanonicalProxyTime(updated)
	}
	if err != nil {
		return item, storedProxyCredential{}, err
	}
	credential := storedProxyCredential{present: version.Valid, ciphertext: ciphertext}
	if version.Valid {
		credential.version = int(version.Int64)
	}
	return item, credential, nil
}

func (s *outboundProxyStore) findByOperation(ctx context.Context, operationID string) (outboundProxyView, []byte, bool, error) {
	var item outboundProxyView
	var enabled int
	var ciphertext []byte
	var version sql.NullInt64
	var created, updated string
	var fingerprint []byte
	err := s.db.QueryRowContext(ctx, `SELECT id,name,scheme,host,port,address_scope,enabled,revision,connection_revision,credential_ciphertext,credential_key_version,created_at,updated_at,input_fingerprint FROM outbound_proxies WHERE operation_id=?`, operationID).Scan(&item.ID, &item.Name, &item.Scheme, &item.Host, &item.Port, &item.AddressScope, &enabled, &item.Revision, &item.ConnectionRevision, &ciphertext, &version, &created, &updated, &fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return item, nil, false, nil
	}
	if err != nil {
		return item, nil, false, err
	}
	item.Enabled, item.HasCredentials = enabled != 0, version.Valid
	item.CreatedAt, err = parseCanonicalProxyTime(created)
	if err == nil {
		item.UpdatedAt, err = parseCanonicalProxyTime(updated)
	}
	return item, fingerprint, true, err
}

func proxyBoundAccounts(ctx context.Context, tx *sql.Tx, proxyID string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT upstream_id FROM upstream_proxy_bindings WHERE proxy_id=? ORDER BY upstream_id`, proxyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		items = append(items, id)
	}
	return items, rows.Err()
}

const upstreamProxyBindingSelect = `SELECT upstream_id,proxy_id,proxy_connection_revision,created_at,updated_at FROM upstream_proxy_bindings`

func scanBinding(scanner proxyScanner, item *upstreamProxyBindingView) error {
	var created, updated string
	if err := scanner.Scan(&item.UpstreamID, &item.ProxyID, &item.ProxyConnectionRevision, &created, &updated); err != nil {
		return err
	}
	var err error
	item.CreatedAt, err = parseCanonicalProxyTime(created)
	if err == nil {
		item.UpdatedAt, err = parseCanonicalProxyTime(updated)
	}
	return err
}

func mustProxyTime(value string) time.Time {
	parsed, _ := parseCanonicalProxyTime(value)
	return parsed
}
func parseCanonicalProxyTime(value string) (time.Time, error) {
	parsed, err := parseTime(value)
	if err != nil || formatAccountPoolTime(parsed) != value {
		return time.Time{}, errOutboundProxyInvalid
	}
	return parsed, nil
}

func validateOutboundProxySchema(ctx context.Context, tx *sql.Tx, allowLoopbackForTesting bool) error {
	if err := validateProxyTable(ctx, tx, "outbound_proxies", outboundProxyDDL, map[string]string{"outbound_proxies_created_idx": outboundProxyCreatedIndexDDL}, 2); err != nil {
		return err
	}
	if err := validateProxyTable(ctx, tx, "upstream_proxy_bindings", upstreamProxyBindingDDL, map[string]string{"upstream_proxy_bindings_proxy_idx": upstreamProxyProxyIndexDDL}, 1); err != nil {
		return err
	}
	if err := validateProxyForeignKeys(ctx, tx); err != nil {
		return err
	}
	return validateStoredOutboundProxies(ctx, tx, allowLoopbackForTesting)
}

func validateProxyTable(ctx context.Context, tx *sql.Tx, table, expected string, indexes map[string]string, automatic int) error {
	var kind, ddl string
	if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, table).Scan(&kind, &ddl); err != nil || kind != "table" || normalizeProxySQL(ddl) != normalizeProxySQL(expected) {
		return errors.New("outbound proxy schema is incompatible")
	}
	for name, expectedDDL := range indexes {
		var indexKind, indexDDL string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&indexKind, &indexDDL); err != nil || indexKind != "index" || normalizeProxySQL(indexDDL) != normalizeProxySQL(expectedDDL) {
			return errors.New("outbound proxy schema is incompatible")
		}
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA index_list(`+table+`)`)
	if err != nil {
		return errors.New("outbound proxy schema is incompatible")
	}
	defer rows.Close()
	seen, auto := map[string]bool{}, 0
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			return errors.New("outbound proxy schema is incompatible")
		}
		if origin == "c" {
			if _, ok := indexes[name]; !ok || unique != 0 || partial != 0 {
				return errors.New("outbound proxy schema is incompatible")
			}
			seen[name] = true
			continue
		}
		if (origin != "pk" && origin != "u") || unique != 1 || partial != 0 {
			return errors.New("outbound proxy schema is incompatible")
		}
		auto++
	}
	if rows.Err() != nil || len(seen) != len(indexes) || auto != automatic {
		return errors.New("outbound proxy schema is incompatible")
	}
	return nil
}

func normalizeProxySQL(value string) string {
	runes := []rune(value)
	var out strings.Builder
	var quote rune
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if quote != 0 {
			out.WriteRune(r)
			if r == quote {
				if i+1 < len(runes) && runes[i+1] == quote {
					out.WriteRune(runes[i+1])
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		switch r {
		case '\'', '"', '`':
			quote = r
			out.WriteRune(r)
		case '[':
			quote = ']'
			out.WriteRune(r)
		default:
			if !unicode.IsSpace(r) {
				out.WriteRune(unicode.ToLower(r))
			}
		}
	}
	return strings.Replace(out.String(), "createtableifnotexists", "createtable", 1)
}

func validateProxyForeignKeys(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_list(upstream_proxy_bindings)`)
	if err != nil {
		return errors.New("outbound proxy schema is incompatible")
	}
	wanted := map[string]string{"upstream_id": "upstreams", "proxy_id": "outbound_proxies"}
	seen := map[string]bool{}
	for rows.Next() {
		var id, sequence int
		var table, from, to, onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &table, &from, &to, &onUpdate, &onDelete, &match); err != nil {
			rows.Close()
			return errors.New("outbound proxy schema is incompatible")
		}
		if wanted[from] != table || to != "id" || onUpdate != "NO ACTION" || onDelete != "RESTRICT" || seen[from] {
			rows.Close()
			return errors.New("outbound proxy schema is incompatible")
		}
		seen[from] = true
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil || len(seen) != 2 {
		return errors.New("outbound proxy schema is incompatible")
	}
	violations, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return errors.New("outbound proxy schema is incompatible")
	}
	violated := violations.Next()
	iterationErr, closeErr = violations.Err(), violations.Close()
	if iterationErr != nil || closeErr != nil || violated {
		return errors.New("outbound proxy foreign key violation")
	}
	return nil
}

func validateStoredOutboundProxies(ctx context.Context, tx *sql.Tx, allowLoopbackForTesting bool) error {
	rows, err := tx.QueryContext(ctx, outboundProxySelect+` ORDER BY id`)
	if err != nil {
		return errors.New("invalid stored outbound proxy")
	}
	for rows.Next() {
		view, credential, err := scanOutboundProxy(rows)
		if err != nil || !validIdentifier(view.ID, 128) || view.Revision < 1 || view.Revision > outboundProxyMaxRevision || view.ConnectionRevision < 1 || view.ConnectionRevision > outboundProxyMaxRevision || view.UpdatedAt.Before(view.CreatedAt) {
			rows.Close()
			return errors.New("invalid stored outbound proxy")
		}
		canonical, ok := canonicalOutboundProxyWithLoopback(view.Name, view.Scheme, view.Host, view.Port, view.AddressScope, allowLoopbackForTesting)
		if !ok || canonical.host != view.Host {
			rows.Close()
			return errors.New("invalid stored outbound proxy")
		}
		if credential.present && (credential.version < 1 || len(credential.ciphertext) == 0 || len(credential.ciphertext) > 32<<10) {
			rows.Close()
			return errors.New("invalid stored outbound proxy")
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errors.New("invalid stored outbound proxy")
	}
	if err := rows.Close(); err != nil {
		return errors.New("invalid stored outbound proxy")
	}
	metadata, err := tx.QueryContext(ctx, `SELECT operation_id,input_fingerprint FROM outbound_proxies ORDER BY id`)
	if err != nil {
		return errors.New("invalid stored outbound proxy")
	}
	for metadata.Next() {
		var operationID string
		var fingerprint []byte
		if err := metadata.Scan(&operationID, &fingerprint); err != nil || !validUUIDOperation(operationID) || len(fingerprint) != sha256.Size {
			metadata.Close()
			return errors.New("invalid stored outbound proxy")
		}
	}
	if err := metadata.Err(); err != nil {
		metadata.Close()
		return errors.New("invalid stored outbound proxy")
	}
	if err := metadata.Close(); err != nil {
		return errors.New("invalid stored outbound proxy")
	}
	bindings, err := tx.QueryContext(ctx, upstreamProxyBindingSelect+` ORDER BY upstream_id`)
	if err != nil {
		return errors.New("invalid stored upstream proxy binding")
	}
	for bindings.Next() {
		var item upstreamProxyBindingView
		if err := scanBinding(bindings, &item); err != nil || !validIdentifier(item.UpstreamID, 128) || !validIdentifier(item.ProxyID, 128) || item.ProxyConnectionRevision < 1 || item.ProxyConnectionRevision > outboundProxyMaxRevision || item.UpdatedAt.Before(item.CreatedAt) {
			bindings.Close()
			return errors.New("invalid stored upstream proxy binding")
		}
		var provider, endpoint string
		var connectionRevision int64
		if err := tx.QueryRowContext(ctx, `SELECT u.provider_kind,u.endpoint,p.connection_revision FROM upstreams u JOIN outbound_proxies p ON p.id=? WHERE u.id=?`, item.ProxyID, item.UpstreamID).Scan(&provider, &endpoint, &connectionRevision); err != nil || !validProxyBindingProvider(provider) || !validHTTPSUpstreamEndpoint(endpoint) || connectionRevision != item.ProxyConnectionRevision {
			bindings.Close()
			return errors.New("invalid stored upstream proxy binding")
		}
	}
	if err := bindings.Err(); err != nil {
		bindings.Close()
		return errors.New("invalid stored upstream proxy binding")
	}
	if err := bindings.Close(); err != nil {
		return errors.New("invalid stored upstream proxy binding")
	}
	return nil
}
