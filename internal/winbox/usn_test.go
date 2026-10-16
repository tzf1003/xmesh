package winbox

import (
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func testChangeFrame(version uint16, usn int64, id, parent byte, reason, attributes uint32) []byte {
	header, position, width := 60, 24, 8
	if version == 3 {
		header, position, width = 76, 40, 16
	}
	size := (header + 2 + 7) &^ 7
	b := make([]byte, size)
	binary.LittleEndian.PutUint32(b, uint32(size))
	binary.LittleEndian.PutUint16(b[4:], version)
	b[8], b[8+width] = id, parent
	binary.LittleEndian.PutUint64(b[position:], uint64(usn))
	binary.LittleEndian.PutUint32(b[position+16:], reason)
	binary.LittleEndian.PutUint32(b[position+28:], attributes)
	binary.LittleEndian.PutUint16(b[position+32:], 2)
	binary.LittleEndian.PutUint16(b[position+34:], uint16(header))
	binary.LittleEndian.PutUint16(b[header:], 'x')
	return b
}

func testChangeBuffer(next int64, records ...[]byte) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(next))
	for _, record := range records {
		b = append(b, record...)
	}
	return b
}

func TestWorkspaceChangeParsing(t *testing.T) {
	records := [][]byte{testChangeFrame(2, 10, 2, 1, usnReasonCreate, usnDirectory), testChangeFrame(3, 20, 3, 2, usnReasonCreate, 0)}
	// V3 high ID bytes must survive; V2 references are zero-extended.
	records[1][23] = 77
	records[1][39] = 88
	next, changes, err := parseWorkspaceChanges(testChangeBuffer(32, records...), 8)
	if err != nil || next != 32 || len(changes) != 2 || changes[0].ID != [16]byte{2} || changes[0].Parent != [16]byte{1} || changes[0].USN != 10 || changes[0].Attributes != usnDirectory || changes[1].ID[15] != 77 || changes[1].Parent[15] != 88 {
		t.Fatal("change decoding", next, changes, err)
	}
	if next, changes, err := parseWorkspaceChanges(testChangeBuffer(32), 8); err != nil || next != 32 || len(changes) != 0 {
		t.Fatal("empty cursor advance", err)
	}
}

func TestWorkspaceChangeRejectsMalformedBuffers(t *testing.T) {
	for _, name := range []string{"short", "negative-start", "backwards-next", "negative-next", "short-header", "oversize", "unaligned", "small", "unsupported", "truncated-v3", "zero-id", "zero-parent", "old-usn", "future-usn", "negative-usn", "repeat-usn", "reverse-usn", "zero-name", "odd-name", "name-before-header", "odd-offset", "oversize-name", "oversize-offset", "trailing"} {
		t.Run(name, func(t *testing.T) {
			start := int64(8)
			record := testChangeFrame(2, 10, 2, 1, usnReasonCreate, 0)
			b := testChangeBuffer(32, record)
			put32 := func(offset int, value uint32) { binary.LittleEndian.PutUint32(b[8+offset:], value) }
			put16 := func(offset int, value uint16) { binary.LittleEndian.PutUint16(b[8+offset:], value) }
			switch name {
			case "short":
				b = b[:7]
			case "negative-start":
				start = -1
			case "backwards-next":
				binary.LittleEndian.PutUint64(b, 7)
			case "negative-next":
				binary.LittleEndian.PutUint64(b, ^uint64(0))
			case "short-header":
				b = b[:12]
			case "oversize":
				put32(0, 4096)
			case "unaligned":
				put32(0, 63)
			case "small":
				put32(0, 8)
			case "unsupported":
				put16(4, 4)
			case "truncated-v3":
				put16(4, 3)
			case "zero-id":
				b[16] = 0
			case "zero-parent":
				b[24] = 0
			case "old-usn":
				binary.LittleEndian.PutUint64(b[32:], 7)
			case "future-usn":
				binary.LittleEndian.PutUint64(b[32:], 32)
			case "negative-usn":
				binary.LittleEndian.PutUint64(b[32:], ^uint64(0))
			case "repeat-usn":
				b = append(b, record...)
			case "reverse-usn":
				b = append(b, testChangeFrame(2, 9, 3, 1, usnReasonCreate, 0)...)
			case "zero-name":
				put16(56, 0)
			case "odd-name":
				put16(56, 1)
			case "name-before-header":
				put16(58, 58)
			case "odd-offset":
				put16(58, 61)
			case "oversize-name":
				put16(56, 512)
			case "oversize-offset":
				put16(58, 512)
			case "trailing":
				b = append(b, 0)
			}
			if _, _, err := parseWorkspaceChanges(b, start); err == nil {
				t.Fatal("malformed change buffer accepted")
			}
		})
	}
}

