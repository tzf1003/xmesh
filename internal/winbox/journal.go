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
	errJournalIncomplete = errors.New("AppContainer journal has an incomplete tail")
	errJournalCorrupt    = errors.New("AppContainer journal is corrupt")
	journalProfileName   = regexp.MustCompile(`^xmesh-l2-[0-9a-f]{32}$`)
	journalProfileSID    = regexp.MustCompile(`^S-1-15-2(?:-[0-9]{1,10}){7}$`)
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

type journalRecord struct {
	Version       int               `json:"version"`
	Sequence      uint64            `json:"sequence"`
	Kind          string            `json:"kind"`
	Profile       string            `json:"profile,omitempty"`
	SID           string            `json:"sid,omitempty"`
	Workspace     *journalWorkspace `json:"workspace,omitempty"`
	Grant         *journalGrant     `json:"grant,omitempty"`
	GrantSequence uint64            `json:"grant_sequence,omitempty"`
}

type journalState struct {
	profile   bool
	workspace bool
	complete  bool
	pending   map[uint64]bool
}

// Check transitions before writing and during replay. Recovery integration must
// additionally verify full SID/profile binding, ACE ownership and filesystem
// identities before editing any security descriptor.
func (s *journalState) accept(r journalRecord) error {
	if r.Version != journalVersion || s.complete {
		return fmt.Errorf("invalid journal version or completed state")
	}
	profileFields := r.Profile != "" || r.SID != ""
	switch r.Kind {
	case "profile":
		if s.profile || r.Sequence != 1 || !journalProfileName.MatchString(r.Profile) || !validJournalSID(r.SID) || r.Workspace != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal profile intent")
		}
		s.profile = true
		s.pending = make(map[uint64]bool)
	case "workspace":
		if !s.profile || s.workspace || len(s.pending) != 0 || profileFields || r.Grant != nil || r.GrantSequence != 0 || r.Workspace == nil {
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
	case "grant":
		if !s.profile || profileFields || r.Workspace != nil || r.GrantSequence != 0 || r.Grant == nil || !validJournalObject(r.Grant.Object) {
			return fmt.Errorf("invalid journal grant intent")
		}
		ace := r.Grant.ACE
		if len(ace) < 16 || len(ace) > 65535 || len(ace)%4 != 0 || ace[0] != 0 || int(binary.LittleEndian.Uint16(ace[2:4])) != len(ace) {
			return fmt.Errorf("invalid journal grant ACE")
		}
		s.pending[r.Sequence] = true
	case "released":
		if !s.profile || profileFields || r.Workspace != nil || r.Grant != nil || !s.pending[r.GrantSequence] {
			return fmt.Errorf("invalid journal grant release")
		}
		delete(s.pending, r.GrantSequence)
	case "complete":
		if !s.profile || len(s.pending) != 0 || profileFields || r.Workspace != nil || r.Grant != nil || r.GrantSequence != 0 {
			return fmt.Errorf("invalid journal completion")
		}
		s.complete = true
	default:
		return fmt.Errorf("unknown journal record kind")
	}
	return nil
}

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
	file    *os.File
	records []journalRecord
	bytes   int64
	blocked error
	state   journalState
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
	// Validate against an independent state: a rejected append must not advance
	// recovery ownership. Decoding below also freezes caller-owned slices.
	next := j.state
	next.pending = make(map[uint64]bool, len(j.state.pending))
	for sequence := range j.state.pending {
		next.pending[sequence] = true
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
	if j.file == nil {
		return nil
	}
	err := j.file.Close()
	j.file = nil
	return err
}
