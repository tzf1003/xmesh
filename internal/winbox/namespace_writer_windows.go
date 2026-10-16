//go:build windows

package winbox

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

var (
	errNamespaceWriterTimeout   = errors.New("AppContainer namespace writer coordination timed out")
	errNamespaceWriterAbandoned = errors.New("AppContainer namespace writer exited before releasing its lease")
)

// namespaceWriterLease serializes participating xmesh operations on a local
// volume. A mutex keyed by the verified volume serial covers ancestor overlap and same-volume
// imports without nested multi-object lock acquisition. Uncoordinated filesystem,
// reparse and ACL writers still require the caller's external writer guard.
// Acquisition precedes profile.mu; Close runs on the acquiring goroutine.
// Mutex ownership and runtime.LockOSThread last only for one operation.
type namespaceWriterLease struct{ handle windows.Handle }

func namespaceWriterIdentity(tree *workspaceTree) (fileIdentity, error) {
	if tree == nil || len(tree.objects) == 0 || len(tree.ancestors) == 0 {
		return fileIdentity{}, fmt.Errorf("AppContainer namespace coordination requires a bound workspace")
	}
	anchor, err := inspectTreeObject(tree.ancestors[0], "")
	if err != nil {
		return fileIdentity{}, err
	}
	if !anchor.directory || anchor.id.ID == [16]byte{} || anchor.id.Volume != tree.objects[0].id.Volume {
		return fileIdentity{}, fmt.Errorf("AppContainer namespace coordination volume identity mismatch")
	}
	return anchor.id, nil
}

func openNamespaceWriterMutex(id fileIdentity) (windows.Handle, error) {
	if id.ID == [16]byte{} {
		return 0, fmt.Errorf("AppContainer namespace coordination requires an object identity")
	}
	// Drive aliases can select different anchor directories on the same volume.
	// Key by the kernel volume serial so those bindings still coordinate. A serial
	// collision between distinct volumes only serializes additional operations.
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Global\xmesh-l2-namespace-v1-%016x`, id.Volume))
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateMutexEx(nil, name, 0, windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return 0, fmt.Errorf("open AppContainer namespace coordination mutex: %w", err)
	}
	if handle == 0 {
		return 0, fmt.Errorf("AppContainer namespace coordination mutex unavailable")
	}
	return handle, nil
}

func acquireNamespaceWriters(ctx context.Context, tree *workspaceTree) (*namespaceWriterLease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, err := namespaceWriterIdentity(tree)
	if err != nil {
		return nil, err
	}
	handle, err := openNamespaceWriterMutex(id)
	if err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	retained := false
	defer func() {
		if !retained {
			_ = windows.CloseHandle(handle)
			runtime.UnlockOSThread()
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := windows.WaitForSingleObject(handle, 25)
		if err != nil {
			return nil, fmt.Errorf("wait for AppContainer namespace coordination: %w", err)
		}
		if result == windows.WAIT_OBJECT_0 || result == windows.WAIT_ABANDONED {
			lease := &namespaceWriterLease{handle: handle}
			retained = true
			// A crashed namespace transaction may have partially changed names.
			// Report it before any journal or descriptor mutation. Recovery and
			// complete identity proof remain separate requirements.
			if result == windows.WAIT_ABANDONED {
				return nil, errors.Join(errNamespaceWriterAbandoned, lease.Close())
			}
			if err := ctx.Err(); err != nil {
				return nil, errors.Join(err, lease.Close())
			}
			return lease, nil
		}
		if result != uint32(windows.WAIT_TIMEOUT) {
			return nil, fmt.Errorf("wait for AppContainer namespace coordination: unexpected result %#x", result)
		}
		if time.Now().After(deadline) {
			return nil, errNamespaceWriterTimeout
		}
	}
}

func (l *namespaceWriterLease) Close() error {
	if l == nil || l.handle == 0 {
		return nil
	}
	err := windows.ReleaseMutex(l.handle)
	// Keep ownership and thread affinity if release unexpectedly fails.
	if err != nil {
		return err
	}
	err = windows.CloseHandle(l.handle)
	l.handle = 0
	runtime.UnlockOSThread()
	return err
}