func TestWorkspaceChangeHistoryContinuity(t *testing.T) {
	good := workspaceChangeRange{JournalID: 17, First: 10, Next: 40, Lowest: 20}
	for _, position := range []int64{20, 30, 40} {
		if err := good.contains(workspaceChangeCursor{17, position}); err != nil {
			t.Fatal(err)
		}
	}
	for _, span := range []workspaceChangeRange{{0, 10, 40, 20}, {18, 10, 40, 20}, {17, -1, 40, 20}, {17, 10, 40, -1}, {17, 31, 40, 20}, {17, 10, 40, 31}, {17, 10, 29, 20}, {17, 41, 40, 20}, {17, 10, 40, 41}} {
		if err := span.contains(workspaceChangeCursor{17, 30}); !errors.Is(err, errWorkspaceChangesLost) {
			t.Fatal("discontinuous history accepted", span, err)
		}
	}
}

func TestWorkspaceChangeIdentityCollection(t *testing.T) {
	root := journalObject{Volume: 17, ID: [16]byte{1}, Directory: true}
	known := map[[16]byte]journalObject{root.ID: root}
	changes := []workspaceChange{
		{ID: [16]byte{2}, Parent: root.ID, Reason: usnReasonCreate, Attributes: usnDirectory},
		{ID: [16]byte{2}, Parent: [16]byte{99}, Reason: usnReasonRenameNew, Attributes: usnDirectory},
		{ID: [16]byte{3}, Parent: [16]byte{2}, Reason: usnReasonCreate},
		{ID: [16]byte{88}, Parent: [16]byte{99}, Reason: usnReasonCreate},
		{ID: [16]byte{3}, Parent: [16]byte{2}, Reason: usnReasonCreate},
	}
	plan, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, nil, 3)
	objects := plan.Created
	if err != nil || len(objects) != 2 || objects[0].ID != [16]byte{2} || !objects[0].Directory || objects[1].ID != [16]byte{3} || len(known) != 1 {
		t.Fatal("moved-directory descendants", objects, err)
	}
	// Previously persisted discovery before a failed cursor write deduplicates.
	for _, object := range objects {
		known[object.ID] = object
	}
	if plan, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, nil, 3); err != nil || len(plan.Created) != 0 {
		t.Fatal("cursor replay deduplication", plan, err)
	}
	for _, name := range []string{"limit", "hardlink", "reparse", "type", "parent-type"} {
		t.Run(name, func(t *testing.T) {
			original := map[[16]byte]journalObject{root.ID: root}
			batch := []workspaceChange{{ID: [16]byte{2}, Parent: root.ID, Reason: usnReasonCreate, Attributes: usnDirectory}, {ID: [16]byte{3}, Parent: [16]byte{2}, Reason: usnReasonCreate}}
			limit := 3
			switch name {
			case "limit":
				limit = 2
			case "hardlink":
				batch[1].Reason |= usnReasonHardLink
			case "reparse":
				batch[1].Reason |= usnReasonReparse
			case "type":
				batch[1].ID = root.ID
			case "parent-type":
				batch[0].Attributes = 0
			}
			if _, err := planWorkspaceChanges(batch, 17, root.ID, original, nil, nil, limit); err == nil || !reflect.DeepEqual(original, map[[16]byte]journalObject{root.ID: root}) {
				t.Fatal("unsupported change discarded caller ownership", err)
			}
		})
	}
}

