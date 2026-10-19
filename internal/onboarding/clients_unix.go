//go:build !windows

package onboarding

import (
	"errors"
	"os"
)

func createPrivateClientFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return f, nil
}

func verifyPrivateClientFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return errors.New("client configuration must have private owner permissions")
	}
	return nil
}

func renameClientFile(source, destination string) error { return os.Rename(source, destination) }

func syncClientDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return errors.New("open client configuration directory")
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return errors.New("sync client configuration directory")
	}
	return nil
}
