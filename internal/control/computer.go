package control

import (
	"context"
	"net/http"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

func (s *Server) getComputer(w http.ResponseWriter, r *http.Request, _ *call) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	s.o.Computer.Configure(s.o.Store.Get().Agent.Computer)
	writeJSON(w, 200, s.o.Computer.LocalState(ctx))
}

func (s *Server) pauseComputer(w http.ResponseWriter, r *http.Request, _ *call) {
	var req v1.ComputerPauseRequest
	if err := decode(r, &req, true); err != nil {
		failWith(w, err)
		return
	}
	s.o.Computer.Pause(req.Paused)
	s.getComputer(w, r, nil)
}

func (s *Server) revokeComputer(w http.ResponseWriter, r *http.Request, _ *call) {
	var req v1.ComputerRevokeRequest
	if err := decode(r, &req, true); err != nil {
		failWith(w, err)
		return
	}
	if req.SessionID == "" {
		failWith(w, argError("session_id", "需要指定桌面会话"))
		return
	}
	s.o.Computer.Revoke(req.SessionID)
	s.getComputer(w, r, nil)
}
