package winbox

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxJournalBytes   = 64 << 20
	maxJournalFrame   = 4 << 20
	maxJournalRecords = 65536
	journalVersion    = 1
)

var (
	errJournalIncomplete       = errors.New("AppContainer journal has an incomplete tail")
	errJournalCorrupt          = errors.New("AppContainer journal is corrupt")
	journalProfileName         = regexp.MustCompile(`^xmesh-l2-[0-9a-f]{32}$`)
	journalProfileSID          = regexp.MustCompile(`^S-1-15-2(?:-[0-9]{1,10}){7}$`)
	journalExecutionID         = regexp.MustCompile(`^[0-9a-f]{32}$`)
	errJournalProcessesPending = errors.New("AppContainer journal retains unconfirmed execution processes")
)

// Records describe recovery ownership, never file contents or command output.
// An intent must be flushed before its corresponding external mutation. Release
// records follow successful cleanup; complete follows profile deletion. Recovery
// must tolerate an intent whose external mutation never happened.
type journalObject struct {
	Volume    uint64   `json:"volume"`
	ID        [16]byte `json:"id"`
	Directory bool     `json:"directory"`
}

type journalGrant struct {
	Object journalObject `json:"object"`
	ACE    []byte        `json:"ace"`
}

type journalWorkspace struct {
	Path    string          `json:"path"`
	Root    journalObject   `json:"root"`
	Objects []journalObject `json:"objects"`
}

// A bound journal keeps its directory and ancestor identities alive separately
// from its recovery records. Opening the file again reconstructs this boundary.
type journalBoundary struct {
	Root      journalObject
	Ancestors []journalObject
}

type journalRecord struct {
	Version           int                    `json:"version"`
	Sequence          uint64                 `json:"sequence"`
	Kind              string                 `json:"kind"`
	Profile           string                 `json:"profile,omitempty"`
	SID               string                 `json:"sid,omitempty"`
	Workspace         *journalWorkspace      `json:"workspace,omitempty"`
	Import            *journalImport         `json:"import,omitempty"`
	CreatedObjects    []journalObject        `json:"created_objects,omitempty"`
	UnknownImports    []journalObject        `json:"unknown_imports,omitempty"`
	PendingDeletes    []journalObject        `json:"pending_deletes,omitempty"`
	DeleteIntents     []journalDeletion      `json:"delete_intents,omitempty"`
	DeletedObjects    []journalDeletion      `json:"deleted_objects,omitempty"`
	ChangeCursor      *workspaceChangeCursor `json:"change_cursor,omitempty"`
	Grant             *journalGrant          `json:"grant,omitempty"`
	GrantSequence     uint64                 `json:"grant_sequence,omitempty"`
	ExecutionID       string                 `json:"execution_id,omitempty"`
	ExecutionSequence uint64                 `json:"execution_sequence,omitempty"`
}

type journalState struct {
	profile           bool
	workspace         bool
	root              journalObject
	workspaceReleased bool
	complete          bool
	pending           map[uint64]bool
	objects           map[journalObject]bool
	granted           map[journalObject]bool
	created           map[journalObject]bool
	unknown           map[journalObject]bool // Subset of created without creation or census proof.
	deletePending     map[journalObject]bool
	deleteIntents     map[journalObject]int64
	deleted           map[journalObject]int64
	changeCursor      *workspaceChangeCursor
	executions        map[uint64]bool
	executionIDs      map[string]bool
}

