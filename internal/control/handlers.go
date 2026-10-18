package control

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

// snapshot completes the host's status with what only the server knows (CTR-CTL-03).
func (s *Server) snapshot() v1.StatusResponse {
	st := s.o.Host.Status()
	st.Control = v1.ControlInfo{APIVersion: v1.APIVersion, KernelVersion: s.o.Version, BootID: s.bus.BootID(), Capabilities: s.caps}
	st.Lifecycle, _ = s.lifecycle.Load().(string)
	st.StartedAt, st.PID = s.started, os.Getpid()
	st.Drain = s.currentDrain()
	st.Paths = v1.Paths{ConfigFile: s.o.Store.Path(), StateDir: s.o.StateDir}
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
		st.Warnings = append(st.Warnings, v1.Warning{Code: v1.WarnPeerOffline, Severity: v1.SeverityWarning, Message: fmt.Sprintf("%d 个对端离线", offline)})
	}
	if s.auditDropped.Load() {
		st.Warnings = append(st.Warnings, v1.Warning{Code: v1.WarnAuditDropped, Severity: v1.SeverityWarning, Message: "有审计记录因含正文被拒绝写入"})
	}
	if s.pairPending.Load() {
		st.RestartRequired = true
		if !slices.ContainsFunc(st.Warnings, func(w v1.Warning) bool { return w.Code == v1.WarnRestartRequired }) {
			st.Warnings = append(st.Warnings, v1.Warning{Code: v1.WarnRestartRequired, Severity: v1.SeverityInfo, Message: "已导入对端配对证书，需要重启后生效"})
		}
	}
	// Order by code so two snapshots of the same state compare equal.
	sort.SliceStable(st.Warnings, func(i, j int) bool { return st.Warnings[i].Code < st.Warnings[j].Code })
	normalize(&st)
	return st
}

// normalize turns nil slices into empty ones: the contract has no null arrays.
func normalize(st *v1.StatusResponse) {
	if st.Roles == nil {
		st.Roles = []string{}
	}
	if st.Network.Tags == nil {
		st.Network.Tags = []string{}
	}
	if st.Warnings == nil {
		st.Warnings = []v1.Warning{}
	}
	if st.Shells == nil {
		st.Shells = []string{}
	}
	if st.Workspaces == nil {
		st.Workspaces = []v1.Workspace{}
	}
	if st.Backends == nil {
		st.Backends = []v1.Backend{}
	}
	if st.Control.Capabilities == nil {
		st.Control.Capabilities = []v1.Capability{}
	}
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request, _ *call) {
	writeJSON(w, 200, s.snapshot())
}

func (s *Server) cachedPeers() []v1.Peer {
	s.peerMu.Lock()
	defer s.peerMu.Unlock()
	return s.peers
}
func (s *Server) refreshPeers(ctx context.Context) ([]v1.Peer, error) {
	peers, err := s.o.Host.Peers(ctx)
	if err != nil {
		return nil, err
	}
	if peers == nil {
		peers = []v1.Peer{}
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
		failWith(w, newError(v1.ErrUnavailable, "暂时无法读取对端列表"))
		return
	}
	writeJSON(w, 200, v1.PeersResponse{Peers: peers})
}

func (s *Server) sessions(w http.ResponseWriter, _ *http.Request, _ *call) {
	writeJSON(w, 200, s.sessionsView())
}
func (s *Server) sessionsView() v1.SessionsResponse {
	v := s.o.Host.Sessions()
	if v.PTY == nil {
		v.PTY = []v1.PtySession{}
	}
	if v.Exec == nil {
		v.Exec = []v1.ExecRun{}
	}
	if v.Leases == nil {
		v.Leases = []v1.BackendLease{}
	}
	return v
}

// currentDrain reconciles the drain mark the server remembers (mode and start time) with the
// host's own flag, which can also be set by the mesh drain tool.
func (s *Server) currentDrain() v1.DrainState {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	return s.reconcileLocked(s.o.Host.Draining())
}
func (s *Server) reconcileLocked(hostDraining bool) v1.DrainState {
	switch {
	case !hostDraining:
		s.drain = v1.DrainState{}
	case !s.drain.Draining:
		keep, since := v1.DrainKeep, v1.Now()
		s.drain = v1.DrainState{Draining: true, Mode: &keep, Since: &since}
	}
	return s.drain
}

// startDrain implements the rules of RUL-16: repeating keep is a no-op, keep may be raised to
// kill without resetting since, and kill cannot be lowered to keep.
func (s *Server) startDrain(w http.ResponseWriter, r *http.Request, _ *call) {
	var req v1.DrainRequest
	if e := decode(r, &req, false); e != nil {
		failWith(w, e)
		return
	}
	switch req.Mode {
	case "":
		req.Mode = v1.DrainKeep
	case v1.DrainKeep, v1.DrainKill:
	default:
		failWith(w, argError("mode", "mode 只能是 keep 或 kill"))
		return
	}
	s.drainMu.Lock()
	state := s.reconcileLocked(s.o.Host.Draining())
	resp := v1.DrainResponse{Drain: state}
	changed := false
	switch {
	case state.Draining && *state.Mode == v1.DrainKill && req.Mode == v1.DrainKeep:
		s.drainMu.Unlock()
		e := newError(v1.ErrConflict, "已处于 kill 排空，不能降级为 keep")
		e.Reason = "drain_downgrade"
		failWith(w, e)
		return
	case state.Draining && *state.Mode == req.Mode:
	case state.Draining: // keep -> kill
		resp.ClosedPTY, resp.ReleasedLeases = s.o.Host.Drain(true)
		mode := v1.DrainKill
		s.drain.Mode = &mode
		changed = true
	default:
		resp.ClosedPTY, resp.ReleasedLeases = s.o.Host.Drain(req.Mode == v1.DrainKill)
		mode, since := req.Mode, v1.Now()
		s.drain = v1.DrainState{Draining: true, Mode: &mode, Since: &since}
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
		s.drain = v1.DrainState{}
	}
	state := s.drain
	s.drainMu.Unlock()
	if was {
		s.lifecycleEvent("resumed")
	}
	writeJSON(w, 200, v1.DrainResponse{Drain: state})
}
