package winbox

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"

	"golang.org/x/sys/windows"
)

var errWriterFenceClosed = errors.New("AppContainer external writer fence is closed")

// workspaceWriterFence is a verified replacement for an opaque coordination
// guard. It enforces and checks namespace, reparse and ACL integrity of the
// directory chains an execution depends on:
//
//   - the volume root through the workspace root, and through the private
//     journal directory, are held as directory-data handles without delete
//     sharing, so the kernel rejects rename and delete of any pinned directory;
//   - every pinned directory is rechecked for identity, directory type, reparse
//     attribute and single link, and (except the workspace root until sealed)
//     for an unchanged DACL;
//   - the NTFS change journal is read from acquisition forward and any delete,
//     rename, reparse, hard-link or security record for a pinned identity is a
//     violation, even if the object's state was restored in between.
//
// Detection is sticky and fails closed: a violation, or any failure to observe
// (a lost or restarted change journal, an unreadable descriptor), makes Verify
// fail permanently. The fence does not freeze writers that hold SeBackup/
// SeRestore/SeTakeOwnership or run as the NTFS owner and can change a DACL
// without sharing conflicts; it proves they did not do so undetected while an
// execution held the fence. Descendants are covered by the workspace change
// monitor. Close releases only handles; it does not clear a violation.
type workspaceWriterFence struct {
	mu        sync.Mutex
	trees     []*workspaceTree // workspace chain, then journal directory chain.
	pinned    map[[16]byte]fenceRole
	objects   []fencePin
	volumes   []*fenceVolume
	violation error
	closed    bool
}

type fencePin struct {
	handle      windows.Handle
	id          fileIdentity
	role        fenceRole
	fingerprint []byte // Nil for the unsealed workspace root.
}

type fenceVolume struct {
	volume uint64
	source *workspaceChangeSource
	cursor workspaceChangeCursor
}