// checkEnvelope rejects a record whose version, completion state or payload
// fields cannot belong to its kind. It is the part of validation shared by every
// record and never changes the state.
func (s *journalState) checkEnvelope(r journalRecord) error {
	if r.Version != journalVersion || s.complete {
		return fmt.Errorf("invalid journal version or completed state")
	}
	executionFields := r.ExecutionID != "" || r.ExecutionSequence != 0
	if executionFields && r.Kind != "execution" && r.Kind != "execution-reaped" {
		return fmt.Errorf("unexpected journal execution fields")
	}
	if r.Import != nil && r.Kind != "workspace-import" {
		return fmt.Errorf("unexpected journal import fields")
	}
	if r.CreatedObjects != nil && r.Kind != "workspace-created" {
		return fmt.Errorf("unexpected journal created-object fields")
	}
	if r.UnknownImports != nil && r.Kind != "workspace-unknown-import" {
		return fmt.Errorf("unexpected journal unknown-import fields")
	}
	if r.PendingDeletes != nil && r.Kind != "workspace-delete-pending" {
		return fmt.Errorf("unexpected journal pending-delete fields")
	}
	if r.DeleteIntents != nil && r.Kind != "workspace-delete-intent" {
		return fmt.Errorf("unexpected journal delete-intent fields")
	}
	if r.DeletedObjects != nil && r.Kind != "workspace-deleted" {
		return fmt.Errorf("unexpected journal deleted-object fields")
	}
	if r.ChangeCursor != nil && r.Kind != "workspace-tracking" && r.Kind != "workspace-tracked" {
		return fmt.Errorf("unexpected journal change cursor")
	}
	return nil
}

