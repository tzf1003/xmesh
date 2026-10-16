package pty

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

func errorCode(err error) string {
	if v, ok := err.(*toolerr.Error); ok {
		return v.Code
	}
	if err == nil {
		return ""
	}
	return "non-tool error: " + err.Error()
}

// pty.write, pty.resize, pty.signal and pty.close need the owner. Any other
// caller gets not_owner and the terminal sees no input, no resize and no
// signal, and the session stays active. The same is true for a session with no
// owner (orphaned) and for a closed session.
// conformance: CON-CONC-04
func Test_CONCONC04_NonOwnerCannotMutate(t *testing.T) {
	m, reg := lockFixture(t)
	owner, other := identity.Caller{ID: "node/owner"}, identity.Caller{ID: "node/other"}
	_ = reg.Add(owner)
	_ = reg.Add(other)
	term := newGateTerminal(false)
	addSession(m, "p-ownedaaaa", owner.ID, term)
	orphanTerm := newGateTerminal(false)
	addSession(m, "p-orphanbbb", owner.ID, orphanTerm)
	m.Orphan(owner.ID) // both sessions lose their owner
	// Take the first session back so one session is owned and one is not.
	if _, err := call(m, owner, "pty.transfer", Object{"session_id": "p-ownedaaaa", "to_caller": owner.ID}); err != nil {
		t.Fatal("reclaiming an orphan", err)
	}

	attempts := []struct {
		name string
		args Object
	}{
		{"pty.write", Object{"session_id": "p-ownedaaaa", "data": "forged"}},
		{"pty.resize", Object{"session_id": "p-ownedaaaa", "cols": 100, "rows": 40}},
		{"pty.signal", Object{"session_id": "p-ownedaaaa", "signal": "KILL"}},
		{"pty.close", Object{"session_id": "p-ownedaaaa"}},
		{"pty.transfer", Object{"session_id": "p-ownedaaaa", "to_caller": other.ID}},
		// An orphan has no owner at all, so nobody may write to it.
		{"pty.write", Object{"session_id": "p-orphanbbb", "data": "forged"}},
		{"pty.signal", Object{"session_id": "p-orphanbbb", "signal": "KILL"}},
		{"pty.close", Object{"session_id": "p-orphanbbb"}},
	}
	for _, a := range attempts {
		_, err := call(m, other, a.name, a.args)
		if code := errorCode(err); code != "not_owner" {
			t.Errorf("%s %v by a non-owner: %q, want not_owner", a.name, a.args, code)
		}
	}
	for name, term := range map[string]*gateTerminal{"owned": term, "orphan": orphanTerm} {
		writes, resizes, signals := term.effects()
		if len(writes) != 0 || len(resizes) != 0 || len(signals) != 0 {
			t.Errorf("%s terminal saw effects of refused calls: writes=%v resizes=%v signals=%v", name, writes, resizes, signals)
		}
	}
	rows := m.List()
	states := map[string]string{}
	owners := map[string]string{}
	for _, row := range rows {
		states[row["id"].(string)] = row["state"].(string)
		owners[row["id"].(string)] = row["owner"].(string)
	}
	if states["p-ownedaaaa"] != "active" || owners["p-ownedaaaa"] != owner.ID || states["p-orphanbbb"] != "orphaned" || owners["p-orphanbbb"] != "" {
		t.Errorf("refused calls changed ownership or state: %v %v", states, owners)
	}
	for _, row := range rows {
		if row["id"] == "p-ownedaaaa" && (row["cols"] != 0 || row["rows"] != 0) {
			t.Errorf("refused resize changed the recorded size: %v", row)
		}
	}
	// The owner is unaffected by the refusals.
	if _, err := call(m, owner, "pty.write", Object{"session_id": "p-ownedaaaa", "data": "ok"}); err != nil {
		t.Fatal(err)
	}
	if writes, _, _ := term.effects(); !reflect.DeepEqual(writes, []string{"ok"}) {
		t.Errorf("owner writes = %v", writes)
	}
	if _, err := call(m, owner, "pty.resize", Object{"session_id": "p-ownedaaaa", "cols": 100, "rows": 40}); err != nil {
		t.Fatal(err)
	}
	// A closed session belongs to nobody for writes either.
	term.release()
	waitState(t, m, "p-ownedaaaa", "closed")
	if _, err := call(m, owner, "pty.write", Object{"session_id": "p-ownedaaaa", "data": "late"}); errorCode(err) != "not_owner" {
		t.Errorf("write to a closed session: %q, want not_owner", errorCode(err))
	}
	if _, err := call(m, owner, "pty.write", Object{"session_id": "p-unknown00", "data": "x"}); errorCode(err) != "session_not_found" {
		t.Errorf("write to an unknown session: %q, want session_not_found", errorCode(err))
	}
}

