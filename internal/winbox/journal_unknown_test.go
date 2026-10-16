package winbox

import (
	"bytes"
	"os"
	"reflect"
	"testing"
)

func unknownJournalFixture(t *testing.T) (*grantJournal, string) {
	t.Helper()
	j, path := trackingJournalFixture(t)
	if _, err := j.append(journalRecord{Kind: "workspace-tracking", ChangeCursor: &workspaceChangeCursor{17, 100}}); err != nil {
		t.Fatal(err)
	}
	return j, path
}

func TestGrantJournalUnknownImportReplay(t *testing.T) {
	j, path := unknownJournalFixture(t)
	execution, err := j.append(journalExecution())
	if err != nil {
		t.Fatal(err)
	}
	objects := []journalObject{createdJournalObject(2), createdJournalObject(3)}
	if _, err := j.append(journalRecord{Kind: "workspace-unknown-import", UnknownImports: objects}); err != nil {
		t.Fatal(err)
	}
	objects[0].ID[0] = 99
	last := j.records[len(j.records)-1]
	if last.UnknownImports[0] != createdJournalObject(2) || len(j.state.unknown) != 2 || len(j.state.created) != 2 {
		t.Fatal("unknown-import ownership was not frozen")
	}
	// Deletion, intent and later creation use the same ownership as other
	// recovered identities.
	if _, err := j.append(journalRecord{Kind: "workspace-delete-intent", DeleteIntents: []journalDeletion{{Object: createdJournalObject(3), USN: 120}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{createdJournalObject(4)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-released"}); err == nil {
		t.Fatal("unknown import bypassed process reaping")
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
	if err != nil || len(j.state.unknown) != 2 || len(j.state.created) != 3 || j.state.unknown[createdJournalObject(4)] || !j.state.executions[execution] {
		t.Fatal("unknown-import replay", err)
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
	if _, err := j.append(journalRecord{Kind: "workspace-unknown-import", UnknownImports: []journalObject{createdJournalObject(6)}}); err == nil {
		t.Fatal("unknown import accepted after release")
	}
}

func TestGrantJournalUnknownImportRejectsInvalidBatch(t *testing.T) {
	for _, name := range []string{"untracked", "empty", "zero-id", "foreign-volume", "initial-id", "duplicate", "duplicate-type", "prior-created", "prior-unknown", "limit", "wrong-kind", "created-fields", "import-fields", "profile", "workspace", "grant", "grant-sequence", "execution", "execution-sequence"} {
		t.Run(name, func(t *testing.T) {
			j, path := unknownJournalFixture(t)
			if _, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{createdJournalObject(2)}}); err != nil {
				t.Fatal(err)
			}
			if _, err := j.append(journalRecord{Kind: "workspace-unknown-import", UnknownImports: []journalObject{createdJournalObject(4)}}); err != nil {
				t.Fatal(err)
			}
			r := journalRecord{Kind: "workspace-unknown-import", UnknownImports: []journalObject{createdJournalObject(6)}}
			switch name {
			case "untracked":
				j.state.changeCursor = nil
			case "empty":
				r.UnknownImports = nil
			case "zero-id":
				r.UnknownImports = []journalObject{{Volume: 17}}
			case "foreign-volume":
				r.UnknownImports[0].Volume = 18
			case "initial-id":
				r.UnknownImports = []journalObject{j.state.root}
			case "duplicate":
				r.UnknownImports = append(r.UnknownImports, r.UnknownImports[0])
			case "duplicate-type":
				r.UnknownImports = append(r.UnknownImports, journalObject{Volume: 17, ID: r.UnknownImports[0].ID, Directory: !r.UnknownImports[0].Directory})
			case "prior-created":
				r.UnknownImports = []journalObject{createdJournalObject(2)}
			case "prior-unknown":
				r.UnknownImports = []journalObject{createdJournalObject(4)}
			case "limit":
				r.UnknownImports = nil
				for id := 10; len(r.UnknownImports) < 10000; id++ {
					var object journalObject
					object.Volume, object.ID = 17, [16]byte{byte(id), byte(id >> 8), 1}
					r.UnknownImports = append(r.UnknownImports, object)
				}
			case "created-fields":
				r.CreatedObjects = r.UnknownImports
			case "import-fields":
				r.Import = &journalImport{Root: r.UnknownImports[0], Objects: r.UnknownImports, Cursor: *j.state.changeCursor}
			case "profile":
				r.Profile = journalProfile().Profile
			case "workspace":
				r.Workspace = j.records[1].Workspace
			case "grant":
				r.Grant = journalTestGrant().Grant
			case "grant-sequence":
				r.GrantSequence = 3
			case "execution":
				r.ExecutionID = journalExecution().ExecutionID
			case "execution-sequence":
				r.ExecutionSequence = 3
			}
			if name == "wrong-kind" {
				// Kind-bound fields: unknown identities on any other record kind.
				r = journalRecord{Kind: "workspace-created", CreatedObjects: []journalObject{createdJournalObject(6)}, UnknownImports: []journalObject{createdJournalObject(8)}}
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			count := len(j.records)
			created, unknown := map[journalObject]bool{}, map[journalObject]bool{}
			for object := range j.state.created {
				created[object] = true
			}
			for object := range j.state.unknown {
				unknown[object] = true
			}
			if _, err := j.append(r); err == nil {
				t.Fatal("invalid unknown import accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(before, after) || len(j.records) != count || !reflect.DeepEqual(created, j.state.created) || !reflect.DeepEqual(unknown, j.state.unknown) {
				t.Fatal("invalid unknown import changed durable ownership", err)
			}
		})
	}
}

func TestGrantJournalUnknownImportWriteFailure(t *testing.T) {
	j, _ := unknownJournalFixture(t)
	if err := j.file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := j.append(journalRecord{Kind: "workspace-unknown-import", UnknownImports: []journalObject{createdJournalObject(2)}}); err == nil || j.blocked == nil || len(j.state.unknown) != 0 || len(j.state.created) != 0 {
		t.Fatal("failed unknown-import write changed ownership", err)
	}
}
