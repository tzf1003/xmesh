package meshadmin

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"tailscale.com/net/stun"
	"tailscale.com/tailcfg"
)

// ProbeCheck is one health check of a DERP region.
type ProbeCheck struct {
	Name   string `json:"name"` // tcp, tls, http, stun
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	// Latency is the round trip of this check, when it has one.
	Latency time.Duration `json:"latency_ns,omitempty"`
}

// ProbeResult is the outcome for one region.
type ProbeResult struct {
	Host   string       `json:"host"`
	OK     bool         `json:"ok"`
	Checks []ProbeCheck `json:"checks"`
}

// ProbeOptions adjust a probe; the zero value probes the real network.
type ProbeOptions struct {
	Timeout time.Duration
	// RootCAs replaces the system pool (tests use a self-signed derper).
	RootCAs *x509.CertPool
	// SkipSTUN skips the UDP check, for networks that block it.
	SkipSTUN bool
}

// ProbeDERP checks a region the way a client would use it: TCP to the DERP
// port, a verified TLS handshake for the hostname (or CertName), the derper
// /derp/probe endpoint, and a STUN binding request over UDP. The configured IP
// address is dialled when present, so the check also covers the case where
// clients skip DNS.
func ProbeDERP(ctx context.Context, s DERPRegionSpec, opt ProbeOptions) ProbeResult {
	if opt.Timeout == 0 {
		opt.Timeout = 5 * time.Second
	}
	port := s.DERPPort
	if port == 0 {
		port = 443
	}
	stunPort := s.STUNPort
	if stunPort == 0 {
		stunPort = 3478
	}
	dialHost := s.Hostname
	switch {
	case s.IPv4 != "":
		dialHost = s.IPv4
	case s.IPv6 != "":
		dialHost = s.IPv6
	}
	addr := net.JoinHostPort(dialHost, strconv.Itoa(port))
	res := ProbeResult{Host: s.Hostname}
	add := func(c ProbeCheck) bool { res.Checks = append(res.Checks, c); return c.OK }

	cctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()

	var d net.Dialer
	start := time.Now()
	conn, err := d.DialContext(cctx, "tcp", addr)
	if err != nil {
		add(ProbeCheck{Name: "tcp", Detail: fmt.Sprintf("cannot connect to %s: %v", addr, err)})
		res.OK = false
		return res
	}
	tcpLat := time.Since(start)
	add(ProbeCheck{Name: "tcp", OK: true, Detail: "connected to " + addr, Latency: tcpLat})

	serverName := s.CertName
	if serverName == "" {
		serverName = s.Hostname
	}
	cfg := &tls.Config{ServerName: serverName, RootCAs: opt.RootCAs, MinVersion: tls.VersionTLS12}
	tconn := tls.Client(conn, cfg)
	tstart := time.Now()
	if dl, ok := cctx.Deadline(); ok {
		_ = tconn.SetDeadline(dl)
	}
	if err := tconn.HandshakeContext(cctx); err != nil {
		conn.Close()
		add(ProbeCheck{Name: "tls", Detail: fmt.Sprintf("TLS verification for %q failed: %v", serverName, err)})
		res.OK = false
		return res
	}
	tconn.Close()
	add(ProbeCheck{Name: "tls", OK: true, Detail: fmt.Sprintf("certificate valid for %q", serverName), Latency: time.Since(tstart)})

	httpOK := probeHTTP(cctx, s, serverName, addr, opt, add)
	stunOK := true
	if !opt.SkipSTUN {
		stunOK = probeSTUN(cctx, dialHost, stunPort, add)
	}
	res.OK = httpOK && stunOK
	return res
}

func probeHTTP(ctx context.Context, s DERPRegionSpec, serverName, addr string, opt ProbeOptions, add func(ProbeCheck) bool) bool {
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{ServerName: serverName, RootCAs: opt.RootCAs, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
		DisableKeepAlives: true,
	}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+serverName+"/derp/probe", nil)
	if err != nil {
		return add(ProbeCheck{Name: "http", Detail: err.Error()})
	}
	start := time.Now()
	resp, err := hc.Do(req)
	if err != nil {
		return add(ProbeCheck{Name: "http", Detail: "GET /derp/probe failed: " + err.Error()})
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode != http.StatusOK {
		return add(ProbeCheck{Name: "http", Detail: fmt.Sprintf("GET /derp/probe returned HTTP %d (is this a DERP server?)", resp.StatusCode)})
	}
	return add(ProbeCheck{Name: "http", OK: true, Detail: "GET /derp/probe returned 200", Latency: time.Since(start)})
}

func probeSTUN(ctx context.Context, host string, port int, add func(ProbeCheck) bool) bool {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return add(ProbeCheck{Name: "stun", Detail: fmt.Sprintf("cannot open UDP to %s:%d: %v", host, port, err)})
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	txid := stun.NewTxID()
	start := time.Now()
	if _, err := conn.Write(stun.Request(txid)); err != nil {
		return add(ProbeCheck{Name: "stun", Detail: "send failed: " + err.Error()})
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return add(ProbeCheck{Name: "stun", Detail: fmt.Sprintf("no STUN reply from udp/%d: %v", port, err)})
	}
	got, _, err := stun.ParseResponse(buf[:n])
	if err != nil || got != txid {
		return add(ProbeCheck{Name: "stun", Detail: "reply is not a matching STUN binding response"})
	}
	return add(ProbeCheck{Name: "stun", OK: true, Detail: fmt.Sprintf("STUN binding answered on udp/%d", port), Latency: time.Since(start)})
}

// RegionLatency is a TCP connect latency to one region of an official DERP map.
type RegionLatency struct {
	RegionID int           `json:"region_id"`
	Code     string        `json:"code"`
	Host     string        `json:"host"`
	Latency  time.Duration `json:"latency_ns"`
	Err      string        `json:"error,omitempty"`
}

// DefaultOfficialDERPMapURL is the public map every Tailscale client falls back to.
const DefaultOfficialDERPMapURL = "https://controlplane.tailscale.com/derpmap/default"

// OfficialLatencies measures the TCP connect time to the first node of each
// official region, so a self-hosted region can be compared with what clients
// get today. It is best effort and returns regions sorted by latency (failures
// last).
func OfficialLatencies(ctx context.Context, mapURL string, hc *http.Client) ([]RegionLatency, error) {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mapURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("meshadmin: fetch official DERP map: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("meshadmin: fetch official DERP map: HTTP %d", resp.StatusCode)
	}
	var dm tailcfg.DERPMap
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&dm); err != nil {
		return nil, fmt.Errorf("meshadmin: official DERP map: %w", err)
	}
	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		out []RegionLatency
		sem = make(chan struct{}, 16)
	)
	for id, r := range dm.Regions {
		if r == nil || len(r.Nodes) == 0 || r.Avoid || r.NoMeasureNoHome {
			continue
		}
		n := r.Nodes[0]
		wg.Add(1)
		go func(id int, code, host string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rl := RegionLatency{RegionID: id, Code: code, Host: host}
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			var d net.Dialer
			start := time.Now()
			c, err := d.DialContext(cctx, "tcp", net.JoinHostPort(host, "443"))
			if err != nil {
				rl.Err = err.Error()
			} else {
				rl.Latency = time.Since(start)
				c.Close()
			}
			mu.Lock()
			out = append(out, rl)
			mu.Unlock()
		}(int(id), r.RegionCode, n.HostName)
	}
	wg.Wait()
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Err == "") != (out[j].Err == "") {
			return out[i].Err == ""
		}
		return out[i].Latency < out[j].Latency
	})
	return out, nil
}
