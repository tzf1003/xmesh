package winbox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"golang.org/x/sys/windows"
)

const maxRecoveryJournals = 256

var errJournalRecoveryBusy = errors.New("AppContainer recovery journal is busy")

type journalRecoveryResult struct {
	Name  string
	State string // retained, busy, complete, recovered
	Err   error
}

// A detached manifest for the trusted execution coordinator. Volumes includes
// every recorded volume, even if its explicit obligations were acknowledged.
type journalRecoveryInfo struct {
	Profile        string
	SID            string
	Workspace      *journalWorkspace
	Objects        []journalObject
	CreatedObjects []journalObject
	Volumes        []uint64
}

type journalRecoveryLease struct {
	Guard   io.Closer
	Volumes map[uint64]windows.Handle
}

// Prepare must reap all profile processes, coordinate namespace/reparse/ACL
// writers, and prove a complete created-object census. Its non-nil Guard holds
// these conditions and volume handles through completion; a returned lease is
// closed even when Prepare fails. Prepare must not create journals in this
// directory or reenter recovery. Production execution remains gated until its
// coordinator can establish these conditions.
type journalRecoveryPrepare func(context.Context, journalRecoveryInfo) (*journalRecoveryLease, error)

// Recover a bounded private directory while coordinating journal creation and
// other sweeps across processes. Census shape errors stop before any callback or
// mutation. Individual busy/damaged/invalid journals stay intact and report an
// error; independent valid journals may complete. Completed files are retained
// and revalidated on subsequent sweeps. The caller can archive them separately
// while admission and recovery are quiescent.
func recoverJournalDirectory(ctx context.Context, path string, maxJournals, maxObjects int, prepare journalRecoveryPrepare) (results []journalRecoveryResult, resultErr error) {
	if maxJournals < 1 || maxJournals > maxRecoveryJournals || maxObjects < 1 || maxObjects > 10000 || prepare == nil {
		return nil, fmt.Errorf("AppContainer recovery requires bounded limits and a process/filesystem coordinator")
	}
	directory, err := openJournalDirectory(ctx, path, false)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	root := directory.tree.objects[0]
	lock, err := acquireJournalDirectory(ctx, root.id)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	var entries []directoryEntry
	err = enumerateDirectory(ctx, root.handle, func(entry directoryEntry) error {
		if len(entries) >= maxJournals {
			return fmt.Errorf("AppContainer recovery directory exceeds %d journals", maxJournals)
		}
		if entry.attributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 || !strings.HasSuffix(entry.name, ".journal") {
			return fmt.Errorf("AppContainer recovery directory contains an unexpected entry")
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	results = make([]journalRecoveryResult, len(entries))
	journals := make([]*grantJournal, len(entries))
	for i, entry := range entries {
		results[i] = journalRecoveryResult{Name: entry.name, State: "retained"}
	}
	defer func() {
		for i, j := range journals {
			if j != nil {
				results[i].Err = errors.Join(results[i].Err, j.Close())
			}
			if results[i].Err != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("%s: %w", results[i].Name, results[i].Err))
			}
		}
	}()
	var totalBytes int64
	totalRecords := 0
	owners := make(map[string]int)
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		j, err := openDirectoryGrantJournal(ctx, directory, entry.name, false)
		journals[i] = j
		results[i].Err = err
		if errors.Is(err, windows.STATUS_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			results[i].State = "busy"
			results[i].Err = errors.Join(errJournalRecoveryBusy, err)
		}
		if j == nil {
			continue
		}
		actual, inspectErr := inspectTreeObject(windows.Handle(j.file.Fd()), "")
		if inspectErr == nil {
			inspectErr = verifyDirectoryEntry(root.id.Volume, entry, actual)
		}
		results[i].Err = errors.Join(results[i].Err, inspectErr)
		totalBytes += j.bytes
		totalRecords += len(j.records)
		if totalBytes > maxJournalBytes || totalRecords > maxJournalRecords {
			return results, fmt.Errorf("AppContainer recovery batch exceeds aggregate journal limits")
		}
		if len(j.records) == 0 {
			results[i].Err = errors.Join(results[i].Err, fmt.Errorf("AppContainer recovery journal has no profile intent"))
			continue
		}
		// Include verified damaged prefixes in duplicate detection. A second
		// record for the same profile cannot independently establish ownership.
		for _, key := range []string{"name:" + j.records[0].Profile, "sid:" + j.records[0].SID} {
			if prior, exists := owners[key]; exists {
				conflict := fmt.Errorf("AppContainer profile appears in multiple recovery journals")
				results[i].Err = errors.Join(results[i].Err, conflict)
				results[prior].Err = errors.Join(results[prior].Err, conflict)
			} else {
				owners[key] = i
			}
		}
		if results[i].Err == nil {
			sid, _, validation := journalRecoveryIdentity(j)
			if validation == nil {
				if j.state.workspace {
					validation = validateWorkspaceJournal(j, sid)
				} else {
					validation = validateSelectedJournal(j, sid)
				}
				windows.FreeSid(sid)
			}
			results[i].Err = validation
		}
	}
	for i, j := range journals {
		if results[i].Err != nil || j == nil {
			continue
		}
		if j.state.complete {
			results[i].State = "complete"
			continue
		}
		if err := ctx.Err(); err != nil {
			return results, err
		}
		lease, err := prepare(ctx, recoveryJournalInfo(j))
		if err == nil && (lease == nil || lease.Guard == nil) {
			err = fmt.Errorf("AppContainer recovery coordinator did not retain a guard")
		}
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			if j.state.workspace {
				err = recoverWorkspaceJournal(j, maxObjects)
			} else {
				err = recoverSelectedJournalVolumes(j, lease.Volumes)
			}
			if err == nil {
				results[i].State = "recovered"
			}
		}
		if lease != nil && lease.Guard != nil {
			err = errors.Join(err, lease.Guard.Close())
		}
		results[i].Err = err
	}
	return results, nil
}

func recoveryJournalInfo(j *grantJournal) journalRecoveryInfo {
	info := journalRecoveryInfo{Profile: j.records[0].Profile, SID: j.records[0].SID}
	volumes := make(map[uint64]bool)
	if j.state.workspace {
		w := *j.records[1].Workspace
		w.Objects = append([]journalObject(nil), w.Objects...)
		info.Workspace = &w
		volumes[w.Root.Volume] = true
	}
	for _, r := range j.records {
		info.CreatedObjects = append(info.CreatedObjects, r.CreatedObjects...)
		if r.Grant != nil {
			info.Objects = append(info.Objects, r.Grant.Object)
			volumes[r.Grant.Object.Volume] = true
		}
	}
	for volume := range volumes {
		info.Volumes = append(info.Volumes, volume)
	}
	sort.Slice(info.Volumes, func(i, k int) bool { return info.Volumes[i] < info.Volumes[k] })
	return info
}
