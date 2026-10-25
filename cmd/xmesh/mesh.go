package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/meshadmin"
	"golang.org/x/term"
)

const meshUsage = `用法: xmesh mesh <子命令> [参数]

管理控制面（官方 Tailscale 或 Headscale）。管理凭据保存在系统钥匙串，不进 xmesh.yaml，内核不持有。
通用参数: --profile 名称（默认 default）  --credential-file 文件（无钥匙串的主机）  --json

子命令:
  login    保存管理凭据  --kind tailscale|headscale  --url 地址  --access read|write
           [--tailnet 名称]  [--client-id ID（Tailscale OAuth）]  密钥从 --secret-file 或标准输入读取
  logout   删除凭据  [--access read|write]
  status   设备、角色与 tag 检查  [--node-config xmesh.yaml ...]
  tags     查看或修改设备 tag  --device ID  [--set tag:a,tag:b]  [--apply]
  authkey  创建预授权 key  --out 文件 [--tags tag:a,tag:b]  [--ttl 1h] [--reusable] [--ephemeral] [--user 用户]
  onboard  新设备接入：创建 key 写入 --authkey-file（0600），可选 --config xmesh.yaml --install
  share    把 Tailscale 连接方式发给已配对设备  --device 名字或ID --config xmesh.yaml
           [--authkey-file 文件 | --reuse-local-key] [--user 用户] [--tags ...] [--reusable] [--ephemeral] [--apply]
           有管理凭据时自动生成 key，否则用你给的文件；对方手动接受后才生效
  policy   策略：--show | --snapshots | --rollback ID | --template personal|team|restricted --owner 属主
           [--apply] [--expect-version 版本]
           document 从标准输入解析或修改完整草稿；publish --expect-version 版本 [--apply --yes] 提交完整草稿
  matrix   网络层与工具层合并的权限矩阵  --node-config xmesh.yaml ...  [--port 7433]
  derp     第三方 DERP：plan|apply|probe|headscale|admit-serve|sync
           --region "code=lab,name=Lab,host=derp.example.com,ipv4=1.2.3.4,ipv6=...,id=900"

写入类命令默认只预览；加 --apply 才写入。写入前会校验、检查版本并保存快照。`

// meshCredentials is the credential store; tests replace it.
var meshCredentials meshadmin.CredentialStore = meshadmin.KeychainStore{}

// meshHTTP lets tests inject a client for admission-server calls.
var meshHTTP *http.Client

func meshCmd(args []string) error { return meshRun(args, os.Stdout, os.Stdin) }

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ";") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// meshCommon holds the flags shared by every subcommand.
type meshCommon struct {
	profile  string
	panel    bool
	credFile string
	json     bool
}

func (c *meshCommon) register(fs *flag.FlagSet) {
	fs.StringVar(&c.profile, "profile", "default", "凭据配置名")
	fs.BoolVar(&c.panel, "panel", false, "使用桌面面板的 0600 凭据文件")
	fs.StringVar(&c.credFile, "credential-file", "", "凭据 JSON 文件（0600），用于没有系统钥匙串的主机")
	fs.BoolVar(&c.json, "json", false, "输出 JSON")
}

func (c *meshCommon) resolveCredentialFile() error {
	if !c.panel || c.credFile != "" {
		return nil
	}
	root := os.Getenv("XMESH_UI_STATE_ROOT")
	if root == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		root = filepath.Join(base, "xmesh")
	}
	c.credFile = filepath.Join(root, "mesh", "mesh-credential.json")
	return nil
}

// provider builds the control-plane client. used is the access level that
// actually answered (a read call falls back to the write credential), and
// source says where the credential came from. Neither value carries the secret.
func (c *meshCommon) provider(access meshadmin.Access) (p meshadmin.Provider, used meshadmin.Access, source string, err error) {
	var cred meshadmin.Credential
	cred, used, source, err = c.credential(access)
	if err != nil {
		return nil, used, source, err
	}
	p, err = meshadmin.NewProvider(cred, used)
	return p, used, source, err
}

// credential resolves the saved admin credential without building a client.
func (c *meshCommon) credential(access meshadmin.Access) (cred meshadmin.Credential, used meshadmin.Access, source string, err error) {
	if err := c.resolveCredentialFile(); err != nil {
		return cred, access, "file", err
	}
	used, source = access, "keychain"
	if c.credFile != "" {
		cred, err = meshadmin.ReadCredentialFile(c.credFile)
		source = "file"
		if errors.Is(err, os.ErrNotExist) {
			err = meshadmin.ErrNoCredential
		}
		if err == nil && cred.Access != "" {
			if access == meshadmin.AccessWrite && cred.Access == meshadmin.AccessRead {
				return cred, access, source, fmt.Errorf("meshadmin: insufficient_scope: saved credential is read-only")
			}
			used = cred.Access
		}
	} else {
		cred, used, err = meshadmin.Resolve(meshCredentials, c.profile, access)
	}
	if err != nil {
		if errors.Is(err, meshadmin.ErrNoCredential) {
			return cred, used, source, fmt.Errorf("没有保存的凭据（profile %q）。先运行: xmesh mesh login --kind tailscale|headscale --url ... --access read|write", c.profile)
		}
		return cred, used, source, err
	}
	return cred, used, source, nil
}

func (c *meshCommon) snapshots(dir string) (meshadmin.SnapshotStore, error) {
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("无法确定快照目录，请用 --snapshot-dir: %w", err)
		}
		dir = filepath.Join(base, "xmesh", "mesh", c.profile, "snapshots")
	}
	return meshadmin.DirStore{Dir: dir}, nil
}

