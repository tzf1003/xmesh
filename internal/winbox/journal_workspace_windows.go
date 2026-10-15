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
// this census covers the initial snapshot and the currently bound root tree.
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
	if j == nil || j.file == nil || j.blocked != nil || !j.state.workspace || len(j.records) < 2 || j.records[1].Workspace == nil || sid == nil || sid.String() != j.records[0].SID {
		return fmt.Errorf("AppContainer recovery requires an owned workspace journal")
	}
	if err := requireJournalProcessesReaped(j); err != nil {
		return err
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
	objects := make(map[journalObject]bool, len(w.Objects))
	identities := make(map[[16]byte]bool, len(w.Objects))
	for _, object := range w.Objects {
		if !validJournalObject(object) || object.Volume != w.Root.Volume || identities[object.ID] {
			return fmt.Errorf("AppContainer journal workspace identity mismatch")
		}
		objects[object] = true
		identities[object.ID] = true
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
	w := j.records[1].Workspace
	if maxObjects < len(w.Objects) || maxObjects > 10000 {
		return fmt.Errorf("AppContainer recovery census limit excludes workspace snapshot")
	}
	current, err := openWorkspaceTree(context.Background(), w.Path, maxObjects)
	if err != nil {
		return err
	}
	defer current.Close()
	if current.objects[0].id != (fileIdentity{Volume: w.Root.Volume, ID: w.Root.ID}) {
		return fmt.Errorf("AppContainer journal workspace root identity changed")
	}
	if err := checkJournalBoundary(j, current); err != nil {
		return err
	}
	initial := make(map[fileIdentity]bool, len(w.Objects))
	objects := make([]treeObject, 0, len(w.Objects)+len(current.objects))
	seen := make(map[fileIdentity]bool)
	defer func() {
		for _, object := range objects {
			windows.CloseHandle(object.handle)
		}
	}()
	retain := func(object treeObject) error {
		if seen[object.id] {
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
		seen[object.id] = true
		return nil
	}
	for _, expected := range w.Objects {
		object := treeObject{id: fileIdentity{Volume: expected.Volume, ID: expected.ID}, directory: expected.Directory}
		initial[object.id] = true
		if err := retain(object); err != nil {
			return err
		}
	}
	for _, object := range current.objects {
		if err := retain(object); err != nil {
			return err
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
			return err
		}
		acl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		if _, _, err := editJournalWorkspaceACL(acl, sid, object, !initial[object.id], intents[object.id]); err != nil {
			return err
		}
	}
	for index := len(objects) - 1; index >= 0; index-- {
		object := objects[index]
		intent := intents[object.id]
		if err := releaseJournalWorkspaceObject(object, sid, !initial[object.id], intent); err != nil {
			return err
		}
		if intent.Grant != nil {
			if _, err := j.append(journalRecord{Kind: "released", GrantSequence: intent.Sequence}); err != nil {
				return err
			}
		}
	}
	_, err = j.append(journalRecord{Kind: "workspace-released"})
	return err
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
