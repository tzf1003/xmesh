package app

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tzf1003/xmesh/internal/ci"
	"github.com/tzf1003/xmesh/internal/config"
)

// startCI runs the GitHub Actions runner supervisor for the bindings of the configuration and
// follows its changes. The returned function stops it, waiting for running jobs up to
// ci.stop_timeout.
func startCI(store *config.Store, stateDir, node string) (mgr *ci.Manager, stop func()) {
	var cache *ci.Cache
	if platform, ok := ci.Platform(); ok {
		cache = &ci.Cache{Dir: filepath.Join(stateDir, "ci-cache"), Source: ci.GitHubSource{}, Platform: platform}
	}
	mgr = ci.New(ci.Options{
		StateDir: stateDir, Node: node,
		Config:  func() config.CI { return store.Get().CI },
		Cache:   cache,
		Inhibit: ci.SystemInhibitor(),
		Logf:    func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	current := store.Get().CI
	mgr.RemoveOrphans(current)
	unsubscribe := store.Subscribe(func(next *config.Config) { mgr.Apply(next.CI) })
	mgr.Apply(current)
	return mgr, func() {
		unsubscribe()
		mgr.Shutdown(store.Get().CI.StopTimeoutDuration())
	}
}
