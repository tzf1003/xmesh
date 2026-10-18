package control

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/ci"
	"github.com/tzf1003/xmesh/internal/config"
)

// --- GitHub Actions runner hosting (CTR-CTL-15) ---

// CIAdmin is the runner supervisor as the control channel sees it. *ci.Manager implements it.
type CIAdmin interface {
	Summary() *v1.CiSummary
	Overview() v1.CiOverview
	Binding(id string) (v1.CiBinding, bool)
	SpecView(spec config.CIBinding, removing bool) v1.CiBinding
	Gone(id string) bool
	Inspect(dir string) (ci.Inspection, error)
	HoldToken(id, token string)
	ForgetForce(id string)
	Restart(id string) error
	Repair(id, token string) error
}

// CIVisibility says whether a repository is public. Tests replace it; the default asks GitHub.
type CIVisibility func(ctx context.Context, host, repo string) (public bool, err error)

var (
	ciTargetPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)
	ciIDPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)
	ciLabelPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	ciSlugStrip     = regexp.MustCompile(`[^a-z0-9]+`)
)

const ciVisibilityTimeout = 20 * time.Second

func (s *Server) ciAdmin(w http.ResponseWriter) CIAdmin {
	if s.o.CI == nil {
		failWith(w, newError(v1.ErrUnavailable, "CI 运行器托管尚未就绪"))
		return nil
	}
	return s.o.CI
}

func reasoned(e *v1.Error, reason string) *v1.Error {
	e.Reason = reason
	return e
}

func (s *Server) getCI(w http.ResponseWriter, _ *http.Request, _ *call) {
	admin := s.ciAdmin(w)
	if admin == nil {
		return
	}
	writeJSON(w, 200, admin.Overview())
}

// ciAccountsUnavailable answers the account routes until GitHub accounts are available.
func (s *Server) ciAccountsUnavailable(w http.ResponseWriter, _ *http.Request, _ *call) {
	failWith(w, reasoned(newError(v1.ErrUnavailable, "此内核还不能保存 GitHub 账号"), "accounts_unavailable"))
}

