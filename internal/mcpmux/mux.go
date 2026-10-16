// Package mcpmux is the common prefix, request and progress routing boundary.
package mcpmux

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/toolerr"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func Split(name, separator string) (string, string, error) {
	prefix, tool, ok := strings.Cut(name, separator)
	if !ok || prefix == "" {
		return "", "", toolerr.New("not_found", "工具前缀不存在", "name", name)
	}
	if tool == "" {
		return "", "", toolerr.Args("name")
	}
	return prefix, tool, nil
}
func Prefix(prefix, name, separator string) string { return prefix + separator + name }
func Rename(tool *mcp.Tool, name string) *mcp.Tool { copy := *tool; copy.Name = name; return &copy }

type route struct {
	upstream string
	id       jsonrpc.ID
}
type Mapper struct {
	mu      sync.Mutex
	next    uint64
	pending map[jsonrpc.ID]route
}

func NewMapper() *Mapper { return &Mapper{pending: map[jsonrpc.ID]route{}} }
func (m *Mapper) Down(upstream string, id jsonrpc.ID) jsonrpc.ID {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.next++
	down, _ := jsonrpc.MakeID(fmt.Sprintf("xmesh-%d", m.next))
	m.pending[down] = route{upstream, id}
	return down
}
func (m *Mapper) Up(down jsonrpc.ID) (string, jsonrpc.ID, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.pending[down]
	delete(m.pending, down)
	return r.upstream, r.id, ok
}
func (m *Mapper) Lookup(upstream string, id jsonrpc.ID) (jsonrpc.ID, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for down, r := range m.pending {
		if r.upstream == upstream && r.id == id {
			return down, true
		}
	}
	return jsonrpc.ID{}, false
}
func (m *Mapper) Disconnect(upstream string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for down, r := range m.pending {
		if r.upstream == upstream {
			delete(m.pending, down)
		}
	}
}

type Transport struct {
	Base     mcp.Transport
	Mapper   *Mapper
	Upstream string
	progress func(context.Context, *mcp.ProgressNotificationParams)
}

// SessionTransport assigns a process-generated session to an in-memory link.
type SessionTransport struct {
	Base mcp.Transport
	ID   string
}

func (t *SessionTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	c, e := t.Base.Connect(ctx)
	if e != nil {
		return nil, e
	}
	return &sessionConnection{Connection: c, id: t.ID}, nil
}

type sessionConnection struct {
	mcp.Connection
	id string
}

func (c *sessionConnection) SessionID() string { return c.id }

func (t *Transport) Connect(ctx context.Context) (mcp.Connection, error) {
	c, e := t.Base.Connect(ctx)
	if e != nil {
		return nil, e
	}
	return &connection{Connection: c, mapper: t.Mapper, upstream: t.Upstream, progress: t.progress}, nil
}

type connection struct {
	mcp.Connection
	mapper   *Mapper
	upstream string
	progress func(context.Context, *mcp.ProgressNotificationParams)
}

func (c *connection) Write(ctx context.Context, msg jsonrpc.Message) error {
	if req, ok := msg.(*jsonrpc.Request); ok {
		copy := *req
		if req.IsCall() {
			copy.ID = c.mapper.Down(c.upstream, req.ID)
		} else if req.Method == "notifications/cancelled" {
			var p map[string]json.RawMessage
			if json.Unmarshal(req.Params, &p) == nil {
				var id any
				if json.Unmarshal(p["requestId"], &id) == nil {
					up, _ := jsonrpc.MakeID(id)
					if down, ok := c.mapper.Lookup(c.upstream, up); ok {
						p["requestId"], _ = json.Marshal(down.Raw())
						copy.Params, _ = json.Marshal(p)
					}
				}
			}
		}
		e := c.Connection.Write(ctx, &copy)
		if e != nil && req.IsCall() {
			c.mapper.Up(copy.ID)
		}
		return e
	}
	return c.Connection.Write(ctx, msg)
}
func (c *connection) Read(ctx context.Context) (jsonrpc.Message, error) {
	for {
		msg, e := c.Connection.Read(ctx)
		if e != nil {
			return nil, e
		}
		if resp, ok := msg.(*jsonrpc.Response); ok {
			up, id, exists := c.mapper.Up(resp.ID)
			if !exists || up != c.upstream {
				continue
			}
			copy := *resp
			copy.ID = id
			return &copy, nil
		}
		if req, ok := msg.(*jsonrpc.Request); ok && !req.IsCall() && req.Method == "notifications/progress" && c.progress != nil {
			var params mcp.ProgressNotificationParams
			if err := json.Unmarshal(req.Params, &params); err != nil {
				slog.Debug("progress_invalid")
				continue
			}
			// Consume progress before reading the response that retires its route.
			// SDK notification handlers may run after the response is delivered.
			c.progress(ctx, &params)
			continue
		}
		return msg, nil
	}
}
func (c *connection) Close() error { c.mapper.Disconnect(c.upstream); return c.Connection.Close() }

