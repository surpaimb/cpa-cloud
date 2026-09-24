// Independently authored KEY-02 socket-peer source policy normalization and matching.
package keypolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"unicode"
)

const (
	MaxSourceCIDRs      = 64
	MaxSourceCIDRBytes  = 64
	MaxSourceCIDRsBytes = 4096

	sourcesTable     = "access_key_policy_sources"
	sourceCIDRsTable = "access_key_policy_source_cidrs"
)

var ErrInvalidPeer = errors.New("invalid socket peer address")

// ParseSocketPeer accepts only the host:port shape supplied by Go's HTTP
// server RemoteAddr. It does not inspect or accept forwarding headers.
func ParseSocketPeer(remoteAddr string) (netip.Addr, error) {
	peer, err := netip.ParseAddrPort(remoteAddr)
	if err != nil || !peer.Addr().IsValid() || peer.Addr().Zone() != "" {
		return netip.Addr{}, ErrInvalidPeer
	}
	return peer.Addr().Unmap(), nil
}

// AllowsSource evaluates only the Key source layer. Employee state and all
// other policy dimensions are intentionally owned by the service integration.
func AllowsSource(policy Policy, peer netip.Addr) bool {
	if !peer.IsValid() || peer.Zone() != "" || validateStored(policy) != nil {
		return false
	}
	peer = peer.Unmap()
	if policy.SourceMode == ModeAll {
		return true
	}
	for _, raw := range policy.SourceCIDRs {
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return false
		}
		if prefix.Contains(peer) {
			return true
		}
	}
	return false
}

func normalizeSources(mode Mode, values []string) ([]string, error) {
	if values == nil || !validMode(mode) || mode == ModeAll && len(values) != 0 || len(values) > MaxSourceCIDRs {
		return nil, ErrInvalidPolicy
	}
	totalBytes := 0
	prefixes := make([]netip.Prefix, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		totalBytes += len([]byte(raw))
		if raw == "" || len([]byte(raw)) > MaxSourceCIDRBytes || totalBytes > MaxSourceCIDRsBytes || strings.Contains(raw, "%") || strings.IndexFunc(raw, unicode.IsSpace) >= 0 {
			return nil, ErrInvalidPolicy
		}
		prefix, err := parseSourcePrefix(raw)
		if err != nil {
			return nil, ErrInvalidPolicy
		}
		canonical := prefix.String()
		if _, duplicate := seen[canonical]; duplicate {
			return nil, ErrInvalidPolicy
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
	result := make([]string, len(prefixes))
	for index, prefix := range prefixes {
		result[index] = prefix.String()
	}
	return result, nil
}

func parseSourcePrefix(raw string) (netip.Prefix, error) {
	if !strings.Contains(raw, "/") {
		address, err := netip.ParseAddr(raw)
		if err != nil || address.Zone() != "" {
			return netip.Prefix{}, ErrInvalidPolicy
		}
		address = address.Unmap()
		return netip.PrefixFrom(address, address.BitLen()).Masked(), nil
	}
	prefix, err := netip.ParsePrefix(raw)
	if err != nil || prefix.Addr().Zone() != "" {
		return netip.Prefix{}, ErrInvalidPolicy
	}
	if prefix.Addr().Is4In6() {
		if prefix.Bits() < 96 {
			return netip.Prefix{}, ErrInvalidPolicy
		}
		return netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Bits()-96).Masked(), nil
	}
	return prefix.Masked(), nil
}

func createSourceTx(ctx context.Context, tx *sql.Tx, keyID string, replacement Replacement) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_sources(key_id,source_mode) VALUES(?,?)`, keyID, replacement.SourceMode); err != nil {
		return fmt.Errorf("create key source policy: %w", err)
	}
	return replaceSourceMembersTx(ctx, tx, keyID, replacement.SourceCIDRs)
}

func replaceSourceTx(ctx context.Context, tx *sql.Tx, keyID string, replacement Replacement) error {
	result, err := tx.ExecContext(ctx, `UPDATE access_key_policy_sources SET source_mode=? WHERE key_id=?`, replacement.SourceMode, keyID)
	if err != nil {
		return fmt.Errorf("replace key source policy: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read key source policy replacement result: %w", err)
	}
	if changed != 1 {
		return ErrPolicyMissing
	}
	return replaceSourceMembersTx(ctx, tx, keyID, replacement.SourceCIDRs)
}

func replaceSourceMembersTx(ctx context.Context, tx *sql.Tx, keyID string, values []string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM access_key_policy_source_cidrs WHERE key_id=?`, keyID); err != nil {
		return fmt.Errorf("clear key source policy CIDRs: %w", err)
	}
	for _, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO access_key_policy_source_cidrs(key_id,cidr) VALUES(?,?)`, keyID, value); err != nil {
			return fmt.Errorf("store key source policy CIDR: %w", err)
		}
	}
	return nil
}

func loadSourceTx(ctx context.Context, tx *sql.Tx, keyID string, policy *Policy) error {
	if err := tx.QueryRowContext(ctx, `SELECT source_mode FROM access_key_policy_sources WHERE key_id=?`, keyID).Scan(&policy.SourceMode); errors.Is(err, sql.ErrNoRows) {
		return ErrPolicyMissing
	} else if err != nil {
		return fmt.Errorf("load key source policy: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT cidr FROM access_key_policy_source_cidrs WHERE key_id=? ORDER BY cidr`, keyID)
	if err != nil {
		return fmt.Errorf("load key source policy CIDRs: %w", err)
	}
	defer rows.Close()
	policy.SourceCIDRs = make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return fmt.Errorf("scan key source policy CIDR: %w", err)
		}
		policy.SourceCIDRs = append(policy.SourceCIDRs, value)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate key source policy CIDRs: %w", err)
	}
	normalized, err := normalizeSources(policy.SourceMode, policy.SourceCIDRs)
	if err != nil {
		return err
	}
	policy.SourceCIDRs = normalized
	return nil
}
