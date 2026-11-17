package delegate

import (
	"sort"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/cwd"
)

// Overview is the delegation view of the control channel (API 2.7): the runners, the live sessions and the tasks of
// every caller. It is read-only and never carries a prompt, output or reply. delegate.list shows a caller only its own
// tasks; this view is not filtered by caller, because the control channel belongs to the node's own user.
type Overview struct {
	// Running is how many tasks hold a concurrency slot, across all callers.
	Running  int
	Runners  []RunnerRow
	Sessions []SessionRow
	// Tasks are the running tasks and the finished ones still retained, newest first.
	Tasks []TaskRow
}

// RunnerRow is one runner of Overview. Path is set when Available is true.
type RunnerRow struct {
	Name      string
	Available bool
	Path      string
}

// SessionRow is one live session of Overview (CTR-DLG-04).
type SessionRow struct {
	ID     string
	Name   string
	Status string
	PID    int
	CWD    string
}

// TaskRow is one task of Overview, in the state a caller sees it.
type TaskRow struct {
	ID        string
	Caller    string
	Runner    string
	State     string
	CWD       string
	SessionID string
	Started   time.Time
	Duration  time.Duration
}

// Overview reads the delegation state under settings. resolver may be nil when the cwd policy does not load: the
// runners are then looked up from the working directory of the process, and no session is listed. Sessions are listed
// only when settings allow them, as in delegate.list.
func (m *Manager) Overview(settings config.Delegate, resolver *cwd.Resolver) Overview {
	dir := ""
	if resolver != nil {
		dir = resolver.Default
	}
	out := Overview{Runners: runnerRows(settings, dir), Sessions: []SessionRow{}, Tasks: []TaskRow{}}
	if settings.SessionsAllowed() && resolver != nil {
		for _, s := range discoverSessions(resolver) {
			out.Sessions = append(out.Sessions, SessionRow{ID: s.ID, Name: s.Name, Status: s.Status, PID: s.PID, CWD: s.CWD})
		}
	}
	m.mu.Lock()
	now := time.Now()
	m.pruneLocked(now)
	out.Running = m.running
	for _, t := range m.tasks {
		end := now
		if t.finished {
			end = t.ended
		}
		out.Tasks = append(out.Tasks, TaskRow{ID: t.id, Caller: t.caller, Runner: t.runner, State: t.stateLocked(), CWD: t.cwd, SessionID: t.sessionID, Started: t.started, Duration: end.Sub(t.started)})
	}
	m.mu.Unlock()
	sort.Slice(out.Tasks, func(i, j int) bool {
		if !out.Tasks[i].Started.Equal(out.Tasks[j].Started) {
			return out.Tasks[i].Started.After(out.Tasks[j].Started)
		}
		return out.Tasks[i].ID < out.Tasks[j].ID
	})
	return out
}
