package computer

import "github.com/tzf1003/xmesh/internal/toolerr"

// Envelope identifies desktop errors so shared error contracts can distinguish
// desktop observations from optimistic filesystem version conflicts.
func Envelope(err error) *toolerr.Error {
	e := toolerr.Internal("computer", err)
	out := toolerr.New(e.Code, e.Message, "resource", "computer:desktop")
	for k, v := range e.Fields {
		out.Fields[k] = v
	}
	if _, ok := out.Fields["reason"]; !ok {
		out.Fields["reason"] = "unavailable"
	}
	if e.Code == "policy_denied" {
		if _, ok := out.Fields["rule"]; !ok {
			out.Fields["rule"] = "computer.host_policy"
			if out.Fields["reason"] == "host_suspended" {
				out.Fields["rule"] = "computer.host_suspended"
			}
		}
	}
	if e.Code == "resource_busy" {
		if out.Fields["reason"] == "unavailable" {
			out.Fields["reason"] = "capacity"
		}
		if _, ok := out.Fields["held_by"]; !ok {
			out.Fields["held_by"] = ""
		}
		if _, ok := out.Fields["expires_in_s"]; !ok {
			out.Fields["expires_in_s"] = 0
		}
	}
	return out
}
