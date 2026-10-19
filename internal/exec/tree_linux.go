//go:build linux

package exec

import (
	"os"
	"strconv"
	"strings"
)

func treeSnapshot() ([]treeIdentity, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	result := make([]treeIdentity, 0, len(entries))
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile("/proc/" + entry.Name() + "/stat")
		if err != nil {
			continue
		}
		end := strings.LastIndexByte(string(data), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) < 20 || fields[0] == "Z" {
			continue
		}
		parent, _ := strconv.Atoi(fields[1])
		group, _ := strconv.Atoi(fields[2])
		result = append(result, treeIdentity{pid, parent, group, fields[19]})
	}
	return result, nil
}
