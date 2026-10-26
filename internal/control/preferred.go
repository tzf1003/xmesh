package control

import (
	"context"
	"net/http"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// PreferredAdmin is the Cloudflare preferred-address pool; the connectivity node implements it.
type PreferredAdmin interface {
	PreferredState() v2.PreferredIPState
	PreferredInspect(text string) v2.PreferredIPInspectResponse
	PreferredRun(ctx context.Context, task string) (v2.PreferredIPState, error)
}

func (s *Server) preferredAdmin(w http.ResponseWriter) PreferredAdmin {
	if s.o.Preferred == nil {
		failWith(w, newError(v2.ErrUnavailable, "设备连接服务尚未就绪"))
		return nil
	}
	return s.o.Preferred
}

func (s *Server) getPreferredIP(w http.ResponseWriter, _ *http.Request, _ *call) {
	if p := s.preferredAdmin(w); p != nil {
		writeJSON(w, 200, p.PreferredState())
	}
}

func (s *Server) runPreferredIP(w http.ResponseWriter, r *http.Request, _ *call) {
	p := s.preferredAdmin(w)
	if p == nil {
		return
	}
	var req v2.PreferredIPRunRequest
	if err := decode(r, &req, true); err != nil {
		failWith(w, err)
		return
	}
	if req.Task != "scan" && req.Task != "refresh" {
		failWith(w, argError("task", "任务只能是 scan 或 refresh"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	state, err := p.PreferredRun(ctx, req.Task)
	if err != nil {
		failWith(w, argError("task", err.Error()))
		return
	}
	writeJSON(w, 200, state)
}

func (s *Server) inspectPreferredIP(w http.ResponseWriter, r *http.Request, _ *call) {
	p := s.preferredAdmin(w)
	if p == nil {
		return
	}
	var req v2.PreferredIPInspectRequest
	if err := decode(r, &req, true); err != nil {
		failWith(w, err)
		return
	}
	writeJSON(w, 200, p.PreferredInspect(req.Text))
}
