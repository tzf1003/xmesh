package onboarding

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
)

var nodeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)

var DefaultDeny = []string{"~/.ssh", "~/.gnupg", "~/.aws", "~/.config/gcloud", "~/.azure", "~/.kube", "~/.docker"}

func Normalize(in Input) (Input, error) {
	if in.SchemaVersion != SchemaVersion {
		return in, problem("unsupported_version", "配置协议版本不受支持", "schema_version")
	}
	if !slices.Contains([]string{"local", "multi-device", "cloud"}, in.Purpose) {
		return in, problem("invalid_args", "请选择使用用途", "purpose")
	}
	if !nodeName.MatchString(in.NodeName) {
		return in, problem("invalid_args", "设备名只能包含字母、数字和连字符", "node_name")
	}
	if !filepath.IsAbs(in.Workspace) || strings.ContainsAny(in.Workspace, "\x00\r\n") {
		return in, problem("invalid_args", "请选择绝对路径的工作目录", "workspace")
	}
	st, err := os.Stat(in.Workspace)
	if err != nil || !st.IsDir() {
		return in, problem("workspace_unavailable", "工作目录不存在或无法访问", "workspace")
	}
	in.Workspace, err = filepath.EvalSymlinks(filepath.Clean(in.Workspace))
	if err != nil {
		return in, problem("workspace_unavailable", "工作目录无法解析为实际路径", "workspace")
	}
	if in.Permission == "" {
		in.Permission = "development"
	}
	if !slices.Contains([]string{"development", "readonly"}, in.Permission) {
		return in, problem("invalid_args", "权限必须为 development 或 readonly", "permission")
	}
	if in.Clients == nil {
		in.Clients = []string{}
	}
	seen := map[string]bool{}
	for _, id := range in.Clients {
		if !slices.Contains([]string{"codex", "claude-code"}, id) || seen[id] {
			return in, problem("invalid_args", "客户端无效或重复", "clients")
		}
		seen[id] = true
	}
	if in.Autostart == nil {
		yes := true
		in.Autostart = &yes
	}
	if in.Network.Mode == "" {
		in.Network.Mode = "lan"
		if in.Purpose == "cloud" {
			in.Network.Mode = "tailnet"
		}
	}
	if !slices.Contains([]string{"lan", "tailnet", "coexist"}, in.Network.Mode) {
		return in, problem("invalid_args", "网络模式无效", "network.mode")
	}
	if in.Purpose == "local" && in.Network.Mode != "lan" {
		return in, problem("invalid_args", "本机用途使用本机连接", "network.mode")
	}
	if in.Purpose == "cloud" && in.Network.Mode != "tailnet" && in.Network.Mode != "coexist" {
		return in, problem("invalid_args", "云端用途需要跨网络连接", "network.mode")
	}
	if in.Network.Mode == "tailnet" || in.Network.Mode == "coexist" {
		if in.Network.ControlURL == "" {
			in.Network.ControlURL = config.DefaultControlURL
		}
		u, e := url.Parse(in.Network.ControlURL)
		if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return in, problem("invalid_args", "控制面必须为有效的 HTTPS 地址", "network.control_url")
		}
		if in.Network.AuthMethod == "" {
			in.Network.AuthMethod = "browser"
		}
		if !slices.Contains([]string{"browser", "key"}, in.Network.AuthMethod) {
			return in, problem("invalid_args", "入网方式无效", "network.auth_method")
		}
		if in.Network.AuthMethod == "key" {
			if !filepath.IsAbs(in.Network.AuthkeyFile) {
				return in, problem("invalid_args", "请选择授权密钥文件", "network.authkey_file")
			}
		} else if in.Network.AuthkeyFile != "" {
			return in, problem("invalid_args", "浏览器登录与密钥文件只能选择一种", "network.authkey_file")
		}
		if in.Purpose == "cloud" && !config.SupportsFunnel(in.Network.ControlURL) {
			return in, problem("funnel_unavailable", "快速云端接入需要官方 Tailscale；其他入口请使用功能中心", "network.control_url")
		}
	} else if in.Network.ControlURL != "" || in.Network.AuthMethod != "" || in.Network.AuthkeyFile != "" {
		return in, problem("invalid_args", "局域网连接不使用控制面凭据", "network")
	}
	return in, nil
}

