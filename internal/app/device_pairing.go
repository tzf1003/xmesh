package app

import (
	"context"
	"strings"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/connectivity"
	"github.com/tzf1003/xmesh/internal/device"
)

type devicePairing struct{ node *connectivity.Node }

func (p devicePairing) PairPasswordState() device.PairStatus { return p.node.Pairing().Status() }
func (p devicePairing) SetPairPassword(password string) (device.PairStatus, error) {
	err := p.node.Pairing().SetPassword(password)
	return p.PairPasswordState(), err
}
func (p devicePairing) ClearPairPassword() error { return p.node.Pairing().ClearPassword() }
func (p devicePairing) Discovered(context.Context) ([]v2.PairDiscoveredDevice, error) {
	result := []v2.PairDiscoveredDevice{}
	for _, candidate := range p.node.Candidates() {
		// Tunnel peers are configured, not discovered.
		if _, trusted := p.node.Trust().Find(candidate.DeviceID); trusted || candidate.Kind == device.Cloudflare {
			continue
		}
		result = append(result, v2.PairDiscoveredDevice{Name: candidate.Name, DeviceID: candidate.DeviceID, Fingerprint: strings.TrimPrefix(candidate.DeviceID, "dev-"), Address: candidate.Address, Path: string(candidate.Kind), SeenAt: v2.At(candidate.SeenAt)})
	}
	return result, nil
}
func (p devicePairing) PairWith(ctx context.Context, kind device.PathKind, address, password, expected string) (device.Trust, error) {
	doc, err := p.node.PairWith(ctx, kind, address, password, expected)
	if err != nil {
		return device.Trust{}, err
	}
	trusted, _ := p.node.Trust().Find(doc.DeviceID)
	return trusted, nil
}
