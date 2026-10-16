package winbox

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// keeperAdoption is a recovery owner's connection to an orphaned keeper. The
// keeper stays alive and keeps every namespace hold whether or not this
// connection survives. Closing the connection therefore never shortens
// protection; the keeper is released only by releaseKeeper after the journal
// is closed and the keeper has itself verified durable completion. An owner
// that dies mid-recovery leaves the keeper adoptable by the next owner.
type keeperAdoption struct {
	pipe     windows.Handle
	process  windows.Handle
	sequence uint64
	state    keeperReply
}

// adoptGuardKeeper connects to the keeper holding key. errKeeperAbsent means
// no keeper process exists; errKeeperNotReady means one exists but is still
// serving a live host or another adoption client.
func adoptGuardKeeper(ctx context.Context, key journalObject) (_ *keeperAdoption, resultErr error) {
	if !validKeeperKey(key) {
		return nil, fmt.Errorf("invalid guard keeper lease key")
	}
	event, err := windows.UTF16PtrFromString(keeperEventName(key))
	if err != nil {
		return nil, err
	}
	probe, err := windows.OpenEvent(windows.SYNCHRONIZE, false, event)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return nil, errKeeperAbsent
	}
	if err != nil {
		return nil, fmt.Errorf("probe AppContainer guard keeper: %w", err)
	}
	windows.CloseHandle(probe)
	pipeName, err := windows.UTF16PtrFromString(keeperPipeName(key))
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	var pipe windows.Handle
	for {
		// Anonymous impersonation: the keeper learns nothing it could use.
		pipe, err = windows.CreateFile(pipeName, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
			windows.SECURITY_SQOS_PRESENT|windows.SECURITY_ANONYMOUS, 0)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) && !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, fmt.Errorf("connect AppContainer guard keeper: %w", err)
		}
		if time.Now().After(deadline) {
			return nil, errors.Join(errKeeperNotReady, err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
	a := &keeperAdoption{pipe: pipe}
	defer func() {
		if resultErr != nil {
			a.Close()
		}
	}()
	reply, err := a.exchange(ctx, keeperRequest{Operation: "status"})
	if err != nil {
		return nil, err
	}
	// The pipe must belong to the process that answered, and the process must be
	// the one that holds the lease key.
	var serverPID uint32
	if err := windows.GetNamedPipeServerProcessId(pipe, &serverPID); err != nil || serverPID == 0 || serverPID != reply.PID {
		return nil, fmt.Errorf("guard keeper process identity mismatch: %v", err)
	}
	if reply.State != keeperStateOrphaned {
		return nil, errKeeperNotReady
	}
	holdsRoot := false
	for _, held := range reply.Held {
		if held.Role == keeperRoleRoot && held.Object == key {
			holdsRoot = true
		}
	}
	if !holdsRoot {
		return nil, fmt.Errorf("guard keeper does not hold the requested workspace root")
	}
	a.process, err = windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, serverPID)
	if err != nil {
		return nil, err
	}
	a.state = reply
	return a, nil
}

// exchange performs one ordered request. Context cancellation cancels the
// pending synchronous pipe I/O; the connection is unusable afterwards.
func (a *keeperAdoption) exchange(ctx context.Context, request keeperRequest) (keeperReply, error) {
	var reply keeperReply
	if a.pipe == 0 {
		return reply, fmt.Errorf("guard keeper adoption is closed")
	}
	a.sequence++
	request.Sequence = a.sequence
	if err := request.validate(); err != nil {
		return reply, err
	}
	done := make(chan error, 1)
	conn := keeperPipe{a.pipe}
	go func() {
		err := writeSupervisorFrame(conn, request)
		if err == nil {
			err = readSupervisorFrame(conn, &reply)
		}
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil && reply.Sequence != request.Sequence {
			err = fmt.Errorf("guard keeper response sequence mismatch")
		}
		if err == nil && reply.Error != "" {
			err = fmt.Errorf("guard keeper: %s", reply.Error)
		}
		return reply, err
	case <-ctx.Done():
		_ = windows.CancelIoEx(a.pipe, nil)
		<-done
		return keeperReply{}, ctx.Err()
	}
}

// Close ends the connection without releasing the keeper.
func (a *keeperAdoption) Close() error {
	var result error
	if a.pipe != 0 {
		result = errors.Join(result, windows.CloseHandle(a.pipe))
		a.pipe = 0
	}
	if a.process != 0 {
		result = errors.Join(result, windows.CloseHandle(a.process))
		a.process = 0
	}
	return result
}

