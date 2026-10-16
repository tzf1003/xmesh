package fs

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// fs.grep prefers `rg --json` and otherwise falls back to the pure Go engine;
// both return the same structure. A stand-in rg that prints ripgrep's JSON event
// stream for the one file in the tree proves that the rg path is taken when an
// rg is on PATH, that its events are mapped to the same fields as the Go
// engine's, and that without an rg the Go engine answers identically.
// conformance: CON-IMPL-05
func Test_CONIMPL05_RipgrepAndGoEnginesReturnTheSameStructure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in rg is a POSIX shell script")
	}
	s, root := fixture(t)
	writeBytes(t, filepath.Join(root, "a.txt"), []byte("alpha\nneedle one needle\nomega\n"))

	bin := t.TempDir()
	log := filepath.Join(bin, "rg-args.log")
	events := `{"type":"begin","data":{"path":{"text":"a.txt"}}}
{"type":"match","data":{"path":{"text":"a.txt"},"lines":{"text":"needle one needle\n"},"line_number":2,"absolute_offset":6,"submatches":[{"match":{"text":"needle"},"start":0,"end":6},{"match":{"text":"needle"},"start":11,"end":17}]}}
{"type":"end","data":{"path":{"text":"a.txt"}}}
{"type":"summary","data":{}}
`
	writeBytes(t, filepath.Join(bin, "events.json"), []byte(events))
	script := "#!/bin/sh\necho \"$@\" >> '" + log + "'\ncat '" + filepath.Join(bin, "events.json") + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "rg"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	invocations := func() int {
		b, err := os.ReadFile(log)
		if err != nil {
			return 0
		}
		return strings.Count(string(b), "\n")
	}
	for _, mode := range []string{"content", "files", "count"} {
		args := Args{Pattern: "needle", OutputMode: mode, HeadLimit: 200, Context: 1}
		before := invocations()
		viaRg, err := s.grepEngine(context.Background(), root, args, false)
		if err != nil {
			t.Fatal(mode, err)
		}
		if invocations() <= before {
			t.Fatalf("%s: the rg on PATH was not used", mode)
		}
		before = invocations()
		viaGo, err := s.grepEngine(context.Background(), root, args, true)
		if err != nil {
			t.Fatal(mode, err)
		}
		if invocations() != before {
			t.Fatalf("%s: the pure Go engine ran rg", mode)
		}
		if !reflect.DeepEqual(viaRg, viaGo) {
			t.Fatalf("%s: engines disagree\nrg=%#v\ngo=%#v", mode, viaRg, viaGo)
		}
		if viaRg["truncated"] != false || viaRg["resolved_path"] != root {
			t.Errorf("%s: %v", mode, viaRg)
		}
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "--json") {
		t.Errorf("rg was not asked for --json: %s", b)
	}
	// Without rg on PATH the Go engine answers, with the same result.
	t.Setenv("PATH", t.TempDir())
	args := Args{Pattern: "needle", OutputMode: "content", HeadLimit: 200}
	before := invocations()
	fallback, err := s.grepEngine(context.Background(), root, args, false)
	if err != nil {
		t.Fatal(err)
	}
	pure, err := s.grepEngine(context.Background(), root, args, true)
	if err != nil {
		t.Fatal(err)
	}
	if invocations() != before || !reflect.DeepEqual(fallback, pure) {
		t.Errorf("fallback differs from the Go engine:\n%#v\n%#v", fallback, pure)
	}
	matches, _ := fallback["matches"].([]Object)
	if len(matches) != 1 || matches[0]["line"] != 2 || matches[0]["text"] != "needle one needle" {
		t.Errorf("matches: %v", fallback["matches"])
	}
}
