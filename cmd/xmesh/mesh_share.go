package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/meshadmin"
)

// shareKeyTTL covers both waits an offer can have: up to OfferTTL queued here for the device to
// collect, then a fresh OfferTTL in its inbox for the user to accept.
const shareKeyTTL = 2 * device.OfferTTL

// createShareKey makes a pre-authorised key through the saved admin connection; tests replace it.
var createShareKey = func(c *meshCommon, req meshadmin.AuthKeyRequest) (meshadmin.AuthKey, error) {
	p, _, _, err := c.provider(meshadmin.AccessWrite)
	if err != nil {
		return meshadmin.AuthKey{}, err
	}
	ctx, cancel := ctx30()
	defer cancel()
	return p.CreateAuthKey(ctx, req)
}

// shareAdmin reports whether a write-capable admin connection is saved for the same control plane
// this node is on, without calling the network. note explains a saved connection that does not fit.
var shareAdmin = func(c *meshCommon, controlURL string) (ok bool, note string) {
	cred, _, _, err := c.credential(meshadmin.AccessWrite)
	if err != nil {
		return false, ""
	}
	if !sameControlPlane(cred, controlURL) {
		return false, "已保存的管理凭据属于另一个控制面（" + string(cred.Kind) + "），不能用来给 " + controlURL + " 生成 key。"
	}
	return true, ""
}

// sameControlPlane checks that an admin credential manages the control plane at controlURL, so a key
// made through it is one the receiving device can use there.
func sameControlPlane(cred meshadmin.Credential, controlURL string) bool {
	switch cred.Kind {
	case meshadmin.KindTailscale:
		return officialTailscale(controlURL)
	case meshadmin.KindHeadscale:
		return !officialTailscale(controlURL) && strings.TrimSuffix(strings.ToLower(cred.URL), "/") == strings.TrimSuffix(strings.ToLower(controlURL), "/")
	}
	return false
}

// officialTailscale reports whether a control URL is Tailscale's own service.
func officialTailscale(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Hostname()) {
	case "controlplane.tailscale.com", "login.tailscale.com":
		return true
	}
	return false
}

// keyGuidance tells the user where to make a key when this machine cannot.
func keyGuidance(controlURL string) string {
	if officialTailscale(controlURL) {
		return "在 https://login.tailscale.com/admin/settings/keys 生成一个 auth key（建议单次使用、有效期 24 小时），保存到文件后用 --authkey-file 重新运行；" +
			"或先运行 xmesh mesh login 保存管理凭据，这里就能自动生成。"
	}
	return "在你的 Headscale 服务器上运行  headscale preauthkeys create --user <用户> --expiration 24h  生成 key，保存到文件后用 --authkey-file 重新运行；" +
		"或先运行 xmesh mesh login 保存管理凭据，这里就能自动生成。"
}

