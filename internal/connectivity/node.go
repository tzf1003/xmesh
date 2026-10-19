// Package connectivity runs independently enabled LAN and Tailnet paths using xmesh device trust.
package connectivity

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/devicelink"
	"github.com/tzf1003/xmesh/internal/filelock"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mdns"
	"github.com/tzf1003/xmesh/internal/mesh"
	"tailscale.com/ipn/store/mem"
	"tailscale.com/tsnet"
)

const serviceType = "_xmesh-v2._tcp"
const identityPath = "/mesh/v2/identity"

type Candidate struct {
	Name       string
	DeviceID   string
	Kind       device.PathKind
	Address    string
	SeenAt     time.Time
	ExpiresAt  time.Time
	Generation uint64
}

type provider struct {
	generation uint64
	cfg        config.Mesh
	cancel     context.CancelFunc
	done       chan struct{}
	err        string
	ready      bool
	lanIP      string
	ts         *tsnet.Server
}

type Node struct {
	mu             sync.Mutex
	ctx            context.Context
	cancel         context.CancelFunc
	stateDir, name string
	cfg            config.Mesh
	identity       *device.Identity
	trust          *device.TrustStore
	pairing        *device.Pairing
	directory      *device.Directory
	links          *devicelink.Manager
	accept         func(context.Context, identity.Principal, mcp.Transport) error
	admin          http.Handler
	providers      map[device.PathKind]*provider
	candidates     map[string]Candidate
	workers        sync.WaitGroup
	closed         bool
	lock           *os.File
	probes         chan Candidate
	probing        map[string]bool
	auditLog       atomic.Pointer[audit.Logger]
}

func Open(ctx context.Context, c *config.Config, stateDir string) (*Node, error) {
	return open(ctx, c, stateDir, true)
}

func open(ctx context.Context, c *config.Config, stateDir string, supervise bool) (*Node, error) {
	if c.SchemaVersion != 2 {
		return nil, fmt.Errorf("设备连接需要 schema_version: 2，请转换配置并重新配对")
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, err
	}
	lockContext, stopLock := context.WithTimeout(ctx, 250*time.Millisecond)
	lock, err := filelock.Acquire(lockContext, filepath.Join(stateDir, "device-runtime.lock"))
	stopLock()
	if err != nil {
		return nil, fmt.Errorf("设备状态目录已有运行实例: %w", err)
	}
	keepLock := false
	defer func() {
		if !keepLock {
			_ = lock.Close()
		}
	}()
	id, err := device.LoadIdentity(ctx, stateDir)
	if err != nil {
		return nil, err
	}
	trust, err := device.OpenTrust(stateDir)
	if err != nil {
		return nil, err
	}
	pairing, err := device.OpenPairing(id, c.Node.Name, trust)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	n := &Node{ctx: ctx, cancel: cancel, stateDir: stateDir, name: c.Node.Name, cfg: c.Mesh, identity: id, trust: trust, pairing: pairing, providers: map[device.PathKind]*provider{}, candidates: map[string]Candidate{}}
	n.lock = lock
	n.probes = make(chan Candidate, 128)
	n.probing = map[string]bool{}
	n.directory = device.NewDirectory(ctx, trust, id.ID)
	links, err := devicelink.NewManager(ctx, devicelink.Options{Identity: id, Trust: trust, Directory: n.directory, Dial: n.dialLink, OnActivity: n.auditActivity, Accept: func(ctx context.Context, p identity.Principal, s *devicelink.Session) error {
		n.mu.Lock()
		accept := n.accept
		n.mu.Unlock()
		if accept == nil {
			return errors.New("设备没有 Agent 角色")
		}
		return accept(ctx, p, &devicelink.Transport{Session: s})
	}})
	if err != nil {
		cancel()
		n.directory.Close()
		return nil, err
	}
	n.links = links
	for range 4 {
		n.workers.Go(n.probeWorker)
	}
	if supervise {
		n.workers.Go(n.supervise)
	}
	keepLock = true
	return n, nil
}

func (n *Node) SetAgent(accept func(context.Context, identity.Principal, mcp.Transport) error) {
	n.mu.Lock()
	n.accept = accept
	n.mu.Unlock()
}

