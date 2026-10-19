package agent

import (
	"encoding/json"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"net/http"
)

// Drain is a control-plane operation, separate from the public MCP tool table.
func (a *Agent) Drain(kill bool) {
	a.draining.Store(true)
	if kill {
		for _, s := range a.Computer.Sessions("") {
			a.Computer.Revoke(s.ID)
		}
		a.PTY.CloseAll()
		a.Leases.ReleaseAll()
	}
}
func (a *Agent) Control(auth Authenticate, onDrain func(string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/control/drain" || r.Method != "POST" {
			http.NotFound(w, r)
			return
		}
		p, e := auth(r)
		if e != nil {
			http.Error(w, "身份认证失败", 401)
			return
		}
		allowed := false
		for _, tag := range p.Tags {
			if tag == "tag:admin" {
				allowed = true
			}
		}
		if !allowed {
			http.Error(w, "需要 tag:admin", 403)
			return
		}
		var request struct {
			Scope  string `json:"scope"`
			Device string `json:"device"`
			Drain  string `json:"drain"`
		}
		if e = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); e != nil || (request.Scope != "device" && request.Scope != "mesh") || (request.Drain != "" && request.Drain != "keep" && request.Drain != "kill") {
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(toolerr.Args("drain").Object())
			return
		}
		name := a.store.Get().Node.Name
		if request.Scope == "device" && request.Device != name {
			http.Error(w, "目标设备不存在", 404)
			return
		}
		a.Drain(request.Drain == "kill")
		if onDrain != nil {
			if request.Scope == "mesh" {
				onDrain("")
			} else {
				onDrain(name)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
}