// Check transitions before writing and during replay. Recovery integration must
// additionally verify full SID/profile binding, ACE ownership and filesystem
// identities before editing any security descriptor.
func (s *journalState) accept(r journalRecord) error {
	if err := s.checkEnvelope(r); err != nil {
		return err
	}
	profileFields := r.Profile != "" || r.SID != ""
	switch r.Kind {
	case "profile":
		if s.profile || r.Sequence != 1 || !journalProfileName.MatchString(r.Profile) || !validJournalSID(r.SID) || r.Workspace != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal profile intent")
		}
		s.profile = true
		s.pending = make(map[uint64]bool)
		s.executions = make(map[uint64]bool)
		s.executionIDs = make(map[string]bool)
	case "execution":
		if !s.profile || s.workspaceReleased || len(s.executions) != 0 || len(s.executionIDs) >= 256 || !journalExecutionID.MatchString(r.ExecutionID) || s.executionIDs[r.ExecutionID] || r.ExecutionSequence != 0 || profileFields || r.Workspace != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal execution intent")
		}
		s.executions[r.Sequence] = true
		s.executionIDs[r.ExecutionID] = true
	case "execution-reaped":
		if !s.profile || r.ExecutionID != "" || !s.executions[r.ExecutionSequence] || profileFields || r.Workspace != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal execution exit confirmation")
		}
		delete(s.executions, r.ExecutionSequence)
	case "workspace":
		if !s.profile || s.workspace || r.Sequence != 2 || len(s.pending) != 0 || profileFields || r.Grant != nil || r.GrantSequence != 0 || r.Workspace == nil {
			return fmt.Errorf("invalid journal workspace intent")
		}
		w := r.Workspace
		if w.Path == "" || len(w.Path) > 32768 || !w.Root.Directory || !validJournalObject(w.Root) || len(w.Objects) == 0 || len(w.Objects) > 10000 || w.Objects[0] != w.Root {
			return fmt.Errorf("invalid journal workspace snapshot")
		}
		seen := make(map[[16]byte]bool, len(w.Objects))
		for _, object := range w.Objects {
			if !validJournalObject(object) || object.Volume != w.Root.Volume || seen[object.ID] {
				return fmt.Errorf("invalid or repeated journal workspace identity")
			}
			seen[object.ID] = true
		}
		s.workspace = true
		s.root = w.Root
		s.objects = make(map[journalObject]bool, len(w.Objects))
		s.granted = make(map[journalObject]bool)
		s.created = make(map[journalObject]bool)
		s.unknown = make(map[journalObject]bool)
		s.deletePending = make(map[journalObject]bool)
		s.deleteIntents = make(map[journalObject]int64)
		s.deleted = make(map[journalObject]int64)
		for _, object := range w.Objects {
			s.objects[object] = true
		}
	case "workspace-tracking", "workspace-tracked":
		if !s.workspace || s.workspaceReleased || r.ChangeCursor == nil || r.ChangeCursor.JournalID == 0 || r.ChangeCursor.Next < 0 || profileFields || r.Workspace != nil || r.CreatedObjects != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal workspace tracking cursor")
		}
		if r.Kind == "workspace-tracking" {
			if s.changeCursor != nil || len(s.executionIDs) != 0 || len(s.created) != 0 || len(s.granted) != len(s.objects) || len(s.pending) != len(s.granted) {
				return fmt.Errorf("workspace tracking requires complete initial admission before execution")
			}
		} else if s.changeCursor == nil || r.ChangeCursor.JournalID != s.changeCursor.JournalID || r.ChangeCursor.Next <= s.changeCursor.Next {
			return fmt.Errorf("invalid journal workspace tracking advance")
		}
		if r.Kind == "workspace-tracked" {
			for _, usn := range s.deleteIntents {
				if usn >= r.ChangeCursor.Next {
					return fmt.Errorf("workspace cursor does not cover deletion intent")
				}
			}
			for _, usn := range s.deleted {
				if usn >= r.ChangeCursor.Next {
					return fmt.Errorf("workspace cursor does not cover deletion receipt")
				}
			}
		}
		cursor := *r.ChangeCursor
		s.changeCursor = &cursor

	case "workspace-import":
		if !s.workspace || s.workspaceReleased || s.changeCursor == nil || r.Import == nil || r.Import.Cursor != *s.changeCursor || profileFields || r.Workspace != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal workspace import admission")
		}
		known := make(map[journalObject]bool, len(s.objects)+len(s.created))
		for object := range s.objects {
			known[object] = true
		}
		for object := range s.created {
			known[object] = true
		}
		added, err := planWorkspaceImport(*r.Import, s.root, known, s.deleted, 10000)
		if err != nil {
			return err
		}
		if len(added) == 0 {
			return fmt.Errorf("journal import adds no identity")
		}
		for _, object := range added {
			s.created[object] = true
		}

	case "workspace-created":
		if !s.workspace || s.workspaceReleased || len(r.CreatedObjects) == 0 || len(s.objects)+len(s.created)+len(r.CreatedObjects) > 10000 || profileFields || r.Workspace != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal workspace created-object batch")
		}
		// Compare physical IDs independently of type. Validate the entire batch
		// before changing replay ownership, including conflicts in its last item.
		seen := make(map[[16]byte]bool, len(s.objects)+len(s.created)+len(r.CreatedObjects))
		var volume uint64
		for object := range s.objects {
			seen[object.ID] = true
			volume = object.Volume
		}
		for object := range s.created {
			seen[object.ID] = true
		}
		for _, object := range r.CreatedObjects {
			if !validJournalObject(object) || object.Volume != volume || seen[object.ID] {
				return fmt.Errorf("invalid or repeated journal created-object identity")
			}
			seen[object.ID] = true
		}
		for _, object := range r.CreatedObjects {
			s.created[object] = true
		}

	case "workspace-unknown-import":
		// An uncoordinated rename-in has no pre-move census. The identity batch
		// takes the same recovery ownership as a created object, with a separate
		// durable mark so reconciliation never mistakes it for a proved creation.
		if !s.workspace || s.workspaceReleased || s.changeCursor == nil || len(r.UnknownImports) == 0 || len(s.objects)+len(s.created)+len(r.UnknownImports) > 10000 || profileFields || r.Workspace != nil || r.CreatedObjects != nil || r.Import != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal workspace unknown-import batch")
		}
		seen := make(map[[16]byte]bool, len(s.objects)+len(s.created)+len(r.UnknownImports))
		for object := range s.objects {
			seen[object.ID] = true
		}
		for object := range s.created {
			seen[object.ID] = true
		}
		for _, object := range r.UnknownImports {
			if !validJournalObject(object) || object.Volume != s.root.Volume || seen[object.ID] {
				return fmt.Errorf("invalid or repeated journal unknown-import identity")
			}
			seen[object.ID] = true
		}
		for _, object := range r.UnknownImports {
			s.created[object] = true
			s.unknown[object] = true
		}

	case "workspace-delete-pending":
		if !s.workspace || s.workspaceReleased || s.changeCursor == nil || len(r.PendingDeletes) == 0 || profileFields || r.Workspace != nil || r.CreatedObjects != nil || r.ChangeCursor != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal workspace pending-delete batch")
		}
		seen := make(map[journalObject]bool, len(r.PendingDeletes))
		for _, object := range r.PendingDeletes {
			_, deleted := s.deleted[object]
			// Kernel observations use retained initial handles and keep the
			// object's census obligations. They supply no USN deletion proof.
			if !s.objects[object] || object == s.root || s.deletePending[object] || deleted || seen[object] {
				return fmt.Errorf("invalid or repeated journal pending-delete identity")
			}
			seen[object] = true
		}
		for _, object := range r.PendingDeletes {
			s.deletePending[object] = true
		}

	case "workspace-delete-intent":
		if !s.workspace || s.workspaceReleased || s.changeCursor == nil || len(r.DeleteIntents) == 0 || profileFields || r.Workspace != nil || r.CreatedObjects != nil || r.DeletedObjects != nil || r.ChangeCursor != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal workspace delete-intent batch")
		}
		seen := make(map[journalObject]bool, len(r.DeleteIntents))
		for _, intent := range r.DeleteIntents {
			object := intent.Object
			_, recorded := s.deleteIntents[object]
			_, deleted := s.deleted[object]
			if (!s.objects[object] && !s.created[object]) || object == s.root || recorded || deleted || seen[object] || intent.USN < s.changeCursor.Next {
				return fmt.Errorf("invalid or repeated journal deletion intent")
			}
			seen[object] = true
		}
		for _, intent := range r.DeleteIntents {
			s.deleteIntents[intent.Object] = intent.USN
		}

	case "workspace-deleted":
		if !s.workspace || s.workspaceReleased || s.changeCursor == nil || len(r.DeletedObjects) == 0 || profileFields || r.Workspace != nil || r.CreatedObjects != nil || r.ChangeCursor != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal workspace deletion batch")
		}
		seen := make(map[journalObject]bool, len(r.DeletedObjects))
		for _, deletion := range r.DeletedObjects {
			object := deletion.Object
			_, already := s.deleted[object]
			// The workspace root remains bound throughout reclamation.
			if (!s.objects[object] && !s.created[object]) || object == s.root || already || seen[object] || deletion.USN < s.changeCursor.Next || s.deleteIntents[object] > deletion.USN {
				return fmt.Errorf("invalid or repeated journal deletion identity")
			}
			seen[object] = true
		}
		for _, deletion := range r.DeletedObjects {
			s.deleted[deletion.Object] = deletion.USN
		}

	case "grant":
		if err := s.checkGrant(r, profileFields); err != nil {
			return err
		}
		s.applyGrant(r)
	case "released":
		if err := s.checkRelease(r, profileFields); err != nil {
			return err
		}
		s.applyRelease(r)
	case "workspace-released":
		if len(s.executions) != 0 {
			return errJournalProcessesPending
		}
		if !s.workspace || s.workspaceReleased || len(s.pending) != 0 || profileFields || r.Workspace != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal workspace release")
		}
		s.workspaceReleased = true
	case "complete":
		if len(s.executions) != 0 {
			return errJournalProcessesPending
		}
		if !s.profile || (s.workspace && !s.workspaceReleased) || len(s.pending) != 0 || profileFields || r.Workspace != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal completion")
		}
		s.complete = true
	default:
		return fmt.Errorf("unknown journal record kind")
	}
	return nil
}

