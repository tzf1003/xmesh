package control

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/device"
)

const offerConfig = `# 节点配置
schema_version: 2
node:
  name: phone
  roles: [agent]
mesh:
  lan:
    enabled: false
agent:
  default_cwd: .
  cwd_policy:
    allow: ["."]
`

const offerSender = "dev-" + pairPeerFingerprint

// fakeOffers is the node's offer service over a real inbox, recording what the API sent.
type fakeOffers struct {
	inbox   *device.OfferInbox
	mu      sync.Mutex
	sent    []device.Offer
	sentTo  []string
	sendErr error
	queue   bool
}

func newFakeOffers(t *testing.T) *fakeOffers {
	t.Helper()
	inbox, err := device.OpenOfferInbox(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeOffers{inbox: inbox}
}

func (o *fakeOffers) SendOffer(_ context.Context, id string, offer device.Offer) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.sendErr != nil {
		return "", o.sendErr
	}
	o.sent, o.sentTo = append(o.sent, offer), append(o.sentTo, id)
	if o.queue {
		return "queued", nil
	}
	return "delivered", nil
}
func (o *fakeOffers) Offers(ctx context.Context) ([]device.ReceivedOffer, error) {
	return o.inbox.List(ctx)
}
func (o *fakeOffers) TakeOffer(ctx context.Context, id string) (device.ReceivedOffer, error) {
	return o.inbox.Take(ctx, id)
}
func (o *fakeOffers) PutOffer(ctx context.Context, offer device.ReceivedOffer) error {
	return o.inbox.Put(ctx, offer)
}

func cloudflareDeviceOffer() device.Offer {
	return device.Offer{ID: device.NewOfferID(), Kind: device.OfferCloudflare, Cloudflare: &device.CloudflareOffer{
		Hostname: "xmesh-phone-1.mesh.example.com", TunnelToken: "tunnel-token-value", AccessID: "client-id.access", AccessSecret: "access-secret-value", PeerHostname: "xmesh-laptop-1.mesh.example.com",
	}}
}

func receive(t *testing.T, o *fakeOffers, offer device.Offer) {
	t.Helper()
	from := device.Trust{Document: device.Document{DeviceID: offerSender}, Alias: "laptop"}
	if err := o.inbox.Receive(context.Background(), from, offer); err != nil {
		t.Fatal(err)
	}
}

func TestOffersUnavailableWithoutRuntimeService(t *testing.T) {
	f := newFixtureConfig(t, offerConfig)
	apiError(t, f.do("GET", "/v2/offers", nil, nil), v2.ErrUnavailable)
	apiError(t, f.do("POST", "/v2/offers/"+device.NewOfferID()+"/accept", nil, nil), v2.ErrUnavailable)
	apiError(t, f.do("DELETE", "/v2/offers/"+device.NewOfferID(), nil, nil), v2.ErrUnavailable)
}

func TestOffersListShowsSenderAndNoCredentials(t *testing.T) {
	admin := newFakeOffers(t)
	offer := cloudflareDeviceOffer()
	receive(t, admin, offer)
	f := newFixtureConfig(t, offerConfig, func(o *Options) { o.Offers = admin })
	r := f.ok("GET", "/v2/offers", nil, "OffersResponse")
	for _, secret := range []string{"tunnel-token-value", "access-secret-value", "client-id.access"} {
		if strings.Contains(string(r.Body), secret) {
			t.Fatalf("the list leaks %q: %s", secret, r.Body)
		}
	}
	var list v2.OffersResponse
	decodeAs(t, r, &list)
	if len(list.Offers) != 1 || list.Offers[0].ID != offer.ID || list.Offers[0].FromID != offerSender || list.Offers[0].FromName != "laptop" || list.Offers[0].Cloudflare.Hostname != "xmesh-phone-1.mesh.example.com" {
		t.Fatalf("%+v", list)
	}
}