func TestWorkspaceChangeDeletionPlan(t *testing.T) {
	root := journalObject{Volume: 17, ID: [16]byte{1}, Directory: true}
	known := map[[16]byte]journalObject{root.ID: root}
	changes := []workspaceChange{
		{ID: [16]byte{2}, Parent: root.ID, USN: 100, Reason: usnReasonCreate, Attributes: usnDirectory},
		{ID: [16]byte{3}, Parent: [16]byte{2}, USN: 110, Reason: usnReasonCreate},
		{ID: [16]byte{3}, Parent: [16]byte{2}, USN: 120, Reason: usnReasonDelete},
		{ID: [16]byte{3}, Parent: [16]byte{2}, USN: 130, Reason: usnReasonDelete | usnReasonClose},
		{ID: [16]byte{2}, Parent: root.ID, USN: 140, Reason: usnReasonDelete | usnReasonClose, Attributes: usnDirectory},
	}
	plan, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, nil, 3)
	if err != nil || len(plan.Created) != 2 || len(plan.Deleted) != 2 || plan.Deleted[0].USN != 130 || plan.Deleted[1].USN != 140 || len(known) != 1 {
		t.Fatal("create/delete checkpoint", plan, err)
	}
	for _, object := range plan.Created {
		known[object.ID] = object
	}
	// Every durable prefix can replay an older source cursor.
	for prefix := 0; prefix <= len(plan.Deleted); prefix++ {
		retired := make(map[journalObject]int64)
		for _, deletion := range plan.Deleted[:prefix] {
			retired[deletion.Object] = deletion.USN
		}
		replay, err := planWorkspaceChanges(changes, 17, root.ID, known, retired, nil, 3)
		if err != nil || len(replay.Created) != 0 || !slices.Equal(replay.Deleted, plan.Deleted[prefix:]) {
			t.Fatal("deletion prefix replay", prefix, replay, err)
		}
	}
	for _, name := range []string{"root", "after-delete", "child-after-parent", "conflicting-close", "hardlink-delete", "reparse-delete"} {
		t.Run(name, func(t *testing.T) {
			batch := append([]workspaceChange(nil), changes...)
			switch name {
			case "root":
				batch[4].ID = root.ID
			case "after-delete":
				batch = append(batch, workspaceChange{ID: [16]byte{3}, Parent: root.ID, USN: 150, Reason: 1})
			case "child-after-parent":
				batch = append(batch, workspaceChange{ID: [16]byte{4}, Parent: [16]byte{2}, USN: 150, Reason: usnReasonCreate})
			case "conflicting-close":
				batch = append(batch, workspaceChange{ID: [16]byte{3}, Parent: root.ID, USN: 150, Reason: usnReasonDelete | usnReasonClose})
			case "hardlink-delete":
				batch[3].Reason |= usnReasonHardLink
			case "reparse-delete":
				batch[3].Reason |= usnReasonReparse
			}
			if _, err := planWorkspaceChanges(batch, 17, root.ID, known, nil, nil, 4); err == nil {
				t.Fatal("unproved deletion accepted")
			}
		})
	}
}

