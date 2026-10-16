package winbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// descendantSecurityFixture prepares a workspace with a subdirectory and files
// that exist at admission, so they belong to the admitted snapshot.
func descendantSecurityFixture(t *testing.T, interval time.Duration) workspaceExecutionOptions {
	t.Helper()
	return descendantSecurityFixtureIn(t, t.TempDir(), interval)
}

func descendantSecurityFixtureIn(t *testing.T, base string, interval time.Duration) workspaceExecutionOptions {
	t.Helper()
	root := filepath.Join(base, "workspace")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	setMediumLabel(t, root)
	state := filepath.Join(base, "state")
	privateJournalDirectory(t, state)
	options := workspaceExecutionOptions{Workspace: root, Journal: filepath.Join(state, "execution.journal"),
		MaxObjects: 32, MaxJobs: 2, Interval: interval, MaxEvents: maxWorkspaceChangeEvents,
		SupervisorCommand: supervisorTestCommand(t, false)}
	options.RequireWriterFence = true
	options.Interval = interval
	sub := filepath.Join(options.Workspace, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{filepath.Join(sub, "initial.txt"), filepath.Join(options.Workspace, "top.txt")} {
		if err := os.WriteFile(name, []byte("initial"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return options
}

// startDescendantHelper copies the test binary into the profile directory and
// starts one of its helper modes in the AppContainer.
func startDescendantHelper(t *testing.T, ctx context.Context, e *workspaceExecution, workspace, mode string) (windows.ProcessInformation, string) {
	t.Helper()
	helper := filepath.Join(e.profile.Directory(), "descendant-helper.exe")
	body, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, body, 0700); err != nil {
		t.Fatal(err)
	}
	proof := filepath.Join(e.profile.Directory(), "proof")
	process, err := e.start(ctx, []string{helper, "-test.run=^Test_WindowsAppContainerDescendantSecurityHelper$", "--", workspace, proof, mode},
		e.profile.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_DESCENDANT_SECURITY_HELPER=1"}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(process.Process); windows.CloseHandle(process.Thread) })
	return process, proof
}

func waitDescendantProof(t *testing.T, ctx context.Context, e *workspaceExecution, proof string) string {
	t.Helper()
	for {
		if data, err := os.ReadFile(proof); err == nil && len(data) != 0 && data[len(data)-1] == '\n' {
			return strings.TrimSpace(string(data))
		}
		select {
		case <-ctx.Done():
			t.Fatal("descendant helper readiness", ctx.Err())
		case <-e.monitor.done:
			t.Fatal("monitor ended before the helper reported", e.monitor.err)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func Test_WindowsAppContainerDescendantSecurity(t *testing.T) {
	skipUnlessWindowsSandbox(t)
	type scenario struct {
		name     string
		interval time.Duration
		target   string // Path relative to the workspace, changed by an external writer.
		viaClose bool   // Rely on the final check instead of a monitor cycle.
	}
	for _, mode := range []scenario{
		{name: "clean"},
		{name: "descendant-file", interval: 20 * time.Millisecond, target: `sub\initial.txt`},
		{name: "descendant-directory", interval: 20 * time.Millisecond, target: "sub"},
		{name: "top-level-file", interval: 20 * time.Millisecond, target: "moved.txt"}, // The helper renames top.txt.
		// The monitor never ticks, so only the post-reap check can see it.
		{name: "final-check", interval: time.Minute, target: `sub\initial.txt`, viaClose: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			interval := mode.interval
			if interval == 0 {
				interval = 20 * time.Millisecond
			}
			options := descendantSecurityFixture(t, interval)
			before, control := kernelJournalState(t, options.Workspace)
			e, err := newWorkspaceExecution(ctx, options, kernelWriterFencePrepare)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.closeContext(context.Background(), 91) }()
			journal, monitor := e.journal, e.monitor
			process, proof := startDescendantHelper(t, ctx, e, options.Workspace, "plain")
			if got := waitDescendantProof(t, ctx, e, proof); got != "ready" {
				t.Fatal("helper", got)
			}
			if mode.target == "" {
				// Sandbox creation, rename and deletion, this package's grants and
				// many monitor cycles must not look like a descriptor change.
				time.Sleep(500 * time.Millisecond)
				select {
				case <-monitor.done:
					t.Fatal("clean execution faulted", monitor.err)
				default:
				}
				if err := e.closeContext(ctx, 91); err != nil {
					t.Fatal("clean close", err)
				}
				if after, afterControl := kernelJournalState(t, options.Workspace); string(before) != string(after) || control != afterControl {
					t.Fatal("workspace descriptor changed after the lifecycle")
				}
				return
			}
			tamperDACL(t, filepath.Join(options.Workspace, mode.target))
			if !mode.viaClose {
				select {
				case <-monitor.done:
				case <-ctx.Done():
					t.Fatal("descendant descriptor change did not fault the monitor")
				}
				if !errors.Is(monitor.err, errWorkspaceSecurityChanged) || !errors.Is(monitor.err, errWriterFenceViolated) {
					t.Fatal("monitor fault cause", monitor.err)
				}
				if result, err := windows.WaitForSingleObject(process.Process, 5000); err != nil || result != windows.WAIT_OBJECT_0 {
					t.Fatal("violation left the sandbox process running", result, err)
				}
				if _, err := e.start(ctx, []string{"unused.exe"}, e.profile.Directory(), nil, nil, 0); err == nil {
					t.Fatal("violated lifecycle admitted a process")
				}
			} else {
				select {
				case <-monitor.done:
					t.Fatal("monitor observed the change before the final check", monitor.err)
				default:
				}
			}
			if err := e.closeContext(ctx, 91); !errors.Is(err, errWorkspaceSecurityChanged) {
				t.Fatal("close did not report the descendant change", err)
			}
			// Cleanup still reclaims every grant by identity.
			if !e.closed || !journal.state.complete {
				t.Fatal("cleanup did not complete", e.closed)
			}
			sid, err := windows.StringToSid(journal.records[0].SID)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"", "sub", `sub\initial.txt`, "moved.txt"} {
				state, _ := kernelJournalState(t, filepath.Join(options.Workspace, name))
				if bytes.Contains(state, unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())) {
					t.Fatalf("%q retained the profile ACE after a violated lifecycle", name)
				}
			}
		})
	}
}

// The sandbox shares the user's SID but not the package SID, so owning an object
// does not give it WRITE_DAC. This proves the kernel refuses a descriptor edit
// by the sandbox itself, on admitted and on self-created objects.
func Test_WindowsAppContainerSandboxCannotWriteDACL(t *testing.T) {
	skipUnlessWindowsSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	options := descendantSecurityFixture(t, 20*time.Millisecond)
	e, err := newWorkspaceExecution(ctx, options, kernelWriterFencePrepare)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.closeContext(context.Background(), 91) }()
	_, proof := startDescendantHelper(t, ctx, e, options.Workspace, "write-dacl")
	if got := waitDescendantProof(t, ctx, e, proof); got != "denied denied denied" {
		t.Fatal("sandbox descriptor edit outcome", got)
	}
	time.Sleep(300 * time.Millisecond)
	select {
	case <-e.monitor.done:
		t.Fatal("refused descriptor edits faulted the monitor", e.monitor.err)
	default:
	}
	if err := e.fence.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.closeContext(ctx, 91); err != nil {
		t.Fatal("close", err)
	}
}

// A change made between monitor cycles must not be followed by a launch.
func Test_WindowsAppContainerStartRefusesDescendantChange(t *testing.T) {
	skipUnlessWindowsSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	options := descendantSecurityFixture(t, time.Minute)
	e, err := newWorkspaceExecution(ctx, options, kernelWriterFencePrepare)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.closeContext(context.Background(), 91) }()
	tamperDACL(t, filepath.Join(options.Workspace, "sub", "initial.txt"))
	// The change record may lag the write by a moment. A launch attempt that
	// reaches the Job consumes its admission, so wait until the check itself can
	// see the change and then require the launch to be refused.
	began := time.Now()
	for {
		err := e.workspace.verifyDescendantSecurity(ctx, e.maxEvents, true)
		if errors.Is(err, errWorkspaceSecurityChanged) {
			t.Log("change visible after", time.Since(began))
			break
		}
		if time.Since(began) > 10*time.Second {
			t.Fatal("descriptor change never became visible", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := e.start(ctx, []string{"unused.exe"}, e.profile.Directory(), nil, nil, 0); !errors.Is(err, errWorkspaceSecurityChanged) {
		t.Fatal("launch was admitted after a descriptor change", err)
	}
	if err := e.closeContext(ctx, 91); !errors.Is(err, errWorkspaceSecurityChanged) || !e.closed {
		t.Fatal("close", err, e.closed)
	}
}

// Writers outside the sandbox can still change the tree's namespace. Ordinary
// creation, rename and deletion are tracked and reclaimed without a violation; a
// reparse point is not something this package or the granted rights can create,
// so it fails the execution and kills its processes.
func Test_WindowsAppContainerTreeExternalWriters(t *testing.T) {
	skipUnlessWindowsSandbox(t)
	for _, mode := range []string{"namespace-activity", "junction"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			base := t.TempDir()
			if mode == "junction" {
				// Reclamation stays withheld while the unproved link exists, so the
				// journal cannot be removed with the test's directory.
				var err error
				if base, err = os.MkdirTemp("", "xmesh-junction-"); err != nil {
					t.Fatal(err)
				}
			}
			options := descendantSecurityFixtureIn(t, base, 20*time.Millisecond)
			before, control := kernelJournalState(t, options.Workspace)
			e, err := newWorkspaceExecution(ctx, options, kernelWriterFencePrepare)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = e.closeContext(context.Background(), 91) }()
			monitor, journal := e.monitor, e.journal
			process, proof := startDescendantHelper(t, ctx, e, options.Workspace, "plain")
			if got := waitDescendantProof(t, ctx, e, proof); got != "ready" {
				t.Fatal("helper", got)
			}
			ext := filepath.Join(options.Workspace, "ext")
			if mode == "junction" {
				if err := os.Mkdir(ext, 0700); err != nil {
					t.Fatal(err)
				}
				makeJunction(t, ext, filepath.Dir(options.Workspace))
				select {
				case <-monitor.done:
				case <-ctx.Done():
					t.Fatal("an external reparse point did not fault the monitor")
				}
				if !errors.Is(monitor.err, errWorkspaceLinkChange) || !errors.Is(monitor.err, errWriterFenceViolated) {
					t.Fatal("monitor fault cause", monitor.err)
				}
				if result, err := windows.WaitForSingleObject(process.Process, 5000); err != nil || result != windows.WAIT_OBJECT_0 {
					t.Fatal("violation left the sandbox process running", result, err)
				}
				// Reclamation is withheld while the unproved link exists.
				if err := e.closeContext(ctx, 91); !errors.Is(err, errWriterFenceViolated) {
					t.Fatal("close did not report the violation", err)
				}
				return
			}
			if err := os.Mkdir(ext, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ext, "a.txt"), []byte("a"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ext, "b.txt"), []byte("b"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(ext, ext+"2"); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(ext+"2", "a.txt")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(options.Workspace, "sub", "initial.txt"), []byte("edited"), 0600); err != nil {
				t.Fatal(err)
			}
			time.Sleep(500 * time.Millisecond)
			select {
			case <-monitor.done:
				t.Fatal("ordinary external namespace activity faulted the monitor", monitor.err)
			default:
			}
			if err := e.closeContext(ctx, 91); err != nil {
				t.Fatal("close", err)
			}
			if after, afterControl := kernelJournalState(t, options.Workspace); string(before) != string(after) || control != afterControl {
				t.Fatal("workspace descriptor changed after the lifecycle")
			}
			sid, err := windows.StringToSid(journal.records[0].SID)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"ext2", `ext2\b.txt`, `sub\initial.txt`} {
				state, _ := kernelJournalState(t, filepath.Join(options.Workspace, name))
				if bytes.Contains(state, unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())) {
					t.Fatalf("%q retained the profile ACE", name)
				}
			}
		})
	}
}