type Progress struct {
	Original any
	Send     func(context.Context, *mcp.ProgressNotificationParams) error
}
type progressRoute struct {
	Progress
	ctx     context.Context
	pending sync.WaitGroup
}
type Bridge struct {
	Session  *mcp.ClientSession
	mu       sync.Mutex
	progress map[string]*progressRoute
	sequence atomic.Uint64
	unrouted func()
	reported time.Time
}
type Options struct {
	Changed func()
	Reverse func() *mcp.ServerSession
	// Unrouted is told about a progress notification whose token matches no
	// pending call (CTR-MUX-02). It runs on its own goroutine and is invoked at
	// most once per second per Bridge, so a noisy backend cannot flood the audit.
	Unrouted func()
}

// unroutedInterval bounds how often Options.Unrouted fires for one Bridge.
const unroutedInterval = time.Second

func Connect(ctx context.Context, t mcp.Transport, opts Options) (*Bridge, error) {
	b := &Bridge{progress: map[string]*progressRoute{}, unrouted: opts.Unrouted}
	client := mcp.NewClient(&mcp.Implementation{Name: "xmesh-mux", Version: "0.1.0"}, &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			if opts.Changed != nil {
				go opts.Changed()
			}
		},
		CreateMessageHandler: func(ctx context.Context, r *mcp.CreateMessageRequest) (*mcp.CreateMessageResult, error) {
			if opts.Reverse != nil {
				if upstream := opts.Reverse(); upstream != nil {
					return upstream.CreateMessage(ctx, r.Params)
				}
			}
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "此后端没有可路由的租约持有者"}
		},
		ElicitationHandler: func(ctx context.Context, r *mcp.ElicitRequest) (*mcp.ElicitResult, error) {
			if opts.Reverse != nil {
				if upstream := opts.Reverse(); upstream != nil {
					return upstream.Elicit(ctx, r.Params)
				}
			}
			return nil, &jsonrpc.Error{Code: jsonrpc.CodeMethodNotFound, Message: "此后端没有可路由的租约持有者"}
		},
	})
	session, e := client.Connect(ctx, &Transport{Base: t, Mapper: NewMapper(), Upstream: "client", progress: b.notifyProgress}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if e != nil {
		return nil, e
	}
	b.Session = session
	return b, nil
}

func (b *Bridge) notifyProgress(ctx context.Context, params *mcp.ProgressNotificationParams) {
	token, isString := params.ProgressToken.(string)
	b.mu.Lock()
	route, ok := b.progress[token]
	if isString && ok {
		route.pending.Add(1)
	}
	report := false
	if !isString || !ok {
		if b.unrouted != nil && time.Since(b.reported) >= unroutedInterval {
			b.reported = time.Now()
			report = true
		}
	}
	b.mu.Unlock()
	if !isString || !ok {
		slog.Debug("progress_unrouted")
		if report {
			go b.unrouted()
		}
		return
	}
	p := *params
	p.ProgressToken = route.Original
	go func() {
		defer route.pending.Done()
		_ = route.Send(route.ctx, &p)
	}()
}

func (b *Bridge) Call(ctx context.Context, name string, args any, progress Progress) (*mcp.CallToolResult, error) {
	params := &mcp.CallToolParams{Name: name, Arguments: args}
	if progress.Send != nil && progress.Original != nil {
		token := fmt.Sprintf("p-%d", b.sequence.Add(1))
		route := &progressRoute{Progress: progress, ctx: ctx}
		b.mu.Lock()
		b.progress[token] = route
		b.mu.Unlock()
		defer func() {
			b.mu.Lock()
			delete(b.progress, token)
			b.mu.Unlock()
			route.pending.Wait()
		}()
		params.SetProgressToken(token)
	}
	return b.Session.CallTool(ctx, params)
}
func (b *Bridge) Tools(ctx context.Context) ([]*mcp.Tool, error) {
	tools := []*mcp.Tool{}
	for tool, e := range b.Session.Tools(ctx, nil) {
		if e != nil {
			return nil, e
		}
		tools = append(tools, tool)
	}
	return tools, nil
}
func (b *Bridge) Close() error { return b.Session.Close() }
