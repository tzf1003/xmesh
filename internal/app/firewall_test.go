package app

import (
	"errors"
	"testing"
	"time"
)

func Test_FirewallBlocks(t *testing.T) {
	const exe = `C:\Users\u\AppData\Local\Programs\xmesh\resources\bin\xmesh.exe`
	private := "network|Private\r\n"
	// The rule's path differs in case, and it covers the Domain and Private profiles.
	rules := "rule|C:\\Users\\u\\Desktop\\other.exe|Any\r\n" +
		`rule|c:\users\u\appdata\local\programs\xmesh\resources\bin\XMESH.exe|Domain, Private` + "\r\n"
	if !firewallBlocks(private+rules, exe) {
		t.Fatal("a block rule for this executable on an active profile must match, whatever the case of the path")
	}
	if firewallBlocks("network|Private\nrule|Any|Any", exe) {
		t.Fatal("a rule for any program is not a rule for this executable")
	}
	// A test node in a sibling folder is another executable.
	if firewallBlocks(private+"rule|C:\\Users\\u\\AppData\\Local\\Programs\\xmesh-test\\xmesh.exe|Any", exe) {
		t.Fatal("a block rule for another xmesh copy must not match")
	}
	if firewallBlocks(private+rules, "") {
		t.Fatal("an unknown executable path matches nothing")
	}
}

func Test_FirewallBlocksOnlyActiveProfiles(t *testing.T) {
	const exe = `C:\xmesh\xmesh.exe`
	scoped := "rule|" + exe + "|Public, Domain\n"
	// The node is on a Private network, so a rule for Public and Domain is not enforced here.
	if firewallBlocks("network|Private\n"+scoped, exe) {
		t.Fatal("a rule for profiles the node is not connected to must not warn")
	}
	if !firewallBlocks("network|DomainAuthenticated\n"+scoped, exe) {
		t.Fatal("a domain network is the Domain profile")
	}
	if !firewallBlocks("network|Public\n"+scoped, exe) {
		t.Fatal("a Public network is the Public profile")
	}
	if firewallBlocks("rule|"+exe+"|Private\n", exe) {
		t.Fatal("without a connected network no profile is active")
	}
	if !firewallBlocks("rule|"+exe+"|Any\n", exe) {
		t.Fatal("a rule for every profile applies whatever the network")
	}
}

// settleFirewall waits until the background read has finished, so a test reads a settled answer.
func settleFirewall(t *testing.T, w *firewallWatch) {
	t.Helper()
	for range 500 {
		w.mu.Lock()
		busy := w.probing
		w.mu.Unlock()
		if !busy {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("probe did not finish")
}

func Test_FirewallWatchKeepsLastAnswer(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	calls := 0
	answer, failNext := true, false
	w := &firewallWatch{exe: "xmesh.exe", now: func() time.Time { return clock }, probe: func(string) (bool, error) {
		calls++
		if failNext {
			return false, errors.New("powershell missing")
		}
		return answer, nil
	}}
	if w.Blocked() {
		t.Fatal("no answer may be reported before the first read")
	}
	settleFirewall(t, w)
	if !w.Blocked() || calls != 1 {
		t.Fatalf("after the first read: calls = %d", calls)
	}
	clock = clock.Add(30 * time.Second)
	if !w.Blocked() || calls != 1 {
		t.Fatalf("within the minute the rules are not read again: calls = %d", calls)
	}
	// A read that fails must not clear the block seen before it.
	clock = clock.Add(31 * time.Second)
	failNext = true
	if !w.Blocked() {
		t.Fatal("the last answer is kept while the next read runs")
	}
	settleFirewall(t, w)
	if !w.Blocked() || calls != 2 {
		t.Fatalf("a failed read must keep the last answer: calls = %d", calls)
	}
	// A later read that works replaces it.
	clock = clock.Add(61 * time.Second)
	failNext, answer = false, false
	w.Blocked()
	settleFirewall(t, w)
	if w.Blocked() || calls != 3 {
		t.Fatalf("a read that works replaces the answer: calls = %d", calls)
	}
	var none *firewallWatch
	if none.Blocked() {
		t.Fatal("a nil watch reports nothing")
	}
}

func Test_FirewallWatchFailedFirstReadReportsNothing(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	w := &firewallWatch{exe: "xmesh.exe", now: func() time.Time { return clock }, probe: func(string) (bool, error) {
		return true, errors.New("powershell missing")
	}}
	w.Blocked()
	settleFirewall(t, w)
	if w.Blocked() {
		t.Fatal("a rule that was never read must not raise the warning")
	}
}
