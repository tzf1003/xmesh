package meshadmin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tailscale.com/derp"
	"tailscale.com/derp/derphttp"
	"tailscale.com/derp/derpserver"
	"tailscale.com/net/netmon"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func keyText(k key.NodePublic) string { b, _ := k.MarshalText(); return string(b) }

// conformance: CON-MESH-06
func TestAllowlistReplaceReloadAndFailClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "allow.json")
	a, err := OpenAllowlist(path)
	if err != nil {
		t.Fatal(err)
	}
	k1, k2 := key.NewNode().Public(), key.NewNode().Public()
	if a.Allowed(keyText(k1)) {
		t.Fatal("empty list admits")
	}
	added, removed, err := a.Replace([]AllowEntry{{Key: keyText(k1), Name: "one"}, {Key: keyText(k2), Name: "two"}})
	if err != nil || added != 2 || removed != 0 {
		t.Fatalf("%d %d %v", added, removed, err)
	}
	// A second process (the sync command) rewrites the file; the open list sees it.
	b, _ := OpenAllowlist(path)
	if _, removed, _ := b.Replace([]AllowEntry{{Key: keyText(k2)}}); removed != 1 {
		t.Fatalf("removed %d", removed)
	}
	if a.Allowed(keyText(k1)) || !a.Allowed(keyText(k2)) {
		t.Fatal("open list did not pick up the rewrite")
	}
	if a.Allowed("garbage") || a.Allowed("") {
		t.Fatal("malformed key admitted")
	}
	// Corrupt file: deny everything.
	if err := writeFileAtomic(path, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	if a.Allowed(keyText(k2)) {
		t.Fatal("corrupt allowlist must fail closed")
	}
	if _, err := OpenAllowlist(path); err == nil {
		t.Fatal("corrupt file opened")
	}
}

// conformance: CON-MESH-06 CTR-MESH-05
func TestAdmitHandlerContract(t *testing.T) {
	a, _ := OpenAllowlist(filepath.Join(t.TempDir(), "a.json"))
	good, bad := key.NewNode().Public(), key.NewNode().Public()
	_, _, _ = a.Replace([]AllowEntry{{Key: keyText(good)}})
	srv := httptest.NewServer((&AdmissionServer{List: a, AdminToken: "tok"}).Handler())
	defer srv.Close()
	post := func(k key.NodePublic) tailcfg.DERPAdmitClientResponse {
		b, _ := json.Marshal(tailcfg.DERPAdmitClientRequest{NodePublic: k, Source: mustAddr("203.0.113.5")})
		resp, err := http.Post(srv.URL+"/admit", "application/json", bytes.NewReader(b))
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("admit: %v %v", err, resp)
		}
		defer resp.Body.Close()
		var out tailcfg.DERPAdmitClientResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return out
	}
	if !post(good).Allow || post(bad).Allow {
		t.Fatal("admission decisions wrong")
	}
	if r, _ := http.Get(srv.URL + "/admit"); r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /admit: %d", r.StatusCode)
	}
	if r, _ := http.Post(srv.URL+"/admit", "application/json", strings.NewReader("{")); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body: %d", r.StatusCode)
	}
	// admin API needs the token
	if r, _ := http.Get(srv.URL + "/admin/keys"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin without token: %d", r.StatusCode)
	}
	off := httptest.NewServer((&AdmissionServer{List: a}).Handler()) // no token configured: admin disabled
	defer off.Close()
	req, _ := http.NewRequest(http.MethodGet, off.URL+"/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer ")
	if r, _ := http.DefaultClient.Do(req); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin must stay closed without a configured token: %d", r.StatusCode)
	}
}

// TestDERPServerEnforcesAdmission runs the real derper server code with
// --verify-client-url pointing at our admission server: an admitted node can
// use the relay and a node that is not on the list cannot (design 13, 准入).
// conformance: CON-MESH-06 CTR-MESH-05
func TestDERPServerEnforcesAdmission(t *testing.T) {
	a, _ := OpenAllowlist(filepath.Join(t.TempDir(), "a.json"))
	allowedKey, deniedKey := key.NewNode(), key.NewNode()
	_, _, _ = a.Replace([]AllowEntry{{Key: keyText(allowedKey.Public()), Name: "allowed"}})
	admit := httptest.NewServer((&AdmissionServer{List: a}).Handler())
	defer admit.Close()

	s := derpserver.New(key.NewNode(), t.Logf)
	defer s.Close()
	s.SetVerifyClientURL(admit.URL + "/admit")
	s.SetVerifyClientURLFailOpen(false)
	relay := httptest.NewServer(derpserver.Handler(s))
	defer relay.Close()

	connect := func(k key.NodePrivate) error {
		c, err := derphttp.NewClient(k, relay.URL+"/derp", t.Logf, netmon.NewStatic())
		if err != nil {
			return err
		}
		defer c.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.Connect(ctx); err != nil {
			return err
		}
		// A denied client is accepted at the HTTP layer and dropped when the server
		// finishes the handshake; make that visible by round-tripping a frame.
		if err := c.SendPing([8]byte{1}); err != nil {
			return err
		}
		done := make(chan error, 1)
		go func() {
			for {
				m, err := c.Recv()
				if err != nil {
					done <- err
					return
				}
				if _, ok := m.(derp.PongMessage); ok {
					done <- nil
					return
				}
			}
		}()
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := connect(allowedKey); err != nil {
		t.Fatalf("admitted node refused: %v", err)
	}
	if err := connect(deniedKey); err == nil {
		t.Fatal("node outside the allowlist could use the relay")
	}
}

// conformance: CTR-MESH-05
func TestSyncAllowlistSelectsTaggedAuthorizedDevices(t *testing.T) {
	f := newFakeTailscale(t)
	p := tsProvider(t, f, AccessRead)
	a, _ := OpenAllowlist(filepath.Join(t.TempDir(), "a.json"))
	res, err := SyncAllowlist(context.Background(), p, a, SyncOptions{})
	if err != nil || res.Total != 1 || res.Added != 1 { // only "laptop" has an xmesh tag
		t.Fatalf("%+v %v", res, err)
	}
	res, _ = SyncAllowlist(context.Background(), p, a, SyncOptions{AllDevices: true})
	if res.Total != 2 || res.Added != 1 {
		t.Fatalf("all devices: %+v", res)
	}
	// remote variant against an AdmissionServer
	srv := httptest.NewServer((&AdmissionServer{List: a, AdminToken: "secret-token"}).Handler())
	defer srv.Close()
	res, err = SyncRemote(context.Background(), p, srv.URL, "secret-token", nil, SyncOptions{})
	if err != nil || res.Total != 1 || res.Removed != 1 {
		t.Fatalf("remote: %+v %v", res, err)
	}
	if _, err := SyncRemote(context.Background(), p, srv.URL, "wrong", nil, SyncOptions{}); err == nil {
		t.Fatal("wrong token accepted")
	}
}