func (n *Node) SetAdmin(handler http.Handler) { n.mu.Lock(); n.admin = handler; n.mu.Unlock() }
func (n *Node) Reconfigure(cfg config.Mesh)   { n.mu.Lock(); n.cfg = cfg; n.mu.Unlock() }
func (n *Node) Identity() *device.Identity    { return n.identity }
func (n *Node) Trust() *device.TrustStore     { return n.trust }
func (n *Node) Pairing() *device.Pairing      { return n.pairing }
func (n *Node) Directory() *device.Directory  { return n.directory }
func (n *Node) Links() *devicelink.Manager    { return n.links }

type ProviderStatus struct {
	Kind           device.PathKind
	Enabled, Ready bool
	Error          string
}

func (n *Node) ProviderStatuses() []ProviderStatus {
	n.mu.Lock()
	defer n.mu.Unlock()
	result := []ProviderStatus{}
	for _, kind := range []device.PathKind{device.LAN, device.Tailnet} {
		enabled := n.cfg.LANEnabled()
		if kind == device.Tailnet {
			enabled = n.cfg.TailnetEnabled()
		}
		status := ProviderStatus{Kind: kind, Enabled: enabled}
		if p := n.providers[kind]; p != nil {
			status.Ready, status.Error = p.ready, p.err
		}
		result = append(result, status)
	}
	return result
}

func (n *Node) Self(context.Context) (identity.Principal, error) {
	return identity.Principal{Node: n.identity.ID, Tags: []string{"tag:peer"}}, nil
}
func (n *Node) Authenticate(r *http.Request) (identity.Principal, error) {
	if r.TLS == nil || len(r.TLS.PeerCertificates) != 1 {
		return identity.Principal{}, device.ErrUntrusted
	}
	trusted, err := n.trust.VerifyCertificate(r.TLS.PeerCertificates[0], "")
	if err != nil {
		return identity.Principal{}, err
	}
	return identity.FromPeer(trusted.Document.DeviceID, trusted.Tags, r.Header.Get("X-Xmesh-Oauth-Client"))
}
func (n *Node) Transport(ctx context.Context, peer mesh.Peer, oauth string) (mcp.Transport, error) {
	return n.links.Open(ctx, peer.ID, oauth)
}

func (n *Node) Peers(context.Context) ([]mesh.Peer, error) {
	devices, _ := n.directory.Snapshot(time.Now())
	peers := make([]mesh.Peer, 0, len(devices))
	for _, d := range devices {
		a := n.links.Activity(d.DeviceID)
		online := a.State == "online" || a.State == "reconnecting"
		for _, path := range d.Paths {
			if path.Reachable {
				online = true
			}
		}
		if d.IdentityConflict {
			online = false
			a.State = "identity_conflict"
		}
		peers = append(peers, mesh.Peer{ID: d.DeviceID, Name: d.Alias, Address: "device://" + d.DeviceID, Tags: slices.Clone(d.Tags), Online: online && !d.IdentityConflict, Direct: a.Path.Kind == device.LAN, Fingerprint: d.DeviceID, Paths: d.Paths, ActivePath: a.Path.Kind, ConnectionState: a.State, DisplayName: d.Name})
	}
	return peers, nil
}

func (n *Node) Listen(context.Context) (net.Listener, error) {
	return nil, errors.New("设备协议由连接服务监听")
}
func (n *Node) HTTPClient(mesh.Peer) *http.Client { return &http.Client{Transport: rejectTransport{}} }

type rejectTransport struct{}

func (rejectTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("设备调用需要新版逻辑会话")
}

func (n *Node) Changed() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	versions, stop := n.directory.Subscribe()
	changed := make(chan struct{}, 1)
	n.workers.Go(func() {
		defer stop()
		defer close(changed)
		for {
			select {
			case <-n.ctx.Done():
				return
			case _, ok := <-versions:
				if !ok {
					return
				}
				select {
				case changed <- struct{}{}:
				default:
				}
			}
		}
	})
	return changed
}

func (n *Node) Candidates() []Candidate {
	n.mu.Lock()
	defer n.mu.Unlock()
	result := []Candidate{}
	seen := map[string]bool{}
	for _, trusted := range n.trust.Snapshot() {
		for _, hint := range trusted.Hints {
			generation, enabled := n.directory.Provider(hint.Kind)
			if !enabled {
				continue
			}
			key := string(hint.Kind) + "|" + hint.Address
			seen[key] = true
			result = append(result, Candidate{Name: trusted.Document.Name, DeviceID: trusted.Document.DeviceID, Kind: hint.Kind, Address: hint.Address, Generation: generation})
		}
	}
	for key, c := range n.candidates {
		if time.Now().After(c.ExpiresAt) {
			delete(n.candidates, key)
			continue
		}
		if !seen[key] {
			result = append(result, c)
		}
	}
	slices.SortFunc(result, func(a, b Candidate) int {
		return strings.Compare(a.DeviceID+string(a.Kind)+a.Address, b.DeviceID+string(b.Kind)+b.Address)
	})
	return result
}

