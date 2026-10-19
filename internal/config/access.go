package config

import (
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// Access is the parsed gateway.access list (CTR-GW-07). Loopback is always served, so the
// "localhost" entry is accepted for readability and changes nothing.
type Access struct {
	lan, tailnet bool
	prefixes     []netip.Prefix
}

var (
	loopbackPrefixes = []netip.Prefix{netip.MustParsePrefix("127.0.0.0/8"), netip.MustParsePrefix("::1/128")}
	// lanPrefixes are the private IPv4 ranges (RFC 1918) and IPv6 unique local addresses.
	lanPrefixes = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("fc00::/7")}
	// tailnetPrefixes are the Tailscale address ranges: CGNAT IPv4 and the tailnet IPv6 prefix.
	tailnetPrefixes = []netip.Prefix{netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("fd7a:115c:a1e0::/48")}
)

// ParseAccess reads the gateway.access entries: localhost, lan, tailnet, or one IP or CIDR.
func ParseAccess(entries []string) (Access, error) {
	var a Access
	for _, entry := range entries {
		switch entry {
		case "localhost":
		case "lan":
			a.lan = true
		case "tailnet":
			a.tailnet = true
		default:
			p, e := parseSource(entry)
			if e != nil {
				return Access{}, fmt.Errorf("gateway.access %q: %w", entry, e)
			}
			a.prefixes = append(a.prefixes, p)
		}
	}
	return a, nil
}

func parseSource(entry string) (netip.Prefix, error) {
	if strings.Contains(entry, "/") {
		return netip.ParsePrefix(entry)
	}
	addr, e := netip.ParseAddr(entry)
	if e != nil {
		return netip.Prefix{}, e
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// AllowsSource reports whether a request from addr is served at all. The token check comes after.
func (a Access) AllowsSource(addr netip.Addr) bool {
	addr = addr.Unmap()
	if inAny(loopbackPrefixes, addr) {
		return true
	}
	if a.lan && inAny(lanPrefixes, addr) && !inAny(tailnetPrefixes, addr) {
		return true
	}
	if a.tailnet && inAny(tailnetPrefixes, addr) {
		return true
	}
	return inAny(a.prefixes, addr)
}

// IsLANAddr reports whether addr is a private LAN address, the ones the lan entry covers.
func IsLANAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return inAny(lanPrefixes, addr) && !inAny(tailnetPrefixes, addr)
}

// IsTailnetAddr reports whether addr is a Tailscale address.
func IsTailnetAddr(addr netip.Addr) bool { return inAny(tailnetPrefixes, addr.Unmap()) }

func inAny(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// HostAllowed reports whether a Host header may name the gateway: an IP literal, localhost, or one of
// the configured host names. Anything else is refused, which also blocks DNS rebinding.
func HostAllowed(header string, hosts []string) bool {
	host := header
	if h, _, e := net.SplitHostPort(header); e == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(host, "["), "]"), ".")
	if host == "" {
		return false
	}
	if _, e := netip.ParseAddr(host); e == nil {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	for _, h := range hosts {
		if strings.EqualFold(host, h) {
			return true
		}
	}
	return false
}

// GatewayListenHost reports whether a gateway.listen value binds the local interface only.
func GatewayListenHost(listen string) (host string, loopback bool, e error) {
	host, _, e = net.SplitHostPort(listen)
	if e != nil {
		return "", false, e
	}
	ip := net.ParseIP(host)
	return host, ip != nil && ip.IsLoopback(), nil
}

// LANEnabled reports whether the lan entry is present: every private LAN address is then listed.
func (a Access) LANEnabled() bool { return a.lan }

// TailnetEnabled reports whether the tailnet entry is present.
func (a Access) TailnetEnabled() bool { return a.tailnet }
