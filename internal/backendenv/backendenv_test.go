package backendenv

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return s, path
}

func TestPatchStoresValuesPrivatelyAndReturnsNamesOnly(t *testing.T) {
	s, path := open(t)
	names, err := s.Patch("github", map[string]string{"GITHUB_TOKEN": "ghp-secret", "API_BASE": "https://example.com"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"API_BASE", "GITHUB_TOKEN"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
	if got := s.Names("github"); !reflect.DeepEqual(got, names) {
		t.Fatalf("Names = %v", got)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
		}
	}
	// The values are in the file for the kernel and in Snapshot for the backend manager, nowhere else.
	if got := s.Snapshot()["github"]["GITHUB_TOKEN"]; got != "ghp-secret" {
		t.Fatalf("snapshot value = %q", got)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot()["github"]["API_BASE"]; got != "https://example.com" {
		t.Fatalf("value after reopen = %q", got)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".*.tmp-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

func TestPatchUnsetsBeforeSettingAndDropsEmptyBackends(t *testing.T) {
	s, _ := open(t)
	if _, err := s.Patch("b", map[string]string{"A": "1", "B": "2"}, nil); err != nil {
		t.Fatal(err)
	}
	names, err := s.Patch("b", map[string]string{"C": "3"}, []string{"A", "MISSING"})
	if err != nil || !reflect.DeepEqual(names, []string{"B", "C"}) {
		t.Fatalf("names = %v, %v", names, err)
	}
	names, err = s.Patch("b", nil, []string{"B", "C"})
	if err != nil || len(names) != 0 {
		t.Fatalf("names = %v, %v", names, err)
	}
	if _, present := s.Snapshot()["b"]; present {
		t.Fatal("a backend without variables must not stay in the file")
	}
}

func TestSnapshotIsACopy(t *testing.T) {
	s, _ := open(t)
	if _, err := s.Patch("b", map[string]string{"A": "1"}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot := s.Snapshot()
	snapshot["b"]["A"] = "changed"
	snapshot["other"] = map[string]string{"X": "y"}
	if got := s.Snapshot(); got["b"]["A"] != "1" || len(got) != 1 {
		t.Fatalf("store changed through its snapshot: %v", got)
	}
}

func TestPatchLimitsVariablesPerBackend(t *testing.T) {
	s, _ := open(t)
	set := map[string]string{}
	for i := 0; i <= MaxVars; i++ {
		set["V"+strings.Repeat("X", i)] = "1"
	}
	if _, err := s.Patch("b", set, nil); err != ErrTooMany {
		t.Fatalf("err = %v, want ErrTooMany", err)
	}
	if len(s.Names("b")) != 0 {
		t.Fatal("a refused patch changed the store")
	}
}

func TestReloadPicksUpAnEditAndPatchKeepsIt(t *testing.T) {
	s, path := open(t)
	if _, err := s.Patch("a", map[string]string{"K": "1"}, nil); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.Reload(); changed || err != nil {
		t.Fatalf("Reload without an edit = %v, %v", changed, err)
	}
	edited := `{"backends":{"a":{"K":"2"},"b":{"Z":"9"}}}`
	if err := os.WriteFile(path, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	if changed, err := s.Reload(); !changed || err != nil {
		t.Fatalf("Reload after an edit = %v, %v", changed, err)
	}
	if got := s.Snapshot()["a"]["K"]; got != "2" {
		t.Fatalf("K = %q after reload", got)
	}
	// A second edit lands between reload and patch: the patch starts from the file, not from memory.
	if err := os.WriteFile(path, []byte(`{"backends":{"a":{"K":"3"},"b":{"Z":"9"}}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Patch("a", map[string]string{"N": "n"}, nil); err != nil {
		t.Fatal(err)
	}
	got := s.Snapshot()
	if got["a"]["K"] != "3" || got["a"]["N"] != "n" || got["b"]["Z"] != "9" {
		t.Fatalf("a hand edit was lost: %v", got)
	}
}

func TestDamagedFileIsNeverOverwritten(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	damaged := []byte(`{"backends": {"a": {"K": `)
	if err := os.WriteFile(path, damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a damaged file")
	}
	s, _ := open(t)
	if _, err := s.Patch("a", map[string]string{"K": "1"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), damaged, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Patch("a", map[string]string{"K": "2"}, nil); err == nil {
		t.Fatal("Patch overwrote a damaged file")
	}
	if raw, _ := os.ReadFile(s.Path()); string(raw) != string(damaged) {
		t.Fatalf("damaged file changed: %q", raw)
	}
	if got := s.Snapshot()["a"]["K"]; got != "1" {
		t.Fatalf("the variables in effect changed: %q", got)
	}
	if _, err := s.Reload(); err == nil {
		t.Fatal("Reload accepted a damaged file")
	}
}

func TestFileWithBadNamesIsRejected(t *testing.T) {
	for _, doc := range []string{
		`{"backends":{"a":{"1BAD":"x"}}}`,
		`{"backends":{"-a":{"K":"x"}}}`,
		`{"backends":{"a":{"K":"x\u0000"}}}`,
		`{"backends":{"a":{"K":"x"}},"extra":1}`,
	} {
		path := filepath.Join(t.TempDir(), FileName)
		if err := os.WriteFile(path, []byte(doc), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(path); err == nil {
			t.Errorf("Open accepted %s", doc)
		}
	}
}

func TestPruneAndClear(t *testing.T) {
	s, _ := open(t)
	for _, name := range []string{"a", "b", "c"} {
		if _, err := s.Patch(name, map[string]string{"K": name}, nil); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := s.Prune(func(name string) bool { return name != "b" })
	if err != nil || !removed {
		t.Fatalf("Prune = %v, %v", removed, err)
	}
	removed, err = s.Prune(func(string) bool { return true })
	if err != nil || removed {
		t.Fatalf("Prune with nothing to drop = %v, %v", removed, err)
	}
	if err = s.Clear("a"); err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot(); len(got) != 1 || got["c"]["K"] != "c" {
		t.Fatalf("after Clear = %v", got)
	}
	if err = s.Clear("never-set"); err != nil {
		t.Fatalf("Clear of an unknown backend = %v", err)
	}
}

func TestValidators(t *testing.T) {
	for _, ok := range []string{"A", "_A", "OPENAI_API_KEY", "a1"} {
		if !ValidName(ok) {
			t.Errorf("ValidName(%q) = false", ok)
		}
	}
	for _, bad := range []string{"", "1A", "A-B", "A=B", "A B", strings.Repeat("A", 129)} {
		if ValidName(bad) {
			t.Errorf("ValidName(%q) = true", bad)
		}
	}
	if ValidValue("a\x00b") || ValidValue(strings.Repeat("x", MaxValueBytes+1)) || !ValidValue("") || !ValidValue(strings.Repeat("x", MaxValueBytes)) {
		t.Error("ValidValue disagrees with its limits")
	}
	if !ValidBackend("github") || !ValidBackend("a-1") || ValidBackend("-a") || ValidBackend("a_b") || ValidBackend("") {
		t.Error("ValidBackend disagrees with the backend name syntax")
	}
}
