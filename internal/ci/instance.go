package ci

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/config"
)

// instance is one runner: a directory with an installed, registered runner and the process that
// listens for jobs. Its goroutine (run) is the only thing that starts or stops that process.
type instance struct {
	m        *Manager
	bid      string
	idx      int
	dir      string
	name     string
	finished chan struct{}
	wakeC    chan struct{}

	mu        sync.Mutex
	snap      v1.CiInstance
	spec      config.CIBinding
	shutting  bool // the binding or the node is going away
	force     bool // do not wait for a running job
	restart   bool // stop gracefully, then start again with a clean slate
	rereg     bool // register again before the next start
	reinstall bool // unpack the newest package over the install before the next start
	tail      []string
	removed   bool // the last run ended because GitHub no longer knows this runner
	conflicts int
}

const (
	tailLines     = 40
	stopGrace     = 20 * time.Second
	registerLimit = 2 * time.Minute
)

func (m *Manager) newInstance(b *binding, idx int) *instance {
	dir := b.dir
	name := fmt.Sprintf("xmesh-%s-%s-%d", m.o.Node, b.spec.ID, idx)
	if b.spec.Source != "import" {
		dir = filepath.Join(b.dir, strconv.Itoa(idx))
	} else if f, err := readRunnerFile(dir); err == nil {
		name = f.AgentName
	}
	if len(name) > 64 {
		name = name[:64]
	}
	in := &instance{m: m, bid: b.spec.ID, idx: idx, dir: dir, name: name, spec: b.spec, finished: make(chan struct{}), wakeC: make(chan struct{}, 1)}
	in.snap = v1.CiInstance{Index: idx, Name: name, State: v1.CiStopped, Since: v1.Now()}
	return in
}

func (i *instance) wake() {
	select {
	case i.wakeC <- struct{}{}:
	default:
	}
}

func (i *instance) setSpec(spec config.CIBinding) {
	i.mu.Lock()
	i.spec = spec
	i.mu.Unlock()
	i.wake()
}

func (i *instance) requestReregister() {
	i.mu.Lock()
	i.rereg = true
	i.restart = true
	i.mu.Unlock()
	i.wake()
}

// requestRestart stops the runner when it is idle and starts it again, forgetting earlier trouble.
func (i *instance) requestRestart(reregister bool) {
	i.mu.Lock()
	i.restart = true
	i.conflicts = 0
	if reregister {
		i.rereg = true
	}
	i.mu.Unlock()
	i.wake()
}

// shutdown ends the instance for good: idle at once, busy after its job unless force.
func (i *instance) shutdown(force bool) {
	i.mu.Lock()
	i.shutting = true
	i.force = i.force || force
	i.mu.Unlock()
	i.wake()
}

func (i *instance) snapshot() v1.CiInstance {
	i.mu.Lock()
	defer i.mu.Unlock()
	s := i.snap
	if s.Job != nil {
		j := *s.Job
		s.Job = &j
	}
	if s.LastResult != nil {
		r := *s.LastResult
		s.LastResult = &r
	}
	if s.Problem != nil {
		p := *s.Problem
		s.Problem = &p
	}
	return s
}

func (i *instance) setState(state v1.CiInstanceState, problem *v1.CiProblem) {
	i.mu.Lock()
	wasBusy := i.snap.State == v1.CiBusy
	if i.snap.State != state {
		i.snap.Since = v1.Now()
	}
	i.snap.State = state
	i.snap.Problem = problem
	if state != v1.CiBusy {
		i.snap.Job = nil
	}
	if state != v1.CiStarting && state != v1.CiIdle && state != v1.CiBusy && state != v1.CiStopping {
		i.snap.PID = 0
	}
	i.mu.Unlock()
	if wasBusy && state != v1.CiBusy {
		i.m.busyChanged(-1)
	}
}

func (i *instance) problem(code v1.CiProblemCode, message string) *v1.CiProblem {
	return &v1.CiProblem{Code: code, Message: message}
}

// wantRunning is whether a listener should be running right now.
func (i *instance) wantRunning() bool {
	// The manager's lock is taken before an instance's (Apply, Bindings), never the other way.
	draining := i.m.isDraining()
	i.mu.Lock()
	defer i.mu.Unlock()
	return !i.shutting && !i.spec.Paused && !draining
}

