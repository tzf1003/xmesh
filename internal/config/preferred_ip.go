package config

import (
	"fmt"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/cfpreferred"
)

// PreferredIP configures how dialing a peer's Cloudflare tunnel picks the edge address. The pool is built
// from the sources below plus the older preferred_ips and preferred_ips_file fields; with nothing in the
// pool the normal DNS address is used, so the section is harmless to leave on.
type PreferredIP struct {
	// Enabled is the master switch; nil means on. Off sends every connection to the DNS address and keeps
	// the pool and its settings.
	Enabled *bool `json:"enabled,omitempty"`
	// Mode is how a connection picks from the pool: "smart" (default) tries the best scored address first,
	// "fixed" only tries FixedIPs.
	Mode     string   `json:"mode,omitempty"`
	FixedIPs []string `json:"fixed_ips,omitempty"`
	// AttemptTimeout bounds one address's whole attempt (TCP, TLS, Access, WebSocket and the inner
	// handshake). Default 4s.
	AttemptTimeout string `json:"attempt_timeout,omitempty"`
	// FailThreshold consecutive failures put an address on Cooldown (default 3 and 30m).
	FailThreshold int    `json:"fail_threshold,omitempty"`
	Cooldown      string `json:"cooldown,omitempty"`
	// NoDNSFallback stops dialing the DNS address when every preferred address failed.
	NoDNSFallback bool              `json:"no_dns_fallback,omitempty"`
	Sources       []PreferredSource `json:"sources,omitempty"`
	Scan          *PreferredScan    `json:"scan,omitempty"`
	// Peers overrides the default for one paired device, keyed by device ID.
	Peers map[string]PreferredPeer `json:"peers,omitempty"`
}

// PreferredSource is one place addresses come from: "manual" (IPs), "file" (Path) or "url" (URL, fetched
// every Refresh). A disabled source stays configured but adds nothing to the pool.
type PreferredSource struct {
	Type     string   `json:"type"`
	Disabled bool     `json:"disabled,omitempty"`
	Name     string   `json:"name,omitempty"`
	IPs      []string `json:"ips,omitempty"`
	Path     string   `json:"path,omitempty"`
	URL      string   `json:"url,omitempty"`
	Refresh  string   `json:"refresh,omitempty"`
}

// PreferredScan samples Cloudflare's published ranges on a schedule and keeps the best addresses.
type PreferredScan struct {
	Enabled  bool   `json:"enabled"`
	Interval string `json:"interval,omitempty"`
	Sample   int    `json:"sample,omitempty"`
	Keep     int    `json:"keep,omitempty"`
	IPv6     bool   `json:"ipv6,omitempty"`
	// Host is the tunnel hostname the handshake test uses; empty means the first configured peer's.
	Host string `json:"host,omitempty"`
	// RegionMode decides which data centres a scan may keep addresses from: "auto" (also when empty) keeps
	// the RegionCount regions that answered fastest, "pick" keeps only Regions, "none" keeps the fastest
	// addresses wherever they landed. It only applies to addresses the scan itself added.
	RegionMode  string `json:"region_mode,omitempty"`
	RegionCount int    `json:"region_count,omitempty"`
	// Regions are region keys (hk, jp, kr, sg, sea, us-w, us-e, eu, other) or three-letter colo codes.
	Regions []string `json:"regions,omitempty"`
}

// PreferredPeer is one device's override: "default" (or empty) follows the global mode, "smart" and "fixed"
// force one, "off" dials the DNS address.
type PreferredPeer struct {
	Mode string   `json:"mode,omitempty"`
	IPs  []string `json:"ips,omitempty"`
}

const (
	DefaultPreferredAttempt   = 4 * time.Second
	DefaultPreferredCooldown  = 30 * time.Minute
	DefaultPreferredFailures  = 3
	DefaultPreferredRefresh   = 12 * time.Hour
	DefaultPreferredScanEvery = 6 * time.Hour
	DefaultPreferredSample    = 200
	DefaultPreferredKeep      = 8
	DefaultPreferredRegions   = 2
	MaxPreferredRegions       = 6
	MaxPreferredSources       = 8
)

// PreferredOn reports whether preferred addresses are used at all.
func (c Cloudflare) PreferredOn() bool {
	return c.PreferredIP == nil || c.PreferredIP.Enabled == nil || *c.PreferredIP.Enabled
}

func (p *PreferredIP) mode() string {
	if p == nil || p.Mode == "" {
		return "smart"
	}
	return p.Mode
}

