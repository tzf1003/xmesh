package app

import (
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/identity"
	"github.com/tzf1003/xmesh/internal/role/agent"
	"github.com/tzf1003/xmesh/internal/role/gateway"
	"reflect"
	"testing"
)

func TestLocalIngressPrincipalUsesIngressGrant(t *testing.T) {
	self := identity.Principal{Node: "device", Tags: []string{"tag:peer", "tag:admin"}}
	principal := localAgentPrincipal(self, gateway.Principal{Ingress: true, ClientID: "oauth-client"})
	if principal.Node != self.Node || !reflect.DeepEqual(principal.Tags, []string{"tag:ingress"}) || !reflect.DeepEqual(self.Tags, []string{"tag:peer", "tag:admin"}) {
		t.Fatal(principal, self)
	}
	ordinary := localAgentPrincipal(self, gateway.Principal{Local: true})
	if !reflect.DeepEqual(ordinary.Tags, self.Tags) {
		t.Fatal(ordinary)
	}
}

func TestAgentNetworkMetadataFollowsHotGeneration(t *testing.T) {
	cfg := parseConfig(t, []string{"agent"}, address(t))
	a, err := agent.New(config.Static(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	self := identity.Principal{Node: "stable-device", Tags: []string{"tag:peer"}}
	for _, item := range []struct {
		tailnet, lan, local bool
		mode                string
	}{{false, true, false, "lan"}, {true, true, false, "coexist"}, {true, false, false, "tailnet"}, {false, false, false, "offline"}, {true, true, true, "local"}} {
		cfg.Mesh.Tailnet.Enabled = item.tailnet
		cfg.Mesh.LAN.Enabled = &item.lan
		setAgentNetwork(a, cfg, item.local, self)
		st := a.Overview()
		if st.Network.Mode != item.mode || st.Network.NodeID != self.Node {
			t.Fatal(item, st.Network)
		}
	}
}
