package winbox

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// volumeRootAnchor opens the root directory of the volume holding dir, the same
// kind of handle the workspace census retains as its anchor.
func volumeRootAnchor(t *testing.T, dir string) (windows.Handle, uint64, string) {
	t.Helper()
	var buffer [windows.MAX_PATH + 1]uint16
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.GetVolumePathName(name, &buffer[0], uint32(len(buffer))); err != nil {
		t.Fatal(err)
	}
	root, err := windows.CreateFile(&buffer[0], windows.READ_CONTROL|windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(root) })
	object, err := inspectTreeObject(root, "")
	if err != nil {
		t.Fatal(err)
	}
	var guid [128]uint16
	n, err := windows.GetFinalPathNameByHandle(root, &guid[0], uint32(len(guid)), 1)
	if err != nil || n == 0 || n >= uint32(len(guid)) {
		t.Fatal("volume GUID path", err)
	}
	return root, object.id.Volume, windows.UTF16ToString(guid[:n])
}

// An elevated account reads the journal through the raw volume device. The same
// journal must also answer through the unprivileged volume-root route, which is
// the only route an ordinary account can have; a non-elevated account must either
// succeed or be refused with errChangeSourceAccess before any sandbox admission.
func Test_WindowsAppContainerChangeSourceAccess(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	anchor, volume, guid := volumeRootAnchor(t, t.TempDir())
	source, err := openWorkspaceChangeSource(anchor, volume)
	if err != nil {
		if windows.GetCurrentProcessToken().IsElevated() || !errors.Is(err, errChangeSourceAccess) {
			t.Fatal("change source", err)
		}
		t.Log("ordinary account refused with an actionable error:", err)
		return
	}
	defer source.Close()
	want, err := source.query()
	if err != nil {
		t.Fatal(err)
	}
	h, err := openVolumeRootForJournal(guid)
	if err != nil {
		t.Fatal("unprivileged volume-root route", err)
	}
	fallback := &workspaceChangeSource{handle: h}
	defer fallback.Close()
	got, err := fallback.query()
	if err != nil || got.JournalID != want.JournalID {
		t.Fatalf("volume-root route reports journal %x, device route %x (%v)", got.JournalID, want.JournalID, err)
	}
	if _, _, err := fallback.read(workspaceChangeCursor{JournalID: got.JournalID, Next: got.Next}); err != nil {
		t.Fatal("volume-root route read", err)
	}
}
