package gateway

import (
	"encoding/json"
	"errors"
	"io"
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

// Authentication must reject a request before inspecting its untrusted body.
func Test_RPCAuthenticationPrecedesParsing(t *testing.T) {
	g, _ := fixture(t, false)
	h := g.HTTP(func(*http.Request) (Principal, error) { return Principal{}, errors.New("invalid") })
	for _, body := range []string{"{bad", `{"method":"no/such"}`, ""} {
		for _, method := range []string{"POST", "GET", "DELETE"} {
			req := httptest.NewRequest(method, "/mcp", strings.NewReader(body))
			unread := &countedBody{Reader: req.Body}
			req.Body = unread
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusUnauthorized || unread.read != 0 {
				t.Fatalf("%s %q: status=%d read=%d", method, body, w.Code, unread.read)
			}
		}
	}
}

type countedBody struct {
	io.Reader
	read int
}

func (b *countedBody) Read(p []byte) (int, error) { n, e := b.Reader.Read(p); b.read += n; return n, e }
func (b *countedBody) Close() error               { return nil }

func Test_RPCPrecheckBoundsUnknownLengthBody(t *testing.T) {
	body := &countedBody{Reader: strings.NewReader(strings.Repeat(" ", maxRPCBodyBytes+4096))}
	req := httptest.NewRequest("POST", "/mcp", body)
	req.ContentLength = -1
	w := httptest.NewRecorder()
	if !precheck(w, req) || w.Code != http.StatusRequestEntityTooLarge || body.read != maxRPCBodyBytes+1 {
		t.Fatalf("status=%d read=%d", w.Code, body.read)
	}
}

func Test_RPCPrecheckRequestShape(t *testing.T) {
	for _, body := range []string{
		`null`, `[]`, `42`, `"request"`, `{}`, `{"jsonrpc":"1.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1,"method":123}`, `{"jsonrpc":"2.0","id":1,"method":null}`,
		`{"jsonrpc":"2.0","id":{},"method":"ping"}`, `{"jsonrpc":"2.0","id":true,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","params":false}`,
		`{"jsonrpc":"2.0","id":1,"method":"ping","result":{}}`, `{"jsonrpc":"2.0","id":1}`,
	} {
		t.Run(body, func(t *testing.T) {
			w := httptest.NewRecorder()
			if !precheck(w, httptest.NewRequest("POST", "/mcp", strings.NewReader(body))) {
				t.Fatal("invalid request reached the SDK")
			}
			out := rpcBody(t, w.Body.String())
			errObj, _ := out["error"].(map[string]any)
			if w.Code != http.StatusBadRequest || errObj["code"] != float64(codeInvalidRequest) || out["id"] != nil {
				t.Fatalf("want 400/-32600/null, got %d %v", w.Code, out)
			}
		})
	}
}

func Test_RPCPrecheckNotificationsAndResponses(t *testing.T) {
	for _, body := range []string{
		`{"jsonrpc":"2.0","method":"notifications/roots/list_changed"}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":"ping","method":"ping","params":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"roots":[]}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"failed"}}`,
	} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/mcp", strings.NewReader(body))
		if precheck(w, req) {
			t.Fatalf("valid message refused: %s: %s", body, w.Body.String())
		}
		got, err := io.ReadAll(req.Body)
		if err != nil || string(got) != body {
			t.Fatalf("SDK body = %q, %v", got, err)
		}
	}
	for _, method := range []string{"notifications/future", "no/such", ""} {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
		w := httptest.NewRecorder()
		if !precheck(w, httptest.NewRequest("POST", "/mcp", strings.NewReader(string(body)))) || w.Code != http.StatusAccepted || w.Body.Len() != 0 {
			t.Fatalf("notification %q: status=%d body=%s", method, w.Code, w.Body.String())
		}
	}
}
