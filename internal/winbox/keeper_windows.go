package winbox

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// RunGuardKeeper runs in a fresh internal helper with explicitly inherited
// request, reply and host-process handles plus the namespace handles listed in
// its initialization frame. While the host lives it serves hold/journal/status/
// release over the inherited pipes. After host exit it keeps every handle, and
// serves one adoption client at a time on a private named pipe, until a release
// whose journal precondition it has verified itself.
func RunGuardKeeper(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("guard keeper requires inherited request, reply and host handles")
	}
	var handles [3]windows.Handle
	seen := make(map[windows.Handle]bool)
	for i, arg := range args {
		n, err := strconv.ParseUint(arg, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid guard keeper bootstrap handle")
		}
		h, err := inheritedSupervisorHandle(n, seen)
		if err != nil {
			return err
		}
		handles[i] = h
	}
	input, output := os.NewFile(uintptr(handles[0]), "keeper-request"), os.NewFile(uintptr(handles[1]), "keeper-reply")
	defer input.Close()
	defer output.Close()
	defer windows.CloseHandle(handles[2])
	for _, h := range handles[:2] {
		kind, err := windows.GetFileType(h)
		if err != nil || kind != windows.FILE_TYPE_PIPE {
			return fmt.Errorf("guard keeper channel is not an inherited pipe: %v", err)
		}
	}
	pid, err := windows.GetProcessId(handles[2])
	if err != nil || pid == windows.GetCurrentProcessId() {
		return fmt.Errorf("guard keeper host identity is unavailable: %v", err)
	}
	var initial keeperRequest
	if err := readSupervisorFrame(input, &initial); err != nil {
		return err
	}
	if err := initial.validate(); err != nil || initial.Operation != "init" {
		return fmt.Errorf("invalid guard keeper initialization: %v", err)
	}
	k := &keeperActor{host: handles[2], init: *initial.Init}
	if err := k.bind(seen); err != nil {
		return err
	}
	defer k.closeHolds()
	// The named event makes the lease key unique among live keepers. It lives
	// exactly as long as this process, so its existence tells a recovery owner
	// that guard handles may still be held for this workspace.
	name, err := windows.UTF16PtrFromString(keeperEventName(k.init.Key))
	if err != nil {
		return err
	}
	event, err := windows.CreateEvent(nil, 1, 0, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if event != 0 {
			windows.CloseHandle(event)
		}
		err = fmt.Errorf("AppContainer workspace already has a guard keeper")
	}
	if err != nil {
		_ = writeSupervisorFrame(output, keeperReply{Sequence: 1, Error: err.Error()})
		return err
	}
	defer windows.CloseHandle(event)
	if err := writeSupervisorFrame(output, keeperReply{Sequence: 1, State: keeperStateArmed}); err != nil {
		return k.orphaned()
	}
	requests := make(chan keeperRequest)
	go func() {
		defer close(requests)
		for sequence := uint64(2); sequence <= maxJournalRecords; sequence++ {
			var request keeperRequest
			if err := readSupervisorFrame(input, &request); err != nil {
				return
			}
			if err := request.validate(); err != nil || request.Sequence != sequence || request.Operation == "init" {
				return
			}
			requests <- request
		}
	}()
	for request := range requests {
		reply, released, err := k.apply(request, keeperStateArmed)
		reply.Sequence = request.Sequence
		if err != nil {
			reply.Error = truncatedKeeperError(err)
		}
		if writeSupervisorFrame(output, reply) != nil {
			break
		}
		if released {
			return nil
		}
	}
	// A closed or invalid channel is not evidence that the host stopped: it may
	// still append to the journal or create objects. Wait for the pinned process.
	return k.orphaned()
}

func truncatedKeeperError(err error) string {
	message := err.Error()
	if len(message) > 8192 {
		message = message[:8192]
	}
	return message
}

type keeperHeldHandle struct {
	handle windows.Handle
	keeperHold
}

type keeperActor struct {
	host    windows.Handle
	init    keeperInit
	held    []keeperHeldHandle
	journal *journalObject
}

func (k *keeperActor) bind(seen map[windows.Handle]bool) error {
	for _, hold := range k.init.Holds {
		h, err := inheritedSupervisorHandle(hold.Handle, seen)
		if err != nil {
			k.closeHolds()
			return err
		}
		k.held = append(k.held, keeperHeldHandle{handle: h, keeperHold: hold})
		actual, err := inspectTreeObject(h, "")
		if err != nil || (journalObject{Volume: actual.id.Volume, ID: actual.id.ID, Directory: actual.directory}) != hold.Object {
			k.closeHolds()
			return fmt.Errorf("guard keeper inherited object identity mismatch: %v", err)
		}
	}
	return nil
}

func (k *keeperActor) closeHolds() {
	for _, h := range k.held {
		windows.CloseHandle(h.handle)
	}
	k.held = nil
}

func (k *keeperActor) describe(state string) keeperReply {
	reply := keeperReply{State: state, PID: windows.GetCurrentProcessId()}
	if k.journal != nil {
		journal := *k.journal
		reply.Journal = &journal
	}
	for _, h := range k.held {
		reply.Held = append(reply.Held, keeperHeld{Object: h.Object, Role: h.Role})
	}
	return reply
}

