package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Delegate configures agent delegation (CTR-DLG-06). It is hot reloaded: the next call sees the new values,
// and turning Enabled off ends the running tasks.
type Delegate struct {
	Enabled    bool `json:"enabled"`
	AllowWrite bool `json:"allow_write"`
	// AllowSessions is nil when the key is absent, which means false: live sessions are opt-in (CTR-DLG-04).
	AllowSessions *bool `json:"allow_sessions,omitempty"`
	// MaxConcurrent is how many tasks may run at once, at least 1 (the schema enforces it). The file default is 2,
	// and Concurrency falls back to that default for a zero value built in code.
	MaxConcurrent  int                       `json:"max_concurrent"`
	DefaultTimeout string                    `json:"default_timeout"`
	MaxTimeout     string                    `json:"max_timeout"`
	Retain         string                    `json:"retain"`
	Runners        map[string]DelegateRunner `json:"runners"`
}

// DelegateRunner overrides one field of a runner (CTR-DLG-05). A nil list keeps the built-in value, and an
// empty list clears it. A runner name that is not built in must set Cmd.
type DelegateRunner struct {
	Cmd        string   `json:"cmd"`
	Args       []string `json:"args"`
	ReadArgs   []string `json:"read_args"`
	WriteArgs  []string `json:"write_args"`
	ResumeArgs []string `json:"resume_args"`
}

// DelegateBuiltinRunners are the runner names every node has. Their defaults live with the runners.
var DelegateBuiltinRunners = []string{"claude", "codex"}

const (
	DefaultDelegateConcurrency = 2
	DefaultDelegateTimeout     = 30 * time.Minute
	DefaultDelegateMaxTimeout  = 2 * time.Hour
	DefaultDelegateRetain      = time.Hour
)

var delegateRunnerName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// SessionsAllowed reports whether live sessions are listed and may be written to. They are off unless allow_sessions
// is set to true (CTR-DLG-04).
func (d Delegate) SessionsAllowed() bool { return d.AllowSessions != nil && *d.AllowSessions }

// Concurrency is the number of tasks that may run at once.
func (d Delegate) Concurrency() int {
	if d.MaxConcurrent < 1 {
		return DefaultDelegateConcurrency
	}
	return d.MaxConcurrent
}

// DefaultTimeoutDuration is the timeout of a task that names none.
func (d Delegate) DefaultTimeoutDuration() time.Duration {
	return durationOr(d.DefaultTimeout, DefaultDelegateTimeout)
}

// MaxTimeoutDuration caps every task timeout.
func (d Delegate) MaxTimeoutDuration() time.Duration {
	return durationOr(d.MaxTimeout, DefaultDelegateMaxTimeout)
}

// RetainDuration is how long a finished task and its events stay visible.
func (d Delegate) RetainDuration() time.Duration {
	return durationOr(d.Retain, DefaultDelegateRetain)
}

func durationOr(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// setDelegateDefaults fills the keys that have a default before the file is read over them.
func setDelegateDefaults(d *Delegate) {
	d.MaxConcurrent = DefaultDelegateConcurrency
	d.DefaultTimeout = DefaultDelegateTimeout.String()
	d.MaxTimeout = DefaultDelegateMaxTimeout.String()
	d.Retain = DefaultDelegateRetain.String()
}

// validateDelegate checks what the schema cannot: the durations and their order, and the runner entries.
func (c *Config) validateDelegate() error {
	fail := func(p string) error { return fmt.Errorf("配置无效: %s", p) }
	d := c.Agent.Delegate
	if d.MaxConcurrent < 1 {
		return fail("/agent/delegate/max_concurrent")
	}
	for _, check := range []struct{ path, value string }{{"/agent/delegate/default_timeout", d.DefaultTimeout}, {"/agent/delegate/max_timeout", d.MaxTimeout}} {
		if check.value == "" {
			continue
		}
		if v, err := time.ParseDuration(check.value); err != nil || v <= 0 {
			return fail(check.path)
		}
	}
	if d.Retain != "" {
		if v, err := time.ParseDuration(d.Retain); err != nil || v < 0 {
			return fail("/agent/delegate/retain")
		}
	}
	if d.DefaultTimeoutDuration() > d.MaxTimeoutDuration() {
		return fail("/agent/delegate/max_timeout: 不得小于 default_timeout")
	}
	for name, r := range d.Runners {
		p := "/agent/delegate/runners/" + name
		if !delegateRunnerName.MatchString(name) {
			return fail(p + ": 名称只能包含字母、数字、下划线和连字符")
		}
		if strings.ContainsAny(r.Cmd, "\x00\r\n") {
			return fail(p + "/cmd")
		}
		if r.Cmd == "" && !slices.Contains(DelegateBuiltinRunners, name) {
			return fail(p + "/cmd: 新增的运行器必须设置 cmd")
		}
		for _, list := range [][]string{r.Args, r.ReadArgs, r.WriteArgs, r.ResumeArgs} {
			for _, arg := range list {
				if strings.ContainsRune(arg, 0) {
					return fail(p + ": 参数不得包含空字符")
				}
			}
		}
	}
	return nil
}
