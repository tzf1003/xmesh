package main

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// repositoryRoot returns the module root. Tests in this package run with the
// package directory (cmd/xmesh) as the working directory.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal("module root not found:", err)
	}
	return root
}

// sourceFiles lists every Go source file below root, skipping VCS metadata.
func sourceFiles(t *testing.T, root string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// The repository is one Go module that builds one binary named xmesh: a single
// go.mod, a single main package (cmd/xmesh), and no cgo that would break
// cross-compilation.
// conformance: CON-ARCH-02
func Test_CONARCH02_SingleModuleSingleBinary(t *testing.T) {
	root := repositoryRoot(t)

	var modules []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.IsDir() && d.Name() == "go.mod" {
			rel, _ := filepath.Rel(root, path)
			modules = append(modules, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 1 || modules[0] != "go.mod" {
		t.Fatalf("the repository must contain exactly the root go.mod, found %v", modules)
	}

	mainDirs := map[string]bool{}
	cgo := []string{}
	fset := token.NewFileSet()
	for _, path := range sourceFiles(t, root) {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(path, err)
		}
		rel, _ := filepath.Rel(root, path)
		if file.Name.Name == "main" {
			mainDirs[filepath.ToSlash(filepath.Dir(rel))] = true
		}
		for _, spec := range file.Imports {
			if imported, _ := strconv.Unquote(spec.Path.Value); imported == "C" {
				cgo = append(cgo, filepath.ToSlash(rel))
			}
		}
	}
	dirs := []string{}
	for dir := range mainDirs {
		dirs = append(dirs, dir)
	}
	sort.Strings(dirs)
	if len(dirs) != 1 || dirs[0] != "cmd/xmesh" {
		t.Fatalf("the only package main directory must be cmd/xmesh, found %v", dirs)
	}
	entries, err := os.ReadDir(filepath.Join(root, "cmd"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "xmesh" {
		names := []string{}
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("cmd/ must contain only xmesh, found %v", names)
	}
	if len(cgo) != 0 {
		t.Fatalf("non-test sources must not import \"C\" (cgo breaks single-source cross-compilation): %v", cgo)
	}
}

// The release workflow builds the same package for six targets with cgo
// disabled. The build is slow, so it runs only when XMESH_TEST_CROSSBUILD=1.
// conformance: CON-ARCH-02
func Test_CONARCH02_CrossCompile(t *testing.T) {
	if os.Getenv("XMESH_TEST_CROSSBUILD") != "1" {
		t.Skip("set XMESH_TEST_CROSSBUILD=1 to cross-compile ./cmd/xmesh for the six release targets")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		goTool = filepath.Join(runtime.GOROOT(), "bin", "go")
		if _, err = os.Stat(goTool); err != nil {
			t.Fatal("go tool not found")
		}
	}
	root := repositoryRoot(t)
	targets := []struct{ goos, goarch string }{
		{"linux", "amd64"}, {"linux", "arm64"},
		{"darwin", "amd64"}, {"darwin", "arm64"},
		{"windows", "amd64"}, {"windows", "arm64"},
	}
	for _, target := range targets {
		t.Run(target.goos+"_"+target.goarch, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "xmesh")
			if target.goos == "windows" {
				out += ".exe"
			}
			cmd := exec.Command(goTool, "build", "-trimpath", "-o", out, "./cmd/xmesh")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "GOOS="+target.goos, "GOARCH="+target.goarch, "CGO_ENABLED=0")
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go build for %s/%s failed: %v\n%s", target.goos, target.goarch, err, output)
			}
			if _, err := os.Stat(out); err != nil {
				t.Fatal("binary not produced:", err)
			}
		})
	}
}