func TestNTFSDeletionRecordProof(t *testing.T) {
	buffer := make([]byte, 12+1024)
	binary.LittleEndian.PutUint64(buffer, 77)
	binary.LittleEndian.PutUint32(buffer[8:], 1024)
	copy(buffer[12:], "FILE")
	binary.LittleEndian.PutUint16(buffer[28:], 3)
	binary.LittleEndian.PutUint16(buffer[34:], 1)
	for _, test := range []struct {
		name string
		ref  uint64
		gone bool
	}{
		{"live", 3<<48 | 77, false},
		{"reused", 2<<48 | 77, true},
		{"unoccupied", 3<<48 | 78, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			gone, err := ntfsDeletionRecord(buffer, test.ref, 1024)
			if err != nil || gone != test.gone {
				t.Fatal("NTFS occupied-record proof", gone, err)
			}
		})
	}
	for _, name := range []string{"short", "geometry-small", "geometry-large", "geometry-unaligned", "length", "truncated", "tail", "magic", "zero-sequence", "unused", "future-slot", "sequence-conflict"} {
		t.Run(name, func(t *testing.T) {
			data := append([]byte(nil), buffer...)
			size := uint32(1024)
			switch name {
			case "short":
				data = data[:11]
			case "geometry-small":
				size = 256
			case "geometry-large":
				size = 131072
			case "geometry-unaligned":
				size = 1000
			case "length":
				binary.LittleEndian.PutUint32(data[8:], 512)
			case "truncated":
				data = data[:len(data)-1]
			case "tail":
				data = append(data, 0)
			case "magic":
				data[12] = 0
			case "zero-sequence":
				binary.LittleEndian.PutUint16(data[28:], 0)
			case "unused":
				binary.LittleEndian.PutUint16(data[34:], 0)
			case "future-slot":
				binary.LittleEndian.PutUint64(data, 79)
			case "sequence-conflict":
				binary.LittleEndian.PutUint64(data, 2<<48|77)
			}
			if _, err := ntfsDeletionRecord(data, 3<<48|78, size); err == nil {
				t.Fatal("malformed deletion evidence accepted")
			}
		})
	}
}

func TestNTFSDeletionGeometry(t *testing.T) {
	good := make([]byte, 128)
	binary.LittleEndian.PutUint64(good, 17)
	binary.LittleEndian.PutUint32(good[48:], 1024)
	binary.LittleEndian.PutUint32(good[96:], 32)
	binary.LittleEndian.PutUint16(good[100:], 3)
	binary.LittleEndian.PutUint16(good[102:], 1)
	if size, err := ntfsDeletionGeometry(good, 17); err != nil || size != 1024 {
		t.Fatal("NTFS deletion geometry", size, err)
	}
	for _, name := range []string{"short", "volume", "major", "minor", "small", "large", "unaligned", "extension-short", "extension-long"} {
		t.Run(name, func(t *testing.T) {
			b := append([]byte(nil), good...)
			switch name {
			case "short":
				b = b[:103]
			case "volume":
				b[0]++
			case "major":
				binary.LittleEndian.PutUint16(b[100:], 4)
			case "minor":
				binary.LittleEndian.PutUint16(b[102:], 2)
			case "small":
				binary.LittleEndian.PutUint32(b[48:], 256)
			case "large":
				binary.LittleEndian.PutUint32(b[48:], 131072)
			case "unaligned":
				binary.LittleEndian.PutUint32(b[48:], 1000)
			case "extension-short":
				binary.LittleEndian.PutUint32(b[96:], 7)
			case "extension-long":
				binary.LittleEndian.PutUint32(b[96:], 33)
			}
			if _, err := ntfsDeletionGeometry(b, 17); err == nil {
				t.Fatal("unsupported NTFS deletion geometry accepted")
			}
		})
	}
}

