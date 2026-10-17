package mesh

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mdns"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
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
	iface         string
	listener      net.Listener
	advertisement *mdns.Responder
	cancel        context.CancelFunc
	done          chan struct{}
	changed       chan struct{}
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
	// Interface names the network interface to listen and advertise on. Empty picks the first
	// up interface with a private IPv4 address that does not look virtual (VM host-only
	// adapters, container bridges, WSL, Hyper-V switches, VPN tunnels); virtual ones are used only
	// when nothing else qualifies.
	Interface string
}

// lanCandidate is what pickLANInterface needs to know about one interface.
type lanCandidate struct {
	Name     string
	Up       bool
	Loopback bool
	IPs      []net.IP
	index    int
}

// virtualNameParts and virtualNamePrefixes recognise adapters that exist for VMs, containers and
// tunnels, which are the wrong place to announce a LAN node on a machine that also has a real NIC.
var (
	virtualNameParts    = strings.Fields("vmnet vmware virtualbox vboxnet vethernet hyper-v docker wsl tailscale zerotier mihomo clash podman virbr flannel")
	virtualNamePrefixes = []string{"veth", "br-", "tun", "tap", "utun", "cni"}
)

func looksVirtual(name string) bool {
	lower := strings.ToLower(name)
	for _, part := range virtualNameParts {
		if strings.Contains(lower, part) {
			return true
		}
	}
	for _, prefix := range virtualNamePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}

// pickLANInterface chooses the interface and its first private IPv4 address. A configured name
// must exist, be up and have such an address, otherwise it is an error rather than a silent
// fallback to another network.
func pickLANInterface(candidates []lanCandidate, want string) (lanCandidate, net.IP, error) {
	private := func(c lanCandidate) net.IP {
		if !c.Up || c.Loopback {
			return nil
		}
		for _, ip := range c.IPs {
			if ip.To4() != nil && ip.IsPrivate() {
				return ip
			}
		}
		return nil
	}
	if want != "" {
		for _, c := range candidates {
			if c.Name != want {
				continue
			}
			if ip := private(c); ip != nil {
				return c, ip, nil
			}
			return lanCandidate{}, nil, fmt.Errorf("mesh.lan.interface %q 没有启用或没有私有 IPv4 地址", want)
		}
		return lanCandidate{}, nil, fmt.Errorf("mesh.lan.interface %q 不存在", want)
	}
	var fallback *lanCandidate
	var fallbackIP net.IP
	for i := range candidates {
		ip := private(candidates[i])
		if ip == nil {
			continue
		}
		if !looksVirtual(candidates[i].Name) {
			return candidates[i], ip, nil
		}
		if fallback == nil {
			fallback, fallbackIP = &candidates[i], ip
		}
	}
	if fallback == nil {
		return lanCandidate{}, nil, fmt.Errorf("LAN 模式需要私有网络接口")
	}
	return *fallback, fallbackIP, nil
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
	l := &LAN{name: name, cert: cert, trust: trust, peers: map[string]lanPeer{}, presence: map[string]lanPeer{}, mdns: options.MDNS, iface: options.Interface, cancel: cancel, done: make(chan struct{}), changed: make(chan struct{}, 1)}
	if !options.MDNS {
		close(l.done)
		return l, nil
	}
	iface, ip, e := selectLANInterface(options.Interface)
	if e != nil {
		cancel()
		return nil, e
	}
	slog.Info("lan_mdns_browse", "interface", iface.Name, "address", ip.String())
	if e = l.startDiscovery(ctx, 10*time.Second, func(ctx context.Context) (<-chan *mdns.Entry, error) {
		return mdns.Browse(ctx, lanService, "local", iface, ip)
	}); e != nil {
		cancel()
		return nil, e
	}
	return l, nil
}

// lanCandidates lists the interfaces of this host in the form pickLANInterface chooses from; the
// candidate's index points into the returned interface list.
func lanCandidates() ([]net.Interface, []lanCandidate, error) {
	interfaces, e := net.Interfaces()
	if e != nil {
		return nil, nil, e
	}
	candidates := make([]lanCandidate, 0, len(interfaces))
	for i := range interfaces {
		iface := &interfaces[i]
		addresses, _ := iface.Addrs()
		candidate := lanCandidate{Name: iface.Name, Up: iface.Flags&net.FlagUp != 0, Loopback: iface.Flags&net.FlagLoopback != 0, index: i}
		for _, address := range addresses {
			if ip, _, err := net.ParseCIDR(address.String()); err == nil {
				candidate.IPs = append(candidate.IPs, ip)
			}
		}
		candidates = append(candidates, candidate)
	}
	return interfaces, candidates, nil
}

// selectLANInterface resolves the interface the node listens, advertises and browses on.
func selectLANInterface(want string) (net.Interface, net.IP, error) {
	interfaces, candidates, e := lanCandidates()
	if e != nil {
		return net.Interface{}, nil, e
	}
	picked, ip, e := pickLANInterface(candidates, want)
	if e != nil {
		return net.Interface{}, nil, e
	}
	return interfaces[picked.index], ip, nil
}

