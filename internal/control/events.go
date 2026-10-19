package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

// Bus keeps the last v2.EventRingSize events of this boot and fans new ones out.
//
// Every payload is decoded back into the typed struct of its event type with unknown fields
// refused, so a field outside the contract (stdout, arguments, file content...) cannot reach the
// stream (INV-10): Publish drops the event and counts it.
type Bus struct {
	mu       sync.Mutex
	boot     string
	seq      uint64
	ring     []v2.Event
	subs     map[*Subscription]struct{}
	rejected uint64
	closed   bool
}

// Subscription receives events published after it was created. C is closed when the subscriber
// fell behind or the bus closed; the client reconnects with Last-Event-ID and replays or resets.
type Subscription struct {
	C      chan v2.Event
	bus    *Bus
	closed bool
}

func NewBus() *Bus {
	raw := make([]byte, 4)
	_, _ = rand.Read(raw)
	return &Bus{boot: hex.EncodeToString(raw), subs: map[*Subscription]struct{}{}}
}

// BootID is the random id of this boot, the first half of every event id.
func (b *Bus) BootID() string { return b.boot }

// Publish validates and stores an event. It never blocks on subscribers.
func (b *Bus) Publish(typ v2.EventType, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	e := v2.Event{Time: v2.Now(), Type: typ, Data: raw}
	if _, err = e.Payload(); err != nil || typ == v2.EventHello || typ == v2.EventReset {
		b.rejected++
		if err == nil {
			err = fmt.Errorf("事件 %s 不进入事件流缓存", typ)
		}
		return err
	}
	b.seq++
	e.Seq, e.ID = b.seq, v2.FormatEventID(b.boot, b.seq)
	b.ring = append(b.ring, e)
	if len(b.ring) > v2.EventRingSize {
		b.ring = append(b.ring[:0], b.ring[len(b.ring)-v2.EventRingSize:]...)
	}
	for s := range b.subs {
		select {
		case s.C <- e:
		default:
			b.dropLocked(s)
		}
	}
	return nil
}

// Rejected counts events refused by the encoder.
func (b *Bus) Rejected() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rejected
}

// Head is the id of the newest event, or nil before the first one.
func (b *Bus) Head() *string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.headLocked()
}
func (b *Bus) headLocked() *string {
	if b.seq == 0 {
		return nil
	}
	head := v2.FormatEventID(b.boot, b.seq)
	return &head
}

func (b *Bus) dropLocked(s *Subscription) {
	if !s.closed {
		s.closed = true
		close(s.C)
		delete(b.subs, s)
	}
}

// Join is what a new stream connection starts with: the hello head, an optional reset, the events
// to replay, and a live subscription. All four are taken under one lock, so no event is missed or
// repeated.
type Join struct {
	Head   *string
	Reset  *v2.ResetData
	Replay []v2.Event
	Sub    *Subscription
}

// Subscribe joins the stream. since is the client's Last-Event-ID, or "" for a first connection.
func (b *Bus) Subscribe(since string) Join {
	b.mu.Lock()
	defer b.mu.Unlock()
	j := Join{Head: b.headLocked(), Sub: &Subscription{C: make(chan v2.Event, 256), bus: b}}
	if b.closed {
		j.Sub.closed = true
		close(j.Sub.C)
		return j
	}
	b.subs[j.Sub] = struct{}{}
	if since == "" {
		return j
	}
	boot, seq, err := v2.ParseEventID(since)
	switch {
	case err != nil || boot != b.boot || seq > b.seq:
		j.Reset = &v2.ResetData{Reason: "boot_changed", Head: j.Head}
	case len(b.ring) > 0 && seq+1 < b.ring[0].Seq, len(b.ring) == 0 && seq < b.seq:
		j.Reset = &v2.ResetData{Reason: "gap", Head: j.Head}
	default:
		for _, e := range b.ring {
			if e.Seq > seq {
				j.Replay = append(j.Replay, e)
			}
		}
	}
	return j
}

func (s *Subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	s.bus.dropLocked(s)
}

// Close ends every subscription and stops accepting events.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for s := range b.subs {
		b.dropLocked(s)
	}
}
