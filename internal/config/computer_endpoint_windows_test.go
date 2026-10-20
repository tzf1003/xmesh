package config

import "testing"

func TestWindowsComputerConfigRejectsInvalidPipe(t *testing.T) {
	f := newReloadFixture(t)
	for _, endpoint := range []string{`\\.\pipe\xmesh-S-1-5-21-1-2-3-1001`, `\\.\pipe\xmesh-invalid-computer`, `C:\computer.sock`} {
		f.write(func(doc map[string]any) { section(section(doc, "agent"), "computer")["endpoint"] = endpoint })
		if _, err := NewStore(f.path); err == nil {
			t.Fatalf("accepted invalid endpoint %q", endpoint)
		}
	}
}