func TestOfferAcceptWritesPrivateFilesAndConfigOnce(t *testing.T) {
	admin := newFakeOffers(t)
	offer := cloudflareDeviceOffer()
	receive(t, admin, offer)
	f := newFixtureConfig(t, offerConfig, func(o *Options) { o.Offers = admin })

	var done v2.OfferAcceptResponse
	decodeAs(t, f.ok("POST", "/v2/offers/"+offer.ID+"/accept", nil, "OfferAcceptResponse"), &done)
	if done.Kind != "cloudflare" || len(done.ChangedPaths) == 0 {
		t.Fatalf("%+v", done)
	}
	raw, _ := os.ReadFile(f.path)
	text := string(raw)
	state := filepath.Join(f.dir, "s", "cloudflare")
	for _, want := range []string{"# 节点配置", "hostname: xmesh-phone-1.mesh.example.com", "access_client_id: client-id.access", "tunnel_token_file: " + filepath.Join(state, "tunnel-"+offer.ID[:8]), "access_client_secret_file: " + filepath.Join(state, "access-"+offer.ID[:8]), offerSender + ": xmesh-laptop-1.mesh.example.com"} {
		if !strings.Contains(text, want) {
			t.Errorf("config lacks %q:\n%s", want, text)
		}
	}
	for _, secret := range []string{"tunnel-token-value", "access-secret-value"} {
		if strings.Contains(text, secret) {
			t.Errorf("the config holds the credential %q", secret)
		}
	}
	token, err := os.ReadFile(filepath.Join(state, "tunnel-"+offer.ID[:8]))
	if err != nil || strings.TrimSpace(string(token)) != "tunnel-token-value" {
		t.Fatalf("%q %v", token, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(filepath.Join(state, "access-"+offer.ID[:8])); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%v %v", info, err)
		}
	}
	if left, _ := admin.inbox.List(context.Background()); len(left) != 0 {
		t.Fatalf("an applied offer stayed in the inbox: %+v", left)
	}
	apiError(t, f.do("POST", "/v2/offers/"+offer.ID+"/accept", nil, nil), v2.ErrNotFound)
}

func TestOfferAcceptRefusesWhenCloudflareIsAlreadyConfiguredAndKeepsTheOffer(t *testing.T) {
	admin := newFakeOffers(t)
	offer := cloudflareDeviceOffer()
	receive(t, admin, offer)
	configured := strings.Replace(offerConfig, "mesh:\n", "mesh:\n  cloudflare:\n    enabled: true\n    hostname: mine.mesh.example.com\n", 1)
	f := newFixtureConfig(t, configured, func(o *Options) { o.Offers = admin })
	apiError(t, f.do("POST", "/v2/offers/"+offer.ID+"/accept", nil, nil), v2.ErrConflict)
	if left, _ := admin.inbox.List(context.Background()); len(left) != 1 {
		t.Fatalf("a refused accept must keep the offer: %+v", left)
	}
	if _, err := os.Stat(filepath.Join(f.dir, "s", "cloudflare")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(f.dir, "s", "cloudflare"))
		if len(entries) != 0 {
			t.Fatalf("credentials written despite the refusal: %v", entries)
		}
	}
	raw, _ := os.ReadFile(f.path)
	if string(raw) != configured {
		t.Fatalf("the config changed:\n%s", raw)
	}
}

func TestOfferAcceptRefusesAForeignListener(t *testing.T) {
	admin := newFakeOffers(t)
	offer := cloudflareDeviceOffer()
	receive(t, admin, offer)
	config := strings.Replace(offerConfig, "mesh:\n", "mesh:\n  cloudflare:\n    enabled: false\n    listen: 127.0.0.1:9000\n", 1)
	f := newFixtureConfig(t, config, func(o *Options) { o.Offers = admin })
	apiError(t, f.do("POST", "/v2/offers/"+offer.ID+"/accept", nil, nil), v2.ErrConflict)
}

