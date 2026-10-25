package connectivity

import (
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/devicelink"
)

func (n *Node) SetAudit(log *audit.Logger) { n.auditLog.Store(log) }

func (n *Node) auditActivity(activity devicelink.Activity) {
	log := n.auditLog.Load()
	if log == nil {
		return
	}
	if activity.State == "unstable" {
		// Instability is only shown on the panel; the audit trail records the drop itself.
		return
	}
	event := "device_link_" + activity.State
	tool := "device.link"
	if activity.Path.Kind != "" {
		tool += "." + string(activity.Path.Kind)
	}
	_ = log.Event(event, n.identity.ID+"/connectivity", "", activity.DeviceID, tool)
}

// auditPathSwitch records smart mode moving a link, with why: "better" for a lasting improvement and
// "unhealthy" for a path that started losing probes.
func (n *Node) auditPathSwitch(deviceID string, from, to device.PathKind, reason string) {
	log := n.auditLog.Load()
	if log == nil {
		return
	}
	_ = log.Event("device_path_switch", n.identity.ID+"/connectivity", "", deviceID, "device.link."+string(from)+">"+string(to)+":"+reason)
}
