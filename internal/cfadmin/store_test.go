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
	if err := s.Delete("default"); err == nil {
		t.Log("delete reports the keychain error but still removes the file")
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
