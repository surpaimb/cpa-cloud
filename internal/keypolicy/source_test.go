// Independently authored KEY-02 socket-peer source policy tests.
package keypolicy

import (
	"context"
	"database/sql"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestNormalizeSourceCIDRsCanonicalizesAndSorts(t *testing.T) {
	normalized, err := Normalize(Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeSelected, SourceCIDRs: []string{
			"2001:0db8:1::9/48", "192.0.2.99/24", "2001:db8::1", "192.0.2.7", "::ffff:198.51.100.7", "::ffff:203.0.113.99/120",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.0/24", "192.0.2.7/32", "198.51.100.7/32", "203.0.113.0/24", "2001:db8::1/128", "2001:db8:1::/48"}
	if !slices.Equal(normalized.SourceCIDRs, want) {
		t.Fatalf("source CIDRs=%v want=%v", normalized.SourceCIDRs, want)
	}
}

func TestNormalizeSourceCIDRsRejectsInvalidDuplicateAndOversizedValues(t *testing.T) {
	validBase := Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{}, SourceMode: ModeSelected,
	}
	cases := []struct {
		name   string
		values []string
	}{
		{"nil", nil},
		{"empty member", []string{""}},
		{"outer whitespace", []string{" 192.0.2.1"}},
		{"internal whitespace", []string{"192.0. 2.1"}},
		{"invalid address", []string{"not-an-ip"}},
		{"invalid prefix", []string{"192.0.2.1/33"}},
		{"zone address", []string{"fe80::1%eth0"}},
		{"zone prefix", []string{"fe80::1%eth0/64"}},
		{"wide mapped prefix", []string{"::ffff:192.0.2.1/95"}},
		{"canonical duplicate", []string{"192.0.2.1/24", "192.0.2.99/24"}},
		{"mapped duplicate", []string{"192.0.2.1", "::ffff:192.0.2.1"}},
		{"member too long", []string{strings.Repeat("1", MaxSourceCIDRBytes+1)}},
		{"too many members", makeSourceValues(MaxSourceCIDRs + 1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := validBase
			input.SourceCIDRs = test.values
			if _, err := Normalize(input); !errors.Is(err, ErrInvalidPolicy) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if _, err := Normalize(Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeAll, SourceCIDRs: []string{"192.0.2.0/24"},
	}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("all with members error=%v", err)
	}
}

func TestParseSocketPeerAndAllowsSource(t *testing.T) {
	tests := []struct {
		remote string
		want   string
	}{
		{"192.0.2.15:44321", "192.0.2.15"},
		{"[2001:db8::15]:44321", "2001:db8::15"},
		{"[::ffff:192.0.2.15]:44321", "192.0.2.15"},
	}
	for _, test := range tests {
		peer, err := ParseSocketPeer(test.remote)
		if err != nil || peer.String() != test.want {
			t.Errorf("ParseSocketPeer(%q)=%v,%v want=%s", test.remote, peer, err, test.want)
		}
	}
	for _, invalid := range []string{"", "192.0.2.1", "example.test:443", "192.0.2.1:http", "192.0.2.1:65536", "[fe80::1%eth0]:443"} {
		if _, err := ParseSocketPeer(invalid); !errors.Is(err, ErrInvalidPeer) {
			t.Errorf("ParseSocketPeer(%q) error=%v", invalid, err)
		}
	}

	policy := Policy{
		Revision: 1, ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeSelected, SourceCIDRs: []string{"192.0.2.0/24", "2001:db8::/48"},
	}
	for _, allowed := range []string{"192.0.2.0", "192.0.2.255", "::ffff:192.0.2.8", "2001:db8::", "2001:db8:0:ffff:ffff:ffff:ffff:ffff"} {
		if !AllowsSource(policy, netip.MustParseAddr(allowed)) {
			t.Errorf("expected %s allowed", allowed)
		}
	}
	for _, denied := range []string{"192.0.1.255", "192.0.3.0", "2001:db9::1"} {
		if AllowsSource(policy, netip.MustParseAddr(denied)) {
			t.Errorf("expected %s denied", denied)
		}
	}
	policy.SourceCIDRs = []string{}
	if AllowsSource(policy, netip.MustParseAddr("192.0.2.1")) {
		t.Fatal("selected empty source policy allowed a peer")
	}
	policy.SourceMode = ModeAll
	if !AllowsSource(policy, netip.MustParseAddr("203.0.113.1")) {
		t.Fatal("all source policy denied a valid peer")
	}
	if AllowsSource(policy, netip.Addr{}) || AllowsSource(policy, netip.MustParseAddr("fe80::1").WithZone("eth0")) {
		t.Fatal("invalid or zoned peer was allowed")
	}
}

func TestAllowsSourcePrefixBoundaries(t *testing.T) {
	tests := []struct {
		cidr    string
		allowed string
		denied  string
	}{
		{"0.0.0.0/0", "203.0.113.9", "2001:db8::1"},
		{"255.255.255.255/32", "255.255.255.255", "255.255.255.254"},
		{"::/0", "2001:db8::1", "203.0.113.9"},
		{"ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff/128", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:ffff", "ffff:ffff:ffff:ffff:ffff:ffff:ffff:fffe"},
	}
	for _, test := range tests {
		policy := Policy{
			Revision: 1, ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
			SourceMode: ModeSelected, SourceCIDRs: []string{test.cidr},
		}
		if !AllowsSource(policy, netip.MustParseAddr(test.allowed)) {
			t.Errorf("%s did not allow %s", test.cidr, test.allowed)
		}
		if AllowsSource(policy, netip.MustParseAddr(test.denied)) {
			t.Errorf("%s allowed %s", test.cidr, test.denied)
		}
	}

	normalized, err := Normalize(Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeSelected, SourceCIDRs: []string{"::ffff:192.0.2.1/96"},
	})
	if err != nil || !slices.Equal(normalized.SourceCIDRs, []string{"0.0.0.0/0"}) {
		t.Fatalf("mapped /96 normalization=%v error=%v", normalized.SourceCIDRs, err)
	}
}

func TestSourcePolicyPersistenceSharesPolicyCAS(t *testing.T) {
	db := openTestDB(t)
	migratePolicyAndInstallSourceSchema(t, db)
	insertKey(t, db, "key-source", "employee-one")
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	created, err := CreateTx(context.Background(), tx, "key-source", Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeSelected, SourceCIDRs: []string{"2001:db8::1", "192.0.2.9/24"},
	}, testTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if created.Revision != 1 || !slices.Equal(created.SourceCIDRs, []string{"192.0.2.0/24", "2001:db8::1/128"}) {
		t.Fatalf("created=%#v", created)
	}
	updated, err := Replace(context.Background(), db, "key-source", 1, Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeSelected, SourceCIDRs: []string{},
	}, testTime.Add(1))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 || updated.SourceMode != ModeSelected || len(updated.SourceCIDRs) != 0 {
		t.Fatalf("updated=%#v", updated)
	}
	restored, err := Replace(context.Background(), db, "key-source", 2, Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeSelected, SourceCIDRs: []string{"2001:db8::1", "192.0.2.9/24"},
	}, testTime.Add(2))
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 3 || !slices.Equal(restored.SourceCIDRs, created.SourceCIDRs) {
		t.Fatalf("restored=%#v", restored)
	}
	if _, err := Replace(context.Background(), db, "key-source", 1, Replacement{
		ProtocolMode: ModeAll, Protocols: []ClientProtocol{}, ModelMode: ModeAll, Models: []string{},
		SourceMode: ModeAll, SourceCIDRs: []string{},
	}, testTime.Add(3)); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("stale source replacement error=%v", err)
	}
	if _, err := db.Exec(`DELETE FROM access_key_policy_sources WHERE key_id='key-source'`); err != nil {
		t.Fatal(err)
	}
	tx, err = db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := LoadTx(context.Background(), tx, "key-source"); !errors.Is(err, ErrPolicyMissing) {
		t.Fatalf("missing source row error=%v", err)
	}
}

func makeSourceValues(count int) []string {
	values := make([]string, count)
	for index := range values {
		values[index] = netip.AddrFrom4([4]byte{10, byte(index / 256), byte(index), 1}).String()
	}
	return values
}

func migratePolicyAndInstallSourceSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	installSourceSchemaForTest(t, db)
}

func installSourceSchemaForTest(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE IF NOT EXISTS access_key_policy_sources(key_id TEXT PRIMARY KEY NOT NULL REFERENCES access_key_policies(key_id) ON DELETE CASCADE,source_mode TEXT NOT NULL CHECK(source_mode IN ('all','selected')))`,
		`CREATE TABLE IF NOT EXISTS access_key_policy_source_cidrs(key_id TEXT NOT NULL REFERENCES access_key_policy_sources(key_id) ON DELETE CASCADE,cidr TEXT NOT NULL,PRIMARY KEY(key_id,cidr))`,
		`INSERT INTO access_key_policy_sources(key_id,source_mode) SELECT key_id,'all' FROM access_key_policies WHERE NOT EXISTS(SELECT 1 FROM access_key_policy_sources s WHERE s.key_id=access_key_policies.key_id)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
}