func (k *keeperActor) apply(r keeperRequest, state string) (keeperReply, bool, error) {
	switch r.Operation {
	case "status":
		return k.describe(state), false, nil
	case "hold":
		// The host duplicated this identity directly into the keeper. Consume it
		// on every rejection as well.
		h := windows.Handle(r.Hold.Handle)
		accepted := false
		defer func() {
			if !accepted {
				windows.CloseHandle(h)
			}
		}()
		if state != keeperStateArmed || uint64(h) != r.Hold.Handle || len(k.held) >= maxKeeperHolds {
			return keeperReply{}, false, fmt.Errorf("guard keeper admission unavailable")
		}
		for _, existing := range k.held {
			if existing.Object == r.Hold.Object {
				return keeperReply{}, false, fmt.Errorf("guard keeper object is already held")
			}
		}
		actual, err := inspectTreeObject(h, "")
		if err != nil || (journalObject{Volume: actual.id.Volume, ID: actual.id.ID, Directory: actual.directory}) != r.Hold.Object {
			return keeperReply{}, false, fmt.Errorf("guard keeper hold identity mismatch: %v", err)
		}
		k.held = append(k.held, keeperHeldHandle{handle: h, keeperHold: *r.Hold})
		accepted = true
		return keeperReply{}, false, nil
	case "journal":
		if state != keeperStateArmed || k.journal != nil {
			return keeperReply{}, false, fmt.Errorf("guard keeper journal binding unavailable")
		}
		journal := *r.Journal
		k.journal = &journal
		return keeperReply{}, false, nil
	case "release":
		if err := k.releaseError(); err != nil {
			return keeperReply{}, false, err
		}
		return keeperReply{}, true, nil
	}
	return keeperReply{}, false, fmt.Errorf("unsupported guard keeper operation")
}

// releaseError reads the journal independently. The journal file is exclusively
// owned by its host, supervisor, or recovery owner while an obligation can still
// change it, so a busy file is simply not releasable.
func (k *keeperActor) releaseError() error {
	path, err := windows.UTF16PtrFromString(k.init.Journal)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(path, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return keeperReleaseError(k.journal, false, journalObject{}, nil)
	}
	if err != nil {
		return fmt.Errorf("guard keeper journal unavailable: %w", err)
	}
	file := os.NewFile(uintptr(h), "keeper-journal")
	defer file.Close()
	object, err := inspectTreeObject(h, "")
	if err != nil || object.directory {
		return fmt.Errorf("guard keeper journal is not an ordinary file: %v", err)
	}
	j, loadErr := loadGrantJournal(file)
	if j == nil {
		return fmt.Errorf("guard keeper journal unreadable: %w", loadErr)
	}
	return keeperReleaseError(k.journal, true, journalObject{Volume: object.id.Volume, ID: object.id.ID}, j)
}

// orphaned keeps every handle after the host has stopped. The loop ends only
// through a verified release; errors never drop the namespace holds.
func (k *keeperActor) orphaned() error {
	for {
		result, err := windows.WaitForSingleObject(k.host, windows.INFINITE)
		if err == nil && result == windows.WAIT_OBJECT_0 {
			break
		}
		time.Sleep(time.Second)
	}
	for {
		pipe, err := k.createPipe()
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		released := k.serve(pipe)
		windows.CloseHandle(pipe)
		if released {
			return nil
		}
	}
}

func (k *keeperActor) createPipe() (windows.Handle, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return 0, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + user.User.Sid.String() + ")(A;;GA;;;SY)")
	if err != nil {
		return 0, err
	}
	attributes := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	name, err := windows.UTF16PtrFromString(keeperPipeName(k.init.Key))
	if err != nil {
		return 0, err
	}
	// One instance, created first: a squatter cannot impersonate this keeper and
	// a second adoption client is refused while one is connected.
	return windows.CreateNamedPipe(name, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, 1<<16, 1<<16, 0, &attributes)
}

// serve handles clients on one pipe instance until a release succeeds. A client
// that disconnects without releasing leaves the keeper orphaned and adoptable.
func (k *keeperActor) serve(pipe windows.Handle) bool {
	for {
		if err := windows.ConnectNamedPipe(pipe, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
			time.Sleep(100 * time.Millisecond)
			_ = windows.DisconnectNamedPipe(pipe)
			continue
		}
		conn := keeperPipe{pipe}
		released := false
		for sequence := uint64(1); sequence <= maxJournalRecords && !released; sequence++ {
			var request keeperRequest
			if err := readSupervisorFrame(conn, &request); err != nil {
				break
			}
			var reply keeperReply
			var err error
			if verr := request.validate(); verr != nil || request.Sequence != sequence || (request.Operation != "status" && request.Operation != "release") {
				err = fmt.Errorf("invalid guard keeper adoption request")
			} else {
				reply, released, err = k.apply(request, keeperStateOrphaned)
			}
			reply.Sequence = request.Sequence
			if err != nil {
				reply.Error = truncatedKeeperError(err)
			}
			if writeSupervisorFrame(conn, reply) != nil {
				// The client may have vanished after a release was decided; the
				// verified release stands either way.
				break
			}
		}
		if released {
			_ = windows.FlushFileBuffers(pipe)
			return true
		}
		_ = windows.DisconnectNamedPipe(pipe)
	}
}

// keeperPipe adapts a synchronous pipe handle without owning its lifetime.
type keeperPipe struct{ handle windows.Handle }

func (p keeperPipe) Read(b []byte) (int, error) {
	var n uint32
	err := windows.ReadFile(p.handle, b, &n, nil)
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) {
		return int(n), io.EOF
	}
	if err != nil {
		return int(n), err
	}
	if n == 0 && len(b) != 0 {
		return 0, io.EOF
	}
	return int(n), nil
}

func (p keeperPipe) Write(b []byte) (int, error) {
	var n uint32
	err := windows.WriteFile(p.handle, b, &n, nil)
	return int(n), err
}
