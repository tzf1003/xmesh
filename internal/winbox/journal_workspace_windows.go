package winbox

import (
	"bytes"
	"context"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Workspace recovery requires reaped profile processes and exclusive namespace,
// reparse and ACL writers. The caller must account for all created identities:
// this census covers initial and durably observed IDs plus the current root tree.
// Runtime tracking of new objects moved outside that tree is a separate admission
// requirement. Production exec/ConPTY keep Windows L2 gated until it is supplied.
func recoverWorkspaceJournal(j *grantJournal, maxObjects int) error {
	sid, name, err := journalRecoveryIdentity(j)
	if err != nil {
		return err
	}
	defer windows.FreeSid(sid)
	if err := validateWorkspaceJournal(j, sid); err != nil {
		return err
	}
	if j.state.complete {
		return nil
	}
	if err := releaseJournalWorkspace(j, sid, maxObjects); err != nil {
		return err
	}
	return finishJournalProfile(j, name)
}

func validateWorkspaceJournal(j *grantJournal, sid *windows.SID) error {
	if err := validateWorkspaceJournalOwnership(j, sid); err != nil {
		return err
	}
	return requireJournalProcessesReaped(j)
}

func validateWorkspaceJournalOwnership(j *grantJournal, sid *windows.SID) error {
	if j == nil || j.file == nil || j.blocked != nil || !j.state.workspace || len(j.records) < 2 || j.records[1].Workspace == nil || sid == nil || sid.String() != j.records[0].SID {
		return fmt.Errorf("AppContainer recovery requires an owned workspace journal")
	}
	w := j.records[1].Workspace
	if _, _, err := workspaceComponents(w.Path); err != nil {
		return err
	}
	// Replay already checks the structural snapshot. Recheck the actual records
	// here so the full SID/ACE/identity ownership manifest precedes all edits.
	if len(w.Objects) == 0 || w.Root != w.Objects[0] || !w.Root.Directory {
		return fmt.Errorf("AppContainer journal workspace root mismatch")
	}
	var cursor *workspaceChangeCursor
	for _, record := range j.records {
		if record.ChangeCursor != nil {
			copy := *record.ChangeCursor
			cursor = &copy
		}
	}
	if (cursor == nil) != (j.state.changeCursor == nil) || (cursor != nil && *cursor != *j.state.changeCursor) {
		return fmt.Errorf("AppContainer journal change cursor ownership mismatch")
	}
	objects := make(map[journalObject]bool, len(w.Objects))
	identities := make(map[[16]byte]bool, len(w.Objects))
	for _, object := range w.Objects {
		if !validJournalObject(object) || object.Volume != w.Root.Volume || identities[object.ID] {
			return fmt.Errorf("AppContainer journal workspace identity mismatch")
		}
		objects[object] = true
		identities[object.ID] = true
	}
	created := make(map[journalObject]bool)
	for _, record := range j.records {
		for _, object := range record.CreatedObjects {
			if !validJournalObject(object) || object.Volume != w.Root.Volume || identities[object.ID] {
				return fmt.Errorf("AppContainer journal created-object identity mismatch")
			}
			created[object] = true
			identities[object.ID] = true
		}
	}
	if len(identities) > 10000 || len(created) != len(j.state.created) {
		return fmt.Errorf("AppContainer journal created-object manifest mismatch")
	}
	for object := range created {
		if !j.state.created[object] {
			return fmt.Errorf("AppContainer journal created-object ownership mismatch")
		}
	}
	intents := make(map[journalObject]int64)
	for _, record := range j.records {
		for _, intent := range record.DeleteIntents {
			object := intent.Object
			if (!objects[object] && !created[object]) || object == w.Root || intent.USN < 0 {
				return fmt.Errorf("AppContainer journal delete-intent ownership mismatch")
			}
			if _, exists := intents[object]; exists {
				return fmt.Errorf("AppContainer journal duplicate deletion intent")
			}
			intents[object] = intent.USN
		}
	}
	if len(intents) != len(j.state.deleteIntents) {
		return fmt.Errorf("AppContainer journal delete-intent manifest mismatch")
	}
	for object, usn := range intents {
		if actual, exists := j.state.deleteIntents[object]; !exists || actual != usn {
			return fmt.Errorf("AppContainer journal delete-intent receipt mismatch")
		}
	}
	deletions := make(map[journalObject]int64)
	for _, record := range j.records {
		for _, deletion := range record.DeletedObjects {
			object := deletion.Object
			if (!objects[object] && !created[object]) || object == w.Root || deletion.USN < 0 {
				return fmt.Errorf("AppContainer journal deletion ownership mismatch")
			}
			if _, exists := deletions[object]; exists {
				return fmt.Errorf("AppContainer journal duplicate deletion receipt")
			}
			deletions[object] = deletion.USN
		}
	}
	if len(deletions) != len(j.state.deleted) {
		return fmt.Errorf("AppContainer journal deletion manifest mismatch")
	}
	for object, usn := range deletions {
		if actual, exists := j.state.deleted[object]; !exists || actual != usn {
			return fmt.Errorf("AppContainer journal deletion receipt mismatch")
		}
	}
	granted := make(map[journalObject]bool)
	for _, record := range j.records {
		if record.Grant == nil {
			continue
		}
		g := *record.Grant
		if !objects[g.Object] || granted[g.Object] {
			return fmt.Errorf("AppContainer journal grant outside workspace snapshot")
		}
		granted[g.Object] = true
		// Directories use the same exact rights as explicit selected grants,
		// together with both child propagation flags.
		ace := bytes.Clone(g.ACE)
		flags := byte(0)
		if g.Object.Directory {
			flags = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
		}
		if len(ace) < 2 || ace[1] != flags {
			return fmt.Errorf("AppContainer journal workspace ACE flags mismatch")
		}
		ace[1] = 0
		g.ACE = ace
		if err := validateSelectedRecoveryACE(g, sid); err != nil {
			return err
		}
	}
	return nil
}

// Release is shared by a live workspace owner and recovery after owner exit.
// The complete bounded scan and every snapshot identity are opened before the
// first descriptor edit. Missing/deleted identities retain the journal rather
// than infer deletion from an unavailable ID. Roots and ancestors stay pinned
// for the complete operation; all handles and acknowledgements are retryable.
func releaseJournalWorkspace(j *grantJournal, sid *windows.SID, maxObjects int) error {
	if err := validateWorkspaceJournal(j, sid); err != nil {
		return err
	}
	if j.state.workspaceReleased {
		return nil
	}
	if j.state.changeCursor != nil {
		if err := checkpointWorkspaceChanges(context.Background(), j, sid, maxObjects, maxWorkspaceChangeEvents); err != nil {
			return err
		}
	}
	census, err := censusJournalWorkspace(j, sid, maxObjects)
	if err != nil {
		return err
	}
	defer census.Close()
	if j.state.changeCursor != nil && len(census.discovered) != 0 {
		return errWorkspaceChangesLost
	}
	// Flush every newly observed identity before the first ACL mutation. A
	// crash or failed release may otherwise lose an object later moved out.
	if err := appendWorkspaceCreated(j, census.discovered); err != nil {
		return err
	}
	for index := len(census.objects) - 1; index >= 0; index-- {
		object := census.objects[index]
		intent := census.intents[object.id]
		if err := releaseJournalWorkspaceObject(object, sid, !census.initial[object.id], intent); err != nil {
			return err
		}
		if intent.Grant != nil {
			if _, err := j.append(journalRecord{Kind: "released", GrantSequence: intent.Sequence}); err != nil {
				return err
			}
		}
	}
	// Deleted initial identities have no remaining descriptor. Acknowledge their
	// grant intents only after every live object passed census and reclamation.
	for _, record := range j.records {
		if record.Grant == nil || !j.state.pending[record.Sequence] {
			continue
		}
		if _, deleted := j.state.deleted[record.Grant.Object]; deleted {
			if _, err := j.append(journalRecord{Kind: "released", GrantSequence: record.Sequence}); err != nil {
				return err
			}
		}
	}
	_, err = j.append(journalRecord{Kind: "workspace-released"})
	return err
}

// Checkpoint records verified observed identities while the trusted caller holds
// namespace/reparse/ACL writers quiescent and serializes journal access. It can
// run before execution reaping because it never changes descriptors. A scan is
// not a complete runtime tracker: unobserved moves and deletions still require
// event tracking and a complete-census proof before production reclamation.
func checkpointJournalWorkspace(j *grantJournal, sid *windows.SID, maxObjects int) error {
	if err := validateWorkspaceJournalOwnership(j, sid); err != nil {
		return err
	}
	if j.state.workspaceReleased || j.state.complete {
		return fmt.Errorf("AppContainer workspace observation requires active ownership")
	}
	if j.state.changeCursor != nil {
		if err := checkpointWorkspaceChanges(context.Background(), j, sid, maxObjects, maxWorkspaceChangeEvents); err != nil {
			return err
		}
	}
	census, err := censusJournalWorkspace(j, sid, maxObjects)
	if err != nil {
		return err
	}
	defer census.Close()
	if j.state.changeCursor != nil && len(census.discovered) != 0 {
		return errWorkspaceChangesLost
	}
	return appendWorkspaceCreated(j, census.discovered)
}

func appendWorkspaceCreated(j *grantJournal, objects []journalObject) error {
	if len(objects) == 0 {
		return nil
	}
	_, err := j.append(journalRecord{Kind: "workspace-created", CreatedObjects: objects})
	return err
}

type journalWorkspaceCensus struct {
	tree       *workspaceTree
	objects    []treeObject
	initial    map[fileIdentity]bool
	intents    map[fileIdentity]journalRecord
	discovered []journalObject
}

func (c *journalWorkspaceCensus) Close() {
	if c.tree != nil {
		c.tree.Close()
		c.tree = nil
	}
	for _, object := range c.objects {
		windows.CloseHandle(object.handle)
	}
	c.objects = nil
}

// The caller validates journal ownership and holds external writers quiescent.
// Every known ID and current descriptor is verified before returning handles.
func censusJournalWorkspace(j *grantJournal, sid *windows.SID, maxObjects int) (*journalWorkspaceCensus, error) {
	w := j.records[1].Workspace
	if maxObjects < len(w.Objects)+len(j.state.created) || maxObjects > 10000 {
		return nil, fmt.Errorf("AppContainer recovery census limit excludes workspace snapshot")
	}
	current, err := openWorkspaceTree(context.Background(), w.Path, maxObjects)
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			current.Close()
		}
	}()
	if current.objects[0].id != (fileIdentity{Volume: w.Root.Volume, ID: w.Root.ID}) {
		return nil, fmt.Errorf("AppContainer journal workspace root identity changed")
	}
	if err := checkJournalBoundary(j, current); err != nil {
		return nil, err
	}
	initial := make(map[fileIdentity]bool, len(w.Objects))
	known := make(map[fileIdentity]bool, len(w.Objects)+len(j.state.created))
	var discovered []journalObject
	objects := make([]treeObject, 0, len(w.Objects)+len(current.objects))
	seen := make(map[fileIdentity]bool)
	defer func() {
		if !success {
			for _, object := range objects {
				windows.CloseHandle(object.handle)
			}
		}
	}()
	retain := func(object treeObject) error {
		key := journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: object.directory}
		if _, deleted := j.state.deleted[key]; deleted {
			return confirmWorkspaceDeletion(current.ancestors[0], key)
		}
		if directory, exists := seen[object.id]; exists {
			if directory != object.directory {
				return fmt.Errorf("AppContainer recovery object type changed")
			}
			return nil
		}
		if len(objects) >= maxObjects {
			return fmt.Errorf("AppContainer recovery union exceeds %d objects", maxObjects)
		}
		h, err := openIdentityObject(current.ancestors[0], object)
		if err != nil {
			return err
		}
		object.handle = h
		objects = append(objects, object)
		seen[object.id] = object.directory
		return nil
	}
	for _, expected := range w.Objects {
		object := treeObject{id: fileIdentity{Volume: expected.Volume, ID: expected.ID}, directory: expected.Directory}
		initial[object.id] = true
		known[object.id] = true
		if err := retain(object); err != nil {
			return nil, err
		}
	}
	for _, record := range j.records {
		for _, expected := range record.CreatedObjects {
			object := treeObject{id: fileIdentity{Volume: expected.Volume, ID: expected.ID}, directory: expected.Directory}
			known[object.id] = true
			if err := retain(object); err != nil {
				return nil, err
			}
		}
	}
	for _, object := range current.objects {
		if err := retain(object); err != nil {
			return nil, err
		}
		if !known[object.id] {
			discovered = append(discovered, journalObject{Volume: object.id.Volume, ID: object.id.ID, Directory: object.directory})
		}
	}
	intents := make(map[fileIdentity]journalRecord)
	for _, record := range j.records {
		if record.Grant != nil && j.state.pending[record.Sequence] {
			g := record.Grant.Object
			intents[fileIdentity{Volume: g.Volume, ID: g.ID}] = record
		}
	}
	// Validate every current descriptor too. Re-read and merge under each object
	// mutex for the actual write, preserving unrelated concurrent profiles.
	for _, object := range objects {
		sd, err := readObjectSecurity(object.handle)
		if err != nil {
			return nil, err
		}
		acl, _, err := sd.DACL()
		if err != nil {
			return nil, err
		}
		if _, _, err := editJournalWorkspaceACL(acl, sid, object, !initial[object.id], intents[object.id]); err != nil {
			return nil, err
		}
	}
	success = true
	return &journalWorkspaceCensus{tree: current, objects: objects, initial: initial, intents: intents, discovered: discovered}, nil
}

