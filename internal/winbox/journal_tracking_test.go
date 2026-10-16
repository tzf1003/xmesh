package winbox

import (
	"bytes"
	"os"
	"testing"
)

func trackingJournalFixture(t *testing.T) (*grantJournal, string) {
	j, path := createdJournalFixture(t)
	grant := journalTestGrant()
	grant.Grant.Object = j.records[1].Workspace.Root
	if _, err := j.append(grant); err != nil {
		t.Fatal(err)
	}
	return j, path
}

func TestGrantJournalTrackingReplay(t *testing.T) {
	j, path := trackingJournalFixture(t)
	cursor := &workspaceChangeCursor{17, 100}
	if _, err := j.append(journalRecord{Kind: "workspace-tracking", ChangeCursor: cursor}); err != nil {
		t.Fatal(err)
	}
	cursor.Next = 999
	if j.state.changeCursor.Next != 100 || j.records[3].ChangeCursor.Next != 100 {
		t.Fatal("caller changed committed cursor")
	}
	execution, err := j.append(journalExecution())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-tracked", ChangeCursor: &workspaceChangeCursor{17, 200}}); err != nil {
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
	if err != nil || j.state.changeCursor == nil || *j.state.changeCursor != (workspaceChangeCursor{17, 200}) || !j.state.executions[execution] {
		t.Fatal("tracking replay", err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-released"}); err == nil {
		t.Fatal("tracking bypassed process gate")
	}
	if _, err := j.append(journalRecord{Kind: "execution-reaped", ExecutionSequence: execution}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "released", GrantSequence: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-released"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-tracked", ChangeCursor: &workspaceChangeCursor{17, 300}}); err == nil {
		t.Fatal("cursor advanced after release")
	}
}

func TestGrantJournalTrackingInvalidTransitions(t *testing.T) {
	for _, name := range []string{"missing", "zero-id", "negative", "initial-without-grants", "initial-after-release", "initial-after-execution", "initial-after-created", "duplicate-start", "advance-without-start", "backwards", "repeated", "foreign-journal", "profile", "workspace", "created", "grant", "grant-sequence", "execution", "execution-sequence", "unrelated"} {
		t.Run(name, func(t *testing.T) {
			j, path := trackingJournalFixture(t)
			r := journalRecord{Kind: "workspace-tracking", ChangeCursor: &workspaceChangeCursor{17, 100}}
			start := func() {
				if _, err := j.append(r); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "missing":
				r.ChangeCursor = nil
			case "zero-id":
				r.ChangeCursor.JournalID = 0
			case "negative":
				r.ChangeCursor.Next = -1
			case "initial-without-grants":
				j, path = createdJournalFixture(t)
			case "initial-after-release":
				if _, err := j.append(journalRecord{Kind: "released", GrantSequence: 3}); err != nil {
					t.Fatal(err)
				}
			case "initial-after-execution":
				if _, err := j.append(journalExecution()); err != nil {
					t.Fatal(err)
				}
			case "initial-after-created":
				if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{createdJournalObject(2)}}); err != nil {
					t.Fatal(err)
				}
			case "duplicate-start":
				start()
			case "advance-without-start":
				r.Kind = "workspace-tracked"
			case "backwards", "repeated", "foreign-journal":
				start()
				r.Kind = "workspace-tracked"
				r.ChangeCursor = &workspaceChangeCursor{17, 99}
				if name == "repeated" {
					r.ChangeCursor.Next = 100
				}
				if name == "foreign-journal" {
					r.ChangeCursor = &workspaceChangeCursor{18, 200}
				}
			case "profile":
				r.Profile = journalProfile().Profile
			case "workspace":
				r.Workspace = j.records[1].Workspace
			case "created":
				r.CreatedObjects = []journalObject{createdJournalObject(2)}
			case "grant":
				r.Grant = journalTestGrant().Grant
			case "grant-sequence":
				r.GrantSequence = 3
			case "execution":
				r.ExecutionID = journalExecution().ExecutionID
			case "execution-sequence":
				r.ExecutionSequence = 3
			case "unrelated":
				r.Kind = "complete"
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			count := len(j.records)
			var cursor workspaceChangeCursor
			if j.state.changeCursor != nil {
				cursor = *j.state.changeCursor
			}
			if _, err := j.append(r); err == nil {
				t.Fatal("invalid cursor transition accepted")
			}
			after, err := os.ReadFile(path)
			var actual workspaceChangeCursor
			if j.state.changeCursor != nil {
				actual = *j.state.changeCursor
			}
			if err != nil || !bytes.Equal(before, after) || len(j.records) != count || actual != cursor {
				t.Fatal("rejected cursor changed durable ownership", err)
			}
		})
	}
}

func TestGrantJournalTrackingFailedWrite(t *testing.T) {
	j, _ := trackingJournalFixture(t)
	if _, err := j.append(journalRecord{Kind: "workspace-tracking", ChangeCursor: &workspaceChangeCursor{17, 100}}); err != nil {
		t.Fatal(err)
	}
	if err := j.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-tracked", ChangeCursor: &workspaceChangeCursor{17, 200}}); err == nil || j.blocked == nil || j.state.changeCursor.Next != 100 || len(j.records) != 4 {
		t.Fatal("failed write advanced cursor", err)
	}
}
