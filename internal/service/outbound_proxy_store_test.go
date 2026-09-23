package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const outboundProxyTestUpstreamDDL = `CREATE TABLE upstreams (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key','codex-membership')),
	endpoint TEXT NOT NULL,
	enabled INTEGER NOT NULL,
	credential_ciphertext BLOB NOT NULL,
	key_version INTEGER NOT NULL,
	revision INTEGER NOT NULL,
	created_at TEXT NOT NULL,
	credential_state TEXT,
	verified_at TEXT,
	operation_id TEXT UNIQUE
)`

type outboundProxyTestStore struct {
	db      *sql.DB
	store   *outboundProxyStore
	path    string
	secrets *secrets
}

func newOutboundProxyTestStore(t *testing.T) outboundProxyTestStore {
	t.Helper()
	directory := t.TempDir()
	if err := createRootKey(directory); err != nil {
		t.Fatal(err)
	}
	secretStore, err := loadSecrets(directory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "proxy.db")
	db := openOutboundProxyTestDB(t, path)
	if _, err := db.Exec(outboundProxyTestUpstreamDDL); err != nil {
		t.Fatal(err)
	}
	store := newOutboundProxyStore(db, secretStore)
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return outboundProxyTestStore{db: db, store: store, path: path, secrets: secretStore}
}

func openOutboundProxyTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func testProxyCreate(operation, host string) outboundProxyCreateInput {
	credential := outboundProxyCredential{username: "proxy-user", password: "proxy-secret-value"}
	return outboundProxyCreateInput{OperationID: operation, Name: "Corporate proxy", Scheme: "https", Host: host, Port: 443, AddressScope: "public", Enabled: true, Credential: &credential}
}

