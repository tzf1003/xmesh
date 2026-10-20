package main

import (
	"encoding/json"
	"flag"
	"os"

	"github.com/tzf1003/xmesh/internal/config"
)

// serviceInfo exposes paths and install options, never credential contents.
func serviceInfo(args []string) error {
	flags := flag.NewFlagSet("service-info", flag.ContinueOnError)
	name := flags.String("name", "", "已安装的服务名")
	file := flags.String("config", "xmesh.yaml", "配置路径")
	flags.Bool("json", true, "输出 JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		cfg, err := config.Load(*file)
		if err != nil {
			return err
		}
		*name = serviceName(cfg.Node.Name)
	}
	if err := migrationServiceName(*name); err != nil {
		return err
	}
	paths, err := installedMigrationPaths(*name)
	if err != nil {
		if os.IsNotExist(err) || serviceMissing(err) {
			return notInstalled(*name)
		}
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"config_file": paths.config, "state_dir": paths.state,
		"oauth_password_file": paths.password, "local_only": paths.localOnly,
		"system": paths.system,
	})
}
