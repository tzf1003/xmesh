// Package adengine pins, verifies, stages and health-checks the agent-desktop
// CLI that the agent-desktop computer engine drives. The manifest is the only
// source of trust: an executable is used only when its SHA-256 matches a value
// recorded here, and a release is used only when it is listed as validated.
package adengine

import (
	"crypto/sha256"
	"crypto/sha512"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
)

// manifest.json pins one upstream release. The upstream CI job updates the
// version, tarball, platforms, validation.record and upstream.latest fields;
// people maintain validated_versions and validated_platforms. The schema is
// documented in docs/contracts/agent-desktop-manifest.md.
//
//go:embed manifest.json
var embeddedManifest []byte

// Engine names as used by the engine file, Info and the status output.
const (
	EngineNative       = "native"
	EngineAgentDesktop = "agent-desktop"
)

// Auto-update policy values for agent.computer.engine.auto_update.
const (
	ModeOff   = "off"
	ModeCheck = "check"
	ModeApply = "apply"
)

// knownPlatforms are the npm-style keys the manifest may use.
var knownPlatforms = []string{"darwin-arm64", "darwin-x64", "linux-arm64", "linux-x64", "win32-arm64", "win32-x64"}

// Manifest is schema version 1.
type Manifest struct {
	SchemaVersion int                 `json:"schema_version"`
	Package       string              `json:"package"`
	Version       string              `json:"version"`
	License       string              `json:"license"`
	Tarball       Tarball             `json:"tarball"`
	Platforms     map[string]Platform `json:"platforms"`
	Validation    Validation          `json:"validation"`
	Upstream      *Upstream           `json:"upstream,omitempty"`
}

// Tarball locates the upstream npm package the artifacts are extracted from.
type Tarball struct {
	URL         string `json:"url"`
	Integrity   string `json:"integrity"`
	PublishedAt string `json:"published_at,omitempty"`
}

// Platform lists the executable and helpers a platform installs.
type Platform struct {
	Binary  File   `json:"binary"`
	Helpers []File `json:"helpers"`
}

// File is one tarball member and the SHA-256 of its contents.
type File struct {
	Member string `json:"member"`
	SHA256 string `json:"sha256"`
}

// Validation records which releases were accepted, and where.
type Validation struct {
	ValidatedVersions  []string `json:"validated_versions"`
	ValidatedPlatforms []string `json:"validated_platforms,omitempty"`
	Record             string   `json:"record,omitempty"`
}

// Upstream is the newest upstream release the CI job has seen, if any.
type Upstream struct {
	Latest string `json:"latest,omitempty"`
}

// Embedded returns the manifest compiled into this build.
func Embedded() (*Manifest, error) { return Parse(embeddedManifest) }

// Parse decodes and validates a manifest. Unknown fields are ignored so that a
// manifest written by a newer CI job still loads.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("agent-desktop 清单解析失败: %w", err)
	}
	if err := m.validate(); err != nil {
		return nil, fmt.Errorf("agent-desktop 清单无效: %w", err)
	}
	return &m, nil
}

func (m *Manifest) validate() error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("schema_version 必须为 1，实际为 %d", m.SchemaVersion)
	}
	if m.Package != "agent-desktop" {
		return fmt.Errorf("package 必须为 agent-desktop，实际为 %q", m.Package)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return fmt.Errorf("version: %w", err)
	}
	if u, err := url.Parse(m.Tarball.URL); err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("tarball.url 必须是 https 地址")
	}
	if _, err := decodeIntegrity(m.Tarball.Integrity); err != nil {
		return fmt.Errorf("tarball.integrity: %w", err)
	}
	if len(m.Platforms) == 0 {
		return errors.New("platforms 不能为空")
	}
	for key, p := range m.Platforms {
		if !slices.Contains(knownPlatforms, key) {
			return fmt.Errorf("未知平台键 %q", key)
		}
		if err := p.Binary.validate(); err != nil {
			return fmt.Errorf("%s binary: %w", key, err)
		}
		for _, h := range p.Helpers {
			if err := h.validate(); err != nil {
				return fmt.Errorf("%s helper: %w", key, err)
			}
		}
	}
	for _, v := range m.Validation.ValidatedVersions {
		if _, err := ParseVersion(v); err != nil {
			return fmt.Errorf("validated_versions: %w", err)
		}
	}
	for _, key := range m.Validation.ValidatedPlatforms {
		if !slices.Contains(knownPlatforms, key) {
			return fmt.Errorf("validated_platforms 含未知平台键 %q", key)
		}
	}
	if m.Upstream != nil && m.Upstream.Latest != "" {
		if _, err := ParseVersion(m.Upstream.Latest); err != nil {
			return fmt.Errorf("upstream.latest: %w", err)
		}
	}
	return nil
}

