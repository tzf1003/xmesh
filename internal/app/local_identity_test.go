package app

import (
	"github.com/tzf1003/xmesh/internal/identity"
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
