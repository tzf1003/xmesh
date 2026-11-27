//go:build !linux && !darwin && !windows

package delegateguard

import "net"

func supportsTreeSampling() bool { return false }

func selfProcessGroupAndSession(int) (int, uintptr) { return 0, 0 }

func (g *Guard) refreshLocked() []struct{} { return nil }

func connPeerPID(net.Conn) (int, bool) { return 0, false }

func (g *Guard) isDelegatedPID(int) bool { return false }

func (g *Guard) isDelegatedTCP(string, string) bool { return false }
