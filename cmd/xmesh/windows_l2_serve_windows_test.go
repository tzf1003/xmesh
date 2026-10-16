package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sys/windows"
)

// These tests run the built xmesh binary as `serve` with agent.policy.windows_appcontainer
// enabled and drive it over MCP, so the whole production start chain is exercised:
// opt-in, runner construction with the keeper and kernel fence, startup recovery,
// and the per-call runner attach. They need an AppContainer-capable Windows host.
//
//	XMESH_TEST_WINDOWS_SANDBOX=1 XMESH_TEST_WINDOWS_BINARY=<built xmesh.exe> \
//	  go test ./cmd/xmesh -run WindowsL2

const l2Secret = "host-only-secret"

// l2Node is one workspace plus the serve process that manages it. The state
// directory outlives the process so a restart sees what a crash left behind.
type l2Node struct {
	t                      *testing.T
	binary                 string
	base, root, outside    string
	config, state, address string
	mu                     sync.Mutex
	cmd                    *osexec.Cmd
	log                    *bytes.Buffer
	exited                 chan struct{}
	client                 *mcp.ClientSession
}

func newL2Node(t *testing.T) *l2Node {
	t.Helper()
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	binary := os.Getenv("XMESH_TEST_WINDOWS_BINARY")
	if !filepath.IsAbs(binary) {
		t.Fatal("XMESH_TEST_WINDOWS_BINARY must name the built xmesh executable")
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	n := &l2Node{t: t, binary: binary, base: base, root: filepath.Join(base, "workspace"), outside: filepath.Join(base, "outside"), config: filepath.Join(base, "config.json"), state: filepath.Join(base, "state")}
	for _, dir := range []string{n.root, n.outside} {
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	setL2MediumLabel(t, n.root)
	if err = os.WriteFile(filepath.Join(n.outside, "secret.txt"), []byte(l2Secret), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	n.address = listener.Addr().String()
	listener.Close()
	doc := map[string]any{
		"node": map[string]any{"name": "l2-serve", "roles": []string{"agent"}},
		"mesh": map[string]any{"mode": "lan", "lan": map[string]any{"mdns": true}},
		"agent": map[string]any{
			"default_cwd": n.root,
			"cwd_policy":  map[string]any{"allow": []string{n.root}},
			"policy":      map[string]any{"isolation": "L2", "windows_appcontainer": true},
			"authz":       map[string]any{"tag:peer": map[string]any{"tools": []string{"*"}, "backends": []string{}}},
		},
	}
	body, _ := json.Marshal(doc)
	if err = os.WriteFile(n.config, body, 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.kill)
	return n
}

func setL2MediumLabel(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.SecurityDescriptorFromString("S:(ML;;NW;;;ME)")
	if err != nil {
		t.Fatal(err)
	}
	sacl, _, err := sd.SACL()
	if err != nil {
		t.Fatal(err)
	}
	if err = windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION, nil, nil, nil, sacl); err != nil {
		t.Fatal(err)
	}
}

// start launches serve and waits for the MCP listener.
func (n *l2Node) start() {
	n.t.Helper()
	n.mu.Lock()
	defer n.mu.Unlock()
	n.log = &bytes.Buffer{}
	cmd := osexec.Command(n.binary, "serve", "--config", n.config, "--local", "--listen", n.address, "--state-dir", n.state)
	cmd.Stdout, cmd.Stderr = n.log, n.log
	if err := cmd.Start(); err != nil {
		n.t.Fatal(err)
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	n.cmd, n.exited = cmd, exited
	deadline := time.Now().Add(60 * time.Second)
	for {
		attempt, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		client, err := mcp.NewClient(&mcp.Implementation{Name: "l2-serve", Version: "1"}, nil).Connect(attempt, &mcp.StreamableClientTransport{Endpoint: "http://" + n.address + "/mcp"}, nil)
		cancel()
		if err == nil {
			n.client = client
			return
		}
		select {
		case <-exited:
			n.t.Fatalf("serve exited before it was ready: %s", n.log.String())
		default:
		}
		if time.Now().After(deadline) {
			n.t.Fatalf("serve was not ready: %v\n%s", err, n.log.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// kill ends the serve process abruptly, as a host crash would. Nothing it held is
// released by it.
func (n *l2Node) kill() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.client != nil {
		n.client.Close()
		n.client = nil
	}
	if n.cmd == nil {
		return
	}
	_ = n.cmd.Process.Kill()
	select {
	case <-n.exited:
	case <-time.After(30 * time.Second):
		n.t.Error("serve did not exit after kill")
	}
	n.cmd = nil
}

func (n *l2Node) call(name string, args map[string]any) map[string]any {
	n.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	result, err := n.client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		n.t.Fatalf("%s: %v\n%s", name, err, n.log.String())
	}
	raw, _ := json.Marshal(result.StructuredContent)
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		n.t.Fatal(err)
	}
	return out
}

func (n *l2Node) warnings() string {
	n.t.Helper()
	list, _ := n.call("sys.info", map[string]any{})["warnings"].([]any)
	var all []string
	for _, w := range list {
		all = append(all, fmt.Sprint(w))
	}
	return strings.Join(all, "\n")
}

// journals lists the recovery journals the runner currently keeps.
func (n *l2Node) journals() []string {
	entries, _ := os.ReadDir(filepath.Join(n.state, "sandbox"))
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	return names
}

func (n *l2Node) waitJournal(present bool) {
	n.t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if (len(n.journals()) != 0) == present {
			return
		}
		if time.Now().After(deadline) {
			n.t.Fatalf("journals present=%v want %v: %v\n%s", len(n.journals()) != 0, present, n.journals(), n.log.String())
		}
	}
}

// descriptor is the workspace root's DACL as the Win32 view reports it. A
// completed or recovered execution must leave it exactly as it was.
func (n *l2Node) descriptor() string {
	n.t.Helper()
	sd, err := windows.GetNamedSecurityInfo(n.root, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		n.t.Fatal(err)
	}
	return sd.String()
}

// cmdCall runs one cmd.exe command inside the sandbox. The arguments never
// contain spaces, so no quoting is involved.
func (n *l2Node) cmdCall(args ...string) map[string]any {
	return n.call("exec.run", map[string]any{"cmd": "cmd.exe", "args": append([]string{"/d", "/c"}, args...)})
}

// writeInside creates a file in the workspace from inside the sandbox.
func (n *l2Node) writeInside(name string) map[string]any {
	return n.call("exec.run", map[string]any{"cmd": "cmd.exe", "args": []string{"/d", "/s", "/c", "echo x>" + name}})
}

type l2Terminal struct {
	n       *l2Node
	session string
	cursor  float64
	text    strings.Builder
	eof     bool
}

func (n *l2Node) openTerminal() *l2Terminal {
	n.t.Helper()
	opened := n.call("pty.open", map[string]any{"cols": 80, "rows": 24, "shell": os.Getenv("ComSpec")})
	session, _ := opened["session_id"].(string)
	if session == "" {
		n.t.Fatalf("terminal did not open: %v\n%s", opened, n.log.String())
	}
	return &l2Terminal{n: n, session: session}
}

func (tm *l2Terminal) write(data string) {
	tm.n.call("pty.write", map[string]any{"session_id": tm.session, "data": data})
}

func (tm *l2Terminal) until(expression string) {
	tm.n.t.Helper()
	re := regexp.MustCompile(expression)
	for deadline := time.Now().Add(90 * time.Second); time.Now().Before(deadline); {
		if re.MatchString(tm.text.String()) {
			return
		}
		if tm.eof {
			break
		}
		tm.read(100)
	}
	tm.n.t.Fatalf("terminal output did not match %q (eof=%v): %q\n%s", expression, tm.eof, tm.text.String(), tm.n.log.String())
}

func (tm *l2Terminal) read(wait int) {
	result := tm.n.call("pty.read", map[string]any{"session_id": tm.session, "cursor": tm.cursor, "wait_ms": wait})
	tm.text.WriteString(fmt.Sprint(result["data"]))
	tm.cursor, _ = result["next_cursor"].(float64)
	tm.eof = result["eof"] == true
}

func (tm *l2Terminal) drain() {
	tm.n.t.Helper()
	for deadline := time.Now().Add(90 * time.Second); !tm.eof; {
		if time.Now().After(deadline) {
			tm.n.t.Fatalf("terminal did not end: %q", tm.text.String())
		}
		tm.read(200)
	}
}

// guardKeepers returns the ids of this binary's guard keeper processes.
func guardKeepers(t *testing.T, binary string) []uint32 {
	t.Helper()
	filter := fmt.Sprintf("Name='%s'", filepath.Base(binary))
	script := "Get-CimInstance Win32_Process -Filter \"" + filter + "\" | Where-Object { $_.CommandLine -like '*__sandbox-guard*' } | ForEach-Object { $_.ProcessId }"
	out, err := osexec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script).Output()
	if err != nil {
		t.Fatal("list guard keepers", err)
	}
	var ids []uint32
	for _, field := range strings.Fields(string(out)) {
		id, err := strconv.ParseUint(field, 10, 32)
		if err != nil {
			t.Fatal("guard keeper process id", field)
		}
		ids = append(ids, uint32(id))
	}
	return ids
}

func killProcesses(t *testing.T, ids []uint32) {
	t.Helper()
	for _, id := range ids {
		process, err := os.FindProcess(int(id))
		if err != nil {
			continue
		}
		_ = process.Kill()
	}
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if len(ids) == 0 {
			return
		}
		remaining := 0
		for _, id := range ids {
			handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, id)
			if err != nil {
				continue
			}
			event, _ := windows.WaitForSingleObject(handle, 0)
			windows.CloseHandle(handle)
			if event != windows.WAIT_OBJECT_0 {
				remaining++
			}
		}
		if remaining == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("processes survived termination", ids)
		}
	}
}

