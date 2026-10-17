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

// Blocked reports the answer of the last read that worked. Before the first one finishes it reports
// false; a read that fails leaves the previous answer in place, so a read that fails for a moment does
// not hide a block that was seen.
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
	if err == nil {
		w.blocked = blocked
	}
}

// firewallBlocks reads the output of the probe script. Each "rule|<program>|<profiles>" line is an
// enabled inbound block rule that names a program, and each "network|<category>" line is a network the
// node is connected to. The rule counts when it names exe and applies to a profile of one of those
// networks, or to all profiles. Paths compare without regard to case, as Windows paths do.
func firewallBlocks(output, exe string) bool {
	if exe == "" {
		return false
	}
	active := map[string]bool{}
	var rules [][2]string
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "|")
		switch {
		case len(fields) == 2 && fields[0] == "network":
			active[networkProfile(fields[1])] = true
		case len(fields) == 3 && fields[0] == "rule":
			rules = append(rules, [2]string{fields[1], fields[2]})
		}
	}
	for _, rule := range rules {
		if !strings.EqualFold(filepath.Clean(rule[0]), filepath.Clean(exe)) {
			continue
		}
		for _, profile := range strings.Split(rule[1], ",") {
			if profile = strings.TrimSpace(profile); profile == "Any" || active[profile] {
				return true
			}
		}
	}
	return false
}

// networkProfile names the firewall profile of a network category. A network joined to a domain is
// the Domain profile.
func networkProfile(category string) string {
	if category == "DomainAuthenticated" {
		return "Domain"
	}
	return category
}
