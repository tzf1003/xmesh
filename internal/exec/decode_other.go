//go:build !windows

package exec

func systemDecode([]byte) (string, bool) { return "", false }
