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
	"strconv"
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

// buildVersion is overridden at release time with -ldflags "-X main.buildVersion=...".
var buildVersion = "0.1.0"

const usage = `用法: xmesh <子命令> [参数]

子命令:
  serve    按配置启动已启用的角色
           --config 路径  --local  --listen 地址  --state-dir 目录
           --ingress  --tunnel-listen 地址  --oauth-password-file 文件
           --control-allow-sid SID,...（仅 Windows：额外允许访问控制管道的用户）
  install  安装为本平台的用户级服务（Windows 默认注册当前用户的登录计划任务）
           --config 路径  --executable 路径  --state-dir 目录
           --oauth-password-file 文件  --system（仅 Windows：管理员 SCM 服务）
  pair     lan 模式下交换节点证书
           --config 路径  --state-dir 目录
           --export 文件，或 --import 文件 --fingerprint 指纹 [--tags tag:peer,tag:device]
  status   读取运行中节点的状态（不读配置文件，没有 --config）
           --endpoint 地址  --tool 工具名  --pat-file 文件  --json
           --control [--node 节点名 | --state-dir 目录]  经本机控制通道读取，不需要 PAT
  control  本机控制通道客户端：status、peers、sessions、events、audit、drain、resume、config
           （xmesh control help 查看参数）
  uninstall / start / stop / restart
           卸载、启动、停止、重启已安装的服务
           --config 路径（或 --name 服务名）  --system（仅 Windows）  --json
  version  打印版本号（也可写作 --version）`

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	switch args[0] {
	case "help", "--help", "-h":
		fmt.Println(usage)
		return nil
	case "__sandbox-supervisor":
		return winbox.RunProcessSupervisor(args[1:])
	case "__sandbox-guard":
		return winbox.RunGuardKeeper(args[1:])
	case "serve":
		return serve(args[1:])
	case "install":
		return withJSON("install", args[1:], install)
	case "pair":
		return withJSON("pair", args[1:], pair)
	case "status":
		return status(args[1:])
	case "control":
		return controlCommand(args[1:])
	case "uninstall", "start", "stop", "restart":
		return withJSON(args[0], args[1:], func(rest []string) error { return serviceCommand(args[0], rest) })
	case "version", "--version":
		fmt.Println("xmesh " + buildVersion)
		return nil
	default:
		return fmt.Errorf("未知子命令: %s\n\n%s", args[0], usage)
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
	controlSIDs := flags.String("control-allow-sid", "", "Windows：额外允许访问控制管道的 SID，逗号分隔")
	if e := flags.Parse(args); e != nil {
		return e
	}
	store, e := config.NewStore(*path)
	if e != nil {
		return e
	}
	options := app.Options{Local: *local, Listen: *listen, StateDir: *state, PasswordFile: *password, Ingress: *ingress, TunnelListen: *tunnel}
	if *controlSIDs != "" {
		options.ControlAllowSIDs = strings.Split(*controlSIDs, ",")
	}
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
	viaControl := flags.Bool("control", false, "经本机控制通道读取（不需要 PAT 或 MCP 地址）")
	node := flags.String("node", "", "与 --control 合用：节点名")
	stateDir := flags.String("state-dir", "", "与 --control 合用：节点状态目录")
	if e := flags.Parse(args); e != nil {
		return e
	}
	if *viaControl {
		return controlStatus(*node, *stateDir, *machine)
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
	if _, ok := info["hostname"]; !ok {
		// 不是 sys.info 的结果（例如 --tool devices.list），人类可读格式不适用。
		fmt.Println(string(b))
		return nil
	}
	fmt.Print(formatStatus(info))
	return nil
}

// formatStatus renders a sys.info result (CTR-SYS-01) as labeled lines; --json prints it unchanged.
func formatStatus(info map[string]any) string {
	var out strings.Builder
	fmt.Fprintf(&out, "节点: %s\n平台: %s/%s\n角色: %s\n", text(info["hostname"]), text(info["os"]), text(info["arch"]), joinList(info["roles"]))
	if network, ok := info["network"].(map[string]any); ok {
		fmt.Fprintf(&out, "网络身份: 模式 %s，节点 %s，标签 %s\n", text(network["mode"]), text(network["node_id"]), joinList(network["tags"]))
	}
	backends, _ := info["backends"].([]any)
	if len(backends) == 0 {
		out.WriteString("后端: 无\n")
	} else {
		out.WriteString("后端:\n")
		for _, entry := range backends {
			if backend, ok := entry.(map[string]any); ok {
				favorite := ""
				if backend["favorite"] == true {
					favorite = "，常用"
				}
				fmt.Fprintf(&out, "  %s（%s，%s%s）\n", text(backend["name"]), text(backend["policy"]), text(backend["state"]), favorite)
			}
		}
	}
	if limits, ok := info["limits"].(map[string]any); ok {
		out.WriteString("配额:\n")
		fmt.Fprintf(&out, "  exec 共享槽 %s/%s，独占槽 %s/%s\n", count(limits["exec_shared_in_use"]), count(limits["exec_shared"]), count(limits["exec_exclusive_in_use"]), count(limits["exec_exclusive"]))
		fmt.Fprintf(&out, "  终端 %s/%s（active %s，closed %s/%s），单个调用方上限 %s\n", count(limits["pty_in_use"]), count(limits["pty_max_sessions"]), count(limits["pty_active"]), count(limits["pty_closed"]), count(limits["pty_closed_retain_max"]), count(limits["pty_max_per_caller"]))
	}
	if info["restart_required"] == true {
		out.WriteString("需要重启: 是\n")
	}
	if warnings, ok := info["warnings"].([]any); ok {
		for _, warning := range warnings {
			fmt.Fprintf(&out, "警告: %v\n", warning)
		}
	}
	return out.String()
}

func text(v any) string {
	if v == nil {
		return "?"
	}
	return fmt.Sprint(v)
}

func joinList(v any) string {
	items, _ := v.([]any)
	if len(items) == 0 {
		return "无"
	}
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = fmt.Sprint(item)
	}
	return strings.Join(parts, "、")
}

// count prints a JSON number as an integer, or "?" when the field is absent.
func count(v any) string {
	n, ok := v.(float64)
	if !ok {
		return "?"
	}
	return strconv.FormatInt(int64(n), 10)
}

type patTransport struct{ token string }

func (p patTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	copy := r.Clone(r.Context())
	copy.Header.Set("Authorization", "Bearer "+p.token)
	return http.DefaultTransport.RoundTrip(copy)
}
