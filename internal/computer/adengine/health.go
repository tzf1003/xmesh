package adengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Exec runs an executable and returns its stdout. The Host's ExecRunner has this
// shape, so callers can supply the same private temp directory the adapter uses.
type Exec func(ctx context.Context, path string, args ...string) ([]byte, error)

// Health runs the read-only checks a binary must pass before it becomes the
// active engine: the version command, the permission query and the display
// listing must each return a successful envelope. A denied permission is not a
// failure here; it is reported by the engine state.
func Health(ctx context.Context, exec Exec, path string) error {
	if exec == nil {
		return errors.New("健康检查缺少执行器")
	}
	for _, args := range [][]string{{"version"}, {"permissions"}, {"list-displays"}} {
		out, err := exec(ctx, path, args...)
		if err != nil {
			return fmt.Errorf("agent-desktop %s: %w", args[0], err)
		}
		if err := checkEnvelope(out); err != nil {
			return fmt.Errorf("agent-desktop %s: %w", args[0], err)
		}
	}
	return nil
}

// checkEnvelope accepts a JSON envelope whose ok field is true.
func checkEnvelope(out []byte) error {
	var env struct {
		OK    bool `json:"ok"`
		Error *struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &env); err != nil {
		return errors.New("返回的不是有效的 JSON 信封")
	}
	if !env.OK {
		code := ""
		if env.Error != nil {
			code = env.Error.Code
		}
		return fmt.Errorf("信封 ok=false %s", code)
	}
	return nil
}
