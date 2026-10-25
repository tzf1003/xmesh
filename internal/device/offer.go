package device

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/filelock"
	"github.com/tzf1003/xmesh/internal/privdir"
)

// OfferPath is where a trusted device delivers an Offer.
const OfferPath = "/mesh/v2/offer"

// OfferCloudflare is the offer kind that joins the receiving device to the sender's Cloudflare mesh.
const OfferCloudflare = "cloudflare"

const (
	// OfferTTL is how long a received offer waits for the user before it is dropped.
	OfferTTL = 24 * time.Hour
	// MaxOffers bounds the inbox; a trusted but misbehaving peer cannot grow it past this.
	MaxOffers = 16
	// MaxOffersPerSender keeps one device from filling the inbox.
	MaxOffersPerSender = 4
	// MaxOfferBytes bounds one delivered offer.
	MaxOfferBytes = 16 << 10
)

var (
	ErrOfferInvalid = errors.New("连接方式提议无效")
	ErrOfferFull    = errors.New("收到的连接方式提议过多，请先处理已有的")
	ErrOfferUnknown = errors.New("没有这条连接方式提议")

	offerID = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// CloudflareOffer carries what a device needs to join the sender's Cloudflare mesh: its own tunnel
// and Access service token, and the sender's hostname so both sides can dial each other. The sender's
// Cloudflare API token is never part of it.
type CloudflareOffer struct {
	// Hostname is the public name of the receiving device's own tunnel.
	Hostname string `json:"hostname"`
	// TunnelToken runs the receiving device's cloudflared.
	TunnelToken string `json:"tunnel_token"`
	// AccessID and AccessSecret are the Access service token used to dial peers.
	AccessID     string `json:"access_id"`
	AccessSecret string `json:"access_secret"`
	// PeerHostname is the sender's own tunnel hostname, recorded as that device's address.
	PeerHostname string `json:"peer_hostname"`
}

// Offer is one connection method a trusted device proposes to another. It grants no trust: the
// receiver still pairs with each peer on its own, and only applies the offer after the user accepts.
type Offer struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"`
	Cloudflare *CloudflareOffer `json:"cloudflare,omitempty"`
}

// ReceivedOffer is an Offer in the inbox with the sender the receiver authenticated, not one the
// sender claims.
type ReceivedOffer struct {
	Offer
	FromID     string    `json:"from_id"`
	FromName   string    `json:"from_name"`
	ReceivedAt time.Time `json:"received_at"`
	ExpiresAt  time.Time `json:"expires_at"`
}

func NewOfferID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// credential accepts the printable ASCII a Cloudflare token or service token is made of.
func credential(s string) bool {
	if len(s) < 8 || len(s) > 4096 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '!' || s[i] > '~' {
			return false
		}
	}
	return true
}

func validHostname(host string) bool {
	u, err := url.Parse("https://" + host)
	return err == nil && host != "" && u.Host == host && u.Hostname() == host && u.Port() == "" && u.User == nil && u.Path == "" && len(host) <= 253
}

func (o Offer) Validate() error {
	if !offerID.MatchString(o.ID) {
		return ErrOfferInvalid
	}
	switch o.Kind {
	case OfferCloudflare:
		c := o.Cloudflare
		if c == nil || !validHostname(c.Hostname) || !validHostname(c.PeerHostname) || c.Hostname == c.PeerHostname ||
			!credential(c.TunnelToken) || !credential(c.AccessSecret) || !credential(c.AccessID) {
			return ErrOfferInvalid
		}
	default:
		return ErrOfferInvalid
	}
	return nil
}

// OfferInbox keeps received offers on disk until the user decides. They hold credentials, so the file
// is private and an offer is deleted as soon as it is accepted, rejected or expired.
type OfferInbox struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

type offerFile struct {
	Version int             `json:"version"`
	Offers  []ReceivedOffer `json:"offers"`
}

func OpenOfferInbox(stateDir string) (*OfferInbox, error) {
	dir := filepath.Join(stateDir, "identity-v2")
	if err := privdir.Ensure(dir); err != nil {
		return nil, err
	}
	return &OfferInbox{path: filepath.Join(dir, "offers.json"), now: time.Now}, nil
}

func (b *OfferInbox) read() (offerFile, error) {
	file := offerFile{Version: Version}
	raw, err := readPrivate(b.path, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, err
	}
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != Version {
		return offerFile{Version: Version}, fmt.Errorf("连接方式提议文件无效")
	}
	return file, nil
}

func (b *OfferInbox) update(ctx context.Context, fn func(*offerFile) error) error {
	lock, err := filelock.Acquire(ctx, b.path+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	// A file that cannot be read is an error, never an empty inbox: writing over it would destroy
	// credentials the sender no longer has.
	file, err := b.read()
	if err != nil {
		return err
	}
	now := b.now()
	file.Offers = slices.DeleteFunc(file.Offers, func(o ReceivedOffer) bool { return !o.ExpiresAt.After(now) })
	if err := fn(&file); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(b.path, raw)
}

// Receive stores an offer from an authenticated, trusted sender. Delivering the same ID again replaces
// the earlier copy, so a retry does not fill the inbox.
func (b *OfferInbox) Receive(ctx context.Context, from Trust, offer Offer) error {
	if err := offer.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.update(ctx, func(file *offerFile) error {
		file.Offers = slices.DeleteFunc(file.Offers, func(o ReceivedOffer) bool { return o.ID == offer.ID && o.FromID == from.Document.DeviceID })
		own := 0
		for _, o := range file.Offers {
			if o.FromID == from.Document.DeviceID {
				own++
			}
		}
		if own >= MaxOffersPerSender || len(file.Offers) >= MaxOffers {
			return ErrOfferFull
		}
		now := b.now()
		file.Offers = append(file.Offers, ReceivedOffer{Offer: offer, FromID: from.Document.DeviceID, FromName: from.Alias, ReceivedAt: now, ExpiresAt: now.Add(OfferTTL)})
		return nil
	})
}

// List returns the pending offers, oldest first. Credentials are included; callers show a summary.
func (b *OfferInbox) List(ctx context.Context) ([]ReceivedOffer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	file, err := b.read()
	if err != nil {
		return nil, err
	}
	now := b.now()
	live := slices.DeleteFunc(slices.Clone(file.Offers), func(o ReceivedOffer) bool { return !o.ExpiresAt.After(now) })
	if len(live) == len(file.Offers) {
		return live, nil
	}
	// Only a readable file with expired offers is rewritten, so their credentials do not linger on disk.
	return live, b.update(ctx, func(*offerFile) error { return nil })
}

// Take removes the offer and returns it, so one accept applies it at most once.
func (b *OfferInbox) Take(ctx context.Context, id string) (ReceivedOffer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var found ReceivedOffer
	err := b.update(ctx, func(file *offerFile) error {
		i := slices.IndexFunc(file.Offers, func(o ReceivedOffer) bool { return o.ID == id })
		if i < 0 {
			return ErrOfferUnknown
		}
		found = file.Offers[i]
		file.Offers = slices.Delete(file.Offers, i, i+1)
		return nil
	})
	return found, err
}

// Put returns an offer taken for applying whose application failed, so the user can retry.
func (b *OfferInbox) Put(ctx context.Context, offer ReceivedOffer) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.update(ctx, func(file *offerFile) error {
		if !slices.ContainsFunc(file.Offers, func(o ReceivedOffer) bool { return o.ID == offer.ID }) {
			file.Offers = append(file.Offers, offer)
		}
		return nil
	})
}
