package onboarding

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/privdir"
)

// Host is the platform boundary. Tests run the same transaction engine against
// deterministic services; desktop and agent processes use the real host.
type Host struct {
	Snapshot  func(context.Context, *Plan) (ServiceSnapshot, error)
	Restore   func(context.Context, *Plan, ServiceSnapshot) error
	Configure func(context.Context, *Plan) error
	Stop      func(context.Context, *Plan) error
	Installed func(*Plan) (bool, error)
	Install   func(context.Context, *Plan) error
	Start     func(context.Context, *Plan) error
	Remove    func(context.Context, *Plan) error
	Network   func(context.Context, *Plan, func(mesh.LoginProgress) error) (string, error)
	Health    func(context.Context, *Plan) error
	Evidence  func(context.Context, *Plan, *Job) (bool, error)
}

func (s *Store) Run(ctx context.Context, id string, h Host) error {
	j, e := s.LoadJob(id)
	if e != nil {
		return e
	}
	unlock, e := s.lock(j.NodeName)
	if e != nil {
		return e
	}
	defer unlock()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if s.cancelRequested(id) {
					cancel()
					return
				}
			}
		}
	}()
	// Re-read after locking: another process may have finished while we waited.
	j, e = s.LoadJob(id)
	if e != nil {
		return e
	}
	if j.Status == "cancelled" {
		return nil
	}
	if s.cancelRequested(id) {
		return s.finishCancellation(j)
	}
	p, e := s.LoadPlan(j.PlanID)
	if e != nil {
		return e
	}
	if e = s.reconcileConfig(p, j); e != nil {
		markStaleJob(j)
		return s.SaveJob(j)
	}
	if j.ServiceRestorePending {
		if e = s.restoreService(ctx, p, j, h); e != nil {
			j.Status = "failed"
			j.NextAction = "服务状态恢复待处理，请使用 doctor 检查后重试"
			return s.SaveJob(j)
		}
	}
	j.Status = "running"
	j.NextAction = ""
	if e = s.SaveJob(j); e != nil {
		return e
	}
	for i := range j.Steps {
		if s.cancelRequested(id) {
			return s.cancelRun(ctx, p, j, h)
		}
		step := &j.Steps[i]
		if e = s.reconcileConfig(p, j); e != nil {
			markStaleJob(j)
			return s.SaveJob(j)
		}
		if step.Status == "succeeded" && step.ID != "start" && step.ID != "verify" {
			continue
		}
		step.Status = "running"
		step.Code = ""
		step.NextAction = ""
		j.Status = "running"
		if e = s.SaveJob(j); e != nil {
			return e
		}
		switch step.ID {
		case "preflight":
			e = s.preflight(p)
			if e == nil && j.ServiceSnapshot == nil && h.Snapshot != nil {
				var snapshot ServiceSnapshot
				snapshot, e = h.Snapshot(ctx, p)
				if e == nil {
					j.ServiceSnapshot = &snapshot
					e = s.SaveJob(j)
				}
			}
		case "write-config":
			if j.ServiceSnapshot != nil && j.ServiceSnapshot.Running && h.Stop != nil && h.Snapshot != nil {
				var current ServiceSnapshot
				current, e = h.Snapshot(ctx, p)
				if e == nil {
					j.ServiceStopped = true
					e = s.SaveJob(j)
				}
				if e == nil && current.Running {
					e = h.Stop(ctx, p)
				}
			}
			if e == nil {
				e = s.writeConfig(p, j)
			}
		case "network":
			if p.Input.Network.Mode != "lan" {
				var dns string
				dns, e = h.Network(ctx, p, func(progress mesh.LoginProgress) error {
					j.AuthURL = progress.URL
					if progress.State == "needs_login" {
						j.Status = "waiting_user"
						j.NextAction = "在浏览器中授权此设备"
					} else if progress.State == "needs_approval" {
						j.Status = "waiting_user"
						j.NextAction = "请管理员批准设备，并授予需要的设备标签"
					} else {
						j.Status = "running"
						j.NextAction = ""
					}
					return s.SaveJob(j)
				})
				j.AuthURL = ""
				if e == nil && p.Input.Purpose == "cloud" {
					e = s.prepareIngress(p, j, dns)
				}
			}
		case "install":
			var have bool
			have, e = h.Installed(p)
			if e == nil && !have {
				j.ServiceOwned = true
				if e = s.SaveJob(j); e != nil {
					return e
				}
				e = h.Install(ctx, p)
				if e == nil {
					j.Readiness.Installed = true
					step.Message = "用户级服务已安装"
				}
			}
			if have {
				if h.Configure != nil {
					e = h.Configure(ctx, p)
				}
				j.Readiness.Installed = true
				step.Message = "已复用现有服务"
			}
		case "start":
			if e = h.Health(ctx, p); e != nil {
				e = h.Start(ctx, p)
			}
			if e == nil {
				e = h.Health(ctx, p)
			}
			j.Readiness.ServiceReady = e == nil
		case "clients":
			e = s.applyClients(p, j)
		case "verify":
			if s.cancelRequested(id) {
				return s.cancelRun(ctx, p, j, h)
			}
			if p.Input.Purpose == "cloud" && !j.IngressPasswordSet {
				step.Status = "waiting_user"
				step.Message = "请设置云端连接的登录口令"
				step.Code = "ingress_password_pending"
				step.NextAction = "在此向导设置登录口令，然后在云端客户端添加连接并完成 OAuth 授权"
				j.Status = "waiting_user"
				j.NextAction = step.NextAction
				return s.SaveJob(j)
			}
			var verified bool
			verified, e = h.Evidence(ctx, p, j)
			if s.cancelRequested(id) {
				return s.cancelRun(ctx, p, j, h)
			}
			if e == nil && !verified {
				step.Status = "waiting_user"
				step.Message = "设备已就绪，客户端待验证"
				step.Code = "client_pending"
				step.NextAction = fmt.Sprintf("在已连接的客户端调用 %s，参数 path 为 %s，然后点击验证", j.VerificationTool, j.VerificationPath)
				j.Status = "waiting_user"
				j.NextAction = step.NextAction
				j.Readiness.ClientVerified = false
				return s.SaveJob(j)
			}
			j.Readiness.ClientVerified = verified
		}
		if s.cancelRequested(id) {
			return s.cancelRun(ctx, p, j, h)
		}
		if e != nil {
			step.Status = "failed"
			step.Code = "setup_" + strings.ReplaceAll(step.ID, "-", "_") + "_failed"
			step.Message = stepFailure(step.ID)
			step.NextAction = stepRecovery(step.ID)
			// Recovery writes its journal before restoring files and services.
			// Publish a terminal result only after those actions have settled.
			j.Status = "running"
			j.NextAction = "正在恢复本次配置变更"
			j.Readiness.ServiceReady = false
			j.Readiness.ClientVerified = false
			// Restore client files before returning a native service to its saved
			// baseline. Network identity files remain available across retries.
			if step.ID == "clients" || j.ServiceSnapshot != nil && (j.ServiceStopped || j.ConfigHash != "" || j.PendingConfigHash != "") {
				remaining := []ClientReceipt{}
				for k := len(j.Receipts) - 1; k >= 0; k-- {
					if err := RollbackClient(j.Receipts[k]); err != nil {
						step.NextAction = "客户端配置已变化，请查看备份并手动恢复"
						remaining = append(remaining, j.Receipts[k])
					}
				}
				j.Receipts = remaining
			}
			if j.ServiceSnapshot != nil && h.Restore != nil && (j.ServiceStopped || j.ConfigHash != "" || j.PendingConfigHash != "") {
				j.ServiceRestorePending = true
				if err := s.SaveJob(j); err != nil {
					return err
				}
				if err := s.restoreService(ctx, p, j, h); err != nil {
					step.NextAction = "服务状态恢复待处理，请使用 doctor 检查后重试"
				}
				j.Readiness.ServiceReady = false
				j.Readiness.ClientVerified = false
			} else if step.ID == "install" || step.ID == "start" {
				rollbackOK := true
				if j.ServiceOwned {
					if have, err := h.Installed(p); err != nil {
						rollbackOK = false
					} else if have {
						if err = h.Remove(ctx, p); err != nil {
							rollbackOK = false
						}
					}
					if rollbackOK {
						j.ServiceOwned = false
						j.Readiness.Installed = false
						for k := range j.Steps {
							if j.Steps[k].ID == "install" {
								j.Steps[k].Status = "pending"
							}
						}
					}
				}
				if !rollbackOK {
					step.NextAction = "本次服务尚未回收，请使用 doctor 检查后重试"
				} else if err := s.restoreConfig(p, j); err != nil {
					step.NextAction = "配置出现后续修改，请从备份核对恢复"
				}
				j.Readiness.ServiceReady = false
			}
			if len(j.Receipts) > 0 {
				step.NextAction = "客户端配置已有后续修改，请核对备份并手动恢复"
			}
			j.Status = "failed"
			j.NextAction = step.NextAction
			_ = s.SaveJob(j)
			return nil // The durable result, rather than process output, owns the failure.
		}
		step.Status = "succeeded"
		if step.Message == step.ID {
			step.Message = stepSuccess(step.ID)
		}
		if e = s.SaveJob(j); e != nil {
			return e
		}
	}
	if s.cancelRequested(id) {
		return s.cancelRun(ctx, p, j, h)
	}
	j.Status = "succeeded"
	j.NextAction = ""
	return s.SaveJob(j)
}

