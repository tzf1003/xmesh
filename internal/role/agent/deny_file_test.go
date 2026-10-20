package agent

import (
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/tzf1003/xmesh/internal/cwd"
)

func Test_DenyFileRetentionFollowsAgentConfiguration(t *testing.T) {
	path, store := configuration(t)
	root := filepath.Dir(path)
	files := []string{filepath.Join(root, "first.yaml"), filepath.Join(root, "second.yaml")}
	for _, file := range files {
		writeDenyList(t, file, root)
	}
	setFile := func(file string) {
		rewriteConfigFile(t, path, func(agent map[string]any) {
			agent["cwd_policy"] = map[string]any{"allow": []string{}, "deny_file": file}
		})
		store.Get()
	}
	churn := func(prefix string) {
		for i := 0; i < 80; i++ {
			cwd.RetainDenyFile(filepath.Join(root, fmt.Sprintf("%s-%d.yaml", prefix, i)))()
		}
	}
	load := func(file string) error {
		_, err := cwd.New(root, nil, cwd.Policy{DenyFile: file})
		return err
	}
	setFile(files[0])
	a, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if err := os.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	churn("first")
	if err := load(files[0]); err != nil {
		t.Fatalf("current deny file fallback was evicted: %v", err)
	}
	setFile(files[1])
	if err := load(files[1]); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(files[1]); err != nil {
		t.Fatal(err)
	}
	churn("second")
	if err := load(files[0]); err == nil {
		t.Fatal("previous configuration still retained its fallback")
	}
	if err := load(files[1]); err != nil {
		t.Fatalf("new deny file fallback was evicted: %v", err)
	}
	a.Close()
	churn("closed")
	if err := load(files[1]); err == nil {
		t.Fatal("closed agent still retained its fallback")
	}
}

func writeDenyList(t *testing.T, file string, entries ...string) {
	t.Helper()
	text := "# 禁止目录\n"
	if len(entries) == 0 {
		text += "[]\n"
	}
	for _, e := range entries {
		text += "- " + strconv.Quote(e) + "\n"
	}
	if err := os.WriteFile(file, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The deny file is part of the hot configuration: an edit applies to the next tool call without a
// restart, an edit that breaks it keeps the last good list and is reported, and fixing it clears the report.
func Test_DenyFileEditsApplyWithoutRestart(t *testing.T) {
	path, store := configuration(t)
	root := filepath.Dir(path)
	secret, open := filepath.Join(root, "secret"), filepath.Join(root, "open")
	for _, dir := range []string{secret, open} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	denyFile := filepath.Join(t.TempDir(), "cwd-deny.yaml")
	writeDenyList(t, denyFile)
	rewriteConfigFile(t, path, func(agent map[string]any) {
		// An empty allow list admits everything the deny list does not cover.
		agent["cwd_policy"] = map[string]any{"allow": []string{}, "deny_file": denyFile}
	})
	a, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	server := httptest.NewServer(a.HTTP(Local))
	t.Cleanup(server.Close)
	client := connect(t, server.URL+"/mcp")
	stat := func(dir string) bool { return !tool(t, client, "fs.stat", map[string]any{"path": dir}).IsError }

	if !stat(secret) || !stat(open) {
		t.Fatal("an empty deny list refused a directory")
	}
	writeDenyList(t, denyFile, secret)
	if stat(secret) || !stat(open) {
		t.Fatal("an added entry did not apply to the next call")
	}

	if err := os.WriteFile(denyFile, []byte("- \"unfinished"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stat(secret) || !stat(open) {
		t.Fatal("a broken deny file dropped the last good list")
	}
	info := object(t, tool(t, client, "sys.info", map[string]any{}))
	if info["policy_reload_failures"].(float64) < 1 {
		t.Fatal("the failure was not counted", info)
	}
	warned := false
	for _, w := range info["warnings"].([]any) {
		if s, _ := w.(string); s == "禁止目录文件当前无法加载，继续使用最近有效内容" {
			warned = true
		}
	}
	if !warned {
		t.Fatal("no warning for the broken deny file", info["warnings"])
	}

	writeDenyList(t, denyFile, open)
	if !stat(secret) || stat(open) {
		t.Fatal("the repaired list did not apply")
	}
	info = object(t, tool(t, client, "sys.info", map[string]any{}))
	for _, w := range info["warnings"].([]any) {
		if s, _ := w.(string); s == "禁止目录文件当前无法加载，继续使用最近有效内容" {
			t.Fatal("the warning outlived the repair")
		}
	}
}
