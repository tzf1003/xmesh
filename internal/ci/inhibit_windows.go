//go:build windows

package ci

import (
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

const (
	esContinuous     = 0x80000000
	esSystemRequired = 0x00000001
)

// SystemInhibitor holds ES_SYSTEM_REQUIRED from a dedicated thread: the state belongs to the
// thread that set it, so the thread stays locked until released.
func SystemInhibitor() Inhibitor {
	proc := windows.NewLazySystemDLL("kernel32.dll").NewProc("SetThreadExecutionState")
	return func() func() {
		stop := make(chan struct{})
		ready := make(chan struct{})
		go func() {
			runtime.LockOSThread()
			defer runtime.UnlockOSThread()
			_, _, _ = proc.Call(uintptr(esContinuous | esSystemRequired))
			close(ready)
			<-stop
			_, _, _ = proc.Call(uintptr(esContinuous))
		}()
		<-ready
		var once sync.Once
		return func() { once.Do(func() { close(stop) }) }
	}
}
