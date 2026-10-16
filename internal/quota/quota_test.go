package quota

import (
	"context"
	"encoding/json"
	"github.com/tzf1003/xmesh/internal/toolerr"
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

func Test_CONSEC10_SetLimitsAffectsLaterCalls(t *testing.T) {
	l := New(1, 1, time.Minute)
	_, done, e := l.Acquire(context.Background(), "a")
	if e != nil {
		t.Fatal(e)
	}
	done()
	if _, _, e = l.Acquire(context.Background(), "a"); e == nil {
		t.Fatal("hourly accepted")
	}
	l.SetLimits(3, 2, time.Second)
	ctx, done, e := l.Acquire(context.Background(), "a")
	if e != nil {
		t.Fatal(e)
	}
	defer done()
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Second {
		t.Fatal("new call duration not applied")
	}
}

func Test_CONSEC10_ExecTimeoutMustFitCallBudget(t *testing.T) {
	l := New(10, 10, 10*time.Minute)
	cases := []struct {
		tool, args string
		fallback   time.Duration
		refused    bool
	}{
		{"exec.run", `{"cmd":"x"}`, 0, false},
		{"exec.run", `{"cmd":"x"}`, 2 * time.Minute, false},
		{"exec.run", `{"cmd":"x"}`, 11 * time.Minute, true},
		{"exec.run", `{"timeout":"10m"}`, 0, false},
		{"exec.run", `{"timeout":"10m1s"}`, 0, true},
		{"exec.run", `{"timeout":"1h"}`, 0, true},
		{"device/exec.run", `{"timeout":"1h"}`, 0, true},
		{"exec.run", `{"timeout":600000}`, 0, false},
		{"exec.run", `{"timeout":600001}`, 0, true},
		{"exec.run", `{"timeout":9223372036854775807}`, 0, true},
		{"exec.run", `{"timeout":"bogus"}`, 0, false},
		{"exec.run", `not json`, 0, false},
		{"exec.output", `{"timeout":"1h"}`, 0, false},
		{"fs.read", `{"timeout":"1h"}`, 0, false},
		{"device/pty.open", `{"timeout":"1h"}`, 0, false},
	}
	for _, c := range cases {
		err := l.CheckExecTimeout(c.tool, json.RawMessage(c.args), c.fallback)
		if (err != nil) != c.refused {
			t.Fatal(c, err)
		}
		if err != nil {
			e := err.(*toolerr.Error)
			if e.Code != "invalid_args" || e.Fields["field"] != "timeout" || e.Fields["limit"] != int64(600000) {
				t.Fatal(c, e.Object())
			}
		}
	}
}
