package computer

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"reflect"
	"strconv"
	"testing"
)

type fakeRunner struct {
	calls [][]string
	reply func(args []string) string
	shot  image.Image
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.calls = append(f.calls, args)
	if len(args) > 0 && args[0] == "screenshot" {
		var b bytes.Buffer
		_ = png.Encode(&b, f.shot)
		_ = os.WriteFile(args[len(args)-1], b.Bytes(), 0o600)
	}
	return []byte(f.reply(args)), nil
}

const okEnv = `{"ok":true,"data":%s}`

func newADFake(t *testing.T, w, h int) (*AgentDesktop, *fakeRunner) {
	t.Helper()
	f := &fakeRunner{shot: image.NewNRGBA(image.Rect(0, 0, w*2, h*2))}
	f.reply = func(args []string) string {
		switch args[0] {
		case "permissions":
			return `{"ok":true,"data":{"accessibility":{"state":"granted"},"screen_recording":{"state":"granted"}}}`
		case "list-displays":
			return `{"ok":true,"data":[{"id":"1","bounds":{"x":100,"y":50,"width":` + itoa(w) + `,"height":` + itoa(h) + `},"is_primary":true,"scale":2}]}`
		}
		return `{"ok":true,"data":{}}`
	}
	return NewAgentDesktop(f, nil, t.TempDir()), f
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestInfoReportsEngineCapabilities(t *testing.T) {
	a, _ := newADFake(t, 800, 600)
	info, err := a.Info(context.Background())
	if err != nil || !info.Available || info.Epoch == "" || info.LayoutVersion == "" || len(info.Displays) != 1 {
		t.Fatalf("%+v %v", info, err)
	}
	for _, c := range []string{"screenshot", "pointer", "keyboard", "engine:agent-desktop"} {
		if !contains(info.Capabilities, c) {
			t.Fatalf("missing %s in %v", c, info.Capabilities)
		}
	}
	if info.DefaultInteraction != "" || len(info.Targets) != 0 {
		t.Fatalf("no fallback must mean foreground only: %+v", info)
	}
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestInfoPermissionRequired(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	f.reply = func(args []string) string {
		return `{"ok":true,"data":{"accessibility":{"state":"denied"},"screen_recording":{"state":"granted"}}}`
	}
	info, err := a.Info(context.Background())
	if err != nil || info.Available || info.State != "permission_required" {
		t.Fatalf("%+v %v", info, err)
	}
}

func TestCaptureScalesAndConvertsCoordinates(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	fr, err := a.Capture(context.Background(), CaptureRequest{MaxWidth: 400})
	if err != nil {
		t.Fatal(err)
	}
	if fr.Width != 400 || fr.Height != 300 || fr.Region != (Region{Width: 800, Height: 600}) || fr.DisplayID != "1" {
		t.Fatalf("%+v", fr)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(fr.Image))
	if err != nil || cfg.Width != 400 {
		t.Fatalf("%v %+v", err, cfg)
	}
	info, _ := a.Info(context.Background())
	if fr.Epoch != info.Epoch || fr.LayoutVersion != info.LayoutVersion {
		t.Fatal("frame identity must match info")
	}
	f.calls = nil
	res, err := a.Execute(context.Background(), Execution{Epoch: info.Epoch, Frame: &fr, Actions: []Action{{Type: "click", X: p(200), Y: p(150), Clicks: 2, Button: "right"}}})
	if err != nil || res.State != "executed" || res.Executed != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	// 200*800/400 = 400 + display origin 100; 150*600/300 = 300 + origin 50.
	want := []string{"--headed", "mouse-click", "--xy", "500,350", "--button", "right", "--count", "2"}
	if !hasCall(f.calls, want) {
		t.Fatalf("calls %v", f.calls)
	}
}

func p(n int) *int { return &n }

func hasCall(calls [][]string, want []string) bool {
	for _, c := range calls {
		if reflect.DeepEqual(c, want) {
			return true
		}
	}
	return false
}

func TestCaptureRegionCrops(t *testing.T) {
	a, _ := newADFake(t, 800, 600)
	fr, err := a.Capture(context.Background(), CaptureRequest{Region: &Region{X: 100, Y: 100, Width: 200, Height: 100}})
	if err != nil || fr.Width != 400 || fr.Height != 200 || fr.Region.X != 100 {
		t.Fatalf("%+v %v", fr, err)
	}
	if _, err = a.Capture(context.Background(), CaptureRequest{Region: &Region{X: 700, Y: 0, Width: 200, Height: 10}}); err == nil {
		t.Fatal("region outside display must be rejected")
	}
}

func TestExecuteKeysAndText(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	info, _ := a.Info(context.Background())
	fr, _ := a.Capture(context.Background(), CaptureRequest{})
	f.calls = nil
	res, _ := a.Execute(context.Background(), Execution{Epoch: info.Epoch, Frame: &fr, Actions: []Action{{Type: "key", Keys: []string{"Control", "Shift", "Z"}}, {Type: "text", Text: "Hi"}}})
	if res.State != "executed" || res.Executed != 2 {
		t.Fatalf("%+v", res)
	}
	for _, w := range [][]string{{"press", "ctrl+shift+z"}, {"press", "shift+h"}, {"press", "i"}} {
		if !hasCall(f.calls, w) {
			t.Fatalf("missing %v in %v", w, f.calls)
		}
	}
	f.calls = nil
	res, _ = a.Execute(context.Background(), Execution{Epoch: info.Epoch, Frame: &fr, Actions: []Action{{Type: "wait", DurationMS: 1}, {Type: "text", Text: "中文"}}})
	if res.State != "failed" || res.Executed != 1 || res.Error != "conflict" || hasCall(f.calls, []string{"press", "shift+h"}) {
		t.Fatalf("non-ASCII must fail before dispatch: %+v calls=%v", res, f.calls)
	}
}

func TestExecuteRejectsStaleIdentity(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	fr, _ := a.Capture(context.Background(), CaptureRequest{})
	res, _ := a.Execute(context.Background(), Execution{Epoch: "other", Frame: &fr, Actions: []Action{{Type: "move", X: p(1), Y: p(1)}}})
	if res.State != "failed" || res.Error != "stale_frame" {
		t.Fatalf("%+v", res)
	}
	f.reply = func(args []string) string {
		if args[0] == "list-displays" {
			return `{"ok":true,"data":[{"id":"1","bounds":{"x":0,"y":0,"width":1024,"height":768},"scale":1}]}`
		}
		return `{"ok":true,"data":{"accessibility":{"state":"granted"},"screen_recording":{"state":"granted"}}}`
	}
	info, _ := a.Info(context.Background())
	res, _ = a.Execute(context.Background(), Execution{Epoch: info.Epoch, Frame: &fr, Actions: []Action{{Type: "move", X: p(1), Y: p(1)}}})
	if res.Error != "stale_frame" {
		t.Fatalf("layout change must invalidate the frame: %+v", res)
	}
}

func TestExecuteErrorDisposition(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	info, _ := a.Info(context.Background())
	fr, _ := a.Capture(context.Background(), CaptureRequest{})
	base := f.reply
	for _, tc := range []struct{ delivery, state string }{{"not_delivered", "failed"}, {"delivered_unverified", "unknown"}, {"unknown", "unknown"}} {
		f.reply = func(args []string) string {
			if args[len(args)-1] != "" && (args[0] == "--headed" || args[0] == "press") {
				return `{"ok":false,"error":{"code":"ACTION_FAILED","message":"x","disposition":{"delivery":"` + tc.delivery + `","retry":"unsafe"}}}`
			}
			return base(args)
		}
		res, _ := a.Execute(context.Background(), Execution{Epoch: info.Epoch, Frame: &fr, Actions: []Action{{Type: "move", X: p(1), Y: p(1)}}})
		if res.State != tc.state || res.Executed != 0 {
			t.Fatalf("%s: %+v", tc.delivery, res)
		}
	}
}

type fakeProvider struct {
	info    Info
	capture int
	exec    []Execution
}

func (f *fakeProvider) Info(context.Context) (Info, error) { return f.info, nil }
func (f *fakeProvider) Capture(_ context.Context, r CaptureRequest) (Frame, error) {
	f.capture++
	return Frame{TargetID: r.TargetID}, nil
}
func (f *fakeProvider) Execute(_ context.Context, e Execution) (ActionResult, error) {
	f.exec = append(f.exec, e)
	return ActionResult{State: "executed", Executed: len(e.Actions)}, nil
}
func (f *fakeProvider) Release(context.Context, Execution) error { return nil }
func (f *fakeProvider) Close() error                             { return nil }

func TestBackgroundRoutesToFallback(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	fb := &fakeProvider{info: Info{ProtocolVersion: ProtocolVersion, Available: true, State: "ready", DesktopID: "desktop", Epoch: "native-epoch", LayoutVersion: "L", Capabilities: []string{"screenshot", "pointer", "keyboard", "background:pointer", "background:keyboard"}, DefaultInteraction: "background", Targets: []Target{{ID: "t1"}}}}
	a.fallback = fb
	info, err := a.Info(context.Background())
	if err != nil || info.Epoch != "native-epoch" || info.DefaultInteraction != "background" || len(info.Targets) != 1 || !contains(info.Capabilities, "engine:agent-desktop") {
		t.Fatalf("fallback owns identity: %+v %v", info, err)
	}
	f.calls = nil
	if _, err = a.Capture(context.Background(), CaptureRequest{TargetID: "t1"}); err != nil || fb.capture != 1 {
		t.Fatalf("%v %d", err, fb.capture)
	}
	res, _ := a.Execute(context.Background(), Execution{Epoch: "native-epoch", Interaction: "background", TargetID: "t1", Actions: []Action{{Type: "key", Keys: []string{"a"}}}})
	if res.State != "executed" || len(fb.exec) != 1 {
		t.Fatalf("%+v", res)
	}
	for _, c := range f.calls {
		if c[0] == "press" || c[0] == "--headed" || c[0] == "screenshot" {
			t.Fatalf("background must never reach agent-desktop physical input: %v", f.calls)
		}
	}
	// Foreground frames carry the fallback's identity so the manager accepts them.
	fr, err := a.Capture(context.Background(), CaptureRequest{})
	if err != nil || fr.Epoch != "native-epoch" || fr.LayoutVersion != "L" {
		t.Fatalf("%+v %v", fr, err)
	}
}

func TestBackgroundWithoutFallbackFails(t *testing.T) {
	a, _ := newADFake(t, 800, 600)
	if _, err := a.Capture(context.Background(), CaptureRequest{TargetID: "t"}); err == nil {
		t.Fatal("target capture needs the fallback")
	}
	res, _ := a.Execute(context.Background(), Execution{Interaction: "background", TargetID: "t", Actions: []Action{{Type: "wait", DurationMS: 1}}})
	if res.State != "failed" || res.Error != "background_unsupported" {
		t.Fatalf("%+v", res)
	}
}

func TestEngineSelection(t *testing.T) {
	dir := t.TempDir()
	if e, err := ReadEngine(dir); err != nil || e != EngineNative {
		t.Fatalf("default must be native: %v %v", e, err)
	}
	if err := WriteEngine(dir, EngineAgentDesktop); err != nil {
		t.Fatal(err)
	}
	if e, _ := ReadEngine(dir); e != EngineAgentDesktop {
		t.Fatal(e)
	}
	if _, err := ParseEngine("bogus"); err == nil {
		t.Fatal("unknown engine must be rejected")
	}
	if err := WriteEngine(dir, "bogus"); err == nil {
		t.Fatal("unknown engine must not be persisted")
	}
	_, _, err := EngineOptions{Engine: EngineAgentDesktop, AgentDesktopPath: "relative", StateDir: dir}.Factories()
	if err == nil {
		t.Fatal("agent-desktop path must be absolute and exist")
	}
}
