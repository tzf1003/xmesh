package winbox

import (
	"errors"
	"testing"
)

func Test_WriterFenceChangeClassification(t *testing.T) {
	ancestor, root, journal, other := [16]byte{1}, [16]byte{2}, [16]byte{3}, [16]byte{4}
	pinned := map[[16]byte]fenceRole{ancestor: fenceAncestor, root: fenceWorkspaceRoot, journal: fenceJournal}
	directory := uint32(usnDirectory)
	tests := []struct {
		name    string
		changes []workspaceChange
		bad     bool
	}{
		{"unpinned identities are ignored", []workspaceChange{{ID: other, Reason: usnReasonDelete | usnReasonReparse | usnReasonSecurity, Attributes: directory}}, false},
		{"benign pinned records", []workspaceChange{{ID: ancestor, Reason: 0x8000 | usnReasonClose, Attributes: directory}, {ID: journal, Reason: usnReasonClose, Attributes: directory}}, false},
		{"root security is validated by its grant owner", []workspaceChange{{ID: root, Reason: usnReasonSecurity, Attributes: directory}}, false},
		{"ancestor security", []workspaceChange{{ID: ancestor, Reason: usnReasonSecurity, Attributes: directory}}, true},
		{"journal directory security", []workspaceChange{{ID: journal, Reason: usnReasonSecurity | usnReasonClose, Attributes: directory}}, true},
		{"ancestor rename", []workspaceChange{{ID: ancestor, Reason: usnReasonRenameOld, Attributes: directory}}, true},
		{"ancestor rename target", []workspaceChange{{ID: ancestor, Reason: usnReasonRenameNew, Attributes: directory}}, true},
		{"root delete", []workspaceChange{{ID: root, Reason: usnReasonDelete, Attributes: directory}}, true},
		{"root reparse", []workspaceChange{{ID: root, Reason: usnReasonReparse, Attributes: directory}}, true},
		{"journal link", []workspaceChange{{ID: journal, Reason: usnReasonHardLink, Attributes: directory}}, true},
		{"pinned identity reused by a file", []workspaceChange{{ID: ancestor, Reason: usnReasonClose}}, true},
	}
	for _, test := range tests {
		err := inspectFenceChanges(test.changes, pinned)
		if (err != nil) != test.bad || (err != nil && !errors.Is(err, errWriterFenceViolated)) {
			t.Errorf("%s: %v", test.name, err)
		}
	}
	// Sealing the root puts its own descriptor under the same rule.
	pinned[root] = fenceAncestor
	if err := inspectFenceChanges([]workspaceChange{{ID: root, Reason: usnReasonSecurity, Attributes: directory}}, pinned); err == nil {
		t.Error("sealed root accepted a security change")
	}
}