func TestWorkspaceChangeDeleteIntentPlan(t *testing.T) {
	root := journalObject{Volume: 17, ID: [16]byte{1}, Directory: true}
	directory := journalObject{Volume: 17, ID: [16]byte{2}, Directory: true}
	known := map[[16]byte]journalObject{root.ID: root, directory.ID: directory}
	changes := []workspaceChange{
		{ID: directory.ID, Parent: root.ID, USN: 100, Reason: usnReasonDelete, Attributes: usnDirectory},
		{ID: directory.ID, Parent: root.ID, USN: 110, Reason: usnReasonDelete, Attributes: usnDirectory},
		{ID: directory.ID, Parent: [16]byte{99}, USN: 120, Reason: usnReasonRenameNew, Attributes: usnDirectory},
		{ID: [16]byte{3}, Parent: directory.ID, USN: 130, Reason: usnReasonCreate},
	}
	plan, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, nil, 3)
	want := journalDeletion{Object: directory, USN: 100}
	if err != nil || !slices.Equal(plan.DeleteIntents, []journalDeletion{want}) || len(plan.Deleted) != 0 || len(plan.Created) != 1 || len(known) != 2 {
		t.Fatal("pending directory retains scope and earliest observation", plan, err)
	}
	intents := map[journalObject]int64{directory: 100}
	for _, object := range plan.Created {
		known[object.ID] = object
	}
	replay, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, intents, 3)
	if err != nil || len(replay.DeleteIntents) != 0 || len(replay.Created) != 0 || len(replay.Deleted) != 0 || intents[directory] != 100 {
		t.Fatal("durable intent prefix replay", replay, err)
	}
	// A later final-close record resolves the original identity, while an
	// unrelated later change (including a cleared disposition) keeps it live.
	for _, close := range []bool{false, true} {
		reason := uint32(1)
		if close {
			reason = usnReasonDelete | usnReasonClose
		}
		plan, err = planWorkspaceChanges([]workspaceChange{{ID: directory.ID, Parent: root.ID, USN: 200, Reason: reason, Attributes: usnDirectory}}, 17, root.ID, known, nil, intents, 3)
		if err != nil || len(plan.DeleteIntents) != 0 || (len(plan.Deleted) == 1) != close || len(intents) != 1 || intents[directory] != 100 {
			t.Fatal("later intent resolution", close, plan, err)
		}
	}
}

func TestWorkspaceChangeUnknownImportPlan(t *testing.T) {
	root := journalObject{Volume: 17, ID: [16]byte{1}, Directory: true}
	known := map[[16]byte]journalObject{root.ID: root}
	imported := journalObject{Volume: 17, ID: [16]byte{2}, Directory: true}
	child := journalObject{Volume: 17, ID: [16]byte{3}}
	file := journalObject{Volume: 17, ID: [16]byte{4}}
	activity := journalObject{Volume: 17, ID: [16]byte{5}}
	changes := []workspaceChange{
		// A rename-in has no creation record; so does later activity on an identity
		// whose entry into the tracked directory was never observed.
		{ID: imported.ID, Parent: root.ID, USN: 100, Reason: usnReasonRenameNew, Attributes: usnDirectory},
		{ID: file.ID, Parent: root.ID, USN: 110, Reason: usnReasonCreate | usnReasonRenameNew},
		{ID: activity.ID, Parent: root.ID, USN: 120, Reason: 1},
		// Descendant creation inside an imported directory remains an ordinary
		// tracked lifetime; its parent is attributable once the import is recorded.
		{ID: child.ID, Parent: imported.ID, USN: 130, Reason: usnReasonCreate},
		// Unrelated parents stay outside this workspace's census.
		{ID: [16]byte{99}, Parent: [16]byte{98}, USN: 140, Reason: usnReasonRenameNew},
	}
	plan, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, nil, 5)
	if err != nil || !slices.Equal(plan.Unknown, []journalObject{imported, file, {Volume: 17, ID: activity.ID}}) || !slices.Equal(plan.Created, []journalObject{child}) || len(known) != 1 {
		t.Fatal("unknown import classification", plan, err)
	}
	// A recorded batch is known to the next plan: replay of an old cursor is empty.
	for _, object := range append(plan.Unknown, plan.Created...) {
		known[object.ID] = object
	}
	replay, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, nil, 5)
	if err != nil || len(replay.Unknown) != 0 || len(replay.Created) != 0 {
		t.Fatal("unknown import replay", replay, err)
	}
	// The cumulative census bound includes unproved identities.
	if _, err := planWorkspaceChanges(changes, 17, root.ID, map[[16]byte]journalObject{root.ID: root}, nil, nil, 4); err == nil {
		t.Fatal("unknown imports exceeded census bound")
	}
	// Link and reparse evidence still requires its own proof, and a rename-in
	// beneath a non-directory remains structurally invalid.
	for _, reason := range []uint32{usnReasonHardLink, usnReasonReparse} {
		batch := []workspaceChange{{ID: imported.ID, Parent: root.ID, USN: 100, Reason: usnReasonRenameNew | reason, Attributes: usnDirectory}}
		if _, err := planWorkspaceChanges(batch, 17, root.ID, map[[16]byte]journalObject{root.ID: root}, nil, nil, 5); err == nil {
			t.Fatal("linked or reparse import accepted", reason)
		}
	}
	under := map[[16]byte]journalObject{root.ID: root, file.ID: file}
	if _, err := planWorkspaceChanges([]workspaceChange{{ID: imported.ID, Parent: file.ID, USN: 100, Reason: usnReasonRenameNew}}, 17, root.ID, under, nil, nil, 5); err == nil {
		t.Fatal("import below a file accepted")
	}
	// Deleting an imported identity before the checkpoint yields the same
	// intent/receipt obligations as any tracked object.
	gone, err := planWorkspaceChanges([]workspaceChange{
		{ID: imported.ID, Parent: root.ID, USN: 100, Reason: usnReasonRenameNew, Attributes: usnDirectory},
		{ID: imported.ID, Parent: root.ID, USN: 110, Reason: usnReasonDelete | usnReasonClose, Attributes: usnDirectory},
	}, 17, root.ID, map[[16]byte]journalObject{root.ID: root}, nil, nil, 5)
	if err != nil || len(gone.Unknown) != 1 || len(gone.Deleted) != 1 || gone.Deleted[0].Object != imported {
		t.Fatal("imported deletion receipt", gone, err)
	}
	if !errors.Is(fmt.Errorf("%w: 1 recorded identities", errWorkspaceUnknownImport), errWorkspaceUnknownImport) {
		t.Fatal("typed unknown import error")
	}
}

