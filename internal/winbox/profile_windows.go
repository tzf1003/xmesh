// Package winbox supplies Windows AppContainer identities for L2 process launch.
package winbox

import (
	"crypto/rand"
	"encoding/hex"
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
	mu        sync.Mutex
	name      *uint16
	sid       *windows.SID
	directory string
	grants    int
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
	p := &Profile{name: name}
	result, _, _ := createProfile.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)), 0, 0, uintptr(unsafe.Pointer(&p.sid)))
	if err := hresult("CreateAppContainerProfile", result); err != nil {
		return nil, err
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
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sid == nil || attributes == nil {
		return fmt.Errorf("AppContainer profile or attribute list unavailable")
	}
	capabilities := &securityCapabilities{AppContainerSID: p.sid}
	return attributes.Update(securityCapabilitiesAttribute, unsafe.Pointer(capabilities), unsafe.Sizeof(*capabilities))
}

// Close deletes the owned profile and its data. A deletion failure can be
// retried after releasing the remaining process or file handles.
func (p *Profile) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sid == nil {
		return nil
	}
	if p.grants != 0 {
		return fmt.Errorf("AppContainer profile retains %d object grants", p.grants)
	}
	result, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(p.name)))
	if err := hresult("DeleteAppContainerProfile", result); err != nil {
		return err
	}
	err := windows.FreeSid(p.sid)
	p.sid = nil
	return err
}
