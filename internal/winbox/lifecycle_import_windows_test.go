package winbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func Test_WindowsAppContainerWorkspaceExecutionImport(t *testing.T) {
	skipUnlessChangeJournal(t)
	for _, stage := range []string{"directory", "file", "prepare-error", "missing-guard", "cancel-prepared", "collision", "guard-retry"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			options := workspaceExecutionTestOptions(t)
			source := filepath.Join(filepath.Dir(options.Workspace), "source")
			destination := filepath.Join(options.Workspace, "imported")
			file := stage == "file"
			if file {
				if err := os.WriteFile(source, []byte("source"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Mkdir(source, 0700); err != nil {
				t.Fatal(err)
			}
			setMediumLabel(t, source)
			before, control := kernelJournalState(t, source)
			label := objectLabel(t, source)
			var order []string
			var e *workspaceExecution
			complete := func() error {
				if e.journal != nil || e.profile != nil || e.workspace != nil || e.processes != nil || e.monitor != nil {
					return errors.New("source guard released before owned resources")
				}
				j, err := openBoundGrantJournal(context.Background(), options.Journal, false)
				if err != nil {
					return err
				}
				defer j.Close()
				if !j.state.complete {
					return errors.New("source guard released before durable completion")
				}
				return nil
			}
			var err error
			e, err = newWorkspaceExecution(ctx, options, func(context.Context, workspaceExecutionOptions) (io.Closer, error) {
				return recoveryTestGuard(func() error { order = append(order, "workspace"); return complete() }), nil
			})
			if err != nil {
				t.Fatal(err)
			}
			importCount := func() int {
				e.profile.mu.Lock()
				defer e.profile.mu.Unlock()
				return len(recoveryJournalInfo(e.journal).Imports)
			}
			defer func() {
				if err := e.closeContext(context.Background(), 1); err != nil {
					t.Error("import lifecycle cleanup", err)
				}
			}()
			if stage == "collision" {
				if err := os.WriteFile(destination, []byte("occupied"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			closes := 0
			sentinel := errors.New("source coordination failure")
			call, stop := context.WithCancel(ctx)
			defer stop()
			moved, importErr := e.moveImport(call, source, destination, func(_ context.Context, from, to string) (io.Closer, error) {
				if from != source || to != destination || importCount() != 0 {
					t.Fatal("source coordination did not precede census")
				}
				guard := recoveryTestGuard(func() error {
					closes++
					if err := complete(); err != nil {
						return err
					}
					if stage == "guard-retry" && closes == 1 {
						return sentinel
					}
					order = append(order, "source")
					return nil
				})
				switch stage {
				case "prepare-error":
					return guard, sentinel
				case "missing-guard":
					return nil, nil
				case "cancel-prepared":
					stop()
				}
				return guard, nil
			})
			wantMove := stage == "directory" || stage == "file" || stage == "guard-retry"
			if moved != wantMove || (importErr == nil) != wantMove || closes != 0 {
				t.Fatal("import result/retained coordination", moved, importErr, closes)
			}
			if stage == "prepare-error" && !errors.Is(importErr, sentinel) || stage == "cancel-prepared" && !errors.Is(importErr, context.Canceled) {
				t.Fatal("import lost preparation error", importErr)
			}
			if wantMove {
				if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) || importCount() != 1 {
					t.Fatal("import namespace/census", err)
				}
				// Exercise imported access through the lifecycle's supervised start.
				helper := filepath.Join(e.profile.Directory(), "import-lifecycle-helper.exe")
				body, err := os.ReadFile(os.Args[0])
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(helper, body, 0700); err != nil {
					t.Fatal(err)
				}
				proof := filepath.Join(e.profile.Directory(), "proof")
				mode := "directory"
				if file {
					mode = "file"
				}
				process, err := e.start(ctx, []string{helper, "-test.run=^Test_WindowsAppContainerWorkspaceMoveImportHelper$", "--", destination, mode, proof},
					e.profile.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_MOVE_IMPORT_HELPER=1"}, nil, 0)
				if err != nil {
					t.Fatal(err)
				}
				defer windows.CloseHandle(process.Process)
				defer windows.CloseHandle(process.Thread)
				if status, err := windows.WaitForSingleObject(process.Process, 10000); err != nil || status != windows.WAIT_OBJECT_0 {
					t.Fatal("imported access helper exit", status, err)
				}
				if data, err := os.ReadFile(proof); err != nil || string(data) != "imported" {
					t.Fatal("imported access proof", err)
				}
			} else if _, err := os.Stat(source); err != nil || importCount() != 0 {
				t.Fatal("failed preparation changed source/census", err)
			}
			if stage == "guard-retry" {
				if err := e.closeContext(ctx, 1); !errors.Is(err, sentinel) || e.closed || len(e.imports) != 1 || e.guard == nil || len(order) != 0 {
					t.Fatal("source guard failure lost cleanup ownership", err)
				}
				if _, err := e.moveImport(ctx, source, destination, nil); err == nil {
					t.Fatal("closing lifecycle admitted import")
				}
			}
			if err := e.closeContext(ctx, 1); err != nil {
				t.Fatal("import cleanup", err)
			}
			wantOrder := []string{"source", "workspace"}
			if stage == "missing-guard" {
				wantOrder = []string{"workspace"}
			}
			if !e.closed || len(e.imports) != 0 || !reflect.DeepEqual(order, wantOrder) {
				t.Fatal("source/workspace release order", order)
			}
			path := source
			if wantMove {
				path = destination
			}
			after, afterControl := kernelJournalState(t, path)
			if !bytes.Equal(before, after) || control != afterControl || label != objectLabel(t, path) {
				t.Fatal("import lifecycle changed unrelated descriptor/control/label")
			}
			if err := e.closeContext(ctx, 1); err != nil || !reflect.DeepEqual(order, wantOrder) {
				t.Fatal("duplicate close released guards twice", err)
			}
		})
	}
}

func Test_WindowsAppContainerWorkspaceExecutionImportAdmission(t *testing.T) {
	// Lexical/lifecycle rejection must precede the source coordinator and I/O.
	ctx, cancel := context.WithCancel(context.Background())
	e := &workspaceExecution{operation: make(chan struct{}, 1), lifetime: ctx, ready: true, workspace: &workspaceGrant{}}
	called := false
	prepare := func(context.Context, string, string) (io.Closer, error) { called = true; return nil, nil }
	for _, stage := range []string{"source", "destination", "limit", "closed", "lifetime", "call"} {
		t.Run(stage, func(t *testing.T) {
			source, destination := `C:\source`, `C:\workspace\target`
			call := context.Background()
			switch stage {
			case "source":
				source = "relative"
			case "destination":
				destination = `C:\workspace\..\target`
			case "limit":
				e.imports = make([]io.Closer, 256)
			case "closed":
				e.closed = true
			case "lifetime":
				cancel()
			case "call":
				call = ctx
			}
			if moved, err := e.moveImport(call, source, destination, prepare); moved || err == nil || called {
				t.Fatal("invalid import reached source coordination", moved, err, called)
			}
			e.imports, e.closed = nil, false
		})
	}
	cancel()
}

func Test_WindowsAppContainerWorkspaceExecutionImportFault(t *testing.T) {
	skipUnlessChangeJournal(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	options := workspaceExecutionTestOptions(t)
	e, err := newWorkspaceExecution(ctx, options, func(context.Context, workspaceExecutionOptions) (io.Closer, error) {
		return recoveryTestGuard(func() error { return nil }), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := e.closeContext(context.Background(), 1); err != nil {
			t.Error(err)
		}
	}()
	helper := filepath.Join(e.profile.Directory(), "import-fault-helper.exe")
	body, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(helper, body, 0700); err != nil {
		t.Fatal(err)
	}
	process, err := e.start(ctx, []string{helper, "-test.run=^Test_WindowsAppContainerProcessOwnerHelper$", "--", "member", "unused"},
		e.profile.Directory(), []string{"SystemRoot=" + os.Getenv("SystemRoot"), "XMESH_PROCESS_OWNER_HELPER=1"}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	source := filepath.Join(filepath.Dir(options.Workspace), "source")
	destination := filepath.Join(options.Workspace, "imported")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	setMediumLabel(t, source)
	before, control := kernelJournalState(t, source)
	tree, err := openImportTree(ctx, source, 32)
	if err != nil {
		t.Fatal(err)
	}
	id := tree.objects[0].id
	tree.Close()
	lock, err := acquireObjectACL(id, 5000)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	closes := 0
	call, stop := context.WithCancel(ctx)
	defer stop()
	type outcome struct {
		moved bool
		err   error
	}
	done := make(chan outcome, 1)
	go func() {
		moved, err := e.moveImport(call, source, destination, func(context.Context, string, string) (io.Closer, error) {
			return recoveryTestGuard(func() error { closes++; return nil }), nil
		})
		done <- outcome{moved, err}
	}()
	// The object ACL lock holds import after the kernel rename. Launch and close
	// must wait on the same lifecycle gate, with their own cancellation intact.
	for {
		if _, err := os.Stat(destination); err == nil {
			break
		}
		select {
		case result := <-done:
			t.Fatal("import ended before fault boundary", result)
		case <-ctx.Done():
			t.Fatal("import fault boundary timed out", ctx.Err())
		case <-time.After(5 * time.Millisecond):
		}
	}
	wait, finish := context.WithTimeout(ctx, 30*time.Millisecond)
	if err := e.closeContext(wait, 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("concurrent close bypassed import", err)
	}
	finish()
	wait, finish = context.WithTimeout(ctx, 30*time.Millisecond)
	if _, err := e.start(wait, []string{"unused.exe"}, "", nil, nil, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("concurrent launch bypassed import", err)
	}
	finish()
	stop()
	lock.Close()
	result := <-done
	if !result.moved || !errors.Is(result.err, context.Canceled) || e.ready || closes != 0 || len(e.imports) != 1 {
		t.Fatal("post-move failure lost admission seal/source ownership", result, e.ready, closes)
	}
	e.profile.mu.Lock()
	confirmed := len(recoveryJournalInfo(e.journal).Imports) == 1 && e.profile.processesConfirmed
	e.profile.mu.Unlock()
	if !confirmed {
		t.Fatal("post-move failure lost durable import/process proof")
	}
	if status, err := windows.WaitForSingleObject(process.Process, 0); err != nil || status != windows.WAIT_OBJECT_0 {
		t.Fatal("failed import retained a live supervised process", status, err)
	}
	if _, err := e.start(ctx, []string{"unused.exe"}, "", nil, nil, 0); err == nil {
		t.Fatal("failed import admitted another process")
	}
	monitor := e.monitor
	if err := e.closeContext(ctx, 1); closes != 1 || !e.closed || err != nil && (monitor.err == nil || !errors.Is(err, monitor.err)) {
		t.Fatal("failed import cleanup", err, closes)
	}
	after, afterControl := kernelJournalState(t, destination)
	if !bytes.Equal(before, after) || control != afterControl {
		t.Fatal("failed import cleanup changed unrelated ACL/control")
	}
}

func Test_WindowsAppContainerWorkspaceExecutionImportGuardOrder(t *testing.T) {
	// Exercise a partial reverse-order release independently of filesystem setup.
	ctx := context.Background()
	var order []int
	sentinel := errors.New("retry source guard")
	fail := true
	e := &workspaceExecution{operation: make(chan struct{}, 1), lifetime: ctx,
		guard: recoveryTestGuard(func() error { order = append(order, 0); return nil }),
		imports: []io.Closer{
			recoveryTestGuard(func() error { order = append(order, 1); return nil }),
			recoveryTestGuard(func() error {
				if fail {
					fail = false
					return sentinel
				}
				order = append(order, 2)
				return nil
			}),
			recoveryTestGuard(func() error { order = append(order, 3); return nil }),
		}}
	if err := e.closeContext(ctx, 1); !errors.Is(err, sentinel) || len(e.imports) != 2 || e.closed || !reflect.DeepEqual(order, []int{3}) {
		t.Fatal("partial import guard release lost ownership", order, err)
	}
	if err := e.closeContext(ctx, 1); err != nil || !e.closed || !reflect.DeepEqual(order, []int{3, 2, 1, 0}) {
		t.Fatal("source guard release retry/order", order, err)
	}
}
