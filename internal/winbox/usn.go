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

// All inherited directories remain scope roots after moving: children created
// under them may still inherit this profile's ACE. External directory imports
// and deletion need independent subtree/deletion proofs before admission.
func collectWorkspaceChanges(changes []workspaceChange, volume uint64, known map[[16]byte]journalObject, maxObjects int) ([]journalObject, error) {
	var created []journalObject
	// The caller's map is a snapshot; errors must not add partial ownership.
	observed := make(map[[16]byte]journalObject, len(known))
	for id, object := range known {
		observed[id] = object
	}
	for _, change := range changes {
		object, exists := observed[change.ID]
		parent, parentExists := observed[change.Parent]
		if !exists && !parentExists {
			continue
		}
		directory := change.Attributes&usnDirectory != 0
		if exists && object.Directory != directory {
			return nil, fmt.Errorf("AppContainer change object type mismatch")
		}
		if parentExists && !parent.Directory {
			return nil, fmt.Errorf("AppContainer change parent is not a directory")
		}
		if change.Reason&(usnReasonDelete|usnReasonHardLink|usnReasonReparse) != 0 {
			return nil, fmt.Errorf("AppContainer workspace change requires deletion, link or reparse proof")
		}
		if exists {
			continue
		}
		if change.Reason&usnReasonRenameNew != 0 || change.Reason&usnReasonCreate == 0 {
			return nil, fmt.Errorf("AppContainer workspace change requires imported-object proof")
		}
		if len(observed) >= maxObjects {
			return nil, fmt.Errorf("AppContainer change census exceeds %d objects", maxObjects)
		}
		object = journalObject{Volume: volume, ID: change.ID, Directory: directory}
		observed[object.ID] = object
		created = append(created, object)
	}
	return created, nil
}
