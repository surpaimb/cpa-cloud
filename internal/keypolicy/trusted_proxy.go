// Independently authored KEY-02 trusted-proxy source resolution.
package keypolicy

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"hash"
	"net/netip"
	"sort"
	"strings"
	"unicode"
)

const (
	MaxTrustedProxyCIDRs      = 64
	MaxTrustedProxyCIDRBytes  = 64
	MaxTrustedProxyCIDRsBytes = 4096
	MaxXForwardedForBytes     = 4096
	MaxXForwardedForHops      = 64

	trustedProxyRevisionDomain = "cpacloud:keypolicy:trusted-proxy-set:v1"
)

var (
	ErrInvalidTrustedProxyConfig = errors.New("invalid trusted proxy configuration")
	ErrInvalidForwardedSource    = errors.New("invalid forwarded source")
)

// TrustedProxySet is an immutable, canonical set of explicitly trusted proxy
// address ranges. Its zero value is invalid; even an empty set must be created
// with NewTrustedProxySet so callers receive a stable trust revision.
type TrustedProxySet struct {
	prefixes []netip.Prefix
	cidrs    []string
	revision string
}

// NewTrustedProxySet validates and canonicalizes administrator-configured
// numeric proxy addresses and prefixes. It never infers trust from address
// properties such as loopback or private-network membership.
func NewTrustedProxySet(values []string) (TrustedProxySet, error) {
	if len(values) > MaxTrustedProxyCIDRs {
		return TrustedProxySet{}, ErrInvalidTrustedProxyConfig
	}

	totalBytes := 0
	prefixes := make([]netip.Prefix, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		rawBytes := len([]byte(raw))
		totalBytes += rawBytes
		if raw == "" || rawBytes > MaxTrustedProxyCIDRBytes || totalBytes > MaxTrustedProxyCIDRsBytes || strings.Contains(raw, "%") || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
			return TrustedProxySet{}, ErrInvalidTrustedProxyConfig
		}
		prefix, err := parseSourcePrefix(raw)
		if err != nil {
			return TrustedProxySet{}, ErrInvalidTrustedProxyConfig
		}
		canonical := prefix.String()
		if _, duplicate := seen[canonical]; duplicate {
			return TrustedProxySet{}, ErrInvalidTrustedProxyConfig
		}
		seen[canonical] = struct{}{}
		prefixes = append(prefixes, prefix)
	}

	sort.Slice(prefixes, func(i, j int) bool {
		left, right := prefixes[i], prefixes[j]
		if left.Addr().BitLen() != right.Addr().BitLen() {
			return left.Addr().BitLen() < right.Addr().BitLen()
		}
		if comparison := left.Addr().Compare(right.Addr()); comparison != 0 {
			return comparison < 0
		}
		return left.Bits() < right.Bits()
	})

	cidrs := make([]string, len(prefixes))
	for index, prefix := range prefixes {
		cidrs[index] = prefix.String()
	}
	return TrustedProxySet{prefixes: prefixes, cidrs: cidrs, revision: trustedProxyRevision(cidrs)}, nil
}

func (s TrustedProxySet) Enabled() bool {
	return s.revision != "" && len(s.prefixes) != 0
}

func (s TrustedProxySet) CIDRs() []string {
	return append([]string{}, s.cidrs...)
}

func (s TrustedProxySet) Revision() string {
	return s.revision
}

type ResolvedSource struct {
	SourceAddr      netip.Addr
	PeerAddr        netip.Addr
	TrustRevision   string
	ViaTrustedProxy bool
}

// Resolve derives the conservative source address for one HTTP request.
// Forwarded and X-Real-IP are intentionally outside this API. The caller must
// pass every physical X-Forwarded-For value using http.Header.Values.
func (s TrustedProxySet) Resolve(remoteAddr string, xForwardedFor []string) (ResolvedSource, error) {
	if s.revision == "" {
		return ResolvedSource{}, ErrInvalidTrustedProxyConfig
	}
	peer, err := ParseSocketPeer(remoteAddr)
	if err != nil {
		return ResolvedSource{}, err
	}
	if !s.contains(peer) {
		return ResolvedSource{SourceAddr: peer, PeerAddr: peer, TrustRevision: s.revision}, nil
	}

	hops, err := parseXForwardedFor(xForwardedFor)
	if err != nil {
		return ResolvedSource{}, err
	}
	for index := len(hops) - 1; index >= 0; index-- {
		if !s.contains(hops[index]) {
			return ResolvedSource{
				SourceAddr: hops[index], PeerAddr: peer, TrustRevision: s.revision, ViaTrustedProxy: true,
			}, nil
		}
	}
	return ResolvedSource{}, ErrInvalidForwardedSource
}

func (s TrustedProxySet) contains(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}
	address = address.Unmap()
	for _, prefix := range s.prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}

func parseXForwardedFor(values []string) ([]netip.Addr, error) {
	if len(values) != 1 || len([]byte(values[0])) > MaxXForwardedForBytes {
		return nil, ErrInvalidForwardedSource
	}
	rawHops := strings.Split(values[0], ",")
	if len(rawHops) == 0 || len(rawHops) > MaxXForwardedForHops {
		return nil, ErrInvalidForwardedSource
	}
	hops := make([]netip.Addr, len(rawHops))
	for index, raw := range rawHops {
		token := strings.Trim(raw, " \t")
		if token == "" || len([]byte(token)) > MaxTrustedProxyCIDRBytes || strings.Contains(token, "%") || strings.IndexFunc(token, unicode.IsSpace) >= 0 {
			return nil, ErrInvalidForwardedSource
		}
		address, err := netip.ParseAddr(token)
		if err != nil || !address.IsValid() || address.Zone() != "" {
			return nil, ErrInvalidForwardedSource
		}
		hops[index] = address.Unmap()
	}
	return hops, nil
}

func trustedProxyRevision(cidrs []string) string {
	digest := sha256.New()
	writeTrustedProxyRevisionFrame(digest, []byte(trustedProxyRevisionDomain))
	for _, cidr := range cidrs {
		writeTrustedProxyRevisionFrame(digest, []byte(cidr))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func writeTrustedProxyRevisionFrame(digest hash.Hash, value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = digest.Write(size[:])
	_, _ = digest.Write(value)
}
