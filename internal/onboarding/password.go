package onboarding

import (
	"context"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/tzf1003/xmesh/internal/control"
)

// SetIngressPassword accepts a write-only value from stdin or a masked desktop
// field and rotates it through the existing owner-only Control API.
func (s *Store) SetIngressPassword(ctx context.Context, id, password string) (*Job, error) {
	n := utf8.RuneCountInString(password)
	hasControl := false
	for _, r := range password {
		if r < 32 {
			hasControl = true
			break
		}
	}
	if n < 12 || n > 256 || strings.TrimSpace(password) != password || hasControl {
		return nil, problem("invalid_args", "登录口令需要 12–256 个字符，首尾不能是空白", "password")
	}
	j, e := s.LoadJob(id)
	if e != nil {
		return nil, e
	}
	unlock, e := s.lock(j.NodeName)
	if e != nil {
		return nil, e
	}
	defer unlock()
	j, e = s.LoadJob(id)
	if e != nil {
		return nil, e
	}
	if j.Purpose != "cloud" || !j.Readiness.ServiceReady {
		return nil, problem("not_ready", "请先等待云端入口服务就绪", "job")
	}
	p, e := s.LoadPlan(j.PlanID)
	if e != nil {
		return nil, e
	}
	ep, e := control.ReadEndpoint(filepath.Dir(p.ConfigPath))
	if e != nil {
		return nil, problem("service_unavailable", "设备服务暂不可连接", "job")
	}
	r, e := control.NewClient(ep).Do(ctx, "PUT", "/v2/ingress/password", map[string]string{"password": password}, nil)
	if e != nil {
		return nil, e
	}
	if e = r.Error(); e != nil {
		return nil, e
	}
	j.IngressPasswordSet = true
	j.NextAction = "在云端客户端添加连接并完成 OAuth 授权，然后执行验证提示词"
	if e = s.SaveJob(j); e != nil {
		return nil, e
	}
	return j, nil
}
