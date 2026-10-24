package device

import (
	"context"
	"fmt"
	"net/url"
	"slices"
)

// PathHint is an address to probe for a paired device. It never grants trust or
// reachability; a later occupant of that address must prove the same public key.
type PathHint struct {
	Kind    PathKind `json:"kind"`
	Address string   `json:"address"`
}

func (h PathHint) Validate() error {
	u, err := url.Parse(h.Address)
	if err != nil || len(h.Address) > 2048 || !h.Kind.Valid() || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("设备连接提示地址无效")
	}
	return nil
}

func (s *TrustStore) RememberPath(ctx context.Context, id string, generation uint64, hint PathHint) error {
	if err := hint.Validate(); err != nil {
		return err
	}
	return s.update(ctx, func(state *trustFile) error {
		trusted, ok := state.Peers[id]
		if !ok || trusted.Revoked || trusted.Generation != generation {
			return ErrGeneration
		}
		trusted.Hints = slices.DeleteFunc(trusted.Hints, func(previous PathHint) bool { return previous == hint })
		trusted.Hints = append([]PathHint{hint}, trusted.Hints...)
		trusted.Hints = trusted.Hints[:min(len(trusted.Hints), 8)]
		state.Peers[id] = trusted
		return nil
	})
}
