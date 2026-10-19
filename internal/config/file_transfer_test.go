package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func Test_FileTransferLimits(t *testing.T) {
	for value, want := range map[string]int64{"": 1 << 30, "1GiB": 1 << 30, "2GiB": 2 << 30, "512MiB": 512 << 20, "1B": 1, "unlimited": 0, "9223372036854775807B": 9223372036854775807} {
		if got, err := (FileTransfer{value}).Limit(); err != nil || got != want {
			t.Fatal(value, got, err)
		}
	}
	for _, value := range []string{"0", "0GiB", "-1", "1GB", "1.5GiB", "9223372036854775808B", "999999999999999999999999TiB"} {
		if _, err := (FileTransfer{value}).Limit(); err == nil {
			t.Fatal("invalid file limit accepted", value)
		}
	}
}

func Test_FileTransferLimitHotReload(t *testing.T) {
	body, err := os.ReadFile("../../docs/contracts/schemas/examples/xmesh.config.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	json.Unmarshal(body, &doc)
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"2GiB", "unlimited", "512MiB"} {
		doc["agent"].(map[string]any)["fs"] = map[string]any{"max_file_size": value}
		body, _ = json.Marshal(doc)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
		if got := s.Get().Agent.FS.MaxFileSize; got != value {
			t.Fatal("limit not applied", got)
		}
		if restart, failures := s.State(); restart || failures != 0 {
			t.Fatal("file limit requires restart", restart, failures)
		}
	}
}