func Test_WindowsL2ServeExecAppContainer(t *testing.T) {
	n := newL2Node(t)
	n.start()
	if warnings := n.warnings(); strings.Contains(warnings, "sandbox.unavailable") {
		t.Fatalf("the runner did not start: %s\n%s", warnings, n.log.String())
	}
	before := n.descriptor()
	secret := filepath.Join(n.outside, "secret.txt")
	denied := n.cmdCall("type", secret)
	text := fmt.Sprint(denied["stdout"], denied["stderr"])
	if denied["exit_code"] == float64(0) || strings.Contains(text, l2Secret) {
		t.Fatalf("the sandboxed command read outside its workspace: %v", denied)
	}
	// Several ways to create something inside the workspace, all logged, so a
	// failure shows which operation the sandbox refuses.
	for _, probe := range []struct {
		name string
		args []string
	}{
		{"echo", []string{"/d", "/s", "/c", "echo x>inside.txt"}},
		{"mkdir", []string{"/d", "/c", "mkdir", "inside-dir"}},
		{"copy", []string{"/d", "/c", "copy", "NUL", "inside-copy.txt"}},
	} {
		result := n.call("exec.run", map[string]any{"cmd": "cmd.exe", "args": probe.args})
		t.Logf("write probe %s: %v", probe.name, result)
	}
	icacls, _ := osexec.Command("icacls.exe", n.root).CombinedOutput()
	t.Logf("workspace ACL after probes:\n%s", icacls)
	written := n.writeInside("inside.txt")
	if written["exit_code"] != float64(0) {
		t.Fatalf("the sandboxed command could not write inside its workspace: %v\n%s", written, n.log.String())
	}
	if _, err := os.Stat(filepath.Join(n.root, "inside.txt")); err != nil {
		t.Fatal("sandbox write missing", err)
	}
	// Both executions are reclaimed before the tool results return.
	if names := n.journals(); len(names) != 0 {
		t.Fatalf("completed executions left journals: %v", names)
	}
	if after := n.descriptor(); after != before {
		t.Fatalf("workspace descriptor changed:\nbefore %s\nafter  %s", before, after)
	}
	if keepers := guardKeepers(t, n.binary); len(keepers) != 0 {
		t.Fatalf("guard keepers remain after completed executions: %v", keepers)
	}
}

