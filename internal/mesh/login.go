package mesh

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/filelock"
	"github.com/tzf1003/xmesh/internal/privdir"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tsnet"
)

// LoginProgress contains just the browser challenge and connection state.
// Node keys and backend diagnostic logs never travel through this channel.
type LoginProgress struct {
	State string `json:"state"`
	URL   string `json:"url,omitempty"`
}

// LoginTailnet authorises the same persistent tsnet identity that serve uses.
// It closes the temporary network before the service opens that state directory.
func LoginTailnet(ctx context.Context, c *config.Config, stateDir string, progress func(LoginProgress) error) error {
	meshConfig := c.Mesh
	if c.SchemaVersion == 2 {
		meshConfig.Mode = "tailnet"
		meshConfig.AuthMethod = c.Mesh.Tailnet.AuthMethod
		meshConfig.Ephemeral = c.Mesh.Tailnet.Ephemeral
		meshConfig.ControlURL = c.Mesh.Tailnet.ControlURL
		if !c.Mesh.Tailnet.Enabled {
			return fmt.Errorf("请先启用 Tailnet 连接路径")
		}
	}
	if meshConfig.Mode != "tailnet" || meshConfig.AuthMethod != "browser" || meshConfig.Ephemeral {
		return fmt.Errorf("浏览器登录需要 tailnet 的 auth_method: browser 持久节点")
	}
	if c.SchemaVersion == 2 {
		if err := privdir.Ensure(filepath.Dir(stateDir)); err != nil {
			return err
		}
		lockCtx, stop := context.WithTimeout(ctx, 250*time.Millisecond)
		lock, err := filelock.Acquire(lockCtx, filepath.Join(filepath.Dir(stateDir), "device-runtime.lock"))
		stop()
		if err != nil {
			return fmt.Errorf("节点正在使用网络状态，请先停止节点再登录")
		}
		defer lock.Close()
	}
	if err := privdir.Ensure(stateDir); err != nil {
		return err
	}
	server := &tsnet.Server{
		Dir: filepath.Join(stateDir, "tsnet"), Hostname: c.Node.Name,
		ControlURL: meshConfig.ControlURL,
		UserLogf:   func(string, ...any) {}, Logf: func(string, ...any) {},
	}
	lc, err := server.LocalClient()
	if err != nil {
		_ = server.Close()
		return fmt.Errorf("无法启动浏览器授权: %w", err)
	}
	defer server.Close()
	return waitTailnetState(ctx, lc.Status, progress, c.SchemaVersion != 2)
}

func waitTailnetLogin(ctx context.Context, status func(context.Context) (*ipnstate.Status, error), progress func(LoginProgress) error) error {
	return waitTailnetState(ctx, status, progress, true)
}

func waitTailnetState(ctx context.Context, status func(context.Context) (*ipnstate.Status, error), progress func(LoginProgress) error, requireDeviceTag bool) error {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	last := LoginProgress{}
	for {
		st, err := status(ctx)
		if err != nil {
			return fmt.Errorf("读取入网授权状态失败: %w", err)
		}
		next := LoginProgress{State: "waiting"}
		if st.AuthURL != "" {
			next = LoginProgress{State: "needs_login", URL: st.AuthURL}
		}
		if st.BackendState == "NeedsMachineAuth" {
			next = LoginProgress{State: "needs_approval"}
		}
		if st.BackendState == "Running" && st.Self != nil && len(st.TailscaleIPs) > 0 {
			if err := requireBrowserRole(st); requireDeviceTag && err != nil {
				return err
			}
			next = LoginProgress{State: "ready"}
		}
		if next != last {
			if err := progress(next); err != nil {
				return err
			}
			last = next
		}
		if next.State == "ready" {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("入网授权已取消或超时，请重试；若设备等待审批，请在控制面批准: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

// requireBrowserRole checks the control plane's granted tags for both initial
// authorization and service startup using an existing identity.
func requireBrowserRole(st *ipnstate.Status) error {
	if st == nil || st.Self == nil || st.Self.Tags == nil || !slices.Contains(st.Self.Tags.AsSlice(), "tag:device") {
		return fmt.Errorf("设备已登录，但尚未获得 tag:device。请在控制面允许当前用户使用该标签后重试")
	}
	return nil
}
