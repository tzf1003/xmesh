package connectivity

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/cfpreferred"
	"github.com/tzf1003/xmesh/internal/config"
)

const (
	preferredStateFile = "preferred-ip.json"
	preferredTick      = time.Minute
	// A failed subscription is tried again after this long, or its refresh interval when that is shorter.
	preferredRetry   = 15 * time.Minute
	preferredFetch   = 15 * time.Second
	preferredListMax = 1 << 20
)

// preferredPool keeps what the kernel knows about Cloudflare edge addresses: live success and failure
// counts, the last speed test, the cached subscription lists and which address each peer last used. Only
// the speed test and subscription caches are written to disk; cooldowns restart empty with the process.
type preferredPool struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
	// scanFn and client are replaced by tests.
	scanFn func(ctx context.Context, addrs []netip.Addr, host string) []cfpreferred.Result
	client *http.Client

	stats map[netip.Addr]*addrStat
	st    preferredFile
	use   map[string]peerUse // by lower-case tunnel hostname

	scanning    bool
	scanStarted time.Time
}

type addrStat struct {
	fails    int
	cooldown time.Time
	lastUsed time.Time
	latency  time.Duration // moving average of live dials
}

type peerUse struct {
	addr netip.Addr // zero when the DNS address was used
	at   time.Time
}

type preferredFile struct {
	Scan scanRecord           `json:"scan"`
	URLs map[string]urlRecord `json:"urls,omitempty"`
}

type scanRecord struct {
	At      time.Time `json:"at"`
	Host    string    `json:"host,omitempty"`
	Sample  int       `json:"sample,omitempty"`
	Error   string    `json:"error,omitempty"`
	Results []scanRow `json:"results,omitempty"`
}

type scanRow struct {
	IP        string `json:"ip"`
	LatencyMS int    `json:"latency_ms"`
	Loss      int    `json:"loss"`
	Keep      bool   `json:"keep,omitempty"`
}

type urlRecord struct {
	At    time.Time `json:"at"`
	IPs   []string  `json:"ips,omitempty"`
	Error string    `json:"error,omitempty"`
}

func newPreferredPool(stateDir string) *preferredPool {
	p := &preferredPool{now: time.Now, stats: map[netip.Addr]*addrStat{}, use: map[string]peerUse{}, st: preferredFile{URLs: map[string]urlRecord{}}}
	p.scanFn = realScan
	p.client = &http.Client{Timeout: preferredFetch, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || req.URL.Scheme != "https" {
			return errors.New("订阅地址的跳转必须是 https，且不超过 3 次")
		}
		return nil
	}}
	if stateDir != "" {
		p.path = filepath.Join(stateDir, preferredStateFile)
		if b, err := os.ReadFile(p.path); err == nil {
			var f preferredFile
			if json.Unmarshal(b, &f) == nil {
				p.st = f
				if p.st.URLs == nil {
					p.st.URLs = map[string]urlRecord{}
				}
			}
		}
	}
	return p
}

func realScan(ctx context.Context, addrs []netip.Addr, host string) []cfpreferred.Result {
	return cfpreferred.Scan(ctx, addrs, host, 3, 16, 3*time.Second)
}

// save writes the persistent part; the caller holds p.mu. A failed write only costs the cache.
func (p *preferredPool) save() {
	if p.path == "" {
		return
	}
	b, err := json.Marshal(p.st)
	if err != nil {
		return
	}
	tmp := p.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, p.path)
	}
}

// member is one address of the pool and where it came from.
type member struct {
	addr   netip.Addr
	source string
}

// members composes the pool in source order: the older preferred_ips and preferred_ips_file fields, then each
// enabled source, then the best scanned addresses. The first source to list an address owns it.
func (p *preferredPool) members(cfg config.Cloudflare) []member {
	var out []member
	seen := map[netip.Addr]bool{}
	add := func(source string, addrs ...netip.Addr) {
		for _, a := range addrs {
			if !seen[a] && len(out) < cfpreferred.MaxList {
				seen[a] = true
				out = append(out, member{a, source})
			}
		}
	}
	add("manual", parseAddrs(cfg.PreferredIPs)...)
	if cfg.PreferredIPsFile != "" {
		add("file", readListFile(cfg.PreferredIPsFile)...)
	}
	if pp := cfg.PreferredIP; pp != nil {
		for _, s := range pp.Sources {
			if s.Disabled {
				continue
			}
			switch s.Type {
			case "manual":
				add("manual", parseAddrs(s.IPs)...)
			case "file":
				add("file", readListFile(s.Path)...)
			case "url":
				p.mu.Lock()
				rec := p.st.URLs[s.URL]
				p.mu.Unlock()
				add("url", parseAddrs(rec.IPs)...)
			}
		}
		if pp.Scan != nil && pp.Scan.Enabled {
			p.mu.Lock()
			for _, r := range p.st.Scan.Results {
				if r.Keep {
					if a, err := cfpreferred.Parse(r.IP); err == nil {
						add("scan", a)
					}
				}
			}
			p.mu.Unlock()
		}
	}
	return out
}

