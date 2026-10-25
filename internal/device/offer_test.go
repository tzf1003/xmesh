package device

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func cloudflareOffer(id string) Offer {
	return Offer{ID: id, Kind: OfferCloudflare, Cloudflare: &CloudflareOffer{
		Hostname: "xmesh-phone-1.mesh.example.com", TunnelToken: "tunnel-token-value", AccessID: "client-id.access", AccessSecret: "access-secret-value", PeerHostname: "xmesh-laptop-1.mesh.example.com",
	}}
}

func sender(id string) Trust {
	return Trust{Document: Document{DeviceID: "dev-" + id}, Alias: "laptop-" + id}
}

func TestOfferValidation(t *testing.T) {
	good := cloudflareOffer(NewOfferID())
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Offer){
		"id":           func(o *Offer) { o.ID = "short" },
		"kind":         func(o *Offer) { o.Kind = "other" },
		"missing body": func(o *Offer) { o.Cloudflare = nil },
		"url host":     func(o *Offer) { o.Cloudflare.Hostname = "evil.example.com/path" },
		"port host":    func(o *Offer) { o.Cloudflare.PeerHostname = "evil.example.com:443" },
		"same host":    func(o *Offer) { o.Cloudflare.PeerHostname = o.Cloudflare.Hostname },
		"token space":  func(o *Offer) { o.Cloudflare.TunnelToken = "has space in it" },
		"empty secret": func(o *Offer) { o.Cloudflare.AccessSecret = "" },
	} {
		offer := cloudflareOffer(NewOfferID())
		mutate(&offer)
		if err := offer.Validate(); !errors.Is(err, ErrOfferInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}

func TestOfferInboxReceivesListsAndTakesOnce(t *testing.T) {
	ctx := context.Background()
	box, err := OpenOfferInbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	offer := cloudflareOffer(NewOfferID())
	if err := box.Receive(ctx, sender("a"), offer); err != nil {
		t.Fatal(err)
	}
	if err := box.Receive(ctx, sender("a"), offer); err != nil {
		t.Fatalf("a retry of the same offer must replace it: %v", err)
	}
	list, err := box.List(ctx)
	if err != nil || len(list) != 1 || list[0].FromID != "dev-a" || list[0].FromName != "laptop-a" {
		t.Fatalf("%+v %v", list, err)
	}
	got, err := box.Take(ctx, offer.ID)
	if err != nil || got.Cloudflare.TunnelToken != "tunnel-token-value" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := box.Take(ctx, offer.ID); !errors.Is(err, ErrOfferUnknown) {
		t.Fatalf("an offer was taken twice: %v", err)
	}
	if err := box.Put(ctx, got); err != nil {
		t.Fatal(err)
	}
	if list, _ := box.List(ctx); len(list) != 1 {
		t.Fatalf("a failed accept must put the offer back: %+v", list)
	}
}

func TestOfferInboxIsBounded(t *testing.T) {
	ctx := context.Background()
	box, _ := OpenOfferInbox(t.TempDir())
	for i := 0; i < MaxOffersPerSender; i++ {
		if err := box.Receive(ctx, sender("a"), cloudflareOffer(NewOfferID())); err != nil {
			t.Fatal(err)
		}
	}
	if err := box.Receive(ctx, sender("a"), cloudflareOffer(NewOfferID())); !errors.Is(err, ErrOfferFull) {
		t.Fatalf("one sender filled past its share: %v", err)
	}
	if err := box.Receive(ctx, sender("b"), cloudflareOffer(NewOfferID())); err != nil {
		t.Fatalf("another sender must still get through: %v", err)
	}
}

func TestOfferInboxDropsExpiredOffersAndKeepsTheFilePrivate(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	box, _ := OpenOfferInbox(dir)
	now := time.Now()
	box.now = func() time.Time { return now }
	if err := box.Receive(ctx, sender("a"), cloudflareOffer(NewOfferID())); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(dir, "identity-v2", "offers.json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%v %v", info, err)
		}
	}
	box.now = func() time.Time { return now.Add(OfferTTL + time.Second) }
	if list, err := box.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("expired offer listed: %+v %v", list, err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "identity-v2", "offers.json"))
	if strings.Contains(string(raw), "tunnel-token-value") {
		t.Fatal("an expired offer's credentials stayed on disk")
	}
}
