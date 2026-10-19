package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/audit"
)

type panelTransport struct{ origin string }

func (p panelTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c := r.Clone(r.Context())
	c.Header.Set("X-Xmesh-Origin", p.origin)
	return http.DefaultTransport.RoundTrip(c)
}

func TestPanelOriginAuditAndAuthorization(t *testing.T) {
	for _, origin := range []string{"panel", "", "other"} {
		t.Run(origin, func(t *testing.T) {
			g, _ := fixture(t, false)
			dir := t.TempDir()
			log, err := audit.New(dir, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			g.opts.Audit = log
			// All clients retain the same read-only scopes, including clients claiming panel origin.
			srv := httptest.NewServer(g.HTTP(func(*http.Request) (Principal, error) {
				return Principal{CredentialID: "pat:default", Scopes: []string{"devices:read"}}, nil
			}))
			defer srv.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			client := mcp.NewClient(&mcp.Implementation{Name: "panel-test", Version: "1"}, nil)
			s, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp", HTTPClient: &http.Client{Transport: panelTransport{origin}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if out := call(t, s, "devices.list", map[string]any{}); out.IsError {
				t.Fatal(out)
			}
			out := call(t, s, "local/exec.run", map[string]any{"cmd": "echo", "args": []string{"private-output"}})
			if !out.IsError {
				t.Fatal("origin granted exec permission")
			}
			files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
			found := false
			for _, file := range files {
				body, _ := os.ReadFile(file)
				if strings.Contains(string(body), "private-output") {
					t.Fatal("argument leaked into audit")
				}
				for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
					var row map[string]any
					if json.Unmarshal([]byte(line), &row) != nil {
						t.Fatal("invalid audit")
					}
					if row["tool"] != "devices.list" {
						continue
					}
					found = true
					if origin == "panel" && row["origin"] != "panel" {
						t.Fatal("panel origin missing", row)
					}
					if origin != "panel" && row["origin"] != nil {
						t.Fatal("unexpected origin", row)
					}
				}
			}
			if !found {
				t.Fatal("gateway audit missing")
			}
		})
	}
}
