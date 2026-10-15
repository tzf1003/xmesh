package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/winbox"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
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
	case "__sandbox-supervisor":
		return winbox.RunProcessSupervisor(args[1:])
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
	patFile := flags.String("pat-file", "", "本机 Gateway 的 PAT 文件")
	if e := flags.Parse(args); e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "xmesh-status", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{Endpoint: *endpoint}
	if *patFile != "" {
		u, err := url.Parse(*endpoint)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil {
			return fmt.Errorf("PAT 仅用于本机 Gateway")
		}
		f, err := os.Open(*patFile)
		if err != nil {
			return fmt.Errorf("无法读取 PAT 文件")
		}
		b, err := io.ReadAll(io.LimitReader(f, 4097))
		f.Close()
		token := strings.TrimSpace(string(b))
		if err != nil || len(b) > 4096 || len(token) < 32 || strings.ContainsAny(token, " \t\r\n") {
			return fmt.Errorf("PAT 文件无效")
		}
		transport.HTTPClient = &http.Client{Transport: patTransport{token: token}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	s, e := c.Connect(ctx, transport, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
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

type patTransport struct{ token string }

func (p patTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+p.token)
	return http.DefaultTransport.RoundTrip(copy)
}
