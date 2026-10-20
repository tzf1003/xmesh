package device

import "testing"

func TestIndependentPendingPairingsCanBothCommit(t *testing.T) {
	a, b, c := pairing(t, "a"), pairing(t, "b"), pairing(t, "c")
	if err := b.SetPassword(password); err != nil {
		t.Fatal(err)
	}
	first, second := preparePair(t, a, b), preparePair(t, c, b)
	if err := b.Confirm(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := b.Confirm(t.Context(), second); err != nil {
		t.Fatal("unrelated pair invalidated pending exchange", err)
	}
	if len(b.trust.Snapshot()) != 2 {
		t.Fatal("pending pairing lost")
	}
}
