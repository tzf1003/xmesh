package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/tzf1003/xmesh/docs/contracts/schemas"
	"github.com/tzf1003/xmesh/internal/cwd"
	"go.yaml.in/yaml/v3"
)

type Config struct {
	Node    Node    `json:"node"`
	Mesh    Mesh    `json:"mesh"`
	Gateway Gateway `json:"gateway"`
	Ingress Ingress `json:"ingress"`
	Agent   Agent   `json:"agent"`
	Audit   Audit   `json:"audit"`
}
type Node struct {
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
}
type Mesh struct {
	Mode        string `json:"mode"`
	ControlURL  string `json:"control_url"`
	AuthkeyFile string `json:"authkey_file"`
	Ephemeral   bool   `json:"ephemeral"`
	LAN         struct {
		MDNS bool `json:"mdns"`
	} `json:"lan"`
}
type Gateway struct {
	Listen         string         `json:"listen"`
	ExposeBackends string         `json:"expose_backends"`
	PATs           map[string]PAT `json:"pats"`
	Quota          Quota          `json:"quota"`
}

// Quota bounds the tool calls one principal may make. A zero field means the
// built-in default, so hand-built configurations keep working.
type Quota struct {
	CallsPerHour    int    `json:"calls_per_hour"`
	MaxConcurrent   int    `json:"max_concurrent"`
	MaxCallDuration string `json:"max_call_duration"`
}

const (
	DefaultCallsPerHour  = 10000
	DefaultMaxConcurrent = 64
	DefaultCallDuration  = 10 * time.Minute
)

// Limits returns the effective quota with defaults applied.
func (q Quota) Limits() (hourly, concurrent int, duration time.Duration) {
	hourly, concurrent, duration = q.CallsPerHour, q.MaxConcurrent, DefaultCallDuration
	if hourly <= 0 {
		hourly = DefaultCallsPerHour
	}
	if concurrent <= 0 {
		concurrent = DefaultMaxConcurrent
	}
	if d, e := time.ParseDuration(q.MaxCallDuration); e == nil && d > 0 {
		duration = d
	}
	return
}

type PAT struct {
	TokenFile string   `json:"token_file"`
	Scopes    []string `json:"scopes"`
}

var nodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)

type Ingress struct {
	MaxPTYPerClient int    `json:"max_pty_per_client"`
	PTYBudgetFile   string `json:"pty_budget_file"`
	// PTYBudgetAuthority names the Agent that hosts the cross-host admission lease.
	PTYBudgetAuthority string `json:"pty_budget_authority"`
	Listen             string `json:"listen"`
	Domain             string `json:"domain"`
	TLS                string `json:"tls"`
	CertFile           string `json:"cert_file"`
	KeyFile            string `json:"key_file"`
	OAuth              struct {
		Issuer  string `json:"issuer"`
		Clients map[string]struct {
			Scopes []string `json:"scopes"`
		} `json:"clients"`
		// DynamicClientScopes caps what a dynamically registered client may hold.
		// Absent means devices:read only; an empty list means none.
		DynamicClientScopes []string `json:"dynamic_client_scopes"`
	} `json:"oauth"`
}
type Agent struct {
	DefaultCWD string                   `json:"default_cwd"`
	Workspaces map[string]cwd.Workspace `json:"workspaces"`
	CWDPolicy  cwd.Policy               `json:"cwd_policy"`
	Exec       Exec                     `json:"exec"`
	PTY        PTY                      `json:"pty"`
	Backends   map[string]Backend       `json:"backends"`
	Authz      map[string]Grant         `json:"authz"`
	Policy     Policy                   `json:"policy"`
	Quota      Quota                    `json:"quota"`
}
type Exec struct {
	MaxConcurrent     int    `json:"max_concurrent"`
	ExclusiveSlots    int    `json:"exclusive_slots"`
	DefaultTimeout    string `json:"default_timeout"`
	IdempotencyCache  int    `json:"idempotency_cache"`
	InlineOutputBytes string `json:"inline_output_bytes"`
}
type PTY struct {
	MaxSessions     int    `json:"max_sessions"`
	MaxPerCaller    int    `json:"max_per_caller"`
	IdleTimeout     string `json:"idle_timeout"`
	OrphanGrace     string `json:"orphan_grace"`
	ScrollbackBytes string `json:"scrollback_bytes"`
	DefaultShell    string `json:"default_shell"`
	ClosedRetain    string `json:"closed_retain"`
	ClosedRetainMax int    `json:"closed_retain_max"`
}
type Backend struct {
	Transport   string   `json:"transport"`
	Command     []string `json:"command"`
	URL         string   `json:"url"`
	CWD         string   `json:"cwd"`
	Policy      string   `json:"policy"`
	IdleTimeout string   `json:"idle_timeout"`
	Favorite    bool     `json:"favorite"`
}
type Grant struct {
	Backends []string `json:"backends"`
	Tools    []string `json:"tools"`
}
type Policy struct {
	Isolation      string `json:"isolation"`
	AllowDangerous bool   `json:"allow_dangerous"`
	Exec           struct {
		AllowCmd     []string `json:"allow_cmd"`
		DenyPatterns []string `json:"deny_patterns"`
	} `json:"exec"`
	Env struct {
		DenyNames          []string `json:"deny_names"`
		DenyPathSubstrings []string `json:"deny_path_substrings"`
	} `json:"env"`
}
type Audit struct {
	Retention string `json:"retention"`
	Dir       string `json:"dir"`
}