// PeerMode is the effective mode for one peer: "smart", "fixed" or "off", plus the fixed addresses.
func (c Cloudflare) PeerMode(deviceID string) (string, []string) {
	p := c.PreferredIP
	if !c.PreferredOn() {
		return "off", nil
	}
	if o, ok := p.peer(deviceID); ok && o.Mode != "" && o.Mode != "default" {
		if o.Mode == "fixed" {
			return "fixed", o.IPs
		}
		return o.Mode, nil
	}
	if p.mode() == "fixed" {
		return "fixed", p.FixedIPs
	}
	return "smart", nil
}

func (p *PreferredIP) peer(id string) (PreferredPeer, bool) {
	if p == nil {
		return PreferredPeer{}, false
	}
	o, ok := p.Peers[id]
	return o, ok
}

func (p *PreferredIP) dur(s string, def time.Duration) time.Duration {
	if p == nil || s == "" {
		return def
	}
	if d, err := time.ParseDuration(s); err == nil && d > 0 {
		return d
	}
	return def
}

func (c Cloudflare) PreferredAttemptTimeout() time.Duration {
	if c.PreferredIP == nil {
		return DefaultPreferredAttempt
	}
	return c.PreferredIP.dur(c.PreferredIP.AttemptTimeout, DefaultPreferredAttempt)
}

func (c Cloudflare) PreferredCooldown() time.Duration {
	if c.PreferredIP == nil {
		return DefaultPreferredCooldown
	}
	return c.PreferredIP.dur(c.PreferredIP.Cooldown, DefaultPreferredCooldown)
}

func (c Cloudflare) PreferredFailThreshold() int {
	if c.PreferredIP == nil || c.PreferredIP.FailThreshold < 1 {
		return DefaultPreferredFailures
	}
	return c.PreferredIP.FailThreshold
}

// RefreshEvery is how often a url source is fetched.
func (s PreferredSource) RefreshEvery() time.Duration {
	if d, err := time.ParseDuration(s.Refresh); err == nil && d > 0 {
		return d
	}
	return DefaultPreferredRefresh
}

// Effective is the scan settings with defaults filled in.
func (s PreferredScan) Effective() PreferredScan {
	if d, err := time.ParseDuration(s.Interval); err != nil || d <= 0 {
		s.Interval = DefaultPreferredScanEvery.String()
	}
	if s.Sample < 1 {
		s.Sample = DefaultPreferredSample
	}
	if s.Keep < 1 {
		s.Keep = DefaultPreferredKeep
	}
	if s.RegionMode == "" {
		s.RegionMode = "auto"
	}
	if s.RegionCount < 1 {
		s.RegionCount = DefaultPreferredRegions
	}
	return s
}

// ScanEvery is the effective scan interval.
func (s PreferredScan) ScanEvery() time.Duration {
	d, _ := time.ParseDuration(s.Effective().Interval)
	return d
}

func validPreferredMode(m string, peer bool) bool {
	if peer {
		return slices.Contains([]string{"", "default", "smart", "fixed", "off"}, m)
	}
	return slices.Contains([]string{"", "smart", "fixed"}, m)
}

func validPreferredIPs(ips []string) error {
	if len(ips) > cfpreferred.MaxList {
		return fmt.Errorf("最多 %d 个地址", cfpreferred.MaxList)
	}
	for _, ip := range ips {
		if _, err := cfpreferred.Parse(ip); err != nil {
			return err
		}
	}
	return nil
}

func validPreferredDuration(s string, min, max time.Duration) bool {
	if s == "" {
		return true
	}
	d, err := time.ParseDuration(s)
	return err == nil && d >= min && d <= max
}