func stepSuccess(id string) string {
	return map[string]string{"preflight": "环境检查通过", "write-config": "配置已保存", "network": "设备网络已就绪", "install": "服务已安装", "start": "MCP 服务可连接", "clients": "客户端配置已保存", "verify": "客户端实际调用已验证"}[id]
}
func stepFailure(id string) string {
	return map[string]string{"preflight": "环境检查未通过，尚不能应用配置", "write-config": "无法保存配置，设备设置未完成", "network": "入网未完成，跨网络功能待处理", "install": "服务注册失败，设备尚未就绪", "start": "服务暂不可用，连接功能待处理", "clients": "客户端配置未完成", "verify": "连接验证暂不可用"}[id]
}
func stepRecovery(id string) string {
	return map[string]string{"preflight": "检查工作目录、凭据文件和端口；配置变化时重新预览", "write-config": "检查目录权限和可用空间后重试", "network": "完成浏览器授权、设备审批、tag:device / tag:ingress 与 Funnel 配置后重试", "install": "检查系统用户服务权限后重试；已有服务可通过 doctor 检查", "start": "使用 doctor 检查服务、端口和网络；修复后重试", "clients": "检查客户端配置及文件权限；发生并发修改时重新预览", "verify": "确认客户端已重新加载配置、设备在线，然后重试"}[id]
}

