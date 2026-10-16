package mcpmux

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"net"
	"sync"
	"testing"
	"time"
)

func Test_CTRMUX01_SameUpstreamIDs(t *testing.T) {
	m := NewMapper()
	id, _ := jsonrpc.MakeID(float64(1))
	a := m.Down("a", id)
	b := m.Down("b", id)
	if a == b {
		t.Fatal("collision")
	}
	up, got, ok := m.Up(b)
	if !ok || up != "b" || got != id {
		t.Fatal(up, got, ok)
	}
	m.Disconnect("a")
	if _, _, ok = m.Up(a); ok {
		t.Fatal("disconnected mapping remained")
	}
}
func Test_CTRMUX01_TransportPipes(t *testing.T) {
	a, b := net.Pipe()
	ctx := context.Background()
	up, e := (&Transport{Base: &mcp.IOTransport{Reader: a, Writer: a}, Mapper: NewMapper(), Upstream: "a"}).Connect(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer up.Close()
	down, e := (&mcp.IOTransport{Reader: b, Writer: b}).Connect(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer down.Close()
	id, _ := jsonrpc.MakeID("original")
	errc := make(chan error, 1)
	go func() { errc <- up.Write(ctx, &jsonrpc.Request{ID: id, Method: "test", Params: json.RawMessage(`{}`)}) }()
	request, e := down.Read(ctx)
	if e != nil {
		t.Fatal(e)
	}
	req := request.(*jsonrpc.Request)
	if req.ID == id {
		t.Fatal("ID not remapped")
	}
	if e = <-errc; e != nil {
		t.Fatal(e)
	}
	go func() { errc <- down.Write(ctx, &jsonrpc.Response{ID: req.ID, Result: json.RawMessage(`{}`)}) }()
	response, e := up.Read(ctx)
	if e != nil || response.(*jsonrpc.Response).ID != id {
		t.Fatal(response, e)
	}
	if e = <-errc; e != nil {
		t.Fatal(e)
	}
}
func Test_CTRMUX02_ProgressIsolationAndReverseDeny(t *testing.T) {
	left, right := net.Pipe()
	server := mcp.NewServer(&mcp.Implementation{Name: "mock", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "progress", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		_ = r.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: r.Params.GetProgressToken(), Progress: 1})
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ss, e := server.Connect(ctx, &mcp.IOTransport{Reader: left, Writer: left}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer ss.Close()
	b, e := Connect(ctx, &mcp.IOTransport{Reader: right, Writer: right}, Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	var wg sync.WaitGroup
	var mu sync.Mutex
	received := map[string]int{}
	for _, caller := range []string{"a", "b"} {
		caller := caller
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := b.Call(ctx, "progress", map[string]any{}, Progress{Original: caller, Send: func(_ context.Context, p *mcp.ProgressNotificationParams) error {
				mu.Lock()
				defer mu.Unlock()
				if p.ProgressToken != caller {
					t.Error("progress crossed caller")
				}
				received[caller]++
				return nil
			}})
			if e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if received["a"] != 1 || received["b"] != 1 {
		t.Fatal(received)
	}
	_, e = ss.CreateMessage(ctx, &mcp.CreateMessageParams{MaxTokens: 10, Messages: []*mcp.SamplingMessage{}})
	if e == nil {
		t.Fatal("ownerless sampling accepted")
	}
}

func Test_CTRMUX02_ResponseWaitsForProgressForwarding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	left, right := net.Pipe()
	server := mcp.NewServer(&mcp.Implementation{Name: "progress-order", Version: "1"}, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	releaseForwarding := sync.OnceFunc(func() { close(release) })
	server.AddTool(&mcp.Tool{Name: "progress", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if err := r.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: r.Params.GetProgressToken(), Progress: 1}); err != nil {
			return nil, err
		}
		select {
		case <-entered:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	ss, err := server.Connect(ctx, &mcp.IOTransport{Reader: left, Writer: left}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	b, err := Connect(ctx, &mcp.IOTransport{Reader: right, Writer: right}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	defer releaseForwarding()
	// A callback can query the same backend while forwarding a notification.
	// Its completion remains part of the original call's lifetime.
	queried := make(chan error, 1)
	done := make(chan error, 1)
	go func() {
		_, err := b.Call(ctx, "progress", map[string]any{}, Progress{Original: "owner", Send: func(ctx context.Context, _ *mcp.ProgressNotificationParams) error {
			close(entered)
			_, err := b.Tools(ctx)
			queried <- err
			select {
			case <-release:
			case <-ctx.Done():
			}
			return err
		}})
		done <- err
	}()
	select {
	case err := <-queried:
		if err != nil {
			t.Fatal("backend reader blocked by progress forwarding", err)
		}
	case <-ctx.Done():
		t.Fatal("progress callback could not query backend", ctx.Err())
	}
	select {
	case err := <-done:
		t.Fatal("call completed before progress forwarding", err)
	case <-time.After(50 * time.Millisecond):
	}
	releaseForwarding()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("call did not complete after progress forwarding", ctx.Err())
	}
}
