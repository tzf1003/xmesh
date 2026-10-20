package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/control"
)

func init() { app.Version = buildVersion }

const controlUsage = `用法: xmesh control <子命令> [参数]

本机控制通道客户端（Unix 域套接字或 Windows 命名管道），输出均为 JSON。
公共参数: --node 节点名  --state-dir 目录（默认在当前用户的节点目录中自动发现）

子命令:
  status | peers | sessions       读取状态、对端、会话
  computer [--action get|pause|resume|revoke --session-id ID]
                                   查看桌面状态、暂停控制、恢复或撤销桌面会话
  events [--last-event-id ID] [--count N] [--timeout 时长]
                                   订阅事件流，每行一个事件（首条为 hello）；
                                   --count 把 hello 也算在内；空闲节点除 hello 外没有事件，
                                   所以要在有限时间内退出请加 --timeout（如 5s），到时正常退出；
                                   带 --last-event-id 时先补发其后的事件，id 失效则先收到 reset
  audit [--tool T --device D --caller C --status ok|error --event E
         --since RFC3339 --until RFC3339 --limit N --cursor C]
  drain [--mode keep|kill]        排空本节点；resume 恢复
  resume
  config get                      读取配置原文与 ETag
  config set [--if-match ETAG] [--dry-run] (--yaml-file 文件 | --set /路径=JSON ... | --delete /路径 ...)
  pair password show              查看连接密码是否已设置（不回显口令）
  pair password set (--password-stdin | --password-file 文件)
                                   设置或修改连接密码；其他设备输入它即可与本机配对
  pair password clear             清除连接密码，关闭被动配对
  pair discover                   列出内网中尚未配对、可以连接的设备
  pair connect (--address 地址 | --fingerprint 指纹) (--password-stdin | --password-file 文件)
                                   用对端的连接密码配对，成功即生效
  pair export [--out 文件]        导出配对文档（高级用法，仅 lan 模式）
  pair import --file 文件 --fingerprint 指纹 [--tags tag:peer,tag:device]
                                   导入配对文档（高级用法）；指纹须是另行核对过的，导入后重启节点生效
  pats list                       列出 Gateway PAT（不含令牌）
  pats create --name 名字 --scope S[,S...]
                                   创建 PAT；令牌只在这条输出里出现一次
  pats rotate 名字                轮转令牌（旧令牌立即失效，新令牌只出现一次）
  pats delete 名字
  ingress clients                 列出已注册的 OAuth 客户端
  ingress revoke 客户端ID         撤销客户端及其已签发的令牌
  ingress password [--password-file 文件]
                                   改登录口令；口令从文件或标准输入读，不能放在命令行上`

// controlTarget resolves which node's channel to talk to.
func controlTarget(node, stateDir string) (control.Endpoint, error) {
	if stateDir != "" {
		return control.ReadEndpoint(stateDir)
	}
	found, err := control.Discover()
	if err != nil {
		return control.Endpoint{}, err
	}
	matches := found[:0:0]
	for _, e := range found {
		if node == "" || e.Node == node {
			matches = append(matches, e)
		}
	}
	switch len(matches) {
	case 0:
		return control.Endpoint{}, fmt.Errorf("没有找到运行中的节点（缺少 control.json）；节点是否已启动？")
	case 1:
		return matches[0], nil
	}
	names := []string{}
	for _, e := range matches {
		names = append(names, e.Node)
	}
	return control.Endpoint{}, fmt.Errorf("找到多个节点（%s），请用 --node 指定", strings.Join(names, "、"))
}

type targetFlags struct{ node, stateDir *string }

func addTarget(f *flag.FlagSet) targetFlags {
	return targetFlags{f.String("node", "", "节点名"), f.String("state-dir", "", "节点状态目录")}
}
func (t targetFlags) client() (*control.Client, error) {
	e, err := controlTarget(*t.node, *t.stateDir)
	if err != nil {
		return nil, err
	}
	return control.NewClient(e), nil
}

