package control

import (
	"net/http"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

// --- GitHub Actions runner hosting (CTR-CTL-15) ---
//
// The kernel does not declare the ci capability yet, so these routes answer not_implemented
// before reaching the handler; the handler only keeps the route table and the registration in step.

func (s *Server) ciNotImplemented(w http.ResponseWriter, _ *http.Request, _ *call) {
	failWith(w, newError(v1.ErrNotImplemented, "此内核尚未实现 CI 运行器托管"))
}