func (i *instance) flags() (shutting, force, restart bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.shutting, i.force, i.restart
}

// waitWake blocks until the instance is poked, the manager stops, or d passes (d <= 0: no timer).
func (i *instance) waitWake(d time.Duration) {
	var timer <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-i.wakeC:
	case <-timer:
	case <-i.m.ctx.Done():
	}
}

func (i *instance) run() {
	defer close(i.finished)
	attempt := 0
	for {
		if i.m.ctx.Err() != nil {
			i.setState(v1.CiStopped, nil)
			return
		}
		if shutting, _, _ := i.flags(); shutting {
			i.setState(v1.CiStopped, nil)
			return
		}
		if !i.wantRunning() {
			i.setState(v1.CiStopped, nil)
			i.waitWake(0)
			continue
		}
		i.clearRestart()
		if p, transient := i.ensureInstalled(); p != nil {
			i.setState(stateOf(transient), p)
			attempt++
			if transient {
				i.waitWake(i.m.o.Backoff(attempt))
			} else {
				i.waitWake(0)
			}
			continue
		}
		if p := i.ensureRegistered(); p != nil {
			i.setState(v1.CiBroken, p)
			i.waitWake(0)
			continue
		}
		started := time.Now()
		exit := i.runOnce()
		ranFor := time.Since(started)
		if ranFor >= i.m.o.StableAfter {
			attempt = 0
		}
		if !exit.asked {
			i.mu.Lock()
			i.snap.Restarts++
			i.mu.Unlock()
		}
		wait, p, broken := i.classify(exit, ranFor)
		switch {
		case broken:
			i.setState(v1.CiBroken, p)
			i.waitWake(0)
		case wait == 0:
			attempt = 0
		default:
			attempt++
			i.setState(v1.CiBackoff, p)
			i.waitWake(i.m.o.Backoff(attempt))
		}
	}
}

func stateOf(transient bool) v1.CiInstanceState {
	if transient {
		return v1.CiBackoff
	}
	return v1.CiBroken
}

func (i *instance) clearRestart() {
	i.mu.Lock()
	i.restart = false
	i.mu.Unlock()
}

// ensureInstalled unpacks the runner into the instance's directory when it is not there. The
// returned problem is non-nil on failure; transient says it is worth retrying by itself.
func (i *instance) ensureInstalled() (p *v1.CiProblem, transient bool) {
	i.mu.Lock()
	source, reinstall := i.spec.Source, i.reinstall
	i.mu.Unlock()
	if source == "import" {
		return nil, false
	}
	launcher := filepath.Join(i.dir, scriptName("config"))
	if _, err := os.Stat(launcher); err == nil && !reinstall {
		return nil, false
	}
	if i.m.o.Cache == nil {
		return i.problem(v1.CiProblemDownloadFailed, "GitHub 没有提供适用于这个系统和架构的官方运行器。"), false
	}
	i.setState(v1.CiInstalling, nil)
	i.m.pkgMu.Lock()
	pkg, err := i.m.o.Cache.Ensure(i.m.ctx, reinstall)
	i.m.pkgMu.Unlock()
	if err != nil {
		return i.problem(v1.CiProblemDownloadFailed, "下载运行器安装包失败，稍后重试: "+short(err.Error())), true
	}
	if err = Extract(pkg.Path, i.dir); err != nil {
		if !reinstall {
			_ = os.RemoveAll(i.dir)
		}
		return i.problem(v1.CiProblemDownloadFailed, "解压运行器安装包失败: "+short(err.Error())), true
	}
	i.mu.Lock()
	i.reinstall = false
	i.mu.Unlock()
	return nil, false
}

