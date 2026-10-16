// Package filelock provides process-scoped locks on local persistent files.
package filelock

import (
	"context"
	"os"
	"time"
)

// Acquire waits for an exclusive lock. Closing the returned file releases it;
// the operating system also releases the lock when the process exits. The lock
// file must remain in place so all contenders continue to lock the same inode.
func Acquire(ctx context.Context, path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		locked, err := tryLock(f)
		if err != nil {
			f.Close()
			return nil, err
		}
		if locked {
			return f, nil
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			f.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
