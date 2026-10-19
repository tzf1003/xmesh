package main

import (
	"encoding/json"
	"html"
	"os"
	"path/filepath"
	"testing"
)

func Test_ServiceNameResolvesRenamedNode(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte(cliConfig), 0600); err != nil {
		t.Fatal(err)
	}
	add := func(name string) {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"Label": name, "ProgramArguments": []string{"/Applications/xmesh.app/Contents/Resources/bin/xmesh", "serve", "--service", "--config", configPath}})
		if err := os.WriteFile(filepath.Join(dir, name+".plist"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	add("xmesh-old-node")
	if got, err := findUserServiceName(dir, "xmesh-cli-node", os.ReadFile); err != nil || got != "xmesh-old-node" {
		t.Fatalf("%s %v", got, err)
	}
	if got, err := findUserServiceName(dir, "xmesh-other", os.ReadFile); err != nil || got != "xmesh-other" {
		t.Fatalf("%s %v", got, err)
	}
	add("xmesh-second-old-node")
	if _, err := findUserServiceName(dir, "xmesh-cli-node", os.ReadFile); err == nil {
		t.Fatal("ambiguous services were accepted")
	}
	add("xmesh-cli-node")
	if got, err := findUserServiceName(dir, "xmesh-cli-node", os.ReadFile); err != nil || got != "xmesh-cli-node" {
		t.Fatalf("%s %v", got, err)
	}
}

func Test_ServiceNameReadsLaunchAgentXML(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "node & service.yaml")
	if err := os.WriteFile(file, []byte(cliConfig), 0600); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict>
<key>Label</key><string>xmesh-old-node</string>
<key>ProgramArguments</key><array><string>/Applications/xmesh.app/Contents/Resources/bin/xmesh</string>
<string>serve</string><string>--service</string><string>--config</string><string>` + html.EscapeString(file) + `</string></array>
</dict></plist>`
	if err := os.WriteFile(filepath.Join(dir, "xmesh-old-node.plist"), []byte(plist), 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := findUserServiceName(dir, "xmesh-cli-node", readLaunchAgent); err != nil || got != "xmesh-old-node" {
		t.Fatalf("%s %v", got, err)
	}
}
