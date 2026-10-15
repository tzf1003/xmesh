package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/mcpmux"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/pty"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/toolerr"
)

type budgetPeer struct {
	Name    string `json:"name"`
	Epoch   string `json:"epoch"`
	Pending bool   `json:"pending"`
}

type budgetState struct {
	Version int                   `json:"version"`
	Peers   map[string]budgetPeer `json:"peers"`
}

// PTYBudget serializes admissions across all clients of one ingress. The journal
// retains Agents with live OAuth terminals and fences calls with uncertain results.
type PTYBudget struct {
	limit int
	path  string
	gate  chan struct{}
	state budgetState
	fault bool
}

func NewPTYBudget(limit int, path string) (*PTYBudget, error) {
	if limit < 1 {
		return nil, fmt.Errorf("入口 PTY 预算必须为正整数")
	}
	b := &PTYBudget{limit: limit, path: path, gate: make(chan struct{}, 1), state: budgetState{Version: 1, Peers: map[string]budgetPeer{}}}
	if path == "" {
		return b, nil
	}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return b, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取入口 PTY 状态: %w", err)
	}
	var loaded budgetState
	if err = json.Unmarshal(body, &loaded); err != nil || loaded.Version != 1 || loaded.Peers == nil {
		return nil, fmt.Errorf("入口 PTY 状态无效")
	}
	b.state = loaded
	for id, peer := range b.state.Peers {
		if id == "" || peer.Name == "" || peer.Epoch == "" {
			return nil, fmt.Errorf("入口 PTY 设备状态无效")
		}
	}
	return b, nil
}

func budgetUnavailable() error {
	return toolerr.New("policy_denied", "无法确认入口 PTY 预算", "rule", "quota.unavailable", "reason", "等待设备恢复并确认资源状态；结果不确定的操作需要重启对应 Agent 后再确认")
}

func (b *PTYBudget) save(state budgetState) error {
	if b.path != "" {
		if err := writeBudgetState(b.path, state); err != nil {
			b.fault = true
			return err
		}
	}
	b.state = state
	return nil
}

func writeBudgetState(path string, state budgetState) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".pty-budget-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = json.NewEncoder(f).Encode(state); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		parent, err := os.Open(dir)
		if err != nil {
			return err
		}
		defer parent.Close()
		return parent.Sync()
	}
	return nil
}

type peerBudget struct {
	peer     mesh.Peer
	bridge   *mcpmux.Bridge
	snapshot pty.BudgetSnapshot
	err      error
}

func (v *view) readPTYBudget(ctx context.Context, peer mesh.Peer) peerBudget {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	v.waitReady(ctx, peer.Name)
	v.mu.Lock()
	d := v.down[peer.Name]
	ready := d != nil && !d.dead && d.peerID == peer.ID && d.peerAddress == peer.Address
	v.mu.Unlock()
	out := peerBudget{peer: peer}
	if !ready {
		out.err = budgetUnavailable()
		return out
	}
	out.bridge = d.bridge
	result, err := d.bridge.Session.ReadResource(ctx, &mcp.ReadResourceParams{URI: pty.BudgetURI})
	if err != nil {
		out.err = err
		return out
	}
	if result == nil || len(result.Contents) != 1 || result.Contents[0] == nil || result.Contents[0].URI != pty.BudgetURI || len(result.Contents[0].Text) > 4<<20 {
		out.err = budgetUnavailable()
		return out
	}
	out.err = json.Unmarshal([]byte(result.Contents[0].Text), &out.snapshot)
	if out.err == nil {
		out.err = out.snapshot.Validate()
	}
	return out
}

