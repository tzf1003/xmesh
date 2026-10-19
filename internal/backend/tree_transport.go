package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	managed "github.com/tzf1003/xmesh/internal/exec"
)

type treeTransport struct {
	command  *exec.Cmd
	shutdown time.Duration
}

func (t *treeTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	input, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	reader, output, err := os.Pipe()
	if err != nil {
		input.Close()
		writer.Close()
		return nil, err
	}
	defer input.Close()
	defer output.Close()
	t.command.Stdin = input
	t.command.Stdout = output
	t.command.Stderr = os.Stderr
	tree, err := managed.StartProcessTree(t.command)
	if err != nil {
		reader.Close()
		writer.Close()
		return nil, err
	}
	done := make(chan struct{})
	go func() { _ = tree.Wait(); _ = tree.Close(); close(done) }()
	connection, err := (&mcp.IOTransport{Reader: io.NopCloser(reader), Writer: writer}).Connect(ctx)
	if err != nil {
		_ = tree.Close()
		reader.Close()
		writer.Close()
		return nil, err
	}
	return &treeConnection{Connection: connection, tree: tree, reader: reader, done: done, shutdown: t.shutdown}, nil
}

type treeConnection struct {
	mcp.Connection
	tree     *managed.ProcessTree
	reader   *os.File
	done     <-chan struct{}
	shutdown time.Duration
	once     sync.Once
	err      error
}

func (c *treeConnection) Close() error {
	c.once.Do(func() {
		_ = c.Connection.Close() // Close stdin first, allowing the backend to flush its journal.
		timer := time.NewTimer(c.shutdown)
		defer timer.Stop()
		select {
		case <-c.done:
		case <-timer.C:
		}
		c.err = c.tree.Close()
		select {
		case <-c.done:
		case <-time.After(2 * time.Second):
			c.err = errors.Join(c.err, fmt.Errorf("backend root did not exit"))
		}
		_ = c.reader.Close()
	})
	return c.err
}
