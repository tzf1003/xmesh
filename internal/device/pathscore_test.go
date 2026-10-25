package device

import "testing"

func TestScorePrefersSteadyOverFast(t *testing.T) {
	// A Tailnet that is fast but loses 15% against a Cloudflare tunnel that is slower and steady.
	tailnet, ok1 := Score(Tailnet, Quality{Samples: 20, LatencyMs: 60, P90Ms: 90, JitterMs: 30, Loss: 0.15, Flaps: 2})
	cloudflare, ok2 := Score(Cloudflare, Quality{Samples: 20, LatencyMs: 220, P90Ms: 260, JitterMs: 40, Loss: 0})
	if !ok1 || !ok2 || cloudflare >= tailnet {
		t.Fatalf("tailnet %v cloudflare %v: the steady path must win", tailnet, cloudflare)
	}
}

func TestScoreNeedsSamplesAndBiasBreaksTies(t *testing.T) {
	if _, ok := Score(LAN, Quality{Samples: MinScoredSamples - 1, LatencyMs: 1}); ok {
		t.Fatal("scored with too few samples")
	}
	q := Quality{Samples: 10, LatencyMs: 80, P90Ms: 80}
	lan, _ := Score(LAN, q)
	tail, _ := Score(Tailnet, q)
	cf, _ := Score(Cloudflare, q)
	if !(lan < tail && tail < cf) {
		t.Fatalf("equal quality should rank lan < tailnet < cloudflare: %v %v %v", lan, tail, cf)
	}
}

func TestScoreDeadPathLosesToAnyAnswering(t *testing.T) {
	dead, _ := Score(LAN, Quality{Samples: 20, Loss: 1})
	slow, _ := Score(Cloudflare, Quality{Samples: 20, LatencyMs: 3000, P90Ms: 4000, JitterMs: 1000, Loss: 0.4, Flaps: 9})
	if dead <= slow {
		t.Fatalf("dead %v should rank below a slow, lossy path %v", dead, slow)
	}
}

func TestUnhealthy(t *testing.T) {
	cases := map[string]struct {
		q    Quality
		want bool
	}{
		"clean":          {Quality{Samples: 20}, false},
		"some loss":      {Quality{Samples: 20, Loss: 0.1, Flaps: 2}, false},
		"heavy loss":     {Quality{Samples: 20, Loss: 0.3}, true},
		"repeated drops": {Quality{Samples: 20, Flaps: 3}, true},
		"too few probes": {Quality{Samples: 3, Loss: 1}, false},
	}
	for name, c := range cases {
		if got := Unhealthy(c.q); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}
