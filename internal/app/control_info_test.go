package app

import (
	"testing"
	"time"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
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

func TestConfiguredCloudflarePathIsListedOnce(t *testing.T) {
	row := v2.Peer{DeviceID: "dev-b", Addrs: []string{"https://192.168.1.2:7433"}}
	addConfiguredCloudflarePath(&row, "b.example.com")
	addConfiguredCloudflarePath(&row, "b.example.com")
	if len(row.Paths) != 1 || row.Paths[0].Kind != "cloudflare" || row.Paths[0].Reachable || row.Paths[0].Address != "https://b.example.com" {
		t.Fatalf("paths = %+v", row.Paths)
	}
	if len(row.Addrs) != 2 || row.Addrs[1] != "https://b.example.com" {
		t.Fatalf("addrs = %v", row.Addrs)
	}
	none := v2.Peer{DeviceID: "dev-c"}
	addConfiguredCloudflarePath(&none, "")
	if len(none.Paths) != 0 {
		t.Fatalf("empty host must add nothing: %+v", none.Paths)
	}
}
