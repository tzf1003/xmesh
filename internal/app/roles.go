package app

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/control"
)

// roleRuntime puts the roles that come and go with the configuration into effect: it starts and
// stops the local gateway and the ingress, moves the gateway to a new gateway.listen and the ingress
// to a new ingress section, while the agent, the network and the sessions go on. A generation is applied by a worker, never inside the store's subscriber
// call: starting a listener there would hold up every reader of the configuration, and the
// subscriber may not read the configuration itself.
//
// What cannot be put into effect leaves the roles as they were and is reported, through the status
// warnings and the reload response, until a later generation or an explicit retry succeeds.
type roleRuntime struct {
	deps     gatewayDeps
	stateDir string
	// ingressDeps is set before the ingress can start; the zero value means the node has no way to run one.
	ingressDeps ingressDeps
	// forceIngress keeps the ingress running although the configuration file does not list the role,
	// as when the node was started with --ingress.
	forceIngress bool
	// fatal ends the process; it is how a role that fails while the node starts is reported.
	fatal func(error)

	mu      sync.Mutex
	gateway *gatewayRole
	ingress *ingressRole
	// ingressFailed is the ingress section the last restart could not move to. It is not tried again
	// by unrelated changes, which would tear the ingress down each time; an explicit retry clears it.
	ingressFailed *config.Ingress
	problems      map[string]string
	failures      uint64
	latest        *config.Config // the generation last submitted, kept for a retry
	pending       *config.Config
	submitted     uint64
	finished      uint64
	settled       chan struct{} // closed, and replaced, whenever finished advances
	wake          chan struct{}
	started       atomic.Bool
	stopWork      context.CancelFunc
	workDone      chan struct{}
}

func newRoleRuntime(deps gatewayDeps, stateDir string, fatal func(error)) *roleRuntime {
	r := &roleRuntime{deps: deps, stateDir: stateDir, fatal: fatal, problems: map[string]string{}, settled: make(chan struct{}), wake: make(chan struct{}, 1)}
	r.deps.serveFailed = func(err error) { r.serveFailed("gateway", "网关", err) }
	return r
}

// configureIngress gives the runtime what it needs to run the ingress.
func (r *roleRuntime) configureIngress(d ingressDeps, forced bool) {
	d.gatewayDeps = r.deps
	d.serveFailed = func(err error) { r.serveFailed("ingress", "入口", err) }
	r.ingressDeps, r.forceIngress = d, forced
}

// serveFailed handles an accept loop that ended by itself. While the node starts that ends the
// process, as it always has; afterwards it is a problem to report, not a reason to stop the node.
func (r *roleRuntime) serveFailed(section, name string, err error) {
	if !r.started.Load() {
		r.fatal(err)
		return
	}
	r.record(map[string]string{section: fmt.Sprintf("%s监听中断：%v", name, err)})
}

// startGateway starts the local gateway as the node starts. Its failure ends the start.
func (r *roleRuntime) startGateway(cfg *config.Config) error {
	created, err := control.EnsureDefaultPAT(r.deps.store, r.stateDir)
	if err != nil {
		return fmt.Errorf("无法创建本机 Gateway 令牌: %w", err)
	}
	if created {
		fmt.Fprintln(os.Stderr, "gateway: 已创建本机 Gateway 令牌（PAT default），令牌文件在状态目录的 pats 下")
	}
	role, err := startGatewayRole(r.deps, cfg)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.gateway = role
	r.mu.Unlock()
	return nil
}

// startIngress starts the ingress as the node starts. Its failure ends the start.
func (r *roleRuntime) startIngress(cfg *config.Config) error {
	role, err := startIngressRole(r.ingressDeps, cfg)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.ingress = role
	r.mu.Unlock()
	return nil
}

// begin starts the worker. Generations submitted before this are applied first, the latest of them.
func (r *roleRuntime) begin(ctx context.Context) {
	ctx, r.stopWork = context.WithCancel(ctx)
	r.workDone = make(chan struct{})
	r.started.Store(true)
	go func() {
		defer close(r.workDone)
		r.work(ctx)
	}()
}

