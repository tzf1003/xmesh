package control

import (
	"errors"
	"net/http"
	"sort"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/backendenv"
	"github.com/tzf1003/xmesh/internal/policy"
)

// --- backend environment variables (CTR-CTL-14) ---
//
// The values are written to the private backendenv file and are never read back: not by these
// routes, not by GET /v2/status, not by the audit record (it keeps only the body's key names).

func (s *Server) backendEnvStore(w http.ResponseWriter) *backendenv.Store {
	if s.o.BackendEnv == nil {
		failWith(w, newError(v2.ErrUnavailable, "此节点没有后端环境变量存储"))
		return nil
	}
	return s.o.BackendEnv
}

// applyBackendEnv tells the running backends about a change at once; the kernel would also see
// it within a second.
func (s *Server) applyBackendEnv() {
	if s.o.ApplyBackendEnv != nil {
		s.o.ApplyBackendEnv()
	}
}

func (s *Server) patchBackendEnv(w http.ResponseWriter, r *http.Request, _ *call) {
	name := pathPart(r, 2)
	store := s.backendEnvStore(w)
	if store == nil {
		return
	}
	if !backendenv.ValidBackend(name) {
		failWith(w, argError("name", "后端名只能含字母、数字和连字符，且以字母或数字开头"))
		return
	}
	var req v2.BackendEnvPatch
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if len(req.Set) == 0 && len(req.Unset) == 0 {
		failWith(w, argError("body", "set 和 unset 至少要有一项"))
		return
	}
	if len(req.Set) > backendenv.MaxVars || len(req.Unset) > backendenv.MaxVars {
		failWith(w, argError("body", "一次最多改 64 个变量"))
		return
	}
	policyConfig := s.o.Store.Get().Agent.Policy
	setNames := make([]string, 0, len(req.Set))
	for variable := range req.Set {
		setNames = append(setNames, variable)
	}
	sort.Strings(setNames)
	for _, variable := range setNames {
		switch {
		case !backendenv.ValidName(variable):
			failWith(w, argError("set", "变量名只能含字母、数字和下划线，且不能以数字开头"))
			return
		case !backendenv.ValidValue(req.Set[variable]):
			failWith(w, argError("set."+variable, "变量值不能含空字符，长度不超过 8192 字节"))
			return
		case policy.CheckBackendEnv(policyConfig, variable, req.Set[variable]) != nil:
			e := argError("set."+variable, "节点策略不允许这个变量")
			e.Reason = "env_denied"
			failWith(w, e)
			return
		}
	}
	for _, variable := range req.Unset {
		if !backendenv.ValidName(variable) {
			failWith(w, argError("unset", "变量名只能含字母、数字和下划线，且不能以数字开头"))
			return
		}
		if _, both := req.Set[variable]; both {
			failWith(w, argError("unset", "同一个变量不能同时出现在 set 和 unset"))
			return
		}
	}
	names, err := store.Patch(name, req.Set, req.Unset)
	switch {
	case errors.Is(err, backendenv.ErrTooMany):
		failWith(w, argError("set", "每个后端最多 64 个环境变量"))
		return
	case err != nil:
		failWith(w, newError(v2.ErrInternal, "无法写入环境变量文件"))
		return
	}
	s.applyBackendEnv()
	writeJSON(w, 200, v2.BackendEnvResponse{Name: name, Names: names})
}

func (s *Server) deleteBackendEnv(w http.ResponseWriter, r *http.Request, _ *call) {
	name := pathPart(r, 2)
	store := s.backendEnvStore(w)
	if store == nil {
		return
	}
	if !backendenv.ValidBackend(name) {
		failWith(w, argError("name", "后端名只能含字母、数字和连字符，且以字母或数字开头"))
		return
	}
	if err := store.Clear(name); err != nil {
		failWith(w, newError(v2.ErrInternal, "无法写入环境变量文件"))
		return
	}
	s.applyBackendEnv()
	w.WriteHeader(204)
}

// backendsOf are the names under agent.backends of a parsed configuration.
func backendsOf(parsed map[string]any) map[string]bool {
	out := map[string]bool{}
	agent, _ := parsed["agent"].(map[string]any)
	backends, _ := agent["backends"].(map[string]any)
	for name := range backends {
		out[name] = true
	}
	return out
}

// pruneBackendEnv drops the variables of backends the configuration just lost, so a removed
// backend does not leave its secrets behind. A name that was never in the configuration is kept:
// a client may store the variables before it adds the backend.
func (s *Server) pruneBackendEnv(before, after map[string]any) {
	if s.o.BackendEnv == nil {
		return
	}
	had, has := backendsOf(before), backendsOf(after)
	removed, err := s.o.BackendEnv.Prune(func(name string) bool { return !had[name] || has[name] })
	if err == nil && removed {
		s.applyBackendEnv()
	}
}