func (s *Store) preflight(p *Plan) error {
	if _, e := Normalize(p.Input); e != nil {
		return e
	}
	if p.Input.Network.AuthMethod == "key" {
		raw, e := os.ReadFile(p.Input.Network.AuthkeyFile)
		if e != nil || len(raw) > 4096 || strings.TrimSpace(string(raw)) == "" {
			return problem("credential_unavailable", "授权密钥文件无法读取", "network.authkey_file")
		}
	}
	if p.BeforeHash == "absent" {
		current, err := fileHash(p.ConfigPath)
		if err != nil {
			return err
		}
		if current != "absent" {
			return nil // reconcileConfig already authenticated the resumed file.
		}
		u := strings.TrimSuffix(strings.TrimPrefix(p.Endpoint, "http://"), "/mcp")
		if connectedGateway(u) {
			return problem("port_unavailable", "预选端口已被占用，请重新预览", "gateway")
		}
		l, e := net.Listen("tcp", u)
		if e != nil {
			return problem("port_unavailable", "预选端口已被占用，请重新预览", "gateway")
		}
		_ = l.Close()
	}
	return nil
}

func privateNew(path string, raw []byte) error {
	if e := checkClientPath(path); e != nil {
		return e
	}
	if e := privdir.Ensure(filepath.Dir(path)); e != nil {
		return e
	}
	f, e := createPrivateClientFile(path)
	if e != nil {
		if os.IsExist(e) {
			if _, exists, err := readClientFile(path); err != nil || !exists {
				return errors.New("existing setup file is not a regular file")
			}
			if err := verifyPrivateClientFile(path); err != nil {
				return err
			}
		}
		return e
	}
	if e = finishClientFile(f, raw); e != nil {
		_ = os.Remove(path)
		return e
	}
	return syncClientDir(filepath.Dir(path))
}

