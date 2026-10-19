//go:build linux || darwin

package exec

import (
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

type treeIdentity struct {
	pid, parent, group int
	birth              string
}

func startTreeCommand(cmd *exec.Cmd) (func() error, func(), error) { return startCommand(cmd) }

// watchTree samples process ancestry; it is not a containment mechanism.
// Cooperative Unix backends must retain their process group or register detached
// helpers with their own supervisor before allowing their parent to exit.
func watchTree(root int) (func() error, error) {
	initial, err := treeSnapshot()
	if err != nil {
		return nil, err
	}
	owned := map[int]string{}
	for _, p := range initial {
		if p.pid == root {
			owned[root] = p.birth
		}
	}
	var mu sync.Mutex
	collect := func() error {
		all, err := treeSnapshot()
		if err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		alive := map[int]string{}
		for _, p := range all {
			alive[p.pid] = p.birth
		}
		// Validate each parent identity before following its current descendants.
		for pid, birth := range owned {
			if alive[pid] != birth {
				delete(owned, pid)
			}
		}
		for changed := true; changed; {
			changed = false
			for _, p := range all {
				if _, ok := owned[p.pid]; ok {
					continue
				}
				if _, parent := owned[p.parent]; parent || p.group == root {
					owned[p.pid] = p.birth
					changed = true
				}
			}
		}
		return nil
	}
	stop, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = collect()
			}
		}
	}()
	return func() error {
		close(stop)
		<-stopped
		if err := collect(); err != nil {
			return err
		}
		for attempt := 0; attempt < 100; attempt++ {
			all, err := treeSnapshot()
			if err != nil {
				return err
			}
			left := 0
			for _, p := range all {
				if birth, ok := owned[p.pid]; ok && birth == p.birth {
					left++
					_ = syscall.Kill(p.pid, syscall.SIGKILL)
				}
			}
			if left == 0 {
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
		return fmt.Errorf("backend process tree cleanup pending")
	}, nil
}