func releaseJournalWorkspaceObject(object treeObject, sid *windows.SID, created bool, intent journalRecord) error {
	lock, err := acquireObjectACL(object.id, 30000)
	if err != nil {
		return err
	}
	defer lock.Close()
	sd, err := readObjectSecurity(object.handle)
	if err != nil {
		return err
	}
	acl, defaulted, err := sd.DACL()
	if err != nil {
		return err
	}
	merged, changed, err := editJournalWorkspaceACL(acl, sid, object, created, intent)
	if err != nil || !changed {
		return err
	}
	return writeObjectDACL(object.handle, sd, merged, defaulted)
}

func editJournalWorkspaceACL(acl *windows.ACL, sid *windows.SID, object treeObject, created bool, intent journalRecord) (*windows.ACL, bool, error) {
	_, aces, err := aclParts(acl)
	if err != nil {
		return nil, false, err
	}
	matches := 0
	if intent.Grant != nil {
		for _, ace := range aces {
			if bytes.Equal(ace, intent.Grant.ACE) {
				matches++
			}
		}
		if matches > 1 {
			return nil, false, fmt.Errorf("AppContainer duplicate journal workspace ACE")
		}
		if matches == 1 {
			acl, err = editObjectACL(acl, intent.Grant.ACE, false)
			if err != nil {
				return nil, false, err
			}
		}
	}
	// Unrecognized SID-bearing entries cannot be silently treated as released.
	_, remaining, err := aclParts(acl)
	if err != nil {
		return nil, false, err
	}
	sidBytes := unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())
	for _, ace := range remaining {
		if bytes.Contains(ace, sidBytes) && (len(ace) != 8+len(sidBytes) || ace[0] != windows.ACCESS_ALLOWED_ACE_TYPE || !bytes.Equal(ace[8:], sidBytes)) {
			return nil, false, fmt.Errorf("AppContainer opaque journal workspace ACE ownership changed")
		}
	}
	merged, changed, err := editInheritedProfileACL(acl, sid, object.directory, created)
	if err != nil {
		return nil, false, err
	}
	if !changed {
		merged = acl
	}
	return merged, changed || matches == 1, nil
}
