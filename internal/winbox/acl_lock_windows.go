package winbox

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

var errObjectACLTimeout = errors.New("AppContainer object ACL coordination timed out")

// Use the object identity across processes and Windows sessions. The kernel
// object's default DACL restricts access to its creator's security context;
// inaccessible coordination objects stop ACL admission. External ACL writers
// must use the same coordination protocol or be quiesced by the workspace owner.
func openObjectACLMutex(id fileIdentity) (windows.Handle, error) {
	if id.ID == [16]byte{} {
		return 0, fmt.Errorf("AppContainer ACL coordination requires an object identity")
	}
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Global\xmesh-l2-acl-v1-%016x-%x`, id.Volume, id.ID))
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateMutexEx(nil, name, 0, windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return 0, fmt.Errorf("open AppContainer ACL coordination mutex: %w", err)
	}
	if h == 0 {
		return 0, fmt.Errorf("AppContainer ACL coordination mutex unavailable")
	}
	return h, nil
}

type objectACLLock struct {
	handle    windows.Handle
	abandoned bool
}

// Windows mutex ownership belongs to an OS thread. The acquiring goroutine
// stays on that thread until Close, including all descriptor reads and writes.
// An abandoned mutex is acquired normally: descriptor writes are atomic and
// every edit rereads the current descriptor. Profile/crash recovery is separate.
func acquireObjectACL(id fileIdentity, timeoutMillis uint32) (*objectACLLock, error) {
	return acquireObjectACLContext(context.Background(), id, timeoutMillis)
}

func acquireObjectACLContext(ctx context.Context, id fileIdentity, timeoutMillis uint32) (*objectACLLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h, err := openObjectACLMutex(id)
	if err != nil {
		return nil, err
	}
	runtime.LockOSThread()
	retained := false
	defer func() {
		if !retained {
			windows.CloseHandle(h)
			runtime.UnlockOSThread()
		}
	}()
	deadline := time.Now().Add(time.Duration(timeoutMillis) * time.Millisecond)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		waitMillis := uint32(0)
		if remaining := time.Until(deadline); remaining > 0 {
			waitMillis = uint32(min((remaining+time.Millisecond-1)/time.Millisecond, 25))
		}
		result, err := windows.WaitForSingleObject(h, waitMillis)
		if err == nil && (result == windows.WAIT_OBJECT_0 || result == windows.WAIT_ABANDONED) {
			lock := &objectACLLock{handle: h, abandoned: result == windows.WAIT_ABANDONED}
			retained = true
			if err := ctx.Err(); err != nil {
				lock.Close()
				return nil, err
			}
			return lock, nil
		}
		if result != uint32(windows.WAIT_TIMEOUT) || err != nil {
			return nil, fmt.Errorf("wait for AppContainer ACL coordination: result=%#x error=%v", result, err)
		}
		if !time.Now().Before(deadline) {
			return nil, errObjectACLTimeout
		}
	}
}

// Close must run in the acquiring goroutine. The handle remains private to the
// guard, and a successful wait establishes ReleaseMutex's ownership precondition.
func (l *objectACLLock) Close() {
	if l.handle == 0 {
		return
	}
	windows.ReleaseMutex(l.handle)
	windows.CloseHandle(l.handle)
	l.handle = 0
	runtime.UnlockOSThread()
}
