package computer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// stubProvider is the smallest Provider that lets the engine stamp be observed.
type stubProvider struct{ info Info }

func (s stubProvider) Info(context.Context) (Info, error) { return s.info, nil }
func (s stubProvider) Capture(context.Context, CaptureRequest) (Frame, error) {
	return Frame{}, nil
}
func (s stubProvider) Execute(context.Context, Execution) (ActionResult, error) {
	return ActionResult{}, nil
}
func (s stubProvider) Release(context.Context, Execution) error { return nil }
func (s stubProvider) Close() error                             { return nil }

func TestResolveDefaultsToNative(t *testing.T) {
	res, err := EngineOptions{StateDir: t.TempDir()}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if res.Configured != EngineNative || res.Effective != EngineNative || res.Status.State != "native" {
		t.Fatalf("default resolution = %s", res)
	}
}

func TestResolveFallsBackWhenBundledCopyIsNotThePin(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "agent-desktop")
	if err := os.WriteFile(exe, []byte("not the pinned build"), 0o755); err != nil {
		t.Fatal(err)
	}
	res, err := EngineOptions{Engine: EngineAgentDesktop, BundledPath: exe, StateDir: t.TempDir()}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if res.Configured != EngineAgentDesktop || res.Effective != EngineNative {
		t.Fatalf("unpinned binary must not run: %s", res)
	}
	if res.Status.State != "integrity_failed" || res.Status.Reason != "binary_hash_mismatch" {
		t.Fatalf("status = %+v", res.Status)
	}
}

func TestResolveReportsUnreadablePointer(t *testing.T) {
	state := t.TempDir()
	root := filepath.Join(state, "engines", "agent-desktop")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "active"), []byte("latest\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := EngineOptions{Engine: EngineAgentDesktop, StateDir: state}.Resolve()
	if err != nil {
		t.Fatal(err)
	}
	if res.Effective != EngineNative || res.Status.Reason != "pointer_invalid" {
		t.Fatalf("invalid pointer = %s", res)
	}
}

func TestEngineProviderStampsResolvedEngine(t *testing.T) {
	p := withEngine(stubProvider{info: Info{Available: true, State: "ready"}}, engineInfo{
		engine: EngineNative, status: "integrity_failed", reason: "binary_hash_mismatch",
	})
	info, err := p.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Engine != "native" || info.EngineStatus != "integrity_failed" || info.EngineReason != "binary_hash_mismatch" || !info.Available {
		t.Fatalf("stamped info = %+v", info)
	}
}