func waitState(t *testing.T, m *Manager, id, want string) {
	t.Helper()
	for deadline := time.Now().Add(lockBound); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		for _, row := range m.List() {
			if row["id"] == id && row["state"] == want {
				return
			}
		}
	}
	t.Fatalf("session %s never reached state %s: %v", id, want, m.List())
}

// pty.read is open to any authorized caller. With wait_ms > 0 and no new data it
// blocks until data, eof, cancellation or the wait elapses; a wait that elapses
// is a success with empty data, the requested cursor as next_cursor, and
// eof=false. The read does not extend the idle timer.
// conformance: CON-CONC-05
func Test_CONCONC05_ReadNeedsNoOwnerAndTimeoutIsSuccess(t *testing.T) {
	m, reg := lockFixture(t)
	owner, reader := identity.Caller{ID: "node/owner"}, identity.Caller{ID: "peer/reader"}
	_ = reg.Add(owner)
	_ = reg.Add(reader)
	term := newGateTerminal(false)
	s := addSession(m, "p-readable1", owner.ID, term)
	term.output("hello")

	var data string
	var cursor uint64
	for deadline := time.Now().Add(lockBound); data == "" && time.Now().Before(deadline); {
		out, err := call(m, reader, "pty.read", Object{"session_id": "p-readable1", "wait_ms": 100})
		if err != nil {
			t.Fatalf("a non-owner read failed: %v", err)
		}
		data, cursor = out["data"].(string), out["next_cursor"].(uint64)
	}
	if data != "hello" || cursor != 5 {
		t.Fatalf("non-owner read got %q next_cursor %d", data, cursor)
	}

	// Elapsed wait: success with empty data and the cursor unchanged.
	started := time.Now()
	out, err := call(m, reader, "pty.read", Object{"session_id": "p-readable1", "cursor": 5, "wait_ms": 60})
	elapsed := time.Since(started)
	if err != nil || out["data"] != "" || out["next_cursor"] != uint64(5) || out["eof"] != false || out["truncated"] != false {
		t.Fatalf("elapsed wait: %v, %v", out, err)
	}
	if elapsed < 50*time.Millisecond {
		t.Fatalf("returned after %s although wait_ms was 60 and no data arrived", elapsed)
	}
	if elapsed > lockBound {
		t.Fatalf("a 60 ms wait took %s", elapsed)
	}
	// wait_ms = 0 never blocks.
	within(t, "a zero wait", func() {
		if out, err := call(m, reader, "pty.read", Object{"session_id": "p-readable1", "cursor": 5, "wait_ms": 0}); err != nil || out["data"] != "" || out["eof"] != false {
			t.Error(out, err)
		}
	})
	// A blocked read wakes up for new output...
	woke := make(chan Object, 1)
	go func() {
		out, _ := call(m, reader, "pty.read", Object{"session_id": "p-readable1", "cursor": 5, "wait_ms": 30000})
		woke <- out
	}()
	waitBlockedInRead(t)
	term.output("!")
	select {
	case out := <-woke:
		if out["data"] != "!" || out["next_cursor"] != uint64(6) {
			t.Fatal(out)
		}
	case <-time.After(lockBound):
		t.Fatal("a blocked read did not wake for new output")
	}
	// ...for cancellation of the call...
	ctx, cancel := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() {
		_, err := s.read(ctx, 6, 32, 30*time.Second)
		cancelled <- err
	}()
	waitBlockedInRead(t)
	cancel()
	select {
	case err := <-cancelled:
		if err == nil {
			t.Fatal("a cancelled read reported success")
		}
	case <-time.After(lockBound):
		t.Fatal("a blocked read ignored cancellation")
	}
	// ...and for eof.
	eof := make(chan Object, 1)
	go func() {
		out, _ := call(m, reader, "pty.read", Object{"session_id": "p-readable1", "cursor": 6, "wait_ms": 30000})
		eof <- out
	}()
	waitBlockedInRead(t)
	term.release()
	select {
	case out := <-eof:
		if out["eof"] != true || out["data"] != "" || out["next_cursor"] != uint64(6) {
			t.Fatal(out)
		}
	case <-time.After(lockBound):
		t.Fatal("a blocked read did not wake for eof")
	}
}

