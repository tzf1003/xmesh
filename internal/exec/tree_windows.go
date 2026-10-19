//go:build windows

package exec

// startCommand binds the whole tree to a kill-on-close Job before it runs.
func watchTree(int) (func() error, error) { return func() error { return nil }, nil }
