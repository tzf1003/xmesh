package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: xmesh serve|status --config xmesh.yaml")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "status":
		return status(args[1:])
	case "version", "--version":
		fmt.Println("xmesh 0.1.0")
		return nil
	default:
		return fmt.Errorf("未知子命令: %s", args[0])
	}
}
func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	path := flags.String("config", "xmesh.yaml", "配置路径")
	local := flags.Bool("local", false, "显式启用阶段 1 的 127.0.0.1 开发监听")
	listen := flags.String("listen", "127.0.0.1:7433", "本机开发 MCP 地址")
	if e := flags.Parse(args); e != nil {
		return e
	}
	store, e := config.NewStore(*path)
	if e != nil {
		return e
	}
	if !*local {
		return fmt.Errorf("请使用 --local 运行本机工具验收；互联监听将在 mesh 阶段接入")
	}
	host, _, e := net.SplitHostPort(*listen)
	if e != nil || host != "127.0.0.1" {
		return fmt.Errorf("--local 必须监听 127.0.0.1")
	}
	if !store.Get().HasRole("agent") {
		return fmt.Errorf("本机模式要求 agent 角色")
	}
	a, e := agent.New(store)
	if e != nil {
		return e
	}
	defer a.Close()
	listener, e := net.Listen("tcp", *listen)
	if e != nil {
		return e
	}
	defer listener.Close()
	server := &http.Server{Handler: a.HTTP(agent.Local), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() { errc <- server.Serve(listener) }()
	fmt.Fprintln(os.Stderr, "本机 Agent MCP: http://"+listener.Addr().String()+"/mcp")
	select {
	case e = <-errc:
		if !errors.Is(e, http.ErrServerClosed) {
			return e
		}
	case <-ctx.Done():
		a.Close()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
	return nil
}
func status(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	endpoint := flags.String("endpoint", "http://127.0.0.1:7433/mcp", "运行中的 MCP 地址")
	machine := flags.Bool("json", false, "输出 JSON")
	if e := flags.Parse(args); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "xmesh-status", Version: "0.1.0"}, nil)
	s, e := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: *endpoint}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if e != nil {
		return e
	}
	defer s.Close()
	o, e := s.CallTool(ctx, &mcp.CallToolParams{Name: "sys.info", Arguments: map[string]any{}})
	if e != nil {
		return e
	}
	if o.IsError {
		return fmt.Errorf("状态读取失败: %v", o.StructuredContent)
	}
	if *machine {
		return json.NewEncoder(os.Stdout).Encode(o.StructuredContent)
	}
	b, _ := json.Marshal(o.StructuredContent)
	var info map[string]any
	_ = json.Unmarshal(b, &info)
	fmt.Printf("节点: %v\n平台: %v/%v\n后端: %v\n配额: %v\n", info["hostname"], info["os"], info["arch"], info["backends"], info["limits"])
	return nil
}
