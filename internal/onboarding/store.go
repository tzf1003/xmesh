package onboarding

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
	"github.com/tzf1003/xmesh/internal/privdir"
)

type Store struct{ Root string }

func Open(root string) (*Store, error) {
	if root == "" {
		base, e := os.UserConfigDir()
		if e != nil {
			return nil, e
		}
		root = filepath.Join(base, "xmesh")
	}
	abs, e := filepath.Abs(root)
	if e != nil {
		return nil, e
	}
	return &Store{Root: abs}, nil
}

func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func fileHash(path string) (string, error) {
	b, exists, e := readClientFile(path)
	if e != nil {
		return "", e
	}
	if !exists {
		return "absent", nil
	}
	return digest(b), nil
}
func newID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }

var idPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s *Store) transaction(kind, id string) (string, error) {
	if !idPattern.MatchString(id) {
		return "", problem("invalid_args", "任务标识无效", "id")
	}
	return filepath.Join(s.Root, "setup", kind, id+".json"), nil
}

func atomicWrite(path string, b []byte) error { return setupAtomicWrite(path, b, nil) }

// atomicWriteCAS performs the final expected-bytes check while the temporary
// file is ready, preventing compensation from overwriting a later edit.
func atomicWriteCAS(path, expected string, b []byte) error {
	lock, err := lockClientPath(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	return setupAtomicWrite(path, b, &expected)
}

func setupAtomicWrite(path string, b []byte, expected *string) error {
	if e := checkClientPath(path); e != nil {
		return e
	}
	if e := privdir.Ensure(filepath.Dir(path)); e != nil {
		return e
	}
	f, e := createRandomClientFile(filepath.Join(filepath.Dir(path), ".setup-"))
	if e != nil {
		return e
	}
	name := f.Name()
	defer os.Remove(name)
	if e = finishClientFile(f, b); e != nil {
		return e
	}
	if expected != nil {
		current, e := fileHash(path)
		if e != nil {
			return e
		}
		if current != *expected {
			return problem("plan_stale", "配置已变化，请重新预览", "plan")
		}
	}
	if e = renameClientFile(name, path); e != nil {
		return e
	}
	return syncClientDir(filepath.Dir(path))
}
func writeJSON(path string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return atomicWrite(path, append(b, '\n'))
}
func readJSON(path string, v any) error {
	raw, exists, e := readClientFile(path)
	if e != nil {
		return e
	}
	if !exists {
		return os.ErrNotExist
	}
	if len(raw) > 2<<20 {
		return errors.New("setup record exceeds the maximum size")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("invalid trailing JSON")
	}
	return nil
}

func (s *Store) Plan(in Input) (*Plan, error) {
	in, e := Normalize(in)
	if e != nil {
		return nil, e
	}
	dir := filepath.Join(s.Root, in.NodeName)
	file := filepath.Join(dir, "xmesh.yaml")
	before, e := fileHash(file)
	if e != nil {
		return nil, problem("config_unreadable", "已有配置无法读取", "node_name")
	}
	listen := ""
	if before != "absent" {
		cfg, e := config.Load(file)
		if e != nil {
			return nil, problem("config_invalid", "已有配置无效，请先修复或换一个设备名", "node_name")
		}
		listen = cfg.Gateway.Listen
		if _, port, e := net.SplitHostPort(listen); e == nil {
			listen = net.JoinHostPort("127.0.0.1", port)
		} else {
			listen = ""
		}
	}
	if listen == "" {
		listen, e = availableGateway()
		if e != nil {
			return nil, e
		}
	}
	text, e := configText(in, dir, listen)
	if e != nil {
		return nil, e
	}
	endpoint := "http://" + listen + "/mcp"
	changes, e := PlanClients(in.Clients, endpoint)
	if e != nil {
		return nil, e
	}
	p := &Plan{SchemaVersion: 1, ID: newID(), Input: in, ConfigPath: file, Config: text, BeforeHash: before, Endpoint: endpoint, ClientChanges: changes, CreatedAt: time.Now().UTC(), Summary: []string{"工作目录：" + in.Workspace, "权限：" + in.Permission, "用途：" + in.Purpose, "本机连接：" + endpoint}}
	if *in.Autostart {
		p.Summary = append(p.Summary, "服务将在登录后自动启动")
	} else {
		p.Summary = append(p.Summary, "服务按需手动启动")
	}
	if before != "absent" {
		p.Summary = append(p.Summary, "将备份现有配置；已有网络身份和凭据保留")
		p.Summary = append(p.Summary, "设备工具授权将替换为所选权限档位，已有自定义授权规则将写入配置备份")
		p.Summary = append(p.Summary, "应用期间会临时停止正在运行的设备服务，再以更新后的连接和权限设置启动")
	}
	if in.Purpose == "cloud" {
		p.Summary = append(p.Summary, "云端连接将启用 Tailscale Funnel 公网 HTTPS 入口及 OAuth 登录；需在控制面启用 Funnel / HTTPS 并设置入口登录口令")
		if in.Permission == "readonly" {
			p.Summary = append(p.Summary, "云端 OAuth 权限限制为设备发现、系统信息与文件读取")
		} else {
			p.Summary = append(p.Summary, "云端 OAuth 权限包含文件读写、命令执行、终端和后端工具")
		}
	}
	if in.Permission == "development" {
		p.Summary = append(p.Summary, "允许文件读写与命令执行；工作目录策略不等同于系统沙箱")
	}
	for _, id := range []string{"preflight", "write-config", "network", "install", "start", "clients", "verify"} {
		p.Steps = append(p.Steps, Step{ID: id, Status: "pending", Message: id})
	}
	path, _ := s.transaction("plans", p.ID)
	if e = writeJSON(path, p); e != nil {
		return nil, e
	}
	return p, nil
}
func (s *Store) LoadPlan(id string) (*Plan, error) {
	p := new(Plan)
	path, e := s.transaction("plans", id)
	if e != nil {
		return nil, e
	}
	if e = readJSON(path, p); e != nil {
		return nil, problem("not_found", "配置计划不存在或已损坏", "plan")
	}
	if p.ID != id || p.SchemaVersion != 1 || !nodeName.MatchString(p.Input.NodeName) || p.ConfigPath != filepath.Join(s.Root, p.Input.NodeName, "xmesh.yaml") {
		return nil, problem("invalid_plan", "配置计划版本无效", "plan")
	}
	return p, nil
}

// jobRecord includes recovery metadata; public JSON deliberately excludes receipts and backups.
type jobRecord struct {
	Job
	Receipts              []ClientReceipt  `json:"client_receipts"`
	ConfigHash            string           `json:"config_hash"`
	ConfigBackup          string           `json:"config_backup"`
	PendingHash           string           `json:"pending_config_hash"`
	PendingRestore        bool             `json:"pending_restore"`
	ServiceOwned          bool             `json:"service_owned"`
	ServiceSnapshot       *ServiceSnapshot `json:"service_snapshot"`
	ServiceStopped        bool             `json:"service_stopped"`
	ServiceRestorePending bool             `json:"service_restore_pending"`
}

func (s *Store) SaveJob(j *Job) error {
	j.UpdatedAt = time.Now().UTC()
	path, e := s.transaction("jobs", j.ID)
	if e != nil {
		return e
	}
	return writeJSON(path, jobRecord{Job: *j, Receipts: j.Receipts, ConfigHash: j.ConfigHash, ConfigBackup: j.ConfigBackup, PendingHash: j.PendingConfigHash, PendingRestore: j.PendingRestore, ServiceOwned: j.ServiceOwned, ServiceSnapshot: j.ServiceSnapshot, ServiceStopped: j.ServiceStopped, ServiceRestorePending: j.ServiceRestorePending})
}
func (s *Store) LoadJob(id string) (*Job, error) {
	path, e := s.transaction("jobs", id)
	if e != nil {
		return nil, e
	}
	var r jobRecord
	if e = readJSON(path, &r); e != nil {
		return nil, problem("not_found", "配置任务不存在或已损坏", "job")
	}
	j := r.Job
	if j.ID != id || j.SchemaVersion != SchemaVersion || !idPattern.MatchString(j.PlanID) || !nodeName.MatchString(j.NodeName) {
		return nil, problem("invalid_job", "配置任务记录无效，请重新生成配置计划", "job")
	}
	j.Receipts = r.Receipts
	j.ConfigHash = r.ConfigHash
	j.ConfigBackup = r.ConfigBackup
	j.PendingConfigHash = r.PendingHash
	j.PendingRestore = r.PendingRestore
	j.ServiceOwned = r.ServiceOwned
	j.ServiceSnapshot = r.ServiceSnapshot
	j.ServiceStopped = r.ServiceStopped
	j.ServiceRestorePending = r.ServiceRestorePending
	return &j, nil
}

func (s *Store) CreateJob(planID string) (*Job, error) {
	p, e := s.LoadPlan(planID)
	if e != nil {
		return nil, e
	}
	unlock, e := s.lock(p.Input.NodeName)
	if e != nil {
		return nil, e
	}
	defer unlock()
	for _, old := range s.jobs() {
		if old.NodeName == p.Input.NodeName && (old.Status == "pending" || old.Status == "running" || old.Status == "waiting_user") {
			if old.PlanID == planID {
				return &old, nil
			}
			return nil, problem("conflict", "该设备已有配置任务，请继续或取消原任务", "node_name")
		}
	}
	current, e := fileHash(p.ConfigPath)
	if e != nil || current != p.BeforeHash {
		return nil, problem("plan_stale", "配置已变化，请重新预览", "plan")
	}
	j := &Job{SchemaVersion: 1, ID: newID(), PlanID: p.ID, NodeName: p.Input.NodeName, Purpose: p.Input.Purpose, ControlURL: p.Input.Network.ControlURL, Status: "pending", Steps: append([]Step{}, p.Steps...), CreatedAt: time.Now().UTC()}
	j.VerificationTool = "local/fs.stat"
	if j.Purpose == "cloud" {
		j.VerificationTool = j.NodeName + "/fs.stat"
	}
	j.VerificationPath = filepath.Join(p.Input.Workspace, ".xmesh-connection-check-"+j.ID)
	if e = s.SaveJob(j); e != nil {
		return nil, e
	}
	return j, nil
}
func (s *Store) jobs() []Job {
	out := []Job{}
	paths, _ := filepath.Glob(filepath.Join(s.Root, "setup", "jobs", "*.json"))
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".json")
		if j, e := s.LoadJob(id); e == nil {
			out = append(out, *j)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

func (s *Store) Inspect(ctx context.Context) Inspection {
	host, _ := os.Hostname()
	suggest := regexp.MustCompile(`[^a-z0-9-]+`).ReplaceAllString(strings.ToLower(host), "-")
	suggest = strings.Trim(suggest, "-")
	if len(suggest) > 63 {
		suggest = suggest[:63]
	}
	if suggest == "" {
		suggest = "my-device"
	}
	out := Inspection{SchemaVersion: 1, Platform: runtime.GOOS, Arch: runtime.GOARCH, Hostname: host, SuggestedNodeName: suggest, Root: s.Root, Clients: InspectClients(), Nodes: []NodeInfo{}, Jobs: s.jobs()}
	entries, _ := os.ReadDir(s.Root)
	for _, ent := range entries {
		if !ent.IsDir() || !nodeName.MatchString(ent.Name()) {
			continue
		}
		path := filepath.Join(s.Root, ent.Name(), "xmesh.yaml")
		if _, e := os.Stat(path); e != nil {
			continue
		}
		n := NodeInfo{Name: ent.Name(), ConfigPath: path}
		if _, e := config.Load(path); e != nil {
			n.Problem = "config_invalid"
		}
		if ep, e := control.ReadEndpoint(filepath.Dir(path)); e == nil {
			var status v2.StatusResponse
			c, stop := context.WithTimeout(ctx, time.Second)
			n.Running = control.NewClient(ep).Fetch(c, "/v2/status", &status) == nil
			stop()
		}
		out.Nodes = append(out.Nodes, n)
	}
	return out
}

func (s *Store) Doctor(ctx context.Context) Diagnostics {
	d := Diagnostics{SchemaVersion: 1, Checks: []Diagnostic{}}
	inspection := s.Inspect(ctx)
	for _, n := range inspection.Nodes {
		status := "ok"
		msg := "设备服务可连接"
		next := ""
		if n.Problem != "" {
			status = "error"
			msg = "设备配置无法读取"
			next = "在设置页修复配置或从备份恢复"
		} else if !n.Running {
			status = "attention"
			msg = "设备服务未运行"
			next = "启动设备，或继续未完成的配置任务"
		}
		d.Checks = append(d.Checks, Diagnostic{ID: n.Name, Status: status, Message: msg, NextAction: next})
	}
	if len(d.Checks) == 0 {
		d.Checks = append(d.Checks, Diagnostic{ID: "setup", Status: "attention", Message: "尚未配置设备", NextAction: "打开桌面首次配置向导，或运行 xmesh setup plan"})
	}
	d.Checks = append(d.Checks, s.detailDiagnostics(ctx, inspection.Nodes)...)
	return d
}
