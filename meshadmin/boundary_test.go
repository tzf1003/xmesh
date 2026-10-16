package meshadmin

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/tzf1003/xmesh"

// importsOf returns the import paths of every Go file under dir (tests included).
func importsOf(t *testing.T, dir string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, perr := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			out[path] = append(out[path], strings.Trim(imp.Path.Value, `"`))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// conformance: CON-MESH-01
func TestMeshadminImportBoundary(t *testing.T) {
	// meshadmin never reaches into the kernel.
	for file, imps := range importsOf(t, ".") {
		for _, imp := range imps {
			if strings.HasPrefix(imp, modulePath+"/internal") {
				t.Errorf("%s imports %s; meshadmin must not depend on internal/", file, imp)
			}
		}
	}
	// The kernel, gateway and agent never reach for meshadmin. Only the CLI front
	// end (cmd/xmesh/mesh*.go) may.
	for _, dir := range []string{"../internal", "../cmd"} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatal(err)
		}
		for file, imps := range importsOf(t, dir) {
			base := filepath.Base(file)
			if filepath.Base(filepath.Dir(file)) == "xmesh" && strings.HasPrefix(base, "mesh") {
				continue
			}
			for _, imp := range imps {
				if imp == modulePath+"/meshadmin" {
					t.Errorf("%s imports meshadmin; only the mesh CLI may", file)
				}
			}
		}
	}
}
