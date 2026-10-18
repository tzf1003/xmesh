package ci

import (
	"bufio"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// process is one running runner script and everything it spawned.
type process struct {
	cmd  *exec.Cmd
	done chan struct{}
	exit int
}

const maxLineBytes = 64 << 10

// startProcess runs script (run.sh or run.cmd, or config.sh with arguments) in dir. Standard
// output and error are read line by line into onLine, which must not block; a line longer than
// 64 KiB is cut. The process leads its own group so that stopping it reaches its children.
func startProcess(dir, script string, args, env []string, onLine func(string)) (*process, error) {
	cmd := exec.Command(script, args...)
	cmd.Dir = dir
	cmd.Env = env
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout, cmd.Stderr = writer, writer
	prepare(cmd)
	if err = cmd.Start(); err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	writer.Close()
	p := &process{cmd: cmd, done: make(chan struct{})}
	adopt(p)
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		defer reader.Close()
		readLines(reader, onLine)
	}()
	go func() {
		err := cmd.Wait()
		// Output the runner flushed before exiting is still in the pipe; read it first so the exit
		// is classified with the last lines in hand.
		select {
		case <-drained:
		case <-time.After(2 * time.Second):
		}
		p.exit = exitCodeOf(cmd, err)
		close(p.done)
	}()
	return p, nil
}

func readLines(r io.Reader, onLine func(string)) {
	br := bufio.NewReaderSize(r, 4096)
	var line []byte
	for {
		chunk, prefix, err := br.ReadLine()
		if len(line) < maxLineBytes {
			line = append(line, chunk...)
		}
		if err != nil {
			if len(line) > 0 {
				onLine(strings.TrimRight(string(line), "\r"))
			}
			return
		}
		if !prefix {
			onLine(strings.TrimRight(string(line), "\r"))
			line = line[:0]
		}
	}
}

func (p *process) pid() int { return p.cmd.Process.Pid }

// exitCodeOf is the process's exit status, 128+signal for a process that was signalled.
func exitCodeOf(cmd *exec.Cmd, err error) int {
	if err == nil {
		return 0
	}
	if cmd.ProcessState != nil {
		return processExit(cmd.ProcessState)
	}
	return 1
}
