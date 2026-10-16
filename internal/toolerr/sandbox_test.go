package toolerr

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tzf1003/xmesh/internal/sandbox"
)

func TestSandboxRunnerReportsOversizedWorkspace(t *testing.T) {
	cause := fmt.Errorf("admit: %w", &sandbox.WorkspaceTooLargeError{Limit: 10000})
	e := SandboxRunner("sandbox.runner_failed", cause)
	if e.Code != "policy_denied" || e.Fields["rule"] != "sandbox.workspace_too_large" || e.Fields["limit"] != 10000 {
		t.Fatalf("oversized workspace: %+v", e)
	}
	if !errors.Is(e, cause) {
		t.Fatal("cause dropped")
	}
	if reason, _ := e.Fields["reason"].(string); reason == "" {
		t.Fatal("no reason for the caller")
	}
}

func TestSandboxRunnerKeepsOtherFailuresGeneric(t *testing.T) {
	large := &sandbox.WorkspaceTooLargeError{Limit: 3}
	// Growth during a run surfaces at cleanup; that stays a cleanup fault.
	if e := SandboxRunner("sandbox.cleanup_pending", large); e.Fields["rule"] != "sandbox.cleanup_pending" {
		t.Fatalf("cleanup failure renamed: %+v", e)
	}
	if e := SandboxRunner("sandbox.runner_failed", errors.New("other")); e.Fields["rule"] != "sandbox.runner_failed" {
		t.Fatalf("generic failure renamed: %+v", e)
	}
}
