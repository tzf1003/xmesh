// Package quota enforces hourly and concurrent call budgets by principal.
package quota

import (
	"context"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"sync"
	"time"
)

type bucket struct {
	calls   []time.Time
	running int
}
type Limiter struct {
	mu                 sync.Mutex
	Hourly, Concurrent int
	Duration           time.Duration
	buckets            map[string]*bucket
}

func New(hourly, concurrent int, duration time.Duration) *Limiter {
	return &Limiter{Hourly: hourly, Concurrent: concurrent, Duration: duration, buckets: map[string]*bucket{}}
}
func (l *Limiter) Acquire(ctx context.Context, principal string) (context.Context, func(), error) {
	l.mu.Lock()
	b := l.buckets[principal]
	if b == nil {
		b = &bucket{}
		l.buckets[principal] = b
	}
	now := time.Now()
	kept := b.calls[:0]
	for _, t := range b.calls {
		if now.Sub(t) < time.Hour {
			kept = append(kept, t)
		}
	}
	b.calls = kept
	if len(kept) >= l.Hourly || b.running >= l.Concurrent {
		l.mu.Unlock()
		return nil, nil, toolerr.New("policy_denied", "调用配额已满", "rule", "quota.exceeded", "reason", "请等待正在进行的调用结束或配额窗口恢复")
	}
	b.calls = append(b.calls, now)
	b.running++
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, l.Duration)
	var once sync.Once
	return ctx, func() { once.Do(func() { cancel(); l.mu.Lock(); b.running--; l.mu.Unlock() }) }, nil
}
