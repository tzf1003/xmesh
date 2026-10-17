package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// firewallEvery is how often the firewall rules are read again. A cancelled firewall prompt adds its
// block rule once, so a minute of staleness does no harm, and PowerShell stays off the status path.
const firewallEvery = time.Minute

// firewallWatch reports whether an inbound block rule of Windows Firewall applies to this executable.
// Reading the rules starts PowerShell, so the probe runs in the background and Blocked returns the
// answer of the last one.
type firewallWatch struct {
	exe   string
	probe func(exe string) (bool, error)
	now   func() time.Time

	mu        sync.Mutex
	probing   bool
	checkedAt time.Time
	blocked   bool
}

func newFirewallWatch() *firewallWatch {
	exe, _ := os.Executable()
	return &firewallWatch{exe: exe, probe: probeInboundBlock, now: time.Now}
}

// Blocked reports the answer of the last probe. Before the first probe finishes it reports false, and
// so it does when the last probe failed: the warning needs a rule that was actually read.
func (w *firewallWatch) Blocked() bool {
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.probing && (w.checkedAt.IsZero() || w.now().Sub(w.checkedAt) >= firewallEvery) {
		w.probing = true
		go w.run()
	}
	return w.blocked
}

func (w *firewallWatch) run() {
	blocked, err := w.probe(w.exe)
	w.mu.Lock()
	defer w.mu.Unlock()
	w.probing = false
	w.checkedAt = w.now()
	w.blocked = err == nil && blocked
}

// blockedBy reports whether one of the programs a firewall rule names, one per line, is exe. Paths
// compare without regard to case, as Windows paths do.
func blockedBy(programs, exe string) bool {
	if exe == "" {
		return false
	}
	for _, program := range strings.Split(programs, "\n") {
		program = strings.TrimSpace(program)
		if program != "" && strings.EqualFold(filepath.Clean(program), filepath.Clean(exe)) {
			return true
		}
	}
	return false
}