// release asks the orphaned keeper to exit and confirms that it did. The keeper
// refuses unless it has verified the bound journal's durable completion, so this
// must run after the journal's recovery owner has closed it.
func (a *keeperAdoption) release(ctx context.Context) error {
	if _, err := a.exchange(ctx, keeperRequest{Operation: "release"}); err != nil {
		return err
	}
	for {
		result, err := windows.WaitForSingleObject(a.process, 25)
		if err != nil {
			return err
		}
		if result == windows.WAIT_OBJECT_0 {
			var code uint32
			if err := windows.GetExitCodeProcess(a.process, &code); err != nil || code != 0 {
				return fmt.Errorf("guard keeper exited abnormally after release: %d %v", code, err)
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

// keeperRecoveryPrepare adopts the keeper covering a workspace journal. When no
// keeper exists (for example after a reboot or logoff killed every process) the
// optional fallback must establish the writer freeze by other means; without one
// the journal is left intact. The returned guard only disconnects: the keeper
// remains until releaseCompletedKeepers verifies completion.
func keeperRecoveryPrepare(fallback journalRecoveryPrepare) journalRecoveryPrepare {
	return func(ctx context.Context, info journalRecoveryInfo) (*journalRecoveryLease, error) {
		if info.Workspace == nil {
			if fallback != nil {
				return fallback(ctx, info)
			}
			return nil, fmt.Errorf("guard keepers cover workspace journals only")
		}
		a, err := adoptGuardKeeper(ctx, info.Workspace.Root)
		if errors.Is(err, errKeeperAbsent) && fallback != nil {
			return fallback(ctx, info)
		}
		if err != nil {
			return nil, err
		}
		return &journalRecoveryLease{Guard: a}, nil
	}
}

// releaseCompletedKeepers frees keepers whose journals are durably complete.
// It runs after the sweep has closed its journals, so a keeper can read each
// journal. Journals still held by others, incomplete journals and keepers that
// refuse stay protected, and are reported.
func releaseCompletedKeepers(ctx context.Context, path string, maxJournals int) (resultErr error) {
	if maxJournals < 1 || maxJournals > maxRecoveryJournals {
		return fmt.Errorf("AppContainer keeper release requires a bounded journal count")
	}
	directory, err := openJournalDirectory(ctx, path, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	root := directory.tree.objects[0]
	lock, err := acquireJournalDirectory(ctx, root.id)
	if err != nil {
		return err
	}
	defer lock.Close()
	var names []string
	err = enumerateDirectory(ctx, root.handle, func(entry directoryEntry) error {
		if len(names) >= maxJournals {
			return fmt.Errorf("AppContainer keeper release directory exceeds %d journals", maxJournals)
		}
		if strings.HasSuffix(entry.name, ".journal") && entry.attributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) == 0 {
			names = append(names, entry.name)
		}
		return nil
	})
	if err != nil {
		return err
	}
	var keys []journalObject
	for _, name := range names {
		j, openErr := openDirectoryGrantJournal(ctx, directory, name, false)
		if j == nil {
			// A journal that a live host, supervisor or other owner still holds is
			// exactly one whose keeper must stay.
			continue
		}
		if openErr == nil && j.state.complete && j.state.workspace {
			for _, record := range j.records {
				if record.Workspace != nil {
					keys = append(keys, record.Workspace.Root)
					break
				}
			}
		}
		resultErr = errors.Join(resultErr, j.Close())
	}
	for _, key := range keys {
		a, err := adoptGuardKeeper(ctx, key)
		if errors.Is(err, errKeeperAbsent) {
			continue
		}
		if err != nil {
			resultErr = errors.Join(resultErr, err)
			continue
		}
		resultErr = errors.Join(resultErr, a.release(ctx), a.Close())
	}
	return resultErr
}

// recoverJournalDirectoryWithKeepers runs the bounded recovery sweep with every
// workspace journal guarded by its surviving keeper, then releases the keepers
// of journals that are durably complete.
func recoverJournalDirectoryWithKeepers(ctx context.Context, path string, maxJournals, maxObjects int, fallback journalRecoveryPrepare) ([]journalRecoveryResult, error) {
	results, err := recoverJournalDirectory(ctx, path, maxJournals, maxObjects, keeperRecoveryPrepare(fallback))
	return results, errors.Join(err, releaseCompletedKeepers(ctx, path, maxJournals))
}
