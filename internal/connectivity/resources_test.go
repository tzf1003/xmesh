package connectivity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/role/agent"
)

func TestConnectivityChild(t *testing.T) {
	args := os.Args
	for i, arg := range args {
		if arg == "--device-exec-helper" {
			count, started, release := args[i+1], args[i+2], args[i+3]
			file, err := os.OpenFile(count, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				os.Exit(41)
			}
			_, _ = file.WriteString("execution\n")
			_ = file.Close()
			if err := os.WriteFile(started, []byte("ready"), 0600); err != nil {
				os.Exit(42)
			}
			for until := time.Now().Add(20 * time.Second); time.Now().Before(until); {
				if _, err := os.Stat(release); err == nil {
					fmt.Print("EXEC_ONCE")
					os.Exit(0)
				}
				time.Sleep(10 * time.Millisecond)
			}
			os.Exit(43)
		}
		if arg == "--device-backend-helper" {
			server := mcp.NewServer(&mcp.Implementation{Name: "session-backend", Version: "2"}, nil)
			server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				return &mcp.CallToolResult{StructuredContent: map[string]any{"pid": os.Getpid()}}, nil
			})
			if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
				os.Exit(44)
			}
			os.Exit(0)
		}
	}
}

func TestAgentResourcesSurviveCarrierReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	a, _ := node(t, "client")
	b, cfg := node(t, "resource-workstation")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	backend := func(policy string) config.Backend {
		return config.Backend{Transport: "stdio", Command: []string{binary, "-test.run=^TestConnectivityChild$", "--", "--device-backend-helper"}, Policy: policy, IdleTimeout: "1m"}
	}
	cfg.Agent.Backends = map[string]config.Backend{"exclusive": backend("exclusive"), "session": backend("per-caller")}
	ag, err := agent.New(config.Static(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ag.Close)
	b.SetAgent(func(ctx context.Context, p identity.Principal, transport mcp.Transport) error {
		_, err := ag.ConnectDevice(ctx, p, transport)
		return err
	})
	server := serveNode(t, b)
	if err := b.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PairWith(ctx, device.LAN, server.URL, "correct horse battery", b.identity.ID); err != nil {
		t.Fatal(err)
	}
	transport, err := a.Transport(ctx, mesh.Peer{ID: b.identity.ID}, "")
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "resource-client", Version: "2"}, nil)
	session, err := client.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	call := func(name string, args any) map[string]any {
		t.Helper()
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || out.IsError {
			t.Fatalf("%s: %v %v", name, out, err)
		}
		raw, _ := json.Marshal(out.StructuredContent)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		return body
	}
	reattach := func() {
		t.Helper()
		epoch := a.links.Activity(b.identity.ID).Epoch
		a.links.DisablePath(device.LAN)
		deadline := time.Now().Add(4 * time.Second)
		for time.Now().Before(deadline) {
			activity := a.links.Activity(b.identity.ID)
			if activity.Epoch > epoch && activity.State == "online" {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Fatal("carrier did not recover", a.links.Activity(b.identity.ID))
	}
	before := call("pty.open", map[string]any{"cols": 80, "rows": 24})
	backendPID := call("session.echo", map[string]any{})["pid"]
	lease := call("backend.acquire", map[string]any{"name": "exclusive", "ttl": "20s"})
	root := cfg.Agent.DefaultCWD
	count, started, release := filepath.Join(root, "count.txt"), filepath.Join(root, "started"), filepath.Join(root, "release")
	result := make(chan error, 1)
	go func() {
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "exec.run", Arguments: map[string]any{"cmd": binary, "args": []string{"-test.run=^TestConnectivityChild$", "--", "--device-exec-helper", count, started, release}, "idempotency_key": "carrier-once"}})
		if err == nil {
			raw, _ := json.Marshal(out.StructuredContent)
			var body map[string]any
			_ = json.Unmarshal(raw, &body)
			if out.IsError || body["stdout"] != "EXEC_ONCE" {
				err = fmt.Errorf("exec result: %s", raw)
			}
		}
		result <- err
	}()
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("exec did not start")
		case <-time.After(5 * time.Millisecond):
		}
	}
	reattach()
	if err := os.WriteFile(release, []byte("continue"), 0600); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("exec result lost")
	}
	executions, err := os.ReadFile(count)
	if err != nil || strings.Count(string(executions), "execution") != 1 {
		t.Fatal("exec repeated", string(executions), err)
	}
	after := call("pty.list", map[string]any{})
	sessions := after["sessions"].([]any)
	if len(sessions) != 1 || sessions[0].(map[string]any)["owner"] != before["owner"] || sessions[0].(map[string]any)["pid"] != before["pid"] {
		t.Fatal("PTY identity changed", before, after)
	}
	if got := call("session.echo", map[string]any{})["pid"]; got != backendPID {
		t.Fatal("per-caller process rebuilt", backendPID, got)
	}
	renewed := call("backend.renew", map[string]any{"name": "exclusive", "ttl": "20s", "_lease_token": lease["token"]})
	if renewed["owner"] != lease["owner"] || renewed["token"] != lease["token"] {
		t.Fatal("lease owner or fencing token changed", lease, renewed)
	}
	call("exclusive.echo", map[string]any{"_lease_token": lease["token"]})
	body := bytes.Repeat([]byte{0, 255, 13, 10}, (2<<20)/4)
	sum := fmt.Sprintf("%x", sha256.Sum256(body))
	upload := call("fs.upload", map[string]any{"path": "artifact.bin", "data": base64.StdEncoding.EncodeToString(body[:1<<20]), "sha256": sum, "byte_offset": 0, "final": false})
	reattach()
	final := call("fs.upload", map[string]any{"path": "artifact.bin", "transfer_id": upload["transfer_id"], "byte_offset": 1 << 20, "data": base64.StdEncoding.EncodeToString(body[1<<20:]), "sha256": sum, "final": true})
	if final["sha256"] != sum {
		t.Fatal("upload hash", final)
	}
	written, err := os.ReadFile(filepath.Join(root, "artifact.bin"))
	if err != nil || !bytes.Equal(written, body) {
		t.Fatal("upload bytes corrupted", err)
	}
	downloaded := call("fs.download", map[string]any{"path": "artifact.bin"})
	first, err := base64.StdEncoding.DecodeString(downloaded["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	reattach()
	second := call("fs.download", map[string]any{"path": "artifact.bin", "transfer_id": downloaded["transfer_id"], "byte_offset": downloaded["next_offset"]})
	chunk, err := base64.StdEncoding.DecodeString(second["data"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(append(first, chunk...), body) || second["file_sha256"] != sum {
		t.Fatal("download did not resume", second)
	}
	call("pty.close", map[string]any{"session_id": before["session_id"]})
	call("backend.release", map[string]any{"name": "exclusive", "_lease_token": lease["token"]})
}
