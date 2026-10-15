package mesh

import (
	"context"
	"fmt"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"tailscale.com/tsnet"
	"time"
)

type Tailnet struct{ server *tsnet.Server }

func OpenTailnet(ctx context.Context, c *config.Config, stateDir string) (*Tailnet, error) {
	key, e := os.ReadFile(c.Mesh.AuthkeyFile)
	if e != nil {
		return nil, fmt.Errorf("无法读取 mesh.authkey_file")
	}
	if strings.TrimSpace(string(key)) == "" {
		return nil, fmt.Errorf("mesh.authkey_file 为空")
	}
	if e = os.MkdirAll(stateDir, 0700); e != nil {
		return nil, e
	}
	if e = os.Chmod(stateDir, 0700); e != nil {
		return nil, e
	}
	server := &tsnet.Server{Dir: filepath.Join(stateDir, "tsnet"), Hostname: c.Node.Name, ControlURL: c.Mesh.ControlURL, AuthKey: strings.TrimSpace(string(key)), UserLogf: func(string, ...any) {}, Logf: func(string, ...any) {}}
	if _, e = server.Up(ctx); e != nil {
		_ = server.Close()
		return nil, fmt.Errorf("tailnet 入网失败: %w", e)
	}
	return &Tailnet{server}, nil
}
func (t *Tailnet) Listen(ctx context.Context) (net.Listener, error) {
	return t.server.Listen("tcp", ":"+strconv.Itoa(AgentPort))
}
func (t *Tailnet) Funnel() (net.Listener, error) { return t.server.ListenFunnel("tcp", ":443") }
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
func (t *Tailnet) Close() error { return t.server.Close() }
