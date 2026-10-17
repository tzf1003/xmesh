package winbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func keeperTestCommand(t *testing.T, cli bool) []string {
	t.Helper()
	if cli {
		path := os.Getenv("XMESH_TEST_WINDOWS_BINARY")
		if path == "" {
			t.Skip("set XMESH_TEST_WINDOWS_BINARY to the built xmesh executable")
		}
		return []string{path, "__sandbox-guard"}
	}
	return []string{os.Args[0], "-test.run=^Test_WindowsAppContainerGuardKeeperHelper$", "--", "keeper"}
}

func Test_WindowsAppContainerGuardKeeperHelper(t *testing.T) {
	if len(os.Args) < 4 || os.Args[len(os.Args)-4] != "keeper" {
		t.Skip("inherited-handle guard keeper helper")
	}
	if err := RunGuardKeeper(os.Args[len(os.Args)-3:]); err != nil {
		t.Fatal(err)
	}
}

type keeperFixture struct {
	base, workspace, state, journal string
	key                             journalObject
}

func newKeeperFixture(t *testing.T) keeperFixture {
	t.Helper()
	f := keeperFixture{base: filepath.Join(t.TempDir(), "base")}
	if err := os.Mkdir(f.base, 0700); err != nil {
		t.Fatal(err)
	}
	f.workspace = filepath.Join(f.base, "workspace")
	if err := os.Mkdir(f.workspace, 0700); err != nil {
		t.Fatal(err)
	}
	setMediumLabel(t, f.workspace)
	f.state = filepath.Join(f.base, "state")
	privateJournalDirectory(t, f.state)
	f.journal = filepath.Join(f.state, "execution.journal")
	tree, err := openWorkspaceRoot(context.Background(), f.workspace)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	object := tree.objects[0]
	f.key = journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: true}
	return f
}

// requireRenameBlocked proves the keeper's namespace holds are effective:
// renaming any held directory fails while the keeper lives.
func requireRenameBlocked(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		moved := path + ".moved"
		if err := os.Rename(path, moved); err == nil {
			_ = os.Rename(moved, path)
			t.Fatalf("rename of %s succeeded while a keeper holds it", path)
		}
	}
}

func requireRenameAllowed(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		moved := path + ".moved"
		if err := os.Rename(path, moved); err != nil {
			t.Fatalf("rename of %s still blocked: %v", path, err)
		}
		if err := os.Rename(moved, path); err != nil {
			t.Fatal(err)
		}
	}
}

func requireKeeperAlive(t *testing.T, process windows.Handle) {
	t.Helper()
	if result, err := windows.WaitForSingleObject(process, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
		t.Fatal("guard keeper is not alive", result, err)
	}
}

func requireKeeperExitedCleanly(t *testing.T, process windows.Handle) {
	t.Helper()
	if result, err := windows.WaitForSingleObject(process, 30000); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal("guard keeper did not exit", result, err)
	}
	var code uint32
	if err := windows.GetExitCodeProcess(process, &code); err != nil || code != 0 {
		t.Fatal("guard keeper exit code", code, err)
	}
}

