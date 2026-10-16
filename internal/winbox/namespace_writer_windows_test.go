package winbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func namespaceWriterRoot(t *testing.T) (string, *workspaceTree) {
	t.Helper()
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	path := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	tree, err := openWorkspaceRoot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tree.Close)
	return path, tree
}

func namespaceWriterHelper(t *testing.T, path, mode string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^Test_WindowsAppContainerNamespaceWriterHelper$")
	cmd.Env = append(os.Environ(), "XMESH_NAMESPACE_WRITER_MODE="+mode, "XMESH_NAMESPACE_WRITER_PATH="+path)
	if body, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("namespace helper %s: %v %s", mode, err, body)
	}
}

func Test_WindowsAppContainerNamespaceWriter(t *testing.T) {
	t.Run("identity", func(t *testing.T) {
		_, tree := namespaceWriterRoot(t)
		if _, err := namespaceWriterIdentity(nil); err == nil {
			t.Fatal("unbound lease accepted")
		}
		if handle, err := openNamespaceWriterMutex(fileIdentity{}); err == nil || handle != 0 {
			t.Fatal("empty identity accepted", err)
		}
		altered := *tree
		altered.objects = append([]treeObject(nil), tree.objects...)
		altered.objects[0].id.Volume ^= 1
		if _, err := namespaceWriterIdentity(&altered); err == nil {
			t.Fatal("wrong volume accepted")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if lease, err := acquireNamespaceWriters(ctx, tree); lease != nil || !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled lease", err)
		}
		lease, err := acquireNamespaceWriters(context.Background(), tree)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
		var flags uint32
		ok, _, err := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation").Call(uintptr(lease.handle), uintptr(unsafe.Pointer(&flags)))
		if ok == 0 || flags&windows.HANDLE_FLAG_INHERIT != 0 {
			t.Fatal("namespace lease is inheritable", flags, err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal("repeated lease release", err)
		}
	})
	t.Run("cross-process", func(t *testing.T) {
		path, tree := namespaceWriterRoot(t)
		sibling := path + "-sibling"
		if err := os.Mkdir(sibling, 0700); err != nil {
			t.Fatal(err)
		}
		lease, err := acquireNamespaceWriters(context.Background(), tree)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
		// Different roots on the same volume share the lease. No cooperating
		// mutation can run until the parent completes its namespace operation.
		namespaceWriterHelper(t, sibling, "blocked")
		if _, err := os.Stat(filepath.Join(sibling, "written")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("blocked writer changed namespace", err)
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		namespaceWriterHelper(t, sibling, "write")
		if body, err := os.ReadFile(filepath.Join(sibling, "written")); err != nil || string(body) != "serialized" {
			t.Fatal("released writer failed", err)
		}
	})
	t.Run("abandoned", func(t *testing.T) {
		path, tree := namespaceWriterRoot(t)
		id, err := namespaceWriterIdentity(tree)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := openNamespaceWriterMutex(id)
		if err != nil {
			t.Fatal(err)
		}
		defer windows.CloseHandle(handle)
		// Preserve the named kernel object while the owning process exits.
		namespaceWriterHelper(t, path, "abandon")
		if lease, err := acquireNamespaceWriters(context.Background(), tree); lease != nil || !errors.Is(err, errNamespaceWriterAbandoned) {
			t.Fatal("abandoned namespace admitted", err)
		}
		// The rejected acquisition released its ownership and OS-thread pin.
		namespaceWriterHelper(t, path, "write")
	})
	t.Run("operation-cancel", func(t *testing.T) {
		_, tree := namespaceWriterRoot(t)
		lease, err := acquireNamespaceWriters(context.Background(), tree)
		if err != nil {
			t.Fatal(err)
		}
		defer lease.Close()
		g := &workspaceGrant{operation: make(chan struct{}, 1), tree: tree}
		done := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			release, err := g.acquire(ctx)
			if release != nil {
				release()
			}
			done <- err
		}()
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("busy namespace ignored operation cancellation", err)
		}
		if len(g.operation) != 0 {
			t.Fatal("namespace failure stranded workspace gate")
		}
		if err := lease.Close(); err != nil {
			t.Fatal(err)
		}
		release, err := g.acquire(context.Background())
		if err != nil {
			t.Fatal("workspace gate unavailable after cancellation", err)
		}
		release()
	})
}

func Test_WindowsAppContainerNamespaceWriterHelper(t *testing.T) {
	mode := os.Getenv("XMESH_NAMESPACE_WRITER_MODE")
	if mode == "" {
		t.Skip("namespace writer subprocess")
	}
	path := os.Getenv("XMESH_NAMESPACE_WRITER_PATH")
	tree, err := openWorkspaceRoot(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer tree.Close()
	timeout := 5 * time.Second
	if mode == "blocked" {
		timeout = 100 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	lease, err := acquireNamespaceWriters(ctx, tree)
	if mode == "blocked" {
		if lease != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("cross-process namespace exclusion failed", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if mode == "abandon" {
		os.Exit(0)
	}
	defer lease.Close()
	if err := os.WriteFile(filepath.Join(path, "written"), []byte("serialized"), 0600); err != nil {
		t.Fatal(err)
	}
}

func Test_WindowsAppContainerNamespaceMonitorAdmission(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	f := newImportMoveFixture(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner, err := newExecutionProcesses(f.p, 1)
	if err != nil {
		t.Fatal(err)
	}
	monitor, err := f.g.monitorChanges(ctx, owner, 10*time.Millisecond, maxWorkspaceChangeEvents)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owner.reap(context.Background(), 0); err != nil {
			t.Error(err)
		}
		if err := monitor.Stop(context.Background()); err != nil {
			t.Error(err)
		}
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}()
	// Hold namespace ownership while a checkpoint queues. Concurrent invalid
	// admission must not obtain namespace while the checkpoint holds profile.mu.
	lease, err := acquireNamespaceWriters(ctx, f.g.tree)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	checkpoint := make(chan error, 1)
	go func() { checkpoint <- monitor.checkpoint(ctx) }()
	for len(f.g.operation) == 0 {
		select {
		case <-ctx.Done():
			t.Fatal("checkpoint did not queue", ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	admitted := make(chan error, 1)
	go func() {
		grant, err := f.p.grantWorkspace(ctx, f.root, 64)
		if grant != nil {
			err = errors.Join(err, grant.Close())
		}
		admitted <- err
	}()
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-checkpoint:
		if err != nil {
			t.Fatal("concurrent checkpoint", err)
		}
	case <-ctx.Done():
		t.Fatal("checkpoint lock order", ctx.Err())
	}
	select {
	case err := <-admitted:
		if err == nil {
			t.Fatal("active profile admitted another workspace")
		}
	case <-ctx.Done():
		t.Fatal("admission lock order", ctx.Err())
	}
	f.p.mu.Lock()
	closing, active := f.p.workspaceClosing, f.p.workspaceMonitor == monitor
	f.p.mu.Unlock()
	if closing || !active {
		t.Fatal("namespace contention stopped active monitor")
	}
}
