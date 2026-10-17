//go:build !windows

package toolerr

func osFields(error) []any { return nil }