func insertProxyTestUpstream(t *testing.T, db *sql.DB, id, provider, endpoint string, revision int64) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES(?,?,?,?,1,X'01',1,?,?)`, id, id, provider, endpoint, revision, formatAccountPoolTime(time.Now().UTC()))
	if err != nil {
		t.Fatal(err)
	}
}

func TestOutboundProxyCreateIdempotencyEncryptionAndRestart(t *testing.T) {
	f := newOutboundProxyTestStore(t)
	ctx := context.Background()
	input := testProxyCreate("10000000-0000-4000-8000-000000000001", "Proxy.Example")
	created, err := f.store.Create(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if created.Host != "proxy.example" || !created.HasCredentials || created.Revision != 1 || created.ConnectionRevision != 1 {
		t.Fatalf("created=%+v", created)
	}
	replayed, err := f.store.Create(ctx, input)
	if err != nil || replayed.ID != created.ID {
		t.Fatalf("replay=%+v err=%v", replayed, err)
	}
	changed := input
	changed.Port = 8443
	if _, err := f.store.Create(ctx, changed); !errors.Is(err, errOutboundProxyOperationConflict) {
		t.Fatalf("changed replay error=%v", err)
	}
	var ciphertext, fingerprint []byte
	if err := f.db.QueryRow(`SELECT credential_ciphertext,input_fingerprint FROM outbound_proxies WHERE id=?`, created.ID).Scan(&ciphertext, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if len(fingerprint) != 32 || strings.Contains(string(ciphertext), "proxy-user") || strings.Contains(string(ciphertext), "proxy-secret-value") || strings.Contains(string(fingerprint), "proxy-secret-value") {
		t.Fatal("stored values expose credentials or omit keyed fingerprint")
	}
	if _, err := f.secrets.decryptOutboundProxyCredential("proxy_other", outboundProxyCredentialVersion, ciphertext); !errors.Is(err, errOutboundProxyCredential) {
		t.Fatalf("wrong AAD error=%v", err)
	}
	credential, err := f.secrets.decryptOutboundProxyCredential(created.ID, outboundProxyCredentialVersion, ciphertext)
	if err != nil || credential.username != "proxy-user" || credential.password != "proxy-secret-value" {
		t.Fatalf("credential round trip failed: %v", err)
	}

	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openOutboundProxyTestDB(t, f.path)
	t.Cleanup(func() { reopened.Close() })
	f.db, f.store.db = reopened, reopened
	if err := f.store.Migrate(ctx); err != nil {
		t.Fatalf("restart migration: %v", err)
	}
	listed, err := f.store.List(ctx, "", 10)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID || !listed[0].HasCredentials {
		t.Fatalf("restart list=%+v err=%v", listed, err)
	}
	encoded := fmt.Sprintf("%v %#v %+q", *input.Credential, *input.Credential, *input.Credential)
	if strings.Contains(encoded, "proxy-user") || strings.Contains(encoded, "proxy-secret-value") {
		t.Fatalf("credential formatting leaked: %s", encoded)
	}
}

func TestOutboundProxyUpdateVersionsBindingsAndRollback(t *testing.T) {
	f := newOutboundProxyTestStore(t)
	ctx := context.Background()
	proxy, err := f.store.Create(ctx, testProxyCreate("10000000-0000-4000-8000-000000000002", "proxy.example"))
	if err != nil {
		t.Fatal(err)
	}
	insertProxyTestUpstream(t, f.db, "ups_bound", "openai-compatible", "https://api.example/v1", 5)
	insertProxyTestUpstream(t, f.db, "ups_bound_two", "anthropic-api-key", "https://api-two.example/v1", 10)
	binding, err := f.store.SetBinding(ctx, upstreamProxyBindingInput{UpstreamID: "ups_bound", ExpectedUpstreamRevision: 5, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: true})
	if err != nil || binding.UpstreamRevision != 6 || binding.Binding == nil || binding.Binding.ProxyConnectionRevision != 1 {
		t.Fatalf("bind=%+v err=%v", binding, err)
	}
	if _, err := f.store.SetBinding(ctx, upstreamProxyBindingInput{UpstreamID: "ups_bound_two", ExpectedUpstreamRevision: 10, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: true}); err != nil {
		t.Fatal(err)
	}
	nameOnly := outboundProxyUpdateInput{ID: proxy.ID, ExpectedRevision: 1, Name: "Renamed", Scheme: "https", Host: "proxy.example", Port: 443, AddressScope: "public", Enabled: true, CredentialMode: outboundProxyCredentialKeep}
	updated, err := f.store.Update(ctx, nameOnly)
	if err != nil || updated.ConnectionChanged || updated.View.Revision != 2 || updated.View.ConnectionRevision != 1 || len(updated.ChangedBoundAccountIDs) != 0 {
		t.Fatalf("name update=%+v err=%v", updated, err)
	}
	assertProxyTestRevisions(t, f.db, proxy.ID, "ups_bound", 2, 1, 6, 1)
	assertProxyTestRevisions(t, f.db, proxy.ID, "ups_bound_two", 2, 1, 11, 1)

	connection := nameOnly
	connection.ExpectedRevision = 2
	connection.Host = "proxy-two.example"
	updated, err = f.store.Update(ctx, connection)
	if err != nil || !updated.ConnectionChanged || len(updated.ChangedBoundAccountIDs) != 2 || updated.ChangedBoundAccountIDs[0] != "ups_bound" || updated.ChangedBoundAccountIDs[1] != "ups_bound_two" {
		t.Fatalf("connection update=%+v err=%v", updated, err)
	}
	assertProxyTestRevisions(t, f.db, proxy.ID, "ups_bound", 3, 2, 7, 2)
	assertProxyTestRevisions(t, f.db, proxy.ID, "ups_bound_two", 3, 2, 12, 2)

	if _, err := f.db.Exec(`CREATE TRIGGER fail_bound_revision BEFORE UPDATE OF revision ON upstreams BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	connection.ExpectedRevision = 3
	connection.Port = 8443
	if _, err := f.store.Update(ctx, connection); err == nil || strings.Contains(err.Error(), "synthetic") {
		t.Fatalf("trigger update error=%v", err)
	}
	assertProxyTestRevisions(t, f.db, proxy.ID, "ups_bound", 3, 2, 7, 2)
	assertProxyTestRevisions(t, f.db, proxy.ID, "ups_bound_two", 3, 2, 12, 2)
	if _, err := f.db.Exec(`DROP TRIGGER fail_bound_revision`); err != nil {
		t.Fatal(err)
	}
	updated, err = f.store.Update(ctx, connection)
	if err != nil || updated.View.Revision != 4 || updated.View.ConnectionRevision != 3 {
		t.Fatalf("retry update=%+v err=%v", updated, err)
	}
	assertProxyTestRevisions(t, f.db, proxy.ID, "ups_bound", 4, 3, 8, 3)
	assertProxyTestRevisions(t, f.db, proxy.ID, "ups_bound_two", 4, 3, 13, 3)

	if _, err := f.db.Exec(`UPDATE upstreams SET revision=? WHERE id='ups_bound'`, outboundProxyMaxRevision); err != nil {
		t.Fatal(err)
	}
	connection.ExpectedRevision = 4
	connection.Port = 9443
	if _, err := f.store.Update(ctx, connection); !errors.Is(err, errOutboundProxyRevisionOverflow) {
		t.Fatalf("overflow error=%v", err)
	}
	var proxyRevision, connectionRevision int64
	if err := f.db.QueryRow(`SELECT revision,connection_revision FROM outbound_proxies WHERE id=?`, proxy.ID).Scan(&proxyRevision, &connectionRevision); err != nil || proxyRevision != 4 || connectionRevision != 3 {
		t.Fatalf("overflow partly committed proxy=%d connection=%d err=%v", proxyRevision, connectionRevision, err)
	}
}

