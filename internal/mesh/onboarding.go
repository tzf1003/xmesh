package mesh

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/privdir"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// PrepareOnboarding opens the future service identity, authorises it, and checks
// the tags and Funnel capability before the service can acquire that state.
// Diagnostic logs stay local to tsnet; callers receive only login progress.
func PrepareOnboarding(ctx context.Context, c *config.Config, stateDir string, ingress bool, progress func(LoginProgress) error) (string, error) {
	if e := privdir.Ensure(stateDir); e != nil {
		return "", e
	}
	tags := tailnetRoleTags(ingress)
	server := &tsnet.Server{Dir: filepath.Join(stateDir, "tsnet"), Hostname: c.Node.Name, ControlURL: c.Mesh.ControlURL, AdvertiseTags: tags, UserLogf: func(string, ...any) {}, Logf: func(string, ...any) {}}
	if c.Mesh.AuthMethod != "browser" {
		key, e := os.ReadFile(c.Mesh.AuthkeyFile)
		if e != nil {
			return "", fmt.Errorf("无法读取入网密钥文件")
		}
		server.AuthKey = strings.TrimSpace(string(key))
	}
	lc, e := server.LocalClient()
	if e != nil {
		_ = server.Close()
		return "", e
	}
	defer server.Close()
	var last LoginProgress
	tick := time.NewTicker(400 * time.Millisecond)
	defer tick.Stop()
	for {
		st, e := lc.Status(ctx)
		if e != nil {
			return "", e
		}
		next := LoginProgress{State: "waiting"}
		if st.AuthURL != "" {
			next = LoginProgress{State: "needs_login", URL: st.AuthURL}
		}
		if st.BackendState == "NeedsMachineAuth" {
			next = LoginProgress{State: "needs_approval"}
		}
		if st.BackendState == "Running" && st.Self != nil && len(st.TailscaleIPs) > 0 {
			if err := onboardingPrerequisites(st, ingress); err != nil {
				return "", err
			}
			if e = progress(LoginProgress{State: "ready"}); e != nil {
				return "", e
			}
			return strings.TrimSuffix(st.Self.DNSName, "."), nil
		}
		if next != last {
			if e = progress(next); e != nil {
				return "", e
			}
			last = next
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-tick.C:
		}
	}
}

func onboardingPrerequisites(st *ipnstate.Status, ingress bool) error {
	if e := requireBrowserRole(st); e != nil {
		return e
	}
	if !ingress {
		return nil
	}
	if !slices.Contains(st.Self.Tags.AsSlice(), "tag:ingress") {
		return fmt.Errorf("请在控制面为此设备授予 tag:ingress 后重试")
	}
	if !st.Self.HasCap("funnel") {
		return fmt.Errorf("请在控制面策略中为此设备启用 Funnel，并开启 HTTPS 后重试")
	}
	if !strings.HasSuffix(strings.TrimSuffix(st.Self.DNSName, "."), ".ts.net") {
		return fmt.Errorf("设备尚未获得 Funnel 域名，请检查 MagicDNS 和 HTTPS 设置")
	}
	return nil
}