// The cursor is the number of bytes ever written to the ring and never goes
// backwards. A reader that is behind the retained window gets truncated=true
// and data from the oldest retained byte; that is a success, never an error.
// Offsets still retained keep their meaning.
// conformance: INV-07
func Test_INV07_CursorIsTotalBytesAndOverwriteIsTruncated(t *testing.T) {
	s := &Session{capacity: 8, changed: make(chan struct{})}
	s.touch()
	total := 0
	last := uint64(0)
	for _, chunk := range []string{"ab", "cdef", "ghijk", "l", "mnopqrstuvwxyz"} {
		s.append([]byte(chunk))
		total += len(chunk)
		if s.cursor != uint64(total) || s.cursor < last {
			t.Fatalf("cursor = %d after %d bytes written (previous %d)", s.cursor, total, last)
		}
		last = s.cursor
	}
	// "abcdefghijklmnopqrstuvwxyz": the last 8 bytes are retained.
	out, err := s.read(context.Background(), 0, 64, 0)
	if err != nil || out["truncated"] != true || out["data"] != "stuvwxyz" || out["next_cursor"] != uint64(26) || out["eof"] != false {
		t.Fatalf("stale cursor: %v, %v", out, err)
	}
	out, err = s.read(context.Background(), 17, 64, 0)
	if err != nil || out["truncated"] != true || out["data"] != "stuvwxyz" {
		t.Fatalf("cursor just before the window: %v, %v", out, err)
	}
	out, err = s.read(context.Background(), 18, 3, 0)
	if err != nil || out["truncated"] != false || out["data"] != "stu" || out["next_cursor"] != uint64(21) {
		t.Fatalf("cursor at the window start: %v, %v", out, err)
	}
	// A reader that continues from its previous next_cursor sees the following
	// bytes: offsets keep the meaning they had when handed out.
	out, err = s.read(context.Background(), 21, 64, 0)
	if err != nil || out["truncated"] != false || out["data"] != "vwxyz" || out["next_cursor"] != uint64(26) {
		t.Fatalf("continuation: %v, %v", out, err)
	}
	s.append([]byte("!"))
	out, _ = s.read(context.Background(), 25, 64, 0)
	if out["data"] != "z!" || out["truncated"] != false {
		t.Fatalf("offsets shifted after another write: %v", out)
	}
	// A cursor ahead of the stream is not an error and is returned unchanged.
	out, err = s.read(context.Background(), 1000, 64, 0)
	if err != nil || out["data"] != "" || out["next_cursor"] != uint64(1000) {
		t.Fatalf("future cursor: %v, %v", out, err)
	}
	// Through the tool interface the overwrite is also a success.
	m, reg := lockFixture(t)
	reader := identity.Caller{ID: "node/reader"}
	_ = reg.Add(reader)
	term := newGateTerminal(false)
	session := addSession(m, "p-ringcursr", reader.ID, term)
	session.capacity = 4
	term.output("0123456789")
	var viaTool Object
	for deadline := time.Now().Add(lockBound); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		viaTool, err = call(m, reader, "pty.read", Object{"session_id": "p-ringcursr", "cursor": 0, "wait_ms": 0})
		if err != nil {
			t.Fatalf("an overwritten cursor must not be an error: %v", err)
		}
		if viaTool["next_cursor"] == uint64(10) {
			break
		}
	}
	if viaTool["truncated"] != true || viaTool["data"] != "6789" {
		t.Fatalf("pty.read of an overwritten cursor: %v", viaTool)
	}
}

