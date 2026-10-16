package winbox

import (
	"context"
	"os"
	"path/filepath"
)

// workspaceExceeds reports whether the tree below root holds more than limit
// objects, counting root itself, every file and every directory. It stops as
// soon as the count passes the limit, so its cost is bounded by limit rather
// than by the size of the tree.
//
// It is an early, advisory refusal: admission takes a profile, a keeper and a
// writer fence, and a workspace such as a home directory would be refused only
// after all of that. The authoritative bound is the handle-based scan during
// grant, which also rejects reparse points; any error here (a vanished path, a
// denied directory) is left to that scan to report.
func workspaceExceeds(ctx context.Context, root string, limit int) bool {
	count := 1
	pending := []string{root}
	for len(pending) != 0 {
		directory := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		file, err := os.Open(directory)
		if err != nil {
			continue
		}
		for {
			if ctx.Err() != nil {
				file.Close()
				return false
			}
			entries, err := file.ReadDir(256)
			for _, entry := range entries {
				count++
				if count > limit {
					file.Close()
					return true
				}
				// Only real directories are followed. A symlink or junction is
				// not descended; the authoritative scan refuses it anyway.
				if entry.IsDir() {
					pending = append(pending, filepath.Join(directory, entry.Name()))
				}
			}
			if err != nil { // io.EOF ends a directory; any other error is left to the scan
				break
			}
		}
		file.Close()
	}
	return false
}
