package winbox

import (
	"bytes"
	"encoding/binary"
	"os"
	"testing"
)

func createdJournalFixture(t *testing.T) (*grantJournal, string) {
	t.Helper()
	j, path := journalFixture(t)
	root := journalObject{Volume: 17, ID: [16]byte{1}, Directory: true}
	for _, record := range []journalRecord{journalProfile(), {Kind: "workspace", Workspace: &journalWorkspace{Path: `C:\workspace`, Root: root, Objects: []journalObject{root}}}} {
		if _, err := j.append(record); err != nil {
			t.Fatal(err)
		}
	}
	return j, path
}

func createdJournalObject(id byte) journalObject {
	return journalObject{Volume: 17, ID: [16]byte{id}, Directory: id%2 == 0}
}

func TestGrantJournalCreatedReplayAndProcessGate(t *testing.T) {
	j, path := createdJournalFixture(t)
	execution, err := j.append(journalExecution())
	if err != nil {
		t.Fatal(err)
	}
	objects := []journalObject{createdJournalObject(2), createdJournalObject(3)}
	if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: objects}); err != nil {
		t.Fatal(err)
	}
	objects[0].ID[0] = 99
	if j.records[3].CreatedObjects[0] != createdJournalObject(2) || len(j.state.created) != 2 {
		t.Fatal("created ownership was not frozen")
	}
	if _, err := j.append(journalRecord{Kind: "workspace-released"}); err == nil {
		t.Fatal("observation bypassed process reaping")
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
	if err != nil || len(j.state.created) != 2 || !j.state.created[createdJournalObject(2)] || !j.state.executions[execution] {
		t.Fatal("created ownership replay", err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{createdJournalObject(4)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "execution-reaped", ExecutionSequence: execution}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-released"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{createdJournalObject(5)}}); err == nil {
		t.Fatal("observation accepted after release")
	}
	if _, err := j.append(journalRecord{Kind: "complete"}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{createdJournalObject(6)}}); err == nil {
		t.Fatal("observation accepted after completion")
	}
}

func TestGrantJournalCreatedRejectsInvalidBatch(t *testing.T) {
	for _, name := range []string{"before-workspace", "empty", "zero-id", "foreign-volume", "initial-id", "initial-type", "duplicate", "duplicate-type", "prior-id", "prior-type", "profile", "workspace", "grant", "grant-sequence", "execution", "execution-sequence", "unrelated-kind"} {
		t.Run(name, func(t *testing.T) {
			j, path := createdJournalFixture(t)
			prior := createdJournalObject(2)
			if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{prior}}); err != nil {
				t.Fatal(err)
			}
			r := journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{createdJournalObject(3), createdJournalObject(4)}}
			switch name {
			case "before-workspace":
				j, path = journalFixture(t)
				if _, err := j.append(journalProfile()); err != nil {
					t.Fatal(err)
				}
			case "empty":
				r.CreatedObjects = []journalObject{}
			case "zero-id":
				r.CreatedObjects[1].ID = [16]byte{}
			case "foreign-volume":
				r.CreatedObjects[1].Volume++
			case "initial-id", "initial-type":
				r.CreatedObjects[1] = j.records[1].Workspace.Root
				if name == "initial-type" {
					r.CreatedObjects[1].Directory = false
				}
			case "duplicate", "duplicate-type":
				r.CreatedObjects[1] = r.CreatedObjects[0]
				if name == "duplicate-type" {
					r.CreatedObjects[1].Directory = !r.CreatedObjects[0].Directory
				}
			case "prior-id", "prior-type":
				r.CreatedObjects[1] = prior
				if name == "prior-type" {
					r.CreatedObjects[1].Directory = !prior.Directory
				}
			case "profile":
				r.Profile = journalProfile().Profile
			case "workspace":
				r.Workspace = j.records[1].Workspace
			case "grant":
				r.Grant = journalTestGrant().Grant
			case "grant-sequence":
				r.GrantSequence = 1
			case "execution":
				r.ExecutionID = journalExecution().ExecutionID
			case "execution-sequence":
				r.ExecutionSequence = 1
			case "unrelated-kind":
				r.Kind = "complete"
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			count, created := len(j.records), len(j.state.created)
			if _, err := j.append(r); err == nil {
				t.Fatal("invalid created-object batch accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || len(j.records) != count || len(j.state.created) != created || j.state.created[createdJournalObject(3)] {
				t.Fatal("rejected batch changed ownership", err)
			}
		})
	}
}

func TestGrantJournalCreatedBoundAndFailedWrite(t *testing.T) {
	t.Run("total-limit", func(t *testing.T) {
		j, _ := createdJournalFixture(t)
		objects := make([]journalObject, 9999)
		for i := range objects {
			objects[i].Volume = 17
			binary.LittleEndian.PutUint64(objects[i].ID[:], uint64(i+2))
		}
		if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: objects}); err != nil {
			t.Fatal(err)
		}
		extra := journalObject{Volume: 17}
		binary.LittleEndian.PutUint64(extra.ID[:], 10001)
		if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{extra}}); err == nil || len(j.state.created) != 9999 {
			t.Fatal("created-object total exceeded or lost", err)
		}
	})
	t.Run("failed-write", func(t *testing.T) {
		j, _ := createdJournalFixture(t)
		if err := j.file.Close(); err != nil {
			t.Fatal(err)
		}
		r := journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{createdJournalObject(2)}}
		if _, err := j.append(r); err == nil || j.blocked == nil || len(j.state.created) != 0 || len(j.records) != 2 {
			t.Fatal("failed write advanced created ownership", err)
		}
		if _, err := j.append(r); err == nil {
			t.Fatal("failed created-object append retried")
		}
	})
}
