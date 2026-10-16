// Package quota enforces hourly and concurrent call budgets by principal.
package quota

import (
	"context"
	"encoding/json"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"strings"
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
	nextSweep          time.Time
}

func New(hourly, concurrent int, duration time.Duration) *Limiter {
	return &Limiter{Hourly: hourly, Concurrent: concurrent, Duration: duration, buckets: map[string]*bucket{}}
}

// SetLimits applies new budgets to later calls. Calls already admitted keep the
// deadline they were given.
func (l *Limiter) SetLimits(hourly, concurrent int, duration time.Duration) {
	l.mu.Lock()
	l.Hourly, l.Concurrent, l.Duration = hourly, concurrent, duration
	l.mu.Unlock()
}

// CheckExecTimeout refuses an exec.run whose timeout cannot fit in the call
// budget. Without it the budget's deadline would cut the command short and the
// caller would see a timeout shorter than the one they asked for. fallback is
// the timeout used when the call names none; zero means the executor decides.
func (l *Limiter) CheckExecTimeout(tool string, args json.RawMessage, fallback time.Duration) error {
	if tool != "exec.run" && !strings.HasSuffix(tool, "/exec.run") {
		return nil
	}
	l.mu.Lock()
	max := l.Duration
	l.mu.Unlock()
	timeout := fallback
	var a struct {
		Timeout json.RawMessage `json:"timeout"`
	}
	if json.Unmarshal(args, &a) == nil && len(a.Timeout) > 0 {
		var text string
		var ms int64
		if json.Unmarshal(a.Timeout, &text) == nil {
			if d, e := time.ParseDuration(text); e == nil {
				timeout = d
			}
		} else if json.Unmarshal(a.Timeout, &ms) == nil && ms > 0 {
			// Compare in milliseconds so a huge value cannot overflow a Duration.
			timeout = max + time.Millisecond
			if ms <= max.Milliseconds() {
				timeout = time.Duration(ms) * time.Millisecond
			}
		}
	}
	if timeout > max {
		return toolerr.New("invalid_args", "timeout 超过单次调用时长上限", "field", "timeout", "reason", "quota.max_call_duration", "limit", max.Milliseconds(), "hint", "缩短 timeout，或由运维调高 quota.max_call_duration")
	}
	return nil
}
func (l *Limiter) Acquire(ctx context.Context, principal string) (context.Context, func(), error) {
	l.mu.Lock()
	now := time.Now()
	if !now.Before(l.nextSweep) {
		for key, candidate := range l.buckets {
			if candidate.running == 0 && (len(candidate.calls) == 0 || now.Sub(candidate.calls[len(candidate.calls)-1]) >= time.Hour) {
				delete(l.buckets, key)
			}
		}
		l.nextSweep = now.Add(time.Minute)
	}
	b := l.buckets[principal]
	if b == nil {
		b = &bucket{}
		l.buckets[principal] = b
	}
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
	duration := l.Duration
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, duration)
	var once sync.Once
	return ctx, func() { once.Do(func() { cancel(); l.mu.Lock(); b.running--; l.mu.Unlock() }) }, nil
}