func (n *Node) httpClient(kind device.PathKind, target string, bootstrap bool) (*http.Client, error) {
	tlsConfig := n.trust.TLS(n.identity, target, false)
	if bootstrap {
		tlsConfig = n.pairing.BootstrapTLS()
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 5 * time.Second, IdleConnTimeout: time.Second}
	if kind == device.Tailnet {
		n.mu.Lock()
		p := n.providers[kind]
		var server *tsnet.Server
		if p != nil {
			server = p.ts
		}
		n.mu.Unlock()
		if server == nil {
			return nil, errors.New("Tailnet 路径尚未就绪")
		}
		transport.DialContext = server.Dial
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}
func (n *Node) dialLink(ctx context.Context, id string, path device.Path) (*websocket.Conn, error) {
	client, err := n.httpClient(path.Kind, id, false)
	if err != nil {
		return nil, err
	}
	defer client.CloseIdleConnections()
	conn, _, err := websocket.Dial(ctx, strings.Replace(path.Address, "https://", "wss://", 1)+devicelink.LinkPath, &websocket.DialOptions{HTTPClient: client, Subprotocols: []string{devicelink.Subprotocol}})
	return conn, err
}

func (n *Node) Handler(kind device.PathKind) http.Handler {
	return n.handler(kind, 0)
}

func (n *Node) handler(kind device.PathKind, generation uint64) http.Handler {
	mux := http.NewServeMux()
	if generation == 0 {
		mux.Handle(devicelink.LinkPath, n.links.Handler(kind))
	} else {
		mux.Handle(devicelink.LinkPath, n.links.HandlerAt(kind, generation))
	}
	mux.HandleFunc("GET "+identityPath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Identity device.Document `json:"identity"`
			BootID   string          `json:"boot_id"`
		}{n.identity.Document(n.name), n.links.BootID()})
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "设备协议已升级，请更新 xmesh 并重新配对", 426)
	})
	mux.HandleFunc("/control/drain", func(w http.ResponseWriter, r *http.Request) {
		n.mu.Lock()
		admin := n.admin
		n.mu.Unlock()
		if admin == nil {
			http.NotFound(w, r)
			return
		}
		admin.ServeHTTP(w, r)
	})
	paired := n.pairing.Handler(mux)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current, enabled := n.directory.Provider(kind)
		if generation != 0 && (!enabled || current != generation) {
			http.Error(w, "连接路径已关闭", http.StatusServiceUnavailable)
			return
		}
		paired.ServeHTTP(w, r)
	})
}

func (n *Node) PairWith(ctx context.Context, kind device.PathKind, address, password, expected string) (device.Document, error) {
	endpoint, err := url.Parse(address)
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Host == "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return device.Document{}, fmt.Errorf("设备地址需要 https://host:port")
	}
	client, err := n.httpClient(kind, "", true)
	if err != nil {
		return device.Document{}, err
	}
	defer client.CloseIdleConnections()
	doc, err := n.pairing.PairWith(ctx, client, strings.TrimSuffix(address, "/"), password, expected)
	if err != nil {
		return device.Document{}, err
	}
	trusted, ok := n.trust.Find(doc.DeviceID)
	if !ok {
		return device.Document{}, device.ErrUntrusted
	}
	if err := n.trust.RememberPath(ctx, doc.DeviceID, trusted.Generation, device.PathHint{Kind: kind, Address: strings.TrimSuffix(address, "/")}); err != nil {
		return device.Document{}, err
	}
	generation, enabled := n.directory.Provider(kind)
	if enabled {
		n.probe(ctx, Candidate{Name: doc.Name, DeviceID: doc.DeviceID, Kind: kind, Address: strings.TrimSuffix(address, "/"), Generation: generation, ExpiresAt: time.Now().Add(30 * time.Second)})
	}
	return doc, nil
}