// An identity created while a release is still editing descriptors inherits
// access from a directory that is not reclaimed yet. The release must notice it
// before declaring the workspace released, and the next release reclaims it.
func Test_WindowsAppContainerReclaimProof(t *testing.T) {
	skipUnlessWindowsSandbox(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	options := descendantSecurityFixture(t, 20*time.Millisecond)
	e, err := newWorkspaceExecution(ctx, options, kernelWriterFencePrepare)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = e.closeContext(context.Background(), 91) }()
	journal := e.journal
	sid, err := windows.StringToSid(journal.records[0].SID)
	if err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(options.Workspace, "late.txt")
	fired := 0
	releaseObjectHook = func(done, total int) {
		if done == 1 && fired == 0 {
			fired++
			if err := os.WriteFile(late, []byte("late"), 0600); err != nil {
				t.Error("late creation", err)
			}
		}
	}
	defer func() { releaseObjectHook = nil }()
	if err := e.closeContext(ctx, 91); !errors.Is(err, errWorkspaceReclaimRaced) {
		t.Fatal("a release that raced a creation was accepted", err)
	}
	if fired != 1 || journal.state.workspaceReleased || e.closed {
		t.Fatal("racing release state", fired, journal.state.workspaceReleased, e.closed)
	}
	// The object created during the release inherited the profile entry.
	if state, _ := kernelJournalState(t, late); !bytes.Contains(state, unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())) {
		t.Fatal("the racing creation did not inherit access; the scenario proved nothing")
	}
	releaseObjectHook = nil
	if err := e.closeContext(ctx, 91); err != nil {
		t.Fatal("second release", err)
	}
	if !e.closed || !journal.state.complete {
		t.Fatal("cleanup did not complete")
	}
	if state, _ := kernelJournalState(t, late); bytes.Contains(state, unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())) {
		t.Fatal("the racing creation kept the profile ACE")
	}
}

