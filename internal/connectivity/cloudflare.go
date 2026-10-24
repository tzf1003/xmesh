package connectivity

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/tzf1003/xmesh/internal/device"
)

// runCloudflare serves the tunnel's local end. cloudflared forwards tcp:// traffic to the loopback
// listener, so what arrives is plain TCP carrying the same pinned mTLS as the other paths. A device
// without a hostname only dials out and has nothing to serve.
func (n *Node) runCloudflare(ctx context.Context, p *provider) error {
	cfg := p.cfg.Cloudflare
	if cfg.Hostname == "" {
		n.mu.Lock()
		p.ready, p.err = true, ""
		n.mu.Unlock()
		<-ctx.Done()
		return ctx.Err()
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr())
	if err != nil {
		return err
	}
	if cfg.TunnelTokenFile != "" {
		binary, err := exec.LookPath(cmp.Or(cfg.Cloudflared, "cloudflared"))
		if err != nil {
			_ = listener.Close()
			return errors.New("找不到 cloudflared，请安装或设置 mesh.cloudflare.cloudflared")
		}
		go n.runCloudflared(ctx, binary, cfg.TunnelTokenFile)
	}
	return n.serve(ctx, device.Cloudflare, p, listener, func(ctx context.Context) { <-ctx.Done() })
}

// cloudflaredArgs forces HTTP/2 over TCP 443: QUIC over UDP is often blocked or throttled where devices
// reach Cloudflare from, and the token comes from a private file so it never appears in a process list.
func cloudflaredArgs(tokenFile string) []string {
	return []string{"tunnel", "--no-autoupdate", "--protocol", "http2", "run", "--token-file", tokenFile}
}

// runCloudflared keeps cloudflared running for the life of the provider, restarting it with a growing
// pause after each exit. Its output is dropped: it can echo the tunnel's connector details.
func (n *Node) runCloudflared(ctx context.Context, binary, tokenFile string) {
	pause := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		cmd := exec.CommandContext(ctx, binary, cloudflaredArgs(tokenFile)...)
		cmd.WaitDelay = 5 * time.Second
		_ = cmd.Run()
		if time.Since(started) > time.Minute {
			pause = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause):
		}
		pause = min(pause*2, time.Minute)
	}
}

// dialCloudflare opens a WebSocket to a peer's tunnel hostname, presents the Access service token, and
// runs the pinned mTLS handshake inside it. The WebSocket belongs to the returned connection, not to ctx.
func (n *Node) dialCloudflare(ctx context.Context, address string, tlsConfig *tls.Config) (net.Conn, error) {
	n.mu.Lock()
	cfg := n.cfg.Cloudflare
	n.mu.Unlock()
	if !cfg.Enabled {
		return nil, errors.New("Cloudflare 通道未启用")
	}
	header := http.Header{}
	if cfg.AccessClientID != "" {
		secret, err := os.ReadFile(cfg.AccessClientSecretFile)
		if err != nil {
			return nil, fmt.Errorf("无法读取 Cloudflare Access 服务令牌密钥")
		}
		header.Set("CF-Access-Client-Id", cfg.AccessClientID)
		header.Set("CF-Access-Client-Secret", strings.TrimSpace(string(secret)))
	}
	outer := n.cloudflareOuter
	if outer == nil {
		outer = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}, TLSHandshakeTimeout: 10 * time.Second}}
		defer outer.CloseIdleConnections()
	}
	ws, _, err := websocket.Dial(ctx, "wss://"+address, &websocket.DialOptions{HTTPClient: outer, HTTPHeader: header})
	if err != nil {
		return nil, err
	}
	raw := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	conn := tls.Client(raw, tlsConfig)
	if err := conn.HandshakeContext(ctx); err != nil {
		_ = raw.Close()
		return nil, err
	}
	return conn, nil
}
