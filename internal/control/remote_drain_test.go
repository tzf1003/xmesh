package control

import (
	"context"
	"encoding/json"
	"testing"

	v2 "github.com/tzf1003/xmesh/api/control/v2"
)

type remoteDrainHost struct {
	*fakeHost
	received v2.DrainRequest
}

func (h *remoteDrainHost) RemoteDrain(_ context.Context, req v2.DrainRequest) ([]v2.DrainOutcome, error) {
	h.received = req
	return []v2.DrainOutcome{{Device: "one"}, {Device: "two", Error: "permission denied"}}, nil
}

func TestRemoteDrainDoesNotChangeLocalLifecycle(t *testing.T) {
	host := &remoteDrainHost{fakeHost: &fakeHost{}}
	f := newFixture(t, func(o *Options) { o.Host = host })
	response := f.do("POST", "/v2/drain", v2.DrainRequest{Scope: "mesh", Mode: v2.DrainKill, TimeoutMS: 1000}, nil)
	var result v2.DrainResponse
	if err := json.Unmarshal(response.Body, &result); err != nil || response.Status != 200 {
		t.Fatal(response, err)
	}
	if host.Draining() || result.Drain.Draining || len(result.Outcomes) != 2 || host.received.Scope != "mesh" || host.received.TimeoutMS != 1000 {
		t.Fatal(host.received, result)
	}
	for _, req := range []v2.DrainRequest{{Scope: "mesh", Device: "one"}, {Scope: "device"}, {Device: "one"}, {Scope: "unknown"}, {Scope: "mesh", TimeoutMS: -1}, {Scope: "mesh", TimeoutMS: v2.MaxDrainTimeoutMS + 1}, {Scope: "local", TimeoutMS: 1}} {
		if got := f.do("POST", "/v2/drain", req, nil); got.Status != 400 {
			t.Fatal("invalid remote request accepted", req, got)
		}
	}
}