func (s *Store) writeConfig(p *Plan, j *Job) error {
	hash, e := fileHash(p.ConfigPath)
	if e != nil {
		return e
	}
	if j.ConfigHash != "" && hash == j.ConfigHash {
		return nil
	}
	if hash != p.BeforeHash {
		return problem("plan_stale", "配置已变化，请重新预览", "plan")
	}
	dir := filepath.Dir(p.ConfigPath)
	deny, _ := json.MarshalIndent(DefaultDeny, "", "  ")
	if e = privateNew(filepath.Join(dir, "cwd-deny.yaml"), append(deny, '\n')); e != nil && !os.IsExist(e) {
		return e
	}
	if e = validateSetupDeny(filepath.Join(dir, "cwd-deny.yaml")); e != nil {
		return e
	}
	var secret [32]byte
	if _, e = rand.Read(secret[:]); e != nil {
		return e
	}
	if e = privateNew(filepath.Join(dir, "pats", "onboarding.token"), []byte(hex.EncodeToString(secret[:])+"\n")); e != nil && !os.IsExist(e) {
		return e
	}
	if e = validateSetupToken(filepath.Join(dir, "pats", "onboarding.token")); e != nil {
		return e
	}
	if p.BeforeHash != "absent" && j.ConfigBackup == "" {
		backup := filepath.Join(s.Root, "setup", "backups", j.ID, "xmesh.yaml")
		raw, e := os.ReadFile(p.ConfigPath)
		if e != nil {
			return e
		}
		if digest(raw) != p.BeforeHash {
			return problem("plan_stale", "配置已变化，请重新预览", "plan")
		}
		if e = privateNew(backup, raw); e != nil {
			if os.IsExist(e) {
				if saved, err := os.ReadFile(backup); err != nil || digest(saved) != p.BeforeHash {
					return e
				}
			} else {
				return e
			}
		}
		j.ConfigBackup = backup
	}
	j.PendingConfigHash = digest([]byte(p.Config))
	j.PendingRestore = false
	if e = s.SaveJob(j); e != nil {
		return e
	}
	if e = atomicWriteCAS(p.ConfigPath, hash, []byte(p.Config)); e != nil {
		return e
	}
	j.ConfigHash = j.PendingConfigHash
	j.PendingConfigHash = ""
	return s.SaveJob(j)
}

func (s *Store) prepareIngress(p *Plan, j *Job, dns string) error {
	if !strings.HasSuffix(dns, ".ts.net") || strings.ContainsAny(dns, "/:?#@ ") {
		return errors.New("invalid Funnel domain")
	}
	raw, e := os.ReadFile(p.ConfigPath)
	if e != nil {
		return e
	}
	if digest(raw) != j.ConfigHash {
		return problem("plan_stale", "配置已变化，请重新预览", "plan")
	}
	issuer := "https://" + dns
	roles := []string{"agent", "gateway", "ingress"}
	next, e := control.ApplyEdits(raw, []control.Edit{
		{Path: "/node/roles", Value: roles},
		{Path: "/ingress/listen", Value: "funnel"},
		{Path: "/ingress/oauth/issuer", Value: issuer},
		{Path: "/ingress/oauth/dynamic_client_scopes", Value: scopes(p.Input.Permission, p.Input.NodeName)},
		{Path: "/ingress/domain", Delete: true},
		{Path: "/ingress/tls", Delete: true},
		{Path: "/ingress/cert_file", Delete: true},
		{Path: "/ingress/key_file", Delete: true},
	})
	if e != nil {
		return e
	}
	if _, e = config.Parse(next); e != nil {
		return e
	}
	var key [24]byte
	if _, e = rand.Read(key[:]); e != nil {
		return e
	}
	if e = privateNew(filepath.Join(filepath.Dir(p.ConfigPath), "oauth-password"), []byte(hex.EncodeToString(key[:])+"\n")); e != nil && !os.IsExist(e) {
		return e
	}
	j.PendingConfigHash = digest(next)
	j.PendingRestore = false
	j.IngressURL = issuer + "/mcp"
	if e = s.SaveJob(j); e != nil {
		return e
	}
	if e = atomicWriteCAS(p.ConfigPath, digest(raw), next); e != nil {
		return e
	}
	j.ConfigHash = j.PendingConfigHash
	j.PendingConfigHash = ""
	return s.SaveJob(j)
}

