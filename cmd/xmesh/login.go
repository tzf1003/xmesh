package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mesh"
)

func login(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	file := fs.String("config", "xmesh.yaml", "配置路径")
	state := fs.String("state-dir", "", "节点状态目录")
	asJSON := fs.Bool("json", false, "按行输出授权进度 JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("login 不接受位置参数")
	}
	c, err := config.Load(*file)
	if err != nil {
		return err
	}
	if *state == "" {
		*state, err = app.StateDir(c.Node.Name)
		if err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	enc := json.NewEncoder(out)
	return mesh.LoginTailnet(ctx, c, filepath.Join(*state, "network"), func(p mesh.LoginProgress) error {
		if *asJSON {
			return enc.Encode(p)
		}
		switch p.State {
		case "needs_login":
			_, err = fmt.Fprintln(out, "请在浏览器打开:", p.URL)
		case "needs_approval":
			_, err = fmt.Fprintln(out, "设备等待控制面批准；批准后会自动继续")
		case "ready":
			_, err = fmt.Fprintln(out, "浏览器授权完成，节点身份已保存")
		}
		return err
	})
}