func meshRun(args []string, out io.Writer, in io.Reader) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, meshUsage)
		return nil
	}
	rest := args[1:]
	switch args[0] {
	case "login":
		return meshLogin(rest, out, in)
	case "logout":
		return meshLogout(rest, out)
	case "status":
		return meshStatus(rest, out)
	case "tags":
		return meshTags(rest, out)
	case "authkey":
		return meshAuthKey(rest, out)
	case "onboard":
		return meshOnboard(rest, out)
	case "share":
		return meshShare(rest, out)
	case "policy":
		return meshPolicy(rest, out, in)
	case "matrix":
		return meshMatrix(rest, out)
	case "derp":
		return meshDERP(rest, out)
	}
	return fmt.Errorf("未知子命令: mesh %s\n\n%s", args[0], meshUsage)
}

func emit(out io.Writer, asJSON bool, v any, human func(io.Writer)) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetEscapeHTML(false)
		return enc.Encode(v)
	}
	human(out)
	return nil
}

func parseFlags(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return fmt.Errorf("%s", meshUsage)
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("多余的参数: %s", strings.Join(fs.Args(), " "))
	}
	return nil
}

func ctx30() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 60*time.Second)
}

// readSecret reads a secret from a file ("-" means stdin) or, when stdin is a
// terminal, prompts without echo. The secret never comes from argv.
func readSecret(file string, in io.Reader, out io.Writer) (string, error) {
	var b []byte
	var err error
	switch {
	case file != "" && file != "-":
		st, serr := os.Stat(file)
		if serr != nil {
			return "", serr
		}
		if st.Size() > 8192 {
			return "", fmt.Errorf("密钥文件过大")
		}
		b, err = os.ReadFile(file)
	default:
		if f, ok := in.(*os.File); ok && file == "" && term.IsTerminal(int(f.Fd())) {
			fmt.Fprint(out, "密钥（输入不回显）: ")
			b, err = term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(out)
		} else {
			b, err = io.ReadAll(io.LimitReader(in, 8193))
			if len(b) > 8192 {
				return "", fmt.Errorf("密钥过大")
			}
		}
	}
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "", fmt.Errorf("密钥为空")
	}
	return s, nil
}

func meshLogin(args []string, out io.Writer, in io.Reader) error {
	var c meshCommon
	fs := flag.NewFlagSet("mesh login", flag.ContinueOnError)
	c.register(fs)
	kind := fs.String("kind", "", "tailscale 或 headscale")
	url := fs.String("url", "", "控制面地址（Headscale 必填；Tailscale 默认官方 API）")
	tailnet := fs.String("tailnet", "", "Tailscale tailnet 名，默认 -")
	clientID := fs.String("client-id", "", "Tailscale OAuth 客户端 ID（不填则密钥视为 API token）")
	access := fs.String("access", "read", "read 或 write")
	secretFile := fs.String("secret-file", "", "密钥文件，- 表示标准输入；缺省时从标准输入读取")
	noVerify := fs.Bool("no-verify", false, "保存前不连接控制面验证")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if err := c.resolveCredentialFile(); err != nil {
		return err
	}
	if *access != "read" && *access != "write" {
		return fmt.Errorf("--access 必须是 read 或 write")
	}
	cred := meshadmin.Credential{Kind: meshadmin.Kind(*kind), URL: *url, Tailnet: *tailnet, ClientID: *clientID}
	switch cred.Kind {
	case meshadmin.KindTailscale:
		cred.Type = meshadmin.CredToken
		if *clientID != "" {
			cred.Type = meshadmin.CredOAuth
		}
	case meshadmin.KindHeadscale:
		cred.Type = meshadmin.CredAPIKey
	default:
		return fmt.Errorf("--kind 必须是 tailscale 或 headscale")
	}
	secret, err := readSecret(*secretFile, in, out)
	if err != nil {
		return err
	}
	cred.Secret = secret
	if err := cred.Validate(); err != nil {
		return err
	}
	acc := meshadmin.Access(*access)
	cred.Access = acc
	if !*noVerify {
		p, err := meshadmin.NewProvider(cred, acc)
		if err != nil {
			return err
		}
		ctx, cancel := ctx30()
		defer cancel()
		if _, err := p.Devices(ctx); err != nil {
			return fmt.Errorf("凭据验证失败（用 --no-verify 跳过）: %w", err)
		}
	}
	storedAt := "系统钥匙串"
	if c.credFile != "" {
		if err := meshadmin.WriteCredentialFile(c.credFile, cred); err != nil {
			return err
		}
		storedAt = c.credFile
	} else if err := meshCredentials.Put(c.profile, acc, cred); err != nil {
		return err
	}
	return emit(out, c.json, map[string]any{"profile": c.profile, "access": acc, "kind": cred.Kind, "stored": true, "file": c.credFile}, func(w io.Writer) {
		fmt.Fprintf(w, "已保存 %s 凭据（profile %s，%s）到%s\n", cred.Kind, c.profile, acc, storedAt)
	})
}

func meshLogout(args []string, out io.Writer) error {
	var c meshCommon
	fs := flag.NewFlagSet("mesh logout", flag.ContinueOnError)
	c.register(fs)
	access := fs.String("access", "", "read 或 write；缺省删除两份")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	var which []meshadmin.Access
	switch *access {
	case "":
		which = []meshadmin.Access{meshadmin.AccessRead, meshadmin.AccessWrite}
	case "read", "write":
		which = []meshadmin.Access{meshadmin.Access(*access)}
	default:
		return fmt.Errorf("--access 必须是 read 或 write")
	}
	if err := c.resolveCredentialFile(); err != nil {
		return err
	}
	if c.credFile != "" {
		if err := os.Remove(c.credFile); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return emit(out, c.json, map[string]any{"profile": c.profile, "deleted": true, "file": c.credFile}, func(w io.Writer) {
			fmt.Fprintf(w, "已删除凭据文件 %s\n", c.credFile)
		})
	}
	for _, a := range which {
		if err := meshCredentials.Delete(c.profile, a); err != nil {
			return err
		}
	}
	return emit(out, c.json, map[string]any{"profile": c.profile, "deleted": true}, func(w io.Writer) {
		fmt.Fprintf(w, "已删除 profile %s 的凭据\n", c.profile)
	})
}

