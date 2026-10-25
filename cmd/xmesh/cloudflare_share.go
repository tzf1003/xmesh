package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/cfadmin"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
)

// shareNode is the running node that already trusts the device being given a Cloudflare tunnel.
type shareNode interface {
	// Trusted maps the device IDs this node trusts to their names.
	Trusted() (map[string]string, error)
	SendOffer(ctx context.Context, deviceID string, offer v2.DeviceOffer) error
}

// openShareNode is replaced by tests.
var openShareNode = func(node, stateDir string) (shareNode, error) {
	n, err := controlInviteNode(node, stateDir)
	if err != nil {
		return nil, err
	}
	return n.(controlNode), nil
}

func (n controlNode) SendOffer(ctx context.Context, id string, offer v2.DeviceOffer) error {
	r, err := n.client.Do(ctx, "POST", "/v2/offers", v2.OfferSendRequest{DeviceID: id, Offer: offer}, nil)
	if err != nil {
		return err
	}
	return r.Error()
}

// resolveDevice finds a trusted device by its ID or, when exactly one has it, its name.
func resolveDevice(trusted map[string]string, want string) (id, name string, err error) {
	if got, ok := trusted[want]; ok {
		return want, got, nil
	}
	for candidate, got := range trusted {
		if got != want {
			continue
		}
		if id != "" {
			return "", "", fmt.Errorf("有多台已配对设备叫 %s，请改用设备 ID", want)
		}
		id, name = candidate, got
	}
	if id == "" {
		return "", "", fmt.Errorf("没有已配对的设备 %s；先配对，或检查设备 ID", want)
	}
	return id, name, nil
}

// cloudflareShare gives a device this node has already paired its own Cloudflare tunnel, over the
// link they share. The device applies it only after its user accepts; the Cloudflare API token never
// leaves this machine.
func cloudflareShare(args []string, out io.Writer) (retErr error) {
	var c cloudflareCommon
	fs := flag.NewFlagSet("cloudflare share", flag.ContinueOnError)
	c.register(fs)
	target := fs.String("device", "", "已配对设备的名字或设备 ID")
	cfgPath := fs.String("config", "", "本机 xmesh.yaml 路径")
	node := fs.String("node", "", "本机节点名")
	stateDir := fs.String("state-dir", "", "本机节点状态目录")
	apply := fs.Bool("apply", false, "确认创建隧道并下发")
	if err := cloudflareFlags(fs, args); err != nil {
		return err
	}
	if *target == "" || *cfgPath == "" {
		return errors.New("需要 --device 与 --config")
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
	sender, err := openShareNode(*node, *stateDir)
	if err != nil {
		return fmt.Errorf("连不上本机运行中的 xmesh（需要先启动）：%w", err)
	}
	trusted, err := sender.Trusted()
	if err != nil {
		return err
	}
	id, name, err := resolveDevice(trusted, *target)
	if err != nil {
		return err
	}
	if !cfadmin.ValidInviteName(name) {
		return fmt.Errorf("设备名 %q 不能用作 Cloudflare 资源名：只含字母、数字和连字符", name)
	}
	if !*apply {
		return emit(out, c.json, map[string]any{"device": name, "device_id": id, "domain": cred.Domain}, func(w io.Writer) {
			fmt.Fprintf(w, "将为已配对设备 %s（%s）在 %s 下创建隧道，并经你们之间已有的加密连接发给它；对方确认后才会应用。加 --apply 执行。\n", name, id, cred.Domain)
		})
	}
	interrupted, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	ctx, cancel := context.WithTimeout(interrupted, 60*time.Second)
	defer cancel()
	mesh.Service = "tcp://127.0.0.1:7434"
	dev, token, err := mesh.Setup(ctx, state, name)
	if err != nil {
		return err
	}
	delivered := false
	defer func() {
		if delivered {
			return
		}
		bg, stop := ctx30()
		defer stop()
		if err := mesh.Remove(bg, state, name); err != nil {
			fmt.Fprintf(os.Stderr, "未能回收 %s 的 Cloudflare 资源，可运行 xmesh cloudflare remove --name %s --apply：%v\n", name, name, err)
		}
	}()
	offer := v2.DeviceOffer{ID: device.NewOfferID(), Kind: device.OfferCloudflare, Cloudflare: &v2.CloudflareOffer{
		Hostname: dev.Hostname, TunnelToken: dev.TunnelToken, AccessID: token.ClientID, AccessSecret: token.Secret, PeerHostname: cf.Hostname,
	}}
	if err := sender.SendOffer(ctx, id, offer); err != nil {
		return fmt.Errorf("没能把连接方式送到 %s（对方需要在线，并且已配对）：%w", name, err)
	}
	delivered = true
	if err := addCloudflarePeer(*cfgPath, id, dev.Hostname); err != nil {
		// The offer is delivered and the tunnel exists; only this node's address book lacks the device.
		return fmt.Errorf("已发给 %s，但未能写入本机的 mesh.cloudflare.peers：%w\n请手动添加  %s: %s", name, err, id, dev.Hostname)
	}
	return emit(out, c.json, map[string]any{"event": "shared", "device": name, "device_id": id, "hostname": dev.Hostname}, func(w io.Writer) {
		fmt.Fprintf(w, "已把 Cloudflare 连接方式发给 %s，并把 %s 记进本机 mesh.cloudflare.peers（重启 xmesh 后生效）。\n在 %s 上运行 xmesh control offers list 查看，xmesh control offers accept <编号> 接受。\n", name, dev.Hostname, name)
	})
}

// --- xmesh control offers ---

const offersUsage = "用法: xmesh control offers list | accept 编号 | reject 编号"

func controlOffers(ctx context.Context, flags *flag.FlagSet, target targetFlags, args []string) error {
	if len(args) == 0 {
		return errors.New(offersUsage)
	}
	verb, rest := args[0], args[1:]
	if err := flags.Parse(rest); err != nil {
		return err
	}
	client, err := target.client()
	if err != nil {
		return err
	}
	switch verb {
	case "list":
		if flags.NArg() != 0 {
			return errors.New(offersUsage)
		}
		return show(client.Do(ctx, "GET", "/v2/offers", nil, nil))
	case "accept", "reject":
		if flags.NArg() != 1 || strings.TrimSpace(flags.Arg(0)) == "" {
			return errors.New(offersUsage)
		}
		id := flags.Arg(0)
		if verb == "accept" {
			return show(client.Do(ctx, "POST", "/v2/offers/"+id+"/accept", nil, nil))
		}
		r, err := client.Do(ctx, "DELETE", "/v2/offers/"+id, nil, nil)
		return showDone(r, err, map[string]any{"id": id, "rejected": true})
	}
	return fmt.Errorf("未知的 offers 子命令: %s\n%s", verb, offersUsage)
}