func acquireWorkspaceWriterFence(ctx context.Context, workspace, journalDirectory string) (_ *workspaceWriterFence, resultErr error) {
	f := &workspaceWriterFence{pinned: make(map[[16]byte]fenceRole)}
	defer func() {
		if resultErr != nil {
			f.release()
		}
	}()
	root, err := openWorkspaceRoot(ctx, workspace)
	if err != nil {
		return nil, err
	}
	f.trees = append(f.trees, root)
	journal, err := openJournalDirectory(ctx, journalDirectory, false)
	if err != nil {
		return nil, err
	}
	// The private-directory proof is retained by its tree; the fence owns it.
	f.trees = append(f.trees, journal.tree)
	journal.tree = nil
	for index, tree := range f.trees {
		for _, ancestor := range tree.ancestors {
			if err := f.pin(ancestor, fenceAncestor); err != nil {
				return nil, err
			}
		}
		role := fenceWorkspaceRoot
		if index == 1 {
			role = fenceJournal
		}
		if err := f.pin(tree.objects[0].handle, role); err != nil {
			return nil, err
		}
		// Open the change journal before taking the descriptor baseline, so a
		// writer racing acquisition is reported instead of absorbed.
		if err := f.watchVolume(tree.ancestors[0], tree.objects[0].id.Volume); err != nil {
			return nil, err
		}
	}
	for index := range f.objects {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if f.objects[index].role == fenceWorkspaceRoot {
			continue
		}
		if f.objects[index].fingerprint, err = descriptorFingerprint(f.objects[index].handle); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (f *workspaceWriterFence) pin(handle windows.Handle, role fenceRole) error {
	object, err := inspectTreeObject(handle, "")
	if err != nil || !object.directory {
		return fmt.Errorf("AppContainer writer fence requires ordinary directories: %w", errors.Join(err, requireDirectory(object)))
	}
	if existing, ok := f.pinned[object.id.ID]; ok {
		// A directory pinned by both chains keeps the stricter role.
		if existing == fenceWorkspaceRoot && role != fenceWorkspaceRoot {
			f.pinned[object.id.ID] = role
			for index := range f.objects {
				if f.objects[index].id == object.id {
					f.objects[index].role = role
				}
			}
		}
		return nil
	}
	f.pinned[object.id.ID] = role
	f.objects = append(f.objects, fencePin{handle: handle, id: object.id, role: role})
	return nil
}

func (f *workspaceWriterFence) watchVolume(anchor windows.Handle, volume uint64) error {
	for _, v := range f.volumes {
		if v.volume == volume {
			return nil
		}
	}
	source, err := openWorkspaceChangeSource(anchor, volume)
	if err != nil {
		return err
	}
	span, err := source.query()
	if err != nil {
		source.Close()
		return err
	}
	f.volumes = append(f.volumes, &fenceVolume{volume: volume, source: source, cursor: workspaceChangeCursor{JournalID: span.JournalID, Next: span.Next}})
	return nil
}

// descriptorFingerprint is the owner-independent kernel DACL, control bits and
// header bytes. It is compared byte-for-byte; no normalization is applied.
func descriptorFingerprint(handle windows.Handle) ([]byte, error) {
	sd, err := readObjectSecurity(handle)
	if err != nil {
		return nil, err
	}
	control, _, err := sd.Control()
	if err != nil {
		return nil, err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	_ = binary.Write(&out, binary.LittleEndian, uint16(control))
	if acl == nil {
		out.WriteString("null")
		return out.Bytes(), nil
	}
	header, aces, err := aclParts(acl)
	if err != nil {
		return nil, err
	}
	out.Write(header)
	for _, ace := range aces {
		_ = binary.Write(&out, binary.LittleEndian, uint32(len(ace)))
		out.Write(ace)
	}
	return out.Bytes(), nil
}

func (f *workspaceWriterFence) fail(err error) error {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if !errors.Is(err, errWriterFenceViolated) {
		err = fmt.Errorf("%w: cannot prove pinned directories unchanged: %v", errWriterFenceViolated, err)
	}
	f.violation = err
	return err
}

// Verify proves, as of its return, that no pinned directory was renamed,
// deleted, converted to a reparse point, linked or re-secured since acquisition.
// It is safe for concurrent use. Cancellation is not a violation.
func (f *workspaceWriterFence) Verify(ctx context.Context) error {
	if f == nil {
		return errWriterFenceClosed
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.violation != nil {
		return f.violation
	}
	if f.closed {
		return errWriterFenceClosed
	}
	return f.fail(f.verifyLocked(ctx))
}

func (f *workspaceWriterFence) verifyLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, pin := range f.objects {
		actual, err := inspectTreeObject(pin.handle, "")
		if err != nil {
			return fmt.Errorf("%w: pinned directory state: %v", errWriterFenceViolated, err)
		}
		if actual.id != pin.id || !actual.directory {
			return fmt.Errorf("%w: pinned directory identity changed", errWriterFenceViolated)
		}
		if pin.fingerprint != nil {
			current, err := descriptorFingerprint(pin.handle)
			if err != nil {
				return err
			}
			if !bytes.Equal(current, pin.fingerprint) {
				return fmt.Errorf("%w: pinned directory descriptor changed", errWriterFenceViolated)
			}
		}
	}
	for _, v := range f.volumes {
		span, err := v.source.query()
		if err != nil {
			return err
		}
		if err := span.contains(v.cursor); err != nil {
			return err
		}
		for v.cursor.Next < span.Next {
			if err := ctx.Err(); err != nil {
				return err
			}
			next, batch, err := v.source.read(v.cursor)
			if err != nil {
				return err
			}
			if next.Next <= v.cursor.Next {
				return errWorkspaceChangesLost
			}
			// Records of another volume's identities cannot collide: the
			// journal and pinned set are per volume.
			if err := inspectFenceChanges(batch, f.pinnedOn(v.volume)); err != nil {
				return err
			}
			v.cursor = next
		}
	}
	return nil
}

func (f *workspaceWriterFence) pinnedOn(volume uint64) map[[16]byte]fenceRole {
	if len(f.volumes) == 1 {
		return f.pinned
	}
	scoped := make(map[[16]byte]fenceRole)
	for _, pin := range f.objects {
		if pin.id.Volume == volume {
			scoped[pin.id.ID] = pin.role
		}
	}
	return scoped
}

// sealRoot is called after the workspace grant and tracking baseline. It first
// verifies everything, then adds the workspace root's own descriptor to the
// protected state; later security records on it are violations. The root is
// changed by this package only in grant admission and in cleanup, after the
// final verification.
func (f *workspaceWriterFence) sealRoot(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.violation != nil {
		return f.violation
	}
	if f.closed {
		return errWriterFenceClosed
	}
	if err := f.fail(f.verifyLocked(ctx)); err != nil {
		return err
	}
	for index := range f.objects {
		pin := &f.objects[index]
		if pin.role != fenceWorkspaceRoot {
			continue
		}
		fingerprint, err := descriptorFingerprint(pin.handle)
		if err != nil {
			return f.fail(err)
		}
		pin.fingerprint, pin.role = fingerprint, fenceAncestor
		f.pinned[pin.id.ID] = fenceAncestor
	}
	return nil
}

// bindChains requires the lifecycle's own workspace and journal-directory
// chains to be the pinned objects, directory by directory.
func (f *workspaceWriterFence) bindChains(workspace, journal *workspaceTree) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed || len(f.trees) != 2 {
		return errWriterFenceClosed
	}
	for index, actual := range []*workspaceTree{workspace, journal} {
		expected := f.trees[index]
		if actual == nil || len(actual.ancestors) != len(expected.ancestors) || len(actual.objects) == 0 {
			return fmt.Errorf("%w: lifecycle directory chain differs from the fenced chain", errWriterFenceViolated)
		}
		handles := append(append([]windows.Handle(nil), actual.ancestors...), actual.objects[0].handle)
		pinned := append(append([]windows.Handle(nil), expected.ancestors...), expected.objects[0].handle)
		for position := range handles {
			a, err := inspectTreeObject(handles[position], "")
			if err != nil {
				return err
			}
			e, err := inspectTreeObject(pinned[position], "")
			if err != nil {
				return err
			}
			if a.id != e.id {
				return f.fail(fmt.Errorf("%w: lifecycle directory chain differs from the fenced chain", errWriterFenceViolated))
			}
		}
	}
	return nil
}

func (f *workspaceWriterFence) release() {
	for _, v := range f.volumes {
		v.source.Close()
	}
	f.volumes = nil
	for index := len(f.trees) - 1; index >= 0; index-- {
		f.trees[index].Close()
	}
	f.trees, f.objects = nil, nil
}

// Close releases the pins. It is idempotent and never clears a violation.
func (f *workspaceWriterFence) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	f.release()
	return nil
}

// writerVerifier is implemented by guards that prove, rather than assume, the
// integrity of the external writer boundary. A lifecycle created with
// RequireWriterFence rejects any other guard.
type writerVerifier interface {
	io.Closer
	Verify(context.Context) error
	sealRoot(context.Context) error
	bindChains(workspace, journal *workspaceTree) error
}

// kernelWriterFencePrepare is the in-host fence factory for
// workspaceExecutionPrepare. It does not by itself supply host-crash survival
// or freeze external writers that can change a DACL without leaving a
// verifiable record.
func kernelWriterFencePrepare(ctx context.Context, options workspaceExecutionOptions) (io.Closer, error) {
	fence, err := acquireWorkspaceWriterFence(ctx, options.Workspace, filepath.Dir(options.Journal))
	if err != nil {
		return nil, err
	}
	return fence, nil
}