func (f File) validate() error {
	if !safeMember(f.Member) {
		return fmt.Errorf("member %q 不是安全的相对路径", f.Member)
	}
	_, err := decodeSHA256(f.SHA256)
	return err
}

// safeMember accepts only clean, relative, forward-slash paths.
func safeMember(name string) bool {
	if name == "" || strings.Contains(name, `\`) || path.IsAbs(name) {
		return false
	}
	return path.Clean(name) == name && name != ".." && !strings.HasPrefix(name, "../")
}

func decodeSHA256(s string) ([]byte, error) {
	b, err := hex.DecodeString(strings.ToLower(s))
	if err != nil || len(b) != sha256.Size {
		return nil, fmt.Errorf("sha256 %q 不是 64 位十六进制", s)
	}
	return b, nil
}

func decodeIntegrity(s string) ([]byte, error) {
	enc, ok := strings.CutPrefix(s, "sha512-")
	if !ok {
		return nil, fmt.Errorf("%q 不是 sha512- 开头的 SRI", s)
	}
	b, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(b) != sha512.Size {
		return nil, fmt.Errorf("%q 解码失败", s)
	}
	return b, nil
}

// Platform returns the artifacts for one platform key.
func (m *Manifest) Platform(key string) (Platform, bool) {
	p, ok := m.Platforms[key]
	return p, ok
}

// Validated reports whether version is accepted on the platform. Without
// validated_platforms, every platform that has artifacts counts.
func (m *Manifest) Validated(version, key string) bool {
	if _, ok := m.Platforms[key]; !ok {
		return false
	}
	if !slices.Contains(m.Validation.ValidatedVersions, version) {
		return false
	}
	return len(m.Validation.ValidatedPlatforms) == 0 || slices.Contains(m.Validation.ValidatedPlatforms, key)
}

// UpstreamLatest returns the newest upstream release the CI job has recorded.
func (m *Manifest) UpstreamLatest() string {
	if m.Upstream == nil {
		return ""
	}
	return m.Upstream.Latest
}

// PlatformKey maps GOOS and GOARCH to the npm-style key the manifest uses.
func PlatformKey(goos, goarch string) string {
	if goos == "windows" {
		goos = "win32"
	}
	if goarch == "amd64" {
		goarch = "x64"
	}
	return goos + "-" + goarch
}

// InstallName is the file name a member is installed under beside the CLI: the
// upstream file name without its platform suffix. Helpers keep the names the
// CLI looks for, such as agent-desktop-macos-helper.
func InstallName(member, key string) string {
	base := path.Base(member)
	ext := ""
	if strings.HasSuffix(base, ".exe") {
		ext, base = ".exe", strings.TrimSuffix(base, ".exe")
	}
	return strings.TrimSuffix(base, "-"+key) + ext
}

// Version is a strict MAJOR.MINOR.PATCH release number.
type Version struct{ Major, Minor, Patch int }

// ParseVersion accepts exactly three decimal components.
func ParseVersion(s string) (Version, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("版本号 %q 不是 X.Y.Z", s)
	}
	var n [3]int
	for i, p := range parts {
		v, err := strconv.Atoi(p)
		if err != nil || v < 0 || strconv.Itoa(v) != p {
			return Version{}, fmt.Errorf("版本号 %q 不是 X.Y.Z", s)
		}
		n[i] = v
	}
	return Version{Major: n[0], Minor: n[1], Patch: n[2]}, nil
}

// Compare returns -1, 0 or 1.
func (v Version) Compare(o Version) int {
	for _, p := range [][2]int{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if p[0] != p[1] {
			if p[0] < p[1] {
				return -1
			}
			return 1
		}
	}
	return 0
}
