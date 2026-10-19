package device

import (
	"errors"
	"testing"
)

func TestManualPathHintsPersistWithoutChangingTrustGeneration(t *testing.T) {
	root := t.TempDir()
	store, err := OpenTrust(root)
	if err != nil {
		t.Fatal(err)
	}
	remote := identity(t)
	if err := store.Grant(t.Context(), remote.Document("remote"), []string{"tag:peer"}, nil); err != nil {
		t.Fatal(err)
	}
	trusted, _ := store.Find(remote.ID)
	hint := PathHint{Kind: LAN, Address: "https://192.168.1.2:7433"}
	if err := store.RememberPath(t.Context(), remote.ID, trusted.Generation, hint); err != nil {
		t.Fatal(err)
	}
	loaded, err := OpenTrust(root)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := loaded.Find(remote.ID)
	if after.Generation != trusted.Generation || len(after.Hints) != 1 || after.Hints[0] != hint {
		t.Fatal(after)
	}
	after.Hints[0].Address = "changed"
	fresh, _ := loaded.Find(remote.ID)
	if fresh.Hints[0] != hint {
		t.Fatal("hint escaped the store by reference")
	}
	if err := store.Revoke(t.Context(), remote.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.RememberPath(t.Context(), remote.ID, trusted.Generation, hint); !errors.Is(err, ErrGeneration) {
		t.Fatal("late hint survived revocation", err)
	}
	if err := store.Grant(t.Context(), remote.Document("remote"), []string{"tag:peer"}, nil); err != nil {
		t.Fatal(err)
	}
	paired, _ := store.Find(remote.ID)
	if len(paired.Hints) != 0 {
		t.Fatal("revoked hints restored", paired)
	}
}

func TestManualPathHintsRejectNonDeviceEndpoints(t *testing.T) {
	for _, hint := range []PathHint{{LAN, "http://192.168.1.2:7433"}, {LAN, "https://user:password@host:7433"}, {LAN, "https://host:7433/mcp"}, {Tailnet, "https://host:7433?secret=value"}, {"other", "https://host:7433"}} {
		if hint.Validate() == nil {
			t.Fatal("invalid hint accepted", hint)
		}
	}
}
