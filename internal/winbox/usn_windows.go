package winbox

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	fsctlQueryUSNJournal     = 0x000900f4
	fsctlReadUSNJournal      = 0x000900bb
	maxWorkspaceChangeEvents = 65536
)

type workspaceChangeSource struct{ handle windows.Handle }

// Open the existing journal through the GUID of the retained volume root. This
// reads an NTFS journal; it never creates/resizes/deletes one or enables privilege.
// The trusted caller holds drive/namespace/reparse/ACL writers quiescent. An
// account or volume that cannot supply the complete stream retains an error.
func openWorkspaceChangeSource(anchor windows.Handle, volume uint64) (*workspaceChangeSource, error) {
	object, err := inspectTreeObject(anchor, "")
	if err != nil || !object.directory || object.id.Volume != volume {
		return nil, fmt.Errorf("AppContainer change source volume binding: %v", err)
	}
	var flags, serial uint32
	var filesystem [64]uint16
	if err := windows.GetVolumeInformationByHandle(anchor, nil, 0, &serial, nil, &flags, &filesystem[0], uint32(len(filesystem))); err != nil {
		return nil, err
	}
	if windows.UTF16ToString(filesystem[:]) != "NTFS" || flags&windows.FILE_SUPPORTS_USN_JOURNAL == 0 {
		return nil, fmt.Errorf("AppContainer change tracking requires an NTFS change journal")
	}
	var name [128]uint16
	// VOLUME_NAME_GUID=1; a volume root must resolve to exactly one GUID path.
	n, err := windows.GetFinalPathNameByHandle(anchor, &name[0], uint32(len(name)), 1)
	if err != nil || n == 0 || n >= uint32(len(name)) {
		return nil, fmt.Errorf("AppContainer change source GUID resolution: %v", err)
	}
	path := windows.UTF16ToString(name[:n])
	if len(path) != 49 || !strings.HasPrefix(path, `\\?\Volume{`) || !strings.HasSuffix(path, `}\`) {
		return nil, fmt.Errorf("AppContainer change source requires a volume-root GUID")
	}
	ptr, err := windows.UTF16PtrFromString(strings.TrimSuffix(path, `\`))
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(ptr, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("open AppContainer NTFS change source: %w", err)
	}
	// Reopen the GUID root and compare its full volume/128-bit root identity to
	// the retained anchor. The GUID selects the raw device used for this stream.
	rootName, err := windows.UTF16PtrFromString(path)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	root, err := windows.CreateFile(rootName, windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	actual, err := inspectTreeObject(root, "")
	windows.CloseHandle(root)
	if err != nil || actual.id != object.id || !actual.directory {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("AppContainer change source GUID identity mismatch: %v", err)
	}
	return &workspaceChangeSource{handle: h}, nil
}

func (s *workspaceChangeSource) Close() {
	if s.handle != 0 {
		windows.CloseHandle(s.handle)
		s.handle = 0
	}
}

func (s *workspaceChangeSource) query() (workspaceChangeRange, error) {
	var storage [10]uint64
	var returned uint32
	err := windows.DeviceIoControl(s.handle, fsctlQueryUSNJournal, nil, 0, (*byte)(unsafe.Pointer(&storage[0])), uint32(unsafe.Sizeof(storage)), &returned, nil)
	if err != nil {
		return workspaceChangeRange{}, fmt.Errorf("query AppContainer NTFS change journal: %w", err)
	}
	if returned < 56 || returned > uint32(unsafe.Sizeof(storage)) {
		return workspaceChangeRange{}, fmt.Errorf("invalid AppContainer change journal range")
	}
	r := workspaceChangeRange{JournalID: storage[0], First: int64(storage[1]), Next: int64(storage[2]), Lowest: int64(storage[3])}
	if err := r.contains(workspaceChangeCursor{JournalID: r.JournalID, Next: r.Next}); err != nil {
		return workspaceChangeRange{}, err
	}
	return r, nil
}

func (s *workspaceChangeSource) read(cursor workspaceChangeCursor) (workspaceChangeCursor, []workspaceChange, error) {
	before, err := s.query()
	if err != nil {
		return cursor, nil, err
	}
	if err := before.contains(cursor); err != nil {
		return cursor, nil, err
	}
	// READ_USN_JOURNAL_DATA_V1 is 48 bytes including tail padding. Zero timeout
	// and BytesToWaitFor request an immediate bounded read of every reason; records
	// are emitted during changes, including while a data handle remains open.
	var input [6]uint64
	b := unsafe.Slice((*byte)(unsafe.Pointer(&input[0])), 48)
	binary.LittleEndian.PutUint64(b[0:], uint64(cursor.Next))
	binary.LittleEndian.PutUint32(b[8:], 0xffffffff)
	binary.LittleEndian.PutUint64(b[32:], cursor.JournalID)
	binary.LittleEndian.PutUint16(b[40:], 2)
	binary.LittleEndian.PutUint16(b[42:], 3)
	output := make([]uint64, 128*1024/8)
	buffer := unsafe.Slice((*byte)(unsafe.Pointer(&output[0])), len(output)*8)
	var returned uint32
	err = windows.DeviceIoControl(s.handle, fsctlReadUSNJournal, &b[0], uint32(len(b)), &buffer[0], uint32(len(buffer)), &returned, nil)
	if err != nil {
		return cursor, nil, fmt.Errorf("read AppContainer NTFS change journal: %w", err)
	}
	if returned > uint32(len(buffer)) {
		return cursor, nil, fmt.Errorf("invalid AppContainer change buffer size")
	}
	next, changes, err := parseWorkspaceChanges(buffer[:returned], cursor.Next)
	if err != nil {
		return cursor, nil, err
	}
	after, err := s.query()
	if err != nil {
		return cursor, nil, err
	}
	// Recheck the start as well as the returned cursor after I/O: retention may
	// have moved or the journal may have restarted during the read.
	if err := after.contains(cursor); err != nil {
		return cursor, nil, err
	}
	advanced := workspaceChangeCursor{JournalID: cursor.JournalID, Next: next}
	if err := after.contains(advanced); err != nil {
		return cursor, nil, err
	}
	return advanced, changes, nil
}

// Begin before any sandbox admission, with external writers coordinated since
// the initial snapshot. Existing grants must all be present, and an exact scan
// must still match the initial objects. The persisted start survives host exit.
func beginWorkspaceChangeTracking(ctx context.Context, j *grantJournal, sid *windows.SID, maxObjects int) error {
	if err := validateWorkspaceJournalOwnership(j, sid); err != nil {
		return err
	}
	if j.state.changeCursor != nil || len(j.state.executionIDs) != 0 || len(j.state.created) != 0 || len(j.state.granted) != len(j.state.objects) || len(j.state.pending) != len(j.state.granted) || j.state.workspaceReleased || j.state.complete {
		return fmt.Errorf("AppContainer change tracking requires complete initial admission")
	}
	census, err := censusJournalWorkspace(j, sid, maxObjects)
	if err != nil {
		return err
	}
	defer census.Close()
	if len(census.discovered) != 0 || len(census.tree.objects) != len(j.state.objects) {
		return fmt.Errorf("AppContainer workspace changed before tracking admission")
	}
	source, err := openWorkspaceChangeSource(census.tree.ancestors[0], j.records[1].Workspace.Root.Volume)
	if err != nil {
		return err
	}
	defer source.Close()
	span, err := source.query()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = j.append(journalRecord{Kind: "workspace-tracking", ChangeCursor: &workspaceChangeCursor{JournalID: span.JournalID, Next: span.Next}})
	return err
}

// Checkpoint is serialized with journal access. Processes may be live; the
// caller coordinates external writers and retries after transient open races.
// Live pinned-object final-close coordination and imported-subtree proofs
// remain required before production L2 use.
func checkpointWorkspaceChanges(ctx context.Context, j *grantJournal, sid *windows.SID, maxObjects, maxEvents int) error {
	if err := validateWorkspaceJournalOwnership(j, sid); err != nil {
		return err
	}
	if j.state.changeCursor == nil || j.state.workspaceReleased || j.state.complete || maxObjects < 1 || maxObjects > 10000 || maxEvents < 1 || maxEvents > maxWorkspaceChangeEvents {
		return fmt.Errorf("AppContainer change checkpoint requires active bounded tracking")
	}
	w := j.records[1].Workspace
	tree, err := openWorkspaceRoot(ctx, w.Path)
	if err != nil {
		return err
	}
	defer tree.Close()
	if tree.objects[0].id != (fileIdentity{Volume: w.Root.Volume, ID: w.Root.ID}) {
		return fmt.Errorf("AppContainer tracked workspace root identity changed")
	}
	if err := checkJournalBoundary(j, tree); err != nil {
		return err
	}
	source, err := openWorkspaceChangeSource(tree.ancestors[0], w.Root.Volume)
	if err != nil {
		return err
	}
	defer source.Close()
	span, err := source.query()
	if err != nil {
		return err
	}
	if err := span.contains(*j.state.changeCursor); err != nil {
		return err
	}
	target := span.Next
	cursor := *j.state.changeCursor
	var changes []workspaceChange
	for cursor.Next < target {
		if err := ctx.Err(); err != nil {
			return err
		}
		next, batch, err := source.read(cursor)
		if err != nil {
			return err
		}
		if next.Next <= cursor.Next {
			return errWorkspaceChangesLost
		}
		if len(changes)+len(batch) > maxEvents {
			return fmt.Errorf("AppContainer change checkpoint exceeds %d events", maxEvents)
		}
		changes = append(changes, batch...)
		cursor = next
	}
	if cursor == *j.state.changeCursor {
		return nil
	}
	known := make(map[[16]byte]journalObject, len(j.state.objects)+len(j.state.created))
	for object := range j.state.objects {
		known[object.ID] = object
	}
	for object := range j.state.created {
		known[object.ID] = object
	}
	if len(known) > maxObjects {
		return fmt.Errorf("AppContainer change checkpoint identity limit")
	}
	plan, err := planWorkspaceChanges(changes, w.Root.Volume, w.Root.ID, known, j.state.deleted, maxObjects)
	if err != nil {
		return err
	}
	deleted := make(map[journalObject]bool, len(j.state.deleted)+len(plan.Deleted))
	for object := range j.state.deleted {
		deleted[object] = true
	}
	for _, deletion := range plan.Deleted {
		deleted[deletion.Object] = true
	}
	// Final delete/close records and current occupied-slot/sequence enumeration
	// are independent proofs. Query failures and live identities retain an error.
	for object := range deleted {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := confirmWorkspaceDeletion(tree.ancestors[0], object); err != nil {
			return err
		}
	}
	for _, object := range plan.Created {
		if deleted[object] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := openIdentityObject(tree.ancestors[0], treeObject{id: fileIdentity{Volume: object.Volume, ID: object.ID}, directory: object.Directory})
		if err != nil {
			return err
		}
		windows.CloseHandle(h)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Each prefix survives a crash. Replaying the old cursor deduplicates both
	// discoveries and deletion receipts before advancing past their source USNs.
	if err := appendWorkspaceCreated(j, plan.Created); err != nil {
		return err
	}
	if len(plan.Deleted) != 0 {
		if _, err := j.append(journalRecord{Kind: "workspace-deleted", DeletedObjects: plan.Deleted}); err != nil {
			return err
		}
	}
	_, err = j.append(journalRecord{Kind: "workspace-tracked", ChangeCursor: &cursor})
	return err
}

func confirmWorkspaceDeletion(volume windows.Handle, object journalObject) error {
	if object.ID == [16]byte{} || [8]byte(object.ID[8:]) != [8]byte{} {
		return fmt.Errorf("AppContainer deletion probe requires a zero-extended NTFS reference")
	}
	source, err := openWorkspaceChangeSource(volume, object.Volume)
	if err != nil {
		return err
	}
	defer source.Close()
	// NTFS_VOLUME_DATA_BUFFER supplies BytesPerFileRecordSegment at offset 48.
	var data [16]uint64
	var returned uint32
	err = windows.DeviceIoControl(source.handle, windows.FSCTL_GET_NTFS_VOLUME_DATA, nil, 0,
		(*byte)(unsafe.Pointer(&data[0])), uint32(unsafe.Sizeof(data)), &returned, nil)
	if err != nil {
		return fmt.Errorf("query AppContainer NTFS record geometry: %w", err)
	}
	if returned > uint32(unsafe.Sizeof(data)) {
		return fmt.Errorf("AppContainer NTFS record geometry binding mismatch")
	}
	geometry := unsafe.Slice((*byte)(unsafe.Pointer(&data[0])), int(returned))
	size, err := ntfsDeletionGeometry(geometry, object.Volume)
	if err != nil {
		return err
	}
	reference := binary.LittleEndian.Uint64(object.ID[:8])
	// GET_NTFS_FILE_RECORD enumerates occupied slots downwards. Request the
	// ordinal; compare its returned slot and record sequence with the full ID.
	ordinal := reference & ntfsReferenceOrdinalMask
	storage := make([]uint64, (12+int(size)+7)/8)
	defer clear(storage) // File-record payloads are transient and never recorded.
	buffer := unsafe.Slice((*byte)(unsafe.Pointer(&storage[0])), len(storage)*8)
	err = windows.DeviceIoControl(source.handle, 0x00090068, (*byte)(unsafe.Pointer(&ordinal)), 8,
		&buffer[0], uint32(len(buffer)), &returned, nil)
	if err != nil {
		return fmt.Errorf("query AppContainer NTFS deletion record: %w", err)
	}
	if returned > uint32(len(buffer)) {
		return fmt.Errorf("AppContainer NTFS deletion record length")
	}
	absent, err := ntfsDeletionRecord(buffer[:returned], reference, size)
	if err != nil {
		return err
	}
	if !absent {
		return fmt.Errorf("AppContainer deletion receipt still has a live identity")
	}
	return nil
}
