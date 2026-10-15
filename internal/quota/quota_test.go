package quota

import (
	"context"
	"testing"
	"time"
)

func Test_CONSEC10_QuotaIsolation(t *testing.T) {
	l := New(2, 1, time.Second)
	_, done, e := l.Acquire(context.Background(), "a")
	if e != nil {
		t.Fatal(e)
	}
	if _, _, e = l.Acquire(context.Background(), "a"); e == nil {
		t.Fatal("concurrency accepted")
	}
	_, other, e := l.Acquire(context.Background(), "b")
	if e != nil {
		t.Fatal(e)
	}
	other()
	done()
	_, done, e = l.Acquire(context.Background(), "a")
	if e != nil {
		t.Fatal(e)
	}
	done()
	if _, _, e = l.Acquire(context.Background(), "a"); e == nil {
		t.Fatal("hourly accepted")
	}
}

func Test_ExpiredBucketsPrunedWhileRunningRetained(t *testing.T) {
	l := New(2, 2, time.Minute)
	old := time.Now().Add(-2 * time.Hour)
	l.buckets["idle"] = &bucket{calls: []time.Time{old}}
	l.buckets["running"] = &bucket{calls: []time.Time{old}, running: 1}
	_, done, e := l.Acquire(context.Background(), "new")
	if e != nil {
		t.Fatal(e)
	}
	done()
	if l.buckets["idle"] != nil || l.buckets["running"] == nil {
		t.Fatal("expired bucket retention")
	}
}