func openDirectoryHold(t *testing.T, path string) windows.Handle {
	t.Helper()
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(name, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES|windows.FILE_LIST_DIRECTORY,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func Test_WindowsAppContainerGuardKeeperLifecycle(t *testing.T) {
	skipUnlessChangeJournal(t)
	for _, cli := range []bool{false, true} {
		t.Run(fmt.Sprintf("cli-%t", cli), func(t *testing.T) {
			command := keeperTestCommand(t, cli)
			launch := func(t *testing.T, f keeperFixture) *guardKeeper {
				t.Helper()
				k, err := launchGuardKeeper(context.Background(), keeperLaunch{Workspace: f.workspace, Journal: f.journal, Command: command})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					// A refused or abandoned keeper must not outlive the test.
					if k.process != 0 {
						_ = windows.TerminateProcess(k.process, 1)
					}
				})
				return k
			}
			bindOwned := func(t *testing.T, k *guardKeeper, f keeperFixture) {
				t.Helper()
				j, err := openBoundGrantJournal(context.Background(), f.journal, false)
				if err != nil {
					t.Fatal(err)
				}
				defer j.Close()
				if err := k.bindJournal(context.Background(), j); err != nil {
					t.Fatal(err)
				}
				if err := k.bindJournal(context.Background(), j); err == nil {
					t.Fatal("journal identity rebound")
				}
			}

			t.Run("absent journal", func(t *testing.T) {
				f := newKeeperFixture(t)
				k := launch(t, f)
				process := k.process
				requireRenameBlocked(t, f.workspace, f.base, f.state)
				requireKeeperAlive(t, process)
				// A second keeper cannot claim the same lease key.
				if second, err := launchGuardKeeper(context.Background(), keeperLaunch{Workspace: f.workspace, Journal: f.journal, Command: command}); err == nil {
					second.Close()
					t.Fatal("two keepers claimed one workspace")
				}
				requireKeeperAlive(t, process)
				requireRenameBlocked(t, f.workspace)
				// Never-created journals carry no obligation.
				dup, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, mustPID(t, process))
				if err != nil {
					t.Fatal(err)
				}
				defer windows.CloseHandle(dup)
				if err := k.Close(); err != nil {
					t.Fatal(err)
				}
				if err := k.Close(); err != nil {
					t.Fatal("close retry", err)
				}
				requireKeeperExitedCleanly(t, dup)
				requireRenameAllowed(t, f.workspace, f.base, f.state)
			})

			t.Run("holds", func(t *testing.T) {
				f := newKeeperFixture(t)
				k := launch(t, f)
				source := filepath.Join(f.base, "source")
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
				requireRenameAllowed(t, source)
				h := openDirectoryHold(t, source)
				if err := k.Hold(context.Background(), h); err != nil {
					t.Fatal(err)
				}
				if err := k.Hold(context.Background(), h); err == nil {
					t.Fatal("duplicate hold accepted")
				}
				// The keeper owns its duplicate; the caller's handle is independent.
				windows.CloseHandle(h)
				requireRenameBlocked(t, source, f.workspace)
				requireKeeperAlive(t, k.process)
				dup, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, mustPID(t, k.process))
				if err != nil {
					t.Fatal(err)
				}
				defer windows.CloseHandle(dup)
				if err := k.Close(); err != nil {
					t.Fatal(err)
				}
				requireKeeperExitedCleanly(t, dup)
				requireRenameAllowed(t, source, f.workspace)
			})

			for _, mode := range []string{"complete", "incomplete", "unbound"} {
				t.Run(mode, func(t *testing.T) {
					f := newKeeperFixture(t)
					dispatchProfile(t, f.state, filepath.Base(f.journal), mode == "complete")
					k := launch(t, f)
					dup, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, mustPID(t, k.process))
					if err != nil {
						t.Fatal(err)
					}
					defer windows.CloseHandle(dup)
					if mode != "unbound" {
						bindOwned(t, k, f)
					}
					// The keeper cannot be released while its host holds the journal.
					held, err := openBoundGrantJournal(context.Background(), f.journal, false)
					if err != nil {
						t.Fatal(err)
					}
					if err := k.Close(); err == nil {
						t.Fatal("keeper released while the journal was still owned")
					}
					held.Close()
					if mode == "complete" {
						if err := k.Close(); err != nil {
							t.Fatal(err)
						}
						requireKeeperExitedCleanly(t, dup)
						requireRenameAllowed(t, f.workspace, f.base, f.state)
						return
					}
					if err := k.Close(); err == nil {
						t.Fatal("keeper released with an outstanding recovery obligation")
					}
					requireKeeperAlive(t, dup)
					requireRenameBlocked(t, f.workspace, f.base, f.state)
				})
			}

			t.Run("no keeper", func(t *testing.T) {
				f := newKeeperFixture(t)
				if _, err := adoptGuardKeeper(context.Background(), f.key); !errors.Is(err, errKeeperAbsent) {
					t.Fatal("adoption without a keeper", err)
				}
				called := 0
				prepare := keeperRecoveryPrepare(func(context.Context, journalRecoveryInfo) (*journalRecoveryLease, error) {
					called++
					return &journalRecoveryLease{Guard: recoveryTestGuard(func() error { return nil })}, nil
				})
				info := journalRecoveryInfo{Workspace: &journalWorkspace{Root: f.key}}
				if lease, err := prepare(context.Background(), info); err != nil || lease == nil || called != 1 {
					t.Fatal("fallback was not used for a missing keeper", err, called)
				}
				info.Workspace = nil
				if _, err := prepare(context.Background(), info); err != nil || called != 2 {
					t.Fatal("selected-object journal bypassed the fallback", err, called)
				}
				strict := keeperRecoveryPrepare(nil)
				info.Workspace = &journalWorkspace{Root: f.key}
				if lease, err := strict(context.Background(), info); !errors.Is(err, errKeeperAbsent) || lease != nil {
					t.Fatal("missing keeper did not fail closed", err)
				}
			})
		})
	}
}

