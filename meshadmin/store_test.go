package meshadmin

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// conformance: CON-MESH-03 CTR-MESH-02
func TestDirStore(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "snaps")
	s := DirStore{Dir: dir}
	id1, err := s.Save(Snapshot{Provider: KindTailscale, Label: "one", Text: "{}", Version: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	id2, _ := s.Save(Snapshot{Provider: KindTailscale, Label: "two", Text: "{ }", Version: "v2"})
	got, err := s.Load(id1)
	if err != nil || got.Text != "{}" || got.Label != "one" || got.Time.IsZero() {
		t.Fatalf("%v %+v", err, got)
	}
	list, _ := s.List()
	if len(list) != 2 || list[0].Text != "" || list[0].ID < list[1].ID {
		t.Fatalf("list: %+v", list)
	}
	if _, err := s.Load("../etc/passwd"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("path traversal id: %v", err)
	}
	if _, err := s.Load("20260101T000000Z-deadbeef"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	_ = id2
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(dir, id1+".json"))
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("snapshot mode %v", st.Mode().Perm())
		}
		ds, _ := os.Stat(dir)
		if ds.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode %v", ds.Mode().Perm())
		}
	}
}

func TestUnifiedDiff(t *testing.T) {
	a := "{\n\t\"a\": 1,\n\t\"b\": 2,\n\t\"c\": 3,\n}\n"
	b := "{\n\t\"a\": 1,\n\t\"b\": 20,\n\t\"c\": 3,\n\t\"d\": 4,\n}\n"
	d := UnifiedDiff(a, b, 1)
	for _, want := range []string{"--- current", "+++ proposed", "-\t\"b\": 2,", "+\t\"b\": 20,", "+\t\"d\": 4,", "@@"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff lacks %q:\n%s", want, d)
		}
	}
	if UnifiedDiff(a, a, 3) != "" {
		t.Fatal("identical texts must give an empty diff")
	}
	if !strings.Contains(UnifiedDiff("", "x\n", 3), "+x") {
		t.Fatal("diff from empty")
	}
}

// conformance: CON-MESH-02 RUL-22
func TestCredentialNeverPrintsSecret(t *testing.T) {
	c := Credential{Kind: KindHeadscale, Type: CredAPIKey, URL: "http://127.0.0.1:1", Secret: "hskey-api-TOPSECRET"}
	for _, s := range []string{c.String(), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), fmt.Sprintf("%v", &c)} {
		if strings.Contains(s, "TOPSECRET") {
			t.Fatalf("secret leaked in %q", s)
		}
	}
	if err := (Credential{Kind: KindHeadscale, Type: CredAPIKey, Secret: "x"}).Validate(); err == nil {
		t.Fatal("headscale credential without URL accepted")
	}
	if err := (Credential{Kind: KindTailscale, Type: CredOAuth, Secret: "x"}).Validate(); err == nil {
		t.Fatal("oauth credential without client id accepted")
	}
}

func TestMemoryStoreResolve(t *testing.T) {
	ms := &MemoryStore{}
	w := Credential{Kind: KindTailscale, Type: CredToken, Secret: "w"}
	if _, _, err := Resolve(ms, "p", AccessRead); !errors.Is(err, ErrNoCredential) {
		t.Fatal(err)
	}
	_ = ms.Put("p", AccessWrite, w)
	c, got, err := Resolve(ms, "p", AccessRead)
	if err != nil || got != AccessWrite || c.Secret != "w" {
		t.Fatalf("read should fall back to write: %v %v", got, err)
	}
	r := Credential{Kind: KindTailscale, Type: CredToken, Secret: "r"}
	_ = ms.Put("p", AccessRead, r)
	if c, got, _ := Resolve(ms, "p", AccessRead); got != AccessRead || c.Secret != "r" {
		t.Fatal("read credential not preferred")
	}
	delete := ms.Delete("p", AccessWrite)
	if delete != nil {
		t.Fatal(delete)
	}
	if _, _, err := Resolve(ms, "p", AccessWrite); !errors.Is(err, ErrNoCredential) {
		t.Fatal("write must never fall back to read")
	}
	if err := ms.Put("../x", AccessRead, r); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad profile: %v", err)
	}
}

// conformance: CTR-MESH-02
func TestReadCredentialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cred.json")
	b, _ := json.Marshal(Credential{Kind: KindHeadscale, Type: CredAPIKey, URL: "http://127.0.0.1:8080", Secret: "k"})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := ReadCredentialFile(path); err != nil || c.Secret != "k" {
		t.Fatalf("%v", err)
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, 0o644)
		if _, err := ReadCredentialFile(path); !errors.Is(err, ErrInvalid) {
			t.Fatalf("world-readable credential file accepted: %v", err)
		}
	}
}

// conformance: CON-MESH-02
func TestWriteSecretFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "authkey")
	if err := WriteSecretFile(p, []byte("k"), false); err != nil {
		t.Fatal(err)
	}
	if err := WriteSecretFile(p, []byte("k2"), false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overwrite without flag: %v", err)
	}
	if err := WriteSecretFile(p, []byte("k2"), true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "k2" {
		t.Fatal("overwrite lost")
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(p); st.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", st.Mode().Perm())
		}
	}
}

func TestCredentialFileRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mesh", "mesh-credential.json")
	c := Credential{Kind: KindTailscale, Type: CredToken, URL: DefaultTailscaleURL, Secret: "tskey-api-example"}
	if err := WriteCredentialFile(p, c); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm()&0o077 != 0 {
			t.Fatalf("credential file is readable by others: %v", st.Mode().Perm())
		}
	}
	back, err := ReadCredentialFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if back.Kind != c.Kind || back.Type != c.Type || back.Secret != c.Secret || back.URL != c.URL {
		t.Fatalf("round trip: %+v", back)
	}
	// An invalid credential is refused before anything is written.
	if err := WriteCredentialFile(filepath.Join(dir, "bad.json"), Credential{Kind: KindTailscale, Type: CredToken}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty secret accepted: %v", err)
	}
}