// Grant intents and their releases are the only records written once per
// workspace object, so they alone have a check/apply/revert split: a check reads
// the state, apply and revert change just the entries the record owns. That lets
// them be validated and committed without cloning every map in the state.
func (s *journalState) checkGrant(r journalRecord, profileFields bool) error {
	if !s.profile || s.workspaceReleased || profileFields || r.Workspace != nil || r.GrantSequence != 0 || r.Grant == nil || !validJournalObject(r.Grant.Object) {
		return fmt.Errorf("invalid journal grant intent")
	}
	ace := r.Grant.ACE
	if len(ace) < 16 || len(ace) > 65535 || len(ace)%4 != 0 || ace[0] != 0 || int(binary.LittleEndian.Uint16(ace[2:4])) != len(ace) {
		return fmt.Errorf("invalid journal grant ACE")
	}
	if s.workspace && (!s.objects[r.Grant.Object] || s.granted[r.Grant.Object]) {
		return fmt.Errorf("invalid or repeated journal workspace grant")
	}
	return nil
}

func (s *journalState) applyGrant(r journalRecord) {
	if s.workspace {
		s.granted[r.Grant.Object] = true
	}
	s.pending[r.Sequence] = true
}

func (s *journalState) revertGrant(r journalRecord) {
	if s.workspace {
		delete(s.granted, r.Grant.Object)
	}
	delete(s.pending, r.Sequence)
}

