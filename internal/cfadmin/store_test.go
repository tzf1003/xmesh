package cfadmin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type brokenStore struct{}

func (brokenStore) Get(string) (Credential, error) {
	return Credential{}, errors.New("no secret service")
}
func (brokenStore) Put(string, Credential) error { return errors.New("no secret service") }
func (brokenStore) Delete(string) error          { return errors.New("no secret service") }

func TestFallbackStoreUsesFileWithoutKeychain(t *testing.T) {
	dir := t.TempDir()
	s := FallbackStore{Primary: brokenStore{}, Dir: dir}
	cred := Credential{Token: "tok", AccountID: strings.Repeat("a", 32), ZoneID: strings.Repeat("b", 32), Domain: "example.com"}
	if _, err := s.Get("default"); err == nil {
		t.Fatal("expected an error before anything is stored")
	}
	if err := s.Put("default", cred); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("default")
	if err != nil || got != cred {
		t.Fatalf("got %v %v", got, err)
	}
	if err := s.Delete("default"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "default.credential.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file should be gone: %v", err)
	}
}

func TestFallbackStorePrefersKeychain(t *testing.T) {
	dir := t.TempDir()
	mem := &MemoryStore{}
	s := FallbackStore{Primary: mem, Dir: dir}
	cred := Credential{Token: "tok", AccountID: strings.Repeat("a", 32), ZoneID: strings.Repeat("b", 32), Domain: "example.com"}
	if err := s.Put("p", cred); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "p.credential.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("no file expected when the keychain works")
	}
	if got, err := s.Get("p"); err != nil || got != cred {
		t.Fatalf("got %v %v", got, err)
	}
}

type flakyStore struct {
	MemoryStore
	down bool
}

func (s *flakyStore) Get(p string) (Credential, error) {
	if s.down {
		return Credential{}, errors.New("no secret service")
	}
	return s.MemoryStore.Get(p)
}

func (s *flakyStore) Put(p string, c Credential) error {
	if s.down {
		return errors.New("no secret service")
	}
	return s.MemoryStore.Put(p, c)
}

func (s *flakyStore) Delete(p string) error {
	if s.down {
		return errors.New("no secret service")
	}
	return s.MemoryStore.Delete(p)
}

func testCred(token string) Credential {
	return Credential{Token: token, AccountID: strings.Repeat("a", 32), ZoneID: strings.Repeat("b", 32), Domain: "example.com"}
}

func TestFallbackStoreFirstLoginWithoutKeychainIsNoCredential(t *testing.T) {
	s := FallbackStore{Primary: brokenStore{}, Dir: t.TempDir()}
	if _, err := s.Get("default"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("err = %v, want ErrNoCredential so the panel can ask for a token", err)
	}
}

func TestFallbackStoreNewestCredentialWinsAcrossBackends(t *testing.T) {
	dir := t.TempDir()
	kc := &flakyStore{}
	s := FallbackStore{Primary: kc, Dir: dir}
	if err := s.Put("p", testCred("old-keychain")); err != nil {
		t.Fatal(err)
	}
	kc.down = true
	if err := s.Put("p", testCred("file-token")); err != nil {
		t.Fatal(err)
	}
	kc.down = false
	if got, _ := s.Get("p"); got.Token != "file-token" {
		t.Fatalf("keychain back up: got %q, want the newer file credential", got.Token)
	}
	if err := s.Put("p", testCred("new-keychain")); err != nil {
		t.Fatal(err)
	}
	kc.down = true
	if got, err := s.Get("p"); err == nil && got.Token == "file-token" {
		t.Fatal("the stale file credential must be gone once the keychain took a newer one")
	}
}

func TestFallbackStoreLogoutWithoutKeychain(t *testing.T) {
	s := FallbackStore{Primary: brokenStore{}, Dir: t.TempDir()}
	if err := s.Put("p", testCred("t")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("p"); err != nil {
		t.Fatalf("logout that removed the file must succeed: %v", err)
	}
}