func parseAddrs(in []string) []netip.Addr {
	var out []netip.Addr
	for _, s := range in {
		if a, err := cfpreferred.Parse(s); err == nil {
			out = append(out, a)
		}
	}
	return out
}

func readListFile(path string) []netip.Addr {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	list, _ := cfpreferred.ParseList(f)
	return list
}

// latency is the best estimate of how fast an address is: the live moving average when it has been used,
// else the last scan. ok is false when neither exists.
func (p *preferredPool) latency(a netip.Addr) (time.Duration, bool) {
	if s := p.stats[a]; s != nil && s.latency > 0 {
		return s.latency, true
	}
	for _, r := range p.st.Scan.Results {
		if r.IP == a.String() {
			return time.Duration(r.LatencyMS) * time.Millisecond, true
		}
	}
	return 0, false
}

// dialOrder is the addresses a connection to peer should try, best first. "fixed" uses the given addresses
// as they are; "smart" ranks the pool by measured latency (unmeasured addresses keep their source order after
// the measured ones) and leaves out addresses cooling down after repeated failures.
func (p *preferredPool) dialOrder(cfg config.Cloudflare, mode string, fixed []string) []netip.Addr {
	switch mode {
	case "fixed":
		return parseAddrs(fixed)
	case "smart":
	default:
		return nil
	}
	pool := p.members(cfg)
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	type ranked struct {
		a    netip.Addr
		cost time.Duration
		ok   bool
	}
	var rs []ranked
	for _, m := range pool {
		if s := p.stats[m.addr]; s != nil && now.Before(s.cooldown) {
			continue
		}
		lat, ok := p.latency(m.addr)
		if s := p.stats[m.addr]; s != nil {
			lat += time.Duration(s.fails) * 500 * time.Millisecond
		}
		rs = append(rs, ranked{m.addr, lat, ok})
	}
	sort.SliceStable(rs, func(i, j int) bool {
		if rs[i].ok != rs[j].ok {
			return rs[i].ok
		}
		return rs[i].ok && rs[i].cost < rs[j].cost
	})
	out := make([]netip.Addr, len(rs))
	for i, r := range rs {
		out[i] = r.a
	}
	return out
}

func (p *preferredPool) stat(a netip.Addr) *addrStat {
	s := p.stats[a]
	if s == nil {
		s = &addrStat{}
		p.stats[a] = s
	}
	return s
}

// succeeded records a dial through addr (the zero address means the DNS address) to the peer at host.
func (p *preferredPool) succeeded(host string, a netip.Addr, took time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if a.IsValid() {
		s := p.stat(a)
		s.fails, s.cooldown, s.lastUsed = 0, time.Time{}, now
		if s.latency == 0 {
			s.latency = took
		} else {
			s.latency = (s.latency*7 + took*3) / 10
		}
	}
	p.use[strings.ToLower(host)] = peerUse{a, now}
}

// failed counts one failed attempt through addr; the threshold-th consecutive failure starts its cooldown.
func (p *preferredPool) failed(a netip.Addr, threshold int, cooldown time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.stat(a)
	s.fails++
	if s.fails >= threshold {
		s.cooldown = p.now().Add(cooldown)
		s.fails = threshold - 1 // one more failure after the cooldown starts the next one
	}
}

// ---- sources ----

// fetchURL downloads a subscription list and records the outcome in the cache.
func (p *preferredPool) fetchURL(ctx context.Context, raw string) {
	addrs, err := p.download(ctx, raw)
	p.mu.Lock()
	defer p.mu.Unlock()
	rec := urlRecord{At: p.now()}
	if err != nil {
		rec.Error = err.Error()
		rec.IPs = p.st.URLs[raw].IPs // keep serving the last good list
	} else {
		for _, a := range addrs {
			rec.IPs = append(rec.IPs, a.String())
		}
	}
	p.st.URLs[raw] = rec
	p.save()
}