// submit hands the worker the generation now in effect. It never blocks.
func (r *roleRuntime) submit(cfg *config.Config) {
	r.mu.Lock()
	r.latest, r.pending = cfg, cfg
	r.submitted++
	r.mu.Unlock()
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *roleRuntime) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		}
		r.mu.Lock()
		cfg, upTo := r.pending, r.submitted
		r.pending = nil
		r.mu.Unlock()
		if cfg == nil {
			continue
		}
		r.record(r.apply(cfg))
		r.mu.Lock()
		r.finished = upTo
		close(r.settled)
		r.settled = make(chan struct{})
		r.mu.Unlock()
	}
}

// apply brings the roles to what cfg asks for and returns, per role, why it could not.
func (r *roleRuntime) apply(cfg *config.Config) map[string]string {
	out := map[string]string{"gateway": "", "ingress": ""}
	r.mu.Lock()
	role, ingress := r.gateway, r.ingress
	r.mu.Unlock()
	switch {
	case cfg.HasRole("gateway") && role == nil:
		if err := r.startGateway(cfg); err != nil {
			out["gateway"] = fmt.Sprintf("网关没能启动：%v", err)
		}
	case !cfg.HasRole("gateway") && role != nil:
		r.mu.Lock()
		r.gateway = nil
		r.mu.Unlock()
		role.stop()
	case role != nil:
		if err := role.rebind(cfg.Gateway.Listen); err != nil {
			out["gateway"] = fmt.Sprintf("网关没能改到监听地址 %s，仍在 %s 上：%v", cfg.Gateway.Listen, role.bindAddress(), err)
		}
	}
	wantIngress := cfg.HasRole("ingress") || r.forceIngress
	switch {
	case wantIngress && ingress == nil:
		if err := r.startIngress(cfg); err != nil {
			out["ingress"] = fmt.Sprintf("入口没能启动：%v", err)
		}
	case !wantIngress && ingress != nil:
		r.mu.Lock()
		r.ingress = nil
		r.mu.Unlock()
		ingress.stop()
	case ingress != nil && !reflect.DeepEqual(ingress.cfg.Ingress, cfg.Ingress):
		r.mu.Lock()
		failed, problem := r.ingressFailed, r.problems["ingress"]
		r.mu.Unlock()
		if failed != nil && reflect.DeepEqual(*failed, cfg.Ingress) {
			out["ingress"] = problem
			break
		}
		err := r.restartIngress(ingress, cfg)
		r.mu.Lock()
		if err != nil {
			section := cfg.Ingress
			r.ingressFailed = &section
			out["ingress"] = err.Error()
		} else {
			r.ingressFailed = nil
		}
		r.mu.Unlock()
	}
	r.mu.Lock()
	if r.ingress == nil || reflect.DeepEqual(r.ingress.cfg.Ingress, cfg.Ingress) {
		r.ingressFailed = nil
	}
	r.mu.Unlock()
	return out
}

// restartIngress moves the running ingress to the section in cfg. The new section is checked before
// the old ingress is touched. The old one then has to be stopped first, because the new one may need
// the very port it holds; if the new one still cannot start, the old section is put back.
func (r *roleRuntime) restartIngress(old *ingressRole, cfg *config.Config) error {
	if err := validateIngress(r.ingressDeps, cfg); err != nil {
		return fmt.Errorf("入口没能改用新的配置，仍按原来的配置运行：%v", err)
	}
	r.mu.Lock()
	r.ingress = nil
	r.mu.Unlock()
	old.stop()
	next, err := startIngressRole(r.ingressDeps, cfg)
	if err == nil {
		r.mu.Lock()
		r.ingress = next
		r.mu.Unlock()
		return nil
	}
	back, e := startIngressRole(r.ingressDeps, old.cfg)
	if e != nil {
		return fmt.Errorf("入口没能改用新的配置：%v；原来的配置也没能恢复，入口已停止：%v", err, e)
	}
	r.mu.Lock()
	r.ingress = back
	r.mu.Unlock()
	return fmt.Errorf("入口没能改用新的配置，仍按原来的配置运行：%v", err)
}

