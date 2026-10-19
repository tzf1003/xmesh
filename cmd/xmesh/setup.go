package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/kardianos/service"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mesh"
	"github.com/tzf1003/xmesh/internal/onboarding"
)

const setupUsage = `xmesh setup inspect --json
xmesh setup plan --input <file|-> --json
xmesh setup apply --plan <id> --json
xmesh setup status|resume|verify|cancel --job <id> --json
xmesh doctor --json

全部命令支持 --root <状态根目录>。打开桌面应用可使用图形向导。
安装与客户端配置前先生成计划，确认摘要后再 apply。
需要登录或客户端操作时，status 返回 waiting_user 和具体下一步。`

func setupCommand(args []string) error { return setupCommandIO(args, os.Stdin, os.Stdout) }
func setupCommandIO(args []string, in io.Reader, out io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		_, e := fmt.Fprintln(out, setupUsage)
		return e
	}
	op := args[0]
	f := flag.NewFlagSet("setup "+op, flag.ContinueOnError)
	root := f.String("root", "", "状态根目录")
	input := f.String("input", "", "配置输入 JSON 路径，- 表示 stdin")
	plan := f.String("plan", "", "已预览的计划标识")
	job := f.String("job", "", "任务标识")
	machine := f.Bool("json", false, "结构化输出")
	if e := f.Parse(args[1:]); e != nil {
		return writeSetup(out, *machine, nil, &onboarding.Error{Code: "invalid_args", Message: "命令参数无效"})
	}
	if f.NArg() != 0 {
		return writeSetup(out, *machine, nil, &onboarding.Error{Code: "invalid_args", Message: "配置命令不接受位置参数"})
	}
	s, e := onboarding.Open(*root)
	if e != nil {
		return writeSetup(out, *machine, nil, e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var result any
	switch op {
	case "inspect":
		result = s.Inspect(ctx)
	case "doctor":
		result = s.Doctor(ctx)
	case "plan":
		var source io.Reader = in
		if *input == "" {
			e = &onboarding.Error{Code: "invalid_args", Message: "请提供 --input 文件或 -", Field: "input"}
			break
		}
		if *input != "-" {
			var file *os.File
			file, e = os.Open(*input)
			if e != nil {
				e = &onboarding.Error{Code: "input_unreadable", Message: "配置输入文件无法读取"}
				break
			}
			defer file.Close()
			source = file
		}
		d := json.NewDecoder(io.LimitReader(source, 1<<20))
		d.DisallowUnknownFields()
		var value onboarding.Input
		if err := d.Decode(&value); err != nil {
			e = &onboarding.Error{Code: "invalid_args", Message: "配置输入不是有效的协议 JSON"}
			break
		}
		if d.Decode(new(any)) != io.EOF {
			e = &onboarding.Error{Code: "invalid_args", Message: "配置输入包含多余内容"}
			break
		}
		var p *onboarding.Plan
		p, e = s.Plan(value)
		if e == nil {
			result = onboarding.PublicPlan(p)
		}
	case "apply":
		var j *onboarding.Job
		j, e = s.CreateJob(*plan)
		if e == nil {
			e = launchSetupWorker(s.Root, j.ID)
			result = j
		}
	case "status":
		result, e = s.LoadJob(*job)
	case "resume":
		var j *onboarding.Job
		var launch bool
		j, launch, e = s.QueueResume(*job)
		if e == nil && launch {
			e = launchSetupWorker(s.Root, j.ID)
		}
		result = j
	case "verify":
		result, e = s.Verify(ctx, *job, setupHost())
	case "ingress-password":
		if *input != "-" {
			e = &onboarding.Error{Code: "invalid_args", Message: "口令通过 --input - 的 JSON stdin 提供"}
			break
		}
		var value struct {
			Password string `json:"password"`
		}
		d := json.NewDecoder(io.LimitReader(in, 4096))
		d.DisallowUnknownFields()
		if d.Decode(&value) != nil || d.Decode(new(any)) != io.EOF {
			e = &onboarding.Error{Code: "invalid_args", Message: "口令输入格式无效"}
			break
		}
		result, e = s.SetIngressPassword(ctx, *job, value.Password)
	case "cancel":
		result, e = s.Cancel(*job)
	default:
		e = &onboarding.Error{Code: "invalid_args", Message: "未知配置操作"}
	}
	return writeSetup(out, *machine, result, e)
}

func writeSetup(out io.Writer, machine bool, result any, err error) error {
	if !machine {
		if err != nil {
			return err
		}
		b, _ := json.MarshalIndent(result, "", "  ")
		_, e := fmt.Fprintln(out, string(b))
		return e
	}
	if err != nil {
		var typed *onboarding.Error
		if !errors.As(err, &typed) {
			typed = &onboarding.Error{Code: "internal", Message: "配置操作无法完成，请运行 xmesh doctor 检查环境"}
		}
		_ = json.NewEncoder(out).Encode(map[string]any{"ok": false, "error": typed})
		return typed
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true, "data": result})
}

