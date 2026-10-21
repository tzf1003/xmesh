package computer

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/toolerr"
)

type nativeResponse struct {
	ID     uint64          `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    string `json:"error"`
		Message string `json:"message"`
		Reason  string `json:"reason"`
	} `json:"error,omitempty"`
}

// Process uses inherited anonymous pipes. Neither the native executor nor its
// stdin is reachable through a network listener or an exec tool argument.
type Process struct {
	cmd       *exec.Cmd
	input     io.WriteCloser
	mu        sync.Mutex
	write     chan struct{}
	serial    uint64
	pending   map[uint64]chan nativeResponse
	done      chan struct{}
	exited    chan struct{}
	closeOnce sync.Once
}

func StartProcess(path string) (*Process, error) {
	return startProcess(path, false)
}

// StartAuthorizedProcess is only used by the local host command's explicit setup action.
func StartAuthorizedProcess(path string) (*Process, error) {
	return startProcess(path, true)
}

func startProcess(path string, authorize bool) (*Process, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("native executor path must be absolute")
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("native executor is not a regular file")
	}
	args := []string{"--stdio"}
	if authorize {
		args = append(args, "--authorize")
	}
	cmd := exec.Command(path, args...)
	prepareNativeProcess(cmd)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		_ = in.Close()
		return nil, err
	}
	// stderr is intentionally not copied into the audit stream. Native diagnostics
	// must be metadata only; the supervisor exposes a structured availability state.
	if err = cmd.Start(); err != nil {
		_ = in.Close()
		_ = out.Close()
		return nil, err
	}
	p := &Process{cmd: cmd, input: in, write: make(chan struct{}, 1), pending: map[uint64]chan nativeResponse{}, done: make(chan struct{}), exited: make(chan struct{})}
	go p.read(out)
	go func() { _ = cmd.Wait(); close(p.exited) }()
	return p, nil
}

func (p *Process) read(r io.Reader) {
	defer close(p.done)
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 4096), 8<<20)
	for s.Scan() {
		var response nativeResponse
		if json.Unmarshal(s.Bytes(), &response) != nil {
			break
		}
		if response.Error != nil && response.Error.Reason == "capture_timeout" {
			// EOF lets native guards cancel actions and release held input before
			// the bounded Close fallback. Handle late replies after caller cancellation
			// too: their blocked platform thread must not poison future captures.
			_ = p.input.Close()
			go func() { _ = p.Close() }()
		}
		p.mu.Lock()
		ch := p.pending[response.ID]
		delete(p.pending, response.ID)
		p.mu.Unlock()
		if ch != nil {
			ch <- response
		}
	}
	_ = p.input.Close()
	_ = p.cmd.Process.Kill()
}

func (p *Process) call(ctx context.Context, method string, in, out any) error {
	p.mu.Lock()
	p.serial++
	id := p.serial
	ch := make(chan nativeResponse, 1)
	p.pending[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pending, id); p.mu.Unlock() }()
	raw, err := json.Marshal(map[string]any{"id": id, "method": method, "params": in})
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return toolerr.Args("request_size")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return toolerr.New("desktop_unavailable", "桌面执行器已退出", "reason", "executor_exited")
	case p.write <- struct{}{}:
	}
	// A native process can stop reading stdin while still keeping stdout open.
	// Bound both the wait for the writer and the pipe write itself. Killing that
	// process is necessary once a partial request might be on its byte stream.
	if err := ctx.Err(); err != nil {
		<-p.write
		return err
	}
	written := make(chan error, 1)
	go func() {
		_, err := p.input.Write(append(raw, '\n'))
		<-p.write
		written <- err
	}()
	select {
	case <-ctx.Done():
		_ = p.cmd.Process.Kill()
		_ = p.input.Close()
		return ctx.Err()
	case <-p.done:
		return toolerr.New("desktop_unavailable", "桌面执行器已退出", "reason", "executor_exited")
	case err := <-written:
		if err != nil {
			_ = p.cmd.Process.Kill()
			return err
		}
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.done:
		return toolerr.New("desktop_unavailable", "桌面执行器已退出", "reason", "executor_exited")
	case r := <-ch:
		if r.Error != nil {
			return toolerr.New(r.Error.Code, r.Error.Message, "reason", r.Error.Reason)
		}
		if out != nil {
			return json.Unmarshal(r.Result, out)
		}
		return nil
	}
}

func (p *Process) Info(ctx context.Context) (Info, error) {
	var out Info
	e := p.call(ctx, "info", nil, &out)
	return out, e
}
func (p *Process) Capture(ctx context.Context, r CaptureRequest) (Frame, error) {
	var out struct {
		Frame
		Image []byte `json:"image"`
	}
	e := p.call(ctx, "capture", r, &out)
	out.Frame.Image = out.Image
	return out.Frame, e
}
func (p *Process) Execute(ctx context.Context, r Execution) (ActionResult, error) {
	var out ActionResult
	e := p.call(ctx, "execute", r, &out)
	if e != nil {
		release, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = p.Release(release, r)
		cancel()
	}
	return out, e
}
func (p *Process) Release(ctx context.Context, r Execution) error {
	return p.call(ctx, "release", r, nil)
}

// Exited lets the local host replace a failed native child without transferring
// the desktop binding to another kernel.
func (p *Process) Exited() bool {
	select {
	case <-p.done:
		return true
	case <-p.exited:
		return true
	default:
		return false
	}
}

func (p *Process) PID() int { return p.cmd.Process.Pid }

func (p *Process) Close() error {
	p.closeOnce.Do(func() {
		_ = p.input.Close()
		select {
		case <-p.exited:
		case <-time.After(2 * time.Second):
			_ = p.cmd.Process.Kill()
			<-p.exited
		}
		<-p.done
	})
	return nil
}