func Test_WindowsAppContainerDescendantSecurityHelper(t *testing.T) {
	if os.Getenv("XMESH_DESCENDANT_SECURITY_HELPER") != "1" {
		t.Skip("AppContainer descendant security helper")
	}
	var container, size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), 29, (*byte)(unsafe.Pointer(&container)), 4, &size); err != nil || container == 0 {
		t.Fatal("helper token", err)
	}
	args := os.Args[len(os.Args)-3:]
	workspace, proof, mode := args[0], args[1], args[2]
	report := func(text string) {
		if err := os.WriteFile(proof, []byte(text+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	directory := filepath.Join(workspace, "new-dir")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	created := filepath.Join(directory, "new.txt")
	if err := os.WriteFile(created, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	switch mode {
	case "plain":
		// Exercise the sandbox's ordinary namespace activity on admitted and new
		// objects: none of it may be mistaken for a descriptor change.
		if err := os.Rename(created, filepath.Join(directory, "renamed.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(workspace, "sub", "initial.txt"), []byte("changed contents"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(filepath.Join(workspace, "top.txt"), filepath.Join(workspace, "moved.txt")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(directory, "renamed.txt")); err != nil {
			t.Fatal(err)
		}
		report("ready")
	case "write-dacl":
		var out []string
		for _, target := range []string{filepath.Join(workspace, "sub", "initial.txt"), filepath.Join(workspace, "sub"), created} {
			everyone, err := windows.CreateWellKnownSid(windows.WinWorldSid)
			if err != nil {
				t.Fatal(err)
			}
			acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
				AccessPermissions: windows.FILE_GENERIC_READ, AccessMode: windows.GRANT_ACCESS, Inheritance: windows.NO_INHERITANCE,
				Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_WELL_KNOWN_GROUP, TrusteeValue: windows.TrusteeValueFromSID(everyone)},
			}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = windows.SetNamedSecurityInfo(target, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
			if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
				out = append(out, "denied")
			} else {
				out = append(out, "allowed:"+errText(err))
			}
		}
		report(strings.Join(out, " "))
	default:
		t.Fatal("unknown helper mode", mode)
	}
	time.Sleep(30 * time.Second)
	t.Fatal("descendant security helper was not terminated")
}

func errText(err error) string {
	if err == nil {
		return "success"
	}
	return err.Error()
}

// Parallel executions of one user grant the same objects with their own package
// SID; that must not look like an external writer, while any other principal does.
func Test_WindowsForeignPackageACE(t *testing.T) {
	ace := func(kind byte, sid string) []byte {
		parsed, err := windows.StringToSid(sid)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]byte, 8, 8+parsed.Len())
		out[0] = kind
		out = append(out, unsafe.Slice((*byte)(unsafe.Pointer(parsed)), parsed.Len())...)
		for len(out)%4 != 0 {
			out = append(out, 0)
		}
		return out
	}
	const own = "S-1-15-2-1-2-3-4-5-6-7"
	const other = "S-1-15-2-7-6-5-4-3-2-1"
	ownSID, _ := windows.StringToSid(own)
	ownRaw := unsafe.Slice((*byte)(unsafe.Pointer(ownSID)), ownSID.Len())
	for _, test := range []struct {
		name string
		ace  []byte
		want bool
	}{
		{"other package allow", ace(windows.ACCESS_ALLOWED_ACE_TYPE, other), true},
		{"other package deny", ace(windows.ACCESS_DENIED_ACE_TYPE, other), true},
		{"own package", ace(windows.ACCESS_ALLOWED_ACE_TYPE, own), false},
		{"all application packages", ace(windows.ACCESS_ALLOWED_ACE_TYPE, "S-1-15-2-1"), false},
		{"everyone", ace(windows.ACCESS_ALLOWED_ACE_TYPE, "S-1-1-0"), false},
		{"audit entry", ace(2, other), false},
	} {
		if got := foreignPackageACE(test.ace, ownRaw); got != test.want {
			t.Errorf("%s: got %v want %v", test.name, got, test.want)
		}
	}
}