func mustPID(t *testing.T, process windows.Handle) uint32 {
	t.Helper()
	pid, err := windows.GetProcessId(process)
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

type keeperCrashProof struct {
	Keeper, Supervisor, Member uint64
	KeeperPID, SupervisorPID   uint32
	Error                      string
}

// Test_WindowsAppContainerGuardKeeperHostCrash is the closed loop: a complete
// workspace execution with a keeper-backed writer guard loses its host process,
// the keeper survives it, and a recovery owner adopts the same keeper, recovers
// under it, and only then releases it after the keeper verifies the journal.
func Test_WindowsAppContainerGuardKeeperHostCrash(t *testing.T) {
	skipUnlessChangeJournal(t)
	supervisorCommand := supervisorTestCommand(t, true)
	keeperCommand := keeperTestCommand(t, true)
	f := newKeeperFixture(t)
	file := filepath.Join(f.workspace, "file.txt")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	setMediumLabel(t, file)
	original, control := kernelJournalState(t, f.workspace)
	originalFile, fileControl := kernelJournalState(t, file)
	label := objectLabel(t, f.workspace)

	var controller windows.Handle
	if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.CurrentProcess(), windows.CurrentProcess(), &controller, windows.PROCESS_DUP_HANDLE|windows.SYNCHRONIZE, false, 0); err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(controller)
	if err := windows.SetHandleInformation(controller, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^Test_WindowsAppContainerGuardKeeperHostHelper$")
	cmd.Env = append(os.Environ(), "XMESH_KEEPER_HOST_WORKSPACE="+f.workspace, "XMESH_KEEPER_HOST_JOURNAL="+f.journal,
		"XMESH_KEEPER_HOST_CONTROLLER="+strconv.FormatUint(uint64(controller), 10),
		"XMESH_KEEPER_HOST_SUPERVISOR="+supervisorCommand[0], "XMESH_KEEPER_HOST_KEEPER="+keeperCommand[0])
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: []syscall.Handle{syscall.Handle(controller)}}
	cmd.Stderr = os.Stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	type result struct {
		proof keeperCrashProof
		err   error
	}
	ready := make(chan result, 1)
	go func() {
		var p keeperCrashProof
		line, err := bufio.NewReader(stdout).ReadBytes('\n')
		if err == nil {
			err = json.Unmarshal(line, &p)
		}
		ready <- result{p, err}
	}()
	var proof keeperCrashProof
	select {
	case r := <-ready:
		if r.err != nil || r.proof.Error != "" {
			t.Fatal("host readiness", r)
		}
		proof = r.proof
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	keeper, supervisor, member := windows.Handle(proof.Keeper), windows.Handle(proof.Supervisor), windows.Handle(proof.Member)
	defer windows.CloseHandle(keeper)
	defer windows.CloseHandle(supervisor)
	defer windows.CloseHandle(member)
	for i, h := range []windows.Handle{keeper, supervisor} {
		want := []uint32{proof.KeeperPID, proof.SupervisorPID}[i]
		if pid, err := windows.GetProcessId(h); err != nil || pid != want {
			t.Fatal("transferred identity", i, pid, want, err)
		}
	}
	requireKeeperAlive(t, keeper)
	requireKeeperAlive(t, member)
	requireRenameBlocked(t, f.workspace, f.base, f.state)
	// A live host keeps the keeper out of reach of an adoption client.
	short, stop := context.WithTimeout(ctx, 400*time.Millisecond)
	if a, err := adoptGuardKeeper(short, f.key); err == nil {
		a.Close()
		t.Fatal("keeper adopted while its host was alive")
	}
	stop()

	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if result, err := windows.WaitForSingleObject(supervisor, 30000); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal("supervisor did not finish after host exit", result, err)
	}
	if result, err := windows.WaitForSingleObject(member, 30000); err != nil || result != windows.WAIT_OBJECT_0 {
		t.Fatal("sandbox member survived host and supervisor", result, err)
	}
	// The decisive property: the host and the supervisor are gone, no process of
	// this test holds the workspace, and the namespace is still pinned.
	requireKeeperAlive(t, keeper)
	requireRenameBlocked(t, f.workspace, f.base, f.state)

	adoptCtx, adoptCancel := context.WithTimeout(ctx, 60*time.Second)
	defer adoptCancel()
	first, err := adoptGuardKeeper(adoptCtx, f.key)
	if err != nil {
		t.Fatal(err)
	}
	if first.state.State != keeperStateOrphaned || first.state.PID != proof.KeeperPID {
		t.Fatal("unexpected adopted keeper state", first.state)
	}
	var sawRoot, sawAnchor, sawJournal bool
	for _, held := range first.state.Held {
		sawRoot = sawRoot || (held.Role == keeperRoleRoot && held.Object == f.key)
		sawAnchor = sawAnchor || held.Role == keeperRoleAnchor
		sawJournal = sawJournal || held.Role == keeperRoleJourn
	}
	if !sawRoot || !sawAnchor || !sawJournal || first.state.Journal == nil {
		t.Fatal("adopted keeper lacks expected holds", first.state)
	}
	// Exactly one recovery owner at a time.
	crowded, stop := context.WithTimeout(ctx, 400*time.Millisecond)
	if second, err := adoptGuardKeeper(crowded, f.key); err == nil {
		second.Close()
		t.Fatal("two recovery owners adopted one keeper")
	}
	stop()
	// An incomplete journal cannot release the keeper, however it is asked.
	if err := first.release(adoptCtx); err == nil {
		t.Fatal("keeper released before recovery completed")
	}
	requireKeeperAlive(t, keeper)
	first.Close()
	// A recovery owner that vanishes mid-recovery leaves the keeper adoptable.
	again, err := adoptGuardKeeper(adoptCtx, f.key)
	if err != nil {
		t.Fatal("re-adoption after an owner disconnect", err)
	}
	again.Close()

	inner := keeperRecoveryPrepare(nil)
	prepared, guardClosed := 0, 0
	report, err := recoverJournalDirectory(adoptCtx, f.state, 4, 32, func(ctx context.Context, info journalRecoveryInfo) (*journalRecoveryLease, error) {
		prepared++
		lease, err := inner(ctx, info)
		if err != nil {
			return nil, err
		}
		adopted := lease.Guard
		lease.Guard = recoveryTestGuard(func() error {
			guardClosed++
			// Recovery has finished and been flushed; the namespace must still be
			// pinned at this point and until the keeper itself verifies completion.
			requireKeeperAlive(t, keeper)
			requireRenameBlocked(t, f.workspace, f.base, f.state)
			return adopted.Close()
		})
		return lease, nil
	})
	if err != nil || prepared != 1 || guardClosed != 1 || len(report) != 1 || report[0].State != "recovered" {
		t.Fatal("recovery under the surviving keeper", report, err, prepared, guardClosed)
	}
	requireKeeperAlive(t, keeper)
	requireRenameBlocked(t, f.workspace, f.base, f.state)
	if err := releaseCompletedKeepers(adoptCtx, f.state, 4); err != nil {
		t.Fatal(err)
	}
	requireKeeperExitedCleanly(t, keeper)
	requireRenameAllowed(t, f.workspace, f.base, f.state)
	if err := releaseCompletedKeepers(adoptCtx, f.state, 4); err != nil {
		t.Fatal("repeated release with no keeper", err)
	}

	after, afterControl := kernelJournalState(t, f.workspace)
	afterFile, afterFileControl := kernelJournalState(t, file)
	if !bytes.Equal(after, original) || afterControl != control || !bytes.Equal(afterFile, originalFile) || afterFileControl != fileControl || objectLabel(t, f.workspace) != label {
		t.Fatal("recovery under the keeper did not restore original security")
	}
	j, err := openBoundGrantJournal(context.Background(), f.journal, false)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if !j.state.complete || len(j.state.executions) != 0 {
		t.Fatal("completion did not persist")
	}
}

