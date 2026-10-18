package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func protocolServer(t *testing.T) *httptest.Server {
	t.Helper()
	g, _ := fixture(t, false)
	server := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) { return Principal{Local: true}, nil }))
	t.Cleanup(server.Close)
	return server
}

func rpcBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var out map[string]any
	if e := json.Unmarshal([]byte(body), &out); e != nil {
		t.Fatalf("not JSON: %v: %.200s", e, body)
	}
	return out
}

func Test_RPCParseErrorIsJSON(t *testing.T) {
	server := protocolServer(t)
	req, _ := http.NewRequest("POST", server.URL+"/mcp", strings.NewReader(`{bad json`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	var body strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		body.Write(buf[:n])
		if err != nil {
			break
		}
	}
	out := rpcBody(t, body.String())
	errObj, _ := out["error"].(map[string]any)
	if res.StatusCode != http.StatusBadRequest || errObj["code"] != float64(-32700) {
		t.Fatalf("want 400/-32700, got %d %v", res.StatusCode, out)
	}
}

func Test_RPCMethodNotFoundIsJSON(t *testing.T) {
	server := protocolServer(t)
	req, _ := http.NewRequest("POST", server.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"no/such","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	res, e := server.Client().Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	var body strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		body.Write(buf[:n])
		if err != nil {
			break
		}
	}
	out := rpcBody(t, body.String())
	errObj, _ := out["error"].(map[string]any)
	if res.StatusCode != http.StatusBadRequest || errObj["code"] != float64(-32601) || out["id"] != float64(7) {
		t.Fatalf("want 400/-32601 with id, got %d %v", res.StatusCode, out)
	}
}

func Test_RPCGetWithoutSessionIsJSON(t *testing.T) {
	server := protocolServer(t)
	res, e := server.Client().Get(server.URL + "/mcp")
	if e != nil {
		t.Fatal(e)
	}
	defer res.Body.Close()
	var body strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		body.Write(buf[:n])
		if err != nil {
			break
		}
	}
	out := rpcBody(t, body.String())
	errObj, _ := out["error"].(map[string]any)
	if res.StatusCode != http.StatusBadRequest || errObj["code"] != float64(-32600) {
		t.Fatalf("want 400/-32600, got %d %v", res.StatusCode, out)
	}
}