// meshShare gives a paired device a way onto this node's tailnet: a pre-authorised key, made through
// the saved admin connection when there is one, otherwise taken from a file the user provides. The
// device applies it only after its user accepts. The admin credential never leaves this machine.
func meshShare(args []string, out io.Writer) error {
	var c meshCommon
	fs := flag.NewFlagSet("mesh share", flag.ContinueOnError)
	c.register(fs)
	target := fs.String("device", "", "已配对设备的名字或设备 ID")
	cfgPath := fs.String("config", "", "本机 xmesh.yaml 路径")
	node := fs.String("node", "", "本机节点名")
	stateDir := fs.String("state-dir", "", "本机节点状态目录")
	keyFile := fs.String("authkey-file", "", "自己生成的 auth key 文件（没有管理凭据时使用）")
	reuseLocal := fs.Bool("reuse-local-key", false, "没有管理凭据时，复用本机 mesh.tailnet.authkey_file 里的 key（它必须是可重复使用的）")
	user := fs.String("user", "", "Headscale 用户名（服务器上有多个用户时必填）")
	tags := fs.String("tags", "", "自动生成 key 时附带的控制面标签（与设备的 xmesh 标签无关）")
	reusable := fs.Bool("reusable", false, "自动生成的 key 可重复使用（默认只能用一次）")
	ephemeral := fs.Bool("ephemeral", false, "让对方注册为临时节点")
	apply := fs.Bool("apply", false, "确认发送")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if *target == "" || *cfgPath == "" {
		return errors.New("需要 --device 与 --config")
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if cfg.SchemaVersion != 2 || !cfg.Mesh.TailnetEnabled() || cfg.Mesh.Tailnet.ControlURL == "" {
		return errors.New("本机还没有启用 Tailnet：先配置 mesh.tailnet 并接入，再把它分享给其他设备")
	}
	controlURL := cfg.Mesh.Tailnet.ControlURL
	offerURL := strings.TrimSuffix(controlURL, "/")
	if officialTailscale(controlURL) {
		offerURL = ""
	}
	wantTags, err := meshadmin.CanonicalTags(splitTags(*tags))
	if err != nil {
		return err
	}
	if *stateDir == "" && *node == "" {
		*node = cfg.Node.Name
	}

	// Pick where the key comes from before touching anything.
	source := ""
	adminOK, adminNote := shareAdmin(&c, controlURL)
	switch {
	case *keyFile != "":
		source = "file"
	case adminOK:
		source = "admin"
	case *reuseLocal:
		source = "local"
	default:
		return errors.New("没有可用的管理凭据，也没有指定 key。" + adminNote + keyGuidance(controlURL))
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
	if !*apply {
		return emit(out, c.json, map[string]any{"device": name, "device_id": id, "key_source": source, "control_url": controlURL}, func(w io.Writer) {
			how := map[string]string{"admin": "用已保存的管理凭据生成一个新的 auth key", "file": "使用 --authkey-file 里的 key", "local": "复用本机 mesh.tailnet.authkey_file 里的 key"}[source]
			fmt.Fprintf(w, "将%s，经你们之间已有的加密连接发给已配对设备 %s（%s）；对方确认后才会应用。加 --apply 执行。\n", how, name, id)
		})
	}

	key, err := shareKey(&c, source, *keyFile, cfg, wantTags, *reusable, *ephemeral, *user, name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	offer := v2.DeviceOffer{ID: device.NewOfferID(), Kind: device.OfferTailscale, Tailscale: &v2.TailscaleOffer{ControlURL: offerURL, AuthKey: key, Ephemeral: *ephemeral}}
	delivery, err := sender.SendOffer(ctx, id, offer)
	if err != nil {
		return fmt.Errorf("没能把连接方式送到 %s（对方需要已配对）：%w", name, err)
	}
	return emit(out, c.json, map[string]any{"event": "shared", "device": name, "device_id": id, "delivery": delivery, "key_source": source}, func(w io.Writer) {
		if delivery == "queued" {
			fmt.Fprintf(w, "本机拨不通 %s，已把 Tailscale 连接方式留在本机，等它经自己的连接来取（它在线时一分钟内）。\n", name)
		} else {
			fmt.Fprintf(w, "已把 Tailscale 连接方式发给 %s。\n", name)
		}
		fmt.Fprintf(w, "在 %s 上运行 xmesh control offers list 查看，xmesh control offers accept <编号> 接受。\n", name)
	})
}

func shareKey(c *meshCommon, source, keyFile string, cfg *config.Config, tags []string, reusable, ephemeral bool, user, deviceName string) (string, error) {
	switch source {
	case "admin":
		k, err := createShareKey(c, meshadmin.AuthKeyRequest{Tags: tags, TTL: shareKeyTTL, Reusable: reusable, Ephemeral: ephemeral, User: user, Description: "xmesh share " + deviceName})
		if err != nil {
			return "", fmt.Errorf("管理凭据没能生成 key：%w\n%s", err, keyGuidance(cfg.Mesh.Tailnet.ControlURL))
		}
		return k.Secret, nil
	case "file":
		return readKeyFile(keyFile)
	default:
		if cfg.Mesh.Tailnet.AuthkeyFile == "" {
			return "", errors.New("本机没有配置 mesh.tailnet.authkey_file，无法复用。" + keyGuidance(cfg.Mesh.Tailnet.ControlURL))
		}
		return readKeyFile(cfg.Mesh.Tailnet.AuthkeyFile)
	}
}

func readKeyFile(path string) (string, error) {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("读不到 key 文件 %s：%w", path, err)
	}
	key := strings.TrimSpace(string(raw))
	if len(key) < 8 || len(key) > 4096 || strings.ContainsAny(key, " \t\r\n") {
		return "", fmt.Errorf("%s 里不是一个有效的 auth key", path)
	}
	return key, nil
}
