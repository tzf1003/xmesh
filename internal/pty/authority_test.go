package pty

import (
	"testing"
	"time"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func authority(t *testing.T) (*BudgetAuthority, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(1000, 0)}
	a := newBudgetAuthority(clock.Now, 10*time.Second)
	clock.advance(11 * time.Second) // leave the post-start settling period
	return a, clock
}

func handle(t *testing.T, a *BudgetAuthority, req AuthorityRequest, owner string) AuthorityReply {
	t.Helper()
	reply, err := a.Handle(req, owner)
	if err != nil {
		t.Fatal(err)
	}
	return reply
}

func Test_BudgetAuthoritySettlesAfterRestart(t *testing.T) {
	clock := &fakeClock{now: time.Unix(1000, 0)}
	a := newBudgetAuthority(clock.Now, 10*time.Second)
	reply := handle(t, a, AuthorityRequest{Op: "acquire"}, "n/a")
	if reply.OK || !reply.Busy || reply.Reason != "settling" {
		t.Fatal("a fresh authority granted a lease before earlier leases could expire", reply)
	}
	clock.advance(10 * time.Second)
	if reply = handle(t, a, AuthorityRequest{Op: "acquire"}, "n/a"); !reply.OK {
		t.Fatal(reply)
	}
}

func Test_BudgetAuthorityMutualExclusionAndFencing(t *testing.T) {
	a, clock := authority(t)
	first := handle(t, a, AuthorityRequest{Op: "acquire"}, "one/x")
	if !first.OK || first.Token == 0 {
		t.Fatal(first)
	}
	if again := handle(t, a, AuthorityRequest{Op: "acquire"}, "one/x"); again.OK {
		t.Fatal("holder re-acquired its own lease")
	}
	if other := handle(t, a, AuthorityRequest{Op: "acquire"}, "two/y"); other.OK || !other.Busy {
		t.Fatal("two holders at once", other)
	}
	clock.advance(11 * time.Second)
	second := handle(t, a, AuthorityRequest{Op: "acquire"}, "two/y")
	if !second.OK || second.Token <= first.Token {
		t.Fatal("expired lease was not replaced with a higher token", second)
	}
	// The former holder can neither mark nor release.
	for _, op := range []string{"mark", "release"} {
		if r := handle(t, a, AuthorityRequest{Op: op, Token: first.Token, Peer: "p", Epoch: "e", Outcome: "ok"}, "one/x"); r.OK {
			t.Fatal("stale holder accepted", op)
		}
	}
	// A different node cannot use the token even with the same nonce.
	if r := handle(t, a, AuthorityRequest{Op: "release", Token: second.Token, Outcome: "ok"}, "one/y"); r.OK {
		t.Fatal("foreign node released the lease")
	}
	if r := handle(t, a, AuthorityRequest{Op: "release", Token: second.Token, Outcome: "ok"}, "two/y"); !r.OK {
		t.Fatal(r)
	}
	if r := handle(t, a, AuthorityRequest{Op: "acquire"}, "one/x"); !r.OK {
		t.Fatal("release did not free the mutex", r)
	}
}

func Test_BudgetAuthorityPendingMarks(t *testing.T) {
	a, clock := authority(t)
	lease := handle(t, a, AuthorityRequest{Op: "acquire"}, "one/x")
	if r := handle(t, a, AuthorityRequest{Op: "mark", Token: lease.Token, Peer: "device-a", Epoch: "e1"}, "one/x"); !r.OK {
		t.Fatal(r)
	}
	// A clean outcome removes the mark.
	handle(t, a, AuthorityRequest{Op: "release", Token: lease.Token, Outcome: "ok"}, "one/x")
	lease = handle(t, a, AuthorityRequest{Op: "acquire"}, "two/y")
	if len(lease.Pending) != 0 {
		t.Fatal("completed call left a pending mark", lease.Pending)
	}
	// An uncertain outcome keeps it for every other ingress.
	handle(t, a, AuthorityRequest{Op: "mark", Token: lease.Token, Peer: "device-a", Epoch: "e1"}, "two/y")
	handle(t, a, AuthorityRequest{Op: "release", Token: lease.Token, Outcome: "uncertain"}, "two/y")
	lease = handle(t, a, AuthorityRequest{Op: "acquire"}, "one/x")
	if lease.Pending["device-a"] != "e1" {
		t.Fatal("uncertain call was forgotten", lease.Pending)
	}
	// A holder that vanished mid-call leaves its mark behind as well.
	handle(t, a, AuthorityRequest{Op: "mark", Token: lease.Token, Peer: "device-b", Epoch: "e2"}, "one/x")
	clock.advance(11 * time.Second)
	lease = handle(t, a, AuthorityRequest{Op: "acquire"}, "two/y")
	if lease.Pending["device-b"] != "e2" || lease.Pending["device-a"] != "e1" {
		t.Fatal("crashed holder's call was forgotten", lease.Pending)
	}
	// Marks of restarted Agents are dropped when the next holder reports them stale.
	handle(t, a, AuthorityRequest{Op: "release", Token: lease.Token, Outcome: "ok", Stale: map[string]string{"device-a": "e1", "device-b": "other"}}, "two/y")
	lease = handle(t, a, AuthorityRequest{Op: "acquire"}, "one/x")
	if _, ok := lease.Pending["device-a"]; ok || lease.Pending["device-b"] != "e2" {
		t.Fatal("stale handling wrong", lease.Pending)
	}
}

