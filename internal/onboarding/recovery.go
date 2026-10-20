package onboarding

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// reconcileConfig recovers either side of a journaled atomic replacement.
// Every accepted file matches the previous committed state or the exact intent.
func (s *Store) reconcileConfig(p *Plan, j *Job) error {
	current, err := fileHash(p.ConfigPath)
	if err != nil {
		return err
	}
	expected := p.BeforeHash
	if j.ConfigHash != "" {
		expected = j.ConfigHash
	}
	if j.PendingConfigHash != "" {
		switch current {
		case j.PendingConfigHash:
			if j.PendingRestore {
				resetConfigSteps(j)
			} else {
				j.ConfigHash = current
				j.PendingConfigHash = ""
			}
		case expected:
			// The intent reached disk, while the original file is still present.
			j.PendingConfigHash = ""
			j.PendingRestore = false
		default:
			return errors.New("configuration changed during setup recovery")
		}
		if err := s.SaveJob(j); err != nil {
			return err
		}
		expected = p.BeforeHash
		if j.ConfigHash != "" {
			expected = j.ConfigHash
		}
	}
	if current != expected {
		return errors.New("configuration changed after the approved setup state")
	}
	return nil
}

func resetConfigSteps(j *Job) {
	j.ConfigHash = ""
	j.PendingConfigHash = ""
	j.PendingRestore = false
	for i := range j.Steps {
		if j.Steps[i].ID == "write-config" || j.Steps[i].ID == "network" {
			j.Steps[i].Status = "pending"
		}
	}
}

func markStaleJob(j *Job) {
	j.Status = "failed"
	j.Readiness.ServiceReady = false
	j.Readiness.ClientVerified = false
	j.NextAction = "配置已变化，请重新预览后应用"
	for i := range j.Steps {
		if j.Steps[i].ID == "write-config" {
			j.Steps[i].Status = "failed"
			j.Steps[i].Code = "plan_stale"
			j.Steps[i].Message = "配置与已确认的任务状态不一致"
			j.Steps[i].NextAction = j.NextAction
		}
	}
}

func verificationStepsReady(j *Job) bool {
	if j.Status == "cancelled" || !j.Readiness.Installed || j.PendingConfigHash != "" || j.ServiceRestorePending {
		return false
	}
	for _, step := range j.Steps {
		if step.ID != "verify" && step.Status != "succeeded" {
			return false
		}
	}
	return true
}

func (s *Store) restoreService(ctx context.Context, p *Plan, j *Job, h Host) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if j.ServiceSnapshot == nil || h.Restore == nil {
		return errors.New("service recovery metadata is unavailable")
	}
	if !j.ServiceSnapshot.Installed && j.ServiceOwned {
		if h.Installed == nil || h.Remove == nil {
			return errors.New("installed service recovery is unavailable")
		}
		have, err := h.Installed(p)
		if err != nil {
			return err
		}
		if have {
			if err := h.Remove(ctx, p); err != nil {
				return err
			}
		}
		j.ServiceOwned = false
		j.Readiness.Installed = false
		if err := s.SaveJob(j); err != nil {
			return err
		}
	}
	if err := s.restoreConfig(p, j); err != nil {
		return err
	}
	if err := h.Restore(ctx, p, *j.ServiceSnapshot); err != nil {
		return err
	}
	j.ServiceOwned = false
	j.ServiceStopped = false
	j.ServiceRestorePending = false
	j.Readiness.Installed = j.ServiceSnapshot.Installed
	j.Readiness.ServiceReady = false
	j.Readiness.ClientVerified = false
	for i := range j.Steps {
		if j.Steps[i].ID == "install" || j.Steps[i].ID == "start" || j.Steps[i].ID == "clients" {
			j.Steps[i].Status = "pending"
		}
	}
	return s.SaveJob(j)
}

func validateSetupToken(path string) error {
	raw, exists, err := readClientFile(path)
	if err != nil || !exists || len(raw) > 4096 {
		return problem("credential_unavailable", "已有本机授权文件无法使用，请检查私有文件权限与内容", "token")
	}
	token := strings.TrimSpace(string(raw))
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, " \t\r\n\x00") {
		return problem("credential_unavailable", "已有本机授权文件格式无效，请从备份核对后重试", "token")
	}
	return verifyPrivateClientFile(path)
}

func validateSetupDeny(path string) error {
	raw, exists, err := readClientFile(path)
	if err != nil || !exists {
		return errors.New("read setup workspace restrictions")
	}
	var document any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return errors.New("workspace restriction file must contain a YAML string list")
	}
	list, ok := document.([]any)
	if !ok {
		return errors.New("workspace restriction file must contain a YAML string list")
	}
	for _, item := range list {
		if _, ok := item.(string); !ok {
			return errors.New("workspace restriction file must contain a YAML string list")
		}
	}
	return verifyPrivateClientFile(filepath.Clean(path))
}