func TestWorkspaceChangeSecurityPlan(t *testing.T) {
	root := journalObject{Volume: 17, ID: [16]byte{1}, Directory: true}
	file := journalObject{Volume: 17, ID: [16]byte{2}}
	known := map[[16]byte]journalObject{root.ID: root, file.ID: file}
	created := journalObject{Volume: 17, ID: [16]byte{3}}
	changes := []workspaceChange{
		{ID: file.ID, Parent: root.ID, USN: 100, Reason: usnReasonSecurity},
		// A repeated record for one identity is reported once, in first-seen order.
		{ID: file.ID, Parent: root.ID, USN: 110, Reason: usnReasonSecurity | usnReasonClose},
		{ID: root.ID, Parent: [16]byte{99}, USN: 120, Reason: usnReasonSecurity | usnReasonClose, Attributes: usnDirectory},
		// A new object's own descriptor activity is an ordinary creation, not a
		// change to a tracked identity's descriptor.
		{ID: created.ID, Parent: root.ID, USN: 130, Reason: usnReasonCreate | usnReasonSecurity},
		// Unrelated volume activity is outside the census.
		{ID: [16]byte{88}, Parent: [16]byte{99}, USN: 140, Reason: usnReasonSecurity},
		// Other reasons on a tracked identity are not security changes.
		{ID: file.ID, Parent: root.ID, USN: 150, Reason: 0x1 | usnReasonClose},
	}
	plan, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, nil, 4)
	if err != nil || !slices.Equal(plan.Security, []journalObject{file, root}) || !slices.Equal(plan.Created, []journalObject{created}) {
		t.Fatal("security classification", plan, err)
	}
	// Planning never mutates the caller's census, and replay is stable.
	if len(known) != 2 {
		t.Fatal("planner changed the known census")
	}
	again, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, nil, 4)
	if err != nil || !slices.Equal(again.Security, plan.Security) {
		t.Fatal("security replay", again, err)
	}
	quiet, err := planWorkspaceChanges(changes[3:], 17, root.ID, known, nil, nil, 4)
	if err != nil || len(quiet.Security) != 0 {
		t.Fatal("unrelated activity was reported as a security change", quiet, err)
	}
	if !errors.Is(errWorkspaceSecurityChanged, errWriterFenceViolated) {
		t.Fatal("a descendant security change must be a writer-fence violation")
	}
}
