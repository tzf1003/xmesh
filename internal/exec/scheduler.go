package exec

import (
	"context"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"sync"
	"time"
)

type waiter struct {
	caller, mode string
	deadline     time.Time
	ready        chan struct{}
	granted      bool
}
type scheduler struct {
	mu                                         sync.Mutex
	maxShared, maxExclusive, shared, exclusive int
	queue                                      []*waiter
	running                                    map[*waiter]bool
	last                                       string
}

func newScheduler(shared, exclusive int) *scheduler {
	return &scheduler{maxShared: shared, maxExclusive: exclusive, running: map[*waiter]bool{}}
}
func (s *scheduler) configure(shared, exclusive int) {
	s.mu.Lock()
	s.maxShared = shared
	s.maxExclusive = exclusive
	s.dispatch()
	s.mu.Unlock()
}
func (s *scheduler) dispatch() {
	for len(s.queue) > 0 {
		hasExclusive := false
		for _, w := range s.queue {
			if w.mode == "exclusive" {
				hasExclusive = true
				break
			}
		}
		if hasExclusive && s.shared > 0 {
			return
		}
		if !hasExclusive && s.exclusive > 0 {
			return
		}
		candidates := []int{}
		for i, w := range s.queue {
			if hasExclusive && w.mode != "exclusive" {
				continue
			}
			candidates = append(candidates, i)
		}
		if len(candidates) == 0 {
			return
		}
		i := candidates[0]
		for _, j := range candidates {
			if s.queue[j].caller != s.last {
				i = j
				break
			}
		}
		w := s.queue[i]
		if w.mode == "exclusive" {
			if s.exclusive >= s.maxExclusive {
				return
			}
			s.exclusive++
		} else {
			if s.shared >= s.maxShared {
				return
			}
			s.shared++
		}
		s.queue = append(s.queue[:i], s.queue[i+1:]...)
		s.running[w] = true
		s.last = w.caller
		w.granted = true
		close(w.ready)
	}
}
func (s *scheduler) saturated(mode string, position int) *toolerr.Error {
	retry := int64(1000)
	now := time.Now()
	known := false
	for w := range s.running {
		if !w.deadline.IsZero() {
			d := max(int64(1), w.deadline.Sub(now).Milliseconds())
			if !known || d < retry {
				retry = d
				known = true
			}
		}
	}
	return toolerr.New("exec_saturated", "执行槽在超时内不可用", "queue_position", position, "mode", mode, "retry_after_ms", retry)
}
func (s *scheduler) acquire(ctx context.Context, caller, mode string) (func(), time.Duration, error) {
	now := time.Now()
	deadline, _ := ctx.Deadline()
	w := &waiter{caller: caller, mode: mode, deadline: deadline, ready: make(chan struct{})}
	s.mu.Lock()
	if len(s.queue) >= s.maxShared {
		e := s.saturated(mode, len(s.queue)+1)
		s.mu.Unlock()
		return nil, 0, e
	}
	s.queue = append(s.queue, w)
	s.dispatch()
	s.mu.Unlock()
	select {
	case <-w.ready:
	case <-ctx.Done():
		s.mu.Lock()
		if !w.granted {
			position := 1
			for i, v := range s.queue {
				if v == w {
					position = i + 1
					s.queue = append(s.queue[:i], s.queue[i+1:]...)
					break
				}
			}
			e := s.saturated(mode, position)
			s.dispatch()
			s.mu.Unlock()
			return nil, 0, e
		}
		s.releaseLocked(w)
		s.mu.Unlock()
		return nil, 0, toolerr.New("exec_saturated", "等待执行槽超时", "queue_position", 1, "mode", mode, "retry_after_ms", 1000)
	}
	return func() { s.mu.Lock(); s.releaseLocked(w); s.mu.Unlock() }, time.Since(now), nil
}
func (s *scheduler) releaseLocked(w *waiter) {
	if !s.running[w] {
		return
	}
	delete(s.running, w)
	if w.mode == "exclusive" {
		s.exclusive--
	} else {
		s.shared--
	}
	s.dispatch()
}
