package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kardianos/service"
	"github.com/tzf1003/xmesh/internal/app"
	"github.com/tzf1003/xmesh/internal/config"
)

func Test_ServiceRuntimeFailurePropagates(t *testing.T) {
	expected := errors.New("listener failed")
	failures := make(chan error, 1)
	p := &program{run: func(context.Context, *config.Store, app.Options) error { return expected }, onFailure: func(e error) { failures <- e }}
	if e := p.Start(nil); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-failures:
		if !errors.Is(e, expected) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("runtime failure did not reach service exit handler")
	}
	for i := 0; i < 2; i++ {
		if e := p.Stop(nil); !errors.Is(e, expected) {
			t.Fatal("stop result", e)
		}
	}
}

func Test_ServiceRequestedStop(t *testing.T) {
	failures := make(chan error, 1)
	p := &program{run: func(ctx context.Context, _ *config.Store, _ app.Options) error { <-ctx.Done(); return nil }, onFailure: func(e error) { failures <- e }}
	if e := p.Start(nil); e != nil {
		t.Fatal(e)
	}
	if e := p.Stop(nil); e != nil {
		t.Fatal(e)
	}
	select {
	case e := <-failures:
		t.Fatal("requested stop treated as failure", e)
	default:
	}
}

func Test_InstallStateDir(t *testing.T) {
	env := func(k string) string {
		if k == "ProgramData" {
			return `D:\PD`
		}
		return ""
	}
	cases := []struct {
		goos, flag, want string
		system           bool
	}{
		{"windows", "", `D:\PD\xmesh\n1`, true},
		{"windows", `E:\own`, `E:\own`, true},
		{"windows", "", "", false},
		{"linux", "", "", true},
		{"darwin", "/s", "/s", false},
	}
	for _, c := range cases {
		if got := installStateDir(c.goos, env, c.system, c.flag, "n1"); got != c.want {
			t.Errorf("%+v: got %q", c, got)
		}
	}
}

// A node whose service was never installed gets a stable code, not the platform tool's text.
func Test_ServiceNotInstalledIsReportedByCode(t *testing.T) {
	out, err := captureStdout(func() error {
		return withJSON("stop", []string{"--json"}, func([]string) error { return notInstalled("xmesh-n1") })
	})
	if err == nil {
		t.Fatal("a missing service must still fail")
	}
	var got struct {
		OK    bool           `json:"ok"`
		Error map[string]any `json:"error"`
	}
	if e := json.Unmarshal([]byte(out), &got); e != nil {
		t.Fatalf("%v: %q", e, out)
	}
	if got.OK || got.Error["error"] != "not_found" || got.Error["reason"] != "not_installed" || got.Error["name"] != "xmesh-n1" {
		t.Fatalf("%v", got)
	}
	if !serviceMissing(fmt.Errorf("wrapped: %w", service.ErrNotInstalled)) || serviceMissing(errors.New("exit status 1")) {
		t.Fatal("serviceMissing")
	}
}

func Test_PairDeviceTagNote(t *testing.T) {
	if note := missingDeviceTagNote([]string{"tag:peer", "tag:device"}); note != "" {
		t.Fatal("a device pairing needs no note:", note)
	}
	if note := missingDeviceTagNote([]string{"tag:peer"}); !strings.Contains(note, "tag:device") {
		t.Fatal("a peer-only pairing must say it is not listed as a device:", note)
	}
}
