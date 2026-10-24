package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tzf1003/xmesh/internal/cfadmin"
	"github.com/tzf1003/xmesh/internal/config"
	"go.yaml.in/yaml/v3"
)

const cloudflareUsage = `用法: xmesh cloudflare <子命令> [参数]

用 Cloudflare API 为设备创建隧道、DNS 名称和 Access 应用。API 令牌保存在系统钥匙串（或 --credential-file），
不进 xmesh.yaml，内核不持有。通用参数: --profile 名称（默认 default）  --credential-file 文件  --json

子命令:
  login    保存 API 令牌  --account 账号ID  --zone 区域ID  --domain 域名  [--secret-file 文件，缺省读标准输入]
           令牌需要权限：Account 的 Cloudflare Tunnel、Access: Apps and Policies、Access: Service Tokens（Edit），
           Zone 的 DNS（Edit）。保存前会连接 Cloudflare 验证。
  logout   删除已保存的令牌
  status   显示本机创建过的设备资源
  setup    为一台设备创建隧道并写好配置  --name 设备名  --config xmesh.yaml
           [--dir 目录，放隧道令牌与 Access 密钥，缺省在配置文件旁的 cloudflare/]  [--apply]
  remove   删除一台设备的隧道、DNS 与 Access 应用  --name 设备名  [--apply]

写入类命令默认只预览；加 --apply 才会调用 Cloudflare 并写文件。`

var (
	// cloudflareStore and cloudflareAPI are replaced by tests.
	cloudflareStore cfadmin.Store = cfadmin.KeychainStore{}
	cloudflareAPI                 = ""
	cloudflareHTTP  *http.Client
)

func cloudflareCmd(args []string) error { return cloudflareRun(args, os.Stdout, os.Stdin) }

type cloudflareCommon struct {
	profile   string
	credFile  string
	stateFile string
	json      bool
}

func (c *cloudflareCommon) register(fs *flag.FlagSet) {
	fs.StringVar(&c.profile, "profile", "default", "配置名")
	fs.StringVar(&c.credFile, "credential-file", "", "凭据 JSON 文件（0600），用于没有系统钥匙串的主机")
	fs.StringVar(&c.stateFile, "state-file", "", "记录已创建资源的文件，缺省在用户配置目录")
	fs.BoolVar(&c.json, "json", false, "输出 JSON")
}

func (c *cloudflareCommon) store() cfadmin.Store {
	if c.credFile != "" {
		return cfadmin.FileStore{Path: c.credFile}
	}
	return cloudflareStore
}

func (c *cloudflareCommon) state() (*cfadmin.StateFile, error) {
	if err := cfadmin.CheckProfile(c.profile); err != nil {
		return nil, err
	}
	path := c.stateFile
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("无法确定配置目录，请用 --state-file：%w", err)
		}
		path = filepath.Join(dir, "xmesh", "cloudflare", c.profile+".json")
	}
	return &cfadmin.StateFile{Path: path}, nil
}

func (c *cloudflareCommon) mesh() (*cfadmin.Mesh, cfadmin.Credential, error) {
	cred, err := c.store().Get(c.profile)
	if errors.Is(err, cfadmin.ErrNoCredential) {
		return nil, cred, fmt.Errorf("还没有保存 Cloudflare 令牌，先运行 xmesh cloudflare login")
	}
	if err != nil {
		return nil, cred, err
	}
	client := cred.Client()
	client.BaseURL, client.HTTP = cloudflareAPI, cloudflareHTTP
	return &cfadmin.Mesh{Client: client, Domain: cred.Domain}, cred, nil
}

func cloudflareFlags(fs *flag.FlagSet, args []string) error {
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return errors.New(cloudflareUsage)
		}
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("多余的参数: %s", strings.Join(fs.Args(), " "))
	}
	return nil
}

func cloudflareRun(args []string, out io.Writer, in io.Reader) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(out, cloudflareUsage)
		return nil
	}
	rest := args[1:]
	switch args[0] {
	case "login":
		return cloudflareLogin(rest, out, in)
	case "logout":
		var c cloudflareCommon
		fs := flag.NewFlagSet("cloudflare logout", flag.ContinueOnError)
		c.register(fs)
		if err := cloudflareFlags(fs, rest); err != nil {
			return err
		}
		return c.store().Delete(c.profile)
	case "status":
		return cloudflareStatus(rest, out)
	case "setup":
		return cloudflareSetup(rest, out)
	case "remove":
		return cloudflareRemove(rest, out)
	}
	return fmt.Errorf("未知子命令: cloudflare %s\n\n%s", args[0], cloudflareUsage)
}

