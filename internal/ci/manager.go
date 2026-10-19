package ci

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
)

// Options configure a Manager.
type Options struct {
	// StateDir is the node's state directory; runners live under <StateDir>/ci.
	StateDir string
	// Node is the node name, part of every runner name and label.
	Node string
	// Config returns the ci section of the configuration in effect.
	Config func() config.CI
	// Cache supplies the runner package; nil means this system has no official runner.
	Cache *Cache
	// Token returns a registration token for a binding that registers through an account. Nil
	// until GitHub accounts are available; such a binding then reports auth_failed.
	Token func(ctx context.Context, b config.CIBinding) (string, error)
	// RemoveToken returns a token that removes a runner of the binding's repository from GitHub.
	// Nil, or a failure, leaves the runner registered there (GitHub drops an offline runner after
	// 14 days, and the web page can delete it sooner).
	RemoveToken func(ctx context.Context, b config.CIBinding) (string, error)
	// Backoff is the wait before restart number attempt (1 for the first). Default 1s doubling to 5m.
	Backoff func(attempt int) time.Duration
	// StableAfter is how long a runner must have run for its backoff to start over. Default 10m.
	StableAfter time.Duration
	// Inhibit keeps the machine awake while a job runs; nil does nothing.
	Inhibit Inhibitor
	// Env is the environment runners are started with. Default: the kernel's, without secrets.
	Env func() []string
	// RunningIn reports whether a listener runs from dir; default looks at the process list.
	RunningIn func(dir string) bool
	Logf      func(format string, args ...any)
}

// Manager supervises every runner instance of the node.
type Manager struct {
	o Options

	pkgMu sync.Mutex // one package download at a time

	mu       sync.Mutex
	bindings map[string]*binding
	tokens   map[string]heldToken // registration tokens by binding id, memory only
	draining bool
	busy     int
	release  func()
	closed   bool
	wg       sync.WaitGroup
	ctx      context.Context
	cancel   context.CancelFunc
}

type heldToken struct {
	token string
	at    time.Time
}

// registrationTokenLife is how long GitHub honours a registration token.
const registrationTokenLife = time.Hour

// binding is one configured repository with its instances.
type binding struct {
	spec      config.CIBinding
	dir       string
	instances map[int]*instance
	removing  bool
	force     bool
}

