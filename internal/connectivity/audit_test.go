package connectivity

import (
	"testing"

	"github.com/tzf1003/xmesh/internal/device"
	"github.com/tzf1003/xmesh/internal/devicelink"
)

func TestLinkToolNamesPathAndDialer(t *testing.T) {
	cases := []struct {
		activity devicelink.Activity
		want     string
	}{
		{devicelink.Activity{}, "device.link"},
		{devicelink.Activity{Path: device.Path{Kind: device.LAN}, Outgoing: true}, "device.link.lan.out"},
		{devicelink.Activity{Path: device.Path{Kind: device.Cloudflare}}, "device.link.cloudflare.in"},
	}
	for _, c := range cases {
		if got := linkTool(c.activity); got != c.want {
			t.Errorf("linkTool(%+v) = %q, want %q", c.activity, got, c.want)
		}
	}
}
