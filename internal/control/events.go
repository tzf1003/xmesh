package control

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ringSize is how many events a reconnecting client can replay.
const ringSize = 1024

// Event is one record of GET /v1/events. Data holds metadata only: the encoder
// below refuses any field that is not on the allow-list of its type (INV-10).
type Event struct {
	ID   uint64         `json:"id"`
	Type string         `json:"type"`
	Time string         `json:"time"`
	Data map[string]any `json:"data"`
}

type field int

const (
	str field = iota
	num
	flag
	list
	obj
)

// eventFields lists, per event type, the only fields an event may carry.
// Parameters, command lines, file content and output are not representable.
var eventFields = map[string]map[string]field{
	"status":    {"health": str, "draining": flag, "restart_required": flag, "warnings": list, "limits": obj},
	"peer":      {"change": str, "id": str, "name": str, "online": flag, "direct": flag, "relay": str, "tags": list},
	"call":      {"event": str, "caller": str, "oauth_client": str, "device": str, "tool": str, "status": str, "error": str, "duration_ms": num, "actor": str},
	"session":   {"change": str, "session_id": str, "name": str, "owner": str, "state": str},
	"warning":   {"message": str, "active": flag},
	"config":    {"etag": str, "source": str, "restart_required": flag, "changes": num},
	"lifecycle": {"state": str, "mode": str, "reason": str},
}

func kindOf(v any) field {
	switch v.(type) {
	case string:
		return str
	case bool:
		return flag
	case []string, []any:
		return list
	case map[string]any:
		return obj
	case nil:
		return str
	default:
		return num
	}
}

// encode returns data as a JSON-normalised map, or an error naming the first
// field that is not allowed for the event type.
func encode(kind string, data map[string]any) (map[string]any, error) {
	allowed, ok := eventFields[kind]
	if !ok {
		return nil, fmt.Errorf("未知事件类型: %s", kind)
	}
	for key, value := range data {
		want, ok := allowed[key]
		if !ok {
			return nil, fmt.Errorf("事件 %s 不允许字段 %s", kind, key)
		}
		if value != nil && kindOf(value) != want && !(want == num && isNumber(value)) {
			return nil, fmt.Errorf("事件 %s 的字段 %s 类型不符", kind, key)
		}
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
func isNumber(v any) bool {
	switch v.(type) {
	case int, int32, int64, uint, uint32, uint64, float32, float64:
		return true
	}
	return false
}

// Bus keeps the last ringSize events and fans new ones out to subscribers.
type Bus struct {
	mu       sync.Mutex
	next     uint64
	ring     []Event
	subs     map[*Subscription]struct{}
	rejected uint64
}

// Subscription receives events published after it was created. C is closed when
// the subscriber fell behind or the bus closed; the client reconnects with
// Last-Event-ID and replays from the ring.
type Subscription struct {
	C      chan Event
	bus    *Bus
	closed bool
}

func NewBus() *Bus { return &Bus{next: 1, subs: map[*Subscription]struct{}{}} }

// Publish validates and stores an event. It never blocks on subscribers.
func (b *Bus) Publish(kind string, data map[string]any) error {
	clean, err := encode(kind, data)
	if err != nil {
		b.mu.Lock()
		b.rejected++
		b.mu.Unlock()
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	e := Event{ID: b.next, Type: kind, Time: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), Data: clean}
	b.next++
	b.ring = append(b.ring, e)
	if len(b.ring) > ringSize {
		b.ring = append(b.ring[:0], b.ring[len(b.ring)-ringSize:]...)
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

func (b *Bus) dropLocked(s *Subscription) {
	if !s.closed {
		s.closed = true
		close(s.C)
		delete(b.subs, s)
	}
}

// Subscribe returns the events after since (0 means none are replayed) and a live
// subscription, atomically, so no event is missed or duplicated. gap reports that
// since is older than the oldest retained event.
func (b *Bus) Subscribe(since uint64, replay bool) (past []Event, gap bool, s *Subscription) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s = &Subscription{C: make(chan Event, 256), bus: b}
	b.subs[s] = struct{}{}
	if !replay {
		return nil, false, s
	}
	if len(b.ring) > 0 && since+1 < b.ring[0].ID {
		gap = true
	}
	for _, e := range b.ring {
		if e.ID > since {
			past = append(past, e)
		}
	}
	return past, gap, s
}
func (s *Subscription) Close() {
	s.bus.mu.Lock()
	defer s.bus.mu.Unlock()
	s.bus.dropLocked(s)
}

// Close ends every subscription.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for s := range b.subs {
		b.dropLocked(s)
	}
}