// New creates a Manager. Nothing runs until Apply is called with a configuration.
func New(o Options) *Manager {
	if o.Backoff == nil {
		o.Backoff = defaultBackoff
	}
	if o.StableAfter <= 0 {
		o.StableAfter = 10 * time.Minute
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.Env == nil {
		o.Env = defaultEnv
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{o: o, bindings: map[string]*binding{}, tokens: map[string]heldToken{}, ctx: ctx, cancel: cancel}
}

func defaultBackoff(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt && d < 5*time.Minute; i++ {
		d *= 2
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}

// defaultEnv is the kernel's environment without what must not reach CI jobs: the mesh auth key
// and the kernel's own settings.
func defaultEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		upper := strings.ToUpper(name)
		if strings.HasPrefix(upper, "XMESH_") || strings.HasPrefix(upper, "TS_") || strings.HasPrefix(upper, "TAILSCALE_") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func (m *Manager) root() string { return filepath.Join(m.o.StateDir, "ci") }

// bindingDir is where the runners of a binding live.
func (m *Manager) bindingDir(b config.CIBinding) string {
	if b.Dir != "" {
		return b.Dir
	}
	return filepath.Join(m.root(), b.ID)
}

// hostFor is the GitHub host a binding registers with: its account's, else github.com.
func (m *Manager) hostFor(spec config.CIBinding) string {
	for _, a := range m.o.Config().Accounts {
		if a.ID == spec.Account && a.Host != "" {
			return a.Host
		}
	}
	return "github.com"
}

// managedDir reports whether the kernel created and owns dir (and may delete it).
func (m *Manager) managedDir(b config.CIBinding) bool { return b.Source != "import" }

// Apply makes the running runners match the configuration: new bindings get instances, removed
// bindings drain and are cleaned up, changed bindings are adjusted in place. It never blocks on
// a runner.
func (m *Manager) Apply(c config.CI) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	seen := map[string]bool{}
	for _, spec := range c.Bindings {
		seen[spec.ID] = true
		b, ok := m.bindings[spec.ID]
		if !ok {
			b = &binding{spec: spec, dir: m.bindingDir(spec), instances: map[int]*instance{}}
			m.bindings[spec.ID] = b
		}
		if b.removing {
			// Added again before the old one finished draining: treat it as a new binding once the
			// old one is gone. The caller sees conflict until then.
			continue
		}
		relabel := !sameStrings(b.spec.Labels, spec.Labels)
		b.spec = spec
		want := spec.Concurrency
		if spec.Source == "import" || want < 1 {
			want = 1
		}
		for idx := 1; idx <= want; idx++ {
			in, ok := b.instances[idx]
			if !ok {
				in = m.newInstance(b, idx)
				b.instances[idx] = in
				m.wg.Add(1)
				go func() { defer m.wg.Done(); in.run() }()
			} else if relabel {
				in.requestReregister()
			}
			in.setSpec(spec)
		}
		for idx, in := range b.instances {
			if idx > want {
				in.shutdown(false)
				delete(b.instances, idx)
				m.cleanupAfter(b, in, false)
			}
		}
	}
	for id, b := range m.bindings {
		if !seen[id] && !b.removing {
			m.removeLocked(id, b)
		}
	}
	for id, held := range m.tokens {
		if time.Since(held.at) > registrationTokenLife || m.bindings[id] == nil {
			delete(m.tokens, id)
		}
	}
}

// removeLocked drains a binding that left the configuration and deletes what the kernel made.
func (m *Manager) removeLocked(id string, b *binding) {
	b.removing = true
	instances := make([]*instance, 0, len(b.instances))
	for _, in := range b.instances {
		in.setSpec(config.CIBinding{ID: b.spec.ID, Paused: true, Source: b.spec.Source, Target: b.spec.Target})
		in.shutdown(b.force)
		instances = append(instances, in)
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for _, in := range instances {
			<-in.finished
		}
		m.deregister(b.spec, instances...)
		if m.managedDir(b.spec) {
			if err := os.RemoveAll(b.dir); err != nil {
				m.o.Logf("ci: 删除 %s 失败: %v", b.dir, err)
			}
		}
		m.mu.Lock()
		delete(m.bindings, id)
		delete(m.tokens, id)
		m.mu.Unlock()
	}()
}

// cleanupAfter removes the directory of an instance that was scaled away, once it has stopped.
func (m *Manager) cleanupAfter(b *binding, in *instance, _ bool) {
	if !m.managedDir(b.spec) {
		return
	}
	dir := in.dir
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		<-in.finished
		m.deregister(b.spec, in)
		_ = os.RemoveAll(dir)
	}()
}

// ForgetForce marks a binding that is about to leave the configuration as one that must not wait
// for its running jobs.
func (m *Manager) ForgetForce(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.bindings[id]; b != nil {
		b.force = true
	}
}

// HoldToken keeps a registration token in memory for the instances of a binding to register
// with. It expires after the hour GitHub allows and is never written anywhere.
func (m *Manager) HoldToken(id, token string) {
	m.mu.Lock()
	m.tokens[id] = heldToken{token: token, at: time.Now()}
	m.mu.Unlock()
	m.kick(id)
}

func (m *Manager) heldToken(id string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.tokens[id]
	if !ok || time.Since(h.at) > registrationTokenLife {
		delete(m.tokens, id)
		return "", false
	}
	return h.token, true
}

func (m *Manager) kick(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b := m.bindings[id]; b != nil {
		for _, in := range b.instances {
			in.wake()
		}
	}
}

// Restart restarts the instances of a binding and clears their backoff and problems. A running
// job finishes first.
func (m *Manager) Restart(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.bindings[id]
	if b == nil || b.removing {
		return fmt.Errorf("没有这个绑定")
	}
	for _, in := range b.instances {
		in.requestRestart(false)
	}
	return nil
}

// Repair registers the instances of a binding again, using token when given (otherwise the
// account's), and restarts them.
func (m *Manager) Repair(id, token string) error {
	m.mu.Lock()
	b := m.bindings[id]
	if b == nil || b.removing {
		m.mu.Unlock()
		return fmt.Errorf("没有这个绑定")
	}
	if token != "" {
		m.tokens[id] = heldToken{token: token, at: time.Now()}
	}
	for _, in := range b.instances {
		in.requestRestart(true)
	}
	m.mu.Unlock()
	return nil
}

// SetDraining stops idle runners and keeps new jobs away (true), or lets them run again.
func (m *Manager) SetDraining(on bool) {
	m.mu.Lock()
	m.draining = on
	for _, b := range m.bindings {
		for _, in := range b.instances {
			in.wake()
		}
	}
	m.mu.Unlock()
}

func (m *Manager) isDraining() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.draining
}

// Shutdown drains every runner and waits up to timeout for running jobs; after that it ends
// what is left. The Manager is unusable afterwards.
func (m *Manager) Shutdown(timeout time.Duration) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.draining = true
	var all []*instance
	for _, b := range m.bindings {
		for _, in := range b.instances {
			in.shutdown(false)
			all = append(all, in)
		}
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() {
		for _, in := range all {
			<-in.finished
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
		for _, in := range all {
			in.shutdown(true)
		}
		<-done
	}
	m.cancel()
	m.wg.Wait()
	m.releaseSleep()
}

// busyChanged is called by instances as jobs start and end; it holds the sleep inhibition while
// any job runs.
func (m *Manager) busyChanged(delta int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.busy += delta
	if m.busy < 0 {
		m.busy = 0
	}
	if m.busy > 0 && m.release == nil && m.o.Inhibit != nil {
		m.release = m.o.Inhibit()
	}
	if m.busy == 0 && m.release != nil {
		m.release()
		m.release = nil
	}
}

func (m *Manager) releaseSleep() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.release != nil {
		m.release()
		m.release = nil
	}
}

