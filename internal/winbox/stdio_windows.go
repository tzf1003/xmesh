package winbox

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// Streams are caller-owned capabilities. Duplication preserves their granted
// rights without changing the original handles' inheritance flags or lifetime.
// A creation-time handle list admits exactly the three duplicates to the child.
type executionStdio struct {
	stdin  windows.Handle
	stdout windows.Handle
	stderr windows.Handle
}

func duplicateExecutionStdio(stdio *executionStdio) (_ []windows.Handle, resultErr error) {
	if stdio == nil {
		return nil, nil
	}
	handles := make([]windows.Handle, 3)
	defer func() {
		if resultErr != nil {
			closeExecutionStdio(handles)
		}
	}()
	for index, source := range []windows.Handle{stdio.stdin, stdio.stdout, stdio.stderr} {
		if source == 0 || source == windows.InvalidHandle {
			return nil, fmt.Errorf("AppContainer standard stream %d requires a valid handle", index)
		}
		kind, err := windows.GetFileType(source)
		if err != nil || (kind != windows.FILE_TYPE_DISK && kind != windows.FILE_TYPE_PIPE && kind != windows.FILE_TYPE_CHAR) {
			return nil, fmt.Errorf("AppContainer standard stream %d requires a file or pipe: type=%d error=%v", index, kind, err)
		}
		if err := windows.DuplicateHandle(windows.CurrentProcess(), source, windows.CurrentProcess(), &handles[index], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return nil, fmt.Errorf("duplicate AppContainer standard stream %d: %w", index, err)
		}
	}
	return handles, nil
}

func closeExecutionStdio(handles []windows.Handle) {
	for _, handle := range handles {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
	}
}