func (n *Node) probe(ctx context.Context, c Candidate) {
	trusted, ok := n.trust.Find(c.DeviceID)
	if !ok {
		return
	}
	client, err := n.httpClient(c.Kind, c.DeviceID, false)
	if err != nil {
		return
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", c.Address+identityPath, nil)
	if err != nil {
		return
	}
	response, err := client.Do(req)
	if err != nil {
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.TLS == nil || len(response.TLS.PeerCertificates) != 1 {
		return
	}
	var body struct {
		Identity device.Document `json:"identity"`
		BootID   string          `json:"boot_id"`
	}
	if json.NewDecoder(http.MaxBytesReader(nil, response.Body, 16<<10)).Decode(&body) != nil || body.Identity.Validate() != nil || body.Identity.DeviceID != c.DeviceID || body.BootID == "" {
		return
	}
	now := time.Now()
	_ = n.directory.Observe(response.TLS.PeerCertificates[0], trusted.Generation, device.Path{Kind: c.Kind, Address: c.Address, ProviderGeneration: c.Generation, BootID: body.BootID, VerifiedAt: now, ExpiresAt: now.Add(30 * time.Second)})
}

func (n *Node) Close() error {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		n.workers.Wait()
		return nil
	}
	n.closed = true
	n.cancel()
	n.mu.Unlock()
	_ = n.links.Close()
	n.workers.Wait()
	n.directory.Close()
	_ = n.lock.Close()
	return nil
}

func (n *Node) Addresses(ctx context.Context) ([]netip.Addr, string, error) {
	n.mu.Lock()
	p := n.providers[device.Tailnet]
	var server *tsnet.Server
	if p != nil {
		server = p.ts
	}
	n.mu.Unlock()
	if server == nil {
		return nil, "", errors.New("Tailnet 尚未就绪")
	}
	lc, err := server.LocalClient()
	if err != nil {
		return nil, "", err
	}
	status, err := lc.Status(ctx)
	if err != nil {
		return nil, "", err
	}
	if status.Self == nil {
		return nil, "", ErrNoAddress
	}
	return slices.Clone(status.Self.TailscaleIPs), strings.TrimSuffix(status.Self.DNSName, "."), nil
}

var ErrNoAddress = errors.New("设备网络地址尚未就绪")

func (n *Node) Funnel() (net.Listener, error) {
	n.mu.Lock()
	p := n.providers[device.Tailnet]
	var server *tsnet.Server
	if p != nil {
		server = p.ts
	}
	n.mu.Unlock()
	if server == nil {
		return nil, ErrNoAddress
	}
	return server.ListenFunnel("tcp", ":443")
}

// supervise applies one configuration snapshot at a time; providers run independently.
func (n *Node) supervise() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	defer func() {
		n.mu.Lock()
		providers := make([]*provider, 0, len(n.providers))
		for _, p := range n.providers {
			p.cancel()
			providers = append(providers, p)
		}
		n.mu.Unlock()
		for _, p := range providers {
			<-p.done
		}
	}()
	for {
		n.mu.Lock()
		cfg := n.cfg
		n.mu.Unlock()
		for _, kind := range []device.PathKind{device.LAN, device.Tailnet} {
			enabled := cfg.LANEnabled()
			if kind == device.Tailnet {
				enabled = cfg.TailnetEnabled()
			}
			n.mu.Lock()
			old := n.providers[kind]
			n.mu.Unlock()
			same := old != nil && ((kind == device.LAN && reflect.DeepEqual(old.cfg.LAN, cfg.LAN)) || (kind == device.Tailnet && reflect.DeepEqual(old.cfg.Tailnet, cfg.Tailnet)))
			if same {
				continue
			}
			generation := uint64(1)
			if old != nil {
				old.cancel()
				<-old.done
				n.mu.Lock()
				generation = old.generation + 1
				n.mu.Unlock()
			}
			_ = n.directory.SetProvider(kind, generation, enabled)
			n.links.DisablePath(kind)
			ctx, cancel := context.WithCancel(n.ctx)
			p := &provider{generation: generation, cfg: cfg, cancel: cancel, done: make(chan struct{})}
			n.mu.Lock()
			n.providers[kind] = p
			n.mu.Unlock()
			if !enabled {
				close(p.done)
				continue
			}
			go func(kind device.PathKind, p *provider) { defer close(p.done); n.runProvider(ctx, kind, p) }(kind, p)
		}
		for _, candidate := range n.Candidates() {
			n.enqueueProbe(candidate)
		}
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (n *Node) runProvider(ctx context.Context, kind device.PathKind, p *provider) {
	for ctx.Err() == nil {
		var err error
		if kind == device.LAN {
			err = n.runLAN(ctx, p)
		} else {
			err = n.runTailnet(ctx, p)
		}
		n.retireProvider(kind, p)
		n.mu.Lock()
		p.ready = false
		if err != nil {
			p.err = err.Error()
		}
		n.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
		n.mu.Lock()
		p.generation++
		generation := p.generation
		n.mu.Unlock()
		_ = n.directory.SetProvider(kind, generation, true)
	}
}

// Retire the generation before closing listeners: upgraded WebSockets are detached
// from http.Server, and their in-flight handshakes must not commit after shutdown.
func (n *Node) retireProvider(kind device.PathKind, p *provider) {
	n.mu.Lock()
	generation, enabled := n.directory.Provider(kind)
	if enabled && generation == p.generation {
		p.generation++
		_ = n.directory.SetProvider(kind, p.generation, false)
	}
	p.ready = false
	n.mu.Unlock()
	n.links.DisablePath(kind)
}

func (n *Node) serve(ctx context.Context, kind device.PathKind, p *provider, listener net.Listener, discovery func(context.Context)) error {
	server := &http.Server{Handler: n.handler(kind, p.generation), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	listener = tls.NewListener(listener, n.pairing.ServerTLS())
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	n.mu.Lock()
	p.ready = true
	p.err = ""
	n.mu.Unlock()
	discoveryCtx, cancel := context.WithCancel(ctx)
	discoveryDone := make(chan struct{})
	go func() { defer close(discoveryDone); discovery(discoveryCtx) }()
	var err error
	select {
	case <-ctx.Done():
		err = ctx.Err()
	case err = <-done:
	}
	n.retireProvider(kind, p)
	cancel()
	_ = server.Close()
	_ = listener.Close()
	<-discoveryDone
	return err
}

func (n *Node) runLAN(ctx context.Context, p *provider) error {
	generation := p.generation
	iface, ip, err := mesh.SelectLANInterface(p.cfg.LAN.Interface)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(ip.String(), "7433"))
	if err != nil {
		return err
	}
	n.mu.Lock()
	p.lanIP = ip.String()
	n.mu.Unlock()
	var advertisement *mdns.Responder
	if p.cfg.LAN.MDNS {
		advertisement, err = mdns.Advertise(mdns.Service{Instance: n.name + "-" + n.identity.ID[4:12], Type: serviceType, Host: n.name + "-" + n.identity.ID[4:12], Port: mesh.AgentPort, Text: []string{"version=2", "device_id=" + n.identity.ID, "name=" + n.name}, IP: ip}, iface)
		if err != nil {
			_ = listener.Close()
			return err
		}
		defer advertisement.Close()
	}
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-child.Done():
				return
			case <-ticker.C:
				_, next, e := mesh.SelectLANInterface(p.cfg.LAN.Interface)
				if e != nil || !next.Equal(ip) {
					cancel()
					return
				}
			}
		}
	}()
	return n.serve(child, device.LAN, p, listener, func(ctx context.Context) { n.browseLAN(ctx, p, generation, iface, ip) })
}

