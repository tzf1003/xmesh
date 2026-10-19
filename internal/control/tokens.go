package control

import (
	"os"

	"github.com/tzf1003/xmesh/internal/config"
	"github.com/tzf1003/xmesh/internal/privdir"
)

// DefaultPATName names the token the kernel creates for the gateway when the configuration has none.
const DefaultPATName = "default"

// DefaultPATScopes are the scopes of that token. They match the reach a local client had before the
// gateway required tokens, so a client that adds the token keeps working.
var DefaultPATScopes = []string{"devices:read", "exec:*", "pty:*", "fs:*", "sys:*", "backend:*:*"}

// EnsureDefaultPAT creates the gateway token when the configuration has no PAT at all (CTR-CTL-09). An
// existing token is never touched: upgrades, updates and reinstalls keep it, and only a rotation the
// user asks for replaces it. It reports whether it created one. The store is reloaded before it returns,
// so the first request already sees the token.
func EnsureDefaultPAT(store *config.Store, stateDir string) (bool, error) {
	path := store.Path()
	cfg := store.Get()
	if path == "" || !cfg.HasRole("gateway") || len(cfg.Gateway.PATs) > 0 {
		return false, nil
	}
	old, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	token, err := newPatToken()
	if err != nil {
		return false, err
	}
	if err = privdir.Ensure(stateDir); err == nil {
		err = privdir.Ensure(patDirOf(stateDir))
	}
	var file string
	if err == nil {
		file, err = writeAvailablePatFile(patDirOf(stateDir), DefaultPATName, token)
	}
	if err != nil {
		return false, err
	}
	// The token file exists before the entry that names it, as in createPat.
	next, err := ApplyEdits(old, []Edit{{Path: "/gateway/pats/" + DefaultPATName, Value: map[string]any{"token_file": file, "scopes": append([]string{}, DefaultPATScopes...)}}})
	if err == nil {
		_, err = config.Parse(next)
	}
	if err == nil {
		err = writeAtomic(path, next)
	}
	if err != nil {
		_ = os.Remove(file)
		_ = os.Remove(file + ".owner")
		return false, err
	}
	store.Get()
	return true, nil
}
