package meshadmin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func tsProvider(t *testing.T, f *fakeTailscale, access Access) Provider {
	t.Helper()
	p, err := NewProvider(Credential{Kind: KindTailscale, Type: CredOAuth, URL: f.srv.URL, ClientID: "cid", Secret: fakeSecret}, access)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// conformance: CTR-MESH-01
func TestTailscaleDevices(t *testing.T) {
	f := newFakeTailscale(t)
	p := tsProvider(t, f, AccessRead)
	devs, err := p.Devices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(devs) != 2 {
		t.Fatalf("got %d devices", len(devs))
	}
	d := devs[0]
	if d.ID != "1001" || !d.Online || d.Tags[0] != "tag:device" || !d.Expires.IsZero() || d.OS != "linux" {
		t.Fatalf("unexpected device: %+v", d)
	}
	if !devs[1].Expires.Equal(time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)) || devs[1].Online || len(devs[1].Tags) != 0 {
		t.Fatalf("unexpected device: %+v", devs[1])
	}
}

// conformance: CON-MESH-02 RUL-22
func TestTailscaleReadCredentialCannotWrite(t *testing.T) {
	f := newFakeTailscale(t)
	p := tsProvider(t, f, AccessRead)
	ctx := context.Background()
	_, err := p.SetTags(ctx, "1001", []string{"tag:peer"})
	var se *ScopeError
	if !errors.Is(err, ErrUnauthorized) || !errors.As(err, &se) || se.Scope != "devices:core" {
		t.Fatalf("want scope hint devices:core, got %v", err)
	}
	if _, err := p.CreateAuthKey(ctx, AuthKeyRequest{Tags: []string{"tag:device"}}); !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "auth_keys") {
		t.Fatalf("auth key: %v", err)
	}
	if _, err := p.SetPolicy(ctx, "{}", ""); !errors.Is(err, ErrUnauthorized) || !strings.Contains(err.Error(), "policy_file") {
		t.Fatalf("policy: %v", err)
	}
}

// conformance: CTR-MESH-03
func TestTailscaleSetTagsAndAuthKey(t *testing.T) {
	f := newFakeTailscale(t)
	p := tsProvider(t, f, AccessWrite)
	ctx := context.Background()
	d, err := p.SetTags(ctx, "1002", []string{"tag:peer", "tag:device"})
	if err != nil || len(d.Tags) != 2 {
		t.Fatalf("set tags: %v %+v", err, d)
	}
	k, err := p.CreateAuthKey(ctx, AuthKeyRequest{Tags: []string{"tag:device"}, TTL: time.Hour, Ephemeral: true})
	if err != nil || k.Secret != "tskey-auth-NEWKEY" || k.ID != "k1" {
		t.Fatalf("auth key: %v %+v", err, k)
	}
	create := f.lastKey["capabilities"].(map[string]any)["devices"].(map[string]any)["create"].(map[string]any)
	if create["preauthorized"] != true || create["ephemeral"] != true || create["reusable"] != false {
		t.Fatalf("key capabilities: %v", create)
	}
	if f.lastKey["expirySeconds"].(float64) != 3600 {
		t.Fatalf("expiry: %v", f.lastKey["expirySeconds"])
	}
	if strings.Contains(k.Redacted().Secret, "tskey") {
		t.Fatal("Redacted kept the secret")
	}
	if _, err := p.CreateAuthKey(ctx, AuthKeyRequest{TTL: time.Hour}); err != nil {
		t.Fatalf("untagged auth key: %v", err)
	}
	create = f.lastKey["capabilities"].(map[string]any)["devices"].(map[string]any)["create"].(map[string]any)
	if _, exists := create["tags"]; exists {
		t.Fatalf("untagged key request assigned tags: %v", create)
	}
}

// conformance: CON-MESH-03 RUL-21
func TestTailscalePolicyEtagConflict(t *testing.T) {
	f := newFakeTailscale(t)
	p := tsProvider(t, f, AccessWrite)
	ctx := context.Background()
	doc, err := p.GetPolicy(ctx)
	if err != nil || !doc.Exists || doc.Version == "" {
		t.Fatalf("get: %v %+v", err, doc)
	}
	f.setPolicy("{\n\t\"acls\": [{\"action\":\"accept\",\"src\":[\"*\"],\"dst\":[\"*:*\"]}],\n}\n") // someone else edits
	_, err = p.SetPolicy(ctx, "{}", doc.Version)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	// Re-read and write succeeds.
	doc, _ = p.GetPolicy(ctx)
	out, err := p.SetPolicy(ctx, "{}", doc.Version)
	if err != nil || out.Version == doc.Version {
		t.Fatalf("set after refresh: %v %+v", err, out)
	}
}

func TestTailscaleValidate(t *testing.T) {
	f := newFakeTailscale(t)
	p := tsProvider(t, f, AccessRead)
	if err := p.ValidatePolicy(context.Background(), "{}"); err != nil {
		t.Fatal(err)
	}
	err := p.ValidatePolicy(context.Background(), "{BADRULE}")
	var ve *ValidationError
	if !errors.As(err, &ve) || !strings.Contains(ve.Message, "BADRULE is not allowed") {
		t.Fatalf("want ValidationError, got %v", err)
	}
}

func TestTailscaleAPITokenAndBadCredentials(t *testing.T) {
	f := newFakeTailscale(t)
	p, err := NewProvider(Credential{Kind: KindTailscale, Type: CredToken, URL: f.srv.URL, Secret: "api-token"}, AccessRead)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Devices(context.Background()); err != nil {
		t.Fatal(err)
	}
	bad, _ := NewProvider(Credential{Kind: KindTailscale, Type: CredOAuth, URL: f.srv.URL, ClientID: "cid", Secret: "wrong"}, AccessRead)
	if _, err := bad.Devices(context.Background()); err == nil {
		t.Fatal("wrong secret accepted")
	} else if strings.Contains(err.Error(), "wrong") {
		t.Fatalf("error leaks the secret: %v", err)
	}
}

// conformance: CTR-MESH-01
func TestRefusesPlainHTTPToRemoteHost(t *testing.T) {
	if _, err := NewTailscale(TailscaleConfig{BaseURL: "http://api.example.com", Token: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("http to remote host accepted: %v", err)
	}
	if _, err := NewHeadscale(HeadscaleConfig{BaseURL: "http://hs.example.com", APIKey: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("http to remote host accepted: %v", err)
	}
	if _, err := NewHeadscale(HeadscaleConfig{BaseURL: "http://127.0.0.1:8080", APIKey: "x"}); err != nil {
		t.Fatalf("loopback http rejected: %v", err)
	}
}
