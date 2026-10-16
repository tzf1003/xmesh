package app

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
)

func agentOnlyStore(t *testing.T) (*config.Store, string) {
	t.Helper()
	root, _ := filepath.EvalSymlinks(t.TempDir())
	roles := []string{"agent"}
	doc := map[string]any{"node": map[string]any{"name": "net-node", "roles": roles}, "mesh": map[string]any{"mode": "lan", "lan": map[string]bool{"mdns": true}}, "agent": map[string]any{"default_cwd": root, "cwd_policy": map[string]any{"allow": []string{root}}, "authz": map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{"*"}}}}}
	body, _ := json.Marshal(doc)
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	return store, root
}

// conformance: CON-NET-01 CON-NET-02
func Test_CONNET01_AgentRefusesPublicListenAddresses(t *testing.T) {
	store, root := agentOnlyStore(t)
	for _, listen := range []string{"0.0.0.0:0", "[::]:0", ":0", "192.0.2.77:7433", "10.1.2.3:7433"} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := Run(ctx, store, Options{Local: true, Listen: listen, StateDir: filepath.Join(root, "state")})
		cancel()
		if err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
			t.Fatalf("local Agent accepted listen address %q: %v", listen, err)
		}
	}
}

// conformance: CON-NET-01 CON-NET-02
func Test_CONNET01_LoopbackAgentIsNotReachableOnOtherAddresses(t *testing.T) {
	store, root := agentOnlyStore(t)
	reserve, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := reserve.Addr().(*net.TCPAddr).Port
	reserve.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, store, Options{Local: true, Listen: net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), StateDir: filepath.Join(root, "state")})
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("runtime shutdown hung")
		}
	}()
	reachable := func(host string) bool {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
		if err == nil {
			c.Close()
		}
		return err == nil
	}
	deadline := time.Now().Add(10 * time.Second)
	for !reachable("127.0.0.1") {
		if time.Now().After(deadline) {
			t.Fatal("agent never listened on loopback")
		}
		time.Sleep(20 * time.Millisecond)
	}
	addresses, _ := net.InterfaceAddrs()
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err != nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		if reachable(ip.String()) {
			t.Fatalf("agent port is reachable on %s, not only on loopback", ip)
		}
	}
}

// conformance: CON-NET-01 CON-NET-03
func Test_CONNET01_NoWildcardListenInAgentNetworkCode(t *testing.T) {
	// Every net.Listen literal in the runtime and mesh packages: loopback, or the
	// ingress role's :443. The tailnet implementation does not call net.Listen at all
	// (its listener is the in-process tsnet netstack, which needs no TUN).
	files := map[string]bool{"runtime.go": false}
	check := func(path string, allowWildcard func(string) bool) {
		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != "net" || !strings.HasPrefix(sel.Sel.Name, "Listen") {
				return true
			}
			if strings.HasSuffix(path, "tailnet.go") {
				t.Errorf("%s calls net.%s; the tailnet Agent listener must be tsnet", path, sel.Sel.Name)
			}
			if len(call.Args) == 2 {
				if lit, ok := call.Args[1].(*ast.BasicLit); ok {
					value, _ := strconv.Unquote(lit.Value)
					if (strings.HasPrefix(value, ":") || strings.HasPrefix(value, "0.0.0.0") || strings.HasPrefix(value, "[::]")) && !allowWildcard(value) {
						t.Errorf("%s binds wildcard address %q", path, value)
					}
				}
			}
			return true
		})
		files[filepath.Base(path)] = true
	}
	check("runtime.go", func(v string) bool { return v == ":443" }) // ingress public:443 only
	for _, name := range []string{"lan.go", "tailnet.go", "mesh.go", "certificates.go"} {
		check(filepath.Join("..", "mesh", name), func(string) bool { return false })
	}
	for name, seen := range files {
		if !seen {
			t.Fatalf("%s not scanned", name)
		}
	}
}
