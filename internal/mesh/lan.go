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
	"net/url"
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
	presence      map[string]lanPeer // every paired node seen over mDNS, keyed by node id
	mdns          bool
	listener      net.Listener
	advertisement *zeroconf.Server
	cancel        context.CancelFunc
	done          chan struct{}
}
type lanPeer struct {
	peer Peer
	seen time.Time
	ttl  time.Duration
}

// LANOptions carries the mesh.lan configuration into the transport.
type LANOptions struct {
	// MDNS enables DNS-SD advertisement and discovery. When false the node neither
	// announces itself nor browses, so it can only be reached by a peer that
	// already knows its address.
	MDNS bool
}

func OpenLAN(ctx context.Context, name, stateDir string, options LANOptions) (*LAN, error) {
	cert, e := LoadCertificate(stateDir, name)
	if e != nil {
		return nil, e
	}
	trust, e := OpenTrust(stateDir)
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(ctx)
	l := &LAN{name: name, cert: cert, trust: trust, peers: map[string]lanPeer{}, presence: map[string]lanPeer{}, mdns: options.MDNS, cancel: cancel, done: make(chan struct{})}
	if !options.MDNS {
		close(l.done)
		return l, nil
	}
	if e = l.startDiscovery(ctx, 10*time.Second, func(ctx context.Context) (<-chan *zeroconf.ServiceEntry, error) {
		resolver, err := zeroconf.NewResolver(nil)
		if err != nil {
			return nil, err
		}
		entries := make(chan *zeroconf.ServiceEntry, 32)
		if err = resolver.Browse(ctx, lanService, "local.", entries); err != nil {
			return nil, err
		}
		return entries, nil
	}); e != nil {
		cancel()
		return nil, e
	}
	return l, nil
}

// zeroconf emits each service once per Browse. Fresh rounds renew liveness
// from new replies instead of treating the resolver's cached entry as a heartbeat.
func (l *LAN) startDiscovery(ctx context.Context, period time.Duration, browse func(context.Context) (<-chan *zeroconf.ServiceEntry, error)) error {
	round, cancel := context.WithTimeout(ctx, period)
	entries, err := browse(round)
	if err != nil {
		cancel()
		return err
	}
	go func() {
		defer close(l.done)
		for {
			l.discover(round, entries)
			<-round.Done()
			cancel()
			if entries != nil {
				for range entries {
				}
			}
			if ctx.Err() != nil {
				return
			}
			round, cancel = context.WithTimeout(ctx, period)
			entries, _ = browse(round)
		}
	}()
	return nil
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
			id := "lan-" + fp[:24]
			ttl := time.Duration(ent.TTL) * time.Second
			l.mu.Lock()
			if l.presence == nil {
				l.presence = map[string]lanPeer{}
			}
			if ttl <= 0 {
				// mDNS goodbye: the node is leaving, whatever it advertised before.
				delete(l.peers, trusted.Name)
				delete(l.presence, id)
				l.mu.Unlock()
				continue
			}
			l.presence[id] = lanPeer{seen: time.Now(), ttl: min(ttl, 30*time.Second)}
			l.mu.Unlock()
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
			l.mu.Lock()
			l.peers[trusted.Name] = lanPeer{Peer{ID: id, Name: trusted.Name, Address: "https://" + address + "/mcp", Tags: trusted.Tags, Online: true, Direct: true, Fingerprint: fp}, time.Now(), min(ttl, 30*time.Second)}
			l.mu.Unlock()
		}
	}
}

// Online reports the paired nodes whose mDNS announcements are still fresh, plus
// this node. It is not authoritative: a paired client that never advertises is
// simply absent, so callers must only act on nodes they have seen here before.
func (l *LAN) Online(context.Context) (map[string]bool, bool, error) {
	online := map[string]bool{"lan-" + l.cert.Fingerprint[:24]: true}
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, p := range l.presence {
		if time.Since(p.seen) > p.ttl {
			delete(l.presence, id)
			continue
		}
		online[id] = true
	}
	return online, false, nil
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
	if !l.mdns {
		return l.listener, nil
	}
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
func (l *LAN) HTTPClient(peer Peer) *http.Client {
	endpoint, err := url.Parse(peer.Address)
	trusted, ok := l.trust.Find(peer.Fingerprint)
	valid := err == nil && endpoint.Scheme == "https" && ok && peer.Name == trusted.Name && peer.ID == "lan-"+peer.Fingerprint[:24]
	transport := &http.Transport{TLSClientConfig: l.trust.TLS(l.cert, peer.Fingerprint, false), TLSHandshakeTimeout: 10 * time.Second, IdleConnTimeout: 30 * time.Second}
	return &http.Client{Transport: lanTransport{base: transport, endpoint: endpoint, valid: valid}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type lanTransport struct {
	base     http.RoundTripper
	endpoint *url.URL
	valid    bool
}

func (t lanTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !t.valid || r.URL.Scheme != "https" || r.URL.Host != t.endpoint.Host {
		return nil, fmt.Errorf("LAN 请求与已配对的目标不一致")
	}
	return t.base.RoundTrip(r)
}
func (t lanTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
func (l *LAN) Close() error {
	l.cancel()
	<-l.done
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
		if tag != "tag:peer" && tag != "tag:device" && tag != "tag:ingress" && tag != "tag:admin" {
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
