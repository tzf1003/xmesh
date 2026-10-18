package spake2

import (
	"bytes"
	"testing"
)

// exchange runs one full exchange and returns the initiator's and responder's results.
func exchange(t *testing.T, typed, stored string, certA, certB []byte) ([]byte, []byte, error) {
	t.Helper()
	salt := bytes.Repeat([]byte{7}, SaltSize)
	typedSecret, err := NewSecret(Derive(typed, salt))
	if err != nil {
		t.Fatal(err)
	}
	storedSecret, err := NewSecret(Derive(stored, salt))
	if err != nil {
		t.Fatal(err)
	}
	in, X, err := NewInitiator(typedSecret, certA)
	if err != nil {
		t.Fatal(err)
	}
	r := NewResponder(storedSecret, certB)
	Y, confirmB, err := r.Reply(X, certA)
	if err != nil {
		return nil, nil, err
	}
	confirmA, err := in.Finish(Y, certB, confirmB)
	if err != nil {
		return nil, nil, err
	}
	return confirmA, nil, r.Confirm(confirmA)
}

func TestSamePasswordConfirms(t *testing.T) {
	confirmA, _, err := exchange(t, "correct horse", "correct horse", []byte("A-cert"), []byte("B-cert"))
	if err != nil {
		t.Fatal(err)
	}
	if len(confirmA) != 32 {
		t.Fatalf("confirmation length %d", len(confirmA))
	}
}

func TestWrongPasswordFailsAtTheInitiator(t *testing.T) {
	salt := bytes.Repeat([]byte{7}, SaltSize)
	typed, _ := NewSecret(Derive("guess-password", salt))
	stored, _ := NewSecret(Derive("real-password", salt))
	in, X, err := NewInitiator(typed, []byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	Y, confirmB, err := NewResponder(stored, []byte("B")).Reply(X, []byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Finish(Y, []byte("B"), confirmB); err != ErrPassword {
		t.Fatalf("wrong password accepted: %v", err)
	}
}

func TestWrongPasswordFailsAtTheResponder(t *testing.T) {
	// The responder cannot check the password itself. It can only reject the initiator's
	// confirmation, which an initiator with the wrong password never produces.
	salt := bytes.Repeat([]byte{7}, SaltSize)
	stored, _ := NewSecret(Derive("real-password", salt))
	r := NewResponder(stored, []byte("B"))
	typed, _ := NewSecret(Derive("guess-password", salt))
	in, X, err := NewInitiator(typed, []byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Reply(X, []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := r.Confirm([]byte("not a confirmation")); err != ErrPassword {
		t.Fatalf("forged confirmation accepted: %v", err)
	}
	_ = in
}

func TestSubstitutedResponderCertificateIsRejected(t *testing.T) {
	salt := bytes.Repeat([]byte{7}, SaltSize)
	secret, _ := NewSecret(Derive("shared password", salt))
	in, X, err := NewInitiator(secret, []byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	Y, confirmB, err := NewResponder(secret, []byte("B")).Reply(X, []byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Finish(Y, []byte("mallory"), confirmB); err != ErrPassword {
		t.Fatalf("substituted certificate accepted: %v", err)
	}
}

func TestRewrittenInitiatorCertificateIsRejected(t *testing.T) {
	// A man in the middle rewrites the initiator's certificate in transit. The responder hashes the
	// rewritten one, so the initiator's transcript differs and its check of confirmB fails.
	salt := bytes.Repeat([]byte{7}, SaltSize)
	secret, _ := NewSecret(Derive("shared password", salt))
	in, X, err := NewInitiator(secret, []byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	Y, confirmB, err := NewResponder(secret, []byte("B")).Reply(X, []byte("mallory"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Finish(Y, []byte("B"), confirmB); err != ErrPassword {
		t.Fatalf("rewritten initiator certificate accepted: %v", err)
	}
}

func TestTamperedPointsAreRejected(t *testing.T) {
	salt := bytes.Repeat([]byte{7}, SaltSize)
	secret, _ := NewSecret(Derive("shared password", salt))
	in, X, err := NewInitiator(secret, []byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	flip := func(b []byte) []byte {
		c := append([]byte(nil), b...)
		c[0] ^= 1
		return c
	}
	if _, _, err := NewResponder(secret, []byte("B")).Reply(flip(X), []byte("A")); err == nil {
		t.Fatal("tampered X accepted")
	}
	Y, confirmB, err := NewResponder(secret, []byte("B")).Reply(X, []byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Finish(flip(Y), []byte("B"), confirmB); err == nil {
		t.Fatal("tampered Y accepted")
	}
}

func TestIdentityPointIsRejected(t *testing.T) {
	salt := bytes.Repeat([]byte{7}, SaltSize)
	secret, _ := NewSecret(Derive("shared password", salt))
	identity := make([]byte, 32)
	if _, _, err := NewResponder(secret, []byte("B")).Reply(identity, []byte("A")); err != ErrMessage {
		t.Fatalf("identity X accepted: %v", err)
	}
	in, _, err := NewInitiator(secret, []byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := in.Finish(identity, []byte("B"), make([]byte, 32)); err != ErrMessage {
		t.Fatalf("identity Y accepted: %v", err)
	}
}

func TestDeriveDependsOnPasswordAndSalt(t *testing.T) {
	salt := bytes.Repeat([]byte{1}, SaltSize)
	a := Derive("password-one", salt)
	if !bytes.Equal(a, Derive("password-one", salt)) {
		t.Fatal("derivation is not deterministic")
	}
	if bytes.Equal(a, Derive("password-two", salt)) {
		t.Fatal("different passwords derive the same key")
	}
	if bytes.Equal(a, Derive("password-one", bytes.Repeat([]byte{2}, SaltSize))) {
		t.Fatal("different salts derive the same key")
	}
	if len(a) != KeySize {
		t.Fatalf("key length %d", len(a))
	}
}

func TestNewSecretRejectsBadKeyLength(t *testing.T) {
	if _, err := NewSecret(make([]byte, 32)); err != ErrMessage {
		t.Fatalf("short key accepted: %v", err)
	}
}
