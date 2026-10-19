//go:build windows

package exec

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf16"
	"unsafe"

	"github.com/tzf1003/xmesh/internal/toolerr"
	"github.com/tzf1003/xmesh/internal/winjob"
	"golang.org/x/sys/windows"
)

// denyJobList makes the job-list attribute fail like a Store application
// execution alias does, so tests can exercise the late-assignment fallback.
var denyJobList bool

// startCommand uses file-backed output owned by Manager.execute. Only the three
// explicit standard handles are inherited; the lifetime Job is assigned by
// CreateProcess before the suspended command can become visible to its host.
// Targets that refuse the job-list attribute (Store application execution
// aliases) are created suspended without it and assigned to the Job before
// their primary thread is resumed.
func startCommand(c *exec.Cmd) (wait func() error, finish func(), err error) {
	return startCommandWithWindow(c, false)
}

func startCommandWithWindow(c *exec.Cmd, hideWindow bool) (wait func() error, finish func(), err error) {
	if c.Err != nil {
		return nil, nil, c.Err
	}
	if c.Process != nil || c.SysProcAttr != nil {
		return nil, nil, fmt.Errorf("command already started or has unsupported process attributes")
	}
	stdout, ok := c.Stdout.(*os.File)
	if !ok {
		return nil, nil, fmt.Errorf("command stdout must be a file")
	}
	stderr, ok := c.Stderr.(*os.File)
	if !ok {
		return nil, nil, fmt.Errorf("command stderr must be a file")
	}
	path, err := commandPath(c.Path, c.Dir)
	if err != nil {
		return nil, nil, err
	}
	// Resolve an extension for explicit relative paths as os/exec does.
	path, err = exec.LookPath(path)
	if err != nil {
		return nil, nil, err
	}
	// A Store application execution alias activates its package, which places
	// every descendant in the package's own Job: they would outlive ours.
	if winjob.IsAppExecLink(path) {
		return nil, nil, toolerr.AppExecAlias("cmd", path)
	}
	application, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(c.Args))
	if err != nil {
		return nil, nil, err
	}
	var directory *uint16
	if c.Dir != "" {
		directory, err = windows.UTF16PtrFromString(c.Dir)
		if err != nil {
			return nil, nil, err
		}
	}
	environment := c.Env
	if environment == nil {
		environment = os.Environ()
	}
	for _, value := range environment {
		if strings.ContainsRune(value, 0) {
			return nil, nil, fmt.Errorf("command environment contains NUL")
		}
	}
	block := utf16.Encode([]rune(strings.Join(environment, "\x00") + "\x00\x00"))

	var stdin *os.File
	var input *os.File
	switch source := c.Stdin.(type) {
	case *os.File:
		stdin = source
	case nil:
		stdin, err = os.Open(os.DevNull)
		if err != nil {
			return nil, nil, err
		}
		defer stdin.Close()
	case *strings.Reader:
		stdin, input, err = os.Pipe()
		if err != nil {
			return nil, nil, err
		}
		defer stdin.Close()
		defer func() {
			if err != nil {
				input.Close()
			}
		}()
	default:
		return nil, nil, fmt.Errorf("command stdin must be a file or a finite string reader")
	}
	handles := [3]windows.Handle{}
	for i, file := range []*os.File{stdin, stdout, stderr} {
		if file == nil {
			return nil, nil, fmt.Errorf("command standard handle unavailable")
		}
		if err = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(file.Fd()), windows.CurrentProcess(), &handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return nil, nil, err
		}
		defer windows.CloseHandle(handles[i])
	}
	job, err := winjob.New()
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	closeJob := func() { once.Do(func() { _ = windows.CloseHandle(job) }) }
	defer func() {
		if err != nil {
			closeJob()
		}
	}()
	// create starts the command suspended. With the job-list attribute the Job
	// is bound inside CreateProcess; without it the caller must assign the Job
	// before the primary thread is resumed.
	create := func(bindJob bool) (process windows.ProcessInformation, err error) {
		if bindJob && denyJobList {
			return process, windows.ERROR_ACCESS_DENIED
		}
		count := uint32(1)
		if bindJob {
			count = 2
		}
		attributes, err := windows.NewProcThreadAttributeList(count)
		if err != nil {
			return process, err
		}
		defer attributes.Delete()
		if bindJob {
			if err = winjob.Apply(attributes, job); err != nil {
				return process, err
			}
		}
		if err = attributes.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
			return process, err
		}
		startup := windows.StartupInfoEx{StartupInfo: windows.StartupInfo{
			Cb: uint32(unsafe.Sizeof(windows.StartupInfoEx{})), Flags: windows.STARTF_USESTDHANDLES,
			StdInput: handles[0], StdOutput: handles[1], StdErr: handles[2],
		}, ProcThreadAttributeList: attributes.List()}
		flags := uint32(windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
		if hideWindow {
			flags |= windows.CREATE_NO_WINDOW
		}
		err = windows.CreateProcess(application, command, nil, nil, true, flags, &block[0], directory, &startup.StartupInfo, &process)
		return process, err
	}
	process, err := create(true)
	boundLate := false
	if winjob.JobListDenied(err) {
		// A Job named in a failed job-list creation cannot take the Store
		// package's process afterwards (every later assignment is refused), so
		// the late binding uses a fresh, never-referenced Job.
		_ = windows.CloseHandle(job)
		job = 0
		if job, err = winjob.New(); err != nil {
			return nil, nil, err
		}
		process, err = create(false)
		boundLate = true
	}
	if err != nil {
		return nil, nil, err
	}
	defer windows.CloseHandle(process.Process)
	defer windows.CloseHandle(process.Thread)
	defer func() {
		if err != nil {
			closeJob()
			_, _ = windows.WaitForSingleObject(process.Process, windows.INFINITE)
		}
	}()
	c.Process, err = os.FindProcess(int(process.ProcessId))
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if err != nil {
			_ = c.Process.Release()
		}
	}()
	if boundLate {
		if err = winjob.AssignAfterCreate(job, process.Process); err != nil {
			// Still suspended and outside the Job: nothing else would end it.
			_ = windows.TerminateProcess(process.Process, 1)
			return nil, nil, err
		}
	}
	var previous uint32
	previous, err = windows.ResumeThread(process.Thread)
	if err != nil {
		return nil, nil, err
	}
	if previous != 1 {
		return nil, nil, fmt.Errorf("unexpected primary thread suspend count: %d", previous)
	}

	copied := make(chan struct{})
	if input == nil {
		close(copied)
	} else {
		go func() {
			defer close(copied)
			defer input.Close()
			_, _ = io.Copy(input, c.Stdin)
		}()
	}
	wait = func() error {
		state, waitErr := c.Process.Wait()
		// Root exit closes the Job before waiting for stdin copying, so a
		// descendant holding its inherited input open cannot delay cleanup.
		closeJob()
		if input != nil {
			_ = input.Close()
		}
		<-copied
		c.ProcessState = state
		if waitErr != nil {
			return waitErr
		}
		if !state.Success() {
			return &exec.ExitError{ProcessState: state}
		}
		return nil
	}
	return wait, closeJob, nil
}

// Explicit paths are relative to the command directory. Rooted and drive-relative
// paths follow Windows' drive semantics, including its per-drive working directory.
func commandPath(path, directory string) (string, error) {
	if path == "" {
		return "", os.ErrInvalid
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	directory, err := filepath.Abs(directory)
	if err != nil {
		return "", err
	}
	volume := filepath.VolumeName(path)
	if volume != "" {
		if len(volume) == len(path) {
			return "", os.ErrInvalid
		}
		if strings.EqualFold(volume, filepath.VolumeName(directory)) {
			return filepath.Join(directory, path[len(volume):]), nil
		}
		return windows.FullPath(path)
	}
	if os.IsPathSeparator(path[0]) {
		return filepath.VolumeName(directory) + path, nil
	}
	return filepath.Join(directory, path), nil
}

func terminate(c *exec.Cmd) {
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(c.Process.Pid))
}
func kill(c *exec.Cmd) { _ = c.Process.Kill() }
func exitCode(c *exec.Cmd, e error) int {
	if e == nil {
		return 0
	}
	if c.ProcessState != nil {
		return c.ProcessState.ExitCode()
	}
	return 1
}
