package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/cfadmin"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/connectivity"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/device"
)

// inviteNode is the running inviting node: its identity, and the connection password that opens the
// pairing window.
type inviteNode interface {
	Fingerprint(ctx context.Context) (string, error)
	SetPassword(ctx context.Context, password string) error
	ClearPassword(ctx context.Context) error
	// Trusted maps the device IDs this node trusts to their names.
	Trusted() (map[string]string, error)
}

var (
	// openInviteNode and invitePoll are replaced by tests.
	openInviteNode = controlInviteNode
	invitePoll     = 250 * time.Millisecond
)

type controlNode struct {
	client *control.Client
	dir    string
}

func controlInviteNode(node, stateDir string) (inviteNode, error) {
	e, err := controlTarget(node, stateDir)
	if err != nil {
		return nil, err
	}
	return controlNode{client: control.NewClient(e), dir: e.Dir}, nil
}

func (n controlNode) Fingerprint(ctx context.Context) (string, error) {
	r, err := n.client.Do(ctx, "POST", "/v2/pair/export", nil, nil)
	if err != nil {
		return "", err
	}
	if err = r.Error(); err != nil {
		return "", err
	}
	var res v2.PairExportResponse
	if err = json.Unmarshal(r.Body, &res); err != nil {
		return "", err
	}
	return res.Pair.Fingerprint, nil
}

func (n controlNode) SetPassword(ctx context.Context, password string) error {
	r, err := n.client.Do(ctx, "PUT", "/v2/pair/password", v2.PairPasswordRequest{Password: password}, nil)
	if err != nil {
		return err
	}
	return r.Error()
}

func (n controlNode) ClearPassword(ctx context.Context) error {
	r, err := n.client.Do(ctx, "DELETE", "/v2/pair/password", nil, nil)
	if err != nil {
		return err
	}
	return r.Error()
}

func (n controlNode) Trusted() (map[string]string, error) {
	trust, err := device.OpenTrust(n.dir)
	if err != nil {
		return nil, err
	}
	ids := map[string]string{}
	for _, t := range trust.Snapshot() {
		ids[t.Document.DeviceID] = t.Document.Name
	}
	return ids, nil
}