func TestOfferRejectDeletesTheOffer(t *testing.T) {
	admin := newFakeOffers(t)
	offer := cloudflareDeviceOffer()
	receive(t, admin, offer)
	f := newFixtureConfig(t, offerConfig, func(o *Options) { o.Offers = admin })
	apiError(t, f.do("DELETE", "/v2/offers/not-an-id", nil, nil), v2.ErrInvalidArgs)
	if r := f.do("DELETE", "/v2/offers/"+offer.ID, nil, nil); r.Status != 204 {
		t.Fatalf("%d %s", r.Status, r.Body)
	}
	apiError(t, f.do("DELETE", "/v2/offers/"+offer.ID, nil, nil), v2.ErrNotFound)
	raw, _ := os.ReadFile(f.path)
	if string(raw) != offerConfig {
		t.Fatalf("rejecting must not touch the config:\n%s", raw)
	}
}

func TestOfferSendValidatesAndDelivers(t *testing.T) {
	admin := newFakeOffers(t)
	f := newFixtureConfig(t, offerConfig, func(o *Options) { o.Offers = admin })
	offer := cloudflareDeviceOffer()
	body := v2.OfferSendRequest{DeviceID: offerSender, Offer: v2.DeviceOffer{ID: offer.ID, Kind: offer.Kind, Cloudflare: &v2.CloudflareOffer{
		Hostname: offer.Cloudflare.Hostname, TunnelToken: offer.Cloudflare.TunnelToken, AccessID: offer.Cloudflare.AccessID, AccessSecret: offer.Cloudflare.AccessSecret, PeerHostname: offer.Cloudflare.PeerHostname,
	}}}
	bad := body
	bad.DeviceID = "laptop"
	apiError(t, f.do("POST", "/v2/offers", bad, nil), v2.ErrInvalidArgs)
	bad = body
	bad.Offer.Cloudflare = &v2.CloudflareOffer{Hostname: "x"}
	apiError(t, f.do("POST", "/v2/offers", bad, nil), v2.ErrInvalidArgs)

	var sent v2.OfferSendResponse
	decodeAs(t, f.ok("POST", "/v2/offers", body, "OfferSendResponse"), &sent)
	if sent.Delivery != "delivered" {
		t.Fatalf("%+v", sent)
	}
	admin.queue = true
	decodeAs(t, f.ok("POST", "/v2/offers", body, "OfferSendResponse"), &sent)
	if sent.Delivery != "queued" {
		t.Fatalf("%+v", sent)
	}
	admin.queue = false
	admin.sent, admin.sentTo = admin.sent[:1], admin.sentTo[:1]
	if len(admin.sent) != 1 || admin.sentTo[0] != offerSender || admin.sent[0].Cloudflare.TunnelToken != "tunnel-token-value" {
		t.Fatalf("%+v %v", admin.sent, admin.sentTo)
	}
	admin.sendErr = device.ErrUntrusted
	apiError(t, f.do("POST", "/v2/offers", body, nil), v2.ErrNotFound)
	admin.sendErr = context.DeadlineExceeded
	apiError(t, f.do("POST", "/v2/offers", body, nil), v2.ErrUnavailable)
}

func tailscaleDeviceOffer(controlURL string) device.Offer {
	return device.Offer{ID: device.NewOfferID(), Kind: device.OfferTailscale, Tailscale: &device.TailscaleOffer{ControlURL: controlURL, AuthKey: "tskey-auth-0123456789abcdef"}}
}

