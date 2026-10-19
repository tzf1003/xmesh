package mesh

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/types/views"
)

func runningLoginStatus(tags ...string) *ipnstate.Status {
	tagView := views.SliceOf(tags)
	return &ipnstate.Status{BackendState: "Running", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.1")}, Self: &ipnstate.PeerStatus{Tags: &tagView}}
}

func TestBrowserLoginProgress(t *testing.T) {
	states := []*ipnstate.Status{
		{BackendState: "NeedsLogin", AuthURL: "https://login.tailscale.com/a/test"},
		{BackendState: "NeedsMachineAuth"}, runningLoginStatus("tag:device"),
	}
	var events []LoginProgress
	index := 0
	err := waitTailnetLogin(context.Background(), func(context.Context) (*ipnstate.Status, error) {
		st := states[index]
		index++
		return st, nil
	}, func(p LoginProgress) error { events = append(events, p); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].URL == "" || events[1].State != "needs_approval" || events[2].State != "ready" {
		t.Fatalf("events: %+v", events)
	}
}

func TestBrowserLoginRequiresRoleTag(t *testing.T) {
	err := waitTailnetLogin(context.Background(), func(context.Context) (*ipnstate.Status, error) { return runningLoginStatus(), nil }, func(p LoginProgress) error { t.Fatal("untagged node reported ready"); return nil })
	if err == nil {
		t.Fatal("untagged node accepted")
	}
}

func TestBrowserLoginCancelledAndFailedCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	status := func(context.Context) (*ipnstate.Status, error) {
		return &ipnstate.Status{BackendState: "NeedsLogin"}, nil
	}
	if err := waitTailnetLogin(ctx, status, func(LoginProgress) error { return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	sentinel := errors.New("output failed")
	if err := waitTailnetLogin(context.Background(), status, func(LoginProgress) error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("got %v", err)
	}
}

func TestBrowserServiceRequiresGrantedRole(t *testing.T) {
	for _, st := range []*ipnstate.Status{nil, {}, {Self: &ipnstate.PeerStatus{}}, runningLoginStatus(), runningLoginStatus("tag:peer")} {
		if err := requireBrowserRole(st); err == nil {
			t.Fatal("browser identity without granted tag:device was accepted")
		}
	}
	if err := requireBrowserRole(runningLoginStatus("tag:device")); err != nil {
		t.Fatal(err)
	}
}