func (s *journalState) checkRelease(r journalRecord, profileFields bool) error {
	if len(s.executions) != 0 {
		return errJournalProcessesPending
	}
	if !s.profile || profileFields || r.Workspace != nil || r.Grant != nil || !s.pending[r.GrantSequence] {
		return fmt.Errorf("invalid journal grant release")
	}
	return nil
}

func (s *journalState) applyRelease(r journalRecord)  { delete(s.pending, r.GrantSequence) }
func (s *journalState) revertRelease(r journalRecord) { s.pending[r.GrantSequence] = true }

func validJournalObject(o journalObject) bool { return o.ID != [16]byte{} }

func validJournalSID(sid string) bool {
	if !journalProfileSID.MatchString(sid) {
		return false
	}
	for _, component := range strings.Split(strings.TrimPrefix(sid, "S-1-15-2-"), "-") {
		if _, err := strconv.ParseUint(component, 10, 32); err != nil {
			return false
		}
	}
	return true
}

// grantJournal has one owner. Windows opens it with exclusive sharing; callers
// serialize goroutines. A failed write or Sync poisons further appends: even a
// failed Sync can have written a complete intent that recovery must inspect.
type grantJournal struct {
	file      *os.File
	records   []journalRecord
	bytes     int64
	blocked   error
	state     journalState
	namespace io.Closer
	boundary  *journalBoundary
}

func loadGrantJournal(file *os.File) (*grantJournal, error) {
	j := &grantJournal{file: file}
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxJournalBytes {
		return nil, fmt.Errorf("%w: size limit", errJournalCorrupt)
	}
	reader := io.NewSectionReader(file, 0, info.Size())
	for j.bytes < info.Size() {
		r, n, err := readJournalFrame(reader)
		if err == nil && (len(j.records) >= maxJournalRecords || r.Sequence != uint64(len(j.records)+1)) {
			err = fmt.Errorf("%w: sequence or record limit", errJournalCorrupt)
		}
		if err == nil {
			if validation := j.state.accept(r); validation != nil {
				err = fmt.Errorf("%w: %v", errJournalCorrupt, validation)
			}
		}
		if err != nil {
			j.blocked = err
			return j, err
		}
		j.bytes += int64(n)
		j.records = append(j.records, r)
	}
	return j, nil
}

