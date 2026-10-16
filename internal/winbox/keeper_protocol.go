package winbox

import (
	"errors"
	"fmt"
)

// The guard keeper is an independent helper process that owns namespace guard
// handles for one workspace execution. It outlives its host: after the host
// exits it keeps every handle and serves a single adoption connection on a
// private named pipe, so a recovery owner can run while the same objects stay
// pinned. It exits only after a release request whose precondition (a durable
// "complete" journal bound to this execution) it verified itself.
const (
	maxKeeperHolds   = 1024
	maxKeeperPath    = 32768
	keeperRoleAnchor = "anchor" // volume root
	keeperRoleRoot   = "root"   // workspace root; the lease key
	keeperRoleParent = "ancestor"
	keeperRoleJourn  = "journal"
	keeperRoleImport = "import"

	keeperStateArmed    = "armed"
	keeperStateOrphaned = "orphaned"
)

var (
	errKeeperAbsent   = errors.New("AppContainer guard keeper does not exist")
	errKeeperNotReady = errors.New("AppContainer guard keeper is not available for adoption")
	errKeeperLost     = errors.New("AppContainer guard keeper exited without a verified release")
)

type keeperHold struct {
	Handle uint64        `json:"handle"`
	Object journalObject `json:"object"`
	Role   string        `json:"role"`
}

type keeperInit struct {
	Key     journalObject `json:"key"`
	Journal string        `json:"journal"`
	Holds   []keeperHold  `json:"holds"`
}

type keeperRequest struct {
	Sequence  uint64         `json:"sequence"`
	Operation string         `json:"operation"`
	Init      *keeperInit    `json:"init,omitempty"`
	Hold      *keeperHold    `json:"hold,omitempty"`
	Journal   *journalObject `json:"journal,omitempty"`
}

type keeperHeld struct {
	Object journalObject `json:"object"`
	Role   string        `json:"role"`
}

type keeperReply struct {
	Sequence uint64         `json:"sequence"`
	State    string         `json:"state,omitempty"`
	PID      uint32         `json:"pid,omitempty"`
	Held     []keeperHeld   `json:"held,omitempty"`
	Journal  *journalObject `json:"journal,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func keeperKeyName(key journalObject) string {
	return fmt.Sprintf("v1-%016x-%x", key.Volume, key.ID)
}

func keeperEventName(key journalObject) string {
	return `Global\xmesh-l2-keeper-` + keeperKeyName(key)
}

func keeperPipeName(key journalObject) string {
	return `\\.\pipe\xmesh-l2-keeper-` + keeperKeyName(key)
}

func validKeeperKey(key journalObject) bool {
	return validJournalObject(key) && key.Directory
}

func (r keeperRequest) validate() error {
	if r.Sequence == 0 || r.Sequence > maxJournalRecords {
		return fmt.Errorf("invalid guard keeper sequence")
	}
	if (r.Operation != "init" && r.Init != nil) || (r.Operation != "hold" && r.Hold != nil) || (r.Operation != "journal" && r.Journal != nil) {
		return fmt.Errorf("unexpected guard keeper payload")
	}
	switch r.Operation {
	case "init":
		if r.Sequence != 1 || r.Init == nil {
			break
		}
		return r.Init.validate()
	case "hold":
		if r.Hold != nil && r.Hold.Role == keeperRoleImport && r.Hold.Handle != 0 && validJournalObject(r.Hold.Object) {
			return nil
		}
	case "journal":
		if r.Journal != nil && validJournalObject(*r.Journal) && !r.Journal.Directory {
			return nil
		}
	case "status", "release":
		return nil
	}
	return fmt.Errorf("invalid guard keeper operation payload")
}

func (i keeperInit) validate() error {
	if !validKeeperKey(i.Key) || i.Journal == "" || len(i.Journal) > maxKeeperPath || len(i.Holds) == 0 || len(i.Holds) > maxKeeperHolds {
		return fmt.Errorf("invalid guard keeper initialization")
	}
	handles := make(map[uint64]bool)
	objects := make(map[journalObject]bool)
	roots, anchors := 0, 0
	for _, hold := range i.Holds {
		if hold.Handle == 0 || handles[hold.Handle] || objects[hold.Object] || !validJournalObject(hold.Object) || !hold.Object.Directory {
			return fmt.Errorf("invalid guard keeper namespace hold")
		}
		handles[hold.Handle], objects[hold.Object] = true, true
		switch hold.Role {
		case keeperRoleRoot:
			roots++
			if hold.Object != i.Key {
				return fmt.Errorf("guard keeper root differs from its lease key")
			}
		case keeperRoleAnchor:
			anchors++
		case keeperRoleParent, keeperRoleJourn:
		default:
			return fmt.Errorf("invalid guard keeper hold role")
		}
	}
	if roots != 1 || anchors == 0 {
		return fmt.Errorf("guard keeper requires one workspace root and a volume anchor")
	}
	return nil
}

// keeperReleaseError decides whether a keeper may exit and drop its holds.
// exists reports whether the journal path currently names a file; actual is that
// file's identity and j its parsed content. bound is the identity the host
// reported once it created the journal. Nothing but a durable completion for the
// bound journal releases the obligation; a never-created or never-written
// journal carries none because a profile intent always precedes any profile.
func keeperReleaseError(bound *journalObject, exists bool, actual journalObject, j *grantJournal) error {
	if !exists {
		if bound != nil {
			return fmt.Errorf("guard keeper journal disappeared")
		}
		return nil
	}
	if j == nil {
		return fmt.Errorf("guard keeper journal is unreadable")
	}
	if bound == nil {
		if j.blocked != nil || len(j.records) != 0 {
			return fmt.Errorf("guard keeper journal is not bound to this keeper")
		}
		return nil
	}
	if actual != *bound {
		return fmt.Errorf("guard keeper journal identity mismatch")
	}
	if j.blocked != nil {
		return fmt.Errorf("guard keeper journal is damaged: %v", j.blocked)
	}
	if !j.state.complete || len(j.state.executions) != 0 || len(j.state.pending) != 0 {
		return fmt.Errorf("guard keeper journal has outstanding recovery obligations")
	}
	return nil
}
