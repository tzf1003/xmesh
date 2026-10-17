package app

import (
	"errors"
	"testing"
	"time"
)

func Test_BlockedBy(t *testing.T) {
	const exe = `C:\Users\u\AppData\Local\Programs\xmesh\resources\bin\xmesh.exe`
	programs := "Any\r\nC:\\Users\\u\\Desktop\\other.exe\r\n" + `c:\users\u\appdata\local\programs\xmesh\resources\bin\XMESH.exe` + "\r\n"
	if !blockedBy(programs, exe) {
		t.Fatal("a block rule for this executable must match, whatever the case of the path")
	}
	if blockedBy("Any\n", exe) {
		t.Fatal("a rule for any program is not a rule for this executable")
	}
	// A test node in a sibling folder is another executable.
	if blockedBy(programs, `C:\Users\u\AppData\Local\Programs\xmesh-test\xmesh.exe`) {
		t.Fatal("a block rule for another xmesh copy must not match")
	}
	if blockedBy(programs, "") {
		t.Fatal("an unknown executable path matches nothing")
	}
}

func Test_FirewallWatchKeepsLastAnswerAndIgnoresFailure(t *testing.T) {
	clock := time.Unix(1_700_000_000, 0)
	calls := 0
	failNext := false
	w := &firewallWatch{exe: "xmesh.exe", now: func() time.Time { return clock }, probe: func(string) (bool, error) {
		calls++
		if failNext {
			return true, errors.New("powershell missing")
		}
		return true, nil
	}}
	// Wait until the background probe has finished, so the test reads a settled answer.
	settle := func() {
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
	if w.Blocked() {
		t.Fatal("no answer may be reported before the first probe")
	}
	settle()
	if !w.Blocked() || calls != 1 {
		t.Fatalf("after the first probe: calls = %d", calls)
	}
	clock = clock.Add(30 * time.Second)
	if !w.Blocked() || calls != 1 {
		t.Fatalf("within the minute the rules are not read again: calls = %d", calls)
	}
	clock = clock.Add(31 * time.Second)
	failNext = true
	if !w.Blocked() {
		t.Fatal("the last answer is kept while the next probe runs")
	}
	settle()
	if w.Blocked() || calls != 2 {
		t.Fatalf("a failed probe must not raise the warning: calls = %d", calls)
	}
	var none *firewallWatch
	if none.Blocked() {
		t.Fatal("a nil watch reports nothing")
	}
}