func readJournalFrame(reader io.Reader) (journalRecord, int, error) {
	var record journalRecord
	var header [8]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return record, 0, errJournalIncomplete
	}
	size := binary.LittleEndian.Uint32(header[:4])
	if size == 0 || size > maxJournalFrame {
		return record, 0, fmt.Errorf("%w: frame length", errJournalCorrupt)
	}
	body := make([]byte, size)
	if _, err := io.ReadFull(reader, body); err != nil {
		return record, 0, errJournalIncomplete
	}
	if crc32.ChecksumIEEE(body) != binary.LittleEndian.Uint32(header[4:]) {
		return record, 0, fmt.Errorf("%w: checksum", errJournalCorrupt)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, 0, fmt.Errorf("%w: record encoding", errJournalCorrupt)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return record, 0, fmt.Errorf("%w: trailing record data", errJournalCorrupt)
	}
	return record, len(body) + len(header), nil
}

func (j *grantJournal) append(record journalRecord) (uint64, error) {
	if j.blocked != nil || j.file == nil {
		return 0, fmt.Errorf("AppContainer journal unavailable: %w", j.blocked)
	}
	if len(j.records) >= maxJournalRecords {
		return 0, fmt.Errorf("AppContainer journal record limit")
	}
	record.Version, record.Sequence = journalVersion, uint64(len(j.records)+1)
	if record.Kind == "grant" || record.Kind == "released" {
		sequences, err := j.appendBatch([]journalRecord{record})
		if err != nil {
			return 0, err
		}
		return sequences[0], nil
	}
	// Validate against an independent state: a rejected append must not advance
	// recovery ownership. Decoding below also freezes caller-owned slices.
	next := j.state
	next.pending = make(map[uint64]bool, len(j.state.pending))
	for sequence := range j.state.pending {
		next.pending[sequence] = true
	}
	next.granted = make(map[journalObject]bool, len(j.state.granted))
	for object := range j.state.granted {
		next.granted[object] = true
	}
	next.created = make(map[journalObject]bool, len(j.state.created))
	for object := range j.state.created {
		next.created[object] = true
	}
	next.unknown = make(map[journalObject]bool, len(j.state.unknown))
	for object := range j.state.unknown {
		next.unknown[object] = true
	}
	next.deletePending = make(map[journalObject]bool, len(j.state.deletePending))
	for object := range j.state.deletePending {
		next.deletePending[object] = true
	}
	next.deleteIntents = make(map[journalObject]int64, len(j.state.deleteIntents))
	for object, usn := range j.state.deleteIntents {
		next.deleteIntents[object] = usn
	}
	next.deleted = make(map[journalObject]int64, len(j.state.deleted))
	for object, usn := range j.state.deleted {
		next.deleted[object] = usn
	}
	next.executions = make(map[uint64]bool, len(j.state.executions))
	for sequence := range j.state.executions {
		next.executions[sequence] = true
	}
	next.executionIDs = make(map[string]bool, len(j.state.executionIDs))
	for id := range j.state.executionIDs {
		next.executionIDs[id] = true
	}
	if err := next.accept(record); err != nil {
		return 0, err
	}
	body, err := json.Marshal(record)
	if err != nil {
		return 0, err
	}
	if len(body) > maxJournalFrame || j.bytes+int64(len(body)+8) > maxJournalBytes {
		return 0, fmt.Errorf("AppContainer journal size limit")
	}
	frame := make([]byte, 8+len(body))
	binary.LittleEndian.PutUint32(frame[:4], uint32(len(body)))
	binary.LittleEndian.PutUint32(frame[4:8], crc32.ChecksumIEEE(body))
	copy(frame[8:], body)
	n, err := j.file.WriteAt(frame, j.bytes)
	if err == nil && n != len(frame) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = j.file.Sync()
	}
	if err != nil {
		j.blocked = err
		return 0, err
	}
	frozen, _, err := readJournalFrame(bytes.NewReader(frame))
	if err != nil {
		j.blocked = err
		return 0, err
	}
	j.records = append(j.records, frozen)
	j.state, j.bytes = next, j.bytes+int64(len(frame))
	return record.Sequence, nil
}

