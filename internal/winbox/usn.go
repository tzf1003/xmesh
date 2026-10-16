package winbox

import (
	"encoding/binary"
	"errors"
	"fmt"
)

var errWorkspaceChangesLost = errors.New("AppContainer workspace change history is incomplete")

const (
	usnReasonCreate    = 0x00000100
	usnReasonDelete    = 0x00000200
	usnReasonRenameNew = 0x00002000
	usnReasonHardLink  = 0x00010000
	usnReasonReparse   = 0x00100000
	usnReasonClose     = 0x80000000
	usnDirectory       = 0x10
)

// USN records are consumed as identities and flags only. Filenames and file
// contents are neither retained nor included in diagnostics/recovery records.
type workspaceChange struct {
	ID, Parent         [16]byte
	USN                int64
	Reason, Attributes uint32
}

type workspaceChangeCursor struct {
	JournalID uint64 `json:"journal_id"`
	Next      int64  `json:"next"`
}

type workspaceChangeRange struct {
	JournalID           uint64
	First, Next, Lowest int64
}

func (r workspaceChangeRange) contains(c workspaceChangeCursor) error {
	if r.JournalID == 0 || c.JournalID != r.JournalID || r.First < 0 || r.Lowest < 0 || r.Next < r.First || r.Next < r.Lowest || c.Next < r.First || c.Next < r.Lowest || c.Next > r.Next {
		return errWorkspaceChangesLost
	}
	return nil
}

// Parse a complete FSCTL_READ_USN_JOURNAL buffer. Record lengths include their
// eight-byte padding. V2's NTFS reference is zero-extended to FILE_ID_128; V3
// carries the full ID. Unknown major versions stop before cursor advancement.
func parseWorkspaceChanges(buffer []byte, start int64) (int64, []workspaceChange, error) {
	if len(buffer) < 8 || start < 0 {
		return 0, nil, fmt.Errorf("invalid AppContainer change buffer")
	}
	next := int64(binary.LittleEndian.Uint64(buffer))
	if next < start {
		return 0, nil, errWorkspaceChangesLost
	}
	var changes []workspaceChange
	previous := start - 1
	for offset := 8; offset < len(buffer); {
		if len(buffer)-offset < 8 {
			return 0, nil, fmt.Errorf("truncated AppContainer change header")
		}
		record := buffer[offset:]
		size := int(binary.LittleEndian.Uint32(record))
		if size < 8 || size%8 != 0 || size > len(record) {
			return 0, nil, fmt.Errorf("invalid AppContainer change length")
		}
		record = record[:size]
		version := binary.LittleEndian.Uint16(record[4:])
		var change workspaceChange
		var header, position int
		switch version {
		case 2:
			header, position = 60, 24
		case 3:
			header, position = 76, 40
		default:
			return 0, nil, fmt.Errorf("unsupported AppContainer change record version %d", version)
		}
		if size < header {
			return 0, nil, fmt.Errorf("truncated AppContainer change record")
		}
		width := 8
		if version == 3 {
			width = 16
		}
		copy(change.ID[:], record[8:8+width])
		copy(change.Parent[:], record[8+width:8+width*2])
		change.USN = int64(binary.LittleEndian.Uint64(record[position:]))
		change.Reason = binary.LittleEndian.Uint32(record[position+16:])
		change.Attributes = binary.LittleEndian.Uint32(record[position+28:])
		nameSize := int(binary.LittleEndian.Uint16(record[position+32:]))
		nameOffset := int(binary.LittleEndian.Uint16(record[position+34:]))
		if change.ID == [16]byte{} || change.Parent == [16]byte{} || change.USN < start || change.USN <= previous || change.USN >= next || nameSize == 0 || nameSize%2 != 0 || nameOffset < header || nameOffset%2 != 0 || nameOffset > size || nameSize > size-nameOffset {
			return 0, nil, fmt.Errorf("invalid AppContainer change identity, order or name boundary")
		}
		previous = change.USN
		changes = append(changes, change)
		offset += size
	}
	if len(changes) != 0 && next == start {
		return 0, nil, errWorkspaceChangesLost
	}
	return next, changes, nil
}