// pty.list reports id, name, owner, pid, state, idle_s, cols, rows and cwd for
// every session and never any buffered terminal text. An orphan has an empty
// owner and a closed session pid 0.
// conformance: CTR-PTY-02
func Test_CTRPTY02_ListFieldsHaveNoBufferText(t *testing.T) {
	m, reg := lockFixture(t)
	owner := identity.Caller{ID: "node/owner"}
	_ = reg.Add(owner)
	active, orphan, closed := newGateTerminal(false), newGateTerminal(false), newGateTerminal(false)
	a := addSession(m, "p-listact01", owner.ID, active)
	a.Name, a.CWD, a.Cols, a.Rows = "build", "/work", 120, 30
	addSession(m, "p-listorph1", owner.ID, orphan)
	addSession(m, "p-listclos1", owner.ID, closed)
	for _, term := range []*gateTerminal{active, orphan, closed} {
		term.output("SECRET-BUFFER-TEXT")
	}
	for deadline := time.Now().Add(lockBound); time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
		m.mu.Lock()
		ready := true
		for _, s := range m.sessions {
			s.mu.Lock()
			ready = ready && s.cursor == uint64(len("SECRET-BUFFER-TEXT"))
			s.mu.Unlock()
		}
		m.mu.Unlock()
		if ready {
			break
		}
	}
	m.mu.Lock()
	m.sessions["p-listorph1"].Owner, m.sessions["p-listorph1"].State = "", "orphaned"
	m.sessions["p-listorph1"].orphanAt = time.Now()
	m.byCaller[owner.ID]--
	m.mu.Unlock()
	closed.release()
	waitState(t, m, "p-listclos1", "closed")

	out, err := call(m, owner, "pty.list", Object{})
	if err != nil {
		t.Fatal(err)
	}
	rows := out["sessions"].([]Object)
	if len(rows) != 3 {
		t.Fatal(rows)
	}
	wantKeys := []string{"cols", "cwd", "id", "idle_s", "name", "owner", "pid", "rows", "state"}
	byID := map[string]Object{}
	for _, row := range rows {
		keys := []string{}
		for key, value := range row {
			keys = append(keys, key)
			if strings.Contains(toString(value), "SECRET-BUFFER-TEXT") {
				t.Errorf("pty.list leaks buffered output in %s: %v", key, row)
			}
		}
		sort.Strings(keys)
		if !reflect.DeepEqual(keys, wantKeys) {
			t.Errorf("list entry fields = %v, want %v", keys, wantKeys)
		}
		byID[row["id"].(string)] = row
	}
	if r := byID["p-listact01"]; r["state"] != "active" || r["owner"] != owner.ID || r["name"] != "build" || r["cwd"] != "/work" || r["cols"] != 120 || r["rows"] != 30 || r["pid"] != 4321 {
		t.Errorf("active entry: %v", r)
	}
	if r := byID["p-listorph1"]; r["state"] != "orphaned" || r["owner"] != "" {
		t.Errorf("orphaned entry: %v", r)
	}
	if r := byID["p-listclos1"]; r["state"] != "closed" || r["pid"] != 0 {
		t.Errorf("closed entry: %v", r)
	}
}

func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
