package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

type fakeShareNode struct {
	trusted map[string]string
	sent    []v2.DeviceOffer
	sentTo  []string
	err     error
}

func (f *fakeShareNode) Trusted() (map[string]string, error) { return f.trusted, nil }
func (f *fakeShareNode) SendOffer(_ context.Context, id string, o v2.DeviceOffer) error {
	if f.err != nil {
		return f.err
	}
	f.sent, f.sentTo = append(f.sent, o), append(f.sentTo, id)
	return nil
}

var shareDevice = "dev-" + strings.Repeat("cd", 32)

func shareFixture(t *testing.T, node *fakeShareNode) (things func() int, cfgPath string, args []string) {
	t.Helper()
	things, cfgPath, args = inviteFixture(t, &fakeInviteNode{})
	old := openShareNode
	openShareNode = func(string, string) (shareNode, error) { return node, nil }
	t.Cleanup(func() { openShareNode = old })
	args[0] = "share"
	args[1], args[2] = "--device", "phone"
	return things, cfgPath, args
}

func TestCloudflareSharePreviewCreatesNothing(t *testing.T) {
	node := &fakeShareNode{trusted: map[string]string{shareDevice: "phone"}}
	things, _, args := shareFixture(t, node)
	var out bytes.Buffer
	if err := cloudflareRun(args, &out, nil); err != nil {
		t.Fatal(err)
	}
	if things() != 0 || len(node.sent) != 0 {
		t.Fatalf("preview changed things: %d %d", things(), len(node.sent))
	}
}

func TestCloudflareShareDeliversTheDevicesOwnTunnelAndRecordsItAsAPeer(t *testing.T) {
	node := &fakeShareNode{trusted: map[string]string{shareDevice: "phone"}}
	things, cfgPath, args := shareFixture(t, node)
	var out bytes.Buffer
	if err := cloudflareRun(append(args, "--apply"), &out, nil); err != nil {
		t.Fatal(err)
	}
	if len(node.sent) != 1 || node.sentTo[0] != shareDevice {
		t.Fatalf("%+v %v", node.sent, node.sentTo)
	}
	o := node.sent[0]
	if o.Kind != "cloudflare" || len(o.ID) != 32 || o.Cloudflare.PeerHostname != "xmesh-laptop.mesh.example.com" ||
		!strings.HasPrefix(o.Cloudflare.TunnelToken, "tunnel-token-") || !strings.HasPrefix(o.Cloudflare.AccessID, "cid-") {
		t.Fatalf("%+v", o)
	}
	if strings.Contains(out.String(), o.Cloudflare.TunnelToken) || strings.Contains(out.String(), o.Cloudflare.AccessSecret) {
		t.Fatalf("the output shows credentials: %s", out.String())
	}
	if things() == 0 {
		t.Fatal("the delivered device's resources were removed")
	}
	cfg, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(cfg), shareDevice+": "+o.Cloudflare.Hostname) {
		t.Fatalf("the sender did not record the device as a peer:\n%s", cfg)
	}
}

func TestCloudflareShareRemovesTheTunnelWhenDeliveryFails(t *testing.T) {
	node := &fakeShareNode{trusted: map[string]string{shareDevice: "phone"}, err: errors.New("device offline")}
	things, cfgPath, args := shareFixture(t, node)
	var out bytes.Buffer
	err := cloudflareRun(append(args, "--apply"), &out, nil)
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("%v", err)
	}
	if things() != 0 {
		t.Fatalf("a tunnel nobody received was left behind: %d", things())
	}
	cfg, _ := os.ReadFile(cfgPath)
	if strings.Contains(string(cfg), shareDevice) {
		t.Fatalf("an undelivered device was recorded:\n%s", cfg)
	}
}

func TestCloudflareShareNeedsAPairedDevice(t *testing.T) {
	node := &fakeShareNode{trusted: map[string]string{shareDevice: "tablet"}}
	things, _, args := shareFixture(t, node)
	var out bytes.Buffer
	if err := cloudflareRun(append(args, "--apply"), &out, nil); err == nil {
		t.Fatal("shared with a device that is not paired")
	}
	if things() != 0 {
		t.Fatalf("created resources for an unknown device: %d", things())
	}
	dup := &fakeShareNode{trusted: map[string]string{shareDevice: "phone", "dev-" + strings.Repeat("ef", 32): "phone"}}
	if _, _, err := resolveDevice(dup.trusted, "phone"); err == nil {
		t.Fatal("an ambiguous name was resolved")
	}
	if id, _, err := resolveDevice(dup.trusted, shareDevice); err != nil || id != shareDevice {
		t.Fatalf("%v %v", id, err)
	}
}