// show prints a successful reply's body, or returns the channel's error.
func show(r *control.Response, err error) error {
	if err != nil {
		return err
	}
	if err = r.Error(); err != nil {
		return err
	}
	var pretty json.RawMessage = r.Body
	out, _ := json.MarshalIndent(pretty, "", "  ")
	fmt.Println(string(out))
	return nil
}

func controlCommand(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", controlUsage)
	}
	verb, rest := args[0], args[1:]
	flags := flag.NewFlagSet("control "+verb, flag.ContinueOnError)
	target := addTarget(flags)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch verb {
	case "computer":
		action := flags.String("action", "get", "get、pause、resume 或 revoke")
		id := flags.String("session-id", "", "撤销的桌面会话 ID")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		switch *action {
		case "get":
			return show(c.Do(ctx, "GET", "/v2/computer", nil, nil))
		case "pause", "resume":
			return show(c.Do(ctx, "POST", "/v2/computer/pause", v2.ComputerPauseRequest{Paused: *action == "pause"}, nil))
		case "revoke":
			if *id == "" {
				return fmt.Errorf("revoke 需要 --session-id")
			}
			return show(c.Do(ctx, "POST", "/v2/computer/revoke", v2.ComputerRevokeRequest{SessionID: *id}, nil))
		default:
			return fmt.Errorf("未知桌面操作: %s", *action)
		}
	case "help", "--help", "-h":
		fmt.Println(controlUsage)
		return nil
	case "status", "peers", "sessions":
		if err := flags.Parse(rest); err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "GET", "/v2/"+verb, nil, nil))
	case "resume":
		if err := flags.Parse(rest); err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "POST", "/v2/resume", nil, nil))
	case "drain":
		mode := flags.String("mode", "keep", "keep 或 kill")
		if err := flags.Parse(rest); err != nil {
			return err
		}
		c, err := target.client()
		if err != nil {
			return err
		}
		return show(c.Do(ctx, "POST", "/v2/drain", map[string]string{"mode": *mode}, nil))
	case "audit":
		return controlAudit(ctx, flags, target, rest)
	case "events":
		return controlEvents(ctx, flags, target, rest)
	case "config":
		return controlConfig(ctx, flags, target, rest)
	case "pair":
		return controlPair(ctx, flags, target, rest)
	case "pats":
		return controlPats(ctx, flags, target, rest)
	case "ingress":
		return controlIngress(ctx, flags, target, rest)
	}
	return fmt.Errorf("未知的 control 子命令: %s\n\n%s", verb, controlUsage)
}

