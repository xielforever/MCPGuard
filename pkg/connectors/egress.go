package connectors

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"net/netip"
	"strings"
	"time"
)

// EgressPolicy applies the deployment's destination policy after name
// resolution and dials a vetted address directly instead of resolving again.
type EgressPolicy struct {
	networks []netip.Prefix
	resolver *net.Resolver
	dialer   net.Dialer
}

func NewEgressPolicy(cidrs string) (*EgressPolicy, error) {
	entries := strings.Split(cidrs, ",")
	if len(entries) > 32 {
		return nil, ErrInput
	}
	policy := &EgressPolicy{
		resolver: net.DefaultResolver,
		dialer:   net.Dialer{Timeout: 2 * time.Second},
	}
	for _, entry := range entries {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(entry))
		if err != nil || prefix.Bits() == 0 || prefix.Addr().Is4In6() {
			return nil, ErrInput
		}
		policy.networks = append(policy.networks, prefix.Masked())
	}
	return policy, nil
}

func (p *EgressPolicy) allows(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, network := range p.networks {
		if network.Contains(address) {
			return true
		}
	}
	return false
}

func (p *EgressPolicy) Resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if p == nil || p.resolver == nil || len(p.networks) == 0 {
		return nil, ErrDenied
	}
	if address, err := netip.ParseAddr(host); err == nil {
		address = address.Unmap()
		if !p.allows(address) {
			return nil, ErrDenied
		}
		return []netip.Addr{address}, nil
	}
	addresses, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, ErrUnavailable
	}
	allowed := make([]netip.Addr, 0, min(len(addresses), 8))
	for _, candidate := range addresses {
		candidate = candidate.Unmap()
		if p.allows(candidate) {
			allowed = append(allowed, candidate)
			if len(allowed) == 8 {
				break
			}
		}
	}
	if len(allowed) == 0 {
		return nil, ErrDenied
	}
	return allowed, nil
}

func (p *EgressPolicy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, ErrInput
	}
	allowed, err := p.Resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, candidate := range allowed {
		conn, err := p.dialer.DialContext(ctx, network, net.JoinHostPort(candidate.String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, ErrUnavailable
}

func (p *EgressPolicy) TLSConfig(serverName, caPEM string) (*tls.Config, error) {
	if serverName == "" || len(caPEM) > 16384 {
		return nil, ErrInput
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: serverName}
	if caPEM != "" {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(caPEM)) {
			return nil, ErrInput
		}
		config.RootCAs = roots
	}
	return config, nil
}
