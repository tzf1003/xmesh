//go:build !windows && !darwin

package main

import "fmt"

func readInstalledMigrationPaths(string) (migrationPaths, error) {
	return migrationPaths{}, fmt.Errorf("此平台请使用 --config 与 --state-dir 指定升级路径")
}
