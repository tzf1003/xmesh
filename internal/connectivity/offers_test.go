package connectivity

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/device"
)

func testOffer() device.Offer {
	return device.Offer{ID: device.NewOfferID(), Kind: device.OfferCloudflare, Cloudflare: &device.CloudflareOffer{
		Hostname: "xmesh-b-1.mesh.example.com", TunnelToken: "tunnel-token-value", AccessID: "client-id.access", AccessSecret: "access-secret-value", PeerHostname: "xmesh-a-1.mesh.example.com",
	}}
}

func TestPairedDeviceDeliversOfferToInboxWithAuthenticatedSender(t *testing.T) {
	a, _ := node(t, "laptop")
	b, _ := node(t, "phone")
	server := serveNode(t, b)
	if err := b.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PairWith(t.Context(), device.LAN, server.URL, "correct horse battery", b.identity.ID); err != nil {
		t.Fatal(err)
	}
	offer := testOffer()
	var err error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if err = a.SendOffer(t.Context(), b.identity.ID, offer); err == nil {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	list, err := b.offers.List(t.Context())
	if err != nil || len(list) != 1 || list[0].ID != offer.ID {
		t.Fatalf("%+v %v", list, err)
	}
	if list[0].FromID != a.identity.ID || list[0].FromName != "laptop" {
		t.Fatalf("the sender is the one the certificate proved: %+v", list[0])
	}
	if applied, _ := a.offers.List(t.Context()); len(applied) != 0 {
		t.Fatal("the sender's own inbox changed")
	}
}

func TestOfferFromAnUnpairedDeviceIsRefused(t *testing.T) {
	a, _ := node(t, "stranger")
	b, _ := node(t, "phone")
	server := serveNode(t, b)
	client, err := a.httpClient(device.LAN, "", true)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Post(server.URL+device.OfferPath, "application/json", strings.NewReader(`{}`))
	if err == nil {
		response.Body.Close()
		if response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusOK {
			t.Fatalf("unpaired device delivered an offer: %d", response.StatusCode)
		}
	}
	if list, _ := b.offers.List(t.Context()); len(list) != 0 {
		t.Fatalf("%+v", list)
	}
}

func TestSendOfferRefusesUnknownDeviceAndInvalidOffer(t *testing.T) {
	a, _ := node(t, "laptop")
	if err := a.SendOffer(t.Context(), "dev-"+strings.Repeat("a", 64), testOffer()); err == nil {
		t.Fatal("offer sent to an unpaired device")
	}
	bad := testOffer()
	bad.Cloudflare.Hostname = "not a host"
	if err := a.SendOffer(t.Context(), "dev-"+strings.Repeat("a", 64), bad); err == nil {
		t.Fatal("invalid offer sent")
	}
}
