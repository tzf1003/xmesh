package main

import (
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

func readInstalledMigrationPaths(name string) (migrationPaths, error) {
	if err := migrationServiceName(name); err != nil {
		return migrationPaths{}, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return migrationPaths{}, err
	}
	directory, err := windows.GetSystemDirectory()
	if err != nil {
		return migrationPaths{}, err
	}
	raw, taskErr := taskCommand(directory, "/Query", "/TN", userTaskName(name, user.User.Sid.String()), "/XML")
	if taskErr == nil {
		command, arguments, err := migrationTaskCommand(raw)
		if err != nil {
			return migrationPaths{}, err
		}
		args, err := windows.DecomposeCommandLine(arguments)
		if err != nil {
			return migrationPaths{}, err
		}
		return migrationPathsFromArgs(append([]string{command}, args...))
	}
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return migrationPaths{}, err
	}
	defer windows.CloseServiceHandle(manager)
	serviceName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return migrationPaths{}, err
	}
	handle, err := windows.OpenService(manager, serviceName, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return migrationPaths{}, err
	}
	service := &mgr.Service{Name: name, Handle: handle}
	defer service.Close()
	cfg, err := service.Config()
	if err != nil {
		return migrationPaths{}, err
	}
	args, err := windows.DecomposeCommandLine(cfg.BinaryPathName)
	if err != nil {
		return migrationPaths{}, err
	}
	paths, err := migrationPathsFromArgs(args)
	paths.system = true
	return paths, err
}