func cloudflareLogin(args []string, out io.Writer, in io.Reader) error {
	var c cloudflareCommon
	fs := flag.NewFlagSet("cloudflare login", flag.ContinueOnError)
	c.register(fs)
	account := fs.String("account", "", "Cloudflare 账号 ID")
	zone := fs.String("zone", "", "域名所在区域的 ID")
	domain := fs.String("domain", "", "设备主机名所用的域名，例如 mesh.example.com")
	secretFile := fs.String("secret-file", "", "API 令牌文件，- 表示标准输入；缺省时从标准输入读取")
	noVerify := fs.Bool("no-verify", false, "保存前不连接 Cloudflare 验证")
	if err := cloudflareFlags(fs, args); err != nil {
		return err
	}
	token, err := readSecret(*secretFile, in, out)
	if err != nil {
		return err
	}
	cred := cfadmin.Credential{Token: token, AccountID: *account, ZoneID: *zone, Domain: *domain}
	if err := cred.Validate(); err != nil {
		return err
	}
	if !*noVerify {
		client := cred.Client()
		client.BaseURL, client.HTTP = cloudflareAPI, cloudflareHTTP
		ctx, cancel := ctx30()
		defer cancel()
		if err := client.Verify(ctx); err != nil {
			return fmt.Errorf("令牌验证失败：%w", err)
		}
	}
	if err := c.store().Put(c.profile, cred); err != nil {
		return err
	}
	return emit(out, c.json, map[string]any{"saved": true, "account": cred.AccountID, "domain": cred.Domain}, func(w io.Writer) {
		fmt.Fprintf(w, "已保存 Cloudflare 令牌（账号 %s，域名 %s）\n", cred.AccountID, cred.Domain)
	})
}

func cloudflareStatus(args []string, out io.Writer) error {
	var c cloudflareCommon
	fs := flag.NewFlagSet("cloudflare status", flag.ContinueOnError)
	c.register(fs)
	if err := cloudflareFlags(fs, args); err != nil {
		return err
	}
	state, err := c.state()
	if err != nil {
		return err
	}
	st, err := state.Load()
	if err != nil {
		return err
	}
	return emit(out, c.json, map[string]any{"devices": st.Devices, "service_token": st.ServiceToken != nil}, func(w io.Writer) {
		if len(st.Devices) == 0 {
			fmt.Fprintln(w, "本机没有创建过设备资源")
		}
		for name, d := range st.Devices {
			fmt.Fprintf(w, "%s  %s  隧道 %s\n", name, d.Hostname, d.TunnelID)
		}
	})
}