func (n *Node) browseLAN(ctx context.Context, p *provider, generation uint64, iface net.Interface, ip net.IP) {
	for ctx.Err() == nil {
		round, cancel := context.WithTimeout(ctx, 10*time.Second)
		var entries <-chan *mdns.Entry
		if p.cfg.LAN.MDNS {
			entries, _ = mdns.Browse(round, serviceType, "local", iface, ip)
		}
		for {
			select {
			case <-round.Done():
				cancel()
				goto next
			case entry, ok := <-entries:
				if !ok {
					entries = nil
					continue
				}
				values := map[string]string{}
				for _, txt := range entry.Text {
					k, v, _ := strings.Cut(txt, "=")
					values[k] = v
				}
				id := values["device_id"]
				if len(id) != 68 || !strings.HasPrefix(id, "dev-") || id == n.identity.ID {
					continue
				}
				for _, addr := range entry.AddrIPv4 {
					if !addr.IsPrivate() {
						continue
					}
					now := time.Now()
					c := Candidate{Name: values["name"], DeviceID: id, Kind: device.LAN, Address: "https://" + net.JoinHostPort(addr.String(), fmt.Sprint(entry.Port)), SeenAt: now, ExpiresAt: now.Add(30 * time.Second), Generation: generation}
					n.recordCandidate(ctx, c)
				}
			}
		}
	next:
		// Explicitly paired addresses remain candidates when multicast discovery is disabled.
		for _, c := range n.Candidates() {
			if c.Kind == device.LAN && c.Generation == generation {
				n.probe(ctx, c)
			}
		}
	}
}

