package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBackgroundExecutableRejectsConsole(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := validateBackgroundExecutable(executable); err == nil {
		t.Fatal("console executable accepted as background kernel")
	}
	if err := requireBackgroundExecutable(filepath.Join(t.TempDir(), "missing.exe")); err == nil {
		t.Fatal("missing background kernel accepted")
	}
}

func TestBackgroundTaskMigration(t *testing.T) {
	binary := os.Getenv("XMESH_TEST_WINDOWS_BINARY")
	if binary == "" {
		t.Skip("requires built CLI and sibling xmesh-background.exe")
	}
	background := filepath.Join(filepath.Dir(binary), "xmesh-background.exe")
	if err := requireBackgroundExecutable(background); err != nil {
		t.Fatal(err)
	}
	var escaped strings.Builder
	// Registration snapshots carry arbitrary XML settings; preserve them verbatim.
	escaped.WriteString(strings.NewReplacer("&", "&amp;", "<", "&lt;").Replace(filepath.Join(filepath.Dir(binary), "xmesh.exe")))
	document := `<Task><Settings><Unknown>keep</Unknown></Settings><Actions><Exec><Command>` + escaped.String() + `</Command><Arguments>serve --state-dir &quot;C:\节点 &amp; state&quot;</Arguments><WorkingDirectory>C:\work</WorkingDirectory></Exec></Actions></Task>`
	called := 0
	register := func(path string) error {
		called++
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		task, err := parseUserTask(taskDocumentText(raw))
		if err != nil || task.Actions.Exec.Command != background || task.Actions.Exec.Arguments != `serve --state-dir "C:\节点 & state"` || !strings.Contains(taskDocumentText(raw), `<Unknown>keep</Unknown>`) {
			t.Fatalf("task changed during background migration: %+v %v", task, err)
		}
		return nil
	}
	if err := ensureTaskBackground([]byte(document), register); err != nil || called != 1 {
		t.Fatal(called, err)
	}
	updated, err := replaceTaskCommand(document, background)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureTaskBackground([]byte(updated), register); err != nil || called != 1 {
		t.Fatal("background task unnecessarily re-registered", called, err)
	}
}