func cloudflareSetup(args []string, out io.Writer) error {
	var c cloudflareCommon
	fs := flag.NewFlagSet("cloudflare setup", flag.ContinueOnError)
	c.register(fs)
	name := fs.String("name", "", "设备名，用于主机名与资源名")
	cfgPath := fs.String("config", "", "xmesh.yaml 路径（schema_version 2）")
	dir := fs.String("dir", "", "放隧道令牌与 Access 密钥的目录")
	apply := fs.Bool("apply", false, "确认创建并写文件")
	if err := cloudflareFlags(fs, args); err != nil {
		return err
	}
	if *name == "" || *cfgPath == "" {
		return errors.New("需要 --name 与 --config")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if cfg.SchemaVersion != 2 {
		return errors.New("配置不是 schema_version 2；先运行 xmesh migrate")
	}
	mesh, cred, err := c.mesh()
	if err != nil {
		return err
	}
	// cloudflared forwards to wherever this node listens, which may not be the default port.
	mesh.Service = "tcp://" + cfg.Mesh.Cloudflare.ListenAddr()
	state, err := c.state()
	if err != nil {
		return err
	}
	secrets := *dir
	if secrets == "" {
		secrets = filepath.Join(filepath.Dir(*cfgPath), "cloudflare")
	}
	if secrets, err = filepath.Abs(secrets); err != nil {
		return err
	}
	plan := map[string]any{
		"device": *name, "domain": cred.Domain, "config": *cfgPath, "secrets_dir": secrets,
		"creates": []string{"Cloudflare 隧道（远程配置，转发 tcp://127.0.0.1:7434）", "代理 CNAME 记录", "Access 应用与仅放行网格服务令牌的策略", "网格服务令牌（本机首台设备时）"},
	}
	if !*apply {
		return emit(out, c.json, plan, func(w io.Writer) {
			fmt.Fprintf(w, "将为设备 %s 在 %s 下创建：\n", *name, cred.Domain)
			for _, item := range plan["creates"].([]string) {
				fmt.Fprintf(w, "  - %s\n", item)
			}
			fmt.Fprintf(w, "并在 %s 写入隧道令牌与 Access 密钥（0600），更新 %s 的 mesh.cloudflare。加 --apply 执行。\n", secrets, *cfgPath)
		})
	}
	tunnelFile, accessFile := filepath.Join(secrets, "tunnel-token"), filepath.Join(secrets, "access-secret")
	// Prove the edited configuration loads before anything is created at Cloudflare: a disabled section can
	// hold values that only fail once it is enabled.
	rehearsal := cloudflareSection{Hostname: "rehearsal." + cred.Domain, AccessClientID: "rehearsal.access", AccessClientSecretFile: accessFile, TunnelTokenFile: tunnelFile}
	if _, _, err := editedCloudflareConfig(*cfgPath, rehearsal); err != nil {
		return err
	}
	ctx, cancel := ctx30()
	defer cancel()
	device, token, err := mesh.Setup(ctx, state, *name)
	if err != nil {
		return err
	}
	cf := cloudflareSection{Hostname: device.Hostname, AccessClientID: token.ClientID, AccessClientSecretFile: accessFile, TunnelTokenFile: tunnelFile}
	if err := writeCloudflareFiles(*cfgPath, cf, device.TunnelToken, token.Secret); err != nil {
		return fmt.Errorf("资源已创建但写本机文件失败，可用 xmesh cloudflare remove --name %s --apply 回收：%w", *name, err)
	}
	return emit(out, c.json, map[string]any{"hostname": device.Hostname, "tunnel_id": device.TunnelID, "config": *cfgPath}, func(w io.Writer) {
		fmt.Fprintf(w, "设备 %s 已就绪：%s\n重启 xmesh 后，内核会用 cloudflared（HTTP/2）连上隧道；需要先安装 cloudflared。\n", *name, device.Hostname)
	})
}

func cloudflareRemove(args []string, out io.Writer) error {
	var c cloudflareCommon
	fs := flag.NewFlagSet("cloudflare remove", flag.ContinueOnError)
	c.register(fs)
	name := fs.String("name", "", "设备名")
	apply := fs.Bool("apply", false, "确认删除")
	if err := cloudflareFlags(fs, args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("需要 --name")
	}
	mesh, _, err := c.mesh()
	if err != nil {
		return err
	}
	state, err := c.state()
	if err != nil {
		return err
	}
	if !*apply {
		fmt.Fprintf(out, "将删除设备 %s 的隧道、DNS 记录与 Access 应用（最后一台还会删除网格服务令牌）。加 --apply 执行。\n", *name)
		return nil
	}
	ctx, cancel := ctx30()
	defer cancel()
	if err := mesh.Remove(ctx, state, *name); err != nil {
		return err
	}
	return emit(out, c.json, map[string]any{"removed": *name}, func(w io.Writer) { fmt.Fprintf(w, "已删除设备 %s 的 Cloudflare 资源\n", *name) })
}

func writeCloudflareFiles(cfgPath string, cf cloudflareSection, tunnelToken, accessSecret string) error {
	if err := writePrivate(cf.TunnelTokenFile, tunnelToken); err != nil {
		return err
	}
	if err := writePrivate(cf.AccessClientSecretFile, accessSecret); err != nil {
		return err
	}
	return setCloudflareConfig(cfgPath, cf)
}

func writePrivate(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content+"\n"), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// cloudflareSection is what setup writes into mesh.cloudflare. Other keys the user set are kept.
type cloudflareSection struct {
	Hostname, AccessClientID, AccessClientSecretFile, TunnelTokenFile string
}

// setCloudflareConfig edits the file as YAML nodes so comments and the other sections survive, and
// writes it only when the result still loads.
func setCloudflareConfig(path string, s cloudflareSection) error {
	edited, mode, err := editedCloudflareConfig(path, s)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, edited, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// editedCloudflareConfig returns the file with mesh.cloudflare set, after checking that it still loads.
func editedCloudflareConfig(path string, s cloudflareSection) ([]byte, os.FileMode, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, 0, errors.New("xmesh.yaml 不是有效的 YAML 映射")
	}
	mesh := childMapping(doc.Content[0], "mesh")
	cf := childMapping(mesh, "cloudflare")
	setScalar(cf, "enabled", "true", "!!bool")
	setScalar(cf, "hostname", s.Hostname, "!!str")
	setScalar(cf, "access_client_id", s.AccessClientID, "!!str")
	setScalar(cf, "access_client_secret_file", s.AccessClientSecretFile, "!!str")
	setScalar(cf, "tunnel_token_file", s.TunnelTokenFile, "!!str")
	edited, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, 0, err
	}
	if _, err := config.Parse(edited); err != nil {
		return nil, 0, fmt.Errorf("修改后的配置无效：%w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, err
	}
	return edited, info.Mode().Perm(), nil
}

func childMapping(parent *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(parent.Content); i += 2 {
		if parent.Content[i].Value == key {
			if parent.Content[i+1].Kind != yaml.MappingNode {
				parent.Content[i+1] = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			return parent.Content[i+1]
		}
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, child)
	return child
}

func setScalar(m *yaml.Node, key, value, tag string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}
