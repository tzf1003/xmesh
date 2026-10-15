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
