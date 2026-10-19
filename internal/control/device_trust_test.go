package control

import (
	"context"
	"errors"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
	"github.com/tzf1003/xmesh/internal/device"
)

func TestDeviceTrustImportAndRevokeUseTheRunningStore(t *testing.T) {
	var store *device.TrustStore
	f := newFixture(t, func(o *Options) {
		id, err := device.LoadIdentity(context.Background(), o.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		store, err = device.OpenTrust(o.StateDir)
		if err != nil {
			t.Fatal(err)
		}
		o.DeviceIdentity, o.DeviceTrust = id, store
	})
	doc := pairingDocument(t, "paired-workstation")
	f.ok("POST", "/v2/pair/import", v2.PairImportRequest{Pair: doc, Fingerprint: doc.Fingerprint}, "PairImportResponse")
	trusted, ok := store.Find(doc.DeviceID)
	if !ok {
		t.Fatal("running store did not adopt import")
	}
	oldGeneration := trusted.Generation
	if r := f.do("DELETE", "/v2/pair/trust/"+doc.DeviceID, nil, nil); r.Status != 204 {
		t.Fatal(r.Status, string(r.Body))
	}
	if _, ok := store.Find(doc.DeviceID); ok {
		t.Fatal("running store kept revoked device")
	}
	if err := store.Grant(t.Context(), trusted.Document, trusted.Tags, &oldGeneration); !errors.Is(err, device.ErrGeneration) {
		t.Fatal("stale pairing restored revoked trust", err)
	}
	apiError(t, f.do("DELETE", "/v2/pair/trust/"+doc.DeviceID, nil, nil), v2.ErrNotFound)
	apiError(t, f.do("DELETE", "/v2/pair/trust/not-a-device", nil, nil), v2.ErrInvalidArgs)
}

func TestDeviceProviderChangesAreHotInConfigPreview(t *testing.T) {
	before := parsedOf([]byte(`{"schema_version":2,"mesh":{"lan":{"enabled":true},"tailnet":{"enabled":false}}}`))
	after := parsedOf([]byte(`{"schema_version":2,"mesh":{"lan":{"enabled":true},"tailnet":{"enabled":true}}}`))
	diff := diffConfigs(before, after)
	if len(diff) != 1 || diff[0].RestartRequired {
		t.Fatal("path toggle requires a process restart", diff)
	}
}
