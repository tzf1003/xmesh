package cwd

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestDenyFileCacheRetainsActiveFallbackAndFailureTotal(t *testing.T) {
	root := t.TempDir()
	secret := mustDir(t, filepath.Join(root, "secret"))
	file := filepath.Join(root, "active.yaml")
	writeDeny(t, file, secret)
	release := RetainDenyFile(file)
	defer release()
	if !refused(t, Policy{DenyFile: file}, root, secret) {
		t.Fatal("active deny list was not loaded")
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	before := DenyFailures()
	for i := 0; i < inactiveDenyFileLimit+10; i++ {
		_, _ = loadDenyFile(filepath.Join(root, fmt.Sprintf("inactive-%d.yaml", i)))
	}
	if !refused(t, Policy{DenyFile: file}, root, secret) {
		t.Fatal("active fallback was evicted")
	}
	if got := DenyFailures() - before; got != inactiveDenyFileLimit+11 {
		t.Fatalf("failure total after eviction = %d", got)
	}
	denyFiles.Lock()
	inactive := 0
	for _, st := range denyFiles.byPath {
		if st.refs == 0 {
			inactive++
		}
	}
	denyFiles.Unlock()
	if inactive > inactiveDenyFileLimit {
		t.Fatalf("cached %d inactive paths", inactive)
	}
	release()
	for i := 0; i < inactiveDenyFileLimit+1; i++ {
		_, _ = loadDenyFile(filepath.Join(root, fmt.Sprintf("next-%d.yaml", i)))
	}
	denyFiles.Lock()
	_, retained := denyFiles.byPath[file]
	denyFiles.Unlock()
	if retained {
		t.Fatal("released path remained after newer paths filled the cache")
	}
}

func writeDeny(t *testing.T, file string, entries ...string) {
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

func mustDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func refused(t *testing.T, policy Policy, root, dir string) bool {
	t.Helper()
	r, err := New(root, nil, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, e := r.Resolve(dir, "", Existing)
	return e != nil
}

// An edit to the deny file applies to the next resolver without a restart: an added entry is
// refused, a removed one is allowed again, and the empty allow list stays open to everything else.
func TestDenyFileEditsApplyToTheNextResolver(t *testing.T) {
	root := t.TempDir()
	a := mustDir(t, filepath.Join(root, "a"))
	b := mustDir(t, filepath.Join(root, "b"))
	file := filepath.Join(t.TempDir(), "cwd-deny.yaml")
	policy := Policy{DenyFile: file}

	writeDeny(t, file, a)
	if !refused(t, policy, root, a) || refused(t, policy, root, b) {
		t.Fatal("first list not applied")
	}
	writeDeny(t, file, a, b)
	if !refused(t, policy, root, b) {
		t.Fatal("an added entry was not applied")
	}
	writeDeny(t, file, b)
	if refused(t, policy, root, a) || !refused(t, policy, root, b) {
		t.Fatal("a removed entry was not applied")
	}
	writeDeny(t, file)
	if refused(t, policy, root, b) {
		t.Fatal("the explicit empty list did not open the directory")
	}
	if DenyFailing(file) {
		t.Fatal("a valid file reported as failing")
	}
}

// A file that stops loading keeps the list that last loaded and is reported, once per distinct
// failure, until it loads again. Nothing in between opens the boundary.
func TestDenyFileKeepsTheLastGoodListWhileBroken(t *testing.T) {
	root := t.TempDir()
	secret := mustDir(t, filepath.Join(root, "secret"))
	other := mustDir(t, filepath.Join(root, "other"))
	file := filepath.Join(t.TempDir(), "cwd-deny.yaml")
	policy := Policy{DenyFile: file}
	writeDeny(t, file, secret)
	if !refused(t, policy, root, secret) {
		t.Fatal("list not applied")
	}
	failures := DenyFailures()

	for name, content := range map[string]string{
		"not a list":    "key: value\n",
		"unfinished":    "- \"" + secret,
		"truncated":     "",
		"comments only": "# 禁止目录\n",
		"not strings":   "- 1\n- [2]\n",
	} {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if !refused(t, policy, root, secret) {
			t.Fatalf("%s: the last good list was dropped", name)
		}
		if refused(t, policy, root, other) {
			t.Fatalf("%s: an unlisted directory was refused", name)
		}
		if !DenyFailing(file) {
			t.Fatalf("%s: failure not reported", name)
		}
	}
	if got := DenyFailures() - failures; got != 5 {
		t.Fatalf("5 distinct bad files counted %d failures", got)
	}
	// The same bad content is not counted again.
	_ = refused(t, policy, root, secret)
	if got := DenyFailures() - failures; got != 5 {
		t.Fatalf("a repeated read counted again: %d", got)
	}

	// Deleting the file keeps the list too.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if !refused(t, policy, root, secret) || !DenyFailing(file) {
		t.Fatal("a deleted file dropped the list or went unreported")
	}

	// A good file clears the report and replaces the list.
	writeDeny(t, file, other)
	if refused(t, policy, root, secret) || !refused(t, policy, root, other) {
		t.Fatal("the repaired file was not applied")
	}
	if DenyFailing(file) {
		t.Fatal("the report did not clear")
	}
}

// With no earlier list to fall back on, a file that cannot load is an error, so a node never starts
// with an unreadable deny file as if it had none.
func TestDenyFileNeverLoadedIsAnError(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "cwd-deny.yaml")
	if _, err := New(root, nil, Policy{DenyFile: file}); err == nil {
		t.Fatal("missing file accepted")
	}
	if !DenyFailing(file) {
		t.Fatal("missing file not reported")
	}
	writeDeny(t, file)
	if _, err := New(root, nil, Policy{DenyFile: file}); err != nil {
		t.Fatal(err)
	}
	if DenyFailing(file) {
		t.Fatal("report did not clear")
	}
}

func Test_StalledDenyReadDoesNotBlockPolicyRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stalled.yaml")
	replacement := filepath.Join(t.TempDir(), "replacement.yaml")
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = loadDenyFileWith(path, func(string) ([]byte, error) { close(entered); <-release; return []byte("[]"), nil })
	}()
	<-entered
	retained := make(chan struct{})
	go func() {
		keep := RetainDenyFile(replacement)
		keep()
		close(retained)
	}()
	select {
	case <-retained:
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("stalled read held the global policy cache")
	}
	close(release)
	<-done
}
