package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
)

// The control channel shows whether each device's sys.info has arrived. Client catalogue refreshes
// cache it per MCP session, and only while a client is connected, so the gateway reads it on its own
// as well.
const (
	// infoTick is how often the discovery loop looks for devices whose reading is due.
	infoTick = 5 * time.Second
	// infoEvery is how long a reading, successful or not, stays current.
	infoEvery = time.Minute
	// infoTimeout bounds one read, dial included.
	infoTimeout = 15 * time.Second
)

// infoRecord is the latest reading of one device.
type infoRecord struct {
	refreshedAt time.Time
	err         string
	attempted   time.Time
	attempting  bool
}

// DeviceInfo is the outcome of the latest sys.info read of one device. A device without an entry
// has not been read yet.
type DeviceInfo struct {
	// RefreshedAt is the latest successful read; zero before the first one has succeeded.
	RefreshedAt time.Time
	// Error is why the latest read failed; empty after a success and while none has finished.
	Error string
}

// DeviceInfo reports the readings of every device the gateway has started to read.
func (g *Gateway) DeviceInfo() map[string]DeviceInfo {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make(map[string]DeviceInfo, len(g.info))
	for name, r := range g.info {
		out[name] = DeviceInfo{RefreshedAt: r.refreshedAt, Error: r.err}
	}
	return out
}

// pollInfo starts a read for every online device whose reading is missing or older than infoEvery.
// The reads run on their own goroutines so one slow peer does not hold up the discovery loop.
func (g *Gateway) pollInfo(now time.Time) {
	devices, _ := g.snapshot()
	g.mu.Lock()
	defer g.mu.Unlock()
	for name := range g.info {
		if _, ok := devices[name]; !ok {
			delete(g.info, name)
		}
	}
	for name, d := range devices {
		if name == g.opts.LocalPeer.Name || !d.online || !d.peer.Online {
			continue
		}
		r := g.info[name]
		if r == nil {
			r = &infoRecord{}
			g.info[name] = r
		}
		if r.attempting || now.Sub(r.attempted) < infoEvery {
			continue
		}
		r.attempting = true
		g.infoWorkers.Add(1)
		go g.readInfo(name, d.peer)
	}
}

func (g *Gateway) readInfo(name string, peer mesh.Peer) {
	defer g.infoWorkers.Done()
	ctx, cancel := context.WithTimeout(g.ctx, infoTimeout)
	defer cancel()
	err := g.sysInfo(ctx, peer)
	g.mu.Lock()
	defer g.mu.Unlock()
	r := g.info[name]
	if r == nil {
		// The device left while it was being read; pollInfo drops the entry on its next pass.
		r = &infoRecord{}
		g.info[name] = r
	}
	r.attempting = false
	r.attempted = time.Now()
	if err == nil {
		r.refreshedAt, r.err = r.attempted, ""
		return
	}
	r.err = err.Error()
}

// sysInfo reads sys.info once on a bridge of its own, with no client identity: the same request a
// local client's refresh makes.
func (g *Gateway) sysInfo(ctx context.Context, peer mesh.Peer) error {
	bridge, err := g.opts.Dial(ctx, peer, Principal{}, mcpmux.Options{})
	if err != nil {
		return err
	}
	defer func() { _ = bridge.Close() }()
	out, err := bridge.Call(ctx, "sys.info", map[string]any{}, mcpmux.Progress{})
	if err != nil {
		return err
	}
	if out == nil || out.IsError {
		return fmt.Errorf("sys.info 暂不可用")
	}
	if out.StructuredContent == nil {
		return fmt.Errorf("sys.info 返回为空")
	}
	return nil
}
