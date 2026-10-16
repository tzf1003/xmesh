package winbox

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

func deletionJournalFixture(t *testing.T) (*grantJournal, string, journalObject) {
	j, path := trackingJournalFixture(t)
	if _, err := j.append(journalRecord{Kind: "workspace-tracking", ChangeCursor: &workspaceChangeCursor{17, 100}}); err != nil {
		t.Fatal(err)
	}
	object := createdJournalObject(2)
	if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{object}}); err != nil {
		t.Fatal(err)
	}
	return j, path, object
}

func TestGrantJournalDeletionReplay(t *testing.T) {
	j, path, object := deletionJournalFixture(t)
	execution, err := j.append(journalExecution())
	if err != nil {
		t.Fatal(err)
	}
	batch := []journalDeletion{{Object: object, USN: 150}}
	if _, err := j.append(journalRecord{Kind: "workspace-deleted", DeletedObjects: batch}); err != nil {
		t.Fatal(err)
	}
	batch[0].USN = 999
	if j.state.deleted[object] != 150 || j.records[6].DeletedObjects[0].USN != 150 {
		t.Fatal("caller changed durable deletion ownership")
	}
	if _, err := j.append(journalRecord{Kind: "workspace-tracked", ChangeCursor: &workspaceChangeCursor{17, 151}}); err != nil {
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
	if err != nil || j.state.deleted[object] != 150 || j.state.changeCursor.Next != 151 || !j.state.executions[execution] {
		t.Fatal("deletion receipt replay", err)
	}
	if _, err := j.append(journalRecord{Kind: "released", GrantSequence: 3}); err == nil {
		t.Fatal("deletion bypassed process exit confirmation")
	}
}

func TestGrantJournalDeletionRejectsInvalidTransitions(t *testing.T) {
	for _, name := range []string{"empty", "untracked", "unknown", "type", "volume", "root", "old-usn", "duplicate", "repeated", "released", "cursor", "created", "grant", "grant-sequence", "profile", "workspace", "execution", "execution-sequence", "unrelated", "cursor-not-covering"} {
		t.Run(name, func(t *testing.T) {
			j, path, object := deletionJournalFixture(t)
			r := journalRecord{Kind: "workspace-deleted", DeletedObjects: []journalDeletion{{Object: object, USN: 150}}}
			switch name {
			case "empty":
				r.DeletedObjects = nil
			case "untracked":
				j.state.changeCursor = nil
			case "unknown":
				r.DeletedObjects[0].Object.ID[0] = 77
			case "type":
				r.DeletedObjects[0].Object.Directory = !object.Directory
			case "volume":
				r.DeletedObjects[0].Object.Volume++
			case "root":
				r.DeletedObjects[0].Object = j.records[1].Workspace.Root
			case "old-usn":
				r.DeletedObjects[0].USN = 99
			case "duplicate":
				r.DeletedObjects = append(r.DeletedObjects, r.DeletedObjects[0])
			case "repeated", "cursor-not-covering":
				if _, err := j.append(r); err != nil {
					t.Fatal(err)
				}
				if name == "cursor-not-covering" {
					r = journalRecord{Kind: "workspace-tracked", ChangeCursor: &workspaceChangeCursor{17, 150}}
				}
			case "released":
				if _, err := j.append(journalRecord{Kind: "released", GrantSequence: 3}); err != nil {
					t.Fatal(err)
				}
				if _, err := j.append(journalRecord{Kind: "workspace-released"}); err != nil {
					t.Fatal(err)
				}
			case "cursor":
				r.ChangeCursor = &workspaceChangeCursor{17, 200}
			case "created":
				r.CreatedObjects = []journalObject{createdJournalObject(3)}
			case "grant":
				r.Grant = journalTestGrant().Grant
			case "grant-sequence":
				r.GrantSequence = 3
			case "profile":
				r.Profile = journalProfile().Profile
			case "workspace":
				r.Workspace = j.records[1].Workspace
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
			deletions := make(map[journalObject]int64)
			for object, usn := range j.state.deleted {
				deletions[object] = usn
			}
			if _, err := j.append(r); err == nil {
				t.Fatal("invalid deletion receipt accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || count != len(j.records) || !reflect.DeepEqual(deletions, j.state.deleted) {
				t.Fatal("rejected receipt changed durable ownership", err)
			}
		})
	}
}

func TestGrantJournalDeletionWriteFailure(t *testing.T) {
	j, _, object := deletionJournalFixture(t)
	if err := j.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-deleted", DeletedObjects: []journalDeletion{{Object: object, USN: 150}}}); err == nil || j.blocked == nil || len(j.state.deleted) != 0 || j.state.changeCursor.Next != 100 {
		t.Fatal("failed deletion write changed ownership", err)
	}
}
