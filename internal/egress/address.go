// Package egress implements the bounded HTTPS CONNECT transport described by
// docs/outbound-proxy-contract.md. It is independently based on Go's net/http,
// net, and crypto/tls packages and RFC 9110 CONNECT semantics.
package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"time"
)

const (
	maxResolvedAddresses = 16
	resolveTimeout       = 5 * time.Second
)

// AddressScope fixes the permitted network class for the configured proxy.
// A hostname may not move between public and private address space.
type AddressScope string

const (
	ScopePublic  AddressScope = "public"
	ScopePrivate AddressScope = "private"
)

var (
	ErrInvalidConfig       = errors.New("egress proxy configuration is invalid")
	ErrResolutionFailed    = errors.New("egress address resolution failed")
	ErrAddressNotPermitted = errors.New("egress address is not permitted")
	ErrTooManyAddresses    = errors.New("egress address count exceeds the limit")
	ErrProxyUnavailable    = errors.New("egress proxy is unavailable")
	ErrProxyRejected       = errors.New("egress proxy rejected CONNECT")
	ErrConnectResponse     = errors.New("egress proxy returned an invalid CONNECT response")
	ErrConnectHeaderLarge  = errors.New("egress proxy CONNECT response headers exceed the limit")
	ErrHTTPSRequired       = errors.New("egress target must use HTTPS")
	ErrForbiddenHeader     = errors.New("egress request contains a proxy-only header")
	ErrRequestFailed       = errors.New("egress request failed")
	ErrConfigConflict      = errors.New("egress cache key has conflicting configuration")
	ErrCacheClosed         = errors.New("egress client cache is closed")
)

type addressResolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}

type systemResolver struct{}

func (systemResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

var carrierGradeNAT = netip.MustParsePrefix("100.64.0.0/10")

// ValidateProxyAddress resolves and validates the proxy without opening a
// connection. Execution repeats the same check for every new connection.
func ValidateProxyAddress(ctx context.Context, config ProxyConfig) error {
	if err := validateProxyConfig(config); err != nil {
		return err
	}
	_, err := resolveProxy(ctx, systemResolver{}, config)
	return err
}

// ValidateTargetAddress validates a target hostname using the same rules that
// are applied immediately before CONNECT. Private targets are never allowed.
func ValidateTargetAddress(ctx context.Context, host string, allowLoopbackForTesting bool) error {
	if err := validateHost(host); err != nil {
		return err
	}
	_, err := resolveTarget(ctx, systemResolver{}, host, allowLoopbackForTesting)
	return err
}

func resolveProxy(ctx context.Context, resolver addressResolver, config ProxyConfig) ([]netip.Addr, error) {
	addresses, err := resolve(ctx, resolver, config.Host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if !proxyAddressAllowed(address, config.Scope, config.AllowLoopbackForTesting) {
			return nil, ErrAddressNotPermitted
		}
	}
	return addresses, nil
}

func resolveTarget(ctx context.Context, resolver addressResolver, host string, allowLoopbackForTesting bool) ([]netip.Addr, error) {
	addresses, err := resolve(ctx, resolver, host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if !targetAddressAllowed(address, allowLoopbackForTesting) {
			return nil, ErrAddressNotPermitted
		}
	}
	return addresses, nil
}

func resolve(ctx context.Context, resolver addressResolver, host string) ([]netip.Addr, error) {
	if parsed, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{parsed.Unmap()}, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, resolveTimeout)
	defer cancel()
	addresses, err := resolver.LookupNetIP(lookupCtx, "ip", host)
	if err != nil || len(addresses) == 0 {
		if lookupCtx.Err() != nil {
			return nil, lookupCtx.Err()
		}
		return nil, ErrResolutionFailed
	}
	if len(addresses) > maxResolvedAddresses {
		return nil, ErrTooManyAddresses
	}
	seen := make(map[netip.Addr]struct{}, len(addresses))
	result := make([]netip.Addr, 0, len(addresses))
	for _, address := range addresses {
		address = address.Unmap()
		if !address.IsValid() {
			return nil, ErrAddressNotPermitted
		}
		if _, duplicate := seen[address]; duplicate {
			continue
		}
		seen[address] = struct{}{}
		result = append(result, address)
	}
	if len(result) == 0 {
		return nil, ErrResolutionFailed
	}
	return result, nil
}

func targetAddressAllowed(address netip.Addr, allowLoopbackForTesting bool) bool {
	address = address.Unmap()
	if address.IsLoopback() {
		return allowLoopbackForTesting
	}
	return publicAddress(address)
}

func proxyAddressAllowed(address netip.Addr, scope AddressScope, allowLoopbackForTesting bool) bool {
	address = address.Unmap()
	if address.IsLoopback() {
		return allowLoopbackForTesting && scope == ScopePrivate
	}
	switch scope {
	case ScopePublic:
		return publicAddress(address)
	case ScopePrivate:
		return address.IsPrivate() && baseAddressAllowed(address)
	default:
		return false
	}
}

func publicAddress(address netip.Addr) bool {
	return baseAddressAllowed(address) && address.IsGlobalUnicast() && !address.IsPrivate() && !carrierGradeNAT.Contains(address)
}

func baseAddressAllowed(address netip.Addr) bool {
	return address.IsValid() && !address.IsUnspecified() && !address.IsLoopback() &&
		!address.IsMulticast() && !address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast()
}

func validateHost(host string) error {
	if host == "" || host != strings.TrimSpace(host) || len(host) > 253 || strings.ContainsAny(host, "\x00\r\n/@?#") {
		return ErrInvalidConfig
	}
	if parsed, err := netip.ParseAddr(host); err == nil {
		if parsed.Zone() != "" {
			return ErrInvalidConfig
		}
		return nil
	}
	if strings.HasSuffix(host, ".") {
		return ErrInvalidConfig
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return ErrInvalidConfig
		}
		for _, character := range label {
			if character > 127 || !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-') {
				return ErrInvalidConfig
			}
		}
	}
	return nil
}
