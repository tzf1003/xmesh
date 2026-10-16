package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/kardianos/service"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
)

const cliConfig = "node:\n  name: cli-node\n  roles: [agent]\nmesh:\n  mode: lan\n  lan:\n    mdns: true\nagent:\n  default_cwd: .\n  cwd_policy:\n    allow: [\".\"]\n"

type cliHost struct{ draining atomic.Bool }

func (h *cliHost) Status() control.Status {
	return control.Status{Node: "cli-node", Roles: []string{"agent"}, Draining: h.draining.Load(), Warnings: []string{"示例警告"}, Backends: []control.Backend{}, Workspaces: []control.Workspace{}, Network: &control.Network{Mode: "lan", Node: "cli-node", Tags: []string{"tag:peer"}}}
}
func (h *cliHost) Peers(context.Context) (control.Peers, error) {
	return control.Peers{Mode: "lan", Peers: []control.Peer{{ID: "n2", Name: "box", Online: true}}}, nil
}
func (h *cliHost) Sessions() control.Sessions { return control.Sessions{} }
func (h *cliHost) Drain(bool)                 { h.draining.Store(true) }
func (h *cliHost) Resume()                    { h.draining.Store(false) }

// startKernelStub runs a real control server and returns its state directory.
func startKernelStub(t *testing.T) (state, configPath string) {
	t.Helper()
	dir := t.TempDir()
	state = filepath.Join(dir, "s")
	if err := os.Mkdir(state, 0700); err != nil {
		t.Fatal(err)
	}
	configPath = filepath.Join(dir, "xmesh.yaml")
	if err := os.WriteFile(configPath, []byte(cliConfig), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := config.NewStore(configPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := control.Start(control.Options{Host: &cliHost{}, Store: store, Version: "t", StateDir: state, AuditDir: filepath.Join(dir, "audit")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return state, configPath
}

func output(t *testing.T, run func() error) (string, error) {
	t.Helper()
	text, err := captureStdout(run)
	return text, err
}

func Test_CLIControlSubcommands(t *testing.T) {
	state, _ := startKernelStub(t)
	object := func(args ...string) map[string]any {
		t.Helper()
		out, err := output(t, func() error { return controlCommand(append(args, "--state-dir", state)) })
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		var v map[string]any
		if err = json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("%v printed %q", args, out)
		}
		return v
	}
	if st := object("status"); st["node"] != "cli-node" || st["version"] != "t" {
		t.Fatal(st)
	}
	if peers := object("peers"); len(peers["peers"].([]any)) != 1 {
		t.Fatal(peers)
	}
	object("sessions")
	if d := object("drain", "--mode", "kill"); d["draining"] != true || d["mode"] != "kill" {
		t.Fatal(d)
	}
	if st := object("status"); st["draining"] != true {
		t.Fatal(st)
	}
	if d := object("resume"); d["draining"] != false {
		t.Fatal(d)
	}
	got := object("config", "get")
	etag := got["etag"].(string)
	if got["yaml"] != cliConfig {
		t.Fatal(got)
	}
	dry := object("config", "set", "--if-match", etag, "--dry-run", "--set", "/agent/policy/allow_dangerous=true")
	if dry["applied"] != false || len(dry["changes"].([]any)) != 1 {
		t.Fatal(dry)
	}
	applied := object("config", "set", "--if-match", etag, "--set", "/agent/exec/max_concurrent=2", "--delete", "/agent/default_cwd")
	if applied["applied"] != true {
		t.Fatal(applied)
	}
	if _, err := output(t, func() error {
		return controlCommand([]string{"config", "set", "--if-match", etag, "--set", "/agent/exec/max_concurrent=3", "--state-dir", state})
	}); err == nil || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("stale etag: %v", err)
	}
	if _, err := output(t, func() error { return controlCommand([]string{"config", "set", "--state-dir", state}) }); err == nil {
		t.Fatal("config set without --if-match")
	}
	if events, err := output(t, func() error {
		return controlCommand([]string{"events", "--last-event-id", "0", "--count", "1", "--state-dir", state})
	}); err != nil || !strings.Contains(events, `"type":"`) {
		t.Fatalf("events: %v %q", err, events)
	}
	if _, err := output(t, func() error { return controlCommand([]string{"bogus"}) }); err == nil {
		t.Fatal("unknown verb")
	}
}

func Test_CLIStatusViaControl(t *testing.T) {
	state, _ := startKernelStub(t)
	text, err := output(t, func() error { return controlStatus("", state, false) })
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"节点: cli-node", "角色: agent", "网络身份: 模式 lan", "警告: 示例警告"} {
		if !strings.Contains(text, want) {
			t.Errorf("缺少 %q:\n%s", want, text)
		}
	}
	raw, err := output(t, func() error { return controlStatus("", state, true) })
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if json.Unmarshal([]byte(raw), &st) != nil || st["node"] != "cli-node" {
		t.Fatalf("--json: %q", raw)
	}
	// through status() itself
	if _, err = output(t, func() error { return status([]string{"--control", "--state-dir", state}) }); err != nil {
		t.Fatal(err)
	}
}

func Test_CLIControlTargetErrors(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"HOME", "XDG_CONFIG_HOME", "AppData"} {
		t.Setenv(name, root)
	}
	if _, err := controlTarget("", ""); err == nil || !strings.Contains(err.Error(), "没有找到运行中的节点") {
		t.Fatalf("no nodes: %v", err)
	}
	base, err := os.UserConfigDir()
	if err != nil {
		t.Skip(err)
	}
	for _, node := range []string{"a", "b"} {
		dir := filepath.Join(base, "xmesh", node)
		if err = os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = control.WriteEndpoint(dir, control.Endpoint{Transport: "unix", Address: "/x/" + node, Node: node}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = controlTarget("", ""); err == nil || !strings.Contains(err.Error(), "--node") {
		t.Fatalf("ambiguous: %v", err)
	}
	if e, err := controlTarget("b", ""); err != nil || e.Address != "/x/b" {
		t.Fatalf("%+v %v", e, err)
	}
	if _, err = controlTarget("zzz", ""); err == nil {
		t.Fatal("unknown node accepted")
	}
}

func Test_CLIJSONWrapper(t *testing.T) {
	run := func(args []string, f func([]string) error) (map[string]any, error) {
		out, err := captureStdout(func() error { return withJSON("demo", args, f) })
		_ = err
		var v map[string]any
		if json.Unmarshal([]byte(out), &v) != nil {
			return nil, err
		}
		return v, err
	}
	say := func(args []string) error {
		os.Stdout.WriteString("已完成第一步\n已完成第二步\n")
		return nil
	}
	got, err := run([]string{"--json", "--x", "1"}, func(args []string) error {
		if strings.Join(args, " ") != "--x 1" {
			t.Errorf("--json leaked into the command's arguments: %v", args)
		}
		return say(args)
	})
	if err != nil || got["ok"] != true || got["action"] != "demo" || len(got["messages"].([]any)) != 2 {
		t.Fatalf("%v %v", got, err)
	}
	got, err = run([]string{"--json"}, func([]string) error { return io.ErrUnexpectedEOF })
	if err == nil || got["ok"] != false || got["error"].(map[string]any)["error"] != "internal" || !strings.Contains(got["error"].(map[string]any)["message"].(string), "unexpected EOF") {
		t.Fatalf("failure should print ok:false and still return the error: %v %v", got, err)
	}
	// without --json the command's own output is untouched
	out, err := captureStdout(func() error { return withJSON("demo", nil, say) })
	if err != nil || !strings.HasPrefix(out, "已完成第一步") {
		t.Fatalf("%q %v", out, err)
	}
}

func Test_CLIServiceCommandArguments(t *testing.T) {
	if err := serviceCommand("stop", []string{"--config", filepath.Join(t.TempDir(), "missing.yaml")}); err == nil || !strings.Contains(err.Error(), "--name") {
		t.Fatalf("missing config should point at --name: %v", err)
	}
	if err := serviceCommand("stop", []string{"--bogus"}); err == nil {
		t.Fatal("unknown flag accepted")
	}
	if os.PathSeparator == '/' {
		if err := serviceCommand("stop", []string{"--system", "--name", "x"}); err == nil || !strings.Contains(err.Error(), "仅适用于 Windows") {
			t.Fatalf("--system off Windows: %v", err)
		}
	}
	if serviceName("n1") != "xmesh-n1" {
		t.Fatal("service name must match install")
	}
}

// The task name used to stop or remove a user task is the one install registered.
func Test_CLIUserTaskNameMatchesInstall(t *testing.T) {
	var registered string
	c := &service.Config{Name: serviceName("n1"), Executable: "x", Arguments: []string{"serve"}}
	if _, err := createUserTask(c, "S-1-5-21-1-2-3-1001", "serve", func(name, _ string) error { registered = name; return nil }); err != nil {
		t.Fatal(err)
	}
	if registered == "" || registered != userTaskName(c.Name, "S-1-5-21-1-2-3-1001") {
		t.Fatalf("registered %q, computed %q", registered, userTaskName(c.Name, "S-1-5-21-1-2-3-1001"))
	}
}

func Test_CLIUsageMentionsControlChannel(t *testing.T) {
	for _, word := range []string{"control", "uninstall", "restart", "--control", "--control-allow-sid", "--json"} {
		if !strings.Contains(usage, word) {
			t.Errorf("usage 缺少 %q", word)
		}
	}
	if !strings.Contains(controlUsage, "drain") || !strings.Contains(controlUsage, "config set") {
		t.Error("control usage incomplete")
	}
}
