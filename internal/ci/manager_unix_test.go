//go:build !windows

package ci

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	v1 "github.com/tzf1003/xmesh/api/control/v1"
	"github.com/tzf1003/xmesh/internal/config"
)

const fakeConfigSh = `#!/bin/sh
if [ "$1" = remove ]; then echo "$ACTIONS_RUNNER_INPUT_TOKEN" >> "$CI_REMOVED_LOG"; rm -f .runner .credentials; exit 0; fi
echo "$@" > config-args
env | grep ACTIONS_RUNNER_INPUT_TOKEN= > config-env
if [ "$ACTIONS_RUNNER_INPUT_TOKEN" = bad ]; then echo "Http response code: Unauthorized from 'POST https://api.github.com/actions/runner-registration'"; exit 1; fi
name=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in --name) name="$2"; shift;; --url) url="$2"; shift;; esac
  shift
done
printf '{"agentName":"%s","gitHubUrl":"%s","workFolder":"_work"}' "$name" "$url" > .runner
echo done > .credentials
`

const fakeRunSh = `#!/bin/sh
trap 'exit 0' TERM INT
mode=idle; [ -f mode ] && mode=$(cat mode)
echo "runs" >> runs
case "$mode" in
  crash) echo "√ Connected to GitHub"; echo "Listening for Jobs"; exit 2;;
  conflict) echo "A session for this runner already exists."; exit 5;;
  removed) echo "The runner registration has been deleted from the server, please re-configure."; exit 1;;
  updating) rm -f mode; exit 3;;
esac
echo "√ Connected to GitHub"
echo "2026-10-18 09:00:00Z: Listening for Jobs"
while :; do
  if [ -f startjob ]; then
    rm -f startjob
    echo "2026-10-18 09:00:01Z: Running job: build"
    while [ ! -f finishjob ]; do sleep 0.05; done
    rm -f finishjob
    echo "2026-10-18 09:00:02Z: Job build completed with result: Succeeded"
    echo "2026-10-18 09:00:02Z: Listening for Jobs"
  fi
  sleep 0.05
done
`

type rig struct {
	t       *testing.T
	m       *Manager
	cfg     config.CI
	mu      sync.Mutex
	root    string
	src     *fakeSource
	awake   atomic.Int32 // sleep inhibitions currently held
	holds   atomic.Int32 // inhibitions ever taken
	release func()
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{t: t, root: t.TempDir()}
	r.src = newFakeSource(t, "2.329.0", map[string]string{"!config.sh": fakeConfigSh, "!run.sh": fakeRunSh})
	cache := &Cache{Dir: filepath.Join(r.root, "ci", "cache"), Source: r.src, Platform: "linux-x64"}
	r.cfg = config.CI{PreventSleepWhileBusy: true, StopTimeout: "1m"}
	r.m = New(Options{
		StateDir: r.root, Node: "node", Cache: cache,
		Config:      func() config.CI { r.mu.Lock(); defer r.mu.Unlock(); return r.cfg },
		Backoff:     func(int) time.Duration { return 20 * time.Millisecond },
		StableAfter: time.Hour,
		Inhibit: func() func() {
			r.awake.Add(1)
			r.holds.Add(1)
			return func() { r.awake.Add(-1) }
		},
		Env:       func() []string { return os.Environ() },
		RunningIn: func(string) bool { return false },
	})
	t.Cleanup(func() { r.m.Shutdown(5 * time.Second) })
	return r
}

func (r *rig) apply(bindings ...config.CIBinding) {
	r.t.Helper()
	r.mu.Lock()
	r.cfg.Bindings = bindings
	cfg := r.cfg
	r.mu.Unlock()
	r.m.Apply(cfg)
}

func tokenBinding(id, target string, concurrency int) config.CIBinding {
	return config.CIBinding{ID: id, Scope: "repo", Target: target, Source: "token", Concurrency: concurrency}
}

func (r *rig) wait(what string, cond func() bool) {
	r.t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.t.Fatalf("timed out waiting for %s; state: %s", what, r.dump())
}

