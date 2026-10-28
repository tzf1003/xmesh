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
	_ = log.Event(event, n.identity.ID+"/connectivity", "", activity.DeviceID, linkTool(activity))
}

// linkTool names the link in the audit trail: the path and which end dialed it, "out" when this end did.
func linkTool(activity devicelink.Activity) string {
	tool := "device.link"
	if activity.Path.Kind == "" {
		return tool
	}
	direction := "in"
	if activity.Outgoing {
		direction = "out"
	}
	return tool + "." + string(activity.Path.Kind) + "." + direction
}

// auditPathSwitch records smart mode moving a link, with why: "better" for a lasting improvement and
// "unhealthy" for a path that started losing probes. Only the end that dialed a link moves it, so a switch
// always concerns a link this end dialed.
func (n *Node) auditPathSwitch(deviceID string, from, to device.PathKind, reason string) {
	log := n.auditLog.Load()
	if log == nil {
		return
	}
	_ = log.Event("device_path_switch", n.identity.ID+"/connectivity", "", deviceID, "device.link."+string(from)+">"+string(to)+":"+reason)
}
