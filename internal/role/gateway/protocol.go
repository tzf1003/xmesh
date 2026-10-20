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
	"initialize":                       true,
	"ping":                             true,
	"logging/setLevel":                 true,
	"completion/complete":              true,
	"resources/list":                   true,
	"resources/templates/list":         true,
	"resources/read":                   true,
	"resources/subscribe":              true,
	"resources/unsubscribe":            true,
	"prompts/list":                     true,
	"prompts/get":                      true,
	"tools/list":                       true,
	"tools/call":                       true,
	"notifications/initialized":        true,
	"notifications/cancelled":          true,
	"notifications/progress":           true,
	"notifications/roots/list_changed": true,
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
	if !json.Valid(body) {
		writeRPCError(w, http.StatusBadRequest, nil, codeParseError, "Parse error")
		return true
	}
	var fields map[string]json.RawMessage
	err := json.Unmarshal(body, &fields)
	var version string
	versionErr := json.Unmarshal(fields["jsonrpc"], &version)
	if err != nil || fields == nil || versionErr != nil || version != "2.0" || !validRPCID(fields["id"]) {
		writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "Invalid Request")
		return true
	}
	methodRaw, hasMethod := fields["method"]
	if !hasMethod {
		// Client responses to server requests belong to the SDK's session.
		_, hasResult := fields["result"]
		_, hasError := fields["error"]
		if hasResult == hasError || len(fields["params"]) != 0 || len(fields["id"]) == 0 {
			writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "Invalid Request")
			return true
		}
		return false
	}
	var method string
	methodErr := json.Unmarshal(methodRaw, &method)
	params := bytes.TrimSpace(fields["params"])
	if methodErr != nil || bytes.Equal(bytes.TrimSpace(methodRaw), []byte("null")) || len(fields["result"]) != 0 || len(fields["error"]) != 0 ||
		(len(params) > 0 && params[0] != '{' && params[0] != '[') {
		writeRPCError(w, http.StatusBadRequest, nil, codeInvalidRequest, "Invalid Request")
		return true
	}
	if !rpcMethods[method] {
		if len(fields["id"]) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return true
		}
		writeRPCError(w, http.StatusBadRequest, fields["id"], codeMethodNotFound, "Method not found: "+method)
		return true
	}
	return false
}

// Validate an ID's shape without translating or remapping its original bytes.
func validRPCID(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch value.(type) {
	case nil, string, float64:
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