// Deleted objects retain their original identity and final-close USN. A trusted
// source and an independent by-ID absence check are both required before this
// receipt is appended. The cursor advances only after the receipt is durable.
type journalDeletion struct {
	Object journalObject `json:"object"`
	USN    int64         `json:"usn"`
}

type workspaceChangePlan struct {
	Created []journalObject
	Deleted []journalDeletion
}

// Inherited directories remain scope roots after moving. A final delete/close
// ends that identity's lifetime; later changes or children require investigation.
// Planning spans the entire bounded checkpoint, including close records in a
// later read buffer. Errors leave caller-owned maps unchanged.
func planWorkspaceChanges(changes []workspaceChange, volume uint64, root [16]byte, known map[[16]byte]journalObject, retired map[journalObject]int64, maxObjects int) (workspaceChangePlan, error) {
	var plan workspaceChangePlan
	observed := make(map[[16]byte]journalObject, len(known))
	for id, object := range known {
		observed[id] = object
	}
	closed := make(map[[16]byte]int64, len(retired))
	for object, usn := range retired {
		closed[object.ID] = usn
	}
	pending := make(map[[16]byte]bool)
	for _, change := range changes {
		object, exists := observed[change.ID]
		parent, parentExists := observed[change.Parent]
		if !exists && !parentExists {
			continue
		}
		if death, ok := closed[change.ID]; ok && change.USN > death {
			return workspaceChangePlan{}, fmt.Errorf("AppContainer change follows final deletion")
		}
		if death, ok := closed[change.Parent]; ok && change.USN > death {
			return workspaceChangePlan{}, fmt.Errorf("AppContainer change follows parent deletion")
		}
		directory := change.Attributes&usnDirectory != 0
		if exists && object.Directory != directory {
			return workspaceChangePlan{}, fmt.Errorf("AppContainer change object type mismatch")
		}
		if parentExists && !parent.Directory {
			return workspaceChangePlan{}, fmt.Errorf("AppContainer change parent is not a directory")
		}
		if change.Reason&(usnReasonHardLink|usnReasonReparse) != 0 {
			return workspaceChangePlan{}, fmt.Errorf("AppContainer workspace change requires link or reparse proof")
		}
		if !exists {
			if change.Reason&usnReasonRenameNew != 0 || change.Reason&usnReasonCreate == 0 {
				return workspaceChangePlan{}, fmt.Errorf("AppContainer workspace change requires imported-object proof")
			}
			if len(observed) >= maxObjects {
				return workspaceChangePlan{}, fmt.Errorf("AppContainer change census exceeds %d objects", maxObjects)
			}
			object = journalObject{Volume: volume, ID: change.ID, Directory: directory}
			observed[object.ID] = object
			plan.Created = append(plan.Created, object)
		}
		if change.Reason&usnReasonDelete != 0 {
			if change.ID == root {
				return workspaceChangePlan{}, fmt.Errorf("AppContainer workspace root deletion")
			}
			pending[change.ID] = true
			if change.Reason&usnReasonClose != 0 {
				if death, ok := closed[change.ID]; ok {
					if death != change.USN {
						return workspaceChangePlan{}, fmt.Errorf("AppContainer conflicting deletion receipt")
					}
				} else {
					closed[change.ID] = change.USN
					plan.Deleted = append(plan.Deleted, journalDeletion{Object: object, USN: change.USN})
				}
			}
		}
	}
	for id := range pending {
		if _, ok := closed[id]; !ok {
			return workspaceChangePlan{}, fmt.Errorf("AppContainer deletion requires final-close proof")
		}
	}
	return plan, nil
}
