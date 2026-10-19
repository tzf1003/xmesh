package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/onboarding"
)

// Opt-in native acceptance installs only a unique current-user service and
// removes it on every exit path. It needs a built persistent executable.
func TestOnboardingNativeService(t *testing.T) {
	bin := os.Getenv("XMESH_TEST_ONBOARDING_BINARY")
	if bin == "" {
		t.Skip("set XMESH_TEST_ONBOARDING_BINARY for native user-service acceptance")
	}
	if runtime.GOOS != "darwin" && runtime.GOOS != "windows" {
		t.Skip("desktop onboarding acceptance")
	}
	root, e := os.MkdirTemp("", "xm-qs-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(root)
	workspace := filepath.Join(root, "工作 with spaces")
	if e = os.Mkdir(workspace, 0700); e != nil {
		t.Fatal(e)
	}
	name := fmt.Sprintf("qs-test-%d", time.Now().UnixNano())
	invoke := func(input []byte, args ...string) json.RawMessage {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Stdin = bytes.NewReader(input)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if e := cmd.Run(); e != nil {
			t.Fatalf("%s failed: %v %s", args[0], e, stderr.String())
		}
		var reply struct {
			OK    bool            `json:"ok"`
			Data  json.RawMessage `json:"data"`
			Error any             `json:"error"`
		}
		if e := json.Unmarshal(stdout.Bytes(), &reply); e != nil || !reply.OK {
			t.Fatalf("invalid reply: %s", stdout.String())
		}
		return reply.Data
	}
	no := false
	input, _ := json.Marshal(onboarding.Input{SchemaVersion: 1, Purpose: "local", NodeName: name, Workspace: workspace, Permission: "readonly", Clients: []string{}, Autostart: &no, Network: onboarding.NetworkInput{Mode: "lan"}})
	var p onboarding.Plan
	if e = json.Unmarshal(invoke(input, "setup", "plan", "--input", "-", "--root", root, "--json"), &p); e != nil {
		t.Fatal(e)
	}
	defer func() {
		for _, action := range []string{"stop", "uninstall"} {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			_ = exec.CommandContext(ctx, bin, action, "--name", "xmesh-"+name).Run()
			cancel()
		}
	}()
	var j onboarding.Job
	if e = json.Unmarshal(invoke(nil, "setup", "apply", "--plan", p.ID, "--root", root, "--json"), &j); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if e = json.Unmarshal(invoke(nil, "setup", "status", "--job", j.ID, "--root", root, "--json"), &j); e != nil {
			t.Fatal(e)
		}
		if j.Status == "failed" {
			t.Fatalf("native setup failed: %+v", j.Steps)
		}
		if j.Status == "waiting_user" && j.Readiness.ServiceReady {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !j.Readiness.ServiceReady || j.Readiness.ClientVerified {
		t.Fatalf("incorrect pre-client state: %+v", j.Readiness)
	}
	token, e := os.ReadFile(filepath.Join(root, name, "pats", "onboarding.token"))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "xmesh-native-acceptance", Version: "1"}, nil)
	session, e := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: p.Endpoint, HTTPClient: &http.Client{Transport: patTransport{token: strings.TrimSpace(string(token))}}}, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer session.Close()
	tools, e := session.ListTools(ctx, &mcp.ListToolsParams{})
	if e != nil || len(tools.Tools) == 0 {
		t.Fatal("no MCP tools", e)
	}
	for _, tool := range tools.Tools {
		if strings.Contains(tool.Name, "exec.") || strings.Contains(tool.Name, "fs.write") {
			t.Fatal("read-only advertised write tool", tool.Name)
		}
	}
	out, e := session.CallTool(ctx, &mcp.CallToolParams{Name: j.VerificationTool, Arguments: map[string]any{"path": j.VerificationPath}})
	if e != nil || out.IsError {
		t.Fatal("challenge failed", e)
	}
	if e = json.Unmarshal(invoke(nil, "setup", "verify", "--job", j.ID, "--root", root, "--json"), &j); e != nil {
		t.Fatal(e)
	}
	if j.Status != "succeeded" || !j.Readiness.ClientVerified {
		t.Fatalf("real MCP call not verified: %+v", j)
	}
	// Unauthenticated initialization is refused on the same loopback listener.
	request, _ := http.NewRequestWithContext(ctx, "POST", p.Endpoint, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"unauth","version":"1"}}}`))
	request.Header.Set("Content-Type", "application/json")
	res, e := http.DefaultClient.Do(request)
	if e != nil {
		t.Fatal(e)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("unauthenticated status %d", res.StatusCode)
	}
	_ = session.Close()
	originalConfig, e := os.ReadFile(p.ConfigPath)
	if e != nil {
		t.Fatal(e)
	}
	originalService, e := snapshotSetupService(context.Background(), &p)
	if e != nil || !originalService.Running {
		t.Fatal("cannot snapshot baseline service", e)
	}
	// Reconfigure an existing running node, then fail a client CAS check. The
	// original read-only config and native service definition must be restored.
	clientHome := filepath.Join(root, "isolated-client")
	if e = os.Mkdir(clientHome, 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("CODEX_HOME", clientHome)
	clientPath := filepath.Join(clientHome, "config.toml")
	if e = os.WriteFile(clientPath, []byte("model = \"original\"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	yes := true
	input, _ = json.Marshal(onboarding.Input{SchemaVersion: 1, Purpose: "local", NodeName: name, Workspace: workspace, Permission: "development", Clients: []string{"codex"}, Autostart: &yes, Network: onboarding.NetworkInput{Mode: "lan"}})
	var nextPlan onboarding.Plan
	if e = json.Unmarshal(invoke(input, "setup", "plan", "--input", "-", "--root", root, "--json"), &nextPlan); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(clientPath, []byte("model = \"concurrent-edit\"\n"), 0600); e != nil {
		t.Fatal(e)
	}
	var failed onboarding.Job
	if e = json.Unmarshal(invoke(nil, "setup", "apply", "--plan", nextPlan.ID, "--root", root, "--json"), &failed); e != nil {
		t.Fatal(e)
	}
	deadline = time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		if e = json.Unmarshal(invoke(nil, "setup", "status", "--job", failed.ID, "--root", root, "--json"), &failed); e != nil {
			t.Fatal(e)
		}
		if failed.Status == "failed" {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if failed.Status != "failed" {
		t.Fatalf("expected client conflict failure, got %s", failed.Status)
	}
	content, e := os.ReadFile(p.ConfigPath)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(content, originalConfig) {
		store, err := onboarding.Open(root)
		if err != nil {
			t.Fatal(err)
		}
		persisted, err := store.LoadJob(failed.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("existing config not restored: steps=%+v next_action=%q recovery_pending=%v config_committed=%v restore_journaled=%v backup_present=%v", failed.Steps, failed.NextAction, persisted.ServiceRestorePending, persisted.ConfigHash != "", persisted.PendingRestore, persisted.ConfigBackup != "")
	}
	content, e = os.ReadFile(clientPath)
	if e != nil || !strings.Contains(string(content), "concurrent-edit") {
		t.Fatal("client edit overwritten")
	}
	// The existing service was running before setup and must be running again.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if e = onboarding.Probe(context.Background(), &p); e == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if e != nil {
		t.Fatal("previous service not restored", e)
	}
	serviceAfter, e := snapshotSetupService(context.Background(), &p)
	if e != nil || !serviceAfter.Running {
		t.Fatal("baseline running state not restored", e)
	}
	if runtime.GOOS == "darwin" && !bytes.Equal(originalService.Definition, serviceAfter.Definition) {
		t.Fatal("native registration settings not restored")
	}
}
