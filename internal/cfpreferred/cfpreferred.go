// Package cfpreferred handles "preferred" Cloudflare edge IPs: addresses inside Cloudflare's published
// ranges that a device dials instead of the one DNS returns, because from some networks the default edge
// address is slow or lossy. Only the TCP destination changes; SNI, Host header, the Access service token
// and the pinned mTLS inside the tunnel are untouched, so a wrong or hostile address can cost availability
// but not confidentiality. The list is therefore restricted to Cloudflare's own ranges.
package cfpreferred

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"
)

// Ranges are Cloudflare's published edge ranges (https://www.cloudflare.com/ips/).
var Ranges = mustPrefixes(
	"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
	"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
	"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
	"2a06:98c0::/29", "2c0f:f248::/32",
)

func mustPrefixes(s ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(s))
	for i, p := range s {
		out[i] = netip.MustParsePrefix(p)
	}
	return out
}

// MaxList bounds how many preferred addresses a device keeps.
const MaxList = 64

// Valid reports whether s is an IP address inside Cloudflare's ranges.
func Valid(s string) bool {
	_, err := Parse(s)
	return err == nil
}

// Parse validates one address.
func Parse(s string) (netip.Addr, error) {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%q 不是有效的 IP 地址", s)
	}
	a = a.Unmap()
	for _, p := range Ranges {
		if p.Contains(a) {
			return a, nil
		}
	}
	return netip.Addr{}, fmt.Errorf("%s 不在 Cloudflare 公布的 IP 段内", a)
}

// ParseList reads addresses from r: one per line, or the result.csv written by CloudflareSpeedTest
// (the address is the first column, a header row is skipped). Blank lines and # comments are ignored,
// duplicates are dropped, order is kept. A line that is not a Cloudflare address is an error rather than
// being skipped, so a poisoned list is rejected whole.
func ParseList(r io.Reader) ([]netip.Addr, error) {
	out, _, err := parseList(r, true)
	return out, err
}

// ParseLines is ParseList for third-party lists: lines that are not Cloudflare addresses are skipped and
// reported (line number, text, reason) instead of rejecting the whole list.
func ParseLines(r io.Reader) ([]netip.Addr, []Reject, error) {
	return parseList(r, false)
}

// Reject is one line ParseLines did not accept.
type Reject struct {
	Line int
	Text string
	Err  error
}

func parseList(r io.Reader, strict bool) ([]netip.Addr, []Reject, error) {
	var out []netip.Addr
	var rejects []Reject
	seen := map[netip.Addr]bool{}
	const limit = 1 << 20
	lr := &io.LimitedReader{R: r, N: limit + 1}
	sc := bufio.NewScanner(lr)
	first := true
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		field, _, csv := strings.Cut(line, ",")
		field = strings.TrimSpace(field)
		isFirst := first
		first = false
		if isFirst && csv && !looksLikeIP(field) {
			continue // CSV header: the first line with content, after any blank lines or comments
		}
		a, err := Parse(field)
		if err != nil {
			if strict {
				return nil, nil, fmt.Errorf("第 %d 行: %w", n, err)
			}
			rejects = append(rejects, Reject{Line: n, Text: field, Err: err})
			continue
		}
		if !seen[a] {
			seen[a] = true
			out = append(out, a)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}
	if lr.N <= 0 {
		return nil, nil, errors.New("列表超过 1 MiB")
	}
	if len(out) > MaxList {
		out = out[:MaxList]
	}
	return out, rejects, nil
}

func looksLikeIP(s string) bool { _, err := netip.ParseAddr(s); return err == nil }

// Pin returns a dial function that connects to addr on the requested port instead of resolving the host.
func Pin(addr netip.Addr) func(ctx context.Context, network, address string) (net.Conn, error) {
	d := &net.Dialer{Timeout: 10 * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		return d.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
	}
}

// Result is one scanned address.
type Result struct {
	Addr    netip.Addr
	Sent    int
	Lost    int
	Latency time.Duration // mean TLS handshake time over the successful attempts
}

// LossRate is the fraction of attempts that failed.
func (r Result) LossRate() float64 {
	if r.Sent == 0 {
		return 1
	}
	return float64(r.Lost) / float64(r.Sent)
}

// Sample draws up to n distinct addresses at random from Cloudflare's ranges (IPv6 only when v6 is set).
func Sample(n int, v6 bool, rng *rand.Rand) []netip.Addr {
	var pool []netip.Prefix
	for _, p := range Ranges {
		if p.Addr().Is4() || v6 {
			pool = append(pool, p)
		}
	}
	seen := map[netip.Addr]bool{}
	var out []netip.Addr
	for tries := 0; len(out) < n && tries < n*8; tries++ {
		p := pool[rng.IntN(len(pool))]
		a := randomIn(p, rng)
		if !seen[a] && p.Contains(a) {
			seen[a] = true
			out = append(out, a)
		}
	}
	return out
}

func randomIn(p netip.Prefix, rng *rand.Rand) netip.Addr {
	b := p.Masked().Addr().AsSlice()
	for i := p.Bits(); i < len(b)*8; i++ {
		if rng.IntN(2) == 1 {
			b[i/8] |= 1 << (7 - i%8)
		}
	}
	a, _ := netip.AddrFromSlice(b)
	return a
}

// Scan measures each address by completing a TLS handshake with serverName on port 443, tries times,
// with at most parallel handshakes at once. Results are sorted best first: fewer losses, then lower latency;
// addresses that never answered are dropped.
func Scan(ctx context.Context, addrs []netip.Addr, serverName string, tries, parallel int, timeout time.Duration) []Result {
	if tries < 1 {
		tries = 3
	}
	if parallel < 1 {
		parallel = 16
	}
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	results := make([]Result, len(addrs))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, a := range addrs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := Result{Addr: a}
			var total time.Duration
			for range tries {
				if ctx.Err() != nil {
					return
				}
				r.Sent++
				d, err := handshake(ctx, a, serverName, timeout)
				if err != nil {
					r.Lost++
					continue
				}
				total += d
			}
			if ok := r.Sent - r.Lost; ok > 0 {
				r.Latency = total / time.Duration(ok)
			}
			results[i] = r
		}()
	}
	wg.Wait()
	out := results[:0]
	for _, r := range results {
		if r.Sent > 0 && r.Lost < r.Sent {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Lost != out[j].Lost {
			return out[i].Lost < out[j].Lost
		}
		return out[i].Latency < out[j].Latency
	})
	return out
}

func handshake(ctx context.Context, a netip.Addr, serverName string, timeout time.Duration) (time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	raw, err := (&net.Dialer{}).DialContext(ctx, "tcp", net.JoinHostPort(a.String(), "443"))
	if err != nil {
		return 0, err
	}
	defer raw.Close()
	conn := tls.Client(raw, &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	if err := conn.HandshakeContext(ctx); err != nil {
		return 0, err
	}
	if len(conn.ConnectionState().PeerCertificates) == 0 {
		return 0, errors.New("no certificate")
	}
	return time.Since(start), nil
}