func loadNodes(paths []string) ([]meshadmin.NodeInfo, error) {
	var nodes []meshadmin.NodeInfo
	for _, p := range paths {
		n, err := meshadmin.LoadNodeInfo(p)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

func meshStatus(args []string, out io.Writer) error {
	var c meshCommon
	var nodeCfgs stringList
	fs := flag.NewFlagSet("mesh status", flag.ContinueOnError)
	c.register(fs)
	fs.Var(&nodeCfgs, "node-config", "某个节点的 xmesh.yaml，用于核对角色与 tag（可重复）")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	nodes, err := loadNodes(nodeCfgs)
	if err != nil {
		return err
	}
	p, used, source, err := c.provider(meshadmin.AccessRead)
	if err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	devs, err := p.Devices(ctx)
	if err != nil {
		return err
	}
	reports := meshadmin.Analyze(devs, nodes, time.Now())
	caps, _ := p.Capabilities(ctx)
	// The UI shows which credential answered: a read-only profile explains why
	// every change is refused, and a file-backed one explains where it lives.
	// The secret is never part of this: only where it lives and what it may do.
	cred := map[string]any{"access": string(used), "profile": c.profile, "source": source}
	if c.credFile != "" {
		cred["file"] = c.credFile
	}
	doc := map[string]any{"provider": p.Kind(), "capabilities": caps, "devices": reports, "credential": cred}
	return emit(out, c.json, doc, func(w io.Writer) {
		fmt.Fprintf(w, "控制面: %s，设备 %d 台，策略写入: %s\n", p.Kind(), len(reports), yesNo(caps.PolicyWrite))
		for _, r := range reports {
			state := "离线"
			if r.Device.Online {
				state = "在线"
			}
			fmt.Fprintf(w, "  %-20s %s  tag: %s  角色: %s\n", r.Device.Name, state, orNone(r.Tags), orNone(rolesText(r.Roles)))
			for _, i := range r.Issues {
				fmt.Fprintf(w, "    [%s] %s: %s\n", i.Severity, i.Code, i.Message)
			}
		}
	})
}

func rolesText(r []meshadmin.Role) []string {
	out := make([]string, len(r))
	for i, x := range r {
		out[i] = string(x)
	}
	return out
}

func orNone(l []string) string {
	if len(l) == 0 {
		return "无"
	}
	return strings.Join(l, ",")
}

func yesNo(b bool) string {
	if b {
		return "是"
	}
	return "否（文件模式，须在服务器上改策略文件）"
}

func splitTags(s string) []string {
	var out []string
	for _, t := range strings.Split(s, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func meshTags(args []string, out io.Writer) error {
	var c meshCommon
	fs := flag.NewFlagSet("mesh tags", flag.ContinueOnError)
	c.register(fs)
	device := fs.String("device", "", "设备 ID")
	set := fs.String("set", "", "新的完整 tag 列表，逗号分隔；空串表示清空")
	apply := fs.Bool("apply", false, "确认写入")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	setGiven := false
	fs.Visit(func(f *flag.Flag) { setGiven = setGiven || f.Name == "set" })
	if *device == "" {
		return fmt.Errorf("需要 --device")
	}
	access := meshadmin.AccessRead
	if setGiven {
		access = meshadmin.AccessWrite
	}
	p, _, _, err := c.provider(access)
	if err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	devs, err := p.Devices(ctx)
	if err != nil {
		return err
	}
	var dev *meshadmin.Device
	for i := range devs {
		if devs[i].ID == *device || devs[i].Name == *device || devs[i].Hostname == *device {
			dev = &devs[i]
		}
	}
	if dev == nil {
		return fmt.Errorf("找不到设备 %q", *device)
	}
	if !setGiven {
		return emit(out, c.json, dev, func(w io.Writer) { fmt.Fprintf(w, "%s tag: %s\n", dev.Name, orNone(dev.Tags)) })
	}
	want, err := meshadmin.CanonicalTags(splitTags(*set))
	if err != nil {
		return err
	}
	// Tags must be owned in the policy, otherwise the control plane drops or refuses them.
	doc, err := p.GetPolicy(ctx)
	if err != nil {
		return err
	}
	var missing []string
	if std, err := meshadmin.StandardJSON(doc.Text); err == nil {
		var pol struct {
			TagOwners map[string]json.RawMessage `json:"tagOwners"`
		}
		_ = json.Unmarshal(std, &pol)
		for _, t := range want {
			if _, ok := pol.TagOwners[t]; !ok {
				missing = append(missing, t)
			}
		}
	}
	result := map[string]any{"device": dev.ID, "from": dev.Tags, "to": want, "missing_tag_owners": missing, "applied": false}
	if len(missing) > 0 {
		result["error"] = "policy tagOwners lacks: " + strings.Join(missing, ",")
		if *apply {
			return fmt.Errorf("策略的 tagOwners 里没有 %s；先用 `xmesh mesh policy --template ...` 或手工补上", strings.Join(missing, ","))
		}
	}
	if *apply {
		if err := confirm(out, fmt.Sprintf("把 %s 的 tag 改为 %s", dev.Name, orNone(want)), false); err != nil {
			return err
		}
		updated, err := p.SetTags(ctx, dev.ID, want)
		if err != nil {
			return err
		}
		result["applied"] = true
		result["now"] = updated.Tags
	}
	return emit(out, c.json, result, func(w io.Writer) {
		fmt.Fprintf(w, "%s: %s -> %s\n", dev.Name, orNone(dev.Tags), orNone(want))
		if len(missing) > 0 {
			fmt.Fprintf(w, "警告: tagOwners 里没有 %s\n", strings.Join(missing, ","))
		}
		if !*apply {
			fmt.Fprintln(w, "仅预览。加 --apply 写入。")
		} else {
			fmt.Fprintln(w, "已写入。")
		}
	})
}

// confirm asks for "yes" on an interactive terminal. Non-interactive callers (the
// UI, scripts) have already shown the preview and pass --apply, which is their
// confirmation; --yes skips the prompt on a terminal.
var confirmIn io.Reader = os.Stdin
var confirmYes bool

func confirm(out io.Writer, what string, yes bool) error {
	if yes || confirmYes {
		return nil
	}
	f, ok := confirmIn.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil
	}
	fmt.Fprintf(out, "%s。输入 yes 确认: ", what)
	var answer string
	_, _ = fmt.Fscanln(confirmIn, &answer)
	if answer != "yes" {
		return meshadmin.ErrNotConfirmed
	}
	return nil
}

func meshAuthKey(args []string, out io.Writer) error {
	var c meshCommon
	fs := flag.NewFlagSet("mesh authkey", flag.ContinueOnError)
	c.register(fs)
	tags := fs.String("tags", "", "可选的网络标签，逗号分隔")
	ttl := fs.Duration("ttl", time.Hour, "有效期")
	reusable := fs.Bool("reusable", false, "可重复使用")
	ephemeral := fs.Bool("ephemeral", false, "临时节点")
	user := fs.String("user", "", "Headscale 用户名")
	file := fs.String("out", "", "写入的文件（0600）；- 表示标准输出")
	overwrite := fs.Bool("overwrite", false, "覆盖已有文件")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *file == "" {
		return fmt.Errorf("需要 --out 文件（key 只显示一次，不会写进日志）；用 --out - 输出到标准输出")
	}
	return createAuthKey(&c, out, *tags, *ttl, *reusable, *ephemeral, *user, *file, *overwrite)
}

func createAuthKey(c *meshCommon, out io.Writer, tags string, ttl time.Duration, reusable, ephemeral bool, user, file string, overwrite bool) error {
	if file != "-" && !overwrite {
		if _, err := os.Lstat(file); err == nil {
			return fmt.Errorf("auth key 文件已存在，请复用该文件或明确选择覆盖")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	want, err := meshadmin.CanonicalTags(splitTags(tags))
	if err != nil {
		return err
	}
	p, _, _, err := c.provider(meshadmin.AccessWrite)
	if err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	k, err := p.CreateAuthKey(ctx, meshadmin.AuthKeyRequest{Tags: want, TTL: ttl, Reusable: reusable, Ephemeral: ephemeral, User: user, Description: "xmesh onboarding"})
	if err != nil {
		return err
	}
	if file == "-" {
		fmt.Fprintln(out, k.Secret)
		return nil
	}
	if err := meshadmin.WriteSecretFile(file, []byte(k.Secret+"\n"), overwrite); err != nil {
		return err
	}
	return emit(out, c.json, map[string]any{"key": k.Redacted(), "file": file}, func(w io.Writer) {
		fmt.Fprintf(w, "已创建 auth key（%s，到期 %s），写入 %s\n", orNone(k.Tags), k.Expires.Format(time.RFC3339), file)
	})
}

func meshOnboard(args []string, out io.Writer) error {
	var c meshCommon
	fs := flag.NewFlagSet("mesh onboard", flag.ContinueOnError)
	c.register(fs)
	tags := fs.String("tags", "", "可选的网络标签，逗号分隔")
	ttl := fs.Duration("ttl", time.Hour, "key 有效期")
	ephemeral := fs.Bool("ephemeral", false, "临时节点")
	user := fs.String("user", "", "Headscale 用户名")
	keyFile := fs.String("authkey-file", "", "写入 auth key 的文件，对应 mesh.tailnet.authkey_file")
	cfg := fs.String("config", "", "xmesh.yaml 路径，仅用于打印后续命令和 --install")
	state := fs.String("state-dir", "", "传给 install 的状态目录")
	doInstall := fs.Bool("install", false, "写好 key 后调用 xmesh install")
	overwrite := fs.Bool("overwrite", false, "覆盖已有 key 文件")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *keyFile == "" {
		return fmt.Errorf("需要 --authkey-file")
	}
	if *doInstall && *cfg == "" {
		return fmt.Errorf("--install 需要 --config")
	}
	if *doInstall {
		candidate, err := config.Load(*cfg)
		if err != nil {
			return err
		}
		if candidate.SchemaVersion != 2 || !candidate.Mesh.TailnetEnabled() {
			return fmt.Errorf("请先配置 schema_version: 2 与 mesh.tailnet.enabled: true，再执行 --install")
		}
	}
	if err := createAuthKey(&c, out, *tags, *ttl, false, *ephemeral, *user, *keyFile, *overwrite); err != nil {
		return err
	}
	if !c.json {
		fmt.Fprintf(out, "在 xmesh.yaml 里设置 schema_version: 2、mesh.tailnet.enabled: true、mesh.tailnet.control_url 与 mesh.tailnet.authkey_file: %s\n", *keyFile)
	}
	args2 := []string{"--config", *cfg}
	if *state != "" {
		args2 = append(args2, "--state-dir", *state)
	}
	if *doInstall {
		return install(args2)
	}
	if *cfg != "" && !c.json {
		fmt.Fprintf(out, "然后运行: xmesh install %s\n", strings.Join(args2, " "))
	}
	return nil
}

func meshPolicy(args []string, out io.Writer, in io.Reader) error {
	if len(args) > 0 && (args[0] == "document" || args[0] == "publish") {
		return meshPolicyDocument(args[0], args[1:], out, in)
	}
	var c meshCommon
	fs := flag.NewFlagSet("mesh policy", flag.ContinueOnError)
	c.register(fs)
	show := fs.Bool("show", false, "显示当前策略")
	list := fs.Bool("snapshots", false, "列出本机快照")
	rollback := fs.String("rollback", "", "恢复指定快照")
	template := fs.String("template", "", "personal、team 或 restricted")
	owner := fs.String("owner", "", "tagOwners 的属主，例如 autogroup:admin 或 alice@")
	port := fs.Int("port", 0, "Agent 端口，默认 7433")
	apply := fs.Bool("apply", false, "确认写入")
	yes := fs.Bool("yes", false, "终端上也不再询问")
	expect := fs.String("expect-version", "", "只在当前策略版本等于该值时写入（预览输出的 base_version）")
	snapDir := fs.String("snapshot-dir", "", "快照目录")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	store, err := c.snapshots(*snapDir)
	if err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	switch {
	case *list:
		snaps, err := store.List()
		if err != nil {
			return err
		}
		return emit(out, c.json, map[string]any{"snapshots": snaps}, func(w io.Writer) {
			if len(snaps) == 0 {
				fmt.Fprintln(w, "没有快照")
			}
			for _, s := range snaps {
				fmt.Fprintf(w, "%s  %s  %s\n", s.ID, s.Provider, s.Label)
			}
		})
	case *show:
		p, _, _, err := c.provider(meshadmin.AccessRead)
		if err != nil {
			return err
		}
		doc, err := p.GetPolicy(ctx)
		if err != nil {
			return err
		}
		secs, _ := meshadmin.ManagedSections(doc.Text)
		return emit(out, c.json, map[string]any{"policy": doc, "managed_sections": secs}, func(w io.Writer) { fmt.Fprint(w, doc.Text) })
	}
	p, _, _, err := c.provider(meshadmin.AccessWrite)
	if err != nil {
		return err
	}
	var plan *meshadmin.PolicyPlan
	var snapID string
	switch {
	case *rollback != "":
		snap, err := store.Load(*rollback)
		if err != nil {
			return err
		}
		if snap.Provider != p.Kind() {
			return fmt.Errorf("快照属于 %s，当前控制面是 %s", snap.Provider, p.Kind())
		}
		snapID = snap.ID
		plan, err = meshadmin.PlanPolicy(ctx, p, "rollback to "+snap.ID, func(string) (string, []string, error) { return snap.Text, nil, nil })
		if err != nil {
			return err
		}
	case *template != "":
		if *owner == "" {
			return fmt.Errorf("需要 --owner（Tailscale 常用 autogroup:admin，Headscale 用 用户名@）")
		}
		frag, err := meshadmin.BuildTemplate(meshadmin.TemplateName(*template), meshadmin.TemplateOptions{Owner: *owner, Port: *port})
		if err != nil {
			return err
		}
		plan, err = meshadmin.PlanPolicy(ctx, p, "template "+*template, func(cur string) (string, []string, error) {
			return meshadmin.ApplyFragment(cur, frag, meshadmin.SectionTagOwners, meshadmin.SectionACLs)
		})
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("需要 --show、--snapshots、--rollback 或 --template 之一")
	}
	return finishPlan(out, &c, p, store, plan, *apply, *yes, *expect, snapID)
}

// finishPlan prints a plan and, with --apply, writes it.
func finishPlan(out io.Writer, c *meshCommon, p meshadmin.Provider, store meshadmin.SnapshotStore, plan *meshadmin.PolicyPlan, apply, yes bool, expect, _ string) error {
	result := map[string]any{"plan": plan, "applied": false}
	if apply {
		if expect != "" && expect != plan.BaseVersion {
			return fmt.Errorf("%w: 预览时的策略版本与现在不一致，请重新预览", meshadmin.ErrConflict)
		}
		if plan.Changed {
			if err := confirm(out, "写入策略（"+plan.Label+"）", yes); err != nil {
				return err
			}
		}
		ctx, cancel := ctx30()
		defer cancel()
		res, err := plan.Apply(ctx, p, store, true)
		if err != nil {
			return err
		}
		result["applied"] = true
		result["result"] = res
	}
	return emit(out, c.json, result, func(w io.Writer) {
		if !plan.Changed {
			fmt.Fprintln(w, "策略无变化")
			return
		}
		fmt.Fprint(w, plan.Diff)
		for _, x := range plan.Warnings {
			fmt.Fprintf(w, "警告: %s\n", x)
		}
		switch {
		case !plan.Writable:
			fmt.Fprintln(w, "该控制面不允许远程写策略（Headscale policy.mode=file）。把下面的完整策略放到服务器策略文件并重载:")
			fmt.Fprint(w, plan.After)
		case apply:
			fmt.Fprintln(w, "已写入。")
			if r, ok := result["result"].(meshadmin.ApplyResult); ok && r.SnapshotID != "" {
				fmt.Fprintf(w, "快照 %s（回滚: xmesh mesh policy --rollback %s --apply）\n", r.SnapshotID, r.SnapshotID)
			}
		default:
			fmt.Fprintf(w, "仅预览（版本 %s）。加 --apply 写入。\n", plan.BaseVersion)
		}
	})
}

func meshMatrix(args []string, out io.Writer) error {
	var c meshCommon
	var nodeCfgs stringList
	fs := flag.NewFlagSet("mesh matrix", flag.ContinueOnError)
	c.register(fs)
	fs.Var(&nodeCfgs, "node-config", "目标节点的 xmesh.yaml（提供工具层 agent.authz，可重复）")
	port := fs.Int("port", 0, "Agent 端口，默认 7433")
	onlyBad := fs.Bool("mismatch-only", false, "只显示两层不一致的格子")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	nodes, err := loadNodes(nodeCfgs)
	if err != nil {
		return err
	}
	p, _, _, err := c.provider(meshadmin.AccessRead)
	if err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	devs, err := p.Devices(ctx)
	if err != nil {
		return err
	}
	doc, err := p.GetPolicy(ctx)
	if err != nil {
		return err
	}
	m, err := meshadmin.BuildMatrix(doc.Text, devs, nodes, *port)
	if err != nil {
		return err
	}
	if *onlyBad {
		var keep []meshadmin.Cell
		for _, cell := range m.Cells {
			if cell.Mismatch != "" {
				keep = append(keep, cell)
			}
		}
		m.Cells = keep
	}
	return emit(out, c.json, m, func(w io.Writer) {
		fmt.Fprintf(w, "%-16s %-16s %-8s %-8s 工具\n", "来源", "目标", "网络", "工具层")
		for _, cell := range m.Cells {
			fmt.Fprintf(w, "%-16s %-16s %-8s %-8s %s\n", cell.Src, cell.Dst, cell.Network, cell.Tools, strings.Join(cell.AllowedTools, ","))
			if cell.Mismatch != "" {
				fmt.Fprintf(w, "  ! %s\n", cell.Mismatch)
			}
		}
	})
}

// ---- derp ----

func parseRegion(spec string) (meshadmin.DERPRegionSpec, error) {
	var r meshadmin.DERPRegionSpec
	for _, kv := range strings.Split(spec, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return r, fmt.Errorf("--region 项 %q 不是 key=value", kv)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "id", "derp-port", "stun-port":
			n, err := strconv.Atoi(v)
			if err != nil {
				return r, fmt.Errorf("--region %s=%q 不是数字", k, v)
			}
			switch k {
			case "id":
				r.ID = n
			case "derp-port":
				r.DERPPort = n
			default:
				r.STUNPort = n
			}
		case "code":
			r.Code = v
		case "name":
			r.Name = v
		case "host":
			r.Hostname = v
		case "ipv4":
			r.IPv4 = v
		case "ipv6":
			r.IPv6 = v
		case "cert-name":
			r.CertName = v
		default:
			return r, fmt.Errorf("--region 不认识的键 %q", k)
		}
	}
	return r, nil
}

func parseRegions(l stringList) ([]meshadmin.DERPRegionSpec, error) {
	if len(l) == 0 {
		return nil, fmt.Errorf("需要至少一个 --region")
	}
	var out []meshadmin.DERPRegionSpec
	for _, s := range l {
		r, err := parseRegion(s)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func meshDERP(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("需要子命令: plan|apply|probe|headscale|admit-serve|sync\n\n%s", meshUsage)
	}
	switch args[0] {
	case "plan", "apply":
		return derpPlanApply(args[0] == "apply", args[1:], out)
	case "probe":
		return derpProbe(args[1:], out)
	case "headscale":
		return derpHeadscale(args[1:], out)
	case "admit-serve":
		return derpAdmitServe(args[1:], out)
	case "sync":
		return derpSync(args[1:], out)
	}
	return fmt.Errorf("未知子命令: mesh derp %s", args[0])
}

func derpPlanApply(apply bool, args []string, out io.Writer) error {
	var c meshCommon
	var regions stringList
	fs := flag.NewFlagSet("mesh derp", flag.ContinueOnError)
	c.register(fs)
	fs.Var(&regions, "region", "DERP 地域，可重复")
	omit := fs.Bool("omit-default", false, "去掉官方 DERP 地域")
	noProbe := fs.Bool("no-probe", false, "写入后不探测")
	skipSTUN := fs.Bool("skip-stun", false, "探测时跳过 UDP STUN")
	yes := fs.Bool("yes", false, "终端上也不再询问")
	expect := fs.String("expect-version", "", "预览输出的 base_version")
	snapDir := fs.String("snapshot-dir", "", "快照目录")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	specs, err := parseRegions(regions)
	if err != nil {
		return err
	}
	p, _, _, err := c.provider(meshadmin.AccessWrite)
	if err != nil {
		return err
	}
	caps, err := func() (meshadmin.Capabilities, error) {
		ctx, cancel := ctx30()
		defer cancel()
		return p.Capabilities(ctx)
	}()
	if err != nil {
		return err
	}
	if !caps.DERPViaPolicy {
		return fmt.Errorf("%s 的 DERP 是服务器配置，不能经 API 写入。运行 `xmesh mesh derp headscale ...` 生成 derp.paths 文件和配置片段", p.Kind())
	}
	store, err := c.snapshots(*snapDir)
	if err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	cur, err := p.GetPolicy(ctx)
	if err != nil {
		return err
	}
	taken, err := meshadmin.ExistingRegionIDs(cur.Text)
	if err != nil {
		return err
	}
	// Regions already managed by xmesh are replaced, so their ids are not "taken".
	managed, _ := managedRegionIDs(cur.Text)
	for id := range managed {
		delete(taken, id)
	}
	specs, err = meshadmin.AssignRegionIDs(specs, taken)
	if err != nil {
		return err
	}
	frag, warns, err := meshadmin.DERPFragment(specs, meshadmin.DERPOptions{OmitDefaultRegions: *omit})
	if err != nil {
		return err
	}
	plan, err := meshadmin.PlanPolicy(ctx, p, "derp regions", func(text string) (string, []string, error) {
		s, w, err := meshadmin.ApplyFragment(text, frag, meshadmin.SectionDERP)
		return s, append(warns, w...), err
	})
	if err != nil {
		return err
	}
	if !apply {
		return finishPlan(out, &c, p, store, plan, false, *yes, *expect, "")
	}
	if *expect != "" && *expect != plan.BaseVersion {
		return fmt.Errorf("%w: 预览时的策略版本与现在不一致，请重新预览", meshadmin.ErrConflict)
	}
	if plan.Changed {
		if err := confirm(out, "写入 DERP 地域", *yes); err != nil {
			return err
		}
	}
	var probe func(context.Context, meshadmin.DERPRegionSpec) meshadmin.ProbeResult
	if !*noProbe {
		probe = func(ctx context.Context, s meshadmin.DERPRegionSpec) meshadmin.ProbeResult {
			return meshadmin.ProbeDERP(ctx, s, meshadmin.ProbeOptions{SkipSTUN: *skipSTUN})
		}
	}
	res, aerr := meshadmin.ApplyDERP(ctx, p, store, plan, specs, probe)
	if aerr != nil && len(res.Probes) == 0 {
		return aerr
	}
	if e := emit(out, c.json, map[string]any{"plan": plan, "result": res, "regions": specs}, func(w io.Writer) {
		fmt.Fprint(w, plan.Diff)
		printProbes(w, res.Probes)
		if res.RolledBack {
			fmt.Fprintln(w, "探测失败，已恢复写入前的策略。")
		}
	}); e != nil {
		return e
	}
	return aerr
}

func managedRegionIDs(text string) (map[int]bool, error) {
	// A managed region is one with the marker comment; ExistingRegionIDs cannot
	// see comments, so compare the sets before and after removing the managed ones.
	all, err := meshadmin.ExistingRegionIDs(text)
	if err != nil {
		return nil, err
	}
	stripped, _, err := meshadmin.ApplyFragment(text, meshadmin.Fragment{}, meshadmin.SectionDERP)
	if err != nil {
		return nil, err
	}
	rest, err := meshadmin.ExistingRegionIDs(stripped)
	if err != nil {
		return nil, err
	}
	out := map[int]bool{}
	for id := range all {
		if !rest[id] {
			out[id] = true
		}
	}
	return out, nil
}

func printProbes(w io.Writer, probes []meshadmin.ProbeResult) {
	for _, r := range probes {
		status := "通过"
		if !r.OK {
			status = "失败"
		}
		fmt.Fprintf(w, "探测 %s: %s\n", r.Host, status)
		for _, ch := range r.Checks {
			mark := "ok  "
			if !ch.OK {
				mark = "FAIL"
			}
			lat := ""
			if ch.Latency > 0 {
				lat = fmt.Sprintf(" (%s)", ch.Latency.Round(time.Millisecond))
			}
			fmt.Fprintf(w, "  %s %-5s %s%s\n", mark, ch.Name, ch.Detail, lat)
		}
	}
}

func derpProbe(args []string, out io.Writer) error {
	var c meshCommon
	var regions stringList
	fs := flag.NewFlagSet("mesh derp probe", flag.ContinueOnError)
	c.register(fs)
	fs.Var(&regions, "region", "DERP 地域，可重复")
	skipSTUN := fs.Bool("skip-stun", false, "跳过 UDP STUN")
	compare := fs.Bool("compare-official", false, "同时测量官方 DERP 地域的 TCP 延迟作对照")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	specs, err := parseRegions(regions)
	if err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	var results []meshadmin.ProbeResult
	for _, s := range specs {
		results = append(results, meshadmin.ProbeDERP(ctx, s, meshadmin.ProbeOptions{SkipSTUN: *skipSTUN}))
	}
	doc := map[string]any{"probes": results}
	var official []meshadmin.RegionLatency
	if *compare {
		official, err = meshadmin.OfficialLatencies(ctx, meshadmin.DefaultOfficialDERPMapURL, nil)
		if err != nil {
			doc["compare_error"] = err.Error()
		}
		doc["official"] = official
	}
	if len(specs) == 1 {
		doc["warnings"] = []string{"只有一个自建 region：它是单点，节点无法中继时没有备选"}
	}
	allOK := true
	for _, r := range results {
		allOK = allOK && r.OK
	}
	if e := emit(out, c.json, doc, func(w io.Writer) {
		printProbes(w, results)
		if len(specs) == 1 {
			fmt.Fprintln(w, "提示: 只有一个自建 region，它是单点。")
		}
		for i, o := range official {
			if i >= 5 {
				break
			}
			if o.Err == "" {
				fmt.Fprintf(w, "官方 %-6s %s\n", o.Code, o.Latency.Round(time.Millisecond))
			}
		}
	}); e != nil {
		return e
	}
	if !allOK {
		return fmt.Errorf("DERP 探测未通过")
	}
	return nil
}

func derpHeadscale(args []string, out io.Writer) error {
	var c meshCommon
	var regions stringList
	fs := flag.NewFlagSet("mesh derp headscale", flag.ContinueOnError)
	c.register(fs)
	fs.Var(&regions, "region", "DERP 地域，可重复")
	omit := fs.Bool("omit-default", false, "去掉官方 DERP map（derp.urls: []）")
	path := fs.String("path", "/etc/headscale/xmesh-derp.yaml", "该文件在 Headscale 服务器上的路径")
	file := fs.String("out", "", "把 derp.paths 文件写到本机这个位置")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	specs, err := parseRegions(regions)
	if err != nil {
		return err
	}
	specs, err = meshadmin.AssignRegionIDs(specs, nil)
	if err != nil {
		return err
	}
	res, warns, err := meshadmin.HeadscaleDERPConfig(specs, meshadmin.DERPOptions{OmitDefaultRegions: *omit}, *path)
	if err != nil {
		return err
	}
	if *file != "" {
		if err := os.WriteFile(*file, res.File, 0o644); err != nil {
			return err
		}
	}
	return emit(out, c.json, map[string]any{"file": string(res.File), "snippet": res.Snippet, "warnings": warns, "written": *file}, func(w io.Writer) {
		fmt.Fprintf(w, "# %s\n%s\n# config.yaml\n%s", *path, res.File, res.Snippet)
		for _, x := range warns {
			fmt.Fprintf(w, "警告: %s\n", x)
		}
		fmt.Fprintln(w, "把文件放到服务器上并重启 Headscale（derp.paths 在启动时读取）。")
	})
}

func derpAdmitServe(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("mesh derp admit-serve", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:8480", "监听地址；放在 derper 能访问的回环或内网地址")
	list := fs.String("allowlist", "", "放行的节点公钥文件")
	tokenFile := fs.String("admin-token-file", "", "管理接口 token 文件；缺省则关闭管理接口")
	certFile := fs.String("cert-file", "", "TLS 证书；与 --key-file 一起启用 HTTPS")
	keyFile := fs.String("key-file", "", "TLS 私钥")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *list == "" {
		return fmt.Errorf("需要 --allowlist")
	}
	al, err := meshadmin.OpenAllowlist(*list)
	if err != nil {
		return err
	}
	srv := &meshadmin.AdmissionServer{List: al, Logf: func(f string, a ...any) { fmt.Fprintf(os.Stderr, f+"\n", a...) }}
	if *tokenFile != "" {
		tok, err := readSecret(*tokenFile, nil, out)
		if err != nil {
			return err
		}
		srv.AdminToken = tok
	}
	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return fmt.Errorf("--listen: %w", err)
	}
	if ip := net.ParseIP(host); (ip == nil || !ip.IsLoopback()) && *certFile == "" {
		fmt.Fprintln(os.Stderr, "警告: 非回环地址上没有 TLS。/admit 不做认证，请只放在 derper 所在的内网，并且不要启用管理接口。")
	}
	hs := &http.Server{Addr: *listen, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second}
	if *certFile != "" {
		hs.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		if *certFile != "" {
			errc <- hs.ListenAndServeTLS(*certFile, *keyFile)
		} else {
			errc <- hs.ListenAndServe()
		}
	}()
	fmt.Fprintf(out, "准入服务监听 %s；derper 参数: --verify-client-url=http://%s/admit --verify-client-url-fail-open=false\n", *listen, *listen)
	select {
	case <-ctx.Done():
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return hs.Shutdown(sctx)
	case err := <-errc:
		return err
	}
}

func derpSync(args []string, out io.Writer) error {
	var c meshCommon
	fs := flag.NewFlagSet("mesh derp sync", flag.ContinueOnError)
	c.register(fs)
	list := fs.String("allowlist", "", "本机放行文件")
	url := fs.String("admission-url", "", "远端准入服务地址（其管理接口）")
	tokenFile := fs.String("admission-token-file", "", "远端管理 token 文件")
	all := fs.Bool("all-devices", false, "放行所有已授权设备，而不只是带 xmesh tag 的")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if (*list == "") == (*url == "") {
		return fmt.Errorf("--allowlist 与 --admission-url 二选一")
	}
	p, _, _, err := c.provider(meshadmin.AccessRead)
	if err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	opt := meshadmin.SyncOptions{AllDevices: *all}
	var res meshadmin.SyncResult
	if *list != "" {
		al, err := meshadmin.OpenAllowlist(*list)
		if err != nil {
			return err
		}
		res, err = meshadmin.SyncAllowlist(ctx, p, al, opt)
		if err != nil {
			return err
		}
	} else {
		if *tokenFile == "" {
			return fmt.Errorf("--admission-url 需要 --admission-token-file")
		}
		tok, err := readSecret(*tokenFile, nil, out)
		if err != nil {
			return err
		}
		res, err = meshadmin.SyncRemote(ctx, p, *url, tok, meshHTTP, opt)
		if err != nil {
			return err
		}
	}
	return emit(out, c.json, res, func(w io.Writer) {
		fmt.Fprintf(w, "放行 %d 台设备（新增 %d，移除 %d）。已建立的中继连接要重连后才受影响。\n", res.Total, res.Added, res.Removed)
	})
}

// keyGuidance tells the user where to make a pre-authorised key when this machine cannot.
func keyGuidance(controlURL string) string {
	if config.OfficialControlURL(controlURL) {
		return "在 https://login.tailscale.com/admin/settings/keys 生成一个 auth key（建议单次使用、有效期 48 小时），保存到文件后用 --authkey-file 重新运行；" +
			"或先运行 xmesh mesh login 保存管理凭据，这里就能自动生成。"
	}
	return "在你的 Headscale 服务器上运行  headscale preauthkeys create --user <用户> --expiration 48h  生成 key，保存到文件后用 --authkey-file 重新运行；" +
		"或先运行 xmesh mesh login 保存管理凭据，这里就能自动生成。"
}

// sameControlPlane checks that an admin credential manages the control plane at controlURL, so a key
// made through it is one the receiving device can use there.
func sameControlPlane(cred meshadmin.Credential, controlURL string) bool {
	switch cred.Kind {
	case meshadmin.KindTailscale:
		return config.OfficialControlURL(controlURL)
	case meshadmin.KindHeadscale:
		return !config.OfficialControlURL(controlURL) && strings.TrimSuffix(strings.ToLower(cred.URL), "/") == strings.TrimSuffix(strings.ToLower(controlURL), "/")
	}
	return false
}