func Test_WindowsL2ServePTYAppContainer(t *testing.T) {
	n := newL2Node(t)
	n.start()
	before := n.descriptor()
	terminal := n.openTerminal()
	terminal.write("echo L2^-PTY\r\n")
	terminal.until(`L2-PTY`)
	terminal.write("type " + filepath.Join(n.outside, "secret.txt") + "\r\n")
	terminal.until(`(?i)access is denied`)
	if strings.Contains(terminal.text.String(), l2Secret) {
		t.Fatal("the sandboxed terminal read outside its workspace")
	}
	terminal.write("exit\r\n")
	terminal.drain()
	for deadline := time.Now().Add(60 * time.Second); len(n.journals()) != 0; time.Sleep(200 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("closed terminal left journals: %v", n.journals())
		}
	}
	if after := n.descriptor(); after != before {
		t.Fatalf("workspace descriptor changed:\nbefore %s\nafter  %s", before, after)
	}
}

// crashAndRecover starts a terminal, kills the host while the execution is live,
// optionally also kills the guard keeper, and restarts until recovery succeeds.
func crashAndRecover(t *testing.T, killKeeper bool) {
	n := newL2Node(t)
	n.start()
	before := n.descriptor()
	terminal := n.openTerminal()
	terminal.write("echo REA^DY\r\n")
	terminal.until(`READY`)
	n.waitJournal(true)
	keepers := guardKeepers(t, n.binary)
	n.kill()
	if killKeeper {
		if len(keepers) == 0 {
			t.Fatal("no guard keeper was running during the execution")
		}
		killProcesses(t, keepers)
		if remaining := guardKeepers(t, n.binary); len(remaining) != 0 {
			t.Fatal("guard keeper survived its termination", remaining)
		}
	}
	if len(n.journals()) == 0 {
		t.Fatal("the crash left no journal to recover")
	}
	// The execution's processes end shortly after the host does; until they are
	// gone recovery refuses and L2 stays closed, which a restart then retries.
	var warnings string
	for attempt := 0; ; attempt++ {
		n.start()
		warnings = n.warnings()
		if !strings.Contains(warnings, "sandbox.unavailable") && len(n.journals()) == 0 {
			break
		}
		if attempt == 8 {
			t.Fatalf("startup recovery did not complete: %q journals=%v\n%s", warnings, n.journals(), n.log.String())
		}
		n.kill()
		time.Sleep(3 * time.Second)
	}
	if after := n.descriptor(); after != before {
		t.Fatalf("recovery did not restore the workspace descriptor:\nbefore %s\nafter  %s", before, after)
	}
	if rest := guardKeepers(t, n.binary); len(rest) != 0 {
		t.Fatalf("recovery left guard keepers: %v", rest)
	}
	if result := n.writeInside("after-recovery.txt"); result["exit_code"] != float64(0) {
		t.Fatalf("L2 did not work after recovery: %v\n%s", result, n.log.String())
	}
}

