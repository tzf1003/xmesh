package pty

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/tzf1003/xmesh/internal/config"
)

// The Windows shell selection lives in a file that only builds on Windows, so
// this test reads its source on every platform: auto must try pwsh, then
// powershell, and nothing else. In particular cmd.exe or %COMSPEC% must never
// appear as a fallback. The Windows-only test next to it runs the real lookup.
// conformance: CON-IMPL-04
func Test_CONIMPL04_WindowsAutoShellIsPwshThenPowershell(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "platform_windows.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var body *ast.BlockStmt
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "defaultShell" && fn.Recv == nil {
			body = fn.Body
		}
	}
	if body == nil {
		t.Fatal("defaultShell is missing from platform_windows.go")
	}
	var lookups, literals []string
	ast.Inspect(body, func(n ast.Node) bool {
		switch v := n.(type) {
		case *ast.CallExpr:
			if sel, ok := v.Fun.(*ast.SelectorExpr); ok {
				lookups = append(lookups, sel.Sel.Name)
			}
		case *ast.BasicLit:
			if v.Kind == token.STRING {
				s, _ := strconv.Unquote(v.Value)
				literals = append(literals, s)
			}
		}
		return true
	})
	if strings.Join(literals, ",") != "pwsh,powershell" {
		t.Errorf("defaultShell resolves %v, want exactly pwsh then powershell", literals)
	}
	if strings.Join(lookups, ",") != "LookPath,LookPath" {
		t.Errorf("defaultShell must only search PATH for those two shells, calls: %v", lookups)
	}
	for _, name := range literals {
		if strings.Contains(strings.ToLower(name), "cmd") {
			t.Errorf("defaultShell mentions %q", name)
		}
	}
	// Nothing else in the platform file may consult COMSPEC to choose a shell.
	source, _ := parser.ParseFile(token.NewFileSet(), "platform_windows.go", nil, parser.ParseComments)
	ast.Inspect(source, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING && strings.Contains(strings.ToLower(lit.Value), "comspec") {
			t.Errorf("platform_windows.go reads COMSPEC: %s", lit.Value)
		}
		return true
	})
}

// The default configuration asks for `auto`, so a Windows node resolves its
// shell through the pwsh/powershell lookup above and not through a fixed
// command interpreter.
// conformance: CON-IMPL-04
func Test_CONIMPL04_DefaultConfigurationIsAuto(t *testing.T) {
	cfg, err := config.Parse([]byte(`{"node":{"name":"n","roles":["agent"]},"mesh":{"mode":"lan","lan":{"mdns":true}},"agent":{"cwd_policy":{"allow":["/"]},"authz":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.PTY.DefaultShell != "auto" {
		t.Fatalf("default_shell defaults to %q, want auto", cfg.Agent.PTY.DefaultShell)
	}
}
