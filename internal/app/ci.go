package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tzf1003/xmesh/internal/ci"
	"github.com/tzf1003/xmesh/internal/config"
)

// startCI runs the GitHub Actions runner supervisor for the bindings of the configuration and
// follows its changes. The returned function stops it, waiting for running jobs up to
// ci.stop_timeout.
func startCI(store *config.Store, stateDir, node string) (mgr *ci.Manager, accounts *ci.Service, stop func()) {
	var cache *ci.Cache
	if platform, ok := ci.Platform(); ok {
		cache = &ci.Cache{Dir: filepath.Join(stateDir, "ci-cache"), Source: ci.GitHubSource{}, Platform: platform}
	}
	accounts = &ci.Service{Store: ci.Accounts{Dir: filepath.Join(stateDir, "ci-accounts")}}
	accountOf := func(b config.CIBinding) (config.CIAccount, bool) {
		for _, a := range store.Get().CI.Accounts {
			if a.ID == b.Account {
				return a, true
			}
		}
		return config.CIAccount{}, false
	}
	mgr = ci.New(ci.Options{
		StateDir: stateDir, Node: node,
		Config:  func() config.CI { return store.Get().CI },
		Cache:   cache,
		Inhibit: ci.SystemInhibitor(),
		Token: func(ctx context.Context, b config.CIBinding) (string, error) {
			a, ok := accountOf(b)
			if !ok {
				return "", fmt.Errorf("没有账号 %q", b.Account)
			}
			return accounts.RegistrationToken(ctx, a, b.Target)
		},
		RemoveToken: func(ctx context.Context, b config.CIBinding) (string, error) {
			a, ok := accountOf(b)
			if !ok {
				return "", fmt.Errorf("没有账号 %q", b.Account)
			}
			return accounts.RemoveToken(ctx, a, b.Target)
		},
		Logf: func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	current := store.Get().CI
	mgr.RemoveOrphans(current)
	unsubscribe := store.Subscribe(func(next *config.Config) { mgr.Apply(next.CI) })
	mgr.Apply(current)
	return mgr, accounts, func() {
		unsubscribe()
		mgr.Shutdown(store.Get().CI.StopTimeoutDuration())
	}
}