func (r *rig) dump() string {
	var parts []string
	for _, b := range r.m.Bindings() {
		for _, in := range b.Instances {
			problem := ""
			if in.Problem != nil {
				problem = string(in.Problem.Code) + ":" + in.Problem.Message
			}
			parts = append(parts, fmt.Sprintf("%s/%d=%s restarts=%d %s", b.ID, in.Index, in.State, in.Restarts, problem))
		}
	}
	return strings.Join(parts, "; ")
}

func (r *rig) instance(id string, index int) v1.CiInstance {
	b, ok := r.m.Binding(id)
	if !ok {
		return v1.CiInstance{}
	}
	for _, in := range b.Instances {
		if in.Index == index {
			return in
		}
	}
	return v1.CiInstance{}
}

func (r *rig) state(id string, index int) v1.CiInstanceState { return r.instance(id, index).State }

func (r *rig) dirOf(id string, index int) string {
	return filepath.Join(r.root, "ci", id, fmt.Sprint(index))
}

func (r *rig) write(path, content string) {
	r.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		r.t.Fatal(err)
	}
}

func (r *rig) read(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func (r *rig) exists(path string) bool { _, err := os.Stat(path); return err == nil }

// conformance: CTR-CTL-15
func Test_CTRCTL15_BindingInstallsRegistersAndRuns(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("xmesh", "REGTOKEN123")
	r.apply(tokenBinding("xmesh", "tzf1003/xmesh", 1))
	r.wait("idle", func() bool { return r.state("xmesh", 1) == v1.CiIdle })

	dir := r.dirOf("xmesh", 1)
	args := r.read(filepath.Join(dir, "config-args"))
	for _, want := range []string{"--unattended", "--url https://github.com/tzf1003/xmesh", "--name xmesh-node-xmesh-1", "--labels xmesh,xmesh-node", "--work _work", "--replace"} {
		if !strings.Contains(args, want) {
			t.Errorf("config.sh arguments %q lack %q", args, want)
		}
	}
	if strings.Contains(args, "REGTOKEN123") {
		t.Error("the registration token appeared in the command line")
	}
	if !strings.Contains(r.read(filepath.Join(dir, "config-env")), "ACTIONS_RUNNER_INPUT_TOKEN=REGTOKEN123") {
		t.Error("the registration token did not reach config.sh through the environment")
	}
	in := r.instance("xmesh", 1)
	if in.PID == 0 || in.Name != "xmesh-node-xmesh-1" {
		t.Errorf("instance = %+v", in)
	}
	summary := r.m.Summary()
	if summary == nil || len(summary.Bindings) != 1 || summary.Bindings[0].Dir != filepath.Join(r.root, "ci", "xmesh") {
		t.Errorf("summary = %+v", summary)
	}
}

func Test_CTRCTL15_ConcurrencyAddsAndRemovesInstances(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOK")
	r.apply(tokenBinding("a", "o/a", 2))
	r.wait("two idle", func() bool { return r.state("a", 1) == v1.CiIdle && r.state("a", 2) == v1.CiIdle })
	if r.src.fetches != 1 {
		t.Errorf("the package was fetched %d times for two instances", r.src.fetches)
	}
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("second instance gone", func() bool { b, _ := r.m.Binding("a"); return len(b.Instances) == 1 && !r.exists(r.dirOf("a", 2)) })
	if r.state("a", 1) != v1.CiIdle {
		t.Errorf("the remaining instance was disturbed: %s", r.dump())
	}
}

func Test_CTRCTL15_JobStatesAndSleepInhibition(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOK")
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	dir := r.dirOf("a", 1)

	r.write(filepath.Join(dir, "startjob"), "")
	r.wait("busy", func() bool { return r.state("a", 1) == v1.CiBusy })
	if job := r.instance("a", 1).Job; job == nil || job.Name != "build" {
		t.Errorf("job = %+v", job)
	}
	if r.awake.Load() != 1 {
		t.Errorf("the machine was not kept awake while a job ran (%d)", r.awake.Load())
	}
	r.write(filepath.Join(dir, "finishjob"), "")
	r.wait("idle again", func() bool { return r.state("a", 1) == v1.CiIdle })
	in := r.instance("a", 1)
	if in.LastResult == nil || in.LastResult.Name != "build" || in.LastResult.Result != v1.CiSucceeded || in.Job != nil {
		t.Errorf("after the job: %+v", in)
	}
	r.wait("sleep allowed again", func() bool { return r.awake.Load() == 0 })
}

func Test_CTRCTL15_CrashedRunnerIsRestarted(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOK")
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	dir := r.dirOf("a", 1)
	r.write(filepath.Join(dir, "mode"), "crash")
	// Kill the idle listener so the crash script is what starts next.
	if err := r.m.Restart("a"); err != nil {
		t.Fatal(err)
	}
	r.wait("crash loop counted", func() bool { return r.instance("a", 1).Restarts >= 2 })
	if p := r.instance("a", 1).Problem; p != nil && p.Code != v1.CiProblemUnclassified {
		t.Errorf("problem = %+v", p)
	}
	r.write(filepath.Join(dir, "mode"), "idle")
	r.wait("idle after recovery", func() bool { return r.state("a", 1) == v1.CiIdle && r.instance("a", 1).Problem == nil })
}

func Test_CTRCTL15_SelfUpdateExitRestartsAtOnce(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOK")
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	dir := r.dirOf("a", 1)
	r.write(filepath.Join(dir, "mode"), "updating")
	_ = r.m.Restart("a")
	r.wait("restarted after the update exit", func() bool {
		return strings.Count(r.read(filepath.Join(dir, "runs")), "runs") >= 3 && r.state("a", 1) == v1.CiIdle
	})
	if p := r.instance("a", 1).Problem; p != nil {
		t.Errorf("an update exit is not a problem: %+v", p)
	}
}

func Test_CTRCTL15_SessionConflictNeedsTheUser(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOK")
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	dir := r.dirOf("a", 1)
	r.write(filepath.Join(dir, "mode"), "conflict")
	_ = r.m.Restart("a")
	r.wait("broken", func() bool { return r.state("a", 1) == v1.CiBroken })
	if p := r.instance("a", 1).Problem; p == nil || p.Code != v1.CiProblemSessionConflict {
		t.Fatalf("problem = %+v", p)
	}
	r.write(filepath.Join(dir, "mode"), "idle")
	if err := r.m.Restart("a"); err != nil {
		t.Fatal(err)
	}
	r.wait("idle after restart", func() bool { return r.state("a", 1) == v1.CiIdle && r.instance("a", 1).Problem == nil })
}

func Test_CTRCTL15_RemovedRunnerIsRegisteredAgain(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "FIRST")
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	dir := r.dirOf("a", 1)
	r.write(filepath.Join(dir, "mode"), "removed")
	_ = r.m.Restart("a")
	r.wait("broken", func() bool { return r.state("a", 1) == v1.CiBroken })
	if p := r.instance("a", 1).Problem; p == nil || p.Code != v1.CiProblemRunnerRemoved {
		t.Fatalf("problem = %+v", p)
	}
	r.write(filepath.Join(dir, "mode"), "idle")
	if err := r.Repair("a", "SECOND"); err != nil {
		t.Fatal(err)
	}
	r.wait("idle after repair", func() bool { return r.state("a", 1) == v1.CiIdle })
	if !strings.Contains(r.read(filepath.Join(dir, "config-env")), "SECOND") {
		t.Errorf("repair did not register with the new token: %q", r.read(filepath.Join(dir, "config-env")))
	}
}