// ensureRegistered registers the runner with GitHub unless it already is.
func (i *instance) ensureRegistered() *v1.CiProblem {
	i.mu.Lock()
	spec, rereg := i.spec, i.rereg
	i.mu.Unlock()
	_, err := os.Stat(filepath.Join(i.dir, ".runner"))
	registered := err == nil
	if spec.Source == "import" {
		if !registered {
			return i.problem(v1.CiProblemRegistrationFailed, "目录里没有 .runner，这个运行器还没有注册。")
		}
		return nil
	}
	if registered && !rereg {
		return nil
	}
	token, ok := i.m.heldToken(i.bid)
	if !ok && spec.Account != "" && i.m.o.Token != nil {
		i.setState(v1.CiRegistering, nil)
		t, err := i.m.o.Token(i.m.ctx, spec)
		if err != nil {
			return i.problem(v1.CiProblemAuthFailed, "无法向 GitHub 取得注册令牌: "+short(err.Error()))
		}
		token, ok = t, true
	}
	if !ok {
		return i.problem(v1.CiProblemRegistrationFailed, "没有可用的注册令牌：请在仓库的 Actions 运行器页复制新的注册令牌后选择「重新注册」。")
	}
	i.setState(v1.CiRegistering, nil)
	if registered {
		for _, f := range []string{".runner", ".credentials", ".credentials_rsaparams"} {
			_ = os.Remove(filepath.Join(i.dir, f))
		}
	}
	if p := i.configure(spec, token); p != nil {
		return p
	}
	i.mu.Lock()
	i.rereg = false
	i.removed = false
	i.mu.Unlock()
	return nil
}

func (i *instance) runnerURL(spec config.CIBinding) string {
	return "https://" + i.m.hostFor(spec) + "/" + spec.Target
}

func (i *instance) labels(spec config.CIBinding) string {
	labels := []string{"xmesh", "xmesh-" + i.m.o.Node}
	for _, l := range spec.Labels {
		dup := false
		for _, have := range labels {
			dup = dup || have == l
		}
		if !dup {
			labels = append(labels, l)
		}
	}
	return strings.Join(labels, ",")
}

// configure runs the runner's own registration script. The token goes through the environment so
// it does not show in the process list.
func (i *instance) configure(spec config.CIBinding, token string) *v1.CiProblem {
	args := []string{"--unattended", "--url", i.runnerURL(spec), "--name", i.name, "--labels", i.labels(spec), "--work", "_work", "--replace"}
	env := append(i.m.o.Env(), "ACTIONS_RUNNER_INPUT_TOKEN="+token)
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		env = append(env, "RUNNER_ALLOW_RUNASROOT=1")
	}
	var mu sync.Mutex
	var lines []string
	p, err := startProcess(i.dir, filepath.Join(i.dir, scriptName("config")), args, env, func(line string) {
		mu.Lock()
		lines = append(lines, strings.ReplaceAll(line, token, "***"))
		mu.Unlock()
	})
	if err != nil {
		return i.problem(v1.CiProblemStartFailed, "无法运行注册脚本: "+short(err.Error()))
	}
	select {
	case <-p.done:
	case <-time.After(registerLimit):
		p.kill()
		<-p.done
		return i.problem(v1.CiProblemRegistrationFailed, "注册超时，请检查网络后重新注册。")
	case <-i.m.ctx.Done():
		p.kill()
		<-p.done
		return i.problem(v1.CiProblemRegistrationFailed, "注册被中断。")
	}
	if p.exit == 0 {
		return nil
	}
	mu.Lock()
	defer mu.Unlock()
	return i.registrationProblem(lines)
}

// unregister runs the runner's own removal script, which deletes the runner at GitHub and the
// local registration files.
func (i *instance) unregister(ctx context.Context, token string) error {
	env := append(i.m.o.Env(), "ACTIONS_RUNNER_INPUT_TOKEN="+token)
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		env = append(env, "RUNNER_ALLOW_RUNASROOT=1")
	}
	var mu sync.Mutex
	var last string
	p, err := startProcess(i.dir, filepath.Join(i.dir, scriptName("config")), []string{"remove"}, env, func(line string) {
		mu.Lock()
		if strings.TrimSpace(line) != "" {
			last = strings.ReplaceAll(line, token, "***")
		}
		mu.Unlock()
	})
	if err != nil {
		return err
	}
	select {
	case <-p.done:
	case <-ctx.Done():
		p.kill()
		<-p.done
		return ctx.Err()
	}
	if p.exit != 0 {
		mu.Lock()
		defer mu.Unlock()
		return fmt.Errorf("退出码 %d: %s", p.exit, short(last))
	}
	return nil
}