func (b *PTYBudget) call(ctx context.Context, v *view, target mesh.Peer, tool string, args map[string]json.RawMessage, invoke func(*mcpmux.Bridge) (*mcp.CallToolResult, error)) (*mcp.CallToolResult, error) {
	select {
	case b.gate <- struct{}{}:
		defer func() { <-b.gate }()
	case <-ctx.Done():
		return agent.Result(nil, budgetUnavailable()), nil
	}
	if ctx.Err() != nil || b.fault || v.p.ClientID == "" {
		return agent.Result(nil, budgetUnavailable()), nil
	}
	devices, generation := v.g.snapshot()
	peers := map[string]mesh.Peer{}
	for _, device := range devices {
		if device.online {
			if device.peer.ID == "" {
				return agent.Result(nil, budgetUnavailable()), nil
			}
			peers[device.peer.ID] = device.peer
		}
	}
	if peer, ok := peers[target.ID]; !ok || peer.Name != target.Name || peer.Address != target.Address {
		return agent.Result(nil, budgetUnavailable()), nil
	}
	for id := range b.state.Peers {
		if _, ok := peers[id]; !ok {
			return agent.Result(nil, budgetUnavailable()), nil
		}
	}
	results := make(chan peerBudget, len(peers))
	for _, peer := range peers {
		go func() { results <- v.readPTYBudget(ctx, peer) }()
	}
	snapshots := make(map[string]peerBudget, len(peers))
	next := budgetState{Version: 1, Peers: map[string]budgetPeer{}}
	for range peers {
		result := <-results
		if result.err != nil {
			return agent.Result(nil, budgetUnavailable()), nil
		}
		snapshots[result.peer.ID] = result
		old := b.state.Peers[result.peer.ID]
		if old.Pending && old.Epoch == result.snapshot.Epoch {
			return agent.Result(nil, budgetUnavailable()), nil
		}
		if len(result.snapshot.Sessions) > 0 {
			next.Peers[result.peer.ID] = budgetPeer{Name: result.peer.Name, Epoch: result.snapshot.Epoch}
		}
	}
	_, currentGeneration := v.g.snapshot()
	if generation != currentGeneration || ctx.Err() != nil {
		return agent.Result(nil, budgetUnavailable()), nil
	}
	principal := "oauth:" + v.p.ClientID
	increase := true
	if tool == "pty.transfer" {
		var id, to string
		if json.Unmarshal(args["session_id"], &id) != nil || json.Unmarshal(args["to_caller"], &to) != nil {
			return agent.Result(nil, toolerr.Args("session_id/to_caller")), nil
		}
		snapshot := snapshots[target.ID].snapshot
		var ok bool
		principal, ok = snapshot.Callers[to]
		if !ok {
			return agent.Result(nil, toolerr.Args("to_caller")), nil
		}
		from, exists := snapshot.Sessions[id]
		if !exists {
			return agent.Result(nil, toolerr.New("session_not_found", "终端会话不存在", "session_id", id)), nil
		}
		increase = from != principal
	}
	inUse := 0
	for _, result := range snapshots {
		count := result.snapshot.Counts[principal]
		// Compare before summing so a malformed large count cannot overflow.
		if increase && count >= b.limit-inUse {
			return agent.Result(nil, toolerr.New("policy_denied", "入口客户端终端配额已满", "rule", "quota.exceeded", "reason", "跨设备 active/orphaned 终端已达到入口 max_pty_per_client")), nil
		}
		if increase {
			inUse += count
		}
	}
	next.Peers[target.ID] = budgetPeer{Name: target.Name, Epoch: snapshots[target.ID].snapshot.Epoch, Pending: true}
	if err := b.save(next); err != nil {
		return agent.Result(nil, budgetUnavailable()), nil
	}
	out, err := invoke(snapshots[target.ID].bridge)
	if err == nil && out != nil {
		finished := budgetState{Version: 1, Peers: map[string]budgetPeer{}}
		for id, peer := range b.state.Peers {
			peer.Pending = false
			finished.Peers[id] = peer
		}
		if saveErr := b.save(finished); saveErr != nil {
			slog.Error("pty_budget_state_write_failed")
		}
	}
	return out, err
}