func assertProxyTestRevisions(t *testing.T, db *sql.DB, proxyID, upstreamID string, proxyRevision, connectionRevision, upstreamRevision, boundConnection int64) {
	t.Helper()
	var gotProxy, gotConnection, gotUpstream, gotBound int64
	if err := db.QueryRow(`SELECT revision,connection_revision FROM outbound_proxies WHERE id=?`, proxyID).Scan(&gotProxy, &gotConnection); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT u.revision,b.proxy_connection_revision FROM upstreams u JOIN upstream_proxy_bindings b ON b.upstream_id=u.id WHERE u.id=?`, upstreamID).Scan(&gotUpstream, &gotBound); err != nil {
		t.Fatal(err)
	}
	if gotProxy != proxyRevision || gotConnection != connectionRevision || gotUpstream != upstreamRevision || gotBound != boundConnection {
		t.Fatalf("revisions proxy=%d connection=%d upstream=%d binding=%d", gotProxy, gotConnection, gotUpstream, gotBound)
	}
}

func TestOutboundProxyBindingGuardsBadCredentialAndExplicitUnbind(t *testing.T) {
	f := newOutboundProxyTestStore(t)
	ctx := context.Background()
	proxy, err := f.store.Create(ctx, testProxyCreate("10000000-0000-4000-8000-000000000003", "proxy.example"))
	if err != nil {
		t.Fatal(err)
	}
	insertProxyTestUpstream(t, f.db, "ups_https", "anthropic-api-key", "https://api.anthropic.example/v1", 1)
	insertProxyTestUpstream(t, f.db, "ups_http", "openai-compatible", "http://api.example/v1", 1)
	insertProxyTestUpstream(t, f.db, "ups_codex", codexMembershipProvider, "https://codex.example/v1", 1)
	for _, upstream := range []string{"ups_http", "ups_codex"} {
		if _, err := f.store.SetBinding(ctx, upstreamProxyBindingInput{UpstreamID: upstream, ExpectedUpstreamRevision: 1, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: true}); !errors.Is(err, errOutboundProxyInvalid) {
			t.Fatalf("%s binding error=%v", upstream, err)
		}
	}
	if _, err := f.store.SetBinding(ctx, upstreamProxyBindingInput{UpstreamID: "ups_https", ExpectedUpstreamRevision: 1, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: true}); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.store.LoadBindingTx(ctx, tx, "ups_https", 2)
	tx.Rollback()
	if err != nil || snapshot == nil || snapshot.Credential == nil || snapshot.Credential.username != "proxy-user" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}

	if _, err := f.db.Exec(`UPDATE outbound_proxies SET credential_ciphertext=X'0102' WHERE id=?`, proxy.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err != nil {
		t.Fatalf("bad ciphertext should remain repairable: %v", err)
	}
	tx, _ = f.db.BeginTx(ctx, nil)
	_, err = f.store.LoadBindingTx(ctx, tx, "ups_https", 2)
	tx.Rollback()
	if !errors.Is(err, errOutboundProxyUnavailable) {
		t.Fatalf("bad credential load error=%v", err)
	}
	if binding, err := f.store.Binding(ctx, "ups_https"); err != nil || binding == nil || binding.ProxyID != proxy.ID {
		t.Fatalf("bad credential silently unbound: %+v err=%v", binding, err)
	}
	result, err := f.store.SetBinding(ctx, upstreamProxyBindingInput{UpstreamID: "ups_https", ExpectedUpstreamRevision: 2, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: false})
	if err != nil || result.Binding != nil || result.UpstreamRevision != 3 {
		t.Fatalf("explicit unbind=%+v err=%v", result, err)
	}
	if binding, err := f.store.Binding(ctx, "ups_https"); err != nil || binding != nil {
		t.Fatalf("binding after unbind=%+v err=%v", binding, err)
	}
	if _, err := f.store.SetBinding(ctx, upstreamProxyBindingInput{UpstreamID: "ups_https", ExpectedUpstreamRevision: 3, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: false}); !errors.Is(err, errUpstreamProxyBindingConflict) {
		t.Fatalf("repeated unbind error=%v", err)
	}
}

func TestOutboundProxyDisabledRemainsBoundAndDirectIsExplicit(t *testing.T) {
	f := newOutboundProxyTestStore(t)
	ctx := context.Background()
	proxy, _ := f.store.Create(ctx, outboundProxyCreateInput{OperationID: "10000000-0000-4000-8000-000000000004", Name: "No auth", Scheme: "https", Host: "proxy.example", Port: 443, AddressScope: "public", Enabled: true})
	insertProxyTestUpstream(t, f.db, "ups_disabled", "gemini-api-key", "https://generativelanguage.googleapis.com/v1beta", 4)
	if _, err := f.store.SetBinding(ctx, upstreamProxyBindingInput{UpstreamID: "ups_disabled", ExpectedUpstreamRevision: 4, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: true}); err != nil {
		t.Fatal(err)
	}
	updated, err := f.store.Update(ctx, outboundProxyUpdateInput{ID: proxy.ID, ExpectedRevision: 1, Name: proxy.Name, Scheme: proxy.Scheme, Host: proxy.Host, Port: proxy.Port, AddressScope: proxy.AddressScope, Enabled: false, CredentialMode: outboundProxyCredentialKeep})
	if err != nil || !updated.ConnectionChanged {
		t.Fatalf("disable=%+v err=%v", updated, err)
	}
	if binding, _ := f.store.Binding(ctx, "ups_disabled"); binding == nil {
		t.Fatal("disable silently removed binding")
	}
	tx, _ := f.db.BeginTx(ctx, nil)
	_, err = f.store.LoadBindingTx(ctx, tx, "ups_disabled", 6)
	tx.Rollback()
	if !errors.Is(err, errOutboundProxyUnavailable) {
		t.Fatalf("disabled load error=%v", err)
	}
	if _, err := f.store.SetBinding(ctx, upstreamProxyBindingInput{UpstreamID: "ups_disabled", ExpectedUpstreamRevision: 6, ProxyID: proxy.ID, ExpectedProxyRevision: 2, Bind: false}); err != nil {
		t.Fatalf("explicit unbind of disabled proxy: %v", err)
	}
	insertProxyTestUpstream(t, f.db, "ups_direct", "openai-compatible", "https://api.example/v1", 1)
	tx, _ = f.db.BeginTx(ctx, nil)
	direct, err := f.store.LoadBindingTx(ctx, tx, "ups_direct", 1)
	tx.Rollback()
	if err != nil || direct != nil {
		t.Fatalf("unbound route=%+v err=%v", direct, err)
	}
}

func TestOutboundProxyCredentialEditsAdvanceOnlyEffectiveConnectionChanges(t *testing.T) {
	f := newOutboundProxyTestStore(t)
	ctx := context.Background()
	created, err := f.store.Create(ctx, testProxyCreate("10000000-0000-4000-8000-000000000009", "proxy.example"))
	if err != nil {
		t.Fatal(err)
	}
	base := outboundProxyUpdateInput{ID: created.ID, Name: created.Name, Scheme: created.Scheme, Host: created.Host, Port: created.Port, AddressScope: created.AddressScope, Enabled: created.Enabled}
	base.ExpectedRevision, base.CredentialMode = 1, outboundProxyCredentialClear
	cleared, err := f.store.Update(ctx, base)
	if err != nil || !cleared.ConnectionChanged || cleared.View.HasCredentials || cleared.View.ConnectionRevision != 2 {
		t.Fatalf("clear=%+v err=%v", cleared, err)
	}
	base.ExpectedRevision = 2
	clearedAgain, err := f.store.Update(ctx, base)
	if err != nil || clearedAgain.ConnectionChanged || clearedAgain.View.Revision != 3 || clearedAgain.View.ConnectionRevision != 2 {
		t.Fatalf("second clear=%+v err=%v", clearedAgain, err)
	}
	replacement := outboundProxyCredential{username: "replacement", password: "new-secret"}
	base.ExpectedRevision, base.CredentialMode, base.Credential = 3, outboundProxyCredentialReplace, &replacement
	replaced, err := f.store.Update(ctx, base)
	if err != nil || !replaced.ConnectionChanged || !replaced.View.HasCredentials || replaced.View.ConnectionRevision != 3 {
		t.Fatalf("replace=%+v err=%v", replaced, err)
	}
}

func TestOutboundProxyMigrationStrictRollbackAndRepair(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "migration.db")
	db := openOutboundProxyTestDB(t, path)
	defer db.Close()
	if _, err := db.Exec(outboundProxyTestUpstreamDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE outbound_proxies(id TEXT PRIMARY KEY, name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateOutboundProxyStore(context.Background(), db); err == nil {
		t.Fatal("incompatible schema was accepted")
	}
	var bindingTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='upstream_proxy_bindings'`).Scan(&bindingTables); err != nil || bindingTables != 0 {
		t.Fatalf("failed migration leaked binding table=%d err=%v", bindingTables, err)
	}
	if _, err := db.Exec(`DROP TABLE outbound_proxies`); err != nil {
		t.Fatal(err)
	}
	if err := migrateOutboundProxyStore(context.Background(), db); err != nil {
		t.Fatalf("repaired migration: %v", err)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX unexpected_proxy_unique ON outbound_proxies(host)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateOutboundProxyStore(context.Background(), db); err == nil {
		t.Fatal("unexpected unique index was accepted")
	}
	if _, err := db.Exec(`DROP INDEX unexpected_proxy_unique`); err != nil {
		t.Fatal(err)
	}
	if err := migrateOutboundProxyStore(context.Background(), db); err != nil {
		t.Fatalf("migration did not recover after index repair: %v", err)
	}
	if _, err := db.Exec(`CREATE INDEX unexpected_proxy_partial ON outbound_proxies(host) WHERE enabled=1`); err != nil {
		t.Fatal(err)
	}
	if err := migrateOutboundProxyStore(context.Background(), db); err == nil {
		t.Fatal("unexpected partial index was accepted")
	}
	if _, err := db.Exec(`DROP INDEX unexpected_proxy_partial`); err != nil {
		t.Fatal(err)
	}
	if err := migrateOutboundProxyStore(context.Background(), db); err != nil {
		t.Fatalf("migration did not recover after partial-index repair: %v", err)
	}
}

