// Package toolerr implements CTR-ERR-01 and the RUL-08 MCP error envelope.
package toolerr

import (
	"encoding/json"
	"errors"
)

type Error struct {
	Code    string
	Message string
	Fields  map[string]any
	Cause   error
}

func (e *Error) Unwrap() error { return e.Cause }

func (e *Error) Error() string { return e.Message }
func (e *Error) Object() map[string]any {
	m := make(map[string]any, len(e.Fields)+2)
	for k, v := range e.Fields {
		m[k] = v
	}
	m["error"] = e.Code
	m["message"] = e.Message
	return m
}
func (e *Error) MarshalJSON() ([]byte, error) { return json.Marshal(e.Object()) }
func New(code, message string, fields ...any) *Error {
	e := &Error{Code: code, Message: message, Fields: map[string]any{}}
	for i := 0; i+1 < len(fields); i += 2 {
		e.Fields[fields[i].(string)] = fields[i+1]
	}
	return e
}
func Args(field string) *Error { return New("invalid_args", "参数无效", "field", field) }
func Path(path, reason string) *Error {
	return New("invalid_cwd", "路径不可用或不在允许范围内", "path", path, "reason", reason)
}
func Internal(op string, err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	v := New("internal", "本机操作失败", append([]any{"op", op}, osFields(err)...)...)
	v.Cause = err
	return v
}
