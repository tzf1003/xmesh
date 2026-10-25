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

func TestOfferInboxNeverOverwritesAFileItCannotRead(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	box, _ := OpenOfferInbox(dir)
	if err := box.Receive(ctx, sender("a"), cloudflareOffer(NewOfferID())); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "identity-v2", "offers.json")
	good, _ := os.ReadFile(file)
	if err := os.WriteFile(file, []byte("{ not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := box.List(ctx); err == nil {
		t.Fatal("a corrupt inbox listed as empty")
	}
	if err := box.Receive(ctx, sender("b"), cloudflareOffer(NewOfferID())); err == nil {
		t.Fatal("a corrupt inbox accepted a new offer")
	}
	if raw, _ := os.ReadFile(file); string(raw) != "{ not json" {
		t.Fatalf("the unreadable file was overwritten: %s", raw)
	}
	if err := os.WriteFile(file, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if list, err := box.List(ctx); err != nil || len(list) != 1 {
		t.Fatalf("the restored inbox: %+v %v", list, err)
	}
}

func TestOfferOutboxQueuesCollectsAcksAndIsBounded(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	out, err := OpenOfferOutbox(dir)
	if err != nil {
		t.Fatal(err)
	}
	offer := cloudflareOffer(NewOfferID())
	if err := out.Queue(ctx, "dev-b", offer); err != nil {
		t.Fatal(err)
	}
	if err := out.Queue(ctx, "dev-b", offer); err != nil {
		t.Fatalf("queueing the same offer again must replace it: %v", err)
	}
	if got, err := out.Pending(ctx, "dev-b"); err != nil || len(got) != 1 || got[0].ID != offer.ID {
		t.Fatalf("%+v %v", got, err)
	}
	if got, _ := out.Pending(ctx, "dev-c"); len(got) != 0 {
		t.Fatalf("another device collected the offer: %+v", got)
	}
	if err := out.Ack(ctx, "dev-c", []string{offer.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := out.Pending(ctx, "dev-b"); len(got) != 1 {
		t.Fatal("another device's ack removed the offer")
	}
	if err := out.Ack(ctx, "dev-b", []string{offer.ID}); err != nil {
		t.Fatal(err)
	}
	if got, _ := out.Pending(ctx, "dev-b"); len(got) != 0 {
		t.Fatalf("an acknowledged offer stayed: %+v", got)
	}
	for i := 0; i < MaxOutboxPerDevice; i++ {
		if err := out.Queue(ctx, "dev-b", cloudflareOffer(NewOfferID())); err != nil {
			t.Fatal(err)
		}
	}
	if err := out.Queue(ctx, "dev-b", cloudflareOffer(NewOfferID())); !errors.Is(err, ErrOfferFull) {
		t.Fatalf("one device filled past its share: %v", err)
	}
	file := filepath.Join(dir, "identity-v2", "offers-out.json")
	if err := os.WriteFile(file, []byte("{ nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := out.Queue(ctx, "dev-c", cloudflareOffer(NewOfferID())); err == nil {
		t.Fatal("an unreadable outbox accepted an offer")
	}
	if raw, _ := os.ReadFile(file); string(raw) != "{ nope" {
		t.Fatal("the unreadable outbox was overwritten")
	}
}

func TestTailscaleOfferValidation(t *testing.T) {
	good := Offer{ID: NewOfferID(), Kind: OfferTailscale, Tailscale: &TailscaleOffer{AuthKey: "tskey-auth-0123456789"}}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, ok := range []string{"https://headscale.example.com", "https://headscale.example.com/", "https://hs.example.com:8443"} {
		good.Tailscale.ControlURL = ok
		if err := good.Validate(); err != nil {
			t.Fatalf("%s: %v", ok, err)
		}
	}
	good.Tailscale.ControlURL = "https://headscale.example.com"
	for name, mutate := range map[string]func(*Offer){
		"missing body": func(o *Offer) { o.Tailscale = nil },
		"short key":    func(o *Offer) { o.Tailscale.AuthKey = "short" },
		"spaced key":   func(o *Offer) { o.Tailscale.AuthKey = "has a space in it" },
		"plain http":   func(o *Offer) { o.Tailscale.ControlURL = "http://headscale.example.com" },
		"path":         func(o *Offer) { o.Tailscale.ControlURL = "https://headscale.example.com/x" },
		"userinfo":     func(o *Offer) { o.Tailscale.ControlURL = "https://u:p@headscale.example.com" },
		"query":        func(o *Offer) { o.Tailscale.ControlURL = "https://headscale.example.com?a=b" },
	} {
		o := Offer{ID: good.ID, Kind: OfferTailscale, Tailscale: &TailscaleOffer{AuthKey: good.Tailscale.AuthKey, ControlURL: good.Tailscale.ControlURL}}
		mutate(&o)
		if err := o.Validate(); !errors.Is(err, ErrOfferInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
}
