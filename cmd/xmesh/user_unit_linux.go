//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// userUnitDir is where systemd keeps a user's own units: $XDG_CONFIG_HOME/systemd/user.
func userUnitDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "systemd", "user"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "systemd", "user"), nil
}

// userUnitExists reports whether the unit file install writes for this service is on disk. The service
// library asks the system manager, not the user's, whether a user service exists, so on its own it calls
// an installed one "not installed".
func userUnitExists(serviceID string) bool {
	dir, err := userUnitDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, serviceID+".service"))
	return err == nil
}

// removeUserUnit takes a user unit away when the library's own uninstall could not (its `systemctl --user
// disable` failed): the enable link and the unit file go, then the manager reloads. Without this a
// reinstall stops at "Init already exists".
func removeUserUnit(serviceID string) error {
	dir, err := userUnitDir()
	if err != nil {
		return err
	}
	unit := serviceID + ".service"
	// Stopping and disabling are best effort: the unit may be stopped, never enabled or not loadable.
	_ = exec.Command("systemctl", "--user", "disable", unit).Run()
	for _, link := range []string{filepath.Join(dir, "default.target.wants", unit), filepath.Join(dir, unit)} {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w: %s", err, out)
	}
	return nil
}
