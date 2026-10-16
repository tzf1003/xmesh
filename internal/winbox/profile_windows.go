// Package winbox supplies Windows AppContainer identities for L2 process launch.
package winbox

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	userenv       = windows.NewLazySystemDLL("userenv.dll")
	createProfile = userenv.NewProc("CreateAppContainerProfile")
	deleteProfile = userenv.NewProc("DeleteAppContainerProfile")
	folderPath    = userenv.NewProc("GetAppContainerFolderPath")
	deriveSID     = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
)

const securityCapabilitiesAttribute = 0x00020009

type securityCapabilities struct {
	AppContainerSID *windows.SID
	Capabilities    *windows.SIDAndAttributes
	CapabilityCount uint32
	Reserved        uint32
}

// Profile owns a fresh AppContainer identity and its private data directory.
// Callers must reap launched processes and release object grants before closing.
type Profile struct {
	mu                          sync.Mutex
	name                        *uint16
	sid                         *windows.SID
	directory                   string
	grants                      int
	journal                     *grantJournal
	deleted                     bool
	processes                   *executionProcesses
	standaloneProcessAttributes bool
	processesConfirmed          bool                    // Protected by mu; includes durable exit confirmation.
	reclaiming                  bool                    // Blocks launch admission throughout workspace reclamation.
	workspaceClosing            bool                    // Keeps admission sealed after tracked handle handoff, including retries.
	workspaceMonitor            *workspaceChangeMonitor // Pins the active observation owner through Stop.
}

func hresult(operation string, result uintptr) error {
	if int32(result) < 0 {
		return fmt.Errorf("%s: HRESULT %#08x", operation, uint32(result))
	}
	return nil
}

// New creates an exclusive random identity. It grants no optional capabilities,
// including network access. Existing identities are never reused.
func New() (*Profile, error) {
	return newProfile(nil)
}

// A journal owner remains private until profile and ACL recovery integration is
// complete. Callers retain and close the journal independently of Profile.Close.
func newProfile(journal *grantJournal) (*Profile, error) {
	for _, procedure := range []*windows.LazyProc{createProfile, deleteProfile, folderPath} {
		if err := procedure.Find(); err != nil {
			return nil, err
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString("xmesh-l2-" + hex.EncodeToString(random[:]))
	if err != nil {
		return nil, err
	}
	if journal != nil {
		if len(journal.records) != 0 {
			return nil, fmt.Errorf("AppContainer profile requires an empty owned journal")
		}
		if err := deriveSID.Find(); err != nil {
			return nil, err
		}
		var expected *windows.SID
		result, _, _ := deriveSID.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&expected)))
		if err := hresult("DeriveAppContainerSidFromAppContainerName", result); err != nil {
			return nil, err
		}
		sid := expected.String()
		windows.FreeSid(expected)
		if _, err := journal.append(journalRecord{Kind: "profile", Profile: windows.UTF16PtrToString(name), SID: sid}); err != nil {
			return nil, err
		}
	}
	p := &Profile{name: name, journal: journal}
	result, _, _ := createProfile.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, uintptr(unsafe.Pointer(&p.sid)))
	if err := hresult("CreateAppContainerProfile", result); err != nil {
		return nil, err
	}
	if journal != nil && p.sid.String() != journal.records[0].SID {
		return nil, errors.Join(fmt.Errorf("created AppContainer profile does not match journal intent"), p.Close())
	}
	var path *uint16
	sid, err := windows.UTF16PtrFromString(p.sid.String())
	if err == nil {
		result, _, _ = folderPath.Call(uintptr(unsafe.Pointer(sid)), uintptr(unsafe.Pointer(&path)))
		err = hresult("GetAppContainerFolderPath", result)
	}
	if err == nil {
		p.directory = windows.UTF16PtrToString(path)
		if !filepath.IsAbs(p.directory) {
			err = fmt.Errorf("AppContainer profile directory is not absolute")
		}
	}
	if path != nil {
		windows.CoTaskMemFree(unsafe.Pointer(path))
	}
	if err != nil {
		cleanup := p.Close()
		if cleanup != nil {
			return nil, fmt.Errorf("%w; profile cleanup: %v", err, cleanup)
		}
		return nil, err
	}
	return p, nil
}

func (p *Profile) Directory() string { return p.directory }

// Apply adds the capability-free AppContainer token attributes to a process
// launch. The attribute list retains the structure until CreateProcess returns.
// Keep the profile alive until all processes using its identity have exited.
func (p *Profile) Apply(attributes *windows.ProcThreadAttributeListContainer) error {
	return p.applyProcess(attributes, nil)
}

func (p *Profile) applyProcess(attributes *windows.ProcThreadAttributeListContainer, owner *executionProcesses) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.processes != owner {
		return fmt.Errorf("AppContainer process launch belongs to another execution owner")
	}
	if p.sid == nil || p.deleted || p.reclaiming || p.workspaceClosing || attributes == nil {
		return fmt.Errorf("AppContainer profile or attribute list unavailable")
	}
	if p.journal != nil && (p.journal.file == nil || p.journal.blocked != nil || p.journal.state.workspaceReleased || p.journal.state.complete) {
		return fmt.Errorf("AppContainer journal is unavailable for process launch")
	}
	if p.journal != nil && owner == nil {
		return fmt.Errorf("AppContainer journaled launch requires an execution process owner")
	}
	capabilities := &securityCapabilities{AppContainerSID: p.sid}
	err := attributes.Update(securityCapabilitiesAttribute, unsafe.Pointer(capabilities), unsafe.Sizeof(*capabilities))
	if err == nil && owner == nil {
		// Standalone attributes can outlive this call and create untracked
		// processes. Such a profile cannot later adopt a managed owner.
		p.standaloneProcessAttributes = true
	}
	return err
}

// Close deletes the owned profile and its data. A deletion failure can be
// retried after releasing the remaining process or file handles.
func (p *Profile) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sid == nil {
		return nil
	}
	if p.processes != nil {
		return fmt.Errorf("AppContainer profile retains an execution process owner")
	}
	if p.journal != nil {
		if err := requireJournalProcessesReaped(p.journal); err != nil {
			return err
		}
	}
	if p.grants != 0 {
		return fmt.Errorf("AppContainer profile retains %d object grants", p.grants)
	}
	if p.journal != nil && (len(p.journal.state.pending) != 0 || (p.journal.state.workspace && !p.journal.state.workspaceReleased)) {
		return fmt.Errorf("AppContainer journal retains recovery obligations")
	}
	if !p.deleted {
		result, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(p.name)))
		if err := hresult("DeleteAppContainerProfile", result); err != nil {
			return err
		}
		p.deleted = true
	}
	if p.journal != nil && !p.journal.state.complete {
		if _, err := p.journal.append(journalRecord{Kind: "complete"}); err != nil {
			return err
		}
	}
	err := windows.FreeSid(p.sid)
	p.sid = nil
	return err
}
