package device

import (
	"testing"
	"time"
)

func TestPathStatsQuality(t *testing.T) {
	now := time.Now()
	var s pathStats
	ms := time.Millisecond
	// 10 probes: three failures, two of them right after a success.
	for i, x := range []struct {
		ok  bool
		rtt time.Duration
	}{{true, 40 * ms}, {true, 50 * ms}, {false, 0}, {true, 60 * ms}, {true, 40 * ms}, {false, 0}, {false, 0}, {true, 200 * ms}, {true, 45 * ms}, {true, 55 * ms}} {
		s.add(sample{at: now.Add(time.Duration(i-10) * time.Second), ok: x.ok, rtt: x.rtt})
	}
	q := s.quality(now)
	if q.Samples != 10 || q.Loss != 0.3 || q.Flaps != 2 {
		t.Fatalf("quality = %+v", q)
	}
	if q.LatencyMs != 50 || q.P90Ms != 60 && q.P90Ms != 200 || q.JitterMs != q.P90Ms-q.LatencyMs {
		t.Fatalf("latency = %+v", q)
	}
}

func TestPathStatsWindow(t *testing.T) {
	now := time.Now()
	var s pathStats
	s.add(sample{at: now.Add(-10 * time.Minute), ok: false})
	s.add(sample{at: now, ok: true, rtt: time.Millisecond})
	if q := s.quality(now); q.Samples != 1 || q.Loss != 0 {
		t.Fatalf("old sample counted: %+v", q)
	}
	for i := 0; i < statsMaxSamples*2; i++ {
		s.add(sample{at: now, ok: true})
	}
	if len(s.samples) != statsMaxSamples {
		t.Fatalf("len = %d", len(s.samples))
	}
}

func TestPathStatsEmpty(t *testing.T) {
	var s pathStats
	if q := s.quality(time.Now()); q != (Quality{}) {
		t.Fatalf("empty quality = %+v", q)
	}
}