// Bindings is the state of every binding, ordered by id.
func (m *Manager) Bindings() []v2.CiBinding {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]v2.CiBinding, 0, len(m.bindings))
	for _, b := range m.bindings {
		out = append(out, m.viewLocked(b))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Binding is the state of one binding.
func (m *Manager) Binding(id string) (v2.CiBinding, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b := m.bindings[id]
	if b == nil {
		return v2.CiBinding{}, false
	}
	return m.viewLocked(b), true
}

// Gone reports whether no binding by that id exists any more, not even a draining one.
func (m *Manager) Gone(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bindings[id] == nil
}

func (m *Manager) viewLocked(b *binding) v2.CiBinding {
	labels := append([]string{}, b.spec.Labels...)
	view := v2.CiBinding{
		ID: b.spec.ID, Scope: "repo", Target: b.spec.Target, Source: v2.CiBindingSource(b.spec.Source),
		Account: b.spec.Account, Public: b.spec.Public, Concurrency: b.spec.Concurrency, Labels: labels,
		Paused: b.spec.Paused, Removing: b.removing, Dir: b.dir, Instances: []v2.CiInstance{},
	}
	if view.Concurrency < 1 {
		view.Concurrency = 1
	}
	if view.Source == "" {
		view.Source = v2.CiSourceAccount
	}
	idx := make([]int, 0, len(b.instances))
	for i := range b.instances {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		view.Instances = append(view.Instances, b.instances[i].snapshot())
	}
	return view
}

// Summary is StatusResponse.ci; nil when nothing is bound.
func (m *Manager) Summary() *v2.CiSummary {
	bindings := m.Bindings()
	if len(bindings) == 0 {
		return nil
	}
	return &v2.CiSummary{Bindings: bindings, Draining: m.isDraining()}
}

// Overview is GET /v2/ci apart from the accounts, which the control layer adds.
func (m *Manager) Overview() v2.CiOverview {
	c := m.o.Config()
	bindings := m.Bindings()
	runner := v2.CiRunnerPackage{}
	if m.o.Cache != nil {
		if pkg, ok := m.o.Cache.Cached(); ok {
			runner = v2.CiRunnerPackage{Version: pkg.Version, Cached: true}
		}
	}
	return v2.CiOverview{
		Accounts: []v2.CiAccount{}, Bindings: bindings, Notices: orEmpty(Notices(len(bindings))), Runner: runner,
		PreventSleepWhileBusy: c.PreventSleepWhileBusy, StopTimeoutS: int(c.StopTimeoutDuration() / time.Second),
		Draining: m.isDraining(),
	}
}

func orEmpty(n []v2.CiNotice) []v2.CiNotice {
	if n == nil {
		return []v2.CiNotice{}
	}
	return n
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string{}, a...), append([]string{}, b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// scriptName is the runner's launcher on this system: config.sh or config.cmd, run.sh or run.cmd.
func scriptName(base string) string {
	if runtime.GOOS == "windows" {
		return base + ".cmd"
	}
	return base + ".sh"
}

// SpecView is the contract view of a binding that has no instances yet (or no longer).
func (m *Manager) SpecView(spec config.CIBinding, removing bool) v2.CiBinding {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.viewLocked(&binding{spec: spec, dir: m.bindingDir(spec), removing: removing, instances: map[int]*instance{}})
}

// RemoveOrphans deletes runner directories under the state directory that no binding of c owns:
// what a binding left behind when the kernel stopped while it was being removed.
func (m *Manager) RemoveOrphans(c config.CI) {
	entries, err := os.ReadDir(m.root())
	if err != nil {
		return
	}
	owned := map[string]bool{}
	for _, b := range c.Bindings {
		if b.Source != "import" {
			owned[b.ID] = true
		}
	}
	for _, e := range entries {
		if e.IsDir() && !owned[e.Name()] {
			if err := os.RemoveAll(filepath.Join(m.root(), e.Name())); err != nil {
				m.o.Logf("ci: 清理遗留目录 %s 失败: %v", e.Name(), err)
			}
		}
	}
}

// deregister removes the runners of instances from GitHub when the binding has an account to ask
// for a remove token with. It is best effort: what cannot be removed is reported in the log.
func (m *Manager) deregister(spec config.CIBinding, instances ...*instance) {
	if spec.Source == "import" || spec.Account == "" || m.o.RemoveToken == nil {
		return
	}
	var registered []*instance
	for _, in := range instances {
		if _, err := os.Stat(filepath.Join(in.dir, ".runner")); err == nil {
			registered = append(registered, in)
		}
	}
	if len(registered) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, in := range registered {
		token, err := m.o.RemoveToken(ctx, spec)
		if err != nil {
			m.o.Logf("ci: 无法向 GitHub 注销 %s: %v", in.name, err)
			return
		}
		if err = in.unregister(ctx, token); err != nil {
			m.o.Logf("ci: 注销 %s 失败: %v", in.name, err)
		}
	}
}
