package cfadmin

import (
	"context"
	"github.com/tzf1003/xmesh/internal/cfadmin/cftest"
	"os"
	"runtime"
	"strings"
	"testing"
)

func newFake() (*cftest.Fake, *Client, func()) {
	f, url, done := cftest.New()
	return f, &Client{BaseURL: url, Token: "secret-token", AccountID: "acct", ZoneID: "zone"}, done
}

func TestProvisionDeviceCreatesRoutedGatedTunnel(t *testing.T) {
	f, client, done := newFake()
	defer done()
	m := &Mesh{Client: client, Domain: "example.com", Prefix: "xmesh-test"}
	token, err := m.NewServiceToken(context.Background(), "mesh")
	if err != nil {
		t.Fatal(err)
	}
	d, err := m.ProvisionDevice(context.Background(), "My Laptop!", token.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(d.Hostname, "xmesh-test-my-laptop-") || !strings.HasSuffix(d.Hostname, ".example.com") {
		t.Fatalf("hostname %q", d.Hostname)
	}
	if !strings.HasPrefix(d.TunnelToken, "tunnel-token-") || d.AppID == "" || d.DNSID == "" || d.PolicyID == "" {
		t.Fatalf("incomplete device %+v", d)
	}
	for _, a := range f.Auth {
		if a != "Bearer secret-token" {
			t.Fatalf("authorization %q", a)
		}
	}
	if strings.Contains(d.String(), "tunnel-token") || strings.Contains(token.String(), "csecret") {
		t.Fatal("secrets must not print")
	}
	if err := m.Teardown(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	for key := range f.Things {
		if !strings.HasPrefix(key, "svc/") {
			t.Errorf("%s left behind", key)
		}
	}
	// A second teardown finds everything gone and is still fine.
	if err := m.Teardown(context.Background(), d); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionDeviceRollsBackWhatItCreated(t *testing.T) {
	for _, failing := range []string{"/configurations", "/dns_records", "POST /accounts/acct/access/apps", "/policies"} {
		f, client, done := newFake()
		m := &Mesh{Client: client, Domain: "example.com"}
		f.FailOn = failing
		if _, err := m.ProvisionDevice(context.Background(), "box", "token-id"); err == nil {
			t.Fatalf("%s: expected failure", failing)
		}
		f.FailOn = ""
		if len(f.Things) != 0 {
			t.Errorf("%s: left behind %v", failing, f.Things)
		}
		done()
	}
}

func TestErrorsCarryCloudflareMessagesButNotTheToken(t *testing.T) {
	_, client, done := newFake()
	defer done()
	err := client.DeleteServiceToken(context.Background(), "missing")
	var api *APIError
	if err == nil || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("%v", err)
	}
	if !IsNotFound(err) {
		t.Fatalf("%v %v", err, api)
	}
}

func TestDomainAndLabelValidation(t *testing.T) {
	_, client, done := newFake()
	defer done()
	for _, domain := range []string{"", "localhost", "Example.com", "a b.com", "-x.com"} {
		if _, err := (&Mesh{Client: client, Domain: domain}).ProvisionDevice(context.Background(), "x", "t"); err == nil {
			t.Errorf("domain %q accepted", domain)
		}
	}
	if Label("  ") != "device" || Label("Büro PC 1") != "b-ro-pc-1" || len(Label(strings.Repeat("a", 80))) != 24 {
		t.Fatal(Label("Büro PC 1"))
	}
}

func TestSetupSharesOneServiceTokenAndRemoveCleansUp(t *testing.T) {
	f, client, done := newFake()
	defer done()
	m := &Mesh{Client: client, Domain: "example.com", Prefix: "xmesh-test"}
	state := &StateFile{Path: t.TempDir() + "/state.json"}
	ctx := context.Background()
	a, token, err := m.Setup(ctx, state, "laptop")
	if err != nil {
		t.Fatal(err)
	}
	b, again, err := m.Setup(ctx, state, "desktop")
	if err != nil {
		t.Fatal(err)
	}
	if token != again || a.Hostname == b.Hostname || a.TunnelToken == "" {
		t.Fatalf("%v %v %v", token, again, a.Hostname)
	}
	tokens := 0
	for key := range f.Things {
		if strings.HasPrefix(key, "svc/") {
			tokens++
		}
	}
	if tokens != 1 {
		t.Fatalf("%d service tokens", tokens)
	}
	raw, _ := os.ReadFile(state.Path)
	if strings.Contains(string(raw), "tunnel-token") {
		t.Fatal("tunnel token must not be stored")
	}
	if info, _ := os.Stat(state.Path); info.Mode().Perm() != 0o600 && runtime.GOOS != "windows" {
		t.Fatalf("state mode %v", info.Mode())
	}
	if _, _, err := m.Setup(ctx, state, "laptop"); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if err := m.Remove(ctx, state, "laptop"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, state, "desktop"); err != nil {
		t.Fatal(err)
	}
	if len(f.Things) != 0 {
		t.Fatalf("left behind %v", f.Things)
	}
	if err := m.Remove(ctx, state, "desktop"); err == nil {
		t.Fatal("removing an unknown device succeeded")
	}
}