func (j *grantJournal) Close() error {
	var result error
	if j.file != nil {
		result = j.file.Close()
		j.file = nil
	}
	if j.namespace != nil {
		result = errors.Join(result, j.namespace.Close())
		j.namespace = nil
	}
	return result
}

// appendBatch writes grant intents, or grant releases, as consecutive frames
// followed by one flush. Each frame is an ordinary record, so replay, recovery
// and older readers see exactly what separate appends would have written. The
// only difference is when the flush happens: a caller that appends intents here
// must still do so before the first mutation any of them describes, which a
// single flush covering all of them satisfies. A release batch is the same
// trade a crash between a descriptor edit and its release record already makes.
//
// Records are validated in order against the live state, each seeing the ones
// before it. A rejected or unflushed batch leaves the state unchanged.
func (j *grantJournal) appendBatch(records []journalRecord) ([]uint64, error) {
	if len(records) == 0 {
		return nil, nil
	}
	if j.blocked != nil || j.file == nil {
		return nil, fmt.Errorf("AppContainer journal unavailable: %w", j.blocked)
	}
	if len(j.records)+len(records) > maxJournalRecords {
		return nil, fmt.Errorf("AppContainer journal record limit")
	}
	applied := make([]journalRecord, 0, len(records))
	revert := func() {
		for i := len(applied) - 1; i >= 0; i-- {
			if applied[i].Kind == "grant" {
				j.state.revertGrant(applied[i])
			} else {
				j.state.revertRelease(applied[i])
			}
		}
	}
	sequences := make([]uint64, len(records))
	var buffer []byte
	size := j.bytes
	for i, record := range records {
		record.Version, record.Sequence = journalVersion, uint64(len(j.records)+i+1)
		profileFields := record.Profile != "" || record.SID != ""
		err := j.state.checkEnvelope(record)
		switch {
		case err != nil:
		case record.Kind == "grant":
			if err = j.state.checkGrant(record, profileFields); err == nil {
				j.state.applyGrant(record)
			}
		case record.Kind == "released":
			if err = j.state.checkRelease(record, profileFields); err == nil {
				j.state.applyRelease(record)
			}
		default:
			err = fmt.Errorf("unexpected journal batch record kind")
		}
		if err != nil {
			revert()
			return nil, err
		}
		applied = append(applied, record)
		body, err := json.Marshal(record)
		if err == nil && (len(body) > maxJournalFrame || size+int64(len(body)+8) > maxJournalBytes) {
			err = fmt.Errorf("AppContainer journal size limit")
		}
		if err != nil {
			revert()
			return nil, err
		}
		frame := make([]byte, 8+len(body))
		binary.LittleEndian.PutUint32(frame[:4], uint32(len(body)))
		binary.LittleEndian.PutUint32(frame[4:8], crc32.ChecksumIEEE(body))
		copy(frame[8:], body)
		buffer = append(buffer, frame...)
		size += int64(len(frame))
		sequences[i] = record.Sequence
	}
	n, err := j.file.WriteAt(buffer, j.bytes)
	if err == nil && n != len(buffer) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = j.file.Sync()
	}
	if err != nil {
		revert()
		j.blocked = err
		return nil, err
	}
	// Freeze the committed records from their encoded form, as a replay would.
	frozen := make([]journalRecord, 0, len(records))
	reader := bytes.NewReader(buffer)
	for range records {
		record, _, err := readJournalFrame(reader)
		if err != nil {
			revert()
			j.blocked = err
			return nil, err
		}
		frozen = append(frozen, record)
	}
	j.records = append(j.records, frozen...)
	j.bytes = size
	return sequences, nil
}
