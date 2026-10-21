package mesh

import (
	"github.com/tzf1003/xmesh/internal/config"
	"reflect"
	"tailscale.com/tailcfg"
	"testing"
)

func TestTailnetRoleTagsSurviveSetupHandoff(t *testing.T) {
	for _, ingress := range []bool{false, true} {
		want := []string{"tag:device"}
		if ingress {
			want = append(want, "tag:ingress")
		}
		if got := tailnetRoleTags(ingress); !reflect.DeepEqual(got, want) {
			t.Fatalf("ingress=%v tags=%v", ingress, got)
		}
	}
}

func TestTemporaryAdminRetainsAuthKeyTags(t *testing.T) {
	c := &config.Config{}
	c.Node.Roles = []string{"agent", "gateway", "ingress"}
	c.Mesh.AuthMethod = "key"
	c.Mesh.Ephemeral = true
	if tags := runtimeTailnetTags(c); tags != nil {
		t.Fatal("temporary key identity changed tags", tags)
	}
	c.Mesh.Ephemeral = false
	if tags := runtimeTailnetTags(c); !reflect.DeepEqual(tags, []string{"tag:device", "tag:ingress"}) {
		t.Fatal(tags)
	}
	c.Mesh.AuthMethod = "browser"
	if tags := runtimeTailnetTags(c); !reflect.DeepEqual(tags, tailnetRoleTags(true)) {
		t.Fatal(tags)
	}
}

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

func TestDeviceTrustOnboardingDoesNotRequireLegacyDeviceTag(t *testing.T) {
	st := runningLoginStatus()
	if err := onboardingPrerequisitesForVersion(st, false, false); err != nil {
		t.Fatal(err)
	}
	if err := onboardingPrerequisitesForVersion(st, true, false); err == nil {
		t.Fatal("cloud prerequisites skipped")
	}
	st = runningLoginStatus()
	st.Self.DNSName = "node.tailnet.ts.net."
	st.Self.CapMap = tailcfg.NodeCapMap{"funnel": nil}
	if err := onboardingPrerequisitesForVersion(st, true, false); err != nil {
		t.Fatal(err)
	}
}

func TestDeviceTrustSetupKeepsNetworkTagsOptional(t *testing.T) {
	c := &config.Config{SchemaVersion: 2}
	for _, ingress := range []bool{false, true} {
		if got := onboardingTailnetTags(c, ingress); got != nil {
			t.Fatalf("ingress=%v tags=%v", ingress, got)
		}
	}
}