func (c *Config) HasRole(role string) bool {
	for _, r := range c.Node.Roles {
		if role == r {
			return true
		}
	}
	return false
}
func Load(path string) (*Config, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	return Parse(b)
}
func Parse(b []byte) (*Config, error) {
	var doc map[string]any
	d := yaml.NewDecoder(bytes.NewReader(b))
	if e := d.Decode(&doc); e != nil {
		return nil, e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return nil, fmt.Errorf("配置只能包含一个 YAML 文档")
	}
	schemaBytes, _ := schemas.Files.ReadFile("config.schema.json")
	var schema any
	if e := json.Unmarshal(schemaBytes, &schema); e != nil {
		return nil, e
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if e := compiler.AddResource("urn:xmesh:config", schema); e != nil {
		return nil, e
	}
	compiled, e := compiler.Compile("urn:xmesh:config")
	if e != nil {
		return nil, e
	}
	// JSON conversion normalizes the YAML decoder's numeric representation.
	raw, e := json.Marshal(doc)
	if e != nil {
		return nil, e
	}
	var normalized any
	if e = json.Unmarshal(raw, &normalized); e != nil {
		return nil, e
	}
	// relay is a known future mode, not a typo: name it as unimplemented (CON-NET-08)
	// instead of surfacing a bare enum mismatch. The process still refuses to start.
	if root, ok := normalized.(map[string]any); ok {
		if mesh, ok := root["mesh"].(map[string]any); ok && mesh["mode"] == "relay" {
			return nil, fmt.Errorf("配置无效: /mesh/mode: relay 模式本期未实现，请改用 tailnet 或 lan")
		}
	}
	if e = compiled.Validate(normalized); e != nil {
		return nil, e
	}
	c := &Config{}
	c.Agent.Exec = Exec{4, 1, "120s", 200, "256KiB"}
	c.Agent.PTY = PTY{20, 8, "30m", "10m", "1MiB", "auto", "5m", 20}
	c.Agent.Policy.Isolation = "L1"
	c.Agent.DefaultCWD = "~"
	c.Gateway.ExposeBackends = "all"
	c.Gateway.Listen = "127.0.0.1:8080"
	c.Ingress.MaxPTYPerClient = 8
	c.Agent.Quota = Quota{DefaultCallsPerHour, DefaultMaxConcurrent, DefaultCallDuration.String()}
	c.Gateway.Quota = c.Agent.Quota
	c.Audit.Retention = "720h"
	if e = json.Unmarshal(raw, c); e != nil {
		return nil, e
	}
	if e = c.Validate(); e != nil {
		return nil, e
	}
	return c, nil
}
func (c *Config) Validate() error {
	fail := func(p string) error { return fmt.Errorf("配置无效: %s", p) }
	if c.Ingress.PTYBudgetFile != "" && !filepath.IsAbs(c.Ingress.PTYBudgetFile) {
		return fail("/ingress/pty_budget_file")
	}
	if c.Ingress.PTYBudgetAuthority != "" && !nodeNamePattern.MatchString(c.Ingress.PTYBudgetAuthority) {
		return fail("/ingress/pty_budget_authority")
	}
	if c.Gateway.Listen != "" {
		host, _, e := net.SplitHostPort(c.Gateway.Listen)
		if e != nil || host != "127.0.0.1" {
			return fail("/gateway/listen")
		}
	}
	if c.HasRole("ingress") {
		if c.Mesh.Mode == "lan" {
			return fail("/ingress: lan 模式不提供 OAuth 或 Funnel")
		}
		if c.Ingress.Listen == "funnel" {
			u, e := url.Parse(c.Mesh.ControlURL)
			if e != nil || !officialControlURL(u) {
				return fail("/mesh/control_url: Funnel 要求官方控制面")
			}
		}
		if e := c.validateIssuerOrigin(); e != nil {
			return e
		}
	}
	for _, q := range []struct {
		path  string
		quota Quota
	}{{"/agent/quota", c.Agent.Quota}, {"/gateway/quota", c.Gateway.Quota}} {
		if q.quota.CallsPerHour < 0 || q.quota.MaxConcurrent < 0 {
			return fail(q.path)
		}
		if q.quota.MaxCallDuration != "" {
			if d, e := time.ParseDuration(q.quota.MaxCallDuration); e != nil || d <= 0 {
				return fail(q.path + "/max_call_duration")
			}
		}
	}
	for name, b := range c.Agent.Backends {
		if b.Transport == "http" {
			u, e := url.Parse(b.URL)
			if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
				return fail("/agent/backends/" + name + "/url")
			}
			host := u.Hostname()
			ip, e := netip.ParseAddr(host)
			tailnet := netip.MustParsePrefix("100.64.0.0/10")
			v6 := netip.MustParsePrefix("fd7a:115c:a1e0::/48")
			if host != "localhost" && (e != nil || (!ip.IsLoopback() && !tailnet.Contains(ip) && !v6.Contains(ip))) {
				return fail("/agent/backends/" + name + "/url")
			}
		}
	}
	durations := map[string]string{"/agent/exec/default_timeout": c.Agent.Exec.DefaultTimeout, "/agent/pty/idle_timeout": c.Agent.PTY.IdleTimeout, "/agent/pty/orphan_grace": c.Agent.PTY.OrphanGrace, "/agent/pty/closed_retain": c.Agent.PTY.ClosedRetain, "/audit/retention": c.Audit.Retention}
	for name, b := range c.Agent.Backends {
		if b.IdleTimeout != "" {
			durations["/agent/backends/"+name+"/idle_timeout"] = b.IdleTimeout
		}
	}
	for p, s := range durations {
		v, e := time.ParseDuration(s)
		if e != nil || v < 0 {
			return fail(p)
		}
	}
	// The call budget's deadline would silently cut a longer default short.
	if _, _, limit := c.Agent.Quota.Limits(); Duration(c.Agent.Exec.DefaultTimeout) > limit {
		return fail("/agent/exec/default_timeout: 不得超过 agent.quota.max_call_duration")
	}
	for p, s := range map[string]string{"/agent/exec/inline_output_bytes": c.Agent.Exec.InlineOutputBytes, "/agent/pty/scrollback_bytes": c.Agent.PTY.ScrollbackBytes} {
		n, e := Size(s)
		if e != nil || n < 1 {
			return fail(p)
		}
	}
	for _, p := range c.Agent.Policy.Exec.DenyPatterns {
		if _, e := regexp.Compile(p); e != nil {
			return fail("/agent/policy/exec/deny_patterns")
		}
	}
	return nil
}
func Duration(s string) time.Duration { v, _ := time.ParseDuration(s); return v }
func Size(s string) (int, error) {
	re := regexp.MustCompile(`^([0-9]+)([KMGT]iB|B)?$`)
	m := re.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("尺寸无效")
	}
	n, e := strconv.ParseUint(m[1], 10, 64)
	if e != nil {
		return 0, e
	}
	shift := uint(0)
	if m[2] != "" && m[2] != "B" {
		shift = uint(strings.Index("KMGT", m[2][:1])+1) * 10
	}
	if n > uint64(math.MaxInt)>>shift {
		return 0, fmt.Errorf("尺寸溢出")
	}
	return int(n << shift), nil
}