func launchSetupWorker(root, id string) error {
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	cmd := exec.Command(exe, "__setup-run", "--root", root, "--job", id)
	detachSetup(cmd)
	if e = cmd.Start(); e != nil {
		return &onboarding.Error{Code: "worker_unavailable", Message: "无法启动配置任务，请重新打开应用后重试"}
	}
	return cmd.Process.Release()
}

func runSetupWorker(args []string) error {
	f := flag.NewFlagSet("setup worker", flag.ContinueOnError)
	root := f.String("root", "", "")
	job := f.String("job", "", "")
	if e := f.Parse(args); e != nil {
		return e
	}
	s, e := onboarding.Open(*root)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	return s.Run(ctx, *job, setupHost())
}

func setupHost() onboarding.Host {
	runCLI := func(ctx context.Context, args ...string) error {
		exe, e := os.Executable()
		if e != nil {
			return e
		}
		return exec.CommandContext(ctx, exe, args...).Run()
	}
	return onboarding.Host{
		Snapshot: snapshotSetupService,
		Restore:  restoreSetupService,
		Stop: func(ctx context.Context, p *onboarding.Plan) error {
			wait, err := prepareSetupServiceStop(ctx, p)
			if err != nil {
				return err
			}
			if err := runCLI(ctx, "stop", "--config", p.ConfigPath); err != nil {
				return err
			}
			return wait()
		},
		Configure: func(ctx context.Context, p *onboarding.Plan) error {
			if e := runCLI(ctx, "uninstall", "--config", p.ConfigPath); e != nil {
				return e
			}
			args := []string{"install", "--config", p.ConfigPath, "--state-dir", filepath.Dir(p.ConfigPath), "--autostart=" + fmt.Sprint(*p.Input.Autostart)}
			if p.Input.Purpose == "local" {
				args = append(args, "--local-only")
			}
			return runCLI(ctx, args...)
		},
		Installed: func(p *onboarding.Plan) (bool, error) { return setupServiceInstalled(p.Input.NodeName) },
		Install: func(ctx context.Context, p *onboarding.Plan) error {
			args := []string{"install", "--config", p.ConfigPath, "--state-dir", filepath.Dir(p.ConfigPath), "--autostart=" + fmt.Sprint(*p.Input.Autostart)}
			if p.Input.Purpose == "local" {
				args = append(args, "--local-only")
			}
			return runCLI(ctx, args...)
		},
		Start: func(ctx context.Context, p *onboarding.Plan) error {
			startErr := runCLI(ctx, "start", "--config", p.ConfigPath)
			wait, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			for {
				if e := onboarding.Probe(wait, p); e == nil {
					return nil
				}
				select {
				case <-wait.Done():
					if startErr != nil {
						return startErr
					}
					return wait.Err()
				case <-time.After(400 * time.Millisecond):
				}
			}
		},
		Remove: func(ctx context.Context, p *onboarding.Plan) error {
			_ = runCLI(ctx, "stop", "--name", serviceName(p.Input.NodeName))
			return runCLI(ctx, "uninstall", "--name", serviceName(p.Input.NodeName))
		},
		Network: func(ctx context.Context, p *onboarding.Plan, progress func(mesh.LoginProgress) error) (string, error) {
			cfg, e := config.Load(p.ConfigPath)
			if e != nil {
				return "", e
			}
			return mesh.PrepareOnboarding(ctx, cfg, filepath.Join(filepath.Dir(p.ConfigPath), "network"), p.Input.Purpose == "cloud", progress)
		},
		Health:   onboarding.Probe,
		Evidence: onboarding.ClientEvidence,
	}
}

func serviceInstalledWithLibrary(node string) (bool, error) {
	name, e := resolveUserServiceName(serviceName(node))
	if e != nil {
		return false, e
	}
	s, e := service.New(&program{}, &service.Config{Name: name, Option: service.KeyValue{"UserService": true}})
	if e != nil {
		return false, e
	}
	_, e = s.Status()
	if serviceMissing(e) {
		return false, nil
	}
	return e == nil, e
}

func agentHelp(out io.Writer) error {
	version := buildVersion
	if !strings.HasPrefix(version, "v") {
		version = "v" + version
	}
	return json.NewEncoder(out).Encode(map[string]any{"schema_version": 1, "version": buildVersion, "docs": "https://tzf1003.github.io/xmesh/versions/" + version + "/agent/install.md", "capabilities": []string{"setup.inspect", "setup.plan", "setup.apply", "setup.status", "setup.resume", "setup.verify", "setup.cancel", "doctor"}, "help": setupUsage})
}