func (r *rig) Repair(id, token string) error { return r.m.Repair(id, token) }

func Test_CTRCTL15_RegistrationFailureNamesTheCauseWithoutTheToken(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "bad")
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("broken", func() bool { return r.state("a", 1) == v1.CiBroken })
	p := r.instance("a", 1).Problem
	if p == nil || p.Code != v1.CiProblemRegistrationFailed || !strings.Contains(p.Message, "无效或已过期") {
		t.Fatalf("problem = %+v", p)
	}
	if strings.Contains(p.Message, "bad") {
		t.Errorf("the message carries the token: %q", p.Message)
	}
	// A new token and a repair get it going.
	if err := r.m.Repair("a", "good"); err != nil {
		t.Fatal(err)
	}
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
}

func Test_CTRCTL15_NoTokenMeansNoRegistration(t *testing.T) {
	r := newRig(t)
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("broken", func() bool { return r.state("a", 1) == v1.CiBroken })
	if p := r.instance("a", 1).Problem; p == nil || p.Code != v1.CiProblemRegistrationFailed || !strings.Contains(p.Message, "注册令牌") {
		t.Fatalf("problem = %+v", p)
	}
}

func Test_CTRCTL15_PauseAndDrainStopIdleRunnersAndWaitForJobs(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOK")
	r.apply(tokenBinding("a", "o/a", 2))
	r.wait("two idle", func() bool { return r.state("a", 1) == v1.CiIdle && r.state("a", 2) == v1.CiIdle })
	r.write(filepath.Join(r.dirOf("a", 1), "startjob"), "")
	r.wait("first busy", func() bool { return r.state("a", 1) == v1.CiBusy })

	r.m.SetDraining(true)
	r.wait("idle one stopped", func() bool { return r.state("a", 2) == v1.CiStopped })
	time.Sleep(200 * time.Millisecond)
	if r.state("a", 1) != v1.CiBusy {
		t.Fatalf("a running job must not be cut by a drain: %s", r.dump())
	}
	if s := r.m.Summary(); s == nil || !s.Draining {
		t.Errorf("summary = %+v", s)
	}
	r.write(filepath.Join(r.dirOf("a", 1), "finishjob"), "")
	r.wait("busy one stopped after its job", func() bool { return r.state("a", 1) == v1.CiStopped })
	if got := r.instance("a", 1).LastResult; got == nil || got.Result != v1.CiSucceeded {
		t.Errorf("the job's result was lost: %+v", got)
	}

	r.m.SetDraining(false)
	r.wait("both back", func() bool { return r.state("a", 1) == v1.CiIdle && r.state("a", 2) == v1.CiIdle })

	spec := tokenBinding("a", "o/a", 2)
	spec.Paused = true
	r.apply(spec)
	r.wait("paused", func() bool { return r.state("a", 1) == v1.CiStopped && r.state("a", 2) == v1.CiStopped })
	spec.Paused = false
	r.apply(spec)
	r.wait("resumed", func() bool { return r.state("a", 1) == v1.CiIdle && r.state("a", 2) == v1.CiIdle })
}

