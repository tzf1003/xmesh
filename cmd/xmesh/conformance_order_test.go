package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The delivery order is cwd, fs, pty, exec, then mcpmux/backend/lease, mesh,
// gateway. A package of an earlier stage, including its own test binary, must
// not depend on a later stage, so the earlier stages build and test on this
// machine without the network stack (tsnet, OAuth, the gateway).
// conformance: CON-IMPL-01
func Test_CONIMPL01_StageDependenciesPointBackwardOnly(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go tool is not available")
	}
	const module = "github.com/tzf1003/xmesh/"
	stages := map[string]int{
		"internal/cwd":          1,
		"internal/fs":           2,
		"internal/pty":          3,
		"internal/exec":         4,
		"internal/mcpmux":       5,
		"internal/backend":      5,
		"internal/lease":        5,
		"internal/mesh":         6,
		"internal/role/gateway": 7,
		"internal/oauth":        7,
	}
	stageOf := func(importPath string) (string, int, bool) {
		importPath = strings.TrimPrefix(importPath, module)
		if i := strings.IndexAny(importPath, " ["); i >= 0 {
			importPath = importPath[:i]
		}
		importPath = strings.TrimSuffix(importPath, ".test")
		for name, stage := range stages {
			if importPath == name || strings.HasPrefix(importPath, name+"/") {
				return name, stage, true
			}
		}
		return "", 0, false
	}
	for name, stage := range stages {
		out, err := exec.Command("go", "list", "-deps", "-test", "-f", "{{if not .Standard}}{{.ImportPath}}{{end}}", "./"+strings.Join([]string{"..", "..", name}, "/")).CombinedOutput()
		if err != nil {
			t.Fatalf("go list %s: %v\n%s", name, err, out)
		}
		for _, dep := range strings.Fields(strings.ReplaceAll(string(out), "\n", " ")) {
			if strings.HasPrefix(dep, "[") || strings.HasSuffix(dep, "]") {
				continue // the bracketed variant annotation of a test package
			}
			if depName, depStage, ok := stageOf(dep); ok && depStage > stage {
				t.Errorf("%s (stage %d) depends on %s (stage %d)", name, stage, depName, depStage)
			}
			if stage <= 5 && strings.HasPrefix(dep, "tailscale.com/") {
				t.Errorf("%s (stage %d) pulls in %s: the network stack must not enter the default tests of stages 1 to 5", name, stage, dep)
			}
		}
	}
}