func Test_BudgetAuthoritySettlesUndeliveredRelease(t *testing.T) {
	a, clock := authority(t)
	lease := handle(t, a, AuthorityRequest{Op: "acquire"}, "one/x")
	handle(t, a, AuthorityRequest{Op: "mark", Token: lease.Token, Peer: "device-a", Epoch: "e1"}, "one/x")
	// The release was lost, so the lease lapses with its mark still set.
	clock.advance(11 * time.Second)
	next := handle(t, a, AuthorityRequest{Op: "acquire", Settle: &AuthoritySettle{Token: lease.Token, Outcome: "ok"}}, "one/x")
	if !next.OK || len(next.Pending) != 0 {
		t.Fatal("owner's own settled call stayed pending", next)
	}
	// Another owner cannot settle someone else's call.
	handle(t, a, AuthorityRequest{Op: "mark", Token: next.Token, Peer: "device-b", Epoch: "e2"}, "one/x")
	handle(t, a, AuthorityRequest{Op: "release", Token: next.Token, Outcome: "uncertain"}, "one/x")
	other := handle(t, a, AuthorityRequest{Op: "acquire", Settle: &AuthoritySettle{Token: next.Token, Outcome: "ok"}}, "two/y")
	if other.Pending["device-b"] != "e2" {
		t.Fatal("foreign owner cleared an uncertain call", other.Pending)
	}
}

func Test_BudgetAuthorityLapsedReleaseOnlyWithoutInterleaving(t *testing.T) {
	a, clock := authority(t)
	lease := handle(t, a, AuthorityRequest{Op: "acquire"}, "one/x")
	handle(t, a, AuthorityRequest{Op: "mark", Token: lease.Token, Peer: "device-a", Epoch: "e1"}, "one/x")
	clock.advance(11 * time.Second)
	if r := handle(t, a, AuthorityRequest{Op: "release", Token: lease.Token, Outcome: "ok"}, "one/x"); !r.OK {
		t.Fatal("late release with nothing interleaved was refused", r)
	}
	lease = handle(t, a, AuthorityRequest{Op: "acquire"}, "one/x")
	handle(t, a, AuthorityRequest{Op: "mark", Token: lease.Token, Peer: "device-a", Epoch: "e1"}, "one/x")
	clock.advance(11 * time.Second)
	handle(t, a, AuthorityRequest{Op: "acquire"}, "two/y") // someone else interleaved
	if r := handle(t, a, AuthorityRequest{Op: "release", Token: lease.Token, Outcome: "ok"}, "one/x"); r.OK {
		t.Fatal("late release accepted after another holder ran")
	}
}

func Test_BudgetAuthorityRejectsMalformedRequests(t *testing.T) {
	a, _ := authority(t)
	for _, req := range []AuthorityRequest{{Op: "bogus"}} {
		if _, err := a.Handle(req, "one/x"); err == nil {
			t.Fatal("malformed request accepted", req)
		}
	}
	lease := handle(t, a, AuthorityRequest{Op: "acquire"}, "one/x")
	if _, err := a.Handle(AuthorityRequest{Op: "mark", Token: lease.Token}, "one/x"); err == nil {
		t.Fatal("mark without peer accepted")
	}
	if _, err := a.Handle(AuthorityRequest{Op: "acquire"}, ""); err == nil {
		t.Fatal("empty owner accepted")
	}
}
