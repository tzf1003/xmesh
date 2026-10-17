package winbox

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// describeHelperExit reports how an exited sandbox helper ended: its exit
// status, the race runtime logs left under the GORACE log_path prefix in dir,
// and the failure reason the helper itself wrote next to its proof file.
func describeHelperExit(process windows.Handle, dir, prefix, proof string) string {
	var exit uint32
	exitErr := windows.GetExitCodeProcess(process, &exit)
	diagnostics := raceRuntimeLogs(dir, prefix)
	if reason, err := os.ReadFile(proof + ".fail"); err == nil {
		diagnostics = append(diagnostics, string(reason))
	}
	return fmt.Sprintf("exit %d (%v), race start failure %t, diagnostics %q", exit, exitErr, raceRuntimeStartFailed(exit, diagnostics...), diagnostics)
}

// descendantProcesses lists the live processes below this test process, with image name, pid
// and age, for failures where a terminal never reports its end.
func descendantProcesses() string {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return fmt.Sprintf("process snapshot: %v", err)
	}
	defer windows.CloseHandle(snapshot)
	parents := map[uint32]uint32{}
	names := map[uint32]string{}
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		parents[entry.ProcessID] = entry.ParentProcessID
		names[entry.ProcessID] = windows.UTF16ToString(entry.ExeFile[:])
	}
	below := func(pid uint32) bool {
		for depth := 0; depth < 32 && pid != 0; depth++ {
			if pid == uint32(os.Getpid()) {
				return true
			}
			pid = parents[pid]
		}
		return false
	}
	var lines []string
	for pid, name := range names {
		if pid != uint32(os.Getpid()) && below(pid) {
			lines = append(lines, fmt.Sprintf("%s(%d<-%d)", name, pid, parents[pid]))
		}
	}
	sort.Strings(lines)
	return strings.Join(lines, " ")
}
