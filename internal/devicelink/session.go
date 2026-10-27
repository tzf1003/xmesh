// Package devicelink carries stable MCP logical sessions across replaceable device links.
package devicelink

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	MaxFrameBytes      = 16 << 20
	SessionBufferBytes = 32 << 20
	ProcessBufferBytes = 128 << 20
)

var (
	ErrFrameTooLarge = errors.New("设备链路消息超过大小限制")
	ErrBackpressure  = errors.New("设备会话接收窗口已满")
	ErrSequence      = errors.New("设备会话消息序号无效")
)

// Budget bounds retained payloads across every link owned by one runtime.
type Budget struct {
	mu                              sync.Mutex
	limit, used, sending, receiving int
	changed                         chan struct{}
}

func NewBudget(limit int) *Budget {
	if limit <= 0 {
		limit = ProcessBufferBytes
	}
	return &Budget{limit: limit, changed: make(chan struct{})}
}

func (b *Budget) reserve(n int, sending bool) (bool, <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	used := b.receiving
	if sending {
		used = b.sending
	}
	if n > b.limit/2-used {
		return false, b.changed
	}
	b.used += n
	if sending {
		b.sending += n
	} else {
		b.receiving += n
	}
	return true, b.changed
}

func (b *Budget) release(n int, sending bool) {
	if n == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.used -= n
	if sending {
		b.sending -= n
	} else {
		b.receiving -= n
	}
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *Budget) Used() int { b.mu.Lock(); defer b.mu.Unlock(); return b.used }

// Packet is one ordered JSON-RPC message within a logical session. The physical link adds
// its authenticated connection epoch when framing it. Sequence numbers start at one.
type Packet struct {
	Sequence uint64 `json:"sequence"`
	Payload  []byte `json:"payload"`
}

type incoming struct {
	message jsonrpc.Message
	bytes   int
}

// Session retains accepted messages until acknowledgment or application delivery. A physical
// disconnect does not call Close: the link owner reattaches this same logical connection.
type Session struct {
	mu                           sync.Mutex
	id                           string
	budget                       *Budget
	limit, used                  int
	sending, receiving           int
	sent, acknowledged, received uint64
	outgoing                     []Packet
	incoming                     []incoming
	changed                      chan struct{}
	closed                       bool
}

func NewSession(id string, budget *Budget) *Session {
	if budget == nil {
		budget = NewBudget(0)
	}
	return &Session{id: id, budget: budget, limit: SessionBufferBytes, changed: make(chan struct{})}
}

func (s *Session) SessionID() string { return s.id }

func (s *Session) signalLocked() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// Changed is an edge-triggered wakeup. Callers read the current state before waiting on it.
func (s *Session) Changed() <-chan struct{} { s.mu.Lock(); defer s.mu.Unlock(); return s.changed }

func (s *Session) Write(ctx context.Context, message jsonrpc.Message) error {
	raw, err := jsonrpc.EncodeMessage(message)
	if err != nil {
		return err
	}
	if len(raw) > MaxFrameBytes || len(raw) > s.limit/2 || len(raw) > s.budget.limit/2 {
		return ErrFrameTooLarge
	}
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return io.EOF
		}
		if err := ctx.Err(); err != nil {
			s.mu.Unlock()
			return err
		}
		var global <-chan struct{}
		if len(raw) <= s.limit/2-s.sending {
			ok, changed := s.budget.reserve(len(raw), true)
			global = changed
			if ok {
				s.sent++
				s.outgoing = append(s.outgoing, Packet{Sequence: s.sent, Payload: raw})
				s.used += len(raw)
				s.sending += len(raw)
				s.signalLocked()
				s.mu.Unlock()
				return nil
			}
		}
		local := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-local:
		case <-global:
		}
	}
}

