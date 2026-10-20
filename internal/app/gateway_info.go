package app

import (
	"context"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/mesh"
)

// tailnetAddresser is implemented by the tailnet network: its own IPs and MagicDNS name.
type tailnetAddresser interface {
	Addresses(context.Context) ([]netip.Addr, string, error)
}

// gatewayInfo describes the gateway block of the status (CTR-CTL-03). Besides the configuration it lists
// the addresses the gateway answers on right now: the bind address decides whether anything beyond
// localhost is reachable, and gateway.access decides which of the LAN, tailnet and configured addresses
// are listed.
func gatewayInfo(cfg *config.Config, network mesh.Network) *v2.GatewayInfo {
	port := strconv.Itoa(gatewayPort(cfg.Gateway.Listen))
	local := "http://" + net.JoinHostPort("127.0.0.1", port) + "/mcp"
	info := &v2.GatewayInfo{
		Listen: cfg.Gateway.Listen, URL: local, Auth: "pat",
		Access: append([]string{}, cfg.Gateway.Access...), Hosts: append([]string{}, cfg.Gateway.Hosts...),
		Addresses: []v2.GatewayAddress{{Kind: "localhost", Host: "127.0.0.1", URL: local}},
	}
	if gatewayLoopbackOnly(cfg.Gateway.Listen) {
		return info
	}
	access, e := config.ParseAccess(cfg.Gateway.Access)
	if e != nil {
		return info
	}
	add := func(kind, host string) {
		info.Addresses = append(info.Addresses, v2.GatewayAddress{Kind: kind, Host: host, URL: "http://" + net.JoinHostPort(host, port) + "/mcp"})
	}
	if access.LANEnabled() {
		for _, a := range lanIPv4s() {
			add("lan", a.String())
		}
	}
	if access.TailnetEnabled() {
		if t, ok := network.(tailnetAddresser); ok {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			ips, name, e := t.Addresses(ctx)
			cancel()
			if e == nil {
				for _, ip := range ips {
					add("tailnet_ip", ip.String())
				}
				if name != "" {
					add("tailnet_name", name)
				}
			}
		}
	}
	for _, h := range cfg.Gateway.Hosts {
		add("host", h)
	}
	return info
}

// lanIPv4s returns the private IPv4 addresses of the interfaces that are up, sorted and without duplicates.
func lanIPv4s() []netip.Addr {
	ifaces, e := net.Interfaces()
	if e != nil {
		return nil
	}
	seen := map[netip.Addr]bool{}
	var out []netip.Addr
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, e := ifc.Addrs()
		if e != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipnet.IP)
			addr = addr.Unmap()
			if !ok || !addr.Is4() || !config.IsLANAddr(addr) || seen[addr] {
				continue
			}
			seen[addr] = true
			out = append(out, addr)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Less(out[j]) })
	return out
}

// tailnetGatewayListener is implemented by the tailnet network: the gateway's listener on the tailnet.
type tailnetGatewayListener interface {
	ListenPort(int) (net.Listener, error)
}

// gatewayPort is the port of gateway.listen; the configuration has already validated it.
func gatewayPort(listen string) int {
	_, p, e := net.SplitHostPort(listen)
	if e != nil {
		return 8080
	}
	n, e := strconv.Atoi(p)
	if e != nil {
		return 8080
	}
	return n
}

// gatewayLoopbackOnly reports whether the gateway binds this machine only.
func gatewayLoopbackOnly(listen string) bool {
	_, loopback, e := config.GatewayListenHost(listen)
	return e == nil && loopback
}
