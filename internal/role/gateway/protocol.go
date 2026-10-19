package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

// rpcParseError is the JSON-RPC 2.0 body MCP clients expect when the request
// is not well-formed. The SDK's HTTP layer answers these cases with plain
// text, which strict clients treat as a transport failure instead of a
// protocol error, so they never learn that the method or body was at fault.
const (
	maxRPCBodyBytes    = 16 << 20
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
)

var rpcMethods = map[string]bool{
	"initialize":                true,
	"ping":                      true,
	"logging/setLevel":          true,
	"completion/complete":       true,
	"resources/list":            true,
	"resources/templates/list":  true,
	"resources/read":            true,
	"resources/subscribe":       true,
	"resources/unsubscribe":     true,
	"prompts/list":              true,
	"prompts/get":               true,
	"tools/list":                true,
	"tools/call":                true,
	"notifications/initialized": true,
	"notifications/cancelled":   true,
	"notifications/progress":    true,
}

// precheck rejects malformed JSON and unknown methods with a JSON-RPC error
// object. It only inspects POST bodies; GET and DELETE are left to the SDK,
// whose session handling owns those semantics.
func precheck(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		if r.Method == http.MethodGet && r.Header.Get("Mcp-Session-Id") == "" {
			writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "GET requires an Mcp-Session-Id header")
			return true
		}
		return false
	}
	body, e := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRPCBodyBytes))
	if e != nil {
		var limit *http.MaxBytesError
		if errors.As(e, &limit) {
			writeRPCError(w, http.StatusRequestEntityTooLarge, nil, codeInvalidRequest, "request body exceeds 16 MiB")
			return true
		}
		writeRPCError(w, http.StatusBadRequest, nil, codeParseError, "cannot read request body")
		return true
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if len(bytes.TrimSpace(body)) == 0 || json.Unmarshal(body, &probe) != nil {
		writeRPCError(w, http.StatusBadRequest, nil, codeParseError, "Parse error")
		return true
	}
	if probe.Method != "" && !rpcMethods[probe.Method] {
		writeRPCError(w, http.StatusBadRequest, probe.ID, codeMethodNotFound, "Method not found: "+probe.Method)
		return true
	}
	return false
}

func writeRPCError(w http.ResponseWriter, status int, id json.RawMessage, code int, message string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}
