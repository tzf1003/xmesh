package config

import (
	"os"
	"path/filepath"
	"testing"
)

func Test_CTRCFG01(t *testing.T) {
	root := "../../docs/contracts/schemas/examples"
	entries, _ := os.ReadDir(root)
	for _, ent := range entries {
		if len(ent.Name()) > 6 && ent.Name()[:6] == "xmesh." {
			t.Run(ent.Name(), func(t *testing.T) {
				if _, e := Load(filepath.Join(root, ent.Name())); e != nil {
					t.Fatal(e)
				}
			})
		}
	}
	for _, name := range []string{"config-relay.json", "config-funnel-with-domain.json"} {
		if _, e := Load(filepath.Join(root, "invalid", name)); e == nil {
			t.Fatal("invalid accepted", name)
		}
	}
}
