package winbox

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

func TestGrantJournalDeleteIntentReplay(t *testing.T) {
	for _, prefix := range []string{"intent", "cursor", "deleted", "final-cursor"} {
		t.Run(prefix, func(t *testing.T) {
			j, path, object := deletionJournalFixture(t)
			batch := []journalDeletion{{Object: object, USN: 150}}
			if _, err := j.append(journalRecord{Kind: "workspace-delete-intent", DeleteIntents: batch}); err != nil {
				t.Fatal(err)
			}
			batch[0].USN = 999
			batch[0].Object.ID[0] = 99
			if j.state.deleteIntents[object] != 150 || j.records[len(j.records)-1].DeleteIntents[0].USN != 150 {
				t.Fatal("caller changed intent ownership")
			}
			if prefix != "intent" {
				if _, err := j.append(journalRecord{Kind: "workspace-tracked", ChangeCursor: &workspaceChangeCursor{17, 151}}); err != nil {
					t.Fatal(err)
				}
			}
			if prefix == "deleted" || prefix == "final-cursor" {
				if _, err := j.append(journalRecord{Kind: "workspace-deleted", DeletedObjects: []journalDeletion{{Object: object, USN: 200}}}); err != nil {
					t.Fatal(err)
				}
			}
			if prefix == "final-cursor" {
				if _, err := j.append(journalRecord{Kind: "workspace-tracked", ChangeCursor: &workspaceChangeCursor{17, 201}}); err != nil {
					t.Fatal(err)
				}
			}
			cursor := *j.state.changeCursor
			deleted := j.state.deleted[object]
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			j, err = loadGrantJournal(f)
			if err != nil || j.state.deleteIntents[object] != 150 || j.state.deleted[object] != deleted || *j.state.changeCursor != cursor || !j.state.created[object] {
				t.Fatal("delete-intent prefix replay", err)
			}
		})
	}
}

func TestGrantJournalDeleteIntentRejectsInvalidTransitions(t *testing.T) {
	for _, name := range []string{"empty", "untracked", "unknown", "type", "volume", "root", "old-usn", "duplicate", "repeated", "deleted", "released", "cursor", "created", "deleted-fields", "grant", "grant-sequence", "profile", "workspace", "execution", "execution-sequence", "unrelated", "cursor-not-covering", "receipt-before-intent"} {
		t.Run(name, func(t *testing.T) {
			j, path, object := deletionJournalFixture(t)
			r := journalRecord{Kind: "workspace-delete-intent", DeleteIntents: []journalDeletion{{Object: object, USN: 150}}}
			switch name {
			case "empty":
				r.DeleteIntents = nil
			case "untracked":
				j.state.changeCursor = nil
			case "unknown":
				r.DeleteIntents[0].Object.ID[0] = 77
			case "type":
				r.DeleteIntents[0].Object.Directory = !object.Directory
			case "volume":
				r.DeleteIntents[0].Object.Volume++
			case "root":
				r.DeleteIntents[0].Object = j.records[1].Workspace.Root
			case "old-usn":
				r.DeleteIntents[0].USN = 99
			case "duplicate":
				r.DeleteIntents = append(r.DeleteIntents, r.DeleteIntents[0])
			case "repeated", "cursor-not-covering", "receipt-before-intent":
				if _, err := j.append(r); err != nil {
					t.Fatal(err)
				}
				if name == "cursor-not-covering" {
					r = journalRecord{Kind: "workspace-tracked", ChangeCursor: &workspaceChangeCursor{17, 150}}
				}
				if name == "receipt-before-intent" {
					r = journalRecord{Kind: "workspace-deleted", DeletedObjects: []journalDeletion{{Object: object, USN: 149}}}
				}
			case "deleted":
				if _, err := j.append(journalRecord{Kind: "workspace-deleted", DeletedObjects: []journalDeletion{{Object: object, USN: 150}}}); err != nil {
					t.Fatal(err)
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
			case "deleted-fields":
				r.DeletedObjects = []journalDeletion{{Object: object, USN: 150}}
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
			intents := make(map[journalObject]int64)
			for object, usn := range j.state.deleteIntents {
				intents[object] = usn
			}
			if _, err := j.append(r); err == nil {
				t.Fatal("invalid deletion intent accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || count != len(j.records) || !reflect.DeepEqual(intents, j.state.deleteIntents) {
				t.Fatal("rejected intent changed durable ownership", err)
			}
		})
	}
}

func TestGrantJournalDeleteIntentWriteFailure(t *testing.T) {
	j, _, object := deletionJournalFixture(t)
	if err := j.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-delete-intent", DeleteIntents: []journalDeletion{{Object: object, USN: 150}}}); err == nil || j.blocked == nil || len(j.state.deleteIntents) != 0 || j.state.changeCursor.Next != 100 {
		t.Fatal("failed intent write changed ownership", err)
	}
}