// A browse delivers each service once. Fresh rounds renew liveness
// from new replies instead of treating the resolver's cached entry as a heartbeat.
func (l *LAN) startDiscovery(ctx context.Context, period time.Duration, browse func(context.Context) (<-chan *mdns.Entry, error)) error {
	round, cancel := context.WithTimeout(ctx, period)
	entries, err := browse(round)
	if err != nil {
		cancel()
		return err
	}
	go func() {
		defer close(l.done)
		lastErr := ""
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
			var err error
			if entries, err = browse(round); err != nil {
				if err.Error() != lastErr {
					slog.Warn("lan_mdns_browse_failed", "error", err)
				}
				lastErr = err.Error()
			} else {
				lastErr = ""
			}
		}
	}()
	return nil
}

// Changed signals that the set of discovered peers changed: a device appeared, moved or said goodbye.
// Expiry by TTL is not signalled; it is noticed by the consumer's next poll.
func (l *LAN) Changed() <-chan struct{} { return l.changed }

func (l *LAN) notify() {
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

func (l *LAN) discover(ctx context.Context, entries <-chan *mdns.Entry) {
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
				_, listed := l.peers[trusted.Name]
				delete(l.peers, trusted.Name)
				delete(l.presence, id)
				l.mu.Unlock()
				if listed {
					l.notify()
				}
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
			for _, ip := range ent.AddrIPv4 {
				if ip.IsPrivate() || ip.IsLoopback() {
					address = net.JoinHostPort(ip.String(), strconv.Itoa(ent.Port))
					break
				}
			}
			if address == "" {
				continue
			}
			peer := Peer{ID: id, Name: trusted.Name, Address: "https://" + address + "/mcp", Tags: trusted.Tags, Online: true, Direct: true, Fingerprint: fp}
			l.mu.Lock()
			previous, listed := l.peers[trusted.Name]
			fresh := listed && time.Since(previous.seen) <= previous.ttl && reflect.DeepEqual(previous.peer, peer)
			l.peers[trusted.Name] = lanPeer{peer, time.Now(), min(ttl, 30*time.Second)}
			l.mu.Unlock()
			if !fresh {
				l.notify()
			}
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
	selected, ip, e := selectLANInterface(l.iface)
	if e != nil {
		return nil, e
	}
	raw, e := net.Listen("tcp", net.JoinHostPort(ip.String(), strconv.Itoa(AgentPort)))
	if e != nil {
		return nil, e
	}
	l.listener = tls.NewListener(raw, l.trust.TLS(l.cert, "", true))
	if !l.mdns {
		return l.listener, nil
	}
	advertisement, e := mdns.Advertise(mdns.Service{Instance: l.name, Type: lanService, Domain: "local", Host: l.name, Port: AgentPort, Text: []string{"fingerprint=" + l.cert.Fingerprint}, IP: ip}, selected)
	if e != nil {
		raw.Close()
		return nil, e
	}
	slog.Info("lan_mdns_advertise", "interface", selected.Name, "address", ip.String(), "host", mdns.Label(l.name)+".local")
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
		l.advertisement.Close()
	}
	if l.listener != nil {
		// The agent's server closes the listener first on shutdown; closing it again is not a failure.
		if e := l.listener.Close(); e != nil && !errors.Is(e, net.ErrClosed) {
			return e
		}
	}
	return nil
}
func ExportPair(stateDir, name, path string) (string, error) {
	cert, e := LoadCertificate(stateDir, name)
	if e != nil {
		return "", e
	}
	data, e := json.MarshalIndent(Trusted{Name: name, Fingerprint: cert.Fingerprint, CertificatePEM: string(cert.PEM), Tags: []string{}}, "", "  ")
	if e != nil {
		return "", e
	}
	if e = os.WriteFile(path, data, 0600); e != nil {
		return "", e
	}
	return cert.Fingerprint, nil
}

// ErrPairFingerprint means the fingerprint the user confirmed is not the one of the pairing file.
var ErrPairFingerprint = errors.New("配对指纹与 --fingerprint 不一致")

// ErrPairTag means a requested LAN role is not one that pairing may grant.
var ErrPairTag = errors.New("不支持的配对角色")

func ImportPair(stateDir, path, expected string, tags []string) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	var p Trusted
	if e = json.Unmarshal(b, &p); e != nil {
		return e
	}
	return ImportTrusted(stateDir, p, expected, tags)
}

// ImportTrusted stores a peer's public pairing document after the user's own copy of its fingerprint
// matches it. The certificate itself must hash to that fingerprint (the trust store checks it).
func ImportTrusted(stateDir string, p Trusted, expected string, tags []string) error {
	if expected == "" || p.Fingerprint != strings.ToLower(strings.ReplaceAll(expected, ":", "")) {
		return ErrPairFingerprint
	}
	for _, tag := range tags {
		if tag != "tag:peer" && tag != "tag:device" && tag != "tag:ingress" && tag != "tag:admin" {
			return ErrPairTag
		}
	}
	p.Tags = tags
	if e := os.MkdirAll(stateDir, 0700); e != nil {
		return e
	}
	store, e := OpenTrust(filepath.Clean(stateDir))
	if e != nil {
		return e
	}
	return store.Add(p)
}