func (p *preferredPool) download(ctx context.Context, raw string) ([]netip.Addr, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, errors.New("订阅地址必须是 https")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "xmesh-preferred-ip")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("无法下载订阅：%w", urlError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("订阅服务器返回 %d", resp.StatusCode)
	}
	addrs, rejects, err := cfpreferred.ParseLines(io.LimitReader(resp.Body, preferredListMax+1))
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		if len(rejects) > 0 {
			return nil, fmt.Errorf("订阅里没有 Cloudflare 地址（%d 行被拒绝）", len(rejects))
		}
		return nil, errors.New("订阅是空的")
	}
	return addrs, nil
}

// urlError drops the URL from a client error so a subscription address with a token in it is not shown.
func urlError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

// refresh fetches every enabled subscription; with force it ignores how recently each was fetched.
func (p *preferredPool) refresh(ctx context.Context, cfg config.Cloudflare, force bool) {
	pp := cfg.PreferredIP
	if pp == nil {
		return
	}
	seen := map[string]bool{}
	for _, s := range pp.Sources {
		if s.Type != "url" || s.Disabled || seen[s.URL] {
			continue
		}
		seen[s.URL] = true
		p.mu.Lock()
		rec, have := p.st.URLs[s.URL]
		wait := s.RefreshEvery()
		if rec.Error != "" {
			wait = min(wait, preferredRetry)
		}
		due := !have || p.now().Sub(rec.At) >= wait
		p.mu.Unlock()
		if force || due {
			p.fetchURL(ctx, s.URL)
		}
	}
}

// ---- scan ----

func scanHost(cfg config.Cloudflare) string {
	if pp := cfg.PreferredIP; pp != nil && pp.Scan != nil && pp.Scan.Host != "" {
		return pp.Scan.Host
	}
	ids := make([]string, 0, len(cfg.Peers))
	for id := range cfg.Peers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	if len(ids) > 0 {
		return cfg.Peers[ids[0]]
	}
	return ""
}

// startScan runs a speed test in the background unless one is already running.
func (p *preferredPool) startScan(ctx context.Context, cfg config.Cloudflare) error {
	pp := cfg.PreferredIP
	scan := config.PreferredScan{}
	if pp != nil && pp.Scan != nil {
		scan = *pp.Scan
	}
	scan = scan.Effective()
	host := scanHost(cfg)
	p.mu.Lock()
	if p.scanning {
		p.mu.Unlock()
		return nil
	}
	if host == "" {
		p.st.Scan.Error = "没有可用于测速的对端主机名：先配对一台设备，或在测速设置里填写主机名"
		p.mu.Unlock()
		return errors.New(p.st.Scan.Error)
	}
	p.scanning, p.scanStarted = true, p.now()
	p.mu.Unlock()
	pool := p.members(cfg)
	go func() {
		rng := rand.New(rand.NewPCG(uint64(time.Now().UnixNano()), 7))
		addrs := cfpreferred.Sample(scan.Sample, scan.IPv6, rng)
		sampled := map[netip.Addr]bool{}
		for _, a := range addrs {
			sampled[a] = true
		}
		for _, m := range pool { // re-measure what is already pooled so the ranking stays honest
			if !sampled[m.addr] && m.source != "scan" {
				addrs = append(addrs, m.addr)
			}
		}
		results := p.scanFn(ctx, addrs, host)
		p.finishScan(ctx, host, len(addrs), results, scan.Keep, pool)
	}()
	return nil
}

func (p *preferredPool) finishScan(ctx context.Context, host string, tested int, results []cfpreferred.Result, keep int, pool []member) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.scanning = false
	rec := scanRecord{At: p.now(), Host: host, Sample: tested}
	if ctx.Err() != nil {
		rec = p.st.Scan // interrupted: keep the previous result
		return
	}
	if len(results) == 0 {
		rec.Error = "没有任何地址握手成功：检查主机名与本机网络"
		rec.Results = p.st.Scan.Results
		p.st.Scan = rec
		p.save()
		return
	}
	pooled := map[netip.Addr]bool{}
	for _, m := range pool {
		pooled[m.addr] = true
	}
	kept := 0
	for _, r := range results {
		row := scanRow{IP: r.Addr.String(), LatencyMS: int(r.Latency.Milliseconds()), Loss: int(r.LossRate()*100 + 0.5)}
		if kept < keep && !pooled[r.Addr] {
			row.Keep = true
			kept++
		} else if !pooled[r.Addr] {
			continue
		}
		rec.Results = append(rec.Results, row)
	}
	p.st.Scan = rec
	p.save()
}