// validate reports the first problem as a JSON pointer plus message, for Config.Validate to fail with.
func (p *PreferredIP) validate() (string, bool) {
	if p == nil {
		return "", true
	}
	const at = "/mesh/cloudflare/preferred_ip"
	if !validPreferredMode(p.Mode, false) {
		return at + "/mode: 只能是 smart 或 fixed", false
	}
	if err := validPreferredIPs(p.FixedIPs); err != nil {
		return at + "/fixed_ips: " + err.Error(), false
	}
	if p.Mode == "fixed" && len(p.FixedIPs) == 0 {
		return at + "/fixed_ips: 固定模式至少要有一个地址", false
	}
	if !validPreferredDuration(p.AttemptTimeout, time.Second, 30*time.Second) {
		return at + "/attempt_timeout: 1 秒到 30 秒", false
	}
	if !validPreferredDuration(p.Cooldown, time.Minute, 24*time.Hour) {
		return at + "/cooldown: 1 分钟到 24 小时", false
	}
	if p.FailThreshold < 0 || p.FailThreshold > 20 {
		return at + "/fail_threshold: 0 到 20（0 表示默认）", false
	}
	if len(p.Sources) > MaxPreferredSources {
		return fmt.Sprintf("%s/sources: 最多 %d 个来源", at, MaxPreferredSources), false
	}
	for i, s := range p.Sources {
		ptr := fmt.Sprintf("%s/sources/%d", at, i)
		switch s.Type {
		case "manual":
			if err := validPreferredIPs(s.IPs); err != nil {
				return ptr + "/ips: " + err.Error(), false
			}
			if s.Path != "" || s.URL != "" {
				return ptr + ": manual 来源只用 ips", false
			}
		case "file":
			if !filepath.IsAbs(s.Path) || len(s.IPs) > 0 || s.URL != "" {
				return ptr + "/path: file 来源需要绝对路径", false
			}
		case "url":
			u, err := url.Parse(s.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(s.URL) > 2048 || len(s.IPs) > 0 || s.Path != "" {
				return ptr + "/url: 订阅地址必须是 https，且不含用户信息", false
			}
			if !validPreferredDuration(s.Refresh, 5*time.Minute, 7*24*time.Hour) {
				return ptr + "/refresh: 5 分钟到 7 天", false
			}
		default:
			return ptr + "/type: 只能是 manual、file 或 url", false
		}
		if len(s.Name) > 64 || strings.ContainsAny(s.Name, "\x00\r\n") {
			return ptr + "/name", false
		}
	}
	if sc := p.Scan; sc != nil {
		if !validPreferredDuration(sc.Interval, 10*time.Minute, 7*24*time.Hour) {
			return at + "/scan/interval: 10 分钟到 7 天", false
		}
		if sc.Sample < 0 || sc.Sample > 2000 || sc.Keep < 0 || sc.Keep > cfpreferred.MaxList {
			return at + "/scan: sample 最多 2000，keep 最多 64", false
		}
		if sc.Host != "" && !hostnamePattern.MatchString(sc.Host) {
			return at + "/scan/host", false
		}
		if !slices.Contains([]string{"", "auto", "none", "pick"}, sc.RegionMode) {
			return at + "/scan/region_mode: 只能是 auto、none 或 pick", false
		}
		if sc.RegionCount < 0 || sc.RegionCount > MaxPreferredRegions {
			return fmt.Sprintf("%s/scan/region_count: 1 到 %d（0 表示默认）", at, MaxPreferredRegions), false
		}
		if len(sc.Regions) > 40 {
			return at + "/scan/regions: 最多 40 项", false
		}
		for _, r := range sc.Regions {
			if !cfpreferred.ValidRegion(r) {
				return at + "/scan/regions: " + r + " 不是地区或三字母机房代码", false
			}
		}
		if sc.RegionMode == "pick" && len(sc.Regions) == 0 {
			return at + "/scan/regions: 指定地区模式至少要选一项", false
		}
	}
	if len(p.Peers) > 64 {
		return at + "/peers: 最多 64 台设备", false
	}
	for id, o := range p.Peers {
		if !devicePattern.MatchString(id) {
			return at + "/peers: 设备 ID 无效", false
		}
		if !validPreferredMode(o.Mode, true) {
			return at + "/peers: 模式只能是 default、smart、fixed 或 off", false
		}
		if err := validPreferredIPs(o.IPs); err != nil {
			return at + "/peers: " + err.Error(), false
		}
		if o.Mode == "fixed" && len(o.IPs) == 0 {
			return at + "/peers: fixed 至少要有一个地址", false
		}
	}
	return "", true
}

// PeerForHost is the paired device whose tunnel hostname is host, or "" when none is configured.
func (c Cloudflare) PeerForHost(host string) string {
	for id, h := range c.Peers {
		if strings.EqualFold(h, host) {
			return id
		}
	}
	return ""
}

// PeerOverridden reports whether the device has its own mode instead of following the default.
func (c Cloudflare) PeerOverridden(deviceID string) bool {
	o, ok := c.PreferredIP.peer(deviceID)
	return ok && o.Mode != "" && o.Mode != "default"
}
