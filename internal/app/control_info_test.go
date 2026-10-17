package app

import (
	"testing"
	"time"

	"github.com/tzf1003/xmesh/internal/role/gateway"
)

// A device nobody has read yet has null in both fields; a failed read says why; a success says when.
func Test_DeviceInfoView(t *testing.T) {
	if pending := deviceInfoView(gateway.DeviceInfo{}); pending.RefreshedAt != nil || pending.Error != nil {
		t.Fatalf("pending device: %+v", pending)
	}
	if failed := deviceInfoView(gateway.DeviceInfo{Error: "拨号失败"}); failed.RefreshedAt != nil || failed.Error == nil || *failed.Error != "拨号失败" {
		t.Fatalf("failed device: %+v", failed)
	}
	when := time.Date(2026, 10, 17, 6, 0, 0, 0, time.UTC)
	if ok := deviceInfoView(gateway.DeviceInfo{RefreshedAt: when}); ok.RefreshedAt == nil || !ok.RefreshedAt.Equal(when) || ok.Error != nil {
		t.Fatalf("read device: %+v", ok)
	}
}

// The latest success wins across gateways; with no success, a failure is kept over no reading.
func Test_MergeReadings(t *testing.T) {
	early, late := time.Date(2026, 10, 17, 6, 0, 0, 0, time.UTC), time.Date(2026, 10, 17, 6, 5, 0, 0, time.UTC)
	regular := map[string]gateway.DeviceInfo{
		"alpha": {RefreshedAt: early},
		"beta":  {Error: "拨号失败"},
		"gamma": {},
	}
	ingress := map[string]gateway.DeviceInfo{
		"alpha": {RefreshedAt: late, Error: "sys.info 暂不可用"},
		"beta":  {},
		"gamma": {Error: "sys.info 暂不可用"},
		"delta": {},
	}
	merged := mergeReadings(regular, ingress)
	if got := merged["alpha"]; !got.RefreshedAt.Equal(late) || got.Error != "sys.info 暂不可用" {
		t.Fatalf("latest success lost: %+v", got)
	}
	if got := merged["beta"]; got.Error != "拨号失败" {
		t.Fatalf("failure replaced by no reading: %+v", got)
	}
	if got := merged["gamma"]; got.Error != "sys.info 暂不可用" {
		t.Fatalf("failure not kept over no reading: %+v", got)
	}
	if got, ok := merged["delta"]; !ok || !got.RefreshedAt.IsZero() || got.Error != "" {
		t.Fatalf("pending device: %+v %v", got, ok)
	}
	if len(mergeReadings()) != 0 {
		t.Fatal("no gateways should give no readings")
	}
}
