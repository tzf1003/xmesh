package winbox

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"testing"
)

func journalExecution() journalRecord {
	return journalRecord{Kind: "execution", ExecutionID: "0123456789abcdef0123456789abcdef"}
}

func TestGrantJournalExecutionReplayAndReleaseGate(t *testing.T) {
	for _, workspace := range []bool{false, true} {
		t.Run(fmt.Sprint("workspace-", workspace), func(t *testing.T) {
			j, path := journalFixture(t)
			if _, err := j.append(journalProfile()); err != nil {
				t.Fatal(err)
			}
			if workspace {
				root := journalTestGrant().Grant.Object
				root.Directory = true
				root.ID[0]++
				file := journalTestGrant().Grant.Object
				if _, err := j.append(journalRecord{Kind: "workspace", Workspace: &journalWorkspace{Path: `C:\workspace`, Root: root, Objects: []journalObject{root, file}}}); err != nil {
					t.Fatal(err)
				}
			}
			grant, err := j.append(journalTestGrant())
			if err != nil {
				t.Fatal(err)
			}
			execution, err := j.append(journalExecution())
			if err != nil {
				t.Fatal(err)
			}
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			j, err = loadGrantJournal(f)
			if err != nil || !j.state.executions[execution] || !j.state.executionIDs[journalExecution().ExecutionID] {
				t.Fatal("execution intent was not durably restored", err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, record := range []journalRecord{{Kind: "released", GrantSequence: grant}, {Kind: "workspace-released"}, {Kind: "complete"}} {
				if _, err := j.append(record); !errors.Is(err, errJournalProcessesPending) {
					t.Fatal("unconfirmed execution allowed reclamation", record.Kind, err)
				}
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(body, after) || !j.state.executions[execution] || !j.state.pending[grant] {
				t.Fatal("rejected reclamation changed durable ownership", err)
			}
			if _, err := j.append(journalRecord{Kind: "execution-reaped", ExecutionSequence: execution}); err != nil {
				t.Fatal(err)
			}
			if _, err := j.append(journalRecord{Kind: "released", GrantSequence: grant}); err != nil {
				t.Fatal(err)
			}
			if workspace {
				if _, err := j.append(journalRecord{Kind: "workspace-released"}); err != nil {
					t.Fatal(err)
				}
				if _, err := j.append(journalExecution()); err == nil {
					t.Fatal("execution accepted after workspace reclamation")
				}
			}
			if _, err := j.append(journalRecord{Kind: "complete"}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			replayed, err := loadGrantJournal(f)
			if err != nil || !replayed.state.complete || len(replayed.state.executions) != 0 {
				t.Fatal("confirmed execution replay retained obligations", err)
			}
		})
	}
}

func TestGrantJournalExecutionRejectsInvalidOwnership(t *testing.T) {
	cases := []struct {
		name   string
		setup  []journalRecord
		record journalRecord
	}{
		{"before-profile", nil, journalExecution()},
		{"invalid-id", []journalRecord{journalProfile()}, journalRecord{Kind: "execution", ExecutionID: "123"}},
		{"mixed-grant", []journalRecord{journalProfile()}, journalRecord{Kind: "execution", ExecutionID: journalExecution().ExecutionID, Grant: journalTestGrant().Grant}},
		{"mixed-profile", []journalRecord{journalProfile()}, journalRecord{Kind: "execution", ExecutionID: journalExecution().ExecutionID, Profile: journalProfile().Profile}},
		{"mixed-sequence", []journalRecord{journalProfile()}, journalRecord{Kind: "execution", ExecutionID: journalExecution().ExecutionID, ExecutionSequence: 1}},
		{"parallel-owner", []journalRecord{journalProfile(), journalExecution()}, journalRecord{Kind: "execution", ExecutionID: "1123456789abcdef0123456789abcdef"}},
		{"unknown-receipt", []journalRecord{journalProfile(), journalExecution()}, journalRecord{Kind: "execution-reaped", ExecutionSequence: 1}},
		{"receipt-payload", []journalRecord{journalProfile(), journalExecution()}, journalRecord{Kind: "execution-reaped", ExecutionSequence: 2, ExecutionID: journalExecution().ExecutionID}},
		{"duplicate-receipt", []journalRecord{journalProfile(), journalExecution(), {Kind: "execution-reaped", ExecutionSequence: 2}}, journalRecord{Kind: "execution-reaped", ExecutionSequence: 2}},
		{"reused-id", []journalRecord{journalProfile(), journalExecution(), {Kind: "execution-reaped", ExecutionSequence: 2}}, journalExecution()},
		{"unrelated-record-payload", []journalRecord{journalProfile()}, journalRecord{Kind: "complete", ExecutionSequence: 1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			j, path := journalFixture(t)
			for _, record := range test.setup {
				if _, err := j.append(record); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			count, pending, ids := len(j.records), len(j.state.executions), len(j.state.executionIDs)
			if _, err := j.append(test.record); err == nil {
				t.Fatal("invalid execution transition accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || count != len(j.records) || pending != len(j.state.executions) || ids != len(j.state.executionIDs) {
				t.Fatal("rejected execution transition changed ownership", err)
			}
		})
	}
}

func TestGrantJournalExecutionLimit(t *testing.T) {
	j, _ := journalFixture(t)
	if _, err := j.append(journalProfile()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 256; i++ {
		sequence, err := j.append(journalRecord{Kind: "execution", ExecutionID: fmt.Sprintf("%032x", i)})
		if err != nil {
			t.Fatal(i, err)
		}
		if _, err := j.append(journalRecord{Kind: "execution-reaped", ExecutionSequence: sequence}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.append(journalRecord{Kind: "execution", ExecutionID: fmt.Sprintf("%032x", 256)}); err == nil || len(j.state.executionIDs) != 256 || len(j.state.executions) != 0 {
		t.Fatal("execution lifetime limit did not retain a clean bounded state", err)
	}
}

func TestGrantJournalFailedExitReceiptRetainsIntent(t *testing.T) {
	j, _ := journalFixture(t)
	if _, err := j.append(journalProfile()); err != nil {
		t.Fatal(err)
	}
	sequence, err := j.append(journalExecution())
	if err != nil {
		t.Fatal(err)
	}
	if err := j.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "execution-reaped", ExecutionSequence: sequence}); err == nil || j.blocked == nil || !j.state.executions[sequence] || len(j.records) != 2 {
		t.Fatal("failed exit receipt discarded its execution obligation", err)
	}
	if _, err := j.append(journalRecord{Kind: "complete"}); err == nil {
		t.Fatal("failed exit receipt allowed completion")
	}
}
