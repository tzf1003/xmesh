package v1

import (
	"fmt"
	"net/http"
)

// ErrorCode is the stable machine code of a control error (control-error.schema.json).
type ErrorCode string

const (
	ErrInvalidArgs         ErrorCode = "invalid_args"
	ErrForbidden           ErrorCode = "forbidden"
	ErrNotFound            ErrorCode = "not_found"
	ErrConflict            ErrorCode = "conflict"
	ErrRoleDisabled        ErrorCode = "role_disabled"
	ErrUnsupportedMode     ErrorCode = "unsupported_mode"
	ErrPreconditionFailed  ErrorCode = "precondition_failed"
	ErrPayloadTooLarge     ErrorCode = "payload_too_large"
	ErrConfigInvalid       ErrorCode = "config_invalid"
	ErrFingerprintMismatch ErrorCode = "fingerprint_mismatch"
	ErrPairRejected        ErrorCode = "pair_rejected"
	ErrRateLimited         ErrorCode = "rate_limited"
	ErrPreconditionNeeded  ErrorCode = "precondition_required"
	ErrInternal            ErrorCode = "internal"
	ErrNotImplemented      ErrorCode = "not_implemented"
	ErrUnavailable         ErrorCode = "unavailable"
)

var statusByCode = map[ErrorCode]int{
	ErrInvalidArgs:         http.StatusBadRequest,
	ErrForbidden:           http.StatusForbidden,
	ErrNotFound:            http.StatusNotFound,
	ErrConflict:            http.StatusConflict,
	ErrRoleDisabled:        http.StatusConflict,
	ErrUnsupportedMode:     http.StatusConflict,
	ErrPreconditionFailed:  http.StatusPreconditionFailed,
	ErrPayloadTooLarge:     http.StatusRequestEntityTooLarge,
	ErrConfigInvalid:       http.StatusUnprocessableEntity,
	ErrFingerprintMismatch: http.StatusUnprocessableEntity,
	ErrPairRejected:        http.StatusUnprocessableEntity,
	ErrRateLimited:         http.StatusTooManyRequests,
	ErrPreconditionNeeded:  http.StatusPreconditionRequired,
	ErrInternal:            http.StatusInternalServerError,
	ErrNotImplemented:      http.StatusNotImplemented,
	ErrUnavailable:         http.StatusServiceUnavailable,
}

// ErrorCodes lists every code the contract defines.
func ErrorCodes() []ErrorCode {
	codes := make([]ErrorCode, 0, len(statusByCode))
	for c := range statusByCode {
		codes = append(codes, c)
	}
	return codes
}

// HTTPStatus is the HTTP status the contract assigns to the code. Unknown codes map to 500.
func (c ErrorCode) HTTPStatus() int {
	if s, ok := statusByCode[c]; ok {
		return s
	}
	return http.StatusInternalServerError
}

// Diagnostic is one entry of Error.Diagnostics.
type Diagnostic struct {
	Pointer string `json:"pointer,omitempty"`
	Line    int    `json:"line,omitempty"`
	Column  int    `json:"column,omitempty"`
	Message string `json:"message"`
}

// Error is the body of every failed response. It has the shape of CTR-ERR-01 with the code set of
// this channel. Message is Chinese, one line, and callers must not branch on it.
type Error struct {
	Code         ErrorCode    `json:"error"`
	Message      string       `json:"message"`
	RetryAfterMS *int64       `json:"retry_after_ms,omitempty"`
	Field        string       `json:"field,omitempty"`
	Reason       string       `json:"reason,omitempty"`
	CurrentETag  string       `json:"current_etag,omitempty"`
	Diagnostics  []Diagnostic `json:"diagnostics,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("control: %s: %s", e.Code, e.Message) }

// HTTPStatus is the status line for this error.
func (e *Error) HTTPStatus() int { return e.Code.HTTPStatus() }
