package sandbox

import "fmt"

// WorkspaceTooLargeError reports a workspace holding more filesystem objects
// (files, directories and the root itself) than the runner admits. The runner
// must grant and reclaim access to every object, so the count is a hard bound,
// not a tuning hint. Callers can recognise it with errors.As and tell the
// caller to choose a smaller working directory.
type WorkspaceTooLargeError struct {
	Limit int
}

func (e *WorkspaceTooLargeError) Error() string {
	return fmt.Sprintf("sandbox workspace holds more than %d objects", e.Limit)
}

// UnavailableError reports that the platform's managed runner cannot be offered
// to this node for a reason the node's user can fix, such as the account it runs
// under. Reason is written for the caller and for the node's user: it carries no
// paths or internal detail and is shown in refused calls, sys.info and the
// control status. Cause is for the node log only.
type UnavailableError struct {
	Reason string
	Cause  error
}

func (e *UnavailableError) Error() string {
	if e.Cause == nil {
		return "sandbox unavailable: " + e.Reason
	}
	return "sandbox unavailable: " + e.Reason + ": " + e.Cause.Error()
}

func (e *UnavailableError) Unwrap() error { return e.Cause }
