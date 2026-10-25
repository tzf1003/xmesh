package device

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/tzf1003/xmesh/internal/filelock"
	"github.com/tzf1003/xmesh/internal/privdir"
)

// MaxOutbox bounds the offers waiting to be collected, and MaxOutboxPerDevice those for one device.
const (
	MaxOutbox          = 16
	MaxOutboxPerDevice = 4
)

// QueuedOffer is an offer that could not be pushed because the target is not dialable from here.
// The target collects it over its own connection to this device.
type QueuedOffer struct {
	To        string    `json:"to"`
	Offer     Offer     `json:"offer"`
	ExpiresAt time.Time `json:"expires_at"`
}

// OfferOutbox keeps offers for devices this one cannot dial. They hold credentials, so the file is
// private, bounded and expiring, and an offer leaves it when the target acknowledges it.
type OfferOutbox struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
}

type outboxFile struct {
	Version int           `json:"version"`
	Offers  []QueuedOffer `json:"offers"`
}

func OpenOfferOutbox(stateDir string) (*OfferOutbox, error) {
	dir := filepath.Join(stateDir, "identity-v2")
	if err := privdir.Ensure(dir); err != nil {
		return nil, err
	}
	return &OfferOutbox{path: filepath.Join(dir, "offers-out.json"), now: time.Now}, nil
}

func (b *OfferOutbox) read() (outboxFile, error) {
	file := outboxFile{Version: Version}
	raw, err := readPrivate(b.path, 1<<20)
	if errors.Is(err, os.ErrNotExist) {
		return file, nil
	}
	if err != nil {
		return file, err
	}
	if err := json.Unmarshal(raw, &file); err != nil || file.Version != Version {
		return outboxFile{Version: Version}, fmt.Errorf("待送达的连接方式文件无效")
	}
	return file, nil
}

func (b *OfferOutbox) expired(o QueuedOffer) bool { return !o.ExpiresAt.After(b.now()) }

func (b *OfferOutbox) update(ctx context.Context, fn func(*outboxFile) error) error {
	lock, err := filelock.Acquire(ctx, b.path+".lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	// An unreadable file is an error, never an empty outbox: writing over it would destroy credentials.
	file, err := b.read()
	if err != nil {
		return err
	}
	file.Offers = slices.DeleteFunc(file.Offers, b.expired)
	if err := fn(&file); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(b.path, raw)
}

// Queue holds an offer for the target to collect. Queueing the same ID again replaces it.
func (b *OfferOutbox) Queue(ctx context.Context, to string, offer Offer) error {
	if err := offer.Validate(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.update(ctx, func(file *outboxFile) error {
		file.Offers = slices.DeleteFunc(file.Offers, func(o QueuedOffer) bool { return o.To == to && o.Offer.ID == offer.ID })
		own := 0
		for _, o := range file.Offers {
			if o.To == to {
				own++
			}
		}
		if own >= MaxOutboxPerDevice || len(file.Offers) >= MaxOutbox {
			return ErrOfferFull
		}
		file.Offers = append(file.Offers, QueuedOffer{To: to, Offer: offer, ExpiresAt: b.now().Add(OfferTTL)})
		return nil
	})
}

// Pending returns the offers waiting for the device, oldest first.
func (b *OfferOutbox) Pending(ctx context.Context, to string) ([]Offer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	file, err := b.read()
	if err != nil {
		return nil, err
	}
	var out []Offer
	for _, o := range file.Offers {
		if o.To == to && !b.expired(o) {
			out = append(out, o.Offer)
		}
	}
	return out, nil
}

// Ack removes the device's offers with these IDs once it has stored them.
func (b *OfferOutbox) Ack(ctx context.Context, to string, ids []string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.update(ctx, func(file *outboxFile) error {
		file.Offers = slices.DeleteFunc(file.Offers, func(o QueuedOffer) bool { return o.To == to && slices.Contains(ids, o.Offer.ID) })
		return nil
	})
}
