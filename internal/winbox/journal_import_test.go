package winbox

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

func importJournalFixture(t *testing.T) (*grantJournal, string, journalImport) {
	t.Helper()
	j, path := trackingJournalFixture(t)
	cursor := workspaceChangeCursor{17, 100}
	if _, err := j.append(journalRecord{Kind: "workspace-tracking", ChangeCursor: &cursor}); err != nil {
		t.Fatal(err)
	}
	directory, file := createdJournalObject(2), createdJournalObject(3)
	return j, path, journalImport{Root: directory, Objects: []journalObject{directory, file}, Cursor: cursor}
}

func TestGrantJournalImportReplay(t *testing.T) {
	j, path, proof := importJournalFixture(t)
	original := append([]journalObject(nil), proof.Objects...)
	if _, err := j.append(journalRecord{Kind: "workspace-import", Import: &proof}); err != nil {
		t.Fatal(err)
	}
	proof.Objects[0].ID[0] = 77
	proof.Cursor.Next++
	if j.records[len(j.records)-1].Import.Root != original[0] || !j.state.created[original[1]] || j.records[len(j.records)-1].Import.Cursor.Next != 100 {
		t.Fatal("caller changed import ownership")
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
	if err != nil || len(j.state.created) != 2 || j.state.changeCursor.Next != 100 {
		t.Fatal("import replay", err)
	}
	known := map[[16]byte]journalObject{j.state.root.ID: j.state.root}
	for object := range j.state.created {
		known[object.ID] = object
	}
	// A pre-admitted subtree can enter and leave the workspace before a
	// checkpoint; its original children and later children remain tracked.
	plan, err := planWorkspaceChanges([]workspaceChange{
		{ID: original[0].ID, Parent: j.state.root.ID, USN: 110, Reason: usnReasonRenameNew, Attributes: usnDirectory},
		{ID: original[1].ID, Parent: [16]byte{99}, USN: 120, Reason: usnReasonRenameNew},
		{ID: original[0].ID, Parent: [16]byte{99}, USN: 130, Reason: usnReasonRenameNew, Attributes: usnDirectory},
		{ID: [16]byte{4}, Parent: original[0].ID, USN: 140, Reason: usnReasonCreate, Attributes: usnDirectory},
	}, 17, j.state.root.ID, known, nil, nil, 4)
	if err != nil || len(plan.Created) != 1 || plan.Created[0] != createdJournalObject(4) {
		t.Fatal("import descendant history", plan, err)
	}
}

func TestGrantJournalImportOverlappingCensus(t *testing.T) {
	j, _, proof := importJournalFixture(t)
	if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: proof.Objects[1:]}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-import", Import: &proof}); err != nil || len(j.state.created) != 2 {
		t.Fatal("overlapping import census", err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-import", Import: &proof}); err == nil {
		t.Fatal("repeated import frame accepted")
	}
}

func TestGrantJournalImportInvalidTransitions(t *testing.T) {
	for _, name := range []string{"missing", "untracked", "empty", "root-order", "file-children", "workspace-root", "zero-id", "volume", "duplicate", "type-conflict", "old-cursor", "future-cursor", "journal", "deleted", "released", "created-fields", "pending-fields", "deleted-fields", "intent-fields", "cursor-fields", "grant", "grant-sequence", "profile", "workspace", "execution", "execution-sequence", "unrelated"} {
		t.Run(name, func(t *testing.T) {
			j, path, proof := importJournalFixture(t)
			r := journalRecord{Kind: "workspace-import", Import: &proof}
			switch name {
			case "missing":
				r.Import = nil
			case "untracked":
				j.state.changeCursor = nil
			case "empty":
				proof.Objects = nil
			case "root-order":
				proof.Root = proof.Objects[1]
			case "file-children":
				proof.Root.Directory = false
				proof.Objects[0] = proof.Root
			case "workspace-root":
				proof.Root = j.state.root
				proof.Objects[0] = proof.Root
			case "zero-id":
				proof.Objects[1].ID = [16]byte{}
			case "volume":
				proof.Objects[1].Volume++
			case "duplicate":
				proof.Objects[1] = proof.Objects[0]
			case "type-conflict":
				if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: proof.Objects[1:]}); err != nil {
					t.Fatal(err)
				}
				proof.Objects[1].Directory = !proof.Objects[1].Directory
			case "old-cursor":
				proof.Cursor.Next--
			case "future-cursor":
				proof.Cursor.Next++
			case "journal":
				proof.Cursor.JournalID++
			case "deleted":
				if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: proof.Objects[1:]}); err != nil {
					t.Fatal(err)
				}
				if _, err := j.append(journalRecord{Kind: "workspace-deleted", DeletedObjects: []journalDeletion{{Object: proof.Objects[1], USN: 110}}}); err != nil {
					t.Fatal(err)
				}
			case "released":
				if _, err := j.append(journalRecord{Kind: "released", GrantSequence: 3}); err != nil {
					t.Fatal(err)
				}
				if _, err := j.append(journalRecord{Kind: "workspace-released"}); err != nil {
					t.Fatal(err)
				}
			case "created-fields":
				r.CreatedObjects = proof.Objects
			case "pending-fields":
				r.PendingDeletes = proof.Objects
			case "deleted-fields":
				r.DeletedObjects = []journalDeletion{{Object: proof.Objects[1], USN: 110}}
			case "intent-fields":
				r.DeleteIntents = []journalDeletion{{Object: proof.Objects[1], USN: 110}}
			case "cursor-fields":
				r.ChangeCursor = &workspaceChangeCursor{17, 100}
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
			owned := make(map[journalObject]bool)
			for object := range j.state.created {
				owned[object] = true
			}
			if _, err := j.append(r); err == nil {
				t.Fatal("invalid import accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || len(j.records) != count || !reflect.DeepEqual(owned, j.state.created) {
				t.Fatal("invalid import changed durable ownership", err)
			}
		})
	}
}

func TestWorkspaceImportCumulativeLimit(t *testing.T) {
	root := journalObject{Volume: 17, ID: [16]byte{1}, Directory: true}
	known := map[journalObject]bool{root: true}
	proof := journalImport{Root: createdJournalObject(2), Objects: []journalObject{createdJournalObject(2), createdJournalObject(3)}, Cursor: workspaceChangeCursor{17, 100}}
	if _, err := planWorkspaceImport(proof, root, known, nil, 2); err == nil || len(known) != 1 {
		t.Fatal("import exceeded cumulative bound")
	}
	if added, err := planWorkspaceImport(proof, root, known, nil, 3); err != nil || len(added) != 2 {
		t.Fatal("exact import limit", err)
	}
	known[proof.Objects[1]] = true
	if added, err := planWorkspaceImport(proof, root, known, nil, 3); err != nil || len(added) != 1 {
		t.Fatal("overlap counted twice", err)
	}
	file := journalImport{Root: createdJournalObject(5), Objects: []journalObject{createdJournalObject(5)}, Cursor: proof.Cursor}
	if added, err := planWorkspaceImport(file, root, known, nil, 3); err != nil || len(added) != 1 {
		t.Fatal("singleton import", err)
	}
}

func TestGrantJournalImportWriteFailure(t *testing.T) {
	j, _, proof := importJournalFixture(t)
	if err := j.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-import", Import: &proof}); err == nil || j.blocked == nil || len(j.state.created) != 0 || j.state.changeCursor.Next != 100 {
		t.Fatal("failed import write changed ownership", err)
	}
}