// ---- view ----

func at(t time.Time) *v2.Time {
	if t.IsZero() {
		return nil
	}
	v := v2.At(t)
	return &v
}

func sourceName(s config.PreferredSource) string {
	if s.Name != "" {
		return s.Name
	}
	switch s.Type {
	case "file":
		return filepath.Base(s.Path)
	case "url":
		if u, err := url.Parse(s.URL); err == nil {
			return u.Host
		}
	}
	return s.Type
}

// state is the view behind GET /v2/preferred-ip.
func (p *preferredPool) state(cfg config.Cloudflare) v2.PreferredIPState {
	pool := p.members(cfg)
	pp := cfg.PreferredIP
	out := v2.PreferredIPState{Enabled: cfg.PreferredOn(), Mode: "smart", Pool: []v2.PreferredIPEntry{}, Sources: []v2.PreferredIPSourceState{}, Peers: []v2.PreferredIPPeerState{}}
	if pp != nil {
		if pp.Mode == "fixed" {
			out.Mode = "fixed"
		}
		out.NoDNSFallback = pp.NoDNSFallback
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	for _, m := range pool {
		e := v2.PreferredIPEntry{IP: m.addr.String(), Source: m.source, Status: "untested"}
		for _, r := range p.st.Scan.Results {
			if r.IP == e.IP {
				l, loss := r.LatencyMS, r.Loss
				e.LatencyMS, e.LossPercent, e.Status = &l, &loss, "ok"
			}
		}
		if s := p.stats[m.addr]; s != nil {
			if s.latency > 0 {
				l := int(s.latency.Milliseconds())
				e.LatencyMS, e.Status = &l, "ok"
			}
			e.Failures = s.fails
			e.LastUsedAt = at(s.lastUsed)
			if now.Before(s.cooldown) {
				e.Status, e.CooldownUntil = "cooling", at(s.cooldown)
			}
		}
		out.Pool = append(out.Pool, e)
	}
	count := map[string]int{}
	for _, m := range pool {
		count[m.source]++
	}
	if len(cfg.PreferredIPs) > 0 {
		out.Sources = append(out.Sources, v2.PreferredIPSourceState{Index: -1, Type: "manual", Name: "preferred_ips", Count: len(cfg.PreferredIPs)})
	}
	if cfg.PreferredIPsFile != "" {
		out.Sources = append(out.Sources, v2.PreferredIPSourceState{Index: -1, Type: "file", Name: filepath.Base(cfg.PreferredIPsFile), Count: len(readListFile(cfg.PreferredIPsFile))})
	}
	if pp != nil {
		for i, s := range pp.Sources {
			row := v2.PreferredIPSourceState{Index: i, Type: s.Type, Name: sourceName(s), Disabled: s.Disabled}
			switch s.Type {
			case "manual":
				row.Count = len(parseAddrs(s.IPs))
			case "file":
				row.Count = len(readListFile(s.Path))
				if row.Count == 0 {
					row.Error = "文件不存在、读不了或没有有效地址"
				}
			case "url":
				rec := p.st.URLs[s.URL]
				row.Count, row.FetchedAt, row.Error = len(rec.IPs), at(rec.At), rec.Error
			}
			out.Sources = append(out.Sources, row)
		}
	}
	scan := v2.PreferredIPScanState{Running: p.scanning, Host: scanHost(cfg), StartedAt: at(p.scanStarted), LastAt: at(p.st.Scan.At), Error: p.st.Scan.Error}
	if !p.scanning {
		scan.StartedAt = nil
	}
	if pp != nil && pp.Scan != nil {
		eff := pp.Scan.Effective()
		scan.Enabled, scan.Sample = pp.Scan.Enabled, eff.Sample
		if pp.Scan.Enabled && !p.st.Scan.At.IsZero() {
			scan.NextAt = at(p.st.Scan.At.Add(pp.Scan.ScanEvery()))
		}
	}
	for _, r := range p.st.Scan.Results {
		if r.Keep {
			scan.Kept++
		}
	}
	out.Scan = scan
	ids := make([]string, 0, len(cfg.Peers))
	for id := range cfg.Peers {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		mode, ips := cfg.PeerMode(id)
		row := v2.PreferredIPPeerState{DeviceID: id, Hostname: cfg.Peers[id], Mode: mode, IPs: append([]string{}, ips...)}
		row.Override = cfg.PeerOverridden(id)
		if u, ok := p.use[strings.ToLower(row.Hostname)]; ok {
			if u.addr.IsValid() {
				row.Current = u.addr.String()
			}
			row.LastAt = at(u.at)
		}
		out.Peers = append(out.Peers, row)
	}
	return out
}

// inspect checks pasted text without changing anything.
func (p *preferredPool) inspect(cfg config.Cloudflare, text string) v2.PreferredIPInspectResponse {
	have := map[netip.Addr]bool{}
	for _, m := range p.members(cfg) {
		have[m.addr] = true
	}
	out := v2.PreferredIPInspectResponse{Items: []v2.PreferredIPInspectItem{}, Accepted: []string{}}
	seen := map[netip.Addr]bool{}
	first := true
	sc := bufio.NewScanner(strings.NewReader(text))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		field, _, csv := strings.Cut(line, ",")
		field = strings.TrimSpace(field)
		isFirst := first
		first = false
		if _, err := netip.ParseAddr(field); isFirst && csv && err != nil {
			continue // CloudflareSpeedTest header row
		}
		a, err := cfpreferred.Parse(field)
		item := v2.PreferredIPInspectItem{Line: n, IP: field}
		switch {
		case err != nil:
			item.Status, item.Note = "rejected", err.Error()
		case have[a] || seen[a]:
			item.IP, item.Status, item.Note = a.String(), "existing", "已在池中或重复"
		default:
			seen[a] = true
			item.IP, item.Status = a.String(), "new"
			out.Accepted = append(out.Accepted, item.IP)
		}
		out.Items = append(out.Items, item)
		if len(out.Items) >= 2000 {
			break
		}
	}
	return out
}