func (i *instance) registrationProblem(lines []string) *v1.CiProblem {
	joined := strings.Join(lines, "\n")
	last := ""
	for j := len(lines) - 1; j >= 0; j-- {
		if strings.TrimSpace(lines[j]) != "" {
			last = strings.TrimSpace(lines[j])
			break
		}
	}
	switch {
	case dependencyMissing(joined):
		return i.problem(v1.CiProblemMissingDependency, "缺少运行器需要的系统库（常见于 Linux 的 ICU），请先安装依赖: "+short(last))
	case strings.Contains(joined, "404") || strings.Contains(joined, "NotFound"):
		return i.problem(v1.CiProblemRegistrationFailed, "GitHub 找不到这个仓库，或注册令牌没有权限。")
	case strings.Contains(joined, "401") || strings.Contains(joined, "Unauthorized") || strings.Contains(joined, "expired"):
		return i.problem(v1.CiProblemRegistrationFailed, "注册令牌无效或已过期（一小时有效），请换一个新的。")
	case strings.Contains(joined, "already configured"):
		return i.problem(v1.CiProblemRegistrationFailed, "这个目录里已经有一个注册过的运行器。")
	}
	return i.problem(v1.CiProblemRegistrationFailed, "注册失败: "+short(last))
}

func dependencyMissing(text string) bool {
	for _, marker := range []string{"libicu", "Libicu", "ICU package", "libssl", "libcoreclr", "Cannot execute dotnet", "dotnet core"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// short cuts a message to one readable line.
func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 120 {
		return string(r[:120]) + "…"
	}
	return s
}

// exitInfo is how one run of the listener ended.
type exitInfo struct {
	code      int
	asked     bool // the process was stopped on purpose
	conflict  bool
	removed   bool
	deprec    bool
	authFail  bool
	tailLines []string
}

// runOnce starts the listener and supervises it until it exits.
func (i *instance) runOnce() exitInfo {
	i.mu.Lock()
	i.tail = nil
	i.removed = false
	i.snap.PID = 0
	i.mu.Unlock()
	env := i.m.o.Env()
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		env = append(env, "RUNNER_ALLOW_RUNASROOT=1")
	}
	i.setState(v1.CiStarting, nil)
	p, err := startProcess(i.dir, filepath.Join(i.dir, scriptName("run")), nil, env, i.onLine)
	if err != nil {
		return exitInfo{code: 127, tailLines: []string{err.Error()}}
	}
	i.mu.Lock()
	i.snap.PID = p.pid()
	i.mu.Unlock()

	var wantedSince, termAt time.Time
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	asked := false
	managerDone := i.m.ctx.Done()
	for {
		select {
		case <-p.done:
			return i.exitOf(p.exit, asked)
		case <-i.wakeC:
		case <-ticker.C:
		case <-managerDone:
		}
		if !termAt.IsZero() {
			managerDone = nil
			if time.Since(termAt) > stopGrace {
				p.kill()
			}
			continue
		}
		shutting, force, restart := i.flags()
		stop := shutting || restart || !i.wantRunning() || i.m.ctx.Err() != nil
		if !stop {
			wantedSince = time.Time{}
			continue
		}
		if wantedSince.IsZero() {
			wantedSince = time.Now()
		}
		busy := i.currentState() == v1.CiBusy
		overdue := time.Since(wantedSince) > i.m.o.Config().StopTimeoutDuration()
		if busy && !force && !overdue && i.m.ctx.Err() == nil {
			continue
		}
		i.setState(v1.CiStopping, nil)
		asked = true
		termAt = time.Now()
		if force || overdue {
			p.kill()
		} else {
			p.terminate()
		}
	}
}

func (i *instance) currentState() v1.CiInstanceState {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.snap.State
}

