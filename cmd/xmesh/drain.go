package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// drainNetwork is the part of a mesh connection the drain command needs.
type drainNetwork interface {
	Peers(context.Context) ([]mesh.Peer, error)
	HTTPClient(mesh.Peer) *http.Client
}

// openDrainNetwork joins the mesh as the administrator. It is a variable so
// tests can substitute a network without a tailnet or mDNS.
var openDrainNetwork = openAdminNetwork

// drain implements `xmesh drain`: it sends the control message of CTR-GW-05 to
// one device, or to every online device when the scope is the whole mesh.
func drain(args []string) error {
	flags := flag.NewFlagSet("drain", flag.ContinueOnError)
	path := flags.String("config", "xmesh.yaml", "配置路径（取 mesh 模式与 control_url）")
	scope := flags.String("scope", "", "device 或 mesh")
	device := flags.String("device", "", "scope=device 时的节点名")
	mode := flags.String("mode", "keep", "keep 保留现有会话到期；kill 终止本机 PTY 并释放租约")
	authkey := flags.String("authkey-file", "", "tailnet：带 tag:admin 的 auth key 文件")
	state := flags.String("state-dir", "", "lan：本机配对状态目录")
	discover := flags.Duration("discover", 8*time.Second, "lan：等待发现设备的时间")
	timeout := flags.Duration("timeout", 15*time.Second, "每台设备的请求超时")
	if e := flags.Parse(args); e != nil {
		return e
	}
	request, e := drainRequest(*scope, *device, *mode)
	if e != nil {
		return e
	}
	cfg, e := config.Load(*path)
	if e != nil {
		return e
	}
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Minute+*discover)
	defer stop()
	network, closeNetwork, e := openDrainNetwork(ctx, cfg, *authkey, *state, *discover)
	if e != nil {
		return e
	}
	defer closeNetwork()
	return runDrain(ctx, os.Stdout, network, request, *timeout)
}

func drainRequest(scope, device, mode string) (agent.DrainRequest, error) {
	switch scope {
	case "device":
		if device == "" {
			return agent.DrainRequest{}, fmt.Errorf("--scope device 需要 --device")
		}
	case "mesh":
		if device != "" {
			return agent.DrainRequest{}, fmt.Errorf("--scope mesh 不接受 --device")
		}
	default:
		return agent.DrainRequest{}, fmt.Errorf("用法: xmesh drain --scope device|mesh [--device NAME] [--mode keep|kill]")
	}
	if mode != "keep" && mode != "kill" {
		return agent.DrainRequest{}, fmt.Errorf("--mode 只能是 keep 或 kill")
	}
	return agent.DrainRequest{Scope: scope, Device: device, Drain: mode}, nil
}

// runDrain resolves the target devices and sends the request to each. Every
// device's outcome is printed; any failure makes the command fail so a script
// never mistakes a partial drain for a complete one.
func runDrain(ctx context.Context, out io.Writer, network drainNetwork, request agent.DrainRequest, perDevice time.Duration) error {
	peers, e := network.Peers(ctx)
	if e != nil {
		return fmt.Errorf("无法读取在线设备: %w", e)
	}
	targets := []mesh.Peer{}
	for _, peer := range peers {
		if isDevice(peer) && (request.Scope == "mesh" || peer.Name == request.Device) {
			targets = append(targets, peer)
		}
	}
	if len(targets) == 0 {
		if request.Scope == "device" {
			return fmt.Errorf("设备不在线或不是 tag:device: %s", request.Device)
		}
		return fmt.Errorf("没有在线的 tag:device 设备")
	}
	failed := 0
	for _, outcome := range agent.FanoutDrain(ctx, targets, network.HTTPClient, request, perDevice) {
		if outcome.Err != nil {
			failed++
			fmt.Fprintf(out, "失败 %s: %v\n", outcome.Device, outcome.Err)
		} else {
			fmt.Fprintf(out, "已下线 %s (scope=%s, mode=%s)\n", outcome.Device, request.Scope, request.Drain)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d/%d 台设备未能下线", failed, len(targets))
	}
	return nil
}

// isDevice keeps LAN peers that were paired as devices; tailnet discovery
// already filters on the tag.
func isDevice(peer mesh.Peer) bool {
	for _, tag := range peer.Tags {
		if tag == "tag:device" {
			return true
		}
	}
	return false
}

// openAdminNetwork connects as an administrator. In tailnet mode it joins as a
// short-lived node using an auth key tagged tag:admin; in LAN mode it presents
// this machine's paired certificate, which peers must have imported with
// tag:admin (`xmesh pair --import ... --tags tag:admin`).
func openAdminNetwork(ctx context.Context, cfg *config.Config, authkey, state string, discover time.Duration) (drainNetwork, func(), error) {
	switch cfg.Mesh.Mode {
	case "tailnet":
		if authkey == "" {
			return nil, nil, fmt.Errorf("tailnet 模式需要 --authkey-file（带 tag:admin 的 auth key）")
		}
		dir, e := os.MkdirTemp("", "xmesh-drain-")
		if e != nil {
			return nil, nil, e
		}
		suffix := make([]byte, 3)
		_, _ = rand.Read(suffix)
		admin := *cfg
		admin.Node.Name = "xmesh-admin-" + hex.EncodeToString(suffix)
		admin.Mesh.AuthkeyFile = authkey
		admin.Mesh.Ephemeral = true
		tailnet, e := mesh.OpenTailnet(ctx, &admin, dir)
		if e != nil {
			_ = os.RemoveAll(dir)
			return nil, nil, e
		}
		return tailnet, func() { _ = tailnet.Close(); _ = os.RemoveAll(dir) }, nil
	case "lan":
		dir := state
		if dir == "" {
			var e error
			if dir, e = app.StateDir(cfg.Node.Name); e != nil {
				return nil, nil, e
			}
		}
		lan, e := mesh.OpenLAN(ctx, cfg.Node.Name, filepath.Join(dir, "lan"))
		if e != nil {
			return nil, nil, e
		}
		deadline := time.Now().Add(discover)
		for time.Now().Before(deadline) {
			if peers, _ := lan.Peers(ctx); len(peers) > 0 {
				break
			}
			select {
			case <-ctx.Done():
			case <-time.After(250 * time.Millisecond):
			}
		}
		return lan, func() { _ = lan.Close() }, nil
	}
	return nil, nil, fmt.Errorf("drain 需要 tailnet 或 lan 模式")
}
