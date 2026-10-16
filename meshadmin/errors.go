package meshadmin

import (
	"errors"
	"fmt"
)

// Sentinel errors. Callers match with errors.Is; details travel in the wrapped
// error text and never include credentials.
var (
	// ErrUnauthorized means the credential was rejected or lacks the scope for the call.
	ErrUnauthorized = errors.New("meshadmin: unauthorized")
	// ErrConflict means the policy changed between read and write (etag mismatch).
	ErrConflict = errors.New("meshadmin: policy changed since it was read")
	// ErrInvalid means the control plane or the local checks rejected the input.
	ErrInvalid = errors.New("meshadmin: invalid input")
	// ErrNotFound means a device, key or snapshot does not exist.
	ErrNotFound = errors.New("meshadmin: not found")
	// ErrUnsupported means the provider or its current mode cannot do this.
	ErrUnsupported = errors.New("meshadmin: unsupported by this control plane")
	// ErrNotConfirmed is returned by Apply when the caller did not confirm.
	ErrNotConfirmed = errors.New("meshadmin: change not confirmed")
)

// ScopeError wraps ErrUnauthorized with the OAuth scope a Tailscale operation
// needs, so a read-only credential produces an actionable hint.
type ScopeError struct {
	Op     string
	Scope  string
	Status int
}

func (e *ScopeError) Error() string {
	if e.Scope == "" {
		return fmt.Sprintf("meshadmin: %s: credential rejected (HTTP %d)", e.Op, e.Status)
	}
	return fmt.Sprintf("meshadmin: %s: credential rejected (HTTP %d); it needs scope %q", e.Op, e.Status, e.Scope)
}

func (e *ScopeError) Unwrap() error { return ErrUnauthorized }

// APIError carries a control-plane failure that has no more specific class.
type APIError struct {
	Op      string
	Status  int
	Message string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("meshadmin: %s: HTTP %d: %s", e.Op, e.Status, e.Message)
}

// ValidationError carries the control plane's policy validation message.
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return "meshadmin: policy validation failed: " + e.Message }
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// InvalidError is a 400-class rejection with the control plane's own message.
type InvalidError struct {
	Op      string
	Message string
}

func (e *InvalidError) Error() string { return "meshadmin: " + e.Op + ": " + e.Message }
func (e *InvalidError) Unwrap() error { return ErrInvalid }
