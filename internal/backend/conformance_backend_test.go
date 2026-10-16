package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
	"github.com/tzf1003/xmesh/internal/lease"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

// TestConformanceBackendProcess is the stdio MCP backend used by the
// conformance tests in this file. It only runs when launched as a helper.
func TestConformanceBackendProcess(t *testing.T) {
	helper := false
	for _, arg := range os.Args {
		if arg == "--xmesh-conformance-helper" {
			helper = true
		}
	}
	if !helper {
		return
	}
	var inflight, maximum atomic.Int64
	server := mcp.NewServer(&mcp.Implementation{Name: "conformance-ida", Version: "1"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}})
	add := func(name string, handler func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error)) {
		server.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, handler)
	}
	ok := func(v map[string]any) (*mcp.CallToolResult, error) {
		b, _ := json.Marshal(v)
		return &mcp.CallToolResult{StructuredContent: v, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
	}
	sample := func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := r.Session.CreateMessage(ctx, &mcp.CreateMessageParams{MaxTokens: 5, Messages: []*mcp.SamplingMessage{{Role: "user", Content: &mcp.TextContent{Text: "hi"}}}})
		if err != nil {
			return ok(map[string]any{"sampled": "", "error": err.Error()})
		}
		text := ""
		if c, isText := res.Content.(*mcp.TextContent); isText {
			text = c.Text
		}
		return ok(map[string]any{"sampled": text})
	}
	// argsraw reports exactly what arrived over the wire.
	add("argsraw", func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		_ = json.Unmarshal(r.Params.Arguments, &args)
		keys := []string{}
		for k := range args {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return ok(map[string]any{"keys": keys, "raw": string(r.Params.Arguments)})
	})
	add("echo", func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args map[string]any
		_ = json.Unmarshal(r.Params.Arguments, &args)
		return ok(args)
	})
	add("probe", func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		now := inflight.Add(1)
		for {
			seen := maximum.Load()
			if now <= seen || maximum.CompareAndSwap(seen, now) {
				break
			}
		}
		time.Sleep(40 * time.Millisecond)
		inflight.Add(-1)
		return ok(map[string]any{"max": maximum.Load(), "pid": os.Getpid()})
	})
	add("crash_after", func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		time.Sleep(200 * time.Millisecond)
		os.Exit(23)
		return nil, nil
	})
	add("sample", sample)
	add("sample_after", func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		time.Sleep(400 * time.Millisecond)
		return sample(ctx, r)
	})
	if e := server.Run(context.Background(), &mcp.StdioTransport{}); e != nil {
		os.Exit(24)
	}
	os.Exit(0)
}

func conformanceCommand(t *testing.T) []string {
	t.Helper()
	bin, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	return []string{bin, "-test.run=TestConformanceBackendProcess", "--", "--xmesh-conformance-helper"}
}

