package computer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Engine selects the desktop executor a Computer Host supervises. The Go
// manager, lease, fencing and idempotency layers are identical for both.
type Engine string

const (
	// EngineNative is the bundled Rust executor (computer-native). It is the default.
	EngineNative Engine = "native"
	// EngineAgentDesktop drives the vercel-labs/agent-desktop CLI. On macOS the
	// native executor stays attached as the background coordinate-input fallback.
	EngineAgentDesktop Engine = "agent-desktop"
)

const engineFile = "engine"

// ParseEngine accepts the empty string as the default engine.
func ParseEngine(s string) (Engine, error) {
	switch Engine(strings.TrimSpace(s)) {
	case "", EngineNative:
		return EngineNative, nil
	case EngineAgentDesktop:
		return EngineAgentDesktop, nil
	}
	return "", fmt.Errorf("未知桌面引擎 %q（可选 native、agent-desktop）", s)
}

// ReadEngine returns the per-device engine stored in the Host state directory.
// A missing file selects the default, so the switch can be flipped without
// reinstalling the user service: write the file, then restart the Host.
func ReadEngine(stateDir string) (Engine, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, engineFile))
	if os.IsNotExist(err) {
		return EngineNative, nil
	}
	if err != nil {
		return "", err
	}
	return ParseEngine(string(b))
}

// WriteEngine persists the per-device engine choice (0600, state dir is private).
func WriteEngine(stateDir string, e Engine) error {
	if _, err := ParseEngine(string(e)); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, engineFile), []byte(e+"\n"), 0o600)
}
