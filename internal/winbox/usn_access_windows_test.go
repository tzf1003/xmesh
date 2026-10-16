package winbox

import (
	"errors"
	"os"
	"testing"

	"golang.org/x/sys/windows"
)

// volumeRootAnchor opens the root directory of the volume holding dir, the same
// kind of handle the workspace census retains as its anchor.
func volumeRootAnchor(t *testing.T, dir string) (windows.Handle, uint64) {
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
	return root, object.id.Volume
}

// An elevated account reads the journal through the raw volume device; any other
// account is refused with ErrChangeJournalAccess before any sandbox admission.
func Test_WindowsAppContainerChangeSourceAccess(t *testing.T) {
	if os.Getenv("XMESH_TEST_WINDOWS_SANDBOX") != "1" {
		t.Skip("set XMESH_TEST_WINDOWS_SANDBOX=1 on Windows")
	}
	anchor, volume := volumeRootAnchor(t, t.TempDir())
	source, err := openWorkspaceChangeSource(anchor, volume)
	if !windows.GetCurrentProcessToken().IsElevated() {
		if !errors.Is(err, ErrChangeJournalAccess) {
			t.Fatal("non-elevated account was not refused with ErrChangeJournalAccess:", err)
		}
		t.Log("ordinary account refused:", err)
		return
	}
	if err != nil {
		t.Fatal("elevated change source", err)
	}
	defer source.Close()
	span, err := source.query()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := source.read(workspaceChangeCursor{JournalID: span.JournalID, Next: span.Next}); err != nil {
		t.Fatal("elevated change source read", err)
	}
}
