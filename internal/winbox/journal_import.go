package winbox

import "fmt"

// A trusted coordinator freezes every source namespace/reparse/ACL writer before
// this complete subtree census, and keeps that guard through the namespace move.
// Recording the census precedes the move, so subsequent descendant changes are
// tracked even if the subtree or a child leaves the workspace before a checkpoint.
type journalImport struct {
	Root    journalObject         `json:"root"`
	Objects []journalObject       `json:"objects"`
	Cursor  workspaceChangeCursor `json:"cursor"`
}

func planWorkspaceImport(proof journalImport, root journalObject, known map[journalObject]bool, deleted map[journalObject]int64, maxObjects int) ([]journalObject, error) {
	if maxObjects < 1 || maxObjects > 10000 || len(known) > maxObjects || len(proof.Objects) == 0 || len(proof.Objects) > maxObjects || proof.Root != proof.Objects[0] || (!proof.Root.Directory && len(proof.Objects) != 1) || proof.Root == root || proof.Cursor.JournalID == 0 || proof.Cursor.Next < 0 {
		return nil, fmt.Errorf("invalid AppContainer import census or limits")
	}
	identities := make(map[[16]byte]journalObject, len(known))
	for object := range known {
		identities[object.ID] = object
	}
	seen := make(map[[16]byte]bool, len(proof.Objects))
	var added []journalObject
	for _, object := range proof.Objects {
		if !validJournalObject(object) || object.Volume != root.Volume || seen[object.ID] || object == root {
			return nil, fmt.Errorf("invalid or repeated AppContainer import identity")
		}
		seen[object.ID] = true
		if _, retired := deleted[object]; retired {
			return nil, fmt.Errorf("AppContainer import contains a deleted identity")
		}
		if previous, exists := identities[object.ID]; exists {
			if previous != object {
				return nil, fmt.Errorf("AppContainer import identity type or volume mismatch")
			}
			continue
		}
		if len(known)+len(added) >= maxObjects {
			return nil, fmt.Errorf("AppContainer import census exceeds %d objects", maxObjects)
		}
		added = append(added, object)
	}
	return added, nil
}

// Full import snapshots can overlap prior observations. Recovery retains each
// physical identity once while the import record preserves the complete census.
func (r journalRecord) discoveredObjects() []journalObject {
	if r.Import != nil {
		return r.Import.Objects
	}
	if r.UnknownImports != nil {
		return r.UnknownImports
	}
	return r.CreatedObjects
}
