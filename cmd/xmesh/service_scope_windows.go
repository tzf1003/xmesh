package main

import (
	"errors"
	"golang.org/x/sys/windows"
)

func installedSystemService(name string) (bool, error) {
	manager, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false, err
	}
	defer windows.CloseServiceHandle(manager)
	serviceName, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	service, err := windows.OpenService(manager, serviceName, windows.SERVICE_QUERY_CONFIG)
	if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	windows.CloseServiceHandle(service)
	return true, nil
}