// cloudflareInvite prepares a new device's tunnel, opens the pairing window on this node and waits for
// the new device to pair, then closes the window. Nothing it creates outlives a failed or expired invite.
func cloudflareInvite(args []string, out io.Writer) (retErr error) {
	var c cloudflareCommon
	fs := flag.NewFlagSet("cloudflare invite", flag.ContinueOnError)
	c.register(fs)
	name := fs.String("name", "", "新设备名")
	cfgPath := fs.String("config", "", "本机 xmesh.yaml 路径")
	node := fs.String("node", "", "本机节点名")
	stateDir := fs.String("state-dir", "", "本机节点状态目录")
	ttl := fs.Duration("ttl", cfadmin.InviteTTL, "加入码有效时长")
	apply := fs.Bool("apply", false, "确认创建并等待对方加入")
	if err := cloudflareFlags(fs, args); err != nil {
		return err
	}
	if *name == "" || *cfgPath == "" {
		return errors.New("需要 --name 与 --config")
	}
	if !cfadmin.ValidInviteName(*name) {
		return errors.New("--name 须是合法的节点名：字母或数字开头，只含字母、数字和连字符，最长 63 个字符")
	}
	if *ttl <= 0 || *ttl > cfadmin.InviteTTL {
		return fmt.Errorf("--ttl 须在 0 到 %s 之间", cfadmin.InviteTTL)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	cf := cfg.Mesh.Cloudflare
	if cfg.SchemaVersion != 2 || !cf.Enabled || cf.Hostname == "" {
		return errors.New("本机还没有启用 Cloudflare 通道：先对本机运行 xmesh cloudflare setup")
	}
	if *stateDir == "" && *node == "" {
		*node = cfg.Node.Name
	}
	mesh, cred, err := c.mesh()
	if err != nil {
		return err
	}
	state, err := c.state()
	if err != nil {
		return err
	}
	if !*apply {
		return emit(out, c.json, map[string]any{"device": *name, "domain": cred.Domain, "ttl": ttl.String()}, func(w io.Writer) {
			fmt.Fprintf(w, "将为新设备 %s 在 %s 下创建隧道，生成 %s 内有效的加入码，并临时开启本机的连接密码。加 --apply 执行。\n", *name, cred.Domain, *ttl)
		})
	}
	// Ctrl-C or SIGTERM at any point after this returns through the cleanup below.
	interrupted, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(interrupted, 30*time.Second)
	fingerprint, node2, err := func() (string, inviteNode, error) {
		defer cancel()
		n, err := openInviteNode(*node, *stateDir)
		if err != nil {
			return "", nil, fmt.Errorf("连不上本机运行中的 xmesh（需要先启动）：%w", err)
		}
		fp, err := n.Fingerprint(ctx)
		return fp, n, err
	}()
	if err != nil {
		return err
	}
	before, err := node2.Trusted()
	if err != nil {
		return err
	}
	password, err := cfadmin.NewPassword()
	if err != nil {
		return err
	}
	ctx, cancel = context.WithTimeout(interrupted, 30*time.Second)
	defer cancel()
	mesh.Service = "tcp://127.0.0.1:7434"
	dev, token, err := mesh.Setup(ctx, state, *name)
	if err != nil {
		return err
	}
	joined := false
	defer func() {
		bg, stop := ctx30()
		defer stop()
		if err := node2.ClearPassword(bg); err != nil {
			fmt.Fprintf(os.Stderr, "警告：未能关闭本机的连接密码（%v）；请运行 xmesh control pair password clear 关闭配对窗口\n", err)
			if retErr == nil {
				retErr = errors.New("未能关闭配对窗口")
			}
		}
		if !joined {
			if err := mesh.Remove(bg, state, *name); err != nil {
				fmt.Fprintf(os.Stderr, "未能回收 %s 的 Cloudflare 资源，可运行 xmesh cloudflare remove --name %s --apply：%v\n", *name, *name, err)
			}
		}
	}()
	expires := time.Now().Add(*ttl)
	invite := cfadmin.Invite{Version: 1, Name: *name, Admin: cf.Hostname, Fingerprint: fingerprint, Password: password, Hostname: dev.Hostname, TunnelToken: dev.TunnelToken, AccessID: token.ClientID, AccessKey: token.Secret, Expires: expires.UnixMilli()}
	code, err := invite.Encode()
	if err != nil {
		return err
	}
	if err := node2.SetPassword(ctx, password); err != nil {
		return err
	}
	if c.json {
		if err := emit(out, true, map[string]any{"event": "invite", "device": *name, "code": code, "expires": expires.UTC().Format(time.RFC3339)}, nil); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(out, "在新设备 %s 上运行（%s 前有效，只能用一次）：\n\n  xmesh join %s\n\n新设备的节点名必须是 %s。等待它加入…（Ctrl-C 取消）\n", *name, expires.Format("15:04:05"), code, *name)
	}
	deadline := time.NewTimer(*ttl)
	defer deadline.Stop()
	tick := time.NewTicker(invitePoll)
	defer tick.Stop()
	for {
		select {
		case <-interrupted.Done():
			return errors.New("已取消，已关闭配对窗口并回收为新设备创建的 Cloudflare 资源")
		case <-deadline.C:
			return errors.New("加入码已过期，已回收为新设备创建的 Cloudflare 资源")
		case <-tick.C:
			now, err := node2.Trusted()
			if err != nil {
				return err
			}
			// Only the device that carries the invited name counts, so an unrelated pairing cannot end the invite.
			for id, got := range now {
				if _, known := before[id]; !known && got == *name {
					joined = true
					if c.json {
						return emit(out, true, map[string]any{"event": "joined", "device": *name, "device_id": id}, nil)
					}
					fmt.Fprintf(out, "设备 %s（%s）已加入，配对窗口已关闭。\n", *name, id)
					return nil
				}
			}
		}
	}
}

// joinFlags is shared so tests can drive the command.
func joinCmd(args []string) error { return joinRun(args, os.Stdout) }

const joinUsage = "用法: xmesh join 加入码 --config xmesh.yaml [--state-dir 目录] [--dir 目录]\n" +
	"加入码来自已有设备上的 xmesh cloudflare invite。加入后重启 xmesh，本机会用 cloudflared 连上隧道。"

// joinPair opens this device's pairing state and pairs with the inviting device through the tunnel.
var joinPair = func(ctx context.Context, cfg *config.Config, stateDir string, inv cfadmin.Invite) (string, error) {
	node, err := connectivity.OpenPairingClient(ctx, cfg, stateDir)
	if err != nil {
		return "", err
	}
	defer node.Close()
	doc, err := node.PairWith(ctx, device.Cloudflare, "https://"+inv.Admin, inv.Password, "dev-"+inv.Fingerprint)
	if err != nil {
		return "", err
	}
	return doc.DeviceID, nil
}

func joinRun(args []string, out io.Writer) error {
	code, rest := leadingName(args)
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfgPath := fs.String("config", "", "本机 xmesh.yaml 路径（schema_version 2）")
	stateDir := fs.String("state-dir", "", "本机节点状态目录")
	dir := fs.String("dir", "", "放隧道令牌与 Access 密钥的目录")
	if err := fs.Parse(rest); err != nil {
		return fmt.Errorf("%w\n%s", err, joinUsage)
	}
	if code == "" && fs.NArg() == 1 {
		code = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return fmt.Errorf("多余的参数\n%s", joinUsage)
	}
	if code == "" || *cfgPath == "" {
		return errors.New(joinUsage)
	}
	inv, err := cfadmin.DecodeInvite(code, time.Now())
	if err != nil {
		return err
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if cfg.SchemaVersion != 2 {
		return errors.New("配置不是 schema_version 2；先运行 xmesh migrate")
	}
	if cfg.Node.Name != inv.Name {
		return fmt.Errorf("这个加入码是给节点名 %q 的，本机配置里的节点名是 %q", inv.Name, cfg.Node.Name)
	}
	defaultListen := config.Cloudflare{}.ListenAddr()
	if l := cfg.Mesh.Cloudflare.Listen; l != "" && l != defaultListen {
		return fmt.Errorf("邀请创建的隧道转发到 %s；本机 mesh.cloudflare.listen 是 %s，加入前请先改回默认值", defaultListen, l)
	}
	if existing := cfg.Mesh.Cloudflare; existing.Hostname != "" || existing.AccessClientID != "" || existing.AccessClientSecretFile != "" || existing.TunnelTokenFile != "" {
		return errors.New("本机已经配置了 Cloudflare 凭据；加入前先清理 mesh.cloudflare，以免覆盖它们")
	}
	if *stateDir == "" {
		if *stateDir, err = app.StateDir(cfg.Node.Name); err != nil {
			return err
		}
	}
	secrets := *dir
	if secrets == "" {
		secrets = filepath.Join(filepath.Dir(*cfgPath), "cloudflare")
	}
	if secrets, err = filepath.Abs(secrets); err != nil {
		return err
	}
	adminID := "dev-" + inv.Fingerprint
	section := cloudflareSection{Hostname: inv.Hostname, AccessClientID: inv.AccessID, AccessClientSecretFile: filepath.Join(secrets, "access-secret"), TunnelTokenFile: filepath.Join(secrets, "tunnel-token"), Peers: map[string]string{adminID: inv.Admin}}
	// Prove the result loads before anything is written or the invite is spent.
	if _, _, err := editedCloudflareConfig(*cfgPath, section); err != nil {
		return err
	}
	for _, path := range []string{section.AccessClientSecretFile, section.TunnelTokenFile} {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s 已存在，不会覆盖；换一个 --dir 或先移走它", path)
		}
	}
	if err := writePrivate(section.AccessClientSecretFile, inv.AccessKey); err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = os.Remove(section.AccessClientSecretFile)
			_ = os.Remove(section.TunnelTokenFile)
		}
	}()
	pairCfg := *cfg
	pairCfg.Mesh.Cloudflare = config.Cloudflare{Enabled: true, Hostname: section.Hostname, AccessClientID: section.AccessClientID, AccessClientSecretFile: section.AccessClientSecretFile}
	interrupted, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(interrupted, 45*time.Second)
	defer cancel()
	id, err := joinPair(ctx, &pairCfg, *stateDir, inv)
	if err != nil {
		return fmt.Errorf("加入失败（加入码只能用一次，失败后请让对方重新生成）：%w", err)
	}
	if id != adminID {
		return errors.New("配对到的设备与加入码不一致")
	}
	if err := writePrivate(section.TunnelTokenFile, inv.TunnelToken); err != nil {
		return err
	}
	if err := setCloudflareConfig(*cfgPath, section); err != nil {
		return err
	}
	done = true
	fmt.Fprintf(out, "已加入网格，并与 %s 配对。重启 xmesh 后，本机会通过 %s 上线；需要先安装 cloudflared。\n", strings.TrimPrefix(adminID, "dev-")[:12], inv.Hostname)
	return nil
}