func TestOutboundProxyMigrationRejectsInvalidStoredRelationship(t *testing.T) {
	f := newOutboundProxyTestStore(t)
	ctx := context.Background()
	proxy, _ := f.store.Create(ctx, outboundProxyCreateInput{OperationID: "10000000-0000-4000-8000-000000000005", Name: "Stored", Scheme: "https", Host: "proxy.example", Port: 443, AddressScope: "public", Enabled: true})
	insertProxyTestUpstream(t, f.db, "ups_relation", "openai-compatible", "https://api.example/v1", 1)
	if _, err := f.store.SetBinding(ctx, upstreamProxyBindingInput{UpstreamID: "ups_relation", ExpectedUpstreamRevision: 1, ProxyID: proxy.ID, ExpectedProxyRevision: 1, Bind: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`UPDATE upstream_proxy_bindings SET proxy_connection_revision=2 WHERE upstream_id='ups_relation'`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err == nil {
		t.Fatal("binding to the wrong connection revision was accepted")
	}
	if _, err := f.db.Exec(`UPDATE upstream_proxy_bindings SET proxy_connection_revision=1 WHERE upstream_id='ups_relation'`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err != nil {
		t.Fatalf("migration did not recover after relationship repair: %v", err)
	}
	stamp := formatAccountPoolTime(time.Now().UTC())
	if _, err := f.db.Exec(`INSERT INTO outbound_proxies(id,name,scheme,host,port,address_scope,enabled,revision,connection_revision,operation_id,input_fingerprint,created_at,updated_at) VALUES(NULL,'Null ID','https','proxy.example',443,'public',1,1,1,'10000000-0000-4000-8000-000000000099',zeroblob(32),?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err == nil {
		t.Fatal("stored NULL primary key was accepted")
	}
	if _, err := f.db.Exec(`DELETE FROM outbound_proxies WHERE id IS NULL`); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Migrate(ctx); err != nil {
		t.Fatalf("migration did not recover after NULL repair: %v", err)
	}
}

func TestNormalizeProxySQLPreservesQuotedLiterals(t *testing.T) {
	canonical := normalizeProxySQL(`CREATE TABLE sample (scope TEXT CHECK(scope IN ('public','private')))`)
	malformed := normalizeProxySQL(" create  TABLE sample(scope TEXT check(scope in ('pub lic','private'))) ")
	wrongCase := normalizeProxySQL("CREATE TABLE sample(scope TEXT CHECK(scope IN ('Public','private'))) ")
	if canonical == malformed || canonical == wrongCase {
		t.Fatal("normalization changed a quoted CHECK literal")
	}
	spaced := normalizeProxySQL(" create  TABLE sample ( scope TEXT check ( scope in ( 'public' , 'private' ) ) ) ")
	if canonical != spaced {
		t.Fatalf("normalization did not ignore unquoted whitespace: %q != %q", canonical, spaced)
	}
}

func TestOutboundProxyStaticValidation(t *testing.T) {
	valid := []struct{ host, scope, canonical string }{
		{"Proxy.Example", "public", "proxy.example"},
		{"203.0.113.10", "public", "203.0.113.10"},
		{"10.20.30.40", "private", "10.20.30.40"},
		{"fd00::10", "private", "fd00::10"},
		{"2001:db8::10", "public", "2001:db8::10"},
	}
	for _, test := range valid {
		got, ok := canonicalOutboundProxy("Proxy", "https", test.host, 443, test.scope)
		if !ok || got.host != test.canonical {
			t.Errorf("valid host %q scope %s => %+v,%v", test.host, test.scope, got, ok)
		}
	}
	invalid := []struct{ host, scope string }{
		{"localhost", "public"}, {"127.0.0.1", "private"}, {"169.254.169.254", "private"}, {"100.64.0.1", "public"},
		{"10.0.0.1", "public"}, {"8.8.8.8", "private"}, {"[2001:db8::1]", "public"}, {"fe80::1%eth0", "private"},
		{"user@proxy.example", "public"}, {"proxy.example/path", "public"}, {"proxy.example?x=1", "public"}, {"proxy.example#x", "public"},
		{"999.999.999.999", "public"}, {"proxy.example\r\nX: y", "public"}, {"例子.example", "public"},
	}
	for _, test := range invalid {
		if _, ok := canonicalOutboundProxy("Proxy", "https", test.host, 443, test.scope); ok {
			t.Errorf("invalid host %q scope %s was accepted", test.host, test.scope)
		}
	}
	if _, ok := canonicalOutboundProxy("Proxy", "http", "proxy.example", 443, "public"); ok {
		t.Fatal("HTTP proxy was accepted")
	}
	if _, ok := canonicalOutboundProxy("Proxy", "https", "proxy.example", 65536, "public"); ok {
		t.Fatal("invalid port was accepted")
	}
}

func TestOutboundProxyLoopbackRequiresExplicitTestFlagAcrossRestart(t *testing.T) {
	f := newOutboundProxyTestStore(t)
	ctx := context.Background()
	input := outboundProxyCreateInput{OperationID: "10000000-0000-4000-8000-000000000008", Name: "TLS test proxy", Scheme: "https", Host: "127.0.0.1", Port: 9443, AddressScope: "private", Enabled: true}
	if _, err := f.store.Create(ctx, input); !errors.Is(err, errOutboundProxyInvalid) {
		t.Fatalf("default loopback error=%v", err)
	}
	f.store.allowLoopbackForTesting = true
	created, err := f.store.Create(ctx, input)
	if err != nil || created.Host != "127.0.0.1" {
		t.Fatalf("test loopback create=%+v err=%v", created, err)
	}
	if err := migrateOutboundProxyStore(ctx, f.db); err == nil {
		t.Fatal("default restart accepted a loopback proxy")
	}
	if err := migrateOutboundProxyStore(ctx, f.db, true); err != nil {
		t.Fatalf("test-enabled restart rejected loopback: %v", err)
	}
	if _, ok := canonicalOutboundProxyWithLoopback("Proxy", "https", "::1", 443, "private", true); !ok {
		t.Fatal("explicit test mode rejected IPv6 loopback")
	}
}

func TestOutboundProxyConcurrentCreateSingleOperation(t *testing.T) {
	f := newOutboundProxyTestStore(t)
	// Permit distinct connections so the operation uniqueness constraint, not
	// the fixture's single connection, is exercised.
	f.db.SetMaxOpenConns(4)
	input := testProxyCreate("10000000-0000-4000-8000-000000000006", "proxy.example")
	const workers = 4
	ids := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			item, err := f.store.Create(context.Background(), input)
			if err != nil {
				errs <- err
				return
			}
			ids <- item.ID
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent create: %v", err)
		}
	}
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	if len(unique) != 1 {
		t.Fatalf("operation created IDs=%v", unique)
	}
	var count int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM outbound_proxies`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("stored count=%d err=%v", count, err)
	}
}

func TestOutboundProxyDatabaseContainsNoPlaintextCredential(t *testing.T) {
	f := newOutboundProxyTestStore(t)
	secret := "unique-proxy-password-do-not-store"
	credential := outboundProxyCredential{username: "unique-proxy-user", password: secret}
	_, err := f.store.Create(context.Background(), outboundProxyCreateInput{OperationID: "10000000-0000-4000-8000-000000000007", Name: "Encrypted", Scheme: "https", Host: "proxy.example", Port: 443, AddressScope: "public", Enabled: true, Credential: &credential})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), secret) || strings.Contains(string(contents), credential.username) {
		t.Fatal("database contains plaintext proxy credential")
	}
}