func (s *Session) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			return nil, io.EOF
		}
		if len(s.incoming) > 0 {
			message := s.incoming[0]
			s.incoming[0] = incoming{}
			s.incoming = s.incoming[1:]
			s.used -= message.bytes
			s.receiving -= message.bytes
			s.budget.release(message.bytes, false)
			s.signalLocked()
			s.mu.Unlock()
			return message.message, nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

// Receive accepts a message atomically with its sequence. A repeated sequence is acknowledged
// without a second delivery. ErrBackpressure leaves both the sequence and budget unchanged;
// the link's receive-window protocol must pause this channel until capacity is available.
func (s *Session) Receive(packet Packet) (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.received, io.EOF
	}
	if packet.Sequence == 0 {
		return s.received, ErrSequence
	}
	if packet.Sequence <= s.received {
		return s.received, nil
	}
	if packet.Sequence != s.received+1 {
		return s.received, ErrSequence
	}
	if len(packet.Payload) > MaxFrameBytes {
		return s.received, ErrFrameTooLarge
	}
	message, err := jsonrpc.DecodeMessage(packet.Payload)
	if err != nil {
		return s.received, err
	}
	if len(packet.Payload) > s.limit/2-s.receiving {
		return s.received, ErrBackpressure
	}
	if ok, _ := s.budget.reserve(len(packet.Payload), false); !ok {
		return s.received, ErrBackpressure
	}
	s.incoming = append(s.incoming, incoming{message, len(packet.Payload)})
	s.used += len(packet.Payload)
	s.receiving += len(packet.Payload)
	s.received = packet.Sequence
	s.signalLocked()
	return s.received, nil
}

// Acknowledge releases exactly the messages the peer has retained, including when the ACK
// is delivered again after reconnecting. A peer cannot acknowledge an unsent message.
func (s *Session) Acknowledge(sequence uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return io.EOF
	}
	if sequence > s.sent {
		return ErrSequence
	}
	if sequence <= s.acknowledged {
		return nil
	}
	for len(s.outgoing) > 0 && s.outgoing[0].Sequence <= sequence {
		n := len(s.outgoing[0].Payload)
		s.outgoing[0] = Packet{}
		s.outgoing = s.outgoing[1:]
		s.used -= n
		s.sending -= n
		s.budget.release(n, true)
	}
	s.acknowledged = sequence
	s.signalLocked()
	return nil
}

// Next returns a copy of the first retained message after sequence. No socket state is involved.
func (s *Session) Next(sequence uint64) (Packet, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Packet{}, false
	}
	for _, packet := range s.outgoing {
		if packet.Sequence > sequence {
			return Packet{packet.Sequence, append([]byte(nil), packet.Payload...)}, true
		}
	}
	return Packet{}, false
}

// Idle reports that nothing is waiting for an acknowledgment or for the application to read.
func (s *Session) Idle() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.outgoing) == 0 && len(s.incoming) == 0
}

func (s *Session) Received() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.received }

func (s *Session) Closed() bool { s.mu.Lock(); defer s.mu.Unlock(); return s.closed }

// Close is the logical end of a session. It wakes all application and backpressure waiters,
// and releases its retained bytes exactly once even when both endpoints initiate shutdown.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	s.budget.release(s.sending, true)
	s.budget.release(s.receiving, false)
	s.sending, s.receiving = 0, 0
	s.used, s.outgoing, s.incoming = 0, nil, nil
	s.signalLocked()
	return nil
}

// Transport gives an existing logical session to the SDK exactly once. Reattachment replaces
// the carrier beneath Session and never reconnects this MCP transport.
type Transport struct {
	Session   *Session
	connected atomic.Bool
}

func (t *Transport) Connect(context.Context) (mcp.Connection, error) {
	if t.Session == nil || !t.connected.CompareAndSwap(false, true) {
		return nil, errors.New("逻辑会话已连接")
	}
	return t.Session, nil
}

var _ mcp.Connection = (*Session)(nil)
var _ mcp.Transport = (*Transport)(nil)
