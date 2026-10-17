//go:build !windows

package app

// probeInboundBlock has nothing to read outside Windows: no Windows Firewall rule applies there.
func probeInboundBlock(string) (bool, error) { return false, nil }