// officialControlHosts are the control planes Funnel is allowed to talk to.
// Anything else, including look-alike suffixes and self-hosted control planes, is refused.
var officialControlHosts = map[string]bool{"controlplane.tailscale.com": true, "login.tailscale.com": true}

func officialControlURL(u *url.URL) bool {
	return u.Scheme == "https" && u.User == nil && officialControlHosts[strings.ToLower(u.Hostname())] && (u.Port() == "" || u.Port() == "443") && (u.Path == "" || u.Path == "/")
}

// IssuerHost returns the issuer's lower-case host without a default port.
func IssuerHost(issuer string) string {
	u, e := url.Parse(issuer)
	if e != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && port != "443" {
		return host + ":" + port
	}
	return host
}

// validateIssuerOrigin ties the OAuth issuer to the origin clients really use.
func (c *Config) validateIssuerOrigin() error {
	fail := func(p string) error { return fmt.Errorf("配置无效: %s", p) }
	issuer := c.Ingress.OAuth.Issuer
	if issuer == "" {
		return nil
	}
	host := IssuerHost(issuer)
	switch c.Ingress.Listen {
	case "public:443":
		if host != strings.ToLower(c.Ingress.Domain) {
			return fail("/ingress/oauth/issuer: 必须与 ingress.domain 一致")
		}
	case "funnel":
		if !strings.HasSuffix(host, ".ts.net") {
			return fail("/ingress/oauth/issuer: Funnel 的 issuer 必须是该节点的 ts.net 源")
		}
	}
	return nil
}
