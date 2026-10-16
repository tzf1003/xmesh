package mcpmux

import (
	"context"
	"encoding/json"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// conformance: CTR-MUX-01
func Test_CTRMUX01_SplitAndPrefixStripOnlyOutermost(t *testing.T) {
	prefix, tool, err := Split("win-dev-01/ida.decompile_function", "/")
	if err != nil || prefix != "win-dev-01" || tool != "ida.decompile_function" {
		t.Fatal(prefix, tool, err)
	}
	// Only the outermost prefix is stripped; the remainder keeps any separators.
	prefix, tool, err = Split("a/b/c", "/")
	if err != nil || prefix != "a" || tool != "b/c" {
		t.Fatal(prefix, tool, err)
	}
	backend, original, err := Split("ida.decompile_function", ".")
	if err != nil || backend != "ida" || original != "decompile_function" {
		t.Fatal(backend, original, err)
	}
	// The first "." splits the backend key, so a tool name may contain more dots.
	backend, original, err = Split("ida.ns.tool", ".")
	if err != nil || backend != "ida" || original != "ns.tool" {
		t.Fatal(backend, original, err)
	}
	for _, name := range []string{"fs_read", "/fs.read"} {
		if _, _, err = Split(name, "/"); err == nil || err.(*toolerr.Error).Code != "not_found" {
			t.Fatalf("%q: want not_found, got %v", name, err)
		}
	}
	if _, _, err = Split("win-dev-01/", "/"); err == nil || err.(*toolerr.Error).Code != "invalid_args" {
		t.Fatalf("empty remainder: want invalid_args, got %v", err)
	}
	if Prefix("local", "fs.read", "/") != "local/fs.read" || Prefix("ida", "echo", ".") != "ida.echo" {
		t.Fatal("prefix")
	}
	if renamed := Rename(&mcp.Tool{Name: "fs.read"}, "local/fs.read"); renamed.Name != "local/fs.read" {
		t.Fatal(renamed.Name)
	}
}

// conformance: CTR-MUX-01 CON-CONC-12
func Test_CTRMUX01_TwoUpstreamsUsingTheSameIDAreNotCrossed(t *testing.T) {
	ctx := context.Background()
	mapper := NewMapper()
	type pair struct {
		up   mcp.Connection
		down mcp.Connection
	}
	open := func(upstream string) pair {
		a, b := net.Pipe()
		up, err := (&Transport{Base: &mcp.IOTransport{Reader: a, Writer: a}, Mapper: mapper, Upstream: upstream}).Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		down, err := (&mcp.IOTransport{Reader: b, Writer: b}).Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { up.Close(); down.Close() })
		return pair{up, down}
	}
	left, right := open("a"), open("b")
	id, _ := jsonrpc.MakeID(float64(1))
	downIDs := map[string]jsonrpc.ID{}
	for name, p := range map[string]pair{"a": left, "b": right} {
		name, p := name, p
		errc := make(chan error, 1)
		go func() {
			errc <- p.up.Write(ctx, &jsonrpc.Request{ID: id, Method: "tools/call", Params: json.RawMessage(`{"name":"` + name + `"}`)})
		}()
		msg, err := p.down.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = <-errc; err != nil {
			t.Fatal(err)
		}
		downIDs[name] = msg.(*jsonrpc.Request).ID
	}
	if downIDs["a"] == downIDs["b"] || downIDs["a"] == id || downIDs["b"] == id {
		t.Fatalf("downstream ids collide or were not remapped: %v", downIDs)
	}
	// Answer in the opposite order; each upstream sees only its own answer, under id 1.
	var wg sync.WaitGroup
	for _, name := range []string{"b", "a"} {
		p := map[string]pair{"a": left, "b": right}[name]
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := p.down.Write(ctx, &jsonrpc.Response{ID: downIDs[name], Result: json.RawMessage(`{"who":"` + name + `"}`)}); err != nil {
				t.Error(err)
			}
		}()
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		msg, err := p.up.Read(readCtx)
		cancel()
		if err != nil {
			t.Fatal(name, err)
		}
		response := msg.(*jsonrpc.Response)
		if response.ID != id || string(response.Result) != `{"who":"`+name+`"}` {
			t.Fatalf("upstream %s received %v %s", name, response.ID, response.Result)
		}
	}
	wg.Wait()
	// A response that arrives after the upstream disconnected is dropped.
	late := open("c")
	errc := make(chan error, 1)
	go func() {
		errc <- late.up.Write(ctx, &jsonrpc.Request{ID: id, Method: "tools/call", Params: json.RawMessage(`{}`)})
	}()
	msg, err := late.down.Read(ctx)
	if err != nil || <-errc != nil {
		t.Fatal(err)
	}
	mapper.Disconnect("c")
	if _, _, ok := mapper.Up(msg.(*jsonrpc.Request).ID); ok {
		t.Fatal("pending mapping survived upstream disconnect")
	}
}

// conformance: CTR-MUX-02
func Test_CTRMUX02_UnknownProgressTokenIsDroppedNotBroadcast(t *testing.T) {
	left, right := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	server := mcp.NewServer(&mcp.Implementation{Name: "progress", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "stray", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = r.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: "never-registered", Progress: 1})
		_ = r.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: r.Params.GetProgressToken(), Progress: 2})
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	ss, err := server.Connect(ctx, &mcp.IOTransport{Reader: left, Writer: left}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	bridge, err := Connect(ctx, &mcp.IOTransport{Reader: right, Writer: right}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	var mu sync.Mutex
	received := map[string][]float64{}
	record := func(who string) Progress {
		return Progress{Original: who, Send: func(_ context.Context, p *mcp.ProgressNotificationParams) error {
			mu.Lock()
			defer mu.Unlock()
			received[who] = append(received[who], p.Progress)
			return nil
		}}
	}
	var wg sync.WaitGroup
	for _, who := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := bridge.Call(ctx, "stray", map[string]any{}, record(who)); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	for _, who := range []string{"first", "second"} {
		if len(received[who]) != 1 || received[who][0] != 2 {
			t.Fatalf("%s received %v; the unregistered-token notification must be dropped and the real one delivered once", who, received[who])
		}
	}
}

// conformance: CON-IMPL-02
func Test_CONIMPL02_OnlyMcpmuxMapsJSONRPCIDs(t *testing.T) {
	// ID remapping, progress remapping and prefix splitting live in this
	// package. The roles and the backend supervisor must reach them through the
	// package API: they may not import the JSON-RPC layer to rewrite ids.
	roots := []string{"../role", "../backend", "../app"}
	checked := 0
	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				return err
			}
			checked++
			for _, imp := range parsed.Imports {
				if strings.Trim(imp.Path.Value, `"`) == "github.com/modelcontextprotocol/go-sdk/jsonrpc" {
					t.Errorf("%s imports the jsonrpc package; id mapping belongs to internal/mcpmux", path)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if checked < 5 {
		t.Fatalf("scanned only %d role/backend files", checked)
	}
}
