package computer

import (
	"context"
	"errors"
	"testing"
)

func TestEngineStaysClosedWhenTheNativeVerdictFails(t *testing.T) {
	ready := Info{ProtocolVersion: ProtocolVersion, Available: true, State: "ready", DesktopID: "desktop", Epoch: "e", LayoutVersion: "l", Capabilities: []string{"screenshot", "pointer", "keyboard"}}
	info, err := combineInfo(ready, nil, Info{}, errors.New("native executor exited"))
	if err != nil || info.Available || info.Reason != "native_unavailable" || len(info.Capabilities) != 0 {
		t.Fatal("engine grant stood in for a missing native verdict", info, err)
	}
	// An engine that is already unavailable for its own reason keeps that reason.
	denied := Info{ProtocolVersion: ProtocolVersion, State: "permission_required", Reason: "agent_desktop_permission", Capabilities: []string{}}
	info, err = combineInfo(denied, nil, Info{}, errors.New("native executor exited"))
	if err != nil || info.Reason != "agent_desktop_permission" {
		t.Fatal("engine lost its own reason while native was down", info, err)
	}
}

func TestEngineStaysClosedWhenTheNativeModuleDidNotStart(t *testing.T) {
	a, f := newADFake(t, 800, 600)
	a.nativeLost = true
	ctx := context.Background()
	info, err := a.Info(ctx)
	if err != nil || info.Available || len(info.Capabilities) != 0 {
		t.Fatal("engine offered its desktop without a native verdict", info, err)
	}
	if _, err = a.Capture(ctx, CaptureRequest{}); code(err) != "desktop_unavailable" {
		t.Fatal("engine captured without a native verdict", err)
	}
	if len(f.calls) != 0 {
		t.Fatal("engine ran its own CLI without a native verdict", f.calls)
	}
}
