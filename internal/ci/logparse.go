// Package ci hosts GitHub Actions runners: it downloads the official runner, registers it, starts
// it, keeps it running and reports what it is doing (CTR-CTL-15). The runner's own stdout is read
// only for its status lines; task output is never kept.
package ci

import (
	"regexp"
	"strings"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// lineKind is what a line of runner output says about the runner.
type lineKind int

const (
	lineOther lineKind = iota
	lineConnected
	lineListening
	lineJobStarted
	lineJobDone
	lineSessionConflict
	lineRunnerRemoved
	lineVersionDeprecated
	lineAuthFailed
	lineMissingDependency
)

type runnerLine struct {
	kind   lineKind
	job    string
	result v2.CiResultKind
}

var (
	jobStartedPattern = regexp.MustCompile(`Running job:\s*(.+?)\s*$`)
	jobDonePattern    = regexp.MustCompile(`Job\s+(.+?)\s+completed with result:\s*([A-Za-z]+)`)
)

// parseLine classifies one line of the runner listener's output. The runner prefixes most lines
// with a timestamp; the patterns do not depend on it.
func parseLine(line string) runnerLine {
	switch {
	case strings.Contains(line, "Listening for Jobs"):
		return runnerLine{kind: lineListening}
	case strings.Contains(line, "Connected to GitHub"):
		return runnerLine{kind: lineConnected}
	case strings.Contains(line, "A session for this runner already exists"):
		return runnerLine{kind: lineSessionConflict}
	case strings.Contains(line, "runner registration has been deleted from the server"),
		strings.Contains(line, "Runner not found"),
		strings.Contains(line, "has been deleted from the server"):
		return runnerLine{kind: lineRunnerRemoved}
	case strings.Contains(line, "version") && strings.Contains(line, "deprecated"):
		return runnerLine{kind: lineVersionDeprecated}
	case strings.Contains(line, "Failed to create a session") && strings.Contains(line, "Unauthorized"):
		return runnerLine{kind: lineAuthFailed}
	case dependencyMissing(line):
		return runnerLine{kind: lineMissingDependency}
	}
	if m := jobDonePattern.FindStringSubmatch(line); m != nil {
		return runnerLine{kind: lineJobDone, job: m[1], result: resultOf(m[2])}
	}
	if m := jobStartedPattern.FindStringSubmatch(line); m != nil {
		return runnerLine{kind: lineJobStarted, job: m[1]}
	}
	return runnerLine{}
}

func resultOf(word string) v2.CiResultKind {
	switch strings.ToLower(word) {
	case "succeeded":
		return v2.CiSucceeded
	case "failed":
		return v2.CiFailed
	case "canceled", "cancelled":
		return v2.CiCanceled
	case "skipped":
		return v2.CiSkipped
	case "abandoned":
		return v2.CiAbandoned
	}
	return v2.CiUnknown
}
