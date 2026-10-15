package winbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
	"unsafe"

	"github.com/tzf1003/xmesh/internal/winjob"
	"golang.org/x/sys/windows"
)

// RunProcessSupervisor runs in a fresh internal helper with explicitly inherited
// pipe, host, journal and namespace handles. It owns Job creation and observation
// before lending assignment handles to the host. Resource tables stay in the
// Agent. The helper has no listener or separately installed service.
func RunProcessSupervisor(args []string) error {
	if len(args) != 3 {
		return fmt.Errorf("supervisor requires inherited request, reply and host handles")
	}
	var handles [3]windows.Handle
	seen := make(map[windows.Handle]bool)
	for i, arg := range args {
		n, err := strconv.ParseUint(arg, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid supervisor bootstrap handle")
		}
		h, err := inheritedSupervisorHandle(n, seen)
		if err != nil {
			return err
		}
		handles[i] = h
	}
	input, output := os.NewFile(uintptr(handles[0]), "supervisor-request"), os.NewFile(uintptr(handles[1]), "supervisor-reply")
	defer input.Close()
	defer output.Close()
	defer windows.CloseHandle(handles[2])
	for _, h := range handles[:2] {
		kind, err := windows.GetFileType(h)
		if err != nil || kind != windows.FILE_TYPE_PIPE {
			return fmt.Errorf("supervisor channel is not an inherited pipe: %v", err)
		}
	}
	pid, err := windows.GetProcessId(handles[2])
	if err != nil || pid == windows.GetCurrentProcessId() {
		return fmt.Errorf("supervisor host identity is unavailable: %v", err)
	}
	var initial supervisorRequest
	if err := readSupervisorFrame(input, &initial); err != nil {
		return err
	}
	if err := initial.validate(); err != nil || initial.Operation != "init" {
		return fmt.Errorf("invalid supervisor initialization: %v", err)
	}
	s := &supervisorActor{host: handles[2], init: *initial.Init}
	if err := s.bind(seen); err != nil {
		return err
	}
	defer s.closeBindings()
	defer s.closeJobs()
	if err := writeSupervisorFrame(output, supervisorReply{Sequence: 1}); err != nil {
		return s.disconnected(err)
	}
	for sequence := uint64(2); sequence <= maxJournalRecords; sequence++ {
		var request supervisorRequest
		if err := readSupervisorFrame(input, &request); err != nil {
			return s.disconnected(err)
		}
		if err := request.validate(); err != nil || request.Sequence != sequence || request.Operation == "init" {
			return s.disconnected(fmt.Errorf("invalid supervisor request transition: %v", err))
		}
		if result, err := windows.WaitForSingleObject(s.host, 0); err != nil || result != uint32(windows.WAIT_TIMEOUT) {
			return s.disconnected(fmt.Errorf("supervisor host stopped: result=%d error=%v", result, err))
		}
		reply, err := s.apply(request)
		reply.Sequence = sequence
		if err != nil {
			reply.Error = err.Error()
			if len(reply.Error) > 8192 {
				reply.Error = reply.Error[:8192]
			}
		}
		if err := writeSupervisorFrame(output, reply); err != nil {
			return s.disconnected(err)
		}
		if request.Operation == "close" && reply.Error == "" {
			return nil
		}
	}
	return s.disconnected(fmt.Errorf("supervisor request limit"))
}

func inheritedSupervisorHandle(value uint64, seen map[windows.Handle]bool) (windows.Handle, error) {
	h := windows.Handle(value)
	if value == 0 || uint64(h) != value || h == windows.InvalidHandle || seen[h] {
		return 0, fmt.Errorf("invalid or repeated supervisor handle")
	}
	var flags uint32
	ok, _, callErr := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation").Call(uintptr(h), uintptr(unsafe.Pointer(&flags)))
	if ok == 0 || flags&windows.HANDLE_FLAG_INHERIT == 0 {
		return 0, fmt.Errorf("supervisor handle was not inherited: %v", callErr)
	}
	if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		return 0, err
	}
	seen[h] = true
	return h, nil
}

type supervisorActor struct {
	host    windows.Handle
	init    supervisorInit
	journal *os.File
	guards  []windows.Handle
	jobs    []executionJob
	sealed  bool
	reaped  bool
}

func (s *supervisorActor) bind(seen map[windows.Handle]bool) error {
	// Adopt only verified inherited handles; the original file object preserves
	// its exclusive sharing and directory handles preserve namespace bindings.
	for i, guard := range append([]supervisorGuard{s.init.Journal}, s.init.Guards...) {
		h, err := inheritedSupervisorHandle(guard.Handle, seen)
		if err != nil {
			s.closeBindings()
			return err
		}
		if i == 0 {
			s.journal = os.NewFile(uintptr(h), "supervisor-journal")
		} else {
			s.guards = append(s.guards, h)
		}
		actual, err := inspectTreeObject(h, "")
		if err != nil || (journalObject{Volume: actual.id.Volume, ID: actual.id.ID, Directory: actual.directory}) != guard.Object {
			s.closeBindings()
			return fmt.Errorf("supervisor inherited object identity mismatch: %v", err)
		}
		if i <= 1 {
			user, err := windows.GetCurrentProcessToken().GetTokenUser()
			if err != nil {
				s.closeBindings()
				return err
			}
			security, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err == nil {
				inheritance := byte(0)
				if i == 1 {
					inheritance = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
				}
				err = validateRecoverySecurity(security, user.User.Sid, inheritance)
			}
			if err != nil {
				s.closeBindings()
				return err
			}
		}
	}
	return nil
}

