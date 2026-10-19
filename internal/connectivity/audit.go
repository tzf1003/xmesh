package connectivity

import (
	"github.com/tzf1003/xmesh/internal/audit"
	"github.com/tzf1003/xmesh/internal/devicelink"
)

func (n *Node) SetAudit(log *audit.Logger) { n.auditLog.Store(log) }

func (n *Node) auditActivity(activity devicelink.Activity) {
	log := n.auditLog.Load()
	if log == nil {
		return
	}
	event := "device_link_" + activity.State
	tool := "device.link"
	if activity.Path.Kind != "" {
		tool += "." + string(activity.Path.Kind)
	}
	_ = log.Event(event, n.identity.ID+"/connectivity", "", activity.DeviceID, tool)
}
