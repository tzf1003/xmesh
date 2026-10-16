package winbox

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"golang.org/x/sys/windows"
)

var errJournalDirectoryTimeout = errors.New("AppContainer journal directory coordination timed out")

// Creation and recovery sweeps share an identity-bound mutex. An exclusive
// journal remains independently owned after the creation lock is released.
// Default kernel-object security limits participants to compatible contexts;
// cross-user/service-session deployment requires its own acceptance.
func acquireJournalDirectory(ctx context.Context, id fileIdentity) (*objectACLLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if id.ID == [16]byte{} {
		return nil, fmt.Errorf("AppContainer journal coordination requires a directory identity")
	}
	name, err := windows.UTF16PtrFromString(fmt.Sprintf(`Global\xmesh-l2-journal-v1-%016x-%x`, id.Volume, id.ID))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutexEx(nil, name, 0, windows.SYNCHRONIZE|windows.MUTEX_MODIFY_STATE)
	if err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return nil, fmt.Errorf("open AppContainer journal coordination mutex: %w", err)
	}
	if h == 0 {
		return nil, fmt.Errorf("AppContainer journal coordination mutex unavailable")
	}
	runtime.LockOSThread()
	retained := false
	defer func() {
		if !retained {
			windows.CloseHandle(h)
			runtime.UnlockOSThread()
		}
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		result, err := windows.WaitForSingleObject(h, 50)
		if err != nil {
			return nil, fmt.Errorf("wait for AppContainer journal coordination: %w", err)
		}
		if result == windows.WAIT_OBJECT_0 || result == windows.WAIT_ABANDONED {
			retained = true
			return &objectACLLock{handle: h, abandoned: result == windows.WAIT_ABANDONED}, nil
		}
		if result != uint32(windows.WAIT_TIMEOUT) {
			return nil, fmt.Errorf("wait for AppContainer journal coordination: unexpected result %#x", result)
		}
		if time.Now().After(deadline) {
			return nil, errJournalDirectoryTimeout
		}
	}
}
