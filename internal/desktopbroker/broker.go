// Package desktopbroker hosts the native executor inside a user's graphical
// session. A SYSTEM kernel connects over a protected local pipe on Windows.
package desktopbroker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/computer"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/privdir"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

type Options struct {
	StateDir   string
	HelperPath string
	Authorize  bool
	Version    string
	Factory    func() (computer.Provider, error)
}

type Broker struct {
	mu       sync.Mutex
	options  Options
	provider computer.Provider
	owner    string
	last     time.Time
	server   *http.Server
	listener net.Listener
	Endpoint string
	stop     chan struct{}
	done     chan struct{}
	closed   bool
	once     sync.Once
}

func Start(o Options) (*Broker, error) {
	if o.StateDir == "" {
		o.StateDir = computer.DefaultStateDir()
	}
	if err := privdir.Ensure(o.StateDir); err != nil {
		return nil, err
	}
	if o.Factory == nil {
		o.Factory = func() (computer.Provider, error) { return computer.StartProcess(o.HelperPath) }
	}
	l, bound, err := control.Listen(control.ListenOptions{StateDir: o.StateDir, Node: "computer"})
	if err != nil {
		return nil, err
	}
	b := &Broker{options: o, listener: l, Endpoint: bound.Address, stop: make(chan struct{}), done: make(chan struct{})}
	if o.Authorize {
		p, e := computer.StartAuthorizedProcess(o.HelperPath)
		if e != nil {
			_ = l.Close()
			return nil, e
		}
		b.provider = p
	}
	b.server = &http.Server{Handler: http.NewCrossOriginProtection().Handler(http.HandlerFunc(b.serve)), ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 15 * time.Second}
	go func() { _ = b.server.Serve(l) }()
	go b.watch()
	return b, nil
}

func (b *Broker) watch() {
	defer close(b.done)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case now := <-t.C:
			b.mu.Lock()
			var p computer.Provider
			if b.provider != nil && b.owner != "" && now.Sub(b.last) > 6*time.Second {
				p = b.provider
				b.provider = nil
				b.owner = ""
			}
			b.mu.Unlock()
			if p != nil {
				_ = p.Close()
			}
		}
	}
}

var ownerPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

func (b *Broker) get(owner string) (computer.Provider, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, toolerr.New("desktop_unavailable", "桌面服务已退出")
	}
	if b.owner != "" && b.owner != owner {
		return nil, toolerr.New("resource_busy", "此桌面已绑定另一节点", "reason", "desktop_bound")
	}
	if child, ok := b.provider.(interface{ Exited() bool }); ok && child.Exited() {
		_ = b.provider.Close()
		b.provider = nil
	}
	if b.provider == nil {
		p, err := b.options.Factory()
		if err != nil {
			return nil, toolerr.New("desktop_unavailable", "原生桌面执行器无法启动", "reason", "executor_start_failed")
		}
		b.provider = p
		b.owner = owner
	}
	if b.owner == "" {
		b.owner = owner
	}
	b.last = time.Now()
	return b.provider, nil
}

func (b *Broker) disconnect(owner string) {
	b.mu.Lock()
	var p computer.Provider
	if b.owner == owner {
		p = b.provider
		b.provider = nil
		b.owner = ""
	}
	b.mu.Unlock()
	if p != nil {
		_ = p.Close()
	}
}

func (b *Broker) serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fail := func(err error) {
		e := toolerr.Internal("computer.broker", err)
		status := http.StatusServiceUnavailable
		if e.Code == "invalid_args" {
			status = 400
		}
		if e.Code == "resource_busy" {
			status = 409
		}
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(e.Object())
	}
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/") {
		http.NotFound(w, r)
		return
	}
	owner := r.Header.Get("X-Xmesh-Computer-Owner")
	if !ownerPattern.MatchString(owner) {
		fail(toolerr.Args("owner"))
		return
	}
	method := strings.TrimPrefix(r.URL.Path, "/v1/")
	if method == "status" {
		state := computer.HostStatus{Running: true, PID: os.Getpid(), Version: b.options.Version}
		b.mu.Lock()
		if process, ok := b.provider.(interface{ PID() int }); ok {
			state.NativePID = process.PID()
		}
		b.mu.Unlock()
		_ = json.NewEncoder(w).Encode(state)
		return
	}
	if method == "authorize" {
		b.mu.Lock()
		if b.closed {
			b.mu.Unlock()
			fail(toolerr.New("desktop_unavailable", "桌面服务已退出"))
			return
		}
		old := b.provider
		b.provider = nil
		// Preserve the current kernel binding across local permission setup. Its next
		// observation sees a new epoch, invalidating all old desktop authority.
		if old != nil {
			_ = old.Close()
		}
		p, err := computer.StartAuthorizedProcess(b.options.HelperPath)
		if err == nil {
			b.provider = p
			b.last = time.Now()
		}
		b.mu.Unlock()
		if err != nil {
			fail(toolerr.New("desktop_unavailable", "原生桌面执行器无法启动", "reason", "executor_start_failed"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"requested": true})
		return
	}
	if method != "info" && method != "capture" && method != "execute" && method != "release" && method != "heartbeat" && method != "disconnect" {
		http.NotFound(w, r)
		return
	}
	if method == "disconnect" {
		b.disconnect(owner)
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	p, err := b.get(owner)
	if err != nil {
		fail(err)
		return
	}
	decode := func(out any) error {
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		d.DisallowUnknownFields()
		return d.Decode(out)
	}
	var out any
	switch method {
	case "heartbeat":
		out = map[string]bool{"ok": true}
	case "info":
		var request struct {
			Protocol int `json:"protocol_version"`
		}
		if decode(&request) != nil || request.Protocol != computer.ProtocolVersion {
			fail(toolerr.New("desktop_unavailable", "桌面协议版本不匹配", "reason", "protocol_mismatch"))
			return
		}
		out, err = p.Info(r.Context())
	case "capture":
		var q computer.CaptureRequest
		if decode(&q) != nil {
			fail(toolerr.Args("capture"))
			return
		}
		var frame computer.Frame
		frame, err = p.Capture(r.Context(), q)
		out = struct {
			computer.Frame
			Image []byte `json:"image"`
		}{frame, frame.Image}
	case "execute":
		var q computer.Execution
		if decode(&q) != nil {
			fail(toolerr.Args("execution"))
			return
		}
		out, err = p.Execute(r.Context(), q)
	case "release":
		var q computer.Execution
		if decode(&q) != nil {
			fail(toolerr.Args("release"))
			return
		}
		err = p.Release(r.Context(), q)
		out = map[string]bool{"released": err == nil}
	}
	if err != nil {
		fail(err)
		return
	}
	if err = json.NewEncoder(w).Encode(out); err != nil && !errors.Is(err, context.Canceled) {
		return
	}
}

func (b *Broker) Close() error {
	var err error
	b.once.Do(func() {
		b.mu.Lock()
		b.closed = true
		close(b.stop)
		owner := b.owner
		b.mu.Unlock()
		_ = b.server.Close()
		_ = b.listener.Close()
		<-b.done
		b.disconnect(owner)
	})
	return err
}

func (b *Broker) String() string { return fmt.Sprintf("computer broker at %s", b.Endpoint) }
