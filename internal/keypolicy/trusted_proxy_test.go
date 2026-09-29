// Independently authored KEY-02 trusted-proxy source tests.
package keypolicy

import (
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
)

func TestTrustedProxySetCanonicalizationRevisionAndCopy(t *testing.T) {
	empty, err := NewTrustedProxySet(nil)
	if err != nil {
		t.Fatal(err)
	}
	if empty.Enabled() || len(empty.CIDRs()) != 0 || len(empty.Revision()) != 64 || empty.Revision() != strings.ToLower(empty.Revision()) {
		t.Fatalf("empty set=%#v cidrs=%v revision=%q", empty, empty.CIDRs(), empty.Revision())
	}
	if empty.Revision() != "c320f317ca2721801b1fe8ab3e81e63ec8208202f32bd55b7111bb0765bb6a81" {
		t.Fatalf("empty revision=%q", empty.Revision())
	}

	set, err := NewTrustedProxySet([]string{
		"2001:0db8:1::9/48", "192.0.2.99/24", "::ffff:198.51.100.7", "::ffff:203.0.113.99/120",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.0.2.0/24", "198.51.100.7/32", "203.0.113.0/24", "2001:db8:1::/48"}
	if !set.Enabled() || !slices.Equal(set.CIDRs(), want) {
		t.Fatalf("set enabled=%v cidrs=%v want=%v", set.Enabled(), set.CIDRs(), want)
	}
	if set.Revision() != "9a47ee3dceab9e502310c2c8067c8c58bb48f1095c88dbae1950b3d208133980" {
		t.Fatalf("set revision=%q", set.Revision())
	}
	cidrs := set.CIDRs()
	cidrs[0] = "changed"
	if !slices.Equal(set.CIDRs(), want) {
		t.Fatalf("CIDRs returned mutable storage: %v", set.CIDRs())
	}

	reordered, err := NewTrustedProxySet([]string{
		"::ffff:203.0.113.99/120", "::ffff:198.51.100.7", "192.0.2.99/24", "2001:0db8:1::9/48",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reordered.Revision() != set.Revision() {
		t.Fatalf("canonical reorder changed revision: %q != %q", reordered.Revision(), set.Revision())
	}
	changed, err := NewTrustedProxySet([]string{"192.0.2.0/25"})
	if err != nil {
		t.Fatal(err)
	}
	if changed.Revision() == set.Revision() || empty.Revision() == set.Revision() {
		t.Fatal("different canonical trust sets shared a revision")
	}

	var zero TrustedProxySet
	if zero.Enabled() || zero.Revision() != "" || len(zero.CIDRs()) != 0 {
		t.Fatalf("unexpected zero value exposure: enabled=%v revision=%q cidrs=%v", zero.Enabled(), zero.Revision(), zero.CIDRs())
	}
	if _, err := zero.Resolve("192.0.2.1:443", nil); !errors.Is(err, ErrInvalidTrustedProxyConfig) {
		t.Fatalf("zero-value resolve error=%v", err)
	}
}

func TestTrustedProxySetRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		values []string
	}{
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
		{"member too long", []string{strings.Repeat("1", MaxTrustedProxyCIDRBytes+1)}},
		{"too many members", makeSourceValues(MaxTrustedProxyCIDRs + 1)},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewTrustedProxySet(test.values); !errors.Is(err, ErrInvalidTrustedProxyConfig) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestTrustedProxyResolveUntrustedPeerIgnoresAllForwardingHeaders(t *testing.T) {
	set := mustTrustedProxySet(t, []string{"10.0.0.0/8", "2001:db8:1::/48"})
	maliciousHeaders := [][]string{
		nil,
		{},
		{""},
		{"not-an-ip"},
		{"203.0.113.1", "198.51.100.2"},
		{strings.Repeat("x", MaxXForwardedForBytes+1)},
	}
	for _, headers := range maliciousHeaders {
		for _, remoteAddr := range []string{"192.0.2.15:44321", "192.168.2.15:44321"} {
			resolved, err := set.Resolve(remoteAddr, headers)
			if err != nil {
				t.Fatalf("remote=%q headers=%q error=%v", remoteAddr, headers, err)
			}
			peer, err := ParseSocketPeer(remoteAddr)
			if err != nil {
				t.Fatal(err)
			}
			assertResolvedSource(t, resolved, peer.String(), peer.String(), set.Revision(), false)
		}
	}

	empty := mustTrustedProxySet(t, nil)
	resolved, err := empty.Resolve("127.0.0.1:8080", []string{"203.0.113.9"})
	if err != nil {
		t.Fatal(err)
	}
	assertResolvedSource(t, resolved, "127.0.0.1", "127.0.0.1", empty.Revision(), false)
}

func TestTrustedProxyResolveWalksRightToLeftToFirstUntrustedHop(t *testing.T) {
	set := mustTrustedProxySet(t, []string{"10.0.0.0/8", "192.168.0.0/16", "2001:db8:1::/48", "127.0.0.1"})
	tests := []struct {
		name       string
		remoteAddr string
		header     string
		wantSource string
		wantPeer   string
	}{
		{"one proxy", "10.0.0.2:443", "203.0.113.7", "203.0.113.7", "10.0.0.2"},
		{"multi proxy ignores farther-left spoof", "10.0.0.3:443", "198.51.100.66, 203.0.113.7, 192.168.1.4", "203.0.113.7", "10.0.0.3"},
		{"ascii optional whitespace", "10.0.0.4:443", "\t203.0.113.8 \t, 192.168.1.5\t", "203.0.113.8", "10.0.0.4"},
		{"ipv6", "[2001:db8:1::10]:443", "2001:db8:ffff::9, 2001:db8:1::20", "2001:db8:ffff::9", "2001:db8:1::10"},
		{"mapped peer and source", "[::ffff:10.0.0.5]:443", "::ffff:203.0.113.9", "203.0.113.9", "10.0.0.5"},
		{"explicit private trust", "192.168.2.5:443", "203.0.113.10", "203.0.113.10", "192.168.2.5"},
		{"explicit loopback trust", "127.0.0.1:443", "198.51.100.7", "198.51.100.7", "127.0.0.1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := set.Resolve(test.remoteAddr, []string{test.header})
			if err != nil {
				t.Fatal(err)
			}
			assertResolvedSource(t, resolved, test.wantSource, test.wantPeer, set.Revision(), true)
		})
	}
}

func TestTrustedProxyResolveRejectsInvalidOrAmbiguousTrustedChains(t *testing.T) {
	set := mustTrustedProxySet(t, []string{"10.0.0.0/8", "192.168.0.0/16"})
	cases := []struct {
		name   string
		values []string
	}{
		{"missing", nil},
		{"no physical values", []string{}},
		{"empty", []string{""}},
		{"multiple physical values", []string{"203.0.113.1", "198.51.100.2"}},
		{"empty first member", []string{", 203.0.113.1"}},
		{"empty middle member", []string{"203.0.113.1,, 192.168.1.1"}},
		{"empty last member", []string{"203.0.113.1,"}},
		{"hostname", []string{"client.example"}},
		{"unknown", []string{"unknown"}},
		{"quoted", []string{"\"203.0.113.1\""}},
		{"bracketed", []string{"[2001:db8::1]"}},
		{"ipv4 port", []string{"203.0.113.1:443"}},
		{"ipv6 port", []string{"[2001:db8::1]:443"}},
		{"zone", []string{"fe80::1%eth0"}},
		{"internal space", []string{"203.0. 113.1"}},
		{"other whitespace", []string{"\u00a0203.0.113.1"}},
		{"overlong token", []string{strings.Repeat("1", MaxTrustedProxyCIDRBytes+1)}},
		{"oversized header", []string{strings.Repeat("1", MaxXForwardedForBytes+1)}},
		{"too many hops", []string{strings.Repeat("203.0.113.1,", MaxXForwardedForHops) + "203.0.113.1"}},
		{"all trusted", []string{"10.1.1.1, 192.168.1.1"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := set.Resolve("10.0.0.2:443", test.values); !errors.Is(err, ErrInvalidForwardedSource) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestTrustedProxyResolveAcceptsMaximumHopBoundary(t *testing.T) {
	set := mustTrustedProxySet(t, []string{"10.0.0.0/8"})
	hops := make([]string, MaxXForwardedForHops)
	hops[0] = "203.0.113.99"
	for index := 1; index < len(hops); index++ {
		hops[index] = "10.0.0.1"
	}
	resolved, err := set.Resolve("10.0.0.2:443", []string{strings.Join(hops, ",")})
	if err != nil {
		t.Fatal(err)
	}
	assertResolvedSource(t, resolved, "203.0.113.99", "10.0.0.2", set.Revision(), true)

	address := "203.0.113.99"
	maxBytesHeader := strings.Repeat(" ", MaxXForwardedForBytes-len(address)) + address
	resolved, err = set.Resolve("10.0.0.2:443", []string{maxBytesHeader})
	if err != nil {
		t.Fatal(err)
	}
	assertResolvedSource(t, resolved, address, "10.0.0.2", set.Revision(), true)
}

func TestTrustedProxyResolvePrefixBoundariesAndInvalidPeer(t *testing.T) {
	set := mustTrustedProxySet(t, []string{"0.0.0.0/0", "::/0"})
	for _, remoteAddr := range []string{"192.0.2.1:443", "[2001:db8::1]:443"} {
		if _, err := set.Resolve(remoteAddr, nil); !errors.Is(err, ErrInvalidForwardedSource) {
			t.Fatalf("trusted /0 peer %q error=%v", remoteAddr, err)
		}
	}
	for _, remoteAddr := range []string{"", "192.0.2.1", "example.test:443", "[fe80::1%eth0]:443"} {
		if _, err := set.Resolve(remoteAddr, []string{"203.0.113.1"}); !errors.Is(err, ErrInvalidPeer) {
			t.Fatalf("remote %q error=%v", remoteAddr, err)
		}
	}
}

func mustTrustedProxySet(t *testing.T, values []string) TrustedProxySet {
	t.Helper()
	set, err := NewTrustedProxySet(values)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func assertResolvedSource(t *testing.T, resolved ResolvedSource, source, peer, revision string, via bool) {
	t.Helper()
	if resolved.SourceAddr != netip.MustParseAddr(source) || resolved.PeerAddr != netip.MustParseAddr(peer) || resolved.TrustRevision != revision || resolved.ViaTrustedProxy != via {
		t.Fatalf("resolved=%#v want source=%s peer=%s revision=%q via=%v", resolved, source, peer, revision, via)
	}
}