func (n *Node) recordCandidate(ctx context.Context, c Candidate) {
	n.mu.Lock()
	if len(n.candidates) < 1024 {
		n.candidates[string(c.Kind)+"|"+c.Address] = c
	}
	n.mu.Unlock()
	n.enqueueProbe(c)
}

func (n *Node) enqueueProbe(c Candidate) {
	key := probeKey(c)
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || n.probing[key] {
		return
	}
	select {
	case n.probes <- c:
		n.probing[key] = true
	default:
	}
}

func probeKey(c Candidate) string { return c.DeviceID + "|" + string(c.Kind) + "|" + c.Address }

func (n *Node) probeWorker() {
	for {
		select {
		case <-n.ctx.Done():
			return
		case c := <-n.probes:
			if c.DeviceID == "" {
				n.discoverTailnet(n.ctx, c.Generation, c.Address)
			} else {
				n.probe(n.ctx, c)
			}
			n.mu.Lock()
			delete(n.probing, probeKey(c))
			n.mu.Unlock()
		}
	}
}

func (n *Node) runTailnet(ctx context.Context, p *provider) error {
	generation := p.generation
	cfg := p.cfg.Tailnet
	server := &tsnet.Server{Dir: filepath.Join(n.stateDir, "network", "tsnet"), Hostname: n.name, ControlURL: cfg.ControlURL, Ephemeral: cfg.Ephemeral, Logf: func(string, ...any) {}, UserLogf: func(string, ...any) {}}
	if cfg.Ephemeral {
		server.Store = new(mem.Store)
	}
	if cfg.AuthMethod != "browser" {
		key, err := os.ReadFile(cfg.AuthkeyFile)
		if err != nil {
			return fmt.Errorf("无法读取 Tailnet 入网密钥")
		}
		server.AuthKey = strings.TrimSpace(string(key))
	}
	if _, err := server.Up(ctx); err != nil {
		_ = server.Close()
		return err
	}
	defer server.Close()
	if cfg.Ephemeral {
		defer func() {
			logout, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if lc, err := server.LocalClient(); err == nil {
				_ = lc.Logout(logout)
			}
		}()
	}
	n.mu.Lock()
	p.ts = server
	n.mu.Unlock()
	defer func() { n.mu.Lock(); p.ts = nil; n.mu.Unlock() }()
	listener, err := server.Listen("tcp", ":7433")
	if err != nil {
		return err
	}
	return n.serve(ctx, device.Tailnet, p, listener, func(ctx context.Context) {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for ctx.Err() == nil {
			lc, e := server.LocalClient()
			if e == nil {
				status, e := lc.Status(ctx)
				if e == nil {
					for _, peer := range status.Peer {
						if !peer.Online || peer.Expired {
							continue
						}
						for _, ip := range peer.TailscaleIPs {
							// Identity is learned over TLS, never inferred from the Tailnet node ID or name.
							n.enqueueProbe(Candidate{Kind: device.Tailnet, Generation: generation, Address: "https://" + net.JoinHostPort(ip.String(), "7433")})
						}
					}
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	})
}

func (n *Node) discoverTailnet(ctx context.Context, generation uint64, address string) {
	client, err := n.httpClient(device.Tailnet, "", true)
	if err != nil {
		return
	}
	defer client.CloseIdleConnections()
	// Bootstrap can expose only the public device document; no trust is granted by discovery.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", address+device.PairPath+"identity", strings.NewReader("{}"))
	response, err := client.Do(request)
	if err != nil {
		return
	}
	defer response.Body.Close()
	var doc device.Document
	if response.StatusCode != 200 || json.NewDecoder(http.MaxBytesReader(nil, response.Body, 16<<10)).Decode(&doc) != nil || doc.Validate() != nil || response.TLS == nil || len(response.TLS.PeerCertificates) != 1 {
		return
	}
	id, err := device.IDFromKey(response.TLS.PeerCertificates[0].PublicKey)
	if err != nil || id != doc.DeviceID || id == n.identity.ID {
		return
	}
	now := time.Now()
	c := Candidate{Name: doc.Name, DeviceID: id, Kind: device.Tailnet, Address: address, SeenAt: now, ExpiresAt: now.Add(30 * time.Second), Generation: generation}
	n.mu.Lock()
	if len(n.candidates) < 1024 {
		n.candidates[string(c.Kind)+"|"+c.Address] = c
	}
	n.mu.Unlock()
	n.probe(ctx, c)
}