// record stores the outcome of an apply: a role named with a message is failing, a role named with
// an empty one is fine now. A failure that was not there before is counted and audited once.
func (r *roleRuntime) record(outcome map[string]string) {
	r.mu.Lock()
	fresh := false
	for section, message := range outcome {
		if message == "" {
			delete(r.problems, section)
			continue
		}
		if r.problems[section] != message {
			r.failures++
			fresh = true
		}
		r.problems[section] = message
	}
	r.mu.Unlock()
	if fresh && r.deps.log != nil {
		_ = r.deps.log.Write(map[string]any{"time": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"), "event": "policy_reload_failed", "caller": "config/internal", "device": r.deps.localPeer.Name, "tool": "config.reload", "status": "error", "error": "internal", "duration_ms": 0})
	}
}

// Settle waits until every generation submitted so far has been applied, and returns what could not
// be. With retry it first applies the latest generation again when something failed, so a bind that
// failed because the port was busy can be tried once the port is free.
func (r *roleRuntime) Settle(ctx context.Context, retry bool) []v1.ApplyError {
	if retry {
		r.mu.Lock()
		again := len(r.problems) > 0 && r.pending == nil && r.latest != nil
		if again {
			r.ingressFailed = nil
			r.pending = r.latest
			r.submitted++
		}
		r.mu.Unlock()
		if again {
			select {
			case r.wake <- struct{}{}:
			default:
			}
		}
	}
	for {
		r.mu.Lock()
		if r.finished >= r.submitted || !r.started.Load() {
			r.mu.Unlock()
			break
		}
		ch := r.settled
		r.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return r.applyErrors()
		}
	}
	return r.applyErrors()
}

func (r *roleRuntime) applyErrors() []v1.ApplyError {
	if r == nil {
		return []v1.ApplyError{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]v1.ApplyError, 0, len(r.problems))
	for section, message := range r.problems {
		out = append(out, v1.ApplyError{Section: section, Message: message})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Section < out[j].Section })
	return out
}

// problemMessages are the failures to put roles into effect, for the status warnings.
func (r *roleRuntime) problemMessages() []string {
	var out []string
	for _, p := range r.applyErrors() {
		out = append(out, p.Message)
	}
	return out
}

func (r *roleRuntime) failureCount() uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failures
}

// gatewayListen is the gateway.listen the local gateway serves now; ok is false when it is not running.
func (r *roleRuntime) gatewayListen() (listen string, ok bool) {
	if r == nil {
		return "", false
	}
	r.mu.Lock()
	role := r.gateway
	r.mu.Unlock()
	if role == nil {
		return "", false
	}
	return role.bindAddress(), true
}

// ingressAdmin is the running ingress as the control channel manages it; nil while none runs.
func (r *roleRuntime) ingressAdmin() control.IngressAdmin {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ingress == nil {
		return nil
	}
	return r.ingress.admin
}

// ingressConfig is the ingress section the running ingress was started from; ok is false when none runs.
func (r *roleRuntime) ingressConfig() (section config.Ingress, ok bool) {
	if r == nil {
		return config.Ingress{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ingress == nil {
		return config.Ingress{}, false
	}
	return r.ingress.cfg.Ingress, true
}

// close stops the worker, after the apply it is in the middle of, and then the roles.
func (r *roleRuntime) close() {
	if r.stopWork != nil {
		r.stopWork()
		<-r.workDone
	}
	r.mu.Lock()
	role, ingress := r.gateway, r.ingress
	r.gateway, r.ingress = nil, nil
	r.mu.Unlock()
	if ingress != nil {
		ingress.stop()
	}
	if role != nil {
		role.stop()
	}
}