func Test_CTRCTL15_RemovingABindingWaitsForItsJobThenDeletesIt(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOK")
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	dir := r.dirOf("a", 1)
	r.write(filepath.Join(dir, "startjob"), "")
	r.wait("busy", func() bool { return r.state("a", 1) == v1.CiBusy })

	r.apply()
	b, ok := r.m.Binding("a")
	if !ok || !b.Removing {
		t.Fatalf("a binding that left the configuration must show as removing: %+v %v", b, ok)
	}
	time.Sleep(200 * time.Millisecond)
	if r.state("a", 1) != v1.CiBusy {
		t.Fatalf("the job was cut: %s", r.dump())
	}
	r.write(filepath.Join(dir, "finishjob"), "")
	r.wait("binding gone", func() bool { return r.m.Gone("a") })
	if r.exists(filepath.Join(r.root, "ci", "a")) {
		t.Error("the runner directory was left behind")
	}
}

func Test_CTRCTL15_ForcedRemovalDoesNotWait(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOK")
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	r.write(filepath.Join(r.dirOf("a", 1), "startjob"), "")
	r.wait("busy", func() bool { return r.state("a", 1) == v1.CiBusy })
	r.m.ForgetForce("a")
	r.apply()
	r.wait("binding gone without waiting", func() bool { return r.m.Gone("a") })
}

