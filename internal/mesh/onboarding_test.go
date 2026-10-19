package mesh

import (
	"tailscale.com/tailcfg"
	"testing"
)

func TestOnboardingRequiresDeviceIngressAndFunnelCapabilities(t *testing.T) {
	device := runningLoginStatus("tag:device")
	if e := onboardingPrerequisites(device, false); e != nil {
		t.Fatal(e)
	}
	if e := onboardingPrerequisites(device, true); e == nil {
		t.Fatal("untagged ingress accepted")
	}
	entry := runningLoginStatus("tag:device", "tag:ingress")
	entry.Self.DNSName = "node.tailnet.ts.net."
	if e := onboardingPrerequisites(entry, true); e == nil {
		t.Fatal("Funnel without capability accepted")
	}
	entry.Self.CapMap = tailcfg.NodeCapMap{"funnel": nil}
	if e := onboardingPrerequisites(entry, true); e != nil {
		t.Fatal(e)
	}
	entry.Self.DNSName = "node.internal.example."
	if e := onboardingPrerequisites(entry, true); e == nil {
		t.Fatal("non-Funnel DNS accepted")
	}
}
