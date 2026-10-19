package control

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// snapshot completes the host's status with what only the server knows (CTR-CTL-03).
func (s *Server) snapshot() v2.StatusResponse {
	st := s.o.Host.Status()
	st.Control = v2.ControlInfo{APIVersion: v2.APIVersion, KernelVersion: s.o.Version, BootID: s.bus.BootID(), Capabilities: s.caps}
	st.Lifecycle, _ = s.lifecycle.Load().(string)
	st.StartedAt, st.PID = s.started, os.Getpid()
	st.Drain = s.currentDrain()
	st.Paths = v2.Paths{ConfigFile: s.o.Store.Path(), StateDir: s.o.StateDir}
	st.Paths.ComputerEndpoint = s.computerEndpoint
	if s.o.AuditDir != "" {
		st.Paths.AuditDir = ptr(s.o.AuditDir)
	}
	if s.o.CI != nil {
		st.CI = s.o.CI.Summary()
	}
	peers := s.cachedPeers()
	offline := 0
	for _, p := range peers {
		if p.Self {
			continue
		}
		st.Peers.Total++
		if p.Online {
			st.Peers.Online++
		} else {
			offline++
		}
	}
	if offline > 0 {
		st.Warnings = append(st.Warnings, v2.Warning{Code: v2.WarnPeerOffline, Severity: v2.SeverityWarning, Message: fmt.Sprintf("%d 个对端离线", offline)})
	}
	if s.auditDropped.Load() {
		st.Warnings = append(st.Warnings, v2.Warning{Code: v2.WarnAuditDropped, Severity: v2.SeverityWarning, Message: "有审计记录因含正文被拒绝写入"})
	}
	// Order by code so two snapshots of the same state compare equal.
	sort.SliceStable(st.Warnings, func(i, j int) bool { return st.Warnings[i].Code < st.Warnings[j].Code })
	normalize(&st)
	return st
}

// normalize turns nil slices into empty ones: the contract has no null arrays.
func normalize(st *v2.StatusResponse) {
	if st.Roles == nil {
		st.Roles = []string{}
	}
	if st.Network.Tags == nil {
		st.Network.Tags = []string{}
	}
	if st.Warnings == nil {
		st.Warnings = []v2.Warning{}
	}
	if st.Shells == nil {
		st.Shells = []string{}
	}
	if st.Workspaces == nil {
		st.Workspaces = []v2.Workspace{}
	}
	if st.Backends == nil {
		st.Backends = []v2.Backend{}
	}
	if st.Control.Capabilities == nil {
		st.Control.Capabilities = []v2.Capability{}
	}
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request, _ *call) {
	writeJSON(w, 200, s.snapshot())
}

func (s *Server) cachedPeers() []v2.Peer {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	return s.peers
}
func (s *Server) refreshPeers(ctx context.Context) ([]v2.Peer, error) {
	peers, err := s.o.Host.Peers(ctx)
	if err != nil {
		return nil, err
	}
	if peers == nil {
		peers = []v2.Peer{}
	}
	s.peerMu.Lock()
	s.peers = peers
	s.peerMu.Unlock()
	return peers, nil
}

func (s *Server) listPeers(w http.ResponseWriter, r *http.Request, _ *call) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	peers, err := s.refreshPeers(ctx)
	if err != nil {
		failWith(w, newError(v2.ErrUnavailable, "暂时无法读取对端列表"))
		return
	}
	writeJSON(w, 200, v2.PeersResponse{Peers: peers})
}

func (s *Server) sessions(w http.ResponseWriter, _ *http.Request, _ *call) {
	writeJSON(w, 200, s.sessionsView())
}
func (s *Server) sessionsView() v2.SessionsResponse {
	v := s.o.Host.Sessions()
	if v.PTY == nil {
		v.PTY = []v2.PtySession{}
	}
	if v.Exec == nil {
		v.Exec = []v2.ExecRun{}
	}
	if v.Leases == nil {
		v.Leases = []v2.BackendLease{}
	}
	return v
}

