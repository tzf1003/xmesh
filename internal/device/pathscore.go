package device

import "math"

const (
	// MinScoredSamples is how many probes a path needs in its window before it is scored; fewer
	// than that and the path keeps the position the user's priority list gives it.
	MinScoredSamples = 5

	// A random block shows up as loss and as drops, not as latency, so both weigh far more than
	// a slow but steady link: 10% loss costs as much as 200 ms, and every drop 300 ms (capped).
	lossPenaltyMs    = 2000.0
	flapPenaltyMs    = 300.0
	maxFlapPenaltyMs = 3000.0
	jitterWeight     = 2.0

	// UnhealthyLoss and UnhealthyFlaps are the point where a path counts as failing, not just worse.
	UnhealthyLoss  = 0.30
	UnhealthyFlaps = 3

	// deadScore ranks a path that answers nothing below every path that answers something.
	deadScore = 1e6
)

// kindBias breaks near-ties toward the path that adds no third party: LAN is almost always right,
// a direct public address is next best, Cloudflare adds latency and costs traffic through the user's account.
func kindBias(kind PathKind) float64 {
	switch kind {
	case LAN:
		return -50
	case Direct:
		return -10
	case Cloudflare:
		return 30
	}
	return 0
}

// Score rates a path from its recent probes, lower being better: median latency, twice the jitter,
// plus penalties for loss and for drops. ok is false while the path has too few samples to judge.
func Score(kind PathKind, q Quality) (score float64, ok bool) {
	if q.Samples < MinScoredSamples {
		return 0, false
	}
	if q.Loss >= 0.999 {
		return deadScore, true
	}
	flaps := math.Min(float64(q.Flaps)*flapPenaltyMs, maxFlapPenaltyMs)
	return q.LatencyMs + jitterWeight*q.JitterMs + q.Loss*lossPenaltyMs + flaps + kindBias(kind), true
}

// Unhealthy reports a path that is failing right now: enough samples, and heavy loss or repeated drops.
func Unhealthy(q Quality) bool {
	return q.Samples >= MinScoredSamples && (q.Loss >= UnhealthyLoss || q.Flaps >= UnhealthyFlaps)
}
