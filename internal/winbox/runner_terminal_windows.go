package winbox

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/sandbox"
	"golang.org/x/sys/windows"
)

// StartTerminal implements sandbox.Runner. The pseudoconsole belongs to the
// returned terminal; it is closed only after the whole execution has been
// terminated and reclaimed, with output drained throughout.
func (r *Runner) StartTerminal(ctx context.Context, workspace string, spec sandbox.Spec, cols, rows int) (sandbox.Terminal, error) {
	if cols < 1 || cols > 32767 || rows < 1 || rows > 32767 {
		return nil, fmt.Errorf("AppContainer terminal size is out of range")
	}
	m, err := r.open(ctx, workspace)
	if err != nil {
		return nil, err
	}
	t := &managedTerminal{m: m, exited: make(chan struct{})}
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, m.abort(ctx, err)
	}
	defer windows.CloseHandle(inR)
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		_ = windows.CloseHandle(inW)
		return nil, m.abort(ctx, err)
	}
	defer windows.CloseHandle(outW)
	t.input = os.NewFile(uintptr(inW), "appcontainer-conpty-input")
	t.output = os.NewFile(uintptr(outR), "appcontainer-conpty-output")
	if err := windows.CreatePseudoConsole(windows.Coord{X: int16(cols), Y: int16(rows)}, inR, outW, 0, &t.console); err != nil {
		t.closePipes()
		return nil, m.abort(ctx, err)
	}
	info, err := m.exec.start(ctx, spec.Args, spec.Dir, spec.Env, nil, t.console)
	if err != nil {
		// No process was admitted, so the console has nothing to drain.
		t.closeConsole()
		t.closePipes()
		return nil, m.abort(ctx, err)
	}
	_ = windows.CloseHandle(info.Thread)
	t.process, t.pid = info.Process, int(info.ProcessId)
	go t.watch()
	return t, nil
}

type managedTerminal struct {
	m             *managedExecution
	input, output *os.File
	process       windows.Handle
	pid           int
	exited        chan struct{}
	consoleMu     sync.Mutex
	console       windows.Handle
	consoleOnce   sync.Once
	closeOnce     sync.Once
	closeErr      error
	killOnce      sync.Once
}

// watch follows the root process. When it exits, the remaining processes of the
// execution are terminated and the sandbox is reclaimed before the console is
// closed, so descendants cannot keep writing to a console that is going away.
func (t *managedTerminal) watch() {
	_, _ = windows.WaitForSingleObject(t.process, windows.INFINITE)
	t.teardown()
}

func (t *managedTerminal) cleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), runnerCleanupLimit)
}

// teardown ends the execution and then the pseudoconsole. It is idempotent; a
// failed reclaim is retained by the runner and retried before the next
// execution. The pseudoconsole is closed regardless so the session ends.
func (t *managedTerminal) teardown() {
	t.closeOnce.Do(func() {
		ctx, cancel := t.cleanupContext()
		defer cancel()
		t.closeErr = t.m.close(ctx)
		// Release the root handle once its exit is signalled. A reclaim that
		// failed can leave the root alive; the handle is then kept for the
		// process lifetime instead of blocking the session's end.
		if result, _ := windows.WaitForSingleObject(t.process, 5000); result == windows.WAIT_OBJECT_0 {
			_ = windows.CloseHandle(t.process)
		}
		close(t.exited)
		t.closeConsole()
	})
}

func (t *managedTerminal) closeConsole() {
	t.consoleOnce.Do(func() {
		// Closing the console flushes its output, so the terminal's owner keeps
		// reading until end of file, as the direct Windows terminal requires.
		t.consoleMu.Lock()
		defer t.consoleMu.Unlock()
		if t.console != 0 {
			windows.ClosePseudoConsole(t.console)
			t.console = 0
		}
	})
}

func (t *managedTerminal) closePipes() {
	if t.input != nil {
		_ = t.input.Close()
	}
	if t.output != nil {
		_ = t.output.Close()
	}
}

func (t *managedTerminal) Read(b []byte) (int, error)  { return t.output.Read(b) }
func (t *managedTerminal) Write(b []byte) (int, error) { return t.input.Write(b) }
func (t *managedTerminal) PID() int                    { return t.pid }

func (t *managedTerminal) Wait() error {
	<-t.exited
	return nil
}

// Close terminates the execution if it is still running, reclaims it, closes the
// pseudoconsole and releases the pipes. It returns the reclaim error, if any.
func (t *managedTerminal) Close() error {
	t.teardown()
	t.closePipes()
	return t.closeErr
}

func (t *managedTerminal) Resize(cols, rows int) error {
	if cols < 1 || cols > 32767 || rows < 1 || rows > 32767 {
		return windows.ERROR_INVALID_PARAMETER
	}
	t.consoleMu.Lock()
	defer t.consoleMu.Unlock()
	if t.console == 0 {
		return windows.ERROR_INVALID_HANDLE
	}
	return windows.ResizePseudoConsole(t.console, windows.Coord{X: int16(cols), Y: int16(rows)})
}

// kill terminates the execution without waiting for reclamation: the call that
// requests it must not block on cleanup. The terminal's watcher completes the
// teardown once the root process has exited.
func (t *managedTerminal) kill() {
	t.killOnce.Do(func() {
		go func() {
			ctx, cancel := t.cleanupContext()
			defer cancel()
			_ = t.m.close(ctx)
		}()
	})
}

func (t *managedTerminal) Signal(name string) error {
	switch name {
	case "KILL":
		t.kill()
		return nil
	case "INT":
		// Console input delivers Ctrl-C to the foreground application of the
		// pseudoconsole, which the host's own console cannot reach.
		_, err := t.input.Write([]byte{3})
		return err
	default:
		go func() { _, _ = t.input.Write([]byte{3}) }()
		go func() {
			select {
			case <-t.exited:
			case <-time.After(250 * time.Millisecond):
				t.kill()
			}
		}()
		return nil
	}
}

var _ sandbox.Terminal = (*managedTerminal)(nil)