// conformanceFixture starts one supervised stdio backend named "ida".
func conformanceFixture(t *testing.T, policy string, upstream func(string) *mcp.ServerSession, changed func()) (*Manager, *lease.Manager) {
	t.Helper()
	root := t.TempDir()
	root, _ = filepath.EvalSymlinks(root)
	leases := lease.New()
	t.Cleanup(leases.Close)
	c := &config.Config{}
	c.Agent.DefaultCWD = root
	c.Agent.CWDPolicy = cwd.Policy{Allow: []string{root}}
	c.Agent.Backends = map[string]config.Backend{"ida": {Transport: "stdio", Command: conformanceCommand(t), Policy: policy, IdleTimeout: "30m"}}
	m := New(c, leases, upstream, changed)
	t.Cleanup(m.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	m.WaitReady(ctx)
	if m.States()["ida"] != "running" {
		t.Fatal("conformance backend not running", m.States())
	}
	return m, leases
}

func errCode(err error) string {
	var e *toolerr.Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}
func errReason(err error) any {
	var e *toolerr.Error
	if errors.As(err, &e) {
		return e.Fields["reason"]
	}
	return nil
}
func structured(t *testing.T, out *mcp.CallToolResult) map[string]any {
	t.Helper()
	b, e := json.Marshal(out.StructuredContent)
	if e != nil {
		t.Fatal(e)
	}
	var v map[string]any
	if e = json.Unmarshal(b, &v); e != nil {
		t.Fatal(e)
	}
	return v
}

// conformance: CTR-BE-02
func Test_CTRBE02_AcquireRenewReleaseOnSharedAndPerCallerAreInvalid(t *testing.T) {
	for _, policy := range []string{"shared", "per-caller"} {
		t.Run(policy, func(t *testing.T) {
			m, _ := conformanceFixture(t, policy, nil, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			for _, name := range []string{"backend.acquire", "backend.renew", "backend.release"} {
				_, err := m.Control(ctx, "n/a", name, raw(map[string]any{"name": "ida", "ttl": "1m", "_lease_token": 1}))
				if errCode(err) != "invalid_args" {
					t.Fatalf("%s on %s backend: %v", name, policy, err)
				}
			}
		})
	}
}

func Test_CTRBE02_AcquireRenewRelease(t *testing.T) {
	m, leases := conformanceFixture(t, "exclusive", nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	control := func(caller, name string, args map[string]any) (any, error) {
		args["name"] = "ida"
		return m.Control(ctx, caller, name, raw(args))
	}
	// ttl is mandatory and bounded by the backend idle_timeout (30m here).
	for _, ttl := range []any{nil, "", "0s", "-5s", "garbage", 60, "31m"} {
		args := map[string]any{}
		if ttl != nil {
			args["ttl"] = ttl
		}
		if _, err := control("n/a", "backend.acquire", args); errCode(err) != "invalid_args" {
			t.Fatalf("acquire ttl=%v: %v", ttl, err)
		}
	}
	if leases.Owner("backend:ida") != "" {
		t.Fatal("rejected acquire created a lease")
	}
	// Free resource: create with token 1 and owner = caller.
	before := time.Now()
	v, err := control("n/a", "backend.acquire", map[string]any{"ttl": "1m"})
	if err != nil {
		t.Fatal(err)
	}
	first := v.(lease.Lease)
	if first.Resource != "backend:ida" || first.Owner != "n/a" || first.Token != 1 {
		t.Fatalf("lease object %+v", first)
	}
	if d := first.Expires.Sub(before); d < 59*time.Second || d > 61*time.Second {
		t.Fatalf("expires %v is not now+ttl", d)
	}
	// Retry by the owner is idempotent: same token, ttl restarted from now.
	v, err = control("n/a", "backend.acquire", map[string]any{"ttl": "2m"})
	if err != nil {
		t.Fatal(err)
	}
	again := v.(lease.Lease)
	if again.Token != first.Token || !again.Expires.After(first.Expires.Add(30*time.Second)) {
		t.Fatalf("idempotent acquire changed lease: %+v -> %+v", first, again)
	}
	// Another caller is refused with reason=held and the holder is named.
	_, err = control("n/b", "backend.acquire", map[string]any{"ttl": "1m"})
	var busy *toolerr.Error
	if !errors.As(err, &busy) || busy.Code != "resource_busy" || busy.Fields["reason"] != "held" || busy.Fields["held_by"] != "n/a" {
		t.Fatalf("second acquire: %v", err)
	}
	// renew: _lease_token is mandatory and must be well formed.
	for _, args := range []map[string]any{{"ttl": "1m"}, {"ttl": "1m", "_lease_token": "abc"}, {"ttl": "1m", "_lease_token": 0}, {"ttl": "1m", "token": first.Token}} {
		if _, err = control("n/a", "backend.renew", args); errCode(err) != "invalid_args" {
			t.Fatalf("renew %v: %v", args, err)
		}
	}
	if _, err = control("n/a", "backend.renew", map[string]any{"_lease_token": first.Token}); errCode(err) != "invalid_args" {
		t.Fatalf("renew without ttl: %v", err)
	}
	if _, err = control("n/a", "backend.renew", map[string]any{"ttl": "1m", "_lease_token": first.Token + 7}); errCode(err) != "resource_busy" || errReason(err) != "token_stale" {
		t.Fatalf("renew wrong token: %v", err)
	}
	if _, err = control("n/b", "backend.renew", map[string]any{"ttl": "1m", "_lease_token": first.Token}); errCode(err) != "resource_busy" || errReason(err) != "token_mismatch" {
		t.Fatalf("renew by non-owner: %v", err)
	}
	// Both the decimal-string and the JSON-integer spellings are accepted; the
	// token never changes and Expires is moved to now+ttl.
	for _, spelling := range []any{first.Token, "1"} {
		v, err = control("n/a", "backend.renew", map[string]any{"ttl": "5m", "_lease_token": spelling})
		if err != nil {
			t.Fatalf("renew %v: %v", spelling, err)
		}
		renewed := v.(lease.Lease)
		if renewed.Token != first.Token || renewed.Owner != "n/a" || !renewed.Expires.After(again.Expires.Add(2*time.Minute)) {
			t.Fatalf("renewed lease %+v", renewed)
		}
	}
	// release: both owner and _lease_token must match.
	for _, args := range []map[string]any{{}, {"token": first.Token}, {"_lease_token": "x"}} {
		if _, err = control("n/a", "backend.release", args); errCode(err) != "invalid_args" {
			t.Fatalf("release %v: %v", args, err)
		}
	}
	if _, err = control("n/b", "backend.release", map[string]any{"_lease_token": first.Token}); errCode(err) != "resource_busy" {
		t.Fatalf("release by non-owner: %v", err)
	}
	if _, err = control("n/a", "backend.release", map[string]any{"_lease_token": first.Token + 1}); errCode(err) != "resource_busy" {
		t.Fatalf("release wrong token: %v", err)
	}
	if leases.Owner("backend:ida") != "n/a" {
		t.Fatal("failed release removed the lease")
	}
	for attempt := 0; attempt < 2; attempt++ { // repeating the same release succeeds
		v, err = control("n/a", "backend.release", map[string]any{"_lease_token": first.Token})
		if err != nil || v.(map[string]any)["released"] != true {
			t.Fatalf("release attempt %d: %v %v", attempt, v, err)
		}
	}
	if leases.Owner("backend:ida") != "" {
		t.Fatal("release left the lease")
	}
	// The counter is never decreased: the next lease has a larger token, and the
	// released token is not usable on the business tool.
	v, err = control("n/b", "backend.acquire", map[string]any{"ttl": "1m"})
	if err != nil {
		t.Fatal(err)
	}
	second := v.(lease.Lease)
	if second.Token <= first.Token || second.Owner != "n/b" {
		t.Fatalf("token reused or owner wrong: %+v after %+v", second, first)
	}
	if _, err = control("n/a", "backend.release", map[string]any{"_lease_token": first.Token}); errCode(err) != "resource_busy" {
		t.Fatalf("stale release after new holder: %v", err)
	}

	// Business-tool validation table of CTR-BE-02.
	call := func(caller string, args map[string]any) error {
		_, err := m.Call(ctx, caller, "ida.echo", raw(args), mcpmux.Progress{})
		return err
	}
	if err = call("n/b", map[string]any{}); errCode(err) != "lease_required" {
		t.Fatalf("no token: %v", err)
	}
	if err = call("n/b", map[string]any{"_lease_token": second.Token + 9}); errCode(err) != "resource_busy" || errReason(err) != "token_stale" {
		t.Fatalf("wrong token: %v", err)
	}
	if err = call("n/b", map[string]any{"_lease_token": first.Token}); errCode(err) != "resource_busy" || errReason(err) != "token_stale" {
		t.Fatalf("released token: %v", err)
	}
	if err = call("n/a", map[string]any{"_lease_token": second.Token}); errCode(err) != "resource_busy" || errReason(err) != "token_mismatch" {
		t.Fatalf("current token by non-owner: %v", err)
	}
	if err = call("n/b", map[string]any{"_lease_token": second.Token}); err != nil {
		t.Fatalf("valid call: %v", err)
	}
	if err = call("n/b", map[string]any{"_lease_token": fmt.Sprint(second.Token)}); err != nil {
		t.Fatalf("decimal-string token: %v", err)
	}
	// An expired lease with the right token is token_stale and is deleted.
	if _, err = control("n/b", "backend.release", map[string]any{"_lease_token": second.Token}); err != nil {
		t.Fatal(err)
	}
	v, err = control("n/a", "backend.acquire", map[string]any{"ttl": "150ms"})
	if err != nil {
		t.Fatal(err)
	}
	short := v.(lease.Lease)
	time.Sleep(350 * time.Millisecond)
	if err = call("n/a", map[string]any{"_lease_token": short.Token}); errCode(err) != "resource_busy" || errReason(err) != "token_stale" {
		t.Fatalf("expired token: %v", err)
	}
	if leases.Owner("backend:ida") != "" {
		t.Fatal("expired lease not deleted")
	}
}

// conformance: RUL-03
func Test_RUL03_BusinessTrafficDoesNotRenewLease(t *testing.T) {
	m, _ := conformanceFixture(t, "exclusive", nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	start := time.Now()
	v, err := m.Control(ctx, "n/a", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1s"}))
	if err != nil {
		t.Fatal(err)
	}
	granted := v.(lease.Lease)
	successes := 0
	var failure error
	for time.Since(start) < 4*time.Second {
		_, failure = m.Call(ctx, "n/a", "ida.echo", raw(map[string]any{"_lease_token": granted.Token}), mcpmux.Progress{})
		if failure != nil {
			break
		}
		successes++
		time.Sleep(50 * time.Millisecond)
	}
	if failure == nil {
		t.Fatal("successful business calls kept extending the lease")
	}
	if successes == 0 {
		t.Fatal("lease expired before any call could be made; the test cannot tell")
	}
	if errCode(failure) != "resource_busy" || errReason(failure) != "token_stale" {
		t.Fatalf("expiry error: %v", failure)
	}
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Fatalf("lease ended after %v, before its 1s ttl", elapsed)
	}
	// Read-only probing does not keep another holder's lease alive either, and
	// an explicit renew is the only thing that moves Expires.
	v, err = m.Control(ctx, "n/a", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "400ms"}))
	if err != nil {
		t.Fatal(err)
	}
	second := v.(lease.Lease)
	time.Sleep(150 * time.Millisecond)
	if _, err = m.Call(ctx, "n/a", "ida.echo", raw(map[string]any{"_lease_token": second.Token}), mcpmux.Progress{}); err != nil {
		t.Fatal(err)
	}
	renewed, err := m.Control(ctx, "n/a", "backend.renew", raw(map[string]any{"name": "ida", "ttl": "5s", "_lease_token": second.Token}))
	if err != nil {
		t.Fatal(err)
	}
	if !renewed.(lease.Lease).Expires.After(second.Expires.Add(3 * time.Second)) {
		t.Fatal("explicit renew did not extend Expires")
	}
}

// conformance: INV-04 CON-TOOL-10 CON-TOOL-11
func Test_INV04_NoLeaseFieldsReachBackend(t *testing.T) {
	exact := func(t *testing.T, out *mcp.CallToolResult) {
		t.Helper()
		got := structured(t, out)
		keys := []string{}
		for _, k := range got["keys"].([]any) {
			keys = append(keys, k.(string))
		}
		// Only the caller's business fields, byte for byte; nothing injected.
		if strings.Join(keys, ",") != "token,value" {
			t.Fatalf("backend received keys %v (%s)", keys, got["raw"])
		}
		var args map[string]any
		if err := json.Unmarshal([]byte(got["raw"].(string)), &args); err != nil || args["token"] != "ordinary" || args["value"] != "v" {
			t.Fatalf("business fields rewritten: %s", got["raw"])
		}
		if strings.Contains(got["raw"].(string), "lease") {
			t.Fatalf("lease field reached backend: %s", got["raw"])
		}
	}
	for _, policy := range []string{"exclusive", "shared", "per-caller"} {
		t.Run("stdio/"+policy, func(t *testing.T) {
			m, _ := conformanceFixture(t, policy, nil, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			args := map[string]any{"token": "ordinary", "value": "v"}
			if policy == "exclusive" {
				v, err := m.Control(ctx, "n/a", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1m"}))
				if err != nil {
					t.Fatal(err)
				}
				args["_lease_token"] = v.(lease.Lease).Token
			} else {
				// shared and per-caller strip a token the caller chose to send.
				args["_lease_token"] = "ignored"
			}
			out, err := m.Call(ctx, "n/a", "ida.argsraw", raw(args), mcpmux.Progress{})
			if err != nil {
				t.Fatal(err)
			}
			exact(t, out)
		})
	}
	t.Run("http/exclusive", func(t *testing.T) {
		server := mcp.NewServer(&mcp.Implementation{Name: "http-ida", Version: "1"}, nil)
		var received sync.Map
		server.AddTool(&mcp.Tool{Name: "argsraw", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			received.Store("raw", string(r.Params.Arguments))
			var args map[string]any
			_ = json.Unmarshal(r.Params.Arguments, &args)
			keys := []string{}
			for k := range args {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			v := map[string]any{"keys": keys, "raw": string(r.Params.Arguments)}
			b, _ := json.Marshal(v)
			return &mcp.CallToolResult{StructuredContent: v, Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil
		})
		httpBackend := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil))
		t.Cleanup(httpBackend.Close)
		root, _ := filepath.EvalSymlinks(t.TempDir())
		leases := lease.New()
		t.Cleanup(leases.Close)
		c := &config.Config{}
		c.Agent.DefaultCWD = root
		c.Agent.CWDPolicy = cwd.Policy{Allow: []string{root}}
		c.Agent.Backends = map[string]config.Backend{"ida": {Transport: "http", URL: httpBackend.URL, Policy: "exclusive", IdleTimeout: "30m"}}
		m := New(c, leases, nil, nil)
		t.Cleanup(m.Close)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		m.WaitReady(ctx)
		v, err := m.Control(ctx, "n/a", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1m"}))
		if err != nil {
			t.Fatal(err)
		}
		out, err := m.Call(ctx, "n/a", "ida.argsraw", raw(map[string]any{"token": "ordinary", "value": "v", "_lease_token": v.(lease.Lease).Token}), mcpmux.Progress{})
		if err != nil {
			t.Fatal(err)
		}
		exact(t, out)
	})
}

// conformance: CON-CONC-06
func Test_CONCONC06_ActorSerializesBackendRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Control: the helper does observe overlap when it is driven directly, so a
	// maximum of 1 below is a property of the actor and not of the helper.
	direct, err := mcpmux.Connect(ctx, &mcp.CommandTransport{Command: exec.Command(conformanceCommand(t)[0], conformanceCommand(t)[1:]...)}, mcpmux.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer direct.Close()
	var wg sync.WaitGroup
	var overlap atomic.Int64
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := direct.Call(ctx, "probe", map[string]any{}, mcpmux.Progress{})
			if err != nil {
				t.Error(err)
				return
			}
			if v := int64(structured(t, out)["max"].(float64)); v > overlap.Load() {
				overlap.Store(v)
			}
		}()
	}
	wg.Wait()
	if overlap.Load() < 2 {
		t.Fatalf("control run saw no concurrent requests (max=%d); the probe cannot detect parallel writes", overlap.Load())
	}

	probe := func(t *testing.T, m *Manager, callers []string, token func(string) any) (map[string]bool, int64) {
		t.Helper()
		var mu sync.Mutex
		pids := map[string]bool{}
		largest := int64(0)
		var group sync.WaitGroup
		for _, caller := range callers {
			for range 4 {
				group.Add(1)
				go func() {
					defer group.Done()
					args := map[string]any{}
					if token != nil {
						args["_lease_token"] = token(caller)
					}
					out, err := m.Call(ctx, caller, "ida.probe", raw(args), mcpmux.Progress{})
					if err != nil {
						t.Error(err)
						return
					}
					v := structured(t, out)
					mu.Lock()
					defer mu.Unlock()
					pids[fmt.Sprint(v["pid"])] = true
					if m := int64(v["max"].(float64)); m > largest {
						largest = m
					}
				}()
			}
		}
		group.Wait()
		return pids, largest
	}
	t.Run("shared", func(t *testing.T) {
		m, _ := conformanceFixture(t, "shared", nil, nil)
		pids, largest := probe(t, m, []string{"n/a", "n/b", "n/c"}, nil)
		if largest != 1 || len(pids) != 1 {
			t.Fatalf("shared backend saw %d concurrent requests across %d processes", largest, len(pids))
		}
	})
	t.Run("exclusive", func(t *testing.T) {
		m, _ := conformanceFixture(t, "exclusive", nil, nil)
		v, err := m.Control(ctx, "n/a", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1m"}))
		if err != nil {
			t.Fatal(err)
		}
		token := v.(lease.Lease).Token
		_, largest := probe(t, m, []string{"n/a"}, func(string) any { return token })
		if largest != 1 {
			t.Fatalf("exclusive backend saw %d concurrent requests", largest)
		}
	})
	t.Run("per-caller", func(t *testing.T) {
		m, _ := conformanceFixture(t, "per-caller", nil, nil)
		pids, largest := probe(t, m, []string{"n/a", "n/b"}, nil)
		if largest != 1 {
			t.Fatalf("a caller's backend saw %d concurrent requests", largest)
		}
		if len(pids) != 2 {
			t.Fatalf("per-caller backends must be one process per caller, saw %d", len(pids))
		}
	})
}

// upstreamWith returns an upstream MCP server session whose client answers
// sampling requests with the given text.
func upstreamWith(t *testing.T, answer string) *mcp.ServerSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	server := mcp.NewServer(&mcp.Implementation{Name: "upstream-" + answer, Version: "1"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2025-11-25"}})
	client := mcp.NewClient(&mcp.Implementation{Name: "client-" + answer, Version: "1"}, &mcp.ClientOptions{
		CreateMessageHandler: func(context.Context, *mcp.CreateMessageRequest) (*mcp.CreateMessageResult, error) {
			return &mcp.CreateMessageResult{Role: "assistant", Model: "fixture", Content: &mcp.TextContent{Text: answer}}, nil
		},
	})
	serverSide, clientSide := mcp.NewInMemoryTransports()
	session, err := server.Connect(ctx, serverSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := client.Connect(ctx, clientSide, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close(); _ = session.Close() })
	return session
}

// conformance: CTR-MUX-03
func Test_CTRMUX03_SamplingRoutedOnlyToTheOwner(t *testing.T) {
	sessions := map[string]*mcp.ServerSession{"n/a": upstreamWith(t, "from-a"), "n/b": upstreamWith(t, "from-b")}
	upstream := func(caller string) *mcp.ServerSession { return sessions[caller] }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t.Run("exclusive/holder", func(t *testing.T) {
		m, _ := conformanceFixture(t, "exclusive", upstream, nil)
		v, err := m.Control(ctx, "n/b", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1m"}))
		if err != nil {
			t.Fatal(err)
		}
		out, err := m.Call(ctx, "n/b", "ida.sample", raw(map[string]any{"_lease_token": v.(lease.Lease).Token}), mcpmux.Progress{})
		if err != nil {
			t.Fatal(err)
		}
		if got := structured(t, out); got["sampled"] != "from-b" {
			t.Fatalf("sampling went to %v", got)
		}
	})
	t.Run("exclusive/no holder", func(t *testing.T) {
		m, leases := conformanceFixture(t, "exclusive", upstream, nil)
		v, err := m.Control(ctx, "n/a", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1m"}))
		if err != nil {
			t.Fatal(err)
		}
		token := v.(lease.Lease).Token
		done := make(chan *mcp.CallToolResult, 1)
		go func() {
			out, err := m.Call(ctx, "n/a", "ida.sample_after", raw(map[string]any{"_lease_token": token}), mcpmux.Progress{})
			if err != nil {
				t.Error(err)
			}
			done <- out
		}()
		time.Sleep(100 * time.Millisecond)
		if _, err = m.Control(ctx, "n/a", "backend.release", raw(map[string]any{"name": "ida", "_lease_token": token})); err != nil {
			t.Fatal(err)
		}
		if leases.Owner("backend:ida") != "" {
			t.Fatal("lease still held")
		}
		out := <-done
		if out == nil {
			t.Fatal("no result")
		}
		got := structured(t, out)
		if got["sampled"] != "" || got["error"] == nil || got["error"] == "" {
			t.Fatalf("holderless exclusive backend was allowed to sample: %v", got)
		}
	})
	t.Run("shared", func(t *testing.T) {
		m, _ := conformanceFixture(t, "shared", upstream, nil)
		out, err := m.Call(ctx, "n/a", "ida.sample", raw(map[string]any{}), mcpmux.Progress{})
		if err != nil {
			t.Fatal(err)
		}
		if got := structured(t, out); got["sampled"] != "" || got["error"] == nil {
			t.Fatalf("shared backend sampled an arbitrary upstream: %v", got)
		}
	})
	t.Run("per-caller", func(t *testing.T) {
		m, _ := conformanceFixture(t, "per-caller", upstream, nil)
		for caller, want := range map[string]string{"n/a": "from-a", "n/b": "from-b"} {
			out, err := m.Call(ctx, caller, "ida.sample", raw(map[string]any{}), mcpmux.Progress{})
			if err != nil {
				t.Fatal(err)
			}
			if got := structured(t, out); got["sampled"] != want {
				t.Fatalf("caller %s got %v", caller, got)
			}
		}
	})
}

// conformance: CTR-MUX-04 CTR-BE-01
func Test_CTRMUX04_CrashFailsAllWaitersAndFencesOldToken(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	waitRunning := func(t *testing.T, m *Manager) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if m.States()["ida"] == "running" {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("backend was not restarted", m.States())
	}
	t.Run("shared/both upstreams", func(t *testing.T) {
		var changes atomic.Int32
		m, _ := conformanceFixture(t, "shared", nil, func() { changes.Add(1) })
		baseline := changes.Load()
		results := make(chan error, 2)
		go func() {
			_, err := m.Call(ctx, "n/a", "ida.crash_after", raw(map[string]any{}), mcpmux.Progress{})
			results <- err
		}()
		time.Sleep(60 * time.Millisecond)
		go func() {
			_, err := m.Call(ctx, "n/b", "ida.echo", raw(map[string]any{}), mcpmux.Progress{})
			results <- err
		}()
		start := time.Now()
		for range 2 {
			select {
			case err := <-results:
				if errCode(err) != "backend_unavailable" {
					t.Fatalf("an upstream waiting on the crashed backend got %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("an upstream is still hanging on the crashed backend")
			}
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("upstreams were failed only at their own timeout")
		}
		waitRunning(t, m)
		if changes.Load()-baseline < 2 {
			t.Fatalf("crash and restart produced %d tools/list_changed triggers", changes.Load()-baseline)
		}
		if out, err := m.Call(ctx, "n/b", "ida.echo", raw(map[string]any{}), mcpmux.Progress{}); err != nil || out.IsError {
			t.Fatalf("restarted backend unusable: %v", err)
		}
	})
	t.Run("exclusive/fencing", func(t *testing.T) {
		m, leases := conformanceFixture(t, "exclusive", nil, nil)
		v, err := m.Control(ctx, "n/a", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "5m"}))
		if err != nil {
			t.Fatal(err)
		}
		old := v.(lease.Lease).Token
		results := make(chan error, 2)
		go func() {
			_, err := m.Call(ctx, "n/a", "ida.crash_after", raw(map[string]any{"_lease_token": old}), mcpmux.Progress{})
			results <- err
		}()
		time.Sleep(60 * time.Millisecond)
		go func() {
			_, err := m.Call(ctx, "n/a", "ida.echo", raw(map[string]any{"_lease_token": old}), mcpmux.Progress{})
			results <- err
		}()
		var first error
		select {
		case first = <-results:
		case <-time.After(10 * time.Second):
			t.Fatal("holder is hanging on the crashed backend")
		}
		// The first error returned to anybody must already find the old token dead.
		if leases.Validate("backend:ida", "n/a", old) == nil {
			t.Fatal("old token still valid when the crash was reported")
		}
		second := first
		select {
		case second = <-results:
		case <-time.After(10 * time.Second):
			t.Fatal("queued request is hanging on the crashed backend")
		}
		for _, err := range []error{first, second} {
			if errCode(err) != "backend_unavailable" && !(errCode(err) == "resource_busy" && errReason(err) == "token_stale") {
				t.Fatalf("request around the crash ended with %v", err)
			}
		}
		if errCode(first) != "backend_unavailable" && errCode(second) != "backend_unavailable" {
			t.Fatalf("nobody was told backend_unavailable: %v / %v", first, second)
		}
		waitRunning(t, m)
		fresh, err := m.Control(ctx, "n/b", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1m"}))
		for deadline := time.Now().Add(5 * time.Second); err != nil && time.Now().Before(deadline); {
			time.Sleep(20 * time.Millisecond)
			fresh, err = m.Control(ctx, "n/b", "backend.acquire", raw(map[string]any{"name": "ida", "ttl": "1m"}))
		}
		if err != nil {
			t.Fatal(err)
		}
		if fresh.(lease.Lease).Token <= old {
			t.Fatalf("new acquire reused a token: %d after %d", fresh.(lease.Lease).Token, old)
		}
		if _, err = m.Call(ctx, "n/a", "ida.echo", raw(map[string]any{"_lease_token": old}), mcpmux.Progress{}); errCode(err) != "resource_busy" || errReason(err) != "token_stale" {
			t.Fatalf("pre-crash token after restart: %v", err)
		}
	})
}