func scopes(permission, device string) []string {
	if permission == "readonly" {
		return []string{"devices:read", "sys:" + device, "fs:read:" + device}
	}
	return []string{"devices:read", "sys:" + device, "fs:" + device, "exec:" + device, "pty:" + device, "backend:" + device + ":*"}
}

func configText(in Input, dir, listen string) (string, error) {
	mesh := map[string]any{
		"lan":     map[string]any{"enabled": in.Network.Mode != "tailnet", "mdns": in.Purpose != "local"},
		"tailnet": map[string]any{"enabled": in.Network.Mode == "tailnet" || in.Network.Mode == "coexist"},
	}
	if in.Network.Mode == "tailnet" || in.Network.Mode == "coexist" {
		tailnet := mesh["tailnet"].(map[string]any)
		tailnet["control_url"] = in.Network.ControlURL
		if in.Network.AuthMethod == "browser" {
			tailnet["auth_method"] = "browser"
		} else {
			tailnet["authkey_file"] = in.Network.AuthkeyFile
		}
	}
	tools := []string{"*"}
	if in.Permission == "readonly" {
		tools = []string{"sys.info", "fs.read", "fs.list", "fs.stat", "fs.glob", "fs.grep", "fs.sha256", "fs.download"}
	}
	grant := map[string]any{"tools": tools, "backends": []string{}}
	if in.Permission == "development" {
		grant["backends"] = []string{"*"}
	}
	grants := map[string]any{"tag:peer": grant, "tag:device": grant}
	if in.Purpose == "cloud" {
		grants["tag:ingress"] = grant
	}
	doc := map[string]any{
		"schema_version": 2,
		"node":           map[string]any{"name": in.NodeName, "roles": []string{"agent", "gateway"}},
		"mesh":           mesh,
		"agent":          map[string]any{"default_cwd": in.Workspace, "cwd_policy": map[string]any{"allow": []string{in.Workspace}, "deny_file": filepath.Join(dir, "cwd-deny.yaml")}, "authz": grants},
		"gateway": map[string]any{"listen": listen, "access": []string{"localhost"}, "pats": map[string]any{
			"onboarding": map[string]any{"token_file": filepath.Join(dir, "pats", "onboarding.token"), "scopes": scopes(in.Permission, "*")},
		}},
	}
	// JSON is a YAML subset and keeps every supplied path a single scalar.
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	if previous, e := os.ReadFile(filepath.Join(dir, "xmesh.yaml")); e == nil {
		// Keep backends, quotas, policy and existing PATs in their original YAML.
		edits := []control.Edit{{Path: "/schema_version", Value: 2}, {Path: "/node/name", Value: in.NodeName}, {Path: "/node/roles", Value: []string{"agent", "gateway"}}, {Path: "/mesh", Value: mesh}, {Path: "/gateway/listen", Value: listen}, {Path: "/gateway/access", Value: []string{"localhost"}}, {Path: "/gateway/pats/onboarding", Value: doc["gateway"].(map[string]any)["pats"].(map[string]any)["onboarding"]}, {Path: "/agent/default_cwd", Value: in.Workspace}, {Path: "/agent/cwd_policy/allow", Value: []string{in.Workspace}}, {Path: "/agent/cwd_policy/deny_file", Value: filepath.Join(dir, "cwd-deny.yaml")}}
		edits = append(edits, control.Edit{Path: "/agent/authz", Value: grants})
		raw, err = control.ApplyEdits(previous, edits)
		if err != nil {
			return "", err
		}
	}
	if _, err = config.Parse(raw); err != nil {
		return "", problem("config_invalid", "生成的配置未通过校验，请核对已有设备设置后重试", "config")
	}
	return string(raw) + "\n", nil
}

func availableGateway() (string, error) {
	for port := 8080; port <= 8099; port++ {
		address := fmt.Sprintf("127.0.0.1:%d", port)
		if connectedGateway(address) {
			continue
		}
		l, e := net.Listen("tcp", address)
		if e == nil {
			_ = l.Close()
			return address, nil
		}
	}
	return "", problem("port_unavailable", "本机端口 8080–8099 均不可用，请关闭占用程序后重试", "gateway")
}

func connectedGateway(address string) bool {
	connection, err := net.DialTimeout("tcp", address, 150*time.Millisecond)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}
