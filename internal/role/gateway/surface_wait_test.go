package gateway

import "testing"

// A listing of the delegate group waits for the other devices. The local device serves these tools only while
// delegation is on there, so a remote device may be the one that brings them (CTR-DLG-06).
// conformance: CTR-DLG-06
func Test_CTRDLG06_ListingDelegateWaitsForOtherDevices(t *testing.T) {
	v := &view{g: &Gateway{}}
	v.g.surface.Store(&Surface{Mode: SurfaceUnified, ListedGroups: []string{"delegate"}})
	if !v.waitsForOthers() {
		t.Fatal("a unified listing of the delegate group does not wait for the other devices")
	}
	v.g.surface.Store(&Surface{Mode: SurfaceUnified, PinnedTools: []string{"delegate.run"}})
	if !v.waitsForOthers() {
		t.Fatal("a pinned delegate tool does not wait for the other devices")
	}
	v.g.surface.Store(&Surface{Mode: SurfaceUnified, ListedGroups: []string{"exec"}})
	if v.waitsForOthers() {
		t.Fatal("a unified listing of a built-in group waits for the other devices")
	}
}