func Test_WindowsAppContainerGuardKeeperHostHelper(t *testing.T) {
	workspace := os.Getenv("XMESH_KEEPER_HOST_WORKSPACE")
	if workspace == "" {
		t.Skip("forced-exit keeper host")
	}
	fail := func(err error) {
		json.NewEncoder(os.Stdout).Encode(keeperCrashProof{Error: err.Error()})
		t.Fatal(err)
	}
	keeperBinary := os.Getenv("XMESH_KEEPER_HOST_KEEPER")
	options := workspaceExecutionOptions{Workspace: workspace, Journal: os.Getenv("XMESH_KEEPER_HOST_JOURNAL"),
		MaxObjects: 32, MaxJobs: 2, Interval: 20 * time.Millisecond, MaxEvents: maxWorkspaceChangeEvents,
		SupervisorCommand: []string{os.Getenv("XMESH_KEEPER_HOST_SUPERVISOR"), "__sandbox-supervisor"}}
	ctx := context.Background()
	e, err := newWorkspaceExecution(ctx, options, func(ctx context.Context, o workspaceExecutionOptions) (io.Closer, error) {
		return launchGuardKeeper(ctx, keeperLaunch{Workspace: o.Workspace, Journal: o.Journal, Command: []string{keeperBinary, "__sandbox-guard"}})
	})
	if err != nil {
		fail(err)
	}
	body, err := sandboxHelperImage()
	if err != nil {
		fail(err)
	}
	helper := filepath.Join(e.profile.Directory(), "keeper-member.exe")
	if err := os.WriteFile(helper, body, 0700); err != nil {
		fail(err)
	}
	member, err := e.start(ctx, []string{helper, "-test.run=^Test_WindowsAppContainerProcessOwnerHelper$", "--", "member", "unused"},
		e.profile.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_PROCESS_OWNER_HELPER=1"}, nil, 0)
	if err != nil {
		fail(err)
	}
	n, err := strconv.ParseUint(os.Getenv("XMESH_KEEPER_HOST_CONTROLLER"), 10, 64)
	if err != nil {
		fail(err)
	}
	controller := windows.Handle(n)
	keeper := e.guard.(*guardKeeper)
	var transferred [3]windows.Handle
	for i, source := range []windows.Handle{keeper.process, e.processes.supervisor.process, member.Process} {
		if err := windows.DuplicateHandle(windows.CurrentProcess(), source, controller, &transferred[i], windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, 0); err != nil {
			fail(err)
		}
	}
	supervisorPID, err := windows.GetProcessId(e.processes.supervisor.process)
	if err != nil {
		fail(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(keeperCrashProof{Keeper: uint64(transferred[0]), Supervisor: uint64(transferred[1]), Member: uint64(transferred[2]),
		KeeperPID: mustPID(t, keeper.process), SupervisorPID: supervisorPID}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(120 * time.Second)
	t.Fatal("controller did not terminate host")
}
