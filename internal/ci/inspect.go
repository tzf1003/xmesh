package ci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// runnerFile is the part of a runner's .runner file this package reads.
type runnerFile struct {
	AgentName  string `json:"agentName"`
	GitHubURL  string `json:"gitHubUrl"`
	WorkFolder string `json:"workFolder"`
	Ephemeral  bool   `json:"ephemeral"`
}

// ErrNotARunner means the directory holds no registered runner.
var ErrNotARunner = errors.New("这个目录里没有已注册的运行器（缺少 .runner）")

// ErrOrgRunner means the runner is registered for an organization or enterprise.
var ErrOrgRunner = errors.New("这是组织或企业级运行器，目前只托管仓库级运行器")

// readRunnerFile reads dir/.runner. The runner writes it with a byte order mark on some systems.
func readRunnerFile(dir string) (runnerFile, error) {
	raw, err := os.ReadFile(filepath.Join(dir, ".runner"))
	if errors.Is(err, os.ErrNotExist) {
		return runnerFile{}, ErrNotARunner
	}
	if err != nil {
		return runnerFile{}, err
	}
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))
	var f runnerFile
	if err = json.Unmarshal(raw, &f); err != nil {
		return runnerFile{}, fmt.Errorf("%w: %v", ErrNotARunner, err)
	}
	return f, nil
}

// repoOfURL turns the runner's GitHub URL into host and owner/repo. A runner registered for an
// organization or enterprise has no repository and is refused: only repository runners are hosted.
func repoOfURL(raw string) (host, repo string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", "", fmt.Errorf("运行器的 GitHub 地址无法识别: %q", raw)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("%w（%s）", ErrOrgRunner, raw)
	}
	return strings.ToLower(u.Host), parts[0] + "/" + parts[1], nil
}

// Inspection is what Inspect learns about a directory that holds a runner.
type Inspection struct {
	Host             string
	Target           string
	RunnerName       string
	ServiceInstalled bool
	ServiceHint      string
	Running          bool
	Ephemeral        bool
}

// Inspect reads a runner directory without changing anything.
func (m *Manager) Inspect(dir string) (Inspection, error) {
	if !filepath.IsAbs(dir) {
		return Inspection{}, fmt.Errorf("目录必须是绝对路径")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return Inspection{}, ErrNotARunner
	}
	f, err := readRunnerFile(dir)
	if err != nil {
		return Inspection{}, err
	}
	host, repo, err := repoOfURL(f.GitHubURL)
	if err != nil {
		return Inspection{}, err
	}
	out := Inspection{Host: host, Target: repo, RunnerName: f.AgentName, Ephemeral: f.Ephemeral}
	if _, err = os.Stat(filepath.Join(dir, ".service")); err == nil {
		out.ServiceInstalled = true
		out.ServiceHint = serviceHint(dir)
	}
	running := m.o.RunningIn
	if running == nil {
		running = listenerRunningIn
	}
	out.Running = running(dir)
	return out, nil
}

// serviceHint is the command that removes the official runner service of dir.
func serviceHint(dir string) string {
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`cd "%s" && .\config.cmd remove`, dir)
	}
	if runtime.GOOS == "darwin" {
		return fmt.Sprintf(`cd "%s" && ./svc.sh stop && ./svc.sh uninstall`, dir)
	}
	return fmt.Sprintf(`cd "%s" && sudo ./svc.sh stop && sudo ./svc.sh uninstall`, dir)
}

// listenerRunningIn reports whether a Runner.Listener started from dir is running. The listener
// is always started by path, so its command line (or image path) names the directory.
func listenerRunningIn(dir string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	needle := filepath.Join(dir, "bin", "Runner.Listener")
	var out []byte
	var err error
	if runtime.GOOS == "windows" {
		out, err = exec.CommandContext(ctx, "powershell", "-NoProfile", "-Command",
			"Get-Process Runner.Listener -ErrorAction SilentlyContinue | ForEach-Object { $_.Path }").Output()
		needle = strings.ToLower(needle)
		out = bytes.ToLower(out)
	} else {
		out, err = exec.CommandContext(ctx, "ps", "-axww", "-o", "command=").Output()
	}
	return err == nil && bytes.Contains(out, []byte(needle))
}
