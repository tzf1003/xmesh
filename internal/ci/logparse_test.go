package ci

import (
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// conformance: CTR-CTL-15
func Test_CTRCTL15_RunnerStatusLines(t *testing.T) {
	cases := []struct {
		line string
		want runnerLine
	}{
		{"√ Connected to GitHub", runnerLine{kind: lineConnected}},
		{"2026-10-18 09:00:00Z: Listening for Jobs", runnerLine{kind: lineListening}},
		{"2026-10-18 09:01:00Z: Running job: go test (ubuntu)", runnerLine{kind: lineJobStarted, job: "go test (ubuntu)"}},
		{"2026-10-18 09:05:00Z: Job go test (ubuntu) completed with result: Succeeded", runnerLine{kind: lineJobDone, job: "go test (ubuntu)", result: v2.CiSucceeded}},
		{"2026-10-18 09:05:00Z: Job lint completed with result: Failed", runnerLine{kind: lineJobDone, job: "lint", result: v2.CiFailed}},
		{"2026-10-18 09:05:00Z: Job lint completed with result: Canceled", runnerLine{kind: lineJobDone, job: "lint", result: v2.CiCanceled}},
		{"2026-10-18 09:05:00Z: Job lint completed with result: Weird", runnerLine{kind: lineJobDone, job: "lint", result: v2.CiUnknown}},
		{"A session for this runner already exists.", runnerLine{kind: lineSessionConflict}},
		{"The runner registration has been deleted from the server, please re-configure.", runnerLine{kind: lineRunnerRemoved}},
		{"Runner version v2.100.0 is deprecated and cannot receive messages.", runnerLine{kind: lineVersionDeprecated}},
		{"some task output Running job is not at the start", runnerLine{}},
		{"", runnerLine{}},
	}
	for _, c := range cases {
		if got := parseLine(c.line); got != c.want {
			t.Errorf("parseLine(%q) = %+v, want %+v", c.line, got, c.want)
		}
	}
}
