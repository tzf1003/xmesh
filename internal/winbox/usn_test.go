package winbox

import (
	"encoding/binary"
	"errors"
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
	plan, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, 3)
	objects := plan.Created
	if err != nil || len(objects) != 2 || objects[0].ID != [16]byte{2} || !objects[0].Directory || objects[1].ID != [16]byte{3} || len(known) != 1 {
		t.Fatal("moved-directory descendants", objects, err)
	}
	// Previously persisted discovery before a failed cursor write deduplicates.
	for _, object := range objects {
		known[object.ID] = object
	}
	if plan, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, 3); err != nil || len(plan.Created) != 0 {
		t.Fatal("cursor replay deduplication", plan, err)
	}
	for _, name := range []string{"limit", "delete", "hardlink", "reparse", "import", "missing-create", "type", "parent-type"} {
		t.Run(name, func(t *testing.T) {
			original := map[[16]byte]journalObject{root.ID: root}
			batch := []workspaceChange{{ID: [16]byte{2}, Parent: root.ID, Reason: usnReasonCreate, Attributes: usnDirectory}, {ID: [16]byte{3}, Parent: [16]byte{2}, Reason: usnReasonCreate}}
			limit := 3
			switch name {
			case "limit":
				limit = 2
			case "delete":
				batch[1].Reason |= usnReasonDelete
			case "hardlink":
				batch[1].Reason |= usnReasonHardLink
			case "reparse":
				batch[1].Reason |= usnReasonReparse
			case "import":
				batch[1].Reason |= usnReasonRenameNew
			case "missing-create":
				batch[1].Reason = 1
			case "type":
				batch[1].ID = root.ID
			case "parent-type":
				batch[0].Attributes = 0
			}
			if _, err := planWorkspaceChanges(batch, 17, root.ID, original, nil, limit); err == nil || !reflect.DeepEqual(original, map[[16]byte]journalObject{root.ID: root}) {
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
	plan, err := planWorkspaceChanges(changes, 17, root.ID, known, nil, 3)
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
		replay, err := planWorkspaceChanges(changes, 17, root.ID, known, retired, 3)
		if err != nil || len(replay.Created) != 0 || !slices.Equal(replay.Deleted, plan.Deleted[prefix:]) {
			t.Fatal("deletion prefix replay", prefix, replay, err)
		}
	}
	for _, name := range []string{"missing-close", "root", "after-delete", "child-after-parent", "conflicting-close", "hardlink-delete", "reparse-delete"} {
		t.Run(name, func(t *testing.T) {
			batch := append([]workspaceChange(nil), changes...)
			switch name {
			case "missing-close":
				batch[3].Reason &^= usnReasonClose
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
			if _, err := planWorkspaceChanges(batch, 17, root.ID, known, nil, 4); err == nil {
				t.Fatal("unproved deletion accepted")
			}
		})
	}
}
