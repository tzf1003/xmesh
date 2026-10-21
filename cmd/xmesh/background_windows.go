package main

import (
	"debug/pe"
	"fmt"
	"path/filepath"
	"strings"
)

func ensureTaskBackground(raw []byte, register func(string) error) error {
	document := taskDocumentText(raw)
	task, err := parseUserTask(document)
	if err != nil {
		return err
	}
	command := task.Actions.Exec.Command
	background := filepath.Join(filepath.Dir(command), "xmesh-background.exe")
	if err := requireBackgroundExecutable(background); err != nil {
		return err
	}
	if strings.EqualFold(filepath.Clean(command), background) {
		return nil
	}
	if !strings.EqualFold(filepath.Base(command), "xmesh.exe") {
		return fmt.Errorf("无法自动替换自定义任务程序: %s", command)
	}
	document, err = replaceTaskCommand(document, background)
	if err != nil {
		return err
	}
	return withTaskDocument(document, register)
}

func validateBackgroundExecutable(path string) error {
	f, err := pe.Open(path)
	if err != nil {
		return fmt.Errorf("无法检查后台内核: %w", err)
	}
	defer f.Close()
	switch header := f.OptionalHeader.(type) {
	case *pe.OptionalHeader64:
		if header.Subsystem == pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
			return nil
		}
	case *pe.OptionalHeader32:
		if header.Subsystem == pe.IMAGE_SUBSYSTEM_WINDOWS_GUI {
			return nil
		}
	}
	return fmt.Errorf("后台内核必须使用 -H=windowsgui 构建，禁止创建前台控制台: %s", path)
}
