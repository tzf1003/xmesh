package winbox

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Recover only explicit selected-object intents. Workspace inheritance and
// execution recovery need additional state tracking before they can use this
// entry point. The caller must have reaped every process using the profile and
// coordinate namespace/reparse writers. The supplied volume is identity-checked
// against every intent before any descriptor edit. The exclusive journal prevents
// a live journal owner from sharing this recovery operation.
func recoverSelectedJournal(j *grantJournal, volume windows.Handle) error {
	var anchor fileIdentity
	if err := windows.GetFileInformationByHandleEx(volume, windows.FileIdInfo, (*byte)(unsafe.Pointer(&anchor)), uint32(unsafe.Sizeof(anchor))); err != nil {
		return err
	}
	return recoverSelectedJournalVolumes(j, map[uint64]windows.Handle{anchor.Volume: volume})
}

// All volume bindings and ACE ownership are validated before the first edit.
// The caller keeps each supplied volume handle and recovery guard alive through
// profile deletion and its durable completion acknowledgement.
func recoverSelectedJournalVolumes(j *grantJournal, volumes map[uint64]windows.Handle) error {
	sid, name, err := journalRecoveryIdentity(j)
	if err != nil {
		return err
	}
	defer windows.FreeSid(sid)
	if err := validateSelectedJournal(j, sid); err != nil {
		return err
	}
	checked := make(map[uint64]bool)
	// Validate the entire ownership manifest before the first external mutation.
	for _, record := range j.records {
		if record.Grant == nil {
			continue
		}
		volume := record.Grant.Object.Volume
		if !checked[volume] {
			var anchor fileIdentity
			if err := windows.GetFileInformationByHandleEx(volumes[volume], windows.FileIdInfo, (*byte)(unsafe.Pointer(&anchor)), uint32(unsafe.Sizeof(anchor))); err != nil {
				return fmt.Errorf("AppContainer recovery volume unavailable: %w", err)
			}
			if anchor.Volume != volume {
				return fmt.Errorf("AppContainer journal recovery volume mismatch")
			}
			checked[volume] = true
		}
	}
	if j.state.complete {
		return nil
	}
	for index := len(j.records) - 1; index >= 0; index-- {
		record := j.records[index]
		if record.Grant == nil || !j.state.pending[record.Sequence] {
			continue
		}
		g := record.Grant
		expected := treeObject{id: fileIdentity{Volume: g.Object.Volume, ID: g.Object.ID}, directory: g.Object.Directory}
		h, err := openIdentityObject(volumes[g.Object.Volume], expected)
		if err != nil {
			return err
		}
		err = recoverSelectedObject(h, expected.id, g.ACE, sid)
		windows.CloseHandle(h)
		if err != nil {
			return err
		}
		if _, err := j.append(journalRecord{Kind: "released", GrantSequence: record.Sequence}); err != nil {
			return err
		}
	}
	return finishJournalProfile(j, name)
}

func validateSelectedJournal(j *grantJournal, sid *windows.SID) error {
	if j == nil || j.file == nil || j.blocked != nil || len(j.records) == 0 || j.state.workspace || sid == nil || sid.String() != j.records[0].SID {
		return fmt.Errorf("AppContainer selected recovery requires an owned journal without workspace inheritance")
	}
	for _, record := range j.records {
		if record.Grant != nil {
			if err := validateSelectedRecoveryACE(*record.Grant, sid); err != nil {
				return err
			}
		}
	}
	return nil
}

func journalRecoveryIdentity(j *grantJournal) (*windows.SID, *uint16, error) {
	if j == nil || j.file == nil || j.blocked != nil || len(j.records) == 0 {
		return nil, nil, fmt.Errorf("AppContainer recovery requires a complete owned journal")
	}
	if err := deriveSID.Find(); err != nil {
		return nil, nil, err
	}
	if err := deleteProfile.Find(); err != nil {
		return nil, nil, err
	}
	profile := j.records[0]
	name, err := windows.UTF16PtrFromString(profile.Profile)
	if err != nil {
		return nil, nil, err
	}
	var sid *windows.SID
	result, _, _ := deriveSID.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sid)))
	if err := hresult("derive recovered AppContainer identity", result); err != nil {
		return nil, nil, err
	}
	if sid.String() != profile.SID {
		windows.FreeSid(sid)
		return nil, nil, fmt.Errorf("AppContainer journal profile/SID binding mismatch")
	}
	return sid, name, nil
}

func finishJournalProfile(j *grantJournal, name *uint16) error {
	result, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(name)))
	runtime.KeepAlive(name)
	// A prior recovery may have deleted the profile before its completion record
	// was flushed. The name/SID binding was independently rederived above.
	const fileNotFound = 0x80070002
	const notFound = 0x80070490
	if uint32(result) != fileNotFound && uint32(result) != notFound {
		if err := hresult("delete recovered AppContainer profile", result); err != nil {
			return err
		}
	}
	_, err := j.append(journalRecord{Kind: "complete"})
	return err
}

func validateSelectedRecoveryACE(g journalGrant, sid *windows.SID) error {
	ace := g.ACE
	mask := uint32(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE)
	if g.Object.Directory {
		mask |= 0x0040
	}
	sidBytes := unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())
	if len(ace) != 8+len(sidBytes) || ace[0] != windows.ACCESS_ALLOWED_ACE_TYPE || ace[1] != 0 ||
		int(binary.LittleEndian.Uint16(ace[2:4])) != len(ace) || binary.LittleEndian.Uint32(ace[4:8]) != mask || !bytes.Equal(ace[8:], sidBytes) {
		return fmt.Errorf("AppContainer journal ACE ownership mismatch")
	}
	return nil
}

func recoverSelectedObject(h windows.Handle, id fileIdentity, target []byte, sid *windows.SID) error {
	lock, err := acquireObjectACL(id, 30000)
	if err != nil {
		return err
	}
	defer lock.Close()
	sd, err := readObjectSecurity(h)
	if err != nil {
		return err
	}
	acl, defaulted, err := sd.DACL()
	if err != nil {
		return err
	}
	_, aces, err := aclParts(acl)
	if err != nil {
		return err
	}
	matches := 0
	sidBytes := unsafe.Slice((*byte)(unsafe.Pointer(sid)), sid.Len())
	for _, ace := range aces {
		if bytes.Equal(ace, target) {
			matches++
		} else if bytes.Contains(ace, sidBytes) {
			return fmt.Errorf("AppContainer recovered profile ACE ownership changed")
		}
	}
	if matches == 0 {
		// The grant intent may precede an unexecuted write, or cleanup may have
		// completed before a crash prevented its release record from flushing.
		return nil
	}
	merged, err := editObjectACL(acl, target, false)
	if err != nil {
		return err
	}
	return writeObjectDACL(h, sd, merged, defaulted)
}
