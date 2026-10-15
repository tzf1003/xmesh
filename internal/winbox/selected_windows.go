package winbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// selectedObjectGrants owns explicit grants for the existing snapshot objects.
// It does not authorize future objects or define the execution namespace.
type selectedObjectGrants struct {
	mu     sync.Mutex
	grants []*objectGrant
}

// grantSelectedObjects applies grants in snapshot order and rolls back on error.
// A non-nil result with an error owns grants whose rollback failed: callers must
// retain it and retry Close. Snapshot closure must be serialized with admission.
func (p *Profile) grantSelectedObjects(ctx context.Context, tree *workspaceTree) (*selectedObjectGrants, error) {
	if tree == nil || len(tree.objects) == 0 {
		return nil, fmt.Errorf("AppContainer object selection is empty")
	}
	selected := &selectedObjectGrants{}
	var admissionErr error
	for _, object := range tree.objects {
		if admissionErr = ctx.Err(); admissionErr != nil {
			break
		}
		var grant *objectGrant
		grant, admissionErr = p.grantSelectedObject(object)
		if admissionErr != nil {
			break
		}
		selected.grants = append(selected.grants, grant)
	}
	if admissionErr == nil {
		admissionErr = ctx.Err()
	}
	if admissionErr == nil {
		return selected, nil
	}
	if cleanupErr := selected.Close(); cleanupErr != nil {
		return selected, errors.Join(admissionErr, fmt.Errorf("rollback selected AppContainer grants: %w", cleanupErr))
	}
	return nil, admissionErr
}

// Close releases every grant in reverse order, retaining failed releases for
// retry while allowing independent objects to finish cleanup. Reap processes
// before release. The snapshot retains its own namespace guards until closed.
func (s *selectedObjectGrants) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result error
	for index := len(s.grants) - 1; index >= 0; index-- {
		if grant := s.grants[index]; grant != nil {
			if err := grant.Close(); err != nil {
				result = errors.Join(result, fmt.Errorf("release selected AppContainer object %d: %w", index, err))
			} else {
				s.grants[index] = nil
			}
		}
	}
	if result == nil {
		s.grants = nil
	}
	return result
}
