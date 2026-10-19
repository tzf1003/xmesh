package devicelink

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func request(n int64) *jsonrpc.Request {
	id, _ := jsonrpc.MakeID(n)
	return &jsonrpc.Request{ID: id, Method: "ping"}
}

func TestLostAcknowledgmentReplaysWithoutSecondDelivery(t *testing.T) {
	budget := NewBudget(1024)
	a, b := NewSession("stable-session", budget), NewSession("stable-session", budget)
	defer a.Close()
	defer b.Close()
	if err := a.Write(t.Context(), request(1)); err != nil {
		t.Fatal(err)
	}
	packet, ok := a.Next(0)
	if !ok {
		t.Fatal("message missing")
	}
	ack, err := b.Receive(packet)
	if err != nil || ack != 1 {
		t.Fatal(ack, err)
	}
	// The carrier fails after delivery but before its acknowledgment reaches the sender.
	replay, ok := a.Next(0)
	if !ok {
		t.Fatal("unacknowledged message not retained")
	}
	ack, err = b.Receive(replay)
	if err != nil || ack != 1 {
		t.Fatal("duplicate sequence rejected", ack, err)
	}
	if err := a.Acknowledge(ack); err != nil {
		t.Fatal(err)
	}
	got, err := b.Read(t.Context())
	if err != nil || got.(*jsonrpc.Request).ID != request(1).ID {
		t.Fatal(got, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	if _, err := b.Read(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("duplicate delivered", err)
	}
	if budget.Used() != 0 {
		t.Fatal("retained message leak", budget.Used())
	}
}

func TestSequenceGapsAndUnsentAcknowledgmentsDoNotChangeState(t *testing.T) {
	s := NewSession("s", NewBudget(1024))
	defer s.Close()
	raw, _ := jsonrpc.EncodeMessage(request(1))
	if _, err := s.Receive(Packet{Sequence: 2, Payload: raw}); !errors.Is(err, ErrSequence) {
		t.Fatal(err)
	}
	if s.Received() != 0 {
		t.Fatal("gap advanced receive cursor")
	}
	if err := s.Write(t.Context(), request(1)); err != nil {
		t.Fatal(err)
	}
	if err := s.Acknowledge(2); !errors.Is(err, ErrSequence) {
		t.Fatal("unsent message acknowledged", err)
	}
	if _, ok := s.Next(0); !ok {
		t.Fatal("invalid acknowledgment removed message")
	}
}

func TestBoundedBufferWakesAfterAckAndClose(t *testing.T) {
	raw, _ := jsonrpc.EncodeMessage(request(1))
	s := NewSession("s", NewBudget(2*len(raw)))
	if err := s.Write(t.Context(), request(1)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Write(t.Context(), request(2)) }()
	select {
	case err := <-done:
		t.Fatal("buffer did not apply backpressure", err)
	case <-time.After(10 * time.Millisecond):
	}
	if err := s.Acknowledge(1); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("ACK did not release writer")
	}
	go func() { done <- s.Write(t.Context(), request(3)) }()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not release writer")
	}
	if s.budget.Used() != 0 {
		t.Fatal("close leaked bytes")
	}
}

func TestConcurrentCloseReleasesBudgetOnce(t *testing.T) {
	s := NewSession("s", NewBudget(1024))
	if err := s.Write(t.Context(), request(1)); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { _ = s.Close() })
	}
	wg.Wait()
	if s.budget.Used() != 0 {
		t.Fatal(s.budget.Used())
	}
}

func TestFullSendWindowsReserveCapacityForInboundMessages(t *testing.T) {
	raw, _ := jsonrpc.EncodeMessage(request(1))
	a, b := NewSession("a", NewBudget(2*len(raw))), NewSession("b", NewBudget(2*len(raw)))
	defer a.Close()
	defer b.Close()
	if err := a.Write(t.Context(), request(1)); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(t.Context(), request(1)); err != nil {
		t.Fatal(err)
	}
	aa, _ := a.Next(0)
	bb, _ := b.Next(0)
	if _, err := a.Receive(bb); err != nil {
		t.Fatal("full send window blocked reverse traffic", err)
	}
	if _, err := b.Receive(aa); err != nil {
		t.Fatal("full send window blocked reverse traffic", err)
	}
}

func pump(ctx context.Context, source, target *Session, duplicate bool) {
	for {
		changed := source.Changed()
		packet, ok := source.Next(0)
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-changed:
			}
			continue
		}
		ack, err := target.Receive(packet)
		if errors.Is(err, ErrBackpressure) {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Millisecond):
			}
			continue
		}
		if err != nil {
			return
		}
		if duplicate {
			_, _ = target.Receive(packet)
		}
		if source.Acknowledge(ack) != nil {
			return
		}
	}
}

func TestMCPSessionAndInFlightCallSurviveCarrierReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	clientConn, serverConn := NewSession("logical-1", NewBudget(0)), NewSession("logical-1", NewBudget(0))
	defer clientConn.Close()
	defer serverConn.Close()
	carrier, stopCarrier := context.WithCancel(ctx)
	go pump(carrier, clientConn, serverConn, true)
	go pump(carrier, serverConn, clientConn, true)
	started, release := make(chan struct{}), make(chan struct{})
	var executions atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "agent", Version: "2"}, nil)
	server.AddTool(&mcp.Tool{Name: "exec.once", InputSchema: map[string]any{"type": "object"}}, func(callCtx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		executions.Add(1)
		close(started)
		select {
		case <-release:
		case <-callCtx.Done():
			return nil, callCtx.Err()
		}
		return &mcp.CallToolResult{StructuredContent: map[string]any{"executions": 1}}, nil
	})
	ss, err := server.Connect(ctx, &Transport{Session: serverConn}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "gateway", Version: "2"}, nil)
	cs, err := client.Connect(ctx, &Transport{Session: clientConn}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	result := make(chan error, 1)
	go func() {
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "exec.once", Arguments: map[string]any{}})
		if err == nil {
			raw, _ := json.Marshal(out.StructuredContent)
			if string(raw) != `{"executions":1}` {
				err = errors.New("wrong result")
			}
		}
		result <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("call not started")
	}
	stopCarrier()
	close(release)
	// The same SDK connections are reattached; neither logical session was closed.
	go pump(ctx, clientConn, serverConn, true)
	go pump(ctx, serverConn, clientConn, true)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("call not recovered")
	}
	if executions.Load() != 1 || cs.ID() != "logical-1" || ss.ID() != "logical-1" {
		t.Fatal("logical identity or single execution lost", executions.Load(), cs.ID(), ss.ID())
	}
}