func (s *Store) applyClients(p *Plan, j *Job) error {
	raw, e := os.ReadFile(filepath.Join(filepath.Dir(p.ConfigPath), "pats", "onboarding.token"))
	if e != nil {
		return e
	}
	token := strings.TrimSpace(string(raw))
	for _, change := range p.ClientChanges {
		if slices.ContainsFunc(j.Receipts, func(r ClientReceipt) bool { return r.ID == change.ID && VerifyClient(r, p.Endpoint) == nil }) {
			continue
		}
		r, e := ApplyClientPrepared(change, p.Endpoint, token, func(receipt ClientReceipt) error {
			filtered := []ClientReceipt{}
			for _, old := range j.Receipts {
				if old.ID != receipt.ID {
					filtered = append(filtered, old)
				}
			}
			j.Receipts = append(filtered, receipt)
			return s.SaveJob(j)
		})
		if e != nil {
			return e
		}
		_ = r
		if e = s.SaveJob(j); e != nil {
			return e
		}
	}
	return nil
}

func (s *Store) restoreConfig(p *Plan, j *Job) error {
	current, e := fileHash(p.ConfigPath)
	if e != nil {
		return e
	}
	if current == p.BeforeHash && j.ConfigHash == "" {
		resetConfigSteps(j)
		return s.SaveJob(j)
	}
	if current != j.ConfigHash {
		return errors.New("configuration changed after setup")
	}
	var original []byte
	if p.BeforeHash != "absent" {
		raw, err := os.ReadFile(j.ConfigBackup)
		if err != nil {
			return err
		}
		if digest(raw) != p.BeforeHash {
			return errors.New("backup changed")
		}
		original = raw
	}
	j.PendingConfigHash = p.BeforeHash
	j.PendingRestore = true
	if e = s.SaveJob(j); e != nil {
		return e
	}
	if p.BeforeHash == "absent" {
		lock, err := lockClientPath(p.ConfigPath)
		if err != nil {
			return err
		}
		defer lock.Close()
		if latest, err := fileHash(p.ConfigPath); err != nil || latest != current {
			return errors.New("configuration changed during recovery")
		}
		e = os.Remove(p.ConfigPath)
		if e == nil {
			e = syncClientDir(filepath.Dir(p.ConfigPath))
		}
	} else {
		e = atomicWriteCAS(p.ConfigPath, current, original)
	}
	if e == nil {
		resetConfigSteps(j)
		return s.SaveJob(j)
	}
	return e
}

// Verify refreshes live service health and client evidence.
func (s *Store) Verify(ctx context.Context, id string, h Host) (*Job, error) {
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
	p, e := s.LoadPlan(j.PlanID)
	if e != nil {
		return nil, e
	}
	if s.cancelRequested(id) {
		return j, s.cancelRun(ctx, p, j, h)
	}
	if !verificationStepsReady(j) {
		return nil, problem("not_ready", "请先完成设备配置步骤后再验证客户端", "job")
	}
	if e = s.reconcileConfig(p, j); e != nil || j.ConfigHash == "" {
		markStaleJob(j)
		return j, s.SaveJob(j)
	}
	if e = h.Health(ctx, p); e != nil {
		j.Readiness.ServiceReady = false
		j.Readiness.ClientVerified = false
		j.Status = "waiting_user"
		j.NextAction = "设备服务无法连接，请启动服务后重试"
	} else {
		j.Readiness.ServiceReady = true
		if p.Input.Purpose == "cloud" && !j.IngressPasswordSet {
			if s.cancelRequested(id) {
				return j, s.cancelRun(ctx, p, j, h)
			}
			j.Status = "waiting_user"
			j.NextAction = "请先设置云端连接的登录口令"
			if e = s.SaveJob(j); e != nil {
				return nil, e
			}
			return j, nil
		}
		verified, err := h.Evidence(ctx, p, j)
		if s.cancelRequested(id) {
			return j, s.cancelRun(ctx, p, j, h)
		}
		if err != nil {
			return nil, err
		}
		j.Readiness.ClientVerified = verified
		if verified && j.Readiness.Installed {
			j.Status = "succeeded"
			j.NextAction = ""
			for i := range j.Steps {
				if j.Steps[i].ID == "verify" {
					j.Steps[i] = Step{ID: "verify", Status: "succeeded", Message: stepSuccess("verify")}
				}
			}
		} else {
			j.Status = "waiting_user"
			j.NextAction = fmt.Sprintf("请在客户端调用 %s，path=%s 后重试", j.VerificationTool, j.VerificationPath)
		}
	}
	if s.cancelRequested(id) {
		return j, s.cancelRun(ctx, p, j, h)
	}
	if e = s.SaveJob(j); e != nil {
		return nil, e
	}
	return j, nil
}