func Test_CTRCTL15_ImportedRunnerIsRunInPlaceAndKept(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.root, "actions-runner")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	r.write(filepath.Join(dir, "run.sh"), fakeRunSh)
	if err := os.Chmod(filepath.Join(dir, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.write(filepath.Join(dir, ".runner"), "\xef\xbb\xbf"+`{"agentName":"my-mac","gitHubUrl":"https://github.com/tzf1003/legacy","workFolder":"_work"}`)

	got, err := r.m.Inspect(dir)
	if err != nil || got.Target != "tzf1003/legacy" || got.RunnerName != "my-mac" || got.ServiceInstalled || got.Running || got.Host != "github.com" {
		t.Fatalf("Inspect = %+v, %v", got, err)
	}
	r.write(filepath.Join(dir, ".service"), "actions.runner.x")
	if got, _ = r.m.Inspect(dir); !got.ServiceInstalled || !strings.Contains(got.ServiceHint, "svc.sh") {
		t.Errorf("an installed official service must be reported with its removal command: %+v", got)
	}
	_ = os.Remove(filepath.Join(dir, ".service"))

	spec := config.CIBinding{ID: "legacy", Scope: "repo", Target: "tzf1003/legacy", Source: "import", Concurrency: 1, Dir: dir}
	r.apply(spec)
	r.wait("idle", func() bool { return r.state("legacy", 1) == v1.CiIdle })
	if r.instance("legacy", 1).Name != "my-mac" {
		t.Errorf("an imported runner keeps its own name: %+v", r.instance("legacy", 1))
	}
	if r.exists(filepath.Join(dir, "config-args")) || r.src.fetches != 0 {
		t.Error("an imported runner must not be installed or registered again")
	}
	r.apply()
	r.wait("binding gone", func() bool { return r.m.Gone("legacy") })
	if !r.exists(filepath.Join(dir, ".runner")) {
		t.Error("the user's runner directory must be left alone")
	}
}

func Test_CTRCTL15_InspectRefusesWhatCannotBeHosted(t *testing.T) {
	r := newRig(t)
	dir := t.TempDir()
	if _, err := r.m.Inspect(dir); err != ErrNotARunner {
		t.Errorf("an empty directory: %v", err)
	}
	if _, err := r.m.Inspect("relative/dir"); err == nil {
		t.Error("a relative path was accepted")
	}
	r.write(filepath.Join(dir, ".runner"), `{"agentName":"org","gitHubUrl":"https://github.com/some-org"}`)
	if _, err := r.m.Inspect(dir); err == nil || !strings.Contains(err.Error(), "组织") {
		t.Errorf("an organization runner: %v", err)
	}
	r.write(filepath.Join(dir, ".runner"), `{"agentName":"x","gitHubUrl":"https://github.com/o/r","ephemeral":true}`)
	if got, err := r.m.Inspect(dir); err != nil || !got.Ephemeral {
		t.Errorf("an ephemeral runner is reported as such: %+v %v", got, err)
	}
}

func Test_CTRCTL15_ShutdownWaitsForJobsThenEndsThem(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOK")
	r.apply(tokenBinding("a", "o/a", 1))
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	r.write(filepath.Join(r.dirOf("a", 1), "startjob"), "")
	r.wait("busy", func() bool { return r.state("a", 1) == v1.CiBusy })

	done := make(chan struct{})
	go func() { r.m.Shutdown(300 * time.Millisecond); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not end a job that outlived the timeout")
	}
	if r.awake.Load() != 0 {
		t.Error("the sleep inhibition outlived the manager")
	}
}

func Test_CTRCTL15_ChangedLabelsRegisterAgain(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOK")
	spec := tokenBinding("a", "o/a", 1)
	r.apply(spec)
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	dir := r.dirOf("a", 1)
	spec.Labels = []string{"gpu"}
	r.apply(spec)
	r.wait("registered with the new labels", func() bool {
		return strings.Contains(r.read(filepath.Join(dir, "config-args")), "xmesh,xmesh-node,gpu") && r.state("a", 1) == v1.CiIdle
	})
}

// conformance: CTR-CTL-15
func Test_CTRCTL15_AccountBindingGetsItsTokenFromGitHubAndDeregistersOnUnbind(t *testing.T) {
	removed := filepath.Join(t.TempDir(), "removed")
	t.Setenv("CI_REMOVED_LOG", removed)
	r := newRig(t)
	var asked atomic.Int32
	r.m.o.Token = func(_ context.Context, b config.CIBinding) (string, error) {
		asked.Add(1)
		return "REG-" + b.Account, nil
	}
	r.m.o.RemoveToken = func(_ context.Context, b config.CIBinding) (string, error) { return "REMOVE-" + b.Account, nil }
	spec := config.CIBinding{ID: "a", Scope: "repo", Target: "o/a", Source: "account", Account: "me", Concurrency: 1}
	r.apply(spec)
	r.wait("idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	env, err := os.ReadFile(filepath.Join(r.dirOf("a", 1), "config-env"))
	if err != nil || !strings.Contains(string(env), "REG-me") {
		t.Fatalf("registered with %q (%v)", env, err)
	}
	if asked.Load() != 1 {
		t.Fatalf("token asked %d times", asked.Load())
	}
	// Restarting does not ask again: the runner stays registered.
	if err = r.m.Restart("a"); err != nil {
		t.Fatal(err)
	}
	r.wait("idle again", func() bool { return r.state("a", 1) == v1.CiIdle })
	if asked.Load() != 1 {
		t.Fatalf("token asked %d times after restart", asked.Load())
	}

	r.apply()
	r.wait("binding gone", func() bool { return r.m.Gone("a") })
	log, err := os.ReadFile(removed)
	if err != nil || strings.TrimSpace(string(log)) != "REMOVE-me" {
		t.Fatalf("removal log = %q (%v)", log, err)
	}
}

// conformance: CTR-CTL-15
func Test_CTRCTL15_ImportedRunnerIsNotDeregisteredOnUnbind(t *testing.T) {
	removed := filepath.Join(t.TempDir(), "removed")
	t.Setenv("CI_REMOVED_LOG", removed)
	r := newRig(t)
	r.m.o.RemoveToken = func(context.Context, config.CIBinding) (string, error) { return "REMOVE", nil }
	dir := t.TempDir()
	r.write(filepath.Join(dir, ".runner"), `{"agentName":"old","gitHubUrl":"https://github.com/o/a","workFolder":"_work"}`)
	r.write(filepath.Join(dir, "config.sh"), fakeConfigSh)
	r.write(filepath.Join(dir, "run.sh"), fakeRunSh)
	for _, f := range []string{"config.sh", "run.sh"} {
		_ = os.Chmod(filepath.Join(dir, f), 0o755)
	}
	r.apply(config.CIBinding{ID: "old", Scope: "repo", Target: "o/a", Source: "import", Account: "me", Dir: dir, Concurrency: 1})
	r.wait("idle", func() bool { return r.state("old", 1) == v1.CiIdle })
	r.apply()
	r.wait("binding gone", func() bool { return r.m.Gone("old") })
	if _, err := os.Stat(removed); err == nil {
		t.Fatal("an imported runner was removed from GitHub")
	}
	if _, err := os.Stat(filepath.Join(dir, ".runner")); err != nil {
		t.Fatalf("the imported runner was changed: %v", err)
	}
}

func Test_ScaleDownUpWaitsForOldInstanceCleanup(t *testing.T) {
	r := newRig(t)
	r.m.HoldToken("a", "TOKEN")
	r.apply(tokenBinding("a", "o/a", 2))
	r.wait("both idle", func() bool { return r.state("a", 1) == v1.CiIdle && r.state("a", 2) == v1.CiIdle })
	dir := r.dirOf("a", 2)
	r.write(filepath.Join(dir, "startjob"), "")
	r.wait("second busy", func() bool { return r.state("a", 2) == v1.CiBusy })
	old := r.instance("a", 2).PID
	r.apply(tokenBinding("a", "o/a", 1))
	r.apply(tokenBinding("a", "o/a", 2))
	if current := r.instance("a", 2).PID; current != old {
		t.Fatal("reused directory before prior process stopped", old, current)
	}
	r.write(filepath.Join(dir, "finishjob"), "")
	r.wait("replacement idle", func() bool { in := r.instance("a", 2); return in.State == v1.CiIdle && in.PID != 0 && in.PID != old })
	if !r.exists(filepath.Join(dir, ".runner")) {
		t.Fatal("replacement directory removed")
	}
}

func Test_SleepInhibitionTracksStoppingProcessAndConfig(t *testing.T) {
	r := newRig(t)
	i := &instance{m: r.m, snap: v1.CiInstance{State: v1.CiIdle}, wakeC: make(chan struct{}, 1)}
	i.onLine("Running job: build")
	if r.awake.Load() != 1 {
		t.Fatal("job did not inhibit")
	}
	i.setState(v1.CiStopping, nil)
	if r.awake.Load() != 1 {
		t.Fatal("stopping released sleep before process exit")
	}
	r.mu.Lock()
	r.cfg.PreventSleepWhileBusy = false
	cfg := r.cfg
	r.mu.Unlock()
	r.m.Apply(cfg)
	if r.awake.Load() != 0 {
		t.Fatal("configuration did not release sleep")
	}
	r.mu.Lock()
	r.cfg.PreventSleepWhileBusy = true
	cfg = r.cfg
	r.mu.Unlock()
	r.m.Apply(cfg)
	if r.awake.Load() != 1 {
		t.Fatal("configuration did not reacquire sleep")
	}
	i.setState(v1.CiStopped, nil)
	if r.awake.Load() != 0 {
		t.Fatal("process exit kept sleep inhibition")
	}
}

func Test_AccountRunnersRecoverRemovedAndDeprecatedVersions(t *testing.T) {
	i := &instance{m: &Manager{o: Options{Token: func(context.Context, config.CIBinding) (string, error) { return "", nil }}}, spec: config.CIBinding{Source: "account", Account: "account"}}
	if _, _, broken := i.classify(exitInfo{removed: true}, time.Minute); broken {
		t.Fatal("account runner requires manual registration")
	}
	if !i.rereg {
		t.Fatal("removed runner did not request registration")
	}
	if _, _, broken := i.classify(exitInfo{authFail: true}, time.Minute); broken {
		t.Fatal("expired account registration requires manual recovery")
	}
	if _, _, broken := i.classify(exitInfo{deprec: true}, time.Minute); broken || !i.reinstall {
		t.Fatal("managed deprecated runner did not update")
	}
}

func Test_ImportedRunnerRepairUsesTheNewToken(t *testing.T) {
	r := newRig(t)
	dir := filepath.Join(r.root, "imported")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.sh"), []byte(fakeConfigSh), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte(fakeRunSh), 0700); err != nil {
		t.Fatal(err)
	}
	r.write(filepath.Join(dir, ".runner"), `{"agentName":"existing","gitHubUrl":"https://github.com/o/a","workFolder":"_work"}`)
	r.apply(config.CIBinding{ID: "a", Scope: "repo", Target: "o/a", Source: "import", Dir: dir, Concurrency: 1})
	r.wait("import idle", func() bool { return r.state("a", 1) == v1.CiIdle })
	if err := r.m.Repair("a", "NEW_TOKEN"); err != nil {
		t.Fatal(err)
	}
	r.wait("import registered again", func() bool {
		return strings.Contains(r.read(filepath.Join(dir, "config-env")), "NEW_TOKEN") && r.state("a", 1) == v1.CiIdle
	})
}
