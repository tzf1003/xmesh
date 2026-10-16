package gateway

import "testing"

func Test_ResumeClearsDeviceAndMeshDrain(t *testing.T) {
	g, _ := fixture(t, false)
	g.Drain("test-node")
	g.Drain("")
	g.mu.Lock()
	drained := g.draining["test-node"] && g.meshDraining
	generation := g.generation
	g.mu.Unlock()
	if !drained {
		t.Fatal("drain did not mark")
	}
	devices, _ := g.snapshot()
	for name, d := range devices {
		if d.online {
			t.Fatal("drained gateway still routes", name)
		}
	}
	g.Resume("test-node")
	g.Resume("")
	g.mu.Lock()
	cleared := !g.draining["test-node"] && !g.meshDraining && g.generation > generation
	g.mu.Unlock()
	if !cleared {
		t.Fatal("resume left marks behind")
	}
	if devices, _ = g.snapshot(); !devices["test-node"].online {
		t.Fatalf("resumed device not routed: %+v", devices)
	}
}
