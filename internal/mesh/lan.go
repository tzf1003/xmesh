package mesh

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/grandcat/zeroconf"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const lanService = "_xmesh._tcp"

type LAN struct {
	name          string
	cert          Certificate
	trust         *TrustStore
	mu            sync.Mutex
	peers         map[string]lanPeer
	listener      net.Listener
	advertisement *zeroconf.Server
	cancel        context.CancelFunc
}
type lanPeer struct {
	peer Peer
	seen time.Time
	ttl  time.Duration
}

func OpenLAN(ctx context.Context, name, stateDir string) (*LAN, error) {
	cert, e := LoadCertificate(stateDir, name)
	if e != nil {
		return nil, e
	}
	trust, e := OpenTrust(stateDir)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(ctx)
	l := &LAN{name: name, cert: cert, trust: trust, peers: map[string]lanPeer{}, cancel: cancel}
	resolver, e := zeroconf.NewResolver(nil)
	if e != nil {
		cancel()
		return nil, e
	}
	entries := make(chan *zeroconf.ServiceEntry, 32)
	if e = resolver.Browse(ctx, lanService, "local.", entries); e != nil {
		cancel()
		return nil, e
	}
	go l.discover(ctx, entries)
	return l, nil
}
func (l *LAN) discover(ctx context.Context, entries <-chan *zeroconf.ServiceEntry) {
	for {
		select {
		case <-ctx.Done():
			return
		case ent, ok := <-entries:
			if !ok {
				return
			}
			txt := map[string]string{}
			for _, v := range ent.Text {
				k, v, _ := strings.Cut(v, "=")
				txt[k] = v
			}
			fp := txt["fingerprint"]
			trusted, ok := l.trust.Find(fp)
			if !ok || trusted.Name == l.name {
				continue
			}
			device := false
			for _, tag := range trusted.Tags {
				if tag == "tag:device" {
					device = true
				}
			}
			if !device {
				continue
			}
			address := ""
			for _, ip := range append(ent.AddrIPv4, ent.AddrIPv6...) {
				if ip.IsPrivate() || ip.IsLoopback() {
					address = net.JoinHostPort(ip.String(), strconv.Itoa(ent.Port))
					break
				}
			}
			if address == "" {
				continue
			}
			ttl := time.Duration(ent.TTL) * time.Second
			if ttl <= 0 {
				l.mu.Lock()
				delete(l.peers, trusted.Name)
				l.mu.Unlock()
				continue
			}
			l.mu.Lock()
			l.peers[trusted.Name] = lanPeer{Peer{ID: "lan-" + fp[:24], Name: trusted.Name, Address: "https://" + address + "/mcp", Tags: trusted.Tags, Online: true, Direct: true}, time.Now(), min(ttl, 30*time.Second)}
			l.mu.Unlock()
		}
	}
}
func (l *LAN) Listen(ctx context.Context) (net.Listener, error) {
	interfaces, e := net.Interfaces()
	if e != nil {
		return nil, e
	}
	var ip net.IP
	var selected *net.Interface
	for i := range interfaces {
		iface := &interfaces[i]
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, _ := iface.Addrs()
		for _, address := range addresses {
			candidate, _, _ := net.ParseCIDR(address.String())
			if candidate.To4() != nil && candidate.IsPrivate() {
				ip = candidate
				selected = iface
				break
			}
		}
		if ip != nil {
			break
		}
	}
	if ip == nil {
		return nil, fmt.Errorf("LAN 模式需要私有网络接口")
	}
	raw, e := net.Listen("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(AgentPort)))
	if e != nil {
		return nil, e
	}
	l.listener = tls.NewListener(raw, l.trust.TLS(l.cert, "", true))
	host := l.name + ".local."
	advertisement, e := zeroconf.RegisterProxy(l.name, lanService, "local.", AgentPort, host, []string{ip.String()}, []string{"fingerprint=" + l.cert.Fingerprint}, []net.Interface{*selected})
	if e != nil {
		raw.Close()
		return nil, e
	}
	l.advertisement = advertisement
	return l.listener, nil
}
func (l *LAN) Peers(context.Context) ([]Peer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := []Peer{}
	for name, p := range l.peers {
		if time.Since(p.seen) > p.ttl {
			delete(l.peers, name)
			continue
		}
		out = append(out, p.peer)
	}
	return out, nil
}
func (l *LAN) Authenticate(r *http.Request) (identity.Principal, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
		return identity.Principal{}, toolerr.New("unauthenticated", "需要已配对的 mTLS 身份")
	}
	fp := fingerprint(r.TLS.PeerCertificates[0])
	p, ok := l.trust.Find(fp)
	if !ok {
		return identity.Principal{}, toolerr.New("unauthenticated", "对端尚未配对")
	}
	return identity.FromPeer("lan-"+fp[:24], p.Tags, r.Header.Get("X-Xmesh-Oauth-Client"))
}
func (l *LAN) Self(context.Context) (identity.Principal, error) {
	return identity.Principal{Node: "lan-" + l.cert.Fingerprint[:24], Tags: []string{"tag:peer"}}, nil
}
func (l *LAN) HTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{TLSClientConfig: l.trust.TLS(l.cert, "", false)}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (l *LAN) Close() error {
	l.cancel()
	if l.advertisement != nil {
		l.advertisement.Shutdown()
	}
	if l.listener != nil {
		return l.listener.Close()
	}
	return nil
}
func ExportPair(stateDir, name, path string) (string, error) {
	cert, e := LoadCertificate(stateDir, name)
	if e != nil {
		return "", e
	}
	data, e := json.MarshalIndent(Trusted{Name: name, Fingerprint: cert.Fingerprint, CertificatePEM: string(cert.PEM)}, "", "  ")
	if e != nil {
		return "", e
	}
	if e = os.WriteFile(path, data, 0600); e != nil {
		return "", e
	}
	return cert.Fingerprint, nil
}
func ImportPair(stateDir, path, expected string, tags []string) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var p Trusted
	if e = json.Unmarshal(b, &p); e != nil {
		return e
	}
	if expected == "" || p.Fingerprint != strings.ToLower(strings.ReplaceAll(expected, ":", "")) {
		return fmt.Errorf("配对指纹与 --fingerprint 不一致")
	}
	for _, tag := range tags {
		if tag != "tag:peer" && tag != "tag:device" && tag != "tag:ingress" {
			return fmt.Errorf("不支持的配对角色")
		}
	}
	p.Tags = tags
	if e = os.MkdirAll(stateDir, 0700); e != nil {
		return e
	}
	store, e := OpenTrust(filepath.Clean(stateDir))
	if e != nil {
		return e
	}
	return store.Add(p)
}