// currentDrain reconciles the drain mark the server remembers (mode and start time) with the
// host's own flag, which can also be set by the mesh drain tool.
func (s *Server) currentDrain() v2.DrainState {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	return s.reconcileLocked(s.o.Host.Draining())
}
func (s *Server) reconcileLocked(hostDraining bool) v2.DrainState {
	switch {
	case !hostDraining:
		s.drain = v2.DrainState{}
	case !s.drain.Draining:
		keep, since := v2.DrainKeep, v2.Now()
		s.drain = v2.DrainState{Draining: true, Mode: &keep, Since: &since}
	}
	return s.drain
}

// startDrain implements the rules of RUL-16: repeating keep is a no-op, keep may be raised to
// kill without resetting since, and kill cannot be lowered to keep.
func (s *Server) startDrain(w http.ResponseWriter, r *http.Request, _ *call) {
	var req v2.DrainRequest
	if e := decode(r, &req, false); e != nil {
		failWith(w, e)
		return
	}
	switch req.Mode {
	case "":
		req.Mode = v2.DrainKeep
	case v2.DrainKeep, v2.DrainKill:
	default:
		failWith(w, argError("mode", "mode 只能是 keep 或 kill"))
		return
	}
	if req.TimeoutMS < 0 || req.TimeoutMS > v2.MaxDrainTimeoutMS {
		failWith(w, argError("timeout_ms", "timeout_ms 超出支持范围"))
		return
	}
	if req.Scope != "" && req.Scope != "local" {
		if (req.Scope != "device" && req.Scope != "mesh") || (req.Scope == "device") != (req.Device != "") {
			failWith(w, argError("scope", "远程排空使用 device 与设备 ID，或 mesh"))
			return
		}
		host, ok := s.o.Host.(RemoteDrainer)
		if !ok {
			failWith(w, newError(v2.ErrUnavailable, "设备连接服务不可用"))
			return
		}
		outcomes, err := host.RemoteDrain(r.Context(), req)
		if err != nil {
			failWith(w, newError(v2.ErrUnavailable, err.Error()))
			return
		}
		writeJSON(w, 200, v2.DrainResponse{Drain: s.currentDrain(), Outcomes: outcomes})
		return
	}
	if req.TimeoutMS != 0 {
		failWith(w, argError("timeout_ms", "本机排空不使用 timeout_ms"))
		return
	}
	if req.Device != "" {
		failWith(w, argError("device", "本机排空不使用 device"))
		return
	}
	s.drainMu.Lock()
	state := s.reconcileLocked(s.o.Host.Draining())
	resp := v2.DrainResponse{Drain: state}
	changed := false
	switch {
	case state.Draining && *state.Mode == v2.DrainKill && req.Mode == v2.DrainKeep:
		s.drainMu.Unlock()
		e := newError(v2.ErrConflict, "已处于 kill 排空，不能降级为 keep")
		e.Reason = "drain_downgrade"
		failWith(w, e)
		return
	case state.Draining && *state.Mode == req.Mode:
	case state.Draining: // keep -> kill
		resp.ClosedPTY, resp.ReleasedLeases = s.o.Host.Drain(true)
		mode := v2.DrainKill
		s.drain.Mode = &mode
		changed = true
	default:
		resp.ClosedPTY, resp.ReleasedLeases = s.o.Host.Drain(req.Mode == v2.DrainKill)
		mode, since := req.Mode, v2.Now()
		s.drain = v2.DrainState{Draining: true, Mode: &mode, Since: &since}
		changed = true
	}
	resp.Drain = s.drain
	s.drainMu.Unlock()
	if changed {
		s.lifecycleEvent("draining")
	}
	writeJSON(w, 200, resp)
}

func (s *Server) resume(w http.ResponseWriter, _ *http.Request, _ *call) {
	s.drainMu.Lock()
	was := s.reconcileLocked(s.o.Host.Draining()).Draining
	if was {
		s.o.Host.Resume()
		s.drain = v2.DrainState{}
	}
	state := s.drain
	s.drainMu.Unlock()
	if was {
		s.lifecycleEvent("resumed")
	}
	writeJSON(w, 200, v2.DrainResponse{Drain: state})
}
