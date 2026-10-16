package pty

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func sessionGroups(session int) []int {
	entries, e := os.ReadDir("/proc")
	if e != nil {
		return nil
	}
	groups := []int{}
	for _, entry := range entries {
		if _, e := strconv.Atoi(entry.Name()); e != nil {
			continue
		}
		data, e := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if e != nil {
			continue
		}
		// comm is parenthesized and can contain spaces and parentheses.
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 4 {
			continue
		}
		sid, _ := strconv.Atoi(fields[3])
		group, _ := strconv.Atoi(fields[2])
		if sid == session {
			groups = append(groups, group)
		}
	}
	return groups
}