func controlAudit(ctx context.Context, flags *flag.FlagSet, target targetFlags, args []string) error {
	query := map[string]*string{}
	for _, name := range []string{"tool", "device", "caller", "status", "event", "since", "until", "limit", "cursor"} {
		query[name] = flags.String(name, "", "")
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	c, err := target.client()
	if err != nil {
		return err
	}
	values := []string{}
	for name, v := range query {
		if *v != "" {
			values = append(values, name+"="+queryEscape(*v))
		}
	}
	path := "/v2/audit"
	if len(values) > 0 {
		path += "?" + strings.Join(values, "&")
	}
	return show(c.Do(ctx, "GET", path, nil, nil))
}
func queryEscape(s string) string {
	var b strings.Builder
	for _, r := range []byte(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '~':
			b.WriteByte(r)
		default:
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}

// controlEvents prints each event's JSON on one line until the count is reached,
// the timeout passes or the stream ends. The hello event counts; an idle node sends nothing
// else (heartbeats are SSE comments), so a count alone can wait for ever.
func controlEvents(ctx context.Context, flags *flag.FlagSet, target targetFlags, args []string) error {
	last := flags.String("last-event-id", "", "从该事件之后续传")
	count := flags.Int("count", 0, "收到这么多事件（含 hello）后退出；0 表示不按数量退出")
	timeout := flags.Duration("timeout", 0, "订阅这么长时间后正常退出，如 5s；0 表示不限时")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *timeout < 0 || *count < 0 {
		return fmt.Errorf("--count 和 --timeout 不能为负")
	}
	c, err := target.client()
	if err != nil {
		return err
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	headers := map[string]string{}
	if *last != "" {
		headers["Last-Event-ID"] = *last
	}
	res, err := c.Stream(ctx, "/v2/events", headers)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		return (&control.Response{Status: res.StatusCode, Body: body}).Error()
	}
	seen := 0
	scanner := bufio.NewScanner(res.Body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
			fmt.Println(data)
			if seen++; *count > 0 && seen >= *count {
				return nil
			}
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return scanner.Err()
}

type editFlags []control.Edit

func (e *editFlags) String() string { return fmt.Sprint(len(*e)) }
func (e *editFlags) Set(v string) error {
	path, raw, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("--set 需要 /路径=JSON 值")
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return fmt.Errorf("--set 的值不是 JSON: %w", err)
	}
	*e = append(*e, control.Edit{Path: path, Value: value})
	return nil
}

type deleteFlags []control.Edit

func (d *deleteFlags) String() string { return "" }
func (d *deleteFlags) Set(path string) error {
	*d = append(*d, control.Edit{Path: path, Delete: true})
	return nil
}

func controlConfig(ctx context.Context, flags *flag.FlagSet, target targetFlags, args []string) error {
	if len(args) == 0 || (args[0] != "get" && args[0] != "set" && args[0] != "reload") {
		return fmt.Errorf("用法: xmesh control config get|set|reload")
	}
	verb := args[0]
	var edits editFlags
	match := flags.String("if-match", "", "ETag（来自 config get）；缺省使用刚读到的当前值")
	dry := flags.Bool("dry-run", false, "只校验并返回差异")
	yamlFile := flags.String("yaml-file", "", "用该文件的内容整体替换配置")
	flags.Var(&edits, "set", "/路径=JSON，可重复")
	flags.Var((*deleteFlags)(&edits), "delete", "/路径，删除该键，可重复")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	c, err := target.client()
	if err != nil {
		return err
	}
	if verb == "get" {
		return show(c.Do(ctx, "GET", "/v2/config", nil, nil))
	}
	if verb == "reload" {
		return show(c.Do(ctx, "POST", "/v2/config/reload", nil, nil))
	}
	current, err := c.Do(ctx, "GET", "/v2/config", nil, nil)
	if err != nil {
		return err
	}
	if err = current.Error(); err != nil {
		return err
	}
	var file v2.ConfigResponse
	if err = json.Unmarshal(current.Body, &file); err != nil {
		return err
	}
	if tag := strings.Trim(*match, `"`); tag != "" && tag != file.ETag {
		return fmt.Errorf("配置文件已被修改（当前 ETag %s），请重新读取", file.ETag)
	}
	var next []byte
	switch {
	case *yamlFile != "" && len(edits) == 0:
		if next, err = os.ReadFile(*yamlFile); err != nil {
			return err
		}
	case *yamlFile == "" && len(edits) > 0:
		if next, err = control.ApplyEdits([]byte(file.Content), edits); err != nil {
			return err
		}
	default:
		return fmt.Errorf("--yaml-file 与 --set/--delete 二选一")
	}
	return show(c.Do(ctx, "PUT", "/v2/config", v2.ConfigPutRequest{Content: string(next), DryRun: *dry}, map[string]string{"If-Match": `"` + file.ETag + `"`}))
}

func text2(v any) string { return fmt.Sprint(v) }

// controlStatus implements `xmesh status --control`.
func controlStatus(node, stateDir string, machine bool) error {
	e, err := controlTarget(node, stateDir)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := control.NewClient(e).Do(ctx, "GET", "/v2/status", nil, nil)
	if err != nil {
		return err
	}
	if err = r.Error(); err != nil {
		return err
	}
	if machine {
		fmt.Println(string(r.Body))
		return nil
	}
	var info map[string]any
	if err = json.Unmarshal(r.Body, &info); err != nil {
		return err
	}
	text := formatStatus(info)
	if drain, ok := info["drain"].(map[string]any); ok && drain["draining"] == true {
		text += "排空中: 是（" + text2(drain["mode"]) + "）\n"
	}
	fmt.Print(text)
	return nil
}
