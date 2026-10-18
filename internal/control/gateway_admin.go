package control

import (
	"context"
	"net/http"
	"os"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
)

// --- gateway reach (CTR-CTL-13) ---

// updateGateway replaces gateway.listen, gateway.access and gateway.hosts in one write. Access and host
// changes take effect on the next request; the running gateway moves to a changed listen address itself,
// and a move that fails is reported in apply_errors.
func (s *Server) updateGateway(w http.ResponseWriter, r *http.Request, _ *call) {
	var req v1.GatewayUpdateRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if !s.o.Store.Get().HasRole("gateway") {
		failWith(w, newError(v1.ErrRoleDisabled, "此节点没有 gateway 角色"))
		return
	}
	if req.Access == nil {
		req.Access = []string{}
	}
	if req.Hosts == nil {
		req.Hosts = []string{}
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return
	}
	edits := []Edit{
		{Path: "/gateway/listen", Value: req.Listen},
		{Path: "/gateway/access", Value: req.Access},
		{Path: "/gateway/hosts", Value: req.Hosts},
	}
	if !s.editConfigLocked(w, old, edits) {
		return
	}
	applyErrors := []v1.ApplyError{}
	if s.o.Applier != nil {
		settle, cancel := context.WithTimeout(r.Context(), applySettleTimeout)
		if errs := s.o.Applier.Settle(settle, false); errs != nil {
			applyErrors = errs
		}
		cancel()
	}
	writeJSON(w, 200, v1.GatewayUpdateResponse{Listen: req.Listen, Access: req.Access, Hosts: req.Hosts, RestartRequired: false, ApplyErrors: applyErrors})
}
