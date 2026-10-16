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
