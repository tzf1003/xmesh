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
	var delivery OfferDelivery
	var err error
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if delivery, err = a.SendOffer(t.Context(), b.identity.ID, offer); err == nil && delivery == OfferDelivered {
			break
		}
	}
	if err != nil || delivery != OfferDelivered {
		t.Fatal(delivery, err)
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
	if _, err := a.SendOffer(t.Context(), "dev-"+strings.Repeat("a", 64), testOffer()); err == nil {
		t.Fatal("offer sent to an unpaired device")
	}
	bad := testOffer()
	bad.Cloudflare.Hostname = "not a host"
	if _, err := a.SendOffer(t.Context(), "dev-"+strings.Repeat("a", 64), bad); err == nil {
		t.Fatal("invalid offer sent")
	}
}

// pairOneWay pairs b with a over a's server, so b can dial a but a has no path to b.
func pairOneWay(t *testing.T) (a, b *Node) {
	t.Helper()
	a, _ = node(t, "laptop")
	b, _ = node(t, "phone")
	server := serveNode(t, a)
	if err := a.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PairWith(t.Context(), device.LAN, server.URL, "correct horse battery", a.identity.ID); err != nil {
		t.Fatal(err)
	}
	return a, b
}

func TestOfferQueuesForADeviceThatOnlyDialsOutAndItCollectsAndAcknowledgesIt(t *testing.T) {
	a, b := pairOneWay(t)
	offer := testOffer()
	// a learned b only from b's incoming connection; it has no address to dial.
	delivery, err := a.SendOffer(t.Context(), b.identity.ID, offer)
	if err != nil || delivery != OfferQueued {
		t.Fatalf("%v %v", delivery, err)
	}
	if pending, _ := a.outbox.Pending(t.Context(), b.identity.ID); len(pending) != 1 {
		t.Fatalf("the offer was not kept: %+v", pending)
	}
	if list, _ := b.offers.List(t.Context()); len(list) != 0 {
		t.Fatalf("the offer arrived before it was collected: %+v", list)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		b.collectOffers(t.Context())
		if list, _ := b.offers.List(t.Context()); len(list) == 1 {
			break
		}
	}
	list, _ := b.offers.List(t.Context())
	if len(list) != 1 || list[0].ID != offer.ID || list[0].FromID != a.identity.ID || list[0].FromName != "laptop" {
		t.Fatalf("%+v", list)
	}
	if pending, _ := a.outbox.Pending(t.Context(), b.identity.ID); len(pending) != 0 {
		t.Fatalf("a collected offer stayed queued: %+v", pending)
	}
	b.collectOffers(t.Context())
	if list, _ := b.offers.List(t.Context()); len(list) != 1 {
		t.Fatalf("collecting again duplicated the offer: %+v", list)
	}
}

func TestCollectGivesAPairedDeviceOnlyItsOwnOffers(t *testing.T) {
	a, b := pairOneWay(t)
	if err := a.outbox.Queue(t.Context(), "dev-"+strings.Repeat("e", 64), testOffer()); err != nil {
		t.Fatal(err)
	}
	b.collectOffers(t.Context())
	if list, _ := b.offers.List(t.Context()); len(list) != 0 {
		t.Fatalf("a device collected an offer meant for another: %+v", list)
	}
}

func TestUnpairedDeviceCannotCollectOrAcknowledgeOffers(t *testing.T) {
	a, _ := node(t, "laptop")
	stranger, _ := node(t, "stranger")
	server := serveNode(t, a)
	if err := a.outbox.Queue(t.Context(), stranger.identity.ID, testOffer()); err != nil {
		t.Fatal(err)
	}
	client, err := stranger.httpClient(device.LAN, "", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/collect", "/ack"} {
		response, err := client.Post(server.URL+device.OfferPath+route, "application/json", strings.NewReader(`{"ids":[]}`))
		if err == nil {
			response.Body.Close()
			if response.StatusCode == http.StatusOK || response.StatusCode == http.StatusNoContent {
				t.Fatalf("%s answered an unpaired device: %d", route, response.StatusCode)
			}
		}
	}
	if pending, _ := a.outbox.Pending(t.Context(), stranger.identity.ID); len(pending) != 1 {
		t.Fatal("an unpaired device removed a queued offer")
	}
}

func TestRefusedOfferIsNotQueued(t *testing.T) {
	a, _ := node(t, "laptop")
	b, _ := node(t, "phone")
	server := serveNode(t, b)
	if err := b.pairing.SetPassword("correct horse battery"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PairWith(t.Context(), device.LAN, server.URL, "correct horse battery", b.identity.ID); err != nil {
		t.Fatal(err)
	}
	for range device.MaxOffersPerSender {
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if d, err := a.SendOffer(t.Context(), b.identity.ID, testOffer()); err == nil && d == OfferDelivered {
				break
			}
		}
	}
	if _, err := a.SendOffer(t.Context(), b.identity.ID, testOffer()); err == nil {
		t.Fatal("an offer the device refused was reported as sent")
	}
	if pending, _ := a.outbox.Pending(t.Context(), b.identity.ID); len(pending) != 0 {
		t.Fatalf("a refused offer was queued for collection: %+v", pending)
	}
}
