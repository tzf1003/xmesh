package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupCLIPlanInspectAndStructuredErrors(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	workspace := t.TempDir()
	input, _ := json.Marshal(map[string]any{"schema_version": 1, "purpose": "local", "node_name": "cli-node", "workspace": workspace, "permission": "readonly", "clients": []string{}, "network": map[string]any{"mode": "lan"}})
	var output bytes.Buffer
	if e := setupCommandIO([]string{"plan", "--input", "-", "--root", root, "--json"}, bytes.NewReader(input), &output); e != nil {
		t.Fatal(e)
	}
	var response struct {
		OK   bool `json:"ok"`
		Data struct {
			ID     string `json:"id"`
			Config string `json:"config"`
		} `json:"data"`
	}
	if e := json.Unmarshal(output.Bytes(), &response); e != nil || !response.OK || len(response.Data.ID) != 32 {
		t.Fatal(output.String(), e)
	}
	output.Reset()
	if e := setupCommandIO([]string{"inspect", "--root", root, "--json"}, nil, &output); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(output.String(), "suggested_node_name") {
		t.Fatal(output.String())
	}
	output.Reset()
	if e := setupCommandIO([]string{"plan", "--input", "-", "--root", root, "--json"}, strings.NewReader(`{"schema_version":1,"unknown":true}`), &output); e == nil {
		t.Fatal("unknown fields accepted")
	}
	if !strings.Contains(output.String(), `"ok":false`) {
		t.Fatal(output.String())
	}
	output.Reset()
	if e := agentHelp(&output); e != nil || !strings.Contains(output.String(), "setup.verify") {
		t.Fatal(output.String(), e)
	}
}
