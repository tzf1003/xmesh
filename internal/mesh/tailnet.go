package mesh

import (
	"context"
	"errors"
	"fmt"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
	"time"
)

type Tailnet struct {
	server    *tsnet.Server
	ephemeral bool
}

func OpenTailnet(ctx context.Context, c *config.Config, stateDir string) (*Tailnet, error) {
	return openTailnet(ctx, c, stateDir, nil)
}

func openTailnet(ctx context.Context, c *config.Config, stateDir string, logf func(string, ...any)) (*Tailnet, error) {
	var key []byte
	var e error
	if c.Mesh.AuthMethod != "browser" {
		key, e = os.ReadFile(c.Mesh.AuthkeyFile)
		if e != nil {
			return nil, fmt.Errorf("无法读取 mesh.authkey_file")
		}
		if strings.TrimSpace(string(key)) == "" {
			return nil, fmt.Errorf("mesh.authkey_file 为空")
		}
	} else if _, err := os.Stat(filepath.Join(stateDir, "tsnet", "tailscaled.state")); err != nil {
		return nil, fmt.Errorf("请先运行 xmesh login 完成浏览器授权")
	}
	if e = os.MkdirAll(stateDir, 0700); e != nil {
		return nil, e
	}
	if e = os.Chmod(stateDir, 0700); e != nil {
		return nil, e
	}
	redactedLog := func(string, ...any) {}
	if logf != nil {
		redactedLog = func(format string, args ...any) {
			message := fmt.Sprintf(format, args...)
			if secret := strings.TrimSpace(string(key)); secret != "" {
				message = strings.ReplaceAll(message, secret, "[REDACTED_SECRET]")
			}
			logf("%s", message)
		}
	}
	server := &tsnet.Server{Dir: filepath.Join(stateDir, "tsnet"), Hostname: c.Node.Name, ControlURL: c.Mesh.ControlURL, AuthKey: strings.TrimSpace(string(key)), UserLogf: redactedLog, Logf: redactedLog}
	if c.Mesh.AuthMethod == "browser" {
		server.AdvertiseTags = []string{"tag:device"}
	}
	if c.Mesh.Ephemeral {
		server.Ephemeral = true
		server.Store = new(mem.Store)
	}
	status, e := server.Up(ctx)
	if e != nil {
		_ = server.Close()
		return nil, fmt.Errorf("tailnet 入网失败: %w", e)
	}
	if c.Mesh.AuthMethod == "browser" {
		if err := requireBrowserRole(status); err != nil {
			_ = server.Close()
			return nil, err
		}
	}
	return &Tailnet{server: server, ephemeral: c.Mesh.Ephemeral}, nil
}
func (t *Tailnet) Listen(ctx context.Context) (net.Listener, error) {
	return t.server.Listen("tcp", ":"+strconv.Itoa(AgentPort))
}
func (t *Tailnet) Funnel() (net.Listener, error) { return t.server.ListenFunnel("tcp", ":443") }

// ListenPort serves on the tailnet address of this node at port: the Gateway's tailnet listener.
func (t *Tailnet) ListenPort(port int) (net.Listener, error) {
	return t.server.Listen("tcp", ":"+strconv.Itoa(port))
}

// Addresses returns this node's tailnet IPs and its MagicDNS name without the trailing dot. It is
// empty until the node has joined the tailnet.
func (t *Tailnet) Addresses(ctx context.Context) ([]netip.Addr, string, error) {
	lc, e := t.server.LocalClient()
	if e != nil {
		return nil, "", e
	}
	status, e := lc.Status(ctx)
	if e != nil {
		return nil, "", e
	}
	if status.Self == nil {
		return nil, "", fmt.Errorf("tailnet 身份尚未就绪")
	}
	return append([]netip.Addr{}, status.Self.TailscaleIPs...), strings.TrimSuffix(status.Self.DNSName, "."), nil
}
func (t *Tailnet) HTTPClient(_ Peer) *http.Client {
	client := t.server.HTTPClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client
}
func (t *Tailnet) Authenticate(r *http.Request) (identity.Principal, error) {
	lc, e := t.server.LocalClient()
	if e != nil {
		return identity.Principal{}, toolerr.New("unauthenticated", "无法获取网络身份")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	who, e := lc.WhoIs(ctx, r.RemoteAddr)
	if e != nil || who == nil || who.Node == nil {
		return identity.Principal{}, toolerr.New("unauthenticated", "WhoIs 无法识别对端")
	}
	return identity.FromPeer(string(who.Node.StableID), who.Node.Tags, r.Header.Get("X-Xmesh-Oauth-Client"))
}
func (t *Tailnet) Self(ctx context.Context) (identity.Principal, error) {
	lc, e := t.server.LocalClient()
	if e != nil {
		return identity.Principal{}, e
	}
	status, e := lc.Status(ctx)
	if e != nil {
		return identity.Principal{}, e
	}
	if status.Self == nil || status.Self.ID == "" {
		return identity.Principal{}, fmt.Errorf("tailnet 身份尚未就绪")
	}
	tags := []string{}
	if status.Self.Tags != nil {
		tags = status.Self.Tags.AsSlice()
	}
	return identity.Principal{Node: string(status.Self.ID), Tags: tags}, nil
}

// Online lists this node and every peer the control plane currently reports as
// online. The tailnet status covers all nodes, so the answer is authoritative.
func (t *Tailnet) Online(ctx context.Context) (map[string]bool, bool, error) {
	lc, e := t.server.LocalClient()
	if e != nil {
		return nil, false, e
	}
	status, e := lc.Status(ctx)
	if e != nil {
		return nil, false, e
	}
	online := map[string]bool{}
	if status.Self != nil {
		online[string(status.Self.ID)] = true
	}
	for _, peer := range status.Peer {
		if peer.Online && !peer.Expired {
			online[string(peer.ID)] = true
		}
	}
	return online, true, nil
}
func (t *Tailnet) Peers(ctx context.Context) ([]Peer, error) {
	lc, e := t.server.LocalClient()
	if e != nil {
		return nil, e
	}
	status, e := lc.Status(ctx)
	if e != nil {
		return nil, e
	}
	out := []Peer{}
	for _, peer := range status.Peer {
		tags := []string{}
		if peer.Tags != nil {
			tags = peer.Tags.AsSlice()
		}
		device := false
		for _, tag := range tags {
			if tag == "tag:device" {
				device = true
			}
		}
		if !device || !peer.Online || peer.Expired || len(peer.TailscaleIPs) == 0 {
			continue
		}
		name := peer.HostName
		if name == "" {
			name = strings.Split(peer.DNSName, ".")[0]
		}
		out = append(out, Peer{ID: string(peer.ID), Name: name, Address: "http://" + net.JoinHostPort(peer.TailscaleIPs[0].String(), strconv.Itoa(AgentPort)) + "/mcp", Tags: tags, Online: true, OS: peer.OS, Relay: peer.Relay, Direct: peer.CurAddr != ""})
	}
	return out, nil
}
func (t *Tailnet) Close() error {
	var logoutErr error
	if t.ephemeral {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		lc, err := t.server.LocalClient()
		if err == nil {
			err = lc.Logout(ctx)
		}
		if err != nil {
			logoutErr = fmt.Errorf("临时节点注销失败: %w", err)
		}
	}
	return errors.Join(logoutErr, t.server.Close())
}
