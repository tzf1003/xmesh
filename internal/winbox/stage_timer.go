package winbox

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// stageTimer records how long each phase of an execution step takes, so the
// cost of workspace admission and reclamation can be attributed to a phase
// instead of guessed. It never influences control flow. Marks are safe for
// concurrent use because the workspace monitor shares contexts with the step
// that created it.
type stageTimer struct {
	mu     sync.Mutex
	last   time.Time
	stages []stageTime
}

type stageTime struct {
	Name    string
	Elapsed time.Duration
}

func (t *stageTimer) mark(name string) {
	if t == nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.last.IsZero() {
		t.stages = append(t.stages, stageTime{name, now.Sub(t.last)})
	}
	t.last = now
}

// begin starts the clock without recording a stage. Retried steps call it again
// so the idle time between attempts is not charged to the next phase.
func (t *stageTimer) begin() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.last = time.Now()
	t.mu.Unlock()
}

func (t *stageTimer) snapshot() []stageTime {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]stageTime(nil), t.stages...)
}

func (t *stageTimer) total() time.Duration {
	var sum time.Duration
	for _, stage := range t.snapshot() {
		sum += stage.Elapsed
	}
	return sum
}

func (t *stageTimer) String() string {
	var b strings.Builder
	for i, stage := range t.snapshot() {
		if i != 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%s=%dms", stage.Name, stage.Elapsed.Milliseconds())
	}
	return b.String()
}

type stageTimerKey struct{}

// withStageTimer lets code deep inside an admission or cleanup path attribute
// its sub-phases to the owning step without threading a parameter through
// every signature. Without a timer in the context, marking is a no-op.
func withStageTimer(ctx context.Context, t *stageTimer) context.Context {
	return context.WithValue(ctx, stageTimerKey{}, t)
}

func stageMark(ctx context.Context, name string) {
	if t, _ := ctx.Value(stageTimerKey{}).(*stageTimer); t != nil {
		t.mark(name)
	}
}

// executionTiming is the per-execution cost report: admission (profile,
// workspace grant, tracking, supervisor), launch (per start call) and cleanup.
type executionTiming struct {
	admit, launch, cleanup stageTimer
	objects                int
}

// The accessors are nil-safe so lifecycle code that builds an execution without
// the constructor still runs.
func (t *executionTiming) launchTimer() *stageTimer {
	if t == nil {
		return nil
	}
	return &t.launch
}

func (t *executionTiming) cleanupTimer() *stageTimer {
	if t == nil {
		return nil
	}
	return &t.cleanup
}
