package control

import (
	"net/http"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// DelegateAdmin is the agent's delegation view behind GET /v2/delegate (API 2.7); the Agent implements it. Nil on a
// node without the agent role, and then the capability is not declared.
type DelegateAdmin interface {
	DelegateState() v2.DelegateState
}

func (s *Server) getDelegate(w http.ResponseWriter, _ *http.Request, _ *call) {
	writeJSON(w, 200, s.o.Delegate.DelegateState())
}