func Test_WindowsL2ServeCrashRecovery(t *testing.T) { crashAndRecover(t, false) }

// Without a keeper (a reboot, a logoff or a killed keeper) recovery freezes the
// workspace with a fresh writer fence.
func Test_WindowsL2ServeNoKeeperRecovery(t *testing.T) { crashAndRecover(t, true) }

// A journal that cannot be recovered must keep L2 closed; the node still serves.
func Test_WindowsL2ServeNoKeeperFailsClosed(t *testing.T) {
	n := newL2Node(t)
	n.start()
	n.kill()
	junk := filepath.Join(n.state, "sandbox", "0123456789abcdef0123456789abcdef.journal")
	if err := os.WriteFile(junk, bytes.Repeat([]byte{0xA5}, 512), 0600); err != nil {
		t.Fatal(err)
	}
	n.start()
	if warnings := n.warnings(); !strings.Contains(warnings, "sandbox.unavailable") {
		t.Fatalf("an unrecoverable journal was not reported: %q\n%s", warnings, n.log.String())
	}
	refused := n.writeInside("must-not-run.txt")
	if refused["error"] != "policy_denied" || refused["rule"] != "sandbox.unavailable" {
		t.Fatalf("L2 ran without a recovered runner: %v", refused)
	}
	if _, err := os.Stat(filepath.Join(n.root, "must-not-run.txt")); err == nil {
		t.Fatal("the refused command ran")
	}
	if _, err := os.Stat(junk); err != nil {
		t.Fatal("the unrecoverable journal was discarded", err)
	}
	// Other tools keep working on the node.
	n.call("fs.write", map[string]any{"path": "plain.txt", "content": "ok"})
}