func (s *supervisorActor) closeBindings() {
	if s.journal != nil {
		s.journal.Close()
		s.journal = nil
	}
	for _, h := range s.guards {
		windows.CloseHandle(h)
	}
	s.guards = nil
}

func (s *supervisorActor) apply(r supervisorRequest) (supervisorReply, error) {
	var reply supervisorReply
	switch r.Operation {
	case "new-job":
		if s.sealed || len(s.jobs) >= s.init.Limit {
			return reply, fmt.Errorf("supervisor Job admission closed or at limit")
		}
		job, err := winjob.New()
		if err != nil {
			return reply, err
		}
		census, err := winjob.NewProcessCensus(job)
		if err != nil {
			return reply, errors.Join(err, windows.CloseHandle(job))
		}
		s.jobs = append(s.jobs, executionJob{handle: job, census: census})
		var lent windows.Handle
		err = windows.DuplicateHandle(windows.CurrentProcess(), job, s.host, &lent, 0, false, windows.DUPLICATE_SAME_ACCESS)
		if err != nil {
			return reply, err
		}
		return supervisorReply{Job: uint32(len(s.jobs)), Handle: uint64(lent)}, nil
	case "retain":
		// The host duplicates this identity directly into the pinned supervisor
		// process. Always consume it, including rejected membership/admission.
		h := windows.Handle(r.Handle)
		defer windows.CloseHandle(h)
		if uint64(h) != r.Handle || s.sealed || r.Job > uint32(len(s.jobs)) {
			return reply, fmt.Errorf("supervisor root admission unavailable")
		}
		return reply, s.jobs[r.Job-1].census.RetainProcess(context.Background(), h)
	case "reap":
		return reply, s.reap(r.ExitCode)
	case "close":
		if !s.reaped {
			return reply, fmt.Errorf("supervisor requires verified reaping before close")
		}
		// Keep observation alive through delivery of the close acknowledgement.
		// A failed write still enters host-exit confirmation with every census.
		return reply, nil
	}
	return reply, fmt.Errorf("unsupported supervisor operation")
}

func (s *supervisorActor) reap(exitCode uint32) error {
	if s.reaped {
		return nil
	}
	s.sealed, s.reaped = true, false
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var result error
	for _, job := range s.jobs {
		result = errors.Join(result, windows.TerminateJobObject(job.handle, exitCode))
	}
	for _, job := range s.jobs {
		result = errors.Join(result, job.census.WaitEmpty(ctx))
	}
	result = errors.Join(result, ctx.Err())
	s.reaped = result == nil
	return result
}

func (s *supervisorActor) closeJobs() error {
	var result error
	for i := range s.jobs {
		job := &s.jobs[i]
		result = errors.Join(result, job.census.Close())
		if job.handle != 0 {
			if err := windows.CloseHandle(job.handle); err != nil {
				result = errors.Join(result, err)
			} else {
				job.handle = 0
			}
		}
	}
	return result
}

func (s *supervisorActor) disconnected(channelErr error) error {
	// EOF can precede the host's exit signal. No journal access is permitted
	// while the pinned host can still append. Keep exclusive file/namespace and
	// all census handles until host exit; uncertain helper failure leaves intent.
	reapErr := s.reap(1)
	result, err := windows.WaitForSingleObject(s.host, windows.INFINITE)
	if err != nil || result != windows.WAIT_OBJECT_0 {
		return errors.Join(channelErr, reapErr, fmt.Errorf("supervisor host exit unconfirmed: %d %v", result, err))
	}
	// Host-side borrowed Job handles can admit another process between channel
	// failure and host exit. Reconfirm from the live census after the pinned
	// host has exited, even if the first termination already appeared complete.
	s.reaped = false
	reapErr = s.reap(1)
	if reapErr != nil {
		return errors.Join(channelErr, reapErr)
	}
	j, err := loadGrantJournal(s.journal)
	if err != nil {
		return errors.Join(channelErr, err)
	}
	if s.init.Intent > uint64(len(j.records)) {
		return fmt.Errorf("supervisor execution intent is absent")
	}
	intent := j.records[s.init.Intent-1]
	if intent.Kind != "execution" || intent.ExecutionID != s.init.ExecutionID {
		return fmt.Errorf("supervisor execution intent binding mismatch")
	}
	if j.state.executions[s.init.Intent] {
		_, err = j.append(journalRecord{Kind: "execution-reaped", ExecutionSequence: s.init.Intent})
	} else {
		err = requireJournalProcessesReaped(j)
	}
	return err
}