// Cancel retains external identity state and completed backups for an explicit retry.
func (s *Store) Cancel(id string) (*Job, error) {
	j, e := s.LoadJob(id)
	if e != nil {
		return nil, e
	}
	request, e := s.transaction("cancellations", id)
	if e != nil {
		return nil, e
	}
	if e = atomicWrite(request, []byte("{}\n")); e != nil {
		return nil, e
	}
	unlock, e := s.lock(j.NodeName)
	if e != nil {
		var issue *Error
		if errors.As(e, &issue) && issue.Code == "busy" {
			j.NextAction = "正在取消配置任务，身份和备份将保留"
			return j, nil
		}
		return nil, e
	}
	defer unlock()
	j, e = s.LoadJob(id)
	if e != nil {
		return nil, e
	}
	if e = s.finishCancellation(j); e != nil {
		return nil, e
	}
	return j, nil
}

func (s *Store) cancelRequested(id string) bool {
	path, err := s.transaction("cancellations", id)
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func (s *Store) finishCancellation(j *Job) error {
	j.Status = "cancelled"
	j.AuthURL = ""
	j.NextAction = "配置已取消；登录身份和备份已保留，可在网络控制面撤销设备"
	j.Readiness.ClientVerified = false
	return s.SaveJob(j)
}

func (s *Store) cancelRun(ctx context.Context, p *Plan, j *Job, h Host) error {
	j.Status = "running"
	j.AuthURL = ""
	j.NextAction = "正在取消并恢复本次配置变更"
	if err := s.SaveJob(j); err != nil {
		return err
	}
	var recoveryErr error
	remaining := []ClientReceipt{}
	for i := len(j.Receipts) - 1; i >= 0; i-- {
		if err := RollbackClient(j.Receipts[i]); err != nil {
			recoveryErr = err
			remaining = append(remaining, j.Receipts[i])
		}
	}
	j.Receipts = remaining
	if j.ServiceSnapshot != nil && h.Restore != nil && (j.ServiceStopped || j.ServiceOwned || j.ConfigHash != "" || j.PendingConfigHash != "") {
		j.ServiceRestorePending = true
		if err := s.SaveJob(j); err != nil {
			return err
		}
		if err := s.restoreService(ctx, p, j, h); err != nil {
			recoveryErr = err
		}
	}
	if recoveryErr != nil {
		j.Status = "failed"
		j.NextAction = "取消后的恢复待处理，请检查服务和备份后重试"
		request, _ := s.transaction("cancellations", j.ID)
		if err := os.Remove(request); err != nil && !os.IsNotExist(err) {
			return err
		}
		return s.SaveJob(j)
	}
	return s.finishCancellation(j)
}

// QueueResume publishes a pollable state before a detached worker is launched.
// An active authorization worker already owns progress and needs no duplicate.
func (s *Store) QueueResume(id string) (*Job, bool, error) {
	j, err := s.LoadJob(id)
	if err != nil {
		return nil, false, err
	}
	if j.Status == "cancelled" || s.cancelRequested(id) {
		return nil, false, problem("cancelled", "任务已取消，请重新生成配置计划", "job")
	}
	unlock, err := s.lock(j.NodeName)
	if err != nil {
		var issue *Error
		if errors.As(err, &issue) && issue.Code == "busy" {
			return j, false, nil
		}
		return nil, false, err
	}
	defer unlock()
	j, err = s.LoadJob(id)
	if err != nil {
		return nil, false, err
	}
	if s.cancelRequested(id) || j.Status == "cancelled" {
		return nil, false, problem("cancelled", "任务已取消，请重新生成配置计划", "job")
	}
	j.Status = "pending"
	j.NextAction = "准备继续配置任务"
	return j, true, s.SaveJob(j)
}
