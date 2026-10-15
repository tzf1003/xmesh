package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/config"
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
		return fmt.Errorf("用法: xmesh serve|install|status|pair --config xmesh.yaml")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "install":
		return install(args[1:])
	case "pair":
		return pair(args[1:])
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
	local := flags.Bool("local", false, "显式启用本机开发模式")
	listen := flags.String("listen", "127.0.0.1:7433", "本机开发 Agent 地址")
	state := flags.String("state-dir", "", "节点状态目录")
	password := flags.String("oauth-password-file", "", "OAuth 单主体口令文件")
	ingress := flags.Bool("ingress", false, "启用已配置的公网入口")
	tunnel := flags.String("tunnel-listen", "127.0.0.1:8443", "反向隧道入口地址")
	asService := flags.Bool("service", false, "由系统服务管理器启动")
	if e := flags.Parse(args); e != nil {
		return e
	}
	store, e := config.NewStore(*path)
	if e != nil {
		return e
	}
	options := app.Options{Local: *local, Listen: *listen, StateDir: *state, PasswordFile: *password, Ingress: *ingress, TunnelListen: *tunnel}
	if *asService {
		return runService(store, options)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.Run(ctx, store, options)
}
func status(args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	endpoint := flags.String("endpoint", "http://127.0.0.1:7433/mcp", "运行中的 MCP 地址")
	machine := flags.Bool("json", false, "输出 JSON")
	tool := flags.String("tool", "sys.info", "状态工具名；Gateway 使用 local/sys.info")
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
	o, e := s.CallTool(ctx, &mcp.CallToolParams{Name: *tool, Arguments: map[string]any{}})
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
	fmt.Printf("节点: %v\n平台: %v/%v\n角色: %v\n网络身份: %v\n后端: %v\n配额: %v\n", info["hostname"], info["os"], info["arch"], info["roles"], info["network"], info["backends"], info["limits"])
	if warnings, ok := info["warnings"].([]any); ok {
		for _, warning := range warnings {
			fmt.Printf("警告: %v\n", warning)
		}
	}
	return nil
}