// ---- node glue ----

func (n *Node) preferredPool() *preferredPool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.preferred == nil {
		n.preferred = newPreferredPool(n.stateDir)
	}
	return n.preferred
}

func (n *Node) cloudflareConfig() config.Cloudflare {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.cfg.Cloudflare
}

// PreferredState is the pool, its scores and the scheduler state.
func (n *Node) PreferredState() v2.PreferredIPState {
	return n.preferredPool().state(n.cloudflareConfig())
}

// PreferredInspect checks pasted addresses against the pool.
func (n *Node) PreferredInspect(text string) v2.PreferredIPInspectResponse {
	return n.preferredPool().inspect(n.cloudflareConfig(), text)
}

// PreferredRun starts a task now: "scan" in the background, "refresh" until every subscription is fetched.
func (n *Node) PreferredRun(ctx context.Context, task string) (v2.PreferredIPState, error) {
	p, cfg := n.preferredPool(), n.cloudflareConfig()
	switch task {
	case "scan":
		if err := p.startScan(n.ctx, cfg); err != nil {
			return v2.PreferredIPState{}, err
		}
	case "refresh":
		p.refresh(ctx, cfg, true)
	default:
		return v2.PreferredIPState{}, fmt.Errorf("未知任务 %q", task)
	}
	return p.state(n.cloudflareConfig()), nil
}

// runPreferred is the scheduler: it refreshes subscriptions and runs the speed test when each is due.
func (n *Node) runPreferred(ctx context.Context) {
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		n.preferredTick(ctx)
		timer.Reset(preferredTick)
	}
}

func (n *Node) preferredTick(ctx context.Context) {
	cfg := n.cloudflareConfig()
	if !cfg.Enabled || !cfg.PreferredOn() || cfg.PreferredIP == nil {
		return
	}
	p := n.preferredPool()
	p.refresh(ctx, cfg, false)
	sc := cfg.PreferredIP.Scan
	if sc == nil || !sc.Enabled {
		return
	}
	p.mu.Lock()
	due := !p.scanning && p.now().Sub(p.st.Scan.At) >= sc.ScanEvery()
	p.mu.Unlock()
	if due {
		_ = p.startScan(ctx, cfg)
	}
}
