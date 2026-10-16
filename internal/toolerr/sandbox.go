package toolerr

// SandboxRunner reports a managed isolation runner that could not start or
// finish an execution. The call is refused under a named rule, so callers see
// the same denial as an unavailable platform instead of an unexplained internal
// error. The underlying cause stays attached for the node log.
func SandboxRunner(rule string, err error) *Error {
	e := New("policy_denied", "托管沙箱运行器拒绝了此次执行", "rule", rule, "reason", "L2 运行器未能完成请求，详见节点日志")
	e.Cause = err
	return e
}
