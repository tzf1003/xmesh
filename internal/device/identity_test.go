package device

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestIdentityConcurrentFirstStartAndRestart(t *testing.T) {
	root := t.TempDir()
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	for range 12 {
		wg.Go(func() {
			id, err := LoadIdentity(t.Context(), root)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id.ID
		})
	}
	wg.Wait()
	close(ids)
	first := ""
	for id := range ids {
		if first == "" {
			first = id
		}
		if id != first {
			t.Fatalf("multiple identities: %s %s", first, id)
		}
	}
	loaded, err := LoadIdentity(t.Context(), root)
	if err != nil || loaded.ID != first {
		t.Fatal(loaded, err)
	}
	if len(first) != len("dev-")+64 {
		t.Fatalf("identity lost digest bits: %s", first)
	}
}

func TestIdentityRenewalKeepsPublicIdentity(t *testing.T) {
	id, err := NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	old, err := identityForKey(id.privateKey, time.Now().AddDate(-2, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	dir := filepath.Join(root, "identity-v2")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveIdentity(filepath.Join(dir, "identity.json"), old); err != nil {
		t.Fatal(err)
	}
	renewed, err := LoadIdentity(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.ID != id.ID || !bytes.Equal(renewed.privateKey, id.privateKey) {
		t.Fatal("renewal changed identity")
	}
	if bytes.Equal(renewed.Certificate.Certificate[0], old.Certificate.Certificate[0]) {
		t.Fatal("certificate not renewed")
	}
}

func TestCorruptIdentityIsPreserved(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadIdentity(t.Context(), root); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "identity-v2", "identity.json")
	raw := []byte(`{"version":2,"private_key":"invalid"}`)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIdentity(t.Context(), root); err == nil {
		t.Fatal("corrupt key silently replaced")
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(raw, got) {
		t.Fatal("corrupt identity overwritten")
	}
}

func TestIdentityRejectsWrongKeyType(t *testing.T) {
	if _, err := IDFromKey([]byte("not a key")); err == nil {
		t.Fatal("wrong key type accepted")
	}
	if _, err := IDFromKey(ed25519.PublicKey{1}); err == nil {
		t.Fatal("short public key accepted")
	}
}
