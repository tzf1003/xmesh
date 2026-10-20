package onboarding

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
)

func (s *Store) detailDiagnostics(ctx context.Context, nodes []NodeInfo) []Diagnostic {
	out := []Diagnostic{}
	for _, n := range nodes {
		cfg, e := config.Load(n.ConfigPath)
		if e != nil {
			continue
		}
		for name, pat := range cfg.Gateway.PATs {
			status := "ok"
			message := "本机访问凭据可用"
			next := ""
			file := pat.TokenFile
			if !filepath.IsAbs(file) {
				file = filepath.Join(filepath.Dir(n.ConfigPath), file)
			}
			if e := validateSetupToken(file); e != nil {
				status = "error"
				message = "本机访问凭据不可用"
				next = "检查令牌文件格式和私有权限，或在授权页轮换凭据"
			}
			out = append(out, Diagnostic{ID: n.Name + "/credential/" + name, Status: status, Message: message, NextAction: next})
		}
		if cfg.Mesh.Tailnet.Enabled && cfg.Mesh.Tailnet.AuthMethod != "browser" {
			if raw, e := os.ReadFile(cfg.Mesh.Tailnet.AuthkeyFile); e != nil || len(raw) > 4096 || strings.TrimSpace(string(raw)) == "" {
				out = append(out, Diagnostic{ID: n.Name + "/network-key", Status: "error", Message: "入网密钥文件不可用", NextAction: "检查私有密钥文件，或使用浏览器重新授权"})
			}
		}
		if ep, e := control.ReadEndpoint(filepath.Dir(n.ConfigPath)); e == nil {
			var status v2.StatusResponse
			if e = control.NewClient(ep).Fetch(ctx, "/v2/status", &status); e == nil {
				for _, warning := range status.Warnings {
					out = append(out, Diagnostic{ID: n.Name + "/" + string(warning.Code), Status: "attention", Message: "运行时需要处理：" + string(warning.Code), NextAction: "打开概览查看此警告的修复入口"})
				}
				if status.RestartRequired {
					out = append(out, Diagnostic{ID: n.Name + "/restart", Status: "attention", Message: "配置更改需要重启", NextAction: "保存工作后重启此设备服务"})
				}
			}
		}
	}
	for _, j := range s.jobs() {
		if j.Status == "failed" || j.Status == "waiting_user" {
			out = append(out, Diagnostic{ID: j.NodeName + "/setup/" + j.ID, Status: "attention", Message: "配置任务需要继续处理", NextAction: j.NextAction})
		}
	}
	return out
}
