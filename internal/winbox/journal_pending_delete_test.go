package winbox

import (
	"bytes"
	"os"
	"testing"
)

func pendingDeleteFixture(t *testing.T) (*grantJournal, string, journalObject) {
	t.Helper()
	j, path := journalFixture(t)
	root := journalObject{Volume: 17, ID: [16]byte{1}, Directory: true}
	object := createdJournalObject(3)
	if _, err := j.append(journalProfile()); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace", Workspace: &journalWorkspace{Path: `C:\workspace`, Root: root, Objects: []journalObject{root, object}}}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []journalObject{root, object} {
		grant := journalTestGrant()
		grant.Grant.Object = item
		if _, err := j.append(grant); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.append(journalRecord{Kind: "workspace-tracking", ChangeCursor: &workspaceChangeCursor{17, 100}}); err != nil {
		t.Fatal(err)
	}
	return j, path, object
}

func TestGrantJournalPendingDeleteReplay(t *testing.T) {
	for _, prefix := range []string{"pending", "cursor", "deleted", "released"} {
		t.Run(prefix, func(t *testing.T) {
			j, path, object := pendingDeleteFixture(t)
			batch := []journalObject{object}
			if _, err := j.append(journalRecord{Kind: "workspace-delete-pending", PendingDeletes: batch}); err != nil {
				t.Fatal(err)
			}
			batch[0].ID[0] ^= 1
			if !j.state.deletePending[object] || j.records[len(j.records)-1].PendingDeletes[0] != object {
				t.Fatal("caller changed kernel observation ownership")
			}
			if prefix != "pending" {
				if _, err := j.append(journalRecord{Kind: "workspace-tracked", ChangeCursor: &workspaceChangeCursor{17, 151}}); err != nil {
					t.Fatal(err)
				}
			}
			if prefix == "deleted" {
				if _, err := j.append(journalRecord{Kind: "workspace-deleted", DeletedObjects: []journalDeletion{{Object: object, USN: 200}}}); err != nil {
					t.Fatal(err)
				}
			}
			if prefix == "released" {
				if _, err := j.append(journalRecord{Kind: "released", GrantSequence: 3}); err != nil {
					t.Fatal(err)
				}
				if _, err := j.append(journalRecord{Kind: "released", GrantSequence: 4}); err != nil {
					t.Fatal(err)
				}
				if _, err := j.append(journalRecord{Kind: "workspace-released"}); err != nil {
					t.Fatal(err)
				}
			}
			cursor, deleted := *j.state.changeCursor, j.state.deleted[object]
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			j, err = loadGrantJournal(f)
			if err != nil || !j.state.deletePending[object] || j.state.deleted[object] != deleted || *j.state.changeCursor != cursor || j.state.workspaceReleased != (prefix == "released") {
				t.Fatal("kernel observation prefix replay", err)
			}
		})
	}
}

func TestGrantJournalPendingDeleteInvalidTransitions(t *testing.T) {
	for _, name := range []string{"empty", "untracked", "unknown", "created", "type", "volume", "root", "duplicate", "repeated", "deleted", "released", "cursor", "created-fields", "intent-fields", "deleted-fields", "grant", "grant-sequence", "profile", "workspace", "execution", "execution-sequence", "unrelated"} {
		t.Run(name, func(t *testing.T) {
			j, path, object := pendingDeleteFixture(t)
			r := journalRecord{Kind: "workspace-delete-pending", PendingDeletes: []journalObject{object}}
			switch name {
			case "empty":
				r.PendingDeletes = nil
			case "untracked":
				j.state.changeCursor = nil
			case "unknown":
				r.PendingDeletes[0].ID[0] = 77
			case "created":
				created := createdJournalObject(77)
				if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{created}}); err != nil {
					t.Fatal(err)
				}
				r.PendingDeletes[0] = created
			case "type":
				r.PendingDeletes[0].Directory = !object.Directory
			case "volume":
				r.PendingDeletes[0].Volume++
			case "root":
				r.PendingDeletes[0] = j.state.root
			case "duplicate":
				r.PendingDeletes = append(r.PendingDeletes, object)
			case "repeated":
				if _, err := j.append(r); err != nil {
					t.Fatal(err)
				}
			case "deleted":
				if _, err := j.append(journalRecord{Kind: "workspace-deleted", DeletedObjects: []journalDeletion{{Object: object, USN: 150}}}); err != nil {
					t.Fatal(err)
				}
			case "released":
				if _, err := j.append(journalRecord{Kind: "released", GrantSequence: 3}); err != nil {
					t.Fatal(err)
				}
				if _, err := j.append(journalRecord{Kind: "released", GrantSequence: 4}); err != nil {
					t.Fatal(err)
				}
				if _, err := j.append(journalRecord{Kind: "workspace-released"}); err != nil {
					t.Fatal(err)
				}
			case "cursor":
				r.ChangeCursor = &workspaceChangeCursor{17, 200}
			case "created-fields":
				r.CreatedObjects = []journalObject{createdJournalObject(77)}
			case "intent-fields":
				r.DeleteIntents = []journalDeletion{{Object: object, USN: 150}}
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
			count, pending := len(j.records), len(j.state.deletePending)
			if _, err := j.append(r); err == nil {
				t.Fatal("invalid kernel observation accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || len(j.records) != count || len(j.state.deletePending) != pending {
				t.Fatal("rejected kernel observation changed ownership", err)
			}
		})
	}
}

func TestGrantJournalPendingDeleteWriteFailure(t *testing.T) {
	j, _, object := pendingDeleteFixture(t)
	if err := j.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-delete-pending", PendingDeletes: []journalObject{object}}); err == nil || j.blocked == nil || len(j.state.deletePending) != 0 || j.state.changeCursor.Next != 100 {
		t.Fatal("failed kernel observation write changed ownership", err)
	}
}