// ciBindingsOf is the ci.bindings list of the file as maps, in file order.
func ciBindingsOf(raw []byte) []map[string]any {
	section, _ := parsedOf(raw)["ci"].(map[string]any)
	list, _ := section["bindings"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func ciString(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// ciSlug is the default binding id for a repository: owner-name, lower case.
func ciSlug(target string) string {
	slug := strings.Trim(ciSlugStrip.ReplaceAllString(strings.ToLower(target), "-"), "-")
	if len(slug) > 36 {
		slug = strings.Trim(slug[:36], "-")
	}
	if slug == "" {
		slug = "repo"
	}
	return slug
}

func (s *Server) ciVisible(ctx context.Context, host, repo string) (bool, error) {
	if s.o.CIVisibility != nil {
		return s.o.CIVisibility(ctx, host, repo)
	}
	return ci.RepoIsPublic(ctx, nil, host, repo)
}

func validateCiLabels(labels []string) *v1.Error {
	if len(labels) > 16 {
		return argError("labels", "标签最多 16 个")
	}
	seen := map[string]bool{}
	for _, l := range labels {
		if !ciLabelPattern.MatchString(l) {
			return argError("labels", fmt.Sprintf("标签 %q 无效：只能含字母、数字、点、下划线和连字符", l))
		}
		if seen[l] {
			return argError("labels", fmt.Sprintf("标签 %q 重复", l))
		}
		seen[l] = true
	}
	return nil
}

func (s *Server) createCIBinding(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.ciAdmin(w)
	if admin == nil {
		return
	}
	var req v1.CiBindingCreate
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	ways := 0
	if req.ImportDir != "" {
		ways++
	}
	if req.Target != "" && req.Account != "" {
		ways++
	}
	if req.Target != "" && req.RegistrationToken != "" {
		ways++
	}
	if ways != 1 || (req.Account != "" && req.RegistrationToken != "") || (req.ImportDir != "" && (req.Target != "" || req.Account != "" || req.RegistrationToken != "")) {
		failWith(w, argError("target", "target 加 account、target 加 registration_token、import_dir 三种方式恰好用一种"))
		return
	}
	if req.ID != "" && !ciIDPattern.MatchString(req.ID) {
		failWith(w, argError("id", "id 只能含小写字母、数字和连字符，最长 40"))
		return
	}
	if req.Concurrency < 0 || req.Concurrency > 16 {
		failWith(w, argError("concurrency", "并发数在 1 到 16 之间"))
		return
	}
	if e := validateCiLabels(req.Labels); e != nil {
		failWith(w, e)
		return
	}

	host, target, source := "github.com", req.Target, "token"
	var imported ci.Inspection
	switch {
	case req.ImportDir != "":
		var err error
		if imported, err = admin.Inspect(req.ImportDir); err != nil {
			failWith(w, ciInspectError(err, "import_dir"))
			return
		}
		if e := ciImportable(imported); e != nil {
			failWith(w, e)
			return
		}
		host, target, source = imported.Host, imported.Target, "import"
	case req.Account != "":
		source = "account"
	}
	if !ciTargetPattern.MatchString(target) {
		failWith(w, argError("target", "target 应为 owner/name"))
		return
	}
	if source == "account" {
		// Accounts arrive with the GitHub login; until then no account exists.
		failWith(w, reasoned(newError(v1.ErrNotFound, "没有这个账号"), "account_not_found"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), ciVisibilityTimeout)
	public, err := s.ciVisible(ctx, host, target)
	cancel()
	if err != nil {
		failWith(w, reasoned(newError(v1.ErrUnavailable, "连不上 GitHub，无法确认仓库是否公开"), "github_unreachable"))
		return
	}
	if public && !req.ConfirmPublic {
		e := argError("confirm_public", "这是公开仓库：它的 fork PR 可以在这台机器上执行代码，需要再确认一次")
		failWith(w, reasoned(e, "public_repo_requires_confirm"))
		return
	}

	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return
	}
	existing := ciBindingsOf(old)
	used := map[string]bool{}
	for _, b := range existing {
		used[ciString(b, "id")] = true
		if strings.EqualFold(ciString(b, "target"), target) {
			failWith(w, newError(v1.ErrConflict, "这个仓库已经绑定过了"))
			return
		}
	}
	id := req.ID
	if id != "" && used[id] {
		failWith(w, newError(v1.ErrConflict, "id 已被使用"))
		return
	}
	if id == "" {
		base := ciSlug(target)
		id = base
		for n := 2; used[id] || !admin.Gone(id); n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
	} else if !admin.Gone(id) {
		failWith(w, newError(v1.ErrConflict, "id 还在被上一个绑定占用，稍后再试"))
		return
	}

	entry := map[string]any{"id": id, "target": target, "source": source}
	if req.Account != "" {
		entry["account"] = req.Account
	}
	if req.Concurrency > 1 {
		entry["concurrency"] = req.Concurrency
	}
	if len(req.Labels) > 0 {
		entry["labels"] = req.Labels
	}
	if public {
		entry["public"] = true
	}
	if source == "import" {
		entry["dir"] = filepath.Clean(req.ImportDir)
	}
	if source == "token" {
		// Held before the binding exists so its instances find the token when they start; it
		// expires with the hour GitHub gives it and is written nowhere.
		admin.HoldToken(id, req.RegistrationToken)
	}
	list := make([]any, 0, len(existing)+1)
	for _, b := range existing {
		list = append(list, b)
	}
	list = append(list, entry)
	if !s.editConfigLocked(w, old, []Edit{{Path: "/ci/bindings", Value: list}}) {
		return
	}
	writeJSON(w, 201, s.ciBindingView(admin, id))
}

// ciBindingView is the binding as the supervisor reports it, or as configured while the
// supervisor has not picked it up yet.
func (s *Server) ciBindingView(admin CIAdmin, id string) v1.CiBinding {
	if view, ok := admin.Binding(id); ok {
		return view
	}
	for _, b := range s.o.Store.Get().CI.Bindings {
		if b.ID == id {
			return admin.SpecView(b, false)
		}
	}
	return v1.CiBinding{ID: id, Instances: []v1.CiInstance{}, Labels: []string{}}
}

func ciInspectError(err error, field string) *v1.Error {
	switch {
	case errors.Is(err, ci.ErrNotARunner):
		return reasoned(argError(field, err.Error()), "not_a_runner")
	case errors.Is(err, ci.ErrOrgRunner):
		return reasoned(argError(field, err.Error()), "not_a_runner")
	default:
		return argError(field, err.Error())
	}
}

// ciImportable refuses the runners the supervisor cannot take over.
func ciImportable(in ci.Inspection) *v1.Error {
	switch {
	case in.Ephemeral:
		return reasoned(argError("import_dir", "这是一次性（ephemeral）运行器，跑完一个任务就失效，不能托管"), "ephemeral_runner")
	case in.ServiceInstalled:
		return reasoned(argError("import_dir", "这个运行器装着官方服务，请先卸载：\n"+in.ServiceHint), "service_installed")
	case in.Running:
		return reasoned(argError("import_dir", "这个运行器正在运行，请先停掉它"), "runner_running")
	}
	return nil
}

func (s *Server) patchCIBinding(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.ciAdmin(w)
	if admin == nil {
		return
	}
	id := pathPart(r, 3)
	var req v1.CiBindingPatch
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if req.Concurrency != nil && (*req.Concurrency < 1 || *req.Concurrency > 16) {
		failWith(w, argError("concurrency", "并发数在 1 到 16 之间"))
		return
	}
	if req.Labels != nil {
		if e := validateCiLabels(*req.Labels); e != nil {
			failWith(w, e)
			return
		}
	}
	if req.Account != nil && !ciIDPattern.MatchString(*req.Account) {
		failWith(w, argError("account", "account 无效"))
		return
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return
	}
	list := make([]any, 0)
	found := false
	for _, b := range ciBindingsOf(old) {
		if ciString(b, "id") == id {
			found = true
			if ciString(b, "source") == "import" && req.Concurrency != nil && *req.Concurrency != 1 {
				failWith(w, reasoned(argError("concurrency", "接管的运行器只有一个实例"), "import_single_instance"))
				return
			}
			if req.Concurrency != nil {
				b["concurrency"] = *req.Concurrency
			}
			if req.Labels != nil {
				if len(*req.Labels) == 0 {
					delete(b, "labels")
				} else {
					b["labels"] = *req.Labels
				}
			}
			if req.Paused != nil {
				if *req.Paused {
					b["paused"] = true
				} else {
					delete(b, "paused")
				}
			}
			if req.Account != nil {
				b["account"] = *req.Account
				if ciString(b, "source") == "token" {
					b["source"] = "account"
				}
			}
		}
		list = append(list, b)
	}
	if !found {
		failWith(w, newError(v1.ErrNotFound, "没有这个绑定"))
		return
	}
	if !s.editConfigLocked(w, old, []Edit{{Path: "/ci/bindings", Value: list}}) {
		return
	}
	writeJSON(w, 200, s.ciBindingView(admin, id))
}

func (s *Server) deleteCIBinding(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.ciAdmin(w)
	if admin == nil {
		return
	}
	id := pathPart(r, 3)
	force := r.URL.Query().Get("force") == "true"
	s.configMu.Lock()
	defer s.configMu.Unlock()
	old, err := os.ReadFile(s.o.Store.Path())
	if err != nil {
		failWith(w, newError(v1.ErrInternal, "无法读取配置文件"))
		return
	}
	list := make([]any, 0)
	found := false
	for _, b := range ciBindingsOf(old) {
		if ciString(b, "id") == id {
			found = true
			continue
		}
		list = append(list, b)
	}
	if !found {
		failWith(w, newError(v1.ErrNotFound, "没有这个绑定"))
		return
	}
	var spec config.CIBinding
	for _, b := range s.o.Store.Get().CI.Bindings {
		if b.ID == id {
			spec = b
		}
	}
	if force {
		admin.ForgetForce(id)
	}
	edit := Edit{Path: "/ci/bindings", Value: list}
	if len(list) == 0 {
		edit = Edit{Path: "/ci/bindings", Delete: true}
	}
	if !s.editConfigLocked(w, old, []Edit{edit}) {
		return
	}
	if view, ok := admin.Binding(id); ok {
		view.Removing = true
		writeJSON(w, 200, view)
		return
	}
	writeJSON(w, 200, admin.SpecView(spec, true))
}

func (s *Server) restartCIBinding(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.ciAdmin(w)
	if admin == nil {
		return
	}
	if err := admin.Restart(pathPart(r, 3)); err != nil {
		failWith(w, newError(v1.ErrNotFound, "没有这个绑定"))
		return
	}
	w.WriteHeader(204)
}

func (s *Server) repairCIBinding(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.ciAdmin(w)
	if admin == nil {
		return
	}
	id := pathPart(r, 3)
	var req v1.CiBindingRepair
	if r.ContentLength != 0 {
		if e := decode(r, &req, false); e != nil {
			failWith(w, e)
			return
		}
	}
	view, ok := admin.Binding(id)
	if !ok || view.Removing {
		failWith(w, newError(v1.ErrNotFound, "没有这个绑定"))
		return
	}
	if view.Account == "" && req.RegistrationToken == "" {
		failWith(w, argError("registration_token", "这个绑定没有账号，重新注册需要从仓库网页复制一个新的注册令牌"))
		return
	}
	if err := admin.Repair(id, req.RegistrationToken); err != nil {
		failWith(w, newError(v1.ErrNotFound, "没有这个绑定"))
		return
	}
	writeJSON(w, 200, s.ciBindingView(admin, id))
}

func (s *Server) inspectCIImport(w http.ResponseWriter, r *http.Request, _ *call) {
	admin := s.ciAdmin(w)
	if admin == nil {
		return
	}
	var req v1.CiImportInspectRequest
	if e := decode(r, &req, true); e != nil {
		failWith(w, e)
		return
	}
	if !filepath.IsAbs(req.Dir) {
		failWith(w, argError("dir", "目录必须是绝对路径"))
		return
	}
	in, err := admin.Inspect(req.Dir)
	if err != nil {
		if errors.Is(err, ci.ErrNotARunner) {
			failWith(w, reasoned(newError(v1.ErrNotFound, err.Error()), "not_a_runner"))
			return
		}
		failWith(w, ciInspectError(err, "dir"))
		return
	}
	writeJSON(w, 200, v1.CiImportInspectResponse{
		Target: in.Target, RunnerName: in.RunnerName, Labels: []string{}, ServiceInstalled: in.ServiceInstalled,
		ServiceHint: in.ServiceHint, Running: in.Running, Ephemeral: in.Ephemeral,
	})
}
