package device

import (
	"math"
	"slices"
	"time"
)

const (
	// statsWindow bounds how long a probe result counts toward a path's quality.
	statsWindow = 5 * time.Minute
	// statsMaxSamples caps memory per path; at the 2s probe cadence it covers the whole window.
	statsMaxSamples = 200
)

type sample struct {
	at  time.Time
	ok  bool
	rtt time.Duration
}

// pathStats is a sliding window of probe results for one device path. It survives a failed
// probe, so a path that flaps keeps its history instead of looking brand new each time it returns.
type pathStats struct {
	samples []sample
}

func (s *pathStats) add(x sample) {
	s.samples = append(s.samples, x)
	s.trim(x.at)
}

func (s *pathStats) trim(now time.Time) {
	cut := 0
	for cut < len(s.samples) && now.Sub(s.samples[cut].at) > statsWindow {
		cut++
	}
	if over := len(s.samples) - cut - statsMaxSamples; over > 0 {
		cut += over
	}
	s.samples = s.samples[cut:]
}

// Quality summarises a path's recent probes. Loss is the failed share (0..1); Flaps counts
// successes followed by a failure, which is how a random block shows up even at low loss.
type Quality struct {
	Samples   int     `json:"samples"`
	LatencyMs float64 `json:"latency_ms"`
	P90Ms     float64 `json:"p90_ms"`
	JitterMs  float64 `json:"jitter_ms"`
	Loss      float64 `json:"loss"`
	Flaps     int     `json:"flaps"`
}

func (s *pathStats) quality(now time.Time) Quality {
	var rtts []float64
	failed, flaps := 0, 0
	total := 0
	prevOK := false
	for _, x := range s.samples {
		if now.Sub(x.at) > statsWindow {
			continue
		}
		total++
		if x.ok {
			rtts = append(rtts, float64(x.rtt)/float64(time.Millisecond))
		} else {
			failed++
			if prevOK {
				flaps++
			}
		}
		prevOK = x.ok
	}
	q := Quality{Samples: total, Flaps: flaps}
	if total > 0 {
		q.Loss = float64(failed) / float64(total)
	}
	if len(rtts) > 0 {
		slices.Sort(rtts)
		q.LatencyMs = median(rtts)
		q.P90Ms = percentile(rtts, 0.9)
		q.JitterMs = q.P90Ms - q.LatencyMs
	}
	return q
}

// median averages the middle pair of an even-sized sorted slice.
func median(sorted []float64) float64 {
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// last is the time of the newest sample, or the zero time when there is none.
func (s *pathStats) last() time.Time {
	if len(s.samples) == 0 {
		return time.Time{}
	}
	return s.samples[len(s.samples)-1].at
}

// percentile reads a sorted slice by nearest rank: the ceiling of p*n, so a small window's p90
// is never below the sample it should cover.
func percentile(sorted []float64, p float64) float64 {
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[min(max(i, 0), len(sorted)-1)]
}