func (i *instance) exitOf(code int, asked bool) exitInfo {
	i.mu.Lock()
	defer i.mu.Unlock()
	info := exitInfo{code: code, asked: asked, tailLines: append([]string{}, i.tail...)}
	for _, l := range i.tail {
		switch parseLine(l).kind {
		case lineSessionConflict:
			info.conflict = true
		case lineRunnerRemoved:
			info.removed = true
		case lineVersionDeprecated:
			info.deprec = true
		case lineAuthFailed:
			info.authFail = true
		}
	}
	return info
}

// onLine folds one line of listener output into the instance's state.
func (i *instance) onLine(line string) {
	ev := parseLine(line)
	i.mu.Lock()
	i.tail = append(i.tail, line)
	if len(i.tail) > tailLines {
		i.tail = i.tail[len(i.tail)-tailLines:]
	}
	delta := 0
	switch ev.kind {
	case lineListening, lineConnected:
		if i.snap.State == v1.CiStarting {
			i.snap.State, i.snap.Since = v1.CiIdle, v1.Now()
			i.snap.Problem = nil
		}
	case lineJobStarted:
		if i.snap.State != v1.CiBusy {
			delta = 1
		}
		i.snap.State, i.snap.Since = v1.CiBusy, v1.Now()
		i.snap.Job = &v1.CiJob{Name: ev.job, StartedAt: v1.Now()}
	case lineJobDone:
		i.snap.LastResult = &v1.CiResult{Name: ev.job, Result: ev.result, FinishedAt: v1.Now()}
		if i.snap.State == v1.CiBusy {
			i.snap.State, i.snap.Since, i.snap.Job = v1.CiIdle, v1.Now(), nil
			delta = -1
		}
	}
	i.mu.Unlock()
	if delta != 0 {
		i.m.busyChanged(delta)
	}
	if ev.kind == lineJobStarted || ev.kind == lineJobDone {
		i.wake()
	}
}

// classify turns an exit into what to do next: how long to wait before starting again (zero:
// at once), the problem to show, and whether the instance needs the user before it can go on.
func (i *instance) classify(e exitInfo, ranFor time.Duration) (wait time.Duration, p *v1.CiProblem, broken bool) {
	shutting, _, restart := i.flags()
	if e.asked || shutting || restart {
		i.mu.Lock()
		i.conflicts = 0
		i.mu.Unlock()
		return 0, nil, false
	}
	switch {
	case e.removed:
		i.mu.Lock()
		i.rereg, i.removed = true, true
		i.mu.Unlock()
		return time.Second, i.problem(v1.CiProblemRunnerRemoved, "GitHub 已经删除了这个运行器，请选择「重新注册」。"), true
	case e.authFail:
		return time.Second, i.problem(v1.CiProblemAuthFailed, "GitHub 拒绝了这个运行器的凭据，请选择「重新注册」。"), true
	case e.deprec || e.code == 7:
		i.mu.Lock()
		i.reinstall = true
		i.mu.Unlock()
		return time.Second, i.problem(v1.CiProblemVersionDeprecated, "运行器版本已被弃用，重启后会换成最新版本。"), true
	case e.conflict || e.code == 5:
		i.mu.Lock()
		i.conflicts++
		n := i.conflicts
		i.mu.Unlock()
		if n >= 3 {
			return time.Second, i.problem(v1.CiProblemSessionConflict, "这个运行器在别处也在运行，请先关掉另一个，再选择「重启」。"), true
		}
		return time.Second, i.problem(v1.CiProblemSessionConflict, "GitHub 上还留着这个运行器的旧会话，稍后自动重试。"), false
	case dependencyMissing(strings.Join(e.tailLines, "\n")):
		return time.Second, i.problem(v1.CiProblemMissingDependency, "缺少运行器需要的系统库，请先安装依赖，再选择「重启」。"), true
	case e.code == 3 || e.code == 4 || e.code == 6:
		// The runner is updating itself or refreshed its configuration: start it again at once.
		return 0, nil, false
	case e.code == 127 && ranFor < time.Second:
		return time.Second, i.problem(v1.CiProblemStartFailed, "无法启动运行器: "+short(strings.Join(e.tailLines, " "))), false
	}
	msg := fmt.Sprintf("运行器退出（退出码 %d），稍后自动重启。", e.code)
	return time.Second, i.problem(v1.CiProblemUnclassified, msg), false
}
