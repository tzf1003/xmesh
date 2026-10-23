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

func TestDeviceTrustTagsUpdateKeepsSessionsOnlyWhenWidening(t *testing.T) {
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
	doc := pairingDocument(t, "paired-cloud")
	f.ok("POST", "/v2/pair/import", v2.PairImportRequest{Pair: doc, Fingerprint: doc.Fingerprint}, "PairImportResponse")
	before, _ := store.Find(doc.DeviceID)
	path := "/v2/pair/trust/" + doc.DeviceID

	f.ok("PATCH", path, v2.PairTrustUpdateRequest{Tags: []string{"tag:ingress", "tag:device", "tag:peer"}}, "PairTrustUpdateResponse")
	widened, _ := store.Find(doc.DeviceID)
	if len(widened.Tags) != 3 || widened.Generation != before.Generation {
		t.Fatal("widening roles must apply immediately without ending sessions", widened.Tags, widened.Generation, before.Generation)
	}

	f.ok("PATCH", path, v2.PairTrustUpdateRequest{Tags: []string{"tag:peer", "tag:device"}}, "PairTrustUpdateResponse")
	narrowed, _ := store.Find(doc.DeviceID)
	if len(narrowed.Tags) != 2 || narrowed.Generation != before.Generation+1 {
		t.Fatal("removing a role must end sessions authorized under it", narrowed.Tags, narrowed.Generation)
	}

	apiError(t, f.do("PATCH", path, v2.PairTrustUpdateRequest{Tags: []string{}}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("PATCH", path, v2.PairTrustUpdateRequest{Tags: []string{"tag:root"}}, nil), v2.ErrInvalidArgs)
	apiError(t, f.do("PATCH", "/v2/pair/trust/not-a-device", v2.PairTrustUpdateRequest{Tags: []string{"tag:peer"}}, nil), v2.ErrInvalidArgs)
	if r := f.do("DELETE", path, nil, nil); r.Status != 204 {
		t.Fatal(r.Status)
	}
	apiError(t, f.do("PATCH", path, v2.PairTrustUpdateRequest{Tags: []string{"tag:peer"}}, nil), v2.ErrNotFound)
}