func TestOfferAcceptEnablesTheTailnetWithAPrivateKeyFile(t *testing.T) {
	admin := newFakeOffers(t)
	offer := tailscaleDeviceOffer("https://headscale.example.com")
	receive(t, admin, offer)
	f := newFixtureConfig(t, offerConfig, func(o *Options) { o.Offers = admin })

	r := f.ok("GET", "/v2/offers", nil, "OffersResponse")
	if strings.Contains(string(r.Body), "tskey-auth") {
		t.Fatalf("the list leaks the key: %s", r.Body)
	}
	var done v2.OfferAcceptResponse
	decodeAs(t, f.ok("POST", "/v2/offers/"+offer.ID+"/accept", nil, "OfferAcceptResponse"), &done)
	if done.Kind != "tailscale" || len(done.ChangedPaths) == 0 {
		t.Fatalf("%+v", done)
	}
	raw, _ := os.ReadFile(f.path)
	text := string(raw)
	id := offer.ID
	keyFile := filepath.Join(f.dir, "s", "authkeys", "authkey-"+id[:8]+"-"+id[8:12]+"-"+id[12:16]+"-"+id[16:20]+"-"+id[20:32])
	for _, want := range []string{"# 节点配置", "control_url: https://headscale.example.com", "authkey_file: " + keyFile} {
		if !strings.Contains(text, want) {
			t.Errorf("config lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "tskey-auth") {
		t.Errorf("the config holds the key:\n%s", text)
	}
	key, err := os.ReadFile(keyFile)
	if err != nil || strings.TrimSpace(string(key)) != "tskey-auth-0123456789abcdef" {
		t.Fatalf("%q %v", key, err)
	}
	if runtime.GOOS != "windows" {
		if info, err := os.Stat(keyFile); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%v %v", info, err)
		}
	}
	if left, _ := admin.inbox.List(context.Background()); len(left) != 0 {
		t.Fatalf("an applied offer stayed in the inbox: %+v", left)
	}
}

func TestOfferAcceptRefusesAnAlreadyConfiguredTailnetAndKeepsTheOffer(t *testing.T) {
	admin := newFakeOffers(t)
	offer := tailscaleDeviceOffer("")
	receive(t, admin, offer)
	configured := strings.Replace(offerConfig, "mesh:\n", "mesh:\n  tailnet:\n    enabled: true\n    control_url: https://controlplane.tailscale.com\n    authkey_file: /somewhere/key\n", 1)
	f := newFixtureConfig(t, configured, func(o *Options) { o.Offers = admin })
	apiError(t, f.do("POST", "/v2/offers/"+offer.ID+"/accept", nil, nil), v2.ErrConflict)
	if left, _ := admin.inbox.List(context.Background()); len(left) != 1 {
		t.Fatalf("a refused accept must keep the offer: %+v", left)
	}
	if entries, _ := os.ReadDir(filepath.Join(f.dir, "s", "authkeys")); len(entries) != 0 {
		t.Fatalf("a key was written despite the refusal: %v", entries)
	}
	if raw, _ := os.ReadFile(f.path); string(raw) != configured {
		t.Fatalf("the config changed:\n%s", raw)
	}
}

func TestOfferSendCarriesTailscaleOffers(t *testing.T) {
	admin := newFakeOffers(t)
	f := newFixtureConfig(t, offerConfig, func(o *Options) { o.Offers = admin })
	offer := tailscaleDeviceOffer("")
	body := v2.OfferSendRequest{DeviceID: offerSender, Offer: v2.DeviceOffer{ID: offer.ID, Kind: offer.Kind, Tailscale: &v2.TailscaleOffer{AuthKey: offer.Tailscale.AuthKey}}}
	f.ok("POST", "/v2/offers", body, "OfferSendResponse")
	if len(admin.sent) != 1 || admin.sent[0].Tailscale == nil || admin.sent[0].Tailscale.AuthKey != offer.Tailscale.AuthKey {
		t.Fatalf("%+v", admin.sent)
	}
	body.Offer.Tailscale = &v2.TailscaleOffer{AuthKey: "short"}
	apiError(t, f.do("POST", "/v2/offers", body, nil), v2.ErrInvalidArgs)
	body.Offer.Tailscale = &v2.TailscaleOffer{AuthKey: offer.Tailscale.AuthKey, ControlURL: "http://plain.example.com"}
	apiError(t, f.do("POST", "/v2/offers", body, nil), v2.ErrInvalidArgs)
}
